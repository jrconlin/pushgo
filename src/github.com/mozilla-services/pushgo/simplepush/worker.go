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

func (w *WorkerWS) Born() time.Time     { return w.born }
func (w *WorkerWS) SetUAID(uaid string) { w.uaid = uaid }
func (w *WorkerWS) UAID() string        { return w.uaid }

// ReadDeadline determines the deadline t for the next read. If the handshake
// timeout and pong interval are not set, t is the zero value.
func (w *WorkerWS) ReadDeadline() (t time.Time) {
	switch w.state {
	// For unidentified clients, the deadline is the handshake timeout relative
	// to the socket creation time. This prevents clients from extending the
	// timeout by sending pings.
	case WorkerInactive:
		if w.helloTimeout > 0 {
			t = w.Born().Add(w.helloTimeout)
		}

	// For clients that have completed the handshake, the deadline is the end of
	// the next pong interval.
	case WorkerActive:
		if w.pongInterval > 0 {
			t = timeNow().Add(w.pongInterval)
		}
	}
	return
}

func (w *WorkerWS) sniffer() {
	// Sniff the websocket for incoming data.
	// Reading from the websocket is a blocking operation, and we also
	// need to write out when an even occurs. This isolates the incoming
	// reads to a separate go process.
	logWarning := w.logger.ShouldLog(WARNING)
	buf := new(bytes.Buffer)

	for !w.stopped() {
		buf.Reset()
		w.SetReadDeadline(w.ReadDeadline())
		raw, err := w.ReadBinary()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if w.state == WorkerInactive {
					if w.logger.ShouldLog(DEBUG) {
						w.logger.Debug("worker", "Worker Idle connection. Closing socket",
							LogFields{"rid": w.logID})
					}
					w.stop()
					continue
				}
				if err = w.WriteText("{}"); err == nil {
					continue
				}
			}
			w.stop()
			if err != io.EOF {
				if w.logger.ShouldLog(ERROR) && !harmlessConnectionError(err) {
					w.logger.Error("worker", "Websocket Error",
						LogFields{"rid": w.logID, "error": ErrStr(err)})
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
					w.logger.Warn("worker", "Malformed request payload", LogFields{
						"rid":      w.logID,
						"expected": string(msg[:syntaxErr.Offset]),
						"error":    syntaxErr.Error()})
				} else {
					w.logger.Warn("worker", "Error validating request payload",
						LogFields{"rid": w.logID, "error": ErrStr(err)})
				}
			}
			w.stop()
			continue
		} else {
			msg = buf.Bytes()
		}

		//ignore {} pings for logging purposes.
		if len(msg) > 5 && w.logger.ShouldLog(DEBUG) {
			w.logger.Debug("worker", "Socket receive",
				LogFields{"rid": w.logID, "raw": string(msg)})
		}
		header := new(RequestHeader)
		if isPingBody(msg) {
			header.Type = "ping"
		} else if err = json.Unmarshal(msg, header); err != nil {
			if logWarning {
				w.logger.Warn("worker", "Error parsing request header",
					LogFields{"rid": w.logID, "error": ErrStr(err)})
			}
			w.handleError(msg, ErrInvalidHeader)
			w.stop()
			continue
		}
		switch strings.ToLower(header.Type) {
		case "purge": // No-op for backward compatibility.
		case "ping":
			err = w.Ping(header, msg)
		case "hello":
			err = w.Hello(header, msg)
		case "ack":
			err = w.Ack(header, msg)
		case "register":
			err = w.Register(header, msg)
		case "unregister":
			err = w.Unregister(header, msg)
		default:
			if logWarning {
				w.logger.Warn("worker", "Bad command",
					LogFields{"rid": w.logID, "cmd": header.Type})
			}
			err = ErrUnsupportedType
		}
		if err != nil {
			if w.logger.ShouldLog(DEBUG) {
				w.logger.Debug("worker", "Run returned error",
					LogFields{"rid": w.logID, "cmd": header.Type, "error": ErrStr(err)})
			}
			w.handleError(msg, err)
			w.stop()
			continue
		}
	}
}

func (w *WorkerWS) stopped() bool {
	return w.state == WorkerStopped
}

func (w *WorkerWS) stop() {
	w.state = WorkerStopped
}

// standardize the error reporting back to the client.
func (w *WorkerWS) handleError(message []byte, err error) (ret error) {
	reply := make(map[string]interface{})
	if ret = json.Unmarshal(message, &reply); ret != nil {
		return
	}
	reply["status"], reply["error"] = ErrToStatus(err)
	return w.WriteJSON(reply)
}

