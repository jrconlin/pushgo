package main


import (
//    "mozilla.org/simplepush"
//    "encoding/json"
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
    command string
    arguments string
}

type PushWS struct {
    io.ReadWrite
    done chan bool
    cmd chan PushCommand
}

//   -- goproc funcs

func (sock socket) Close() error {
    sock.done <- true
    // remove from the map registry
    log.Printf("INFO: Closing socket\n")
    return nil
}

//    -- Workers
func PS_Run(sock PushWS) {
    // This is the socket
    // read the incoming json
    // process the commands
    //
    // write the reply
}

//    -- Master
func handleMasterCommand(cmd PushCommand) {
    //
    // hello: add to the map registry
    // delete: remove from the map registry
}


func PushSocketHandler(ws *websocket.Conn) {
    s := PushWS{ws, make(chan bool), make(chan PushCommand)}
    go PS_Run(s)
    for {
        select:
        case <-s.done:
            return
        case cmd:=<-s.cmd:
            handleMasterCommand(cmd)
    }
}

    // do stuff

}

// -- Rest
func UpdateHandler(resp http.ResponseWriter, req *http.Request) {
    // Handle the version updates.
}

// -- main
func main(){
    config := getConfig("config.ini")
    fmt.Println(config)

    http.Handle("/ws", websocket.Handler(PushSocketHandler))
    http.Handle("/update", UpdateHandler)
    host := get(config, "host", "localhost")
    port := get(config, "port", "8080")
    err := http.ListenAndServe(fmt.Sprintf("%s:%s", host, port), nil)
    if err != nil {
        panic ("ListenAndServe: " + err.Error())
    }
}

