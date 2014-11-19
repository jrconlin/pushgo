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
)

type Packet interface {
	Type() PacketType
}

type Exchange interface {
	Sync() bool
	Id() interface{}
}

type CanReply interface {
	CanReply() bool
}

type Request interface {
	Packet
	Exchange
	CanReply
	json.Marshaler
	Reply(Reply)
	Error(error)
	Do() (Reply, error)
	Close()
}

type Reply interface {
	Packet
	Exchange
	Status() int
}

type clientPacket interface {
	CanReply
	getReplies() chan Reply
	getErrors() chan error
}

type Update struct {
	ChannelId string `json:"channelID"`
	Version   int64  `json:"version"`
}

func NewHelo(deviceId string, channelIds []string) Request {
	return ClientHelo{deviceId, channelIds, make(chan Reply), make(chan error)}
}

type ClientHelo struct {
	DeviceId   string
	ChannelIds []string
	replies    chan Reply
	errors     chan error
}

func (ClientHelo) Type() PacketType          { return Helo }
func (ClientHelo) CanReply() bool            { return true }
func (ClientHelo) Sync() bool                { return true }
func (ch ClientHelo) Id() interface{}        { return ch.DeviceId }
func (ch ClientHelo) Reply(r Reply)          { ch.replies <- r }
func (ch ClientHelo) Error(err error)        { ch.errors <- err }
func (ch ClientHelo) Do() (Reply, error)     { return doClientPacket(ch) }
func (ch ClientHelo) Close()                 { closeClientPacket(ch) }
func (ch ClientHelo) getReplies() chan Reply { return ch.replies }
func (ch ClientHelo) getErrors() chan error  { return ch.errors }

func (ch ClientHelo) MarshalJSON() ([]byte, error) {
	channelIds := ch.ChannelIds
	if channelIds == nil {
		channelIds = []string{}
	}
	return json.Marshal(struct {
		MessageType PacketType  `json:"messageType"`
		DeviceId    interface{} `json:"uaid"`
		ChannelIds  []string    `json:"channelIDs"`
	}{ch.Type(), ch.DeviceId, channelIds})
}

type ServerHelo struct {
	StatusCode int
	DeviceId   string
	Redirect   string
}

func (ServerHelo) Type() PacketType   { return Helo }
func (ServerHelo) HasRequest() bool   { return true }
func (ServerHelo) Sync() bool         { return true }
func (sh ServerHelo) Id() interface{} { return sh.DeviceId }
func (sh ServerHelo) Status() int     { return sh.StatusCode }

func NewRegister(channelId string) Request {
	return ClientRegister{channelId, make(chan Reply), make(chan error)}
}

type ClientRegister struct {
	ChannelId string
	replies   chan Reply
	errors    chan error
}

func (ClientRegister) Type() PacketType          { return Register }
func (ClientRegister) CanReply() bool            { return true }
func (ClientRegister) Sync() bool                { return false }
func (cr ClientRegister) Id() interface{}        { return cr.ChannelId }
func (cr ClientRegister) Reply(r Reply)          { cr.replies <- r }
func (cr ClientRegister) Error(err error)        { cr.errors <- err }
func (cr ClientRegister) Do() (Reply, error)     { return doClientPacket(cr) }
func (cr ClientRegister) Close()                 { closeClientPacket(cr) }
func (cr ClientRegister) getReplies() chan Reply { return cr.replies }
func (cr ClientRegister) getErrors() chan error  { return cr.errors }

func (cr ClientRegister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType  `json:"messageType"`
		ChannelId   interface{} `json:"channelID"`
	}{cr.Type(), cr.ChannelId})
}

type ServerRegister struct {
	StatusCode int
	ChannelId  string
	Endpoint   string
}

func (ServerRegister) Type() PacketType   { return Register }
func (ServerRegister) HasRequest() bool   { return true }
func (ServerRegister) Sync() bool         { return false }
func (sr ServerRegister) Id() interface{} { return sr.ChannelId }
func (sr ServerRegister) Status() int     { return sr.StatusCode }

func NewUnregister(channelId string, canReply bool) Request {
	return ClientUnregister{channelId, makeReplies(canReply), make(chan error)}
}

type ClientUnregister struct {
	ChannelId string
	replies   chan Reply
	errors    chan error
}

func (ClientUnregister) Type() PacketType          { return Unregister }
func (cu ClientUnregister) CanReply() bool         { return cu.replies != nil }
func (ClientUnregister) Sync() bool                { return false }
func (cu ClientUnregister) Id() interface{}        { return cu.ChannelId }
func (cu ClientUnregister) Reply(r Reply)          { replyClientPacket(cu, r) }
func (cu ClientUnregister) Error(err error)        { cu.errors <- err }
func (cu ClientUnregister) Do() (Reply, error)     { return doClientPacket(cu) }
func (cu ClientUnregister) Close()                 { closeClientPacket(cu) }
func (cu ClientUnregister) getReplies() chan Reply { return cu.replies }
func (cu ClientUnregister) getErrors() chan error  { return cu.errors }

