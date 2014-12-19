/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/mozilla-services/pushgo/client"
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

	router.SetLocator(mckLocator)

	srv := new(Serv)
	defSrvConfig := srv.ConfigStruct()
	srv.Init(app, defSrvConfig)
	app.SetServer(srv)

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
		err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
		So(err, ShouldBeNil)
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
		err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
		So(err, ShouldEqual, myErr)
	})

	Convey("Should succeed self-routing to a valid uaid", t, func() {
		mockWorker := NewMockWorker(mockCtrl)
		client := &Client{mockWorker, &PushWS{}, uaid}
		app.AddClient(uaid, client)

		thisNode := router.URL()
		thisNodeList := []string{thisNode}

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
		mckStat.EXPECT().Increment("router.dial.success").AnyTimes()
		mckStat.EXPECT().Increment("router.dial.error").AnyTimes()
		mckStat.EXPECT().Increment("router.broadcast.hit")
		mckStat.EXPECT().Timer("updates.routed.hits", gomock.Any())
		mckStat.EXPECT().Timer("router.handled", gomock.Any())

		err := router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
		So(err, ShouldBeNil)
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

	router.SetLocator(mckLocator)

	srv := new(Serv)
	defSrvConfig := srv.ConfigStruct()
	mckStat.EXPECT().Gauge("update.client.connections", gomock.Any()).AnyTimes()
	srv.Init(app, defSrvConfig)
	app.SetServer(srv)

	cancelSignal := make(chan bool)

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

	for i := 0; i < b.N; i++ {
		mckLocator.EXPECT().Contacts(gomock.Any()).Return(thisNodeList, nil)
		mckStat.EXPECT().Increment(gomock.Any()).AnyTimes()
		mckStat.EXPECT().Gauge(gomock.Any(), gomock.Any()).AnyTimes()
		mckStat.EXPECT().Timer(gomock.Any(), gomock.Any()).AnyTimes()
		mckStore.EXPECT().IDsToKey(gomock.Any(), gomock.Any()).Return("", true)
		mckStore.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
		mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
		mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).AnyTimes()
		mockWorker.EXPECT().Flush(gomock.Any(), gomock.Any(), chid, version,
			"").Return(nil)

		router.Route(cancelSignal, uaid, chid, version, sentAt, "", "")
	}

	mckLocator.EXPECT().Close()
	router.Close()
}

func TestBroadcastStaticLocator(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping router test in short mode")
	}

	cServer := &TestServer{Name: "c", LogLevel: 0}
	cApp, cConn, err := cServer.Dial()
	if err != nil {
		t.Fatalf("Error starting test application %q: %s", cServer.Name, err)
	}
	defer cApp.Stop()
	defer cConn.Close()
	defer cConn.Purge()

	bServer := &TestServer{
		Name:     "b",
		LogLevel: 0,
		Contacts: []string{cApp.Router().URL()},
	}
	bApp, bConn, err := bServer.Dial()
	if err != nil {
		t.Fatalf("Error starting test application %q: %s", bServer.Name, err)
	}
	defer bServer.Stop()
	defer bConn.Close()
	defer bConn.Purge()

	aServer := &TestServer{
		Name:     "a",
		LogLevel: 0,
		Contacts: []string{
			cApp.Router().URL(),
			bApp.Router().URL(),
		},
	}
	aApp, aConn, err := aServer.Dial()
	if err != nil {
		t.Fatalf("Error starting test application %q: %s", aServer.Name, err)
	}
	defer aServer.Stop()
	defer aConn.Close()
	defer aConn.Purge()

	// Subscribe to a channel on c.
	cChan, cEndpoint, err := cConn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %s", err)
	}

	stopChan := make(chan bool)
	defer close(stopChan)
	errChan := make(chan error)
	notifyApp := func(app *Application, channelId, endpoint string, count int) {
		uri, err := url.Parse(endpoint)
		if err != nil {
			select {
			case <-stopChan:
			case errChan <- err:
			}
			return
		}
		uri.Host = app.EndpointHandlers().Listener().Addr().String()
		for i := 1; i <= count; i++ {
			if err = client.Notify(uri.String(), int64(i)); err != nil {
				break
			}
		}
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}

	// Wait for updates on c.
	go func() {
		var err error
		for i := 0; i < 10; i++ {
			var updates []client.Update
			if updates, err = cConn.ReadBatch(); err != nil {
				break
			}
			if err = cConn.AcceptBatch(updates); err != nil {
				break
			}
		}
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}()

	// Send an update via a.
	go notifyApp(aApp, cChan, cEndpoint, 5)

	// Send an update via b.
	go notifyApp(bApp, cChan, cEndpoint, 5)

	for i := 0; i < 3; i++ {
		if err := <-errChan; err != nil {
			t.Fatal(err)
		}
	}
}
