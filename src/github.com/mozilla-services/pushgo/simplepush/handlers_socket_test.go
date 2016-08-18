/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/rafrombrc/gomock/gomock"
	"golang.org/x/net/websocket"
)

// listenerConfig implements ListenerConfig.
type listenerConfig struct {
	listener net.Listener
	useTLS   bool
	maxConns int
}

func (conf listenerConfig) UseTLS() bool     { return conf.useTLS }
func (conf listenerConfig) GetMaxConns() int { return conf.maxConns }
func (conf listenerConfig) Listen() (net.Listener, error) {
	return conf.listener, nil
}

// dialSocketListener opens a WebSocket client connection to pl with config.
// config.Location, config.Origin, and config.Version must be set.
func dialSocketListener(pl *pipeListener, config *websocket.Config) (
	conn *websocket.Conn, err error) {

	socket, err := pl.Dial("", "")
	if err != nil {
		return nil, err
	}
	if conn, err = websocket.NewClient(config, socket); err != nil {
		socket.Close()
		return nil, err
	}
	return conn, nil
}

func TestSocketListenConfig(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckListenerConfig := NewMockListenerConfig(mockCtrl)

	app := NewApplication()
	app.hostname = "example.org"
	app.SetLogger(mckLogger)
	app.SetMetrics(mckStat)

	sh := NewSocketHandler()
	sh.setApp(app)

	// Should forward Listen errors.
	listenErr := errors.New("splines not reticulated")
	mckListenerConfig.EXPECT().Listen().Return(nil, listenErr)
	if err := sh.listenWithConfig(mckListenerConfig); err != listenErr {
		t.Errorf("Wrong error: got %#v; want %#v", err, listenErr)
	}

	// Should use the wss:// scheme if UseTLS returns true.
	ml := newMockListener(netAddr{"test", "[::1]:8080"})
	gomock.InOrder(
		mckListenerConfig.EXPECT().Listen().Return(ml, nil),
		mckListenerConfig.EXPECT().UseTLS().Return(true),
		mckListenerConfig.EXPECT().GetMaxConns().Return(1),
	)
	if err := sh.listenWithConfig(mckListenerConfig); err != nil {
		t.Errorf("Error setting listener: %s", err)
	}
	if maxConns := sh.MaxConns(); maxConns != 1 {
		t.Errorf("Mismatched maximum connection count: got %d; want 1",
			maxConns)
	}
	expectedURL := "wss://example.org:8080"
	if url := sh.URL(); url != expectedURL {
		t.Errorf("Mismatched handler URL: got %q; want %q",
			url, expectedURL)
	}
}

func TestSocketListenerConfig(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)

	tests := []struct {
		name     string
		hostname string
		conf     listenerConfig
		url      string
		ok       bool
	}{
		{
			name:     "Invalid address, TLS, default hostname",
			hostname: "example.com",
			conf: listenerConfig{
				listener: newMockListener(netAddr{"test", "!@#$"}),
				useTLS:   true,
				maxConns: 5,
			},
			url: "wss://example.com",
			ok:  true,
		},
		{
			name:     "IPv6 address, default hostname",
			hostname: "localhost",
			conf: listenerConfig{
				listener: newMockListener(netAddr{"test", "[::1]:8080"}),
				useTLS:   false,
				maxConns: 1,
			},
			url: "ws://localhost:8080",
			ok:  true,
		},
		{
			name: "IPv4 address, TLS, no default hostname",
			conf: listenerConfig{
				listener: newMockListener(netAddr{"test", "127.0.0.1:8090"}),
				useTLS:   true,
				maxConns: 1,
			},
			url: "wss://127.0.0.1:8090",
			ok:  true,
		},
	}
	for _, test := range tests {
		func() {
			app := NewApplication()
			if len(test.hostname) > 0 {
				app.hostname = test.hostname
			}
			app.SetLogger(mckLogger)
			app.SetMetrics(mckStat)
			sh := NewSocketHandler()
			defer sh.Close()
			sh.setApp(app)
			err := sh.listenWithConfig(test.conf)
			if err != nil {
				if test.ok {
					t.Errorf("On test %s, got listener error: %s", test.name, err)
				}
				return
			}
			if !test.ok {
				listener := sh.Listener()
				t.Errorf("On test %s, got %#v; want listener error", test.name, listener)
				listener.Close()
				return
			}
			if actual := sh.URL(); actual != test.url {
				t.Errorf("Mismatched handler URL: got %q; want %q",
					actual, test.url)
			}
			if actual := sh.MaxConns(); actual != test.conf.maxConns {
				t.Errorf("Mismatched maximum connection count: got %d; want %d",
					actual, test.conf.maxConns)
			}
		}()
	}
}

