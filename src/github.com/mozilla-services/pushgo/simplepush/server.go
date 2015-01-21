/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"errors"
	"runtime"
	"strconv"
	"text/template"
	"time"
)

// -- SERVER this handles REST requests and coordinates between connected
// clients (e.g. wakes client when data is ready, potentially issues remote
// wake command to client, etc.)

// Basic global server options
type ServerConfig struct {
	PushEndpoint string `toml:"push_endpoint_template" env:"push_endpoint_template"`
}

// Server responds to client commands and delivers updates.
type Server interface {
	// RequestFlush sends an update containing chid, vers, and data to worker. If
	// worker is nil, RequestFlush is a no-op. If RequestFlush panics and a
	// proprietary pinger is set, the update will be delivered via the
	// proprietary mechanism.
	RequestFlush(worker Worker, chid string, vers int64, data string) (err error)

	// UpdateWorker updates the storage backend and flushes an update to worker.
	// sentAt indicates when the update was sent by the application server.
	UpdateWorker(worker Worker, chid string, vers int64,
		sentAt time.Time, data string) (err error)

	// Hello adds worker to the worker map and registers the connecting client
	// with the router. If connect is not empty and a proprietary pinger is
	// set, the client will be registered with the pinger.
	Hello(worker Worker, connect []byte) error

	// Regis constructs an endpoint URL for the given channel ID chid. The app
	// server can use this URL to send updates to worker.
	Regis(worker Worker, chid string) (endpoint string, err error)

	// Bye removes worker from the worker map, deregisters the client from the
	// router, and closes the underlying socket. Invoking Bye multiple times for
	// the same worker is a no-op.
	Bye(worker Worker) error
}

func NewServer() *Serv {
	return new(Serv)
}

type Serv struct {
	app      *Application
	logger   *SimpleLogger
	metrics  Statistician
	store    Store
	router   Router
	key      []byte
	template *template.Template
	prop     PropPinger
}

func (self *Serv) ConfigStruct() interface{} {
	return &ServerConfig{
		PushEndpoint: "{{.CurrentHost}}/update/{{.Token}}",
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
	self.router = app.Router()

	if self.template, err = template.New("Push").Parse(conf.PushEndpoint); err != nil {
		self.logger.Panic("server", "Could not parse push endpoint template",
			LogFields{"error": err.Error()})
		return err
	}

	return nil
}

// A client connects!
func (self *Serv) Hello(worker Worker, connect []byte) error {

	uaid := worker.UAID()

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("server", "handling 'hello'",
			LogFields{"uaid": uaid})
	}

	if len(connect) > 0 && self.prop != nil {
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
	self.app.AddWorker(uaid, worker)
	self.router.Register(uaid)
	self.logger.Info("dash", "Client registered", nil)

	// We don't register the list of known ChannelIDs since we echo
	// back any ChannelIDs sent on behalf of this UAID.
	return nil
}

func (self *Serv) Bye(worker Worker) error {
	// Remove the UAID as a registered listener.
	// NOTE: in instances where proprietary wake-ups are issued, you may
	// wish not to delete the record from Clients, since this is the only
	// way to note a record needs waking.
	//
	// For that matter, you may wish to store the Proprietary wake data to
	// something commonly shared (like memcache) so that the device can be
	// woken when not connected.
	now := time.Now()
	uaid := worker.UAID()
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("server", "Cleaning up socket",
			LogFields{"uaid": uaid})
	}
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Socket connection terminated",
			LogFields{
				"uaid":     uaid,
				"duration": strconv.FormatInt(int64(now.Sub(worker.Born())), 10)})
	}
	if removed := self.app.RemoveWorker(uaid, worker); removed {
		self.router.Unregister(uaid)
	}
	return worker.Close()
}

func (self *Serv) Regis(worker Worker, chid string) (endpoint string, err error) {
	// A semi-no-op, since we don't care about the appid, but we do want
	// to create a valid endpoint.
	// Generate the call back URL
	uaid := worker.UAID()
	token, err := self.store.IDsToKey(uaid, chid)
	if err != nil {
		return "", err
	}
	if token, err = self.encodePK(token); err != nil {
		if self.logger.ShouldLog(ERROR) {
			self.logger.Error("server", "Token Encoding error",
				LogFields{"uaid": uaid,
					"channelID": chid})
		}
		return "", err
	}

	if endpoint, err = self.genEndpoint(token); err != nil {
		if self.logger.ShouldLog(ERROR) {
			self.logger.Error("server",
				"Could not generate Push Endpoint",
				LogFields{"error": err.Error()})
		}
		return "", err
	}
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("server",
			"Generated Push Endpoint",
			LogFields{"uaid": uaid,
				"channelID": chid,
				"token":     token,
				"endpoint":  endpoint})
	}
	return endpoint, nil
}

func (self *Serv) encodePK(key string) (token string, err error) {
	if len(self.key) == 0 {
		return key, nil
	}
	// if there is a key, encrypt the token
	btoken := []byte(key)
	return Encode(self.key, btoken)
}

func (self *Serv) genEndpoint(token string) (string, error) {
	// cheezy variable replacement.
	endpoint := new(bytes.Buffer)
	if err := self.template.Execute(endpoint, struct {
		Token       string
		CurrentHost string
	}{
		token,
		self.app.EndpointHandler().URL(),
	}); err != nil {
		return "", err
	}
	return endpoint.String(), nil
}

// RequestFlush implements Server.RequestFlush.
func (self *Serv) RequestFlush(worker Worker, channel string,
	version int64, data string) (err error) {

	var uaid string
	defer func() {
		if r := recover(); r != nil {
			if flushErr, ok := r.(error); ok {
				err = flushErr
			} else {
				err = errors.New("Error requesting flush")
			}
			if self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("server",
					"requestFlush failed",
					LogFields{"error": err.Error(),
						"uaid":  uaid,
						"stack": string(stack[:n])})
			}
			if len(uaid) > 0 && self.prop != nil {
				self.prop.Send(uaid, version, data)
			}
		}
		return
	}()

	if worker != nil {
		uaid = worker.UAID()
		if self.logger.ShouldLog(INFO) {
			self.logger.Info("server",
				"Requesting flush",
				LogFields{"uaid": uaid,
					"chid":    channel,
					"version": strconv.FormatInt(version, 10),
					"data":    data,
				})
		}

		// Attempt to send the command
		return worker.Flush(0, channel, version, data)
	}
	return nil
}

// UpdateWorker implements Server.UpdateWorker.
func (self *Serv) UpdateWorker(worker Worker, chid string,
	vers int64, time time.Time, data string) (err error) {

	if worker == nil {
		return nil
	}
	uaid := worker.UAID()
	var reason string
	if err = self.store.Update(uaid, chid, vers); err != nil {
		reason = "Failed to update channel"
		goto updateError
	}

	if err = self.RequestFlush(worker, chid, vers, data); err != nil {
		reason = "Failed to flush"
		goto updateError
	}
	return nil

updateError:
	if self.logger.ShouldLog(ERROR) {
		self.logger.Error("server", reason,
			LogFields{"error": err.Error(),
				"uaid": uaid,
				"chid": chid})
	}
	return err
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
