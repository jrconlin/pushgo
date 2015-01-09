/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mozilla-services/pushgo/id"
)

// A set of error strings that aren't too bad
var HarmlessConnectionErrors = []string{
	"connection timed out",
	"TLS handshake error",
}

//    -- Workers
//      these write back to the websocket.

type Worker interface {
	Run(*PushWS)
	Flush(*PushWS, int64, string, int64, string) error
}

type WorkerWS struct {
	app          *Application
	logger       *SimpleLogger
	id           string
	state        WorkerState
	lastPing     time.Time
	pingInt      time.Duration
	metrics      Statistician
	helloTimeout time.Duration
	pongInterval time.Duration
}

type WorkerState int

const (
	WorkerInactive WorkerState = iota
	WorkerActive
	WorkerStopped
)

type RequestHeader struct {
	Type string `json:"messageType"`
}

type HelloRequest struct {
	DeviceID   string            `json:"uaid"`
	ChannelIDs []json.RawMessage `json:"channelIDs"`
	PingData   json.RawMessage   `json:"connect"`
}

type HelloReply struct {
	Type        string  `json:"messageType"`
	DeviceID    string  `json:"uaid"`
	Status      int     `json:"status"`
	RedirectURL *string `json:"redirect,omitempty"`
}

type RegisterRequest struct {
	ChannelID string `json:"channelID"`
}

type RegisterReply struct {
	Type      string `json:"messageType"`
	DeviceID  string `json:"uaid"`
	Status    int    `json:"status"`
	ChannelID string `json:"channelID"`
	Endpoint  string `json:"pushEndpoint"`
}

type UnregisterRequest struct {
	ChannelID string `json:"channelID"`
}

type UnregisterReply struct {
	Type      string `json:"messageType"`
	Status    int    `json:"status"`
	ChannelID string `json:"channelID"`
}

type FlushReply struct {
	Type    string   `json:"messageType"`
	Updates []Update `json:"updates,omitempty"`
	Expired []string `json:"expired,omitempty"`
}

type ACKRequest struct {
	Updates []Update `json:"updates"`
	Expired []string `json:"expired"`
}

type PingReply struct {
	Type   string `json:"messageType"`
	Status int    `json:"status"`
}

type FlushData struct {
	LastAccessed int64  `json:"lastaccessed"`
	Channel      string `json:"channel"`
	Version      int64  `json:"version"`
	Data         string `json:"data"`
}

func NewWorker(app *Application, id string) *WorkerWS {
	return &WorkerWS{
		app:          app,
		logger:       app.Logger(),
		metrics:      app.Metrics(),
		id:           id,
		state:        WorkerInactive,
		pingInt:      app.clientMinPing,
		helloTimeout: app.clientHelloTimeout,
		pongInterval: app.clientPongInterval,
	}
}

// Indicates whether a connection error is harmless
func harmlessConnectionError(err error) (harmless bool) {
	harmless = false
	errStr := err.Error()
	for _, connError := range HarmlessConnectionErrors {
		if strings.Contains(errStr, connError) {
			harmless = true
			return
		}
	}
	return
}

func (self *WorkerWS) Deadline() (t time.Time) {
	var d time.Duration
	switch self.state {
	case WorkerInactive:
		d = self.helloTimeout
	case WorkerActive:
		d = self.pongInterval
	}
	if d > 0 {
		t = timeNow().Add(d)
	}
	return
}

