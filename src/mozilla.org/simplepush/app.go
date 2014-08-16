/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"code.google.com/p/go.net/websocket"
	"github.com/gorilla/mux"
)

type ApplicationConfig struct {
	Hostname           string `toml:"current_host"`
	Host               string
	Port               int
	TokenKey           string `toml:"token_key"`
	MaxConnections     int    `toml:"max_connections"`
	UseAwsHost         bool   `toml:"use_aws_host"`
	SslCertFile        string `toml:"ssl_cert_file"`
	SslKeyFile         string `toml:"ssl_key_file"`
	ClientMinPing      string `toml:"client_min_ping_interval"`
	ClientHelloTimeout string `toml:"client_hello_timeout"`
	PushLongPongs      bool   `toml:"push_long_pongs"`
	Gcm                GCMConfig
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
	clientCount        *int32
	server             *Serv
	storage            *Storage
	router             *Router
	handlers           *Handler
	propping           *PropPing
}

func (a *Application) ConfigStruct() interface{} {
	defaultHost, _ := os.Hostname()
	return &ApplicationConfig{
		Hostname:           defaultHost,
		Host:               "0.0.0.0",
		Port:               8080,
		MaxConnections:     1000,
		UseAwsHost:         false,
		ClientMinPing:      "20s",
		ClientHelloTimeout: "30s",
	}
}

// Fully initialize the application, this initializes all the other components
// as well.
// Note: We implement the Init method to comply with the interface, so the app
// passed here will be nil.
func (a *Application) Init(app *Application, config interface{}) (err error) {
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

	usingSSL := len(conf.SslCertFile) > 0 && len(conf.SslKeyFile) > 0
	if usingSSL && conf.Port == 443 {
		a.fullHostname = fmt.Sprintf("https://%s", a.hostname)
	} else if usingSSL {
		a.fullHostname = fmt.Sprintf("https://%s:%d", a.hostname, conf.Port)
	} else if conf.Port == 80 {
		a.fullHostname = fmt.Sprintf("http://%s", a.hostname)
	} else {
		a.fullHostname = fmt.Sprintf("http://%s:%d", a.hostname, conf.Port)
	}

	a.host = conf.Host
	a.port = conf.Port
	if a.clientMinPing, err = time.ParseDuration(conf.ClientMinPing); err != nil {
		err = fmt.Errorf("Unable to parse 'client_min_ping_interval: %s",
			err.Error())
		return
	}
	if a.clientHelloTimeout, err = time.ParseDuration(conf.ClientHelloTimeout); err != nil {
		err = fmt.Errorf("Unable to parse 'client_hello_timeout: %s",
			err.Error())
		return
	}
	a.pushLongPongs = conf.PushLongPongs
	a.sslCertFile = conf.SslCertFile
	a.sslKeyFile = conf.SslKeyFile
	a.maxConnnections = conf.MaxConnections
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

func (a *Application) SetPropPing(ping IPropPing) (err error) {
	a.propping, err = NewPropPing(ping)
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

	a.log.Info("app", fmt.Sprintf("listening on %s:%d", a.host, a.port), nil)

	// Weigh the anchor!
	go func() {
		addr := a.host + ":" + strconv.Itoa(a.port)
		if len(a.sslCertFile) > 0 && len(a.sslKeyFile) > 0 {
			a.log.Info("app", "Using TLS", nil)
			errChan <- http.ListenAndServeTLS(addr, a.sslCertFile, a.sslKeyFile, RESTMux)
		} else {
			errChan <- http.ListenAndServe(addr, RESTMux)
		}
	}()

	go func() {
		addr := ":" + strconv.Itoa(a.router.port)
		a.log.Info("app", "Starting Router", LogFields{"port": addr})
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

//TODO: move these to handler so we can deal with multiple prop.ping formats
func (a *Application) PropPing() *PropPing {
	return a.propping
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
	a.clientMux.Lock()
	delete(a.clients, uaid)
	a.clientMux.Unlock()
	atomic.AddInt32(a.clientCount, -1)
}
