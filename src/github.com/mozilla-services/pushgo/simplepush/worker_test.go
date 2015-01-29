/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"text/template"
	"time"

	"github.com/rafrombrc/gomock/gomock"
	. "github.com/smartystreets/goconvey/convey"
)

func TestWorkerRegister(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	invalidTemplate, err := template.New("Push").Parse("{{nil}}")
	if err != nil {
		t.Fatalf("Error parsing invalid endpoint template: %s", err)
	}

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckEndHandler := NewMockHandler(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should register channels", t, func() {
		app := NewApplication()
		app.endpointTemplate = testEndpointTemplate
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetEndpointHandler(mckEndHandler)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should reject unidentified clients", func() {
			wws.SetUAID("")
			err := wws.Register(nil,
				[]byte(`{"channelID": "3fc2d1a2950411e49b203c15c2c622fe"}`))
			So(err, ShouldEqual, ErrNoHandshake)
		})

		Convey("Should reject invalid channel IDs", func() {
			var err error
			wws.SetUAID("923e7a42951511e490403c15c2c622fe")

			// Invalid JSON.
			err = wws.Register(nil, []byte(`{"channelID": "123",}`))
			So(err, ShouldEqual, ErrInvalidParams)

			// Invalid channel ID.
			err = wws.Register(nil, []byte(`{"channelID": "123"}`))
			So(err, ShouldEqual, ErrInvalidParams)
		})

		Convey("Should fail if storage is unavailable", func() {
			uaid := "d0afa324950511e48aed3c15c2c622fe"
			wws.SetUAID(uaid)

			chid := "f2265458950511e49cae3c15c2c622fe"
			storeErr := errors.New("oops")

			mckStore.EXPECT().Register(uaid, chid,
				int64(0)).Return(storeErr)

			err := wws.Register(&RequestHeader{Type: "register"}, []byte(
				`{"channelID":"f2265458950511e49cae3c15c2c622fe"}`))
			So(err, ShouldEqual, storeErr)
		})

		Convey("Should fail for invalid primary keys", func() {
			uaid := "4eac2fad173b4306a7cbf6b3a3092edf"
			wws.SetUAID(uaid)

			chid := "f6022b9012a74e4ba392f13b86b7d8f6"
			keyErr := errors.New("universe has imploded")

			gomock.InOrder(
				mckStore.EXPECT().Register(uaid, chid, int64(0)).Return(nil),
				mckStore.EXPECT().IDsToKey(uaid, chid).Return("", keyErr),
			)
			err := wws.Register(&RequestHeader{Type: "register"}, []byte(
				`{"channelID":"f6022b9012a74e4ba392f13b86b7d8f6"}`))
			So(err, ShouldEqual, keyErr)
		})

		Convey("Should fail for invalid endpoint templates", func() {
			uaid := "0806b368-30ff-4327-9274-f23dc9fba5d9"
			wws.SetUAID(uaid)

			chid := "70107e766f9d4a03b0a75e3fb42f73a0"

			app.endpointTemplate = invalidTemplate
			gomock.InOrder(
				mckStore.EXPECT().Register(uaid, chid, int64(0)).Return(nil),
				mckStore.EXPECT().IDsToKey(uaid, chid).Return("123", nil),
				mckEndHandler.EXPECT().URL().Return("https://example.com"),
			)

			err := wws.Register(&RequestHeader{Type: "register"}, []byte(
				`{"channelID":"70107e766f9d4a03b0a75e3fb42f73a0"}`))
			So(err, ShouldNotBeNil)
		})

		Convey("Should generate endpoints for registered channels", func() {
			uaid := "8fe81c44950611e4aafe3c15c2c622fe"
			wws.SetUAID(uaid)

			chid := "930c80b8950611e4be663c15c2c622fe"

			gomock.InOrder(
				mckStore.EXPECT().Register(uaid, chid, int64(0)).Return(nil),
				mckStore.EXPECT().IDsToKey(uaid, chid).Return("123", nil),
				mckEndHandler.EXPECT().URL().Return("https://example.com"),
				mckSocket.EXPECT().WriteJSON(RegisterReply{
					Type:      "register",
					DeviceID:  uaid,
					Status:    200,
					ChannelID: chid,
					Endpoint:  "https://example.com/123",
				}),
				mckStat.EXPECT().Increment("updates.client.register"),
			)

			err := wws.Register(&RequestHeader{Type: "register"}, []byte(
				`{"channelID":"930c80b8950611e4be663c15c2c622fe"}`))
			So(err, ShouldBeNil)
		})
	})
}

