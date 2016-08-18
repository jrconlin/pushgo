/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var defaultPorts = map[string]string{
	"https": "443",
	"wss":   "443",
	"http":  "80",
	"ws":    "80",
}

type Hostnamer interface {
	Hostname() string
}

// HostPort returns the host and port on which ln is listening. If dh is nil
// or the default hostname is empty, the IP of ln will be used instead.
func HostPort(ln net.Listener, dh Hostnamer) (host, port string) {
	var defaultHost string
	if dh != nil {
		defaultHost = dh.Hostname()
	}
	var addr net.Addr
	if ln != nil {
		addr = ln.Addr()
	}
	if addr == nil {
		return defaultHost, ""
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return defaultHost, ""
	}
	if len(defaultHost) > 0 {
		host = defaultHost
	}
	return host, port
}

// CanonicalURL constructs a URL from the given scheme, host, and port,
// excluding default port numbers.
func CanonicalURL(scheme, host, port string) string {
	hasZone := strings.IndexByte(host, '%') >= 0
	if hasZone {
		// Percent-encode zone identifiers per RFC 6874.
		host = strings.Replace(host, "%", "%25", -1)
	}
	if hasZone || strings.IndexByte(host, ':') >= 0 {
		host = "[" + host + "]"
	}
	if len(port) == 0 || defaultPorts[scheme] == port {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

// ParseRetryAfter parses a Retry-After header value. Per RFC 7231
// section 7.1.3, the value may be either an absolute time or the
// number of seconds to wait.
func ParseRetryAfter(header string) (d time.Duration, ok bool) {
	if len(header) == 0 {
		return 0, false
	}
	sec, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		t, err := http.ParseTime(header)
		if err != nil {
			return 0, false
		}
		d = t.Sub(timeNow())
	} else {
		d = time.Duration(sec) * time.Second
	}
	if d > 0 {
		return d, true
	}
	return 0, false
}

type ListenerError struct {
	Message     string
	IsTemporary bool
}

func (err *ListenerError) Error() string   { return err.Message }
func (err *ListenerError) Timeout() bool   { return false }
func (err *ListenerError) Temporary() bool { return err.IsTemporary }

var (
	// errTooBusy is a temporary error returned when too many simultaneous
	// connections are open. The server will sleep before accepting new
	// connections.
	errTooBusy = &ListenerError{"Too many requests", true}

	// errClosed is returned when the listener is closed.
	errClosed = &ListenerError{"Listener closed", false}
)

// limitConn decrements the active connection count for closed connections.
type limitConn struct {
	net.Conn
	removeOnce sync.Once
	removeConn func()
}

// Close implements net.Conn.Close.
func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.removeOnce.Do(c.removeConn)
	return err
}

// isTerminalState indicates whether state is the last connection state for
// which the http.Server.ConnState hook will be called. This is used by
// ServeCloser to remove tracked connections from its map.
func isTerminalState(state http.ConnState) bool {
	return state == http.StateClosed || state == http.StateHijacked
}

// NewServeCloser wraps srv and returns a closable HTTP server.
func NewServeCloser(srv *http.Server) (s *ServeCloser) {
	s = &ServeCloser{
		Server:    srv,
		stateHook: srv.ConnState,
		conns:     make(map[net.Conn]bool),
	}
	srv.ConnState = s.connState
	return
}

// ServeCloser is an HTTP server with graceful shutdown support. Closing a
// server cancels any pending requests by closing the underlying connections.
// Since a server may accept connections from multiple listeners (e.g.,
// `srv.Serve(tcpListener)` and `srv.Serve(tlsListener)`), callers should close
// the underlying listeners before closing the server.
type ServeCloser struct {
	*http.Server
	stateHook func(net.Conn, http.ConnState)
	connsLock sync.Mutex // Protects conns.
	conns     map[net.Conn]bool
	closeOnce Once
}

// Close stops the server. The returned error is always nil; it is included
// to satisfy the io.Closer interface.
func (s *ServeCloser) Close() error {
	return s.closeOnce.Do(s.close)
}

func (s *ServeCloser) close() error {
	// Disable HTTP keep-alive for requests handled before the underlying
	// connections are closed.
	s.SetKeepAlivesEnabled(false)
	s.connsLock.Lock()
	defer s.connsLock.Unlock()
	for c := range s.conns {
		delete(s.conns, c)
		c.Close()
	}
	return nil
}

