/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package main

import (
	"code.google.com/p/go.net/websocket"
	"github.com/gorilla/mux"
	"mozilla.org/simplepush"
	"mozilla.org/simplepush/router"
	"mozilla.org/simplepush/storage"
	"mozilla.org/util"

	"encoding/base64"
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
	logger  *util.MzLogger
	metrics *util.Metrics
	store   *storage.Store
	route   *router.Router
)

const SIGUSR1 = syscall.SIGUSR1
const VERSION = "1.2"

// -- main
func main() {
	var certFile string
	var keyFile string
	var key []byte
	var wsport string
	var wshost string

	flag.Parse()
	config, err := util.ReadMzConfig(*configFile)
	if err != nil {
		log.Fatal(err.Error())
	}
	// The config file requires some customization and normalization
	config = simplepush.FixConfig(config)
	config.Override("VERSION", VERSION)
	runtime.GOMAXPROCS(runtime.NumCPU())

	// Report what the app believes the current host to be, and what version.
	log.Printf("CurrentHost: %s, Version: %s",
		config.Get("shard.current_host", "localhost"), VERSION)

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
		config.Override("logger.enable", "1")
		config.Override("logger.filter", strconv.FormatInt(int64(*logging), 10))
	}
	logger = util.NewMzLogger(config)

	metrics := util.NewMetrics(config.Get(
		"metrics.prefix",
		"simplepush"),
		logger,
		config)

	// Currently, we're opting for a memcache "storage" mechanism, however
	// and key/value store would suffice. (bonus points if the records are
	// self expiring.)
	store = storage.NewStore(config, logger)

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
	route = router.New(config, logger, metrics, store, simplepush.ClientCollision)
	if route == nil {
		log.Fatal("No router")
	}

	token_str := config.Get("token_key", "")
	if len(token_str) > 0 {
		key, err = base64.URLEncoding.DecodeString(token_str)
		if err != nil {
			log.Fatal(err.Error())
		}
	}

	// Initialize the common server.
	simplepush.InitServer(config, logger, metrics, store, key)
	handlers := simplepush.NewHandler(config, logger, store, route, metrics, key)

	// Config the server
	RESTMux := mux.NewRouter()
	WSMux := RESTMux
	RouteMux := mux.NewRouter()
	host := config.Get("host", "localhost")
	port := config.Get("port", "8080")

	// Register the handlers
	// each websocket gets it's own handler.
	if config.Get("wsport", port) != port {
		wsport = config.Get("wsport", port)
		wshost = config.Get("wshost", host)
		WSMux = mux.NewRouter()
	}

	RESTMux.HandleFunc("/update/{key}", handlers.UpdateHandler)
	RESTMux.HandleFunc("/status/", handlers.StatusHandler)
	RESTMux.HandleFunc("/realstatus/", handlers.RealStatusHandler)
	RESTMux.HandleFunc("/metrics/", handlers.MetricsHandler)
	WSMux.Handle("/", websocket.Handler(handlers.PushSocketHandler))

	RouteMux.HandleFunc("/route/{uaid}", handlers.RouteHandler)

	// Hoist the main sail.
	if logger != nil {
		logger.Info("main",
			fmt.Sprintf("listening on %s:%s", host, port), nil)
	}

	// Get the (optional) SSL certs
	certFile = config.Get("ssl.certfile", "")
	keyFile = config.Get("ssl.keyfile", "")
	wscertFile := config.Get("ssl.ws.certfile", certFile)
	wskeyFile := config.Get("ssl.ws.keyfile", keyFile)

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
			errChan <- http.ListenAndServeTLS(addr, certFile, keyFile, RESTMux)
		} else {
			errChan <- http.ListenAndServe(addr, RESTMux)
		}
	}()

	go func() {
		addr := ":" + config.Get("shard.port", "3000")
		if logger != nil {
			logger.Info("main", "Starting Router", util.Fields{"port": addr})
			// TODO: Need TLS?
		}
		errChan <- http.ListenAndServe(addr, RouteMux)
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

	select {
	case err := <-errChan:
		if err != nil {
			panic("ListenAndServe: " + err.Error())
		}
	case <-sigChan:
		if logger != nil {
			logger.Info("main", "Recieved signal, shutting down.", nil)
			route.Unregister()
		}
		route = nil
	}
}

// 04fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
