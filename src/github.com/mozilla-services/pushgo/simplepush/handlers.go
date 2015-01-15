/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"net"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// ServeMux is an HTTP request multiplexer, implemented by the RouteMux and
// http.ServeMux types.
type ServeMux interface {
	Handle(string, http.Handler)
	HandleFunc(string, func(http.ResponseWriter, *http.Request))
	ServeHTTP(http.ResponseWriter, *http.Request)
}

// RouteMux implements the ServeMux interface for a *mux.Router.
type RouteMux mux.Router

// Handle wraps mux.Router.Handle, ignoring the returned *mux.Router for
// compatibility with the ServeMux interface.
func (r *RouteMux) Handle(path string, handler http.Handler) {
	(*mux.Router)(r).Handle(path, handler)
}

// HandleFunc wraps mux.Router.HandleFunc, ignoring the return value.
func (r *RouteMux) HandleFunc(path string,
	f func(http.ResponseWriter, *http.Request)) {

	(*mux.Router)(r).HandleFunc(path, f)
}

// ServeHTTP wraps mux.Router.ServeHTTP.
func (r *RouteMux) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	(*mux.Router)(r).ServeHTTP(res, req)
}

type Handler interface {
	Listener() net.Listener
	MaxConns() int
	URL() string
	ServeMux() ServeMux
	Start(chan<- error)
	Close() error
}

type ListenerConfig struct {
	Addr            string
	MaxConns        int    `toml:"max_connections" env:"max_conns"`
	KeepAlivePeriod string `toml:"tcp_keep_alive" env:"keep_alive"`
	CertFile        string `toml:"cert_file" env:"cert"`
	KeyFile         string `toml:"key_file" env:"key"`
}

func (conf *ListenerConfig) UseTLS() bool {
	return len(conf.CertFile) > 0 && len(conf.KeyFile) > 0
}

func (conf *ListenerConfig) Listen() (ln net.Listener, err error) {
	keepAlivePeriod, err := time.ParseDuration(conf.KeepAlivePeriod)
	if err != nil {
		return nil, err
	}
	if conf.UseTLS() {
		return ListenTLS(conf.Addr, conf.CertFile, conf.KeyFile, conf.MaxConns, keepAlivePeriod)
	}
	return Listen(conf.Addr, conf.MaxConns, keepAlivePeriod)
}
