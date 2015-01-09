/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"time"

	"golang.org/x/net/websocket"
)

// Socket describes a WebSocket connection. The default implementation is the
// WebSocket type; there's also a MockSocket for testing.
type Socket interface {
	// Origin returns the value of the Origin header from the WebSocket handshake.
	Origin() string

	// SetReadDeadline sets the read deadline on the underlying net.Conn. Once
	// the deadline expires, ReadJSON, ReadBinary, and ReadText will return
	// timeout errors. A zero value for t clears the deadline.
	SetReadDeadline(t time.Time) error

	// SetWriteDeadline sets the net.Conn write deadline.
	SetWriteDeadline(t time.Time) error

	// ReadJSON reads a JSON-encoded text or binary frame from the socket into v.
	ReadJSON(v interface{}) error

	// WriteJSON writes v as a JSON-encoded text frame to the socket.
	WriteJSON(v interface{}) error

	// ReadBinary reads a text or binary frame from the socket.
	ReadBinary() (data []byte, err error)

	// WriteBinary writes a binary frame containing data to the socket.
	WriteBinary(data []byte) error

	// ReadText reads a text frame from the socket.
	ReadText() (data string, err error)

	// WriteText writes a text frame containing data to the socket.
	WriteText(data string) error

	// Close initiates the WebSocket closing handshake and closes the
	// underlying net.Conn.
	Close() error
}

// WebSocket implements the Socket interface for a *websocket.Conn.
type WebSocket websocket.Conn

func (ws *WebSocket) Origin() (uri string) {
	conf := (*websocket.Conn)(ws).Config()
	if conf == nil {
		return
	}
	origin := conf.Origin
	if origin == nil {
		return
	}
	return origin.String()
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
