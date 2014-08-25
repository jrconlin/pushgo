/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"io"
	"sync"
	"testing"

	"github.com/mozilla-services/pushgo/id"
)

const (
	// maxChannels is the maximum number of channels allowed in the opening
	// handshake. Clients that specify more channels will receive a new device
	// ID. Can be obtained via `app.Store.MaxChannels()`.
	maxChannels = 500
)

var channelIds = MustGenerateIds(maxChannels + 1)

func TestPush(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	// Send 50 messages on 3 channels.
	if err := DoTest(origin, 3, 50); err != nil {
		t.Fatalf("Smoke test failed: %#v", err)
	}
}

func TestDuplicateHandshake(t *testing.T) {
	deviceId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating device ID: %#v", err)
	}
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	firstId, err := conn.WriteHelo(deviceId)
	if err != nil {
		t.Fatalf("Error writing initial handshake request: %#v", err)
	}
	if firstId == deviceId {
		t.Errorf("Want new device ID for initial handshake; got %#v", firstId)
	}
	secondId, err := conn.WriteHelo(firstId)
	if err != nil {
		t.Fatalf("Error writing duplicate handshake request: %#v", err)
	}
	if secondId != firstId {
		t.Errorf("Mismatched device ID for duplicate handshake: got %#v; want %#v", secondId, firstId)
	}
	thirdId, err := conn.WriteHelo("")
	if err != nil {
		t.Fatalf("Error writing implicit handshake request: %#v", err)
	}
	if thirdId != secondId {
		t.Errorf("Mismatched device ID for implicit handshake: got %#v; want %#v", thirdId, secondId)
	}
}

func TestMultiRegister(t *testing.T) {
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	endpoint, err := conn.Register(channelId)
	if err != nil {
		t.Fatalf("Error writing registration request: %#v", err)
	}
	if !isValidEndpoint(endpoint) {
		t.Errorf("Invalid push endpoint: %#v", endpoint)
	}
	_, err = conn.Register("")
	if err != io.EOF {
		t.Fatalf("Error writing malformed registration request: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	if clientErr, ok := err.(Error); ok && clientErr.Status() != 401 {
		t.Errorf("Unexpected close error status: got %#v; want 401", clientErr.Status())
	} else if !ok {
		t.Fatalf("Type assertion failed for close error: %#v", err)
	}
}

func TestChannelTooLong(t *testing.T) {
	channelId, err := GenerateIdSize("", 32)
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	_, err = conn.Register(channelId)
	if err != io.EOF {
		t.Fatalf("Error writing registration request with large channel ID: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	if clientErr, ok := err.(Error); ok && clientErr.Status() != 401 {
		t.Errorf("Unexpected close error status: got %#v; want 401", clientErr.Status())
	} else if !ok {
		t.Fatalf("Type assertion failed for close error: %#v", err)
	}
}

func TestTooManyChannels(t *testing.T) {
	deviceId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating device ID: %#v", err)
	}
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	actualId, err := conn.WriteHelo(deviceId, channelIds...)
	if err != nil {
		t.Fatalf("Error writing large handshake request: %#v", err)
	}
	if actualId == deviceId {
		t.Errorf("Want new device ID; got %#v", actualId)
	}
}

func TestRegisterSeparate(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, err := Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	var registerWait sync.WaitGroup
	registerOne := func(channelId string) {
		defer registerWait.Done()
		endpoint, err := conn.Register(channelId)
		if err != nil {
			t.Errorf("On test %v, error writing registration request: %#v", channelId, err)
			return
		}
		if !isValidEndpoint(endpoint) {
			t.Errorf("On test %v, invalid push endpoint: %#v", channelId, endpoint)
		}
	}
	defer conn.Close()
	defer conn.Purge()
	registerWait.Add(len(channelIds))
	for _, channelId := range channelIds {
		go registerOne(channelId)
	}
	registerWait.Wait()
}
