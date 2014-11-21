/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-etcd/etcd"

	"github.com/mozilla-services/pushgo/id"
)

const (
	minTTL = 2 * time.Second
)

var (
	ErrMinTTL       = fmt.Errorf("Default TTL too short; want at least %s", minTTL)
	ErrEtcdStatus   = fmt.Errorf("etcd returned unexpected health check result")
	ErrRouterClosed = fmt.Errorf("Router closed")
)

// IsEtcdKeyExist indicates whether the given error reports that an etcd key
// already exists.
func IsEtcdKeyExist(err error) bool {
	clientErr, ok := err.(*etcd.EtcdError)
	return ok && clientErr.ErrorCode == 105
}

// IsEtcdTemporary indicates whether the given error is a temporary
// etcd error.
func IsEtcdTemporary(err error) bool {
	clientErr, ok := err.(*etcd.EtcdError)
	// Raft (300-class) and internal (400-class) errors are temporary.
	return !ok || clientErr.ErrorCode >= 300 && clientErr.ErrorCode < 500
}

type EtcdLocatorConf struct {
	// Dir is the etcd key prefix for storing contacts. Defaults to
	// "push_hosts".
	Dir string

	// Servers is a list of etcd servers.
	Servers []string

	// DefaultTTL is the maximum amount of time that registered contacts will be
	// considered valid. Defaults to "24h".
	DefaultTTL string `env:"ttl"`

	// RefreshInterval is the maximum amount of time that a cached contact list
	// will be considered valid. Defaults to "5m".
	RefreshInterval string `toml:"refresh_interval" env:"refresh_interval"`

	// MaxRetries is the number of times to retry failed requests. Defaults to 5.
	MaxRetries int `toml:"max_retries" env:"max_retries"`

	// RetryDelay is the amount of time to wait before retrying requests.
	// Defaults to "200ms".
	RetryDelay string `toml:"retry_delay" env:"retry_delay"`

	// MaxJitter is the maximum per-retry randomized delay. Defaults to "400ms".
	MaxJitter string `toml:"max_jitter" env:"max_jitter"`

	// MaxDelay is the maximum amount of time to wait before retrying failed
	// requests with exponential backoff. Defaults to "5s".
	MaxDelay string `toml:"max_delay" env:"max_delay"`
}

// EtcdLocator stores routing endpoints in etcd and polls for new contacts.
type EtcdLocator struct {
	logger          *SimpleLogger
	metrics         Statistician
	refreshInterval time.Duration
	defaultTTL      time.Duration
	maxRetries      int
	retryDelay      time.Duration
	maxJitter       time.Duration
	maxDelay        time.Duration
	serverList      []string
	dir             string
	url             string
	key             string
	client          *etcd.Client
	contactsLock    sync.RWMutex
	contacts        []string
	contactsErr     error
	lastFetch       time.Time
	isClosing       bool
	closeSignal     chan bool
	closeWait       sync.WaitGroup
	closeLock       sync.Mutex
	lastErr         error
}

func NewEtcdLocator() *EtcdLocator {
	return &EtcdLocator{
		closeSignal: make(chan bool),
	}
}

func (*EtcdLocator) ConfigStruct() interface{} {
	return &EtcdLocatorConf{
		Dir:             "push_hosts",
		Servers:         []string{"http://localhost:4001"},
		DefaultTTL:      "24h",
		RefreshInterval: "5m",
		MaxRetries:      5,
		RetryDelay:      "200ms",
		MaxJitter:       "400ms",
		MaxDelay:        "5s",
	}
}

