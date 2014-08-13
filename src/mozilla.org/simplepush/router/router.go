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
package router

import (
	"github.com/coreos/go-etcd/etcd"
	"mozilla.org/simplepush/storage"
	"mozilla.org/util"

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
	config      *util.MzConfig
	logger      *util.MzLogger
	metrics     *util.Metrics
	bucketSize  int64
	template    *template.Template
	ctimeout    time.Duration
	rwtimeout   time.Duration
	defaultTTL  uint64
	client      *etcd.Client
	lastRefresh time.Time
	serverList  []string
	storage     *storage.Store
	collider    func(string) bool
	host        string
	scheme      string
	rclient     *http.Client
}

// Get the servers from the etcd cluster
func (self *Router) getServers() (reply []string, err error) {
	mux.Lock()
	defer mux.Unlock()
	if time.Now().Sub(self.lastRefresh) < (time.Minute * 5) {
		return self.serverList, nil
	}
	nodeList, err := self.client.Get(ETCD_DIR, false, false)
	if err != nil {
		return self.serverList, err
	}
	reply = []string{}
	for _, node := range nodeList.Node.Nodes {
		reply = append(reply, node.Value)
	}
	self.serverList = reply
	self.lastRefresh = time.Now()
	return self.serverList, nil
}

func (self *Router) getBuckets(servers []string) (buckets [][]string) {
	bucket := 0
	counter := 0
	buckets = make([][]string, (len(servers)/int(self.bucketSize))+1)
	buckets[0] = make([]string, self.bucketSize)
	for _, i := range rand.Perm(len(servers)) {
		if counter >= int(self.bucketSize) {
			bucket = bucket + 1
			counter = 0
			buckets[bucket] = make([]string, self.bucketSize)
		}
		buckets[bucket][counter] = servers[i]
		counter++
	}
	return buckets
}

// register the server to the etcd cluster.
func (self *Router) Register() (err error) {
	if self.config.GetFlag("shard.use_aws_host") {
		if self.host, err = util.GetAWSPublicHostname(); err != nil {
			self.logger.Error("router", "Could not get AWS hostname. Using host",
				util.Fields{"error": err.Error()})
		}
	}
	if self.host == "" {
		self.host = self.config.Get("host", "localhost")
	}
	port := self.config.Get("shard.port", "3000")
	if port != "80" {
		self.host = self.host + ":" + port
	}
	self.logger.Debug("router", "Registering host", util.Fields{"host": self.host})
	// remember, paths are absolute.
	_, err = self.client.Set(ETCD_DIR+self.host,
		self.host,
		self.defaultTTL)
	if err != nil {
		self.logger.Error("router",
			"Failed to register",
			util.Fields{"error": err.Error(),
				"host": self.host})
	}
	return err
}

func (self *Router) Unregister() (err error) {
	if self.host != "" {
		_, err = self.client.Delete(self.host, false)
	}
	return err
}

