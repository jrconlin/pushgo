/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush/sperrors"
	mozutil "mozilla.org/util"
	"runtime/debug"

	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var MissingChannelErr = errors.New("Missing channelID")
var BadUAIDErr = errors.New("Bad UAID")

//    -- Workers
//      these write back to the websocket.

type Worker struct {
	log     *mozutil.HekaLogger
	state   int
	filter  *regexp.Regexp
	config  mozutil.JsMap
	stopped bool
	wg      *sync.WaitGroup
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
var workerFilter *regexp.Regexp = regexp.MustCompile("[^\\w-]")

func NewWorker(config mozutil.JsMap) *Worker {
	switch config["db.max_channels"].(type) {
	case string:
		vi, _ := config["db.max_channels"]
		if val, err := strconv.Atoi(vi.(string)); err != nil {
			config["db.max_channels"] = val
		}
	case int:
		config["db.max_channels"] = config["db.max_channels"].(int)
	case int16:
		config["db.max_channels"] = int(config["db.max_channels"].(int16))
	case int32:
		config["db.max_channels"] = int(config["db.max_channels"].(int32))
	case int64:
		config["db.max_channels"] = int(config["db.max_channels"].(int64))
	default:
		config["db.max_channels"] = CHID_DEFAULT_MAX_NUM
	}

	return &Worker{
		log:     mozutil.NewHekaLogger(config),
		state:   INACTIVE,
		filter:  workerFilter,
		config:  config,
		stopped: false,
		wg:      new(sync.WaitGroup)}
}

func (self *Worker) sniffer(sock PushWS, in chan mozutil.JsMap, stopChan chan bool) {
	// Sniff the websocket for incoming data.
	// Reading from the websocket is a blocking operation, and we also
	// need to write out when an even occurs. This isolates the incoming
	// reads to a separate go process.
	var socket = sock.Socket
	for {
		var raw []byte
		var buffer mozutil.JsMap

		// Were we told to shut down?
		if self.stopped {
			// Notify the main worker loop in case it didn't see the
			// connection drop
			close(stopChan)

			// Indicate we shut down successfully
			self.wg.Done()
			return
		}

		err := websocket.Message.Receive(socket, &raw)
		if err != nil {
			self.log.Error("worker",
				"Websocket Error",
				mozutil.JsMap{"error": err.Error()})
			self.stopped = true
			continue
		}
		if len(raw) > 0 {
			if len(raw) > 5 {
				self.log.Info("worker",
					"Socket receive",
					mozutil.JsMap{"raw": string(raw)})
			}
			err := json.Unmarshal(raw, &buffer)
			if err != nil {
				self.log.Error("worker",
					"Unparsable data", mozutil.JsMap{"raw": raw})
				self.stopped = true
				continue
			}
			if len(buffer) > 10 {
				self.log.Info("worker",
					"Socket send",
					mozutil.JsMap{"raw": buffer})
			}
			// Only do something if there's something to do.
			in <- buffer
		}
	}
}

// standardize the error reporting back to the client.
func (self *Worker) handleError(sock PushWS, message mozutil.JsMap, err error) (ret error) {
	self.log.Info("worker", "Sending error", mozutil.JsMap{"error": err})
	message["status"], message["error"] = sperrors.ErrToStatus(err)
	return websocket.JSON.Send(sock.Socket, message)
}

// General workhorse loop for the websocket handler.
func (self *Worker) Run(sock PushWS) {
	var err error

	// Instantiate a websocket reader, a blocking operation
	// (Remember, we need to be able to write out PUSH events
	// as they happen.)
	in := make(chan mozutil.JsMap)

	// Setup the sniffer goroutine and increment a waitgroup for it
	// along with a stopChan so it can notify us if it shut down
	// suddenly
	self.wg.Add(1)
	stopChan := make(chan bool)
	go self.sniffer(sock, in, stopChan)

	if timeout_s, ok := self.config["socket.hello_timeout"]; ok {
		timeout, _ := time.ParseDuration(timeout_s.(string))
		time.AfterFunc(timeout,
			func() {
				if sock.Uaid == "" {
					self.log.Error("worker", "Idle hello. Closing socket", nil)
					sock.Socket.Close()
				}
			})
	}

	defer func(sock PushWS) {
		if r := recover(); r != nil {
			sock.Logger.Error("worker", r.(error).Error(), nil)
		}
		sock.Socket.Close()
		return
	}(sock)

	// Indicate we will accept a command
	sock.Acmd <- true

	for {
		// We should shut down?
		if self.stopped {
			// Closing the socket should interrupt the sniffer if it's
			// still running so that it shuts down
			sock.Socket.Close()

			// Pull any remaining commands off, ensure we don't wait around
			select {
			case <-sock.Ccmd:
				self.log.Info("worker", "Cleared messages from socket", nil)
			default:
			}
			break
		}

		select {
		case <-stopChan:
			// Notified by the sniffer that the connection is lost, which
			// means stopped is set to true
			continue
		case cmd := <-sock.Ccmd:
			// A new Push has happened. Flush out the data to the
			// device (and potentially remotely wake it if that fails)
			self.log.Info("worker",
				"Client cmd",
				mozutil.JsMap{"cmd": cmd.Command})

			if cmd.Command == FLUSH {
				self.log.Info("worker",
					fmt.Sprintf("Flushing... %s", sock.Uaid), nil)
				if self.Flush(sock, 0) != nil {
					break
				}
				// additional non-client commands are TBD.
			}
			// Indicate we will accept a command
			sock.Acmd <- true

		case buffer := <-in:
			if len(buffer) > 0 {
				self.log.Info("worker",
					fmt.Sprintf("Client Read buffer, %s %d\n", buffer,
						len(buffer)), nil)
			}
			if len(buffer) == 0 {
				// Empty buffers are "pings"
				buffer["messageType"] = "ping"
			}
			// process the client commands
			var messageType string
			if mt, ok := buffer["messageType"]; !ok {
				self.log.Info("worker", "Invalid message",
					mozutil.JsMap{"reason": "Missing messageType",
						"data": buffer})
				self.handleError(sock,
					mozutil.JsMap{},
					sperrors.UnknownCommandError)
				break
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
				err = self.Hello(&sock, buffer)
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
				self.log.Warn("worker",
					"Bad command",
					buffer)
				err = sperrors.UnknownCommandError
			}
			if err != nil {
				self.log.Debug("worker", "Run returned error", nil)
				self.handleError(sock, buffer, err)
				self.stopped = true
				continue
			}
		}
	}
	self.log.Debug("worker", "Waiting for sniffer to shut-down", nil)
	self.wg.Wait()
	self.log.Debug("worker", "Run has completed a shut-down", nil)
}

// Associate the UAID for this socket connection (and flush any data that
// may be pending for the connection)
func (self *Worker) Hello(sock *PushWS, buffer interface{}) (err error) {
	// register the UAID
	defer func() {
		if r := recover(); r != nil {
			debug.PrintStack()
			self.log.Error("worker",
				"Unhandled error",
				mozutil.JsMap{"cmd": "hello", "error": r})
			err = sperrors.InvalidDataError
		}
	}()

	//Force the client to re-register all it's clients.
	// This is done by returning a new UAID.
	forceReset := false

	var suggestedUAID string

	data := buffer.(mozutil.JsMap)
	if _, ok := data["uaid"]; !ok {
		// Must include "uaid" (even if blank)
		data["uaid"] = ""
	}
	if redir, ok := self.config["db.redirect"]; ok {
		resp := mozutil.JsMap{
			"messageType": data["messageType"],
			"status":      302,
			"redirect":    redir,
			"uaid":        sock.Uaid}
		self.log.Debug("worker", "sending redirect", resp)
		websocket.JSON.Send(sock.Socket, resp)
		return nil
	}
	suggestedUAID = data["uaid"].(string)
	if data["channelIDs"] == nil {
		// Must include "channelIDs" (even if empty)
		self.log.Debug("worker", "Missing ChannelIDs", nil)
		return sperrors.MissingDataError
	}
	if len(sock.Uaid) > 0 &&
		len(data["uaid"].(string)) > 0 &&
		sock.Uaid != suggestedUAID {
		// if there's already a Uaid for this channel, don't accept a new one
		self.log.Debug("worker", "Conflicting UAIDs", nil)
		return sperrors.InvalidChannelError
	}
	if self.filter.Find([]byte(strings.ToLower(suggestedUAID))) != nil {
		self.log.Debug("worker", "Invalid character in UAID", nil)
		return sperrors.InvalidChannelError
	}
	if len(sock.Uaid) == 0 {
		// if there's no UAID for the socket, accept or create a new one.
		sock.Uaid = suggestedUAID
		if len(sock.Uaid) > UAID_MAX_LEN {
			self.log.Debug("worker", "UAID is too long", nil)
			return sperrors.InvalidDataError
		}
		if len(sock.Uaid) == 0 {
			forceReset = forceReset || true
		}
		if num := len(data["channelIDs"].([]interface{})); num > 0 {
			// are there a suspicious number of channels?
			if num > self.config["db.max_channels"].(int) {
				forceReset = forceReset || true
			}
			if !sock.Store.IsKnownUaid(sock.Uaid) {
				forceReset = forceReset || true
			}
		}
	}
	if forceReset {
		self.log.Warn("worker", "Resetting UAID for device",
			mozutil.JsMap{"uaid": sock.Uaid})
		if len(sock.Uaid) > 0 {
			sock.Store.PurgeUAID(sock.Uaid)
		}
		sock.Uaid, _ = mozutil.GenUUID4()
	}
	// register the sockets (NOOP)
	// register any proprietary connection requirements
	// alert the master of the new UAID.
	cmd := PushCommand{
		Command: HELLO,
		Arguments: mozutil.JsMap{
			"uaid":  sock.Uaid,
			"chids": data["channelIDs"]},
	}
	// blocking call back to the boss.
	raw_result, args := HandleServerCommand(cmd, sock)
	result := PushCommand{raw_result, args}
	if err = sock.Store.SetUAIDHost(sock.Uaid); err != nil {
		return err
	}

	self.log.Debug("worker", "sending response",
		mozutil.JsMap{"cmd": "hello", "error": err,
			"uaid": sock.Uaid})
	websocket.JSON.Send(sock.Socket, mozutil.JsMap{
		"messageType": data["messageType"],
		"status":      result.Command,
		"uaid":        sock.Uaid})
	self.state = ACTIVE
	if err == nil {
		// Get the lastAccessed time from wherever
		return self.Flush(*sock, 0)
	}
	return err
}

// Clear the data that the client stated it received, then re-flush any
// records (including new data)
func (self *Worker) Ack(sock PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			self.log.Error("worker",
				"Unhandled error",
				mozutil.JsMap{"cmd": "ack", "error": r})
			err = sperrors.InvalidDataError
		}
	}()

	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data := buffer.(mozutil.JsMap)
	if data["updates"] == nil {
		return sperrors.MissingDataError
	}
	err = sock.Store.Ack(sock.Uaid, data)
	// Get the lastAccessed time from wherever.
	if err == nil {
		return self.Flush(sock, 0)
	}
	self.log.Debug("worker", "sending response",
		mozutil.JsMap{"cmd": "ack", "error": err})
	return err
}

