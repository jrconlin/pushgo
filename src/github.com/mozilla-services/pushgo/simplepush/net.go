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

// LimitConn tracks and returns leases for accepted connections. Based on
// limitListenerConn from package netutil, copyright 2013, The Go Authors.
type LimitConn struct {
	net.Conn
	releaseOnce sync.Once
	release     func()
}

// Close implements net.Conn.Close.
func (c *LimitConn) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}

// LimitListener restricts the number of concurrent connections accepted by the
// underlying listener, and sets a keep-alive timer on accepted connections.
// Based on tcpKeepAliveListener from package net/http, copyright 2009,
// The Go Authors.
type LimitListener struct {
	*net.TCPListener
	pending         chan struct{}
	keepAlivePeriod time.Duration
}

func (l *LimitListener) acquire() { l.pending <- struct{}{} }
func (l *LimitListener) release() { <-l.pending }

// Accept implements net.Listener.Addr.
func (l *LimitListener) Accept() (conn net.Conn, err error) {
	l.acquire()
	socket, err := l.AcceptTCP()
	if err != nil {
		l.release()
		return nil, err
	}
	socket.SetKeepAlive(true)
	socket.SetKeepAlivePeriod(l.keepAlivePeriod)
	return &LimitConn{Conn: socket, release: l.release}, nil
}

// Listen returns an active HTTP listener. This is identical to ListenAndServe
// from package net/http, but listens on a random port if addr is omitted, and
// does not call http.Server.Serve. Copyright 2009, The Go Authors.
func Listen(addr string, maxConns int, keepAlivePeriod time.Duration) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &LimitListener{listener.(*net.TCPListener),
		make(chan struct{}, maxConns), keepAlivePeriod}, nil
}

// ListenTLS returns an active HTTPS listener. Based on ListenAndServeTLS from
// package net/http, copyright 2009, The Go Authors.
func ListenTLS(addr, certFile, keyFile string, maxConns int, keepAlivePeriod time.Duration) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
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
	}
	return tls.NewListener(&LimitListener{listener.(*net.TCPListener),
		make(chan struct{}, maxConns), keepAlivePeriod}, config), nil
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
