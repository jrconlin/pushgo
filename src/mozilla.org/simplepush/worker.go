/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush/sperrors"
	"mozilla.org/util"

	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var MissingChannelErr = errors.New("Missing channelID")

//    -- Workers
//      these write back to the websocket.

type Worker struct {
	log   *util.HekaLogger
	state int
}

const (
	INACTIVE = 0
	ACTIVE   = 1
)

const (
	UAID_MAX_LEN = 100
	CHID_MAX_LEN = 100
)

func NewWorker(config util.JsMap) *Worker {
	return &Worker{log: util.NewHekaLogger(config), state: INACTIVE}
}

func (self *Worker) sniffer(sock PushWS, in chan util.JsMap) {
	// Sniff the websocket for incoming data.
	// Reading from the websocket is a blocking operation, and we also
	// need to write out when an even occurs. This isolates the incoming
	// reads to a separate go process.
	var socket = sock.Socket
	for {
		var raw []byte
		var buffer util.JsMap
		err := websocket.Message.Receive(socket, &raw)
		if err != nil {
			self.log.Error("worker",
				fmt.Sprintf("Websocket Error %s", err), nil)
			break
		}
		if len(raw) > 0 {
			self.log.Info("worker",
				fmt.Sprintf("Socket received %s", string(raw)), nil)
			err := json.Unmarshal(raw, &buffer)
			if err != nil {
				self.log.Error("worker",
					"Unparsable data", util.JsMap{"raw": raw})
				break
			}
			self.log.Info("worker",
				fmt.Sprintf("Socket sending %s", buffer), nil)
			// Only do something if there's something to do.
			in <- buffer
		}
	}
	// Clean up the server side (This will delete records associated
	// with the UAID.
	sock.Scmd <- PushCommand{Command: DIE, Arguments: nil}
	socket.Close()
}

// standardize the error reporting back to the client.
func (self *Worker) handleError(sock PushWS, message util.JsMap, err error) (ret error) {
	self.log.Info("worker", fmt.Sprintf("Sending error %s", err), nil)
	message["status"] = sperrors.ErrToStatus(err)
	message["error"] = err.Error()
	return websocket.JSON.Send(sock.Socket, message)
}

// General workhorse loop for the websocket handler.
func (self *Worker) Run(sock PushWS) {
	var err error

	// Instantiate a websocket reader, a blocking operation
	// (Remember, we need to be able to write out PUSH events
	// as they happen.)
	in := make(chan util.JsMap)
	go self.sniffer(sock, in)

	for {
		select {
		case cmd := <-sock.Ccmd:
			// A new Push has happened. Flush out the data to the
			// device (and potentially remotely wake it if that fails)
			self.log.Info("worker",
				fmt.Sprintf("Client Run cmd: %d", cmd.Command),
				nil)
			if cmd.Command == FLUSH {
				self.log.Info("worker",
					fmt.Sprintf("Flushing... %s", sock.Uaid), nil)
				self.Flush(sock, time.Now().UTC().Unix())
				// additional non-client commands are TBD.
			}
		case buffer := <-in:
			defer func(sock PushWS) {
				if r := recover(); r != nil {
					sock.Logger.Error("worker", r.(error).Error(), nil)
				}
				sock.Scmd <- PushCommand{Command: DIE, Arguments: nil}
				sock.Socket.Close()
				return
			}(sock)

			self.log.Info("worker",
				fmt.Sprintf("Client Read buffer, %s %d\n", buffer,
					len(buffer)), nil)
			if len(buffer) == 0 {
				// Empty buffers are "pings"
				buffer["messageType"] = "ping"
			}
			// process the client commands
			if _, ok := buffer["messageType"]; !ok {
				self.log.Info("worker", "Invalid message",
					util.JsMap{"reason": "Missing messageType",
						"data": buffer})
				self.handleError(sock,
					util.JsMap{},
					sperrors.UnknownCommandError)
				break
			}
			switch strings.ToLower(buffer["messageType"].(string)) {
			case "hello":
				err = self.Hello(&sock, buffer)
			case "ack":
				err = self.Ack(sock, buffer)
			case "register":
				err = self.Register(sock, buffer)
				self.log.Info("worker", fmt.Sprintf("Reg result:: %s", err), nil)
			case "unregister":
				err = self.Unregister(sock, buffer)
			case "ping":
				err = self.Ping(sock, buffer)
			default:
				self.log.Warn("worker",
					fmt.Sprintf("I have no idea what [%s] is.", buffer),
					nil)
				err = sperrors.UnknownCommandError
			}
			if err != nil {
				self.handleError(sock, buffer, err)
				break
			}
		}
	}
	sock.Scmd <- PushCommand{Command: DIE, Arguments: nil}
	sock.Socket.Close()
}

