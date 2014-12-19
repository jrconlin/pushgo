/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mozilla-services/pushgo/client"
)

func newTestHandler(t *testing.T) *Application {

	tlogger, _ := NewLogger(&TestLogger{DEBUG, t})

	mx := &TestMetrics{}
	mx.Init(nil, nil)
	store := &NoStore{logger: tlogger, maxChannels: 10}
	pping := &NoopPing{}
	app := NewApplication()
	app.hostname = "test"
	app.host = "test"
	app.clientMinPing = 10 * time.Second
	app.clientHelloTimeout = 10 * time.Second
	app.pushLongPongs = true
	app.metrics = mx
	app.store = store
	app.propping = pping
	app.SetLogger(tlogger)
	server := &Serv{}
	server.Init(app, server.ConfigStruct())
	app.SetServer(server)
	locator := &NoLocator{logger: tlogger}
	router := NewBroadcastRouter()
	router.Init(app, router.ConfigStruct())
	router.SetLocator(locator)
	app.SetRouter(router)

	eh := NewEndpointHandlers()
	ehConfig := eh.ConfigStruct()
	ehConfig.(*EndpointHandlersConfig).MaxDataLen = 140
	eh.Init(app, ehConfig)
	app.SetEndpointHandlers(eh)

	return app
}

func Test_UpdateHandler(t *testing.T) {
	var err error
	uaid := "deadbeef000000000000000000000000"
	chid := "decafbad000000000000000000000000"
	data := "This is a test of the emergency broadcasting system."

	app := newTestHandler(t)
	noPush := &PushWS{
		Socket: nil,
		Born:   time.Now(),
	}
	noPush.SetUAID(uaid)

	worker := &NoWorker{Socket: noPush,
		Logger: app.Logger(),
	}

	app.AddClient(uaid, &Client{
		Worker(worker),
		noPush,
		uaid})
	resp := httptest.NewRecorder()
	// don't bother with encryption right now.
	key, _ := app.Store().IDsToKey(uaid, chid)
	req, err := http.NewRequest("PUT",
		fmt.Sprintf("http://test/update/%s", key),
		nil)
	if req == nil {
		t.Fatal("Update put returned nil")
	}
	if err != nil {
		t.Fatal(err)
	}
	req.Form = make(url.Values)
	req.Form.Add("version", "1")
	req.Form.Add("data", data)

	// Yay! Actually try the test!
	tmux := app.EndpointHandlers().ServeMux()
	tmux.ServeHTTP(resp, req)
	if resp.Body.String() != "{}" {
		t.Error("Unexpected response from server")
	}
	rep := FlushData{}
	if err = json.Unmarshal(worker.Outbuffer, &rep); err != nil {
		t.Errorf("Could not read output buffer %s", err.Error())
	}
	if rep.Data != data {
		t.Error("Returned data does not match expected value")
	}
	if rep.Version != 1 {
		t.Error("Returned version does not match expected value")
	}

	//retry without a Content-Type header
	resp = httptest.NewRecorder()
	req.Header.Set("Content-Type", "")
	tmux.ServeHTTP(resp, req)
	if resp.Body.String() != "{}" {
		t.Error("Unexpected response from server")
	}
	rep = FlushData{}
	if err = json.Unmarshal(worker.Outbuffer, &rep); err != nil {
		t.Errorf("Could not read output buffer %s", err.Error())
	}
	if rep.Data != data {
		t.Error("Returned data does not match expected value")
	}
	if rep.Version != 1 {
		t.Error("Returned version does not match expected value")
	}

}

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
