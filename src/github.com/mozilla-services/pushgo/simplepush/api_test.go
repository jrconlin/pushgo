/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	ws "golang.org/x/net/websocket"

	"github.com/mozilla-services/pushgo/client"
	"github.com/mozilla-services/pushgo/id"
)

const (
	// AllowDupes indicates whether the Simple Push server under test allows
	// duplicate registrations. TODO: Expose as an Application flag.
	AllowDupes = true

	// validId is a placeholder device ID used by typeTest.
	validId = "57954545-c1bc-4fc4-9c1a-cd186d861336"

	// NilId simulates a nil packet ID, as Conn.Send() will close the connection
	// with an error if Request.Id() == nil. getId() converts NilId to nil.
	NilId client.PacketType = -1
)

func getId(r client.Request) (id interface{}) {
	if id = r.Id(); id == NilId {
		return nil
	}
	return
}

func generateIdSize(size int) (results string, err error) {
	bytes := make([]byte, size)
	if _, err = rand.Read(bytes); err != nil {
		return
	}
	return hex.EncodeToString(bytes), nil
}

// CustomRegister is a custom channel registration packet that supports
// arbitrary types for the channel ID.
type CustomRegister struct {
	ChannelId interface{}
	replies   chan client.Reply
	errors    chan error
}

func (CustomRegister) Type() client.PacketType    { return client.Register }
func (CustomRegister) CanReply() bool             { return true }
func (CustomRegister) Sync() bool                 { return false }
func (r CustomRegister) Id() interface{}          { return r.ChannelId }
func (r CustomRegister) Reply(reply client.Reply) { r.replies <- reply }
func (r CustomRegister) Error(err error)          { r.errors <- err }

func (r CustomRegister) Close() {
	close(r.replies)
	close(r.errors)
}

func (r CustomRegister) Do() (reply client.Reply, err error) {
	select {
	case reply = <-r.replies:
	case err = <-r.errors:
	}
	return
}

func (r CustomRegister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType client.PacketType `json:"messageType"`
		ChannelId   interface{}       `json:"channelID"`
	}{r.Type(), getId(r)})
}

// CustomHelo is a custom handshake packet that specifies an extra field and
// supports arbitrary types for the message type, device ID, and channel IDs.
type CustomHelo struct {
	MessageType interface{}
	DeviceId    interface{}
	ChannelIds  []interface{}
	Extra       string
	replies     chan client.Reply
	errors      chan error
}

func (CustomHelo) Type() client.PacketType    { return client.Helo }
func (CustomHelo) CanReply() bool             { return true }
func (CustomHelo) Sync() bool                 { return true }
func (h CustomHelo) Id() interface{}          { return h.DeviceId }
func (h CustomHelo) Reply(reply client.Reply) { h.replies <- reply }
func (h CustomHelo) Error(err error)          { h.errors <- err }

func (h CustomHelo) Close() {
	close(h.replies)
	close(h.errors)
}

func (h CustomHelo) Do() (reply client.Reply, err error) {
	select {
	case reply = <-h.replies:
	case err = <-h.errors:
	}
	return
}

func (h CustomHelo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType interface{}   `json:"messageType"`
		DeviceId    interface{}   `json:"uaid"`
		ChannelIds  []interface{} `json:"channelIDs"`
		Extra       string        `json:"customKey,omitempty"`
	}{h.MessageType, getId(h), h.ChannelIds, h.Extra})
}

// ClientInvalidACK wraps a ClientACK in a synchronous request packet with a
// fabricated message ID to track error replies.
type ClientInvalidACK struct {
	client.Request
}

func (ClientInvalidACK) Sync() bool { return true }

// ServerInvalidACK tracks invalid acknowledgement replies. The reply packet
// payload contains the data sent in the ClientACK.
type ServerInvalidACK struct {
	Updates    []client.Update
	StatusCode int
}

func (ServerInvalidACK) Type() client.PacketType { return client.ACK }
func (ServerInvalidACK) HasRequest() bool        { return true }
func (ServerInvalidACK) Sync() bool              { return true }
func (a ServerInvalidACK) Id() interface{}       { return client.ACKId }
func (a ServerInvalidACK) Status() int           { return a.StatusCode }

// ServerUnregister is a reply to a ClientUnregister request. Simple Push
// servers are not required to support deregistration, so deregistration
// packets are not tracked by default.
type ServerUnregister struct {
	StatusCode int
	ChannelId  string
}

