/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
// Most of this is copied straight from heka's TOML config setup

package simplepush

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	capn "github.com/glycerine/go-capnproto"
	"github.com/gorilla/mux"
)

var (
	ErrNoLocator       = errors.New("Discovery service not configured")
	ErrInvalidRoutable = errors.New("Malformed routable")
)

type BroadcastRouterConfig struct {
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

	// IdleConns is the maximum number of idle connections to maintain per host.
	// Defaults to 50.
	IdleConns int `toml:"idle_conns" env:"idle_conns"`

	// DefaultHost is the default hostname of the proxy endpoint. No default
	// value; overrides simplepush.Application.Hostname() if specified.
	DefaultHost string `toml:"default_host" env:"default_host"`

	// Listener specifies the address and port, maximum connections, TCP
	// keep-alive period, and certificate information for the routing listener.
	Listener TCPListenerConfig

	MaxDataLen int `toml:"max_data_len" env:"max_data_len"`
}

// Router proxies incoming updates to the Simple Push server ("contact") that
// currently maintains a WebSocket connection to the target device.
type BroadcastRouter struct {
	app         *Application
	hostname    string
	locator     Locator
	listener    net.Listener
	maxConns    int
	server      Server
	logger      *SimpleLogger
	metrics     Statistician
	ctimeout    time.Duration
	rwtimeout   time.Duration
	bucketSize  int
	url         string
	rclient     *http.Client
	closeWait   sync.WaitGroup
	closeSignal chan bool
	maxDataLen  int
	routerMux   *mux.Router
	closeOnce   Once
}

func NewBroadcastRouter() (r *BroadcastRouter) {
	r = &BroadcastRouter{
		routerMux:   mux.NewRouter(),
		closeSignal: make(chan bool),
		rclient:     new(http.Client),
	}
	r.routerMux.HandleFunc("/route/{uaid}", r.RouteHandler)
	return r
}

