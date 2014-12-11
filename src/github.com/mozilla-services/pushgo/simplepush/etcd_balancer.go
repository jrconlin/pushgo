/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-etcd/etcd"

	"github.com/mozilla-services/pushgo/retry"
)

var (
	// ErrNoPeers is returned if the cluster is full.
	ErrNoPeers = errors.New("No peers available")

	// ErrNoDir is returned if an etcd key path for a peer node does not start
	// with the directory name.
	ErrNoDir = errors.New("Key missing directory name")

	// ErrNoScheme is returned if an etcd key path does not contain the scheme of
	// a peer server.
	ErrNoScheme = errors.New("Key missing scheme")

	// ErrNoHost is returned if an etcd key path does not contain the peer's host
	// and port.
	ErrNoHost = errors.New("Key missing host")
)

type EtcdBalancerConf struct {
	// Dir is the etcd directory containing the free connection counts.
	// Defaults to "push_free_conns".
	Dir string

	// Servers is a list of etcd servers.
	Servers []string

	// TTL is the maximum amount of time that published connection counts will
	// be considered valid. Defaults to "1m".
	TTL string

	// Threshold is the redirection threshold. Once this threshold is reached,
	// the balancer will redirect connecting clients to other hosts.
	// Defaults to 0.95 (i.e., clients will be redirected once the host is at
	// 95% capacity).
	Threshold float64

	// UpdateInterval is the interval for publishing client counts to etcd.
	// Defaults to "10s".
	UpdateInterval string `toml:"update_interval" env:"update_interval"`

	// Retry specifies request retry options.
	Retry retry.Config
}

// EtcdBalancer stores the number of available client connections in etcd.
// Clients connecting to an overloaded host will be redirected using a
// weighted random strategy.
type EtcdBalancer struct {
	client    *etcd.Client
	maxConns  int
	threshold float64
	dir       string
	url       *url.URL
	key       string
	rh        *retry.Helper
	connCount func() int

	fetchLock sync.RWMutex // Protects the following fields.
	peers     EtcdPeers
	fetchErr  error
	lastFetch time.Time

	log            *SimpleLogger
	metrics        Statistician
	updateInterval time.Duration
	ttl            time.Duration

	closeLock sync.Mutex // Protects isClosed.
	isClosed  bool

	closeWait   sync.WaitGroup
	closeSignal chan bool
}

// EtcdPeer contains peer information.
type EtcdPeer struct {
	URL       string
	FreeConns int64
}

// EtcdPeers is a list of peers sorted by free connection count.
type EtcdPeers []EtcdPeer

func (p EtcdPeers) Len() int           { return len(p) }
func (p EtcdPeers) Less(i, j int) bool { return p[i].FreeConns < p[j].FreeConns }
func (p EtcdPeers) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

func (p EtcdPeers) Sum() (sum int64) {
	for _, n := range p {
		sum += n.FreeConns
	}
	return sum
}

// Choose returns a weighted random choice from the peer list.
func (p EtcdPeers) Choose() (peer EtcdPeer, ok bool) {
	if len(p) == 0 {
		ok = false
		return
	}
	sum := p.Sum()
	if sum == 0 {
		ok = false
		return
	}
	if len(p) == 1 {
		return p[0], true
	}
	w := rand.Int63n(sum)
	i := sort.Search(len(p)-1, func(i int) bool { return p[i].FreeConns >= w })
	return p[i], true
}

func NewEtcdBalancer() *EtcdBalancer {
	return &EtcdBalancer{
		closeSignal: make(chan bool),
	}
}

func (b *EtcdBalancer) ConfigStruct() interface{} {
	return &EtcdBalancerConf{
		Dir:            "push_free_conns",
		Servers:        []string{"http://localhost:4001"},
		TTL:            "1m",
		Threshold:      0.95,
		UpdateInterval: "10s",
	}
}

