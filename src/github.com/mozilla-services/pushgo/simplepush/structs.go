/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"time"
)

type CommandType int

const (
	REGIS CommandType = iota
	HELLO
	DIE
)

var cmdLabels = map[CommandType]string{
	REGIS: "Register",
	HELLO: "Hello",
	DIE:   "Die"}

type PushCommand struct {
	// Use mutable int value
	Command   CommandType //command type
	Arguments JsMap       //command arguments
}

type PushWS struct {
	uaid   string // Hex-encoded client ID; not normalized
	Socket Socket // Remote connection
	Store
	Logger    *SimpleLogger
	Metrics   *Metrics
	Born      time.Time
	closeOnce Once
}

func (ws *PushWS) UAID() (uaid string) {
	return ws.uaid
}

func (ws *PushWS) SetUAID(uaid string) {
	ws.uaid = uaid
}

func (ws *PushWS) IsClosed() (closed bool) {
	return ws.closeOnce.IsDone()
}

func (ws *PushWS) Close() error {
	return ws.closeOnce.Do(ws.close)
}

func (ws *PushWS) close() error {
	socket := ws.Socket
	if socket == nil {
		return nil
	}
	return socket.Close()
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
