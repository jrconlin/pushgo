/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"testing"
	"time"

	"github.com/rafrombrc/gomock/gomock"
	. "github.com/smartystreets/goconvey/convey"
)

func TestServerHello(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := NewApplication()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckPinger := NewMockPropPinger(mockCtrl)
	app.SetPropPinger(mckPinger)

	mckRouter := NewMockRouter(mockCtrl)
	app.SetRouter(mckRouter)

	srv := NewServer()
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		t.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)

	mckSocket := NewMockSocket(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	pingData := []byte(`{"regid":123}`)

	Convey("Should register with the proprietary pinger", t, func() {
		uaid := "c4fe17154cd74500ad1d51f2955fd79c"
		pws := &PushWS{Socket: mckSocket, Logger: app.Logger()}
		pws.SetUAID(uaid)

		gomock.InOrder(
			mckPinger.EXPECT().Register(uaid, pingData),
			mckRouter.EXPECT().Register(uaid),
		)
		inArgs := JsMap{
			"worker":     mckWorker,
			"uaid":       uaid,
			"channelIDs": []string{"1", "2"},
			"connect":    pingData,
		}
		result, outArgs := srv.HandleCommand(PushCommand{
			Command: HELLO, Arguments: inArgs}, pws)

		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)

		client, clientConnected := app.GetClient(uaid)
		So(clientConnected, ShouldBeTrue)
		So(client, ShouldResemble, &Client{
			Worker: mckWorker,
			PushWS: pws,
			UAID:   uaid,
		})
	})

	Convey("Should not fail if pinger registration fails", t, func() {
		uaid := "3529f588b03e411295b8df6d38e63ce7"
		pws := &PushWS{Socket: mckSocket, Logger: app.Logger()}
		pws.SetUAID(uaid)

		gomock.InOrder(
			mckPinger.EXPECT().Register(uaid, pingData).Return(errors.New(
				"external system on fire")),
			mckRouter.EXPECT().Register(uaid),
		)
		result, _ := srv.Hello(mckWorker, PushCommand{
			Command: HELLO,
			Arguments: JsMap{
				"worker":     mckWorker,
				"uaid":       uaid,
				"channelIDs": []string{"3", "4"},
				"connect":    pingData,
			},
		}, pws)
		So(result, ShouldEqual, 200)
		So(app.ClientExists(uaid), ShouldBeTrue)
	})
}

func TestServerBye(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	uaid := "0cd9b0990bb749eb808206924e40a323"
	app := NewApplication()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckPinger := NewMockPropPinger(mockCtrl)
	app.SetPropPinger(mckPinger)

	mckRouter := NewMockRouter(mockCtrl)
	app.SetRouter(mckRouter)

	srv := NewServer()
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		t.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)

	mckWorker := NewMockWorker(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should remove the client from the map", t, func() {
		prevPushSock := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		prevPushSock.SetUAID(uaid)
		prevClient := &Client{mckWorker, prevPushSock, uaid}

		app.AddClient(uaid, prevClient)
		So(app.ClientExists(uaid), ShouldBeTrue)

		gomock.InOrder(
			mckRouter.EXPECT().Unregister(uaid),
			mckSocket.EXPECT().Close(),
		)
		cmd := PushCommand{Command: DIE}
		status, args := srv.HandleCommand(cmd, prevPushSock)
		So(status, ShouldEqual, 0)
		So(args, ShouldEqual, nil)
		So(app.ClientExists(uaid), ShouldBeFalse)

		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)
		app.AddClient(uaid, &Client{mckWorker, pws, uaid})

		srv.HandleCommand(cmd, prevPushSock)
		So(app.ClientExists(uaid), ShouldBeTrue)
	})
}

