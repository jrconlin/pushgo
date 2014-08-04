package client

import (
	"encoding/json"
)

var packetNames = map[PacketType]string{
	Helo:         "hello",
	Register:     "register",
	Unregister:   "unregister",
	Notification: "notification",
	ACK:          "ack",
	Purge:        "purge",
	Ping:         "ping",
}

type PacketType int

const (
	Helo PacketType = iota
	Register
	Unregister
	Notification
	ACK
	Purge
	Ping
)

func (t PacketType) String() string {
	return packetNames[t]
}

func (t PacketType) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

type Request interface {
	json.Marshaler
	Type() PacketType
	CanReply() bool
	Sync() bool
	Id() string
	Reply(Reply)
	Error(error)
	Do() (Reply, error)
	Close()
}

type Reply interface {
	Type() PacketType
	HasRequest() bool
	Sync() bool
	CanSpool() bool
	Id() string
	Status() int
}

type ClientHelo struct {
	DeviceId   string
	ChannelIds []string
	replies    chan Reply
	errors     chan error
}

func (*ClientHelo) Type() PacketType    { return Helo }
func (*ClientHelo) CanReply() bool      { return true }
func (*ClientHelo) Sync() bool          { return true }
func (h *ClientHelo) Id() string        { return h.DeviceId }
func (h *ClientHelo) Reply(reply Reply) { h.replies <- reply }
func (h *ClientHelo) Error(err error)   { h.errors <- err }

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
	value := struct {
		MessageType PacketType `json:"messageType"`
		DeviceId    string     `json:"uaid"`
		ChannelIds  []string   `json:"channelIDs"`
	}{h.Type(), h.DeviceId, h.ChannelIds}
	return json.Marshal(value)
}

type ServerHelo struct {
	StatusCode int
	DeviceId   string
	Redirect   string
}

func (*ServerHelo) Type() PacketType { return Helo }
func (*ServerHelo) HasRequest() bool { return true }
func (*ServerHelo) Sync() bool       { return true }
func (*ServerHelo) CanSpool() bool   { return false }
func (h *ServerHelo) Id() string     { return h.DeviceId }
func (h *ServerHelo) Status() int    { return h.StatusCode }

type ClientRegister struct {
	ChannelId string
	replies   chan Reply
	errors    chan error
}

func (*ClientRegister) Type() PacketType    { return Register }
func (*ClientRegister) CanReply() bool      { return true }
func (*ClientRegister) Sync() bool          { return false }
func (r *ClientRegister) Id() string        { return r.ChannelId }
func (r *ClientRegister) Reply(reply Reply) { r.replies <- reply }
func (r *ClientRegister) Error(err error)   { r.errors <- err }

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
	value := struct {
		MessageType PacketType `json:"messageType"`
		ChannelId   string     `json:"channelID"`
	}{r.Type(), r.ChannelId}
	return json.Marshal(value)
}

type ServerRegister struct {
	StatusCode int
	ChannelId  string
	Endpoint   string
}

func (*ServerRegister) Type() PacketType { return Register }
func (*ServerRegister) HasRequest() bool { return true }
func (*ServerRegister) Sync() bool       { return false }
func (*ServerRegister) CanSpool() bool   { return false }
func (r *ServerRegister) Id() string     { return r.ChannelId }
func (r *ServerRegister) Status() int    { return r.StatusCode }

type ClientUnregister struct {
	ChannelId string
	errors    chan error
}

func (*ClientUnregister) Type() PacketType     { return Unregister }
func (*ClientUnregister) CanReply() bool       { return false }
func (*ClientUnregister) Sync() bool           { return false }
func (u *ClientUnregister) Id() string         { return u.ChannelId }
func (u *ClientUnregister) Reply(Reply)        {}
func (u *ClientUnregister) Error(err error)    { u.errors <- err }
func (u *ClientUnregister) Close()             { close(u.errors) }
func (u *ClientUnregister) Do() (Reply, error) { return nil, <-u.errors }

func (u *ClientUnregister) MarshalJSON() ([]byte, error) {
	value := struct {
		MessageType PacketType `json:"messageType"`
		ChannelId   string     `json:"channelID"`
	}{u.Type(), u.ChannelId}
	return json.Marshal(value)
}

type ClientPing chan error

func (ClientPing) Type() PacketType               { return Ping }
func (ClientPing) Id() string                     { return "*" }
func (ClientPing) Reply(Reply)                    {}
func (p ClientPing) Error(err error)              { p <- err }
func (p ClientPing) Close()                       { close(p) }
func (p ClientPing) Do() (Reply, error)           { return nil, <-p }
func (p ClientPing) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

type ClientPurge chan error

func (ClientPurge) Type() PacketType               { return Purge }
func (ClientPurge) CanReply() bool                 { return false }
func (ClientPurge) Sync() bool                     { return false }
func (ClientPurge) Id() string                     { return "*" }
func (ClientPurge) Reply(Reply)                    {}
func (p ClientPurge) Error(err error)              { p <- err }
func (p ClientPurge) Close()                       { close(p) }
func (p ClientPurge) Do() (Reply, error)           { return nil, <-p }
func (c ClientPurge) MarshalJSON() ([]byte, error) { return []byte(`{"messageType":"purge"}`), nil }

type ServerUpdates []Update

func (ServerUpdates) Type() PacketType { return Notification }
func (ServerUpdates) HasRequest() bool { return false }
func (ServerUpdates) Sync() bool       { return false }
func (ServerUpdates) CanSpool() bool   { return true }
func (ServerUpdates) Id() string       { return "*" }
func (ServerUpdates) Status() int      { return 200 }

type ClientACK struct {
	Updates []Update
	errors  chan error
}

func (*ClientACK) Type() PacketType     { return ACK }
func (*ClientACK) CanReply() bool       { return false }
func (*ClientACK) Sync() bool           { return false }
func (*ClientACK) Id() string           { return "*" }
func (*ClientACK) Reply(Reply)          {}
func (a *ClientACK) Error(err error)    { a.errors <- err }
func (a *ClientACK) Close()             { close(a.errors) }
func (a *ClientACK) Do() (Reply, error) { return nil, <-a.errors }

func (a *ClientACK) MarshalJSON() ([]byte, error) {
	value := struct {
		MessageType PacketType `json:"messageType"`
		Updates     []Update   `json:"updates"`
	}{a.Type(), a.Updates}
	v, err := json.Marshal(value)
	return v, err
}

type Update struct {
	ChannelId string `json:"channelID"`
	Version   int64  `json:"version"`
}
