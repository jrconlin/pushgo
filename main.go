package main

//#TODO: refactor

import (
//    "mozilla.org/simplepush"
//    "encoding/json"
//    "io/ioutil"
    "code.google.com/p/go.net/websocket"
    "mozilla.org/simplepush"
    "mozilla.org/simplepush/storage"

    "bufio"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "strings"
    "time"
)

// -- utils
func getConfig(filename string) (storage.JsMap) {
    config := make(storage.JsMap)
    // Yay for no equivalent to readln
    file, err := os.Open(filename)
    if err != nil {
        log.Fatal (err)
    }
    reader := bufio.NewReader(file)
    for line, err := reader.ReadString('\n');
        err == nil;
        line, err = reader.ReadString('\n') {
        // skip lines beginning with '#/;'
        if strings.Contains("#/;", string(line[0])){
            continue
        }
        kv := strings.SplitN(line, "=", 2)
        if len(kv) < 2 {
            continue
        }
        config[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
    }
    if err != nil && err != io.EOF {
        log.Panic(err)
    }
    return config
}

func get(ma storage.JsMap, key string, def string) (string) {
    val, ok := ma[key].(string)
    if ! ok {
        val = def
    }
    return val
}

//-- Handlers
// -- Websocket

const (
    UNREG = iota
    REGIS
    HELLO
    ACK
    FLUSH
    RETRN
)

type PushCommand struct {
    // Use mutable int value
    command int            //command type (UNREG, REGIS, ACK, etc)
    arguments interface {} //command arguments
}

type PushWS struct {
    uaid []byte             // id
    socket *websocket.Conn  // Remote connection
    done chan bool          // thread close flag
    cmd chan PushCommand    // internal command channel
    store *storage.Storage
}

//   -- goproc funcs

func (sock PushWS) Close() error {
    log.Printf("INFO: Closing socket %s \n", sock.uaid)
    sock.cmd <- PushCommand{UNREG, sock.uaid}
    sock.done <- true
    // remove from the map registry
    return nil
}

//    -- Workers
//      these write back to the websocket.

func sniffer(socket *websocket.Conn, in chan storage.JsMap) {
    // Sniff the websocket for incoming data.
    var buffer storage.JsMap
    for {
        websocket.JSON.Receive(socket, &buffer)
        log.Printf("DEBUG: Socket Client sent: %s", buffer)
        in<- buffer
    }
}


func handleErr(sock PushWS, err error) {
    websocket.JSON.Send(sock.socket,
        storage.JsMap {
            "messageType": err.Error(),
            "status": 500})
}


func PS_Run(sock PushWS) {
    // This is the socket
    // read the incoming json
    for {
        var err error
        in := make(chan storage.JsMap)
        go sniffer(sock.socket, in)
        select {
            case cmd := <-sock.cmd:
                if cmd.command == FLUSH {
                    log.Printf("Flushing...");
                    PS_flush(sock, time.Now().UTC().Unix())
                }
            case buffer := <-in:
                log.Printf("INFO: Read buffer, %s \n", buffer)
                // process the commands
                switch buffer["messageType"] {
                    case "hello":
                        err = PS_hello(sock, buffer)
                    case "ack":
                        err = PS_ack(sock, buffer)
                    case "register":
                        err = PS_register(sock, buffer)
                    case "unregister":
                        err = PS_unregister(sock, buffer)
                    default:
                        websocket.JSON.Send(sock.socket,
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


func PS_hello(sock PushWS, buffer interface{}) (err error) {
    // register the UAID
    data := buffer.(storage.JsMap)
    if data["uaid"] == nil {
        data["uaid"], _ = simplepush.GenUUID4()
    }
    sock.uaid = []byte(data["uaid"].(string))
    // register the sockets
    // register any proprietary connection requirements
    // alert the master of the new UAID.
    cmd := PushCommand{HELLO, storage.JsMap{
         "uaid": data["uaid"],
         "chids": data["channelIDs"]}}
    // blocking call back to the boss.
    sock.cmd<- cmd
    result := <-sock.cmd
    websocket.JSON.Send(sock.socket, storage.JsMap{
                    "messageType": data["messageType"],
                    "status": result.command,
                    "uaid": data["uaid"]})
    if (err == nil) {
        // Get the lastAccessed time from wherever
        PS_flush(sock, 0)
    }
    return err
}


func PS_ack(sock PushWS, buffer interface{}) (err error) {
    res := sock.store.Ack(string(sock.uaid), buffer.(storage.JsMap))
    // Get the lastAccessed time from wherever.
    if res.Success {
        PS_flush(sock, 0)
    }
    return res.Err
}


func PS_register(sock PushWS, buffer interface{}) (err error) {
    data := buffer.(storage.JsMap)
    appid := data["channelID"].(string)
    res := sock.store.RegisterAppID(string(sock.uaid), appid, "")
    // have the server generate the callback URL.
    cmd := PushCommand{command: REGIS,
                       arguments: data}
    sock.cmd<- cmd
    result := <-sock.cmd
    endpoint := result.arguments.(storage.JsMap)["pushEndpoint"].(string)
    // return the info back to the socket
    websocket.JSON.Send(sock.socket, storage.JsMap{
        "messageType": data["messageType"],
        "status": result.command,
        "pushEndpoint": endpoint})
    return res.Err
}


func PS_unregister(sock PushWS, buffer interface{}) (err error) {
    data := buffer.(storage.JsMap)
    appid := buffer.(storage.JsMap)["channelID"].(string)
    res := sock.store.DeleteAppID(string(sock.uaid), appid, false)
    websocket.JSON.Send(sock.socket, storage.JsMap{
        "messageType": data["messageType"],
        "status":200,
        "channelID": appid})
    return res.Err
}


func PS_flush(sock PushWS, lastAccessed int64) {
    // flush pending data back to Client
    outBuffer := make(storage.JsMap)
    outBuffer["messageType"] = "notification"
    // Fetch the pending updates from #storage
    updates, err := sock.store.GetUpdates(string(sock.uaid), lastAccessed)
    if err != nil {
        handleErr(sock, err)
        return
    }

    log.Printf("INFO: Flushing data back to socket", updates)

    websocket.JSON.Send(sock.socket, updates)
}

//    -- Master

type ClientProprietary struct {
    //-- socket proprietary information
    Ip          string              `json:"ip"`
    Port        string              `json:"port"`
    LastContact time.Time           `json:"-"`
}


type Client struct {
    Pushws      PushWS              `json:"-"`
    UAID        string              `json:"uaid"`
    Prop        *ClientProprietary   `json:"-"`
    }

var Clients map[string]*Client


func srv_set_proprietary_info(args storage.JsMap) (cp *ClientProprietary){
    ip := ""
    port := ""
    lastContact := time.Now()

    if args["ip"] != nil {
        ip = args["ip"].(string)
    }
    if args["port"] != nil {
        port = args["port"].(string)
    }

    return &ClientProprietary{ip, port, lastContact}
}

func srv_hello(cmd PushCommand, sock PushWS) (result int, arguments storage.JsMap) {
    args := cmd.arguments.(storage.JsMap)
    log.Printf("INFO: handling 'hello'", args)

    // overwrite previously registered UAIDs
    // Raw client
    var uaid string
     if args["uaid"] == nil {
        uaid, _ = simplepush.GenUUID4()
        log.Printf("Generating new UAID %s", uaid)
    } else {
        uaid = args["uaid"].(string)
        log.Printf("Using existing UAID '%s'", uaid)
        delete (args, "uaid")
    }

    prop := srv_set_proprietary_info(args)

    // Add the ChannelIDs?
    Clients[uaid] = &Client{Pushws: sock,
                             UAID: uaid,
                             Prop: prop}

    // We don't really care, since we report back all channelIDs for
    //  a given UAID.
    args["uaid"] = uaid
    arguments = args
    result = 200
    return result, arguments
}

func srv_unreg(cmd PushCommand, sock PushWS) (result int, arguments storage.JsMap) {
    // This is effectively a no-op, since we don't hold client session info
    args := cmd.arguments.(storage.JsMap)
    args["status"] = 200
    return 200, args
}

func srv_regis(cmd PushCommand, sock PushWS, config storage.JsMap) (result int, arguments storage.JsMap) {
    args := cmd.arguments.(storage.JsMap)
    args["status"] = 200
    if _, ok := config["pushEndpoint"]; !ok {
        config["pushEndpoint"] = "http://localhost/<token>"
    }
    // Generate the call back URL
    token := storage.GenPK(string(sock.uaid), args["channelID"].(string))
    // cheezy variable replacement.
    args["pushEndpoint"] = strings.Replace(config["pushEndpoint"].(string),
        "<token>", token, -1)
    return 200, args
}

func srv_clientPing(prop *ClientProprietary) (err error) {
    // Perform whatever steps are needed to remotely wake the client.

    return nil
}


func srv_requestFlush(client *Client) (err error) {
    defer func(client *Client) {
        r :=recover()
        log.Printf("ERROR: %s", r)
        srv_clientPing(client.Prop)
        return
    }(client)

    if srv, ok := Clients[client.UAID]; ok {
        srv.Pushws.cmd <- PushCommand{command: FLUSH,
                                       arguments: nil}
        return err
    }
    return srv_clientPing(client.Prop)
}

func handleMasterCommand(cmd PushCommand, sock PushWS, config storage.JsMap) (result int, args storage.JsMap){
    log.Printf("INFO: Server Handling command %s", cmd)
//    chids := cmd.arguments.(storage.JsMap)["chids"].([]interface{})
//    for key := range chids {
//        log.Printf("\t %s", chids[key].(string))
//    }
    var ret storage.JsMap
    args = cmd.arguments.(storage.JsMap)
    args["uaid"] = ret["uaid"]

    // hello: add to the map registry
    // delete: remove from the map registry
    switch int(cmd.command) {
        case HELLO:
            log.Printf("INFO: Server Handling HELLO event...");
            result, ret = srv_hello(cmd, sock)
        case UNREG:
            log.Printf("INFO: Server Handling UNREG event...");
            result, ret = srv_unreg(cmd, sock)
        case REGIS:
            log.Printf("INFO: Server handling REGIS event...")
            result, ret = srv_regis(cmd, sock, config)
    }

    return result, args
}


func PushSocketHandler(ws *websocket.Conn) {
    // can we pass this in somehow?

    config := getConfig("config.ini")
    store := storage.New(config)
    s := PushWS{uaid:nil,
                    socket:ws,
                    done: make(chan bool),
                    cmd: make(chan PushCommand),
                    store: store}
    go PS_Run(s)
    for {
        select {
            case <-s.done:
                log.Printf("DEBUG: Killing handler for %s", s.uaid)
                delete(Clients, string(s.uaid))
                return
            case cmd:= <-s.cmd:
                result, args := handleMasterCommand(cmd, s, config)
                log.Printf("DEBUG: Returning Result", result)
                s.cmd<- PushCommand{result, args}

        }
    }
}

    // do stuff
// -- Rest
func UpdateHandler(resp http.ResponseWriter, req *http.Request, config storage.JsMap) {
    // Handle the version updates.
    log.Printf("DEBUG: A wild update appears")
    /*
    if (req.Method != "PUT") {
        http.Error(resp, "", http.StatusMethodNotAllowed)
        return
    }
    */
    vers := fmt.Sprintf("%d", time.Now().UTC().Unix())

    elements := strings.Split(req.URL.Path, "/")
    pk := elements[len(elements)-1]
    if len(pk) == 0 {
        http.Error(resp, "Token not found", http.StatusNotFound)
        return
    }

    log.Printf("INFO: setting version for %s to %s", pk, vers)
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
    if srv, ok := Clients[uaid]; ok {
        srv_requestFlush(srv)
    }
    return
}


func StatusHandler(resp http.ResponseWriter, req *http.Request, config storage.JsMap) {
    resp.Write([]byte("OK"))
}

func makeHandler(fn func (http.ResponseWriter, *http.Request, storage.JsMap)) http.HandlerFunc {
    config := getConfig("config.ini")
    return func(resp http.ResponseWriter, req *http.Request) {
        fn(resp, req, config)
    }
}

// -- main
func main(){
    config := getConfig("config.ini")

    // Register the handlers
    http.Handle("/ws", websocket.Handler(PushSocketHandler))
    http.HandleFunc("/update/", makeHandler(UpdateHandler))
    http.HandleFunc("/status/", makeHandler(StatusHandler))

    // Config the server
    host := get(config, "host", "localhost")
    port := get(config, "port", "8080")

    // Hoist the main sail
    err := http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
    if err != nil {
        panic ("ListenAndServe: " + err.Error())
    }
}

