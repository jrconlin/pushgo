/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/mozilla-services/pushgo/id"
)

var testID = "d1c7c768-b1be-4c70-93a6-9b52910d4baa"

func installWorkerMocks() {
	idGenerate = func() (string, error) {
		return testID, nil
	}
}

func revertWorkerMocks() {
	idGenerate = id.Generate
}

func newTestApp(t *testing.T) (app *Application) {
	app = NewApplication()
	app.hostname = "test"
	app.clientMinPing = 10 * time.Second
	app.clientHelloTimeout = 10 * time.Second
	app.pushLongPongs = true
	log, _ := NewLogger(&TestLogger{DEBUG, t})
	app.SetLogger(log)

	tm := &TestMetrics{
		Counters: make(map[string]int64),
		Gauges:   make(map[string]int64),
	}
	app.SetMetrics(tm)

	ns := &NoStore{logger: log, maxChannels: 10}
	app.SetStore(ns)

	np := new(NoopPing)
	app.SetPropPinger(np)

	rt := NewBroadcastRouter()
	rtConf := rt.ConfigStruct().(*BroadcastRouterConfig)
	rtConf.Listener.Addr = "" // Bind to an ephemeral port.
	rt.Init(app, rtConf)
	app.SetRouter(rt)

	nl := &NoLocator{logger: log}
	app.SetLocator(nl)

	srv := new(Serv)
	srv.Init(app, srv.ConfigStruct())
	app.SetServer(srv)

	sh := NewSocketHandler()
	shConf := sh.ConfigStruct().(*SocketHandlerConfig)
	shConf.Listener.Addr = ""
	sh.Init(app, shConf)
	app.SetSocketHandler(sh)

	nb := new(NoBalancer)
	app.SetBalancer(nb)

	eh := NewEndpointHandler()
	ehConf := eh.ConfigStruct().(*EndpointHandlerConfig)
	ehConf.Listener.Addr = ""
	eh.Init(app, ehConf)
	app.SetEndpointHandler(eh)

	hh := NewHealthHandlers()
	hh.Init(app, nil)

	return app
}

func TestWorkerACK(t *testing.T) {
	// ...
}

func TestWorkerHandshakeRedirect(t *testing.T) {
	installWorkerMocks()
	defer revertWorkerMocks()

	Convey("Should return a 307 if the current node is full", t, func() {
		// ...
	})
	Convey("Should return a 429 if the cluster is full", t, func() {
		// ...
	})
	Convey("Should not query the balancer for duplicate handshakes", t, func() {
		// ...
	})
}

func TestWorkerHandshakeDupe(t *testing.T) {
	installWorkerMocks()
	defer revertWorkerMocks()

	Convey("Should allow duplicate handshakes for matching IDs", t, func() {
		// ...
	})
	Convey("Should allow duplicate handshakes for omitted IDs", t, func() {
		// ...
	})
	Convey("Should reject duplicate handshakes for mismatched IDs", t, func() {
		// ...
	})
}

