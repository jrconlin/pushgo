/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	ws "code.google.com/p/go.net/websocket"

	"github.com/mozilla-services/pushgo/id"
)

const (
	// Origin specifies the URI of the Simple Push WebSocket server. Can be
	// obtained via `app.FullHostname()`.
	Origin = "ws://localhost:8080"

	// AllowDupes indicates whether the Simple Push server under test allows
	// duplicate registrations. TODO: Expose as an `app` configuration flag.
	AllowDupes = true

	// validId is a placeholder device ID used by `typeTest`.
	validId = "57954545-c1bc-4fc4-9c1a-cd186d861336"
)

// CustomRegister is a custom channel registration packet that supports
// arbitrary types for the channel ID.
type CustomRegister struct {
	ChannelId interface{}
	replies   chan Reply
	errors    chan error
}

func (*CustomRegister) Type() PacketType        { return Register }
func (*CustomRegister) CanReply() bool          { return true }
func (*CustomRegister) Sync() bool              { return false }
func (r *CustomRegister) Id() interface{}       { return r.ChannelId }
func (r *CustomRegister) Reply(reply Reply)     { r.replies <- reply }
func (r *CustomRegister) Error(err error)       { r.errors <- err }
func (r *CustomRegister) getErrors() chan error { return r.errors }

func (r *CustomRegister) Close() {
	close(r.replies)
	close(r.errors)
}

func (r *CustomRegister) Do() (reply Reply, err error) {
	select {
	case reply = <-r.replies:
	case err = <-r.errors:
	}
	return
}

func (r *CustomRegister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType  `json:"messageType"`
		ChannelId   interface{} `json:"channelID"`
	}{r.Type(), getId(r)})
}

// CustomHelo is a custom handshake packet that specifies an extra field and
// supports arbitrary types for the message type, device ID, and channel IDs.
type CustomHelo struct {
	MessageType interface{}   `json:"messageType"`
	DeviceId    interface{}   `json:"uaid"`
	ChannelIds  []interface{} `json:"channelIDs"`
	Extra       string        `json:"customKey,omitempty"`
	replies     chan Reply
	errors      chan error
}

func (*CustomHelo) Type() PacketType        { return Helo }
func (*CustomHelo) CanReply() bool          { return true }
func (*CustomHelo) Sync() bool              { return true }
func (h *CustomHelo) Id() interface{}       { return h.DeviceId }
func (h *CustomHelo) Reply(reply Reply)     { h.replies <- reply }
func (h *CustomHelo) Error(err error)       { h.errors <- err }
func (h *CustomHelo) getErrors() chan error { return h.errors }

func (h *CustomHelo) Close() {
	close(h.replies)
	close(h.errors)
}

func (h *CustomHelo) Do() (reply Reply, err error) {
	select {
	case reply = <-h.replies:
	case err = <-h.errors:
	}
	return
}

// ClientInvalidACK wraps a `ClientACK` packet in a synchronous request packet
// with a fabricated message ID to track error replies.
type ClientInvalidACK struct {
	ClientACK
}

func (*ClientInvalidACK) Id() interface{} { return ACKId }
func (*ClientInvalidACK) CanReply() bool  { return true }
func (*ClientInvalidACK) Sync() bool      { return true }

// ServerInvalidACK tracks invalid acknowledgement replies. The reply packet
// payload contains the data sent in the `ClientACK` packet.
type ServerInvalidACK struct {
	Updates    []Update
	StatusCode int
}

func (*ServerInvalidACK) Type() PacketType  { return ACK }
func (*ServerInvalidACK) HasRequest() bool  { return true }
func (*ServerInvalidACK) Sync() bool        { return true }
func (a *ServerInvalidACK) Id() interface{} { return ACKId }
func (a *ServerInvalidACK) Status() int     { return a.StatusCode }

// ClientTracked wraps a packet with reply tracking support.
type ClientTracked struct {
	replies chan Reply
	requestWithErrors
}

func (*ClientTracked) CanReply() bool      { return true }
func (t *ClientTracked) Reply(reply Reply) { t.replies <- reply }

func (t *ClientTracked) Close() {
	t.requestWithErrors.Close()
	close(t.replies)
}

func (t *ClientTracked) Do() (reply Reply, err error) {
	select {
	case reply = <-t.replies:
	case err = <-t.getErrors():
	}
	return
}

