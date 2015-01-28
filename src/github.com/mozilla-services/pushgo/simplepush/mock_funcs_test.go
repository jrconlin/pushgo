/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

// This file contains common testing helpers.

import (
	"net"
	"net/http"
	"sync"
	"text/template"
	"time"
)

// testEndpointTemplate is a prepared push endpoint template used to
// test Application.CreateEndpoint.
var testEndpointTemplate = template.Must(template.New("Push").Parse(
	"{{.CurrentHost}}/{{.Token}}"))

// testID is a UUID string returned by idGenerate.
var testID = "d1c7c768b1be4c7093a69b52910d4baa"

// useMockFuncs replaces all non-deterministic functions with mocks: time is
// frozen, the process ID is fixed, and the UUID generation functions return
// predictable IDs. useMockFuncs is only exposed to tests.
func useMockFuncs() {
	cryptoRandRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0
		}
		return len(b), nil
	}
	idGenerate = func() (string, error) { return testID, nil }
	idGenerateBytes = func() ([]byte, error) {
		// d1c7c768-b1be-4c70-93a6-9b52910d4baa.
		return []byte{0xd1, 0xc7, 0xc7, 0x68, 0xb1, 0xbe, 0x4c, 0x70, 0x93,
			0xa6, 0x9b, 0x52, 0x91, 0x0d, 0x4b, 0xaa}, nil
	}
	osGetPid = func() int { return 1234 }
	timeNow = func() time.Time {
		// 2009-11-10 23:00:00 UTC; matches the Go Playground.
		return time.Unix(1257894000, 0).UTC()
	}
}

// netAddr implements net.Addr.
type netAddr struct {
	network string
	address string
}

func (a netAddr) Network() string { return a.network }
func (a netAddr) String() string  { return a.address }

// newMockListener creates a fake net.Listener with addr.
func newMockListener(addr net.Addr) *mockListener {
	return &mockListener{addr: addr}
}

// mockListener is a fake net.Listener for testing.
type mockListener struct {
	addr   net.Addr                 // The listener address.
	accept func() (net.Conn, error) // Optional handler called by Accept.
	close  func() error             // Optional handler called by Close.
}

// Accept calls ml.accept, or returns an error if ml.accept is nil.
func (ml *mockListener) Accept() (net.Conn, error) {
	if ml.accept != nil {
		return ml.accept()
	}
	return nil, &netErr{timeout: false, temporary: false}
}

// Close calls ml.close, or returns nil if ml.close is nil.
func (ml *mockListener) Close() error {
	if ml.close != nil {
		return ml.close()
	}
	return nil
}

// Addr returns the listener address.
func (ml *mockListener) Addr() net.Addr { return ml.addr }

// newPipeListener creates a synchronous net.Listener for testing.
func newPipeListener() *pipeListener {
	return &pipeListener{
		connChan:  make(chan net.Conn),
		closeChan: make(chan bool),
	}
}

// pipeListener is used to test WebSocket and HTTP client interactions without
// starting a TCP listener. A client can call Dial to establish a new client
// connection; the corresponding Accept call returns the server end of the
// connection.
type pipeListener struct {
	connChan  chan net.Conn
	closeChan chan bool
	closeOnce Once
}

// Dial creates a socket pair and blocks until the server end is accepted by a
// matching Accept call. network and addr are ignored; they are only specified
// to satisfy the http.Client.Dial signature. If the listener is closed, Dial
// returns a net.Error.
func (pl *pipeListener) Dial(network string, addr string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case <-pl.closeChan:
		return nil, &netErr{timeout: false, temporary: false}

	case pl.connChan <- server:
	}
	return client, nil
}

// Accept blocks until a client calls Dial to establish a client connection,
// then returns the server end of the connection. If the listener is closed,
// Accept returns a net.Error with err.Temporary() == false.
func (pl *pipeListener) Accept() (conn net.Conn, err error) {
	select {
	case <-pl.closeChan:
		err = &netErr{timeout: false, temporary: false}
	case conn = <-pl.connChan:
	}
	return
}

// Close closes the listener, unblocking all pending Dial and Accept calls. It
// is safe to call Close multiple times; subsequent calls are no-ops. The
// returned error is always nil.
func (pl *pipeListener) Close() error {
	return pl.closeOnce.Do(pl.close)
}

func (pl *pipeListener) close() error {
	close(pl.closeChan)
	return nil
}

// Addr returns a fake network address for this listener.
func (pl *pipeListener) Addr() net.Addr { return netAddr{"pipe", "pipe"} }

// newServeWaiter wraps srv in a serveWaiter.
func newServeWaiter(srv *http.Server) (t *serveWaiter) {
	handler := srv.Handler
	if handler == nil {
		handler = http.DefaultServeMux
	}
	t = &serveWaiter{ServeCloser: NewServeCloser(srv)}
	srv.Handler = &waitGroupHandler{srv: t, handler: handler}
	return
}

// serveWaiter wraps a ServeCloser with request tracking support. This is used
// by the tests to ensure that all cleanup functions have a chance to run, as
// there is no explicit synchronization between the main goroutine and the
// goroutines started by http.Server.Serve.
type serveWaiter struct {
	*ServeCloser
	serveWait sync.WaitGroup // Request counter.
}

// Close blocks until all pending requests have completed, then closes the
// underlying ServeCloser.
func (t *serveWaiter) Close() error {
	t.serveWait.Wait()
	return t.ServeCloser.Close()
}

// waitGroupHandler wraps an http.Handler, updating the parent serveWaiter's
// request counter for each request. This allows the parent to block until all
// requests have been handled. Based on waitGroupHandler from package
// net/http/httptest, copyright 2011, The Go Authors.
type waitGroupHandler struct {
	srv     *serveWaiter
	handler http.Handler
}

func (h *waitGroupHandler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	h.srv.serveWait.Add(1)
	defer h.srv.serveWait.Done()
	h.handler.ServeHTTP(res, req)
}
