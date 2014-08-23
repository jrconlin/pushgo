// HTTP version of the cross machine router
// Fetch the servers from etcd, divvy them up into buckets,
// proxy the update to the servers, stopping once you've gotten
// a successful return.
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
	"text/template"
	"time"
)

var (
	ErrNoLocator      = errors.New("Discovery service not configured")
	AvailableLocators = make(AvailableExtensions)
)

// Routable is the update payload sent to each contact.
type Routable struct {
	DeviceID  string `json:"uaid"`
	ChannelID string `json:"chid"`
	Version   int64  `json:"version"`
	Time      string `json:"time"`
}

type LocatorConf struct {
	Etcd EtcdConf
}

// Locator describes a contact discovery service.
type Locator interface {
	// Close stops and releases any resources associated with the Locator.
	Close() error

	// Contacts returns a slice of candidate peers for the router to probe. For
	// an etcd-based Locator, the slice may contain all nodes in the cluster;
	// for a DHT-based Locator, the slice may contain either a single node or a
	// short list of the closest nodes.
	Contacts(uaid string) ([]string, error)

	// MaxParallel returns the maximum number of contacts that the router should
	// probe before checking replies.
	MaxParallel() int
}

type RouterConfig struct {
	// Ctimeout is the maximum amount of time that the router's `RoundTripper`
	// should wait for a dial to succeed. Defaults to 3 seconds.
	Ctimeout string

	// Rwtimeout is the maximum amount of time that the router should wait for an
	// HTTP request to complete. Defaults to 3 seconds.
	Rwtimeout string

	// Scheme is the scheme component of the proxy endpoint, used by the router
	// to construct the endpoint of a peer. Defaults to `"http"`.
	Scheme string

	// DefaultHost is the default hostname of the proxy endpoint. No default
	// value; overrides `app.Hostname()` if specified.
	DefaultHost string `toml:"default_host"`

	// Port is the port that the router will use to receive proxied updates. This
	// port should not be publicly accessible. Defaults to 3000.
	Port int

	// UrlTemplate is a text/template source string for constructing the proxy
	// endpoint URL. Interpolated variables are `{{.Scheme}}`, `{{.Host}}`,
	// and `{{.Uaid}}`.
	UrlTemplate string `toml:"url_template"`
}

// Router proxies incoming updates to the Simple Push server ("contact") that
// currently maintains a WebSocket connection to the target device.
type Router struct {
	locator     Locator
	logger      *SimpleLogger
	metrics     *Metrics
	template    *template.Template
	ctimeout    time.Duration
	rwtimeout   time.Duration
	scheme      string
	hostname    string
	port        int
	rclient     *http.Client
	closeSignal chan bool
}

func NewRouter() *Router {
	return &Router{
		closeSignal: make(chan bool),
	}
}

func (*Router) ConfigStruct() interface{} {
	return &RouterConfig{
		Ctimeout:    "3s",
		Rwtimeout:   "3s",
		Scheme:      "http",
		Port:        3000,
		UrlTemplate: "{{.Scheme}}://{{.Host}}/route/{{.Uaid}}",
	}
}

func (r *Router) Init(app *Application, config interface{}) (err error) {
	conf := config.(*RouterConfig)
	r.logger = app.Logger()
	r.metrics = app.Metrics()

	r.template, err = template.New("Route").Parse(conf.UrlTemplate)
	if err != nil {
		r.logger.Critical("router", "Could not parse router template",
			LogFields{"error": err.Error(),
				"template": conf.UrlTemplate})
		return
	}
	r.ctimeout, err = time.ParseDuration(conf.Ctimeout)
	if err != nil {
		r.logger.Error("router", "Could not parse ctimeout",
			LogFields{"error": err.Error(),
				"ctimeout": conf.Ctimeout})
		return
	}
	r.rwtimeout, err = time.ParseDuration(conf.Rwtimeout)
	if err != nil {
		r.logger.Error("router", "Could not parse rwtimeout",
			LogFields{"error": err.Error(),
				"rwtimeout": conf.Rwtimeout})
		return
	}

	r.scheme = conf.Scheme
	r.hostname = conf.DefaultHost
	if r.hostname == "" {
		r.hostname = app.Hostname()
	}
	r.port = conf.Port

	r.rclient = &http.Client{
		Transport: &http.Transport{
			Dial: TimeoutDialer(r.ctimeout, r.rwtimeout),
			ResponseHeaderTimeout: r.rwtimeout,
			TLSClientConfig:       new(tls.Config),
		},
	}

	return
}

