/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush/storage"
	"mozilla.org/util"

	"time"
)

const (
	UNREG = iota
	REGIS
	HELLO
	ACK
	FLUSH
	RETRN
	DIE
)

type PushCommand struct {
	// Use mutable int value
	Command   int         //command type (UNREG, REGIS, ACK, etc)
	Arguments interface{} //command arguments
}

type PushWS struct {
	Uaid   string           // id
	Socket *websocket.Conn  // Remote connection
	Scmd   chan PushCommand // server command channel
	Ccmd   chan PushCommand // client command channel
	Store  *storage.Storage
	Logger *util.HekaLogger
	Born   time.Time
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
