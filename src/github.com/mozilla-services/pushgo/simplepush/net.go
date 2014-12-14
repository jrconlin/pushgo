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

var defaultPorts = map[string]int{
	"https": 443,
	"wss":   443,
	"http":  80,
	"ws":    80,
}

// CanonicalURL constructs a URL from the given scheme, host, and port,
// excluding default port numbers.
func CanonicalURL(scheme, host string, port int) string {
	if strings.IndexByte(host, ':') >= 0 || strings.IndexByte(host, '%') >= 0 {
		host = "[" + host + "]"
	}
	if defaultPorts[scheme] == port {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
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
		d = t.Sub(time.Now())
	} else {
		d = time.Duration(sec) * time.Second
	}
	if d > 0 {
		return d, true
	}
	return 0, false
}

// TooBusyError is a temporary error returned when too many simultaneous
// connections are open. The server will sleep before accepting new
// connections.
type TooBusyError struct{}

func (err TooBusyError) Error() string   { return "Too many requests" }
func (err TooBusyError) Timeout() bool   { return false }
func (err TooBusyError) Temporary() bool { return true }

var errTooBusy = TooBusyError{}

// LimitConn decrements the active connection count for closed connections.
type LimitConn struct {
	net.Conn
	removeOnce sync.Once
	removeConn func()
}

// Close implements net.Conn.Close.
func (c *LimitConn) Close() error {
	err := c.Conn.Close()
	c.removeOnce.Do(c.removeConn)
	return err
}

// NewServeCloser wraps srv and returns a closable HTTP server.
func NewServeCloser(srv *http.Server) (s *ServeCloser) {
	s = &ServeCloser{
		Server: srv,
		conns:  make(map[net.Conn]bool),
	}
	if srv.ConnState != nil {
		s.stateHook = srv.ConnState
	}
	srv.ConnState = s.connState
	return
}

// ServeCloser is an HTTP server with graceful shutdown support. Closing a
// server cancels any pending requests by closing the underlying connections.
type ServeCloser struct {
	*http.Server
	stateHook func(net.Conn, http.ConnState)
	connsLock sync.Mutex // Protects conns.
	conns     map[net.Conn]bool
	closed    int32 // Accessed atomically.
}

// Close stops the server.
func (s *ServeCloser) Close() error {
	if !atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		return nil
	}
	s.connsLock.Lock()
	defer s.connsLock.Unlock()
	for c := range s.conns {
		delete(s.conns, c)
		c.Close()
	}
	return nil
}

func (s *ServeCloser) addConn(c net.Conn) {
	if atomic.LoadInt32(&s.closed) == 1 {
		c.Close()
		return
	}
	s.connsLock.Lock()
	defer s.connsLock.Unlock()
	s.conns[c] = true
}

func (s *ServeCloser) removeConn(c net.Conn) {
	if atomic.LoadInt32(&s.closed) == 1 {
		c.Close()
		return
	}
	s.connsLock.Lock()
	defer s.connsLock.Unlock()
	delete(s.conns, c)
}

func (s *ServeCloser) connState(c net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		// Track new connections.
		s.addConn(c)
	case http.StateClosed, http.StateHijacked:
		// Remove closed and hijacked connections. Clients must track hijacked
		// connections separately.
		s.removeConn(c)
	}
	if s.stateHook != nil {
		s.stateHook(c, state)
	}
}

// LimitListener restricts the number of concurrent connections accepted by the
// underlying listener, and sets a keep-alive timer on accepted connections.
// Based on tcpKeepAliveListener from package net/http, copyright 2009,
// The Go Authors.
type LimitListener struct {
	*net.TCPListener
	maxConns        int
	conns           int32
	keepAlivePeriod time.Duration
}

func (l *LimitListener) addConn()    { atomic.AddInt32(&l.conns, 1) }
func (l *LimitListener) removeConn() { atomic.AddInt32(&l.conns, -1) }

// ConnCount returns the number of active connections.
func (l *LimitListener) ConnCount() int { return int(atomic.LoadInt32(&l.conns)) }

// Accept implements net.Listener.Addr.
func (l *LimitListener) Accept() (conn net.Conn, err error) {
	if l.ConnCount() >= l.maxConns {
		return nil, errTooBusy
	}
	socket, err := l.AcceptTCP()
	if err != nil {
		return nil, err
	}
	socket.SetKeepAlive(true)
	socket.SetKeepAlivePeriod(l.keepAlivePeriod)
	l.addConn()
	return &LimitConn{Conn: socket, removeConn: l.removeConn}, nil
}

// Listen returns an active HTTP listener. This is identical to ListenAndServe
// from package net/http, but listens on a random port if addr is omitted, and
// does not call http.Server.Serve. Copyright 2009, The Go Authors.
func Listen(addr string, maxConns int, keepAlivePeriod time.Duration) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &LimitListener{ln.(*net.TCPListener), maxConns, 0,
		keepAlivePeriod}, nil
}

// ListenTLS returns an active HTTPS listener. Based on ListenAndServeTLS from
// package net/http, copyright 2009, The Go Authors.
func ListenTLS(addr, certFile, keyFile string, maxConns int, keepAlivePeriod time.Duration) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
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
	return tls.NewListener(&LimitListener{ln.(*net.TCPListener), maxConns,
		0, keepAlivePeriod}, config), nil
}
