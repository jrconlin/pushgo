package main


import (
//    "mozilla.org/simplepush"
//    "encoding/json"
//    "io/ioutil"
    "code.google.com/p/go.net/websocket"
    "fmt"
    "bufio"
    "io"
    "log"
    "strings"
    "os"
    "net/http"
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

type PushCommand struct {
    // TODO: Enum?
    // 0 unregister
    // 1 register
    // 2 ack
    // 3 flush
    //99 return
    command int
    arguments interface {}
}

type PushWS struct {
    uaid []byte
    socket *websocket.Conn
    done chan bool
    cmd chan PushCommand
}

//   -- goproc funcs

func (sock PushWS) Close() error {
    log.Printf("INFO: Closing socket %s \n", sock.uaid)
    sock.cmd <- PushCommand{0, sock.uaid}
    sock.done <- true
    // remove from the map registry
    return nil
}

//    -- Workers

func sniffer(socket *websocket.Conn, in chan map[string]interface{}) {
    var buffer map[string]interface{}
    for {
        websocket.JSON.Receive(socket, &buffer)
        in<- buffer
    }
}


func PS_Run(sock PushWS) {
    // This is the socket
    // read the incoming json
    for {
        in := make(chan map[string]interface{})
        go sniffer(sock.socket, in)
        select {
            case cmd := <-sock.cmd:
                if cmd.command == 3 {
                    websocket.JSON.Send(sock.socket, cmd.arguments)
                }
            case buffer := <-in:
                log.Printf("INFO: Read buffer, %s \n", buffer)
                // process the commands
                switch buffer["messageType"] {
                    case "hello":
                        PS_hello(sock, buffer)
                    case "ack":
                        PS_ack(sock, buffer)
                    case "register":
                        PS_register(sock, buffer)
                    case "unregister":
                        PS_unregister(sock, buffer)
                    default:
                        websocket.JSON.Send(sock.socket,
                            map[string]interface{}{
                                "messageType": buffer["messageType"],
                                "status": 401})
                }
       }
    }
}


func PS_hello(sock PushWS, buffer interface{}) (err error) {
    // register the UAID
    data := buffer.(map[string]interface{})
    sock.uaid = data["uaid"].([]byte)
    // register the sockets
    // register any proprietary connection requirements
    // alert the master of the new UAID.
    cmd := PushCommand{1, map[string]interface{}{
         "id": data["uaid"],
         "chids": data["channelIDs"]}}
    // blocking call back to the boss.
    sock.cmd<-cmd
    result := <-sock.cmd
    websocket.JSON.Send(sock.socket, map[string]interface{}{
                    "messageType": data["messageType"],
                    "status": result.command,
                    "uaid": data["uaid"]})
    PS_flush(sock)
    return err
}


func PS_ack(sock PushWS, buffer interface{}) (err error) {
    return err
}


func PS_register(sock PushWS, buffer interface{}) (err error) {
    return err
}


func PS_unregister(sock PushWS, buffer interface{}) (err error) {
    return err
}


func PS_flush(sock PushWS) {
    log.Printf("INFO: Flushing data to socket\n")
}


//    -- Master
func handleMasterCommand(cmd PushCommand, sock PushWS) (result int){
    log.Printf("Handling command %s")
    // hello: add to the map registry
    // delete: remove from the map registry
    return 200
}


func PushSocketHandler(ws *websocket.Conn) {
    s := PushWS{nil, ws, make(chan bool), make(chan PushCommand)}
    go PS_Run(s)
    for {
        select {
            case <-s.done:
                return
            case cmd:=<-s.cmd:
                result := handleMasterCommand(cmd, s)
                s.cmd<- PushCommand{result, nil}

        }
    }
}

    // do stuff
// -- Rest
func UpdateHandler(resp http.ResponseWriter, req *http.Request) {
    // Handle the version updates.
}

// -- main
func main(){
    config := getConfig("config.ini")
    fmt.Println(config)

    http.Handle("/ws", websocket.Handler(PushSocketHandler))
    http.HandleFunc("/update", UpdateHandler)
    host := get(config, "host", "localhost")
    port := get(config, "port", "8080")
    err := http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
    if err != nil {
        panic ("ListenAndServe: " + err.Error())
    }
}

