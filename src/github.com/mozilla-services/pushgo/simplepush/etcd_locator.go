package simplepush

import (
	"fmt"
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
	// `"push_hosts"`.
	Dir string `toml:"dir"`

	// BucketSize is the maximum number of requests that the router should send
	// before checking replies. Defaults to 10.
	BucketSize int `toml:"bucket_size"`

	// Servers is a list of etcd servers.
	Servers []string

	// DefaultTTL is the maximum amount of time that registered contacts will be
	// considered valid. Defaults to `"24h"`.
	DefaultTTL string

	// RefreshInterval is the maximum amount of time that a cached contact list
	// will be considered valid. Defaults to `"5m"`.
	RefreshInterval string `toml:"refresh_interval"`
}

// EtcdLocator stores routing endpoints in etcd and periodically polls for new
// contacts.
type EtcdLocator struct {
	sync.Mutex
	logger          *SimpleLogger
	metrics         *Metrics
	refreshInterval time.Duration
	defaultTTL      time.Duration
	bucketSize      int
	serverList      []string
	dir             string
	authority       string
	key             string
	client          *etcd.Client
	lastRefresh     time.Time
	closeSignal     chan bool
	closeWait       sync.WaitGroup
}

func NewEtcdLocator() *EtcdLocator {
	return &EtcdLocator{
		closeSignal: make(chan bool),
	}
}

func (*EtcdLocator) ConfigStruct() interface{} {
	return &EtcdLocatorConf{
		Dir:             "push_hosts",
		BucketSize:      10,
		Servers:         []string{"http://localhost:4001"},
		DefaultTTL:      "24h",
		RefreshInterval: "5m",
	}
}

func (l *EtcdLocator) Init(app *Application, config interface{}) (err error) {
	conf := config.(*EtcdLocatorConf)
	l.logger = app.Logger()
	l.metrics = app.Metrics()

	l.refreshInterval, err = time.ParseDuration(conf.RefreshInterval)
	if err != nil {
		l.logger.Error("etcd", "Could not parse refreshInterval",
			LogFields{"error": err.Error(),
				"refreshInterval": conf.RefreshInterval})
		return
	}
	// default time for the server to be "live"
	l.defaultTTL, err = time.ParseDuration(conf.DefaultTTL)
	if err != nil {
		l.logger.Critical("etcd",
			"Could not parse etcd default TTL",
			LogFields{"value": conf.DefaultTTL, "error": err.Error()})
		return
	}
	if l.defaultTTL < minTTL {
		l.logger.Critical("etcd",
			"default TTL too short",
			LogFields{"value": conf.DefaultTTL})
		return ErrMinTTL
	}

	l.bucketSize = conf.BucketSize
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
	_, err = l.client.CreateDir(l.dir, 0)
	if err != nil {
		clientErr, ok := err.(*etcd.EtcdError)
		if !ok || clientErr.ErrorCode != 105 {
			l.logger.Error("etcd", "etcd createDir error", LogFields{
				"error": err.Error()})
			return
		}
	}
	if _, err = l.getServers(); err != nil {
		l.logger.Critical("etcd", "Could not initialize server list",
			LogFields{"error": err.Error()})
		return
	}
	if err = l.Register(); err != nil {
		l.logger.Critical("etcd", "Could not register with etcd",
			LogFields{"error": err.Error()})
		return
	}

	l.closeWait.Add(1)
	go l.refresh()
	return
}

// Close stops the locator and closes the etcd client connection. Implements
// `Locator.Close()`.
func (l *EtcdLocator) Close() (err error) {
	close(l.closeSignal)
	l.closeWait.Wait()
	if l.key != "" {
		_, err = l.client.Delete(l.key, false)
	}
	return err
}

// Contacts returns a shuffled list of all nodes in the Simple Push cluster.
// Implements `Locator.Contacts()`.
func (l *EtcdLocator) Contacts(string) ([]string, error) {
	contacts, err := l.getServers()
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

// MaxParallel returns the maximum number of requests that the router should
// send in parallel. Implements `Locator.MaxParallel()`.
func (l *EtcdLocator) MaxParallel() int {
	return l.bucketSize
}

// Register registers the server to the etcd cluster.
func (l *EtcdLocator) Register() error {
	if l.logger.ShouldLog(DEBUG) {
		l.logger.Debug("etcd", "Registering host", LogFields{"host": l.authority})
	}
	if _, err := l.client.Set(l.key, l.authority, uint64(l.defaultTTL/time.Second)); err != nil {
		l.logger.Error("etcd", "Failed to register",
			LogFields{"error": err.Error(),
				"key":  l.key,
				"host": l.authority})
		return err
	}
	return nil
}

// getServers gets the contact list from etcd.
func (l *EtcdLocator) getServers() ([]string, error) {
	l.Lock()
	defer l.Unlock()
	if time.Now().Sub(l.lastRefresh) < l.refreshInterval {
		return l.serverList, nil
	}
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
	l.serverList = reply
	l.lastRefresh = time.Now()
	return reply, nil
}

// refresh periodically re-registers the host with etcd.
func (l *EtcdLocator) refresh() {
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

func init() {
	AvailableLocators["etcd"] = func() HasConfigStruct { return NewEtcdLocator() }
}
