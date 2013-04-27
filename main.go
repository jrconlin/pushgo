package main


import (
//    "mozilla.org/simplepush"
//    "encoding/json"
//    "io/ioutil"
    "code.google.com/p/go.net/websocket"
    "mozilla.org/simplepush"
    "mozilla.org/simplepush/storage"
    "fmt"
    "bufio"
    "io"
    "log"
    "strings"
    "os"
    "net/http"
    "time"
)

// -- utils
func getConfig(filename string) (map[string]string) {
    config := make(map[string]string)
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
            log.Printf("Ignoring invalid line %s", line)
            continue
        }
        config[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
    }
    if err != nil && err != io.EOF {
        log.Panic(err)
    }
    return config
}

func get(ma map[string]string, key string, def string) (string) {
    val, ok := ma[key]
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

type jsMap map[string]interface{}

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

func sniffer(socket *websocket.Conn, in chan jsMap) {
    // Sniff the websocket for incoming data.
    var buffer jsMap
    for {
        websocket.JSON.Receive(socket, &buffer)
        log.Printf("Sniffed:", buffer)
        in<- buffer
    }
}


func handleErr(sock PushWS, err error) {
    websocket.JSON.Send(sock.socket,
        jsMap {
            "messageType": err.Error(),
            "status": 500})
}


func PS_Run(sock PushWS) {
    // This is the socket
    // read the incoming json
    for {
        var err error
        in := make(chan jsMap)
        go sniffer(sock.socket, in)
        select {
            case cmd := <-sock.cmd:
                if cmd.command == FLUSH {
                    log.Printf("Flushing...");
                    websocket.JSON.Send(sock.socket, cmd.arguments)
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
                            jsMap{
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
    data := buffer.(jsMap)
    if data["uaid"] == nil {
        data["uaid"], _ = simplepush.GenUUID4()
    }
    sock.uaid = []byte(data["uaid"].(string))
    // register the sockets
    // register any proprietary connection requirements
    // alert the master of the new UAID.
    cmd := PushCommand{HELLO, jsMap{
         "uaid": data["uaid"],
         "chids": data["channelIDs"]}}
    // blocking call back to the boss.
    sock.cmd<- cmd
    result := <-sock.cmd
    websocket.JSON.Send(sock.socket, jsMap{
                    "messageType": data["messageType"],
                    "status": result.command,
                    "uaid": data["uaid"]})
    if (err == nil) {
        PS_flush(sock, time.Now().Unix())
    }
    return err
}


func PS_ack(sock PushWS, buffer interface{}) (err error) {
    err = sock.store.Ack(string(sock.uaid), buffer.(jsMap))
    if err == nil {
        PS_flush(sock, time.Now().Unix())
    }
    return err
}


func PS_register(sock PushWS, buffer interface{}) (err error) {
    appid := buffer.(jsMap)["channelID"].(string)
    err = sock.store.RegisterAppID(string(sock.uaid), appid, "")
    return err
}


func PS_unregister(sock PushWS, buffer interface{}) (err error) {
    appid := buffer.(jsMap)["channelID"].(string)
    err = sock.store.DeleteAppID(string(sock.uaid), appid, false)
    return err
}


func PS_flush(sock PushWS, lastAccessed int64) {
    // flush pending data back to Client
    outBuffer := make(jsMap)
    outBuffer["messageType"] = "notification"
    // Fetch the pending updates from #storage
    outBuffer["updates"] = []jsMap{{
            "channelID": "abc",
            "version": 123}}
    outBuffer["expired"] = [][]byte{}
    websocket.JSON.Send(sock.socket, outBuffer)
}

//    -- Master

type ClientProprietary struct {
    //-- socket proprietary information
    Ip          string              `json:"ip"`
    Port        string              `json:"port"`
    LastContact time.Time           `json:"-"`
}


type Client struct {
    Websocket   *websocket.Conn     `json:"-"`
    UAID        string              `json:"uaid"`
    Prop        ClientProprietary   `json:"-"`
    }

var Clients map[string]*Client


func srv_set_proprietary_info(args jsMap) (cp *ClientProprietary){
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

func srv_hello(cmd PushCommand, sock PushWS) (result int, arguments jsMap) {
    args := cmd.arguments.(jsMap)
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
    // We don't really care, since we report back all channelIDs for
    //  a given UAID.
    log.Printf("INFO: Do something with these %s %s", uaid, prop)
    args["uaid"] = uaid
    arguments = args
    result = 200
    return result, arguments
}

func handleMasterCommand(cmd PushCommand, sock PushWS) (result int, args jsMap){
    log.Printf("Handling command %s", cmd)
//    chids := cmd.arguments.(jsMap)["chids"].([]interface{})
//    for key := range chids {
//        log.Printf("\t %s", chids[key].(string))
//    }
    switch int(cmd.command) {
        case HELLO:
            log.Printf("INFO: Handling HELLO event...");
            var ret jsMap
            result, ret = srv_hello(cmd, sock)
            args = cmd.arguments.(jsMap)
            args["uaid"] = ret["uaid"]
    }

    // hello: add to the map registry
    // delete: remove from the map registry
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
                return
            case cmd:= <-s.cmd:
                result, args := handleMasterCommand(cmd, s)
                log.Printf("DEBUG: Returning Result", result)
                s.cmd<- PushCommand{result, args}

        }
    }
}

    // do stuff
// -- Rest
func UpdateHandler(resp http.ResponseWriter, req *http.Request) {
    // Handle the version updates.
    log.Printf("A wild update appears")
    resp.Header().Set("Content-Type", "application/json")
    resp.Write([]byte("{}"))
}

func StatusHandler(resp http.ResponseWriter, req *http.Request) {
    resp.Write([]byte("OK"))
}


// -- main
func main(){
    config := getConfig("config.ini")
    fmt.Println(config)

    // Register the handlers
    http.Handle("/ws", websocket.Handler(PushSocketHandler))
    http.Handle("/update/", http.HandlerFunc(UpdateHandler))
    http.Handle("/status/", http.HandlerFunc(StatusHandler))

    // Config the server
    host := get(config, "host", "localhost")
    port := get(config, "port", "8080")

    // Hoist the main sail
    err := http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
    if err != nil {
        panic ("ListenAndServe: " + err.Error())
    }
}

