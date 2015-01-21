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

// Worker encapsulates a client connection.
type Worker interface {
	// Born returns the client connection time.
	Born() time.Time

	// UAID returns the device ID as a hex-encoded string, or an empty string
	// if the client has not completed the handshake.
	UAID() string

	// SetUAID assigns uaid to the client.
	SetUAID(uaid string)

	// Run reads and responds to client commands, blocking until the connection
	// is closed by either party.
	Run()

	// Flush sends an update containing chid, version, and data to the client.
	// If chid is an empty string, all pending updates since the lastAccessed
	// time (expressed in seconds since Epoch) will be flushed to the client.
	Flush(lastAccessed int64, chid string, version int64, data string) error

	// Close unblocks the run loop and closes the underlying socket.
	Close() error
}

type WorkerWS struct {
	Socket
	born         time.Time
	app          *Application
	logger       *SimpleLogger
	metrics      Statistician
	store        Store
	logID        string
	uaid         string
	state        WorkerState
	lastPing     time.Time
	pingInt      time.Duration
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

func NewWorker(app *Application, socket Socket, logID string) *WorkerWS {
	return &WorkerWS{
		Socket:       socket,
		born:         time.Now(),
		app:          app,
		logger:       app.Logger(),
		metrics:      app.Metrics(),
		store:        app.Store(),
		logID:        logID,
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

func (self *WorkerWS) Born() time.Time     { return self.born }
func (self *WorkerWS) SetUAID(uaid string) { self.uaid = uaid }
func (self *WorkerWS) UAID() string        { return self.uaid }

// ReadDeadline determines the deadline t for the next read. If the handshake
// timeout and pong interval are not set, t is the zero value.
func (self *WorkerWS) ReadDeadline() (t time.Time) {
	switch self.state {
	// For unidentified clients, the deadline is the handshake timeout relative
	// to the socket creation time. This prevents clients from extending the
	// timeout by sending pings.
	case WorkerInactive:
		if self.helloTimeout > 0 {
			t = self.Born().Add(self.helloTimeout)
		}

	// For clients that have completed the handshake, the deadline is the end of
	// the next pong interval.
	case WorkerActive:
		if self.pongInterval > 0 {
			t = timeNow().Add(self.pongInterval)
		}
	}
	return
}

func (self *WorkerWS) sniffer() {
	// Sniff the websocket for incoming data.
	// Reading from the websocket is a blocking operation, and we also
	// need to write out when an even occurs. This isolates the incoming
	// reads to a separate go process.
	logWarning := self.logger.ShouldLog(WARNING)
	buf := new(bytes.Buffer)

	for !self.stopped() {
		buf.Reset()
		self.SetReadDeadline(self.ReadDeadline())
		raw, err := self.ReadBinary()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if self.state == WorkerInactive {
					if self.logger.ShouldLog(DEBUG) {
						self.logger.Debug("dash", "Worker Idle connection. Closing socket",
							LogFields{"rid": self.logID})
					}
					self.stop()
					continue
				}
				if err = self.WriteText("{}"); err == nil {
					continue
				}
			}
			self.stop()
			if err != io.EOF {
				if self.logger.ShouldLog(ERROR) && !harmlessConnectionError(err) {
					self.logger.Error("worker", "Websocket Error",
						LogFields{"rid": self.logID, "error": ErrStr(err)})
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
						"rid":      self.logID,
						"expected": string(msg[:syntaxErr.Offset]),
						"error":    syntaxErr.Error()})
				} else {
					self.logger.Warn("worker", "Error validating request payload",
						LogFields{"rid": self.logID, "error": ErrStr(err)})
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
				LogFields{"rid": self.logID, "raw": string(msg)})
		}
		header := new(RequestHeader)
		if isPingBody(msg) {
			header.Type = "ping"
		} else if err = json.Unmarshal(msg, header); err != nil {
			if logWarning {
				self.logger.Warn("worker", "Error parsing request header",
					LogFields{"rid": self.logID, "error": ErrStr(err)})
			}
			self.handleError(msg, ErrInvalidHeader)
			self.stop()
			continue
		}
		switch strings.ToLower(header.Type) {
		case "purge": // No-op for backward compatibility.
		case "ping":
			err = self.Ping(header, msg)
		case "hello":
			err = self.Hello(header, msg)
		case "ack":
			err = self.Ack(header, msg)
		case "register":
			err = self.Register(header, msg)
		case "unregister":
			err = self.Unregister(header, msg)
		default:
			if logWarning {
				self.logger.Warn("worker", "Bad command",
					LogFields{"rid": self.logID, "cmd": header.Type})
			}
			err = ErrUnsupportedType
		}
		if err != nil {
			if self.logger.ShouldLog(DEBUG) {
				self.logger.Debug("worker", "Run returned error",
					LogFields{"rid": self.logID, "cmd": header.Type, "error": ErrStr(err)})
			}
			self.handleError(msg, err)
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
func (self *WorkerWS) handleError(message []byte, err error) (ret error) {
	reply := make(map[string]interface{})
	if ret = json.Unmarshal(message, &reply); ret != nil {
		return
	}
	reply["status"], reply["error"] = ErrToStatus(err)
	return self.WriteJSON(reply)
}

// General workhorse loop for the websocket handler.
func (self *WorkerWS) Run() {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled connection error", LogFields{
					"rid":   self.logID,
					"error": ErrStr(err),
					"stack": string(stack[:n])})
			}
		}
		return
	}()

	self.sniffer()

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Run has completed a shut-down",
			LogFields{"rid": self.logID})
	}
}

