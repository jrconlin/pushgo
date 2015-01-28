/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/net/websocket"
)

func NewSocketHandler() (h *SocketHandler) {
	h = &SocketHandler{mux: mux.NewRouter()}
	h.mux.Handle("/", websocket.Server{
		Handler:   h.PushSocketHandler,
		Handshake: h.checkOrigin,
	})
	return h
}

type SocketHandlerConfig struct {
	Origins  []string
	Listener TCPListenerConfig
}

type SocketHandler struct {
	app       *Application
	logger    *SimpleLogger
	metrics   Statistician
	store     Store
	origins   []*url.URL
	listener  net.Listener
	server    Server
	mux       *mux.Router
	url       string
	maxConns  int
	closeOnce Once
}

func (h *SocketHandler) ConfigStruct() interface{} {
	return &SocketHandlerConfig{
		Listener: TCPListenerConfig{
			Addr:            ":8080",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
	}
}

func (h *SocketHandler) Init(app *Application, config interface{}) (err error) {
	conf := config.(*SocketHandlerConfig)
	h.setApp(app)
	if err = h.setOrigins(conf.Origins); err != nil {
		h.logger.Panic("handlers_socket", "Could not set allowed origins",
			LogFields{"error": err.Error()})
		return err
	}
	if err = h.listenWithConfig(conf.Listener); err != nil {
		h.logger.Panic("handlers_socket", "Could not attach WebSocket listener",
			LogFields{"error": err.Error()})
		return err
	}
	h.server = NewServeCloser(&http.Server{
		Handler: &LogHandler{h.mux, h.logger},
		ErrorLog: log.New(&LogWriter{
			Logger: h.logger,
			Name:   "handlers_socket",
			Level:  ERROR,
		}, "", 0),
	})
	return nil
}

// setApp sets the parent application for this WebSocket handler.
func (h *SocketHandler) setApp(app *Application) {
	h.app = app
	h.logger = app.Logger()
	h.metrics = app.Metrics()
	h.store = app.Store()
}

// listenWithConfig starts a listener for this WebSocket handler.
func (h *SocketHandler) listenWithConfig(conf ListenerConfig) (err error) {
	if h.listener, err = conf.Listen(); err != nil {
		return err
	}
	var scheme string
	if conf.UseTLS() {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	host, port := HostPort(h.listener, h.app)
	h.url = CanonicalURL(scheme, host, port)
	h.maxConns = conf.GetMaxConns()
	return nil
}

// setOrigins sets the allowed WebSocket origins.
func (h *SocketHandler) setOrigins(origins []string) (err error) {
	h.origins = make([]*url.URL, len(origins))
	for i, origin := range origins {
		if h.origins[i], err = url.ParseRequestURI(origin); err != nil {
			return fmt.Errorf("Error parsing origin %q: %s", origin, err)
		}
	}
	return nil
}

func (h *SocketHandler) Listener() net.Listener { return h.listener }
func (h *SocketHandler) MaxConns() int          { return h.maxConns }
func (h *SocketHandler) URL() string            { return h.url }
func (h *SocketHandler) ServeMux() ServeMux     { return (*RouteMux)(h.mux) }

func (h *SocketHandler) Start(errChan chan<- error) {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "Starting WebSocket server",
			LogFields{"url": h.url})
	}
	errChan <- h.server.Serve(h.listener)
}

func (h *SocketHandler) PushSocketHandler(ws *websocket.Conn) {
	requestID := ws.Request().Header.Get(HeaderID)
	worker := NewWorker(h.app, (*WebSocket)(ws), requestID)

	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "websocket connection",
			LogFields{"rid": requestID})
	}
	defer func() {
		now := time.Now()
		// Clean-up the resources
		worker.Close()
		h.metrics.Timer("client.socket.lifespan", now.Sub(worker.Born()))
		h.metrics.Increment("client.socket.disconnect")
	}()

	h.metrics.Increment("client.socket.connect")

	worker.Run()
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "Server for client shut-down",
			LogFields{"rid": requestID})
	}
}

func (h *SocketHandler) checkOrigin(conf *websocket.Config, req *http.Request) (err error) {
	if conf.Origin, err = websocket.Origin(conf, req); err != nil {
		if h.logger.ShouldLog(NOTICE) {
			h.logger.Notice("handlers_socket", "Error parsing WebSocket origin",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
	}
	if len(h.origins) == 0 {
		return nil
	}
	if conf.Origin == nil {
		return ErrMissingOrigin
	}
	for _, origin := range h.origins {
		if isSameOrigin(conf.Origin, origin) {
			return nil
		}
	}
	if h.logger.ShouldLog(WARNING) {
		h.logger.Warn("handlers_socket",
			"Rejected WebSocket connection from unknown origin", LogFields{
				"rid": req.Header.Get(HeaderID), "origin": conf.Origin.String()})
	}
	return ErrInvalidOrigin
}

func (h *SocketHandler) Close() error {
	return h.closeOnce.Do(h.close)
}

func (h *SocketHandler) close() (err error) {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "Closing WebSocket handler",
			LogFields{"url": h.url})
	}
	if h.listener != nil {
		if err = h.listener.Close(); err != nil && h.logger.ShouldLog(ERROR) {
			h.logger.Error("handlers_socket", "Error closing WebSocket listener",
				LogFields{"error": err.Error(), "url": h.url})
		}
	}
	if h.server != nil {
		h.server.Close()
	}
	return
}

func isSameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}
