/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/mozilla-services/pushgo/client"
	"github.com/mozilla-services/pushgo/id"
)

var channelIds = id.MustGenerate(maxChannels + 1)

func TestPush(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	// Send 50 messages on 3 channels.
	if err := client.DoTest(origin, 3, 50); err != nil {
		t.Fatalf("Smoke test failed: %#v", err)
	}
}

func roundTrip(conn *client.Conn, deviceId, channelId, endpoint string, version int64) (err error) {
	stopChan, errChan := make(chan bool), make(chan error)
	defer close(stopChan)
	go func() {
		err := client.Notify(endpoint, version)
		if err != nil {
			err = fmt.Errorf("Error sending update %d on channel %q: %s",
				version, channelId, err)
		}
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}()
	go func() {
		var err error
		timeout := time.After(15 * time.Second)
		for ok := true; ok; {
			var packet client.Packet
			select {
			case ok = <-stopChan:
			case <-timeout:
				ok = false
				err = client.ErrTimedOut

			case packet, ok = <-conn.Packets:
				if !ok {
					err = client.ErrChanClosed
					break
				}
				updates, _ := packet.(client.ServerUpdates)
				if len(updates) == 0 {
					continue
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
					continue
				}
				ok = false
				if update.Version != version {
					err = fmt.Errorf("Wrong update version: got %d; want %d",
						update.Version, version)
					break
				}
			}
		}
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}()
	for i := 0; i < 2; i++ {
		if err := <-errChan; err != nil {
			return err
		}
	}
	return nil
}

func sendUpdate(origin string, deviceId *string, canReset bool, channelId string) (err error) {
	originalId := *deviceId
	var channelIds = []string{}
	if !canReset {
		channelIds = append(channelIds, channelId)
	}
	conn, err := client.DialId(origin, deviceId, channelIds...)
	if err != nil {
		return fmt.Errorf("Error dialing origin: %s", err)
	}
	defer conn.Close()
	defer conn.Purge()
	if *deviceId != originalId {
		return fmt.Errorf("Mismatched device IDs: got %q; want %q", *deviceId, originalId)
	}
	endpoint, err := conn.Register(channelId)
	if err != nil {
		return fmt.Errorf("Error subscribing to channel %q: %s", channelId, err)
	}
	if err = roundTrip(conn, *deviceId, channelId, endpoint, 1); err != nil {
		return err
	}
	return nil
}

func TestPushReconnect(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %s", err)
	}
	deviceId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating device ID: %s", err)
	}
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %s", err)
	}
	if err = sendUpdate(origin, &deviceId, true, channelId); err != nil {
		t.Fatalf("Error sending initial notification: %s", err)
	}
	addExistsHook(deviceId, true)
	defer removeExistsHook(deviceId)
	// Allow the client to reconnect if its previous entry has not been removed
	// from the map.
	setReplaceEnabled(true)
	defer setReplaceEnabled(false)
	if err = sendUpdate(origin, &deviceId, false, channelId); err != nil {
		t.Fatalf("Error sending notification after reconnect: %s", err)
	}
}

func TestHandshakeWithId(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	deviceId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating device ID: %#v", err)
	}
	conn, err := client.DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	actualId, err := conn.WriteHelo(deviceId)
	if err != nil {
		t.Fatalf("Error writing handshake request: %#v", err)
	}
	if actualId != deviceId {
		t.Errorf("Mismatched device IDs: got %#v; want %#v", actualId, deviceId)
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
	conn, err := client.DialOrigin(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	defer conn.Purge()
	firstId, err := conn.WriteHelo(deviceId)
	if err != nil {
		t.Fatalf("Error writing initial handshake request: %#v", err)
	}
	if firstId != deviceId {
		t.Errorf("Mismatched device ID for initial handshake: got %#v; want %#v", firstId, deviceId)
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
	conn, _, err := client.Dial(origin)
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
	if clientErr, ok := err.(client.Error); ok && clientErr.Status() != 401 {
		t.Errorf("Unexpected close error status: got %#v; want 401", clientErr.Status())
	} else if !ok {
		t.Fatalf("Type assertion failed for close error: %#v", err)
	}
}

func TestChannelTooLong(t *testing.T) {
	channelId, err := generateIdSize(32)
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
	_, err = conn.Register(channelId)
	if err != io.EOF {
		t.Fatalf("Error writing registration request with large channel ID: got %#v; want io.EOF", err)
	}
	err = conn.Close()
	if clientErr, ok := err.(client.Error); ok && clientErr.Status() != 401 {
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
	conn, err := client.DialOrigin(origin)
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
	conn, _, err := client.Dial(origin)
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
