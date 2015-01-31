/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rafrombrc/gomock/gomock"
	"golang.org/x/net/websocket"
)

// newMockReadyNotifier wraps l in a mockReadyNotifier.
func newMockReadyNotifier(l Locator) *mockReadyNotifier {
	return &mockReadyNotifier{Locator: l, readyChan: make(chan bool)}
}

// mockReadyNotifier implements the Locator and ReadyNotifier interfaces. This
// is used to test blocking incoming WebSocket connections until the locator is
// ready.
type mockReadyNotifier struct {
	Locator
	readyChan chan bool
}

// SignalReady signals that the locator is ready.
func (m *mockReadyNotifier) SignalReady() { close(m.readyChan) }

// ReadyNotify returns a channel that is closed by SignalReady.
func (m *mockReadyNotifier) ReadyNotify() <-chan bool { return m.readyChan }

func TestLocatorReadyNotify(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	uaid := "fce61180716a40ed8e79bf5ff0ba34bc"

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()

	var (
		// routerPipes maps fake peer addresses to their respective pipes. Used
		// by dialRouter to connect to peers.
		routerPipes = make(map[netAddr]*pipeListener)

		// contacts is a list of peer URLs for the locator.
		contacts []string
	)

	// Fake listener for the sender's router, used to test self-routing.
	sndRouterAddr := netAddr{"tcp", "snd-router.example.com:3000"}
	sndRouterPipe := newPipeListener()
	defer sndRouterPipe.Close()
	routerPipes[sndRouterAddr] = sndRouterPipe
	contacts = append(contacts, "http://snd-router.example.com:3000")

	// Fake listener for the receiver's router, used to test routing updates
	// to different hosts.
	recvRouterAddr := netAddr{"tcp", "recv-router.example.com:3000"}
	recvRouterPipe := newPipeListener()
	defer recvRouterPipe.Close()
	routerPipes[recvRouterAddr] = recvRouterPipe
	contacts = append(contacts, "http://recv-router.example.com:3000")

	// Fake listener for the receiver's WebSocket handler, used to accept a
	// WebSocket client connection.
	socketHandlerPipe := newPipeListener()
	defer socketHandlerPipe.Close()

	// Fake locator.
	mckLocator := NewMockLocator(mockCtrl)
	mckLocator.EXPECT().Contacts(uaid).Return(contacts, nil).Times(2)

	// Fake dialer to connect to each peer's routing listener.
	dialRouter := func(network, address string) (net.Conn, error) {
		if pipe, ok := routerPipes[netAddr{network, address}]; ok {
			return pipe.Dial(network, address)
		}
		return nil, &netErr{temporary: false, timeout: false}
	}
	// Configures a fake router for the app.
	setRouter := func(app *Application, listener net.Listener) {
		r := NewBroadcastRouter()
		r.setApp(app)
		r.setClientOptions(10, 3*time.Second, 3*time.Second) // Defaults.
		r.setClientTransport(&http.Transport{Dial: dialRouter})
		r.listenWithConfig(listenerConfig{listener: listener})
		r.maxDataLen = 4096
		r.server = newServeWaiter(&http.Server{Handler: r.ServeMux()})
		app.SetRouter(r)
	}

	// sndApp is the server broadcasting the update. The locator returns the
	// addresses of the sender and receiver to test self-routing.
	sndApp := NewApplication()
	sndApp.SetLogger(mckLogger)
	sndStat := NewMockStatistician(mockCtrl)
	sndApp.SetMetrics(sndStat)
	sndApp.SetLocator(mckLocator)
	// Set up a fake router for the sender.
	setRouter(sndApp, sndRouterPipe)

	// recvApp is the server receiving the update.
	recvApp := NewApplication()
	recvApp.SetLogger(mckLogger)
	recvStat := NewMockStatistician(mockCtrl)
	recvApp.SetMetrics(recvStat)
	recvStore := NewMockStore(mockCtrl)
	recvApp.SetStore(recvStore)
	// Wrap the fake locator in a type that implements ReadyNotifier.
	recvLocator := newMockReadyNotifier(mckLocator)
	recvApp.SetLocator(recvLocator)
	// Set up a fake WebSocket handler for the receiver.
	recvSocketHandler := NewSocketHandler()
	recvSocketHandler.setApp(recvApp)
	recvSocketHandler.listenWithConfig(listenerConfig{
		listener: socketHandlerPipe})
	recvSocketHandler.server = newServeWaiter(&http.Server{Handler: recvSocketHandler.ServeMux()})
	recvApp.SetSocketHandler(recvSocketHandler)
	// Set up a fake router for the receiver.
	setRouter(recvApp, recvRouterPipe)

	chid := "2b7c5c27d6224bfeaf1c158c3c57fca3"
	version := int64(2)
	data := "I'm a little teapot, short and stout."

	var wg sync.WaitGroup // Waits for the client to close.
	wg.Add(1)
	dialChan := make(chan bool) // Signals when the client connects.
	timeout := closeAfter(2 * time.Second)

	go func() {
		defer wg.Done()
		origin := &url.URL{Scheme: "ws", Host: "recv-conn.example.com"}
		ws, err := dialSocketListener(socketHandlerPipe, &websocket.Config{
			Location: origin,
			Origin:   origin,
			Version:  websocket.ProtocolVersionHybi13,
		})
		if err != nil {
			t.Errorf("Error dialing host: %s", err)
			return
		}
		defer ws.Close()
		err = websocket.JSON.Send(ws, struct {
			Type       string   `json:"messageType"`
			DeviceID   string   `json:"uaid"`
			ChannelIDs []string `json:"channelIDs"`
		}{"hello", uaid, []string{}})
		if err != nil {
			t.Errorf("Error writing handshake request: %s", err)
			return
		}
		helloReply := new(HelloReply)
		if err = websocket.JSON.Receive(ws, helloReply); err != nil {
			t.Errorf("Error reading handshake reply: %s", err)
			return
		}
		select {
		case dialChan <- true:
		case <-timeout:
			t.Errorf("Timed out waiting for router")
			return
		}
		flushReply := new(FlushReply)
		if err = websocket.JSON.Receive(ws, flushReply); err != nil {
			t.Errorf("Error reading routed update: %s", err)
			return
		}
		ok := false
		expected := Update{chid, uint64(version), data}
		for _, update := range flushReply.Updates {
			if ok = update == expected; ok {
				break
			}
		}
		if !ok {
			t.Errorf("Missing update %#v in %#v", expected, flushReply.Updates)
			return
		}
	}()

	// Start the handlers.
	errChan := make(chan error, 3)
	go sndApp.Router().Start(errChan)
	go recvApp.SocketHandler().Start(errChan)
	go recvApp.Router().Start(errChan)

	// First and second routing attempts to self.
	sndStat.EXPECT().Increment("updates.routed.unknown").Times(2)
	// First routing attempt to peer.
	sndStat.EXPECT().Increment("router.broadcast.miss")
	sndStat.EXPECT().Timer("updates.routed.misses", gomock.Any())
	sndStat.EXPECT().Timer("router.handled", gomock.Any())
	// Second routing attempt to peer.
	sndStat.EXPECT().Increment("router.broadcast.hit")
	sndStat.EXPECT().Timer("updates.routed.hits", gomock.Any())
	sndStat.EXPECT().Timer("router.handled", gomock.Any())

	// Initial routing attempt to peer.
	recvStat.EXPECT().Increment("updates.routed.unknown")
	// Client connects to peer.
	recvStat.EXPECT().Increment("client.socket.connect")
	recvStore.EXPECT().CanStore(0).Return(true)
	recvStat.EXPECT().Increment("updates.client.hello")
	recvStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil)
	recvStat.EXPECT().Timer("client.flush", gomock.Any())
	// Second routing attempt to peer.
	recvStat.EXPECT().Increment("updates.routed.incoming")
	recvStat.EXPECT().Increment("updates.sent")
	recvStat.EXPECT().Timer("client.flush", gomock.Any())
	recvStat.EXPECT().Increment("updates.routed.received")
	recvStat.EXPECT().Timer("client.socket.lifespan", gomock.Any())
	recvStat.EXPECT().Increment("client.socket.disconnect")

	// Initial routing attempt should fail; the WebSocket listener shouldn't
	// accept client connections before the locator is ready.
	delivered, err := sndApp.Router().Route(nil, uaid, chid, version, timeNow(),
		"disconnected", data)
	if err != nil {
		t.Errorf("Error routing to disconnected client: %s", err)
	} else if delivered {
		t.Error("Should not route to disconnected client")
	}
	// Signal the locator is ready, then wait for the client to connect.
	recvLocator.SignalReady()
	select {
	case <-dialChan:
	case <-time.After(5 * time.Second):
		t.Fatalf("Timed out waiting for the client to connect")
	}
	// Routing should succeed once the client is connected.
	delivered, err = sndApp.Router().Route(nil, uaid, chid, version, timeNow(),
		"connected", data)
	if err != nil {
		t.Errorf("Error routing to connected client: %s", err)
	} else if !delivered {
		t.Error("Should route to connected client")
	}

	mckLocator.EXPECT().Close().Times(2)
	if err := recvApp.Close(); err != nil {
		t.Errorf("Error closing peer: %s", err)
	}
	wg.Wait()
	if err := sndApp.Close(); err != nil {
		t.Errorf("Error closing self: %s", err)
	}
	// Wait for the handlers to stop.
	for i := 0; i < 3; i++ {
		<-errChan
	}
}
