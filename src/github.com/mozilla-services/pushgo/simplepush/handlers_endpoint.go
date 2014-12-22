/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
)

func NewEndpointHandlers() (h *EndpointHandlers) {
	h = new(EndpointHandlers)
	h.Closable.CloserOnce = h
	return h
}

type EndpointHandlersConfig struct {
	MaxDataLen int `toml:"max_data_len" env:"max_data_len"`
	Listener   ListenerConfig
}

type EndpointHandlers struct {
	Closable
	app        *Application
	logger     *SimpleLogger
	metrics    Statistician
	store      Store
	router     Router
	pinger     PropPinger
	balancer   Balancer
	hostname   string
	tokenKey   []byte
	listener   net.Listener
	server     *ServeCloser
	mux        *mux.Router
	url        string
	maxConns   int
	maxDataLen int
}

func (h *EndpointHandlers) ConfigStruct() interface{} {
	return &EndpointHandlersConfig{
		MaxDataLen: 4096,
		Listener: ListenerConfig{
			Addr:            ":8081",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
	}
}

func (h *EndpointHandlers) Init(app *Application, config interface{}) (err error) {
	conf := config.(*EndpointHandlersConfig)

	h.app = app
	h.logger = app.Logger()
	h.metrics = app.Metrics()
	h.store = app.Store()
	h.router = app.Router()
	h.pinger = app.PropPinger()
	h.balancer = app.Balancer()

	h.tokenKey = app.TokenKey()

	if h.listener, err = conf.Listener.Listen(); err != nil {
		h.logger.Panic("handlers_endpoint", "Could not attach update listener",
			LogFields{"error": err.Error()})
		return err
	}

	var scheme string
	if conf.Listener.UseTLS() {
		scheme = "https"
	} else {
		scheme = "http"
	}
	host, port := HostPort(h.listener, app)
	h.url = CanonicalURL(scheme, host, port)

	h.maxConns = conf.Listener.MaxConns
	h.maxDataLen = conf.MaxDataLen

	h.mux = mux.NewRouter()
	h.mux.HandleFunc("/update/{key}", h.UpdateHandler)

	h.server = NewServeCloser(&http.Server{
		ConnState: func(c net.Conn, state http.ConnState) {
			if state == http.StateNew {
				h.metrics.Increment("endpoint.socket.connect")
			} else if state == http.StateClosed {
				h.metrics.Increment("endpoint.socket.disconnect")
			}
		},
		Handler: &LogHandler{h.mux, h.logger},
		ErrorLog: log.New(&LogWriter{
			Logger: h.logger.Logger,
			Name:   "handlers_endpoint",
			Level:  ERROR,
		}, "", 0),
	})

	return nil
}

func (h *EndpointHandlers) Listener() net.Listener { return h.listener }
func (h *EndpointHandlers) MaxConns() int          { return h.maxConns }
func (h *EndpointHandlers) URL() string            { return h.url }
func (h *EndpointHandlers) ServeMux() *mux.Router  { return h.mux }

func (h *EndpointHandlers) Start(errChan chan<- error) {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_endpoint", "Starting update server",
			LogFields{"url": h.url})
	}
	errChan <- h.server.Serve(h.listener)
}

