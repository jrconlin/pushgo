package simplepush

import (
    "mozilla.org/simplepush/storage"
    "mozilla.org/util"

    "strings"
    "time"
    "fmt"
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

var serverSingleton *Serv


type Serv struct {
    config util.JsMap
    log *util.HekaLogger
}

func NewServer(config util.JsMap, logger *util.HekaLogger) *Serv {
    return &Serv{config: config,
                 log: logger}
}

func InitServer(config util.JsMap, logger *util.HekaLogger) (err error) {
    serverSingleton = NewServer(config, logger)
    return nil
}

func (self *Serv) Set_proprietary_info(args util.JsMap) (cp *ClientProprietary){
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

func (self *Serv) Hello(cmd PushCommand, sock *PushWS) (result int, arguments util.JsMap) {
    args := cmd.Arguments.(util.JsMap)
    self.log.Info("server", "handling 'hello'", args)

    // overwrite previously registered UAIDs
    // Raw client
    var uaid string
    if args["uaid"] == "" {
        uaid, _ = GenUUID4()
        self.log.Info("server", fmt.Sprintf("Generating new UAID %s", uaid), nil)
    } else {
        uaid = args["uaid"].(string)
        self.log.Info("server", fmt.Sprintf("Using existing UAID '%s'", uaid), nil)
        delete (args, "uaid")
    }

    prop := self.Set_proprietary_info(args)
    self.log.Info("server", fmt.Sprintf("INFO : New prop %s", prop), nil)

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

func (self *Serv) Unreg(cmd PushCommand, sock PushWS) (result int, arguments util.JsMap) {
    // This is effectively a no-op, since we don't hold client session info
    args := cmd.Arguments.(util.JsMap)
    args["status"] = 200
    return 200, args
}

func (self *Serv) Regis(cmd PushCommand, sock PushWS) (result int, arguments util.JsMap) {
    args := cmd.Arguments.(util.JsMap)
    args["status"] = 200
    if _, ok := self.config["pushEndpoint"]; !ok {
        self.config["pushEndpoint"] = "http://localhost/update/<token>"
    }
    // Generate the call back URL
    token := storage.GenPK(sock.Uaid, args["channelID"].(string))
    self.log.Info("server", fmt.Sprintf("UAID %s, channel %s", sock.Uaid,
               args["channelID"].(string)), nil)
    // cheezy variable replacement.
    args["pushEndpoint"] = strings.Replace(self.config["pushEndpoint"].(string),
        "<token>", token, -1)
    self.log.Info("server", fmt.Sprintf("regis generated callback %s", args["pushEndpoint"]), nil)
    return 200, args
}

func (self *Serv) ClientPing(prop *ClientProprietary) (err error) {
    // Perform whatever steps are needed to remotely wake the client.

    return nil
}


func (self *Serv) RequestFlush(client *Client) (err error) {
    defer func(client *Client) {
        r :=recover()
        if r != nil {
            self.log.Error("server", fmt.Sprintf("requestFlush failed  %s", r), nil)
            if client != nil {
                self.ClientPing(client.Prop)
            }
        }
        return
    }(client)

    if client != nil {

        self.log.Info("server", fmt.Sprintf("Requesting flush for %s", client.UAID, client.Pushws), nil)
        client.Pushws.Ccmd <- PushCommand{Command: FLUSH,
                              Arguments: &util.JsMap{"uaid": client.UAID}}
    }
    return nil
}

func Flush(client *Client) (err error) {
    return serverSingleton.RequestFlush(client)
}


func (self *Serv) HandleCommand(cmd PushCommand, sock PushWS) (result int, args util.JsMap){
    self.log.Info("server", fmt.Sprintf("Server Handling command %s", cmd), nil)
    var ret util.JsMap
    if cmd.Arguments != nil {
        args = cmd.Arguments.(util.JsMap)
    } else {
        args = make(util.JsMap)
    }

    switch int(cmd.Command) {
        case HELLO:
            self.log.Info("server", "Server Handling HELLO event...", nil);
            result, ret = self.Hello(cmd, &sock)
        case UNREG:
            self.log.Info("server", "Server Handling UNREG event...", nil);
            result, ret = self.Unreg(cmd, sock)
        case REGIS:
            self.log.Info("server", "Server handling REGIS event...", nil)
            result, ret = self.Regis(cmd, sock)
    }

    args["uaid"] = ret["uaid"]
    return result, args
}

func HandleServerCommand(cmd PushCommand, sock PushWS) (result int, args util.JsMap){
    return serverSingleton.HandleCommand(cmd, sock)
}