func TestWorkerFlush(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should flush pending updates", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should reject unidentified clients", func() {
			wws.SetUAID("")

			err := wws.Send("", int64(0), "")
			So(err, ShouldBeNil)
			So(wws.stopped(), ShouldBeTrue)
		})

		Convey("Should fail if storage is unavailable", func() {
			uaid := "6fcb17770fa54b95af7bd338c0f28737"
			wws.SetUAID(uaid)

			fetchErr := errors.New("synergies not aligned")
			mckStore.EXPECT().FetchAll(uaid,
				gomock.Any()).Return(nil, nil, fetchErr)
			err := wws.Flush(0)
			So(err, ShouldEqual, fetchErr)
			So(wws.stopped(), ShouldBeFalse)
		})

		Convey("Should flush pending updates from storage", func() {
			uaid := "bdee3a9cbbbf484a9cf8e11d1d22cf8c"
			wws.SetUAID(uaid)

			updates := []Update{
				{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
				{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"},
			}
			expired := []string{"c778e94a950b11e4ba7f3c15c2c622fe"}

			gomock.InOrder(
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(
					updates, expired, nil),
				mckSocket.EXPECT().WriteJSON(FlushReply{
					Type:    "notification",
					Updates: updates,
					Expired: expired,
				}),
				mckStat.EXPECT().IncrementBy("updates.sent", int64(2)),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err := wws.Flush(0)
			So(err, ShouldBeNil)
		})

		Convey("Should not write to the socket if no updates are pending", func() {
			uaid := "21fd5a6e27764853b32308e0724b971d"
			wws.SetUAID(uaid)

			gomock.InOrder(
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)
			err := wws.Flush(0)
			So(err, ShouldBeNil)
		})
	})
}

func TestWorkerSend(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)
	mckPinger := NewMockPropPinger(mockCtrl)

	Convey("Should deliver new updates", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetPropPinger(mckPinger)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should reject unidentified clients", func() {
			wws.SetUAID("")

			err := wws.Flush(0)
			So(err, ShouldBeNil)
			So(wws.stopped(), ShouldBeTrue)
		})

		Convey("Should send a proprietary ping if flush panics", func() {
			uaid := "4dd3327e8a68462389eb6ab771c05867"
			wws.SetUAID(uaid)

			writeErr := errors.New("universe has imploded")
			writePanic := func(interface{}) error {
				panic(writeErr)
				return nil
			}
			chid := "41d1a3a6517b47d5a4aaabd82ae5f3ba"
			version := int64(3)
			data := "Unfortunately, as you probably already know, people"

			gomock.InOrder(
				mckSocket.EXPECT().WriteJSON(FlushReply{
					Type:    "notification",
					Updates: []Update{{chid, uint64(version), data}},
				}).Do(writePanic),
				mckPinger.EXPECT().Send(uaid, version, data),
			)

			err := wws.Send(chid, version, data)
			So(err, ShouldNotBeNil)
		})

		Convey("Should send an update with the given payload", func() {
			wws.SetUAID("4f76ffb92747471c85f8ebda7650b047")

			chid := "732bb79fc5684b76a67b9a08f547f968"
			version := int64(3)
			data := "Here is my handle; here is my spout"

			gomock.InOrder(
				mckSocket.EXPECT().WriteJSON(FlushReply{
					Type:    "notification",
					Updates: []Update{{chid, uint64(version), data}},
				}),
				mckStat.EXPECT().Increment("updates.sent"),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err := wws.Send(chid, version, data)
			So(err, ShouldBeNil)
		})
	})
}

