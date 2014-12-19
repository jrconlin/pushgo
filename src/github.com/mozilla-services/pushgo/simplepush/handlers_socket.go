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
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/net/websocket"
)

func NewSocketHandlers() *SocketHandlers {
	return &SocketHandlers{
		sockets: make(map[*websocket.Conn]bool),
	}
}

type SocketHandlersConfig struct {
	Origins  []string
	Listener ListenerConfig
}

type SocketHandlers struct {
	app         *Application
	logger      *SimpleLogger
	metrics     Statistician
	store       Store
	hostname    string
	origins     []*url.URL
	listener    net.Listener
	mux         *mux.Router
	url         string
	maxConns    int
	socketsLock sync.Mutex // Protects sockets.
	sockets     map[*websocket.Conn]bool
	closed      int32 // Accessed atomically.
}

func (h *SocketHandlers) ConfigStruct() interface{} {
	return &SocketHandlersConfig{
		Listener: ListenerConfig{
			Addr:            ":8080",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
	}
}

func (h *SocketHandlers) Init(app *Application, config interface{}) (err error) {
	conf := config.(*SocketHandlersConfig)

	h.app = app
	h.logger = app.Logger()
	h.metrics = app.Metrics()
	h.store = app.Store()

	h.hostname = app.Hostname()

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
	h.mux = mux.NewRouter()
	h.mux.Handle("/", websocket.Server{Handler: h.PushSocketHandler,
		Handshake: h.checkOrigin})

	var scheme string
	if conf.Listener.UseTLS() {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	host, port := h.hostPort()
	h.url = CanonicalURL(scheme, host, port)

	h.maxConns = conf.Listener.MaxConns
	return nil
}

func (h *SocketHandlers) Listener() net.Listener { return h.listener }
func (h *SocketHandlers) MaxConns() int          { return h.maxConns }
func (h *SocketHandlers) URL() string            { return h.url }
func (h *SocketHandlers) ServeMux() *mux.Router  { return h.mux }

func (h *SocketHandlers) Start(errChan chan<- error) {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_socket", "Starting WebSocket server",
			LogFields{"addr": h.listener.Addr().String()})
	}
	clientSrv := NewServeCloser(&http.Server{
		Handler: &LogHandler{h.mux, h.logger},
		ErrorLog: log.New(&LogWriter{
			Logger: h.logger.Logger,
			Name:   "handlers_socket",
			Level:  ERROR,
		}, "", 0),
	})
	errChan <- clientSrv.Serve(h.listener)
}

func (h *SocketHandlers) PushSocketHandler(ws *websocket.Conn) {
	h.addSocket(ws)
	defer h.removeSocket(ws)

	requestID := ws.Request().Header.Get(HeaderID)
	sock := PushWS{Socket: ws,
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
		h.app.Server().HandleCommand(PushCommand{DIE, nil}, &sock) // TODO: Circular dependency.
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

func (h *SocketHandlers) checkOrigin(conf *websocket.Config, req *http.Request) (err error) {
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

func (h *SocketHandlers) addSocket(ws *websocket.Conn) {
	if atomic.LoadInt32(&h.closed) == 1 {
		ws.Close()
		return
	}
	h.socketsLock.Lock()
	defer h.socketsLock.Unlock()
	h.sockets[ws] = true
}

func (h *SocketHandlers) removeSocket(ws *websocket.Conn) {
	if atomic.LoadInt32(&h.closed) == 1 {
		ws.Close()
		return
	}
	h.socketsLock.Lock()
	defer h.socketsLock.Unlock()
	delete(h.sockets, ws)
}

func (h *SocketHandlers) Close() error {
	if !atomic.CompareAndSwapInt32(&h.closed, 0, 1) {
		return nil
	}
	h.socketsLock.Lock()
	defer h.socketsLock.Unlock()
	for ws := range h.sockets {
		delete(h.sockets, ws)
		ws.Close()
		// TODO: Sleep between disconnects.
	}
	return nil
}

func (h *SocketHandlers) hostPort() (host string, port int) {
	addr := h.listener.Addr().(*net.TCPAddr)
	if host = h.hostname; len(host) == 0 {
		host = addr.IP.String()
	}
	return host, addr.Port
}

func isSameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}
