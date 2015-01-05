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

	ws "golang.org/x/net/websocket"

	"github.com/mozilla-services/pushgo/client"
	"github.com/mozilla-services/pushgo/id"
)

var channelIds = id.MustGenerate(maxChannels + 1)

func TestPush(t *testing.T) {
	origin, err := testServer.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	// Send 50 messages on 3 channels.
	if err := client.DoTest(origin, 3, 50); err != nil {
		t.Fatalf("Smoke test failed: %s", err)
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
		var (
			pendingAccepts []client.Update
			err            error
		)
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
				pendingAccepts = append(pendingAccepts, updates...)
				var (
					update    client.Update
					hasUpdate bool
				)
				for _, update = range updates {
					if update.ChannelId == channelId && update.Version >= version {
						hasUpdate = true
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
		if acceptErr := conn.AcceptBatch(pendingAccepts); acceptErr != nil {
			err = fmt.Errorf("Error acknowledging updates: %s", acceptErr)
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

func reconnect(origin, deviceId, channelId, endpoint string) (err error) {
	socket, err := ws.Dial(origin, "", origin)
	if err != nil {
		return fmt.Errorf("Error dialing origin: %s", err)
	}
	connId, err := id.Generate()
	if err != nil {
		return fmt.Errorf("Error generating connection ID: %#v", err)
	}
	conn := client.NewConn(socket, connId, true)
	defer conn.Close()
	defer conn.Purge()
	actualId, err := conn.WriteHelo(deviceId, channelId)
	if err != nil {
		return fmt.Errorf("Error writing handshake request: %s", err)
	}
	if actualId != deviceId {
		return fmt.Errorf("Mismatched device IDs: got %q; want %q",
			actualId, deviceId)
	}
	if err = roundTrip(conn, deviceId, channelId, endpoint, 2); err != nil {
		return fmt.Errorf("Error sending notification after reconnect: %s", err)
	}
	return nil
}

func connect(origin string) (deviceId, channelId, endpoint string, err error) {
	if channelId, err = id.Generate(); err != nil {
		err = fmt.Errorf("Error generating channel ID: %s", err)
		return
	}
	conn, deviceId, err := client.Dial(origin)
	if err != nil {
		err = fmt.Errorf("Error dialing origin: %s", err)
		return
	}
	defer conn.Close()
	defer conn.Purge()
	if endpoint, err = conn.Register(channelId); err != nil {
		err = fmt.Errorf("Error subscribing to channel %q: %s",
			channelId, err)
		return
	}
	if err = roundTrip(conn, deviceId, channelId, endpoint, 1); err != nil {
		err = fmt.Errorf("Error sending initial notification: %s", err)
		return
	}
	return
}

func TestPushReconnect(t *testing.T) {
	origin, err := testServer.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %s", err)
	}
	deviceId, channelId, endpoint, err := connect(origin)
	if err != nil {
		t.Fatal(err)
	}
	addExistsHook(deviceId, true)
	defer removeExistsHook(deviceId)
	if err = reconnect(origin, deviceId, channelId, endpoint); err != nil {
		t.Fatal(err)
	}
}

func TestDupeDisconnect(t *testing.T) {
	channelId, err := id.Generate()
	if err != nil {
		t.Fatalf("Error generating channel ID: %#v", err)
	}
	origin, err := testServer.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %s", err)
	}
	conn, deviceId, err := client.Dial(origin, channelId)
	if err != nil {
		t.Fatalf("Error dialing origin: %s", err)
	}
	defer conn.Close()
	stopChan := make(chan bool)
	defer close(stopChan)
	errChan := make(chan error)
	go func() {
		var err error
		select {
		case <-conn.CloseNotify():
		case <-time.After(5 * time.Second):
			err = fmt.Errorf("Initial connection for %q not closed", deviceId)
		}
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}()
	go func(dupeId string) {
		dupeConn, err := client.DialId(origin, &dupeId, channelId)
		if err != nil {
			err = fmt.Errorf("Error reconnecting to origin: %s", err)
		}
		defer dupeConn.Close()
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}(deviceId)
	for i := 0; i < 2; i++ {
		if err := <-errChan; err != nil {
			t.Fatal(err)
		}
	}
}

func TestHandshakeWithId(t *testing.T) {
	origin, err := testServer.Origin()
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
	origin, err := testServer.Origin()
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
	origin, err := testServer.Origin()
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
	origin, err := testServer.Origin()
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
	origin, err := testServer.Origin()
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
	origin, err := testServer.Origin()
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
