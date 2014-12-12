/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/mozilla-services/pushgo/client"
	"github.com/mozilla-services/pushgo/id"
)

var ErrInvalidCaseTest = &client.ClientError{"Invalid case test type."}

// CaseTestType is used by Case{ClientPing, ACK}.MarshalJSON() to generate
// different JSON representations of the underlying ping or ACK packet. If
// a packet doesn't support a particular representation (e.g., CaseACK
// doesn't support any of the FieldType* test types), MarshalJSON() should
// return ErrInvalidCaseTest.
type CaseTestType int

const (
	FieldTypeLower CaseTestType = iota + 1
	FieldTypeSpace
	FieldChansCap
	FieldIdCap
	ValueTypeUpper
	ValueTypeCap
	ValueTypeEmpty
)

func (t CaseTestType) String() string {
	switch t {
	case FieldTypeLower:
		return "lowercase message type field name"
	case FieldTypeSpace:
		return "leading and trailing whitespace in message type field name"
	case FieldChansCap:
		return "mixed-case channel IDs field name"
	case FieldIdCap:
		return "mixed-case device ID field name"
	case ValueTypeUpper:
		return "uppercase message type value"
	case ValueTypeCap:
		return "mixed-case message type value"
	case ValueTypeEmpty:
		return "empty message type value"
	}
	return "unknown case test type"
}

func decodePing(c *client.Conn, fields client.Fields, statusCode int, errorText string) (client.Packet, error) {
	if len(errorText) > 0 {
		return nil, &client.ServerError{"ping", c.Origin(), errorText, statusCode}
	}
	return ServerPing{statusCode}, nil
}

func decodeCaseACK(c *client.Conn, fields client.Fields, statusCode int, errorText string) (client.Packet, error) {
	if len(errorText) > 0 {
		return nil, &client.ServerError{"ack", c.Origin(), errorText, statusCode}
	}
	return nil, nil
}

type caseTest struct {
	CaseTestType
	statusCode  int
	shouldReset bool
}

