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
func StatusHandler(resp http.ResponseWriter, req *http.Request, config util.JsMap, logger *util.HekaLogger) {
        resp.Write([]byte("OK"))
}


// -- REST
func UpdateHandler(resp http.ResponseWriter, req *http.Request, config util.JsMap, logger *util.HekaLogger) {
    // Handle the version updates.
    logger.Debug("main","A wild update appears", nil)
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

    logger.Info("main", fmt.Sprintf("setting version for %s to %s", pk, vers), nil)
    store := storage.New(config, logger)
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
        Flush(client)
    }
    return
}


// -- Socket
func PushSocketHandler(ws *websocket.Conn) {
    // can we pass this in somehow?
    config := util.MzGetConfig("config.ini")
    logger:= util.NewHekaLogger(config)
    store := storage.New(config, logger)
    sock := PushWS{Uaid: "",
                    Socket: ws,
                    Scmd: make(chan PushCommand),
                    Ccmd: make(chan PushCommand),
                    Store: store,
                    Logger: logger}
    go NewWorker(config).Run(sock)
    for {
        select {
            case serv_cmd:= <-sock.Scmd:
                result, args := HandleServerCommand(serv_cmd, &sock)
                sock.Logger.Debug("main",
                                 fmt.Sprintf("Returning Result %s", result),
                                 nil)
                sock.Scmd<- PushCommand{result, args}
        }
    }
}