// General workhorse loop for the websocket handler.
func (w *WorkerWS) Run() {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && w.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				w.logger.Error("worker", "Unhandled connection error", LogFields{
					"rid":   w.logID,
					"error": ErrStr(err),
					"stack": string(stack[:n])})
			}
		}
		return
	}()

	w.sniffer()

	if w.logger.ShouldLog(INFO) {
		w.logger.Info("worker", "Run has completed a shut-down",
			LogFields{"rid": w.logID})
	}
}

// Associate the UAID for this socket connection (and flush any data that
// may be pending for the connection)
func (w *WorkerWS) Hello(header *RequestHeader, message []byte) (err error) {
	logWarning := w.logger.ShouldLog(WARNING)
	// register the UAID
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && w.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				w.logger.Error("worker", "Unhandled error", LogFields{"rid": w.logID,
					"cmd": "hello", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()

	request := new(HelloRequest)
	if err = json.Unmarshal(message, request); err != nil {
		return ErrInvalidParams
	}
	wroteReply, err := w.registerDevice(header, request)
	if err != nil {
		return err
	}
	if wroteReply {
		w.stop()
		return nil
	}
	uaid := w.UAID()
	if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "sending response",
			LogFields{"rid": w.logID, "cmd": "hello", "uaid": uaid})
	}
	reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":200}`,
		header.Type, uaid)
	if err = w.WriteText(reply); err != nil {
		if logWarning {
			w.logger.Warn("worker", "Error writing client handshake", LogFields{
				"rid": w.logID, "error": err.Error()})
		}
		return err
	}
	w.metrics.Increment("updates.client.hello")
	if w.logger.ShouldLog(INFO) {
		w.logger.Info("worker", "Client successfully connected",
			LogFields{"rid": w.logID})
	}
	w.state = WorkerActive
	// Get the lastAccessed time from wherever
	return w.Flush(0, "", 0, "")
}

// registerDevice adds the worker to the worker map and registers the
// connecting client with the router.
func (w *WorkerWS) registerDevice(header *RequestHeader,
	request *HelloRequest) (wroteReply bool, err error) {

	uaid, allowRedirect, err := w.handshake(request)
	if err != nil {
		return false, err
	}
	w.SetUAID(uaid)
	if allowRedirect {
		if wroteReply = w.checkRedirect(header); wroteReply {
			return
		}
	}
	// register any proprietary connection requirements
	w.registerPropPing([]byte(request.PingData))
	// Add the worker to the map and register with the router.
	if added := w.app.AddWorker(uaid, w); added {
		// Avoid re-registration for duplicate handshakes.
		w.app.Router().Register(uaid)
	}
	w.logger.Info("worker", "Client registered", nil)
	return false, nil
}