// Associate the UAID for this socket connection (and flush any data that
// may be pending for the connection)
func (self *WorkerWS) Hello(header *RequestHeader, message []byte) (err error) {
	logWarning := self.logger.ShouldLog(WARNING)
	// register the UAID
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.logID,
					"cmd": "hello", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()

	request := new(HelloRequest)
	if err = json.Unmarshal(message, request); err != nil {
		return ErrInvalidParams
	}
	uaid, allowRedirect, err := self.handshake(request)
	if err != nil {
		return err
	}
	self.SetUAID(uaid)

	if allowRedirect {
		b := self.app.Balancer()
		if b == nil {
			goto registerDevice
		}
		origin, shouldRedirect, err := b.RedirectURL()
		if err != nil {
			if logWarning {
				self.logger.Warn("worker", "Failed to redirect client", LogFields{
					"error": err.Error(), "rid": self.logID, "cmd": header.Type})
			}
			reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":429}`,
				header.Type, uaid)
			err = self.WriteText(reply)
			self.stop()
			return err
		}
		if !shouldRedirect {
			goto registerDevice
		}
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("worker", "Redirecting client", LogFields{
				"rid": self.logID, "cmd": header.Type, "origin": origin})
		}
		reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":307,"redirect":%q}`,
			header.Type, uaid, origin)
		err = self.WriteText(reply)
		self.stop()
		return err
	}

registerDevice:
	// register any proprietary connection requirements
	// alert the master of the new UAID.
	// It's not a bad idea from a security POV to only send
	// known args through to the server.
	// blocking call back to the boss.
	status, _ := ErrToStatus(
		self.app.Server().Hello(self, []byte(request.PingData)))
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"rid": self.logID, "cmd": "hello", "uaid": uaid})
	}
	reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":%d}`,
		header.Type, uaid, status)
	if err = self.WriteText(reply); err != nil {
		if logWarning {
			self.logger.Warn("dash", "Error writing client handshake", LogFields{
				"rid": self.logID, "error": err.Error()})
		}
		return err
	}
	self.metrics.Increment("updates.client.hello")
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("dash", "Client successfully connected",
			LogFields{"rid": self.logID})
	}
	self.state = WorkerActive
	// Get the lastAccessed time from wherever
	return self.Flush(0, "", 0, "")
}

