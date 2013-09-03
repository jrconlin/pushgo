/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	storage "mozilla.org/simplepush/storage/mcstorage"
	"mozilla.org/util"

	"strings"
    "strconv"
	"sync"
	"time"
    "runtime/debug"
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
	Worker *Worker
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
	config util.JsMap
	logger *util.HekaLogger
	key    []byte
}

func NewServer(config util.JsMap, logger *util.HekaLogger) *Serv {
	var key []byte
	if k, ok := config["token_key"]; ok {
		key = k.([]byte)
	}
	return &Serv{config: config,
		key:    key,
		logger: logger}
}

func InitServer(config util.JsMap, logger *util.HekaLogger) (err error) {
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

// report if there's a collision with the UAIDs
func ClientCollision(uaid string) (collision bool) {
	_, collision = Clients[uaid]
	return collision
}

func (self *Serv) ClientPing(prop *ClientProprietary) (err error) {
	// Perform whatever steps are needed to remotely wake the client.
	// TODO: Perform whatever proprietary steps are required to remotely
	// wake a given device (e.g. send a UDP ping, SMS message, etc.)
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
	cp = new(ClientProprietary)
	cp.LastContact = time.Now()

	if args["ip"] != nil {
		cp.Ip = args["ip"].(string)
	}
	if args["port"] != nil {
		cp.Port = args["port"].(string)
	}
	return
}

// A client connects!
func (self *Serv) Hello(worker *Worker, cmd PushCommand, sock *PushWS) (result int, arguments util.JsMap) {
	var uaid string

	args := cmd.Arguments.(util.JsMap)
	if self.logger != nil {
        chidss := ""
        if chids, ok := args["channelIDs"]; ok {
            chidss = "[" + strings.Join(chids.([]string), ", ") + "]"
        }
		self.logger.Info("server", "handling 'hello'",
			util.Fields{"uaid": args["uaid"].(string),
				"channelIDs": chidss})
	}

	// TODO: If the client needs to connect to a different server,
	// Look up the appropriate server (based on UAID)
	// return a response that looks like:
	// { uaid: UAIDValue, status: 302, redirect: NewWS_URL }

	// New connects overwrite previous connections.
	// Raw client
	if args["uaid"] == "" {
		uaid, _ = util.GenUUID4()
		if self.logger != nil {
			self.logger.Debug("server",
				"Generating new UAID",
				util.Fields{"uaid": uaid})
		}
	} else {
		uaid = args["uaid"].(string)
		if self.logger != nil {
			self.logger.Debug("server",
				"Using existing UAID",
				util.Fields{"uaid": uaid})
		}
		delete(args, "uaid")
	}

	prop := self.Set_proprietary_info(args)
	if self.logger != nil {
		self.logger.Debug("server", "Proprietary Info",
			util.Fields{"ip": prop.Ip,
				"port": prop.Port})
	}

	// Create a new, live client entry for this record.
	// See Bye for discussion of potential longer term storage of this info
	sock.Uaid = uaid
	client := &Client{
		Worker: worker,
		PushWS: *sock,
		UAID:   uaid,
		Prop:   prop,
	}
	MuClient.Lock()
	Clients[uaid] = client
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
	if self.logger != nil {
		self.logger.Debug("server", "Cleaning up socket",
			util.Fields{"uaid": uaid})
		self.logger.Info("timer", "Socket connection terminated",
			util.Fields{
				"uaid":     uaid,
				"duration": strconv.FormatInt(time.Now().Sub(sock.Born).Nanoseconds(), 10)})
	}
	defer MuClient.Unlock()
	MuClient.Lock()
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
	var endPoint string
	if _, ok := self.config["push.endpoint"]; !ok {
		if _, ok := self.config["pushEndpoint"]; !ok {
			endPoint = "http://localhost/update/<token>"
		} else {
			endPoint = self.config["pushEndpoint"].(string)
		}
	} else {
		endPoint = self.config["push.endpoint"].(string)
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
			if self.logger != nil {
				self.logger.Error("server",
					"Token Encoding error",
					util.Fields{"uaid": sock.Uaid,
						"channelID": args["channelID"].(string)})
			}
			return 500, nil
		}

	}
	// cheezy variable replacement.
	endPoint = strings.Replace(endPoint, "<token>", token, -1)
    host := self.config["shard.current_host"].(string) + ":" +
		self.config["port"].(string)
	endPoint = strings.Replace(endPoint, "<current_host>", host, -1)
	args["push.endpoint"] = endPoint
	if self.logger != nil {
		self.logger.Info("server",
			"Generated Endpoint",
			util.Fields{"uaid": sock.Uaid,
				"channelID": args["channelID"].(string),
				"token":     token,
				"endpoint":  endPoint})
	}
	return 200, args
}

func (self *Serv) RequestFlush(client *Client, channel string, version int64) (err error) {
	defer func(client *Client) {
		r := recover()
		if r != nil {
			if self.logger != nil {
				self.logger.Error("server",
					"requestFlush failed",
					util.Fields{"error": r.(error).Error(),
						"uaid": client.UAID})
                debug.PrintStack()
			}
			if client != nil {
				self.ClientPing(client.Prop)
			}
		}
		return
	}(client)

	if client != nil {
		if self.logger != nil {
			self.logger.Info("server",
				"Requesting flush",
				util.Fields{"uaid": client.UAID,
					"channel": channel,
					"version": strconv.FormatInt(version, 10)})
		}

		// Attempt to send the command
		client.Worker.Flush(&client.PushWS, 0, channel, version)
	}
	return nil
}

func Flush(client *Client, channel string, version int64) (err error) {
	// Ask the central service to flush for a given client.
	return serverSingleton.RequestFlush(client, channel, version)
}

func (self *Serv) Purge(cmd PushCommand, sock *PushWS) (result int, arguments util.JsMap) {
	result = 200
	return
}

func (self *Serv) HandleCommand(cmd PushCommand, sock *PushWS) (result int, args util.JsMap) {
	var ret util.JsMap
	if cmd.Arguments != nil {
		args = cmd.Arguments.(util.JsMap)
	} else {
		args = make(util.JsMap)
	}

	switch int(cmd.Command) {
	case HELLO:
		if self.logger != nil {
			self.logger.Debug("server", "Handling HELLO event", nil)
		}
		worker := args["worker"].(*Worker)
		result, ret = self.Hello(worker, cmd, sock)
	case UNREG:
		if self.logger != nil {
			self.logger.Debug("server", "Handling UNREG event", nil)
		}
		result, ret = self.Unreg(cmd, sock)
	case REGIS:
		if self.logger != nil {
			self.logger.Debug("server", "Handling REGIS event", nil)
		}
		result, ret = self.Regis(cmd, sock)
	case DIE:
		if self.logger != nil {
			self.logger.Debug("server", "Cleanup", nil)
		}
		self.Bye(sock)
		return 0, nil
	case PURGE:
		if self.logger != nil {
			self.logger.Debug("Server", "Purge", nil)
		}
		self.Purge(cmd, sock)
	}

	args["uaid"] = ret["uaid"]
	return result, args
}

func HandleServerCommand(cmd PushCommand, sock *PushWS) (result int, args util.JsMap) {
	return serverSingleton.HandleCommand(cmd, sock)
}

func init() {
	Clients = make(map[string]*Client)
	cClients = 0
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
