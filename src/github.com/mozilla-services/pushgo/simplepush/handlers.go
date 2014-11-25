/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"time"

	capn "github.com/glycerine/go-capnproto"
	"github.com/gorilla/mux"
	"golang.org/x/net/websocket"
)

type HandlerConfig struct {
	MaxDataLen int `toml:"max_data_len" env:"max_data_len"`
}

type Handler struct {
	app        *Application
	logger     *SimpleLogger
	store      Store
	router     *Router
	metrics    Statistician
	tokenKey   []byte
	propping   PropPinger
	maxDataLen int
}

type StatusReport struct {
	Healthy          bool         `json:"ok"`
	Clients          int          `json:"clientCount"`
	MaxClientConns   int          `json:"maxClients"`
	MaxEndpointConns int          `json:"maxEndpointConns"`
	Store            PluginStatus `json:"store"`
	Pinger           PluginStatus `json:"pinger"`
	Locator          PluginStatus `json:"locator"`
	Goroutines       int          `json:"goroutines"`
	Version          string       `json:"version"`
}

type PluginStatus struct {
	Healthy bool  `json:"ok"`
	Error   error `json:"error,omitempty"`
}

func (self *Handler) ConfigStruct() interface{} {
	return &HandlerConfig{
		MaxDataLen: 1024,
	}
}

func (self *Handler) Init(app *Application, config interface{}) error {
	self.app = app
	self.logger = app.Logger()
	self.store = app.Store()
	self.metrics = app.Metrics()
	self.router = app.Router()
	self.tokenKey = app.TokenKey()
	self.SetPropPinger(app.PropPinger())
	self.maxDataLen = config.(*HandlerConfig).MaxDataLen
	return nil
}

