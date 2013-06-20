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
    "io/ioutil"
	"log"
	"net/http"
    "net/url"
	"os"
    "strconv"
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

// -- main
func main() {

	var configFile string

	flag.StringVar(&configFile, "config", "config.ini", "Configuration File")
	flag.Parse()
	log.Printf("Using config %s", configFile)
	config := util.MzGetConfig(configFile)

	if _, ok := config["shard.currentHost"]; !ok {
		currentHost := "localhost"
		if val := os.Getenv("HOST"); len(val) > 0 {
			currentHost = val
		} else {
            val, ok := config["shard.use_aws_host"]
            if ok {
                usehost, err := strconv.ParseBool(val.(string))
                if err ==nil && usehost {
                    var awsHost string
                    var err error
                    awsHost, err = awsGetPublicHostname()
                    if err == nil {
                        currentHost = awsHost
                    }
                }
            }
        }
		config["shard.currentHost"] = currentHost
	}

	log.Printf("CurrentHost: %s", config["shard.currentHost"])

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
	http.HandleFunc("/update/", makeHandler(simplepush.UpdateHandler))
	http.HandleFunc("/status/", makeHandler(simplepush.StatusHandler))
	http.Handle("/", websocket.Handler(simplepush.PushSocketHandler))

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
