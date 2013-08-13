/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush"
	storage "mozilla.org/simplepush/storage/mcstorage"
	mozutil "mozilla.org/util"

	"flag"
	"fmt"
	"log"
	"net/http"
)

var logger *mozutil.HekaLogger
var store *storage.Storage

// -- main
func main() {

	var configFile string

	flag.StringVar(&configFile, "config", "config.ini", "Configuration File")
	flag.Parse()
	config := mozutil.MzGetConfig(configFile)

	config = simplepush.FixConfig(config)
	log.Printf("CurrentHost: %s", config["shard.current_host"])

	logger = mozutil.NewHekaLogger(config)
	store = storage.New(config, logger)

	// Initialize the common server.
	simplepush.InitServer(config, logger)
	handlers := simplepush.NewHandler(config, logger, store)

	// Register the handlers
	// each websocket gets it's own handler.
	http.HandleFunc("/update/", handlers.UpdateHandler)
	http.HandleFunc("/status/", handlers.StatusHandler)
	http.HandleFunc("/realstatus/", handlers.RealStatusHandler)
	http.Handle("/", websocket.Handler(handlers.PushSocketHandler))

	// Config the server
	host := mozutil.MzGet(config, "host", "localhost")
	port := mozutil.MzGet(config, "port", "8080")

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
