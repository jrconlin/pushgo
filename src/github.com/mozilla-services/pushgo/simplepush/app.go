/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/net/websocket"
)

// The Simple Push server version, set by the linker.
var VERSION string

var (
	ErrMissingOrigin = errors.New("Missing WebSocket origin")
	ErrInvalidOrigin = errors.New("WebSocket origin not allowed")
)

type ApplicationConfig struct {
	Origins            []string
	Hostname           string `toml:"current_host" env:"current_host"`
	TokenKey           string `toml:"token_key" env:"token_key"`
	UseAwsHost         bool   `toml:"use_aws_host" env:"use_aws"`
	ResolveHost        bool   `toml:"resolve_host" env:"resolve_host"`
	ClientMinPing      string `toml:"client_min_ping_interval" env:"min_ping"`
	ClientHelloTimeout string `toml:"client_hello_timeout" env:"hello_timeout"`
	PushLongPongs      bool   `toml:"push_long_pongs" env:"long_pongs"`
}

type Application struct {
	origins            []*url.URL
	hostname           string
	host               string
	port               int
	clientMinPing      time.Duration
	clientHelloTimeout time.Duration
	pushLongPongs      bool
	tokenKey           []byte
	log                *SimpleLogger
	metrics            *Metrics
	clients            map[string]*Client
	clientMux          *sync.RWMutex
	clientCount        *int32
	server             *Serv
	store              Store
	router             *Router
	handlers           *Handler
	propping           PropPinger
}

func (a *Application) ConfigStruct() interface{} {
	defaultHost, _ := os.Hostname()
	return &ApplicationConfig{
		Hostname:           defaultHost,
		UseAwsHost:         false,
		ResolveHost:        false,
		ClientMinPing:      "20s",
		ClientHelloTimeout: "30s",
	}
}

// Fully initialize the application, this initializes all the other components
// as well.
// Note: We implement the Init method to comply with the interface, so the app
// passed here will be nil.
func (a *Application) Init(_ *Application, config interface{}) (err error) {
	conf := config.(*ApplicationConfig)

	if len(conf.Origins) > 0 {
		a.origins = make([]*url.URL, len(conf.Origins))
		for index, origin := range conf.Origins {
			if a.origins[index], err = url.ParseRequestURI(origin); err != nil {
				return fmt.Errorf("Error parsing origin: %s", err)
			}
		}
	}

	if conf.UseAwsHost {
		if a.hostname, err = GetAWSPublicHostname(); err != nil {
			return fmt.Errorf("Error querying AWS instance metadata service: %s", err)
		}
	} else if conf.ResolveHost {
		addr, err := net.ResolveIPAddr("ip", conf.Hostname)
		if err != nil {
			return fmt.Errorf("Error resolving hostname: %s", err)
		}
		a.hostname = addr.String()
	} else {
		a.hostname = conf.Hostname
	}

	if len(conf.TokenKey) > 0 {
		if a.tokenKey, err = base64.URLEncoding.DecodeString(conf.TokenKey); err != nil {
			return fmt.Errorf("Malformed token key: %s", err)
		}
	}

	if a.clientMinPing, err = time.ParseDuration(conf.ClientMinPing); err != nil {
		return fmt.Errorf("Unable to parse 'client_min_ping_interval': %s",
			err.Error())
	}
	if a.clientHelloTimeout, err = time.ParseDuration(conf.ClientHelloTimeout); err != nil {
		return fmt.Errorf("Unable to parse 'client_hello_timeout': %s",
			err.Error())
	}
	a.pushLongPongs = conf.PushLongPongs
	a.clients = make(map[string]*Client)
	a.clientMux = new(sync.RWMutex)
	count := int32(0)
	a.clientCount = &count
	return
}

// Set a logger
func (a *Application) SetLogger(logger Logger) (err error) {
	a.log, err = NewLogger(logger)
	return
}

func (a *Application) SetPropPinger(ping PropPinger) (err error) {
	a.propping = ping
	return
}

func (a *Application) SetMetrics(metrics *Metrics) error {
	a.metrics = metrics
	return nil
}

func (a *Application) SetStore(store Store) error {
	a.store = store
	return nil
}

func (a *Application) SetRouter(router *Router) error {
	a.router = router
	return nil
}

func (a *Application) SetServer(server *Serv) error {
	a.server = server
	return nil
}

func (a *Application) SetHandlers(handlers *Handler) error {
	a.handlers = handlers
	return nil
}

