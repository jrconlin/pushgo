/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"os"
	"sync"
)

type ApplicationConfig struct {
	hostname       string `toml:"current_host"`
	host           string
	port           int
	maxConnections int    `toml:"max_connections"`
	useAwsHost     bool   `toml:"use_aws_host"`
	sslCertFile    string `toml:"ssl_cert_file"`
	sslKeyFile     string `toml:"ssl_key_file"`
}

type Application struct {
	hostname        string
	host            string
	port            int
	maxConnnections int
	log             *SimpleLogger
	metrics         *Metrics
	clients         map[string]*Client
	clientMux       *sync.RWMutex
	server          *Serv
	storage         *Storage
	router          *Router
}

func (a *Application) ConfigStruct() interface{} {
	defaultHost := os.Hostname()
	return &ApplicationConfig{
		hostname:       defaultHost,
		host:           "0.0.0.0",
		port:           8080,
		maxConnections: 1000,
		useAwsHost:     false,
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

	a.host = conf.host
	a.port = conf.port
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

// Start the application
func (a *Application) Run() error {}

func (a *Application) Hostname() string {
	return a.hostname
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
