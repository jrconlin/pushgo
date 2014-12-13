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

		mckLocator := NewMockLocator(mockCtrl)

		router := NewBroadcastRouter()
		defaultConfig := router.ConfigStruct()
		conf := defaultConfig.(*BroadcastRouterConfig)
		conf.Listener.Addr = ":8000"
		router.Init(app, conf)
		app.SetRouter(router)

		router.SetLocator(mckLocator)
		cancelSignal := make(chan bool)
		/*
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
		*/
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

			gomock.InOrder(
				mckLocator.EXPECT().Contacts(gomock.Any()).Return(thisNodeList, nil),
				mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true),
				mckLogger.EXPECT().Log(gomock.Any(), "router",
					"Fetched contact list from discovery service", gomock.Any()),
				mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true),
				mckLogger.EXPECT().Log(gomock.Any(), "router",
					"Sending push...", gomock.Any()),
				mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true),
				mckLogger.EXPECT().Log(gomock.Any(), "router",
					"Sending request", gomock.Any()),
				// At this point the handler is running
				mckLogger.EXPECT().ShouldLog(WARNING).Return(true),
				// We log http responses, so this will be called next
				mckLogger.EXPECT().ShouldLog(INFO).Return(true),
				mckLogger.EXPECT().Log(gomock.Any(), "http",
					gomock.Any(), gomock.Any()),
				mckStat.EXPECT().Increment("updates.routed.incoming"),
			)
			err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
			So(err, ShouldBeNil)

			mckLocator.EXPECT().Close()
			router.Close()

		})
	})
}