func (b *EtcdBalancer) Init(app *Application, config interface{}) (err error) {
	conf := config.(*EtcdBalancerConf)
	b.log = app.Logger()
	b.metrics = app.Metrics()

	b.connCount = app.ClientCount
	b.maxConns = app.Server().MaxClientConns()

	b.threshold = conf.Threshold
	b.dir = path.Clean(conf.Dir)

	clientURL := app.Server().ClientURL()
	if b.url, err = url.ParseRequestURI(clientURL); err != nil {
		b.log.Panic("balancer", "Error parsing client endpoint", LogFields{
			"error": err.Error(), "url": clientURL})
		return err
	}
	if len(b.url.Host) > 0 {
		b.key = path.Join(b.dir, b.url.Scheme, b.url.Host)
	}

	if b.updateInterval, err = time.ParseDuration(conf.UpdateInterval); err != nil {
		b.log.Panic("balancer", "Error parsing 'updateInterval'", LogFields{
			"error": err.Error(), "updateInterval": conf.UpdateInterval})
		return err
	}
	if b.ttl, err = time.ParseDuration(conf.TTL); err != nil {
		b.log.Panic("balancer", "Error parsing 'ttl'", LogFields{
			"error": err.Error(), "ttl": conf.TTL})
		return err
	}

	if b.rh, err = conf.Retry.NewHelper(); err != nil {
		b.log.Panic("balancer", "Error configuring retry helper",
			LogFields{"error": err.Error()})
		return err
	}
	b.rh.CloseNotifier = b
	b.rh.CanRetry = IsEtcdTemporary

	b.client = etcd.NewClient(conf.Servers)
	b.client.CheckRetry = b.checkRetry

	if _, err = b.client.CreateDir(b.dir, 0); err != nil {
		if !IsEtcdKeyExist(err) {
			b.log.Panic("balancer", "Error creating etcd directory",
				LogFields{"error": err.Error()})
			return err
		}
	}

	b.closeWait.Add(2)
	go b.updateCounts()
	go b.publishCounts()

	return nil
}

// RedirectURL returns the absolute URL of an available peer. Implements
// Balancer.RedirectURL().
func (b *EtcdBalancer) RedirectURL() (url string, ok bool, err error) {
	currentConns := int64(b.connCount())
	if float64(currentConns)/float64(b.maxConns) < b.threshold {
		return "", false, nil
	}
	b.fetchLock.RLock()
	if b.fetchErr != nil && time.Since(b.lastFetch) > b.ttl {
		err = b.fetchErr
	}
	peer, ok := b.peers.Choose()
	b.fetchLock.RUnlock()
	if !ok || int64(b.maxConns)-currentConns >= peer.FreeConns {
		return "", false, ErrNoPeers
	}
	return peer.URL, true, err
}

func (b *EtcdBalancer) updateCounts() {
	defer b.closeWait.Done()
	ticker := time.NewTicker(b.updateInterval)
	for ok := true; ok; {
		select {
		case ok = <-b.closeSignal:
		case t := <-ticker.C:
			peers, err := b.Fetch()
			b.fetchLock.Lock()
			if err != nil {
				b.fetchErr = err
			} else {
				b.lastFetch = t
				b.peers = peers
			}
			b.fetchLock.Unlock()
		}
	}
	ticker.Stop()
}

func (b *EtcdBalancer) publishCounts() {
	defer b.closeWait.Done()
	publishInterval := time.Duration(0.75*b.ttl.Seconds()) * time.Second
	ticker := time.NewTicker(publishInterval)
	for ok := true; ok; {
		select {
		case ok = <-b.closeSignal:
		case <-ticker.C:
			b.Publish()
		}
	}
	ticker.Stop()
}

// Status determines whether etcd is available. Implements Balancer.Status().
func (b *EtcdBalancer) Status() (ok bool, err error) {
	if ok, err = IsEtcdHealthy(b.client); err != nil {
		if b.log.ShouldLog(ERROR) {
			b.log.Error("balancer", "Failed etcd health check",
				LogFields{"error": err.Error()})
		}
	}
	return
}

// Close stops the balancer and closes the connection to etcd. Implements
// Balancer.Close().
func (b *EtcdBalancer) Close() (err error) {
	b.closeLock.Lock()
	isClosed := b.isClosed
	if !isClosed {
		b.isClosed = true
	}
	b.closeLock.Unlock()
	if isClosed {
		return nil
	}
	close(b.closeSignal)
	b.closeWait.Wait()
	if len(b.key) > 0 {
		_, err = b.client.Delete(b.key, false)
	}
	return err
}

// parseKey extracts the scheme and host from an etcd key in the form of
// "/push_free_conns/ws/172.16.0.0:8081".
func (b *EtcdBalancer) parseKey(key string) (scheme, host string, err error) {
	if len(key) == 0 || key[0] != '/' {
		err = ErrNoDir
		return
	}
	path := strings.TrimPrefix(key[1:], b.dir)
	if len(path) == 0 {
		err = ErrNoDir
		return
	}
	parts := strings.SplitN(path[1:], "/", 2)
	if len(parts) == 0 {
		err = ErrNoScheme
		return
	}
	if len(parts) == 1 {
		err = ErrNoHost
		return
	}
	return parts[0], parts[1], nil
}