func TestWorkerACK(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should accept acknowledgements", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should reject unidentified clients", func() {
			wws.SetUAID("")
			err := wws.Ack(nil, nil)
			So(err, ShouldEqual, ErrNoHandshake)
		})

		Convey("Should reject invalid JSON", func() {
			var err error
			uaid := "ae3c55b4e6df4519ad9963b6caebd452"
			wws.SetUAID(uaid)

			err = wws.Ack(nil, []byte(`{"updates":[],}`))
			So(err, ShouldEqual, ErrInvalidParams)

			err = wws.Ack(nil, []byte(`{"updates":[]}`))
			So(err, ShouldEqual, ErrNoParams)

			err = wws.Ack(nil, []byte(`{"updates":[],"expired":[]}`))
			So(err, ShouldEqual, ErrNoParams)
		})

		Convey("Should drop acknowledged updates", func() {
			uaid := "f443ace5e7ba4cd498ec7be46aaf9019"
			wws.SetUAID(uaid)

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
			err := wws.Ack(nil, ackBytes)
			So(err, ShouldBeNil)
		})

		Convey("Should drop expired channels", func() {
			uaid := "60e4a4d575bb46bbbf4f569dd38dbc3b"
			wws.SetUAID(uaid)

			gomock.InOrder(
				mckStat.EXPECT().Increment("updates.client.ack"),
				mckStore.EXPECT().Drop(uaid, "c778e94a950b11e4ba7f3c15c2c622fe"),
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			ackBytes, _ := json.Marshal(ACKRequest{
				Expired: []string{"c778e94a950b11e4ba7f3c15c2c622fe"},
			})
			err := wws.Ack(nil, ackBytes)
			So(err, ShouldBeNil)
		})

		Convey("Should respond with pending updates", func() {
			uaid := "cb8859fded244a0fb0f11d2ed0c9a8b4"
			wws.SetUAID(uaid)

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
				mckSocket.EXPECT().WriteJSON(FlushReply{
					Type:    "notification",
					Updates: flushUpdates,
					Expired: flushExpired,
				}),
				mckStat.EXPECT().IncrementBy("updates.sent", int64(2)),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			ackBytes, _ := json.Marshal(ACKRequest{
				Updates: []Update{
					{ChannelID: "3b17fc39d36547789cb97d73a3b291bb", Version: 7}},
			})
			err := wws.Ack(nil, ackBytes)
			So(err, ShouldBeNil)
		})

		Convey("Should not flush pending updates if an error occurs", func() {
			var err error
			uaid := "acc6135949c747d9a146e13cf9861380"
			wws.SetUAID(uaid)

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
			err = wws.Ack(nil, ackBytes)
			So(err, ShouldEqual, updateErr)

			expiredErr := errors.New("applicative in covariant position")
			gomock.InOrder(
				mckStat.EXPECT().Increment("updates.client.ack"),
				mckStore.EXPECT().Drop(uaid,
					"9d7db81aace04ad993cf8241281e9dda").Return(nil),
				mckStore.EXPECT().Drop(uaid,
					"c778e94a950b11e4ba7f3c15c2c622fe").Return(expiredErr),
			)
			err = wws.Ack(nil, ackBytes)
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
			err = wws.Ack(nil, ackBytes)
			So(err, ShouldEqual, fetchErr)
		})
	})
}

func TestWorkerUnregister(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should deregister channels", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)

		Convey("Should reject unidentified clients", func() {
			wws := NewWorker(app, mckSocket, "test")
			wws.SetUAID("")

			err := wws.Unregister(nil, nil)
			So(err, ShouldEqual, ErrNoHandshake)
		})

		Convey("Should reject invalid JSON", func() {
			wws := NewWorker(app, mckSocket, "test")
			wws.SetUAID("780e7e2a41ce46c0917077642e18ca21")

			err := wws.Unregister(nil, []byte(`{"channelID":"",}`))
			So(err, ShouldEqual, ErrInvalidParams)
		})

		Convey("Should reject empty channels", func() {
			wws := NewWorker(app, mckSocket, "test")
			wws.SetUAID("d499edd49b5b490599fe9dd124ea02bd")

			err := wws.Unregister(nil, []byte(`{"channelID":""}`))
			So(err, ShouldEqual, ErrNoParams)
		})

		Convey("Should always return success", func() {
			var err error
			uaid := "af607eb788e74c73ba763947183c4aef"

			wws := NewWorker(app, mckSocket, "test")
			wws.SetUAID(uaid)

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
			err = wws.Unregister(&RequestHeader{Type: "unregister"}, badUnreg)
			So(err, ShouldBeNil)

			okUnreg, _ := json.Marshal(UnregisterRequest{okChanID})
			err = wws.Unregister(&RequestHeader{Type: "unregister"}, okUnreg)
			So(err, ShouldBeNil)
		})
	})
}

func TestWorkerHandshakeRedirect(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should support redirects", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetBalancer(mckBalancer)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should return a 307 if the current node is full", func() {
			wws.SetUAID("")

			redirectReply := HelloReply{
				Type:        "hello",
				DeviceID:    testID,
				Status:      307,
				RedirectURL: new(string),
			}
			*redirectReply.RedirectURL = "https://example.com/2"
			replyBytes, _ := json.Marshal(redirectReply)
			gomock.InOrder(
				mckBalancer.EXPECT().RedirectURL().Return(
					"https://example.com/2", true, nil),
				mckSocket.EXPECT().WriteText(string(replyBytes)),
			)
			err := wws.Hello(&RequestHeader{Type: "hello"},
				[]byte(`{"uaid":"","channelIDs":[]}`))
			So(err, ShouldBeNil)
			So(wws.stopped(), ShouldBeTrue)
		})

		Convey("Should return a 429 if the cluster is full", func() {
			wws.SetUAID("")

			replyBytes, _ := json.Marshal(HelloReply{
				Type:     "HELLO",
				DeviceID: testID,
				Status:   429,
			})
			gomock.InOrder(
				mckBalancer.EXPECT().RedirectURL().Return("", false, ErrNoPeers),
				mckSocket.EXPECT().WriteText(string(replyBytes)),
			)
			err := wws.Hello(&RequestHeader{Type: "HELLO"},
				[]byte(`{"uaid":"","channelIDs":[]}`))

			So(err, ShouldBeNil)
			So(wws.stopped(), ShouldBeTrue)
		})
	})
}

