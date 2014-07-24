/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush/router"
	"mozilla.org/simplepush/sperrors"
	storage "mozilla.org/simplepush/storage/mcstorage"
	"mozilla.org/util"

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

func awsGetPublicHostname() (hostname string, err error) {
	req := &http.Request{Method: "GET",
		URL: &url.URL{
			Scheme: "http",
			Host:   "169.254.169.254",
			Path:   "/latest/meta-data/public-hostname"}}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var hostBytes []byte
		hostBytes, err = ioutil.ReadAll(resp.Body)
		if err == nil {
			hostname = string(hostBytes)
		}
		return
	}
	return
}

func FixConfig(config *util.MzConfig) *util.MzConfig {
	currentHost := "localhost"
	if config.GetFlag("shard.use_aws_host") {
		currentHost, _ = util.GetAWSPublicHostname()
	}
	config.SetDefault("shard.current_host", currentHost)
	config.SetDefault("heak.current_host", currentHost)

	return config

}

func ErrStr(err error) string {
	if err == nil {
		return ""
	} else {
		return err.Error()
	}
}

func IStr(i interface{}) (reply string) {
	defer func() {
		if r := recover(); r != nil {
			reply = "Undefined"
		}
	}()

	if i != nil {
		reply = i.(string)
	} else {
		reply = ""
	}
	return reply
}

type Handler struct {
	config          *util.MzConfig
	logger          *util.HekaLogger
	storage         *storage.Storage
	router          *router.Router
	metrics         *util.Metrics
	max_connections int64
	token_key       []byte
}

func NewHandler(config *util.MzConfig,
	logger *util.HekaLogger,
	storage *storage.Storage,
	router *router.Router,
	metrics *util.Metrics,
	token_key []byte) *Handler {
	max_connections, err := strconv.ParseInt(config.Get("max_connections", "1000"), 10, 64)
	if err != nil || max_connections == 0 {
		logger.Error("handler", "Invalid value for max_connections, using 1000",
			util.Fields{"error": err.Error()})
		max_connections = 1000
	}
	return &Handler{config: config,
		logger:          logger,
		storage:         storage,
		router:          router,
		metrics:         metrics,
		max_connections: max_connections,
		token_key:       token_key,
	}
}