// handshake performs the opening handshake.
func (w *WorkerWS) handshake(request *HelloRequest) (
	deviceID string, allowRedirect bool, err error) {

	logWarning := w.logger.ShouldLog(WARNING)
	currentID := w.UAID()

	if request.ChannelIDs == nil {
		// Must include "channelIDs" (even if empty)
		if logWarning {
			w.logger.Warn("worker", "Missing ChannelIDs",
				LogFields{"rid": w.logID})
		}
		return "", false, ErrNoParams
	}

	if len(currentID) > 0 {
		if len(request.DeviceID) == 0 || currentID == request.DeviceID {
			// Duplicate handshake with omitted or identical device ID. Allow the
			// caller to flush pending notifications, but avoid querying the balancer.
			if w.logger.ShouldLog(DEBUG) {
				w.logger.Debug("worker", "Duplicate client handshake",
					LogFields{"rid": w.logID})
			}
			return currentID, false, nil
		}
		// if there's already a Uaid for this device, don't accept a new one
		if logWarning {
			w.logger.Warn("worker", "Conflicting UAIDs",
				LogFields{"rid": w.logID})
		}
		return "", false, ErrExistingID
	}
	var (
		prevWorker      Worker
		workerConnected bool
	)
	if len(request.DeviceID) == 0 {
		if w.logger.ShouldLog(DEBUG) {
			w.logger.Debug("worker", "Generating new UAID for device",
				LogFields{"rid": w.logID})
		}
		goto forceReset
	}
	if !id.Valid(request.DeviceID) {
		if logWarning {
			w.logger.Warn("worker", "Invalid character in UAID",
				LogFields{"rid": w.logID})
		}
		return "", false, ErrInvalidID
	}
	if !w.store.CanStore(len(request.ChannelIDs)) {
		// are there a suspicious number of channels?
		if logWarning {
			w.logger.Warn("worker",
				"Too many channel IDs in handshake; resetting UAID", LogFields{
					"rid":      w.logID,
					"uaid":     request.DeviceID,
					"channels": strconv.Itoa(len(request.ChannelIDs))})
		}
		w.store.DropAll(request.DeviceID)
		goto forceReset
	}
	prevWorker, workerConnected = w.app.GetWorker(request.DeviceID)
	if workerConnected {
		if w.logger.ShouldLog(INFO) {
			w.logger.Info("worker", "UAID collision; disconnecting previous client",
				LogFields{"rid": w.logID, "uaid": request.DeviceID})
		}
		prevWorker.Close()
	}
	if len(request.ChannelIDs) > 0 && !w.store.Exists(request.DeviceID) {
		if logWarning {
			w.logger.Warn("worker",
				"Channel IDs specified in handshake for nonexistent UAID",
				LogFields{"rid": w.logID, "uaid": request.DeviceID})
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

// checkRedirect determines if a connecting client should be redirected to a
// different host. wroteReply indicates whether checkRedirect responded to the
// client; if so, the caller should close the connection.
func (w *WorkerWS) checkRedirect(header *RequestHeader) (wroteReply bool) {
	b := w.app.Balancer()
	if b == nil {
		return false
	}
	uaid := w.UAID()
	origin, shouldRedirect, err := b.RedirectURL()
	if err != nil {
		if w.logger.ShouldLog(WARNING) {
			w.logger.Warn("worker", "Failed to redirect client", LogFields{
				"error": err.Error(), "rid": w.logID, "cmd": header.Type})
		}
		reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":429}`,
			header.Type, uaid)
		w.WriteText(reply)
		return true
	}
	if !shouldRedirect {
		return false
	}
	if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "Redirecting client", LogFields{
			"rid": w.logID, "cmd": header.Type, "origin": origin})
	}
	reply := fmt.Sprintf(`{"messageType":%q,"uaid":%q,"status":307,"redirect":%q}`,
		header.Type, uaid, origin)
	w.WriteText(reply)
	return true
}

// registerPropPing registers the client with the proprietary pinger if one is
// set and connect is not empty.
func (w *WorkerWS) registerPropPing(connect []byte) (err error) {
	pinger := w.app.PropPinger()
	if pinger == nil || len(connect) == 0 {
		return nil
	}
	uaid := w.UAID()
	if err = pinger.Register(uaid, connect); err != nil {
		if w.logger.ShouldLog(WARNING) {
			w.logger.Warn("worker", "Could not set proprietary info",
				LogFields{"error": err.Error(),
					"connect": string(connect)})
		}
		return err
	}
	return nil
}

// Clear the data that the client stated it received, then re-flush any
// records (including new data)
func (w *WorkerWS) Ack(_ *RequestHeader, message []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && w.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				w.logger.Error("worker", "Unhandled error", LogFields{"rid": w.logID,
					"cmd": "ack", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()
	uaid := w.UAID()
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
	w.metrics.Increment("updates.client.ack")
	for _, update := range request.Updates {
		if err = w.store.Drop(uaid, update.ChannelID); err != nil {
			goto logError
		}
	}
	for _, channelID := range request.Expired {
		if err = w.store.Drop(uaid, channelID); err != nil {
			goto logError
		}
	}
	if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "sending response",
			LogFields{"rid": w.logID, "cmd": "ack"})
	}
	// Get the lastAccessed time from wherever.
	return w.Flush(0, "", 0, "")
logError:
	if w.logger.ShouldLog(WARNING) {
		w.logger.Warn("worker", "sending response",
			LogFields{"rid": w.logID, "cmd": "ack", "error": ErrStr(err)})
	}
	return err
}