func TestWorkerHandshakeDupe(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)

	Convey("Should handle duplicate handshakes", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetBalancer(mckBalancer)
		app.SetRouter(mckRouter)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should allow duplicate handshakes for empty IDs", func() {
			uaid := "4738b3be952911e4a7dc3c15c2c622fe"
			wws.SetUAID(uaid)

			gomock.InOrder(
				mckRouter.EXPECT().Register(uaid).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)
			err := wws.Hello(&RequestHeader{Type: "hello"},
				[]byte(`{"uaid":"","channelIDs":[]}`))

			So(err, ShouldBeNil)
			So(wws.stopped(), ShouldBeFalse)
		})

		Convey("Should allow duplicate handshakes for matching IDs", func() {
			uaid := "bd12381c953811e490cb3c15c2c622fe"
			wws.SetUAID(uaid)

			gomock.InOrder(
				mckRouter.EXPECT().Register(uaid).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)
			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"bd12381c953811e490cb3c15c2c622fe","channelIDs":[]}`))

			So(err, ShouldBeNil)
			So(wws.stopped(), ShouldBeFalse)
		})

		Convey("Should reject duplicate handshakes for mismatched IDs", func() {
			uaid := "479f5444953211e484b43c15c2c622fe"
			wws.SetUAID(uaid)

			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"720f6b9a953411e4aadf3c15c2c622fe","channelIDs":[]}`))
			So(err, ShouldEqual, ErrExistingID)
		})
	})
}

func TestWorkerError(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should report errors", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetBalancer(mckBalancer)

		Convey("Should include request fields in error responses", func() {
			uaid := "ffb0232c953911e4b5133c15c2c622fe"
			wws := NewWorker(app, mckSocket, "test")
			wws.SetUAID(uaid)

			newID := "720f6b9a953411e4aadf3c15c2c622fe"
			errStatus, errText := ErrToStatus(ErrExistingID)

			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte(`{
					"messageType": "HELLO",
					"uaid": "720f6b9a953411e4aadf3c15c2c622fe",
					"channelIDs": ["1"]
				}`), nil),
				mckSocket.EXPECT().WriteJSON(map[string]interface{}{
					"status":      errStatus,
					"error":       errText,
					"messageType": "HELLO",
					"uaid":        newID,
					"channelIDs":  []interface{}{"1"},
				}),
			)
			wws.Run()
		})
	})
}

func BenchmarkWorkerRun(b *testing.B) {
	// I don't believe that goconvey handles benchmark well, so sadly, can't
	// reuse the test code.
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(b)
	defer mockCtrl.Finish()

	app := NewApplication()
	app.endpointTemplate = testEndpointTemplate
	app.pushLongPongs = false

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
		"89101cfa01dd4294a00e3a813cb3da97").Return("123", nil).Times(b.N)
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

	mckEndHandler := NewMockHandler(mockCtrl)
	mckEndHandler.EXPECT().URL().Return("https://example.com").Times(b.N)
	app.SetEndpointHandler(mckEndHandler)

	mckSocket := NewMockSocket(mockCtrl)

	// Prebuild the buffers so we're not timing JSON.
	helloBytes := []byte(`{
		"messageType": "hello",
		"uaid": "",
		"channelIDs": []
	}`)
	regBytes := []byte(`{
		"messageType": "register",
		"channelID": "89101cfa01dd4294a00e3a813cb3da97"
	}`)
	pingBytes := []byte("{}")
	unregBytes := []byte(`{
		"messageType": "unregister",
		"channelID": "89101cfa01dd4294a00e3a813cb3da97"
	}`)

	// Reset so we only test the bits we care about.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gomock.InOrder(
			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return(helloBytes, nil),
			mckSocket.EXPECT().WriteText(gomock.Any()),

			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return(regBytes, nil),
			mckSocket.EXPECT().WriteJSON(gomock.Any()),

			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return(pingBytes, nil),
			mckSocket.EXPECT().WriteText(gomock.Any()),

			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return(unregBytes, nil),
			mckSocket.EXPECT().WriteJSON(gomock.Any()),

			mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
			mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			mckSocket.EXPECT().Close(),
		)
		wws := NewWorker(app, mckSocket, "test")
		wws.Run()
		wws.Close()
	}
}

