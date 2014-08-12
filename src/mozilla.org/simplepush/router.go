// HTTP version of the cross machine router
// Fetch the servers from etcd, divvy them up into buckets,
// proxy the update to the servers, stopping once you've gotten
// a successful return.
// PROS:
//  Very simple to implement
//  hosts can autoannounce
//  no AWS dependencies
// CONS:
//  fair bit of cross traffic (try to minimize)

// Obviously a PubSub would be better, but might require more server
// state management (because of duplicate messages)
package simplepush

import (
	"github.com/coreos/go-etcd/etcd"

	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
)

const (
	ETCD_DIR = "push_hosts/"
)

var (
	errNextCastle = errors.New("Client not found")
	mux           sync.Mutex
)

type Router struct {
	logger      *SimpleLogger
	metrics     *Metrics
	bucketSize  int64
	template    *template.Template
	ctimeout    time.Duration
	rwtimeout   time.Duration
	defaultTTL  uint64
	client      *etcd.Client
	lastRefresh time.Time
	serverList  []string
	storage     *storage.Storage
	collider    func(string) bool
	host        string
	scheme      string
	rclient     *http.Client
}

type RouterConfig struct {
    urlTemplate string `toml:"url_template"`
    ctimeout string
    rwtimeout string
    scheme string
    defaultTTL string
    etcd_servers []string
    bucket_size int
}

func (r *Router) ConfigStruct() interface{} {

}

func (r *Router) Init(app *Application, config interface{}) (err errors) {
    collider func(string) bool) (r *Router) {
    template, err := template.New("Route").Parse(config.Get("shard.url_template",
        "{{.Scheme}}://{{.Host}}/route/{{.Uaid}}"))
    if err != nil {
        logger.Critical("router", "Could not parse router template",
            LogFields{"error": err.Error()})
        return
    }
    ctimeout, err := time.ParseDuration(config.Get("shard.ctimeout", "3s"))
    if err != nil {
        ctimeout, _ = time.ParseDuration("3s")
    }
    rwtimeout, err := time.ParseDuration(config.Get("shard.rwtimeout", "3s"))
    if err != nil {
        rwtimeout, _ = time.ParseDuration("3s")
    }
    scheme := config.Get("shard.scheme", "http")
    // default time for the server to be "live"
    defaultTTLs := config.Get("shard.defaultTTL", "24h")
    defaultTTLd, err := time.ParseDuration(defaultTTLs)
    if err != nil {
        logger.Critical("router",
            "Could not parse router default TTL",
            LogFields{"value": defaultTTLs, "error": err.Error()})
    }
    defaultTTL := uint64(defaultTTLd.Seconds())
    if defaultTTL < 2 {
        logger.Critical("router",
            "default TTL too short",
            LogFields{"value": defaultTTLs})
    }
    serverList := strings.Split(config.Get("shard.etcd_servers", "http://localhost:4001"), ",")
    bucketSize, err := strconv.ParseInt(config.Get("shard.bucket_size", "10"), 10, 64)
    if err != nil {
        logger.Critical("router",
            "invalid value specified for shard.etcd_servers",
            LogFields{"error": err.Error()})
    }
    logger.Debug("router",
        "connecting to etcd servers",
        LogFields{"list": strings.Join(serverList, ";")})
    client := etcd.NewClient(serverList)
    // create the push hosts directory (if not already there)
    _, err = client.CreateDir(ETCD_DIR, 0)
    if err != nil {
        if err.Error()[:3] != "105" {
            logger.Error("router", "etcd createDir error", LogFields{
                "error": err.Error()})
        }
    }
    rclient := &http.Client{
        Transport: &http.Transport{
            Dial: TimeoutDialer(ctimeout, rwtimeout),
            ResponseHeaderTimeout: rwtimeout,
            TLSClientConfig:       &tls.Config{},
        },
    }

    r = &Router{config: config,
        logger:     logger,
        metrics:    metrics,
        template:   template,
        client:     client,
        defaultTTL: defaultTTL,
        collider:   collider,
        ctimeout:   ctimeout,
        rwtimeout:  rwtimeout,
        scheme:     scheme,
        bucketSize: bucketSize,
        rclient:    rclient,
    }
    if _, err = r.getServers(); err != nil {
        logger.Critical("router", "Could not initialize server list",
            LogFields{"error": err.Error()})
        return nil
    }
    // auto refresh slightly more often than the TTL
    go func(r *Router, defaultTTL uint64) {
        timeout := 0.75 * float64(defaultTTL)
        tick := time.NewTicker(time.Second * time.Duration(timeout))
        for {
            <-tick.C
            r.Register()
        }
    }(r, defaultTTL)
    r.Register()
    return r
}


// Get the servers from the etcd cluster
func (r *Router) getServers() (reply []string, err error) {
	mux.Lock()
	defer mux.Unlock()
	if time.Now().Sub(r.lastRefresh) < (time.Minute * 5) {
		return r.serverList, nil
	}
	nodeList, err := r.client.Get(ETCD_DIR, false, false)
	if err != nil {
		return r.serverList, err
	}
	reply = []string{}
	for _, node := range nodeList.Node.Nodes {
		reply = append(reply, node.Value)
	}
	r.serverList = reply
	r.lastRefresh = time.Now()
	return r.serverList, nil
}

