/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"github.com/gorilla/mux"
	"mozilla.org/simplepush/sperrors"

	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	toomany int32 = 0
)

type Handler struct {
	app             *Application
	logger          *SimpleLogger
	storage         *Storage
	metrics         *Metrics
	max_connections int64
	token_key       []byte
}

func NewHandler(app *Application, token_key []byte) *Handler {
	max_connections, err := strconv.ParseInt(config.Get("max_connections", "1000"), 10, 64)
	if err != nil || max_connections == 0 {
		logger.Error("handler", "Invalid value for max_connections, using 1000",
			LogFields{"error": err.Error()})
		max_connections = 1000
	}
	return &Handler{
		app:             *Application,
		logger:          app.Logger(),
		storage:         app.Storage(),
		metrics:         metrics,
		max_connections: app.MaxConnections(),
		token_key:       token_key,
	}
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
	clientCount := ClientCount()
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

	clientCount := ClientCount()
	if okClients = clientCount > int(self.max_connections); !okClients {
		msg += "Exceeding max_connections, "
	}
	mcStatus, err := self.storage.Status()
	if !mcStatus {
		msg += fmt.Sprintf(" Memcache error %s,", err)
	}
	ok := okClients && mcStatus
	gcount := runtime.NumGoroutine()
	repMap := util.JsMap{"ok": ok,
		"clientCount": clientCount,
		"maxClients":  self.max_connections,
		"mcstatus":    mcStatus,
		"goroutines":  gcount,
		"version":     self.config.Get("VERSION", "Unknown"),
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
	var pping *PropPing

	if ClientCount() > int(self.max_connections) {
		if self.logger != nil {
			if toomany == 0 {
				atomic.StoreInt32(&toomany, 1)
				self.logger.Error("handler", "Socket Count Exceeded", nil)
			}
		}
		http.Error(resp, "{\"error\": \"Server unavailable\"}",
			http.StatusServiceUnavailable)
		self.metrics.Increment("updates.appserver.too_many_connections")
		return
	}
	if toomany != 0 {
		atomic.StoreInt32(&toomany, 0)
	}

	timer := time.Now()
	filter := regexp.MustCompile("[^\\w-\\.\\=]")
	if self.logger != nil {
		self.logger.Debug("update", "Handling Update",
			LogFields{"path": req.URL.Path})
	}

	defer func() {
		if self.logger != nil {
			self.logger.Debug("update", "+++++++++++++ DONE +++", nil)
		}
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
		if self.logger != nil {
			self.logger.Error("update", "No token, rejecting request",
				LogFields{"remoteAddr": req.RemoteAddr,
					"path": req.URL.Path})
		}
		http.Error(resp, "Token not found", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	if token := self.token_key; len(token) > 0 {
		if self.logger != nil {
			// Note: dumping the []uint8 keys can produce terminal glitches
			self.logger.Debug("main", "Decoding...", nil)
		}
		var err error
		bpk, err := Decode(token, pk)
		if err != nil {
			if self.logger != nil {
				self.logger.Error("update",
					"Could not decode token",
					LogFields{"primarykey": pk,
						"remoteAddr": req.RemoteAddr,
						"path":       req.URL.Path,
						"error":      ErrStr(err)})
			}
			http.Error(resp, "", http.StatusNotFound)
			self.metrics.Increment("updates.appserver.invalid")
			return
		}

		pk = strings.TrimSpace(string(bpk))
	}

	if filter.Find([]byte(pk)) != nil {
		if self.logger != nil {
			self.logger.Error("update",
				"Invalid token for update",
				LogFields{"token": pk,
					"path": req.URL.Path})
		}
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	uaid, chid, err := storage.ResolvePK(pk)
	if err != nil {
		if self.logger != nil {
			self.logger.Error("update",
				"Could not resolve PK",
				LogFields{"primaryKey": pk,
					"path":  req.URL.Path,
					"error": ErrStr(err)})
		}
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	if chid == "" {
		if self.logger != nil {
			self.logger.Error("update",
				"Incomplete primary key",
				LogFields{"uaid": uaid,
					"channelID":  chid,
					"remoteAddr": req.RemoteAddr})
		}
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	// Is this ours or should we punt to a different server?
	if !ClientCollision(uaid) {
		// TODO: Move PropPing here? otherwise it's connected?
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
	connect, err := self.storage.GetPropConnect(uaid)
	if err == nil && len(connect) > 0 {
		fmt.Printf("### Connect %s\n", connect)
		// TODO: store the prop ping?
		pping, err = NewPropPing(connect,
			uaid,
			self.config,
			self.logger,
			self.storage,
			self.metrics)
		if err != nil {
			self.logger.Warn("update",
				"Could not generate Proprietary Ping",
				LogFields{"error": err.Error(),
					"connect": connect})
		}
	}

	// At this point we should have a valid endpoint in the URL
	self.metrics.Increment("updates.appserver.incoming")

	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	if pping != nil && pping.CanBypassWebsocket() {
		err = pping.Send(version)
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
	if self.logger != nil {
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
	err = self.storage.UpdateChannel(pk, version)

	if err != nil {
		if self.logger != nil {
			self.logger.Error("update", "Cound not update channel",
				LogFields{"UAID": uaid,
					"channelID": chid,
					"version":   strconv.FormatInt(version, 10),
					"error":     err.Error()})
		}
		status, _ := sperrors.ErrToStatus(err)
		http.Error(resp, "Could not update channel version", status)
		return
	}
	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	self.metrics.Increment("updates.received")
	// Ping the appropriate server
	defer MuClient.Unlock()
	MuClient.Lock()
	if client, ok := Clients[uaid]; ok {
		Flush(client, chid, int64(version))
	}
	return
}

func (self *Handler) PushSocketHandler(ws *websocket.Conn) {
	if ClientCount() > int(self.max_connections) {
		if self.logger != nil {
			if toomany == 0 {
				// Don't flood the error log.
				atomic.StoreInt32(&toomany, 1)
				self.logger.Error("dash", "Socket Count Exceeded", nil)
			}
		}
		websocket.JSON.Send(ws, util.JsMap{
			"status": http.StatusServiceUnavailable,
			"error":  "Server Unavailable"})
		return
	}
	if toomany != 0 {
		atomic.StoreInt32(&toomany, 0)
	}
	timer := time.Now()
	sock := PushWS{Uaid: "",
		Socket:  ws,
		Storage: self.storage,
		Logger:  self.logger,
		Born:    timer}
	atomic.AddInt32(&cClients, 1)

	if sock.Logger != nil {
		sock.Logger.Info("handler", "websocket connection", LogFields{
			"UserAgent":  strings.Join(ws.Request().Header["User-Agent"], ","),
			"URI":        ws.Request().RequestURI,
			"RemoteAddr": ws.Request().RemoteAddr,
		})
	}
	defer func(logger *util.MzLogger) {
		if r := recover(); r != nil {
			debug.PrintStack()
			if logger != nil {
				logger.Error("main", "Unknown error",
					LogFields{"error": r.(error).Error()})
			} else {
				log.Printf("Socket Unknown Error: %s", r.(error).Error())
			}
		}
		// Clean-up the resources
		HandleServerCommand(PushCommand{DIE, nil}, &sock)
		atomic.AddInt32(&cClients, -1)
	}(sock.Logger)

	NewWorker(self.config, self.logger, self.metrics).Run(&sock)
	if self.logger != nil {
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
	if !ok || !ClientCollision(uaid) {
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
	jdata := make(util.JsMap)
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
	err = GetServer().Update(chid,
		uaid,
		vers,
		ts)
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

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