func (b *EtcdBalancer) filterPeers(root *etcd.Node) (peers EtcdPeers, err error) {
	logWarning := b.log.ShouldLog(WARNING)
	walkFn := func(n *etcd.Node) error {
		if len(n.Value) == 0 {
			// Ignore empty nodes.
			return nil
		}
		scheme, host, err := b.parseKey(n.Key)
		if err != nil {
			// Ignore malformed keys.
			if logWarning {
				b.log.Warn("balancer", "Failed to parse host key from etcd", LogFields{
					"error": err.Error(), "key": n.Key})
			}
			return nil
		}
		if scheme == b.url.Scheme && host == b.url.Host {
			// Ignore origin server.
			return nil
		}
		freeConns, err := strconv.ParseInt(n.Value, 10, 64)
		if err != nil {
			if logWarning {
				b.log.Warn("balancer", "Failed to parse connection count from etcd",
					LogFields{"error": err.Error(), "host": host, "count": n.Value})
			}
			return nil
		}
		if freeConns == 0 {
			// Ignore full peers.
			return nil
		}
		peers = append(peers, EtcdPeer{
			URL:       fmt.Sprintf("%s://%s", scheme, host),
			FreeConns: freeConns})
		return nil
	}
	if err = EtcdWalk(root, walkFn); err != nil {
		return nil, err
	}
	return peers, nil
}

// Fetch retrieves a list of peer nodes from etcd, sorted by free connections.
func (b *EtcdBalancer) Fetch() (peers EtcdPeers, err error) {
	var response *etcd.Response
	fetchOnce := func() (err error) {
		response, err = b.client.Get(b.dir, false, true)
		return err
	}
	retries, err := b.rh.RetryFunc(fetchOnce)
	b.metrics.IncrementBy("balancer.fetch.retry", int64(retries))
	if err != nil {
		if b.log.ShouldLog(CRITICAL) {
			b.log.Critical("balancer",
				"Failed to retrieve free connection counts from etcd",
				LogFields{"error": err.Error()})
		}
		b.metrics.Increment("balancer.fetch.error")
		return nil, err
	}
	b.metrics.Increment("balancer.fetch.success")
	if peers, err = b.filterPeers(response.Node); err != nil {
		if b.log.ShouldLog(ERROR) {
			b.log.Error("balancer", "Failed to filter peers from etcd", LogFields{
				"error": err.Error()})
		}
		return nil, err
	}
	sort.Sort(peers)
	return peers, nil
}

// Publish stores the client count for the current node in etcd.
func (b *EtcdBalancer) Publish() (err error) {
	freeConns := strconv.Itoa(b.maxConns - b.connCount())
	if b.log.ShouldLog(INFO) {
		b.log.Info("balancer", "Publishing free connection count to etcd",
			LogFields{"host": b.url.Host, "conns": freeConns})
	}
	publishOnce := func() (err error) {
		_, err = b.client.Set(b.key, freeConns, uint64(b.ttl/time.Second))
		return err
	}
	retries, err := b.rh.RetryFunc(publishOnce)
	b.metrics.IncrementBy("balancer.publish.retry", int64(retries))
	if err != nil {
		if b.log.ShouldLog(CRITICAL) {
			b.log.Critical("balancer", "Error publishing connection count to etcd",
				LogFields{"error": err.Error(), "conns": freeConns, "host": b.url.Host})
		}
		b.metrics.Increment("balancer.publish.error")
		return err
	}
	b.metrics.Increment("balancer.publish.success")
	return nil
}

func (b *EtcdBalancer) CloseNotify() <-chan bool {
	return b.closeSignal
}

func (b *EtcdBalancer) checkRetry(cluster *etcd.Cluster, attempt int,
	lastResp http.Response, err error) error {

	if b.log.ShouldLog(ERROR) {
		b.log.Error("balancer", "etcd request error", LogFields{
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
	if _, ok := b.rh.RetryAttempt(attempt, len(cluster.Machines), retryErr); !ok {
		b.metrics.Increment("balancer.etcd.error")
		return &etcd.EtcdError{
			ErrorCode: etcd.ErrCodeEtcdNotReachable,
			Message: fmt.Sprintf("Error connecting to etcd after %d retries",
				attempt),
		}
	}
	b.metrics.Increment("balancer.etcd.retry")
	return nil
}

func init() {
	AvailableBalancers["etcd"] = func() HasConfigStruct { return NewEtcdBalancer() }
}
