/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"container/list"
	"crypto/tls"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// TooBusyError is a temporary error returned when too many goroutines are
// active at once. The server will sleep before accepting new connections,
// allowing existing goroutines to finish.
type TooBusyError struct{}

func (e TooBusyError) Error() string   { return "Too many requests" }
func (e TooBusyError) Timeout() bool   { return false }
func (e TooBusyError) Temporary() bool { return true }

var errTooBusy error = TooBusyError{}

// NewTokenBucket creates a full token bucket.
func NewTokenBucket(limitBy string, capacity, fillRate int64) *TokenBucket {
	return &TokenBucket{capacity, fillRate, capacity, time.Now(), limitBy}
}

// TokenBucket is a token bucket implementation ported from restify, copyright
// 2012, Mark Cavage. It is not safe for concurrent use.
type TokenBucket struct {
	Capacity int64
	FillRate int64
	tokens   int64
	time     time.Time
	limitBy  string
}

// Consume takes tokens from the bucket. Returns false if the bucket does not
// contain the given number of tokens.
func (b *TokenBucket) Consume(tokens int64) bool {
	b.fill()
	if tokens <= b.tokens {
		b.tokens -= tokens
		return true
	}
	return false
}

// fill adds more tokens to the bucket.
func (b *TokenBucket) fill() {
	now := time.Now()
	if now.Before(b.time) {
		b.time = now.Add(-1 * time.Second)
	}
	if b.tokens < b.Capacity {
		delta := b.FillRate * int64(now.Sub(b.time)/time.Second)
		if b.tokens += delta; b.tokens > b.Capacity {
			b.tokens = b.Capacity
		}
	}
	b.time = now
}

// TokenTable is an interface for token storage adapters. Implementations are
// not guaranteed to be safe for concurrent use by multiple goroutines.
type TokenTable interface {
	Get(limitBy string) (*TokenBucket, bool)
	Put(*TokenBucket) error
}

// NewLRUTable creates an in-memory LRU token table.
func NewLRUTable(maxEntries int) *LRUTable {
	return &LRUTable{maxEntries, list.New(), make(map[string]*list.Element)}
}

// LRUTable is a least-recently used cache that maps throttling keys to token
// buckets. It is not safe for concurrent use.
type LRUTable struct {
	MaxEntries int
	lastAccess *list.List
	entries    map[string]*list.Element
}

// Get implements TokenTable.Get.
func (t *LRUTable) Get(limitBy string) (*TokenBucket, bool) {
	entry, ok := t.entries[limitBy]
	if !ok {
		return nil, false
	}
	t.lastAccess.MoveToFront(entry)
	return entry.Value.(*TokenBucket), true
}

// Put implements TokenTable.Put.
func (t *LRUTable) Put(bucket *TokenBucket) error {
	if entry, ok := t.entries[bucket.limitBy]; ok {
		t.lastAccess.MoveToFront(entry)
		entry.Value = bucket
		return nil
	}
	if t.lastAccess.Len() >= t.MaxEntries {
		entry := t.lastAccess.Back()
		if entry == nil {
			return nil
		}
		delete(t.entries, t.lastAccess.Remove(entry).(*TokenBucket).limitBy)
	}
	t.entries[bucket.limitBy] = t.lastAccess.PushFront(bucket)
	return nil
}

// LimitHandler returns a rate-limited http.Handler.
func LimitHandler(handler http.Handler, burst, rate int64, maxEntries int, trustProxy bool) http.Handler {
	return &RateLimitedHandler{
		Handler:    handler,
		TokenTable: NewLRUTable(maxEntries),
		Burst:      burst,
		Rate:       rate,
		MaxEntries: maxEntries,
		TrustProxy: trustProxy,
	}
}

// RateLimitedHandler throttles incoming HTTP requests based on the client's
// IP address.
type RateLimitedHandler struct {
	sync.Mutex
	http.Handler
	TokenTable
	Burst      int64
	Rate       int64
	MaxEntries int
	TrustProxy bool
}

func (l *RateLimitedHandler) canAccept(remoteAddr string) bool {
	var (
		bucket *TokenBucket
		ok     bool
	)
	defer l.Unlock()
	l.Lock()
	if bucket, ok = l.TokenTable.Get(remoteAddr); !ok {
		bucket = NewTokenBucket(remoteAddr, l.Burst, l.Rate)
		if err := l.TokenTable.Put(bucket); err != nil {
			return false
		}
	}
	return bucket.Consume(1)
}

// ServeHTTP implements http.Handler.ServeHTTP.
func (l *RateLimitedHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// `X-Forwarded-For` may contain multiple values; the first is the client IP.
	var remoteAddr string
	if host := req.Header.Get("X-Forwarded-For"); l.TrustProxy && len(host) > 0 {
		remoteAddr = host
	} else {
		remoteAddr, _, _ = net.SplitHostPort(req.RemoteAddr)
	}
	if !l.canAccept(remoteAddr) {
		http.Error(rw, "Too many requests", 429)
		return
	}
	l.Handler.ServeHTTP(rw, req)
}

// RateLimitedListener rejects incoming connections if the server is
// overloaded, and sets a keep-alive timer on accepted connections. Based on
// tcpKeepAliveListener from package net/http, copyright 2009, The Go Authors.
type RateLimitedListener struct {
	*net.TCPListener
	maxGoroutines   int
	keepAlivePeriod time.Duration
}

// Accept implements net.Listener.Addr.
func (listener RateLimitedListener) Accept() (conn net.Conn, err error) {
	if runtime.NumGoroutine() >= listener.maxGoroutines {
		return nil, errTooBusy
	}
	socket, err := listener.AcceptTCP()
	if err != nil {
		return nil, err
	}
	socket.SetKeepAlive(true)
	socket.SetKeepAlivePeriod(listener.keepAlivePeriod)
	return socket, nil
}

// Listen returns an active HTTP listener. This is identical to ListenAndServe
// from package net/http, but listens on a random port if addr is omitted, and
// does not call http.Server.Serve. Copyright 2009, The Go Authors.
func Listen(addr string, maxGoroutines int, keepAlivePeriod time.Duration) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return RateLimitedListener{listener.(*net.TCPListener), maxGoroutines, keepAlivePeriod}, nil
}

// ListenTLS returns an active HTTPS listener. Based on ListenAndServeTLS from
// package net/http, copyright 2009, The Go Authors.
func ListenTLS(addr, certFile, keyFile string, maxGoroutines int, keepAlivePeriod time.Duration) (net.Listener, error) {
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
	return tls.NewListener(RateLimitedListener{listener.(*net.TCPListener), maxGoroutines, keepAlivePeriod}, config), nil
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
