/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.google.com/p/go.net/websocket"

	"mozilla.org/simplepush/sperrors"
)

var MissingChannelErr = errors.New("Missing channelID")
var BadUAIDErr = errors.New("Bad UAID")

//    -- Workers
//      these write back to the websocket.

type Worker struct {
	app          *Application
	logger       *SimpleLogger
	state        int
	filter       *regexp.Regexp
	stopped      bool
	maxChannels  int
	lastPing     time.Time
	pingInt      int
	wg           *sync.WaitGroup
	metrics      *Metrics
	helloTimeout time.Duration
}

const (
	INACTIVE = 0
	ACTIVE   = 1
)

const (
	UAID_MAX_LEN         = 100
	CHID_MAX_LEN         = 100
	CHID_DEFAULT_MAX_NUM = 200
)

// Allow [0-9a-z_-]/i as valid ChannelID characters.
var workerFilter *regexp.Regexp = regexp.MustCompile("[^a-fA-F0-9\\-]")

func NewWorker(app *Application) *Worker {
	var maxChannels int
	var pingInterval int

	maxChannels = app.Store().MaxChannels()
	pingInterval = int(app.clientMinPing.Seconds())

	return &Worker{
		app:          app,
		logger:       app.Logger(),
		metrics:      app.Metrics(),
		state:        INACTIVE,
		filter:       workerFilter,
		stopped:      false,
		lastPing:     time.Now(),
		pingInt:      pingInterval,
		maxChannels:  maxChannels,
		wg:           new(sync.WaitGroup),
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
				time.Now().Sub(sock.Born).Nanoseconds())
			return
		}
		err = websocket.Message.Receive(socket, &raw)
		if err != nil {
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
				switch mt.(type) {
				case string:
					messageType = mt.(string)
				default:
					messageType = ""
				}
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
						LogFields{"messageType": buffer["messageType"].(string)})
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
					messageType, err.Error())
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
				self.logger.Error("dash",
					"Worker Idle connection. Closing socket", nil)
				sock.Socket.Close()
			}
		})

	defer func(sock *PushWS) {
		if r := recover(); r != nil {
			self.logger.Error("worker", r.(error).Error(), nil)
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
			self.logger.Error("worker",
				"Unhandled error",
				LogFields{"cmd": "hello", "error": r.(error).Error()})
			err = sperrors.InvalidDataError
		}
	}()

	//Force the client to re-register all it's clients.
	// This is done by returning a new UAID.
	forceReset := false

	var suggestedUAID string

	data := buffer.(JsMap)
	if _, ok := data["uaid"]; !ok {
		// Must include "uaid" (even if blank)
		data["uaid"] = ""
	}
	/* NOTE: This seems to be a redirect, which I don't believe we support
	if redir := self.config.Get("db.redirect", ""); len(redir) > 0 {
		resp := JsMap{
			"messageType": data["messageType"],
			"status":      302,
			"redirect":    redir,
			"uaid":        sock.Uaid}
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("worker", "sending redirect",
				LogFields{"messageType": data["messageType"].(string),
					"status":   strconv.FormatInt(data["status"].(int64), 10),
					"redirect": data["redirect"].(string),
					"uaid":     data["uaid"].(string)})
		}
		websocket.JSON.Send(sock.Socket, resp)
		return nil
	} */
	suggestedUAID = data["uaid"].(string)
	if data["channelIDs"] == nil {
		// Must include "channelIDs" (even if empty)
		self.logger.Debug("worker", "Missing ChannelIDs", nil)
		return sperrors.MissingDataError
	}
	if len(sock.Uaid) > 0 &&
		len(data["uaid"].(string)) > 0 &&
		sock.Uaid != suggestedUAID {
		// if there's already a Uaid for this channel, don't accept a new one
		self.logger.Debug("worker", "Conflicting UAIDs", nil)
		return sperrors.InvalidChannelError
	}
	if self.filter.Find([]byte(strings.ToLower(suggestedUAID))) != nil {
		self.logger.Debug("worker", "Invalid character in UAID", nil)
		return sperrors.InvalidChannelError
	}
	if len(sock.Uaid) == 0 {
		// if there's no UAID for the socket, accept or create a new one.
		sock.Uaid = suggestedUAID
		if len(sock.Uaid) > UAID_MAX_LEN {
			self.logger.Debug("worker", "UAID is too long", nil)
			return sperrors.InvalidDataError
		}
		if len(sock.Uaid) == 0 {
			forceReset = forceReset || true
		}
		if self.app.ClientExists(sock.Uaid) {
			forceReset = true
		}
		if num := len(data["channelIDs"].([]interface{})); num > 0 {
			// are there a suspicious number of channels?
			if num > self.maxChannels {
				forceReset = forceReset || true
			}
			if !sock.Store.Exists(sock.Uaid) {
				forceReset = forceReset || true
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
		sock.Uaid, _ = GenUUID4()
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
	raw_result, args := self.app.Server().HandleCommand(cmd, sock)
	result := PushCommand{raw_result, args}

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"cmd": "hello", "error": ErrStr(err),
				"uaid": sock.Uaid})
	}
	// websocket.JSON.Send(sock.Socket, JsMap{
	// 	"messageType": data["messageType"],
	// 	"status":      result.Command,
	// 	"uaid":        sock.Uaid})
	msg := []byte("{\"messageType\":\"" + data["messageType"].(string) +
		"\",\"status\":" + strconv.FormatInt(int64(result.Command), 10) +
		",\"uaid\":\"" + sock.Uaid + "\"}")
	_, err = sock.Socket.Write(msg)
	self.metrics.Increment("updates.client.hello")
	self.logger.Info("dash", "Client successfully connected", nil)
	self.state = ACTIVE
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
			debug.PrintStack()
			self.logger.Error("worker",
				"Unhandled error",
				LogFields{"cmd": "ack", "error": r.(error).Error()})
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
			self.logger.Error("worker",
				"Unhandled error",
				LogFields{"cmd": "register", "error": ErrStr(r.(error))})
			debug.PrintStack()
			err = sperrors.InvalidDataError
		}
	}()

	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data := buffer.(JsMap)
	if data["channelID"] == nil {
		return sperrors.InvalidDataError
	}
	appid := data["channelID"].(string)
	if len(appid) > CHID_MAX_LEN {
		return sperrors.InvalidDataError
	}
	if self.filter.FindStringIndex(strings.ToLower(appid)) != nil {
		return sperrors.InvalidDataError
	}
	err = sock.Store.Register(sock.Uaid, appid, 0)
	if err != nil {
		self.logger.Error("worker",
			fmt.Sprintf("ERROR: Register failed %s", err),
			nil)
		return err
	}
	// have the server generate the callback URL.
	cmd := PushCommand{Command: REGIS, Arguments: data}
	raw_result, args := self.app.Server().HandleCommand(cmd, sock)
	result := PushCommand{raw_result, args}
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker",
			"Server returned", LogFields{"Command": strconv.FormatInt(int64(result.Command), 10),
				"args.channelID": IStr(args["channelID"]),
				"args.uaid":      IStr(args["uaid"])})
	}
	endpoint := result.Arguments.(JsMap)["push.endpoint"].(string)
	// return the info back to the socket
	reply := JsMap{"messageType": data["messageType"],
		"uaid":         sock.Uaid,
		"status":       200,
		"channelID":    data["channelID"],
		"pushEndpoint": endpoint}
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response", LogFields{
			"messageType":  reply["messageType"].(string),
			"uaid":         reply["uaid"].(string),
			"status":       strconv.FormatInt(int64(reply["status"].(int)), 10),
			"channelID":    reply["channelID"].(string),
			"pushEndpoint": reply["pushEndpoint"].(string)})
	}
	websocket.JSON.Send(sock.Socket, reply)
	self.metrics.Increment("updates.client.register")
	return err
}

