package simplepush

import (
    "code.google.com/p/go.net/websocket"
    "mozilla.org/simplepush/storage"

    "encoding/json"
    "errors"
    "log"
    "strings"
    "time"
)

//    -- Workers
//      these write back to the websocket.

func sniffer(socket *websocket.Conn, in chan storage.JsMap) {
    // Sniff the websocket for incoming data.
    var raw []byte
    var buffer storage.JsMap
    for {
        websocket.Message.Receive(socket, &raw)
        if len(raw) > 0 {
            log.Printf("INFO :Socket received %s", raw)
            err := json.Unmarshal(raw, &buffer)
            if err != nil {
                panic(err)
            }
            // Only do something if there's something to do.
            in<- buffer
        }
    }
}

func handleErr(sock PushWS, err error) (ret error){
    return handleError(sock, err, 500)
}

func handleError(sock PushWS, err error, status int) (ret error){
    log.Printf("INFO : Sending error %s", err)
    websocket.JSON.Send(sock.Socket,
        storage.JsMap {
            "messageType": err.Error(),
            "status": status})
    return nil
}


func PS_Run(sock PushWS) {
    // This is the socket
    // read the incoming json
    var err error
    in := make(chan storage.JsMap)
    go sniffer(sock.Socket, in)
    for {
        select {
            case cmd := <-sock.Ccmd:
                log.Printf("INFO : Client Run cmd: %s", cmd)
                if cmd.Command == FLUSH {
                    log.Printf("INFO : Flushing... %s", sock.Uaid);
                    PS_flush(sock, time.Now().UTC().Unix())
                }
            case buffer := <-in:
                log.Printf("INFO : Client Read buffer, %s \n", buffer)
                // process the commands
                switch strings.ToLower(buffer["messageType"].(string)) {
                    case "hello":
                        err = PS_hello(&sock, buffer)
                    case "ack":
                        err = PS_ack(sock, buffer)
                    case "register":
                        err = PS_register(sock, buffer)
                    case "unregister":
                        err = PS_unregister(sock, buffer)
                    default:
                        log.Printf("WARN : I have no idea what [%s] is.", buffer)
                        websocket.JSON.Send(sock.Socket,
                            storage.JsMap{
                                "messageType": buffer["messageType"],
                                "status": 401})
                }
                if err != nil {
                    handleErr(sock, err)
                }
       }
   }
}


func PS_hello(sock *PushWS, buffer interface{}) (err error) {
    // register the UAID
    data := buffer.(storage.JsMap)
    if data["uaid"] == nil {
        data["uaid"], _ = GenUUID4()
    }
    sock.Uaid = data["uaid"].(string)
    // register the sockets
    // register any proprietary connection requirements
    // alert the master of the new UAID.
    cmd := PushCommand{HELLO, storage.JsMap{
         "uaid": data["uaid"],
         "chids": data["channelIDs"]}}
    // blocking call back to the boss.
    sock.Scmd<- cmd
    result := <-sock.Scmd
    log.Printf("INFO : sending HELLO response....")
    websocket.JSON.Send(sock.Socket, storage.JsMap{
                    "messageType": data["messageType"],
                    "status": result.Command,
                    "uaid": data["uaid"]})
    if (err == nil) {
        // Get the lastAccessed time from wherever
        PS_flush(*sock, 0)
    }
    return err
}


func PS_ack(sock PushWS, buffer interface{}) (err error) {
    res := sock.Store.Ack(sock.Uaid, buffer.(storage.JsMap))
    // Get the lastAccessed time from wherever.
    if res.Success {
        PS_flush(sock, 0)
    }
    return res.Err
}


func PS_register(sock PushWS, buffer interface{}) (err error) {
    data := buffer.(storage.JsMap)
    appid := data["channelID"].(string)
    res := sock.Store.RegisterAppID(sock.Uaid, appid, "")
    if !res.Success {
        log.Printf("ERROR: RegisterAppID failed %s", res.Err)
        handleError(sock, res.Err, res.Status)
        return err
    }
    // have the server generate the callback URL.
    cmd := PushCommand{Command: REGIS,
                       Arguments: data}
    sock.Scmd<- cmd
    result := <-sock.Scmd
    log.Printf("DEBUG: Server returned %s", result)
    endpoint := result.Arguments.(storage.JsMap)["pushEndpoint"].(string)
    // return the info back to the socket
    log.Printf("INFO : Sending REGIS response....")
    websocket.JSON.Send(sock.Socket, storage.JsMap{
        "messageType": data["messageType"],
        "status": result.Command,
        "pushEndpoint": endpoint})
    return res.Err
}


func PS_unregister(sock PushWS, buffer interface{}) (err error) {
    data := buffer.(storage.JsMap)
    if _, ok := data["channelID"]; !ok {
        err = errors.New("Missing channelID")
        return handleError(sock, err, 400)
    }
    appid := data["channelID"].(string)
    err = sock.Store.DeleteAppID(sock.Uaid, appid, false)
    if err != nil {
        return handleErr(sock, err)
    }
    log.Printf("INFO : Sending UNREG response ..")
    websocket.JSON.Send(sock.Socket, storage.JsMap{
        "messageType": data["messageType"],
        "status":200,
        "channelID": appid})
    return err
}


func PS_flush(sock PushWS, lastAccessed int64) {
    // flush pending data back to Client
    outBuffer := make(storage.JsMap)
    outBuffer["messageType"] = "notification"
    if sock.Uaid == "" {
        log.Printf("ERROR: Undefined UAID for socket. Aborting.")
        sock.Done <- true
    }
    // Fetch the pending updates from #storage
    updates, err := sock.Store.GetUpdates(sock.Uaid, lastAccessed)
    if err != nil {
        handleErr(sock, err)
        return
    }
    if updates == nil {
        return
    }
    log.Printf("INFO : Flushing data back to socket", updates)
    websocket.JSON.Send(sock.Socket, updates)
}