func (t caseTest) TestPing() error {
	origin, err := Server.Origin()
	if err != nil {
		return fmt.Errorf("On test %v, error initializing test server: %#v", t.CaseTestType, err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		return fmt.Errorf("On test %v, error dialing origin: %#v", t.CaseTestType, err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("ping", client.DecoderFunc(decodePing))
	request := CaseClientPing{t.CaseTestType, client.NewPing(true)}
	reply, err := conn.WriteRequest(request)
	if err != nil {
		return fmt.Errorf("On test %v, error writing ping packet: %#v", t.CaseTestType, err)
	}
	if reply.Status() != t.statusCode {
		return fmt.Errorf("On test %v, unexpected status code: got %#v; want %#v", t.CaseTestType, reply.Status(), t.statusCode)
	}
	return nil
}

func (t caseTest) TestACK() error {
	origin, err := Server.Origin()
	if err != nil {
		return fmt.Errorf("On test %v, error initializing test server: %#v", t.CaseTestType, err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		return fmt.Errorf("On test %v, error dialing origin: %#v", t.CaseTestType, err)
	}
	defer conn.Close()
	defer conn.Purge()
	conn.RegisterDecoder("ack", client.DecoderFunc(decodeCaseACK))
	request := CaseACK{t.CaseTestType, client.NewACK(nil, true)}
	_, err = conn.WriteRequest(request)
	if t.statusCode >= 200 && t.statusCode < 300 {
		if err != nil {
			return fmt.Errorf("On test %v, error writing acknowledgement: %#v", t.CaseTestType, err)
		}
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("On test %v, error writing acknowledgement: got %#v; want io.EOF", t.CaseTestType, err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
	if !ok {
		return fmt.Errorf("On test %v, type assertion failed for close error: %#v", t.CaseTestType, err)
	}
	if clientErr.Status() != t.statusCode {
		return fmt.Errorf("On test %v, unexpected close error status: got %#v; want %#v", t.CaseTestType, clientErr.Status(), t.statusCode)
	}
	return nil
}

func (t caseTest) TestHelo() error {
	deviceId, err := id.Generate()
	if err != nil {
		return fmt.Errorf("On test %v, error generating device ID: %#v", t.CaseTestType, err)
	}
	addExistsHook(deviceId, true)
	defer removeExistsHook(deviceId)
	channelId, err := id.Generate()
	if err != nil {
		return fmt.Errorf("On test %v, error generating channel ID: %#v", t.CaseTestType, err)
	}
	origin, err := Server.Origin()
	if err != nil {
		return fmt.Errorf("On test %v, error initializing test server: %#v", t.CaseTestType, err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		return fmt.Errorf("On test %v, error dialing origin: %#v", t.CaseTestType, err)
	}
	defer conn.Close()
	defer conn.Purge()
	request := CaseHelo{t.CaseTestType, client.NewHelo(deviceId, []string{channelId}).(client.ClientHelo)}
	reply, err := conn.WriteRequest(request)
	if t.statusCode >= 200 && t.statusCode < 300 {
		if err != nil {
			return fmt.Errorf("On test %v, error writing handshake request: %#v", t.CaseTestType, err)
		}
		helo, ok := reply.(client.ServerHelo)
		if !ok {
			return fmt.Errorf("On test %v, type assertion failed for handshake reply: %#v", t.CaseTestType, reply)
		}
		if helo.StatusCode != t.statusCode {
			return fmt.Errorf("On test %v, unexpected reply status: got %#v; want %#v", t.CaseTestType, helo.StatusCode, t.statusCode)
		}
		if t.shouldReset {
			if helo.DeviceId == deviceId {
				return fmt.Errorf("On test %v, want new device ID; got %#v", t.CaseTestType, deviceId)
			}
			return nil
		}
		if helo.DeviceId != deviceId {
			return fmt.Errorf("On test %v, mismatched device ID: got %#v; want %#v", t.CaseTestType, helo.DeviceId, deviceId)
		}
		return nil
	}
	if err != io.EOF {
		return fmt.Errorf("On test %v, error writing handshake: got %#v; want io.EOF", t.CaseTestType, err)
	}
	err = conn.Close()
	clientErr, ok := err.(client.Error)
	if !ok {
		return fmt.Errorf("On test %v, type assertion failed for close error: %#v", t.CaseTestType, err)
	}
	if clientErr.Status() != t.statusCode {
		return fmt.Errorf("On test %v, unexpected close error status: got %#v; want %#v", t.CaseTestType, clientErr.Status(), t.statusCode)
	}
	return nil
}

type CaseACK struct {
	CaseTestType
	client.Request
}

func (CaseACK) Sync() bool { return true }

func (a CaseACK) MarshalJSON() ([]byte, error) {
	var results interface{}
	messageType := a.Type().String()
	packet := a.Request.(client.ClientACK)
	switch a.CaseTestType {
	case ValueTypeUpper:
		results = struct {
			MessageType string          `json:"messageType"`
			Updates     []client.Update `json:"updates"`
		}{strings.ToUpper(messageType), packet.Updates}

	case ValueTypeCap:
		results = struct {
			MessageType string          `json:"messageType"`
			Updates     []client.Update `json:"updates"`
		}{strings.ToUpper(messageType[:1]) + strings.ToLower(messageType[1:]), packet.Updates}

	case ValueTypeEmpty:
		results = struct {
			MessageType string          `json:"messageType"`
			Updates     []client.Update `json:"updates"`
		}{"", packet.Updates}

	default:
		return nil, ErrInvalidCaseTest
	}
	return json.Marshal(results)
}

type CaseClientPing struct {
	CaseTestType
	client.Request
}

func (p CaseClientPing) Sync() bool { return true }

func (p CaseClientPing) Close() {
	p.Request.Close()
}

func (p CaseClientPing) MarshalJSON() ([]byte, error) {
	switch p.CaseTestType {
	case ValueTypeUpper:
		return []byte(`{"messageType":"PING"}`), nil

	case ValueTypeCap:
		return []byte(`{"messageType":"Ping"}`), nil

	case ValueTypeEmpty:
		return []byte{'{', '}'}, nil
	}
	return nil, ErrInvalidCaseTest
}

type ServerPing struct {
	StatusCode int
}

func (ServerPing) Type() client.PacketType { return client.Ping }
func (ServerPing) Id() interface{}         { return client.PingId }
func (ServerPing) HasRequest() bool        { return true }
func (ServerPing) Sync() bool              { return true }
func (p ServerPing) Status() int           { return p.StatusCode }

type CaseHelo struct {
	CaseTestType
	client.ClientHelo
}

func (h CaseHelo) MarshalJSON() ([]byte, error) {
	var results interface{}
	messageType := h.Type().String()
	switch h.CaseTestType {
	case FieldTypeLower:
		results = struct {
			MessageType string   `json:"messagetype"`
			DeviceId    string   `json:"uaid"`
			ChannelIds  []string `json:"channelIDs"`
		}{messageType, h.DeviceId, h.ChannelIds}

	case FieldTypeSpace:
		results = struct {
			MessageType string   `json:" messageType "`
			DeviceId    string   `json:"uaid"`
			ChannelIds  []string `json:"channelIDs"`
		}{messageType, h.DeviceId, h.ChannelIds}

	case FieldChansCap:
		results = struct {
			MessageType string   `json:"messageType"`
			DeviceId    string   `json:"uaid"`
			ChannelIds  []string `json:"ChannelIDs"`
		}{messageType, h.DeviceId, h.ChannelIds}

	case FieldIdCap:
		results = struct {
			MessageType string   `json:"messageType"`
			DeviceId    string   `json:"uaiD"`
			ChannelIds  []string `json:"channelIDs"`
		}{messageType, h.DeviceId, h.ChannelIds}

	case ValueTypeUpper:
		results = struct {
			MessageType string   `json:"messageType"`
			DeviceId    string   `json:"uaid"`
			ChannelIds  []string `json:"channelIDs"`
		}{strings.ToUpper(messageType), h.DeviceId, h.ChannelIds}

	case ValueTypeCap:
		results = struct {
			MessageType string   `json:"messageType"`
			DeviceId    string   `json:"uaid"`
			ChannelIds  []string `json:"channelIDs"`
		}{strings.ToUpper(messageType[:1]) + strings.ToLower(messageType[1:]), h.DeviceId, h.ChannelIds}

	case ValueTypeEmpty:
		results = struct {
			MessageType string   `json:"messageType"`
			DeviceId    string   `json:"uaid"`
			ChannelIds  []string `json:"channelIDs"`
		}{"", h.DeviceId, h.ChannelIds}

	default:
		return nil, ErrInvalidCaseTest
	}
	return json.Marshal(results)
}

var ackTests = []caseTest{
	caseTest{ValueTypeUpper, 401, false},
	caseTest{ValueTypeCap, 401, false},
	caseTest{ValueTypeEmpty, 401, false},
}

func TestACKCase(t *testing.T) {
	for _, test := range ackTests {
		if err := test.TestACK(); err != nil {
			t.Error(err)
		}
	}
}

var pingTests = []caseTest{
	caseTest{ValueTypeUpper, 200, false},
	caseTest{ValueTypeCap, 200, false},
	caseTest{ValueTypeEmpty, 200, false},
}

func TestPingCase(t *testing.T) {
	for _, test := range pingTests {
		if err := test.TestPing(); err != nil {
			t.Error(err)
		}
	}
}

var heloTests = []caseTest{
	caseTest{FieldTypeSpace, 401, false},
	caseTest{FieldIdCap, 200, false},
	caseTest{ValueTypeUpper, 200, false},
	caseTest{ValueTypeCap, 200, false},
	caseTest{ValueTypeEmpty, 401, false},
}

func TestHeloCase(t *testing.T) {
	for _, test := range heloTests {
		if err := test.TestHelo(); err != nil {
			t.Error(err)
		}
	}
}
