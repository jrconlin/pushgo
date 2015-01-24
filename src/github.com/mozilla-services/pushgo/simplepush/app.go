/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"text/template"
	"time"
)

// The Simple Push server version.
const VERSION = "1.5.0"

var (
	ErrMissingOrigin = errors.New("Missing WebSocket origin")
	ErrInvalidOrigin = errors.New("WebSocket origin not allowed")
)

type ApplicationConfig struct {
	Hostname           string `toml:"current_host" env:"current_host"`
	TokenKey           string `toml:"token_key" env:"token_key"`
	PushEndpoint       string `toml:"push_endpoint_template" env:"push_endpoint_template"`
	UseAwsHost         bool   `toml:"use_aws_host" env:"use_aws_host"`
	ResolveHost        bool   `toml:"resolve_host" env:"resolve_host"`
	ClientMinPing      string `toml:"client_min_ping_interval" env:"client_min_ping_interval"`
	ClientHelloTimeout string `toml:"client_hello_timeout" env:"client_hello_timeout"`
	PushLongPongs      bool   `toml:"push_long_pongs" env:"push_long_pongs"`
	ClientPongInterval string `toml:"client_pong_interval" env:"client_pong_interval"`
}

func NewApplication() (a *Application) {
	a = &Application{
		workers:   make(map[string]Worker),
		closeChan: make(chan bool),
	}
	return a
}

type Application struct {
	info               InstanceInfo
	hostname           string
	host               string
	port               int
	clientMinPing      time.Duration
	clientHelloTimeout time.Duration
	clientPongInterval time.Duration
	pushLongPongs      bool
	tokenKey           []byte
	endpointTemplate   *template.Template
	log                *SimpleLogger
	metrics            Statistician
	workers            map[string]Worker
	workerMux          sync.RWMutex
	workerCount        int32
	store              Store
	router             Router
	locator            Locator
	balancer           Balancer
	sh                 Handler // WebSocket handler.
	eh                 Handler // HTTP update handler.
	ph                 Handler // Performance profiling handlers.
	propping           PropPinger
	closeChan          chan bool
	closeOnce          Once
}

func (a *Application) ConfigStruct() interface{} {
	defaultHost, _ := os.Hostname()
	return &ApplicationConfig{
		Hostname:           defaultHost,
		PushEndpoint:       "{{.CurrentHost}}/update/{{.Token}}",
		UseAwsHost:         false,
		ResolveHost:        false,
		ClientMinPing:      "20s",
		ClientHelloTimeout: "30s",
		ClientPongInterval: "5m",
	}
}

