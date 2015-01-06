/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/rafrombrc/gomock/gomock"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/mozilla-services/pushgo/id"
)

func enableLongPongs(app *Application, enabled bool) func() {
	prev := app.pushLongPongs
	app.pushLongPongs = enabled
	return func() { app.pushLongPongs = prev }
}

func newTestApp(tb TBLoggingInterface) (app *Application) {
	app = NewApplication()
	app.hostname = "test"
	app.clientMinPing = 10 * time.Second
	app.clientPongInterval = 10 * time.Second
	app.clientHelloTimeout = 10 * time.Second
	app.pushLongPongs = true
	log, _ := NewLogger(&TestLogger{DEBUG, tb})
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
	if err := eh.Init(app, ehConf); err != nil {
		tb.Logf("Could not init Endpoint: %s", err)
		return nil
	}

	app.SetEndpointHandler(eh)

	hh := NewHealthHandlers()
	hh.Init(app, nil)

	return app
}

func TestWorkerRegister(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := newTestApp(t)

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	Convey("Should reject unidentified clients", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")
		wws := NewWorker(app, "test")
		err := wws.Register(pws, nil,
			[]byte(`{"channelID": "3fc2d1a2950411e49b203c15c2c622fe"}`))
		So(err, ShouldEqual, ErrInvalidCommand)
	})

	Convey("Should reject invalid channel IDs", t, func() {
		var err error
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("923e7a42951511e490403c15c2c622fe")
		wws := NewWorker(app, "test")

		// Invalid JSON.
		err = wws.Register(pws, nil, []byte(`{"channelID": "123",}`))
		So(err, ShouldEqual, ErrInvalidParams)

		// Invalid channel ID.
		err = wws.Register(pws, nil, []byte(`{"channelID": "123"}`))
		So(err, ShouldEqual, ErrInvalidParams)
	})

	Convey("Should fail if storage is unavailable", t, func() {
		rs := NewRecorderSocket()

		deviceID := "d0afa324950511e48aed3c15c2c622fe"
		channelID := "f2265458950511e49cae3c15c2c622fe"

		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(deviceID)
		wws := NewWorker(app, "test")
		storeErr := errors.New("oops")

		regBytes, _ := json.Marshal(struct {
			ChannelID string `json:"channelID"`
		}{channelID})

		mckStore.EXPECT().Register(deviceID, channelID,
			int64(0)).Return(storeErr)
		err := wws.Register(pws, &RequestHeader{Type: "register"}, regBytes)
		So(err, ShouldEqual, storeErr)
	})

	Convey("Should generate endpoints for registered channels", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		deviceID := "8fe81c44950611e4aafe3c15c2c622fe"
		channelID := "930c80b8950611e4be663c15c2c622fe"
		pws.SetUAID(deviceID)
		wws := NewWorker(app, "test")

		regBytes, _ := json.Marshal(struct {
			ChannelID string `json:"channelID"`
		}{channelID})

		mckStore.EXPECT().Register(deviceID, channelID,
			int64(0)).Return(nil)
		mckServ.EXPECT().HandleCommand(gomock.Eq(PushCommand{
			Command:   REGIS,
			Arguments: JsMap{"channelID": channelID},
		}), pws).Return(200, JsMap{
			"status":        200,
			"channelID":     channelID,
			"uaid":          deviceID,
			"push.endpoint": "https://example.org",
		})
		mckStat.EXPECT().Increment("updates.client.register")

		err := wws.Register(pws, &RequestHeader{Type: "register"}, regBytes)
		So(err, ShouldBeNil)

		dec := json.NewDecoder(rs.Outgoing)
		regReply := new(RegisterReply)
		So(dec.Decode(regReply), ShouldEqual, nil)
		So(regReply.Type, ShouldEqual, "register")
		So(regReply.DeviceID, ShouldEqual, deviceID)
		So(regReply.Status, ShouldEqual, 200)
		So(regReply.ChannelID, ShouldEqual, channelID)
		So(regReply.Endpoint, ShouldEqual, "https://example.org")
	})
}

