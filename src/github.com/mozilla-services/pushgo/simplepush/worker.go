/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"code.google.com/p/go.net/websocket"

	"github.com/mozilla-services/pushgo/id"
	"github.com/mozilla-services/pushgo/simplepush/sperrors"
)

var (
	MissingChannelErr = errors.New("Missing channelID")
	BadUAIDErr        = errors.New("Bad UAID")
)

//    -- Workers
//      these write back to the websocket.

type Worker struct {
	app          *Application
	logger       *SimpleLogger
	state        WorkerState
	stopped      bool
	maxChannels  int
	lastPing     time.Time
	pingInt      time.Duration
	metrics      *Metrics
	helloTimeout time.Duration
}

type WorkerState int

const (
	WorkerInactive WorkerState = 0
	WorkerActive               = 1
)

type RegisterReply struct {
	Type      interface{} `json:"messageType"`
	DeviceID  string      `json:"uaid"`
	Status    int         `json:"status"`
	ChannelID string      `json:"channelID"`
	Endpoint  string      `json:"pushEndpoint"`
}

type UnregisterReply struct {
	Type      interface{} `json:"messageType"`
	Status    int         `json:"status"`
	ChannelID string      `json:"channelID"`
}

type FlushReply struct {
	Type    interface{} `json:"messageType"`
	Updates []Update    `json:"updates"`
	Expired []string    `json:"expired"`
}

type PingReply struct {
	Type   interface{} `json:"messageType"`
	Status int         `json:"status"`
}

const CHID_DEFAULT_MAX_NUM = 200

func NewWorker(app *Application) *Worker {
	return &Worker{
		app:          app,
		logger:       app.Logger(),
		metrics:      app.Metrics(),
		state:        WorkerActive,
		stopped:      false,
		pingInt:      app.clientMinPing,
		maxChannels:  app.Store().MaxChannels(),
		helloTimeout: app.clientHelloTimeout,
	}
}

func (self *Worker) sniffer(sock *PushWS) {
	// Sniff the websocket for incoming data.
	// Reading from the websocket is a blocking operation, and we also
	// need to write out when an even occurs. This isolates the incoming
	// reads to a separate go process.
	var (
		socket = sock.Socket
		raw    []byte
		//eofCount    int    = 0
		err         error
		messageType string
	)

	for {
		// declare buffer here so that the struct is cleared between msgs.
		var buffer JsMap = JsMap{}
		raw = raw[:0]
		err = nil

		// Were we told to shut down?
		if self.stopped {
			// Notify the main worker loop in case it didn't see the
			// connection drop
			log.Printf("Stopping %s %dns...", sock.Uaid,
				time.Now().Sub(sock.Born))
			return
		}
		if err = websocket.Message.Receive(socket, &raw); err != nil {
			self.stopped = true
			self.logger.Error("worker",
				"Websocket Error",
				LogFields{"error": ErrStr(err)})
			continue
		}
		if len(raw) <= 0 {
			continue
		}

		//eofCount = 0
		//ignore {} pings for logging purposes.
		if len(raw) > 5 {
			if self.logger.ShouldLog(INFO) {
				self.logger.Info("worker",
					"Socket receive",
					LogFields{"raw": string(raw)})
			}
		}
		if string(raw) == "{}" {
			buffer["messageType"] = "ping"
		} else {
			err := json.Unmarshal(raw, &buffer)
			if err != nil {
				self.logger.Error("worker",
					"Unparsable data", LogFields{"raw": string(raw),
						"error": ErrStr(err)})
				self.stopped = true
				continue
			}
			if len(buffer) == 0 {
				// Empty buffers are "pings"
				buffer["messageType"] = "ping"
			}
		}
		if buffer["messageType"] == "ping" {
			err = self.Ping(sock, buffer)
		} else {
			// process the client commands
			if mt, ok := buffer["messageType"]; !ok {
				if self.logger.ShouldLog(INFO) {
					self.logger.Info("worker", "Invalid message",
						LogFields{"reason": "Missing messageType"})
				}
				self.handleError(sock,
					JsMap{},
					sperrors.UnknownCommandError)
				self.stopped = true
				continue
			} else {
				messageType, _ = mt.(string)
			}
			buffer["messageType"] = strings.ToLower(messageType)
			switch strings.ToLower(messageType) {
			case "hello":
				err = self.Hello(sock, buffer)
			case "ack":
				err = self.Ack(sock, buffer)
			case "register":
				err = self.Register(sock, buffer)
			case "unregister":
				err = self.Unregister(sock, buffer)
			case "ping":
				err = self.Ping(sock, buffer)
			case "purge":
				err = self.Purge(sock, buffer)
			default:
				if self.logger.ShouldLog(WARNING) {
					self.logger.Warn("worker",
						"Bad command",
						LogFields{"messageType": messageType})
				}
				err = sperrors.UnknownCommandError
			}
		}
		if err != nil {
			if self.logger.ShouldLog(DEBUG) {
				self.logger.Debug("worker", "Run returned error",
					LogFields{"error": ErrStr(err)})
			} else {
				log.Printf("sniffer:%s Unknown error occurred %s",
					messageType, ErrStr(err))
			}
			self.handleError(sock, buffer, err)
			self.stopped = true
			continue
		}
	}
}

