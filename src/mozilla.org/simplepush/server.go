/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// -- SERVER this handles REST requests and coordinates between connected
// clients (e.g. wakes client when data is ready, potentially issues remote
// wake command to client, etc.)

type Client struct {
	// client descriptor info.
	Worker *Worker
	PushWS PushWS    `json:"-"`
	UAID   string    `json:"uaid"`
	Prop   *PropPing `json:"-"`
}

// Basic global server options
type ServerConfig struct {
	PushEndpoint       string `toml:"push_endpoint"`
	PushLongPongs      int    `toml:"push_long_pongs"`
	ClientMinPing      string `toml:"client_min_ping"`
	ClientHelloTimeout string `toml:"client_hello_timeout"`
}

type Serv struct {
	app          *Application
	logger       *SimpleLogger
	metrics      *Metrics
	store        Store
	key          []byte
	pushEndpoint string
}

func (self *Serv) ConfigStruct() interface{} {
	return &ServerConfig{
		PushEndpoint: "<current_host>/update/<token>",
	}
}

func (self *Serv) Init(app *Application, config interface{}) (err error) {
	conf := config.(*ServerConfig)
	self.app = app
	self.logger = app.Logger()
	self.metrics = app.Metrics()
	self.store = app.Store()
	self.key = app.TokenKey()
	self.pushEndpoint = conf.PushEndpoint
	return
}

// A client connects!
func (self *Serv) Hello(worker *Worker, cmd PushCommand, sock *PushWS) (result int, arguments JsMap) {
	var uaid string
	var prop *PropPing
	var err error

	args := cmd.Arguments.(JsMap)
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
		uaid, _ = GenUUID4()
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

	// build the connect string from legacy elements
	if _, ok := args["connect"]; !ok {
		ip, iok := args["ip"]
		port, pok := args["port"]
		if iok && pok {
			args["connect"] = JsMap{
				"type": "udp",
				"port": port,
				"ip":   ip,
			}
		}
	}

	if connect, ok := args["connect"]; ok && connect != nil {
		// Currently marshalling to deal with the interface{} issues.
		cs, _ := json.Marshal(connect)
		fmt.Printf("Prop Ping %s\n\n", connect)
		prop, err = NewPropPing(string(cs), uaid, self.app)

		if err != nil {
			self.logger.Warn("server", "Could not set proprietary info",
				LogFields{"error": err.Error(),
					"connect": string(cs)})
		} else if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("server", "Proprietary Info",
				LogFields{"connect": string(cs)})
		}
	}

	// Create a new, live client entry for this record.
	// See Bye for discussion of potential longer term storage of this info
	sock.Uaid = uaid
	client := &Client{
		Worker: worker,
		PushWS: *sock,
		UAID:   uaid,
		Prop:   prop,
	}
	self.app.AddClient(uaid, client)
	self.logger.Info("dash", "Client registered", nil)
	self.metrics.Increment("updates.client.connect")

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
	uaid := sock.Uaid
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("server", "Cleaning up socket",
			LogFields{"uaid": uaid})
	}
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Socket connection terminated",
			LogFields{
				"uaid":     uaid,
				"duration": strconv.FormatInt(time.Now().Sub(sock.Born).Nanoseconds(), 10)})
	}
	self.metrics.Timer("socket.lifespan",
		time.Now().Unix()-sock.Born.Unix())
	self.app.RemoveClient(uaid)
	self.metrics.Increment("updates.client.disconnect")
}

func (self *Serv) Unreg(cmd PushCommand, sock *PushWS) (result int, arguments JsMap) {
	// This is effectively a no-op, since we don't hold client session info
	args := cmd.Arguments.(JsMap)
	args["status"] = 200
	return 200, args
}

func (self *Serv) Regis(cmd PushCommand, sock *PushWS) (result int, arguments JsMap) {
	// A semi-no-op, since we don't care about the appid, but we do want
	// to create a valid endpoint.
	var err error
	args := cmd.Arguments.(JsMap)
	args["status"] = 200
	var endPoint string
	endPoint = self.pushEndpoint
	// Generate the call back URL
	token, ok := self.store.IDsToKey(sock.Uaid, args["channelID"].(string))
	if !ok {
		return 500, nil
	}
	// if there is a key, encrypt the token
	if len(self.key) != 0 {
		btoken := []byte(token)
		token, err = Encode(self.key, btoken)
		if err != nil {
			self.logger.Error("server", "Token Encoding error",
				LogFields{"uaid": sock.Uaid,
					"channelID": args["channelID"].(string)})
			return 500, nil
		}

	}
	// cheezy variable replacement.
	endPoint = strings.Replace(endPoint, "<token>", token, -1)
	endPoint = strings.Replace(endPoint, "<current_host>", self.app.FullHostname(), -1)
	args["push.endpoint"] = endPoint
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("server",
			"Generated Endpoint",
			LogFields{"uaid": sock.Uaid,
				"channelID": args["channelID"].(string),
				"token":     token,
				"endpoint":  endPoint})
	}
	return 200, args
}

func (self *Serv) RequestFlush(client *Client, channel string, version int64) (err error) {
	defer func(client *Client, version int64) {
		r := recover()
		if r != nil {
			self.logger.Error("server",
				"requestFlush failed",
				LogFields{"error": r.(error).Error(),
					"uaid": client.UAID})
			debug.PrintStack()
			if client != nil {
				client.Prop.Send(version)
			}
		}
		return
	}(client, version)

	if client != nil {
		if self.logger.ShouldLog(INFO) {
			self.logger.Info("server",
				"Requesting flush",
				LogFields{"uaid": client.UAID,
					"channel": channel,
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

	err = self.store.Update(pk, vers)
	if err != nil {
		reason = "Failed to update channel"
		goto updateError
	}

	err = self.RequestFlush(client, chid, vers)
	if err == nil {
		return
	}
	reason = "Failed to flush"

updateError:
	self.logger.Error("server", reason,
		LogFields{"error": err.Error(),
			"UID":  uid,
			"CHID": chid,
		})
	return err
}

func (self *Serv) HandleCommand(cmd PushCommand, sock *PushWS) (result int, args JsMap) {
	var ret JsMap
	if cmd.Arguments != nil {
		args = cmd.Arguments.(JsMap)
	} else {
		args = make(JsMap)
	}

	switch int(cmd.Command) {
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

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
