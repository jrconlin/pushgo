/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/net/websocket"
)

type HandlerConfig struct {
	Origins    []string
	MaxDataLen int `toml:"max_data_len" env:"max_data_len"`
}

type Handler struct {
	app         *Application
	logger      *SimpleLogger
	store       Store
	router      Router
	balancer    Balancer
	metrics     Statistician
	origins     []*url.URL
	tokenKey    []byte
	propping    PropPinger
	maxDataLen  int
	clientMux   *mux.Router
	endpointMux *mux.Router
}

type StatusReport struct {
	Healthy          bool         `json:"ok"`
	Clients          int          `json:"clientCount"`
	MaxClientConns   int          `json:"maxClients"`
	MaxEndpointConns int          `json:"maxEndpointConns"`
	Store            PluginStatus `json:"store"`
	Pinger           PluginStatus `json:"pinger"`
	Locator          PluginStatus `json:"locator"`
	Balancer         PluginStatus `json:"balancer"`
	Goroutines       int          `json:"goroutines"`
	Version          string       `json:"version"`
}

type PluginStatus struct {
	Healthy bool  `json:"ok"`
	Error   error `json:"error,omitempty"`
}

func (h *Handler) ConfigStruct() interface{} {
	return &HandlerConfig{
		MaxDataLen: 4096,
	}
}

func (h *Handler) Init(app *Application, config interface{}) (err error) {
	h.app = app
	h.logger = app.Logger()
	h.store = app.Store()
	h.metrics = app.Metrics()
	h.router = app.Router()
	h.balancer = app.Balancer()
	h.tokenKey = app.TokenKey()
	h.SetPropPinger(app.PropPinger())
	h.maxDataLen = config.(*HandlerConfig).MaxDataLen

	h.clientMux = mux.NewRouter()
	h.clientMux.HandleFunc("/status/", h.StatusHandler)
	h.clientMux.HandleFunc("/realstatus/", h.RealStatusHandler)
	h.clientMux.Handle("/", websocket.Server{Handler: h.PushSocketHandler,
		Handshake: h.checkOrigin})

	h.endpointMux = mux.NewRouter()
	h.endpointMux.HandleFunc("/update/{key}", h.UpdateHandler)
	h.endpointMux.HandleFunc("/status/", h.StatusHandler)
	h.endpointMux.HandleFunc("/realstatus/", h.RealStatusHandler)
	h.endpointMux.HandleFunc("/metrics/", h.MetricsHandler)

	conf := config.(*HandlerConfig)
	if len(conf.Origins) > 0 {
		h.origins = make([]*url.URL, len(conf.Origins))
		for index, origin := range conf.Origins {
			if h.origins[index], err = url.ParseRequestURI(origin); err != nil {
				return fmt.Errorf("Error parsing origin: %s", err)
			}
		}
	}

	return
}

func (h *Handler) Start(errChan chan<- error) {
	server := h.app.Server()

	go func() {
		clientLn := server.ClientListener()
		if h.logger.ShouldLog(INFO) {
			h.logger.Info("app", "Starting WebSocket server",
				LogFields{"addr": clientLn.Addr().String()})
		}
		clientSrv := &http.Server{
			Handler:  &LogHandler{h.clientMux, h.logger},
			ErrorLog: log.New(&LogWriter{h.logger.Logger, "worker", ERROR}, "", 0)}
		errChan <- clientSrv.Serve(clientLn)
	}()

	go func() {
		endpointLn := server.EndpointListener()
		if h.logger.ShouldLog(INFO) {
			h.logger.Info("app", "Starting update server",
				LogFields{"addr": endpointLn.Addr().String()})
		}
		endpointSrv := &http.Server{
			ConnState: func(c net.Conn, state http.ConnState) {
				if state == http.StateNew {
					h.metrics.Increment("endpoint.socket.connect")
				} else if state == http.StateClosed {
					h.metrics.Increment("endpoint.socket.disconnect")
				}
			},
			Handler:  &LogHandler{h.endpointMux, h.logger},
			ErrorLog: log.New(&LogWriter{h.logger.Logger, "endpoint", ERROR}, "", 0)}
		errChan <- endpointSrv.Serve(endpointLn)
	}()
}

func (h *Handler) checkOrigin(conf *websocket.Config, req *http.Request) (err error) {
	if len(h.origins) == 0 {
		return nil
	}
	if conf.Origin, err = websocket.Origin(conf, req); err != nil {
		if h.logger.ShouldLog(WARNING) {
			h.logger.Warn("http", "Error parsing WebSocket origin",
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
		h.logger.Warn("http", "Rejected WebSocket connection from unknown origin",
			LogFields{"rid": req.Header.Get(HeaderID), "origin": conf.Origin.String()})
	}
	return ErrInvalidOrigin
}

func (h *Handler) MetricsHandler(resp http.ResponseWriter, req *http.Request) {
	snapshot := h.metrics.Snapshot()
	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(snapshot)
	if err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handler", "Could not generate metrics report",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		resp.WriteHeader(http.StatusServiceUnavailable)
		resp.Write([]byte("{}"))
		return
	}
	if reply == nil {
		reply = []byte("{}")
	}
	resp.Write(reply)
}

// VIP response
func (h *Handler) StatusHandler(resp http.ResponseWriter,
	req *http.Request) {
	reply := []byte(fmt.Sprintf(`{"status":"OK","clients":%d,"version":"%s"}`,
		h.app.ClientCount(), VERSION))

	resp.Header().Set("Content-Type", "application/json")
	resp.Write(reply)
}

func (h *Handler) RealStatusHandler(resp http.ResponseWriter,
	req *http.Request) {

	status := StatusReport{
		MaxClientConns:   h.app.Server().MaxClientConns(),
		MaxEndpointConns: h.app.Server().MaxEndpointConns(),
		Version:          VERSION,
	}

	status.Store.Healthy, status.Store.Error = h.store.Status()
	if pinger := h.PropPinger(); pinger != nil {
		status.Pinger.Healthy, status.Pinger.Error = pinger.Status()
	}
	if locator := h.router.Locator(); locator != nil {
		status.Locator.Healthy, status.Locator.Error = locator.Status()
	}
	if balancer := h.balancer; balancer != nil {
		status.Balancer.Healthy, status.Balancer.Error = balancer.Status()
	}

	status.Healthy = status.Store.Healthy && status.Pinger.Healthy &&
		status.Locator.Healthy && status.Balancer.Healthy

	status.Clients = h.app.ClientCount()
	status.Goroutines = runtime.NumGoroutine()

	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(status)
	if err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handler", "Could not generate status report",
				LogFields{"error": err.Error()})
		}
		resp.WriteHeader(http.StatusServiceUnavailable)
		resp.Write([]byte("{}"))
		return
	}

	if !status.Healthy {
		resp.WriteHeader(http.StatusServiceUnavailable)
	}
	resp.Write(reply)
}