func (self *WorkerWS) sniffer(sock *PushWS) {
	// Sniff the websocket for incoming data.
	// Reading from the websocket is a blocking operation, and we also
	// need to write out when an even occurs. This isolates the incoming
	// reads to a separate go process.
	logWarning := self.logger.ShouldLog(WARNING)
	buf := new(bytes.Buffer)

	for !self.stopped() {
		buf.Reset()
		sock.Socket.SetReadDeadline(self.Deadline())
		raw, err := sock.Socket.ReadBinary()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if self.state == WorkerInactive {
					if self.logger.ShouldLog(DEBUG) {
						self.logger.Debug("dash", "Worker Idle connection. Closing socket",
							LogFields{"rid": self.id})
					}
					self.stop()
					continue
				}
				if err = sock.Socket.WriteText("{}"); err == nil {
					continue
				}
			}
			self.stop()
			if err != io.EOF {
				if self.logger.ShouldLog(ERROR) && !harmlessConnectionError(err) {
					self.logger.Error("worker", "Websocket Error",
						LogFields{"rid": self.id, "error": ErrStr(err)})
				}
			}
			continue
		}
		if len(raw) <= 0 {
			continue
		}
		var msg []byte
		if isPingBody(raw) {
			// Fast case: empty object literal; no whitespace.
			msg = raw
		} else if err = json.Compact(buf, raw); err != nil {
			// Slower case: validate and remove insignificant whitespace from the
			// incoming slice.
			if logWarning {
				if syntaxErr, ok := err.(*json.SyntaxError); ok {
					self.logger.Warn("worker", "Malformed request payload", LogFields{
						"rid":      self.id,
						"expected": string(msg[:syntaxErr.Offset]),
						"error":    syntaxErr.Error()})
				} else {
					self.logger.Warn("worker", "Error validating request payload",
						LogFields{"rid": self.id, "error": ErrStr(err)})
				}
			}
			self.stop()
			continue
		} else {
			msg = buf.Bytes()
		}

		//ignore {} pings for logging purposes.
		if len(msg) > 5 && self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("worker", "Socket receive",
				LogFields{"rid": self.id, "raw": string(msg)})
		}
		header := new(RequestHeader)
		if isPingBody(msg) {
			header.Type = "ping"
		} else if err = json.Unmarshal(msg, header); err != nil {
			if logWarning {
				self.logger.Warn("worker", "Error parsing request header",
					LogFields{"rid": self.id, "error": ErrStr(err)})
			}
			self.handleError(sock, msg, ErrUnknownCommand)
			self.stop()
			continue
		}
		switch strings.ToLower(header.Type) {
		case "ping":
			err = self.Ping(sock, header, msg)
		case "hello":
			err = self.Hello(sock, header, msg)
		case "ack":
			err = self.Ack(sock, header, msg)
		case "register":
			err = self.Register(sock, header, msg)
		case "unregister":
			err = self.Unregister(sock, header, msg)
		case "purge":
			err = self.Purge(sock, header, msg)
		default:
			if logWarning {
				self.logger.Warn("worker", "Bad command",
					LogFields{"rid": self.id, "cmd": header.Type})
			}
			err = ErrUnknownCommand
		}
		if err != nil {
			if self.logger.ShouldLog(DEBUG) {
				self.logger.Debug("worker", "Run returned error",
					LogFields{"rid": self.id, "cmd": header.Type, "error": ErrStr(err)})
			}
			self.handleError(sock, msg, err)
			self.stop()
			continue
		}
	}
}

func (self *WorkerWS) stopped() bool {
	return self.state == WorkerStopped
}

func (self *WorkerWS) stop() {
	self.state = WorkerStopped
}

// standardize the error reporting back to the client.
func (self *WorkerWS) handleError(sock *PushWS, message []byte, err error) (ret error) {
	reply := make(map[string]interface{})
	if ret = json.Unmarshal(message, &reply); ret != nil {
		return
	}
	reply["status"], reply["error"] = ErrToStatus(err)
	return sock.Socket.WriteJSON(reply)
}

// General workhorse loop for the websocket handler.
func (self *WorkerWS) Run(sock *PushWS) {
	defer func(sock *PushWS) {
		sock.Socket.Close()
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled connection error", LogFields{
					"rid":   self.id,
					"error": ErrStr(err),
					"stack": string(stack[:n])})
			}
		}
		return
	}(sock)

	self.sniffer(sock)

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Run has completed a shut-down",
			LogFields{"rid": self.id})
	}
}

// Associate the UAID for this socket connection (and flush any data that
// may be pending for the connection)
func (self *WorkerWS) Hello(sock *PushWS, header *RequestHeader, message []byte) (err error) {
	logWarning := self.logger.ShouldLog(WARNING)
	// register the UAID
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.id,
					"cmd": "hello", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()

	request := new(HelloRequest)
	if err = json.Unmarshal(message, request); err != nil {
		return ErrInvalidParams
	}
	uaid, allowRedirect, err := self.handshake(sock, request)
	if err != nil {
		return err
	}
	sock.SetUAID(uaid)

	if allowRedirect {
		b := self.app.Balancer()
		if b == nil {
			goto registerDevice
		}
		origin, shouldRedirect, err := b.RedirectURL()
		if err != nil {
			if logWarning {
				self.logger.Warn("worker", "Failed to redirect client", LogFields{
					"error": err.Error(), "rid": self.id, "cmd": header.Type})
			}
			reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":429}`,
				header.Type, uaid)
			err = sock.Socket.WriteText(reply)
			self.stop()
			return err
		}
		if !shouldRedirect {
			goto registerDevice
		}
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("worker", "Redirecting client", LogFields{
				"rid": self.id, "cmd": header.Type, "origin": origin})
		}
		reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":307,"redirect":%q}`,
			header.Type, uaid, origin)
		err = sock.Socket.WriteText(reply)
		self.stop()
		return err
	}

