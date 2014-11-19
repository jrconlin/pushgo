/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"time"

	"golang.org/x/net/websocket"
)

type CommandType int

const (
	UNREG CommandType = iota
	REGIS
	HELLO
	ACK
	FLUSH
	RETRN
	DIE
	PURGE
)

var cmdLabels = map[CommandType]string{
	UNREG: "Unregister",
	REGIS: "Register",
	HELLO: "Hello",
	ACK:   "ACK",
	FLUSH: "Flush",
	RETRN: "Return",
	DIE:   "Die",
	PURGE: "Purge"}

type PushCommand struct {
	// Use mutable int value
	Command   CommandType //command type (UNREG, REGIS, ACK, etc)
	Arguments JsMap       //command arguments
}

type PushWS struct {
	Uaid     string          // Hex-encoded client ID; not normalized
	deviceID []byte          // Raw client ID bytes
	Socket   *websocket.Conn // Remote connection
	Store
	Logger  *SimpleLogger
	Metrics *Metrics
	Born    time.Time
}

func (ws *PushWS) Origin() string {
	// protect against null pointers.
	if ws == nil {
		return "Unknown"
	}
	if ws.Socket == nil {
		return "No Socket"
	}
	conf := ws.Socket.Config()
	if conf == nil {
		return "No Socket Config"
	}
	origin := conf.Origin
	if origin == nil {
		return "No Socket Origin"
	}
	return origin.String()
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
