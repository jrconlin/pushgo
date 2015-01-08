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

func TestWorkerRegister(t *testing.T) {
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

	srv := NewServer()
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		t.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)

	mckEndHandler := NewMockHandler(mockCtrl)
	app.SetEndpointHandler(mckEndHandler)

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should reject unidentified clients", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		err := wws.Register(pws, nil,
			[]byte(`{"channelID": "3fc2d1a2950411e49b203c15c2c622fe"}`))
		So(err, ShouldEqual, ErrInvalidCommand)
	})

	Convey("Should reject invalid channel IDs", t, func() {
		var err error
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("923e7a42951511e490403c15c2c622fe")

		// Invalid JSON.
		err = wws.Register(pws, nil, []byte(`{"channelID": "123",}`))
		So(err, ShouldEqual, ErrInvalidParams)

		// Invalid channel ID.
		err = wws.Register(pws, nil, []byte(`{"channelID": "123"}`))
		So(err, ShouldEqual, ErrInvalidParams)
	})

	Convey("Should fail if storage is unavailable", t, func() {
		uaid := "d0afa324950511e48aed3c15c2c622fe"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		chid := "f2265458950511e49cae3c15c2c622fe"
		regBytes, _ := json.Marshal(struct {
			ChannelID string `json:"channelID"`
		}{chid})

		storeErr := errors.New("oops")
		mckStore.EXPECT().Register(uaid, chid,
			int64(0)).Return(storeErr)
		err := wws.Register(pws, &RequestHeader{Type: "register"}, regBytes)
		So(err, ShouldEqual, storeErr)
	})

	Convey("Should generate endpoints for registered channels", t, func() {
		uaid := "8fe81c44950611e4aafe3c15c2c622fe"
		wws := NewWorker(app, "test")
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		chid := "930c80b8950611e4be663c15c2c622fe"
		regBytes, _ := json.Marshal(struct {
			ChannelID string `json:"channelID"`
		}{chid})

		gomock.InOrder(
			mckStore.EXPECT().Register(uaid, chid,
				int64(0)).Return(nil),
			mckStore.EXPECT().IDsToKey(uaid, chid).Return("123", true),
			mckEndHandler.EXPECT().URL().Return("https://example.com"),
			mckStat.EXPECT().Increment("updates.client.register"),
		)

		err := wws.Register(pws, &RequestHeader{Type: "register"}, regBytes)
		So(err, ShouldBeNil)

		dec := json.NewDecoder(rs.Outgoing)
		regReply := new(RegisterReply)
		So(dec.Decode(regReply), ShouldEqual, nil)
		So(regReply.Type, ShouldEqual, "register")
		So(regReply.DeviceID, ShouldEqual, uaid)
		So(regReply.Status, ShouldEqual, 200)
		So(regReply.ChannelID, ShouldEqual, chid)
		So(regReply.Endpoint, ShouldEqual, "https://example.com/update/123")
	})
}

