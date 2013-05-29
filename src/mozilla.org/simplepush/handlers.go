/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush/storage"
	"mozilla.org/util"

	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// VIP response
func StatusHandler(resp http.ResponseWriter, req *http.Request, config util.JsMap, logger *util.HekaLogger) {
    // return "OK" only if all is well.
    // TODO: make sure all is well.
	resp.Write([]byte("OK"))
}

// -- REST
func UpdateHandler(resp http.ResponseWriter, req *http.Request, config util.JsMap, logger *util.HekaLogger) {
	// Handle the version updates.
	logger.Debug("main", fmt.Sprintf("Handling Update %s", req.URL.Path), nil)
	if req.Method != "PUT" {
		http.Error(resp, "", http.StatusMethodNotAllowed)
		return
	}
	vers := fmt.Sprintf("%d", time.Now().UTC().Unix())

	elements := strings.Split(req.URL.Path, "/")
	pk := elements[len(elements)-1]
	if len(pk) == 0 {
		logger.Error("main", "No token, rejecting request", nil)
		http.Error(resp, "Token not found", http.StatusNotFound)
		return
	}

	store := storage.New(config, logger)
	if token, ok := config["token_key"]; ok {
		var err error
		bpk, err := Decode(token.([]byte),
			pk)
		if err != nil {
			logger.Error("main",
				fmt.Sprintf("Could not decode token %s, %s", pk, err),
				nil)
			http.Error(resp, "", http.StatusNotFound)
			return
		}

		pk = strings.TrimSpace(string(bpk))
	}

	uaid, appid, err := storage.ResolvePK(pk)
    if err != nil {
        logger.Error("main",
            fmt.Sprintf("Could not resolve PK %s, %s", pk, err), nil)
        return
    }
	logger.Info("main",
		fmt.Sprintf("setting version for %s.%s to %s", uaid, appid, vers),
		nil)
	res := store.UpdateChannel(pk, vers)

	if !res.Success {
        log.Printf("Could not update channel %s.%s :: %s", uaid, appid, res.Err)
		http.Error(resp, res.Err.Error(), res.Status)
		return
	}
	resp.Header().Set("Content-Type", "application/json")
	resp.Write([]byte("{}"))
	// Ping the appropriate server
	if client, ok := Clients[uaid]; ok {
		Flush(client)
	}
	return
}

// -- Socket
func PushSocketHandler(ws *websocket.Conn) {
	// can we pass this in somehow?
	config := util.MzGetConfig("config.ini")
	// Convert the token_key from base64 (if present)
	if k, ok := config["token_key"]; ok {
		key, _ := base64.URLEncoding.DecodeString(k.(string))
		config["token_key"] = key
	}
	logger := util.NewHekaLogger(config)
	store := storage.New(config, logger)
	sock := PushWS{Uaid: "",
		Socket: ws,
		Scmd:   make(chan PushCommand),
		Ccmd:   make(chan PushCommand),
		Store:  store,
		Logger: logger}
	go NewWorker(config).Run(sock)
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
