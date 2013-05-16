package simplepush

import (
    "code.google.com/p/go.net/websocket"
    "mozilla.org/util"

    "encoding/json"
    "fmt"
    "errors"
    "log"
    "strings"
    "time"
)

//    -- Workers
//      these write back to the websocket.

type Worker struct {
    log *util.HekaLogger
}


func NewWorker(config util.JsMap) *Worker {
    return &Worker{log: util.NewHekaLogger(config)}
}

func (self *Worker) sniffer(socket *websocket.Conn, in chan util.JsMap) {
    // Sniff the websocket for incoming data.
    var raw []byte
    var buffer util.JsMap
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

func (self *Worker) handleErr(sock PushWS, err error) (ret error){
    return self.handleError(sock, err, 500)
}

func (self *Worker) handleError(sock PushWS, err error, status int) (ret error){
    self.log.Info("worker", fmt.Sprintf("Sending error %s", err), nil)
    websocket.JSON.Send(sock.Socket,
        util.JsMap {
            "messageType": err.Error(),
            "status": status})
    return nil
}


func (self *Worker) Run(sock PushWS) {
    // This is the socket
    // read the incoming json
    var err error

    in := make(chan util.JsMap)
    go self.sniffer(sock.Socket, in)
    for {
        select {
            case cmd := <-sock.Ccmd:
                log.Printf("INFO : Client Run cmd: %s", cmd)
                if cmd.Command == FLUSH {
                    self.log.Info("worker",
                        fmt.Sprintf("Flushing... %s", sock.Uaid), nil);
                    self.Flush(sock, time.Now().UTC().Unix())
                }
            case buffer := <-in:
                self.log.Info("worker", fmt.Sprintf("INFO : Client Read buffer, %s \n", buffer), nil)
                // process the commands
                switch strings.ToLower(buffer["messageType"].(string)) {
                    case "hello":
                        err = self.Hello(&sock, buffer)
                    case "ack":
                        err = self.Ack(sock, buffer)
                    case "register":
                        err = self.Register(sock, buffer)
                    case "unregister":
                        err = self.Unregister(sock, buffer)
                    default:
                        self.log.Warn("worker",
                            fmt.Sprintf("I have no idea what [%s] is.", buffer),
                            nil)
                        websocket.JSON.Send(sock.Socket,
                            util.JsMap{
                                "messageType": buffer["messageType"],
                                "status": 401})
                }
                if err != nil {
                    self.handleErr(sock, err)
                }
       }
   }
}


func (self *Worker) Hello(sock *PushWS, buffer interface{}) (err error) {
    // register the UAID
    data := buffer.(util.JsMap)
    if data["uaid"] == nil {
        data["uaid"], _ = GenUUID4()
    }
    sock.Uaid = data["uaid"].(string)
    // register the sockets
    // register any proprietary connection requirements
    // alert the master of the new UAID.
    cmd := PushCommand{HELLO, util.JsMap{
         "uaid": data["uaid"],
         "chids": data["channelIDs"]}}
    // blocking call back to the boss.
    sock.Scmd<- cmd
    result := <-sock.Scmd
    self.log.Info("worker", "sending HELLO response....", nil)
    websocket.JSON.Send(sock.Socket, util.JsMap{
                    "messageType": data["messageType"],
                    "status": result.Command,
                    "uaid": data["uaid"]})
    if (err == nil) {
        // Get the lastAccessed time from wherever
        self.Flush(*sock, 0)
    }
    return err
}


func (self *Worker) Ack(sock PushWS, buffer interface{}) (err error) {
    res := sock.Store.Ack(sock.Uaid, buffer.(util.JsMap))
    // Get the lastAccessed time from wherever.
    if res.Success {
        self.Flush(sock, 0)
    }
    return res.Err
}


func (self *Worker) Register(sock PushWS, buffer interface{}) (err error) {
    data := buffer.(util.JsMap)
    appid := data["channelID"].(string)
    res := sock.Store.RegisterAppID(sock.Uaid, appid, "")
    if !res.Success {
        self.handleError(sock, res.Err, res.Status)
        self.log.Error("worker",
                       fmt.Sprintf("ERROR: RegisterAppID failed %s", res.Err),
                       nil)
        return err
    }
    // have the server generate the callback URL.
    cmd := PushCommand{Command: REGIS,
                       Arguments: data}
    sock.Scmd<- cmd
    result := <-sock.Scmd
    self.log.Debug("worker", fmt.Sprintf("Server returned %s", result), nil)
    endpoint := result.Arguments.(util.JsMap)["pushEndpoint"].(string)
    // return the info back to the socket
    self.log.Info("worker", "Sending REGIS response....", nil)
    websocket.JSON.Send(sock.Socket, util.JsMap{
        "messageType": data["messageType"],
        "status": result.Command,
        "pushEndpoint": endpoint})
    return res.Err
}


func (self *Worker) Unregister(sock PushWS, buffer interface{}) (err error) {
    data := buffer.(util.JsMap)
    if _, ok := data["channelID"]; !ok {
        err = errors.New("Missing channelID")
        return self.handleError(sock, err, 400)
    }
    appid := data["channelID"].(string)
    err = sock.Store.DeleteAppID(sock.Uaid, appid, false)
    if err != nil {
        return self.handleErr(sock, err)
    }
    self.log.Info("worker", "Sending UNREG response ..", nil)
    websocket.JSON.Send(sock.Socket, util.JsMap{
        "messageType": data["messageType"],
        "status":200,
        "channelID": appid})
    return err
}


func (self *Worker) Flush(sock PushWS, lastAccessed int64) {
    // flush pending data back to Client
    outBuffer := make(util.JsMap)
    outBuffer["messageType"] = "notification"
    if sock.Uaid == "" {
        self.log.Error("worker", "Undefined UAID for socket. Aborting.", nil)
        sock.Done <- true
    }
    // Fetch the pending updates from #storage
    updates, err := sock.Store.GetUpdates(sock.Uaid, lastAccessed)
    if err != nil {
        self.handleErr(sock, err)
        return
    }
    if updates == nil {
        return
    }
    self.log.Info("worker", "Flushing data back to socket", updates)
    websocket.JSON.Send(sock.Socket, updates)
}