func TestWorkerFlush(t *testing.T) {
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

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should reject unidentified clients", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		err := wws.Flush(pws, 0, "", 0, "")
		So(err, ShouldBeNil)
		So(wws.stopped(), ShouldBeTrue)
	})

	Convey("Should fail if storage is unavailable", t, func() {
		uaid := "6fcb17770fa54b95af7bd338c0f28737"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		fetchErr := errors.New("synergies not aligned")
		mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, fetchErr)
		err := wws.Flush(pws, 0, "", 0, "")
		So(err, ShouldEqual, fetchErr)
		So(wws.stopped(), ShouldBeFalse)
	})

	Convey("Should flush pending updates if the channel is omitted", t, func() {
		uaid := "bdee3a9cbbbf484a9cf8e11d1d22cf8c"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		updates := []Update{
			{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
			{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"},
		}
		expired := []string{"c778e94a950b11e4ba7f3c15c2c622fe"}

		gomock.InOrder(
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(
				updates, expired, nil),
			mckSocket.EXPECT().WriteJSON(&FlushReply{
				Type:    "notification",
				Updates: updates,
				Expired: expired,
			}),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err := wws.Flush(pws, 0, "", 0, "")
		So(err, ShouldBeNil)
	})

	Convey("Should not write to the socket if no updates are pending", t, func() {
		uaid := "21fd5a6e27764853b32308e0724b971d"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		gomock.InOrder(
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)
		err := wws.Flush(pws, 0, "", 0, "")
		So(err, ShouldBeNil)
	})

	Convey("Should send an update packet if a channel is given", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("4f76ffb92747471c85f8ebda7650b047")

		chid := "732bb79fc5684b76a67b9a08f547f968"
		version := int64(3)
		data := "Here is my handle; here is my spout"

		gomock.InOrder(
			mckStat.EXPECT().IncrementBy("updates.sent", int64(1)),
			mckSocket.EXPECT().WriteJSON(&FlushReply{
				Type:    "notification",
				Updates: []Update{{chid, uint64(version), data}},
			}),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err := wws.Flush(pws, 0, chid, version, data)
		So(err, ShouldBeNil)
	})
}

func TestWorkerACK(t *testing.T) {
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

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should reject unidentified clients", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		err := wws.Ack(pws, nil, nil)
		So(err, ShouldEqual, ErrInvalidCommand)
	})

	Convey("Should reject invalid JSON", t, func() {
		var err error

		uaid := "ae3c55b4e6df4519ad9963b6caebd452"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		err = wws.Ack(pws, nil, []byte(`{"updates":[],}`))
		So(err, ShouldEqual, ErrInvalidParams)

		err = wws.Ack(pws, nil, []byte(`{"updates":[]}`))
		So(err, ShouldEqual, ErrNoParams)

		err = wws.Ack(pws, nil, []byte(`{"updates":[],"expired":[]}`))
		So(err, ShouldEqual, ErrNoParams)
	})

	Convey("Should drop acknowledged updates", t, func() {
		uaid := "f443ace5e7ba4cd498ec7be46aaf9019"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		gomock.InOrder(
			mckStat.EXPECT().Increment("updates.client.ack"),
			mckStore.EXPECT().Drop(uaid, "263d09f8950b11e4a1f83c15c2c622fe"),
			mckStore.EXPECT().Drop(uaid, "bac9d83a950b11e4bd713c15c2c622fe"),
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		ackBytes, _ := json.Marshal(ACKRequest{Updates: []Update{
			{ChannelID: "263d09f8950b11e4a1f83c15c2c622fe", Version: 2},
			{ChannelID: "bac9d83a950b11e4bd713c15c2c622fe", Version: 4},
		}})
		err := wws.Ack(pws, nil, ackBytes)
		So(err, ShouldBeNil)
	})

	Convey("Should drop expired channels", t, func() {
		uaid := "60e4a4d575bb46bbbf4f569dd38dbc3b"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		gomock.InOrder(
			mckStat.EXPECT().Increment("updates.client.ack"),
			mckStore.EXPECT().Drop(uaid, "c778e94a950b11e4ba7f3c15c2c622fe"),
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		ackBytes, _ := json.Marshal(ACKRequest{
			Expired: []string{"c778e94a950b11e4ba7f3c15c2c622fe"},
		})
		err := wws.Ack(pws, nil, ackBytes)
		So(err, ShouldBeNil)
	})

	Convey("Should respond with pending updates", t, func() {
		uaid := "cb8859fded244a0fb0f11d2ed0c9a8b4"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		flushUpdates := []Update{
			{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
			{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"},
		}
		flushExpired := []string{"c778e94a950b11e4ba7f3c15c2c622fe"}

		gomock.InOrder(
			mckStat.EXPECT().Increment("updates.client.ack"),
			mckStore.EXPECT().Drop(uaid, "3b17fc39d36547789cb97d73a3b291bb"),
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(
				flushUpdates, flushExpired, nil),
			mckSocket.EXPECT().WriteJSON(&FlushReply{
				Type:    "notification",
				Updates: flushUpdates,
				Expired: flushExpired,
			}),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		ackBytes, _ := json.Marshal(ACKRequest{
			Updates: []Update{
				{ChannelID: "3b17fc39d36547789cb97d73a3b291bb", Version: 7}},
		})
		err := wws.Ack(pws, nil, ackBytes)
		So(err, ShouldBeNil)
	})

	Convey("Should not flush pending updates if an error occurs", t, func() {
		var err error

		uaid := "acc6135949c747d9a146e13cf9861380"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		ackBytes, _ := json.Marshal(ACKRequest{
			Updates: []Update{
				{ChannelID: "9d7db81aace04ad993cf8241281e9dda", Version: 6}},
			Expired: []string{"c778e94a950b11e4ba7f3c15c2c622fe"},
		})

		updateErr := errors.New("core competencies not leveraged")
		gomock.InOrder(
			mckStat.EXPECT().Increment("updates.client.ack"),
			mckStore.EXPECT().Drop(uaid,
				"9d7db81aace04ad993cf8241281e9dda").Return(updateErr),
		)
		err = wws.Ack(pws, nil, ackBytes)
		So(err, ShouldEqual, updateErr)

		expiredErr := errors.New("applicative in covariant position")
		gomock.InOrder(
			mckStat.EXPECT().Increment("updates.client.ack"),
			mckStore.EXPECT().Drop(uaid,
				"9d7db81aace04ad993cf8241281e9dda").Return(nil),
			mckStore.EXPECT().Drop(uaid,
				"c778e94a950b11e4ba7f3c15c2c622fe").Return(expiredErr),
		)
		err = wws.Ack(pws, nil, ackBytes)
		So(err, ShouldEqual, expiredErr)

		fetchErr := errors.New("unavailable for legal reasons")
		gomock.InOrder(
			mckStat.EXPECT().Increment("updates.client.ack"),
			mckStore.EXPECT().Drop(uaid,
				"9d7db81aace04ad993cf8241281e9dda").Return(nil),
			mckStore.EXPECT().Drop(uaid,
				"c778e94a950b11e4ba7f3c15c2c622fe").Return(nil),
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(
				nil, nil, fetchErr),
		)
		err = wws.Ack(pws, nil, ackBytes)
		So(err, ShouldEqual, fetchErr)
	})
}

func TestWorkerUnregister(t *testing.T) {
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

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should reject unidentified clients", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		err := wws.Unregister(pws, nil, nil)
		So(err, ShouldEqual, ErrInvalidCommand)
	})

	Convey("Should reject invalid JSON", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("780e7e2a41ce46c0917077642e18ca21")

		err := wws.Unregister(pws, nil, []byte(`{"channelID":"",}`))
		So(err, ShouldEqual, ErrInvalidParams)
	})

	Convey("Should reject empty channels", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("d499edd49b5b490599fe9dd124ea02bd")

		err := wws.Unregister(pws, nil, []byte(`{"channelID":""}`))
		So(err, ShouldEqual, ErrNoParams)
	})

	Convey("Should always return success", t, func() {
		var err error
		uaid := "af607eb788e74c73ba763947183c4aef"

		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		badChanID := "7e9ffa79e30245039504b070885123e8"
		okChanID := "0266aa1ab42842598f6c73ff2132de1e"

		mckStat.EXPECT().Increment("updates.client.unregister").Times(2)

		storeErr := errors.New("omg totes my bad")
		mckStore.EXPECT().Unregister(uaid, badChanID).Return(storeErr)
		mckSocket.EXPECT().WriteJSON(UnregisterReply{
			Type:      "unregister",
			Status:    200,
			ChannelID: badChanID})

		mckStore.EXPECT().Unregister(uaid, okChanID).Return(nil)
		mckSocket.EXPECT().WriteJSON(UnregisterReply{
			Type:      "unregister",
			Status:    200,
			ChannelID: okChanID})

		badUnreg, _ := json.Marshal(UnregisterRequest{badChanID})
		err = wws.Unregister(pws, &RequestHeader{Type: "unregister"}, badUnreg)
		So(err, ShouldBeNil)

		okUnreg, _ := json.Marshal(UnregisterRequest{okChanID})
		err = wws.Unregister(pws, &RequestHeader{Type: "unregister"}, okUnreg)
		So(err, ShouldBeNil)
	})
}

func TestWorkerHandshakeRedirect(t *testing.T) {
	installMocks()
	defer revertMocks()

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

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	Convey("Should return a 307 if the current node is full", t, func() {
		wws := NewWorker(app, "test")
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID("")

		mckBalancer.EXPECT().RedirectURL().Return("https://example.com/2", true, nil)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"},
			[]byte(`{"uaid":"","channelIDs":[]}`))
		So(err, ShouldBeNil)
		So(wws.stopped(), ShouldBeTrue)

		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldBeNil)
		So(helloReply.Type, ShouldEqual, "hello")
		So(helloReply.Status, ShouldEqual, 307)
		So(helloReply.DeviceID, ShouldEqual, testID)
		So(helloReply.RedirectURL, ShouldNotBeNil)
		So(*helloReply.RedirectURL, ShouldEqual, "https://example.com/2")
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
		So(wws.stopped(), ShouldBeTrue)

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

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should allow duplicate handshakes for empty IDs", t, func() {
		uaid := "4738b3be952911e4a7dc3c15c2c622fe"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, nil)
		mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		err := wws.Hello(pws, &RequestHeader{Type: "hello"},
			[]byte(`{"uaid":"","channelIDs":[]}`))
		So(err, ShouldBeNil)
		So(wws.stopped(), ShouldBeFalse)
	})

	Convey("Should allow duplicate handshakes for matching IDs", t, func() {
		uaid := "bd12381c953811e490cb3c15c2c622fe"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, nil)
		mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		helloBytes, _ := json.Marshal(struct {
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{uaid, []string{}})
		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, helloBytes)
		So(err, ShouldBeNil)
		So(wws.stopped(), ShouldBeFalse)
	})

	Convey("Should reject duplicate handshakes for mismatched IDs", t, func() {
		uaid := "479f5444953211e484b43c15c2c622fe"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"720f6b9a953411e4aadf3c15c2c622fe","channelIDs":[]}`))
		So(err, ShouldEqual, ErrExistingID)
	})
}

func TestWorkerError(t *testing.T) {
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

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	Convey("Should include request fields in error responses", t, func() {
		uaid := "ffb0232c953911e4b5133c15c2c622fe"
		wws := NewWorker(app, "test")
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		pws.SetUAID(uaid)

		newID := "720f6b9a953411e4aadf3c15c2c622fe"
		chids := id.MustGenerate(1)
		rs.RecordJSON(struct {
			Type       string   `json:"messageType"`
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{"HELLO", newID, chids})

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
		So(errorReply.ChannelIDs, ShouldResemble, chids)
	})
}

func BenchmarkWorkerRun(b *testing.B) {
	// I don't believe that goconvey handles benchmark well, so sadly, can't
	// reuse the test code.
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(b)
	defer mockCtrl.Finish()

	app := NewApplication()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckBalancer := NewMockBalancer(mockCtrl)
	mckBalancer.EXPECT().RedirectURL().Return("", false, nil).Times(b.N)
	app.SetBalancer(mckBalancer)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Increment("updates.client.hello").Times(b.N)
	mckStat.EXPECT().Timer("client.flush", gomock.Any()).Times(b.N)
	mckStat.EXPECT().Increment("updates.client.register").Times(b.N)
	mckStat.EXPECT().Increment("updates.client.ping").Times(b.N)
	mckStat.EXPECT().Increment("updates.client.unregister").Times(b.N)
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	mckStore.EXPECT().IDsToKey(testID,
		"89101cfa01dd4294a00e3a813cb3da97").Return("123", true).Times(b.N)
	mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(
		nil, nil, nil).Times(b.N)
	mckStore.EXPECT().Register(testID,
		"89101cfa01dd4294a00e3a813cb3da97", int64(0)).Times(b.N)
	mckStore.EXPECT().Unregister(testID,
		"89101cfa01dd4294a00e3a813cb3da97").Times(b.N)
	app.SetStore(mckStore)

	mckRouter := NewMockRouter(mockCtrl)
	mckRouter.EXPECT().Register(testID).Times(b.N)
	mckRouter.EXPECT().Unregister(testID).Times(b.N)
	app.SetRouter(mckRouter)

	srv := new(Serv)
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		b.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)

	mckEndHandler := NewMockHandler(mockCtrl)
	mckEndHandler.EXPECT().URL().Return("https://example.com").Times(b.N)
	app.SetEndpointHandler(mckEndHandler)

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
		srv.Bye(pws)
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

	app := NewApplication()
	app.clientMinPing = 10 * time.Second
	app.clientPongInterval = 10 * time.Second
	app.clientHelloTimeout = 10 * time.Second

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should close unidentified connections", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(timeNow().Add(wws.helloTimeout)),
			mckSocket.EXPECT().ReadBinary().Return(nil, &netErr{timeout: true}),
			mckSocket.EXPECT().Close(),
		)

		wws.Run(pws)
		So(wws.stopped(), ShouldBeTrue)
	})

	Convey("Should send pongs after handshake", t, func() {
		revertPongs := enableLongPongs(app, false)
		defer revertPongs()

		wws := NewWorker(app, "test")
		So(wws.state, ShouldEqual, WorkerInactive)
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		gomock.InOrder(
			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, nil),
			mckSocket.EXPECT().WriteText(gomock.Any()),
			mckStat.EXPECT().Increment("updates.client.hello"),
			mckStore.EXPECT().FetchAll(testID, gomock.Any()),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"},
			[]byte(`{"uaid":"","channelIDs":[]}`))
		So(err, ShouldBeNil)
		So(wws.state, ShouldEqual, WorkerActive)

		mckSocket.EXPECT().SetReadDeadline(timeNow().Add(wws.pongInterval)).Times(3)
		gomock.InOrder(
			mckSocket.EXPECT().ReadBinary().Return(nil, &netErr{timeout: true}),
			mckSocket.EXPECT().WriteText("{}"),

			mckSocket.EXPECT().ReadBinary().Return(nil, nil),

			mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			mckSocket.EXPECT().Close(),
		)

		wws.Run(pws)
		So(wws.stopped(), ShouldBeTrue)
	})

	Convey("Should ignore empty packets", t, func() {
		revertPongs := enableLongPongs(app, true)
		defer revertPongs()

		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		mckSocket.EXPECT().SetReadDeadline(gomock.Any()).Times(3)
		gomock.InOrder(
			mckSocket.EXPECT().ReadBinary().Return(nil, nil),
			mckSocket.EXPECT().ReadBinary().Return([]byte("{}"), nil),
			mckSocket.EXPECT().WriteJSON(PingReply{Type: "ping", Status: 200}),
			mckStat.EXPECT().Increment("updates.client.ping"),
			mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			mckSocket.EXPECT().Close(),
		)

		wws.Run(pws)
	})

	Convey("Should reject invalid JSON", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return([]byte(`{"messageType",}`), nil),
			mckSocket.EXPECT().Close(),
		)
		wws.Run(pws)
	})

	Convey("Should reject invalid packet headers", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		errReply := map[string]interface{}{"messageType": false}
		errReply["status"], errReply["error"] = ErrToStatus(ErrUnknownCommand)

		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return([]byte(
				`{"messageType":false}`), nil),
			mckSocket.EXPECT().WriteJSON(gomock.Any()),
			mckSocket.EXPECT().Close(),
		)
		wws.Run(pws)
	})

	Convey("Should reject unknown commands", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		errReply := map[string]interface{}{"messageType": "salutation"}
		errReply["status"], errReply["error"] = ErrToStatus(ErrUnknownCommand)

		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return([]byte(
				`{"messageType":"salutation"}`), nil),
			mckSocket.EXPECT().WriteJSON(errReply),
			mckSocket.EXPECT().Close(),
		)
		wws.Run(pws)
	})

	Convey("Should respond to client packets", t, func() {
		revertPongs := enableLongPongs(app, true)
		defer revertPongs()

		wws := NewWorker(app, "test")
		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}

		rs.Record([]byte(`{
				"messageType": "hello",
				"uaid": "",
				"channelIDs": [],
				"connect": {"id": 123}
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

		gomock.InOrder(
			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckServ.EXPECT().HandleCommand(PushCommand{
				Command: HELLO,
				Arguments: JsMap{
					"worker":  wws,
					"uaid":    testID,
					"chids":   []string{},
					"connect": []byte(`{"id":123}`),
				},
			}, pws).Return(200, nil),
			mckStat.EXPECT().Increment("updates.client.hello"),
			mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			mckStore.EXPECT().Register(testID,
				"89101cfa01dd4294a00e3a813cb3da97", int64(0)),
			mckServ.EXPECT().HandleCommand(PushCommand{
				Command:   REGIS,
				Arguments: JsMap{"channelID": "89101cfa01dd4294a00e3a813cb3da97"},
			}, pws).Return(200, JsMap{
				"uaid":          testID,
				"channelID":     "89101cfa01dd4294a00e3a813cb3da97",
				"push.endpoint": "https://example.com/123",
			}),
			mckStat.EXPECT().Increment("updates.client.register"),
			mckStat.EXPECT().Increment("updates.client.ping"),
			mckStore.EXPECT().Unregister(testID, "89101cfa01dd4294a00e3a813cb3da97"),
			mckStat.EXPECT().Increment("updates.client.unregister"),
		)
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
		So(regReply.Endpoint, ShouldEqual, "https://example.com/123")
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
}

func TestRunCase(t *testing.T) {
	installMocks()
	defer revertMocks()

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

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckSocket := NewMockSocket(mockCtrl)

	wws := NewWorker(app, "test")
	pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

	mckSocket.EXPECT().SetReadDeadline(gomock.Any()).Times(3)

	helloReply, _ := json.Marshal(HelloReply{
		Type:     "HELLO",
		Status:   200,
		DeviceID: testID})

	gomock.InOrder(
		mckSocket.EXPECT().ReadBinary().Return([]byte(
			`{"messageType":"HELLO","uaid":"","channelIDs":[]}`), nil),
		mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, nil),
		mckSocket.EXPECT().WriteText(string(helloReply)),
		mckStat.EXPECT().Increment("updates.client.hello"),
		mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
		mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		mckSocket.EXPECT().ReadBinary().Return([]byte(`{
			"messageType": "RegisteR",
			"channelID": "929c148c588746b29f4ea3dee52fdbd0"
		}`), nil),
		mckStore.EXPECT().Register(testID,
			"929c148c588746b29f4ea3dee52fdbd0", int64(0)),
		mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, JsMap{
			"channelID":     "929c148c588746b29f4ea3dee52fdbd0",
			"uaid":          testID,
			"push.endpoint": "https://example.com/1"}),
		mckSocket.EXPECT().WriteJSON(RegisterReply{
			Type:      "RegisteR",
			DeviceID:  testID,
			Status:    200,
			ChannelID: "929c148c588746b29f4ea3dee52fdbd0",
			Endpoint:  "https://example.com/1"}),
		mckStat.EXPECT().Increment("updates.client.register"),
		mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
		mckSocket.EXPECT().Close().Return(nil),
	)

	wws.Run(pws)
}

