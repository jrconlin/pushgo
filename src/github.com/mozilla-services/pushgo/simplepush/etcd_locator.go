/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"math/rand"
	"net/url"
	"path"
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
}

// EtcdLocator stores routing endpoints in etcd and polls for new contacts.
type EtcdLocator struct {
	logger          *SimpleLogger
	metrics         *Metrics
	refreshInterval time.Duration
	defaultTTL      time.Duration
	serverList      []string
	dir             string
	url             string
	key             string
	client          *etcd.Client
	contactsLock    sync.RWMutex
	contacts        []string
	contactsErr     error
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
	l.client = etcd.NewClient(l.serverList)

	// create the push hosts directory (if not already there)
	if _, err = l.client.CreateDir(l.dir, 0); err != nil {
		clientErr, ok := err.(*etcd.EtcdError)
		if !ok || clientErr.ErrorCode != 105 {
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
	err = l.contactsErr
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
	if _, err = l.client.Set(l.key, l.url,
		uint64(l.defaultTTL/time.Second)); err != nil {
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
	nodeList, err := l.client.Get(l.dir, false, false)
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
		case <-fetchTick.C:
			contacts, err := l.getServers()
			l.contactsLock.Lock()
			l.contacts, l.contactsErr = contacts, err
			l.contactsLock.Unlock()
		}
	}
	fetchTick.Stop()
}

func init() {
	AvailableLocators["etcd"] = func() HasConfigStruct { return NewEtcdLocator() }
}
