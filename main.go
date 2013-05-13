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
    "encoding/json"
    "errors"
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
    uaid string             // id
    socket *websocket.Conn  // Remote connection
    done chan bool          // thread close flag
    scmd chan PushCommand   // server command channel
    ccmd chan PushCommand   // client command channel
    store *storage.Storage
}

//   -- goproc funcs

func (sock PushWS) Close() error {
    log.Printf("INFO : Closing socket %s \n", sock.uaid)
    sock.scmd <- PushCommand{UNREG, sock.uaid}
    sock.done <- true
    // remove from the map registry
    return nil
}

//    -- Workers
//      these write back to the websocket.

func sniffer(socket *websocket.Conn, in chan storage.JsMap) {
    // Sniff the websocket for incoming data.
    var raw []byte
    var buffer storage.JsMap
    for {
        if Safety = Safety + 1; Safety > 10 {
            panic("Safety")
        }
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
    websocket.JSON.Send(sock.socket,
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
    go sniffer(sock.socket, in)
    for {
        select {
            case cmd := <-sock.ccmd:
                log.Printf("INFO : Client Run cmd: %s", cmd)
                if cmd.command == FLUSH {
                    log.Printf("INFO : Flushing... %s", sock.uaid);
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


func PS_hello(sock *PushWS, buffer interface{}) (err error) {
    // register the UAID
    data := buffer.(storage.JsMap)
    if data["uaid"] == nil {
        data["uaid"], _ = simplepush.GenUUID4()
    }
    sock.uaid = data["uaid"].(string)
    // register the sockets
    // register any proprietary connection requirements
    // alert the master of the new UAID.
    cmd := PushCommand{HELLO, storage.JsMap{
         "uaid": data["uaid"],
         "chids": data["channelIDs"]}}
    // blocking call back to the boss.
    sock.scmd<- cmd
    result := <-sock.scmd
    log.Printf("INFO : sending HELLO response....")
    websocket.JSON.Send(sock.socket, storage.JsMap{
                    "messageType": data["messageType"],
                    "status": result.command,
                    "uaid": data["uaid"]})
    if (err == nil) {
        // Get the lastAccessed time from wherever
        PS_flush(*sock, 0)
    }
    return err
}


func PS_ack(sock PushWS, buffer interface{}) (err error) {
    res := sock.store.Ack(sock.uaid, buffer.(storage.JsMap))
    // Get the lastAccessed time from wherever.
    if res.Success {
        PS_flush(sock, 0)
    }
    return res.Err
}


func PS_register(sock PushWS, buffer interface{}) (err error) {
    data := buffer.(storage.JsMap)
    appid := data["channelID"].(string)
    res := sock.store.RegisterAppID(sock.uaid, appid, "")
    if !res.Success {
        log.Printf("ERROR: RegisterAppID failed %s", res.Err)
        handleError(sock, res.Err, res.Status)
        return err
    }
    // have the server generate the callback URL.
    cmd := PushCommand{command: REGIS,
                       arguments: data}
    sock.scmd<- cmd
    result := <-sock.scmd
    log.Printf("DEBUG: Server returned %s", result)
    endpoint := result.arguments.(storage.JsMap)["pushEndpoint"].(string)
    // return the info back to the socket
    log.Printf("INFO : Sending REGIS response....")
    websocket.JSON.Send(sock.socket, storage.JsMap{
        "messageType": data["messageType"],
        "status": result.command,
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
    err = sock.store.DeleteAppID(sock.uaid, appid, false)
    if err != nil {
        return handleErr(sock, err)
    }
    log.Printf("INFO : Sending UNREG response ..")
    websocket.JSON.Send(sock.socket, storage.JsMap{
        "messageType": data["messageType"],
        "status":200,
        "channelID": appid})
    return err
}


func PS_flush(sock PushWS, lastAccessed int64) {
    // flush pending data back to Client
    outBuffer := make(storage.JsMap)
    outBuffer["messageType"] = "notification"
    if sock.uaid == "" {
        log.Printf("ERROR: Undefined UAID for socket. Aborting.")
        sock.done <- true
    }
    // Fetch the pending updates from #storage
    updates, err := sock.store.GetUpdates(sock.uaid, lastAccessed)
    if err != nil {
        handleErr(sock, err)
        return
    }
    if updates == nil {
        return
    }
    log.Printf("INFO : Flushing data back to socket", updates)
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
var Safety int


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

func srv_hello(cmd PushCommand, sock *PushWS) (result int, arguments storage.JsMap) {
    args := cmd.arguments.(storage.JsMap)
    log.Printf("INFO : handling 'hello'", args)

    // overwrite previously registered UAIDs
    // Raw client
    var uaid string
    if args["uaid"] == "" {
        uaid, _ = simplepush.GenUUID4()
        log.Printf("INFO : Generating new UAID %s", uaid)
    } else {
        uaid = args["uaid"].(string)
        log.Printf("INFO :Using existing UAID '%s'", uaid)
        delete (args, "uaid")
    }

    prop := srv_set_proprietary_info(args)
    log.Printf("INFO : New prop %s", prop)

    // Add the ChannelIDs?
    sock.uaid = uaid
    client := &Client{Pushws: *sock,
                      UAID: uaid,
                      Prop: prop}
    Clients[uaid] = client

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
        config["pushEndpoint"] = "http://localhost/update/<token>"
    }
    // Generate the call back URL
    token := storage.GenPK(sock.uaid, args["channelID"].(string))
    log.Printf("INFO : UAID %s, channel %s", sock.uaid,
               args["channelID"].(string))
    // cheezy variable replacement.
    args["pushEndpoint"] = strings.Replace(config["pushEndpoint"].(string),
        "<token>", token, -1)
    log.Printf("INFO : regis generated callback %s", args["pushEndpoint"])
    return 200, args
}

func srv_clientPing(prop *ClientProprietary) (err error) {
    // Perform whatever steps are needed to remotely wake the client.

    return nil
}


func srv_requestFlush(client *Client) (err error) {
    defer func(client *Client) {
        r :=recover()
        if r != nil {
            log.Printf("ERROR: requestFlush failed  %s", r)
            if client != nil {
                srv_clientPing(client.Prop)
            }
        }
        return
    }(client)

    if client != nil {

        log.Printf("INFO : Requesting flush for %s", client.UAID, client.Pushws)
        client.Pushws.ccmd <- PushCommand{command: FLUSH,
                              arguments: &storage.JsMap{"uaid": client.UAID}}
    }
    return nil
}

func handleMasterCommand(cmd PushCommand, sock PushWS, config storage.JsMap) (result int, args storage.JsMap){
    log.Printf("INFO : Server Handling command %s", cmd)
//    chids := cmd.arguments.(storage.JsMap)["chids"].([]interface{})
//    for key := range chids {
//        log.Printf("\t %s", chids[key].(string))
//    }
    var ret storage.JsMap
    if cmd.arguments != nil {
        args = cmd.arguments.(storage.JsMap)
    } else {
        args = make(storage.JsMap)
    }

    // hello: add to the map registry
    // delete: remove from the map registry
    switch int(cmd.command) {
        case HELLO:
            log.Printf("INFO : Server Handling HELLO event...");
            result, ret = srv_hello(cmd, &sock)
        case UNREG:
            log.Printf("INFO : Server Handling UNREG event...");
            result, ret = srv_unreg(cmd, sock)
        case REGIS:
            log.Printf("INFO : Server handling REGIS event...")
            result, ret = srv_regis(cmd, sock, config)
    }

    args["uaid"] = ret["uaid"]
    return result, args
}


func PushSocketHandler(ws *websocket.Conn) {
    // can we pass this in somehow?

    config := getConfig("config.ini")
    store := storage.New(config)
    s := PushWS{uaid:"",
                    socket:ws,
                    done: make(chan bool),
                    scmd: make(chan PushCommand),
                    ccmd: make(chan PushCommand),
                    store: store}
    go PS_Run(s)
    for {
        select {
            case <-s.done:
                log.Printf("DEBUG: Killing handler for %s", s.uaid)
                delete(Clients, string(s.uaid))
                return
            case serv_cmd:= <-s.scmd:
                result, args := handleMasterCommand(serv_cmd, s, config)
                log.Printf("DEBUG: Returning Result", result)
                s.scmd<- PushCommand{result, args}
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
        srv_requestFlush(client)
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
    Safety = 0

    Clients = make(map[string]*Client)

    // Register the handlers
    http.Handle("/ws", websocket.Handler(PushSocketHandler))
    http.HandleFunc("/update/", makeHandler(UpdateHandler))
    http.HandleFunc("/status/", makeHandler(StatusHandler))

    // Config the server
    host := get(config, "host", "localhost")
    port := get(config, "port", "8080")

    // Hoist the main sail
    log.Printf("INFO : listening on %s:%s", host, port)
    err := http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
    if err != nil {
        panic ("ListenAndServe: " + err.Error())
    }
}