func TestWorkerACK(t *testing.T) {
	// ...
}

func TestWorkerHandshakeRedirect(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := newTestApp(t)

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	mckStat.EXPECT().Increment(gomock.Not("updates.client.hello")).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	Convey("Should return a 307 if the current node is full", t, func() {
		wws := NewWorker(app, "test")
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		mckBalancer.EXPECT().RedirectURL().Return("https://example.org/2", true, nil)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"},
			[]byte(`{"uaid":"","channelIDs":[]}`))
		So(err, ShouldBeNil)
		So(wws.stopped, ShouldBeTrue)

		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldBeNil)
		So(helloReply.Type, ShouldEqual, "hello")
		So(helloReply.Status, ShouldEqual, 307)
		So(helloReply.DeviceID, ShouldEqual, testID)
		So(helloReply.RedirectURL, ShouldNotBeNil)
		So(*helloReply.RedirectURL, ShouldEqual, "https://example.org/2")
	})

	Convey("Should return a 429 if the cluster is full", t, func() {
		wws := NewWorker(app, "test")
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		mckBalancer.EXPECT().RedirectURL().Return("", false, ErrNoPeers)

		err := wws.Hello(pws, &RequestHeader{Type: "HELLO"},
			[]byte(`{"uaid":"","channelIDs":[]}`))
		So(err, ShouldBeNil)
		So(wws.stopped, ShouldBeTrue)

		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldBeNil)
		So(helloReply.Type, ShouldEqual, "HELLO")
		So(helloReply.Status, ShouldEqual, 429)
		So(helloReply.DeviceID, ShouldEqual, testID)
		So(helloReply.RedirectURL, ShouldBeNil)
	})
}