// Register a new ChannelID. Optionally, encrypt the endpoint.
func (w *WorkerWS) Register(header *RequestHeader, message []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && w.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				w.logger.Error("worker", "Unhandled error", LogFields{"rid": w.logID,
					"cmd": "register", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()

	uaid := w.UAID()
	if uaid == "" {
		return ErrNoHandshake
	}
	request := new(RegisterRequest)
	if err = json.Unmarshal(message, request); err != nil || !id.Valid(request.ChannelID) {
		return ErrInvalidParams
	}
	if err = w.store.Register(uaid, request.ChannelID, 0); err != nil {
		if w.logger.ShouldLog(WARNING) {
			w.logger.Warn("worker", "Register failed, error updating backing store",
				LogFields{"rid": w.logID, "cmd": "register", "error": ErrStr(err)})
		}
		return err
	}
	key, err := w.store.IDsToKey(uaid, request.ChannelID)
	if err != nil {
		if w.logger.ShouldLog(WARNING) {
			w.logger.Warn("worker", "Error generating primary key",
				LogFields{"rid": w.logID, "cmd": "register", "error": ErrStr(err)})
		}
		return err
	}
	endpoint, err := w.app.CreateEndpoint(key)
	if err != nil {
		if w.logger.ShouldLog(WARNING) {
			w.logger.Warn("worker", "Error registering endpoint", LogFields{
				"rid":   w.logID,
				"uaid":  uaid,
				"chid":  request.ChannelID,
				"error": ErrStr(err)})
		}
		return err
	}
	status, _ := ErrToStatus(err)
	if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "Server returned", LogFields{
			"rid":  w.logID,
			"cmd":  "register",
			"code": strconv.FormatInt(int64(status), 10),
			"chid": request.ChannelID,
			"uaid": uaid})
	}
	// return the info back to the socket
	if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "sending response", LogFields{
			"rid":          w.logID,
			"cmd":          "register",
			"uaid":         uaid,
			"code":         strconv.FormatInt(int64(status), 10),
			"channelID":    request.ChannelID,
			"pushEndpoint": endpoint})
	}
	w.WriteJSON(RegisterReply{header.Type, uaid, status, request.ChannelID, endpoint})
	w.metrics.Increment("updates.client.register")
	return nil
}

// Unregister a ChannelID.
func (w *WorkerWS) Unregister(header *RequestHeader, message []byte) (err error) {
	logWarning := w.logger.ShouldLog(WARNING)
	defer func() {
		if r := recover(); r != nil {
			if err, _ := r.(error); err != nil && w.logger.ShouldLog(ERROR) {
				stack := make([]byte, 1<<16)
				n := runtime.Stack(stack, false)
				w.logger.Error("worker", "Unhandled error", LogFields{"rid": w.logID,
					"cmd": "register", "error": ErrStr(err), "stack": string(stack[:n])})
			}
			err = ErrInvalidParams
		}
	}()
	uaid := w.UAID()
	if uaid == "" {
		if logWarning {
			w.logger.Warn("worker", "Unregister failed, missing sock.uaid",
				LogFields{"rid": w.logID})
		}
		return ErrNoHandshake
	}
	request := new(UnregisterRequest)
	if err = json.Unmarshal(message, request); err != nil {
		return ErrInvalidParams
	}
	if len(request.ChannelID) == 0 {
		if logWarning {
			w.logger.Warn("worker", "Unregister failed, missing channelID",
				LogFields{"rid": w.logID})
		}
		return ErrNoParams
	}
	// Always return success for an UNREG.
	if err = w.store.Unregister(uaid, request.ChannelID); err != nil {
		if logWarning {
			w.logger.Warn("worker", "Unregister failed, error updating backing store",
				LogFields{"rid": w.logID, "error": ErrStr(err)})
		}
	} else if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "sending response",
			LogFields{"rid": w.logID, "cmd": "unregister"})
	}
	w.WriteJSON(UnregisterReply{header.Type, 200, request.ChannelID})
	w.metrics.Increment("updates.client.unregister")
	return nil
}

// Dump any records associated with the UAID.
func (w *WorkerWS) Flush(lastAccessed int64, channel string,
	version int64, data string) (err error) {

	// flush pending data back to Client
	timer := timeNow()
	uaid := w.UAID()
	defer func() {
		now := timeNow()
		if w.logger.ShouldLog(INFO) {
			w.logger.Info("worker",
				"Client flush completed",
				LogFields{"duration": strconv.FormatInt(int64(now.Sub(timer)), 10),
					"uaid": uaid})
		}
		if err != nil || w.stopped() {
			return
		}
		w.metrics.Timer("client.flush", now.Sub(timer))
	}()
	if uaid == "" {
		if w.logger.ShouldLog(WARNING) {
			w.logger.Warn("worker", "Undefined UAID for socket. Aborting.",
				LogFields{"rid": w.logID})
		}
		// Have the server clean up records associated with this UAID.
		// (Probably "none", but still good for housekeeping)
		w.stop()
		return nil
	}
	if len(channel) > 0 {
		// if we have a channel, don't flush. we can get them later in the ACK
		return w.flushUpdate(channel, version, data)
	}
	return w.flushPending(lastAccessed)
}

