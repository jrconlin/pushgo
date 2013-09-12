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
    "runtime"
	"runtime/pprof"
	"strconv"
	"syscall"
)

var (
	configFile *string = flag.String("config", "config.ini", "Configuration File")
	profile    *string = flag.String("profile", "", "Profile file output")
	memProfile *string = flag.String("memProfile", "", "Profile file output")
	logging    *int    = flag.Int("logging", 0,
		"logging level (0=none,1=critical ... 10=verbose")
	logger *util.HekaLogger
	store  *storage.Storage
	route  *router.Router
)

const SIGUSR1 = syscall.SIGUSR1
const VERSION = "0.6"

// -- main
func main() {
	var certFile string
	var keyFile string

	flag.Parse()
	config := util.MzGetConfig(*configFile)
	// The config file requires some customization and normalization
	config = simplepush.FixConfig(config)
	config["VERSION"] = VERSION
    runtime.GOMAXPROCS(runtime.NumCPU())

	// Report what the app believes the current host to be, and what version.
	log.Printf("CurrentHost: %s, Version: %s",
		config["shard.current_host"], VERSION)

	// Only create profiles if requested. To view the application profiles,
	// see http://blog.golang.org/profiling-go-programs
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

	// Logging can be CPU intensive (note: variable reflection is VERY
	// CPU intensive. Avoid things like log.Printf("%v", someStruct) )
	// If logging is specified as a command line flag, it overrides the
	// value specified in the config file. This allows short term logging
	// for operations.
	if *logging > 0 {
		config["logger.enable"] = "1"
		config["logger.filter"] = strconv.FormatInt(int64(*logging), 10)
	}
	if v, ok := config["logger.enable"]; ok {
		if v, _ := strconv.ParseBool(v.(string)); v {
			logger = util.NewHekaLogger(config)
			logger.Info("main", "Enabling full logger", nil)
		}
	}

	// Routing allows stand-alone instances to send updates between themselves.
	// Websock does not allow for native redirects in some browsers. Routing
	// allows websocket connections and updates to be handled by any server,
	// with the approriate notification sent.
	//
	// Note: While this is fairly primative, it works. There are more efficient
	// models and systems that could be used for this (e.g. 0mq, rabbit, etc.)
	// however those also add additional complexity to the server system.
	// Since this is mostly point-to-point (we know the host location to send
	// to), there wasn't much justification to add that complexity.
	// Obviously, this can and will change over time.
	route = &router.Router{
		Port:   util.MzGet(config, "shard.port", "3000"),
		Logger: logger,
	}
	defer func() {
		if route != nil {
			route.CloseAll()
		}
	}()

	// Currently, we're opting for a memcache "storage" mechanism, however
	// and key/value store would suffice. (bonus points if the records are
	// self expiring.)
	store = storage.New(config, logger)

	// Initialize the common server.
	simplepush.InitServer(config, logger)
	handlers := simplepush.NewHandler(config, logger, store, route)

	// Config the server
	var wsport string
	var wshost string
	var WSMux *http.ServeMux = http.DefaultServeMux
	var RESTMux *http.ServeMux = http.DefaultServeMux
	host := util.MzGet(config, "host", "localhost")
	port := util.MzGet(config, "port", "8080")

	// Register the handlers
	// each websocket gets it's own handler.
	if util.MzGet(config, "wsport", port) != port {
		wsport = util.MzGet(config, "wsport", port)
		wshost = util.MzGet(config, "wshost", host)
		WSMux = http.NewServeMux()
	}

	RESTMux.HandleFunc("/update/", handlers.UpdateHandler)
	RESTMux.HandleFunc("/status/", handlers.StatusHandler)
	RESTMux.HandleFunc("/realstatus/", handlers.RealStatusHandler)
	RESTMux.HandleFunc("/metrics/", handlers.MetricsHandler)
	WSMux.Handle("/", websocket.Handler(handlers.PushSocketHandler))

	// Hoist the main sail.
	if logger != nil {
		logger.Info("main",
			fmt.Sprintf("listening on %s:%s", host, port), nil)
	}

	// Get the (optional) SSL certs
	if name, ok := config["ssl.certfile"]; ok {
		certFile = name.(string)
	}
	if name, ok := config["ssl.keyfile"]; ok {
		keyFile = name.(string)
	}
	wscertFile := util.MzGet(config, "ssl.ws.certfile", certFile)
	wskeyFile := util.MzGet(config, "ssl.ws.keyfile", keyFile)

	// wait for sigint
	sigChan := make(chan os.Signal)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGHUP, SIGUSR1)
	errChan := make(chan error)

	// Weigh the anchor!
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
	// Oh, we have a different context for WebSockets. Weigh that anchor too!
	if WSMux != RESTMux {
		if logger != nil {
			logger.Info("main", "Starting separate context for WS", nil)
			logger.Info("main",
				fmt.Sprintf("ws listen on %s:%s", wshost, wsport), nil)
		}
		go func() {
			wsaddr := wshost + ":" + wsport
			if len(wscertFile) > 0 && len(wskeyFile) > 0 {
				errChan <- http.ListenAndServeTLS(wsaddr, wscertFile, wskeyFile, WSMux)
			} else {
				errChan <- http.ListenAndServe(wsaddr, WSMux)
			}
		}()
	}

	// And we're underway!
	go route.HandleUpdates(updater)

	select {
	case err := <-errChan:
		if err != nil {
			panic("ListenAndServe: " + err.Error())
		}
	case <-sigChan:
		if logger != nil {
			logger.Info("main", "Recieved signal, shutting down.", nil)
		}
		route.CloseAll()
		route = nil
	}
}

// Handle a routed update.
func updater(update *router.Update) (err error) {
	//log.Printf("UPDATE::: %s", update)
	simplepush.MetricIncrement("routing update: in")
	pk, _ := storage.GenPK(update.Uaid, update.Chid)
	err = store.UpdateChannel(pk, update.Vers)
	if client, ok := simplepush.Clients[update.Uaid]; ok {
		simplepush.Flush(client, update.Chid, int64(update.Vers))
	}
	return nil
}

// 04fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
