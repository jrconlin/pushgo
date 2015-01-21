/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"net"
	"time"

	"github.com/gorilla/mux"
)

type Handler interface {
	Listener() net.Listener
	MaxConns() int
	URL() string
	ServeMux() *mux.Router
	Start(chan<- error)
	Close() error
}

type ListenerConfig struct {
	Addr            string
	MaxConns        int    `toml:"max_connections" env:"max_connections"`
	KeepAlivePeriod string `toml:"tcp_keep_alive" env:"tcp_keep_alive"`
	CertFile        string `toml:"cert_file" env:"cert_file"`
	KeyFile         string `toml:"key_file" env:"key_file"`
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