func (self *Handler) MetricsHandler(resp http.ResponseWriter, req *http.Request) {
	snapshot := self.metrics.Snapshot()
	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(snapshot)
	if err != nil {
		if self.logger.ShouldLog(ERROR) {
			self.logger.Error("handler", "Could not generate metrics report",
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
func (self *Handler) StatusHandler(resp http.ResponseWriter,
	req *http.Request) {
	reply := []byte(fmt.Sprintf(`{"status":"OK","clients":%d,"version":"%s"}`,
		self.app.ClientCount(), VERSION))

	resp.Header().Set("Content-Type", "application/json")
	resp.Write(reply)
}

func (self *Handler) RealStatusHandler(resp http.ResponseWriter,
	req *http.Request) {

	status := StatusReport{
		MaxClientConns:   self.app.Server().MaxClientConns(),
		MaxEndpointConns: self.app.Server().MaxEndpointConns(),
		Version:          VERSION,
	}

	status.Store.Healthy, status.Store.Error = self.store.Status()
	if pinger := self.PropPinger(); pinger != nil {
		status.Pinger.Healthy, status.Pinger.Error = pinger.Status()
	}
	if locator := self.router.Locator(); locator != nil {
		status.Locator.Healthy, status.Locator.Error = locator.Status()
	}

	status.Healthy = status.Store.Healthy && status.Pinger.Healthy &&
		status.Locator.Healthy

	status.Clients = self.app.ClientCount()
	status.Goroutines = runtime.NumGoroutine()

	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(status)
	if err != nil {
		if self.logger.ShouldLog(ERROR) {
			self.logger.Error("handler", "Could not generate status report",
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
func (self *Handler) UpdateHandler(resp http.ResponseWriter, req *http.Request) {
	// Handle the version updates.
	timer := time.Now()
	requestID := req.Header.Get(HeaderID)
	logWarning := self.logger.ShouldLog(WARNING)
	var (
		err        error
		version    int64
		uaid, chid string
	)

	defer func(err *error) {
		now := time.Now()
		ok := *err == nil
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("update", "+++++++++++++ DONE +++",
				LogFields{"rid": requestID})
		}
		if self.logger.ShouldLog(INFO) {
			self.logger.Info("dash", "Client Update complete", LogFields{
				"rid":        requestID,
				"uaid":       uaid,
				"chid":       chid,
				"successful": strconv.FormatBool(ok)})
		}
		if ok {
			self.metrics.Timer("updates.handled", now.Sub(timer))
		}
	}(&err)

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("update", "Handling Update",
			LogFields{"rid": requestID})
	}

	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	svers := req.FormValue("version")
	if svers != "" {
		if version, err = strconv.ParseInt(svers, 10, 64); err != nil || version < 0 {
			resp.Header().Set("Content-Type", "application/json")
			resp.WriteHeader(http.StatusBadRequest)
			resp.Write([]byte(`"Invalid Version"`))
			self.metrics.Increment("updates.appserver.invalid")
			return
		}
	} else {
		version = time.Now().UTC().Unix()
	}

	data := req.FormValue("data")
	if len(data) > self.maxDataLen {
		if logWarning {
			self.logger.Warn("update", "Data too large, rejecting request",
				LogFields{"rid": requestID})
		}
		http.Error(resp, fmt.Sprintf("Data exceeds max length of %d bytes",
			self.maxDataLen), http.StatusRequestEntityTooLarge)
		self.metrics.Increment("updates.appserver.toolong")
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
			self.logger.Warn("update", "No token, rejecting request",
				LogFields{"rid": requestID})
		}
		http.Error(resp, "Token not found", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	if tokenKey := self.tokenKey; len(tokenKey) > 0 {
		// Note: dumping the []uint8 keys can produce terminal glitches
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("main", "Decoding...",
				LogFields{"rid": requestID})
		}
		var err error
		bpk, err := Decode(tokenKey, pk)
		if err != nil {
			if logWarning {
				self.logger.Warn("update", "Could not decode primary key", LogFields{
					"rid": requestID, "pk": pk, "error": err.Error()})
			}
			http.Error(resp, "", http.StatusNotFound)
			self.metrics.Increment("updates.appserver.invalid")
			return
		}

		pk = string(bytes.TrimSpace(bpk))
	}

	if !validPK(pk) {
		if logWarning {
			self.logger.Warn("update", "Invalid primary key for update",
				LogFields{"rid": requestID, "pk": pk})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	uaid, chid, ok = self.store.KeyToIDs(pk)
	if !ok {
		if logWarning {
			self.logger.Warn("update", "Could not resolve primary key",
				LogFields{"rid": requestID, "pk": pk})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	if chid == "" {
		if logWarning {
			self.logger.Warn("update", "Primary key missing channel ID",
				LogFields{"rid": requestID, "uaid": uaid})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	// At this point we should have a valid endpoint in the URL
	self.metrics.Increment("updates.appserver.incoming")

	// is there a Proprietary Ping for this?
	pinger := self.PropPinger()
	if pinger == nil {
		goto sendUpdate
	}
	if ok, err = pinger.Send(uaid, version); err != nil {
		if logWarning {
			self.logger.Warn("update", "Could not send proprietary ping", LogFields{
				"rid": requestID, "uaid": uaid, "error": err.Error()})
		}
		goto sendUpdate
	}
	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	if ok && pinger.CanBypassWebsocket() {
		// Neat! Might as well return.
		self.metrics.Increment("updates.appserver.received")
		resp.Header().Set("Content-Type", "application/json")
		resp.Write([]byte("{}"))
		return
	}

sendUpdate:
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("update", "setting version for ChannelID", LogFields{
			"rid":     requestID,
			"uaid":    uaid,
			"chid":    chid,
			"version": strconv.FormatInt(version, 10)})
	}

	if err = self.store.Update(pk, version); err != nil {
		if logWarning {
			self.logger.Warn("update", "Could not update channel", LogFields{
				"rid":     requestID,
				"uaid":    uaid,
				"chid":    chid,
				"version": strconv.FormatInt(version, 10),
				"error":   err.Error()})
		}
		status, _ := ErrToStatus(err)
		self.metrics.Increment("updates.appserver.error")
		http.Error(resp, "Could not update channel version", status)
		return
	}

	// Ping the appropriate server
	// Is this ours or should we punt to a different server?
	client, clientConnected := self.app.GetClient(uaid)
	if !clientConnected {
		// TODO: Move PropPinger here? otherwise it's connected?
		self.metrics.Increment("updates.routed.outgoing")
		var cancelSignal <-chan bool
		if cn, ok := resp.(http.CloseNotifier); ok {
			cancelSignal = cn.CloseNotify()
		}
		if err = self.router.Route(cancelSignal, uaid, chid, version, time.Now().UTC(), requestID, data); err != nil {
			resp.WriteHeader(http.StatusNotFound)
			resp.Write([]byte("false"))
			return
		}
	}

	if clientConnected {
		self.app.Server().RequestFlush(client, chid, int64(version), data)
		self.metrics.Increment("updates.appserver.received")
	}

	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	return
}

func (self *Handler) PushSocketHandler(ws *websocket.Conn) {
	requestID := ws.Request().Header.Get(HeaderID)
	sock := PushWS{Uaid: "",
		Socket: ws,
		Store:  self.store,
		Logger: self.logger,
		Born:   time.Now()}

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("handler", "websocket connection",
			LogFields{"rid": requestID})
	}
	defer func() {
		now := time.Now()
		// Clean-up the resources
		self.app.Server().HandleCommand(PushCommand{DIE, nil}, &sock)
		self.metrics.Timer("socket.lifespan", now.Sub(sock.Born))
		self.metrics.Increment("socket.disconnect")
	}()

	self.metrics.Increment("socket.connect")

	NewWorker(self.app, requestID).Run(&sock)
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("main", "Server for client shut-down",
			LogFields{"rid": requestID})
	}
}

// Boy Golly, sure would be nice to put this in router.go, huh?
// well, thanks to go being picky about circular references, you can't.
func (self *Handler) RouteHandler(resp http.ResponseWriter, req *http.Request) {
	var err error
	logWarning := self.logger.ShouldLog(WARNING)
	// get the uaid from the url
	uaid, ok := mux.Vars(req)["uaid"]
	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		self.metrics.Increment("updates.routed.invalid")
		return
	}
	// if uid is not present, or doesn't exist in the known clients...
	if !ok || !self.app.ClientExists(uaid) {
		http.Error(resp, "UID Not Found", http.StatusNotFound)
		self.metrics.Increment("updates.routed.unknown")
		return
	}
	// We know of this one.
	var (
		r        Routable
		chid     string
		timeNano int64
		sentAt   time.Time
		data     string
	)
	segment, err := capn.ReadFromStream(req.Body, nil)
	if err != nil {
		if logWarning {
			self.logger.Warn("router", "Could not read update body",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		goto invalidBody
	}
	r = ReadRootRoutable(segment)
	chid = r.ChannelID()
	if len(chid) == 0 {
		if logWarning {
			self.logger.Warn("router", "Missing channel ID",
				LogFields{"rid": req.Header.Get(HeaderID), "uaid": uaid})
		}
		goto invalidBody
	}
	// routed data is already in storage.
	self.metrics.Increment("updates.routed.incoming")
	timeNano = r.Time()
	sentAt = time.Unix(timeNano/1e9, timeNano%1e9)
	// Never trust external data
	data = r.Data()
	if len(data) > self.maxDataLen {
		if logWarning {
			self.logger.Warn("router", "Data segment too long, truncating",
				LogFields{"rid": req.Header.Get(HeaderID),
					"uaid": uaid})
		}
		data = data[:self.maxDataLen]
	}
	if err = self.app.Server().Update(chid, uaid, r.Version(), sentAt, data); err != nil {
		if logWarning {
			self.logger.Warn("router", "Could not update local user",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		http.Error(resp, "Server Error", http.StatusInternalServerError)
		self.metrics.Increment("updates.routed.error")
		return
	}
	resp.Write([]byte("Ok"))
	self.metrics.Increment("updates.routed.received")
	return

invalidBody:
	http.Error(resp, "Invalid body", http.StatusNotAcceptable)
	self.metrics.Increment("updates.routed.invalid")
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