func (*BroadcastRouter) ConfigStruct() interface{} {
	return &BroadcastRouterConfig{
		BucketSize: 10,
		Ctimeout:   "3s",
		Rwtimeout:  "3s",
		IdleConns:  50,
		Listener: TCPListenerConfig{
			Addr:            ":3000",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
		MaxDataLen: 4096,
	}
}

func (r *BroadcastRouter) Init(app *Application, config interface{}) (err error) {
	conf := config.(*BroadcastRouterConfig)

	r.hostname = conf.DefaultHost
	r.setApp(app)

	// Client configs.
	ctimeout, err := time.ParseDuration(conf.Ctimeout)
	if err != nil {
		r.logger.Panic("router", "Could not parse ctimeout",
			LogFields{"error": err.Error(),
				"ctimeout": conf.Ctimeout})
		return err
	}
	rwtimeout, err := time.ParseDuration(conf.Rwtimeout)
	if err != nil {
		r.logger.Panic("router", "Could not parse rwtimeout",
			LogFields{"error": err.Error(),
				"rwtimeout": conf.Rwtimeout})
		return err
	}
	r.setClientOptions(conf.BucketSize, ctimeout, rwtimeout)
	r.setClientTransport(&http.Transport{
		Dial:                r.dial,
		MaxIdleConnsPerHost: conf.IdleConns,
		TLSClientConfig:     new(tls.Config),
	})

	// Server configs.
	if err = r.listenWithConfig(conf.Listener); err != nil {
		r.logger.Panic("router", "Could not attach listener",
			LogFields{"error": err.Error()})
		return err
	}
	r.maxDataLen = conf.MaxDataLen
	r.server = NewServeCloser(&http.Server{
		ConnState: func(c net.Conn, state http.ConnState) {
			if state == http.StateNew {
				r.metrics.Increment("router.socket.connect")
			} else if state == http.StateClosed {
				r.metrics.Increment("router.socket.disconnect")
			}
		},
		Handler:  &LogHandler{r.routerMux, r.logger},
		ErrorLog: log.New(&LogWriter{r.logger, "router", ERROR}, "", 0)})

	return nil
}

// setApp sets the parent application for this router.
func (r *BroadcastRouter) setApp(app *Application) {
	r.app = app
	r.logger = app.Logger()
	r.metrics = app.Metrics()
	if len(r.hostname) == 0 {
		r.hostname = app.Hostname()
	}
}

// listenWithConfig starts a listener for the routing handler.
func (r *BroadcastRouter) listenWithConfig(conf ListenerConfig) (err error) {
	if r.listener, err = conf.Listen(); err != nil {
		return err
	}
	var scheme string
	if conf.UseTLS() {
		scheme = "https"
	} else {
		scheme = "http"
	}
	host, port := HostPort(r.listener, r)
	r.url = CanonicalURL(scheme, host, port)
	r.maxConns = conf.GetMaxConns()
	return nil
}

// setClientOptions sets the bucket size, connection timeout, and request
// timeout for the HTTP client.
func (r *BroadcastRouter) setClientOptions(bucketSize int, ctimeout,
	rwtimeout time.Duration) {

	r.bucketSize = bucketSize
	r.ctimeout = ctimeout
	r.rwtimeout = rwtimeout
	r.rclient.Timeout = rwtimeout
}

// setClientTransport overrides the HTTP client transport for this router.
// This is used by the tests to install a synthetic dialer.
func (r *BroadcastRouter) setClientTransport(transport http.RoundTripper) {
	r.rclient.Transport = transport
}

func (r *BroadcastRouter) Hostname() string { return r.hostname }

func (r *BroadcastRouter) Start(errChan chan<- error) {
	routeLn := r.Listener()
	if r.logger.ShouldLog(INFO) {
		r.logger.Info("app", "Starting routing server",
			LogFields{"url": r.url})
	}
	errChan <- r.server.Serve(routeLn)
}

func (r *BroadcastRouter) RouteHandler(resp http.ResponseWriter, req *http.Request) {
	var err error
	logWarning := r.logger.ShouldLog(WARNING)
	// get the uaid from the url
	uaid, ok := mux.Vars(req)["uaid"]
	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		r.metrics.Increment("updates.routed.invalid")
		return
	}
	// if uid is not present, or doesn't exist in the known clients...
	if !ok {
		http.Error(resp, "UID Not Found", http.StatusNotFound)
		r.metrics.Increment("updates.routed.unknown")
		return
	}

	worker, found := r.app.GetWorker(uaid)
	if !found {
		http.Error(resp, "UID Not Found", http.StatusNotFound)
		r.metrics.Increment("updates.routed.unknown")
		return
	}

	// We know of this one.
	var (
		routable   Routable
		chid, data string
	)
	segment, err := capn.ReadFromStream(req.Body, nil)
	if err != nil {
		if logWarning {
			r.logger.Warn("router", "Could not read update body",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		goto invalidBody
	}
	routable = ReadRootRoutable(segment)
	chid = routable.ChannelID()
	if len(chid) == 0 {
		if logWarning {
			r.logger.Warn("router", "Missing channel ID",
				LogFields{"rid": req.Header.Get(HeaderID), "uaid": uaid})
		}
		goto invalidBody
	}
	r.metrics.Increment("updates.routed.incoming")
	// Never trust external data
	data = routable.Data()
	if len(data) > r.maxDataLen {
		if logWarning {
			r.logger.Warn("router", "Data segment too long, truncating",
				LogFields{"rid": req.Header.Get(HeaderID),
					"uaid": uaid})
		}
		data = data[:r.maxDataLen]
	}
	// routed data is already in storage.
	if err = worker.Send(chid, routable.Version(), data); err != nil {
		if logWarning {
			r.logger.Warn("router", "Could not update local user",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		http.Error(resp, "Server Error", http.StatusInternalServerError)
		r.metrics.Increment("updates.routed.error")
		return
	}
	resp.Write([]byte("Ok"))
	r.metrics.Increment("updates.routed.received")
	return

invalidBody:
	http.Error(resp, "Invalid body", http.StatusNotAcceptable)
	r.metrics.Increment("updates.routed.invalid")
}

func (r *BroadcastRouter) dial(netw, addr string) (c net.Conn, err error) {
	c, err = net.DialTimeout(netw, addr, r.ctimeout)
	if err != nil {
		r.metrics.Increment("router.dial.error")
		return nil, err
	}
	r.metrics.Increment("router.dial.success")
	return c, nil
}

func (r *BroadcastRouter) Listener() net.Listener {
	return r.listener
}

func (r *BroadcastRouter) MaxConns() int {
	return r.maxConns
}

func (r *BroadcastRouter) URL() string {
	return r.url
}

func (r *BroadcastRouter) ServeMux() ServeMux {
	return (*RouteMux)(r.routerMux)
}

func (r *BroadcastRouter) Register(uaid string) error {
	return nil
}

func (r *BroadcastRouter) Unregister(uaid string) error {
	return nil
}

func (r *BroadcastRouter) Status() (bool, error) {
	locator := r.app.Locator()
	if locator != nil {
		return locator.Status()
	}
	return true, nil
}

func (r *BroadcastRouter) Close() error {
	return r.closeOnce.Do(r.close)
}

func (r *BroadcastRouter) close() (err error) {
	if r.logger.ShouldLog(INFO) {
		r.logger.Info("router", "Closing router",
			LogFields{"url": r.url})
	}
	close(r.closeSignal)
	r.closeWait.Wait()
	if err = r.listener.Close(); err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "Error closing routing listener",
				LogFields{"error": err.Error(), "url": r.url})
		}
	}
	r.server.Close()
	return nil
}

// Route routes an update packet to the correct server.
func (r *BroadcastRouter) Route(cancelSignal <-chan bool, uaid, chid string,
	version int64, sentAt time.Time, logID string, data string) (
	delivered bool, err error) {

	startTime := time.Now()
	locator := r.app.Locator()
	if locator == nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "No discovery service set; unable to route message",
				LogFields{"rid": logID, "uaid": uaid, "chid": chid})
		}
		r.metrics.Increment("router.broadcast.error")
		return false, ErrNoLocator
	}
	segment := capn.NewBuffer(nil)
	routable := NewRootRoutable(segment)
	routable.SetChannelID(chid)
	routable.SetVersion(version)
	routable.SetTime(sentAt.UnixNano())
	routable.SetData(data)
	contacts, err := locator.Contacts(uaid)
	if err != nil {
		if r.logger.ShouldLog(CRITICAL) {
			r.logger.Critical("router", "Could not query discovery service for contacts",
				LogFields{"rid": logID, "error": err.Error()})
		}
		r.metrics.Increment("router.broadcast.error")
		return false, err
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
			"data":    data,
			"time":    strconv.FormatInt(sentAt.UnixNano(), 10)})
	}
	delivered, err = r.notifyAll(cancelSignal, contacts, uaid, segment, logID)
	endTime := time.Now()
	if err != nil {
		if r.logger.ShouldLog(WARNING) {
			r.logger.Warn("router", "Could not post to server",
				LogFields{"rid": logID, "error": err.Error()})
		}
		r.metrics.Increment("router.broadcast.error")
		return false, err
	}
	var counterName, timerName string
	if delivered {
		counterName = "router.broadcast.hit"
		timerName = "updates.routed.hits"
	} else {
		counterName = "router.broadcast.miss"
		timerName = "updates.routed.misses"
	}
	r.metrics.Increment(counterName)
	r.metrics.Timer(timerName, endTime.Sub(sentAt))
	r.metrics.Timer("router.handled", endTime.Sub(startTime))
	return delivered, nil
}

