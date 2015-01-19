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

	"github.com/gorilla/mux"
)

func NewEndpointHandler() (h *EndpointHandler) {
	h = &EndpointHandler{mux: mux.NewRouter()}
	h.mux.HandleFunc("/update/{key}", h.UpdateHandler)
	return h
}

type EndpointHandlerConfig struct {
	MaxDataLen  int  `toml:"max_data_len" env:"max_data_len"`
	AlwaysRoute bool `toml:"always_route" env:"always_route"`
	Listener    ListenerConfig
}

type EndpointHandler struct {
	app         *Application
	logger      *SimpleLogger
	metrics     Statistician
	store       Store
	router      Router
	pinger      PropPinger
	balancer    Balancer
	hostname    string
	tokenKey    []byte
	listener    net.Listener
	server      *ServeCloser
	mux         *mux.Router
	url         string
	maxConns    int
	maxDataLen  int
	alwaysRoute bool
	closeOnce   Once
}

func (h *EndpointHandler) ConfigStruct() interface{} {
	return &EndpointHandlerConfig{
		MaxDataLen:  4096,
		AlwaysRoute: false,
		Listener: ListenerConfig{
			Addr:            ":8081",
			MaxConns:        1000,
			KeepAlivePeriod: "3m",
		},
	}
}

func (h *EndpointHandler) Init(app *Application, config interface{}) (err error) {
	conf := config.(*EndpointHandlerConfig)
	h.setApp(app)

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
	h.setMaxDataLen(conf.MaxDataLen)
	h.alwaysRoute = conf.AlwaysRoute

	return nil
}

func (h *EndpointHandler) Listener() net.Listener { return h.listener }
func (h *EndpointHandler) MaxConns() int          { return h.maxConns }
func (h *EndpointHandler) URL() string            { return h.url }
func (h *EndpointHandler) ServeMux() *mux.Router  { return h.mux }

// setApp sets the parent application for this endpoint handler.
func (h *EndpointHandler) setApp(app *Application) {
	h.app = app
	h.logger = app.Logger()
	h.metrics = app.Metrics()
	h.store = app.Store()
	h.router = app.Router()
	h.pinger = app.PropPinger()
	h.tokenKey = app.TokenKey()
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
			Logger: h.logger,
			Name:   "handlers_endpoint",
			Level:  ERROR,
		}, "", 0),
	})
}

// setMaxDataLen sets the maximum data length to v
func (h *EndpointHandler) setMaxDataLen(v int) {
	h.maxDataLen = v
}

func (h *EndpointHandler) Start(errChan chan<- error) {
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_endpoint", "Starting update server",
			LogFields{"url": h.url})
	}
	errChan <- h.server.Serve(h.listener)
}

func (h *EndpointHandler) decodePK(token string) (key string, err error) {
	if len(token) == 0 {
		return "", fmt.Errorf("Missing primary key")
	}
	if len(h.tokenKey) == 0 {
		return token, nil
	}
	bpk, err := Decode(h.tokenKey, token)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(bpk)), nil
}

func (h *EndpointHandler) resolvePK(token string) (uaid, chid string, err error) {
	pk, err := h.decodePK(token)
	if err != nil {
		err = fmt.Errorf("Error decoding primary key: %s", err)
		return "", "", err
	}
	if !validPK(pk) {
		err = fmt.Errorf("Invalid primary key: %q", pk)
		return "", "", err
	}
	if uaid, chid, err = h.store.KeyToIDs(pk); err != nil {
		return "", "", err
	}
	return uaid, chid, nil
}

func (h *EndpointHandler) doPropPing(uaid string, version int64, data string) (ok bool, err error) {
	if h.pinger == nil {
		return false, nil
	}
	if ok, err = h.pinger.Send(uaid, version, data); err != nil {
		return false, fmt.Errorf("Could not send proprietary ping: %s", err)
	}
	if !ok {
		return false, nil
	}
	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	return h.pinger.CanBypassWebsocket(), nil
}

// getUpdateParams extracts the update version and data from req.
func (h *EndpointHandler) getUpdateParams(req *http.Request) (version int64, data string, err error) {
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type",
			"application/x-www-form-urlencoded")
	}
	svers := req.FormValue("version")
	if svers != "" {
		if version, err = strconv.ParseInt(svers, 10, 64); err != nil || version < 0 {
			return 0, "", ErrBadVersion
		}
	} else {
		version = timeNow().UTC().Unix()
	}

	data = req.FormValue("data")
	if len(data) > h.maxDataLen {
		return 0, "", ErrDataTooLong
	}
	return
}