func (ServerUnregister) Type() client.PacketType { return client.Unregister }
func (ServerUnregister) HasRequest() bool        { return true }
func (ServerUnregister) Sync() bool              { return false }
func (u ServerUnregister) Id() interface{}       { return u.ChannelId }
func (u ServerUnregister) Status() int           { return u.StatusCode }

// MultipleRegister is a malformed ClientRegister packet that specifies
// multiple channel IDs in the payload.
type MultipleRegister struct {
	client.ClientRegister
}

func (r MultipleRegister) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MessageType client.PacketType `json:"messageType"`
		ChannelIds  []string          `json:"channelIDs"`
	}{r.Type(), []string{r.ChannelId}})
}

func decodeUnregisterReply(c *client.Conn, fields client.Fields, statusCode int, errorText string) (client.Packet, error) {
	if len(errorText) > 0 {
		return nil, &client.ServerError{"unregister", c.Origin(), errorText, statusCode}
	}
	channelId, hasChannelId := fields["channelID"].(string)
	if !hasChannelId {
		return nil, &client.IncompleteError{"register", c.Origin(), "channelID"}
	}
	reply := ServerUnregister{
		StatusCode: statusCode,
		ChannelId:  channelId,
	}
	return reply, nil
}

func decodeServerInvalidACK(c *client.Conn, fields client.Fields, statusCode int, errorText string) (client.Packet, error) {
	if len(errorText) == 0 {
		return nil, nil
	}
	updates, hasUpdates := fields["updates"].([]interface{})
	if !hasUpdates {
		return nil, &client.IncompleteError{"ack", c.Origin(), "updates"}
	}
	reply := ServerInvalidACK{
		Updates:    make([]client.Update, len(updates)),
		StatusCode: statusCode,
	}
	for index, field := range updates {
		var (
			update    map[string]interface{}
			channelId string
			version   float64
			ok        bool
		)
		if update, ok = field.(map[string]interface{}); !ok {
			return nil, &client.IncompleteError{MessageType: "ack", Origin: c.Origin(), Field: "update"}
		}
		if channelId, ok = update["channelID"].(string); !ok {
			return nil, &client.IncompleteError{MessageType: "ack", Origin: c.Origin(), Field: "channelID"}
		}
		if version, ok = update["version"].(float64); !ok {
			return nil, &client.IncompleteError{MessageType: "ack", Origin: c.Origin(), Field: "version"}
		}
		reply.Updates[index] = client.Update{ChannelId: channelId, Version: int64(version)}
	}
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

type typeTest struct {
	name        string
	messageType interface{}
	deviceId    interface{}
	statusCode  int
	shouldReset bool
}

func (t typeTest) Run() error {
	origin, err := Server.Origin()
	if err != nil {
		return fmt.Errorf("On test %v, error initializing test server: %#v", t.name, err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		return fmt.Errorf("On test %v, error dialing origin: %#v", t.name, err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := CustomHelo{
		MessageType: t.messageType,
		DeviceId:    t.deviceId,
		ChannelIds:  []interface{}{"1", "2"},
		Extra:       "custom value",
		replies:     make(chan client.Reply),
		errors:      make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if t.statusCode >= 200 && t.statusCode < 300 {
		if err != nil {
			return fmt.Errorf("On test %v, error writing handshake request: %#v", t.name, err)
		}
		helo, ok := reply.(client.ServerHelo)
		if !ok {
			return fmt.Errorf("On test %v, type assertion failed for handshake reply: %#v", t.name, reply)
		}
		if helo.StatusCode != t.statusCode {
			return fmt.Errorf("On test %v, unexpected reply status: got %#v; want %#v", t.name, helo.StatusCode, t.statusCode)
		}
		deviceId, _ := t.deviceId.(string)
		if len(deviceId) == 0 && !id.Valid(helo.DeviceId) {
			return fmt.Errorf("On test %v, got invalid device ID: %#v", t.name, helo.DeviceId)
		} else if !t.shouldReset && deviceId != helo.DeviceId {
			return fmt.Errorf("On test %v, mismatched device ID: got %#v; want %#v", t.name, helo.DeviceId, deviceId)
		} else if t.shouldReset && deviceId == helo.DeviceId {
			return fmt.Errorf("On test %v, want new device ID; got %#v", t.name, deviceId)
		}
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("On test %v, error writing handshake: got %#v; want io.EOF", t.name, err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
	if !ok {
		return fmt.Errorf("On test %v, type assertion failed for close error: %#v", t.name, err)
	}
	if clientErr.Status() != t.statusCode {
		return fmt.Errorf("On test %v, unexpected close error status: got %#v; want %#v", t.name, clientErr.Status(), t.statusCode)
	}
	return nil
}

var typeTests = []typeTest{
	{"invalid device ID", "hello", "invalid_uaid", 503, true},

	// Leading and trailing whitespace.
	{"whitespace in device ID", "hello", " fooey barrey ", 503, true},
	{"whitespace in message type", " fooey barrey ", validId, 401, true},

	// Special characters.
	{"special characters in device ID", "hello", `!@#$%^&*()-+`, 503, true},
	{"special characters in message type", `!@#$%^&*()-+`, validId, 401, true},

	// Integer strings.
	{`device ID = "0"`, "hello", "0", 503, true},
	{`message type = "0"`, "0", validId, 401, true},
	{`device ID = "1"`, "hello", "1", 503, true},
	{`message type = "1"`, "1", validId, 401, true},

	// Integers.
	{"device ID = 0", "hello", 0, 401, true},
	{"message type = 0", 0, validId, 401, true},
	{"device ID = 1", "hello", 1, 401, true},
	{"message type = 1", 1, validId, 401, true},

	// Negative integers.
	{"negative integer string as device ID", "hello", "-66000", 503, true},
	{"negative integer string as message type", "-66000", validId, 401, true},
	{"negative integer as device ID", "hello", -66000, 401, true},
	{"negative integer as message type", -66000, validId, 401, true},

	// "True", "true", "False", and "false".
	{`device ID = "True"`, "hello", "True", 503, true},
	{`message type = "True"`, "True", validId, 401, true},
	{`device ID = "true"`, "hello", "true", 503, true},
	{`message type = "true"`, "true", validId, 401, true},
	{`device ID = "False"`, "hello", "False", 503, true},
	{`message type = "False"`, "False", validId, 401, true},
	{`device ID = "false"`, "hello", "false", 503, true},
	{`message type = "false"`, "false", validId, 401, true},

	// `true` and `false`.
	{"device ID = true", "hello", true, 401, true},
	{"message type = true", true, validId, 401, true},
	{"device ID = false", "hello", false, 401, true},
	{"message type = false", false, validId, 401, true},

	// "None", "null", "nil", and `nil`.
	{`device ID = "None"`, "hello", "None", 503, true},
	{`message type = "None"`, "None", validId, 401, true},
	{`device ID = "null"`, "hello", "null", 503, true},
	{`message type = "null"`, "null", validId, 401, true},
	{`device ID = "nil"`, "hello", "nil", 503, true},
	{`message type = "nil"`, "nil", validId, 401, true},
	{"message type = nil", NilId, validId, 401, true},

	// Quoted strings.
	{"quoted string as device ID", "hello", `"foo bar"`, 503, true},
	{"quoted string as message type", `"foo bar"`, validId, 401, true},
}

func TestMessageTypes(t *testing.T) {
	longId, err := generateIdSize(64000)
	if err != nil {
		t.Fatalf("Error generating longId: %#v", err)
	}

	existingId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating existingId: %#v", err)
	}
	addExistsHook(existingId, true)
	defer removeExistsHook(existingId)

	missingId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating missingId: %#v", err)
	}
	addExistsHook(missingId, false)
	defer removeExistsHook(missingId)

	specialTypes := []typeTest{
		{"long device ID", "hello", longId, 503, true},
		{"long message type", longId, validId, 401, true},

		{"existing device ID with channels", "hello", existingId, 200, false},
		// Sending channel IDs with an unknown device ID should return a new device ID.
		{"unknown device ID with channels", "hello", missingId, 200, true},
	}
	for _, test := range specialTypes {
		if err := test.Run(); err != nil {
			t.Error(err)
		}
	}

	for _, test := range typeTests {
		if err := test.Run(); err != nil {
			t.Error(err)
		}
	}
}

func TestNilDeviceId(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := CustomHelo{
		MessageType: "hello",
		DeviceId:    NilId,
		ChannelIds:  []interface{}{},
		Extra:       "extra field",
		replies:     make(chan client.Reply),
		errors:      make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if err != nil {
		t.Fatalf("Error writing handshake request: %#v", err)
	}
	helo, ok := reply.(client.ServerHelo)
	if !ok {
		t.Errorf("Type assertion failed for handshake reply: %#v", reply)
	}
	if !id.Valid(helo.DeviceId) {
		t.Errorf("Got invalid device ID: %#v", helo.DeviceId)
	}
}

func TestDuplicateRegister(t *testing.T) {
	if !AllowDupes {
		t.Log("Duplicate channel IDs not supported; skipping test")
		return
	}
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, _, err := client.Dial(origin)
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
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := client.NewRegister(channelId)
	_, err = conn.WriteRequest(request)
	if err != io.EOF {
		t.Fatalf("Error writing premature registration request: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
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
	addExistsHook(deviceId, true)
	defer removeExistsHook(deviceId)
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := client.DialOrigin(origin)
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
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, _, err := client.Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := MultipleRegister{client.NewRegister(channelId).(client.ClientRegister)}
	_, err = conn.WriteRequest(request)
	if err != io.EOF {
		t.Fatalf("Error writing registration request: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
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
		return fmt.Errorf("On handshake test %v, error generating device ID: %#v", t.name, err)
	}
	addExistsHook(deviceId, true)
	defer removeExistsHook(deviceId)
	origin, err := Server.Origin()
	if err != nil {
		return fmt.Errorf("On handshake test %v, error initializing test server: %#v", t.name, err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		return fmt.Errorf("On handshake test %v, error dialing origin: %#v", t.name, err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := CustomHelo{
		MessageType: "hello",
		DeviceId:    deviceId,
		ChannelIds:  []interface{}{t.channelId},
		replies:     make(chan client.Reply),
		errors:      make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if t.statusCode >= 200 && t.statusCode < 300 {
		if err != nil {
			return fmt.Errorf("On handshake test %v, error writing request: %#v", t.name, err)
		}
		helo, ok := reply.(client.ServerHelo)
		if !ok {
			return fmt.Errorf("On handshake test %v, type assertion failed for reply: %#v", t.name, reply)
		}
		if helo.StatusCode != 200 {
			return fmt.Errorf("On handshake test %v, unexpected status code: got %#v; want 200", t.name, helo.StatusCode)
		}
		// The Simple Push server requires the channelIDs field to be present in
		// the handshake, but does not validate its contents, since any queued
		// messages will be immediately flushed to the client.
		if helo.DeviceId != deviceId {
			return fmt.Errorf("On handshake test %v, mismatched device ID: got %#v; want %#v", t.name, helo.DeviceId, deviceId)
		}
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("On handshake test %v, error writing request: got %#v; want io.EOF", t.name, err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
	if !ok {
		return fmt.Errorf("On handshake test %v, type assertion failed for close error: %#v", t.name, err)
	}
	if clientErr.Status() != t.statusCode {
		return fmt.Errorf("On handshake test %v, unexpected close error status: got %#v; want %#v", t.name, clientErr.Status(), t.statusCode)
	}
	return nil
}

func (t idTest) TestRegister() error {
	origin, err := Server.Origin()
	if err != nil {
		return fmt.Errorf("On registration test %v, error initializing test server: %#v", t.name, err)
	}
	conn, _, err := client.Dial(origin)
	if err != nil {
		return fmt.Errorf("On registration test %v, error dialing origin: %#v", t.name, err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := CustomRegister{
		ChannelId: t.channelId,
		replies:   make(chan client.Reply),
		errors:    make(chan error),
	}
	reply, err := conn.WriteRequest(request)
	if t.statusCode >= 200 && t.statusCode < 300 {
		if err != nil {
			return fmt.Errorf("On registration test %v, error writing request: %#v", t.name, err)
		}
		if reply.Status() != t.statusCode {
			return fmt.Errorf("On registration test %v, unexpected status code: got %#v; want %#v", t.name, reply.Status(), t.statusCode)
		}
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("On registration test %v, error writing request: got %#v; want io.EOF", t.name, err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
	if !ok {
		return fmt.Errorf("On registration test %v, type assertion failed for close error: %#v", t.name, err)
	}
	if clientErr.Status() != t.statusCode {
		return fmt.Errorf("On registration test %v, unexpected close error status: got %#v; want %#v", t.name, clientErr.Status(), t.statusCode)
	}
	return nil
}

var registerIdTests = []idTest{
	{"invalid ID", "invalid_uaid", 401},
	{"leading and trailing whitespace", " fooey barrey ", 401},
	{"special characters", `!@#$%^&*()-+`, 401},
	{`"0"`, "0", 401},
	{`"1"`, "1", 401},
	{"negative integer string", "-66000", 401},
	{"negative integer", -66000, 401},
	{`"True"`, "True", 401},
	{`"False"`, "False", 401},
	{`"true"`, "true", 401},
	{`"false"`, "false", 401},
	{"true", true, 401},
	{"false", false, 401},
	{`"None"`, "None", 401},
	{`"null"`, "null", 401},
	{`"nil"`, "nil", 401},
	{"nil", NilId, 401},
	{"quoted string", `"foo bar"`, 401},
	{"null character", "\x00", 401},
	{"control characters", "\x01\x00\x12\x59", 401},
}

func TestRegisterInvalidIds(t *testing.T) {
	for _, test := range registerIdTests {
		if err := test.TestRegister(); err != nil {
			t.Error(err)
		}
	}
}

// The Simple Push server does not validate the contents of the channelIDs
// field, so handshakes containing invalid channel IDs should succeed.
var heloIdTests = []idTest{
	{"invalid channel ID", "invalid_uaid", 200},
	{"leading and trailing whitespace in channel ID", " fooey barrey ", 200},
	{"special characters in channel ID", `!@#$%^&*()-+`, 200},
	{`channel ID = "0"`, "0", 200},
	{`channel ID = "1"`, "1", 200},
	{"negative integer string as channel ID", "-66000", 200},
	{"negative integer as channel ID", -66000, 200},
	{`channel ID = "True"`, "True", 200},
	{`channel ID = "False"`, "False", 200},
	{`channel ID = "true"`, "true", 200},
	{`channel ID = "false"`, "false", 200},
	{"channel ID = true", true, 200},
	{"channel ID = false", false, 200},
	{`channel ID = "None"`, "None", 200},
	{`channel ID = "null"`, "null", 200},
	{`channel ID = "nil"`, "nil", 200},
	{"channel ID = nil", NilId, 200},
	{"quoted string as channel ID", `"foo bar"`, 200},
	{"null character in channel ID", "\x00", 200},
	{"control characters in channel ID", "\x01\x00\x12\x59", 200},
}

func TestHeloInvalidIds(t *testing.T) {
	for _, test := range heloIdTests {
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
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("unregister", client.DecoderFunc(decodeUnregisterReply))
	request := client.NewUnregister(channelId, true)
	_, err = conn.WriteRequest(request)
	if err != io.EOF {
		t.Fatalf("Error writing deregistration request: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
	if !ok {
		t.Fatalf("Type assertion failed for close error: %#v", err)
	}
	if clientErr.Status() != 401 {
		t.Errorf("Unexpected close error status: got %#v; want 401", clientErr.Status())
	}
}

func TestUnregister(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, _, err := client.Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	channelId, _, err := conn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %#v", err)
	}
	conn.RegisterDecoder("unregister", client.DecoderFunc(decodeUnregisterReply))
	for index := 0; index < 2; index++ {
		request := client.NewUnregister(channelId, true)
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
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	socket, err := ws.Dial(origin, "", origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	// Spool all notifications, including those received on dregistered channels.
	conn := client.NewConn(socket, true)
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
			var packet client.Packet
			select {
			case ok = <-signal:
			case <-timeout:
				ok = false
				errors <- client.ErrTimedOut

			case <-pendingTimer:
				ok = false

			// Read the update, but don't call AcceptUpdate().
			case packet, ok = <-conn.Packets:
				if !ok {
					err = client.ErrChanClosed
					break
				}
				var (
					updates    client.ServerUpdates
					hasUpdates bool
				)
				if updates, hasUpdates = packet.(client.ServerUpdates); !hasUpdates {
					break
				}
				var (
					update    client.Update
					hasUpdate bool
				)
				for _, update = range updates {
					if hasUpdate = update.ChannelId == channelId; hasUpdate {
						break
					}
				}
				if !hasUpdate {
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
		case errors <- client.Notify(endpoint, version):
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
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, _, err := client.Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("ping", client.DecoderFunc(decodePing))
	var pingWait sync.WaitGroup
	writePing := func() {
		defer pingWait.Done()
		reply, err := conn.WriteRequest(client.NewPing(true))
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
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("ack", client.DecoderFunc(decodeServerInvalidACK))
	updates := []client.Update{
		client.Update{channelId, time.Now().UTC().Unix()},
	}
	request := ClientInvalidACK{client.NewACK(updates, true)}
	reply, err := conn.WriteRequest(request)
	if err != nil {
		t.Fatalf("Error writing acknowledgement: %#v", err)
	}
	if reply.Status() != 401 {
		t.Errorf("Incorrect status code: got %#v, wanted 401", reply.Status())
	}
	if r, ok := reply.(ServerInvalidACK); ok {
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
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, _, err := client.Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	err = client.PushThrough(conn, 1, 1)
	if err != nil {
		t.Fatalf("Error sending and acknowledge update: %#v", err)
	}
}
