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
	"runtime/debug"
	"strconv"
	"time"

	"code.google.com/p/go.net/websocket"
	"github.com/gorilla/mux"

	"github.com/mozilla-services/pushgo/simplepush/sperrors"
)

type HandlerConfig struct{}

type Handler struct {
	app      *Application
	logger   *SimpleLogger
	store    Store
	router   *Router
	metrics  *Metrics
	tokenKey []byte
	propping PropPinger
}

type ServerStatus struct {
	Healthy      bool   `json:"ok"`
	Clients      int    `json:"clientCount"`
	MaxClients   int    `json:"maxClients"`
	StoreHealthy bool   `json:"mcstatus"`
	Goroutines   int    `json:"goroutines"`
	Error        string `json:"error,omitempty"`
	Message      string `json:"message,omitempty"`
}

func (self *Handler) ConfigStruct() interface{} {
	return &HandlerConfig{}
}

func (self *Handler) Init(app *Application, config interface{}) error {
	self.app = app
	self.logger = app.Logger()
	self.store = app.Store()
	self.metrics = app.Metrics()
	self.router = app.Router()
	self.tokenKey = app.TokenKey()
	self.SetPropPinger(app.PropPinger())
	return nil
}

func (self *Handler) MetricsHandler(resp http.ResponseWriter, req *http.Request) {
	snapshot := self.metrics.Snapshot()
	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(snapshot)
	if err != nil {
		self.logger.Error("handler", "Could not generate metrics report",
			LogFields{"error": err.Error()})
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
	// TODO: make sure all is well.
	reply := []byte(fmt.Sprintf(`{"status":"OK","clients":%d}`,
		self.app.ClientCount()))

	resp.Header().Set("Content-Type", "application/json")
	resp.Write(reply)
}

func (self *Handler) RealStatusHandler(resp http.ResponseWriter,
	req *http.Request) {
	var msg string

	ok, err := self.store.Status()
	if !ok {
		msg += fmt.Sprintf("Storage error: %s,", err)
	}
	status := &ServerStatus{
		Healthy:      ok,
		Clients:      self.app.ClientCount(),
		StoreHealthy: ok,
		Goroutines:   runtime.NumGoroutine(),
		Message:      msg,
	}
	if err != nil {
		status.Error = err.Error()
	}
	reply, err := json.Marshal(status)

	resp.Header().Set("Content-Type", "application/json")
	if !ok {
		resp.WriteHeader(http.StatusServiceUnavailable)
	}
	resp.Write(reply)
}

// -- REST
func (self *Handler) UpdateHandler(resp http.ResponseWriter, req *http.Request) {
	// Handle the version updates.
	timer := time.Now()
	var (
		err        error
		version    int64
		uaid, chid string
	)

	defer func(err *error) {
		now := time.Now()
		ok := *err == nil
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("update", "+++++++++++++ DONE +++", nil)
		}
		if len(uaid) > 0 && len(chid) > 0 && self.logger.ShouldLog(INFO) {
			self.logger.Info("dash", "Client Update complete",
				LogFields{
					"uaid":       uaid,
					"path":       req.URL.Path,
					"channelID":  chid,
					"successful": strconv.FormatBool(ok),
					"duration":   strconv.FormatInt(int64(now.Sub(timer)), 10)})
		}
		if ok {
			self.metrics.Timer("updates.handled", now.Sub(timer))
		}
	}(&err)

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("update", "Handling Update",
			LogFields{"path": req.URL.Path})
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

	// elements := strings.Split(req.URL.Path, "/")
	// pk := elements[len(elements)-1]
	var pk string
	pk, ok := mux.Vars(req)["key"]
	// TODO:
	// is there a magic flag for proxyable endpoints?
	// e.g. update/p/gcm/LSoC or something?
	// (Note, this would allow us to use smarter FE proxies.)
	if !ok || len(pk) == 0 {
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("update", "No token, rejecting request",
				LogFields{"remoteAddr": req.RemoteAddr,
					"path": req.URL.Path})
		}
		http.Error(resp, "Token not found", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	if token := self.tokenKey; len(token) > 0 {
		// Note: dumping the []uint8 keys can produce terminal glitches
		self.logger.Debug("main", "Decoding...", nil)
		var err error
		bpk, err := Decode(token, pk)
		if err != nil {
			self.logger.Debug("update",
				"Could not decode token",
				LogFields{"primarykey": pk,
					"remoteAddr": req.RemoteAddr,
					"path":       req.URL.Path,
					"error":      ErrStr(err)})
			http.Error(resp, "", http.StatusNotFound)
			self.metrics.Increment("updates.appserver.invalid")
			return
		}

		pk = string(bytes.TrimSpace(bpk))
	}

	if !validPK(pk) {
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("update",
				"Invalid token for update",
				LogFields{"token": pk,
					"path": req.URL.Path})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	uaid, chid, ok = self.store.KeyToIDs(pk)
	if !ok {
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("update",
				"Could not resolve PK",
				LogFields{"primaryKey": pk,
					"path": req.URL.Path})
		}
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	if chid == "" {
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("update",
				"Incomplete primary key",
				LogFields{"uaid": uaid,
					"channelID":  chid,
					"remoteAddr": req.RemoteAddr})
		}
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	// Is this ours or should we punt to a different server?
	if !self.app.ClientExists(uaid) {
		// TODO: Move PropPinger here? otherwise it's connected?
		self.metrics.Increment("updates.routed.outgoing")
		resp.Header().Set("Content-Type", "application/json")
		if err = self.router.SendUpdate(uaid, chid, version, time.Now().UTC()); err == nil {
			resp.Write([]byte("{}"))
		} else {
			resp.WriteHeader(http.StatusNotFound)
			resp.Write([]byte("false"))
		}
		return
	}

	// At this point we should have a valid endpoint in the URL
	self.metrics.Increment("updates.appserver.incoming")

	// is there a Proprietary Ping for this?
	if self.propping == nil {
		goto sendUpdate
	}
	if ok, err = self.propping.Send(uaid, version); err != nil {
		self.logger.Warn("update",
			"Could not generate Proprietary Ping",
			LogFields{"error": err.Error(),
				"uaid": uaid})
		goto sendUpdate
	}
	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	if ok && self.propping.CanBypassWebsocket() {
		// Neat! Might as well return.
		self.metrics.Increment("updates.appserver.received")
		resp.Header().Set("Content-Type", "application/json")
		resp.Write([]byte("{}"))
		return
	}

sendUpdate:
	if self.logger.ShouldLog(INFO) {
		self.logger.Info("update",
			"setting version for ChannelID",
			LogFields{"uaid": uaid, "channelID": chid,
				"version": strconv.FormatInt(version, 10)})
	}

	if err = self.store.Update(pk, version); err != nil {
		self.logger.Warn("update", "Could not update channel",
			LogFields{"UAID": uaid,
				"channelID": chid,
				"version":   strconv.FormatInt(version, 10),
				"error":     err.Error()})
		status, _ := sperrors.ErrToStatus(err)
		self.metrics.Increment("updates.appserver.error")
		http.Error(resp, "Could not update channel version", status)
		return
	}
	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	self.metrics.Increment("updates.appserver.received")
	// Ping the appropriate server
	if client, ok := self.app.GetClient(uaid); ok {
		self.app.Server().RequestFlush(client, chid, int64(version))
	}
	return
}

func (self *Handler) PushSocketHandler(ws *websocket.Conn) {
	sock := PushWS{Uaid: "",
		Socket: ws,
		Store:  self.store,
		Logger: self.logger,
		Born:   time.Now()}

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("handler", "websocket connection", LogFields{
			"UserAgent":  ws.Request().Header.Get("User-Agent"),
			"URI":        ws.Request().RequestURI,
			"RemoteAddr": ws.Request().RemoteAddr,
		})
	}
	defer func() {
		now := time.Now()
		if r := recover(); r != nil {
			debug.PrintStack()
			self.logger.Error("main", "Unknown error",
				LogFields{"error": r.(error).Error()})
		}
		// Clean-up the resources
		self.app.Server().HandleCommand(PushCommand{DIE, nil}, &sock)
		self.metrics.Timer("socket.lifespan", now.Sub(sock.Born))
		self.metrics.Increment("socket.disconnect")
	}()

	self.metrics.Increment("socket.connect")

	NewWorker(self.app).Run(&sock)

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("main", "Server for client shut-down", nil)
	}
}

