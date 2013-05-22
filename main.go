package main

import (
//    "mozilla.org/simplepush"
//    "encoding/json"
//    "io/ioutil"
    "code.google.com/p/go.net/websocket"
    "mozilla.org/simplepush"
    "mozilla.org/util"

    "fmt"
    "net/http"
)

var logger *util.HekaLogger

// -- utils
func makeHandler(fn func (http.ResponseWriter, *http.Request, util.JsMap, *util.HekaLogger)) http.HandlerFunc {
    config := util.MzGetConfig("config.ini")
    return func(resp http.ResponseWriter, req *http.Request) {
        fn(resp, req, config, logger)
    }
}

// -- main
func main(){
    config := util.MzGetConfig("config.ini")
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
        panic ("ListenAndServe: " + err.Error())
    }
}