func TestWorkerHello(t *testing.T) {
	installMocks()
	defer revertMocks()

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

	mckRouter := NewMockRouter(mockCtrl)
	app.SetRouter(mckRouter)

	srv := NewServer()
	if err := srv.Init(app, srv.ConfigStruct()); err != nil {
		t.Fatalf("Error initializing server: %s", err)
	}
	app.SetServer(srv)

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should supply a device ID if the client omits one", t, func() {
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")

		gomock.InOrder(
			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckRouter.EXPECT().Register(testID),
			mckSocket.EXPECT().WriteText(gomock.Any()),
			mckStat.EXPECT().Increment("updates.client.hello"),
			mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"","channelIDs":[]}`))
		So(err, ShouldBeNil)

		So(pws.UAID(), ShouldEqual, testID)
		So(app.ClientExists(testID), ShouldBeTrue)

		gomock.InOrder(
			mckRouter.EXPECT().Unregister(testID),
			mckSocket.EXPECT().Close(),
		)
		app.Server().HandleCommand(PushCommand{Command: DIE}, pws)
		So(app.ClientExists(testID), ShouldBeFalse)
	})

	Convey("Should reject invalid JSON", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		err := wws.Hello(pws, nil, []byte(`{"uaid":false}`))
		So(err, ShouldEqual, ErrInvalidParams)
	})

	Convey("Should reject invalid IDs", t, func() {
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")
		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"!@#$","channelIDs":[]}`))
		So(err, ShouldEqual, ErrInvalidID)
		So(app.ClientExists("!@#$"), ShouldBeFalse)
	})

	Convey("Should issue new IDs for excessive prior channels", t, func() {
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")

		prevID := "ba14b1f1-90d0-4e72-8acf-e6ab71362e91"
		chids := id.MustGenerate(5)
		helloBytes, _ := json.Marshal(struct {
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{prevID, chids})

		gomock.InOrder(
			mckStore.EXPECT().CanStore(len(chids)).Return(false),
			mckStore.EXPECT().DropAll(prevID),
			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckRouter.EXPECT().Register(testID),
			mckSocket.EXPECT().WriteText(gomock.Any()),
			mckStat.EXPECT().Increment("updates.client.hello"),
			mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, helloBytes)
		So(err, ShouldBeNil)

		So(app.ClientExists(prevID), ShouldBeFalse)
		So(app.ClientExists(testID), ShouldBeTrue)
	})

	Convey("Should require the `channelIDs` field", t, func() {
		var err error

		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		err = wws.Hello(pws, nil, []byte(`{"uaid":"","channelIDs":false}`))
		So(err, ShouldEqual, ErrInvalidParams)

		err = wws.Hello(pws, nil, []byte(`{"uaid":""}`))
		So(err, ShouldEqual, ErrNoParams)
	})

	Convey("Should issue new IDs for nonexistent registrations", t, func() {
		oldID := "2214c771a8474edfb14448577863594d"
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		gomock.InOrder(
			mckStore.EXPECT().CanStore(1).Return(true),
			mckStore.EXPECT().Exists(oldID).Return(false),
			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckRouter.EXPECT().Register(testID),
			mckSocket.EXPECT().WriteText(gomock.Any()),
			mckStat.EXPECT().Increment("updates.client.hello"),
			mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"2214c771a8474edfb14448577863594d","channelIDs":["1"]}`))
		So(err, ShouldBeNil)

		So(app.ClientExists(oldID), ShouldBeFalse)
		So(app.ClientExists(testID), ShouldBeTrue)
	})
}

func TestWorkerPing(t *testing.T) {
	installMocks()
	defer revertMocks()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := NewApplication()
	app.clientMinPing = 10 * time.Second

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	app.SetMetrics(mckStat)

	mckSocket := NewMockSocket(mockCtrl)
	mckSocket.EXPECT().SetReadDeadline(gomock.Any()).AnyTimes()

	Convey("Should allow pings from unidentified clients", t, func() {
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
		revertPongs := enableLongPongs(app, true)
		defer revertPongs()

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

		So(wws.stopped(), ShouldBeTrue)
	})
}

func TestHandshakeFlush(t *testing.T) {
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

	mckBalancer := NewMockBalancer(mockCtrl)
	app.SetBalancer(mckBalancer)

	mckServ := NewMockServer(mockCtrl)
	app.SetServer(mckServ)

	mckRouter := NewMockRouter(mockCtrl)
	app.SetRouter(mckRouter)

	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should flush updates after handshake", t, func() {
		uaid := "b0b8afe6950c11e49aa73c15c2c622fe"
		chids := id.MustGenerate(3)

		rs := NewRecorderSocket()
		pws := &PushWS{Socket: rs, Store: app.Store(), Logger: app.Logger()}
		wws := NewWorker(app, "test")

		mckStore.EXPECT().CanStore(len(chids)).Return(true)
		mckStore.EXPECT().Exists(uaid).Return(true)
		mckBalancer.EXPECT().RedirectURL().Return("", false, nil)
		mckServ.EXPECT().HandleCommand(PushCommand{
			Command: HELLO,
			Arguments: JsMap{
				"worker":  wws,
				"uaid":    uaid,
				"chids":   chids,
				"connect": []byte(nil),
			},
		}, pws).Return(200, nil)
		mckStat.EXPECT().Increment("updates.client.hello")
		mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return([]Update{
			{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
			{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"},
		}, []string{"c778e94a950b11e4ba7f3c15c2c622fe"}, nil)
		mckStat.EXPECT().Timer("client.flush", gomock.Any())

		helloBytes, _ := json.Marshal(struct {
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{uaid, chids})
		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, helloBytes)
		So(err, ShouldBeNil)

		dec := json.NewDecoder(rs.Outgoing)
		helloReply := new(HelloReply)
		So(dec.Decode(helloReply), ShouldBeNil)
		So(helloReply.Type, ShouldEqual, "hello")
		So(helloReply.Status, ShouldEqual, 200)
		So(helloReply.DeviceID, ShouldEqual, uaid)
		flushReply := new(FlushReply)
		So(dec.Decode(flushReply), ShouldBeNil)
		So(flushReply.Type, ShouldEqual, "notification")
		So(flushReply.Updates, ShouldResemble, []Update{
			{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
			{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"}})
		So(flushReply.Expired, ShouldResemble, []string{
			"c778e94a950b11e4ba7f3c15c2c622fe"})
	})

	Convey("Should not flush updates if the handshake fails", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}
		handshakeErr := &netErr{temporary: true}

		errReply := map[string]interface{}{
			"messageType": "hello",
			"uaid":        "",
			"channelIDs":  []interface{}{"1"},
		}
		errReply["status"], errReply["error"] = ErrToStatus(handshakeErr)

		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return([]byte(
				`{"messageType":"hello","uaid":"","channelIDs":["1"]}`), nil),

			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckServ.EXPECT().HandleCommand(gomock.Any(), pws).Return(200, nil),

			mckSocket.EXPECT().WriteText(gomock.Any()).Return(handshakeErr),
			mckSocket.EXPECT().WriteJSON(errReply),
			mckSocket.EXPECT().Close(),
		)
		wws.Run(pws)
	})
}

func TestWorkerClientCollision(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	uaid := "1b156db9cda04b59ae6f85d229628306"

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()

	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	prevSocket := NewMockSocket(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should disconnect stale clients", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetRouter(mckRouter)
		app.SetBalancer(mckBalancer)

		mckServ := NewMockServer(mockCtrl)
		app.SetServer(mckServ)

		prevWorker := NewWorker(app, "test")
		prevPushSock := &PushWS{Socket: prevSocket,
			Store: app.Store(), Logger: app.Logger()}
		prevPushSock.SetUAID(uaid)

		prevClient := &Client{prevWorker, prevPushSock, uaid}
		app.AddClient(uaid, prevClient)
		So(app.ClientExists(uaid), ShouldBeTrue)

		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		gomock.InOrder(
			mckStore.EXPECT().CanStore(1).Return(true),
			mckServ.EXPECT().HandleCommand(PushCommand{DIE, nil},
				prevPushSock).Return(200, nil),
			mckStore.EXPECT().Exists(uaid).Return(true),
			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckServ.EXPECT().HandleCommand(PushCommand{
				Command: HELLO,
				Arguments: JsMap{
					"worker":  wws,
					"uaid":    uaid,
					"chids":   []string{"1"},
					"connect": []byte(nil),
				},
			}, pws).Return(200, nil),
			mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil),
			mckStat.EXPECT().Increment("updates.client.hello"),
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err := wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"1b156db9cda04b59ae6f85d229628306","channelIDs":["1"]}`))
		So(err, ShouldBeNil)
		So(pws.UAID(), ShouldEqual, uaid)
	})

	Convey("Should remove stale clients from the map", t, func() {
		var err error

		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetRouter(mckRouter)
		app.SetBalancer(mckBalancer)

		srv := NewServer()
		err = srv.Init(app, srv.ConfigStruct())
		So(err, ShouldBeNil)
		app.SetServer(srv)

		prevWorker := NewWorker(app, "test")
		prevPushSock := &PushWS{Socket: prevSocket,
			Store: app.Store(), Logger: app.Logger()}
		prevPushSock.SetUAID(uaid)

		prevClient := &Client{prevWorker, prevPushSock, uaid}
		app.AddClient(uaid, prevClient)
		So(app.ClientExists(uaid), ShouldBeTrue)

		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		gomock.InOrder(
			mckStore.EXPECT().CanStore(1).Return(true),
			mckRouter.EXPECT().Unregister(uaid),
			prevSocket.EXPECT().Close(),
			mckStore.EXPECT().Exists(uaid).Return(true),
			mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
			mckRouter.EXPECT().Register(uaid),
			mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil),
			mckStat.EXPECT().Increment("updates.client.hello"),
			mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
			mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		)

		err = wws.Hello(pws, &RequestHeader{Type: "hello"}, []byte(
			`{"uaid":"1b156db9cda04b59ae6f85d229628306","channelIDs":["1"]}`))
		So(err, ShouldBeNil)

		So(pws.UAID(), ShouldEqual, uaid)
		curClient, clientConnected := app.GetClient(uaid)
		So(clientConnected, ShouldBeTrue)
		So(curClient, ShouldResemble, &Client{wws, pws, uaid})
	})
}

// Test that harmless errors are harmless
func TestHarmlessConnectionError(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := NewApplication()
	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	app.SetLogger(mckLogger)
	mckStat := NewMockStatistician(mockCtrl)
	app.SetMetrics(mckStat)
	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)
	mckSocket := NewMockSocket(mockCtrl)

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

	Convey("Harmless socket errors should not be logged", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return(nil,
				errors.New("read tcp YYYYYYYYYYYY:YYYYY: connection timed out")),
		)

		wws.sniffer(pws)
	})
	Convey("Unknown socket errors should be logged", t, func() {
		wws := NewWorker(app, "test")
		pws := &PushWS{Socket: mckSocket, Store: app.Store(), Logger: app.Logger()}

		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return(nil,
				errors.New("universe has imploded")),
			mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
				gomock.Any(), gomock.Any()),
		)

		wws.sniffer(pws)
	})
}
