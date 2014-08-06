package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mozilla.org/util"
	"net"
	"sync"
	"time"
)

var (
	NL     []byte = []byte("\n")
	EOL    []byte = []byte("\x04\n")
	routes map[string]*Route
)

type Router struct {
	Port   string
	Logger *util.HekaLogger
    Metrics *util.Metrics
}

type Route struct {
	socket net.Conn
}

var MuRoutes sync.Mutex

type Update struct {
	Uaid string    `json:"uaid"`
	Chid string    `json:"chid"`
	Vers int64     `json:"vers"`
	Time time.Time `json:"time"`
}

type Updater func(*Update, *util.HekaLogger, *util.Metrics) error

func (self *Router) HandleUpdates(updater Updater) {
	/* There appears to be a difference in how the connection is specified.
	   "0.0.0.0:3000" creates a binding port that blocks any additional
	   connections. ":3000", however, creates a multiplexing port that allows
	   multiple connections. There are, however, complications. The remote
	   port appears not to be able to see the remote connection terminate.
	   Any future connection will succeed, but will not pass data.
	   This is why I have the explicit "close command" in place while I sort
	   out where the hell the bug is in the TCP handler layer.
	*/
	listener, err := net.Listen("tcp", ":"+self.Port)
	if err != nil {
		if self.Logger != nil {
			self.Logger.Critical("router",
				"Could not open listener:"+err.Error(), nil)
		} else {
			log.Printf("error listening %s", err.Error())
		}
		return
	}
	log.Printf("Listening for updates on *:" + self.Port)

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

// Perform the actual update
func (self *Router) doupdate(updater Updater, conn net.Conn) (err error) {
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if err == io.EOF && n == 0 {
				if self.Logger != nil {
					self.Logger.Debug("router",
						"Closing listener socket."+err.Error(), nil)
				}
				err = nil
				break
			}
			break
		}
		//log.Printf("Updates::: " + string(buf[:n]))
		update := Update{}
		items := bytes.Split(buf[:n], NL)
		for _, item := range items {
			if bytes.Equal(item, EOL) {
				conn.Close()
				continue
			}
			//log.Printf("item ::: %s", item)
			if len(item) == 0 {
				continue
			}
			json.Unmarshal(item, &update)
			if self.Logger != nil {
				self.Logger.Debug("router",
					fmt.Sprintf("Handling update %s", item), nil)
			}
			if len(update.Uaid) == 0 {
				continue
			}
			// TODO group updates by UAID and send in batch
			updater(&update, self.Logger, self.Metrics)
		}
	}
	if err != nil {
		if self.Logger != nil {
			self.Logger.Error("updater", "Error: update: "+err.Error(), nil)
		}
	}
	conn.Close()
	return err
}

func (self *Router) SendUpdate(host, uaid, chid string, version int64, timer time.Time) (err error) {

	var route *Route
	var ok bool

	if route, ok = routes[host]; !ok {
		// create a new route
		if self.Logger != nil {
			self.Logger.Info("router", "Creating new route to "+host, nil)
		}
		conn, err := net.Dial("tcp", host+":"+self.Port)
		if err != nil {
			return err
		}
		if self.Logger != nil {
			self.Logger.Info("router", "Creating new route to "+host, nil)
		}
		route = &Route{
			socket: conn,
		}
		routes[host] = route
	}

	data, err := json.Marshal(Update{
		Uaid: uaid,
		Chid: chid,
		Vers: version,
		Time: timer})
	if err != nil {
		return err
	}
	if self.Logger != nil {
		self.Logger.Debug("router", "Writing to host "+host, nil)
	}
	buf := bytes.NewBuffer(data)
	buf.Write(NL)
	_, err = route.socket.Write(buf.Bytes())
	if err != nil {
		if self.Logger != nil {
			self.Logger.Error("router", "Closing socket to "+host, nil)
			log.Printf("ERROR: %s", err.Error())
		}
		route.socket.Close()
		delete(routes, host)
	}
	return err
}

// Shut down all connections by passing the End Of Line command
// REALLY don't like this
func (self *Router) CloseAll() {
	for host, route := range routes {
		log.Printf("TERMINATING connection to %s", host)
		route.socket.Write(EOL)
		route.socket.Close()
	}
}

func init() {
	routes = make(map[string]*Route)
}
