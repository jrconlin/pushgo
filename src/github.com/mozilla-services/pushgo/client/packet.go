/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"encoding/json"
)

type (
	PacketType int
	PacketId   int
)

const (
	Helo PacketType = iota + 1
	Register
	Unregister
	ACK
	Updates
	Purge
	Ping
)

func (t PacketType) String() string {
	switch t {
	case Helo:
		return "hello"
	case Register:
		return "register"
	case Unregister:
		return "unregister"
	case ACK:
		return "ack"
	case Updates:
		return "updates"
	case Purge:
		return "purge"
	case Ping:
		return "ping"
	}
	return "unknown packet type"
}

func (t PacketType) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

const (
	ACKId PacketId = iota + 1
	PurgeId
	PingId

	// nilId is used by tests to simulate a nil packet ID, as Conn.Send() will
	// close the connection with an error if Request.Id() == nil. getId()
	// converts nilId to nil.
	nilId
)

type requestWithMarshal interface {
	Request
	json.Marshaler
}

type requestWithErrors interface {
	requestWithMarshal
	getErrors() chan error
}

func getId(r Request) (id interface{}) {
	if id = r.Id(); id == nilId {
		return nil
	}
	return
}

type HasType interface {
	Type() PacketType
}

type Request interface {
	HasType
	CanReply() bool
	Sync() bool
	Id() interface{}
	Reply(Reply)
	Error(error)
	Do() (Reply, error)
	Close()
}

type Reply interface {
	HasType
	Sync() bool
	Id() interface{}
	Status() int
}

type ClientHelo struct {
	DeviceId   string
	ChannelIds []string
	replies    chan Reply
	errors     chan error
}

func (*ClientHelo) Type() PacketType        { return Helo }
func (*ClientHelo) CanReply() bool          { return true }
func (*ClientHelo) Sync() bool              { return true }
func (h *ClientHelo) Id() interface{}       { return h.DeviceId }
func (h *ClientHelo) Reply(reply Reply)     { h.replies <- reply }
func (h *ClientHelo) Error(err error)       { h.errors <- err }
func (h *ClientHelo) getErrors() chan error { return h.errors }

func (h *ClientHelo) Close() {
	close(h.replies)
	close(h.errors)
}

func (h *ClientHelo) Do() (reply Reply, err error) {
	select {
	case reply = <-h.replies:
	case err = <-h.errors:
	}
	return
}

func (h *ClientHelo) MarshalJSON() ([]byte, error) {
	channelIds := h.ChannelIds
	if channelIds == nil {
		channelIds = []string{}
	}
	return json.Marshal(struct {
		MessageType PacketType  `json:"messageType"`
		DeviceId    interface{} `json:"uaid"`
		ChannelIds  []string    `json:"channelIDs"`
	}{h.Type(), getId(h), channelIds})
}

type ServerHelo struct {
	StatusCode int
	DeviceId   string
	Redirect   string
}

func (*ServerHelo) Type() PacketType  { return Helo }
func (*ServerHelo) HasRequest() bool  { return true }
func (*ServerHelo) Sync() bool        { return true }
func (h *ServerHelo) Id() interface{} { return h.DeviceId }
func (h *ServerHelo) Status() int     { return h.StatusCode }

type ClientRegister struct {
	ChannelId string
	replies   chan Reply
	errors    chan error
}

func (*ClientRegister) Type() PacketType        { return Register }
func (*ClientRegister) CanReply() bool          { return true }
func (*ClientRegister) Sync() bool              { return false }
func (r *ClientRegister) Id() interface{}       { return r.ChannelId }
func (r *ClientRegister) Reply(reply Reply)     { r.replies <- reply }
func (r *ClientRegister) Error(err error)       { r.errors <- err }
func (r *ClientRegister) getErrors() chan error { return r.errors }

func (r *ClientRegister) Close() {
	close(r.replies)
	close(r.errors)
}

func (r *ClientRegister) Do() (reply Reply, err error) {
	select {
	case reply = <-r.replies:
	case err = <-r.errors:
	}
	return
}

func (r *ClientRegister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType  `json:"messageType"`
		ChannelId   interface{} `json:"channelID"`
	}{r.Type(), getId(r)})
}

type ServerRegister struct {
	StatusCode int
	ChannelId  string
	Endpoint   string
}

func (*ServerRegister) Type() PacketType  { return Register }
func (*ServerRegister) HasRequest() bool  { return true }
func (*ServerRegister) Sync() bool        { return false }
func (r *ServerRegister) Id() interface{} { return r.ChannelId }
func (r *ServerRegister) Status() int     { return r.StatusCode }

type ClientUnregister struct {
	ChannelId string
	errors    chan error
}

func (*ClientUnregister) Type() PacketType        { return Unregister }
func (*ClientUnregister) CanReply() bool          { return false }
func (*ClientUnregister) Sync() bool              { return false }
func (u *ClientUnregister) Id() interface{}       { return u.ChannelId }
func (u *ClientUnregister) Reply(Reply)           {}
func (u *ClientUnregister) Error(err error)       { u.errors <- err }
func (u *ClientUnregister) Close()                { close(u.errors) }
func (u *ClientUnregister) Do() (Reply, error)    { return nil, <-u.errors }
func (u *ClientUnregister) getErrors() chan error { return u.errors }

func (u *ClientUnregister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType  `json:"messageType"`
		ChannelId   interface{} `json:"channelID"`
	}{u.Type(), getId(u)})
}

type ClientPing chan error

func (ClientPing) Type() PacketType             { return Ping }
func (ClientPing) CanReply() bool               { return true }
func (ClientPing) Sync() bool                   { return true }
func (ClientPing) Id() interface{}              { return PingId }
func (ClientPing) Reply(Reply)                  {}
func (p ClientPing) Error(err error)            { p <- err }
func (p ClientPing) Close()                     { close(p) }
func (p ClientPing) Do() (Reply, error)         { return nil, <-p }
func (ClientPing) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (p ClientPing) getErrors() chan error      { return p }

type ClientPurge chan error

func (ClientPurge) Type() PacketType             { return Purge }
func (ClientPurge) CanReply() bool               { return false }
func (ClientPurge) Sync() bool                   { return true }
func (ClientPurge) Id() interface{}              { return PurgeId }
func (ClientPurge) Reply(Reply)                  {}
func (p ClientPurge) Error(err error)            { p <- err }
func (p ClientPurge) Close()                     { close(p) }
func (p ClientPurge) Do() (Reply, error)         { return nil, <-p }
func (ClientPurge) MarshalJSON() ([]byte, error) { return []byte(`{"messageType":"purge"}`), nil }
func (p ClientPurge) getErrors() chan error      { return p }

type ClientACK struct {
	Updates []Update
	errors  chan error
}

func (*ClientACK) Type() PacketType        { return ACK }
func (*ClientACK) CanReply() bool          { return false }
func (*ClientACK) Sync() bool              { return false }
func (*ClientACK) Id() interface{}         { return ACKId }
func (*ClientACK) Reply(Reply)             {}
func (a *ClientACK) Error(err error)       { a.errors <- err }
func (a *ClientACK) Close()                { close(a.errors) }
func (a *ClientACK) Do() (Reply, error)    { return nil, <-a.errors }
func (a *ClientACK) getErrors() chan error { return a.errors }

func (a *ClientACK) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType `json:"messageType"`
		Updates     []Update   `json:"updates"`
	}{a.Type(), a.Updates})
}

type Update struct {
	ChannelId string `json:"channelID"`
	Version   int64  `json:"version"`
}

type ServerUpdates []Update

func (ServerUpdates) Type() PacketType { return Updates }