// -- REST
func (h *EndpointHandler) UpdateHandler(resp http.ResponseWriter, req *http.Request) {
	// Handle the version updates.
	timer := timeNow()
	requestID := req.Header.Get(HeaderID)
	logWarning := h.logger.ShouldLog(WARNING)
	var (
		err        error
		updateSent bool
		version    int64
		uaid, chid string
	)

	defer func() {
		now := timeNow()
		if h.logger.ShouldLog(DEBUG) {
			h.logger.Debug("handlers_endpoint", "+++++++++++++ DONE +++",
				LogFields{"rid": requestID})
		}
		if h.logger.ShouldLog(INFO) {
			h.logger.Info("handlers_endpoint", "Client Update complete", LogFields{
				"rid":        requestID,
				"uaid":       uaid,
				"chid":       chid,
				"successful": strconv.FormatBool(updateSent)})
		}
		if updateSent {
			h.metrics.Timer("updates.handled", now.Sub(timer))
		}
	}()

	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_endpoint", "Handling Update",
			LogFields{"rid": requestID})
	}

	if req.Method != "PUT" {
		writeJSON(resp, http.StatusMethodNotAllowed, []byte(`"Method Not Allowed"`))
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	version, data, err := h.getUpdateParams(req)
	if err != nil {
		if err == ErrDataTooLong {
			if logWarning {
				h.logger.Warn("handlers_endpoint", "Data too large, rejecting request",
					LogFields{"rid": requestID})
			}
			writeJSON(resp, http.StatusRequestEntityTooLarge, []byte(fmt.Sprintf(
				`"Data exceeds max length of %d bytes"`, h.maxDataLen)))
			h.metrics.Increment("updates.appserver.toolong")
			return
		}
		writeJSON(resp, http.StatusBadRequest, []byte(`"Invalid Version"`))
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	// TODO:
	// is there a magic flag for proxyable endpoints?
	// e.g. update/p/gcm/LSoC or something?
	// (Note, this would allow us to use smarter FE proxies.)
	token := mux.Vars(req)["key"]
	if uaid, chid, err = h.resolvePK(token); err != nil {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Invalid primary key for update",
				LogFields{"error": err.Error(), "rid": requestID, "token": token})
		}
		writeJSON(resp, http.StatusNotFound, []byte(`"Invalid Token"`))
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	// At this point we should have a valid endpoint in the URL
	h.metrics.Increment("updates.appserver.incoming")

	// is there a Proprietary Ping for this?
	updateSent, err = h.doPropPing(uaid, version, data)
	if err != nil {
		if logWarning {
			h.logger.Warn("handlers_endpoint", "Could not send proprietary ping",
				LogFields{"rid": requestID, "uaid": uaid, "error": err.Error()})
		}
	} else if updateSent {
		// Neat! Might as well return.
		h.metrics.Increment("updates.appserver.received")
		writeSuccess(resp)
		return
	}

	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handlers_endpoint", "setting version for ChannelID",
			LogFields{"rid": requestID, "uaid": uaid, "chid": chid,
				"version": strconv.FormatInt(version, 10)})
	}

	if err = h.store.Update(uaid, chid, version); err != nil {
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
		writeJSON(resp, status, []byte(`"Could not update channel version"`))
		return
	}

	cn, _ := resp.(http.CloseNotifier)
	if !h.deliver(cn, uaid, chid, version, requestID, data) {
		writeJSON(resp, http.StatusNotFound, []byte("false"))
		return
	}

	writeSuccess(resp)
	updateSent = true
	return
}

// deliver routes an incoming update to the appropriate server.
func (h *EndpointHandler) deliver(cn http.CloseNotifier, uaid, chid string,
	version int64, requestID string, data string) (ok bool) {

	w, workerConnected := h.app.GetWorker(uaid)
	// Always route to other servers first, in case we're holding open a stale
	// connection and the client has already reconnected to a different server.
	if h.alwaysRoute || !workerConnected {
		h.metrics.Increment("updates.routed.outgoing")
		// Abort routing if the connection goes away.
		var cancelSignal <-chan bool
		if cn != nil {
			cancelSignal = cn.CloseNotify()
		}
		// Route the update.
		ok, _ = h.router.Route(cancelSignal, uaid, chid, version,
			timeNow().UTC(), requestID, data)
		if ok {
			return true
		}
	}
	// If the device is not connected to this server, indicate whether routing
	// was successful.
	if !workerConnected {
		return
	}
	// Try local delivery if routing failed.
	err := h.app.Server().RequestFlush(w, chid, version, data)
	if err != nil {
		h.metrics.Increment("updates.appserver.rejected")
		return false
	}
	h.metrics.Increment("updates.appserver.received")
	return true
}

func (h *EndpointHandler) Close() error {
	return h.closeOnce.Do(h.close)
}

func (h *EndpointHandler) close() error {
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

func writeJSON(resp http.ResponseWriter, status int, data []byte) {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(status)
	resp.Write(data)
}

func writeSuccess(resp http.ResponseWriter) {
	writeJSON(resp, http.StatusOK, []byte("{}"))
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