func (l *EtcdLocator) Init(app *Application, config interface{}) (err error) {
	conf := config.(*EtcdLocatorConf)
	l.logger = app.Logger()
	l.metrics = app.Metrics()

	if l.refreshInterval, err = time.ParseDuration(conf.RefreshInterval); err != nil {
		l.logger.Alert("etcd", "Could not parse refreshInterval",
			LogFields{"error": err.Error(),
				"refreshInterval": conf.RefreshInterval})
		return err
	}
	// default time for the server to be "live"
	if l.defaultTTL, err = time.ParseDuration(conf.DefaultTTL); err != nil {
		l.logger.Alert("etcd",
			"Could not parse etcd default TTL",
			LogFields{"value": conf.DefaultTTL, "error": err.Error()})
		return err
	}
	if l.defaultTTL < minTTL {
		l.logger.Alert("etcd",
			"default TTL too short",
			LogFields{"value": conf.DefaultTTL})
		return ErrMinTTL
	}
	if l.retryDelay, err = time.ParseDuration(conf.RetryDelay); err != nil {
		l.logger.Alert("etcd",
			"Could not parse etcd 'retryDelay'",
			LogFields{"value": conf.RetryDelay, "error": err.Error()})
		return err
	}
	if l.maxJitter, err = time.ParseDuration(conf.MaxJitter); err != nil {
		l.logger.Alert("etcd",
			"Could not parse etcd 'maxJitter'",
			LogFields{"value": conf.MaxJitter, "error": err.Error()})
		return err
	}
	if l.maxDelay, err = time.ParseDuration(conf.MaxDelay); err != nil {
		l.logger.Alert("etcd",
			"Could not parse etcd 'maxDelay'",
			LogFields{"value": conf.MaxDelay, "error": err.Error()})
		return err
	}
	l.maxRetries = conf.MaxRetries

	l.serverList = conf.Servers
	l.dir = path.Clean(conf.Dir)

	// Use the hostname and port of the current server as the etcd key.
	l.url = app.Router().URL()
	uri, err := url.ParseRequestURI(l.url)
	if err != nil {
		l.logger.Alert("etcd", "Error parsing router URL", LogFields{
			"error": err.Error(), "url": l.url})
		return err
	}
	if len(uri.Host) > 0 {
		l.key = path.Join(l.dir, uri.Host)
	}

	if l.logger.ShouldLog(INFO) {
		l.logger.Info("etcd", "connecting to etcd servers",
			LogFields{"list": strings.Join(l.serverList, ";")})
	}
	etcd.SetLogger(log.New(&LogWriter{l.logger, "etcd", DEBUG}, "", 0))
	l.client = etcd.NewClient(l.serverList)
	l.client.CheckRetry = l.checkRetry

	// create the push hosts directory (if not already there)
	if _, err = l.client.CreateDir(l.dir, 0); err != nil {
		if !IsEtcdKeyExist(err) {
			l.logger.Alert("etcd", "etcd createDir error", LogFields{
				"error": err.Error()})
			return err
		}
	}
	if err = l.Register(); err != nil {
		l.logger.Alert("etcd", "Could not register with etcd",
			LogFields{"error": err.Error()})
		return err
	}
	if l.contacts, err = l.getServers(); err != nil {
		l.logger.Alert("etcd", "Could not fetch contact list",
			LogFields{"error": err.Error()})
		return err
	}

	l.closeWait.Add(2)
	go l.registerLoop()
	go l.fetchLoop()
	return nil
}

func (l *EtcdLocator) checkRetry(cluster *etcd.Cluster, retries int, lastResp http.Response, err error) error {
	if l.logger.ShouldLog(ERROR) {
		l.logger.Error("etcd", "etcd request error", LogFields{
			"error":   err.Error(),
			"retries": strconv.Itoa(retries),
			"status":  strconv.Itoa(lastResp.StatusCode)})
	}
	if retries >= l.maxRetries*len(cluster.Machines) {
		l.metrics.Increment("locator.etcd.error")
		return &etcd.EtcdError{
			ErrorCode: etcd.ErrCodeEtcdNotReachable,
			Message:   fmt.Sprintf("Error connecting to etcd after %d retries", retries),
		}
	}
	l.metrics.Increment("locator.etcd.retry.request")
	if lastResp.StatusCode >= 500 {
		retryDelay := time.Duration(int64(l.retryDelay) * (1 << uint(retries-1)))
		if retryDelay > l.maxDelay {
			retryDelay = l.maxDelay
		}
		delay := time.Duration(int64(retryDelay) + rand.Int63n(int64(l.maxJitter)))
		select {
		case <-l.closeSignal:
			return ErrRouterClosed
		case <-time.After(delay):
		}
	}
	return nil
}

// Close stops the locator and closes the etcd client connection. Implements
// Locator.Close().
func (l *EtcdLocator) Close() (err error) {
	defer l.closeLock.Unlock()
	l.closeLock.Lock()
	if l.isClosing {
		return l.lastErr
	}
	close(l.closeSignal)
	l.closeWait.Wait()
	if l.key != "" {
		_, err = l.client.Delete(l.key, false)
	}
	l.isClosing = true
	l.lastErr = err
	return err
}

// Contacts returns a shuffled list of all nodes in the Simple Push cluster.
// Implements Locator.Contacts().
func (l *EtcdLocator) Contacts(string) (contacts []string, err error) {
	l.contactsLock.RLock()
	contacts = make([]string, len(l.contacts))
	copy(contacts, l.contacts)
	if l.contactsErr != nil && time.Since(l.lastFetch) > l.defaultTTL {
		err = l.contactsErr
	}
	l.contactsLock.RUnlock()
	return
}

