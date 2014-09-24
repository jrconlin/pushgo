/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"errors"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/mozilla-services/pushgo/id"
)

// -- SERVER this handles REST requests and coordinates between connected
// clients (e.g. wakes client when data is ready, potentially issues remote
// wake command to client, etc.)

type Client struct {
	// client descriptor info.
	Worker *Worker
	PushWS PushWS `json:"-"`
	UAID   string `json:"uaid"`
}

// Basic global server options
type ServerConfig struct {
	PushEndpoint string         `toml:"push_endpoint_template" env:"push_url_template"`
	Client       ListenerConfig `toml:"websocket" env:"ws"`
	Endpoint     ListenerConfig
}

type ListenerConfig struct {
	Addr            string
	MaxConns        int    `toml:"max_connections" env:"max_conns"`
	KeepAlivePeriod string `toml:"tcp_keep_alive" env:"keep_alive"`
	CertFile        string `toml:"cert_file" env:"cert"`
	KeyFile         string `toml:"key_file" env:"key"`
}

func (conf *ListenerConfig) UseTLS() bool {
	return len(conf.CertFile) > 0 && len(conf.KeyFile) > 0
}

func (conf *ListenerConfig) Listen() (ln net.Listener, err error) {
	keepAlivePeriod, err := time.ParseDuration(conf.KeepAlivePeriod)
	if err != nil {
		return nil, err
	}
	if conf.UseTLS() {
		return ListenTLS(conf.Addr, conf.CertFile, conf.KeyFile, conf.MaxConns, keepAlivePeriod)
	}
	return Listen(conf.Addr, conf.MaxConns, keepAlivePeriod)
}

func NewServer() *Serv {
	return &Serv{
		closeSignal: make(chan bool),
	}
}

type Serv struct {
	app              *Application
	logger           *SimpleLogger
	hostname         string
	clientLn         net.Listener
	clientURL        string
	maxClientConns   int
	endpointLn       net.Listener
	endpointURL      string
	maxEndpointConns int
	metrics          *Metrics
	store            Store
	key              []byte
	template         *template.Template
	prop             PropPinger
	isClosing        bool
	closeSignal      chan bool
	closeLock        sync.Mutex
}