// Register a new ChannelID. Optionally, encrypt the endpoint.
func (self *Worker) Register(sock PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			self.log.Error("worker",
				"Unhandled error",
				mozutil.JsMap{"cmd": "register", "error": r})
			err = sperrors.InvalidDataError
		}
	}()

	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data := buffer.(mozutil.JsMap)
	if data["channelID"] == nil {
		return sperrors.InvalidDataError
	}
	appid := data["channelID"].(string)
	if len(appid) > CHID_MAX_LEN {
		return sperrors.InvalidDataError
	}
	if self.filter.Find([]byte(strings.ToLower(appid))) != nil {
		return sperrors.InvalidDataError
	}
	err = sock.Store.RegisterAppID(sock.Uaid, appid, 0)
	if err != nil {
		self.log.Error("worker",
			fmt.Sprintf("ERROR: RegisterAppID failed %s", err),
			nil)
		return err
	}
	// have the server generate the callback URL.
	cmd := PushCommand{Command: REGIS, Arguments: data}
	raw_result, args := HandleServerCommand(cmd, &sock)
	result := PushCommand{raw_result, args}
	self.log.Debug("worker", fmt.Sprintf("Server returned %s", result), nil)
	endpoint := result.Arguments.(mozutil.JsMap)["pushEndpoint"].(string)
	// return the info back to the socket
	reply := mozutil.JsMap{"messageType": data["messageType"],
		"uaid":         sock.Uaid,
		"status":       200,
		"channelID":    data["channelID"],
		"pushEndpoint": endpoint}
	self.log.Debug("worker", "sending response", reply)
	websocket.JSON.Send(sock.Socket, reply)
	return err
}