func (cu ClientUnregister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType  `json:"messageType"`
		ChannelId   interface{} `json:"channelID"`
	}{cu.Type(), cu.ChannelId})
}

func NewPing(canReply bool) Request {
	return ClientPing{makeReplies(canReply), make(chan error)}
}

type ClientPing struct {
	replies chan Reply
	errors  chan error
}

func (ClientPing) Type() PacketType             { return Ping }
func (cp ClientPing) CanReply() bool            { return cp.replies != nil }
func (ClientPing) Sync() bool                   { return true }
func (ClientPing) Id() interface{}              { return PingId }
func (cp ClientPing) Reply(r Reply)             { replyClientPacket(cp, r) }
func (cp ClientPing) Error(err error)           { cp.errors <- err }
func (cp ClientPing) Do() (Reply, error)        { return doClientPacket(cp) }
func (cp ClientPing) Close()                    { closeClientPacket(cp) }
func (ClientPing) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }
func (cp ClientPing) getReplies() chan Reply    { return cp.replies }
func (cp ClientPing) getErrors() chan error     { return cp.errors }

func NewPurge(canReply bool) Request {
	return ClientPurge{makeReplies(canReply), make(chan error)}
}

type ClientPurge struct {
	replies chan Reply
	errors  chan error
}

func (ClientPurge) Type() PacketType             { return Purge }
func (cp ClientPurge) CanReply() bool            { return cp.replies != nil }
func (ClientPurge) Sync() bool                   { return true }
func (ClientPurge) Id() interface{}              { return PurgeId }
func (cp ClientPurge) Reply(r Reply)             { replyClientPacket(cp, r) }
func (cp ClientPurge) Error(err error)           { cp.errors <- err }
func (cp ClientPurge) Close()                    { closeClientPacket(cp) }
func (cp ClientPurge) Do() (Reply, error)        { return doClientPacket(cp) }
func (ClientPurge) MarshalJSON() ([]byte, error) { return []byte(`{"messageType":"purge"}`), nil }
func (cp ClientPurge) getReplies() chan Reply    { return cp.replies }
func (cp ClientPurge) getErrors() chan error     { return cp.errors }

func NewACK(updates []Update, canReply bool) Request {
	return ClientACK{updates, makeReplies(canReply), make(chan error)}
}

type ClientACK struct {
	Updates []Update
	replies chan Reply
	errors  chan error
}

func (ClientACK) Type() PacketType          { return ACK }
func (ca ClientACK) CanReply() bool         { return ca.replies != nil }
func (ClientACK) Sync() bool                { return false }
func (ClientACK) Id() interface{}           { return ACKId }
func (ca ClientACK) Reply(r Reply)          { replyClientPacket(ca, r) }
func (ca ClientACK) Error(err error)        { ca.errors <- err }
func (ca ClientACK) Close()                 { closeClientPacket(ca) }
func (ca ClientACK) Do() (Reply, error)     { return doClientPacket(ca) }
func (ca ClientACK) getReplies() chan Reply { return ca.replies }
func (ca ClientACK) getErrors() chan error  { return ca.errors }

func (ca ClientACK) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType `json:"messageType"`
		Updates     []Update   `json:"updates"`
	}{ca.Type(), ca.Updates})
}

type ServerUpdates []Update

func (ServerUpdates) Type() PacketType { return Updates }

// makeReplies returns a reply channel if the canReply flag is set. This is
// useful for testing replies to de-registrations, pings, purges, and ACKs.
func makeReplies(canReply bool) chan Reply {
	if canReply {
		return make(chan Reply)
	}
	return nil
}

// closeClientPacket closes an outgoing client packet's reply and error
// channels. nil reply channels will not be closed.
func closeClientPacket(p clientPacket) {
	if p.CanReply() {
		close(p.getReplies())
	}
	close(p.getErrors())
}

// replyClientPacket sends a reply packet on the outgoing packet's reply
// channel. If the reply channel is nil, the reply will be dropped.
func replyClientPacket(p clientPacket, r Reply) {
	if p.CanReply() {
		p.getReplies() <- r
	}
}

// doClientPacket waits for a reply or error to be sent on the outgoing
// packet's respective channels.
func doClientPacket(p clientPacket) (r Reply, err error) {
	select {
	case r = <-p.getReplies():
	case err = <-p.getErrors():
	}
	return
}