// Start the application
func (a *Application) Run() (errChan chan error) {
	errChan = make(chan error)

	clientMux := mux.NewRouter()
	clientMux.HandleFunc("/status/", a.handlers.StatusHandler)
	clientMux.HandleFunc("/realstatus/", a.handlers.RealStatusHandler)
	clientMux.Handle("/", websocket.Server{Handler: a.handlers.PushSocketHandler,
		Handshake: a.checkOrigin})

	endpointMux := mux.NewRouter()
	endpointMux.HandleFunc("/update/{key}", a.handlers.UpdateHandler)
	endpointMux.HandleFunc("/status/", a.handlers.StatusHandler)
	endpointMux.HandleFunc("/realstatus/", a.handlers.RealStatusHandler)
	endpointMux.HandleFunc("/metrics/", a.handlers.MetricsHandler)

	routeMux := mux.NewRouter()
	routeMux.HandleFunc("/route/{uaid}", a.handlers.RouteHandler)

	// Weigh the anchor!
	go func() {
		clientLn := a.server.ClientListener()
		if a.log.ShouldLog(INFO) {
			a.log.Info("app", "Starting WebSocket server",
				LogFields{"addr": clientLn.Addr().String()})
		}
		clientSrv := &http.Server{
			Handler:  &LogHandler{clientMux, a.log},
			ErrorLog: log.New(&LogWriter{a.log.Logger, "worker", ERROR}, "", 0)}
		errChan <- clientSrv.Serve(clientLn)
	}()

	go func() {
		endpointLn := a.server.EndpointListener()
		if a.log.ShouldLog(INFO) {
			a.log.Info("app", "Starting update server",
				LogFields{"addr": endpointLn.Addr().String()})
		}
		endpointSrv := &http.Server{
			Handler:  &LogHandler{endpointMux, a.log},
			ErrorLog: log.New(&LogWriter{a.log.Logger, "endpoint", ERROR}, "", 0)}
		errChan <- endpointSrv.Serve(endpointLn)
	}()

	go func() {
		routeLn := a.router.Listener()
		if a.log.ShouldLog(INFO) {
			a.log.Info("app", "Starting router",
				LogFields{"addr": routeLn.Addr().String()})
		}
		routeSrv := &http.Server{
			Handler:  &LogHandler{routeMux, a.log},
			ErrorLog: log.New(&LogWriter{a.log.Logger, "router", ERROR}, "", 0)}
		errChan <- routeSrv.Serve(routeLn)
	}()

	return errChan
}

func (a *Application) Hostname() string {
	return a.hostname
}

func (a *Application) Logger() *SimpleLogger {
	return a.log
}

//TODO: move these to handler so we can deal with multiple prop.ping formats
func (a *Application) PropPinger() PropPinger {
	return a.propping
}

func (a *Application) Store() Store {
	return a.store
}

func (a *Application) Metrics() *Metrics {
	return a.metrics
}

func (a *Application) Router() *Router {
	return a.router
}

func (a *Application) Server() *Serv {
	return a.server
}

func (a *Application) TokenKey() []byte {
	return a.tokenKey
}

func (a *Application) ClientCount() (count int) {
	return int(atomic.LoadInt32(a.clientCount))
}

func (a *Application) ClientExists(uaid string) (collision bool) {
	_, collision = a.GetClient(uaid)
	return
}

func (a *Application) GetClient(uaid string) (client *Client, ok bool) {
	a.clientMux.RLock()
	client, ok = a.clients[uaid]
	a.clientMux.RUnlock()
	return
}

func (a *Application) checkOrigin(conf *websocket.Config,
	req *http.Request) (err error) {

	if len(a.origins) == 0 {
		return nil
	}
	if conf.Origin, err = websocket.Origin(conf, req); err != nil {
		if a.log.ShouldLog(WARNING) {
			a.log.Warn("http", "Error parsing WebSocket origin",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		return err
	}
	if conf.Origin == nil {
		return ErrMissingOrigin
	}
	for _, origin := range a.origins {
		if isSameOrigin(conf.Origin, origin) {
			return nil
		}
	}
	if a.log.ShouldLog(WARNING) {
		a.log.Warn("http", "Rejected WebSocket connection from unknown origin",
			LogFields{"rid": req.Header.Get(HeaderID), "origin": conf.Origin.String()})
	}
	return ErrInvalidOrigin
}

func (a *Application) AddClient(uaid string, client *Client) {
	a.clientMux.Lock()
	a.clients[uaid] = client
	a.clientMux.Unlock()
	atomic.AddInt32(a.clientCount, 1)
}

func (a *Application) RemoveClient(uaid string) {
	var ok bool
	a.clientMux.Lock()
	if _, ok = a.clients[uaid]; ok {
		delete(a.clients, uaid)
	}
	a.clientMux.Unlock()
	if ok {
		atomic.AddInt32(a.clientCount, -1)
	}
}

func (a *Application) Stop() {
	a.server.Close()
	a.router.Close()
	a.store.Close()
	a.log.Close()
}

func isSameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}
