/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-etcd/etcd"

	"github.com/mozilla-services/pushgo/retry"
)

const (
	minTTL = 2 * time.Second
)

var (
	ErrMinTTL     = fmt.Errorf("Default TTL too short; want at least %s", minTTL)
	ErrEtcdStatus = fmt.Errorf("etcd returned unexpected health check result")
)

type EtcdLocatorConf struct {
	// Dir is the etcd key prefix for storing contacts. Defaults to
	// "push_hosts".
	Dir string

	// Servers is a list of etcd servers.
	Servers []string

	// DefaultTTL is the maximum amount of time that registered contacts will be
	// considered valid. Defaults to "1m".
	DefaultTTL string

	// RefreshInterval is the maximum amount of time that a cached contact list
	// will be considered valid. Defaults to "10s".
	RefreshInterval string `toml:"refresh_interval" env:"refresh_interval"`

	// StartDelay is the amount of time to wait after registering the host with
	// etcd. This should be 1-2 times the refresh interval to ensure the locator
	// has a complete view of the cluster.
	StartDelay string `toml:"start_delay" env:"start_delay"`

	// CloseDelay is the amount of time to wait after closing the locator and
	// removing the host from etcd. This should be 1-2 times the refresh interval.
	CloseDelay string `toml:"close_delay" env:"close_delay"`

	// Retry specifies request retry options.
	Retry retry.Config
}

// EtcdLocator stores routing endpoints in etcd and polls for new contacts.
type EtcdLocator struct {
	logger          *SimpleLogger
	metrics         Statistician
	refreshInterval time.Duration
	defaultTTL      time.Duration
	startDelay      time.Duration
	closeDelay      time.Duration
	rh              *retry.Helper
	serverList      []string
	dir             string
	url             string
	key             string
	client          *etcd.Client
	contactsLock    sync.RWMutex
	contacts        []string
	contactsErr     error
	lastFetch       time.Time
	closeOnce       Once
	readySignal     chan bool
	closeSignal     chan bool
	closeWait       sync.WaitGroup
}

func NewEtcdLocator() (l *EtcdLocator) {
	l = &EtcdLocator{
		readySignal: make(chan bool),
		closeSignal: make(chan bool),
	}
	return l
}

func (*EtcdLocator) ConfigStruct() interface{} {
	return &EtcdLocatorConf{
		Dir:             "push_hosts",
		Servers:         []string{"http://localhost:4001"},
		DefaultTTL:      "1m",
		RefreshInterval: "10s",
		StartDelay:      "10s",
		CloseDelay:      "20s",
		Retry: retry.Config{
			Retries:   5,
			Delay:     "200ms",
			MaxDelay:  "5s",
			MaxJitter: "400ms",
		},
	}
}

func (l *EtcdLocator) Init(app *Application, config interface{}) (err error) {
	conf := config.(*EtcdLocatorConf)
	l.logger = app.Logger()
	l.metrics = app.Metrics()

	if l.refreshInterval, err = time.ParseDuration(conf.RefreshInterval); err != nil {
		l.logger.Panic("locator", "Could not parse refresh interval",
			LogFields{"error": err.Error(),
				"refreshInterval": conf.RefreshInterval})
		return err
	}
	// default time for the server to be "live"
	if l.defaultTTL, err = time.ParseDuration(conf.DefaultTTL); err != nil {
		l.logger.Panic("locator",
			"Could not parse etcd default TTL",
			LogFields{"value": conf.DefaultTTL, "error": err.Error()})
		return err
	}
	if l.defaultTTL < minTTL {
		l.logger.Panic("locator",
			"default TTL too short",
			LogFields{"value": conf.DefaultTTL})
		return ErrMinTTL
	}
	if l.startDelay, err = time.ParseDuration(conf.StartDelay); err != nil {
		l.logger.Panic("locator", "Could not parse start delay",
			LogFields{"error": err.Error(),
				"startDelay": conf.StartDelay})
		return err
	}
	if l.closeDelay, err = time.ParseDuration(conf.CloseDelay); err != nil {
		l.logger.Panic("locator", "Could not parse close delay",
			LogFields{"error": err.Error(),
				"closeDelay": conf.CloseDelay})
		return err
	}

	l.serverList = conf.Servers
	l.dir = path.Clean(conf.Dir)

	// Use the hostname and port of the current server as the etcd key.
	l.url = app.Router().URL()
	uri, err := url.ParseRequestURI(l.url)
	if err != nil {
		l.logger.Panic("locator", "Error parsing router URL", LogFields{
			"error": err.Error(), "url": l.url})
		return err
	}
	if len(uri.Host) > 0 {
		l.key = path.Join(l.dir, uri.Host)
	}

	if l.rh, err = conf.Retry.NewHelper(); err != nil {
		l.logger.Panic("locator", "Error configuring retry helper",
			LogFields{"error": err.Error()})
		return err
	}
	l.rh.CloseNotifier = l
	l.rh.CanRetry = IsEtcdTemporary

	if l.logger.ShouldLog(INFO) {
		l.logger.Info("locator", "connecting to etcd servers",
			LogFields{"list": strings.Join(l.serverList, ";")})
	}
	l.client = etcd.NewClient(l.serverList)
	l.client.CheckRetry = l.checkRetry

	// create the push hosts directory (if not already there)
	if _, err = l.client.CreateDir(l.dir, 0); err != nil {
		if !IsEtcdKeyExist(err) {
			l.logger.Panic("locator", "etcd createDir error", LogFields{
				"error": err.Error()})
			return err
		}
	}
	if err = l.Register(); err != nil {
		l.logger.Panic("locator", "Could not register with etcd",
			LogFields{"error": err.Error()})
		return err
	}
	if l.contacts, err = l.getServers(); err != nil {
		l.logger.Panic("locator", "Could not fetch contact list from etcd",
			LogFields{"error": err.Error()})
		return err
	}

	l.closeWait.Add(2)
	if l.startDelay > 0 {
		l.closeWait.Add(1)
		time.AfterFunc(l.startDelay, l.closeReady)
	}
	go l.registerHost()
	go l.refreshHosts()

	return nil
}

