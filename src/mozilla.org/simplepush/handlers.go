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
	resp.Write([]byte("OK"))
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

	self.logger.Debug("main",
        fmt.Sprintf("Handling Update %s", req.URL.Path), nil)
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
	}
	if vers == 0 {
		vers = time.Now().UTC().Unix()
	}

	elements := strings.Split(req.URL.Path, "/")
	pk := elements[len(elements)-1]
	if len(pk) == 0 {
		self.logger.Error("main", "No token, rejecting request",
			util.JsMap{"remoteAddr": req.RemoteAddr})
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
			self.logger.Error("main",
				"Could not decode token",
				util.JsMap{"primarykey": pk,
					"remoteAddr": req.RemoteAddr,
					"path":       req.RequestURI,
					"error":      err})
			http.Error(resp, "", http.StatusNotFound)
			return
		}

		pk = strings.TrimSpace(string(bpk))
	}

	if filter.Find([]byte(pk)) != nil {
		self.logger.Error("main",
			"Invalid token for update",
			nil)
		http.Error(resp, "Invalid Token", http.StatusNotFound)
		return
	}

	uaid, appid, err := storage.ResolvePK(pk)
	if err != nil {
	    self.logger.Error("main",
			fmt.Sprintf("Could not resolve PK %s, %s", pk, err), nil)
		return
	}

	if appid == "" {
		self.logger.Error("main",
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
		self.logger.Error("main",
			fmt.Sprintf("Could not discover host for %s, %s (using default)",
				uaid, err), nil)
		host = util.MzGet(self.config, "shard.defaultHost", "localhost")
	}
	if util.MzGetFlag(self.config, "shard.doProxy") {
		if host != currentHost && host != "localhost" {
			self.logger.Info("main",
				fmt.Sprintf("Proxying request to %s", host+port), nil)
			err = proxyNotification(host+port, req.URL.Path)
			if err != nil {
				self.logger.Error("main",
					fmt.Sprintf("Proxy to %s failed: %s", host+port, err),
					nil)
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

	self.logger.Info("main",
		fmt.Sprintf("setting version for %s.%s to %d", uaid, appid, vers),
		nil)
	err = store.UpdateChannel(pk, vers)

	if err != nil {
		errstr := fmt.Sprintf("Could not update channel %s.%s :: %s", uaid, appid, err)
		self.logger.Warn("main", errstr, nil)
		status, _ := sperrors.ErrToStatus(err)
		http.Error(resp, errstr, status)
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
			log.Error("main", "Unknown error", util.JsMap{"error": r.(error).Error()})
		}
	}(sock.Logger)

	go NewWorker(self.config).Run(sock)
	for {
		select {
		case serv_cmd := <-sock.Scmd:
			result, args := HandleServerCommand(serv_cmd, &sock)
			sock.Logger.Debug("main",
				fmt.Sprintf("Returning Result %s", result),
				nil)
			sock.Scmd <- PushCommand{result, args}
		}
	}
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
