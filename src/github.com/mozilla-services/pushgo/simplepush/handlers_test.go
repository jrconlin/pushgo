/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func newTestHandler(tb TBLoggingInterface) *Application {

	tlogger, _ := NewLogger(&TestLogger{DEBUG, tb})

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
	locator := &NoLocator{logger: tlogger}
	router := NewBroadcastRouter()
	routerConf := router.ConfigStruct().(*BroadcastRouterConfig)
	routerConf.Listener.Addr = ""
	router.Init(app, routerConf)
	app.SetRouter(router)
	app.SetLocator(locator)

	sh := NewSocketHandler()
	shConfig := sh.ConfigStruct().(*SocketHandlerConfig)
	shConfig.Listener.Addr = ""
	if err := sh.Init(app, shConfig); err != nil {
		tb.Logf("Failed to create WebSocket handler: %s", err)
		return nil
	}
	app.SetSocketHandler(sh)

	eh := NewEndpointHandler()
	ehConfig := eh.ConfigStruct().(*EndpointHandlerConfig)
	ehConfig.Listener.Addr = ""
	ehConfig.MaxDataLen = 140
	if err := eh.Init(app, ehConfig); err != nil {
		tb.Logf("Failed to create update handler: %s", err)
		return nil
	}
	app.SetEndpointHandler(eh)

	return app
}

func randomText(size int) string {
	rbuf := make([]byte, size)
	rand.Read(rbuf)
	for i := 0; i < len(rbuf); i++ {
		rbuf[i] = uint8(rbuf[i])%94 + 32
	}
	return string(rbuf)
}

func Benchmark_UpdateHandler(b *testing.B) {
	uaid := "deadbeef000000000000000000000000"
	chid := "decafbad000000000000000000000000"
	app := newTestHandler(b)
	defer app.Close()
	if app == nil {
		b.Fatal()
	}
	worker := &NoWorker{Logger: app.Logger()}
	worker.SetUAID(uaid)
	app.AddWorker(uaid, worker)
	resp := httptest.NewRecorder()
	key, _ := app.Store().IDsToKey(uaid, chid)
	updateUrl := fmt.Sprintf("http://test/update/%s", key)
	tmux := app.EndpointHandler().ServeMux()
	vals := make(url.Values)
	data := randomText(64)
	// Begin benchmark:
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequest("PUT", updateUrl, nil)
		if req == nil || err != nil {
			b.Errorf("PUT failed: %s", err)
			b.Fail()
		}
		req.Form = vals
		req.Form.Set("version", strconv.FormatInt(int64(i), 10))
		req.Form.Set("data", data)
		tmux.ServeHTTP(resp, req)
		if resp.Body.String() != "{}" {
			b.Errorf(`Unexpected response from server: "%s"`, resp.Body.String())
			b.Fail()
		}
		resp.Body.Reset()
	}
}

func Test_UpdateHandler(t *testing.T) {
	var err error
	uaid := "deadbeef000000000000000000000000"
	chid := "decafbad000000000000000000000000"
	data := "This is a test of the emergency broadcasting system."

	app := newTestHandler(t)
	defer app.Close()
	worker := &NoWorker{Logger: app.Logger()}
	worker.SetUAID(uaid)
	app.AddWorker(uaid, worker)
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
	tmux := app.EndpointHandler().ServeMux()
	tmux.ServeHTTP(resp, req)
	if resp.Body.String() != "{}" {
		t.Error("Unexpected response from server")
	}
	rep := SendData{}
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
	rep = SendData{}
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