func (self *Serv) ConfigStruct() interface{} {
	return &ServerConfig{
		PushEndpoint: "{{.CurrentHost}}/update/{{.Token}}",
		Client: ListenerConfig{
			Addr:            ":8080",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
		Endpoint: ListenerConfig{
			Addr:            ":8081",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
	}
}

func (self *Serv) Init(app *Application, config interface{}) (err error) {
	conf := config.(*ServerConfig)

	self.app = app
	self.logger = app.Logger()
	self.metrics = app.Metrics()
	self.store = app.Store()
	self.prop = app.PropPinger()
	self.key = app.TokenKey()
	self.hostname = app.Hostname()

	if self.template, err = template.New("Push").Parse(conf.PushEndpoint); err != nil {
		self.logger.Alert("server", "Could not parse push endpoint template",
			LogFields{"error": err.Error()})
		return err
	}

	if self.clientLn, err = conf.Client.Listen(); err != nil {
		self.logger.Alert("server", "Could not attach WebSocket listener",
			LogFields{"error": err.Error()})
		return err
	}
	var scheme string
	if conf.Client.UseTLS() {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	host, port := self.hostPort(self.clientLn)
	self.clientURL = CanonicalURL(scheme, host, port)
	self.maxClientConns = conf.Client.MaxConns

	if self.endpointLn, err = conf.Endpoint.Listen(); err != nil {
		self.logger.Alert("server", "Could not attach update listener",
			LogFields{"error": err.Error()})
		return err
	}
	if conf.Endpoint.UseTLS() {
		scheme = "https"
	} else {
		scheme = "http"
	}
	host, port = self.hostPort(self.endpointLn)
	self.endpointURL = CanonicalURL(scheme, host, port)
	self.maxEndpointConns = conf.Endpoint.MaxConns

	go self.sendClientCount()
	return nil
}

func (self *Serv) ClientListener() net.Listener {
	return self.clientLn
}

func (self *Serv) ClientURL() string {
	return self.clientURL
}

func (self *Serv) MaxClientConns() int {
	return self.maxClientConns
}

func (self *Serv) EndpointListener() net.Listener {
	return self.endpointLn
}

func (self *Serv) EndpointURL() string {
	return self.endpointURL
}

func (self *Serv) MaxEndpointConns() int {
	return self.maxEndpointConns
}

func (self *Serv) hostPort(ln net.Listener) (host string, port int) {
	addr := ln.Addr().(*net.TCPAddr)
	if host = self.hostname; len(host) == 0 {
		host = addr.IP.String()
	}
	return host, addr.Port
}

// A client connects!
func (self *Serv) Hello(worker *Worker, cmd PushCommand, sock *PushWS) (result int, arguments JsMap) {
	var uaid string

	args := cmd.Arguments
	if self.logger.ShouldLog(INFO) {
		chidss := ""
		if chids, ok := args["channelIDs"]; ok {
			chidss = "[" + strings.Join(chids.([]string), ", ") + "]"
		}
		self.logger.Info("server", "handling 'hello'",
			LogFields{"uaid": args["uaid"].(string),
				"channelIDs": chidss})
	}

	// TODO: If the client needs to connect to a different server,
	// Look up the appropriate server (based on UAID)
	// return a response that looks like:
	// { uaid: UAIDValue, status: 302, redirect: NewWS_URL }

	// New connects overwrite previous connections.
	// Raw client
	if args["uaid"] == "" {
		uaid, _ = id.Generate()
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("server",
				"Generating new UAID",
				LogFields{"uaid": uaid})
		}
	} else {
		uaid = args["uaid"].(string)
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("server",
				"Using existing UAID",
				LogFields{"uaid": uaid})
		}
		delete(args, "uaid")
	}

	if connect, _ := args["connect"].([]byte); len(connect) > 0 && self.prop != nil {
		if err := self.prop.Register(uaid, connect); err != nil {
			if self.logger.ShouldLog(WARNING) {
				self.logger.Warn("server", "Could not set proprietary info",
					LogFields{"error": err.Error(),
						"connect": string(connect)})
			}
		}
	}

	// Create a new, live client entry for this record.
	// See Bye for discussion of potential longer term storage of this info
	sock.Uaid = uaid
	client := &Client{
		Worker: worker,
		PushWS: *sock,
		UAID:   uaid,
	}
	self.app.AddClient(uaid, client)
	self.logger.Info("dash", "Client registered", nil)

	// We don't register the list of known ChannelIDs since we echo
	// back any ChannelIDs sent on behalf of this UAID.
	args["uaid"] = uaid
	arguments = args
	result = 200
	return result, arguments
}

func (self *Serv) Bye(sock *PushWS) {
	// Remove the UAID as a registered listener.
	// NOTE: in instances where proprietary wake-ups are issued, you may
	// wish not to delete the record from Clients, since this is the only
	// way to note a record needs waking.
	//
	// For that matter, you may wish to store the Proprietary wake data to
	// something commonly shared (like memcache) so that the device can be
	// woken when not connected.
	now := time.Now()
	uaid := sock.Uaid
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("server", "Cleaning up socket",
			LogFields{"uaid": uaid})
	}
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Socket connection terminated",
			LogFields{
				"uaid":     uaid,
				"duration": strconv.FormatInt(int64(now.Sub(sock.Born)), 10)})
	}
	self.app.RemoveClient(uaid)
}

func (self *Serv) Unreg(cmd PushCommand, sock *PushWS) (result int, arguments JsMap) {
	// This is effectively a no-op, since we don't hold client session info
	args := cmd.Arguments
	args["status"] = 200
	return 200, args
}