func TestSocketOrigin(t *testing.T) {
	var err error

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)

	app := NewApplication()
	app.SetLogger(mckLogger)
	app.SetMetrics(mckStat)
	app.SetStore(mckStore)
	app.SetRouter(mckRouter)

	sh := NewSocketHandler()
	defer sh.Close()
	sh.setApp(app)

	pipe := newPipeListener()
	defer pipe.Close()
	if err := sh.listenWithConfig(listenerConfig{listener: pipe}); err != nil {
		t.Fatalf("Error setting listener: %s", err)
	}
	sh.server = newServeWaiter(&http.Server{Handler: sh.ServeMux()})
	app.SetSocketHandler(sh)

	errChan := make(chan error, 1)
	go sh.Start(errChan)

	uaid := "5e1e5984569c4f00bf4bea47754a6403"
	gomock.InOrder(
		mckStat.EXPECT().Increment("client.socket.connect"),
		mckStore.EXPECT().CanStore(0).Return(true),
		mckRouter.EXPECT().Register(uaid),
		mckStat.EXPECT().Increment("updates.client.hello"),
		mckStore.EXPECT().FetchAll(uaid, gomock.Any()).Return(nil, nil, nil),
		mckStat.EXPECT().Timer("client.flush", gomock.Any()),
		mckRouter.EXPECT().Unregister(uaid),
		mckStat.EXPECT().Timer("client.socket.lifespan", gomock.Any()),
		mckStat.EXPECT().Increment("client.socket.disconnect"),
	)

	origin := &url.URL{Scheme: "https", Host: "example.com"}
	conn, err := dialSocketListener(pipe, &websocket.Config{
		Location: origin,
		Origin:   origin,
		Version:  websocket.ProtocolVersionHybi13,
	})
	if err != nil {
		t.Fatalf("Error dialing origin: %s", err)
	}
	defer conn.Close()
	err = websocket.JSON.Send(conn, struct {
		Type       string   `json:"messageType"`
		DeviceID   string   `json:"uaid"`
		ChannelIDs []string `json:"channelIDs"`
	}{"hello", uaid, []string{}})
	if err != nil {
		t.Fatalf("Error writing client handshake: %s", err)
	}
	reply := new(HelloReply)
	if err = websocket.JSON.Receive(conn, reply); err != nil {
		t.Fatalf("Error reading server handshake: %s", err)
	}
	if reply.DeviceID != uaid {
		t.Fatalf("Mismatched device ID: got %q; want %q", reply.DeviceID, uaid)
	}

	worker, workerConnected := app.GetWorker(uaid)
	if !workerConnected {
		t.Fatalf("Missing worker for device ID %q", uaid)
	}
	workerOrigin := worker.Origin()
	if expectedOrigin := origin.String(); workerOrigin != expectedOrigin {
		t.Errorf("Mismatched origins: got %q; want %q",
			workerOrigin, expectedOrigin)
	}
}

func TestSocketSameOrigin(t *testing.T) {
	tests := []struct {
		name     string
		origins  [2]string
		expected bool
	}{
		{"Matching domains", [2]string{"https://example.com", "https://example.com"}, true},
		{"Mismatched protocols", [2]string{"http://example.com", "https://example.com"}, false},
		{"Mismatched domains", [2]string{"https://example.com", "https://example.org"}, false},
		{"Mismatched ports", [2]string{"https://example.com:1", "https://example.com:2"}, false},

		// This contradicts RFC 6454, but avoids complicating isSameOrigin with
		// logic for validating and extracting port numbers from hosts.
		// net.SplitHostPort is almost adequate for this, but conflates invalid
		// hosts and missing ports.
		{"Explicit ports", [2]string{"https://example.com", "https://example.com:443"}, false},
	}
	for _, test := range tests {
		a, err := url.ParseRequestURI(test.origins[0])
		if err != nil {
			t.Errorf("On test %s, invalid URL %q: %s", test.name, test.origins[0], err)
		}
		b, err := url.ParseRequestURI(test.origins[1])
		if err != nil {
			t.Errorf("On test %s, invalid URL %q: %s", test.name, test.origins[1], err)
		}
		if actual := isSameOrigin(a, b); actual != test.expected {
			t.Errorf("On test %s, got %s; want %s", test.name, actual, test.expected)
		}
		if actual := isSameOrigin(b, a); actual != test.expected {
			t.Errorf("Test %s not transitive: got %s; want %s", test.name, actual,
				test.expected)
		}
	}
}