// ServerUnregister is a reply to a `ClientUnregister` request. Simple Push
// servers are not required to support deregistration, so deregistration
// packets are not tracked by default.
type ServerUnregister struct {
	StatusCode int
	ChannelId  string
}

func (*ServerUnregister) Type() PacketType  { return Unregister }
func (*ServerUnregister) HasRequest() bool  { return true }
func (*ServerUnregister) Sync() bool        { return false }
func (u *ServerUnregister) Id() interface{} { return u.ChannelId }
func (u *ServerUnregister) Status() int     { return u.StatusCode }

// MultipleRegister is a malformed `ClientRegister` packet that specifies
// multiple channel IDs in the payload.
type MultipleRegister struct {
	ClientRegister
}

func (r *MultipleRegister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType PacketType `json:"messageType"`
		ChannelIds  []string   `json:"channelIDs"`
	}{r.Type(), []string{r.ChannelId}})
}

func decodeUnregisterReply(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error) {
	if len(errorText) > 0 {
		return nil, &ServerError{"unregister", c.Origin(), errorText, statusCode}
	}
	channelId, hasChannelId := fields["channelID"].(string)
	if !hasChannelId {
		return nil, &IncompleteError{"register", c.Origin(), "channelID"}
	}
	reply := &ServerUnregister{
		StatusCode: statusCode,
		ChannelId:  channelId,
	}
	return reply, nil
}

func decodeServerInvalidACK(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error) {
	if len(errorText) == 0 {
		return nil, nil
	}
	updates, hasUpdates := fields["updates"].([]interface{})
	if !hasUpdates {
		return nil, &IncompleteError{"notification", c.Origin(), "updates"}
	}
	reply := new(ServerInvalidACK)
	reply.Updates = make([]Update, len(updates))
	for index, field := range updates {
		update, err := c.decodeUpdate(field)
		if err != nil {
			return nil, err
		}
		reply.Updates[index] = *update
	}
	reply.StatusCode = statusCode
	return reply, nil
}

func isValidEndpoint(endpoint string) bool {
	uri, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	if !uri.IsAbs() {
		return false
	}
	return strings.HasPrefix(uri.Path, "/update/")
}