// Associate the UAID for this socket connection (and flush any data that
// may be pending for the connection)
func (self *Worker) Hello(sock *PushWS, buffer interface{}) (err error) {
	// register the UAID
	defer func() {
		if r := recover(); r != nil {
			self.log.Error("worker",
				fmt.Sprintf("Unhandled error in Hello: %s", r),
				nil)
			err = sperrors.InvalidDataError
		}
	}()

	data := buffer.(util.JsMap)
	if _, ok := data["uaid"]; !ok {
		// Must include "uaid" (even if blank)
		return sperrors.MissingDataError
	}
	if data["channelIDs"] == nil {
		// Must include "channelIDs" (even if empty)
		return sperrors.MissingDataError
	}
	if len(sock.Uaid) > 0 && len(data["uaid"].(string)) > 0 && sock.Uaid != data["uaid"].(string) {
		// if there's already a Uaid for this channel, don't accept a new one
		return sperrors.InvalidCommandError
	}
	if len(sock.Uaid) == 0 {
		// if there's no UAID for the socket, accept or create a new one.
		sock.Uaid = data["uaid"].(string)
		if len(sock.Uaid) > UAID_MAX_LEN {
			return sperrors.InvalidDataError
		}
		if len(sock.Uaid) == 0 {
			sock.Uaid, _ = GenUUID4()
		}
	}
	// register the sockets (NOOP)
	// register any proprietary connection requirements
	// alert the master of the new UAID.
	cmd := PushCommand{Command: HELLO,
		Arguments: util.JsMap{
			"uaid":  sock.Uaid,
			"chids": data["channelIDs"]}}
	// blocking call back to the boss.
	sock.Scmd <- cmd
	result := <-sock.Scmd
	if err = sock.Store.SetUAIDHost(sock.Uaid); err != nil {
		return err
	}

	self.log.Info("worker", "sending HELLO response....",
		util.JsMap{"uaid": sock.Uaid})
	websocket.JSON.Send(sock.Socket, util.JsMap{
		"messageType": data["messageType"],
		"status":      result.Command,
		"uaid":        sock.Uaid})
	self.state = ACTIVE
	if err == nil {
		// Get the lastAccessed time from wherever
		self.Flush(*sock, 0)
	}
	return err
}

// Clear the data that the client stated it received, then re-flush any
// records (including new data)
func (self *Worker) Ack(sock PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			self.log.Error("worker",
				fmt.Sprintf("Unhandled error in Ack: %s", r),
				nil)
			err = sperrors.InvalidDataError
		}
	}()

	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data := buffer.(util.JsMap)
	if data["updates"] == nil {
		return sperrors.MissingDataError
	}
	err = sock.Store.Ack(sock.Uaid, data)
	// Get the lastAccessed time from wherever.
	if err == nil {
		self.Flush(sock, 0)
		return nil
	}
	self.log.Info("worker", fmt.Sprintf("Flushed ACK returning %s", err), nil)
	return err
}

// Register a new ChannelID. Optionally, encrypt the endpoint.
func (self *Worker) Register(sock PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			self.log.Error("worker",
				fmt.Sprintf("Unhandled error in Register: %s", r),
				nil)
			err = sperrors.InvalidDataError
		}
	}()

	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data := buffer.(util.JsMap)
	if data["channelID"] == nil {
		return sperrors.MissingDataError
	}
	appid := data["channelID"].(string)
	if len(appid) > CHID_MAX_LEN {
		return sperrors.InvalidDataError
	}
	err = sock.Store.RegisterAppID(sock.Uaid, appid, "")
	if err != nil {
		self.log.Error("worker",
			fmt.Sprintf("ERROR: RegisterAppID failed %s", err),
			nil)
		return err
	}
	// have the server generate the callback URL.
	cmd := PushCommand{Command: REGIS,
		Arguments: data}
	sock.Scmd <- cmd
	result := <-sock.Scmd
	self.log.Debug("worker", fmt.Sprintf("Server returned %s", result), nil)
	endpoint := result.Arguments.(util.JsMap)["pushEndpoint"].(string)
	// return the info back to the socket
	self.log.Info("worker", "Sending REGIS response....", nil)
	websocket.JSON.Send(sock.Socket, util.JsMap{
		"messageType":  data["messageType"],
		"status":       result.Command,
		"channelID":    data["channelID"],
		"pushEndpoint": endpoint})
	return err
}

// Unregister a ChannelID.
func (self *Worker) Unregister(sock PushWS, buffer interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			self.log.Error("worker",
				fmt.Sprintf("Unhandled error in Unregister: %s", r),
				nil)
			err = sperrors.InvalidDataError
		}
	}()
	if sock.Uaid == "" {
		return sperrors.InvalidCommandError
	}
	data := buffer.(util.JsMap)
	if data["channelID"] == nil {
		return sperrors.MissingDataError
	}
	appid := data["channelID"].(string)
	// Always return success for an UNREG.
	sock.Store.DeleteAppID(sock.Uaid, appid, false)
	self.log.Info("worker", "Sending UNREG response ..", nil)
	websocket.JSON.Send(sock.Socket, util.JsMap{
		"messageType": data["messageType"],
		"status":      200,
		"channelID":   appid})
	return err
}

// Dump any records associated with the UAID.
func (self *Worker) Flush(sock PushWS, lastAccessed int64) {
	// flush pending data back to Client
	messageType := "notification"
	timer := time.Now()
	defer func(timer time.Time, sock PushWS) {
		sock.Logger.Info("timer",
			"Client flush completed",
			util.JsMap{"duration": time.Now().Sub(timer).Nanoseconds(),
				"uaid": sock.Uaid})
	}(timer, sock)
	if sock.Uaid == "" {
		self.log.Error("worker", "Undefined UAID for socket. Aborting.", nil)
		// Have the server clean up records associated with this UAID.
		// (Probably "none", but still good for housekeeping)
		sock.Scmd <- PushCommand{Command: DIE, Arguments: nil}
		sock.Socket.Close()
	}
	// Fetch the pending updates from #storage
	updates, err := sock.Store.GetUpdates(sock.Uaid, lastAccessed)
	if err != nil {
		self.handleError(sock, util.JsMap{"messageType": messageType}, err)
		return
	}
	if updates == nil {
		return
	}
	updates["messageType"] = messageType
	self.log.Info("worker", "Flushing data back to socket", updates)
	websocket.JSON.Send(sock.Socket, updates)
}

func (self *Worker) Ping(sock PushWS, buffer interface{}) (err error) {
	data := buffer.(util.JsMap)
	websocket.JSON.Send(sock.Socket, util.JsMap{
		"messageType": data["messageType"],
		"status":      200})
	return nil
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
