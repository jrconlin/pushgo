package simplepush

import (

    "mozilla.org/simplepush/storage"
    "mozilla.org/util"
    "code.google.com/p/go.net/websocket"

    "net/http"
    "log"
    "fmt"
    "strings"
    "time"

)

// VIP response
func StatusHandler(resp http.ResponseWriter, req *http.Request, config storage. JsMap) {
        resp.Write([]byte("OK"))
    }


// -- Rest
func UpdateHandler(resp http.ResponseWriter, req *http.Request, config storage.JsMap) {
    // Handle the version updates.
    log.Printf("DEBUG: A wild update appears")
    if false {
    if (req.Method != "PUT") {
        http.Error(resp, "", http.StatusMethodNotAllowed)
        return
    }
}
    vers := fmt.Sprintf("%d", time.Now().UTC().Unix())

    elements := strings.Split(req.URL.Path, "/")
    pk := elements[len(elements)-1]
    if len(pk) == 0 {
        http.Error(resp, "Token not found", http.StatusNotFound)
        return
    }

    log.Printf("INFO : setting version for %s to %s", pk, vers)
    store := storage.New(config)
    res := store.UpdateChannel(pk, vers)
    uaid, _ := storage.ResolvePK(pk)

    if !res.Success {
        log.Printf("%s", res.Err)
        http.Error(resp, res.Err.Error(), res.Status)
        return
    }
    resp.Header().Set("Content-Type", "application/json")
    resp.Write([]byte("{}"))
    // Ping the appropriate server
    if client, ok := Clients[uaid]; ok {
        Srv_requestFlush(client)
    }
    return
}

func PushSocketHandler(ws *websocket.Conn) {
    // can we pass this in somehow?
    config := util.MzGetConfig("config.ini")
    store := storage.New(config)
    s := PushWS{Uaid:"",
                    Socket:ws,
                    Done: make(chan bool),
                    Scmd: make(chan PushCommand),
                    Ccmd: make(chan PushCommand),
                    Store: store}
    go PS_Run(s)
    for {
        select {
            case <-s.Done:
                log.Printf("DEBUG: Killing handler for %s", s.Uaid)
                delete(Clients, string(s.Uaid))
                return
            case serv_cmd:= <-s.Scmd:
                result, args := Srv_handleMasterCommand(serv_cmd, s, config)
                log.Printf("DEBUG: Returning Result", result)
                s.Scmd<- PushCommand{result, args}
        }
    }
}