registerDevice:
	// register any proprietary connection requirements
	// alert the master of the new UAID.
	// It's not a bad idea from a security POV to only send
	// known args through to the server.
	cmd := PushCommand{
		Command: HELLO,
		Arguments: JsMap{
			"worker":  self,
			"uaid":    uaid,
			"chids":   cleanChannelIDs(request.ChannelIDs),
			"connect": []byte(request.PingData),
		},
	}
	// blocking call back to the boss.
	status, _ := self.app.Server().HandleCommand(cmd, sock)

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"rid": self.id, "cmd": "hello", "uaid": uaid})
	}
	reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":%d}`,
		header.Type, uaid, status)
	if err = sock.Socket.WriteText(reply); err != nil {
		if logWarning {
			self.logger.Warn("dash", "Error writing client handshake", LogFields{
				"rid": self.id, "error": err.Error()})
		}
		return err
	}
	self.metrics.Increment("updates.client.hello")
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Client successfully connected",
			LogFields{"rid": self.id})
	}
	self.state = WorkerActive
	// Get the lastAccessed time from wherever
	return self.Flush(sock, 0, "", 0, "")
}

func (self *WorkerWS) handshake(sock *PushWS, request *HelloRequest) (
	deviceID string, allowRedirect bool, err error) {

	logWarning := self.logger.ShouldLog(WARNING)
	currentID := sock.UAID()

	if request.ChannelIDs == nil {
		// Must include "channelIDs" (even if empty)
		if logWarning {
			self.logger.Warn("worker", "Missing ChannelIDs",
				LogFields{"rid": self.id})
		}
		return "", false, ErrNoParams
	}

	if len(currentID) > 0 {
		if len(request.DeviceID) == 0 || currentID == request.DeviceID {
			// Duplicate handshake with omitted or identical device ID. Allow the
			// caller to flush pending notifications, but avoid querying the balancer.
			if self.logger.ShouldLog(DEBUG) {
				self.logger.Debug("worker", "Duplicate client handshake",
					LogFields{"rid": self.id})
			}
			return currentID, false, nil
		}
		// if there's already a Uaid for this device, don't accept a new one
		if logWarning {
			self.logger.Warn("worker", "Conflicting UAIDs",
				LogFields{"rid": self.id})
		}
		return "", false, ErrExistingID
	}
	var (
		client          *Client
		clientConnected bool
	)
	if len(request.DeviceID) == 0 {
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("worker", "Generating new UAID for device",
				LogFields{"rid": self.id})
		}
		goto forceReset
	}
	if !id.Valid(request.DeviceID) {
		if logWarning {
			self.logger.Warn("worker", "Invalid character in UAID",
				LogFields{"rid": self.id})
		}
		return "", false, ErrInvalidID
	}
	if !sock.Store.CanStore(len(request.ChannelIDs)) {
		// are there a suspicious number of channels?
		if logWarning {
			self.logger.Warn("worker",
				"Too many channel IDs in handshake; resetting UAID", LogFields{
					"rid":      self.id,
					"uaid":     request.DeviceID,
					"channels": strconv.Itoa(len(request.ChannelIDs))})
		}
		sock.Store.DropAll(request.DeviceID)
		goto forceReset
	}
	client, clientConnected = self.app.GetClient(request.DeviceID)
	if clientConnected {
		if self.logger.ShouldLog(INFO) {
			self.logger.Info("worker", "UAID collision; disconnecting previous client",
				LogFields{"rid": self.id, "uaid": request.DeviceID})
		}
		self.app.Server().HandleCommand(PushCommand{DIE, nil}, client.PushWS)
	}
	if len(request.ChannelIDs) > 0 && !sock.Store.Exists(request.DeviceID) {
		if logWarning {
			self.logger.Warn("worker",
				"Channel IDs specified in handshake for nonexistent UAID",
				LogFields{"rid": self.id, "uaid": request.DeviceID})
		}
		goto forceReset
	}
	return request.DeviceID, true, nil

forceReset:
	if deviceID, err = idGenerate(); err != nil {
		return "", false, err
	}
	return deviceID, true, nil
}

