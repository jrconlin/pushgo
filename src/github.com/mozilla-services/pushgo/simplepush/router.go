/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
// HTTP version of the cross machine router
// Fetch a list of peers (via an etcd store, DHT, or static list), divvy them
// up into buckets, proxy the update to the servers, stopping once you've
// gotten a successful return.
// PROS:
//  Very simple to implement
//  hosts can autoannounce
//  no AWS dependencies
// CONS:
//  fair bit of cross traffic (try to minimize)

// Obviously a PubSub would be better, but might require more server
// state management (because of duplicate messages)
package simplepush

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoLocator       = errors.New("Discovery service not configured")
	ErrInvalidRoutable = errors.New("Malformed routable")
)

var routablePool = sync.Pool{New: func() interface{} {
	return new(Routable)
}}

func NewRoutable() *Routable {
	return routablePool.Get().(*Routable)
}

// Routable is the update payload sent to each contact.
type Routable struct {
	ChannelID string
	Version   int64
	Time      time.Time
	bytes     []byte
}

func (r *Routable) MarshalText() ([]byte, error) {
	version := strconv.FormatInt(r.Version, 10)
	sentAt := r.Time.Format(time.RFC3339Nano)
	size := len(r.ChannelID) + len(version) + len(sentAt) + 2
	if cap(r.bytes) < size {
		r.bytes = make([]byte, size)
	} else {
		r.bytes = r.bytes[:size]
	}
	offset := copy(r.bytes, r.ChannelID)
	r.bytes[offset] = '\n'
	offset++
	offset += copy(r.bytes[offset:], version)
	r.bytes[offset] = '\n'
	offset++
	offset += copy(r.bytes[offset:], sentAt)
	return r.bytes, nil
}

func (r *Routable) UnmarshalText(data []byte) (err error) {
	if len(data) == 0 {
		return ErrInvalidRoutable
	}
	if cap(data) > cap(r.bytes) {
		r.bytes = data
	}
	startVersion := bytes.IndexByte(data, '\n')
	if startVersion < 0 {
		return ErrInvalidRoutable
	}
	r.ChannelID = string(data[:startVersion])
	startVersion++
	startTime := bytes.IndexByte(data[startVersion:], '\n')
	if startTime < 0 {
		return ErrInvalidRoutable
	}
	version := string(data[startVersion : startVersion+startTime])
	startTime++
	if r.Version, err = strconv.ParseInt(version, 10, 64); err != nil {
		return err
	}
	sentAt := string(data[startVersion+startTime:])
	if r.Time, err = time.Parse(time.RFC3339Nano, sentAt); err != nil {
		return err
	}
	return nil
}

func (r *Routable) Recycle() {
	if cap(r.bytes) > 1024 {
		return
	}
	r.bytes = r.bytes[:0]
	routablePool.Put(r)
}

type RouterConfig struct {
	// BucketSize is the maximum number of contacts to probe at once. The router
	// will defer requests until all nodes in a bucket have responded. Defaults
	// to 10 contacts.
	BucketSize int `toml:"bucket_size" env:"bucket_size"`

	// PoolSize is the number of goroutines to spawn for routing messages.
	// Defaults to 30.
	PoolSize int `toml:"pool_size" env:"pool_size"`

	// Ctimeout is the maximum amount of time that the router's rclient should
	// should wait for a dial to succeed. Defaults to 3 seconds.
	Ctimeout string

	// Rwtimeout is the maximum amount of time that the router should wait for an
	// HTTP request to complete. Defaults to 3 seconds.
	Rwtimeout string

	// DefaultHost is the default hostname of the proxy endpoint. No default
	// value; overrides simplepush.Application.Hostname() if specified.
	DefaultHost string `toml:"default_host" env:"default_host"`

	// Listener specifies the address and port, maximum connections, TCP
	// keep-alive period, and certificate information for the routing listener.
	Listener ListenerConfig
}

// Router proxies incoming updates to the Simple Push server ("contact") that
// currently maintains a WebSocket connection to the target device.
type Router struct {
	locator     Locator
	listener    net.Listener
	logger      *SimpleLogger
	metrics     *Metrics
	ctimeout    time.Duration
	rwtimeout   time.Duration
	bucketSize  int
	poolSize    int
	url         string
	runs        chan func()
	rclient     *http.Client
	closeWait   sync.WaitGroup
	isClosed    bool
	closeSignal chan bool
	closeLock   sync.Mutex
	lastErr     error
}