func (self *WorkerWS) handshake(request *HelloRequest) (
	deviceID string, allowRedirect bool, err error) {

	logWarning := self.logger.ShouldLog(WARNING)
	currentID := self.UAID()

	if request.ChannelIDs == nil {
		// Must include "channelIDs" (even if empty)
		if logWarning {
			self.logger.Warn("worker", "Missing ChannelIDs",
				LogFields{"rid": self.logID})
		}
		return "", false, ErrNoParams
	}

	if len(currentID) > 0 {
		if len(request.DeviceID) == 0 || currentID == request.DeviceID {
			// Duplicate handshake with omitted or identical device ID. Allow the
			// caller to flush pending notifications, but avoid querying the balancer.
			if self.logger.ShouldLog(DEBUG) {
				self.logger.Debug("worker", "Duplicate client handshake",
					LogFields{"rid": self.logID})
			}
			return currentID, false, nil
		}
		// if there's already a Uaid for this device, don't accept a new one
		if logWarning {
			self.logger.Warn("worker", "Conflicting UAIDs",
				LogFields{"rid": self.logID})
		}
		return "", false, ErrExistingID
	}
	var (
		prevWorker      Worker
		workerConnected bool
	)
	if len(request.DeviceID) == 0 {
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("worker", "Generating new UAID for device",
				LogFields{"rid": self.logID})
		}
		goto forceReset
	}
	if !id.Valid(request.DeviceID) {
		if logWarning {
			self.logger.Warn("worker", "Invalid character in UAID",
				LogFields{"rid": self.logID})
		}
		return "", false, ErrInvalidID
	}
	if !self.store.CanStore(len(request.ChannelIDs)) {
		// are there a suspicious number of channels?
		if logWarning {
			self.logger.Warn("worker",
				"Too many channel IDs in handshake; resetting UAID", LogFields{
					"rid":      self.logID,
					"uaid":     request.DeviceID,
					"channels": strconv.Itoa(len(request.ChannelIDs))})
		}
		self.store.DropAll(request.DeviceID)
		goto forceReset
	}
	prevWorker, workerConnected = self.app.GetWorker(request.DeviceID)
	if workerConnected {
		if self.logger.ShouldLog(INFO) {
			self.logger.Info("worker", "UAID collision; disconnecting previous client",
				LogFields{"rid": self.logID, "uaid": request.DeviceID})
		}
		self.app.Server().Bye(prevWorker)
	}
	if len(request.ChannelIDs) > 0 && !self.store.Exists(request.DeviceID) {
		if logWarning {
			self.logger.Warn("worker",
				"Channel IDs specified in handshake for nonexistent UAID",
				LogFields{"rid": self.logID, "uaid": request.DeviceID})
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
func (self *WorkerWS) Ack(_ *RequestHeader, message []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.logID,
					"cmd": "ack", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()
	uaid := self.UAID()
	if uaid == "" {
		return ErrNoHandshake
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
		if err = self.store.Drop(uaid, update.ChannelID); err != nil {
			goto logError
		}
	}
	for _, channelID := range request.Expired {
		if err = self.store.Drop(uaid, channelID); err != nil {
			goto logError
		}
	}
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"rid": self.logID, "cmd": "ack"})
	}
	// Get the lastAccessed time from wherever.
	return self.Flush(0, "", 0, "")
logError:
	if self.logger.ShouldLog(WARNING) {
		self.logger.Warn("worker", "sending response",
			LogFields{"rid": self.logID, "cmd": "ack", "error": ErrStr(err)})
	}
	return err
}

// Register a new ChannelID. Optionally, encrypt the endpoint.
func (self *WorkerWS) Register(header *RequestHeader, message []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.logID,
					"cmd": "register", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()

	uaid := self.UAID()
	if uaid == "" {
		return ErrNoHandshake
	}
	request := new(RegisterRequest)
	if err = json.Unmarshal(message, request); err != nil || !id.Valid(request.ChannelID) {
		return ErrInvalidParams
	}
	if err = self.store.Register(uaid, request.ChannelID, 0); err != nil {
		if self.logger.ShouldLog(WARNING) {
			self.logger.Warn("worker", "Register failed, error updating backing store",
				LogFields{"rid": self.logID, "cmd": "register", "error": ErrStr(err)})
		}
		return err
	}
	// have the server generate the callback URL.
	endpoint, err := self.app.Server().Regis(self, request.ChannelID)
	status, _ := ErrToStatus(err)
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "Server returned", LogFields{
			"rid":  self.logID,
			"cmd":  "register",
			"code": strconv.FormatInt(int64(status), 10),
			"chid": request.ChannelID,
			"uaid": uaid})
	}
	// return the info back to the socket
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response", LogFields{
			"rid":          self.logID,
			"cmd":          "register",
			"uaid":         uaid,
			"code":         strconv.FormatInt(int64(status), 10),
			"channelID":    request.ChannelID,
			"pushEndpoint": endpoint})
	}
	self.WriteJSON(RegisterReply{header.Type, uaid, status, request.ChannelID, endpoint})
	self.metrics.Increment("updates.client.register")
	return nil
}

