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
	h = new(SocketHandler)
	h.Closable.CloserOnce = h
	return h
}

type SocketHandlerConfig struct {
	Origins  []string
	Listener ListenerConfig
}

type SocketHandler struct {
	Closable
	app      *Application
	logger   *SimpleLogger
	metrics  Statistician
	store    Store
	origins  []*url.URL
	listener net.Listener
	server   *ServeCloser
	mux      *mux.Router
	url      string
	maxConns int
}

func (h *SocketHandler) ConfigStruct() interface{} {
	return &SocketHandlerConfig{
		Listener: ListenerConfig{
			Addr:            ":8080",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
	}
}

func (h *SocketHandler) Init(app *Application, config interface{}) (err error) {
	conf := config.(*SocketHandlerConfig)

	h.app = app
	h.logger = app.Logger()
	h.metrics = app.Metrics()
	h.store = app.Store()

	if len(conf.Origins) > 0 {
		h.origins = make([]*url.URL, len(conf.Origins))
		for index, origin := range conf.Origins {
			if h.origins[index], err = url.ParseRequestURI(origin); err != nil {
				return fmt.Errorf("Error parsing origin: %s", err)
			}
		}
	}

	if h.listener, err = conf.Listener.Listen(); err != nil {
		h.logger.Panic("handlers_socket", "Could not attach WebSocket listener",
			LogFields{"error": err.Error()})
		return err
	}

	var scheme string
	if conf.Listener.UseTLS() {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	host, port := HostPort(h.listener, app)
	h.url = CanonicalURL(scheme, host, port)

	h.maxConns = conf.Listener.MaxConns

	h.mux = mux.NewRouter()
	h.mux.Handle("/", websocket.Server{Handler: h.PushSocketHandler,
		Handshake: h.checkOrigin})

	h.server = NewServeCloser(&http.Server{
		Handler: &LogHandler{h.mux, h.logger},
		ErrorLog: log.New(&LogWriter{
			Logger: h.logger.Logger,
			Name:   "handlers_socket",
			Level:  ERROR,
		}, "", 0),
	})

	return nil
}

func (h *SocketHandler) Listener() net.Listener { return h.listener }
func (h *SocketHandler) MaxConns() int          { return h.maxConns }
func (h *SocketHandler) URL() string            { return h.url }
func (h *SocketHandler) ServeMux() *mux.Router  { return h.mux }

func (h *SocketHandler) Start(errChan chan<- error) {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "Starting WebSocket server",
			LogFields{"url": h.url})
	}
	errChan <- h.server.Serve(h.listener)
}

func (h *SocketHandler) PushSocketHandler(ws *websocket.Conn) {
	requestID := ws.Request().Header.Get(HeaderID)
	sock := PushWS{Socket: (*WebSocket)(ws),
		Store:  h.store,
		Logger: h.logger,
		Born:   time.Now()}

	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "websocket connection",
			LogFields{"rid": requestID})
	}
	defer func() {
		now := time.Now()
		// Clean-up the resources
		h.app.Server().HandleCommand(PushCommand{DIE, nil}, &sock)
		h.metrics.Timer("client.socket.lifespan", now.Sub(sock.Born))
		h.metrics.Increment("client.socket.disconnect")
	}()

	h.metrics.Increment("client.socket.connect")

	NewWorker(h.app, requestID).Run(&sock)
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "Server for client shut-down",
			LogFields{"rid": requestID})
	}
}

func (h *SocketHandler) checkOrigin(conf *websocket.Config, req *http.Request) (err error) {
	if len(h.origins) == 0 {
		return nil
	}
	if conf.Origin, err = websocket.Origin(conf, req); err != nil {
		if h.logger.ShouldLog(WARNING) {
			h.logger.Warn("handlers_socket", "Error parsing WebSocket origin",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		return err
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

func (h *SocketHandler) CloseOnce() error {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "Closing WebSocket handler",
			LogFields{"url": h.url})
	}
	var errors MultipleError
	if err := h.listener.Close(); err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handlers", "Error closing WebSocket listener",
				LogFields{"error": err.Error(), "url": h.url})
		}
		errors = append(errors, err)
	}
	if err := h.server.Close(); err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handlers", "Error closing WebSocket server",
				LogFields{"error": err.Error(), "url": h.url})
		}
		errors = append(errors, err)
	}
	if len(errors) > 0 {
		return errors
	}
	return nil
}

func isSameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}