func TestServerRegister(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()

	mckStore := NewMockStore(mockCtrl)
	mckPinger := NewMockPropPinger(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckEndHandler := NewMockHandler(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	uaid := "480ce74851d04104bfe11204c020ee81"
	chid := "5b0ae7e9de7f42529a361e3bfe318142"

	Convey("Should reject invalid IDs", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetStore(mckStore)
		app.SetPropPinger(mckPinger)
		app.SetRouter(mckRouter)
		app.SetEndpointHandler(mckEndHandler)
		srv := NewServer()
		So(srv.Init(app, srv.ConfigStruct()), ShouldBeNil)
		app.SetServer(srv)
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		mckStore.EXPECT().IDsToKey(uaid, chid).Return("", false)

		inArgs := JsMap{"channelID": chid}
		result, outArgs := srv.HandleCommand(PushCommand{
			Command:   REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 500)
		So(outArgs, ShouldEqual, inArgs)
		So(inArgs["status"], ShouldEqual, 200)
	})

	Convey("Should not encrypt endpoints without a key", t, func() {
		app := NewApplication()
		app.SetTokenKey("")
		app.SetLogger(mckLogger)
		app.SetStore(mckStore)
		app.SetPropPinger(mckPinger)
		app.SetRouter(mckRouter)
		app.SetEndpointHandler(mckEndHandler)
		srv := NewServer()
		So(srv.Init(app, srv.ConfigStruct()), ShouldBeNil)
		app.SetServer(srv)
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		gomock.InOrder(
			mckStore.EXPECT().IDsToKey(uaid, chid).Return("abc", true),
			mckEndHandler.EXPECT().URL().Return("https://example.com"),
		)

		inArgs := JsMap{"channelID": chid}
		result, outArgs := srv.HandleCommand(PushCommand{
			Command:   REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)
		So(outArgs["status"], ShouldEqual, 200)
		So(outArgs["push.endpoint"], ShouldEqual, "https://example.com/update/abc")
	})

	Convey("Should encrypt endpoints with a key", t, func() {
		app := NewApplication()
		app.SetTokenKey("HVozKz_n-DPopP5W877DpRKQOW_dylVf")
		app.SetLogger(mckLogger)
		app.SetStore(mckStore)
		app.SetPropPinger(mckPinger)
		app.SetRouter(mckRouter)
		app.SetEndpointHandler(mckEndHandler)
		srv := NewServer()
		So(srv.Init(app, srv.ConfigStruct()), ShouldBeNil)
		app.SetServer(srv)
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		gomock.InOrder(
			mckStore.EXPECT().IDsToKey(uaid, chid).Return("456", true),
			mckEndHandler.EXPECT().URL().Return("https://example.org"),
		)
		inArgs := JsMap{"channelID": chid}
		result, outArgs := srv.HandleCommand(PushCommand{
			Command:   REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)
		So(outArgs["status"], ShouldEqual, 200)
		So(outArgs["push.endpoint"], ShouldEqual,
			"https://example.org/update/AAECAwQFBgcICQoLDA0OD3afbw==")
	})

	Convey("Should reject invalid keys", t, func() {
		app := NewApplication()
		app.SetTokenKey("lLyhlLk8qus1ky4ER8yjN5o=") // aes.KeySizeError(17)
		app.SetLogger(mckLogger)
		app.SetStore(mckStore)
		app.SetPropPinger(mckPinger)
		app.SetRouter(mckRouter)
		app.SetEndpointHandler(mckEndHandler)
		srv := NewServer()
		So(srv.Init(app, srv.ConfigStruct()), ShouldBeNil)
		app.SetServer(srv)
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		mckStore.EXPECT().IDsToKey(uaid, chid).Return("123", true)

		inArgs := JsMap{"channelID": chid}
		result, outArgs := srv.HandleCommand(PushCommand{
			Command:   REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 500)
		So(outArgs, ShouldEqual, inArgs)
		So(inArgs["status"], ShouldEqual, 200)
	})
}

func TestRequestFlush(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := NewApplication()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckPinger := NewMockPropPinger(mockCtrl)
	app.SetPropPinger(mckPinger)

	srv := NewServer()
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		t.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)

	mckSocket := NewMockSocket(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	uaid := "41085ed1e5474ec9aa6ddef595b1bb6f"
	pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
	pws.SetUAID(uaid)
	client := &Client{mckWorker, pws, uaid}

	Convey("Should allow nil clients", t, func() {
		err := srv.RequestFlush(nil, "", 0, "")
		So(err, ShouldBeNil)
	})

	Convey("Should flush to the underlying worker", t, func() {
		mckWorker.EXPECT().Flush(pws, int64(0), "", int64(0), "").Return(nil)
		err := srv.RequestFlush(client, "", 0, "")
		So(err, ShouldBeNil)
	})

	Convey("Should send a proprietary ping if flush panics", t, func() {
		flushErr := errors.New("universe has imploded")
		flushPanic := func(*PushWS, int64, string, int64, string) error {
			panic(flushErr)
			return nil
		}
		chid := "41d1a3a6517b47d5a4aaabd82ae5f3ba"
		version := int64(3)
		data := "Unfortunately, as you probably already know, people"

		gomock.InOrder(
			mckWorker.EXPECT().Flush(pws, int64(0),
				chid, version, data).Do(flushPanic),
			mckPinger.EXPECT().Send(uaid, version, data),
		)

		err := srv.RequestFlush(client, chid, version, data)
		So(err, ShouldEqual, flushErr)
	})
}

func TestUpdateClient(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := NewApplication()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	srv := NewServer()
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		t.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)

	mckSocket := NewMockSocket(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	uaid := "e3cbb350304b4327a069d5c07fc434b8"
	pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
	pws.SetUAID(uaid)
	client := &Client{mckWorker, pws, uaid}

	chid := "d468168f24cc4ca8bae3b07530559be6"
	version := int64(3)
	data := "This is a test of the emergency broadcasting system."

	Convey("Should flush updates to the worker", t, func() {
		gomock.InOrder(
			mckStore.EXPECT().Update(uaid, chid, version).Return(nil),
			mckWorker.EXPECT().Flush(pws, int64(0), chid, version, data),
		)
		err := srv.UpdateClient(client, chid, uaid, version, time.Time{}, data)
		So(err, ShouldBeNil)
	})

	Convey("Should fail if storage is unavailable", t, func() {
		updateErr := errors.New("omg, everything is exploding")
		mckStore.EXPECT().Update(uaid, chid, version).Return(updateErr)
		err := srv.UpdateClient(client, chid, uaid, version, time.Time{}, data)
		So(err, ShouldEqual, updateErr)
	})

	Convey("Should fail if the worker returns an error", t, func() {
		flushErr := errors.New("cannot brew coffee with a teapot")
		gomock.InOrder(
			mckStore.EXPECT().Update(uaid, chid, version).Return(nil),
			mckWorker.EXPECT().Flush(pws, int64(0), chid, version, data).Return(flushErr),
		)
		err := srv.UpdateClient(client, chid, uaid, version, time.Time{}, data)
		So(err, ShouldEqual, flushErr)
	})
}

func BenchmarkEndpoint(b *testing.B) {
	mockCtrl := gomock.NewController(b)
	defer mockCtrl.Finish()

	app := NewApplication()
	srv := NewServer()
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		b.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)
	mckEndHandler := NewMockHandler(mockCtrl)
	mckEndHandler.EXPECT().URL().Return("https://example.com").Times(b.N)
	app.SetEndpointHandler(mckEndHandler)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		endpoint, err := srv.genEndpoint("123")
		if err != nil {
			b.Fatalf("Error generating push endpoint: %s", err)
		}
		expected := "https://example.com/update/123"
		if endpoint != expected {
			b.Fatalf("Wrong endpoint: got %q; want %q", endpoint, expected)
		}
	}
}
