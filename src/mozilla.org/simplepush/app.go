/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"fmt"
	"github.com/gorilla/mux"
	"time"

	"encoding/base64"
	"net/http"
	"os"
	"sync"
)

type ApplicationConfig struct {
	hostname           string `toml:"current_host"`
	host               string
	port               int
	tokenKey           string `toml:"token_key"`
	maxConnections     int    `toml:"max_connections"`
	useAwsHost         bool   `toml:"use_aws_host"`
	sslCertFile        string `toml:"ssl_cert_file"`
	sslKeyFile         string `toml:"ssl_key_file"`
	clientMinPing      string `toml:"client_min_ping_interval"`
	clientHelloTimeout string `toml:"client_hello_timeout"`
	pushLongPongs      bool   `toml:"push_long_pongs"`
	gcm                GCMConfig
}

type GCMConfig struct {
	TTL         int    `toml:"ttl"`
	CollapseKey string `toml:"collapse_key"`
	ProjectId   string `toml:"project_id"`
	DryRun      bool   `toml:"dry_run"`
	ApiKey      string `toml:"api_key"`
	Url         string `toml:"url"`
}

type Application struct {
	hostname           string
	fullHostname       string
	host               string
	port               int
	maxConnnections    int
	clientMinPing      time.Duration
	clientHelloTimeout time.Duration
	pushLongPongs      bool
	tokenKey           []byte
	sslCertFile        string
	sslKeyFile         string
	log                *SimpleLogger
	metrics            *Metrics
	clients            map[string]*Client
	clientMux          *sync.RWMutex
	server             *Serv
	storage            *Storage
	router             *Router
	handlers           *Handler
	gcm                *GCMConfig
}

func (a *Application) ConfigStruct() interface{} {
	defaultHost, _ := os.Hostname()
	return &ApplicationConfig{
		hostname:           defaultHost,
		host:               "0.0.0.0",
		port:               8080,
		maxConnections:     1000,
		useAwsHost:         false,
		clientMinPing:      "20s",
		clientHelloTimeout: "30s",
		gcm: GCMConfig{
			TTL:         259200,
			CollapseKey: "simplepush",
			ProjectId:   "simplepush-gcm",
			DryRun:      false,
			Url:         "https://android.googleapis.com/gcm/send",
		},
	}
}

// Fully initialize the application, this initializes all the other components
// as well.
// Note: We implement the Init method to comply with the interface, so the app
// passed here will be nil.
func (a *Application) Init(app *Application, config interface{}) (err error) {
	conf := config.(*ApplicationConfig)

	if conf.useAwsHost {
		if a.hostname, err = GetAWSPublicHostname(); err != nil {
			return
		}
	} else {
		a.hostname = conf.hostname
	}

	token_str := conf.tokenKey
	if len(token_str) > 0 {
		if a.tokenKey, err = base64.URLEncoding.DecodeString(token_str); err != nil {
			return
		}
	}

	usingSSL := len(conf.sslCertFile) > 0 && len(conf.sslKeyFile) > 0
	if usingSSL && conf.port == 443 {
		a.fullHostname = "https://" + a.hostname
	} else if usingSSL {
		a.fullHostname = "https://" + a.hostname + ":" + string(conf.port)
	} else if conf.port == 80 {
		a.fullHostname = "http://" + a.hostname
	} else {
		a.fullHostname = "http://" + a.hostname + ":" + string(conf.port)
	}

	a.gcm = &conf.gcm
	a.host = conf.host
	a.port = conf.port
	if a.clientMinPing, err = time.ParseDuration(conf.clientMinPing); err != nil {
		err = fmt.Errorf("Unable to parse 'client_min_ping_interval: %s",
			err.Error())
		return
	}
	if a.clientHelloTimeout, err = time.ParseDuration(conf.clientHelloTimeout); err != nil {
		err = fmt.Errorf("Unable to parse 'client_hello_timeout: %s",
			err.Error())
		return
	}
	a.pushLongPongs = conf.pushLongPongs
	a.sslCertFile = conf.sslCertFile
	a.sslKeyFile = conf.sslKeyFile
	a.maxConnnections = conf.maxConnections
	a.clients = make(map[string]*Client)
	a.clientMux = new(sync.RWMutex)
	return
}

// Set a logger
func (a *Application) SetLogger(logger Logger) (err error) {
	a.log, err = NewLogger(logger)
	return
}

func (a *Application) SetMetrics(metrics *Metrics) error {
	a.metrics = metrics
	return nil
}

func (a *Application) SetStorage(storage *Storage) error {
	a.storage = storage
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
	RouteMux := mux.NewRouter()

	RESTMux.HandleFunc("/update/{key}", a.handlers.UpdateHandler)
	RESTMux.HandleFunc("/status/", a.handlers.StatusHandler)
	RESTMux.HandleFunc("/realstatus/", a.handlers.RealStatusHandler)
	RESTMux.HandleFunc("/metrics/", a.handlers.MetricsHandler)
	RESTMux.Handle("/", websocket.Handler(a.handlers.PushSocketHandler))

	RouteMux.HandleFunc("/route/{uaid}", a.handlers.RouteHandler)

	a.log.Info("app", fmt.Sprintf("listening on %s:%s", a.host, a.port), nil)

	// Weigh the anchor!
	go func() {
		addr := a.host + ":" + string(a.port)
		if len(a.sslCertFile) > 0 && len(a.sslKeyFile) > 0 {
			a.log.Info("main", "Using TLS", nil)
			errChan <- http.ListenAndServeTLS(addr, a.sslCertFile, a.sslKeyFile, RESTMux)
		} else {
			errChan <- http.ListenAndServe(addr, RESTMux)
		}
	}()

	go func() {
		addr := ":" + string(a.router.port)
		a.log.Info("main", "Starting Router", LogFields{"port": addr})
		errChan <- http.ListenAndServe(addr, RouteMux)
	}()

	return errChan
}

func (a *Application) Hostname() string {
	return a.hostname
}

func (a *Application) FullHostname() string {
	return a.fullHostname
}

func (a *Application) MaxConnections() int {
	return a.maxConnnections
}

func (a *Application) Logger() *SimpleLogger {
	return a.log
}

func (a *Application) Storage() *Storage {
	return a.storage
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
	a.clientMux.RLock()
	count = len(a.clients)
	a.clientMux.RUnlock()
	return
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
}

func (a *Application) RemoveClient(uaid string) {
	a.clientMux.Lock()
	delete(a.clients, uaid)
	a.clientMux.Unlock()
}
