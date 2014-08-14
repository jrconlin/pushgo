/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"time"

	"code.google.com/p/go.net/websocket"
)

const (
	UNREG = iota
	REGIS
	HELLO
	ACK
	FLUSH
	RETRN
	DIE
	PURGE
)

var cmdLabels = map[int]string{
	0: "Unregister",
	1: "Register",
	2: "Hello",
	3: "ACK",
	4: "Flush",
	5: "Return",
	6: "Die",
	7: "Purge"}

type PushCommand struct {
	// Use mutable int value
	Command   int         //command type (UNREG, REGIS, ACK, etc)
	Arguments interface{} //command arguments
}

type PushWS struct {
	Uaid   string          // id
	Socket *websocket.Conn // Remote connection
	Store
	Logger  *SimpleLogger
	Metrics *Metrics
	Born    time.Time
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
