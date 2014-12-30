/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"sync"
	"time"
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
	uaidLock sync.RWMutex
	uaid     string // Hex-encoded client ID; not normalized
	Socket   Socket // Remote connection
	Store
	Logger    *SimpleLogger
	Metrics   *Metrics
	Born      time.Time
	closeLock sync.RWMutex
	closed    bool
}

func (ws *PushWS) UAID() (uaid string) {
	ws.uaidLock.RLock()
	uaid = ws.uaid
	ws.uaidLock.RUnlock()
	return
}

func (ws *PushWS) SetUAID(uaid string) {
	ws.uaidLock.Lock()
	ws.uaid = uaid
	ws.uaidLock.Unlock()
}

func (ws *PushWS) IsClosed() (closed bool) {
	ws.closeLock.RLock()
	closed = ws.closed
	ws.closeLock.RUnlock()
	return
}

func (ws *PushWS) Close() error {
	if ws == nil {
		return nil
	}
	ws.closeLock.Lock()
	if ws.closed {
		ws.closeLock.Unlock()
		return nil
	}
	ws.closed = true
	ws.closeLock.Unlock()
	socket := ws.Socket
	if socket == nil {
		return nil
	}
	return socket.Close()
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
