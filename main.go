package main

//#TODO: refactor

import (
//    "mozilla.org/simplepush"
//    "encoding/json"
//    "io/ioutil"
    "code.google.com/p/go.net/websocket"
    "mozilla.org/simplepush"
    "mozilla.org/util"
    "mozilla.org/simplepush/storage"

    "fmt"
    "log"
    "net/http"
)

// -- utils
func makeHandler(fn func (http.ResponseWriter, *http.Request, storage.JsMap)) http.HandlerFunc {
    config := util.MzGetConfig("config.ini")
    return func(resp http.ResponseWriter, req *http.Request) {
        fn(resp, req, config)
    }
}

// -- main
func main(){
    config := util.MzGetConfig("config.ini")

    simplepush.Clients = make(map[string]*simplepush.Client)

    // Register the handlers
    http.Handle("/ws", websocket.Handler(simplepush.PushSocketHandler))
    http.HandleFunc("/update/", makeHandler(simplepush.UpdateHandler))
    http.HandleFunc("/status/", makeHandler(simplepush.StatusHandler))

    // Config the server
    host := util.MzGet(config, "host", "localhost")
    port := util.MzGet(config, "port", "8080")

    // Hoist the main sail
    log.Printf("INFO : listening on %s:%s", host, port)
    err := http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
    if err != nil {
        panic ("ListenAndServe: " + err.Error())
    }
}

