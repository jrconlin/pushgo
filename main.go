/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"code.google.com/p/go.net/websocket"
	"mozilla.org/simplepush"
	"mozilla.org/simplepush/router"
	storage "mozilla.org/simplepush/storage/mcstorage"
	"mozilla.org/util"

	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"strconv"
	"syscall"
)

var (
	configFile *string = flag.String("config", "config.ini", "Configuration File")
	profile    *string = flag.String("profile", "", "Profile file output")
	memProfile *string = flag.String("memProfile", "", "Profile file output")
	logging    *bool   = flag.Bool("logging", true, "Whether logging is enabled")
	logger     *util.HekaLogger
	store      *storage.Storage
)

const SIGUSR1 = syscall.SIGUSR1

// -- main
func main() {
	flag.Parse()
	config := util.MzGetConfig(*configFile)

	config = simplepush.FixConfig(config)
	log.Printf("CurrentHost: %s", config["shard.current_host"])

	if *profile != "" {
		log.Printf("Creating profile...")
		f, err := os.Create(*profile)
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			log.Printf("Closing profile...")
			pprof.StopCPUProfile()
		}()
		pprof.StartCPUProfile(f)
	}
	//  Disable logging for high capacity runs
	if v, ok := config["logger.enable"]; ok {
		if v, _ := strconv.ParseBool(v.(string)); v {
			logger = util.NewHekaLogger(config)
			logger.Info("main", "Enabling full logger", nil)
		}
	}
	router := &router.Router{
		Port: util.MzGet(config, "shard.port", "3000"),
        Logger: logger,
	}
	if *memProfile != "" {
		defer func() {
			profFile, err := os.Create(*memProfile)
			if err != nil {
				log.Fatalln(err)
			}
			pprof.WriteHeapProfile(profFile)
			profFile.Close()
		}()
	}

	store = storage.New(config, logger)

	// Initialize the common server.
	simplepush.InitServer(config, logger)
	handlers := simplepush.NewHandler(config, logger, store, router)

	// Register the handlers
	// each websocket gets it's own handler.
	http.HandleFunc("/update/", handlers.UpdateHandler)
	http.HandleFunc("/status/", handlers.StatusHandler)
	http.HandleFunc("/realstatus/", handlers.RealStatusHandler)
	http.Handle("/", websocket.Handler(handlers.PushSocketHandler))

	// Config the server
	host := util.MzGet(config, "host", "localhost")
	port := util.MzGet(config, "port", "8080")

	// Hoist the main sail
	if logger != nil {
		logger.Info("main",
			fmt.Sprintf("listening on %s:%s", host, port), nil)
	}

	var certFile string
	var keyFile string
	if name, ok := config["ssl.certfile"]; ok {
		certFile = name.(string)
	}
	if name, ok := config["ssl.keyfile"]; ok {
		keyFile = name.(string)
	}

	// wait for sigint
	sigChan := make(chan os.Signal)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGHUP, SIGUSR1)

	errChan := make(chan error)
	go func() {
		addr := host + ":" + port
		if len(certFile) > 0 && len(keyFile) > 0 {
			if logger != nil {
				logger.Info("main", "Using TLS", nil)
			}
			errChan <- http.ListenAndServeTLS(addr, certFile, keyFile, nil)
		} else {
			errChan <- http.ListenAndServe(addr, nil)
		}
	}()

	go router.HandleUpdates(updater)

	select {
	case err := <-errChan:
		if err != nil {
			panic("ListenAndServe: " + err.Error())
		}
	case <-sigChan:
		if logger != nil {
			logger.Info("main", "Recieved signal, shutting down.", nil)
		}
	}
}

func updater(update *router.Update) (err error) {
	log.Printf("UPDATE::: %s", update)
	pk, _ := storage.GenPK(update.Uaid, update.Chid)
	err = store.UpdateChannel(pk, update.Vers)
	if client, ok := simplepush.Clients[update.Uaid]; ok {
		simplepush.Flush(client, update.Chid, int64(update.Vers))
	}
	return nil
}

// 04fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
