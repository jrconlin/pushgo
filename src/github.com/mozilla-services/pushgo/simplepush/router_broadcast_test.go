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

func TestBroadcastRouter(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	uaid := "2130ac71-6f04-47cf-b7dc-2570ba1d2afe"
	chid := "90662645-a7b5-4dfe-8105-a290553507e4"
	version := int64(10)
	sentAt := time.Now()

	app := NewApplication()
	appConfig := app.ConfigStruct()
	app.Init(nil, appConfig)

	mckLogger := NewMockLogger(mockCtrl)
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckLocator := NewMockLocator(mockCtrl)

	router := NewBroadcastRouter()
	defaultConfig := router.ConfigStruct()
	conf := defaultConfig.(*BroadcastRouterConfig)
	conf.Listener.Addr = ""
	router.Init(app, conf)
	app.SetRouter(router)

	app.SetLocator(mckLocator)

	cancelSignal := make(chan bool)

	errChan := make(chan error, 10)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true)
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
	go router.Start(errChan)

	mckStat.EXPECT().Increment("router.socket.connect").AnyTimes()
	mckStat.EXPECT().Increment("router.socket.disconnect").AnyTimes()

	Convey("Should fail to route a non-existent uaid", t, func() {

		mckLocator.EXPECT().Contacts(gomock.Any()).Return([]string{}, nil)
		mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).Times(2)
		mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
			gomock.Any(), gomock.Any()).Times(2)
		mckStat.EXPECT().Increment("router.dial.success").AnyTimes()
		mckStat.EXPECT().Increment("router.dial.error").AnyTimes()
		mckStat.EXPECT().Increment("router.broadcast.miss").Times(1)
		mckStat.EXPECT().Timer(gomock.Any(), gomock.Any()).Times(2)
		ok, err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
		So(err, ShouldBeNil)
		So(ok, ShouldBeFalse)
	})

	Convey("Should fail to route if contacts errors", t, func() {
		myErr := errors.New("Oops")
		mckLocator.EXPECT().Contacts(gomock.Any()).Return([]string{}, myErr)
		mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).Times(2)
		mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
			gomock.Any(), gomock.Any()).Times(2)
		mckStat.EXPECT().Increment("router.dial.success").AnyTimes()
		mckStat.EXPECT().Increment("router.dial.error").AnyTimes()
		mckStat.EXPECT().Increment("router.broadcast.error").Times(1)
		ok, err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
		So(err, ShouldEqual, myErr)
		So(ok, ShouldBeFalse)
	})

	Convey("Should succeed self-routing to a valid uaid", t, func() {
		mockWorker := NewMockWorker(mockCtrl)
		app.AddWorker(uaid, mockWorker)

		thisNode := router.URL()
		thisNodeList := []string{thisNode}

		mckLocator.EXPECT().Contacts(gomock.Any()).Return(thisNodeList, nil)
		mckStat.EXPECT().Increment("updates.routed.incoming")
		mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
		mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).AnyTimes()
		mockWorker.EXPECT().Send(chid, version, "").Return(nil)
		mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
		mckStat.EXPECT().Increment("updates.routed.received")
		mckStat.EXPECT().Increment("router.dial.success").AnyTimes()
		mckStat.EXPECT().Increment("router.dial.error").AnyTimes()
		mckStat.EXPECT().Increment("router.broadcast.hit")
		mckStat.EXPECT().Timer("updates.routed.hits", gomock.Any())
		mckStat.EXPECT().Timer("router.handled", gomock.Any())

		ok, err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)
	})

	mckLocator.EXPECT().Close()
	router.Close()
}

func BenchmarkRouter(b *testing.B) {
	mockCtrl := gomock.NewController(b)
	defer mockCtrl.Finish()

	uaid := "2130ac71-6f04-47cf-b7dc-2570ba1d2afe"
	chid := "90662645-a7b5-4dfe-8105-a290553507e4"
	version := int64(10)
	sentAt := time.Now()

	app := NewApplication()
	appConfig := app.ConfigStruct()
	app.Init(nil, appConfig)

	mckLogger := NewMockLogger(mockCtrl)
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckLocator := NewMockLocator(mockCtrl)

	router := NewBroadcastRouter()
	defaultConfig := router.ConfigStruct()
	conf := defaultConfig.(*BroadcastRouterConfig)
	conf.Listener.Addr = ""
	router.Init(app, conf)
	app.SetRouter(router)

	app.SetLocator(mckLocator)

	cancelSignal := make(chan bool)

	mockWorker := NewMockWorker(mockCtrl)
	app.AddWorker(uaid, mockWorker)

	thisNode := router.URL()
	thisNodeList := []string{thisNode}

	errChan := make(chan error, 10)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true)
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
	go router.Start(errChan)
	<-time.After(time.Duration(1) * time.Second)

	for i := 0; i < b.N; i++ {
		mckLocator.EXPECT().Contacts(gomock.Any()).Return(thisNodeList, nil)
		mckStat.EXPECT().Increment(gomock.Any()).AnyTimes()
		mckStat.EXPECT().Gauge(gomock.Any(), gomock.Any()).AnyTimes()
		mckStat.EXPECT().Timer(gomock.Any(), gomock.Any()).AnyTimes()
		mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
		mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).AnyTimes()
		mockWorker.EXPECT().Send(chid, version, "").Return(nil)

		router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
	}

	mckLocator.EXPECT().Close()
	router.Close()
}