func TestWorkerHandshakeDupe(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := newTestApp(t)

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	mckStat.EXPECT().Increment(gomock.Not("updates.client.hello")).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should allow duplicate handshakes for empty IDs", t, func() {
		deviceID := "4738b3be952911e4a7dc3c15c2c622fe"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(deviceID)

		mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, nil)
		mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(deviceID, gomock.Any()).Return(nil, nil, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		err := wws.Hello(pws, &RequestHeader{Type: "hello"},
			[]byte(`{"uaid":"","channelIDs":[]}`))
		So(err, ShouldBeNil)
		So(wws.stopped, ShouldBeFalse)
	})

	Convey("Should allow duplicate handshakes for matching IDs", t, func() {
		deviceID := "bd12381c953811e490cb3c15c2c622fe"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(deviceID)

		mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, nil)
		mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(deviceID, gomock.Any()).Return(nil, nil, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		helloBytes, _ := json.Marshal(struct {
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{deviceID, []string{}})
		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, helloBytes)
		So(err, ShouldBeNil)
		So(wws.stopped, ShouldBeFalse)
	})

	Convey("Should reject duplicate handshakes for mismatched IDs", t, func() {
		deviceID := "479f5444953211e484b43c15c2c622fe"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(deviceID)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"720f6b9a953411e4aadf3c15c2c622fe","channelIDs":[]}`))
		So(err, ShouldEqual, ErrExistingID)
	})
}

func TestWorkerError(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := newTestApp(t)

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	Convey("Should include request fields in error responses", t, func() {
		deviceID := "ffb0232c953911e4b5133c15c2c622fe"
		wws := NewWorker(app, "test")
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(deviceID)

		newID := "720f6b9a953411e4aadf3c15c2c622fe"
		channelIDs := id.MustGenerate(1)
		rs.RecordJSON(struct {
			Type       string   `json:"messageType"`
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{"HELLO", newID, channelIDs})

		wws.Run(pws)

		dec := json.NewDecoder(rs.Outgoing)
		var errorReply struct {
			Status     int      `json:"status"`
			Error      *string  `json:"error"`
			Type       string   `json:"messageType"`
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}
		So(dec.Decode(&errorReply), ShouldBeNil)
		So(errorReply.Status, ShouldEqual, 503)
		So(errorReply.Error, ShouldNotBeNil)
		So(errorReply.Type, ShouldEqual, "HELLO")
		So(errorReply.DeviceID, ShouldEqual, newID)
		So(errorReply.ChannelIDs, ShouldResemble, channelIDs)
	})
}

func BenchmarkWorkerRun(b *testing.B) {
	// I don't believe that goconvey handles benchmark well, so sadly, can't
	// reuse the test code.
	installMocks()
	defer revertMocks()

	app := newTestApp(b)
	rs := NewRecorderSocket()

	// Prebuild the buffers so we're not timing JSON.
	buf := bytes.NewBuffer([]byte{})
	writeJSON(buf, []byte(`{
			"messageType": "hello",
			"uaid": "",
			"channelIDs": []
		}`))
	writeJSON(buf, []byte(`{
			"messageType": "register",
			"channelID": "89101cfa01dd4294a00e3a813cb3da97"
		}`))
	writeJSON(buf, []byte("{}"))
	writeJSON(buf, []byte(`{
			"messageType": "unregister",
			"channelID": "89101cfa01dd4294a00e3a813cb3da97"
		}`))

	// Reset so we only test the bits we care about.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		rs.Incoming.Write(buf.Bytes())
		wws := NewWorker(app, "test")
		wws.Run(pws)
		rs.Outgoing.Truncate(0)
	}
}

type netErr struct {
	timeout   bool
	temporary bool
}

func (err *netErr) Error() string {
	return fmt.Sprintf("netErr: timeout: %v; temporary: %v",
		err.timeout, err.temporary)
}

func (err *netErr) Timeout() bool   { return err.timeout }
func (err *netErr) Temporary() bool { return err.temporary }

func TestWorkerRun(t *testing.T) {
	installMocks()
	defer revertMocks()

	app := newTestApp(t)

	Convey("Should respond to client commands", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		rs.Record([]byte(`{
			"messageType": "hello",
			"uaid": "",
			"channelIDs": []
		}`))
		rs.Record([]byte(`{
			"messageType": "register",
			"channelID": "89101cfa01dd4294a00e3a813cb3da97"
		}`))
		rs.Record([]byte("{}"))
		rs.Record([]byte(`{
			"messageType": "unregister",
			"channelID": "89101cfa01dd4294a00e3a813cb3da97"
		}`))
		wws := NewWorker(app, "test")
		wws.Run(pws)
		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldBeNil)
		So(helloReply.Type, ShouldEqual, "hello")
		So(helloReply.Status, ShouldEqual, 200)
		So(helloReply.DeviceID, ShouldEqual, testID)
		regReply := new(RegisterReply)
		So(dec.Decode(regReply), ShouldBeNil)
		So(regReply.Type, ShouldEqual, "register")
		So(regReply.DeviceID, ShouldEqual, testID)
		So(regReply.Status, ShouldEqual, 200)
		So(regReply.ChannelID, ShouldEqual, "89101cfa01dd4294a00e3a813cb3da97")
		pingReply := new(PingReply)
		So(dec.Decode(pingReply), ShouldBeNil)
		So(pingReply.Type, ShouldEqual, "ping")
		So(pingReply.Status, ShouldEqual, 200)
		unregReply := new(UnregisterReply)
		So(dec.Decode(unregReply), ShouldBeNil)
		So(unregReply.Type, ShouldEqual, "unregister")
		So(unregReply.Status, ShouldEqual, 200)
		So(unregReply.ChannelID, ShouldEqual, "89101cfa01dd4294a00e3a813cb3da97")
	})
	Convey("Should preserve command type case", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		rs.Record([]byte(`{
			"messageType": "HELLO",
			"uaid": "",
			"channelIDs": []
		}`))
		rs.Record([]byte(`{
			"messageType": "RegisteR",
			"channelID": "929c148c588746b29f4ea3dee52fdbd0"
		}`))
		wws := NewWorker(app, "test")
		wws.Run(pws)
		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldBeNil)
		So(helloReply.Type, ShouldEqual, "HELLO")
		So(helloReply.Status, ShouldEqual, 200)
		So(helloReply.DeviceID, ShouldEqual, testID)
		regReply := new(RegisterReply)
		So(dec.Decode(regReply), ShouldBeNil)
		So(regReply.Type, ShouldEqual, "RegisteR")
		So(regReply.Status, ShouldEqual, 200)
		So(regReply.ChannelID, ShouldEqual, "929c148c588746b29f4ea3dee52fdbd0")
	})
}

func TestWorkerHello(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := newTestApp(t)

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	mckStat.EXPECT().Increment(gomock.Not("updates.client.hello")).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	Convey("Should issue a new device ID if the client omits one", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")

		mckBalancer.EXPECT().RedirectURL().Return("", false, nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"","channelIDs": []}`))
		So(err, ShouldBeNil)

		So(pws.UAID(), ShouldEqual, testID)
		So(app.ClientExists(testID), ShouldBeTrue)
		app.Server().HandleCommand(PushCommand{Command: DIE}, pws)
	})

	Convey("Should reject invalid UUIDs", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")
		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"!@#$","channelIDs":[]}`))
		So(err, ShouldEqual, ErrInvalidID)
		So(app.ClientExists("!@#$"), ShouldBeFalse)
	})

	Convey("Should issue a new device ID for excessive channels", t, func() {
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")

		prevID := "ba14b1f1-90d0-4e72-8acf-e6ab71362e91"
		channelIDs := id.MustGenerate(5)
		helloBytes, _ := json.Marshal(struct {
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{prevID, channelIDs})

		mckStore.EXPECT().CanStore(len(channelIDs)).Return(false)
		mckStore.EXPECT().DropAll(prevID)
		mckBalancer.EXPECT().RedirectURL().Return("", false, nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, helloBytes)
		So(err, ShouldBeNil)

		So(app.ClientExists(prevID), ShouldBeFalse)
		So(app.ClientExists(testID), ShouldBeTrue)
	})

	Convey("Should require the `channelIDs` field", t, func() {
		// ...
	})

	Convey("Should issue a new device ID for nonexistent channels", t, func() {
		// ...
	})
}

func TestWorkerPing(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := newTestApp(t)

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	app.SetMetrics(mckStat)

	mckSocket := NewMockSocket(mockCtrl)
	mckSocket.EXPECT().SetReadDeadline(gomock.Any()).AnyTimes()

	Convey("Should allow pings from unidentified connections", t, func() {
		revertPongs := enableLongPongs(app, false)
		defer revertPongs()

		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		gomock.InOrder(
			mckSocket.EXPECT().WriteText("{}"),
			mckStat.EXPECT().Increment("updates.client.ping"),
		)

		err := wws.Ping(pws, &RequestHeader{Type: "ping"}, nil)
		So(err, ShouldBeNil)
	})

	Convey("Can respond with short pongs", t, func() {
		revertPongs := enableLongPongs(app, false)
		defer revertPongs()

		wws := NewWorker(app, "test")
		wws.pingInt = 0 // Disable minimum ping interval check.
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		mckStat.EXPECT().Increment("updates.client.ping").Times(4)

		gomock.InOrder(
			mckSocket.EXPECT().ReadBinary().Return([]byte("{}"), nil),
			mckSocket.EXPECT().WriteText("{}"),

			mckSocket.EXPECT().ReadBinary().Return([]byte("\t{\r\n} "), nil),
			mckSocket.EXPECT().WriteText("{}"),

			mckSocket.EXPECT().ReadBinary().Return([]byte(
				`{"messageType":"ping"}`), nil),
			mckSocket.EXPECT().WriteText("{}"),

			mckSocket.EXPECT().ReadBinary().Return([]byte(
				`{"messageType":"PING"}`), nil),
			mckSocket.EXPECT().WriteText("{}"),

			mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			mckSocket.EXPECT().Close(),
		)

		wws.Run(pws)
	})

	Convey("Can respond with long pongs", t, func() {
		revertPongs := enableLongPongs(app, true)
		defer revertPongs()

		wws := NewWorker(app, "test")
		wws.pingInt = 0
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		mckStat.EXPECT().Increment("updates.client.ping").Times(4)

		gomock.InOrder(
			mckSocket.EXPECT().ReadBinary().Return([]byte("{}"), nil),
			mckSocket.EXPECT().WriteJSON(PingReply{Type: "ping", Status: 200}),

			mckSocket.EXPECT().ReadBinary().Return([]byte("\t{\r\n} "), nil),
			mckSocket.EXPECT().WriteJSON(PingReply{Type: "ping", Status: 200}),

			mckSocket.EXPECT().ReadBinary().Return([]byte(
				`{"messageType":"ping"}`), nil),
			mckSocket.EXPECT().WriteJSON(PingReply{Type: "ping", Status: 200}),

			mckSocket.EXPECT().ReadBinary().Return([]byte(
				`{"messageType":"PING"}`), nil),
			mckSocket.EXPECT().WriteJSON(PingReply{Type: "PING", Status: 200}),

			mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			mckSocket.EXPECT().Close(),
		)

		wws.Run(pws)
	})

	Convey("Should return an error for excessive pings", t, func() {
		var err error
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("04b1c85c95e011e49b103c15c2c622fe")

		mckSocket.EXPECT().WriteJSON(PingReply{Type: "ping", Status: 200}).Times(2)
		mckStat.EXPECT().Increment("updates.client.ping").Times(2)

		err = wws.Ping(pws, &RequestHeader{Type: "ping"}, nil)
		So(err, ShouldBeNil)

		wws.lastPing = wws.lastPing.Add(-wws.pingInt)
		err = wws.Ping(pws, &RequestHeader{Type: "ping"}, nil)
		So(err, ShouldBeNil)

		gomock.InOrder(
			mckSocket.EXPECT().Origin().Return("https://example.com", true),
			mckStat.EXPECT().Increment("updates.client.too_many_pings"),
		)

		wws.lastPing = wws.lastPing.Add(-wws.pingInt / 2)
		err = wws.Ping(pws, &RequestHeader{Type: "ping"}, nil)
		So(err, ShouldEqual, ErrTooManyPings)

		So(wws.stopped, ShouldBeTrue)
	})
}

func TestMockHello(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := newTestApp(t)

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	mckStat.EXPECT().Increment(gomock.Not("updates.client.hello")).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	Convey("Should flush messages after handshake", t, func() {
		deviceID := "b0b8afe6950c11e49aa73c15c2c622fe"
		channelIDs := id.MustGenerate(3)

		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")

		mckStore.EXPECT().CanStore(len(channelIDs)).Return(true)
		mckStore.EXPECT().Exists(deviceID).Return(true)
		mckBalancer.EXPECT().RedirectURL().Return("", false, nil)
		mckServ.EXPECT().HandleCommand(gomock.Eq(PushCommand{
			Command: HELLO,
			Arguments: JsMap{
				"worker":  wws,
				"uaid":    deviceID,
				"chids":   channelIDs,
				"connect": []byte(nil),
			},
		}), pws).Return(200, nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(deviceID, gomock.Any()).Return([]Update{
			{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
			{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"},
		}, []string{"c778e94a950b11e4ba7f3c15c2c622fe"}, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		helloBytes, _ := json.Marshal(struct {
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{deviceID, channelIDs})
		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, helloBytes)
		So(err, ShouldBeNil)

		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldBeNil)
		So(helloReply.Type, ShouldEqual, "hello")
		So(helloReply.Status, ShouldEqual, 200)
		So(helloReply.DeviceID, ShouldEqual, deviceID)
		flushReply := new(FlushReply)
		So(dec.Decode(flushReply), ShouldBeNil)
		So(flushReply.Type, ShouldEqual, "notification")
		So(flushReply.Updates, ShouldResemble, []Update{
			{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
			{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"}})
		So(flushReply.Expired, ShouldResemble, []string{
			"c778e94a950b11e4ba7f3c15c2c622fe"})
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
