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

	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
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
	snapshot map[string]int64
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

func FixConfig(config util.JsMap) util.JsMap {
	if _, ok := config["shard.current_host"]; !ok {
		currentHost := "localhost"
		if val := os.Getenv("HOST"); len(val) > 0 {
			currentHost = val
		} else {
			if util.MzGetFlag(config, "shard.use_aws_host") {
				var awsHost string
				var err error
				awsHost, err = awsGetPublicHostname()
				if err == nil {
					currentHost = awsHost
				}
			}
		}
		config["shard.current_host"] = currentHost
	}
	// Convert the token_key from base64 (if present)
	if k, ok := config["token_key"]; ok {
		key, err := base64.URLEncoding.DecodeString(k.(string))
		if err != nil {
			log.Fatal(err)
		}
		config["token_key"] = key
	}

	DEFAULT_MAX_CONNECTIONS := 1000
	config["heka.current_host"] = config["shard.current_host"]
	if _, ok := config["max_connections"]; ok {
		var err error
		val := config["max_connections"].(string)
		ival, err := strconv.ParseInt(val, 10, 0)
		if err != nil {
			config["max_connections"] = DEFAULT_MAX_CONNECTIONS
		} else {
			config["max_connections"] = int(ival)
		}
	} else {
		config["max_connections"] = DEFAULT_MAX_CONNECTIONS
	}

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
	config util.JsMap
	logger *util.HekaLogger
	store  *storage.Storage
	router *router.Router
}

func NewHandler(config util.JsMap, logger *util.HekaLogger,
	store *storage.Storage, router *router.Router) *Handler {
	return &Handler{config: config,
		logger: logger,
		store:  store,
		router: router}
}

func (self *Handler) MetricsHandler(resp http.ResponseWriter, req *http.Request) {
	var tempshot map[string]int64
	var newsnapshot = MetricsSnapshot()

	// Looping needs optimizing, obviously
	if self.config["metrics.counters"] != 0 {
		for k, v := range newsnapshot {
			if _, exists := snapshot[k]; exists {
				tempshot[k] = v - snapshot[k]
			} else {
				tempshot[k] = v
			}
			snapshot[k] = v
		}
	} else {
		// Gauges
		for k, v := range newsnapshot {
			tempshot[k] = v
			snapshot[k] = v
		}
	}
	reply, err := json.Marshal(tempshot)
	if err != nil {
		if self.logger != nil {
			self.logger.Error("handler", "Failed to marshal metrics", nil)
		}
	}
	resp.Write(reply)
}

