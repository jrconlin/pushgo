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
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
)

var (
	configFile *string = flag.String("config", "config.ini", "Configuration File")
	profile    *string = flag.String("profile", "", "Profile file output")
	logger     *mozutil.HekaLogger
	store      *storage.Storage
)

const SIGUSR1 = syscall.SIGUSR1

// -- main
func main() {
	flag.Parse()
	config := mozutil.MzGetConfig(*configFile)

	config = simplepush.FixConfig(config)
	log.Printf("CurrentHost: %s", config["shard.current_host"])

	if *profile != "" {
		f, err := os.Create(*profile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

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

	// wait for sigint
	sigChan := make(chan os.Signal)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGHUP, SIGUSR1)

	errChan := make(chan error)
	go func() {
		errChan <- http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			panic("ListenAndServe: " + err.Error())
		}
	case <-sigChan:
		logger.Info("main", "Recieved signal, shutting down.", nil)
	}
}

// 04fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