func TestSocketInvalidOrigin(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStat.EXPECT().Increment("client.socket.connect").AnyTimes()
	mckStat.EXPECT().Timer("client.socket.lifespan", gomock.Any()).AnyTimes()
	mckStat.EXPECT().Increment("client.socket.disconnect").AnyTimes()
	mckStore := NewMockStore(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)

	testWithOrigin := func(allowedOrigins []string,
		config *websocket.Config) error {

		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetRouter(mckRouter)

		sh := NewSocketHandler()
		defer sh.Close()
		sh.setApp(app)
		sh.setOrigins(allowedOrigins)
		pipe := newPipeListener()
		if err := sh.listenWithConfig(listenerConfig{listener: pipe}); err != nil {
			return err
		}
		sh.server = newServeWaiter(&http.Server{Handler: sh.ServeMux()})
		app.SetSocketHandler(sh)

		errChan := make(chan error, 1)
		go sh.Start(errChan)

		conn, err := dialSocketListener(pipe, config)
		if err != nil {
			return err
		}
		conn.Close()
		return nil
	}

	location := &url.URL{Scheme: "https", Host: "example.com"}
	badURL := &url.URL{Scheme: "!@#$", Host: "^&*-"}

	tests := []struct {
		name           string
		allowedOrigins []string
		config         *websocket.Config
		err            error
	}{
		{
			"Should allow all origins if none are specified",
			nil,
			&websocket.Config{
				Version:  websocket.ProtocolVersionHybi13,
				Location: location,
				Origin:   location,
			},
			nil,
		},
		{
			"Should match multiple origins",
			[]string{"https://example.com", "https://example.org", "https://example.net"},
			&websocket.Config{
				Version:  websocket.ProtocolVersionHybi13,
				Location: location,
				Origin:   &url.URL{Scheme: "https", Host: "example.net"},
			},
			nil,
		},
		{
			"Should reject mismatched origins",
			[]string{"https://example.com"},
			&websocket.Config{
				Version:  websocket.ProtocolVersionHybi13,
				Location: location,
				Origin:   &url.URL{Scheme: "http", Host: "example.org"},
			},
			websocket.ErrBadStatus, // 403 status code.
		},
		{
			"Should reject malformed origins if some are specified",
			[]string{"https://example.com"},
			&websocket.Config{
				Version:  websocket.ProtocolVersionHybi13,
				Location: location,
				Origin:   badURL, // Malformed URL.
			},
			websocket.ErrBadStatus, // 403 status code.
		},
		{
			"Should ignore malformed origins if none are specified",
			nil,
			&websocket.Config{
				Version:  websocket.ProtocolVersionHybi13,
				Location: location,
				Origin:   badURL,
			},
			nil,
		},
		{
			"Should allow empty origins if none are specified",
			nil,
			&websocket.Config{
				Version:  websocket.ProtocolVersionHybi13,
				Location: location,
				Origin:   &url.URL{},
			},
			nil,
		},
		{
			"Should reject empty origins if some are specified",
			[]string{"https://example.com"},
			&websocket.Config{
				Version:  websocket.ProtocolVersionHybi13,
				Location: location,
				Origin:   &url.URL{},
			},
			websocket.ErrBadStatus,
		},
	}
	for _, test := range tests {
		err := testWithOrigin(test.allowedOrigins, test.config)
		if err != test.err {
			t.Errorf("On test %s, got %#v; want %#v", test.name, err, test.err)
		}
	}
}