func (self *Router) send(cli *http.Client, req *http.Request, host string) (err error) {
	resp, err := cli.Do(req)
	if err != nil {
		self.logger.Error("router", "Router send failed", util.Fields{"error": err.Error()})
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		self.logger.Debug("router", "Update Handled", util.Fields{"host": host})
		return nil
	} else {
		self.logger.Debug("router", "Denied", util.Fields{"host": host})
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

func (self *Router) bucketSend(uaid string, msg []byte, serverList []string) (success bool, err error) {
	self.logger.Debug("router", "Sending push...", util.Fields{"msg": string(msg),
		"servers": strings.Join(serverList, ", ")})
	test := make(chan bool, 1)
	// blast it out to all servers in the already randomized list.
	for _, server := range serverList {
		if server == self.host || server == "" {
			continue
		}
		url := new(bytes.Buffer)
		if err = self.template.Execute(url, struct {
			Scheme string
			Host   string
			Uaid   string
		}{
			Scheme: self.scheme,
			Host:   server,
			Uaid:   uaid,
		}); err == nil {
			go func(server, url, msg string) {
				// do not optimize this. Request needs a fresh body per call.
				body := strings.NewReader(msg)
				if req, err := http.NewRequest("PUT", url, body); err == nil {
					self.logger.Debug("router", "Sending request",
						util.Fields{"server": server,
							"url":  url,
							"body": msg})
					req.Header.Add("Content-Type", "application/json")
					if err = self.send(self.rclient, req, server); err == nil {
						self.logger.Debug("router", "Server accepted", util.Fields{"server": server})
						test <- true
						return
					}
				}
			}(server, url.String(), string(msg))
		} else {
			self.logger.Error("router", "Could not build routing URL", util.Fields{"error": err.Error()})
			return success, err
		}
	}
	select {
	case <-test:
		success = true
		break
	case <-time.After(self.ctimeout + self.rwtimeout + (1 * time.Second)):
		success = false
		break
	}
	return success, nil
}

func (self *Router) SendUpdate(uaid, chid string, version int64, timer time.Time) (err error) {
	var server string
	serverList, err := self.getServers()
	if err != nil {
		self.logger.Error("router", "Could not get server list",
			util.Fields{"error": err.Error()})
		return err
	}
	buckets := self.getBuckets(serverList)
	msg, err := json.Marshal(util.JsMap{
		"uaid":    uaid,
		"chid":    chid,
		"version": version,
		"time":    timer.Format(time.RFC3339Nano),
	})
	if err != nil {
		self.logger.Error("router", "Could not compose routing message",
			util.Fields{"error": err.Error()})
		return err
	}
	for _, bucket := range buckets {
		if success, err := self.bucketSend(uaid, msg, bucket); err != nil || success {
			break
		}
	}
	if err != nil {
		self.logger.Error("router",
			"Could not post to server",
			util.Fields{"host": server, "error": err.Error()})
	}
	return err
}

func New(config *util.MzConfig,
	logger *util.MzLogger,
	metrics *util.Metrics,
	storage *storage.Store,
	collider func(string) bool) (self *Router) {
	template, err := template.New("Route").Parse(config.Get("shard.url_template",
		"{{.Scheme}}://{{.Host}}/route/{{.Uaid}}"))
	if err != nil {
		logger.Critical("router", "Could not parse router template",
			util.Fields{"error": err.Error()})
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
			util.Fields{"value": defaultTTLs, "error": err.Error()})
	}
	defaultTTL := uint64(defaultTTLd.Seconds())
	if defaultTTL < 2 {
		logger.Critical("router",
			"default TTL too short",
			util.Fields{"value": defaultTTLs})
	}
	serverList := strings.Split(config.Get("shard.etcd_servers", "http://localhost:4001"), ",")
	bucketSize, err := strconv.ParseInt(config.Get("shard.bucket_size", "10"), 10, 64)
	if err != nil {
		logger.Critical("router",
			"invalid value specified for shard.etcd_servers",
			util.Fields{"error": err.Error()})
	}
	logger.Debug("router",
		"connecting to etcd servers",
		util.Fields{"list": strings.Join(serverList, ";")})
	client := etcd.NewClient(serverList)
	// create the push hosts directory (if not already there)
	_, err = client.CreateDir(ETCD_DIR, 0)
	if err != nil {
		if err.Error()[:3] != "105" {
			logger.Error("router", "etcd createDir error", util.Fields{
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

	self = &Router{config: config,
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
	if _, err = self.getServers(); err != nil {
		logger.Critical("router", "Could not initialize server list",
			util.Fields{"error": err.Error()})
		return nil
	}
	// auto refresh slightly more often than the TTL
	go func(self *Router, defaultTTL uint64) {
		timeout := 0.75 * float64(defaultTTL)
		tick := time.NewTicker(time.Second * time.Duration(timeout))
		for {
			<-tick.C
			self.Register()
		}
	}(self, defaultTTL)
	self.Register()
	return self
}
