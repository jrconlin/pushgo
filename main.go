/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush"
	"mozilla.org/util"

	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
)

var logger *util.HekaLogger

// -- utils
func makeHandler(fn func(http.ResponseWriter, *http.Request, util.JsMap, *util.HekaLogger)) http.HandlerFunc {
	config := util.MzGetConfig("config.ini")
	// Convert the token_key from base64 (if present)
	if k, ok := config["token_key"]; ok {
		key, _ := base64.URLEncoding.DecodeString(k.(string))
		config["token_key"] = key
	}

	return func(resp http.ResponseWriter, req *http.Request) {
		fn(resp, req, config, logger)
	}
}

// -- main
func main() {

	var configFile string

	flag.StringVar(&configFile, "config", "config.ini", "Configuration File")
	flag.Parse()
	log.Printf("Using config %s", configFile)
	config := util.MzGetConfig(configFile)

	// Convert the token_key from base64 (if present)
	if k, ok := config["token_key"]; ok {
		key, _ := base64.URLEncoding.DecodeString(k.(string))
		config["token_key"] = key
	}

	logger = util.NewHekaLogger(config)

	simplepush.Clients = make(map[string]*simplepush.Client)

	// Initialize the common server.
	simplepush.InitServer(config, logger)

	// Register the handlers
	// each websocket gets it's own handler.
	http.Handle("/ws", websocket.Handler(simplepush.PushSocketHandler))
	http.HandleFunc("/update/", makeHandler(simplepush.UpdateHandler))
	http.HandleFunc("/status/", makeHandler(simplepush.StatusHandler))

	// Config the server
	host := util.MzGet(config, "host", "localhost")
	port := util.MzGet(config, "port", "8080")

	// Hoist the main sail
	logger.Info("main",
		fmt.Sprintf("listening on %s:%s", host, port), nil)
	err := http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
	if err != nil {
		panic("ListenAndServe: " + err.Error())
	}
}

// 04fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