// VIP response
func (self *Handler) StatusHandler(resp http.ResponseWriter,
	req *http.Request) {
	// return "OK" only if all is well.
	// TODO: make sure all is well.
	clientCount := ClientCount()
	maxClients := self.config["max_connections"].(int)
	ok := clientCount < maxClients
	OK := "OK"
	if !ok {
		OK = "NOPE"
	}
	reply := fmt.Sprintf("{\"status\":\"%s\",\"clients\":%d}",
		OK, clientCount)
	if !ok {
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
	maxClients := self.config["max_connections"].(int)
	if okClients = clientCount < maxClients; !okClients {
		msg += "Exceeding max_connections, "
	}
	mcStatus, err := self.store.Status()
	if !mcStatus {
		msg += fmt.Sprintf(" Memcache error %s,", err)
	}
	ok := okClients && mcStatus
	gcount := runtime.NumGoroutine()
	repMap := util.JsMap{"ok": ok,
		"clientCount": clientCount,
		"maxClients":  maxClients,
		"mcstatus":    mcStatus,
		"goroutines":  gcount}
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

	if ClientCount() > self.config["max_connections"].(int) {
		if self.logger != nil {
			if toomany == 0 {
				atomic.StoreInt32(&toomany, 1)
				self.logger.Error("handler", "Socket Count Exceeded", nil)
			}
		}
		MetricIncrement("too many connections")
		http.Error(resp, "{\"error\": \"Server unavailable\"}",
			http.StatusServiceUnavailable)
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
	if self.logger != nil {
		self.logger.Debug("update", "=========== UPDATE ====", nil)
	}

	defer func() {
		if self.logger != nil {
			self.logger.Debug("update", "+++++++++++++ DONE +++", nil)
		}
	}()
	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		return
	}

	svers := req.FormValue("version")
	if svers != "" {
		vers, err = strconv.ParseInt(svers, 10, 64)
		if err != nil || vers < 0 {
			http.Error(resp, "\"Invalid Version\"", http.StatusBadRequest)
			return
		}
	} else {
		vers = time.Now().UTC().Unix()
	}

	elements := strings.Split(req.URL.Path, "/")
	pk := elements[len(elements)-1]
	if len(pk) == 0 {
		if self.logger != nil {
			self.logger.Error("update", "No token, rejecting request",
				util.Fields{"remoteAddr": req.RemoteAddr,
					"path": req.URL.Path})
		}
		http.Error(resp, "Token not found", http.StatusNotFound)
		return
	}

	if token, ok := self.config["token_key"]; ok && len(token.([]uint8)) > 0 {
		if self.logger != nil {
			// Note: dumping the []uint8 keys can produce terminal glitches
			self.logger.Debug("main", "Decoding...", nil)
		}
		var err error
		bpk, err := Decode(token.([]byte),
			pk)
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
		return
	}

	if chid == "" {
		if self.logger != nil {
			self.logger.Error("update",
				"Incomplete primary key",
				util.Fields{"uaid": uaid,
					"channelID":  chid,
					"remoteAddr": req.RemoteAddr})
		}
		return
	}

	//log.Printf("<< %s.%s = %d", uaid, chid, vers)

	if iport, ok := self.config["port"]; ok {
		port = iport.(string)
	}
	if port == "80" {
		port = ""
	}
	if port != "" {
		port = ":" + port
	}
	currentHost := util.MzGet(self.config, "shard.current_host", "localhost")
	host, err := self.store.GetUAIDHost(uaid)
	if err != nil {
		if self.logger != nil {
			self.logger.Error("update",
				"Could not discover host for UAID",
				util.Fields{"uaid": uaid,
					"error": err.Error()})
		}
		host = util.MzGet(self.config, "shard.defaultHost", "localhost")
	}
	if util.MzGetFlag(self.config, "shard.do_proxy") {
		if host != currentHost && host != "localhost" {
			if self.logger != nil {
				self.logger.Info("update",
					"Proxying request for UAID",
					util.Fields{"uaid": uaid,
						"destination": host + port})
			}
			MetricIncrement("routing update: out")
			// Use tcp routing.
			if util.MzGetFlag(self.config, "shard.router") {
				// If there was an error routing the update, don't
				// tell the AppServer. Chances are it's temporary, and
				// the client will get the update on next refresh/reconnect
				self.router.SendUpdate(host, uaid, chid, vers, timer)
			} else {
				proto := "http"
				if len(util.MzGet(self.config, "ssl.certfile", "")) > 0 {
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
			if err != nil {
				http.Error(resp, err.Error(), 500)
			} else {
				resp.Write([]byte("Ok"))
			}
			return
		}
	}

	if self.logger != nil {
		defer func(uaid, chid, path string, timer time.Time) {
			self.logger.Info("timer", "Client Update complete",
				util.Fields{
					"uaid":      uaid,
					"path":      req.URL.Path,
					"channelID": chid,
					"duration":  strconv.FormatInt(time.Now().Sub(timer).Nanoseconds(), 10)})
		}(uaid, chid, req.URL.Path, timer)

		self.logger.Info("update",
			"setting version for ChannelID",
			util.Fields{"uaid": uaid, "channelID": chid,
				"version": strconv.FormatInt(vers, 10)})
	}
	MetricIncrement("update channel")
	err = self.store.UpdateChannel(pk, vers)

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
	// Ping the appropriate server
	if client, ok := Clients[uaid]; ok {
		Flush(client, chid, int64(vers))
	}
	return
}

func (self *Handler) PushSocketHandler(ws *websocket.Conn) {
	if ClientCount() > self.config["max_connections"].(int) {
		if self.logger != nil {
			if toomany == 0 {
				// Don't flood the error log.
				atomic.StoreInt32(&toomany, 1)
				self.logger.Error("handler", "Socket Count Exceeded", nil)
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
		Socket: ws,
		Store:  self.store,
		Logger: self.logger,
		Born:   timer}
	atomic.AddInt32(&cClients, 1)

	if sock.Logger != nil {
		sock.Logger.Info("main", "New socket connection detected", nil)
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

	NewWorker(self.config, self.logger).Run(&sock)
	if self.logger != nil {
		self.logger.Debug("main", "Server for client shut-down", nil)
	}
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