func TestWorkerPinger(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckPinger := NewMockPropPinger(mockCtrl)

	Convey("Should support proprietary pingers", t, func() {
		app := NewApplication()
		app.endpointTemplate = testEndpointTemplate
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetBalancer(mckBalancer)
		app.SetRouter(mckRouter)
		app.SetPropPinger(mckPinger)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should register with the proprietary pinger", func() {
			gomock.InOrder(
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckPinger.EXPECT().Register(testID, []byte(`{"regid":123}`)).Return(nil),
				mckRouter.EXPECT().Register(testID),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)
			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"","channelIDs":[],"connect":{"regid":123}}`))
			So(err, ShouldBeNil)
			So(wws.state, ShouldEqual, WorkerActive)
		})

		Convey("Should not fail if pinger registration fails", func() {
			uaid := "3529f588b03e411295b8df6d38e63ce7"

			gomock.InOrder(
				mckStore.EXPECT().CanStore(0).Return(true),
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckPinger.EXPECT().Register(uaid, []byte(`[123]`)).Return(errors.New(
					"external system on fire")),
				mckRouter.EXPECT().Register(uaid).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(`{
				"uaid": "3529f588b03e411295b8df6d38e63ce7",
				"channelIDs": [],
				"connect": [123]
			}`))
			So(err, ShouldBeNil)
			So(wws.state, ShouldEqual, WorkerActive)
		})
	})
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
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckPinger := NewMockPropPinger(mockCtrl)
	mckEndHandler := NewMockHandler(mockCtrl)

	Convey("Should read and respond to client commands", t, func() {
		app := NewApplication()
		app.endpointTemplate = testEndpointTemplate
		app.clientMinPing = 10 * time.Second
		app.clientPongInterval = 10 * time.Second
		app.clientHelloTimeout = 10 * time.Second
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetBalancer(mckBalancer)
		app.SetRouter(mckRouter)
		app.SetPropPinger(mckPinger)
		app.SetEndpointHandler(mckEndHandler)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should close unidentified connections", func() {
			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(wws.Born().Add(wws.helloTimeout)),
				mckSocket.EXPECT().ReadBinary().Return(nil, &netErr{timeout: true}),
			)

			wws.Run()
			So(wws.stopped(), ShouldBeTrue)
		})

		Convey("Should send pongs after handshake", func() {
			app.pushLongPongs = true
			So(wws.state, ShouldEqual, WorkerInactive)

			gomock.InOrder(
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckRouter.EXPECT().Register(testID).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(testID, gomock.Any()),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err := wws.Hello(&RequestHeader{Type: "hello"},
				[]byte(`{"uaid":"","channelIDs":[]}`))
			So(err, ShouldBeNil)
			So(wws.state, ShouldEqual, WorkerActive)

			mckSocket.EXPECT().SetReadDeadline(timeNow().Add(wws.pongInterval)).Times(3)
			gomock.InOrder(
				mckSocket.EXPECT().ReadBinary().Return(nil, &netErr{timeout: true}),
				mckSocket.EXPECT().WriteText("{}"),

				mckSocket.EXPECT().ReadBinary().Return(nil, nil),

				mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			)

			wws.Run()
			So(wws.stopped(), ShouldBeTrue)
		})

		Convey("Should ignore empty packets", func() {
			app.pushLongPongs = true
			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return(nil, nil),

				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte("{}"), nil),
				mckSocket.EXPECT().WriteJSON(PingReply{Type: "ping", Status: 200}),
				mckStat.EXPECT().Increment("updates.client.ping"),

				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			)

			wws.Run()
		})

		Convey("Should reject invalid JSON", func() {
			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte(`{"messageType",}`), nil),
			)
			wws.Run()
		})

		Convey("Should reject invalid packet headers", func() {
			errReply := map[string]interface{}{"messageType": false}
			errReply["status"], errReply["error"] = ErrToStatus(ErrUnsupportedType)

			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte(
					`{"messageType":false}`), nil),
				mckSocket.EXPECT().WriteJSON(gomock.Any()),
			)
			wws.Run()
		})

		Convey("Should reject unknown commands", func() {
			errReply := map[string]interface{}{"messageType": "salutation"}
			errReply["status"], errReply["error"] = ErrToStatus(ErrUnsupportedType)

			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte(
					`{"messageType":"salutation"}`), nil),
				mckSocket.EXPECT().WriteJSON(errReply),
			)
			wws.Run()
		})

		Convey("Should respond to client packets", func() {
			app.pushLongPongs = true
			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte(`{
					"messageType": "hello",
					"uaid": "",
					"channelIDs": [],
					"connect": {"id": 123}
				}`), nil),
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckPinger.EXPECT().Register(testID, []byte(`{"id":123}`)).Return(nil),
				mckRouter.EXPECT().Register(testID).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),

				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte(`{
					"messageType": "register",
					"channelID": "89101cfa01dd4294a00e3a813cb3da97"
				}`), nil),
				mckStore.EXPECT().Register(testID,
					"89101cfa01dd4294a00e3a813cb3da97", int64(0)),
				mckStore.EXPECT().IDsToKey(testID,
					"89101cfa01dd4294a00e3a813cb3da97").Return("123", nil),
				mckEndHandler.EXPECT().URL().Return("https://example.com"),
				mckSocket.EXPECT().WriteJSON(RegisterReply{
					Type:      "register",
					DeviceID:  testID,
					Status:    200,
					ChannelID: "89101cfa01dd4294a00e3a813cb3da97",
					Endpoint:  "https://example.com/123",
				}),
				mckStat.EXPECT().Increment("updates.client.register"),

				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte("{}"), nil),
				mckSocket.EXPECT().WriteJSON(PingReply{
					Type:   "ping",
					Status: 200,
				}),
				mckStat.EXPECT().Increment("updates.client.ping"),

				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return([]byte(`{
					"messageType": "unregister",
					"channelID": "89101cfa01dd4294a00e3a813cb3da97"
				}`), nil),
				mckStore.EXPECT().Unregister(testID, "89101cfa01dd4294a00e3a813cb3da97"),
				mckSocket.EXPECT().WriteJSON(UnregisterReply{
					Type:      "unregister",
					Status:    200,
					ChannelID: "89101cfa01dd4294a00e3a813cb3da97",
				}),
				mckStat.EXPECT().Increment("updates.client.unregister"),

				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
			)
			wws.Run()
		})
	})
}

func TestRunCase(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := NewApplication()
	app.endpointTemplate = testEndpointTemplate

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

	mckEndHandler := NewMockHandler(mockCtrl)
	app.SetEndpointHandler(mckEndHandler)

	mckSocket := NewMockSocket(mockCtrl)

	wws := NewWorker(app, mckSocket, "test")

	helloReply, _ := json.Marshal(HelloReply{
		Type:     "HELLO",
		Status:   200,
		DeviceID: testID})

	gomock.InOrder(
		mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
		mckSocket.EXPECT().ReadBinary().Return([]byte(
			`{"messageType":"HELLO","uaid":"","channelIDs":[]}`), nil),
		mckRouter.EXPECT().Register(testID).Return(nil),
		mckSocket.EXPECT().WriteText(string(helloReply)),
		mckStat.EXPECT().Increment("updates.client.hello"),
		mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
		mckStat.EXPECT().Timer("client.flush", gomock.Any()),

		mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
		mckSocket.EXPECT().ReadBinary().Return([]byte(`{
			"messageType": "RegisteR",
			"channelID": "929c148c588746b29f4ea3dee52fdbd0"
		}`), nil),
		mckStore.EXPECT().Register(testID,
			"929c148c588746b29f4ea3dee52fdbd0", int64(0)),
		mckStore.EXPECT().IDsToKey(testID,
			"929c148c588746b29f4ea3dee52fdbd0").Return("1", nil),
		mckEndHandler.EXPECT().URL().Return("https://example.com"),
		mckSocket.EXPECT().WriteJSON(RegisterReply{
			Type:      "RegisteR",
			DeviceID:  testID,
			Status:    200,
			ChannelID: "929c148c588746b29f4ea3dee52fdbd0",
			Endpoint:  "https://example.com/1"}),
		mckStat.EXPECT().Increment("updates.client.register"),

		mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
		mckSocket.EXPECT().ReadBinary().Return(nil, io.EOF),
	)

	wws.Run()
}

func TestWorkerHello(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should perform handshakes", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetRouter(mckRouter)
		app.SetBalancer(mckBalancer)

		Convey("Should supply a device ID if the client omits one", func() {
			wws := NewWorker(app, mckSocket, "test")

			gomock.InOrder(
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckRouter.EXPECT().Register(testID).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"","channelIDs":[]}`))
			So(err, ShouldBeNil)

			So(wws.UAID(), ShouldEqual, testID)
			So(app.WorkerExists(testID), ShouldBeTrue)

			gomock.InOrder(
				mckRouter.EXPECT().Unregister(testID),
				mckSocket.EXPECT().Close(),
			)
			wws.Close()
			So(app.WorkerExists(testID), ShouldBeFalse)
		})

		Convey("Should reject invalid JSON", func() {
			wws := NewWorker(app, mckSocket, "test")

			err := wws.Hello(nil, []byte(`{"uaid":false}`))
			So(err, ShouldEqual, ErrInvalidParams)
		})

		Convey("Should reject invalid IDs", func() {
			wws := NewWorker(app, mckSocket, "test")
			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"!@#$","channelIDs":[]}`))
			So(err, ShouldEqual, ErrInvalidID)
			So(app.WorkerExists("!@#$"), ShouldBeFalse)
		})

		Convey("Should issue new IDs for excessive prior channels", func() {
			wws := NewWorker(app, mckSocket, "test")

			prevID := "ba14b1f190d04e728acfe6ab71362e91"
			gomock.InOrder(
				mckStore.EXPECT().CanStore(5).Return(false),
				mckStore.EXPECT().DropAll(prevID),
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckRouter.EXPECT().Register(testID).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)
			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(`{
				"uaid": "ba14b1f190d04e728acfe6ab71362e91",
				"channelIDs": ["1", "2", "3", "4", "5"]
			}`))

			So(err, ShouldBeNil)
			So(app.WorkerExists(prevID), ShouldBeFalse)
			So(app.WorkerExists(testID), ShouldBeTrue)
		})

		Convey("Should require the `channelIDs` field", func() {
			var err error

			wws := NewWorker(app, mckSocket, "test")

			err = wws.Hello(nil, []byte(`{"uaid":"","channelIDs":false}`))
			So(err, ShouldEqual, ErrInvalidParams)

			err = wws.Hello(nil, []byte(`{"uaid":""}`))
			So(err, ShouldEqual, ErrNoParams)
		})

		Convey("Should issue new IDs for nonexistent registrations", func() {
			oldID := "2214c771a8474edfb14448577863594d"
			wws := NewWorker(app, mckSocket, "test")

			gomock.InOrder(
				mckStore.EXPECT().CanStore(1).Return(true),
				mckStore.EXPECT().Exists(oldID).Return(false),
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckRouter.EXPECT().Register(testID).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(testID, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"2214c771a8474edfb14448577863594d","channelIDs":["1"]}`))
			So(err, ShouldBeNil)

			So(app.WorkerExists(oldID), ShouldBeFalse)
			So(app.WorkerExists(testID), ShouldBeTrue)
		})
	})
}

