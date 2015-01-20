/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/aes"
	"errors"
	"testing"
	"time"

	"github.com/rafrombrc/gomock/gomock"
	. "github.com/smartystreets/goconvey/convey"
)

func TestServerHello(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckPinger := NewMockPropPinger(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	pingData := []byte(`{"regid":123}`)

	Convey("Should handle connecting clients", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetPropPinger(mckPinger)
		app.SetRouter(mckRouter)

		srv := NewServer()
		if err := srv.Init(app, srv.ConfigStruct()); err != nil {
			t.Fatalf("Error initializing server: %s", err)
		}
		app.SetServer(srv)

		Convey("Should register with the proprietary pinger", func() {
			uaid := "c4fe17154cd74500ad1d51f2955fd79c"
			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckPinger.EXPECT().Register(uaid, pingData),
				mckRouter.EXPECT().Register(uaid),
			)
			err := srv.Hello(mckWorker, pingData)
			So(err, ShouldBeNil)

			w, workerConnected := app.GetWorker(uaid)
			So(workerConnected, ShouldBeTrue)
			So(w, ShouldResemble, mckWorker)
		})

		Convey("Should not fail if pinger registration fails", func() {
			uaid := "3529f588b03e411295b8df6d38e63ce7"

			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckPinger.EXPECT().Register(uaid, pingData).Return(errors.New(
					"external system on fire")),
				mckRouter.EXPECT().Register(uaid),
			)

			err := srv.Hello(mckWorker, pingData)
			So(err, ShouldBeNil)
			So(app.WorkerExists(uaid), ShouldBeTrue)
		})
	})
}

func TestServerBye(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStore := NewMockStore(mockCtrl)
	mckPinger := NewMockPropPinger(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	Convey("Should clean up after disconnected clients", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetStore(mckStore)
		app.SetPropPinger(mckPinger)
		app.SetRouter(mckRouter)

		srv := NewServer()
		if err := srv.Init(app, srv.ConfigStruct()); err != nil {
			t.Fatalf("Error initializing server: %s", err)
		}
		app.SetServer(srv)

		Convey("Should remove the client from the map", func() {
			var err error
			uaid := "0cd9b0990bb749eb808206924e40a323"
			prevWorker := NewMockWorker(mockCtrl)
			prevWorker.EXPECT().Born().AnyTimes()
			app.AddWorker(uaid, prevWorker)
			So(app.WorkerExists(uaid), ShouldBeTrue)

			gomock.InOrder(
				prevWorker.EXPECT().UAID().Return(uaid),
				mckRouter.EXPECT().Unregister(uaid),
				prevWorker.EXPECT().Close(),
			)
			err = srv.Bye(prevWorker)
			So(err, ShouldBeNil)
			So(app.WorkerExists(uaid), ShouldBeFalse)

			app.AddWorker(uaid, mckWorker)

			gomock.InOrder(
				prevWorker.EXPECT().UAID().Return(uaid),
				prevWorker.EXPECT().Close(),
			)
			err = srv.Bye(prevWorker)
			So(err, ShouldBeNil)
			So(app.WorkerExists(uaid), ShouldBeTrue)
		})
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
	mckWorker := NewMockWorker(mockCtrl)

	uaid := "480ce74851d04104bfe11204c020ee81"
	chid := "5b0ae7e9de7f42529a361e3bfe318142"

	Convey("Should set up channels", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetStore(mckStore)
		app.SetPropPinger(mckPinger)
		app.SetRouter(mckRouter)
		app.SetEndpointHandler(mckEndHandler)

		Convey("Should reject invalid IDs", func() {
			srv := NewServer()
			if err := srv.Init(app, srv.ConfigStruct()); err != nil {
				t.Fatalf("Error initializing server: %s", err)
			}
			app.SetServer(srv)

			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckStore.EXPECT().IDsToKey(uaid, chid).Return("", ErrInvalidKey),
			)

			_, err := srv.Regis(mckWorker, chid)
			So(err, ShouldEqual, ErrInvalidKey)
		})

		Convey("Should not encrypt endpoints without a key", func() {
			app.SetTokenKey("")
			srv := NewServer()
			if err := srv.Init(app, srv.ConfigStruct()); err != nil {
				t.Fatalf("Error initializing server: %s", err)
			}
			app.SetServer(srv)

			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckStore.EXPECT().IDsToKey(uaid, chid).Return("abc", nil),
				mckEndHandler.EXPECT().URL().Return("https://example.com"),
			)

			endpoint, err := srv.Regis(mckWorker, chid)
			So(err, ShouldBeNil)
			So(endpoint, ShouldEqual, "https://example.com/update/abc")
		})

		Convey("Should encrypt endpoints with a key", func() {
			app.SetTokenKey("HVozKz_n-DPopP5W877DpRKQOW_dylVf")
			srv := NewServer()
			if err := srv.Init(app, srv.ConfigStruct()); err != nil {
				t.Fatalf("Error initializing server: %s", err)
			}
			app.SetServer(srv)

			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckStore.EXPECT().IDsToKey(uaid, chid).Return("456", nil),
				mckEndHandler.EXPECT().URL().Return("https://example.org"),
			)

			endpoint, err := srv.Regis(mckWorker, chid)
			So(err, ShouldBeNil)
			So(endpoint, ShouldEqual,
				"https://example.org/update/AAECAwQFBgcICQoLDA0OD3afbw==")
		})

		Convey("Should reject invalid keys", func() {
			app.SetTokenKey("lLyhlLk8qus1ky4ER8yjN5o=")
			srv := NewServer()
			if err := srv.Init(app, srv.ConfigStruct()); err != nil {
				t.Fatalf("Error initializing server: %s", err)
			}
			app.SetServer(srv)

			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckStore.EXPECT().IDsToKey(uaid, chid).Return("123", nil),
			)

			_, err := srv.Regis(mckWorker, chid)
			So(err, ShouldEqual, aes.KeySizeError(17))
		})
	})
}

