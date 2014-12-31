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

func TestDeliver(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	app := NewApplication()
	app.hostname = "test"
	app.clientMinPing = 10 * time.Second
	app.clientHelloTimeout = 10 * time.Second
	app.pushLongPongs = true

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	app.SetLogger(mckLogger)

	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	app.SetMetrics(mckStat)

	mckStore := NewMockStore(mockCtrl)
	app.SetStore(mckStore)

	mckRouter := NewMockRouter(mockCtrl)
	app.SetRouter(mckRouter)

	srv := new(Serv)
	srv.Init(app, srv.ConfigStruct())
	app.SetServer(srv)

	eh := NewEndpointHandler()
	ehConf := eh.ConfigStruct().(*EndpointHandlerConfig)
	ehConf.AlwaysRoute = true
	ehConf.Listener.Addr = ""
	eh.Init(app, ehConf)
	app.SetEndpointHandler(eh)

	Convey("Should route messages if the device is not connected", t, func() {
		// ...
	})

	Convey("Should route messages if `AlwaysRoute` is enabled", t, func() {
		uaid := "6952a68e-e0e7-444e-bc54-f935c4444b13"
		mockWorker := NewMockWorker(mockCtrl)
		pws := &PushWS{}
		client := &Client{mockWorker, pws, uaid}
		app.AddClient(uaid, client)

		chid := "b7ede546-585f-4cc9-b95e-9340e3406951"
		logID := "debc7e71-a6ac-4d17-8729-79999c7034f6"
		version := int64(1)
		data := "Happy, happy, joy, joy!"

		mckStat.EXPECT().Increment("updates.routed.outgoing").Times(1)
		mckRouter.EXPECT().Route(nil, uaid, chid, version, gomock.Any(), logID, data).Return(false, nil).Times(1)
		mockWorker.EXPECT().Flush(pws, int64(0), chid, version, data).Return(nil).Times(1)
		mckStat.EXPECT().Increment("updates.appserver.received").Times(1)

		ok := eh.deliver(nil, uaid, chid, version, logID, data)
		So(ok, ShouldBeTrue)
	})
}