// flushUpdate writes an update containing chid, version, and data.
func (w *WorkerWS) flushUpdate(chid string, version int64, data string) (
	err error) {

	uaid := w.UAID()
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		pinger := w.app.PropPinger()
		if pinger != nil {
			pinger.Send(uaid, version, data)
		}
		err = fmt.Errorf("Error requesting flush: %#v", r)
		if w.logger.ShouldLog(ERROR) {
			stack := make([]byte, 1<<16)
			n := runtime.Stack(stack, false)
			w.logger.Error("worker", "Panic flushing update",
				LogFields{"error": ErrStr(err),
					"uaid":  uaid,
					"stack": string(stack[:n])})
		}
	}()
	if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "Flushing update to client", LogFields{
			"rid":     w.logID,
			"uaid":    uaid,
			"chid":    chid,
			"version": strconv.FormatInt(version, 10),
		})
	}
	// hand craft a notification update to the client.
	// TODO: allow bulk updates.
	updates := []Update{{chid, uint64(version), data}}
	w.WriteJSON(FlushReply{"notification", updates, nil})
	w.metrics.Increment("updates.sent")
	return nil
}

// flushPending writes all pending updates since the lastAccessed time,
// expressed in seconds since Epoch.
func (w *WorkerWS) flushPending(lastAccessed int64) (err error) {
	uaid := w.UAID()
	updates, expired, err := w.store.FetchAll(uaid, time.Unix(lastAccessed, 0))
	if err != nil {
		if w.logger.ShouldLog(WARNING) {
			w.logger.Warn("worker", "Failed to flush pending updates to client",
				LogFields{"rid": w.logID, "uaid": uaid, "error": ErrStr(err)})
		}
		return err
	}
	if len(updates) == 0 && len(expired) == 0 {
		return nil
	}
	if w.logger.ShouldLog(DEBUG) {
		logStrings := make([]string, len(updates))
		for i, update := range updates {
			logStrings[i] = fmt.Sprintf(">> %s.%s = %d", uaid,
				update.ChannelID, update.Version)
		}
		w.logger.Debug("worker", "Flushing pending updates to client", LogFields{
			"rid":     w.logID,
			"updates": fmt.Sprintf("[%s]", strings.Join(logStrings, ", "))})
	}
	w.WriteJSON(FlushReply{"notification", updates, expired})
	w.metrics.IncrementBy("updates.sent", int64(len(updates)))
	return nil
}

func (w *WorkerWS) Ping(header *RequestHeader, _ []byte) (err error) {
	now := timeNow()
	if w.pingInt > 0 && !w.lastPing.IsZero() && now.Sub(w.lastPing) < w.pingInt {
		if w.logger.ShouldLog(WARNING) {
			source := w.Origin()
			if len(source) == 0 {
				source = "No Socket Origin"
			}
			w.logger.Warn("worker", "Client sending too many pings",
				LogFields{"rid": w.logID, "source": source})
		}
		w.stop()
		w.metrics.Increment("updates.client.too_many_pings")
		return ErrTooManyPings
	}
	w.lastPing = now
	if w.app.pushLongPongs {
		w.WriteJSON(PingReply{header.Type, 200})
	} else {
		w.WriteText("{}")
	}
	w.metrics.Increment("updates.client.ping")
	return nil
}

// Close removes worker from the worker map, deregisters the client from the
// router, and closes the underlying socket. Invoking Close multiple times for
// the same worker is a no-op.
func (w *WorkerWS) Close() error {
	now := timeNow()
	uaid := w.UAID()
	if w.logger.ShouldLog(DEBUG) {
		w.logger.Debug("worker", "Cleaning up socket",
			LogFields{"uaid": uaid})
	}
	if w.logger.ShouldLog(INFO) {
		w.logger.Info("worker", "Socket connection terminated",
			LogFields{
				"uaid":     uaid,
				"duration": strconv.FormatInt(int64(now.Sub(w.Born())), 10)})
	}
	// NOTE: in instances where proprietary wake-ups are issued, you may
	// wish not to delete the worker from the map, since this is the only
	// way to note a record needs waking.
	//
	// For that matter, you may wish to store the Proprietary wake data to
	// something commonly shared (like memcache) so that the device can be
	// woken when not connected.
	if removed := w.app.RemoveWorker(uaid, w); removed {
		w.app.Router().Unregister(uaid)
	}
	return w.Socket.Close()
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
