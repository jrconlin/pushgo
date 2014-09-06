/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"code.google.com/p/go.net/websocket"
	"github.com/gorilla/mux"
)

type ApplicationConfig struct {
	Hostname           string `toml:"current_host" env:"current_host"`
	TokenKey           string `toml:"token_key" env:"token_key"`
	MaxGoroutines      int    `toml:"max_connections" env:"max_goroutines"`
	KeepAlivePeriod    string `toml:"keep_alive_period" env:"keep_alive_period"`
	UseAwsHost         bool   `toml:"use_aws_host" env:"use_aws"`
	ClientMinPing      string `toml:"client_min_ping_interval" env:"min_ping"`
	ClientHelloTimeout string `toml:"client_hello_timeout" env:"hello_timeout"`
	PushLongPongs      bool   `toml:"push_long_pongs" env:"long_pongs"`
}

type Application struct {
	hostname           string
	host               string
	port               int
	maxGoroutines      int
	keepAlivePeriod    time.Duration
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
		MaxGoroutines:      1000,
		KeepAlivePeriod:    "3m",
		UseAwsHost:         false,
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

	if conf.UseAwsHost {
		if a.hostname, err = GetAWSPublicHostname(); err != nil {
			return
		}
	} else {
		a.hostname = conf.Hostname
	}

	if len(conf.TokenKey) > 0 {
		if a.tokenKey, err = base64.URLEncoding.DecodeString(conf.TokenKey); err != nil {
			return
		}
	}

	if a.clientMinPing, err = time.ParseDuration(conf.ClientMinPing); err != nil {
		err = fmt.Errorf("Unable to parse 'client_min_ping_interval': %s",
			err.Error())
		return
	}
	if a.clientHelloTimeout, err = time.ParseDuration(conf.ClientHelloTimeout); err != nil {
		err = fmt.Errorf("Unable to parse 'client_hello_timeout': %s",
			err.Error())
		return
	}
	if a.keepAlivePeriod, err = time.ParseDuration(conf.KeepAlivePeriod); err != nil {
		err = fmt.Errorf("Unable to parse 'keep_alive_period': %s",
			err.Error())
		return
	}
	a.pushLongPongs = conf.PushLongPongs
	a.maxGoroutines = conf.MaxGoroutines
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

	RESTMux := mux.NewRouter()
	RESTListener := a.server.Listener()

	RouteMux := mux.NewRouter()
	RouteListener := a.router.Listener()

	RESTMux.HandleFunc("/update/{key}", a.handlers.UpdateHandler)
	RESTMux.HandleFunc("/status/", a.handlers.StatusHandler)
	RESTMux.HandleFunc("/realstatus/", a.handlers.RealStatusHandler)
	RESTMux.HandleFunc("/metrics/", a.handlers.MetricsHandler)
	RESTMux.Handle("/", websocket.Handler(a.handlers.PushSocketHandler))

	RouteMux.HandleFunc("/route/{uaid}", a.handlers.RouteHandler)

	// Weigh the anchor!
	go func() {
		a.log.Info("app", fmt.Sprintf("listening on %s", RESTListener.Addr()), nil)
		errChan <- http.Serve(RESTListener, RESTMux)
	}()

	go func() {
		a.log.Info("app", "Starting Router", LogFields{"addr": RouteListener.Addr().String()})
		errChan <- http.Serve(RouteListener, RouteMux)
	}()

	return errChan
}

func (a *Application) Hostname() string {
	return a.hostname
}

func (a *Application) MaxGoroutines() int {
	return a.maxGoroutines
}

func (a *Application) KeepAlivePeriod() time.Duration {
	return a.keepAlivePeriod
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
}