func (s *ServeCloser) hasConn(c net.Conn) bool {
	s.connsLock.Lock()
	defer s.connsLock.Unlock()
	return s.conns[c]
}

func (s *ServeCloser) addConn(c net.Conn) {
	if s.closeOnce.IsDone() {
		c.Close()
		return
	}
	s.connsLock.Lock()
	defer s.connsLock.Unlock()
	s.conns[c] = true
}

func (s *ServeCloser) removeConn(c net.Conn) {
	if s.closeOnce.IsDone() {
		return
	}
	s.connsLock.Lock()
	defer s.connsLock.Unlock()
	delete(s.conns, c)
}

func (s *ServeCloser) connState(c net.Conn, state http.ConnState) {
	if state == http.StateNew {
		// Track new connections.
		s.addConn(c)
	} else if isTerminalState(state) {
		// Remove connections in the terminal state (i.e., closed and hijacked).
		// Callers must track hijacked connections separately.
		s.removeConn(c)
	}
	if s.stateHook != nil {
		s.stateHook(c, state)
	}
}

// keepAliver is used to set keep-alive settings on supported connections.
type keepAliver interface {
	SetKeepAlive(bool) error
	SetKeepAlivePeriod(time.Duration) error
}

// LimitListener restricts the number of concurrent connections accepted by the
// underlying listener, and sets a keep-alive timer on accepted connections.
// Based on tcpKeepAliveListener from package net/http, copyright 2009,
// The Go Authors.
type LimitListener struct {
	net.Listener
	MaxConns        int
	KeepAlivePeriod time.Duration
	conns           int32
	closeOnce       Once
}

func (l *LimitListener) addConn()    { atomic.AddInt32(&l.conns, 1) }
func (l *LimitListener) removeConn() { atomic.AddInt32(&l.conns, -1) }

// ConnCount returns the number of active connections.
func (l *LimitListener) ConnCount() int { return int(atomic.LoadInt32(&l.conns)) }

// setKeepAlive enables TCP keep-alive on c. If the keep-alive period is not
// set or c is not a TCP connection, setKeepAlive is a no-op.
func (l *LimitListener) setKeepAlive(c net.Conn) {
	if l.KeepAlivePeriod <= 0 {
		return
	}
	socket, ok := c.(keepAliver)
	if !ok {
		return
	}
	socket.SetKeepAlive(true)
	socket.SetKeepAlivePeriod(l.KeepAlivePeriod)
}

// Accept implements net.Listener.Addr.
func (l *LimitListener) Accept() (conn net.Conn, err error) {
	if l.closeOnce.IsDone() {
		// Avoid accepting new connections if the listener has been
		// closed.
		return nil, errClosed
	}
	if l.ConnCount() >= l.MaxConns {
		return nil, errTooBusy
	}
	socket, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.setKeepAlive(socket)
	l.addConn()
	return &limitConn{Conn: socket, removeConn: l.removeConn}, nil
}

// Close implements net.Listener.Close.
func (l *LimitListener) Close() error {
	return l.closeOnce.Do(l.Listener.Close)
}

// Listen returns an active HTTP listener. This is identical to ListenAndServe
// from package net/http, but listens on a random port if addr is omitted, and
// does not call http.Server.Serve. Copyright 2009, The Go Authors.
func Listen(addr string, maxConns int, keepAlivePeriod time.Duration) (
	net.Listener, error) {

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &LimitListener{Listener: ln, MaxConns: maxConns,
		KeepAlivePeriod: keepAlivePeriod}, nil
}

// ListenTLS returns an active HTTPS listener. Based on ListenAndServeTLS from
// package net/http, copyright 2009, The Go Authors.
func ListenTLS(addr, certFile, keyFile string, maxConns int,
	keepAlivePeriod time.Duration) (net.Listener, error) {

	ln, err := Listen(addr, maxConns, keepAlivePeriod)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return newTLSListener(ln, cert), nil
}

// newTLSListener returns a TLS listener with required Mozilla settings.
func newTLSListener(ln net.Listener, cert tls.Certificate) net.Listener {
	config := &tls.Config{
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{cert},
		// The following are Mozilla required TLS settings.
		MinVersion:               tls.VersionTLS10,
		PreferServerCipherSuites: true,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA},
	}
	return tls.NewListener(ln, config)
}
