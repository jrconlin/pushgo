/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	storage "mozilla.org/simplepush/storage/mcstorage"
	mozutil "mozilla.org/util"

    "sync/atomic"
	"fmt"
	"strings"
	"sync"
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

// go appears not to reduce map size on delete. yay.
var cClients int32
var MuClient sync.Mutex

var serverSingleton *Serv

type Serv struct {
	config mozutil.JsMap
	log    *mozutil.HekaLogger
	key    []byte
}

func NewServer(config mozutil.JsMap, logger *mozutil.HekaLogger) *Serv {
	var key []byte
	if k, ok := config["token_key"]; ok {
		key = k.([]byte)
	}
	return &Serv{config: config,
		key: key,
		log: logger}
}

func InitServer(config mozutil.JsMap, logger *mozutil.HekaLogger) (err error) {
	serverSingleton = NewServer(config, logger)
	return nil
}

func ClientCount() int {
	/*	defer MuClient.Unlock()
		MuClient.Lock()
		return len(Clients)
	*/
	return int(cClients)
}

func (self *Serv) ClientPing(prop *ClientProprietary) (err error) {
	// Perform whatever steps are needed to remotely wake the client.
	// TODO: Perform whatever proprietary steps are required to remotely
	// wake a given device (e.g. send a UDP ping, SMS message, etc.)
	return nil
}

func (self *Serv) Set_proprietary_info(args mozutil.JsMap) (cp *ClientProprietary) {
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
func (self *Serv) Hello(cmd PushCommand, sock *PushWS) (result int, arguments mozutil.JsMap) {
	var uaid string

	args := cmd.Arguments.(mozutil.JsMap)
	self.log.Info("server", "handling 'hello'", args)

	// TODO: If the client needs to connect to a different server,
	// Look up the appropriate server (based on UAID)
	// return a response that looks like:
	// { uaid: UAIDValue, status: 302, redirect: NewWS_URL }

	// New connects overwrite previous connections.
	// Raw client
	if args["uaid"] == "" {
		uaid, _ = mozutil.GenUUID4()
		self.log.Debug("server",
			"Generating new UAID",
			mozutil.JsMap{"uaid": uaid})
	} else {
		uaid = args["uaid"].(string)
		self.log.Debug("server",
			"Using existing UAID",
			mozutil.JsMap{"uaid": uaid})
		delete(args, "uaid")
	}

	prop := self.Set_proprietary_info(args)
	self.log.Debug("server", "Proprietary Info", mozutil.JsMap{"info": prop})

	// Create a new, live client entry for this record.
	// See Bye for discussion of potential longer term storage of this info
	sock.Uaid = uaid
	client := &Client{PushWS: *sock,
		UAID: uaid,
		Prop: prop}
	MuClient.Lock()
	Clients[uaid] = client
    atomic.AddInt32(&cClients, 1)
	MuClient.Unlock()

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
	self.log.Debug("server", "Cleaning up socket", mozutil.JsMap{"uaid": uaid})
	self.log.Info("timer", "Socket connection terminated", mozutil.JsMap{
		"uaid":     uaid,
		"duration": time.Now().Sub(sock.Born).Nanoseconds()})
	defer MuClient.Unlock()
	MuClient.Lock()
	delete(Clients, uaid)
    atomic.AddInt32(&cClients, -1)
}

func (self *Serv) Unreg(cmd PushCommand, sock *PushWS) (result int, arguments mozutil.JsMap) {
	// This is effectively a no-op, since we don't hold client session info
	args := cmd.Arguments.(mozutil.JsMap)
	args["status"] = 200
	return 200, args
}

func (self *Serv) Regis(cmd PushCommand, sock *PushWS) (result int, arguments mozutil.JsMap) {
	// A semi-no-op, since we don't care about the appid, but we do want
	// to create a valid endpoint.
	var err error
	args := cmd.Arguments.(mozutil.JsMap)
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
	// if there is a key, encrypt the token
	if self.key != nil {
		btoken := []byte(token)
		token, err = Encode(self.key, btoken)
		if err != nil {
			self.log.Error("server",
				"Token Encoding error",
				mozutil.JsMap{"uaid": sock.Uaid,
					"channelID": args["channelID"]})
			return 500, nil
		}

	}
	// cheezy variable replacement.
	args["pushEndpoint"] = strings.Replace(self.config["pushEndpoint"].(string),
		"<token>", token, -1)
	host := fmt.Sprintf("%s:%s", self.config["shard.current_host"].(string),
		self.config["port"].(string))
	args["pushEndpoint"] = strings.Replace(args["pushEndpoint"].(string),
		"<current_host>", host, -1)
	self.log.Info("server",
		"Generated Endpoint",
		mozutil.JsMap{"uaid": sock.Uaid,
			"channelID": args["channelID"],
			"token":     token,
			"endpoint":  args["pushEndpoint"]})
	return 200, args
}

func (self *Serv) RequestFlush(client *Client) (err error) {
	defer func(client *Client) {
		r := recover()
		if r != nil {
			self.log.Error("server",
				"requestFlush failed",
				mozutil.JsMap{"error": r,
					"uaid": client.UAID})
			if client != nil {
				self.ClientPing(client.Prop)
			}
		}
		return
	}(client)

	if client != nil {
		self.log.Info("server",
			"Requesting flush",
			mozutil.JsMap{"uaid": client.UAID})
        // Ensure we're allowed to send a command
        select {
        case <-client.PushWS.Acmd:
            client.PushWS.Ccmd <- PushCommand{Command: FLUSH,
                Arguments: &mozutil.JsMap{"uaid": client.UAID}}
        case <-time.After(time.Duration(50) * time.Millisecond):
            self.log.Info("server", "Client unavailable to receive command",
                mozutil.JsMap{"uaid": client.UAID})
            }
	}
	return nil
}

func Flush(client *Client) (err error) {
	// Ask the central service to flush for a given client.
	return serverSingleton.RequestFlush(client)
}

func (self *Serv) Purge(cmd PushCommand, sock *PushWS) (result int, arguments mozutil.JsMap) {
	result = 200
	return
}

func (self *Serv) HandleCommand(cmd PushCommand, sock *PushWS) (result int, args mozutil.JsMap) {
	self.log.Debug("server",
		"Handling command",
		mozutil.JsMap{"cmd": cmd})
	var ret mozutil.JsMap
	if cmd.Arguments != nil {
		args = cmd.Arguments.(mozutil.JsMap)
	} else {
		args = make(mozutil.JsMap)
	}

	switch int(cmd.Command) {
	case HELLO:
		self.log.Debug("server", "Handling HELLO event", nil)
		result, ret = self.Hello(cmd, sock)
	case UNREG:
		self.log.Debug("server", "Handling UNREG event", nil)
		result, ret = self.Unreg(cmd, sock)
	case REGIS:
		self.log.Debug("server", "Handling REGIS event", nil)
		result, ret = self.Regis(cmd, sock)
	case DIE:
		self.log.Debug("server", "Cleanup", nil)
		self.Bye(sock)
		return 0, nil
	case PURGE:
		self.log.Debug("Server", "Purge", nil)
		self.Purge(cmd, sock)
	}

	args["uaid"] = ret["uaid"]
	return result, args
}

func HandleServerCommand(cmd PushCommand, sock *PushWS) (result int, args mozutil.JsMap) {
	return serverSingleton.HandleCommand(cmd, sock)
}

func init() {
	Clients = make(map[string]*Client)
	cClients = 0
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
