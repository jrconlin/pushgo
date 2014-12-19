/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/tls"
	"fmt"
	"net"
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

// TimeoutDialer returns a dialer function suitable for use with an
// http.Transport instance.
func TimeoutDialer(cTimeout, rwTimeout time.Duration) func(net, addr string) (c net.Conn, err error) {
	return func(netw, addr string) (c net.Conn, err error) {
		c, err = net.DialTimeout(netw, addr, cTimeout)
		if err != nil {
			return nil, err
		}
		// do we need this if ResponseHeaderTimeout is set?
		c.SetDeadline(time.Now().Add(rwTimeout))
		return c, nil
	}
}