func TestWorkerPing(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)
	mckSocket.EXPECT().SetReadDeadline(gomock.Any()).AnyTimes()

	Convey("Should respond to pings", t, func() {
		app := NewApplication()
		app.clientMinPing = 10 * time.Second
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should allow pings from unidentified clients", func() {
			app.pushLongPongs = false
			wws.SetUAID("")

			gomock.InOrder(
				mckSocket.EXPECT().WriteText("{}"),
				mckStat.EXPECT().Increment("updates.client.ping"),
			)

			err := wws.Ping(&RequestHeader{Type: "ping"}, nil)
			So(err, ShouldBeNil)
		})

		Convey("Can respond with short pongs", func() {
			app.pushLongPongs = false
			wws.pingInt = 0 // Disable minimum ping interval check.

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
			)
			wws.Run()
		})

		Convey("Can respond with long pongs", func() {
			app.pushLongPongs = true
			wws.pingInt = 0

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
			)
			wws.Run()
		})

		Convey("Should return an error for excessive pings", func() {
			var err error
			app.pushLongPongs = true
			wws.SetUAID("04b1c85c95e011e49b103c15c2c622fe")

			mckSocket.EXPECT().WriteJSON(PingReply{Type: "ping", Status: 200}).Times(2)
			mckStat.EXPECT().Increment("updates.client.ping").Times(2)

			err = wws.Ping(&RequestHeader{Type: "ping"}, nil)
			So(err, ShouldBeNil)

			wws.lastPing = wws.lastPing.Add(-wws.pingInt)
			err = wws.Ping(&RequestHeader{Type: "ping"}, nil)
			So(err, ShouldBeNil)

			gomock.InOrder(
				mckSocket.EXPECT().Origin().Return("https://example.com"),
				mckStat.EXPECT().Increment("updates.client.too_many_pings"),
			)

			wws.lastPing = wws.lastPing.Add(-wws.pingInt / 2)
			err = wws.Ping(&RequestHeader{Type: "ping"}, nil)
			So(err, ShouldEqual, ErrTooManyPings)

			So(wws.stopped(), ShouldBeTrue)
		})
	})
}

