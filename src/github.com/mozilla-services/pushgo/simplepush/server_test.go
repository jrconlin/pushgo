/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/rafrombrc/gomock/gomock"
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

	mckStat := NewMockStatistician(mockCtrl)
	app.SetMetrics(mckStat)

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

	mckSocket := NewMockSocket(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	Convey("Should register with the proprietary pinger", t, func() {
		uaid := "c4fe17154cd74500ad1d51f2955fd79c"
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		pingData := []byte(`{"regid":123}`)
		inArgs := JsMap{
			"uaid": uaid,
			"channelIDs": []string{},
			"connect": pingData,
		}

		gomock.InOrder(
			mckPinger.EXPECT().Register(uaid, pingData),
			mckRouter.EXPECT().Register(uaid),
		)

		result, outArgs := srv.Hello(mckWorker, PushCommand{
			Command: HELLO, Arguments: inArgs}, pws)
		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)

		client, clientConnected := app.GetClient(uaid)
		So(clientConnected, ShouldBeTrue)
		So(client, ShouldResemble, &Client{
			Worker: mckWorker,
			PushWS: pws,
			UAID: uaid,
		})
	})
}

func TestServerBye(t *testing.T) {
	// ...
}

func TestServerRegister(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()

	mckStat := NewMockStatistician(mockCtrl)
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
		app.SetMetrics(mckStat)
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
			Command: REGIS,
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
		app.SetMetrics(mckStat)
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
			Command: REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)
		So(outArgs["status"], ShouldEqual, 200)
		So(outArgs["push.endpoint"], ShouldEqual, "https://example.com/update/abc")
	})

	Convey("Should support 16-byte keys (AES-128)", t, func() {
		app := NewApplication()
		app.SetTokenKey("QRJ3FUr1VwPlB9tC0h8gQg==")
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
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
			mckStore.EXPECT().IDsToKey(uaid, chid).Return("123", true),
			mckEndHandler.EXPECT().URL().Return("https://example.com"),
		)

		inArgs := JsMap{"channelID": chid}
		result, outArgs := srv.HandleCommand(PushCommand{
			Command: REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)
		So(outArgs["status"], ShouldEqual, 200)
		So(outArgs["push.endpoint"], ShouldEqual,
			"https://example.com/update/AAECAwQFBgcICQoLDA0OD4wQZQ==")
	})

	Convey("Should support 24-byte keys (AES-192)", t, func() {
		app := NewApplication()
		app.SetTokenKey("HVozKz_n-DPopP5W877DpRKQOW_dylVf")
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
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
			Command: REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)
		So(outArgs["status"], ShouldEqual, 200)
		So(outArgs["push.endpoint"], ShouldEqual,
			"https://example.org/update/AAECAwQFBgcICQoLDA0OD3afbw==")
	})

	Convey("Should support 32-byte keys (AES-256)", t, func() {
		app := NewApplication()
		app.SetTokenKey("XRQBxQjKTuNk2k8qbSNlwlAMtWYe45Ynw22hl7bM5uY=")
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
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
			mckStore.EXPECT().IDsToKey(uaid, chid).Return("789", true),
			mckEndHandler.EXPECT().URL().Return("https://example.net"),
		)
		inArgs := JsMap{"channelID": chid}
		result, outArgs := srv.HandleCommand(PushCommand{
			Command: REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 200)
		So(outArgs, ShouldEqual, inArgs)
		So(outArgs["status"], ShouldEqual, 200)
		So(outArgs["push.endpoint"], ShouldEqual,
			"https://example.net/update/AAECAwQFBgcICQoLDA0OD6R-pQ==")
	})

	Convey("Should reject invalid keys", t, func() {
		app := NewApplication()
		app.SetTokenKey("lLyhlLk8qus1ky4ER8yjN5o=") // aes.KeySizeError(17)
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
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
			Command: REGIS,
			Arguments: inArgs,
		}, pws)

		So(result, ShouldEqual, 500)
		So(outArgs, ShouldEqual, inArgs)
		So(inArgs["status"], ShouldEqual, 200)
	})
}

func TestRequestFlush(t *testing.T) {
	// ...
}

func TestUpdateClient(t *testing.T) {
	// ...
}

func TestHandleCommand(t *testing.T) {
	// ...
}