// Clear the data that the client stated it received, then re-flush any
// records (including new data)
func (self *WorkerWS) Ack(sock *PushWS, _ *RequestHeader, message []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.id,
					"cmd": "ack", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()
	uaid := sock.UAID()
	if uaid == "" {
		return ErrInvalidCommand
	}
	request := new(ACKRequest)
	if err = json.Unmarshal(message, request); err != nil {
		return ErrInvalidParams
	}
	if len(request.Updates) == 0 && len(request.Expired) == 0 {
		return ErrNoParams
	}
	self.metrics.Increment("updates.client.ack")
	for _, update := range request.Updates {
		if err = sock.Store.Drop(uaid, update.ChannelID); err != nil {
			goto logError
		}
	}
	for _, channelID := range request.Expired {
		if err = sock.Store.Drop(uaid, channelID); err != nil {
			goto logError
		}
	}
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"rid": self.id, "cmd": "ack"})
	}
	// Get the lastAccessed time from wherever.
	return self.Flush(sock, 0, "", 0, "")
logError:
	if self.logger.ShouldLog(WARNING) {
		self.logger.Warn("worker", "sending response",
			LogFields{"rid": self.id, "cmd": "ack", "error": ErrStr(err)})
	}
	return err
}

// Register a new ChannelID. Optionally, encrypt the endpoint.
func (self *WorkerWS) Register(sock *PushWS, header *RequestHeader, message []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.id,
					"cmd": "register", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()

	uaid := sock.UAID()
	if uaid == "" {
		return ErrInvalidCommand
	}
	request := new(RegisterRequest)
	if err = json.Unmarshal(message, request); err != nil || !id.Valid(request.ChannelID) {
		return ErrInvalidParams
	}
	if err = sock.Store.Register(uaid, request.ChannelID, 0); err != nil {
		if self.logger.ShouldLog(WARNING) {
			self.logger.Warn("worker", "Register failed, error updating backing store",
				LogFields{"rid": self.id, "cmd": "register", "error": ErrStr(err)})
		}
		return err
	}
	// have the server generate the callback URL.
	cmd := PushCommand{
		Command:   REGIS,
		Arguments: JsMap{"channelID": request.ChannelID},
	}
	status, args := self.app.Server().HandleCommand(cmd, sock)
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "Server returned", LogFields{
			"rid":  self.id,
			"cmd":  "register",
			"code": strconv.FormatInt(int64(status), 10),
			"chid": IStr(args["channelID"]),
			"uaid": IStr(args["uaid"])})
	}
	endpoint, _ := args["push.endpoint"].(string)
	// return the info back to the socket
	statusCode := 200
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response", LogFields{
			"rid":          self.id,
			"cmd":          "register",
			"uaid":         uaid,
			"code":         strconv.FormatInt(int64(statusCode), 10),
			"channelID":    request.ChannelID,
			"pushEndpoint": endpoint})
	}
	sock.Socket.WriteJSON(RegisterReply{header.Type, uaid, statusCode, request.ChannelID, endpoint})
	self.metrics.Increment("updates.client.register")
	return nil
}

// Unregister a ChannelID.
func (self *WorkerWS) Unregister(sock *PushWS, header *RequestHeader, message []byte) (err error) {
	logWarning := self.logger.ShouldLog(WARNING)
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.id,
					"cmd": "register", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()
	uaid := sock.UAID()
	if uaid == "" {
		if logWarning {
			self.logger.Warn("worker", "Unregister failed, missing sock.uaid",
				LogFields{"rid": self.id})
		}
		return ErrInvalidCommand
	}
	request := new(UnregisterRequest)
	if err = json.Unmarshal(message, request); err != nil {
		return ErrInvalidParams
	}
	if len(request.ChannelID) == 0 {
		if logWarning {
			self.logger.Warn("worker", "Unregister failed, missing channelID",
				LogFields{"rid": self.id})
		}
		return ErrNoParams
	}
	// Always return success for an UNREG.
	if err = sock.Store.Unregister(uaid, request.ChannelID); err != nil {
		if logWarning {
			self.logger.Warn("worker", "Unregister failed, error updating backing store",
				LogFields{"rid": self.id, "error": ErrStr(err)})
		}
	} else if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"rid": self.id, "cmd": "unregister"})
	}
	sock.Socket.WriteJSON(UnregisterReply{header.Type, 200, request.ChannelID})
	self.metrics.Increment("updates.client.unregister")
	return nil
}

