/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"time"

	"golang.org/x/net/websocket"
)

type Socket interface {
	Origin() (string, bool)
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	ReadJSON(v interface{}) error
	WriteJSON(v interface{}) error
	ReadBinary() (data []byte, err error)
	WriteBinary(data []byte) error
	ReadText() (data string, err error)
	WriteText(data string) error
	Close() error
}

type WebSocket websocket.Conn

func (ws *WebSocket) Origin() (uri string, ok bool) {
	conf := (*websocket.Conn)(ws).Config()
	if conf == nil {
		return
	}
	origin := conf.Origin
	if origin == nil {
		return
	}
	return origin.String(), true
}

func (ws *WebSocket) SetReadDeadline(t time.Time) error {
	return (*websocket.Conn)(ws).SetReadDeadline(t)
}

func (ws *WebSocket) SetWriteDeadline(t time.Time) error {
	return (*websocket.Conn)(ws).SetWriteDeadline(t)
}

func (ws *WebSocket) ReadJSON(v interface{}) error {
	return websocket.JSON.Receive((*websocket.Conn)(ws), v)
}

func (ws *WebSocket) WriteJSON(v interface{}) error {
	return websocket.JSON.Send((*websocket.Conn)(ws), v)
}

func (ws *WebSocket) ReadBinary() (data []byte, err error) {
	err = websocket.Message.Receive((*websocket.Conn)(ws), &data)
	return
}

func (ws *WebSocket) WriteBinary(data []byte) error {
	return websocket.Message.Send((*websocket.Conn)(ws), data)
}

func (ws *WebSocket) ReadText() (data string, err error) {
	err = websocket.Message.Receive((*websocket.Conn)(ws), &data)
	return
}

func (ws *WebSocket) WriteText(data string) error {
	return websocket.Message.Send((*websocket.Conn)(ws), data)
}

func (ws *WebSocket) Close() error {
	return (*websocket.Conn)(ws).Close()
}

func NewRecorderSocket() *RecorderSocket {
	inc := new(bytes.Buffer)
	out := new(bytes.Buffer)
	return &RecorderSocket{
		Incoming: inc,
		inDec:    json.NewDecoder(inc),
		Outgoing: out,
		outEnc:   json.NewEncoder(out),
	}
}

type RecorderSocket struct {
	Incoming *bytes.Buffer
	inDec    *json.Decoder
	Outgoing *bytes.Buffer
	outEnc   *json.Encoder
}

func (rs *RecorderSocket) Origin() (origin string, ok bool) {
	return
}

func (rs *RecorderSocket) SetReadDeadline(time.Time) error {
	return nil
}

func (rs *RecorderSocket) SetWriteDeadline(time.Time) error {
	return nil
}

func (rs *RecorderSocket) ReadJSON(v interface{}) error {
	return rs.inDec.Decode(v)
}

func (rs *RecorderSocket) WriteJSON(v interface{}) error {
	return rs.outEnc.Encode(v)
}

func (rs *RecorderSocket) ReadBinary() (data []byte, err error) {
	return rs.Incoming.ReadBytes('\n')
}

func (rs *RecorderSocket) WriteBinary(data []byte) error {
	rs.Outgoing.Write(data)
	rs.Outgoing.WriteByte('\n')
	return nil
}

func (rs *RecorderSocket) ReadText() (data string, err error) {
	return rs.Incoming.ReadString('\n')
}

func (rs *RecorderSocket) WriteText(data string) error {
	rs.Outgoing.WriteString(data)
	rs.Outgoing.WriteByte('\n')
	return nil
}

func (rs *RecorderSocket) Close() error {
	return nil
}