// -- REST
func (h *Handler) UpdateHandler(resp http.ResponseWriter, req *http.Request) {
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
			h.logger.Debug("update", "+++++++++++++ DONE +++",
				LogFields{"rid": requestID})
		}
		if h.logger.ShouldLog(INFO) {
			h.logger.Info("dash", "Client Update complete", LogFields{
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
		h.logger.Info("update", "Handling Update",
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
			h.logger.Warn("update", "Data too large, rejecting request",
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
			h.logger.Warn("update", "No token, rejecting request",
				LogFields{"rid": requestID})
		}
		http.Error(resp, "Token not found", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	if tokenKey := h.tokenKey; len(tokenKey) > 0 {
		// Note: dumping the []uint8 keys can produce terminal glitches
		if h.logger.ShouldLog(DEBUG) {
			h.logger.Debug("main", "Decoding...",
				LogFields{"rid": requestID})
		}
		var err error
		bpk, err := Decode(tokenKey, pk)
		if err != nil {
			if logWarning {
				h.logger.Warn("update", "Could not decode primary key", LogFields{
					"rid": requestID, "pk": pk, "error": err.Error()})
			}
			http.Error(resp, "", http.StatusNotFound)
			h.metrics.Increment("updates.appserver.invalid")
			return
		}

		pk = string(bytes.TrimSpace(bpk))
	}

	if !validPK(pk) {
		if logWarning {
			h.logger.Warn("update", "Invalid primary key for update",
				LogFields{"rid": requestID, "pk": pk})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	uaid, chid, ok = h.store.KeyToIDs(pk)
	if !ok {
		if logWarning {
			h.logger.Warn("update", "Could not resolve primary key",
				LogFields{"rid": requestID, "pk": pk})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	if chid == "" {
		if logWarning {
			h.logger.Warn("update", "Primary key missing channel ID",
				LogFields{"rid": requestID, "uaid": uaid})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		h.metrics.Increment("updates.appserver.invalid")
		return
	}

	// At this point we should have a valid endpoint in the URL
	h.metrics.Increment("updates.appserver.incoming")

	// is there a Proprietary Ping for this?
	pinger := h.PropPinger()
	if pinger == nil {
		goto sendUpdate
	}
	if ok, err = pinger.Send(uaid, version, data); err != nil {
		if logWarning {
			h.logger.Warn("update", "Could not send proprietary ping", LogFields{
				"rid": requestID, "uaid": uaid, "error": err.Error()})
		}
		goto sendUpdate
	}
	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	if ok && pinger.CanBypassWebsocket() {
		// Neat! Might as well return.
		h.metrics.Increment("updates.appserver.received")
		resp.Header().Set("Content-Type", "application/json")
		resp.Write([]byte("{}"))
		return
	}

sendUpdate:
	if h.logger.ShouldLog(INFO) {
		h.logger.Info("update", "setting version for ChannelID", LogFields{
			"rid":     requestID,
			"uaid":    uaid,
			"chid":    chid,
			"version": strconv.FormatInt(version, 10)})
	}

	if err = h.store.Update(pk, version); err != nil {
		if logWarning {
			h.logger.Warn("update", "Could not update channel", LogFields{
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
	if h.app.AlwaysRoute || !clientConnected {
		// TODO: Move PropPinger here? otherwise it's connected?
		h.metrics.Increment("updates.routed.outgoing")
		var cancelSignal <-chan bool
		if cn, ok := resp.(http.CloseNotifier); ok {
			cancelSignal = cn.CloseNotify()
		}
		err = h.router.Route(cancelSignal, uaid, chid, version,
			time.Now().UTC(), requestID, data)
		if err != nil && !clientConnected {
			resp.WriteHeader(http.StatusNotFound)
			resp.Write([]byte("false"))
			return
		}
	}

	if clientConnected {
		h.app.Server().RequestFlush(client, chid, int64(version), data)
		h.metrics.Increment("updates.appserver.received")
	}

	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	return
}

func (h *Handler) PushSocketHandler(ws *websocket.Conn) {
	requestID := ws.Request().Header.Get(HeaderID)
	sock := PushWS{Socket: ws,
		Store:  h.store,
		Logger: h.logger,
		Born:   time.Now()}

	if h.logger.ShouldLog(INFO) {
		h.logger.Info("handler", "websocket connection",
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
		h.logger.Info("main", "Server for client shut-down",
			LogFields{"rid": requestID})
	}
}

func (r *Handler) SetPropPinger(ping PropPinger) (err error) {
	r.propping = ping
	return
}

func (r *Handler) PropPinger() PropPinger {
	return r.propping
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
