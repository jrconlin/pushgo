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
	host        string
	scheme      string
	port        int
	rclient     *http.Client
	mux         sync.Mutex
}

type RouterConfig struct {
	bucketSize  int64  `toml:"bucket_size"`
	urlTemplate string `toml:"url_template"`
	ctimeout    string
	rwtimeout   string
	scheme      string
	defaultHost string `toml:"default_host"`
	port        int
	defaultTTL  string
	etcdServers []string `toml:"etcd_servers"`
}

func (r *Router) ConfigStruct() interface{} {
	return &RouterConfig{
		urlTemplate: "{{.Scheme}}://{{.Host}}/route/{{.Uaid}}",
		ctimeout:    "3s",
		rwtimeout:   "3s",
		scheme:      "http",
		defaultTTL:  "24h",
		port:        3000,
		bucketSize:  int64(10),
		etcdServers: []string{"http://localhost:4001"},
	}
}

func (r *Router) Init(app *Application, config interface{}) (err error) {
	conf := config.(*RouterConfig)
	r.logger = app.Logger()
	r.metrics = app.Metrics()

	r.template, err = template.New("Route").Parse(conf.urlTemplate)
	if err != nil {
		r.logger.Critical("router", "Could not parse router template",
			LogFields{"error": err.Error()})
		return
	}
	r.ctimeout, err = time.ParseDuration(conf.ctimeout)
	if err != nil {
		r.ctimeout, _ = time.ParseDuration("3s")
	}
	r.rwtimeout, err = time.ParseDuration(conf.rwtimeout)
	if err != nil {
		r.rwtimeout, _ = time.ParseDuration("3s")
	}
	r.scheme = conf.scheme
	r.port = conf.port
	r.bucketSize = conf.bucketSize
	r.serverList = conf.etcdServers

	if conf.defaultHost == "" {
		r.host = app.Hostname()
	} else {
		r.host = conf.defaultHost
	}

	// default time for the server to be "live"
	defaultTTLs := conf.defaultTTL
	defaultTTLd, err := time.ParseDuration(defaultTTLs)
	if err != nil {
		r.logger.Critical("router",
			"Could not parse router default TTL",
			LogFields{"value": defaultTTLs, "error": err.Error()})
	}
	r.defaultTTL = uint64(defaultTTLd.Seconds())
	if r.defaultTTL < 2 {
		r.logger.Critical("router",
			"default TTL too short",
			LogFields{"value": defaultTTLs})
	}

	r.logger.Debug("router",
		"connecting to etcd servers",
		LogFields{"list": strings.Join(r.serverList, ";")})
	r.client = etcd.NewClient(r.serverList)
	// create the push hosts directory (if not already there)
	_, err = r.client.CreateDir(ETCD_DIR, 0)
	if err != nil {
		if err.Error()[:3] != "105" {
			r.logger.Error("router", "etcd createDir error", LogFields{
				"error": err.Error()})
		}
	}
	r.rclient = &http.Client{
		Transport: &http.Transport{
			Dial: TimeoutDialer(r.ctimeout, r.rwtimeout),
			ResponseHeaderTimeout: r.rwtimeout,
			TLSClientConfig:       &tls.Config{},
		},
	}
	if _, err = r.getServers(); err != nil {
		r.logger.Critical("router", "Could not initialize server list",
			LogFields{"error": err.Error()})
		return
	}

	// auto refresh slightly more often than the TTL
	go func(r *Router, defaultTTL uint64) {
		timeout := 0.75 * float64(r.defaultTTL)
		tick := time.Tick(time.Second * time.Duration(timeout))
		for _ = range tick {
			r.Register()
		}
	}(r, r.defaultTTL)
	r.Register()
	return nil
}

// Get the servers from the etcd cluster
func (r *Router) getServers() (reply []string, err error) {
	r.mux.Lock()
	defer r.mux.Unlock()
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
	var hostname string
	if r.port != 80 {
		hostname = r.host + ":" + string(r.port)
	} else {
		hostname = r.host
	}
	if r.logger.ShouldLog(DEBUG) {
		r.logger.Debug("router", "Registering host", LogFields{"host": r.host})
	}
	// remember, paths are absolute.
	_, err = r.client.Set(ETCD_DIR+hostname, hostname, r.defaultTTL)
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
	msg, err := json.Marshal(JsMap{
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