// standardize the error reporting back to the client.
func (self *Worker) handleError(sock *PushWS, message JsMap, err error) (ret error) {
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("worker", "Sending error",
			LogFields{"error": ErrStr(err)})
	}
	message["status"], message["error"] = sperrors.ErrToStatus(err)
	return websocket.JSON.Send(sock.Socket, message)
}

// General workhorse loop for the websocket handler.
func (self *Worker) Run(sock *PushWS) {
	time.AfterFunc(self.helloTimeout,
		func() {
			if sock.Uaid == "" {
				self.logger.Debug("dash",
					"Worker Idle connection. Closing socket", nil)
				sock.Socket.Close()
			}
		})

	defer func(sock *PushWS) {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil {
				self.logger.Error("worker", ErrStr(err), nil)
			}
			sock.Socket.Close()
		}
		return
	}(sock)

	self.sniffer(sock)
	sock.Socket.Close()

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Run has completed a shut-down", nil)
	}
}

// Associate the UAID for this socket connection (and flush any data that
// may be pending for the connection)
func (self *Worker) Hello(sock *PushWS, buffer interface{}) (err error) {
	// register the UAID
	defer func() {
		if r := recover(); r != nil {
			debug.PrintStack()
			if err, _ := r.(error); err != nil {
				self.logger.Error("worker",
					"Unhandled error",
					LogFields{"cmd": "hello", "error": ErrStr(err)})
			}
			err = sperrors.InvalidDataError
		}
	}()

	//Force the client to re-register all it's clients.
	// This is done by returning a new UAID.
	forceReset := false

	var (
		data JsMap
		ok   bool
	)
	if data, ok = buffer.(JsMap); !ok {
		return sperrors.InvalidDataError
	}
	if _, ok = data["uaid"]; !ok {
		// Must include "uaid" (even if blank)
		data["uaid"] = ""
	}
	suggestedUAID, ok := data["uaid"].(string)
	if !ok {
		return sperrors.InvalidDataError
	}
	messageType, _ := data["messageType"].(string)
	/* NOTE: This seems to be a redirect, which I don't believe we support
	if redir := self.config.Get("db.redirect", ""); len(redir) > 0 {
		statusCode := 302
		resp := JsMap{
			"messageType": messageType,
			"status":      statusCode,
			"redirect":    redir,
			"uaid":        sock.Uaid}
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("worker", "sending redirect",
				LogFields{"messageType": messageType,
					"status":   strconv.FormatInt(statusCode, 10),
					"redirect": redir,
					"uaid":     suggestedUAID})
		}
		websocket.JSON.Send(sock.Socket, resp)
		return nil
	} */
	if data["channelIDs"] == nil {
		// Must include "channelIDs" (even if empty)
		self.logger.Debug("worker", "Missing ChannelIDs", nil)
		return sperrors.MissingDataError
	}
	if len(sock.Uaid) > 0 &&
		len(suggestedUAID) > 0 &&
		sock.Uaid != suggestedUAID {
		// if there's already a Uaid for this channel, don't accept a new one
		self.logger.Debug("worker", "Conflicting UAIDs", nil)
		return sperrors.InvalidChannelError
	}
	if len(suggestedUAID) > 0 && !id.Valid(suggestedUAID) {
		self.logger.Debug("worker", "Invalid character in UAID", nil)
		return sperrors.InvalidChannelError
	}
	if len(sock.Uaid) == 0 {
		// if there's no UAID for the socket, accept or create a new one.
		sock.Uaid = suggestedUAID
		forceReset = len(sock.Uaid) == 0
		if !forceReset {
			forceReset = self.app.ClientExists(sock.Uaid)
		}
		if !forceReset {
			channelIDs, _ := data["channelIDs"].([]interface{})
			// are there a suspicious number of channels?
			if len(channelIDs) > self.maxChannels {
				forceReset = true
			}
			if !forceReset {
				forceReset = !sock.Store.Exists(sock.Uaid)
			}
		}
	}
	if forceReset {
		if self.logger.ShouldLog(WARNING) {
			self.logger.Warn("worker", "Resetting UAID for device",
				LogFields{"uaid": sock.Uaid})
		}
		if len(sock.Uaid) > 0 {
			sock.Store.DropAll(sock.Uaid)
		}
		sock.Uaid, _ = id.Generate()
	}
	// register any proprietary connection requirements
	// alert the master of the new UAID.
	// It's not a bad idea from a security POV to only send
	// known args through to the server.
	cmd := PushCommand{
		Command: HELLO,
		Arguments: JsMap{
			"worker":  self,
			"uaid":    sock.Uaid,
			"chids":   data["channelIDs"],
			"connect": data["connect"],
		},
	}
	// blocking call back to the boss.
	status, _ := self.app.Server().HandleCommand(cmd, sock)

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"cmd": "hello", "error": ErrStr(err),
				"uaid": sock.Uaid})
	}
	// websocket.JSON.Send(sock.Socket, JsMap{
	// 	"messageType": data["messageType"],
	// 	"status":      status,
	// 	"uaid":        sock.Uaid})
	msg := []byte(fmt.Sprintf(`{"messageType":"%s","status":%d,"uaid":"%s"}`,
		messageType, status, sock.Uaid))
	_, err = sock.Socket.Write(msg)
	self.metrics.Increment("updates.client.hello")
	self.logger.Info("dash", "Client successfully connected", nil)
	self.state = WorkerActive
	if err == nil {
		// Get the lastAccessed time from wherever
		return self.Flush(sock, 0, "", 0)
	}
	return err
}