func (self *Serv) Regis(cmd PushCommand, sock *PushWS) (result int, arguments JsMap) {
	// A semi-no-op, since we don't care about the appid, but we do want
	// to create a valid endpoint.
	var err error
	args := cmd.Arguments
	args["status"] = 200
	// Generate the call back URL
	chid, _ := args["channelID"].(string)
	token, ok := self.store.IDsToKey(sock.Uaid, chid)
	if !ok {
		return 500, nil
	}
	// if there is a key, encrypt the token
	if len(self.key) != 0 {
		btoken := []byte(token)
		if token, err = Encode(self.key, btoken); err != nil {
			if self.logger.ShouldLog(ERROR) {
				self.logger.Error("server", "Token Encoding error",
					LogFields{"uaid": sock.Uaid,
						"channelID": chid})
			}
			return 500, nil
		}

	}

	// cheezy variable replacement.
	endpoint := new(bytes.Buffer)
	if err = self.template.Execute(endpoint, struct {
		Token       string
		CurrentHost string
	}{
		token,
		self.EndpointURL(),
	}); err != nil {
		if self.logger.ShouldLog(ERROR) {
			self.logger.Error("server",
				"Could not generate Push Endpoint",
				LogFields{"error": err.Error()})
		}
		return 500, nil
	}
	args["push.endpoint"] = endpoint.String()
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("server",
			"Generated Push Endpoint",
			LogFields{"uaid": sock.Uaid,
				"channelID": chid,
				"token":     token,
				"endpoint":  args["push.endpoint"].(string)})
	}
	return 200, args
}

func (self *Serv) RequestFlush(client *Client, channel string, version int64) (err error) {
	defer func(client *Client, version int64) {
		r := recover()
		if r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				self.logger.Error("server",
					"requestFlush failed",
					LogFields{"error": r.(error).Error(),
						"uaid": client.UAID})
			}
			debug.PrintStack()
			if client != nil && self.prop != nil {
				self.prop.Send(client.UAID, version)
			}
		}
		return
	}(client, version)

	if client != nil {
		if self.logger.ShouldLog(INFO) {
			self.logger.Info("server",
				"Requesting flush",
				LogFields{"uaid": client.UAID,
					"chid":    channel,
					"version": strconv.FormatInt(version, 10)})
		}

		// Attempt to send the command
		client.Worker.Flush(&client.PushWS, 0, channel, version)
	}
	return nil
}

func (self *Serv) Purge(cmd PushCommand, sock *PushWS) (result int, arguments JsMap) {
	result = 200
	return
}

func (self *Serv) Update(chid, uid string, vers int64, time time.Time) (err error) {
	var pk string
	updateErr := errors.New("Update Error")
	reason := "Unknown UID"

	client, ok := self.app.GetClient(uid)
	if !ok {
		err = updateErr
		goto updateError
	}

	pk, ok = self.store.IDsToKey(uid, chid)
	if !ok {
		reason = "Failed to generate PK"
		goto updateError
	}

	if err = self.store.Update(pk, vers); err != nil {
		reason = "Failed to update channel"
		goto updateError
	}

	if err = self.RequestFlush(client, chid, vers); err == nil {
		return
	}
	reason = "Failed to flush"

updateError:
	if self.logger.ShouldLog(ERROR) {
		self.logger.Error("server", reason,
			LogFields{"error": err.Error(),
				"uaid": uid,
				"chid": chid})
	}
	return err
}

func (self *Serv) HandleCommand(cmd PushCommand, sock *PushWS) (result int, args JsMap) {
	var ret JsMap
	if cmd.Arguments != nil {
		args = cmd.Arguments
	} else {
		args = make(JsMap)
	}

	switch cmd.Command {
	case HELLO:
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("server", "Handling HELLO event", nil)
		}
		worker := args["worker"].(*Worker)
		result, ret = self.Hello(worker, cmd, sock)
	case UNREG:
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("server", "Handling UNREG event", nil)
		}
		result, ret = self.Unreg(cmd, sock)
	case REGIS:
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("server", "Handling REGIS event", nil)
		}
		result, ret = self.Regis(cmd, sock)
	case DIE:
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("server", "Cleanup", nil)
		}
		self.Bye(sock)
		return 0, nil
	case PURGE:
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("Server", "Purge", nil)
		}
		self.Purge(cmd, sock)
	}

	args["uaid"] = ret["uaid"]
	return result, args
}

func (self *Serv) Close() error {
	defer self.closeLock.Unlock()
	self.closeLock.Lock()
	if self.isClosing {
		return nil
	}
	self.isClosing = true
	close(self.closeSignal)
	self.clientLn.Close()
	self.endpointLn.Close()
	return nil
}

func (self *Serv) sendClientCount() {
	ticker := time.NewTicker(1 * time.Second)
	for ok := true; ok; {
		select {
		case ok = <-self.closeSignal:
		case <-ticker.C:
			self.metrics.Gauge("update.client.connections", int64(self.app.ClientCount()))
		}
	}
	ticker.Stop()
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