func TestRequestFlush(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckPinger := NewMockPropPinger(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	uaid := "41085ed1e5474ec9aa6ddef595b1bb6f"

	Convey("Should flush updates to the client", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetPropPinger(mckPinger)

		srv := NewServer()
		if err := srv.Init(app, srv.ConfigStruct()); err != nil {
			t.Fatalf("Error initializing server: %s", err)
		}
		app.SetServer(srv)

		Convey("Should allow nil clients", func() {
			err := srv.RequestFlush(nil, "", 0, "")
			So(err, ShouldBeNil)
		})

		Convey("Should flush to the underlying worker", func() {
			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckWorker.EXPECT().Flush(int64(0), "", int64(0), "").Return(nil),
			)
			err := srv.RequestFlush(mckWorker, "", 0, "")
			So(err, ShouldBeNil)
		})

		Convey("Should send a proprietary ping if flush panics", func() {
			flushErr := errors.New("universe has imploded")
			flushPanic := func(int64, string, int64, string) error {
				panic(flushErr)
				return nil
			}
			chid := "41d1a3a6517b47d5a4aaabd82ae5f3ba"
			version := int64(3)
			data := "Unfortunately, as you probably already know, people"

			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckWorker.EXPECT().Flush(int64(0), chid, version, data).Do(flushPanic),
				mckPinger.EXPECT().Send(uaid, version, data),
			)

			err := srv.RequestFlush(mckWorker, chid, version, data)
			So(err, ShouldEqual, flushErr)
		})
	})
}

func TestUpdateClient(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStore := NewMockStore(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	uaid := "e3cbb350304b4327a069d5c07fc434b8"
	chid := "d468168f24cc4ca8bae3b07530559be6"
	version := int64(3)
	data := "This is a test of the emergency broadcasting system."

	Convey("Should update storage and flush updates", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetStore(mckStore)

		srv := NewServer()
		if err := srv.Init(app, srv.ConfigStruct()); err != nil {
			t.Fatalf("Error initializing server: %s", err)
		}
		app.SetServer(srv)

		Convey("Should flush updates to the worker", func() {
			mckWorker.EXPECT().UAID().Return(uaid).Times(2)
			gomock.InOrder(
				mckStore.EXPECT().Update(uaid, chid, version).Return(nil),
				mckWorker.EXPECT().Flush(int64(0), chid, version, data),
			)
			err := srv.UpdateWorker(mckWorker, chid, version, time.Time{}, data)
			So(err, ShouldBeNil)
		})

		Convey("Should fail if storage is unavailable", func() {
			updateErr := errors.New("omg, everything is exploding")
			gomock.InOrder(
				mckWorker.EXPECT().UAID().Return(uaid),
				mckStore.EXPECT().Update(uaid, chid, version).Return(updateErr),
			)
			err := srv.UpdateWorker(mckWorker, chid, version, time.Time{}, data)
			So(err, ShouldEqual, updateErr)
		})

		Convey("Should fail if the worker returns an error", func() {
			flushErr := errors.New("cannot brew coffee with a teapot")
			mckWorker.EXPECT().UAID().Return(uaid).Times(2)
			gomock.InOrder(
				mckStore.EXPECT().Update(uaid, chid, version).Return(nil),
				mckWorker.EXPECT().Flush(int64(0), chid, version, data).Return(flushErr),
			)
			err := srv.UpdateWorker(mckWorker, chid, version, time.Time{}, data)
			So(err, ShouldEqual, flushErr)
		})
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