// Sending channel IDs with an unknown device ID should return a new device ID.
func TestHeloReset(t *testing.T) {
	deviceId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating device ID: %#v", err)
	}
	conn, err := DialOrigin(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := &CustomHelo{
		MessageType: "hello",
		DeviceId:    deviceId,
		ChannelIds:  []interface{}{"1", "2"},
		replies:     make(chan Reply),
		errors:      make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if err != nil {
		t.Fatalf("Error writing handshake request: %#v", err)
	}
	helo, ok := reply.(*ServerHelo)
	if !ok {
		t.Fatalf("Type assertion failed for reply: %#v", reply)
	}
	if helo.StatusCode != 200 {
		t.Errorf("Unexpected status code: got %#v; want 200", reply.Status())
	}
	// TODO: Specifying channel IDs for a nonexistent device ID should reset the
	// device ID to its original value. This is not the current memcached
	// adapter behavior.
	if helo.DeviceId != deviceId {
		t.Errorf("Mismatched device ID: got %#v; want %#v", helo.DeviceId, deviceId)
	}
}

type typeTest struct {
	name        string
	messageType interface{}
	deviceId    interface{}
	statusCode  int
}

func (t typeTest) Run() error {
	conn, err := DialOrigin(Origin)
	if err != nil {
		return fmt.Errorf("On test %v, error dialing origin: %#v", t.name, err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := &CustomHelo{
		MessageType: t.messageType,
		DeviceId:    t.deviceId,
		ChannelIds:  []interface{}{"1", "2"},
		Extra:       "custom value",
		replies:     make(chan Reply),
		errors:      make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if t.statusCode >= 200 && t.statusCode < 300 {
		if err != nil {
			return fmt.Errorf("On test %v, error writing handshake request: %#v", t.name, err)
		}
		helo, ok := reply.(*ServerHelo)
		if !ok {
			return fmt.Errorf("On test %v, type assertion failed for handshake reply: %#v", t.name, reply)
		}
		if helo.StatusCode != t.statusCode {
			return fmt.Errorf("On test %v, unexpected reply status: got %#v; want %#v", t.name, helo.StatusCode, t.statusCode)
		}
		deviceId, _ := t.deviceId.(string)
		if len(deviceId) == 0 && !id.Valid(helo.DeviceId) {
			return fmt.Errorf("On test %v, got invalid device ID: %#v", t.name, helo.DeviceId)
		} else if deviceId != helo.DeviceId {
			return fmt.Errorf("On test %v, mismatched device ID: got %#v; want %#v", t.name, helo.DeviceId, deviceId)
		}
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("On test %v, error writing handshake: got %#v; want io.EOF", t.name, err)
	}
	err = conn.Close()
	clientErr, ok := err.(Error)
	if !ok {
		return fmt.Errorf("On test %v, type assertion failed for close error: %#v", t.name, err)
	}
	if clientErr.Status() != t.statusCode {
		return fmt.Errorf("On test %v, unexpected close error status: got %#v; want %#v", t.name, clientErr.Status(), t.statusCode)
	}
	return nil
}

var typeTests = []typeTest{
	typeTest{"invalid device ID", "hello", "invalid_uaid", 503},

	// Leading and trailing whitespace.
	typeTest{"whitespace in device ID", "hello", " fooey barrey ", 503},
	typeTest{"whitespace in message type", " fooey barrey ", validId, 401},

	// Special characters.
	typeTest{"special characters in device ID", "hello", `!@#$%^&*()-+`, 503},
	typeTest{"special characters in message type", `!@#$%^&*()-+`, validId, 401},

	// Integer strings.
	typeTest{`device ID = "0"`, "hello", "0", 200},
	typeTest{`message type = "0"`, "0", validId, 401},
	typeTest{`device ID = "1"`, "hello", "1", 200},
	typeTest{`message type = "1"`, "1", validId, 401},

	// Integers.
	typeTest{"device ID = 0", "hello", 0, 401},
	typeTest{"message type = 0", 0, validId, 401},
	typeTest{"device ID = 1", "hello", 1, 401},
	typeTest{"message type = 1", 1, validId, 401},

	// Negative integers.
	typeTest{"negative integer string as device ID", "hello", "-66000", 200},
	typeTest{"negative integer string as message type", "-66000", validId, 401},
	typeTest{"negative integer as device ID", "hello", -66000, 401},
	typeTest{"negative integer as message type", -66000, validId, 401},

	// "True", "true", "False", and "false".
	typeTest{`device ID = "True"`, "hello", "True", 503},
	typeTest{`message type = "True"`, "True", validId, 401},
	typeTest{`device ID = "true"`, "hello", "true", 503},
	typeTest{`message type = "true"`, "true", validId, 401},
	typeTest{`device ID = "False"`, "hello", "False", 503},
	typeTest{`message type = "False"`, "False", validId, 401},
	typeTest{`device ID = "false"`, "hello", "false", 503},
	typeTest{`message type = "false"`, "false", validId, 401},

	// `true` and `false`.
	typeTest{"device ID = true", "hello", true, 401},
	typeTest{"message type = true", true, validId, 401},
	typeTest{"device ID = false", "hello", false, 401},
	typeTest{"message type = false", false, validId, 401},

	// "None", "null", "nil", and `nil`.
	typeTest{`device ID = "None"`, "hello", "None", 503},
	typeTest{`message type = "None"`, "None", validId, 401},
	typeTest{`device ID = "null"`, "hello", "null", 503},
	typeTest{`message type = "null"`, "null", validId, 401},
	typeTest{`device ID = "nil"`, "hello", "nil", 503},
	typeTest{`message type = "nil"`, "nil", validId, 401},
	typeTest{"device ID = nil", "hello", nilId, 401},
	typeTest{"message type = nil", nilId, validId, 401},

	// Quoted strings.
	typeTest{"quoted string as device ID", "hello", `"foo bar"`, 503},
	typeTest{"quoted string as message type", `"foo bar"`, validId, 401},
}

func TestMessageTypes(t *testing.T) {
	longId, err := GenerateIdSize("", 64000)
	if err != nil {
		t.Fatalf("Error generating device ID: %#v", err)
	}
	if err = (typeTest{"long device ID", "hello", longId, 401}).Run(); err != nil {
		t.Error(err)
	}
	if err = (typeTest{"long message type", longId, validId, 401}).Run(); err != nil {
		t.Error(err)
	}
	for _, test := range typeTests {
		if err := test.Run(); err != nil {
			t.Error(err)
		}
	}
}

func TestDuplicateRegister(t *testing.T) {
	if !AllowDupes {
		t.Log("Duplicate channel IDs not supported; skipping test")
		return
	}
	conn, err := Dial(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	channelId, endpoint, err := conn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %#v", err)
	}
	if !isValidEndpoint(endpoint) {
		t.Errorf("Invalid push endpoint for channel %#v: %#v", channelId, endpoint)
	}
	nextEndpoint, err := conn.Register(channelId)
	if err != nil {
		t.Fatalf("Error writing duplicate registration request: %#v", err)
	}
	if !isValidEndpoint(nextEndpoint) {
		t.Errorf("Invalid push endpoint for duplicate registration: %#v", nextEndpoint)
	}
}

func TestPrematureRegister(t *testing.T) {
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	conn, err := DialOrigin(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := &ClientRegister{
		ChannelId: channelId,
		replies:   make(chan Reply),
		errors:    make(chan error),
	}
	_, err = conn.WriteRequest(request)
	if err != io.EOF {
		t.Fatalf("Error writing premature registration request: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	clientErr, ok := err.(Error)
	if !ok {
		t.Fatalf("Type assertion failed for close error: %#v", err)
	}
	if clientErr.Status() != 401 {
		t.Errorf("Unexpected close error status: got %#v; want 401", clientErr.Status())
	}
}

func TestDuplicateRegisterHandshake(t *testing.T) {
	deviceId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating device ID: %#v", err)
	}
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	conn, err := DialOrigin(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	actualId, err := conn.WriteHelo(deviceId, channelId)
	if err != nil {
		t.Fatalf("Error writing handshake request: %#v", err)
	}
	if actualId != deviceId {
		t.Errorf("Mismatched device ID: got %#v; want %#v", actualId, deviceId)
	}
	if !AllowDupes {
		return
	}
	endpoint, err := conn.Register(channelId)
	if err != nil {
		t.Fatalf("Error writing duplicate registration request: %#v", err)
	}
	if !isValidEndpoint(endpoint) {
		t.Errorf("Invalid push endpoint for channel %#v: %#v", channelId, endpoint)
	}
}

func TestMultipleRegister(t *testing.T) {
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	conn, err := Dial(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := &MultipleRegister{ClientRegister{
		channelId,
		make(chan Reply),
		make(chan error),
	}}
	_, err = conn.WriteRequest(request)
	if err != io.EOF {
		t.Fatalf("Error writing registration request: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	clientErr, ok := err.(Error)
	if !ok {
		t.Fatalf("Type assertion failed for close error: %#v", err)
	}
	if clientErr.Status() != 401 {
		t.Errorf("Unexpected close error status: got %#v; want 401", clientErr.Status())
	}
}

type idTest struct {
	name       string
	channelId  interface{}
	statusCode int
}

func (t idTest) TestHelo() error {
	deviceId, err := id.Generate()
	if err != nil {
		return fmt.Errorf("On test %v, error generating device ID: %#v", t.name, err)
	}
	conn, err := DialOrigin(Origin)
	if err != nil {
		return fmt.Errorf("On test %v, error dialing origin: %#v", t.name, err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := &CustomHelo{
		MessageType: "hello",
		DeviceId:    deviceId,
		ChannelIds:  []interface{}{t.channelId},
		replies:     make(chan Reply),
		errors:      make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if err != nil {
		return fmt.Errorf("On test %v, error writing handshake: %#v", t.name, err)
	}
	helo, ok := reply.(*ServerHelo)
	if !ok {
		return fmt.Errorf("On test %v, type assertion failed for handshake reply: %#v", t.name, reply)
	}
	if helo.StatusCode != 200 {
		return fmt.Errorf("On test %v, unexpected status code: got %#v; want 200", t.name, helo.StatusCode)
	}
	// The Simple Push server requires the `channelIDs` field to be present in
	// the handshake, but does not validate its contents, since any queued
	// messages will be immediately flushed to the client.
	if helo.DeviceId != deviceId {
		return fmt.Errorf("On test %v, mismatched device IDs: got %#v; want %#v", t.name, helo.DeviceId, deviceId)
	}
	return nil
}

func (t idTest) TestRegister() error {
	conn, err := Dial(Origin)
	if err != nil {
		return fmt.Errorf("On test %v, error dialing origin: %#v", t.name, err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := &CustomRegister{
		ChannelId: t.channelId,
		replies:   make(chan Reply),
		errors:    make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if t.statusCode >= 200 && t.statusCode < 300 {
		if err != nil {
			return fmt.Errorf("On test %v, error writing registration request: %#v", t.name, err)
		}
		if reply.Status() != t.statusCode {
			return fmt.Errorf("On test %v, unexpected status code: got %#v; want %#v", t.name, reply.Status(), t.statusCode)
		}
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("On test %v, error writing registration request: got %#v; want io.EOF", t.name, err)
	}
	err = conn.Close()
	clientErr, ok := err.(Error)
	if !ok {
		return fmt.Errorf("On test %v, type assertion failed for close error: %#v", t.name, err)
	}
	if clientErr.Status() != t.statusCode {
		return fmt.Errorf("On test %v, unexpected close error status: got %#v; want %#v", t.name, clientErr.Status(), t.statusCode)
	}
	return nil
}

var idTests = []idTest{
	idTest{"invalid ID", "invalid_uaid", 401},
	idTest{"leading and trailing whitespace", " fooey barrey ", 401},
	idTest{"special characters", `!@#$%^&*()-+`, 401},
	idTest{`"0"`, "0", 200},
	idTest{`"1"`, "1", 200},
	idTest{"negative integer string", "-66000", 401},
	idTest{"negative integer", -66000, 401},
	idTest{`"True"`, "True", 401},
	idTest{`"False"`, "False", 401},
	idTest{`"true"`, "true", 401},
	idTest{`"false"`, "false", 401},
	idTest{"true", true, 401},
	idTest{"false", false, 401},
	idTest{`"None"`, "None", 401},
	idTest{`"null"`, "null", 401},
	idTest{`"nil"`, "nil", 401},
	idTest{"nil", nilId, 401},
	idTest{"quoted string", `"foo bar"`, 401},
	idTest{"null character", "\x00", 401},
	idTest{"control characters", "\x01\x00\x12\x59", 401},
}

func TestRegisterInvalidIds(t *testing.T) {
	for _, test := range idTests {
		if err := test.TestRegister(); err != nil {
			t.Error(err)
		}
	}
}

func TestHeloInvalidIds(t *testing.T) {
	for _, test := range idTests {
		if err := test.TestHelo(); err != nil {
			t.Error(err)
		}
	}
}

func TestPrematureUnregister(t *testing.T) {
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	conn, err := DialOrigin(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("unregister", DecoderFunc(decodeUnregisterReply))
	request := &ClientTracked{
		replies: make(chan Reply),
		requestWithErrors: &ClientUnregister{
			ChannelId: channelId,
			errors:    make(chan error),
		},
	}
	_, err = conn.WriteRequest(request)
	if err != io.EOF {
		t.Fatalf("Error writing deregistration request: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	clientErr, ok := err.(Error)
	if !ok {
		t.Fatalf("Type assertion failed for close error: %#v", err)
	}
	if clientErr.Status() != 401 {
		t.Errorf("Unexpected close error status: got %#v; want 401", clientErr.Status())
	}
}

func TestUnregister(t *testing.T) {
	conn, err := Dial(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	channelId, _, err := conn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %#v", err)
	}
	conn.RegisterDecoder("unregister", DecoderFunc(decodeUnregisterReply))
	for index := 0; index < 2; index++ {
		request := &ClientTracked{
			replies: make(chan Reply),
			requestWithErrors: &ClientUnregister{
				ChannelId: channelId,
				errors:    make(chan error),
			},
		}
		reply, err := conn.WriteRequest(request)
		if err != nil {
			t.Fatalf("Error writing deregistration request (attempt %d): %#v", index, err)
		}
		if reply.Status() != 200 {
			t.Errorf("Unexpected status code for deregistration attempt %d: got %#v, want 200", index, reply.Status())
		}
	}
}

func TestUnregisterRace(t *testing.T) {
	socket, err := ws.Dial(Origin, "", Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	// Spool all notifications, including those received on dregistered channels.
	conn := NewConn(socket, true)
	defer conn.Close()
	if _, err = conn.WriteHelo(""); err != nil {
		t.Fatalf("Error writing handshake request: %#v", err)
	}
	defer conn.Purge()
	channelId, endpoint, err := conn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %#v", err)
	}
	if !isValidEndpoint(endpoint) {
		t.Fatalf("Invalid push endpoint for channel %#v: %#v", channelId, endpoint)
	}
	version := time.Now().UTC().Unix()
	var notifyWait sync.WaitGroup
	signal, errors := make(chan bool), make(chan error)
	notifyWait.Add(2)
	go func() {
		defer notifyWait.Done()
		timeout := time.After(1 * time.Minute)
		var (
			isRemoved    bool
			pendingTimer <-chan time.Time
		)
		for ok := true; ok; {
			var update Update
			select {
			case ok = <-signal:
			case <-timeout:
				ok = false
				errors <- ErrTimedOut

			case <-pendingTimer:
				ok = false

			// Read the update, but don't call `AcceptUpdate()`.
			case update, ok = <-conn.Messages():
				if !ok {
					err = ErrChanClosed
					break
				}
				if update.ChannelId != channelId {
					break
				}
				var err error
				if update.Version != version {
					err = fmt.Errorf("Expected update %#v, not %#v", version, update.Version)
				} else if isRemoved {
					err = fmt.Errorf("Update %#v resent on deregistered channel %#v", update.Version, update.ChannelId)
				} else {
					err = conn.Unregister(channelId)
				}
				if err != nil {
					ok = false
					errors <- err
					break
				}
				isRemoved = true
				timeout = nil
				// Queued updates should be sent immediately.
				pendingTimer = time.After(1 * time.Second)
			}
		}
	}()
	go func() {
		defer notifyWait.Done()
		select {
		case <-signal:
		case errors <- Notify(endpoint, version):
		}
	}()
	go func() {
		notifyWait.Wait()
		close(errors)
	}()
	for err = range errors {
		if err != nil {
			close(signal)
			t.Fatal(err)
		}
	}
}

func TestPing(t *testing.T) {
	conn, err := Dial(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("ping", DecoderFunc(decodePing))
	var pingWait sync.WaitGroup
	writePing := func() {
		defer pingWait.Done()
		reply, err := conn.WriteRequest(&ClientTracked{
			replies:           make(chan Reply),
			requestWithErrors: make(ClientPing),
		})
		if err != nil {
			t.Errorf("Error writing ping request: %#v", err)
			return
		}
		if reply.Status() != 200 {
			t.Errorf("Unexpected ping reply status code: got %#v; want 200", reply.Status())
		}
	}
	writeRegister := func() {
		defer pingWait.Done()
		channelId, endpoint, err := conn.Subscribe()
		if err != nil {
			t.Errorf("Error subscribing to channel: %#v", err)
		}
		if !isValidEndpoint(endpoint) {
			t.Errorf("Invalid push endpoint for channel %#v: %#v", channelId, endpoint)
		}
	}
	pingWait.Add(2)
	go writeRegister()
	go writePing()
	pingWait.Wait()
}

func TestPrematureACK(t *testing.T) {
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	conn, err := DialOrigin(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("ack", DecoderFunc(decodeServerInvalidACK))
	updates := []Update{
		Update{channelId, time.Now().UTC().Unix()},
	}
	clientACK := ClientACK{
		updates,
		make(chan error),
	}
	request := &ClientTracked{
		replies:           make(chan Reply),
		requestWithErrors: &ClientInvalidACK{clientACK},
	}
	reply, err := conn.WriteRequest(request)
	if err != nil {
		t.Fatalf("Error writing acknowledgement: %#v", err)
	}
	if reply.Status() != 401 {
		t.Errorf("Incorrect status code: got %#v, wanted 401", reply.Status())
	}
	if r, ok := reply.(*ServerInvalidACK); ok {
		if len(r.Updates) != len(updates) {
			t.Errorf("Incorrect update count: got %#v; want %#v", len(r.Updates), len(updates))
		} else {
			for index, update := range r.Updates {
				if update != updates[index] {
					t.Errorf("On update %#v, got %#v; want %#v", index, update, updates[index])
				}
			}
		}
	} else {
		t.Errorf("Type assertion failed for reply: %#v", reply)
	}
	// The connection should be closed by the push server after sending
	// the error response.
	if err = conn.Close(); err != io.EOF {
		t.Fatalf("Unexpected close error: got %#v; want io.EOF", err)
	}
}

func TestACK(t *testing.T) {
	conn, err := Dial(Origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	err = PushThrough(conn, 1, 1)
	if err != nil {
		t.Fatalf("Error sending and acknowledge update: %#v", err)
	}
}
