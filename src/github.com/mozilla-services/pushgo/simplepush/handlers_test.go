/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/mozilla-services/pushgo/client"
)

func endpointIds(uri *url.URL) (deviceId, channelId string, ok bool) {
	if !uri.IsAbs() {
		ok = false
		return
	}
	pathPrefix := "/update/"
	i := strings.Index(uri.Path, pathPrefix)
	if i < 0 {
		ok = false
		return
	}
	key := strings.SplitN(uri.Path[i+len(pathPrefix):], ".", 2)
	if len(key) < 2 {
		ok = false
		return
	}
	return key[0], key[1], true
}

func TestBadKey(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, deviceId, err := client.Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	channelId, endpoint, err := conn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %#v", err)
	}
	uri, err := url.ParseRequestURI(endpoint)
	if err != nil {
		t.Fatalf("Error parsing push endpoint %#v: %#v", endpoint, err)
	}
	keyDevice, keyChannel, ok := endpointIds(uri)
	if !ok {
		t.Errorf("Incomplete push endpoint: %#v", endpoint)
	}
	if keyDevice != deviceId {
		t.Errorf("Mismatched device IDs: got %#v; want %#v", keyDevice, deviceId)
	}
	if keyChannel != channelId {
		t.Errorf("Mismatched channel IDs: got %#v; want %#v", keyChannel, channelId)
	}
	newURI, _ := uri.Parse(fmt.Sprintf("/update/%s", keyDevice))
	err = client.Notify(newURI.String(), 1)
	clientErr, ok := err.(client.Error)
	if !ok {
		t.Errorf("Type assertion failed for endpoint error: %#v", err)
	} else if clientErr.Status() != 404 {
		t.Errorf("Unexpected endpoint status: got %#v; want 404", clientErr.Status())
	}
}

func TestMissingKey(t *testing.T) {
	origin, err := Server.Origin()
	if err != nil {
		t.Fatalf("Error initializing test server: %#v", err)
	}
	conn, deviceId, err := client.Dial(origin)
	if err != nil {
		t.Fatalf("Error dialing origin: %#v", err)
	}
	defer conn.Close()
	channelId, endpoint, err := conn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %#v", err)
	}
	uri, err := url.ParseRequestURI(endpoint)
	if err != nil {
		t.Fatalf("Error parsing push endpoint %#v: %#v", endpoint, err)
	}
	keyDevice, keyChannel, ok := endpointIds(uri)
	if !ok {
		t.Errorf("Incomplete push endpoint: %#v", endpoint)
	}
	if keyDevice != deviceId {
		t.Errorf("Mismatched device IDs: got %#v; want %#v", keyDevice, deviceId)
	}
	if keyChannel != channelId {
		t.Errorf("Mismatched channel IDs: got %#v; want %#v", keyChannel, channelId)
	}
	newURI, _ := uri.Parse(fmt.Sprintf("/update/%s.", keyDevice))
	err = client.Notify(newURI.String(), 1)
	clientErr, ok := err.(client.Error)
	if !ok {
		t.Errorf("Type assertion failed for endpoint error: %#v", err)
	} else if clientErr.Status() != 404 {
		t.Errorf("Unexpected endpoint status: got %#v; want 404", clientErr.Status())
	}
}