func TestHandshakeFlush(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckBalancer := NewMockBalancer(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckSocket := NewMockSocket(mockCtrl)

	Convey("Should send updates to the client", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetBalancer(mckBalancer)
		app.SetRouter(mckRouter)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Should flush updates after handshake", func() {
			uaid := "b0b8afe6950c11e49aa73c15c2c622fe"
			updates := []Update{
				{"263d09f8950b11e4a1f83c15c2c622fe", 2, "I'm a little teapot"},
				{"bac9d83a950b11e4bd713c15c2c622fe", 4, "Short and stout"},
			}
			expired := []string{"c778e94a950b11e4ba7f3c15c2c622fe"}

			gomock.InOrder(
				mckStore.EXPECT().CanStore(3).Return(true),
				mckStore.EXPECT().Exists(uaid).Return(true),
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckRouter.EXPECT().Register(uaid).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(
					updates, expired, nil),
				mckSocket.EXPECT().WriteJSON(FlushReply{
					Type:    "notification",
					Updates: updates,
					Expired: expired,
				}),
				mckStat.EXPECT().IncrementBy("updates.sent", int64(2)),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)
			err := wws.Hello(&RequestHeader{Type: "hello"}, []byte(`{
				"uaid": "b0b8afe6950c11e49aa73c15c2c622fe",
				"channelIDs": ["1", "2", "3"]
			}`))

			So(err, ShouldBeNil)
		})

		Convey("Should not flush updates if the handshake fails", func() {
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
				mckRouter.EXPECT().Register(gomock.Any()).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()).Return(handshakeErr),
				mckSocket.EXPECT().WriteJSON(errReply),
			)
			wws.Run()
		})
	})
}

