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
	"net"
	"net/http"
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
	BucketSize int `toml:"bucket_size"`

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
	DefaultHost string `toml:"default_host"`

	// Addr is the interface and port that the router will use to receive proxied
	// updates. The port should not be publicly accessible. Defaults to ":3000".
	Addr string

	// UrlTemplate is a text/template source string for constructing the proxy
	// endpoint URL. Interpolated variables are {{.Scheme}}, {{.Host}}, and
	// {{.Uaid}}.
	UrlTemplate string `toml:"url_template"`
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
		Addr:        ":3000",
		UrlTemplate: "{{.Scheme}}://{{.Host}}/route/{{.Uaid}}",
	}
}

func (r *Router) Init(app *Application, config interface{}) (err error) {
	conf := config.(*RouterConfig)
	r.logger = app.Logger()
	r.metrics = app.Metrics()

	if r.template, err = template.New("Route").Parse(conf.UrlTemplate); err != nil {
		r.logger.Critical("router", "Could not parse router template",
			LogFields{"error": err.Error(),
				"template": conf.UrlTemplate})
		return err
	}
	if r.ctimeout, err = time.ParseDuration(conf.Ctimeout); err != nil {
		r.logger.Error("router", "Could not parse ctimeout",
			LogFields{"error": err.Error(),
				"ctimeout": conf.Ctimeout})
		return err
	}
	if r.rwtimeout, err = time.ParseDuration(conf.Rwtimeout); err != nil {
		r.logger.Error("router", "Could not parse rwtimeout",
			LogFields{"error": err.Error(),
				"rwtimeout": conf.Rwtimeout})
		return err
	}

	if r.listener, err = Listen(conf.Addr); err != nil {
		r.logger.Error("router", "Could not attach listener",
			LogFields{"error": err.Error()})
		return err
	}

	r.bucketSize = conf.BucketSize
	r.scheme = conf.Scheme
	r.hostname = conf.DefaultHost
	if len(r.hostname) == 0 {
		r.hostname = app.Hostname()
	}

	addr := r.listener.Addr().(*net.TCPAddr)
	if len(r.hostname) == 0 {
		r.hostname = addr.IP.String()
	}
	r.port = addr.Port

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

// SendUpdate routes an update packet to the correct server.
func (r *Router) SendUpdate(uaid, chid string, version int64, timer time.Time) (err error) {
	startTime := time.Now()
	locator := r.Locator()
	if locator == nil {
		r.logger.Warn("router", "No discovery service set; unable to route message",
			LogFields{"uaid": uaid, "chid": chid})
		r.metrics.Increment("router.broadcast.error")
		return ErrNoLocator
	}
	msg, err := json.Marshal(&Routable{
		ChannelID: chid,
		Version:   version,
		Time:      timer.Format(time.RFC3339Nano),
	})
	if err != nil {
		r.logger.Error("router", "Could not compose routing message",
			LogFields{"error": err.Error()})
		r.metrics.Increment("router.broadcast.error")
		return err
	}
	contacts, err := locator.Contacts(uaid)
	if err != nil {
		r.logger.Error("router", "Could not query discovery service for contacts",
			LogFields{"error": err.Error()})
		r.metrics.Increment("router.broadcast.error")
		return err
	}
	ok, err := r.notifyAll(contacts, uaid, msg)
	endTime := time.Now()
	if err != nil {
		r.logger.Error("router", "Could not post to server",
			LogFields{"error": err.Error()})
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
	r.metrics.Timer(timerName, endTime.Sub(timer))
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
func (r *Router) notifyAll(contacts []string, uaid string, msg []byte) (ok bool, err error) {
	if r.logger.ShouldLog(DEBUG) {
		r.logger.Debug("router", "Sending push...", LogFields{"msg": string(msg),
			"servers": strings.Join(contacts, ", ")})
	}
	for fromIndex := 0; !ok && fromIndex < len(contacts); {
		toIndex := fromIndex + r.bucketSize
		if toIndex > len(contacts) {
			toIndex = len(contacts)
		}
		if ok, err = r.notifyBucket(contacts[fromIndex:toIndex], uaid, msg); err != nil {
			break
		}
		fromIndex += toIndex
	}
	return
}

// notifyBucket routes a message to all contacts in a bucket, returning as soon
// as a contact accepts the update.
func (r *Router) notifyBucket(contacts []string, uaid string, msg []byte) (ok bool, err error) {
	result, stop := make(chan bool), make(chan struct{})
	defer close(stop)
	for _, contact := range contacts {
		url, err := r.formatURL(contact, uaid)
		if err != nil {
			r.logger.Error("router", "Could not build routing URL",
				LogFields{"error": err.Error()})
			return false, err
		}
		go r.notifyContact(result, stop, contact, url, msg)
	}
	select {
	case ok = <-r.closeSignal:
		return false, io.EOF
	case ok = <-result:
	case <-time.After(r.ctimeout + r.rwtimeout + 1*time.Second):
	}
	return ok, nil
}

// notifyContact routes a message to a single contact.
func (r *Router) notifyContact(result chan<- bool, stop <-chan struct{}, contact, url string, msg []byte) {
	body := bytes.NewReader(msg)
	req, err := http.NewRequest("PUT", url, body)
	if err != nil {
		r.logger.Warn("router", "Router request failed", LogFields{"error": err.Error()})
		return
	}
	if r.logger.ShouldLog(DEBUG) {
		r.logger.Debug("router", "Sending request",
			LogFields{"server": contact,
				"url":  url,
				"body": string(msg)})
	}
	req.Header.Add("Content-Type", "application/json")
	resp, err := r.rclient.Do(req)
	if err != nil {
		r.logger.Warn("router", "Router send failed", LogFields{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		r.logger.Debug("router", "Denied", LogFields{"server": contact})
		return
	}
	r.logger.Debug("router", "Server accepted", LogFields{"server": contact})
	select {
	case <-stop:
	case result <- true:
	}
}
