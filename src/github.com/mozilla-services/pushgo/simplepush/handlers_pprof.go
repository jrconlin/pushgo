/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
)

type ProfileHandlersConfig struct {
	Enabled  bool
	Listener ListenerConfig
}

// ProfileHandlers exposes handlers provided by package net/http/pprof. These
// are useful for profiling CPU and heap usage during load testing.
type ProfileHandlers struct {
	logger   *SimpleLogger
	listener net.Listener
	server   *ServeCloser
	url      string
	maxConns int
}

func (p *ProfileHandlers) ConfigStruct() interface{} {
	return &ProfileHandlersConfig{
		Enabled: false,
		Listener: ListenerConfig{
			Addr:            ":8082",
			MaxConns:        100,
			KeepAlivePeriod: "3m",
		},
	}
}

func (p *ProfileHandlers) Init(app *Application, config interface{}) (err error) {
	conf := config.(*ProfileHandlersConfig)
	p.logger = app.Logger()

	if !conf.Enabled {
		return nil
	}

	if p.listener, err = conf.Listener.Listen(); err != nil {
		p.logger.Panic("handlers_pprof", "Could not attach profiling listener",
			LogFields{"error": err.Error()})
		return err
	}

	var scheme string
	if conf.Listener.UseTLS() {
		scheme = "https"
	} else {
		scheme = "http"
	}
	host, port := HostPort(p.listener, app)
	p.url = CanonicalURL(scheme, host, port)

	p.maxConns = conf.Listener.MaxConns
	p.server = NewServeCloser(&http.Server{
		Handler: &LogHandler{http.DefaultServeMux, p.logger},
		ErrorLog: log.New(&LogWriter{
			Logger: p.logger,
			Name:   "handlers_profile",
			Level:  ERROR,
		}, "", 0),
	})

	return nil
}

func (p *ProfileHandlers) Listener() net.Listener { return p.listener }
func (p *ProfileHandlers) MaxConns() int          { return p.maxConns }
func (p *ProfileHandlers) URL() string            { return p.url }
func (p *ProfileHandlers) ServeMux() ServeMux     { return http.DefaultServeMux }

func (p *ProfileHandlers) Start(errChan chan<- error) {
	if p.server == nil {
		if p.logger.ShouldLog(INFO) {
			p.logger.Info("handlers_profile", "Profiling server disabled", nil)
		}
		return
	}
	if p.logger.ShouldLog(WARNING) {
		p.logger.Warn("handlers_profile", "Starting profiling server",
			LogFields{"url": p.url})
	}
	errChan <- p.server.Serve(p.listener)
}

func (p *ProfileHandlers) Close() error {
	var errors MultipleError
	if p.listener != nil {
		if err := p.listener.Close(); err != nil {
			if p.logger.ShouldLog(ERROR) {
				p.logger.Error("handlers_profile", "Error closing profiling listener",
					LogFields{"error": err.Error(), "url": p.url})
			}
			errors = append(errors, err)
		}
	}
	if p.server != nil {
		if err := p.server.Close(); err != nil {
			if p.logger.ShouldLog(ERROR) {
				p.logger.Error("handlers_profile", "Error closing profiling server",
					LogFields{"error": err.Error(), "url": p.url})
			}
			errors = append(errors, err)
		}
	}
	if len(errors) > 0 {
		return errors
	}
	return nil
}
