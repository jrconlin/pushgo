/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
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

	Convey("With a fresh Application and BroadCastRouter", t, func() {
		app := new(Application)
		appConfig := app.ConfigStruct()
		err := app.Init(nil, appConfig)
		So(err, ShouldBeNil)

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
		conf.Listener.Addr = ":8000"
		router.Init(app, conf)
		app.SetRouter(router)

		router.SetLocator(mckLocator)

		srv := new(Serv)
		defSrvConfig := srv.ConfigStruct()
		srvConfig := defSrvConfig.(*ServerConfig)

		srvConfig.Client.Addr = ":10990"
		srvConfig.Endpoint.Addr = ":10899"
		srv.Init(app, defSrvConfig)
		app.SetServer(srv)

		cancelSignal := make(chan bool)

		Convey("Should fail to route a non-existent uaid", func() {

			errChan := make(chan error, 10)
			mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true)
			mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
			go router.Start(errChan)

			mckLocator.EXPECT().Contacts(gomock.Any()).Return([]string{}, nil)
			mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).Times(2)
			mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
				gomock.Any(), gomock.Any()).Times(2)
			mckStat.EXPECT().Increment("router.broadcast.miss").Times(1)
			mckStat.EXPECT().Timer(gomock.Any(), gomock.Any()).Times(2)
			err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
			So(err, ShouldBeNil)

			mckLocator.EXPECT().Close()
			router.Close()
		})
	})
}

func TestBroadcastRouterValid(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	uaid := "2130ac71-6f04-47cf-b7dc-2570ba1d2afe"
	chid := "90662645-a7b5-4dfe-8105-a290553507e4"
	version := int64(10)
	sentAt := time.Now()

	Convey("With a fresh Application and BroadCastRouter", t, func() {
		app := new(Application)
		appConfig := app.ConfigStruct()
		err := app.Init(nil, appConfig)
		So(err, ShouldBeNil)

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
		conf.Listener.Addr = ":8200"
		router.Init(app, conf)
		app.SetRouter(router)

		router.SetLocator(mckLocator)

		srv := new(Serv)
		defSrvConfig := srv.ConfigStruct()
		srvConfig := defSrvConfig.(*ServerConfig)

		srvConfig.Client.Addr = ":11990"
		srvConfig.Endpoint.Addr = ":11899"
		srv.Init(app, defSrvConfig)
		app.SetServer(srv)

		cancelSignal := make(chan bool)

		Convey("Should succeed self-routing to a valid uaid", func() {
			mockWorker := NewMockWorker(mockCtrl)
			client := &Client{mockWorker, &PushWS{}, uaid}
			app.AddClient(uaid, client)

			thisNode := router.URL()
			thisNodeList := []string{thisNode}

			errChan := make(chan error, 10)
			mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true)
			mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
			go router.Start(errChan)
			<-time.After(time.Duration(1) * time.Second)

			mckLocator.EXPECT().Contacts(gomock.Any()).Return(thisNodeList, nil)
			mckStat.EXPECT().Increment("updates.routed.incoming")
			mckStore.EXPECT().IDsToKey(gomock.Any(), gomock.Any()).Return("", true)
			mckStore.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
			mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
			mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
				gomock.Any()).AnyTimes()
			mockWorker.EXPECT().Flush(gomock.Any(), gomock.Any(), chid, version,
				"").Return(nil)
			mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
			mckStat.EXPECT().Increment("updates.routed.received")
			mckStat.EXPECT().Increment("router.broadcast.hit")
			mckStat.EXPECT().Timer("updates.routed.hits", gomock.Any())
			mckStat.EXPECT().Timer("router.handled", gomock.Any())

			err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
			So(err, ShouldBeNil)

			mckLocator.EXPECT().Close()
			router.Close()
		})
	})
}
