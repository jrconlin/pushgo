/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"io"
	"math/rand"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-etcd/etcd"
)

const (
	minTTL = 2 * time.Second
)

var ErrMinTTL = fmt.Errorf("Default TTL too short; want at least %s", minTTL)

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

// etcdFetch is an etcd contact list request.
type etcdFetch struct {
	replies chan []string
	errors  chan error
}

// EtcdLocator stores routing endpoints in etcd and polls for new contacts.
type EtcdLocator struct {
	logger          *SimpleLogger
	metrics         *Metrics
	refreshInterval time.Duration
	defaultTTL      time.Duration
	serverList      []string
	dir             string
	authority       string
	key             string
	client          *etcd.Client
	fetches         chan etcdFetch
	isClosing       bool
	closeSignal     chan bool
	closeWait       sync.WaitGroup
	closeLock       sync.Mutex
	lastErr         error
}

func NewEtcdLocator() *EtcdLocator {
	return &EtcdLocator{
		fetches:     make(chan etcdFetch),
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
		l.logger.Error("etcd", "Could not parse refreshInterval",
			LogFields{"error": err.Error(),
				"refreshInterval": conf.RefreshInterval})
		return err
	}
	// default time for the server to be "live"
	if l.defaultTTL, err = time.ParseDuration(conf.DefaultTTL); err != nil {
		l.logger.Critical("etcd",
			"Could not parse etcd default TTL",
			LogFields{"value": conf.DefaultTTL, "error": err.Error()})
		return err
	}
	if l.defaultTTL < minTTL {
		l.logger.Critical("etcd",
			"default TTL too short",
			LogFields{"value": conf.DefaultTTL})
		return ErrMinTTL
	}

	l.serverList = conf.Servers
	l.dir = path.Clean(conf.Dir)

	// The authority of the current server is used as the etcd key.
	router := app.Router()
	if hostname := router.hostname; hostname != "" {
		if router.scheme == "https" && router.port != 443 || router.scheme == "http" && router.port != 80 {
			l.authority = fmt.Sprintf("%s:%d", hostname, router.port)
		} else {
			l.authority = hostname
		}
		l.key = path.Join(l.dir, l.authority)
	}

	l.logger.Debug("etcd", "connecting to etcd servers",
		LogFields{"list": strings.Join(l.serverList, ";")})
	l.client = etcd.NewClient(l.serverList)

	// create the push hosts directory (if not already there)
	if _, err = l.client.CreateDir(l.dir, 0); err != nil {
		clientErr, ok := err.(*etcd.EtcdError)
		if !ok || clientErr.ErrorCode != 105 {
			l.logger.Error("etcd", "etcd createDir error", LogFields{
				"error": err.Error()})
			return err
		}
	}
	if err = l.Register(); err != nil {
		l.logger.Critical("etcd", "Could not register with etcd",
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
	replies, errors := make(chan []string, 1), make(chan error, 1)
	l.fetches <- etcdFetch{replies, errors}
	select {
	case <-l.closeSignal:
		return nil, io.EOF

	case contacts = <-replies:
	case err = <-errors:
	}
	if err != nil {
		l.logger.Error("etcd", "Could not get server list",
			LogFields{"error": err.Error()})
		return nil, err
	}
	for length := len(contacts); length > 0; {
		index := rand.Intn(length)
		length--
		contacts[index], contacts[length] = contacts[length], contacts[index]
	}
	return contacts, nil
}

// Register registers the server to the etcd cluster.
func (l *EtcdLocator) Register() (err error) {
	if l.logger.ShouldLog(DEBUG) {
		l.logger.Debug("etcd", "Registering host", LogFields{"host": l.authority})
	}
	if _, err = l.client.Set(l.key, l.authority, uint64(l.defaultTTL/time.Second)); err != nil {
		l.logger.Error("etcd", "Failed to register",
			LogFields{"error": err.Error(),
				"key":  l.key,
				"host": l.authority})
		return err
	}
	return nil
}

// getServers gets the current contact list from etcd.
func (l *EtcdLocator) getServers() (servers []string, err error) {
	nodeList, err := l.client.Get(l.dir, false, false)
	if err != nil {
		return nil, err
	}
	reply := make([]string, 0, len(nodeList.Node.Nodes))
	for _, node := range nodeList.Node.Nodes {
		if node.Value == l.authority || node.Value == "" {
			continue
		}
		reply = append(reply, node.Value)
	}
	return reply, nil
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

// fetchLoop polls etcd for new nodes and responds to requests for contacts.
func (l *EtcdLocator) fetchLoop() {
	defer l.closeWait.Done()
	var (
		lastReply   []string
		lastRefresh time.Time
	)
	for ok := true; ok; {
		select {
		case ok = <-l.closeSignal:
		case <-time.After(l.refreshInterval):
			if reply, err := l.getServers(); err == nil {
				lastReply = reply
				lastRefresh = time.Now()
			}

		case fetch := <-l.fetches:
			if !lastRefresh.IsZero() && time.Now().Sub(lastRefresh) < l.refreshInterval {
				fetch.replies <- lastReply
				break
			}
			reply, err := l.getServers()
			if err != nil {
				fetch.errors <- err
				break
			}
			lastReply = reply
			lastRefresh = time.Now()
			fetch.replies <- reply
		}
	}
}

func init() {
	AvailableLocators["etcd"] = func() HasConfigStruct { return NewEtcdLocator() }
}
