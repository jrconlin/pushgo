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
}

type PacketType int

const (
	Helo PacketType = iota
	Register
	Unregister
	Notification
	ACK
	Purge
)

func (t PacketType) String() string {
	return packetNames[t]
}

type Request interface {
	json.Marshaler
	Type() PacketType
	Id() string
	replies() chan Reply
	errors() chan error
}

type Reply interface {
	Type() PacketType
	Id() string
	Status() int
}

type ClientHelo struct {
	DeviceId   string
	ChannelIds []string
	replyChan  chan Reply
	errChan    chan error
}

func (*ClientHelo) Type() PacketType         { return Helo }
func (helo *ClientHelo) Id() string          { return helo.DeviceId }
func (helo *ClientHelo) replies() chan Reply { return helo.replyChan }
func (helo *ClientHelo) errors() chan error  { return helo.errChan }

func (helo *ClientHelo) MarshalJSON() ([]byte, error) {
	var channelIds []string
	if helo.ChannelIds == nil {
		channelIds = make([]string, 0)
	} else {
		channelIds = helo.ChannelIds
	}
	value := struct {
		MessageType string   `json:"messageType"`
		DeviceId    string   `json:"uaid"`
		ChannelIds  []string `json:"channelIDs"`
	}{
		helo.Type().String(),
		helo.DeviceId,
		channelIds,
	}
	return json.Marshal(value)
}

type ServerHelo struct {
	StatusCode int
	DeviceId   string
	Redirect   string
}

func (*ServerHelo) Type() PacketType { return Helo }
func (helo *ServerHelo) Id() string  { return helo.DeviceId }
func (helo *ServerHelo) Status() int { return helo.StatusCode }

type ClientRegister struct {
	ChannelId string
	replyChan chan Reply
	errChan   chan error
}

func (*ClientRegister) Type() PacketType      { return Register }
func (r *ClientRegister) Id() string          { return r.ChannelId }
func (r *ClientRegister) replies() chan Reply { return r.replyChan }
func (r *ClientRegister) errors() chan error  { return r.errChan }

func (r *ClientRegister) MarshalJSON() ([]byte, error) {
	value := struct {
		MessageType string `json:"messageType"`
		ChannelId   string `json:"channelID"`
	}{
		Register.String(),
		r.ChannelId,
	}
	return json.Marshal(value)
}

type ServerRegister struct {
	StatusCode int
	ChannelId  string
	Endpoint   string
}

func (*ServerRegister) Type() PacketType { return Register }
func (r *ServerRegister) Id() string     { return r.ChannelId }
func (r *ServerRegister) Status() int    { return r.StatusCode }

type ClientUnregister struct {
	ChannelId string
	errChan   chan error
}

func (*ClientUnregister) Type() PacketType      { return Unregister }
func (u *ClientUnregister) Id() string          { return u.ChannelId }
func (u *ClientUnregister) replies() chan Reply { return nil }
func (u *ClientUnregister) errors() chan error  { return u.errChan }

func (u *ClientUnregister) MarshalJSON() ([]byte, error) {
	value := struct {
		MessageType string `json:"messageType"`
		ChannelId   string `json:"channelID"`
	}{
		Unregister.String(),
		u.ChannelId,
	}
	return json.Marshal(value)
}

type ClientPurge chan error

func (ClientPurge) Type() PacketType     { return Purge }
func (ClientPurge) Id() string           { return "*" }
func (ClientPurge) replies() chan Reply  { return nil }
func (c ClientPurge) errors() chan error { return c }

func (c ClientPurge) MarshalJSON() ([]byte, error) {
	return []byte(`{"messageType":"purge"}`), nil
}

type ServerUpdates []Update

func (ServerUpdates) Type() PacketType { return Notification }
func (ServerUpdates) Id() string       { return "*" }
func (ServerUpdates) Status() int      { return 200 }

type ClientACK struct {
	Updates []Update
	errChan chan error
}

func (*ClientACK) Type() PacketType      { return ACK }
func (a *ClientACK) Id() string          { return "" }
func (a *ClientACK) replies() chan Reply { return nil }
func (a *ClientACK) errors() chan error  { return a.errChan }

func (a *ClientACK) MarshalJSON() ([]byte, error) {
	value := struct {
		MessageType string   `json:"messageType"`
		Updates     []Update `json:"updates"`
	}{
		ACK.String(),
		a.Updates,
	}
	v, err := json.Marshal(value)
	return v, err
}

type Update struct {
	ChannelId string `json:"channelID"`
	Version   int    `json:"version"`
}