// Unregister a ChannelID.
func (self *Worker) Unregister(sock PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			self.log.Error("worker",
				"Unhandled error",
				mozutil.JsMap{"cmd": "register", "error": r})
			err = sperrors.InvalidDataError
		}
	}()
	if sock.Uaid == "" {
		self.log.Error("worker", "Unregister failed, missing sock.uaid", nil)
		return sperrors.InvalidCommandError
	}
	data := buffer.(mozutil.JsMap)
	if data["channelID"] == nil {
		self.log.Error("worker", "Unregister failed, missing channelID", nil)
		return sperrors.MissingDataError
	}
	appid := data["channelID"].(string)
	// Always return success for an UNREG.
	sock.Store.DeleteAppID(sock.Uaid, appid, false)
	self.log.Debug("worker", "sending response",
		mozutil.JsMap{"cmd": "unregister", "error": err})
	websocket.JSON.Send(sock.Socket, mozutil.JsMap{
		"messageType": data["messageType"],
		"status":      200,
		"channelID":   appid})
	return err
}

// Dump any records associated with the UAID.
func (self *Worker) Flush(sock PushWS, lastAccessed int64) error {
	// flush pending data back to Client
	messageType := "notification"
	timer := time.Now()
	defer func(timer time.Time, sock PushWS) {
		sock.Logger.Info("timer",
			"Client flush completed",
			mozutil.JsMap{"duration": time.Now().Sub(timer).Nanoseconds(),
				"uaid": sock.Uaid})
	}(timer, sock)
	if sock.Uaid == "" {
		self.log.Error("worker", "Undefined UAID for socket. Aborting.", nil)
		// Have the server clean up records associated with this UAID.
		// (Probably "none", but still good for housekeeping)
		self.stopped = true
		return nil
	}
	// Fetch the pending updates from #storage
	updates, err := sock.Store.GetUpdates(sock.Uaid, lastAccessed)
	if err != nil {
		self.handleError(sock, mozutil.JsMap{"messageType": messageType}, err)
		return err
	}
	if updates == nil {
		return nil
	}
	updates["messageType"] = messageType
	self.log.Debug("worker", "Flushing data back to socket", updates)
	websocket.JSON.Send(sock.Socket, updates)
	return nil
}

func (self *Worker) Ping(sock PushWS, buffer interface{}) (err error) {
	data := buffer.(mozutil.JsMap)
	websocket.JSON.Send(sock.Socket, mozutil.JsMap{
		"messageType": data["messageType"],
		"status":      200})
	return nil
}

func (self *Worker) Purge(sock PushWS, buffer interface{}) (err error) {
	/*
	   // If needed...
	   sock.Scmd <- PushCommand{Command: PURGE,
	       Arguments:mozutil.JsMap{"uaid": sock.Uaid}}
	   result := <-sock.Scmd
	*/
	websocket.JSON.Send(sock.Socket, mozutil.JsMap{})
	return nil
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