// Status determines whether etcd can respond to requests. Implements
// Locator.Status().
func (l *EtcdLocator) Status() (ok bool, err error) {
	fakeID, err := id.Generate()
	if err != nil {
		return false, err
	}
	key, expected := "status_"+fakeID, "test"
	if _, err = l.client.Set(key, expected, uint64(6*time.Second)); err != nil {
		return false, err
	}
	resp, err := l.client.Get(key, false, false)
	if err != nil {
		return false, err
	}
	if resp.Node.Value != expected {
		l.logger.Error("etcd", "Unexpected health check result",
			LogFields{"expected": expected, "actual": resp.Node.Value})
		return false, ErrEtcdStatus
	}
	l.client.Delete(key, false)
	return true, nil
}

// Register registers the server to the etcd cluster.
func (l *EtcdLocator) Register() (err error) {
	if l.logger.ShouldLog(INFO) {
		l.logger.Info("etcd", "Registering host", LogFields{
			"key": l.key, "url": l.url})
	}
	retries := 0
	retryDelay := l.retryDelay
	for ok := true; ok && retries < l.maxRetries; retries++ {
		if _, err = l.client.Set(l.key, l.url,
			uint64(l.defaultTTL/time.Second)); err != nil {

			if !IsEtcdTemporary(err) {
				break
			}
			if retryDelay > l.maxDelay {
				retryDelay = l.maxDelay
			}
			delay := time.Duration(int64(retryDelay) + rand.Int63n(int64(l.maxJitter)))
			select {
			case ok = <-l.closeSignal:
			case <-time.After(delay):
				retryDelay *= 2
			}
			continue
		}
		break
	}
	l.metrics.IncrementBy("locator.etcd.retry.register", int64(retries))
	if err != nil {
		if l.logger.ShouldLog(ERROR) {
			l.logger.Error("etcd", "Failed to register", LogFields{
				"error": err.Error(), "key": l.key, "url": l.url})
		}
		return err
	}
	return nil
}

// getServers gets the current contact list from etcd.
func (l *EtcdLocator) getServers() (servers []string, err error) {
	var nodeList *etcd.Response
	retries := 0
	retryDelay := l.retryDelay
	for ok := true; ok && retries < l.maxRetries; retries++ {
		if nodeList, err = l.client.Get(l.dir, false, false); err != nil {
			if !IsEtcdTemporary(err) {
				break
			}
			if retryDelay > l.maxDelay {
				retryDelay = l.maxDelay
			}
			delay := time.Duration(int64(retryDelay) + rand.Int63n(int64(l.maxJitter)))
			select {
			case ok = <-l.closeSignal:
			case <-time.After(delay):
				retryDelay *= 2
			}
			continue
		}
		break
	}
	l.metrics.IncrementBy("locator.etcd.retry.fetch", int64(retries))
	if err != nil {
		if l.logger.ShouldLog(ERROR) {
			l.logger.Error("etcd", "Could not get server list",
				LogFields{"error": err.Error()})
		}
		return nil, err
	}
	servers = make([]string, 0, len(nodeList.Node.Nodes))
	for _, node := range nodeList.Node.Nodes {
		if node.Value == l.url || node.Value == "" {
			continue
		}
		servers = append(servers, node.Value)
	}
	for length := len(servers); length > 0; {
		i := rand.Intn(length)
		length--
		servers[i], servers[length] = servers[length], servers[i]
	}
	return servers, nil
}

// refreshLoop periodically re-registers the current node with etcd.
func (l *EtcdLocator) registerLoop() {
	defer l.closeWait.Done()
	// auto refresh slightly more often than the TTL
	timeout := 0.75 * l.defaultTTL.Seconds()
	ticker := time.NewTicker(time.Duration(timeout) * time.Second)
	for ok := true; ok; {
		select {
		case ok = <-l.closeSignal:
		case <-ticker.C:
			l.Register()
		}
	}
	ticker.Stop()
}

// fetchLoop polls etcd for new nodes.
func (l *EtcdLocator) fetchLoop() {
	defer l.closeWait.Done()
	fetchTick := time.NewTicker(l.refreshInterval)
	for ok := true; ok; {
		select {
		case ok = <-l.closeSignal:
		case t := <-fetchTick.C:
			contacts, err := l.getServers()
			l.contactsLock.Lock()
			if err != nil {
				l.contactsErr = err
			} else {
				l.contacts = contacts
				l.contactsErr = nil
			}
			l.lastFetch = t
			l.contactsLock.Unlock()
		}
	}
	fetchTick.Stop()
}

func init() {
	rand.Seed(time.Now().UnixNano())
	AvailableLocators["etcd"] = func() HasConfigStruct { return NewEtcdLocator() }
}
