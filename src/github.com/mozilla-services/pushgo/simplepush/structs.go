/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

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

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