func (self *Handler) MetricsHandler(resp http.ResponseWriter, req *http.Request) {
	snapshot := self.metrics.Snapshot()
	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(snapshot)
	if err != nil {
		self.logger.Error("handler", "Could not generate metrics report",
			util.Fields{"error": err.Error()})
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
	var port string
	var vers int64
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
			util.Fields{"path": req.URL.Path})
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
		vers, err = strconv.ParseInt(svers, 10, 64)
		if err != nil || vers < 0 {
			http.Error(resp, "\"Invalid Version\"", http.StatusBadRequest)
			self.metrics.Increment("updates.appserver.invalid")
			return
		}
	} else {
		vers = time.Now().UTC().Unix()
	}

	elements := strings.Split(req.URL.Path, "/")
	pk := elements[len(elements)-1]
	// TODO:
	// is there a magic flag for proxyable endpoints?
	// e.g. update/p/gcm/LSoC or something?
	// (Note, this would allow us to use smarter FE proxies.)
	if len(pk) == 0 {
		if self.logger != nil {
			self.logger.Error("update", "No token, rejecting request",
				util.Fields{"remoteAddr": req.RemoteAddr,
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
					util.Fields{"primarykey": pk,
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
				util.Fields{"token": pk,
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
				util.Fields{"primaryKey": pk,
					"path":  req.URL.Path,
					"error": ErrStr(err)})
		}
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	// is there a Proprietary Ping for this?
	connect, err := self.storage.GetPropConnect(uaid)
	fmt.Printf("### PropConnect: %+v\n", connect)
	if err == nil && len(connect) > 0 {
		fmt.Printf("### Connect %s\n", connect)
		pping, err = NewPropPing(connect,
			uaid,
			self.config,
			self.logger,
			self.storage,
			self.metrics)
		if err != nil {
			self.logger.Warn("update",
				"Could not generate Proprietary Ping",
				util.Fields{"error": err.Error(),
					"connect": connect})
		}
	}

	if chid == "" {
		if self.logger != nil {
			self.logger.Error("update",
				"Incomplete primary key",
				util.Fields{"uaid": uaid,
					"channelID":  chid,
					"remoteAddr": req.RemoteAddr})
		}
		self.metrics.Increment("updates.appserver.invalid")
		return
	}

	// At this point we should have a valid endpoint in the URL
	self.metrics.Increment("updates.appserver.incoming")

	//log.Printf("<< %s.%s = %d", uaid, chid, vers)

	port = self.config.Get("port", "80")
	if port == "80" {
		port = ""
	}
	if port != "" {
		port = ":" + port
	}
	currentHost := self.config.Get("shard.current_host", "localhost")
	host, err := self.storage.GetUAIDHost(uaid)
	if err != nil {
		if err == storage.UnknownUAIDError {
			// try the proprietary ping
			if pping != nil {
				err = pping.Send(vers)
				if err != nil {
					resp.Header().Set("Content-Type", "application/json")
					resp.Write([]byte("{}"))
					self.metrics.Increment("updates.clients.proprietary")
					return
				}
			}
			if self.logger != nil {
				self.logger.Error("update",
					"Could not discover host for UAID",
					util.Fields{"uaid": uaid,
						"error": err.Error()})
			}
			host = self.config.Get("shard.defaultHost", "localhost")
		}
	}

	/* if this is a GCM connected host, boot vers immediately to GCM
	 */
	if pping != nil && pping.CanBypassWebsocket() {
		err = pping.Send(vers)
		if err != nil {
			self.logger.Error("update",
				"Could not force to proprietary ping",
				util.Fields{"error": err.Error()})
		} else {
			// Neat! Might as well return.
			resp.Header().Set("Content-Type", "application/json")
			resp.Write([]byte("{}"))
			return
		}
	}

	if self.config.GetFlag("shard.do_proxy") {
		if host != currentHost && host != "localhost" {
			if self.logger != nil {
				self.logger.Info("update",
					"Proxying request for UAID",
					util.Fields{"uaid": uaid,
						"destination": host + port})
			}
			// Use tcp routing.
			if self.config.GetFlag("shard.router") {
				// If there was an error routing the update, don't
				// tell the AppServer. Chances are it's temporary, and
				// the client will get the update on next refresh/reconnect
				self.router.SendUpdate(host, uaid, chid, vers, timer)
			} else {
				proto := "http"
				if len(self.config.Get("ssl.certfile", "")) > 0 {
					proto = "https"
				}

				err = proxyNotification(proto, host+port, req.URL.Path, vers)
				if err != nil && self.logger != nil {
					self.logger.Error("update",
						"Proxy failed", util.Fields{
							"uaid":        uaid,
							"destination": host + port,
							"error":       err.Error()})
				}
			}
			self.metrics.Increment("updates.routed.outgoing")
			if err != nil {
				http.Error(resp, err.Error(), 500)
			} else {
				resp.Header().Set("Content-Type", "application/json")
				resp.Write([]byte("{}"))
			}
			return
		}
	}

	if self.logger != nil {
		defer func(uaid, chid, path string, timer time.Time, err error) {
			self.logger.Info("dash", "Client Update complete",
				util.Fields{
					"uaid":       uaid,
					"path":       req.URL.Path,
					"channelID":  chid,
					"successful": strconv.FormatBool(err == nil),
					"duration":   strconv.FormatInt(time.Now().Sub(timer).Nanoseconds(), 10)})
		}(uaid, chid, req.URL.Path, timer, err)

		self.logger.Info("update",
			"setting version for ChannelID",
			util.Fields{"uaid": uaid, "channelID": chid,
				"version": strconv.FormatInt(vers, 10)})
	}
	err = self.storage.UpdateChannel(pk, vers)

	if err != nil {
		if self.logger != nil {
			self.logger.Error("update", "Cound not update channel",
				util.Fields{"UAID": uaid,
					"channelID": chid,
					"version":   strconv.FormatInt(vers, 10),
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
		Flush(client, chid, int64(vers))
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
		sock.Logger.Info("handler", "websocket connection", util.Fields{
			"UserAgent":  strings.Join(ws.Request().Header["User-Agent"], ","),
			"URI":        ws.Request().RequestURI,
			"RemoteAddr": ws.Request().RemoteAddr,
		})
	}
	defer func(logger *util.HekaLogger) {
		if r := recover(); r != nil {
			debug.PrintStack()
			if logger != nil {
				logger.Error("main", "Unknown error",
					util.Fields{"error": r.(error).Error()})
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

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