// Boy Golly, sure would be nice to put this in router.go, huh?
// well, thanks to go being picky about circular references, you can't.
func (self *Handler) RouteHandler(resp http.ResponseWriter, req *http.Request) {
	var (
		err error
		ts  time.Time
	)
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
	if req.ContentLength < 1 {
		self.logger.Warn("router", "Routed update contained no body",
			LogFields{"uaid": uaid})
		http.Error(resp, "Missing body", http.StatusNotAcceptable)
		self.metrics.Increment("updates.routed.invalid")
		return
	}
	defer req.Body.Close()
	decoder := json.NewDecoder(req.Body)
	request := new(Routable)
	if err = decoder.Decode(request); err != nil {
		self.logger.Error("router",
			"Could not read update body",
			LogFields{"error": err.Error()})
		goto invalidBody
	}
	if len(request.ChannelID) == 0 {
		self.logger.Error("router", "Missing channel ID", LogFields{"uaid": uaid})
		goto invalidBody
	}
	if ts, err = time.Parse(time.RFC3339Nano, request.Time); err != nil {
		self.logger.Error("router", "Could not parse time",
			LogFields{"error": err.Error(),
				"uaid": uaid,
				"chid": request.ChannelID,
				"time": request.Time})
		goto invalidBody
	}
	// routed data is already in storage.
	self.metrics.Increment("updates.routed.incoming")
	if err = self.app.Server().Update(request.ChannelID, uaid, request.Version, ts); err != nil {
		self.logger.Error("router",
			"Could not update local user",
			LogFields{"error": err.Error()})
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
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '_' && b != '.' && b != '=' {
			return false
		}
	}
	return true
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
