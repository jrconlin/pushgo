package simplepush

import (
    "mozilla.org/simplepush/storage"

    "log"
    "strings"
    "time"
)

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


func Srv_set_proprietary_info(args storage.JsMap) (cp *ClientProprietary){
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

func Srv_hello(cmd PushCommand, sock *PushWS) (result int, arguments storage.JsMap) {
    args := cmd.Arguments.(storage.JsMap)
    log.Printf("INFO : handling 'hello'", args)

    // overwrite previously registered UAIDs
    // Raw client
    var uaid string
    if args["uaid"] == "" {
        uaid, _ = GenUUID4()
        log.Printf("INFO : Generating new UAID %s", uaid)
    } else {
        uaid = args["uaid"].(string)
        log.Printf("INFO :Using existing UAID '%s'", uaid)
        delete (args, "uaid")
    }

    prop := Srv_set_proprietary_info(args)
    log.Printf("INFO : New prop %s", prop)

    // Add the ChannelIDs?
    sock.Uaid = uaid
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

func Srv_unreg(cmd PushCommand, sock PushWS) (result int, arguments storage.JsMap) {
    // This is effectively a no-op, since we don't hold client session info
    args := cmd.Arguments.(storage.JsMap)
    args["status"] = 200
    return 200, args
}

func Srv_regis(cmd PushCommand, sock PushWS, config storage.JsMap) (result int, arguments storage.JsMap) {
    args := cmd.Arguments.(storage.JsMap)
    args["status"] = 200
    if _, ok := config["pushEndpoint"]; !ok {
        config["pushEndpoint"] = "http://localhost/update/<token>"
    }
    // Generate the call back URL
    token := storage.GenPK(sock.Uaid, args["channelID"].(string))
    log.Printf("INFO : UAID %s, channel %s", sock.Uaid,
               args["channelID"].(string))
    // cheezy variable replacement.
    args["pushEndpoint"] = strings.Replace(config["pushEndpoint"].(string),
        "<token>", token, -1)
    log.Printf("INFO : regis generated callback %s", args["pushEndpoint"])
    return 200, args
}

func Srv_clientPing(prop *ClientProprietary) (err error) {
    // Perform whatever steps are needed to remotely wake the client.

    return nil
}


func Srv_requestFlush(client *Client) (err error) {
    defer func(client *Client) {
        r :=recover()
        if r != nil {
            log.Printf("ERROR: requestFlush failed  %s", r)
            if client != nil {
                Srv_clientPing(client.Prop)
            }
        }
        return
    }(client)

    if client != nil {

        log.Printf("INFO : Requesting flush for %s", client.UAID, client.Pushws)
        client.Pushws.Ccmd <- PushCommand{Command: FLUSH,
                              Arguments: &storage.JsMap{"uaid": client.UAID}}
    }
    return nil
}

func Srv_handleMasterCommand(cmd PushCommand, sock PushWS, config storage.JsMap) (result int, args storage.JsMap){
    log.Printf("INFO : Server Handling command %s", cmd)
    var ret storage.JsMap
    if cmd.Arguments != nil {
        args = cmd.Arguments.(storage.JsMap)
    } else {
        args = make(storage.JsMap)
    }

    switch int(cmd.Command) {
        case HELLO:
            log.Printf("INFO : Server Handling HELLO event...");
            result, ret = Srv_hello(cmd, &sock)
        case UNREG:
            log.Printf("INFO : Server Handling UNREG event...");
            result, ret = Srv_unreg(cmd, sock)
        case REGIS:
            log.Printf("INFO : Server handling REGIS event...")
            result, ret = Srv_regis(cmd, sock, config)
    }

    args["uaid"] = ret["uaid"]
    return result, args
}