func TestWorkerClientCollision(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

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

	uaid := "1b156db9cda04b59ae6f85d229628306"

	Convey("Should handle client collisions", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetRouter(mckRouter)
		app.SetBalancer(mckBalancer)

		prevWorker := NewWorker(app, prevSocket, "test")
		prevWorker.SetUAID(uaid)
		app.AddWorker(uaid, prevWorker)
		So(app.WorkerExists(uaid), ShouldBeTrue)

		curWorker := NewWorker(app, mckSocket, "test")

		Convey("Should disconnect stale clients", func() {
			gomock.InOrder(
				mckStore.EXPECT().CanStore(1).Return(true),
				mckRouter.EXPECT().Unregister(uaid).Return(nil),
				prevSocket.EXPECT().Close(),
				mckStore.EXPECT().Exists(uaid).Return(true),
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckRouter.EXPECT().Register(uaid).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err := curWorker.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"1b156db9cda04b59ae6f85d229628306","channelIDs":["1"]}`))
			So(err, ShouldBeNil)
			So(curWorker.UAID(), ShouldEqual, uaid)
			worker, workerConnected := app.GetWorker(uaid)
			So(workerConnected, ShouldBeTrue)
			So(worker, ShouldEqual, curWorker)
		})

		Convey("Should remove stale clients from the map", func() {
			var err error

			gomock.InOrder(
				mckStore.EXPECT().CanStore(1).Return(true),
				mckRouter.EXPECT().Unregister(uaid),
				prevSocket.EXPECT().Close(),
				mckStore.EXPECT().Exists(uaid).Return(true),
				mckBalancer.EXPECT().RedirectURL().Return("", false, nil),
				mckRouter.EXPECT().Register(uaid).Return(nil),
				mckSocket.EXPECT().WriteText(gomock.Any()).Return(nil),
				mckStat.EXPECT().Increment("updates.client.hello"),
				mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
				mckStat.EXPECT().Timer("client.flush", gomock.Any()),
			)

			err = curWorker.Hello(&RequestHeader{Type: "hello"}, []byte(
				`{"uaid":"1b156db9cda04b59ae6f85d229628306","channelIDs":["1"]}`))
			So(err, ShouldBeNil)

			So(curWorker.UAID(), ShouldEqual, uaid)
			worker, workerConnected := app.GetWorker(uaid)
			So(workerConnected, ShouldBeTrue)
			So(worker, ShouldEqual, curWorker)
		})
	})
}

// Test that harmless errors are harmless
func TestHarmlessConnectionError(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Not(ERROR), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
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
	Convey("Should log run loop errors", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)

		wws := NewWorker(app, mckSocket, "test")

		Convey("Harmless socket errors should not be logged", func() {
			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return(nil,
					errors.New("read tcp YYYYYYYYYYYY:YYYYY: connection timed out")),
			)
			wws.Run()
		})
		Convey("Unknown socket errors should be logged", func() {
			gomock.InOrder(
				mckSocket.EXPECT().SetReadDeadline(gomock.Any()),
				mckSocket.EXPECT().ReadBinary().Return(nil,
					errors.New("universe has imploded")),
				mckLogger.EXPECT().Log(ERROR, gomock.Any(),
					gomock.Any(), gomock.Any()),
			)
			wws.Run()
		})
	})
}