func (l *EtcdLocator) closeReady() {
	defer l.closeWait.Done()
	close(l.readySignal)
}

// ReadyNotify implements ReadyNotifier.ReadyNotify.
func (l *EtcdLocator) ReadyNotify() <-chan bool {
	return l.readySignal
}

func (l *EtcdLocator) checkRetry(cluster *etcd.Cluster, attempt int,
	lastResp http.Response, err error) error {

	if l.logger.ShouldLog(ERROR) {
		l.logger.Error("locator", "etcd request error", LogFields{
			"error":   err.Error(),
			"attempt": strconv.Itoa(attempt),
			"status":  strconv.Itoa(lastResp.StatusCode)})
	}
	var retryErr error
	if lastResp.StatusCode >= 500 {
		retryErr = retry.StatusError(lastResp.StatusCode)
	} else {
		retryErr = err
	}
	if _, ok := l.rh.RetryAttempt(attempt, len(cluster.Machines), retryErr); !ok {
		l.metrics.Increment("locator.etcd.error")
		return &etcd.EtcdError{
			ErrorCode: etcd.ErrCodeEtcdNotReachable,
			Message: fmt.Sprintf("Error connecting to etcd after %d attempts",
				attempt),
		}
	}
	l.metrics.Increment("locator.etcd.retry.request")
	return nil
}

// Close stops the locator and closes the etcd client connection.
func (l *EtcdLocator) Close() error {
	return l.closeOnce.Do(l.close)
}

func (l *EtcdLocator) close() (err error) {
	if l.logger.ShouldLog(INFO) {
		l.logger.Info("locator", "Closing etcd locator",
			LogFields{"key": l.key})
	}
	close(l.closeSignal)
	l.closeWait.Wait()
	if l.key == "" {
		return nil
	}
	if _, err = l.client.Delete(l.key, false); err != nil {
		if IsEtcdKeyNotExist(err) {
			return nil
		}
		if l.logger.ShouldLog(ERROR) {
			l.logger.Error("locator", "Error deregistering from etcd",
				LogFields{"error": err.Error(), "key": l.key})
		}
	}
	if l.closeDelay <= 0 {
		return err
	}
	if l.logger.ShouldLog(INFO) {
		l.logger.Info("locator", "Waiting for etcd deregistration to propagate",
			LogFields{"closeDelay": l.closeDelay.String()})
	}
	time.Sleep(l.closeDelay)
	return err
}

// Contacts returns a shuffled list of all nodes in the Simple Push cluster.
// Implements Locator.Contacts().
func (l *EtcdLocator) Contacts(string) (contacts []string, err error) {
	if l.closeOnce.IsDone() {
		return
	}
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
	if l.closeOnce.IsDone() {
		return
	}
	if ok, err = IsEtcdHealthy(l.client); err != nil {
		if l.logger.ShouldLog(ERROR) {
			l.logger.Error("locator", "Failed etcd health check",
				LogFields{"error": err.Error()})
		}
	}
	return
}

// Register registers the server to the etcd cluster.
func (l *EtcdLocator) Register() error {
	if l.logger.ShouldLog(INFO) {
		l.logger.Info("locator", "Registering host with etcd", LogFields{
			"key": l.key, "url": l.url})
	}
	registerOnce := func() (err error) {
		_, err = l.client.Set(l.key, l.url, uint64(l.defaultTTL/time.Second))
		return err
	}
	retries, err := l.rh.RetryFunc(registerOnce)
	l.metrics.IncrementBy("locator.etcd.retry.register", int64(retries))
	if err != nil {
		if l.logger.ShouldLog(CRITICAL) {
			l.logger.Critical("locator", "Failed to register host with etcd",
				LogFields{"error": err.Error(), "key": l.key, "url": l.url})
		}
		return err
	}
	return nil
}

// getServers gets the current contact list from etcd.
func (l *EtcdLocator) getServers() (servers []string, err error) {
	var nodeList *etcd.Response
	getOnce := func() (err error) {
		nodeList, err = l.client.Get(l.dir, false, false)
		return err
	}
	retries, err := l.rh.RetryFunc(getOnce)
	l.metrics.IncrementBy("locator.etcd.retry.fetch", int64(retries))
	if err != nil {
		if l.logger.ShouldLog(CRITICAL) {
			l.logger.Critical("locator", "Could not get server list from etcd",
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

// registerHost periodically re-registers the current node with etcd.
func (l *EtcdLocator) registerHost() {
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

// refreshHosts polls etcd for new nodes.
func (l *EtcdLocator) refreshHosts() {
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
				l.lastFetch = t
				l.contacts = contacts
				l.contactsErr = nil
			}
			l.contactsLock.Unlock()
		}
	}
	fetchTick.Stop()
}

func (l *EtcdLocator) CloseNotify() <-chan bool {
	return l.closeSignal
}

func init() {
	AvailableLocators["etcd"] = func() HasConfigStruct { return NewEtcdLocator() }
}