func (r *Router) getBuckets(servers []string) (buckets [][]string) {
	bucket := 0
	counter := 0
	buckets = make([][]string, (len(servers)/int(r.bucketSize))+1)
	buckets[0] = make([]string, r.bucketSize)
	for _, i := range rand.Perm(len(servers)) {
		if counter >= int(r.bucketSize) {
			bucket = bucket + 1
			counter = 0
			buckets[bucket] = make([]string, r.bucketSize)
		}
		buckets[bucket][counter] = servers[i]
		counter++
	}
	return buckets
}

// register the server to the etcd cluster.
func (r *Router) Register() (err error) {
	if r.config.GetFlag("shard.use_aws_host") {
		if r.host, err = util.GetAWSPublicHostname(); err != nil {
			r.logger.Error("router", "Could not get AWS hostname. Using host",
				LogFields{"error": err.Error()})
		}
	}
	if r.host == "" {
		r.host = r.config.Get("host", "localhost")
	}
	port := r.config.Get("shard.port", "3000")
	if port != "80" {
		r.host = r.host + ":" + port
	}
	r.logger.Debug("router", "Registering host", LogFields{"host": r.host})
	// remember, paths are absolute.
	_, err = r.client.Set(ETCD_DIR+r.host,
		r.host,
		r.defaultTTL)
	if err != nil {
		r.logger.Error("router",
			"Failed to register",
			LogFields{"error": err.Error(),
				"host": r.host})
	}
	return err
}

func (r *Router) Unregister() (err error) {
	if r.host != "" {
		_, err = r.client.Delete(r.host, false)
	}
	return err
}

func (r *Router) send(cli *http.Client, req *http.Request, host string) (err error) {
	resp, err := cli.Do(req)
	if err != nil {
		r.logger.Error("router", "Router send failed", LogFields{"error": err.Error()})
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		r.logger.Debug("router", "Update Handled", LogFields{"host": host})
		return nil
	} else {
		r.logger.Debug("router", "Denied", LogFields{"host": host})
		return errNextCastle
	}
	return err
}

func TimeoutDialer(cTimeout, rwTimeout time.Duration) func(net, addr string) (c net.Conn, err error) {

	return func(netw, addr string) (c net.Conn, err error) {
		c, err = net.DialTimeout(netw, addr, cTimeout)
		if err != nil {
			return nil, err
		}
		// do we need this if ResponseHeaderTimeout is set?
		c.SetDeadline(time.Now().Add(rwTimeout))
		return c, nil
	}
}

func (r *Router) bucketSend(uaid string, msg []byte, serverList []string) (success bool, err error) {
	r.logger.Debug("router", "Sending push...", LogFields{"msg": string(msg),
		"servers": strings.Join(serverList, ", ")})
	test := make(chan bool, 1)
	// blast it out to all servers in the already randomized list.
	for _, server := range serverList {
		if server == r.host || server == "" {
			continue
		}
		url := new(bytes.Buffer)
		if err = r.template.Execute(url, struct {
			Scheme string
			Host   string
			Uaid   string
		}{
			Scheme: r.scheme,
			Host:   server,
			Uaid:   uaid,
		}); err == nil {
			go func(server, url, msg string) {
				// do not optimize this. Request needs a fresh body per call.
				body := strings.NewReader(msg)
				if req, err := http.NewRequest("PUT", url, body); err == nil {
					r.logger.Debug("router", "Sending request",
						LogFields{"server": server,
							"url":  url,
							"body": msg})
					req.Header.Add("Content-Type", "application/json")
					if err = r.send(r.rclient, req, server); err == nil {
						r.logger.Debug("router", "Server accepted", LogFields{"server": server})
						test <- true
						return
					}
				}
			}(server, url.String(), string(msg))
		} else {
			r.logger.Error("router", "Could not build routing URL", LogFields{"error": err.Error()})
			return success, err
		}
	}
	select {
	case <-test:
		success = true
		break
	case <-time.After(r.ctimeout + r.rwtimeout + (1 * time.Second)):
		success = false
		break
	}
	return success, nil
}

func (r *Router) SendUpdate(uaid, chid string, version int64, timer time.Time) (err error) {
	var server string
	serverList, err := r.getServers()
	if err != nil {
		r.logger.Error("router", "Could not get server list",
			LogFields{"error": err.Error()})
		return err
	}
	buckets := r.getBuckets(serverList)
	msg, err := json.Marshal(util.JsMap{
		"uaid":    uaid,
		"chid":    chid,
		"version": version,
		"time":    timer.Format(time.RFC3339Nano),
	})
	if err != nil {
		r.logger.Error("router", "Could not compose routing message",
			LogFields{"error": err.Error()})
		return err
	}
	for _, bucket := range buckets {
		if success, err := r.bucketSend(uaid, msg, bucket); err != nil || success {
			break
		}
	}
	if err != nil {
		r.logger.Error("router",
			"Could not post to server",
			LogFields{"host": server, "error": err.Error()})
	}
	return err
}