// notifyAll partitions a slice of contacts into buckets, then broadcasts an
// update to each bucket.
func (r *BroadcastRouter) notifyAll(cancelSignal <-chan bool, contacts []string,
	uaid string, segment *capn.Segment, logID string) (delivered bool, err error) {

	for fromIndex := 0; !delivered && fromIndex < len(contacts); {
		toIndex := fromIndex + r.bucketSize
		if toIndex > len(contacts) {
			toIndex = len(contacts)
		}
		if delivered, err = r.notifyBucket(cancelSignal, contacts[fromIndex:toIndex],
			uaid, segment, logID); err != nil {
			break
		}
		fromIndex += toIndex
	}
	return
}

// notifyBucket routes a message to all contacts in a bucket, returning as soon
// as a contact accepts the update.
func (r *BroadcastRouter) notifyBucket(cancelSignal <-chan bool,
	contacts []string, uaid string, segment *capn.Segment, logID string) (
	delivered bool, err error) {

	timeout := r.ctimeout + r.rwtimeout + 1*time.Second
	deliveries := make(chan bool, len(contacts))
	for _, contact := range contacts {
		url := fmt.Sprintf("%s/route/%s", contact, uaid)
		go r.notifyContact(deliveries, url, segment, logID)
	}
	timer := time.After(timeout)
	for i := 0; !delivered && i < cap(deliveries); i++ {
		select {
		case <-r.closeSignal:
			return false, io.EOF
		case <-cancelSignal:
		case delivered = <-deliveries:
		case <-timer:
		}
	}
	return delivered, nil
}

// notifyContact routes a message to a single contact.
func (r *BroadcastRouter) notifyContact(deliveries chan<- bool, url string,
	segment *capn.Segment, logID string) {

	buf := bytes.Buffer{}
	segment.WriteTo(&buf)
	req, err := http.NewRequest("PUT", url, &buf)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("router", "Router request failed",
				LogFields{"rid": logID, "error": err.Error()})
		}
		deliveries <- false
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
		deliveries <- false
		return
	}
	defer resp.Body.Close()
	// Discard the response body. If the body is not fully consumed, the HTTP
	// client will not reuse the underlying TCP connection.
	io.Copy(ioutil.Discard, resp.Body)
	if resp.StatusCode != 200 {
		if r.logger.ShouldLog(DEBUG) {
			r.logger.Debug("router", "Denied",
				LogFields{"rid": logID, "url": url})
		}
		deliveries <- false
		return
	}
	if r.logger.ShouldLog(INFO) {
		r.logger.Info("router", "Server accepted",
			LogFields{"rid": logID, "url": url})
	}
	deliveries <- true
}

func init() {
	AvailableRouters["broadcast"] = func() HasConfigStruct {
		return NewBroadcastRouter()
	}
	AvailableRouters.SetDefault("broadcast")
}