func NewRouter() *Router {
	return &Router{
		runs:        make(chan func()),
		closeSignal: make(chan bool),
	}
}

func (*Router) ConfigStruct() interface{} {
	return &RouterConfig{
		BucketSize: 10,
		PoolSize:   30,
		Ctimeout:   "3s",
		Rwtimeout:  "3s",
		Listener: ListenerConfig{
			Addr:            ":3000",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
	}
}

func (r *Router) Init(app *Application, config interface{}) (err error) {
	conf := config.(*RouterConfig)
	r.logger = app.Logger()
	r.metrics = app.Metrics()

	if r.ctimeout, err = time.ParseDuration(conf.Ctimeout); err != nil {
		r.logger.Alert("router", "Could not parse ctimeout",
			LogFields{"error": err.Error(),
				"ctimeout": conf.Ctimeout})
		return err
	}
	if r.rwtimeout, err = time.ParseDuration(conf.Rwtimeout); err != nil {
		r.logger.Alert("router", "Could not parse rwtimeout",
			LogFields{"error": err.Error(),
				"rwtimeout": conf.Rwtimeout})
		return err
	}

	if r.listener, err = conf.Listener.Listen(); err != nil {
		r.logger.Alert("router", "Could not attach listener",
			LogFields{"error": err.Error()})
		return err
	}
	var scheme string
	if conf.Listener.UseTLS() {
		scheme = "https"
	} else {
		scheme = "http"
	}
	host := conf.DefaultHost
	if len(host) == 0 {
		host = app.Hostname()
	}
	addr := r.listener.Addr().(*net.TCPAddr)
	if len(host) == 0 {
		host = addr.IP.String()
	}
	r.url = CanonicalURL(scheme, host, addr.Port)

	r.bucketSize = conf.BucketSize
	r.poolSize = conf.PoolSize

	r.rclient = &http.Client{
		Transport: &http.Transport{
			Dial: TimeoutDialer(r.ctimeout, r.rwtimeout),
			ResponseHeaderTimeout: r.rwtimeout,
			TLSClientConfig:       new(tls.Config),
		},
	}

	r.closeWait.Add(r.poolSize)
	for i := 0; i < r.poolSize; i++ {
		go r.runLoop()
	}

	return nil
}

func (r *Router) SetLocator(locator Locator) error {
	r.locator = locator
	return nil
}

func (r *Router) Locator() Locator {
	return r.locator
}

func (r *Router) Listener() net.Listener {
	return r.listener
}

func (r *Router) URL() string {
	return r.url
}

func (r *Router) Close() (err error) {
	r.closeLock.Lock()
	err = r.lastErr
	if r.isClosed {
		r.closeLock.Unlock()
		return err
	}
	r.isClosed = true
	close(r.closeSignal)
	if locator := r.Locator(); locator != nil {
		r.lastErr = locator.Close()
	}
	if err := r.listener.Close(); err != nil {
		r.lastErr = err
	}
	r.closeLock.Unlock()
	r.closeWait.Wait()
	return err
}

// Route routes an update packet to the correct server.
func (r *Router) Route(cancelSignal <-chan bool, uaid, chid string, version int64, sentAt time.Time, logID string) (err error) {
	startTime := time.Now()
	locator := r.Locator()
	if locator == nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "No discovery service set; unable to route message",
				LogFields{"rid": logID, "uaid": uaid, "chid": chid})
		}
		r.metrics.Increment("router.broadcast.error")
		return ErrNoLocator
	}
	routable := NewRoutable()
	defer routable.Recycle()
	routable.ChannelID = chid
	routable.Version = version
	routable.Time = sentAt
	msg, err := routable.MarshalText()
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "Could not compose routing message",
				LogFields{"rid": logID, "error": err.Error()})
		}
		r.metrics.Increment("router.broadcast.error")
		return err
	}
	contacts, err := locator.Contacts(uaid)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "Could not query discovery service for contacts",
				LogFields{"rid": logID, "error": err.Error()})
		}
		r.metrics.Increment("router.broadcast.error")
		return err
	}
	if r.logger.ShouldLog(DEBUG) {
		r.logger.Debug("router", "Fetched contact list from discovery service",
			LogFields{"rid": logID, "servers": strings.Join(contacts, ", ")})
	}
	if r.logger.ShouldLog(INFO) {
		r.logger.Info("router", "Sending push...", LogFields{
			"rid":     logID,
			"uaid":    uaid,
			"chid":    chid,
			"version": strconv.FormatInt(version, 10),
			"time":    strconv.FormatInt(sentAt.UnixNano(), 10)})
	}
	ok, err := r.notifyAll(cancelSignal, contacts, uaid, msg, logID)
	endTime := time.Now()
	if err != nil {
		if r.logger.ShouldLog(WARNING) {
			r.logger.Warn("router", "Could not post to server",
				LogFields{"rid": logID, "error": err.Error()})
		}
		r.metrics.Increment("router.broadcast.error")
		return err
	}
	var counterName, timerName string
	if ok {
		counterName = "router.broadcast.hit"
		timerName = "updates.routed.hits"
	} else {
		counterName = "router.broadcast.miss"
		timerName = "updates.routed.misses"
	}
	r.metrics.Increment(counterName)
	r.metrics.Timer(timerName, endTime.Sub(sentAt))
	r.metrics.Timer("router.handled", endTime.Sub(startTime))
	return nil
}

