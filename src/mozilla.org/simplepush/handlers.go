/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"code.google.com/p/go.net/websocket"
	"github.com/gorilla/mux"

	"mozilla.org/simplepush/sperrors"
)

type HandlerConfig struct{}

type Handler struct {
	app             *Application
	logger          *SimpleLogger
	store           Store
	router          *Router
	metrics         *Metrics
	max_connections int
	token_key       []byte
	propping        PropPinger
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
	self.max_connections = app.MaxConnections()
	self.token_key = app.TokenKey()
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
	// return "OK" only if all is well.
	// TODO: make sure all is well.
	clientCount := self.app.ClientCount()
	OK := "OK"
	if clientCount >= int(self.max_connections) {
		OK = "NOPE"
	}
	reply := fmt.Sprintf("{\"status\":\"%s\",\"clients\":%d}",
		OK, clientCount)
	if OK != "OK" {
		http.Error(resp, reply, http.StatusServiceUnavailable)
	} else {
		resp.Write([]byte(reply))
	}
}

func (self *Handler) RealStatusHandler(resp http.ResponseWriter,
	req *http.Request) {
	var okClients bool
	var msg string

	clientCount := self.app.ClientCount()
	if okClients = clientCount > int(self.max_connections); !okClients {
		msg += "Exceeding max_connections, "
	}
	mcStatus, err := self.store.Status()
	if !mcStatus {
		msg += fmt.Sprintf(" Memcache error %s,", err)
	}
	ok := okClients && mcStatus
	gcount := runtime.NumGoroutine()
	repMap := JsMap{"ok": ok,
		"clientCount": clientCount,
		"maxClients":  self.max_connections,
		"mcstatus":    mcStatus,
		"goroutines":  gcount,
	}
	if err != nil {
		repMap["error"] = err.Error()
	}
	if msg != "" {
		repMap["message"] = msg
	}
	reply, err := json.Marshal(repMap)

	if ok {
		resp.Write(reply)
	} else {
		http.Error(resp, string(reply), http.StatusServiceUnavailable)
	}
}

func proxyNotification(proto, host, path string, vers int64) (err error) {

	// I have tried a number of variations on this, but while it's possible
	// to add Form values to this request, they're never actually sent out.
	// Thus, I'm using the URL arguments option. Which sucks.
	req, err := http.NewRequest("PUT",
		fmt.Sprintf("%s://%s%s?version=%d", proto, host, path, vers), nil)
	if err != nil {
		return err
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 300 {
		return nil
	}
	rbody, err := ioutil.ReadAll(resp.Body)
	return errors.New(fmt.Sprintf("Proxy failed. Returned (%d)\n %s",
		resp.StatusCode, rbody))
}

// -- REST
func (self *Handler) UpdateHandler(resp http.ResponseWriter, req *http.Request) {
	// Handle the version updates.
	var err error
	var version int64

	if self.app.ClientCount() > int(self.max_connections) {
		http.Error(resp, "{\"error\": \"Server unavailable\"}",
			http.StatusServiceUnavailable)
		self.metrics.Increment("updates.appserver.too_many_connections")
		return
	}

	timer := time.Now()
	filter := regexp.MustCompile("[^\\w-\\.\\=]")
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("update", "Handling Update",
			LogFields{"path": req.URL.Path})
	}

	defer func() {
		self.logger.Debug("update", "+++++++++++++ DONE +++", nil)
	}()
	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	svers := req.FormValue("version")
	if svers != "" {
		version, err = strconv.ParseInt(svers, 10, 64)
		if err != nil || version < 0 {
			http.Error(resp, "\"Invalid Version\"", http.StatusBadRequest)
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

	if token := self.token_key; len(token) > 0 {
		// Note: dumping the []uint8 keys can produce terminal glitches
		self.logger.Debug("main", "Decoding...", nil)
		var err error
		bpk, err := Decode(token, pk)
		if err != nil {
			self.logger.Error("update",
				"Could not decode token",
				LogFields{"primarykey": pk,
					"remoteAddr": req.RemoteAddr,
					"path":       req.URL.Path,
					"error":      ErrStr(err)})
			http.Error(resp, "", http.StatusNotFound)
			self.metrics.Increment("updates.appserver.invalid")
			return
		}

		pk = strings.TrimSpace(string(bpk))
	}

	if filter.Find([]byte(pk)) != nil {
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

	uaid, chid, ok := self.store.KeyToIDs(pk)
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
			resp.Write([]byte("false"))
		}
		return
	}

	// is there a Proprietary Ping for this?
	//TODO: This should return JsMap, err
	connect, err := self.store.FetchPing(uaid)
	if err == nil && len(connect) > 0 {
		// TODO: store the prop ping?
		c_js := make(JsMap)
		err = json.Unmarshal([]byte(connect), &c_js)
		if err != nil {
			self.logger.Warn("update",
				"Could not resolve Proprietary Ping connection string",
				LogFields{"error": err.Error(),
					"connect": connect})
		} else {
			err = self.propping.Register(c_js, uaid)
			if err != nil {
				self.logger.Warn("update",
					"Could not generate Proprietary Ping",
					LogFields{"error": err.Error(),
						"connect": connect})
			}
		}
	}

	// At this point we should have a valid endpoint in the URL
	self.metrics.Increment("updates.appserver.incoming")

	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	if self.propping != nil && self.propping.CanBypassWebsocket() {
		err = self.propping.Send(version)
		if err != nil {
			self.logger.Error("update",
				"Could not force to proprietary ping",
				LogFields{"error": err.Error()})
		} else {
			// Neat! Might as well return.
			resp.Header().Set("Content-Type", "application/json")
			resp.Write([]byte("{}"))
			return
		}
	}
	if self.logger.ShouldLog(INFO) {
		defer func(uaid, chid, path string, timer time.Time, err error) {
			self.logger.Info("dash", "Client Update complete",
				LogFields{
					"uaid":       uaid,
					"path":       req.URL.Path,
					"channelID":  chid,
					"successful": strconv.FormatBool(err == nil),
					"duration":   strconv.FormatInt(time.Now().Sub(timer).Nanoseconds(), 10)})
		}(uaid, chid, req.URL.Path, timer, err)

		self.logger.Info("update",
			"setting version for ChannelID",
			LogFields{"uaid": uaid, "channelID": chid,
				"version": strconv.FormatInt(version, 10)})
	}
	err = self.store.Update(pk, version)

	if err != nil {
		self.logger.Error("update", "Could not update channel",
			LogFields{"UAID": uaid,
				"channelID": chid,
				"version":   strconv.FormatInt(version, 10),
				"error":     err.Error()})
		status, _ := sperrors.ErrToStatus(err)
		http.Error(resp, "Could not update channel version", status)
		return
	}
	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	self.metrics.Increment("updates.received")
	// Ping the appropriate server
	if client, ok := self.app.GetClient(uaid); ok {
		self.app.Server().RequestFlush(client, chid, int64(version))
	}
	return
}

func (self *Handler) PushSocketHandler(ws *websocket.Conn) {
	if self.app.ClientCount() > int(self.max_connections) {
		websocket.JSON.Send(ws, JsMap{
			"status": http.StatusServiceUnavailable,
			"error":  "Server Unavailable"})
		return
	}
	timer := time.Now()
	sock := PushWS{Uaid: "",
		Socket: ws,
		Store:  self.store,
		Logger: self.logger,
		Born:   timer}

	if self.logger.ShouldLog(INFO) {
		self.logger.Info("handler", "websocket connection", LogFields{
			"UserAgent":  strings.Join(ws.Request().Header["User-Agent"], ","),
			"URI":        ws.Request().RequestURI,
			"RemoteAddr": ws.Request().RemoteAddr,
		})
	}
	defer func() {
		if r := recover(); r != nil {
			debug.PrintStack()
			self.logger.Error("main", "Unknown error",
				LogFields{"error": r.(error).Error()})
		}
		// Clean-up the resources
		self.app.Server().HandleCommand(PushCommand{DIE, nil}, &sock)
	}()

	NewWorker(self.app).Run(&sock)
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("main", "Server for client shut-down", nil)
	}
}

