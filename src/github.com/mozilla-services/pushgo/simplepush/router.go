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

	capn "github.com/glycerine/go-capnproto"
)

var (
	ErrNoLocator       = errors.New("Discovery service not configured")
	ErrInvalidRoutable = errors.New("Malformed routable")
)

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
	segment := capn.NewBuffer(nil)
	routable := NewRootRoutable(segment)
	routable.SetChannelID(chid)
	routable.SetVersion(version)
	routable.SetTime(sentAt.UnixNano())
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
	ok, err := r.notifyAll(cancelSignal, contacts, uaid, segment, logID)
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
	uaid string, segment *capn.Segment, logID string) (ok bool, err error) {

	for fromIndex := 0; !ok && fromIndex < len(contacts); {
		toIndex := fromIndex + r.bucketSize
		if toIndex > len(contacts) {
			toIndex = len(contacts)
		}
		if ok, err = r.notifyBucket(cancelSignal, contacts[fromIndex:toIndex],
			uaid, segment, logID); err != nil {
			break
		}
		fromIndex += toIndex
	}
	return
}

// notifyBucket routes a message to all contacts in a bucket, returning as soon
// as a contact accepts the update.
func (r *Router) notifyBucket(cancelSignal <-chan bool, contacts []string,
	uaid string, segment *capn.Segment, logID string) (ok bool, err error) {

	result, stop := make(chan bool), make(chan struct{})
	defer close(stop)
	timeout := r.ctimeout + r.rwtimeout + 1*time.Second
	timer := time.NewTimer(timeout)
	for _, contact := range contacts {
		url := fmt.Sprintf("%s/route/%s", contact, uaid)
		notify := func() {
			r.notifyContact(result, stop, url, segment, logID)
		}
		select {
		case <-r.closeSignal:
			return false, io.EOF
		case <-cancelSignal:
			return false, nil
		case ok = <-result:
			return ok, nil
		case <-timer.C:
			return false, nil
		case r.runs <- notify:
		}
	}
	timer.Reset(timeout)
	select {
	case ok = <-r.closeSignal:
		return false, io.EOF
	case <-cancelSignal:
	case ok = <-result:
	case <-timer.C:
	}
	return ok, nil
}

// notifyContact routes a message to a single contact.
func (r *Router) notifyContact(result chan<- bool, stop <-chan struct{},
	url string, segment *capn.Segment, logID string) {

	reader, writer := io.Pipe()
	go pipeTo(writer, segment)
	req, err := http.NewRequest("PUT", url, reader)
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
	case <-time.After(1 * time.Second):
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

func pipeTo(dest io.WriteCloser, src io.WriterTo) (err error) {
	if _, err = src.WriteTo(dest); err != nil {
		return
	}
	return dest.Close()
}