// notifyAll partitions a slice of contacts into buckets, then broadcasts an
// update to each bucket.
func (r *Router) notifyAll(cancelSignal <-chan bool, contacts []string,
	uaid string, msg []byte, logID string) (ok bool, err error) {

	for fromIndex := 0; !ok && fromIndex < len(contacts); {
		toIndex := fromIndex + r.bucketSize
		if toIndex > len(contacts) {
			toIndex = len(contacts)
		}
		if ok, err = r.notifyBucket(cancelSignal, contacts[fromIndex:toIndex], uaid, msg, logID); err != nil {
			break
		}
		fromIndex += toIndex
	}
	return
}

// notifyBucket routes a message to all contacts in a bucket, returning as soon
// as a contact accepts the update.
func (r *Router) notifyBucket(cancelSignal <-chan bool, contacts []string,
	uaid string, msg []byte, logID string) (ok bool, err error) {

	result, stop := make(chan bool), make(chan struct{})
	defer close(stop)
	for _, contact := range contacts {
		url := fmt.Sprintf("%s/route/%s", contact, uaid)
		notify := func() {
			r.notifyContact(result, stop, url, msg, logID)
		}
		r.Submit(notify)
	}
	select {
	case ok = <-r.closeSignal:
		return false, io.EOF
	case <-cancelSignal:
	case ok = <-result:
	case <-time.After(r.ctimeout + r.rwtimeout + 1*time.Second):
	}
	return ok, nil
}

// notifyContact routes a message to a single contact.
func (r *Router) notifyContact(result chan<- bool, stop <-chan struct{},
	url string, msg []byte, logID string) {

	body := bytes.NewReader(msg)
	req, err := http.NewRequest("PUT", url, body)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "Router request failed",
				LogFields{"rid": logID, "error": err.Error()})
		}
		return
	}
	req.Header.Set(HeaderID, logID)
	if r.logger.ShouldLog(DEBUG) {
		r.logger.Debug("router", "Sending request",
			LogFields{"rid": logID, "url": url})
	}
	req.Header.Add("Content-Type", "application/json")
	resp, err := r.rclient.Do(req)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "Router send failed",
				LogFields{"rid": logID, "error": err.Error()})
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if r.logger.ShouldLog(DEBUG) {
			r.logger.Debug("router", "Denied",
				LogFields{"rid": logID, "url": url})
		}
		return
	}
	if r.logger.ShouldLog(INFO) {
		r.logger.Info("router", "Server accepted",
			LogFields{"rid": logID, "url": url})
	}
	select {
	case <-stop:
	case result <- true:
	}
}

func (r *Router) Submit(run func()) {
	select {
	case <-r.closeSignal:
	case r.runs <- run:
	}
}

func (r *Router) runLoop() {
	defer r.closeWait.Done()
	for ok := true; ok; {
		select {
		case ok = <-r.closeSignal:
		case run := <-r.runs:
			run()
		}
	}
}