// -- REST
func (h *EndpointHandlers) UpdateHandler(resp http.ResponseWriter, req *http.Request) {
	// Handle the version updates.
	timer := time.Now()
	requestID := req.Header.Get(HeaderID)
	logWarning := h.logger.ShouldLog(WARNING)
	var (
		err        error
		version    int64
		uaid, chid string
	)

	defer func(err *error) {
		now := time.Now()
		ok := *err == nil
		if h.logger.ShouldLog(DEBUG) {
			h.logger.Debug("handlers_endpoint", "+++++++++++++ DONE +++",
				LogFields{"rid": requestID})
		}
		if h.logger.ShouldLog(INFO) {
			h.logger.Info("handlers_endpoint", "Client Update complete", LogFields{
				"rid":        requestID,
				"uaid":       uaid,
				"chid":       chid,
				"successful": strconv.FormatBool(ok)})
		}
		if ok {
			h.metrics.Timer("updates.handled", now.Sub(timer))
		}
	}(&err)

	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_endpoint", "Handling Update",
			LogFields{"rid": requestID})
	}

	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type",
			"application/x-www-form-urlencoded")
	}
	svers := req.FormValue("version")
	if svers != "" {
		if version, err = strconv.ParseInt(svers, 10, 64); err != nil || version < 0 {
			resp.Header().Set("Content-Type", "application/json")
			resp.WriteHeader(http.StatusBadRequest)
			resp.Write([]byte(`"Invalid Version"`))
			h.metrics.Increment("updates.appserver.invalid")
			return
		}
	} else {
		version = time.Now().UTC().Unix()
	}

	data := req.FormValue("data")
	if len(data) > h.maxDataLen {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Data too large, rejecting request",
				LogFields{"rid": requestID})
		}
		http.Error(resp, fmt.Sprintf("Data exceeds max length of %d bytes",
			h.maxDataLen), http.StatusRequestEntityTooLarge)
		h.metrics.Increment("updates.appserver.toolong")
		return
	}

	var pk string
	pk, ok := mux.Vars(req)["key"]
	// TODO:
	// is there a magic flag for proxyable endpoints?
	// e.g. update/p/gcm/LSoC or something?
	// (Note, this would allow us to use smarter FE proxies.)
	if !ok || len(pk) == 0 {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "No token, rejecting request",
				LogFields{"rid": requestID})
		}
		http.Error(resp, "Token not found", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	if tokenKey := h.tokenKey; len(tokenKey) > 0 {
		// Note: dumping the []uint8 keys can produce terminal glitches
		if h.logger.ShouldLog(DEBUG) {
			h.logger.Debug("handlers_endpoint", "Decoding...",
				LogFields{"rid": requestID})
		}
		var err error
		bpk, err := Decode(tokenKey, pk)
		if err != nil {
			if logWarning {
				h.logger.Warn("handlers_endpoint", "Could not decode primary key",
					LogFields{"rid": requestID, "pk": pk, "error": err.Error()})
			}
			http.Error(resp, "", http.StatusNotFound)
			h.metrics.Increment("updates.appserver.invalid")
			return
		}

		pk = string(bytes.TrimSpace(bpk))
	}

	if !validPK(pk) {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Invalid primary key for update",
				LogFields{"rid": requestID, "pk": pk})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	uaid, chid, ok = h.store.KeyToIDs(pk)
	if !ok {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Could not resolve primary key",
				LogFields{"rid": requestID, "pk": pk})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	if chid == "" {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Primary key missing channel ID",
				LogFields{"rid": requestID, "uaid": uaid})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	// At this point we should have a valid endpoint in the URL
	h.metrics.Increment("updates.appserver.incoming")

	// is there a Proprietary Ping for this?
	if h.pinger == nil {
		goto sendUpdate
	}
	if ok, err = h.pinger.Send(uaid, version, data); err != nil {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Could not send proprietary ping",
				LogFields{"rid": requestID, "uaid": uaid, "error": err.Error()})
		}
		goto sendUpdate
	}
	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	if ok && h.pinger.CanBypassWebsocket() {
		// Neat! Might as well return.
		h.metrics.Increment("updates.appserver.received")
		resp.Header().Set("Content-Type", "application/json")
		resp.Write([]byte("{}"))
		return
	}

sendUpdate:
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_endpoint", "setting version for ChannelID",
			LogFields{"rid": requestID, "uaid": uaid, "chid": chid,
				"version": strconv.FormatInt(version, 10)})
	}

	if err = h.store.Update(pk, version); err != nil {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Could not update channel", LogFields{
				"rid":     requestID,
				"uaid":    uaid,
				"chid":    chid,
				"version": strconv.FormatInt(version, 10),
				"error":   err.Error()})
		}
		status, _ := ErrToStatus(err)
		h.metrics.Increment("updates.appserver.error")
		http.Error(resp, "Could not update channel version", status)
		return
	}

	// Ping the appropriate server
	// Is this ours or should we punt to a different server?
	client, clientConnected := h.app.GetClient(uaid)
	if !clientConnected {
		// TODO: Move PropPinger here? otherwise it's connected?
		h.metrics.Increment("updates.routed.outgoing")
		var cancelSignal <-chan bool
		if cn, ok := resp.(http.CloseNotifier); ok {
			cancelSignal = cn.CloseNotify()
		}
		if err = h.router.Route(cancelSignal, uaid, chid, version, time.Now().UTC(), requestID, data); err != nil {
			resp.WriteHeader(http.StatusNotFound)
			resp.Write([]byte("false"))
			return
		}
	}

	if clientConnected {
		h.app.Server().RequestFlush(client, chid, int64(version), data) // TODO: Circular dependency.
		h.metrics.Increment("updates.appserver.received")
	}

	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	return
}

func (h *EndpointHandlers) CloseOnce() error {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_endpoint", "Closing update handler",
			LogFields{"url": h.url})
	}
	var errors MultipleError
	if err := h.listener.Close(); err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handlers_endpoint", "Error closing update listener",
				LogFields{"error": err.Error(), "url": h.url})
		}
		errors = append(errors, err)
	}
	if err := h.server.Close(); err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handlers_endpoint", "Error closing update server",
				LogFields{"error": err.Error(), "url": h.url})
		}
		errors = append(errors, err)
	}
	if len(errors) > 0 {
		return errors
	}
	return nil
}

func validPK(pk string) bool {
	for i := 0; i < len(pk); i++ {
		b := pk[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		// Accept bin64 && UUID encoding
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '_' && b != '.' && b != '=' && b != '-' {
			return false
		}
	}
	return true
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