// Clear the data that the client stated it received, then re-flush any
// records (including new data)
func (self *Worker) Ack(sock *PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil {
				self.logger.Error("worker",
					"Unhandled error",
					LogFields{"cmd": "ack", "error": ErrStr(err)})
			}
			debug.PrintStack()
			err = sperrors.InvalidDataError
		}
	}()
	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data, _ := buffer.(JsMap)
	updates, _ := data["updates"].([]interface{})
	if len(updates) == 0 {
		return sperrors.MissingDataError
	}
	var (
		update map[string]interface{}
		schid  string
		ok     bool
	)
	for _, field := range updates {
		if update, ok = field.(map[string]interface{}); !ok {
			continue
		}
		if schid, ok = update["channelID"].(string); !ok {
			continue
		}
		if err = sock.Store.Drop(sock.Uaid, schid); err != nil {
			break
		}
	}
	expired, _ := data["expired"].([]interface{})
	for _, field := range expired {
		if schid, ok = field.(string); !ok {
			continue
		}
		if err = sock.Store.Drop(sock.Uaid, schid); err != nil {
			break
		}
	}
	// Get the lastAccessed time from wherever.
	if err == nil {
		return self.Flush(sock, 0, "", 0)
	}
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"cmd": "ack", "error": ErrStr(err)})
	}
	self.metrics.Increment("updates.client.ack")
	return err
}

// Register a new ChannelID. Optionally, encrypt the endpoint.
func (self *Worker) Register(sock *PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil {
				self.logger.Error("worker",
					"Unhandled error",
					LogFields{"cmd": "register", "error": ErrStr(err)})
			}
			debug.PrintStack()
			err = sperrors.InvalidDataError
		}
	}()

	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data, _ := buffer.(JsMap)
	appid, _ := data["channelID"].(string)
	if !id.Valid(appid) {
		return sperrors.InvalidDataError
	}
	if err = sock.Store.Register(sock.Uaid, appid, 0); err != nil {
		self.logger.Error("worker",
			fmt.Sprintf("ERROR: Register failed %s", err),
			nil)
		return err
	}
	// have the server generate the callback URL.
	cmd := PushCommand{Command: REGIS, Arguments: data}
	status, args := self.app.Server().HandleCommand(cmd, sock)
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker",
			"Server returned", LogFields{"Command": strconv.FormatInt(int64(status), 10),
				"args.channelID": IStr(args["channelID"]),
				"args.uaid":      IStr(args["uaid"])})
	}
	endpoint, _ := args["push.endpoint"].(string)
	// return the info back to the socket
	messageType, _ := data["messageType"].(string)
	statusCode := 200
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response", LogFields{
			"messageType":  messageType,
			"uaid":         sock.Uaid,
			"status":       strconv.FormatInt(int64(statusCode), 10),
			"channelID":    appid,
			"pushEndpoint": endpoint})
	}
	websocket.JSON.Send(sock.Socket, RegisterReply{messageType, sock.Uaid, statusCode, appid, endpoint})
	self.metrics.Increment("updates.client.register")
	return err
}