// Unregister a ChannelID.
func (self *Worker) Unregister(sock *PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			self.logger.Error("worker",
				"Unhandled error",
				LogFields{"cmd": "register", "error": r.(error).Error()})
			err = sperrors.InvalidDataError
		}
	}()
	if sock.Uaid == "" {
		self.logger.Error("worker",
			"Unregister failed, missing sock.uaid", nil)
		return sperrors.InvalidCommandError
	}
	data := buffer.(JsMap)
	if data["channelID"] == nil {
		self.logger.Error("worker",
			"Unregister failed, missing channelID", nil)
		return sperrors.MissingDataError
	}
	appid := data["channelID"].(string)
	// Always return success for an UNREG.
	sock.Store.Unregister(sock.Uaid, appid)
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "sending response",
			LogFields{"cmd": "unregister", "error": ErrStr(err)})
	}
	websocket.JSON.Send(sock.Socket, JsMap{
		"messageType": data["messageType"],
		"status":      200,
		"channelID":   appid})
	self.metrics.Increment("updates.client.unregister")
	return err
}

// Dump any records associated with the UAID.
func (self *Worker) Flush(sock *PushWS, lastAccessed int64, channel string, version int64) (err error) {
	// flush pending data back to Client
	messageType := "notification"
	timer := time.Now()
	defer func(timer time.Time, sock *PushWS) {
		if sock.Logger != nil {
			sock.Logger.Info("timer",
				"Client flush completed",
				LogFields{"duration": strconv.FormatInt(time.Now().Sub(timer).Nanoseconds(), 10),
					"uaid": sock.Uaid})
		}
		if self.metrics != nil {
			self.metrics.Timer("client.flush",
				time.Now().Unix()-timer.Unix())
		}
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
		reply   JsMap
	)
	mod := false
	// if we have a channel, don't flush. we can get them later in the ACK
	if len(channel) == 0 {
		var expired []string
		updates, expired, err = sock.Store.FetchAll(sock.Uaid, time.Unix(lastAccessed, 0))
		if err != nil {
			self.handleError(sock, JsMap{"messageType": messageType}, err)
			return err
		}
		if len(updates) > 0 || len(expired) > 0 {
			reply = JsMap{"updates": updates, "expired": expired}
		}
	} else {
		// hand craft a notification update to the client.
		// TODO: allow bulk updates.
		updates = []Update{Update{channel, uint64(version)}}
		reply = JsMap{"updates": updates}
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

	reply["messageType"] = messageType
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("worker", "Flushing data back to socket",
			LogFields{"updates": "[" + strings.Join(logStrings, ", ") + "]"})
	}
	websocket.JSON.Send(sock.Socket, reply)
	return nil
}

func (self *Worker) Ping(sock *PushWS, buffer interface{}) (err error) {
	if self.pingInt > 0 && int(self.lastPing.Sub(time.Now()).Seconds()) < self.pingInt {
		source := sock.Socket.Config().Origin
		self.logger.Error("dash", "Client sending too many pings",
			LogFields{"source": source.String()})
		self.stopped = true
		self.metrics.Increment("updates.client.too_many_pings")
		return sperrors.TooManyPingsError
	}
	data := buffer.(JsMap)
	if self.app.pushLongPongs {
		websocket.JSON.Send(sock.Socket, JsMap{
			"messageType": data["messageType"],
			"status":      200})
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
