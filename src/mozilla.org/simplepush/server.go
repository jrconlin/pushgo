/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
    "mozilla.org/simplepush/storage"
    "mozilla.org/util"

    "fmt"
    "strings"
    "time"
)

// -- SERVER this handles REST requests and coordinates between connected
// clients (e.g. wakes client when data is ready, potentially issues remote
// wake command to client, etc.)

type ClientProprietary struct {
    // socket proprietary information for calling out to a device (using UDP)
    Ip          string    `json:"ip"`
    Port        string    `json:"port"`
    LastContact time.Time `json:"-"`
}

type Client struct {
    // client descriptor info.
    PushWS PushWS             `json:"-"`
    UAID   string             `json:"uaid"`
    Prop   *ClientProprietary `json:"-"`
}

// Active clients
var Clients map[string]*Client

var serverSingleton *Serv

type Serv struct {
    config util.JsMap
    log    *util.HekaLogger
    key    []byte
}

func NewServer(config util.JsMap, logger *util.HekaLogger) *Serv {
    var key []byte
    if k, ok := config["token_key"]; ok {
        key = k.([]byte)
    }
    return &Serv{config: config,
        key: key,
        log: logger}
}

func InitServer(config util.JsMap, logger *util.HekaLogger) (err error) {
    serverSingleton = NewServer(config, logger)
    return nil
}

func (self *Serv) Set_proprietary_info(args util.JsMap) (cp *ClientProprietary) {
    // As noted in Bye, you may wish to store this info into a self-expiring
    // semi-persistant storage system like memcache. This will allow device
    // info to be fetched and acted upon after disconnects.
    //
    // Since it's possible that every device provider may use different means
    // to remotely activate devices, this is left as an excercise for the
    // reader.
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

// A client connects!
func (self *Serv) Hello(cmd PushCommand, sock *PushWS) (result int, arguments util.JsMap) {
    var uaid string

    args := cmd.Arguments.(util.JsMap)
    self.log.Info("server", "handling 'hello'", args)

    // TODO: If the client needs to connect to a different server,
    // Look up the appropriate server (based on UAID)
    // return a response that looks like:
    // { uaid: UAIDValue, status: 302, redirect: NewWS_URL }

    // New connects overwrite previous connections.
    // Raw client
    if args["uaid"] == "" {
        uaid, _ = GenUUID4()
        self.log.Info("server",
            fmt.Sprintf("Generating new UAID %s", uaid), nil)
    } else {
        uaid = args["uaid"].(string)
        self.log.Info("server",
            fmt.Sprintf("Using existing UAID '%s'", uaid), nil)
        delete(args, "uaid")
    }

    prop := self.Set_proprietary_info(args)
    self.log.Info("server", fmt.Sprintf("INFO : New prop %s", prop), nil)

    // Create a new, live client entry for this record.
    // See Bye for discussion of potential longer term storage of this info
    sock.Uaid = uaid
    client := &Client{PushWS: *sock,
        UAID: uaid,
        Prop: prop}
    Clients[uaid] = client

    // We don't register the list of known ChannelIDs since we echo
    // back any ChannelIDs sent on behalf of this UAID.
    args["uaid"] = uaid
    arguments = args
    result = 200
    return result, arguments
}

func (self *Serv) Bye(sock *PushWS) {
    // Remove the UAID as a registered listener.
    // NOTE: in instances where proprietary wake-ups are issued, you may
    // wish not to delete the record from Clients, since this is the only
    // way to note a record needs waking.
    //
    // For that matter, you may wish to store the Proprietary wake data to
    // something commonly shared (like memcache) so that the device can be
    // woken when not connected.
    uaid := sock.Uaid
    self.log.Info("server", fmt.Sprintf("Cleaning up socket %s", uaid), nil)
    delete(Clients, uaid)
}

func (self *Serv) Unreg(cmd PushCommand, sock *PushWS) (result int, arguments util.JsMap) {
    // This is effectively a no-op, since we don't hold client session info
    args := cmd.Arguments.(util.JsMap)
    args["status"] = 200
    return 200, args
}

func (self *Serv) Regis(cmd PushCommand, sock *PushWS) (result int, arguments util.JsMap) {
    // A semi-no-op, since we don't care about the appid, but we do want
    // to create a valid endpoint.
    var err error
    args := cmd.Arguments.(util.JsMap)
    args["status"] = 200
    if _, ok := self.config["pushEndpoint"]; !ok {
        self.config["pushEndpoint"] = "http://localhost/update/<token>"
    }
    // Generate the call back URL
    token, err := storage.GenPK(sock.Uaid,
        args["channelID"].(string))
    if err != nil {
        return 500, nil
    }
    if self.key != nil {
        btoken := []byte(token)
        token, err = Encode(self.key, btoken)
        if err != nil {
            self.log.Error("server",
                fmt.Sprintf("Token Encoding error: %s", err),
                nil)
            return 500, nil
        }

    }
    self.log.Info("server",
        fmt.Sprintf("UAID %s, channel %s, Token %s", sock.Uaid,
            args["channelID"].(string),
            token), nil)
    // cheezy variable replacement.
    args["pushEndpoint"] = strings.Replace(self.config["pushEndpoint"].(string),
        "<token>", token, -1)
    self.log.Info("server",
        fmt.Sprintf("regis generated callback %s for %s.%s",
            args["pushEndpoint"], sock.Uaid, args["channelID"]),
        nil)
    return 200, args
}

func (self *Serv) ClientPing(prop *ClientProprietary) (err error) {
    // Perform whatever steps are needed to remotely wake the client.
    // TODO: Perform whatever proprietary steps are required to remotely
    // wake a given device (e.g. send a UDP ping, SMS message, etc.)
    return nil
}

func (self *Serv) RequestFlush(client *Client) (err error) {
    defer func(client *Client) {
        r := recover()
        if r != nil {
            self.log.Error("server",
                fmt.Sprintf("requestFlush failed  %s", r), nil)
            if client != nil {
                self.ClientPing(client.Prop)
            }
        }
        return
    }(client)

    if client != nil {
        self.log.Info("server",
            fmt.Sprintf("Requesting flush for %s",
                client.UAID), nil)
        client.PushWS.Ccmd <- PushCommand{Command: FLUSH,
            Arguments: &util.JsMap{"uaid": client.UAID}}
    }
    return nil
}

func Flush(client *Client) (err error) {
    // Ask the central service to flush for a given client.
    return serverSingleton.RequestFlush(client)
}

func (self *Serv) HandleCommand(cmd PushCommand, sock *PushWS) (result int, args util.JsMap) {
    self.log.Info("server",
        fmt.Sprintf("Server Handling command %s", cmd), nil)
    var ret util.JsMap
    if cmd.Arguments != nil {
        args = cmd.Arguments.(util.JsMap)
    } else {
        args = make(util.JsMap)
    }

    switch int(cmd.Command) {
    case HELLO:
        self.log.Info("server", "Server Handling HELLO event...", nil)
        result, ret = self.Hello(cmd, sock)
    case UNREG:
        self.log.Info("server", "Server Handling UNREG event...", nil)
        result, ret = self.Unreg(cmd, sock)
    case REGIS:
        self.log.Info("server", "Server handling REGIS event...", nil)
        result, ret = self.Regis(cmd, sock)
    case DIE:
        self.log.Info("server", "Server cleanup...", nil)
        self.Bye(sock)
        return 0, nil
    }

    args["uaid"] = ret["uaid"]
    return result, args
}

func HandleServerCommand(cmd PushCommand, sock *PushWS) (result int, args util.JsMap) {
    return serverSingleton.HandleCommand(cmd, sock)
}