// Unregister a ChannelID.
func (self *Worker) Unregister(sock *PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil {
				self.logger.Error("worker",
					"Unhandled error",
					LogFields{"cmd": "register", "error": ErrStr(err)})
			}
			err = sperrors.InvalidDataError
		}
	}()
	if sock.Uaid == "" {
		self.logger.Error("worker",
			"Unregister failed, missing sock.uaid", nil)
		return sperrors.InvalidCommandError
	}
	data, _ := buffer.(JsMap)
	appid, _ := data["channelID"].(string)
	if len(appid) == 0 {
		self.logger.Error("worker",
			"Unregister failed, missing channelID", nil)
		return sperrors.MissingDataError
	}
	// Always return success for an UNREG.
	sock.Store.Unregister(sock.Uaid, appid)
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"cmd": "unregister", "error": ErrStr(err)})
	}
	websocket.JSON.Send(sock.Socket, UnregisterReply{data["messageType"], 200, appid})
	self.metrics.Increment("updates.client.unregister")
	return err
}

// Dump any records associated with the UAID.
func (self *Worker) Flush(sock *PushWS, lastAccessed int64, channel string, version int64) (err error) {
	// flush pending data back to Client
	timer := time.Now()
	messageType := "notification"
	defer func(timer time.Time, sock *PushWS) {
		now := time.Now()
		if sock.Logger.ShouldLog(INFO) {
			sock.Logger.Info("timer",
				"Client flush completed",
				LogFields{"duration": strconv.FormatInt(int64(now.Sub(timer)), 10),
					"uaid": sock.Uaid})
		}
		self.metrics.Timer("client.flush", now.Sub(timer))
	}(timer, sock)
	if sock.Uaid == "" {
		self.logger.Error("worker",
			"Undefined UAID for socket. Aborting.", nil)
		// Have the server clean up records associated with this UAID.
		// (Probably "none", but still good for housekeeping)
		self.stopped = true
		return nil
	}
	// Fetch the pending updates from #storage
	var (
		updates []Update
		reply   *FlushReply
	)
	mod := false
	// if we have a channel, don't flush. we can get them later in the ACK
	if len(channel) == 0 {
		var expired []string
		if updates, expired, err = sock.Store.FetchAll(sock.Uaid, time.Unix(lastAccessed, 0)); err != nil {
			self.logger.Error("worker", "Failed to flush Update to client.",
				LogFields{"uaid": sock.Uaid, "error": err.Error()})
			return err
		}
		if len(updates) > 0 || len(expired) > 0 {
			reply = &FlushReply{messageType, updates, expired}
		}
	} else {
		// hand craft a notification update to the client.
		// TODO: allow bulk updates.
		updates = []Update{Update{channel, uint64(version)}}
		reply = &FlushReply{messageType, updates, nil}
	}
	if reply == nil {
		return nil
	}
	var logStrings []string
	if len(channel) > 0 {
		logStrings := make([]string, len(updates))
		prefix := ">>"
		if !mod {
			prefix = "+>"
		}
		for index, update := range updates {
			logStrings[index] = fmt.Sprintf("%s %s.%s = %d", prefix, sock.Uaid, update.ChannelID, update.Version)
			self.metrics.Increment("updates.sent")
		}
	}

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "Flushing data back to socket",
			LogFields{"updates": "[" + strings.Join(logStrings, ", ") + "]"})
	}
	websocket.JSON.Send(sock.Socket, reply)
	return nil
}

func (self *Worker) Ping(sock *PushWS, buffer interface{}) (err error) {
	now := time.Now()
	if self.pingInt > 0 && !self.lastPing.IsZero() && now.Sub(self.lastPing) < self.pingInt {
		source := sock.Socket.Config().Origin
		self.logger.Error("dash", "Client sending too many pings",
			LogFields{"source": source.String()})
		self.stopped = true
		self.metrics.Increment("updates.client.too_many_pings")
		return sperrors.TooManyPingsError
	}
	self.lastPing = now
	data, _ := buffer.(JsMap)
	if self.app.pushLongPongs {
		websocket.JSON.Send(sock.Socket, PingReply{data["messageType"], 200})
	} else {
		websocket.Message.Send(sock.Socket, "{}")
	}
	self.metrics.Increment("updates.client.ping")
	return nil
}

// TESTING func, purge associated records for this UAID
func (self *Worker) Purge(sock *PushWS, buffer interface{}) (err error) {
	/*
	   // If needed...
	   sock.Scmd <- PushCommand{Command: PURGE,
	       Arguments:JsMap{"uaid": sock.Uaid}}
	   result := <-sock.Scmd
	*/
	websocket.JSON.Send(sock.Socket, JsMap{})
	return nil
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