func TestWorkerRun(t *testing.T) {
	installWorkerMocks()
	defer revertWorkerMocks()

	app := newTestApp(t)

	Convey("Should respond to client commands", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		json.Compact(rs.Incoming, []byte(`{
			"messageType": "hello",
			"uaid": "",
			"channelIDs": []
		}`))
		rs.Incoming.WriteByte('\n')
		json.Compact(rs.Incoming, []byte(`{
			"messageType": "register",
			"channelID": "89101cfa01dd4294a00e3a813cb3da97"
		}`))
		rs.Incoming.WriteByte('\n')
		rs.Incoming.WriteString("{}")
		rs.Incoming.WriteByte('\n')
		json.Compact(rs.Incoming, []byte(`{
			"messageType": "unregister",
			"channelID": "89101cfa01dd4294a00e3a813cb3da97"
		}`))
		rs.Incoming.WriteByte('\n')
		wws := NewWorker(app, "test")
		wws.Run(pws)
		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldEqual, nil)
		So(helloReply.Type, ShouldEqual, "hello")
		So(helloReply.Status, ShouldEqual, 200)
		So(helloReply.DeviceID, ShouldEqual, testID)
		regReply := new(RegisterReply)
		So(dec.Decode(regReply), ShouldEqual, nil)
		So(regReply.Type, ShouldEqual, "register")
		So(regReply.DeviceID, ShouldEqual, testID)
		So(regReply.Status, ShouldEqual, 200)
		So(regReply.ChannelID, ShouldEqual, "89101cfa01dd4294a00e3a813cb3da97")
		pingReply := new(PingReply)
		So(dec.Decode(pingReply), ShouldEqual, nil)
		So(pingReply.Type, ShouldEqual, "ping")
		So(pingReply.Status, ShouldEqual, 200)
		unregReply := new(UnregisterReply)
		So(dec.Decode(unregReply), ShouldEqual, nil)
		So(unregReply.Type, ShouldEqual, "unregister")
		So(unregReply.Status, ShouldEqual, 200)
		So(unregReply.ChannelID, ShouldEqual, "89101cfa01dd4294a00e3a813cb3da97")
	})
	Convey("Should preserve command type case", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		json.Compact(rs.Incoming, []byte(`{
			"messageType": "HELLO",
			"uaid": "",
			"channelIDs": []
		}`))
		rs.Incoming.WriteByte('\n')
		json.Compact(rs.Incoming, []byte(`{
			"messageType": "RegisteR",
			"channelID": "929c148c588746b29f4ea3dee52fdbd0"
		}`))
		rs.Incoming.WriteByte('\n')
		wws := NewWorker(app, "test")
		wws.Run(pws)
		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldEqual, nil)
		So(helloReply.Type, ShouldEqual, "HELLO")
		So(helloReply.Status, ShouldEqual, 200)
		So(helloReply.DeviceID, ShouldEqual, testID)
		regReply := new(RegisterReply)
		So(dec.Decode(regReply), ShouldEqual, nil)
		So(regReply.Type, ShouldEqual, "RegisteR")
		So(regReply.Status, ShouldEqual, 200)
		So(regReply.ChannelID, ShouldEqual, "929c148c588746b29f4ea3dee52fdbd0")
	})
}

func TestWorkerHello(t *testing.T) {
	installWorkerMocks()
	defer revertWorkerMocks()

	app := newTestApp(t)

	Convey("Should issue a new device ID if the client omits one", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")
		header := &RequestHeader{Type: "hello"}
		err := wws.Hello(pws, header, []byte(
			`{"messageType":"hello","uaid":"","channelIDs": []}`))
		So(err, ShouldEqual, nil)
		So(pws.UAID(), ShouldEqual, testID)
		So(app.ClientExists(testID), ShouldEqual, true)
		app.Server().Bye(pws)
	})

	Convey("Should reject invalid UUIDs", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")
		header := &RequestHeader{Type: "hello"}
		err := wws.Hello(pws, header, []byte(
			`{"messageType":"hello","uaid":"!@#$","channelIDs":[]}`))
		So(err, ShouldEqual, ErrInvalidID)
		So(app.ClientExists("!@#$"), ShouldEqual, false)
	})

	Convey("Should issue a new device ID for excessive channels", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")
		header := &RequestHeader{Type: "hello"}
		helloBytes, _ := json.Marshal(struct {
			Type       string   `json:"messageType"`
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{"hello", "ba14b1f1-90d0-4e72-8acf-e6ab71362e91", id.MustGenerate(11)})
		err := wws.Hello(pws, header, helloBytes)
		So(err, ShouldEqual, nil)
		So(app.ClientExists("ba14b1f1-90d0-4e72-8acf-e6ab71362e91"),
			ShouldEqual, false)
		So(app.ClientExists(testID), ShouldEqual, true)
	})

	Convey("Should issue a new device ID for nonexistent channels", t, func() {
		// ...
	})
}

// Test that harmless errors are harmless
func TestHarmlessConnectionError(t *testing.T) {
	Convey("Harmless errors are harmless", t, func() {
		errs := []error{
			errors.New("http: TLS handshake error from XXXXXX: read tcp XXXXXXX:XXX: connection reset by peer"),
			errors.New("read tcp YYYYYYYYYYYY:YYYYY: connection timed out"),
		}
		for _, err := range errs {
			So(harmlessConnectionError(err), ShouldEqual, true)
		}
	})
	Convey("Unknown errors are harmful", t, func() {
		errs := []error{
			errors.New("omg, everything is exploding"),
			errors.New("universe has imploded"),
		}
		for _, err := range errs {
			So(harmlessConnectionError(err), ShouldEqual, false)
		}
	})
}
