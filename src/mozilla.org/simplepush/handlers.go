/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush/sperrors"
	storage "mozilla.org/simplepush/storage/mcstorage"
	mozutil "mozilla.org/util"

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
	"time"
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

func FixConfig(config mozutil.JsMap) mozutil.JsMap {
	if _, ok := config["shard.current_host"]; !ok {
		currentHost := "localhost"
		if val := os.Getenv("HOST"); len(val) > 0 {
			currentHost = val
		} else {
			if mozutil.MzGetFlag(config, "shard.use_aws_host") {
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

type Handler struct {
	config mozutil.JsMap
	logger *mozutil.HekaLogger
	store  *storage.Storage
}

func NewHandler(config mozutil.JsMap, logger *mozutil.HekaLogger, store *storage.Storage) *Handler {
	return &Handler{config: config,
		logger: logger,
		store:  store}
}

// VIP response
func (self *Handler) StatusHandler(resp http.ResponseWriter, req *http.Request) {
	// return "OK" only if all is well.
	// TODO: make sure all is well.
	clientCount := ClientCount()
	resp.Write([]byte(fmt.Sprintf("{\"status\":\"OK\",\"clients\":%d}", clientCount)))
}

func (self *Handler) RealStatusHandler(resp http.ResponseWriter, req *http.Request) {
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
	repMap := mozutil.JsMap{"ok": ok,
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

	resp.Write(reply)
}

func proxyNotification(host, path string) (err error) {
	req := &http.Request{Method: "PUT",
		URL: &url.URL{
			Scheme: "http",
			Host:   host,
			Path:   path}}
	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 300 {
		return nil
	}
	body, err := ioutil.ReadAll(resp.Body)
	return errors.New(fmt.Sprintf("Proxy failed. Returned (%d)\n %s",
		resp.StatusCode, body))
}

// -- REST
func (self *Handler) UpdateHandler(resp http.ResponseWriter, req *http.Request) {
	// Handle the version updates.
	var err error
	var port string
	var vers int64

	timer := time.Now()
	filter := regexp.MustCompile("[^\\w-\\.\\=]")
	self.logger.Debug("main", "Config", self.config)

	self.logger.Debug("update", "Handling Update",
		mozutil.JsMap{"path": req.URL.Path})
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
		self.logger.Error("update", "No token, rejecting request",
			mozutil.JsMap{"remoteAddr": req.RemoteAddr,
				"path": req.URL.Path})
		http.Error(resp, "Token not found", http.StatusNotFound)
		return
	}

	if token, ok := self.config["token_key"]; ok && len(token.([]uint8)) > 0 {
		self.logger.Debug("main", "Decoding key", mozutil.JsMap{"token": token})
		var err error
		bpk, err := Decode(token.([]byte),
			pk)
		if err != nil {
			self.logger.Error("update",
				"Could not decode token",
				mozutil.JsMap{"primarykey": pk,
					"remoteAddr": req.RemoteAddr,
					"path":       req.URL.Path,
					"error":      err})
			http.Error(resp, "", http.StatusNotFound)
			return
		}

		pk = strings.TrimSpace(string(bpk))
	}

	if filter.Find([]byte(pk)) != nil {
		self.logger.Error("update",
			"Invalid token for update",
			mozutil.JsMap{"token": pk,
				"path": req.URL.Path})
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		return
	}

	uaid, appid, err := storage.ResolvePK(pk)
	if err != nil {
		self.logger.Error("update",
			"Could not resolve PK",
			mozutil.JsMap{"primaryKey": pk,
				"path":  req.URL.Path,
				"error": err})
		return
	}

	if appid == "" {
		self.logger.Error("update",
			"Incomplete primary key",
			mozutil.JsMap{"uaid": uaid,
				"channelID":  appid,
				"remoteAddr": req.RemoteAddr})
		return
	}

	if iport, ok := self.config["port"]; ok {
		port = iport.(string)
	}
	if port != "" && port != "80" {
		port = ":" + port
	}
	currentHost := mozutil.MzGet(self.config, "shard.current_host", "localhost")
	host, err := self.store.GetUAIDHost(uaid)
	if err != nil {
		self.logger.Error("update",
			"Could not discover host for UAID",
			mozutil.JsMap{"uaid": uaid,
				"error": err})
		host = mozutil.MzGet(self.config, "shard.defaultHost", "localhost")
	}
	if mozutil.MzGetFlag(self.config, "shard.doProxy") {
		if host != currentHost && host != "localhost" {
			self.logger.Info("update",
				"Proxying request for UAID",
				mozutil.JsMap{"uaid": uaid,
					"destination": host + port})
			err = proxyNotification(host+port, req.URL.Path)
			if err != nil {
				self.logger.Error("update",
					"Proxy failed", mozutil.JsMap{
						"uaid":        uaid,
						"destination": host + port,
						"error":       err})
			}
			return
		}
	}

	defer func(uaid, appid, path string, timer time.Time) {
		self.logger.Info("timer", "Client Update complete",
			mozutil.JsMap{
				"uaid":      uaid,
				"path":      req.URL.Path,
				"channelID": appid,
				"duration":  time.Now().Sub(timer).Nanoseconds()})
	}(uaid, appid, req.URL.Path, timer)

	self.logger.Info("update",
		"setting version for ChannelID",
		mozutil.JsMap{"uaid": uaid, "channelID": appid, "version": vers})
	err = self.store.UpdateChannel(pk, vers)

	if err != nil {
		self.logger.Error("update", "Cound not update channel",
			mozutil.JsMap{"UAID": uaid,
				"channelID": appid,
				"version":   vers,
				"error":     err})
		status, _ := sperrors.ErrToStatus(err)
		http.Error(resp, "Could not update channel version", status)
		return
	}
	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	self.logger.Info("timer", "Client Update complete",
		mozutil.JsMap{"uaid": uaid,
			"channelID": appid,
			"duration":  time.Now().Sub(timer).Nanoseconds()})
	// Ping the appropriate server
	if client, ok := Clients[uaid]; ok {
		Flush(client)
	}
	return
}

func (self *Handler) PushSocketHandler(ws *websocket.Conn) {
	timer := time.Now()
	if ClientCount() > self.config["max_connections"].(int) {
		self.logger.Error("handler", "Too Many Sockets!", nil)
		return
	}
	sock := PushWS{Uaid: "",
		Socket: ws,
		Ccmd:   make(chan PushCommand, 1),
        Acmd:   make(chan bool, 1),
		Store:  self.store,
		Logger: self.logger,
		Born:   timer}

	sock.Logger.Info("main", "New socket connection detected", nil)
	defer func(logger *mozutil.HekaLogger) {
		if r := recover(); r != nil {
			debug.PrintStack()
			logger.Error("main", "Unknown error",
				mozutil.JsMap{"error": r.(error).Error()})
		}
	}(sock.Logger)

	NewWorker(self.config).Run(sock)
	HandleServerCommand(PushCommand{DIE, nil}, &sock)
	self.logger.Debug("main", "Server for client shut-down", nil)
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