// Dump any records associated with the UAID.
func (self *WorkerWS) Flush(sock *PushWS, lastAccessed int64, channel string, version int64, data string) (err error) {
	// flush pending data back to Client
	timer := timeNow()
	logWarning := self.logger.ShouldLog(WARNING)
	messageType := "notification"
	uaid := sock.UAID()
	defer func(timer time.Time, sock *PushWS, err *error) {
		now := timeNow()
		if sock.Logger.ShouldLog(INFO) {
			sock.Logger.Info("timer",
				"Client flush completed",
				LogFields{"duration": strconv.FormatInt(int64(now.Sub(timer)), 10),
					"uaid": uaid})
		}
		if *err != nil || self.stopped() {
			return
		}
		self.metrics.Timer("client.flush", now.Sub(timer))
	}(timer, sock, &err)
	if uaid == "" {
		if logWarning {
			self.logger.Warn("worker", "Undefined UAID for socket. Aborting.",
				LogFields{"rid": self.id})
		}
		// Have the server clean up records associated with this UAID.
		// (Probably "none", but still good for housekeeping)
		self.stop()
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
		if updates, expired, err = sock.Store.FetchAll(uaid, time.Unix(lastAccessed, 0)); err != nil {
			if logWarning {
				self.logger.Warn("worker", "Failed to flush Update to client.",
					LogFields{"rid": self.id, "uaid": uaid, "error": err.Error()})
			}
			return err
		}
		if len(updates) > 0 || len(expired) > 0 {
			reply = &FlushReply{messageType, updates, expired}
		}
	} else {
		// hand craft a notification update to the client.
		// TODO: allow bulk updates.
		updates = []Update{Update{channel, uint64(version), data}}
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
		for i, update := range updates {
			logStrings[i] = fmt.Sprintf("%s %s.%s = %d", prefix, uaid,
				update.ChannelID, update.Version)
		}
		self.metrics.IncrementBy("updates.sent", int64(len(updates)))
	}

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "Flushing data back to socket", LogFields{
			"rid":     self.id,
			"updates": fmt.Sprintf("[%s]", strings.Join(logStrings, ", "))})
	}
	sock.Socket.WriteJSON(reply)
	return nil
}

func (self *WorkerWS) Ping(sock *PushWS, header *RequestHeader, _ []byte) (err error) {
	now := timeNow()
	if self.pingInt > 0 && !self.lastPing.IsZero() && now.Sub(self.lastPing) < self.pingInt {
		if self.logger.ShouldLog(WARNING) {
			source, ok := sock.Socket.Origin()
			if !ok {
				source = "No Socket Origin"
			}
			self.logger.Warn("dash", "Client sending too many pings",
				LogFields{"rid": self.id, "source": source})
		}
		self.stop()
		self.metrics.Increment("updates.client.too_many_pings")
		return ErrTooManyPings
	}
	self.lastPing = now
	if self.app.pushLongPongs {
		sock.Socket.WriteJSON(PingReply{header.Type, 200})
	} else {
		sock.Socket.WriteText("{}")
	}
	self.metrics.Increment("updates.client.ping")
	return nil
}

// TESTING func, purge associated records for this UAID
func (self *WorkerWS) Purge(sock *PushWS, _ *RequestHeader, _ []byte) (err error) {
	/*
	   // If needed...
	   sock.Scmd <- PushCommand{Command: PURGE,
	       Arguments:JsMap{"uaid": sock.UAID()}}
	   result := <-sock.Scmd
	*/
	sock.Socket.WriteText("{}")
	return nil
}

func isPingBody(raw []byte) bool {
	return len(raw) == 0 || len(raw) == 2 && raw[0] == '{' && raw[1] == '}'
}

//== Fake Worker

type NoWorker struct {
	Inbuffer  []byte
	Outbuffer []byte
	Logger    *SimpleLogger
	Socket    *PushWS
}

func (r *NoWorker) Run(s *PushWS) {
	r.Socket = s
	r.Logger.Debug("noworker", "Run", nil)
}

func (r *NoWorker) Flush(_ *PushWS, lastAccessed int64, channel string, version int64, data string) error {
	r.Logger.Debug("noworker", "Got Flush", LogFields{
		"channel": channel,
		"data":    data,
	})
	r.Outbuffer, _ = json.Marshal(&FlushData{lastAccessed,
		channel, version, data})
	return nil
}

func cleanChannelIDs(ids []json.RawMessage) (clean []string) {
	clean = make([]string, len(ids))
	for i, b := range ids {
		clean[i] = string(b[1 : len(b)-1])
	}
	return
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
