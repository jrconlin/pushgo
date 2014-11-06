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
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

var ErrNoLocator = errors.New("Discovery service not configured")

// Routable is the update payload sent to each contact.
type Routable struct {
	ChannelID string `json:"chid"`
	Version   int64  `json:"version"`
	Time      string `json:"time"`
}

type RouterConfig struct {
	// BucketSize is the maximum number of contacts to probe at once. The router
	// will defer requests until all nodes in a bucket have responded. Defaults
	// to 10 contacts.
	BucketSize int `toml:"bucket_size" env:"bucket_size"`

	// Ctimeout is the maximum amount of time that the router's rclient should
	// should wait for a dial to succeed. Defaults to 3 seconds.
	Ctimeout string

	// Rwtimeout is the maximum amount of time that the router should wait for an
	// HTTP request to complete. Defaults to 3 seconds.
	Rwtimeout string

	// Scheme is the scheme component of the proxy endpoint, used by the router
	// to construct the endpoint of a peer. Defaults to "http".
	Scheme string

	// DefaultHost is the default hostname of the proxy endpoint. No default
	// value; overrides simplepush.Application.Hostname() if specified.
	DefaultHost string `toml:"default_host" env:"default_host"`

	// UrlTemplate is a text/template source string for constructing the proxy
	// endpoint URL. Interpolated variables are {{.Scheme}}, {{.Host}}, and
	// {{.Uaid}}.
	UrlTemplate string `toml:"url_template" env:"url_template"`

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
	template    *template.Template
	ctimeout    time.Duration
	rwtimeout   time.Duration
	bucketSize  int
	scheme      string
	hostname    string
	port        int
	rclient     *http.Client
	isClosing   bool
	closeSignal chan bool
	closeLock   sync.Mutex
	lastErr     error
}

func NewRouter() *Router {
	return &Router{
		closeSignal: make(chan bool),
	}
}

func (*Router) ConfigStruct() interface{} {
	return &RouterConfig{
		BucketSize:  10,
		Ctimeout:    "3s",
		Rwtimeout:   "3s",
		Scheme:      "http",
		UrlTemplate: "{{.Scheme}}://{{.Host}}/route/{{.Uaid}}",
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

	if r.template, err = template.New("Route").Parse(conf.UrlTemplate); err != nil {
		r.logger.Alert("router", "Could not parse router template",
			LogFields{"error": err.Error(),
				"template": conf.UrlTemplate})
		return err
	}
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
	if r.hostname = conf.DefaultHost; len(r.hostname) == 0 {
		r.hostname = app.Hostname()
	}
	addr := r.listener.Addr().(*net.TCPAddr)
	if len(r.hostname) == 0 {
		r.hostname = addr.IP.String()
	}
	r.port = addr.Port

	r.bucketSize = conf.BucketSize
	r.scheme = conf.Scheme

	r.rclient = &http.Client{
		Transport: &http.Transport{
			Dial: TimeoutDialer(r.ctimeout, r.rwtimeout),
			ResponseHeaderTimeout: r.rwtimeout,
			TLSClientConfig:       new(tls.Config),
		},
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

func (r *Router) Close() (err error) {
	defer r.closeLock.Unlock()
	r.closeLock.Lock()
	if r.isClosing {
		return r.lastErr
	}
	close(r.closeSignal)
	if locator := r.Locator(); locator != nil {
		err = locator.Close()
	}
	r.listener.Close()
	r.isClosing = true
	r.lastErr = err
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
	msg, err := json.Marshal(&Routable{
		ChannelID: chid,
		Version:   version,
		Time:      sentAt.Format(time.RFC3339Nano),
	})
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

// formatURL constructs a proxy endpoint for the given contact and device ID.
func (r *Router) formatURL(contact, uaid string) (string, error) {
	url := new(bytes.Buffer)
	err := r.template.Execute(url, struct {
		Scheme, Host, Uaid string
	}{r.scheme, contact, uaid})
	if err != nil {
		return "", err
	}
	return url.String(), nil
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
	for _, i := range rand.Perm(len(contacts)) {
		contact := contacts[i]
		url, err := r.formatURL(contact, uaid)
		if err != nil {
			if r.logger.ShouldLog(ERROR) {
				r.logger.Error("router", "Could not build routing URL",
					LogFields{"rid": logID, "error": err.Error()})
			}
			return false, err
		}
		go r.notifyContact(result, stop, contact, url, msg, logID)
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
	contact, url string, msg []byte, logID string) {

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
			LogFields{"rid": logID, "server": contact, "url": url})
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
				LogFields{"rid": logID, "server": contact})
		}
		return
	}
	if r.logger.ShouldLog(INFO) {
		r.logger.Info("router", "Server accepted",
			LogFields{"rid": logID, "server": contact})
	}
	select {
	case <-stop:
	case result <- true:
	}
}
