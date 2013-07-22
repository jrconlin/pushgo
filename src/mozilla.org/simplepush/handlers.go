/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush/sperrors"
	"mozilla.org/simplepush/storage"
	"mozilla.org/util"

	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
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

	config["heka.current_host"] = config["shard.current_host"]

	return config

}

type Handler struct {
	config util.JsMap
	logger *util.HekaLogger
}

func NewHandler(config util.JsMap, logger *util.HekaLogger) *Handler {
	return &Handler{config: config,
		logger: logger}
}

// VIP response
func (self *Handler) StatusHandler(resp http.ResponseWriter, req *http.Request) {
	// return "OK" only if all is well.
	// TODO: make sure all is well.
    clientCount := len(Clients)
    resp.Write([]byte(fmt.Sprintf("OK\nclients:%d\n", clientCount)))
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
		util.JsMap{"path": req.URL.Path})
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
			util.JsMap{"remoteAddr": req.RemoteAddr,
				"path": req.URL.Path})
		http.Error(resp, "Token not found", http.StatusNotFound)
		return
	}

	store := storage.New(self.config, self.logger)
	if token, ok := self.config["token_key"]; ok && len(token.([]uint8)) > 0 {
		self.logger.Debug("main", "Decoding key", util.JsMap{"token": token})
		var err error
		bpk, err := Decode(token.([]byte),
			pk)
		if err != nil {
			self.logger.Error("update",
				"Could not decode token",
				util.JsMap{"primarykey": pk,
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
			util.JsMap{"token": pk,
				"path": req.URL.Path})
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		return
	}

	uaid, appid, err := storage.ResolvePK(pk)
	if err != nil {
		self.logger.Error("update",
			"Could not resolve PK",
			util.JsMap{"primaryKey": pk,
				"path":  req.URL.Path,
				"error": err})
		return
	}

	if appid == "" {
		self.logger.Error("update",
			"Incomplete primary key",
			util.JsMap{"uaid": uaid,
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
	currentHost := util.MzGet(self.config, "shard.current_host", "localhost")
	host, err := store.GetUAIDHost(uaid)
	if err != nil {
		self.logger.Error("update",
			"Could not discover host for UAID",
			util.JsMap{"uaid": uaid,
				"error": err})
		host = util.MzGet(self.config, "shard.defaultHost", "localhost")
	}
	if util.MzGetFlag(self.config, "shard.doProxy") {
		if host != currentHost && host != "localhost" {
			self.logger.Info("update",
				"Proxying request for UAID",
				util.JsMap{"uaid": uaid,
					"destination": host + port})
			err = proxyNotification(host+port, req.URL.Path)
			if err != nil {
				self.logger.Error("update",
					"Proxy failed", util.JsMap{
						"uaid":        uaid,
						"destination": host + port,
						"error":       err})
			}
			return
		}
	}

	defer func(uaid, appid, path string, timer time.Time) {
		self.logger.Info("timer", "Client Update complete",
			util.JsMap{
				"uaid":      uaid,
				"path":      req.URL.Path,
				"channelID": appid,
				"duration":  time.Now().Sub(timer).Nanoseconds()})
	}(uaid, appid, req.URL.Path, timer)

	self.logger.Info("update",
		"setting version for ChannelID",
		util.JsMap{"uaid": uaid, "channelID": appid, "version": vers})
	err = store.UpdateChannel(pk, vers)

	if err != nil {
		self.logger.Error("update", "Cound not update channel",
			util.JsMap{"UAID": uaid,
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
		util.JsMap{"uaid": uaid,
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
	store := storage.New(self.config, self.logger)
	sock := PushWS{Uaid: "",
		Socket: ws,
		Scmd:   make(chan PushCommand),
		Ccmd:   make(chan PushCommand),
		Store:  store,
		Logger: self.logger,
		Born:   timer}

	sock.Logger.Info("main", "New socket connection detected", nil)
	defer func(log *util.HekaLogger) {
		if r := recover(); r != nil {
			debug.PrintStack()
			log.Error("main", "Unknown error",
				util.JsMap{"error": r.(error).Error()})
		}
	}(sock.Logger)

	go NewWorker(self.config).Run(sock)
	for {
		select {
		case serv_cmd := <-sock.Scmd:
			result, args := HandleServerCommand(serv_cmd, &sock)
			sock.Logger.Debug("main",
				"Server Returning Result",
				util.JsMap{"result": result,
					"args": args})
			sock.Scmd <- PushCommand{result, args}
		}
	}
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
