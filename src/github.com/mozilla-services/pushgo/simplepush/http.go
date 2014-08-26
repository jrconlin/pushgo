/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/tls"
	"net"
	"time"
)

// TCPKeepAliveListener sets a keep-alive timer on an incoming connection.
// This is a reimplementation of the unexported tcpKeepAliveListener struct
// from package net/http, copyright 2009, The Go Authors.
type TCPKeepAliveListener struct {
	*net.TCPListener
}

// Accept implements net.Listener.Addr.
func (listener TCPKeepAliveListener) Accept() (conn net.Conn, err error) {
	socket, err := listener.AcceptTCP()
	if err != nil {
		return
	}
	socket.SetKeepAlive(true)
	socket.SetKeepAlivePeriod(3 * time.Minute)
	return socket, nil
}

// Listen returns an active HTTP listener. This is identical to ListenAndServe
// from package net/http, but listens on a random port if addr is omitted, and
// does not call http.Server.Serve. Copyright 2009, The Go Authors.
func Listen(addr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return TCPKeepAliveListener{listener.(*net.TCPListener)}, nil
}

// ListenTLS returns an active HTTPS listener. Based on ListenAndServeTLS from
// package net/http, copyright 2009, The Go Authors.
func ListenTLS(addr, certFile, keyFile string) (net.Listener, error) {
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
	return tls.NewListener(TCPKeepAliveListener{listener.(*net.TCPListener)}, config), nil
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