// Unregister a ChannelID.
func (self *WorkerWS) Unregister(header *RequestHeader, message []byte) (err error) {
	logWarning := self.logger.ShouldLog(WARNING)
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && self.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				self.logger.Error("worker", "Unhandled error", LogFields{"rid": self.logID,
					"cmd": "register", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()
	uaid := self.UAID()
	if uaid == "" {
		if logWarning {
			self.logger.Warn("worker", "Unregister failed, missing sock.uaid",
				LogFields{"rid": self.logID})
		}
		return ErrNoHandshake
	}
	request := new(UnregisterRequest)
	if err = json.Unmarshal(message, request); err != nil {
		return ErrInvalidParams
	}
	if len(request.ChannelID) == 0 {
		if logWarning {
			self.logger.Warn("worker", "Unregister failed, missing channelID",
				LogFields{"rid": self.logID})
		}
		return ErrNoParams
	}
	// Always return success for an UNREG.
	if err = self.store.Unregister(uaid, request.ChannelID); err != nil {
		if logWarning {
			self.logger.Warn("worker", "Unregister failed, error updating backing store",
				LogFields{"rid": self.logID, "error": ErrStr(err)})
		}
	} else if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"rid": self.logID, "cmd": "unregister"})
	}
	self.WriteJSON(UnregisterReply{header.Type, 200, request.ChannelID})
	self.metrics.Increment("updates.client.unregister")
	return nil
}

// Dump any records associated with the UAID.
func (self *WorkerWS) Flush(lastAccessed int64, channel string, version int64, data string) (err error) {
	// flush pending data back to Client
	timer := timeNow()
	logWarning := self.logger.ShouldLog(WARNING)
	messageType := "notification"
	uaid := self.UAID()
	defer func() {
		now := timeNow()
		if self.logger.ShouldLog(INFO) {
			self.logger.Info("timer",
				"Client flush completed",
				LogFields{"duration": strconv.FormatInt(int64(now.Sub(timer)), 10),
					"uaid": uaid})
		}
		if err != nil || self.stopped() {
			return
		}
		self.metrics.Timer("client.flush", now.Sub(timer))
	}()
	if uaid == "" {
		if logWarning {
			self.logger.Warn("worker", "Undefined UAID for socket. Aborting.",
				LogFields{"rid": self.logID})
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
		if updates, expired, err = self.store.FetchAll(uaid, time.Unix(lastAccessed, 0)); err != nil {
			if logWarning {
				self.logger.Warn("worker", "Failed to flush Update to client.",
					LogFields{"rid": self.logID, "uaid": uaid, "error": err.Error()})
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
			"rid":     self.logID,
			"updates": fmt.Sprintf("[%s]", strings.Join(logStrings, ", "))})
	}
	self.WriteJSON(reply)
	return nil
}

func (self *WorkerWS) Ping(header *RequestHeader, _ []byte) (err error) {
	now := timeNow()
	if self.pingInt > 0 && !self.lastPing.IsZero() && now.Sub(self.lastPing) < self.pingInt {
		if self.logger.ShouldLog(WARNING) {
			source := self.Origin()
			if len(source) == 0 {
				source = "No Socket Origin"
			}
			self.logger.Warn("dash", "Client sending too many pings",
				LogFields{"rid": self.logID, "source": source})
		}
		self.stop()
		self.metrics.Increment("updates.client.too_many_pings")
		return ErrTooManyPings
	}
	self.lastPing = now
	if self.app.pushLongPongs {
		self.WriteJSON(PingReply{header.Type, 200})
	} else {
		self.WriteText("{}")
	}
	self.metrics.Increment("updates.client.ping")
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
	uaid      string
}

func (r *NoWorker) Close() error        { return nil }
func (r *NoWorker) Born() time.Time     { return time.Time{} }
func (r *NoWorker) UAID() string        { return r.uaid }
func (r *NoWorker) SetUAID(uaid string) { r.uaid = uaid }

func (r *NoWorker) Run() {
	r.Logger.Debug("noworker", "Run", nil)
}

func (r *NoWorker) Flush(lastAccessed int64, channel string, version int64, data string) error {
	r.Logger.Debug("noworker", "Got Flush", LogFields{
		"channel": channel,
		"data":    data,
	})
	r.Outbuffer, _ = json.Marshal(&FlushData{lastAccessed,
		channel, version, data})
	return nil
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