func (r *Router) SetLocator(locator Locator) error {
	r.locator = locator
	return nil
}

func (r *Router) Locator() Locator {
	return r.locator
}

func (r *Router) Close() (err error) {
	close(r.closeSignal)
	if r.locator != nil {
		err = r.locator.Close()
	}
	return
}

// SendUpdate broadcasts an update packet to the correct server.
func (r *Router) SendUpdate(uaid, chid string, version int64, timer time.Time) (err error) {
	if r.locator == nil {
		r.logger.Warn("router", "No discovery service set; unable to route message",
			LogFields{"uaid": uaid, "chid": chid})
		return ErrNoLocator
	}
	msg, err := json.Marshal(&Routable{
		DeviceID:  uaid,
		ChannelID: chid,
		Version:   version,
		Time:      timer.Format(time.RFC3339Nano),
	})
	if err != nil {
		r.logger.Error("router", "Could not compose routing message",
			LogFields{"error": err.Error()})
		return
	}
	contacts, err := r.locator.Contacts(uaid)
	if err != nil {
		r.logger.Error("router", "Could not query discovery service for contacts",
			LogFields{"error": err.Error()})
		return
	}
	if _, err = r.notifyAll(contacts, uaid, msg); err != nil {
		r.logger.Error("router", "Could not post to server",
			LogFields{"error": err.Error()})
		return
	}
	return
}

// FormatURL constructs a proxy endpoint for the given contact and device ID.
func (r *Router) FormatURL(contact, uaid string) (string, error) {
	url := new(bytes.Buffer)
	err := r.template.Execute(url, struct {
		Scheme, Host, Uaid string
	}{r.scheme, contact, uaid})
	if err != nil {
		return "", err
	}
	return url.String(), nil
}

func (r *Router) notifyAll(contacts []string, uaid string, msg []byte) (ok bool, err error) {
	if r.logger.ShouldLog(DEBUG) {
		r.logger.Debug("router", "Sending push...", LogFields{"msg": string(msg),
			"servers": strings.Join(contacts, ", ")})
	}
	// The buffered lease channel ensures that the number of in-flight requests
	// never exceeds `r.locator.MaxParallel()`.
	leases := make(chan struct{}, r.locator.MaxParallel())
	for i := 0; i < cap(leases); i++ {
		leases <- struct{}{}
	}
	stop := make(chan struct{})
	defer close(stop)
	// Broadcast the message to all contacts in the list returned by the
	// discovery service. The `stop` channel will be closed as soon as a
	// contact accepts the update.
	result := make(chan bool)
	for _, contact := range contacts {
		url, err := r.FormatURL(contact, uaid)
		if err != nil {
			r.logger.Error("router", "Could not build routing URL",
				LogFields{"error": err.Error()})
			return false, err
		}
		go r.notifyOne(result, leases, stop, contact, url, msg)
	}
	select {
	case ok = <-r.closeSignal:
		err = io.EOF
	case ok = <-result:
	case <-time.After(r.ctimeout + r.rwtimeout + 1*time.Second):
	}
	return
}

// notifyOne routes a message to a single contact.
func (r *Router) notifyOne(result chan<- bool, leases chan struct{}, stop <-chan struct{}, contact, url string, msg []byte) {
	lease := <-leases
	defer func() {
		leases <- lease
	}()
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

// TimeoutDialer returns a dialer function suitable for use with an
// `http.Transport` instance.
func TimeoutDialer(cTimeout, rwTimeout time.Duration) func(net, addr string) (c net.Conn, err error) {

	return func(netw, addr string) (c net.Conn, err error) {
		c, err = net.DialTimeout(netw, addr, cTimeout)
		if err != nil {
			return nil, err
		}
		// do we need this if ResponseHeaderTimeout is set?
		c.SetDeadline(time.Now().Add(rwTimeout))
		return c, nil
	}
}