// Fully initialize the application, this initializes all the other components
// as well.
// Note: We implement the Init method to comply with the interface, so the app
// passed here will be nil.
func (a *Application) Init(_ *Application, config interface{}) (err error) {
	conf := config.(*ApplicationConfig)

	if conf.UseAwsHost {
		a.info = new(EC2Info)
	} else if conf.ResolveHost {
		addr, err := net.ResolveIPAddr("ip", conf.Hostname)
		if err != nil {
			return fmt.Errorf("Error resolving hostname: %s", err)
		}
		a.info = LocalInfo{addr.String()}
	} else {
		a.info = LocalInfo{conf.Hostname}
	}
	if a.hostname, err = a.info.PublicHostname(); err != nil {
		return fmt.Errorf("Error determining hostname: %s", err)
	}

	if err = a.SetTokenKey(conf.TokenKey); err != nil {
		return fmt.Errorf("Malformed token key: %s", err)
	}
	if a.endpointTemplate, err = template.New("Push").Parse(conf.PushEndpoint); err != nil {
		return fmt.Errorf("Error parsing push endpoint template: %s", err)
	}

	if a.clientMinPing, err = time.ParseDuration(conf.ClientMinPing); err != nil {
		return fmt.Errorf("Unable to parse 'client_min_ping_interval': %s",
			err.Error())
	}
	if a.clientPongInterval, err = time.ParseDuration(conf.ClientPongInterval); err != nil {
		return fmt.Errorf("Unable to parse 'client_pong_interval': %s",
			err.Error())
	}
	if a.clientHelloTimeout, err = time.ParseDuration(conf.ClientHelloTimeout); err != nil {
		return fmt.Errorf("Unable to parse 'client_hello_timeout': %s",
			err.Error())
	}
	a.pushLongPongs = conf.PushLongPongs
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

func (a *Application) SetMetrics(metrics Statistician) error {
	a.metrics = metrics
	return nil
}

func (a *Application) SetStore(store Store) error {
	a.store = store
	return nil
}

func (a *Application) SetRouter(router Router) error {
	a.router = router
	return nil
}

func (a *Application) SetLocator(locator Locator) error {
	a.locator = locator
	return nil
}

func (a *Application) SetBalancer(b Balancer) error {
	a.balancer = b
	return nil
}

func (a *Application) SetSocketHandler(h Handler) error {
	a.sh = h
	return nil
}

func (a *Application) SetEndpointHandler(h Handler) error {
	a.eh = h
	return nil
}

func (a *Application) SetProfileHandlers(h Handler) error {
	a.ph = h
	return nil
}

// Start the application
func (a *Application) Run() (errChan chan error) {
	errChan = make(chan error, 4)

	go a.sh.Start(errChan)
	go a.eh.Start(errChan)
	go a.router.Start(errChan)
	go a.ph.Start(errChan)

	go a.sendClientCount()
	return errChan
}

func (a *Application) Hostname() string {
	return a.hostname
}

func (a *Application) InstanceInfo() InstanceInfo {
	return a.info
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

func (a *Application) Metrics() Statistician {
	return a.metrics
}

func (a *Application) Router() Router {
	return a.router
}

func (a *Application) Locator() Locator {
	return a.locator
}

func (a *Application) Balancer() Balancer {
	return a.balancer
}

func (a *Application) SocketHandler() Handler {
	return a.sh
}

func (a *Application) EndpointHandler() Handler {
	return a.eh
}

func (a *Application) ProfileHandlers() Handler {
	return a.ph
}

func (a *Application) TokenKey() []byte {
	return a.tokenKey
}

func (a *Application) SetTokenKey(key string) (err error) {
	if len(key) == 0 {
		a.tokenKey = nil
	} else {
		a.tokenKey, err = base64.URLEncoding.DecodeString(key)
	}
	return
}

func (a *Application) WorkerCount() (count int) {
	return int(atomic.LoadInt32(&a.workerCount))
}

func (a *Application) WorkerExists(uaid string) (collision bool) {
	_, collision = a.GetWorker(uaid)
	return
}

func (a *Application) GetWorker(uaid string) (worker Worker, ok bool) {
	a.workerMux.RLock()
	worker, ok = a.workers[uaid]
	a.workerMux.RUnlock()
	return
}

func (a *Application) AddWorker(uaid string, worker Worker) (replaced bool) {
	if a.closeOnce.IsDone() {
		worker.Close()
		return
	}
	a.workerMux.Lock()
	// Avoid incrementing the worker count for duplicate handshakes. Callers
	// can use this to short-circuit other operations (e.g., re-registering
	// with the router).
	_, replaced = a.workers[uaid]
	a.workers[uaid] = worker
	a.workerMux.Unlock()
	if !replaced {
		atomic.AddInt32(&a.workerCount, 1)
	}
	return
}

func (a *Application) RemoveWorker(uaid string, worker Worker) (removed bool) {
	if a.closeOnce.IsDone() {
		return
	}
	a.workerMux.Lock()
	if prevWorker, ok := a.workers[uaid]; ok && prevWorker == worker {
		delete(a.workers, uaid)
		removed = true
	}
	a.workerMux.Unlock()
	if removed {
		atomic.AddInt32(&a.workerCount, -1)
	}
	return removed
}

func (a *Application) closeWorkers() {
	a.workerMux.Lock()
	defer a.workerMux.Unlock()
	for uaid, worker := range a.workers {
		delete(a.workers, uaid)
		worker.Close()
	}
}

// CreateEndpoint allocates an update endpoint with the given primary key.
func (a *Application) CreateEndpoint(key string) (string, error) {
	token, err := a.encodePK(key)
	if err != nil {
		return "", err
	}
	return a.genEndpoint(token)
}

// encodePK encodes a primary key if a token key is specified.
func (a *Application) encodePK(key string) (token string, err error) {
	tokenKey := a.TokenKey()
	if len(tokenKey) == 0 {
		return key, nil
	}
	btoken := []byte(key)
	return Encode(tokenKey, btoken)
}

// genEndpoint generates an update endpoint.
func (a *Application) genEndpoint(token string) (string, error) {
	var currentHost string
	if eh := a.EndpointHandler(); eh != nil {
		currentHost = eh.URL()
	}
	// cheezy variable replacement.
	endpoint := new(bytes.Buffer)
	if err := a.endpointTemplate.Execute(endpoint, struct {
		Token       string
		CurrentHost string
	}{
		token,
		currentHost,
	}); err != nil {
		return "", err
	}
	return endpoint.String(), nil
}

func (a *Application) Close() error {
	return a.closeOnce.Do(a.close)
}

func (a *Application) close() error {
	var errors MultipleError
	if eh := a.EndpointHandler(); eh != nil {
		// Stop the update listener; close all connections.
		if err := eh.Close(); err != nil {
			errors = append(errors, err)
		}
	}
	if b := a.Balancer(); b != nil {
		// Deregister from the balancer.
		if err := b.Close(); err != nil {
			errors = append(errors, err)
		}
	}
	if sh := a.SocketHandler(); sh != nil {
		// Close the WebSocket listener.
		if err := sh.Close(); err != nil {
			errors = append(errors, err)
		}
	}
	// Disconnect existing clients.
	a.closeWorkers()
	// Stop publishing client counts.
	close(a.closeChan)
	if l := a.Locator(); l != nil {
		// Deregister from the discovery service.
		if err := a.locator.Close(); err != nil {
			errors = append(errors, err)
		}
	}
	if r := a.Router(); r != nil {
		// Close the routing listener.
		if err := r.Close(); err != nil {
			errors = append(errors, err)
		}
	}
	if ph := a.ProfileHandlers(); ph != nil {
		// Stop the profiling listener.
		if err := ph.Close(); err != nil {
			errors = append(errors, err)
		}
	}
	if s := a.Store(); s != nil {
		// Close database connections.
		if err := s.Close(); err != nil {
			errors = append(errors, err)
		}
	}
	if len(errors) > 0 {
		return errors
	}
	return nil
}

func (a *Application) sendClientCount() {
	ticker := time.NewTicker(1 * time.Second)
	for ok := true; ok; {
		select {
		case ok = <-a.closeChan:
		case <-ticker.C:
			a.Metrics().Gauge("update.client.connections", int64(a.WorkerCount()))
		}
	}
	ticker.Stop()
}
