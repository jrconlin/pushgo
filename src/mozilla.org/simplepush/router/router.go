package router

import (
	"encoding/json"
	"io"
	"log"
	"mozilla.org/util"
	"net"
)

type Router struct {
	Port   string
	Logger *util.HekaLogger
}

type Route struct {
	socket net.Conn
}

var routes map[string]*Route

type Update struct {
	Uaid string `json:"uaid"`
	Chid string `json:"chid"`
	Vers int64  `json:"vers"`
}

type Updater func(*Update) error

func (self *Router) HandleUpdates(updater Updater) {
	listener, err := net.Listen("tcp", "0.0.0.0:"+self.Port)
	if err != nil {
		if self.Logger != nil {
			self.Logger.Critical("router",
				"Could not open listener:"+err.Error(), nil)
		} else {
			log.Printf("error listening %s", err.Error())
		}
		return
	}
	log.Printf("Listening for updates on 0.0.0.0:" + self.Port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if self.Logger != nil {
				self.Logger.Critical("router",
					"Could not accept connection:"+err.Error(), nil)
			} else {
				log.Printf("Could not accept listener:%s", err.Error())
			}
		}
		go self.doupdate(updater, conn)
	}
}

func (self *Router) doupdate(updater Updater, conn net.Conn) (err error) {
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if err == io.EOF && n == 0 {
				if self.Logger != nil {
					self.Logger.Debug("router", "Closing listener socket.", nil)
				}
				err = nil
				break
			}
			break
		}
		update := Update{}
		json.Unmarshal(buf[:n], &update)
		if len(update.Uaid) == 0 {
			continue
		}
		updater(&update)
	}
	if err != nil {
		if self.Logger != nil {
			self.Logger.Error("updater", "Error: update: "+err.Error(), nil)
		}
	}
	conn.Close()
	return err
}

func (self *Router) SendUpdate(host, uaid, chid string, version int64) (err error) {

	var route *Route
	var ok bool

	if route, ok = routes[host]; !ok {
		// create a new route
		conn, err := net.Dial("tcp", host+":"+self.Port)
		if err != nil {
			return err
		}
		route = &Route{
			socket: conn,
		}
		routes[host] = route
	}

	data, err := json.Marshal(Update{
		Uaid: uaid,
		Chid: chid,
		Vers: version})
	if err != nil {
		return err
	}
	_, err = route.socket.Write(data)
	if err != nil {
		delete(routes, host)
	}
	return err
}

func init() {
	routes = make(map[string]*Route)
}