// Boy Golly, sure would be nice to put this in router.go, huh?
// well, thanks to go being picky about circular references, you can't.
func (self *Handler) RouteHandler(resp http.ResponseWriter, req *http.Request) {
	var err error
	var chid string
	var ts time.Time
	var vers int64
	// get the uaid from the url
	uaid, ok := mux.Vars(req)["uaid"]
	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		return
	}
	// if uid is not present, or doesn't exist in the known clients...
	if !ok || !self.app.ClientExists(uaid) {
		http.Error(resp, "UID Not Found", http.StatusNotFound)
		return
	}
	// We know of this one.
	if req.ContentLength < 1 {
		self.logger.Warn("router", "Routed update contained no body",
			LogFields{"uaid": uaid})
		http.Error(resp, "Missing body", http.StatusNotAcceptable)
		return
	}
	defer req.Body.Close()
	decoder := json.NewDecoder(req.Body)
	jdata := make(JsMap)
	err = decoder.Decode(&jdata)
	if err != nil {
		self.logger.Error("router",
			"Could not read update body",
			LogFields{"error": err.Error()})
		http.Error(resp, "Invalid body", http.StatusNotAcceptable)
		return
	}
	if v, ok := jdata["chid"]; !ok {
		self.logger.Error("router",
			"Missing chid", nil)
		http.Error(resp, "Invalid body", http.StatusNotAcceptable)
		return
	} else {
		chid = v.(string)
	}
	if v, ok := jdata["time"]; !ok {
		self.logger.Error("router",
			"Missing time", nil)
		http.Error(resp, "Invalid body", http.StatusNotAcceptable)
		return
	} else {
		ts, err = time.Parse(time.RFC3339Nano, v.(string))
		if err != nil {
			self.logger.Error("router", "Could not parse time",
				LogFields{"error": err.Error(),
					"uaid": uaid,
					"chid": chid,
					"time": v.(string)})
			http.Error(resp, "Invalid body", http.StatusNotAcceptable)
			return
		}
	}
	if v, ok := jdata["version"]; !ok {
		self.logger.Warn("router",
			"Missing version", nil)
		vers = int64(time.Now().UTC().Unix())
	} else {
		vers = int64(v.(float64))
	}
	// routed data is already in storage.
	self.metrics.Increment("updates.routed.incoming")
	err = self.app.Server().Update(chid, uaid, vers, ts)
	if err != nil {
		self.logger.Error("router",
			"Could not update local user",
			LogFields{"error": err.Error()})
		http.Error(resp, "Server Error", http.StatusInternalServerError)
		return
	}
	http.Error(resp, "Ok", http.StatusOK)
	return
}

func (r *Handler) SetPropPinger(ping PropPinger) (err error) {
	r.propping = ping
	return
}

func (r *Handler) PropPinger() PropPinger {
	return r.propping
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
