/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

// thin memcache wrapper

/** This library uses libmemcache (via gomc) to do most of it's work.
 * libmemcache handles key sharding, multiple nodes, node balancing, etc.
 */

import (
	"github.com/ianoshen/gomc"

	"mozilla.org/simplepush/sperrors"

	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	DELETED = iota
	LIVE
	REGISTERED
)

var (
	config        JsMap
	no_whitespace *strings.Replacer = strings.NewReplacer(" ", "",
		"\x08", "",
		"\x09", "",
		"\x0a", "",
		"\x0b", "",
		"\x0c", "",
		"\x0d", "")
)

var mcsPoolSize int32

// Attempting to use dynamic pools seems to cause all sorts of problems.
// The first being that the pool channel can't be resized once it's built
// in New(). The other being that during shutdown, go throws a popdefer
// phase error that I've not been able to chase the source. Since this doesn't
// happen for statically set pools, I'm going to go with that for now.
// var mcsMaxPoolSize int32

// ChannelRecord
// I am using very short IDs here because these are also stored as part of the GLOB
// and space matters in MC.
type cr struct {
	S int8   //State
	V uint64 // Version
	L int64  // Last touched
}

// IDArray
// Again, short name because GLOB stores it too and space is precious.
type ia [][]byte

// used by sort
func (self ia) Len() int {
	return len(self)
}
func (self ia) Swap(i, j int) {
	self[i], self[j] = self[j], self[i]
}
func (self ia) Less(i, j int) bool {
	return bytes.Compare(self[i], self[j]) < 0
}

type StorageError struct {
	err string
}

func (e StorageError) Error() string {
	// foo call so that import log doesn't complain
	_ = log.Flags()
	return "StorageError: " + e.err
}

var UnknownUAIDError = errors.New("UnknownUAIDErr")

// Returns the location of a string in a slice of strings or -1 if
// the string isn't present in the slice
func indexOf(list ia, val []byte) (index int) {
	for index, v := range list {
		if bytes.Equal(v, val) {
			return index
		}
	}
	return -1
}

// Returns a new slice with the string at position pos removed or
// an equivilant slice if the pos is not in the bounds of the slice
func remove(list [][]byte, pos int) (res [][]byte) {
	if pos < 0 || pos == len(list) {
		return list
	}
	return append(list[:pos], list[pos+1:]...)
}

// Use the AWS system to query for the endpoints to use.
// (Allows for dynamic endpoint assignments)
func getElastiCacheEndpoints(configEndpoint string) (string, error) {
	c, err := net.Dial("tcp", configEndpoint)
	if err != nil {
		return "", err
	}
	defer c.Close()

	reader, writer := bufio.NewReader(c), bufio.NewWriter(c)
	writer.Write([]byte("config get cluster\r\n"))
	writer.Flush()

	reader.ReadString('\n')
	reader.ReadString('\n')
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", nil
	}

	endPoints := strings.Split(line, " ")
	if len(endPoints) < 1 {
		return "", errors.New("Elasticache returned no endPoints")
	}

	retEndpoints := make([]string, 0)
	for _, v := range endPoints {
		endPoint := strings.Split(v, "|")
		if len(endPoint) < 3 {
			continue
		}
		retEndpoints = append(retEndpoints, fmt.Sprintf("%s:%s", endPoint[1], strings.TrimSpace(endPoint[2])))
	}
	return strings.Join(retEndpoints, ","), nil
}

func getElastiCacheEndpointsTimeout(configEndpoint string, seconds int) (string, error) {
	type strErr struct {
		ep  string
		err error
	}

	ch := make(chan strErr)

	go func() {
		ep, err := getElastiCacheEndpoints(configEndpoint)
		ch <- strErr{ep, err}
	}()
	select {
	case se := <-ch:
		return se.ep, se.err
	case <-time.After(time.Duration(seconds) * time.Second):
		return "", errors.New("Elasticache config timeout")
	}

}

// Break apart a user readable Primary Key into useable fields
func ResolvePK(pk string) (uaid, chid string, err error) {
	items := strings.SplitN(pk, ".", 2)
	if len(items) < 2 {
		return pk, "", nil
	}
	return items[0], items[1], nil
}

// Generate a user readable Primary Key
func GenPK(uaid, chid string) (pk string, err error) {
	pk = fmt.Sprintf("%s.%s", uaid, chid)
	return pk, nil
}

// Convert a user readable UUID string into it's binary equivalent
func cleanID(id string) []byte {
	if len(id)%2 == 1 {
		id = "0" + id
	}
	res, err := hex.DecodeString(strings.TrimSpace(strings.Replace(id, "-", "", -1)))
	if err == nil {
		return res
	}
	return nil

}

// Generate a binary Primary Key from user readable strings
func binPKFromStrings(uaid, chid string) (pk []byte, err error) {
	return binGenPK(cleanID(uaid), cleanID(chid))
}

// Convert a binary Primary Key to user readable strings
func stringsFromBinPK(pk []byte) (uaid, chid string, err error) {
	//uuidFmt = "%s-%s-%s-%s-%s"
	uaid = hex.EncodeToString(pk[:15])
	chid = hex.EncodeToString(pk[16:])
	return uaid, chid, nil
}

// Generate a binary Primary Key
func binGenPK(uaid, chid []byte) (pk []byte, err error) {
	pk = make([]byte, 32)
	aoff := 16 - len(uaid)
	if aoff < 0 {
		aoff = 0
	}
	boff := 32 - len(chid)
	if boff < 16 {
		boff = 16
	}
	copy(pk[aoff:], uaid)
	copy(pk[boff:], chid)
	return pk, nil
}

// break apart a binary Primary Key
func binResolvePK(pk []byte) (uaid, chid []byte, err error) {
	return pk[:15], pk[16:], nil
}

// Encode a key for Memcache to use.
func keycode(key []byte) string {
	// Sadly, can't use full byte chars for key values, so have to encode
	// to base64. Ideally, this would just be
	// return string(key)
	return base64.StdEncoding.EncodeToString(key)
}
func keydecode(key string) []byte {
	ret, _ := base64.StdEncoding.DecodeString(key)
	return ret
}

type MemcacheConf struct {
	recvTimeout  string `toml:"recv_timeout"`
	sendTimeout  string `toml:"send_timeout"`
	pollTimeout  string `toml:"poll_timeout"`
	retryTimeout string `toml:"retry_timeout"`
	poolSize     int    `toml:"pool_size"`
	server       []string
}

type DbConf struct {
	timeoutLive   int64  `toml:"timeout_live"`
	timeoutReg    int64  `toml:"timeout_reg"`
	timeoutDel    int64  `toml:"timeout_del"`
	handleTimeout string `toml:"handle_time"`
	propPrefix    string `toml:"prop_prefix"`
}

type StorageConf struct {
	elasticacheConfigEndpoint string `toml:"elasticache_config_endpoint"`
	memcache                  MemcacheConf
	db                        DbConf
}

type Storage struct {
	mcs        chan gomc.Client
	logger     *SimpleLogger
	mc_timeout time.Duration
	poolSize   int64
	timeouts   map[string]int64
	servers    []string
	propPrefix string
}

func (self *Storage) ConfigStruct() interface{} {
	return &StorageConf{
		memcache: &MemcacheConf{
			recvTimeout:  "1s",
			sendTimeout:  "1s",
			pollTimeout:  "1s",
			retryTimeout: "1s",
			poolSize:     100,
			server:       []string{"127.0.0.1:11211"},
		},
		db: &DbConf{
			timeoutLive:   259200,
			timeoutReg:    10800,
			timeoutDel:    86400,
			handleTimeout: "5s",
			propPrefix:    "_pc-",
		},
	}
}

func parseDuration(val, fieldName string, logger *SimpleLogger) (t time.Duration) {
	t, err = time.ParseDuration(val)
	if err != nil {
		logger.Error("storage", fmt.Sprintf("Could not parse %s", fieldName),
			LogFields{"error": err.Error()})
	}
}

func (self *Storage) Init(app *Application, config interface{}) (err error) {
	conf := config.(*StorageConf)
	self.logger = app.Logger()

	if conf.elasticacheConfigEndpoint != "" {
		memcacheEndpoint, err := getElastiCacheEndpointsTimeout(conf.elasticacheConfigEndpoint, 2)
		if err == nil {
			// do NOT include any spaces
			self.servers = strings.Split(
				no_whitespace.Replace(config.Get("memcache.server", "")),
				",")
		} else {
			self.logger.Error("storage", "Elastisearch error.",
				LogFields{"error": err.Error()})
		}
	} else {
		self.servers = conf.memcache.server
	}

	self.mc_timeout = parseDuration(conf.db.handleTimeout, "storage.db.handle_timeout", self.logger)
	if self.mc_timeout == nil {
		self.mc_timeout = 10 * time.Second
	}
	self.poolSize = conf.memcache.poolSize
	self.propPrefix = conf.db.propPrefix

	timeouts := make(map[string]int64)
	timeouts["live"] = conf.db.timeoutLive
	timeouts["reg"] = conf.db.timeoutReg
	timeouts["del"] = conf.db.timeoutDel
	timeouts["mc_recv_timeout"] = parseDuration(conf.memcache.recvTimeout,
		"storage.memcache.recv_timeout", self.logger).Nanoseconds() * 1000
	timeouts["mc_send_timeout"] = parseDuration(conf.memcache.sendTimeout,
		"storage.memcache.send_timeout", self.logger).Nanoseconds() * 1000
	timeouts["mc_poll_timeout"] = parseDuration(conf.memcache.pollTimeout,
		"storage.memcache.poll_timeout", self.logger).Nanoseconds() * 1000
	timeouts["mc_retry_timeout"] = parseDuration(conf.memcache.retryTimeout,
		"storage.memcache.retry_timeout", self.logger).Nanoseconds() * 1000
	self.timeouts = timeouts

	logger.Info("storage", "Creating new gomc handler", nil)

	mcs := make(chan gomc.Client, poolSize)
	for i := int64(0); i < poolSize; i++ {
		mcs <- self.newMC()
	}

	return
}

// Generate a new Memcache Client
func (self *Storage) newMC() (mc gomc.Client) {
	var err error
	/*	if mcsPoolSize >= mcsMaxPoolSize {
			return nil
		}
	*/
	if mc, err = gomc.NewClient(self.servers, 1, gomc.ENCODING_GOB); err != nil {
		logger.Critical("storage", "CRITICAL HIT!",
			LogFields{"error": err.Error()})
	}
	// internally hash key using MD5 (for key distribution)
	mc.SetBehavior(gomc.BEHAVIOR_KETAMA_HASH, 1)
	// Use the binary protocol, which allows us faster data xfer
	// and better data storage (can use full UTF-8 char space)
	mc.SetBehavior(gomc.BEHAVIOR_BINARY_PROTOCOL, 1)
	//mc.SetBehavior(gomc.BEHAVIOR_NO_BLOCK, 1)
	// NOTE! do NOT set BEHAVIOR_NOREPLY + Binary. This will cause
	// libmemcache to drop into an infinite loop.
	mc_timeout := func(s string) uint64 {
		return uint64(self.timeouts["mc_"+s+"_timeout"])
	}
	mc.SetBehavior(gomc.BEHAVIOR_SND_TIMEOUT, mc_timeout("send"))
	mc.SetBehavior(gomc.BEHAVIOR_RCV_TIMEOUT, mc_timeout("recv"))
	mc.SetBehavior(gomc.BEHAVIOR_POLL_TIMEOUT, mc_timeout("poll"))
	mc.SetBehavior(gomc.BEHAVIOR_RETRY_TIMEOUT, mc_timeout("retry"))
	atomic.AddInt32(&mcsPoolSize, 1)
	return mc
}

func (self *Storage) isFatal(err error) bool {
	// if it has anything to do with the connection, restart the server.
	// this is crappy, crappy behavior, but it's what go wants.
	return false
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "timeout") {
		return false
	}
	if strings.Contains(err.Error(), "NOT FOUND") {
		return false
	}
	switch err {
	case nil:
		return false
	default:
		self.logger.Critical("storage", "CRITICAL HIT! RESTARTING!",
			LogFields{"error": err.Error()})
		return true
	}
}

func (self *Storage) fetchRec(pk []byte) (result *cr, err error) {
	//fetch a record from Memcache
	result = &cr{}
	if pk == nil {
		err = sperrors.InvalidPrimaryKeyError
		return nil, err
	}

	defer func() {
		if err := recover(); err != nil {
			self.isFatal(err.(error))
			self.logger.Error("storage",
				fmt.Sprintf("could not fetch record for %s", pk),
				LogFields{"primarykey": keycode(pk),
					"error": err.(error).Error()})
		}
	}()

	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return nil, err
	}
	//mc.Timeout = time.Second * 10
	err = mc.Get(keycode(pk), result)
	if err != nil && strings.Contains("NOT FOUND", err.Error()) {
		err = nil
	}
	if err != nil {
		self.isFatal(err)
		self.logger.Error("storage",
			"Get Failed",
			LogFields{"primarykey": hex.EncodeToString(pk),
				"error": err.Error()})
		return nil, err
	}

	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("storage",
			"Fetched",
			LogFields{"primarykey": hex.EncodeToString(pk),
				"result": fmt.Sprintf("state: %d, vers: %d, last: %d",
					result.S, result.V, result.L),
			})
	}
	return result, err
}

func (self *Storage) fetchAppIDArray(uaid []byte) (result ia, err error) {
	if uaid == nil {
		return result, nil
	}
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return result, err
	}
	//mc.Timeout = time.Second * 10
	err = mc.Get(keycode(uaid), &result)
	if err != nil {
		if strings.Contains("NOT FOUND", err.Error()) {
			return result, nil
		} else {
			self.isFatal(err)
		}
		return result, err
	}
	// pare out duplicates.
	for i, chid := range result {
		if dup := indexOf(result[i+1:], chid); dup > -1 {
			result = remove(result, i+dup)
		}
	}
	return result, err
}

func (self *Storage) storeAppIDArray(uaid []byte, arr ia) (err error) {
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return nil
	}
	//mc.Timeout = time.Second * 10
	// sort the array
	sort.Sort(arr)

	err = mc.Set(keycode(uaid), arr, 0)
	if err != nil {
		self.isFatal(err)
	}
	return err
}

func (self *Storage) storeRec(pk []byte, rec *cr) (err error) {
	if pk == nil {
		return sperrors.InvalidPrimaryKeyError
	}

	if rec == nil {
		err = sperrors.NoDataToStoreError
		return err
	}

	var ttl int64
	switch rec.S {
	case DELETED:
		ttl = self.timeouts["del"]
	case REGISTERED:
		ttl = self.timeouts["reg"]
	default:
		ttl = self.timeouts["live"]
	}
	rec.L = time.Now().UTC().Unix()
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return err
	}

	err = mc.Set(keycode(pk), rec, time.Duration(ttl)*time.Second)
	if err != nil {
		self.isFatal(err)
		self.logger.Error("storage",
			"Failure to set item",
			LogFields{"primarykey": keycode(pk),
				"error": err.Error()})
	}
	return err
}

func (self *Storage) Close() {
	//	self.mc.Close()
}

func (self *Storage) UpdateChannel(pks string, vers int64) (err error) {
	suaid, schid, err := ResolvePK(pks)
	pk, err := binPKFromStrings(suaid, schid)
	if err != nil {
		return err
	}
	return self.binUpdateChannel(pk, vers)
}

//TODO: Optimize this to decode the PK for updates
func (self *Storage) binUpdateChannel(pk []byte, vers int64) (err error) {

	var cRec *cr

	if len(pk) == 0 {
		return sperrors.InvalidPrimaryKeyError
	}

	pks := hex.EncodeToString(pk)
	uaid, chid, err := binResolvePK(pk)
	suaid := hex.EncodeToString(uaid)
	schid := hex.EncodeToString(chid)

	cRec, err = self.fetchRec(pk)

	if err != nil && err.Error() != "NOT FOUND" {
		self.logger.Error("storage",
			"fetchRec error",
			LogFields{"primarykey": pks,
				"error": err.Error()})
		return err
	}

	if cRec != nil {
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("storage",
				"Replacing record",
				LogFields{"primarykey": pks})
		}
		if cRec.S != DELETED {
			newRecord := &cr{
				V: uint64(vers),
				S: LIVE,
				L: time.Now().UTC().Unix()}
			err = self.storeRec(pk, newRecord)
			if err != nil {
				return err
			}
			return nil
		}
	}
	// No record found or the record setting was DELETED
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("storage",
			"Registering channel",
			LogFields{"uaid": suaid,
				"channelID": schid,
				"version":   strconv.FormatInt(vers, 10)})
	}
	err = self.binRegisterAppID(uaid, chid, vers)
	if err == sperrors.ChannelExistsError {
		pk, err = binGenPK(uaid, chid)
		if err != nil {
			return err
		}
		return self.binUpdateChannel(pk, vers)
	}
	return err
}

func (self *Storage) RegisterAppID(uaid, chid string, vers int64) (err error) {
	return self.binRegisterAppID(cleanID(uaid), cleanID(chid), vers)
}

func (self *Storage) binRegisterAppID(uaid, chid []byte, vers int64) (err error) {

	var rec *cr

	if len(chid) == 0 {
		return sperrors.NoChannelError
	}

	appIDArray, err := self.fetchAppIDArray(uaid)
	// Yep, this should eventually be optimized to a faster scan.
	if indexOf(appIDArray, chid) < 0 {
		err = self.storeAppIDArray(uaid, append(appIDArray, chid))

		if err != nil {
			return err
		}
	}

	rec = &cr{
		S: REGISTERED,
		L: time.Now().UTC().Unix()}
	if vers != 0 {
		rec.V = uint64(vers)
		rec.S = LIVE
	}

	pk, err := binGenPK(uaid, chid)
	if err != nil {
		return err
	}

	err = self.storeRec(pk, rec)
	if err != nil {
		return err
	}
	return nil
}

func (self *Storage) DeleteAppID(suaid, schid string, clearOnly bool) (err error) {

	if len(schid) == 0 {
		return sperrors.NoChannelError
	}

	uaid := cleanID(suaid)
	chid := cleanID(schid)

	appIDArray, err := self.fetchAppIDArray(uaid)
	if err != nil {
		return err
	}
	pos := indexOf(appIDArray, chid)
	if pos >= 0 {
		self.storeAppIDArray(uaid, remove(appIDArray, pos))
		pk, err := binGenPK(uaid, chid)
		if err != nil {
			return err
		}
		for x := 0; x < 3; x++ {
			rec, err := self.fetchRec(pk)
			if err == nil {
				rec.S = DELETED
				err = self.storeRec(pk, rec)
				break
			} else {
				self.logger.Error("storage",
					"Could not delete Channel",
					LogFields{"primarykey": hex.EncodeToString(pk),
						"error": err.Error()})
			}
		}
	} else {
		err = sperrors.InvalidChannelError
	}
	return err
}

func (self *Storage) IsKnownUaid(suaid string) bool {
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("storage", "IsKnownUaid", LogFields{"uaid": suaid})
	}
	_, err := self.fetchAppIDArray(cleanID(suaid))
	if err == nil {
		return true
	}
	return false
}

func (self *Storage) GetUpdates(suaid string, lastAccessed int64) (results util.JsMap, err error) {
	uaid := cleanID(suaid)
	appIDArray, err := self.fetchAppIDArray(uaid)

	var updates []map[string]interface{}

	expired := make([]string, 0, 20)
	items := make([]string, 0, 20)

	for _, chid := range appIDArray {
		pk, _ := binGenPK(uaid, chid)
		items = append(items, keycode(pk))
	}
	if self.logger.ShouldLog(DEBUG) {
		self.logger.Debug("storage",
			"Fetching items",
			LogFields{"uaid": keycode(uaid),
				"items": "[" + strings.Join(items, ", ") + "]"})
	}
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return nil, err
	}

	// Apparently, GetMulti is currently broken. Feel free to uncomment
	// this when that code is working correctly.
	/*
		    recs, err := mc.GetMulti(items)
			if err != nil {
				if strings.Contains("NOT FOUND", err.Error()) {
					err = nil
				} else {
					self.isFatal(err)
					if self.logger != nil {
						self.logger.Error("storage", "GetUpdate failed",
							LogFields{"uaid": suaid,
								"error": err.Error()})
					}
					return nil, err
				}
			}

			if recs == nil {
				return nil, err
			}

			// Result has no len or counter.
			resCount := 0
			var i cr
			for _, key := range items {
				if err := recs.Get(key, &i); err == nil {
					resCount = resCount + 1
				}
			}

			if resCount == 0 {
				if self.logger != nil {
					self.logger.Debug("storage",
						"GetUpdates No records found", LogFields{"uaid": suaid})
				}
				return nil, err
			}
	*/
	for _, key := range items {
		var val cr
		//err := recs.Get(key, &val)
		err := mc.Get(key, &val)
		if err != nil {
			continue
		}
		uaid, chid, err := binResolvePK(keydecode(key))
		suaid := hex.EncodeToString(uaid)
		schid := hex.EncodeToString(chid)
		if self.logger.ShouldLog(DEBUG) {
			self.logger.Debug("storage",
				"GetUpdates Fetched record ",
				LogFields{"uaid": suaid,
					"chid":  schid,
					"value": fmt.Sprintf("%d,%d,%d", val.L, val.S, val.V)})
		}
		if err != nil {
			return nil, err
		}
		if val.L < lastAccessed {
			if self.logger.ShouldLog(DEBUG) {
				self.logger.Debug("storage", "Skipping record...",
					LogFields{"uaid": suaid, "chid": schid})
			}
			continue
		}
		// Yay! Go translates numeric interface values as float64s
		// Apparently float64(1) != int(1).
		switch val.S {
		case LIVE:
			var vers uint64
			newRec := make(util.JsMap)
			newRec["channelID"] = hex.EncodeToString(chid)
			vers = val.V
			if vers == 0 {
				vers = uint64(time.Now().UTC().Unix())
				self.logger.Error("storage",
					"GetUpdates Using Timestamp",
					LogFields{"uaid": suaid, "chid": schid})
			}
			newRec["version"] = vers
			updates = append(updates, newRec)
		case DELETED:
			self.logger.Info("storage",
				"GetUpdates Deleting record",
				LogFields{"uaid": suaid, "chid": schid})
			expired = append(expired, schid)
		case REGISTERED:
			// Item registered, but not yet active. Ignore it.
		default:
			self.logger.Warn("storage",
				"Unknown state",
				LogFields{"uaid": suaid, "chid": schid})
		}

	}
	if len(expired) == 0 && len(updates) == 0 {
		return nil, nil
	}
	results = make(util.JsMap)
	results["expired"] = expired
	results["updates"] = updates
	return results, err
}

func (self *Storage) Ack(uaid string, ackPacket map[string]interface{}) (err error) {
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return err
	}
	if _, ok := ackPacket["expired"]; ok {
		if ackPacket["expired"] != nil {
			if cnt := len(ackPacket["expired"].([]interface{})); cnt > 0 {
				expired := make([]string, cnt)
				json.Unmarshal(ackPacket["expired"].([]byte), &expired)
				for _, chid := range expired {
					pk, _ := binPKFromStrings(uaid, chid)
					err = mc.Delete(keycode(pk), time.Duration(0))
					if err != nil {
						self.isFatal(err)
					}
				}
			}
		}
	}
	if p, ok := ackPacket["updates"]; ok {
		if p != nil {
			// unspool the loaded records.
			for _, rec := range p.([]interface{}) {
				if rec == nil {
					continue
				}
				recmap := rec.(map[string]interface{})
				pk, _ := binPKFromStrings(uaid, recmap["channelID"].(string))
				err = mc.Delete(keycode(pk), time.Duration(0))
				if err != nil {
					self.isFatal(err)
				}
			}
		}
	}

	if err != nil && strings.Contains("NOT FOUND", err.Error()) {
		err = nil
	}

	if err != nil {
		return err
	}
	return nil
}

func (self *Storage) ReloadData(uaid string, updates []string) (err error) {
	//TODO: This is not really required.
	_, _ = uaid, updates
	return nil
}

func (self *Storage) PurgeUAID(suaid string) (err error) {
	uaid := cleanID(suaid)
	appIDArray, err := self.fetchAppIDArray(uaid)
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return err
	}
	if err == nil && len(appIDArray) > 0 {
		for _, chid := range appIDArray {
			pk, _ := binGenPK(uaid, chid)
			err = mc.Delete(keycode(pk), time.Duration(0))
		}
	}
	err = mc.Delete(keycode(uaid), time.Duration(0))
	return nil
}

func (self *Storage) Status() (success bool, err error) {
	defer func() {
		if recv := recover(); recv != nil {
			success = false
			err = recv.(error)
			return
		}
	}()

	fake_id, _ := util.GenUUID4()
	key := "status_" + fake_id
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return false, err
	}
	err = mc.Set("status_"+fake_id, "test", 6*time.Second)
	if err != nil {
		return false, err
	}
	var val string
	err = mc.Get(key, &val)
	if err != nil || val != "test" {
		return false, errors.New("Invalid value returned")
	}
	mc.Delete(key, time.Duration(0))
	return true, nil
}

func (self *Storage) returnMC(mc gomc.Client) {
	if mc != nil {
		atomic.AddInt32(&mcsPoolSize, 1)
		self.mcs <- mc
	}
}

func (self *Storage) getMC() (gomc.Client, error) {
	select {
	case mc := <-self.mcs:
		if mc == nil {
			log.Printf("NULL MC!")
			return nil, StorageError{"Connection Pool Saturated"}
		}
		atomic.AddInt32(&mcsPoolSize, -1)
		return mc, nil
	case <-time.After(self.mc_timeout):
		// As noted before, dynamic pool sizing turns out to be deeply
		// problematic at this time.
		// TODO: Investigate why go throws the popdefer phase error on
		// socket shutdown.
		/*		if mcsPoolSize < mcsMaxPoolSize {
					if self.logger != nil {
						self.logger.Warn("storage", "Increasing Pool Size",
							LogFields{"poolSize": strconv.FormatInt(int64(mcsPoolSize+1), 10)})
					}
					return newMC(self.servers,
						self.config,
						self.logger), nil
				} else {
		*/
		if self.logger != nil {
			self.logger.Error("storage", "Connection Pool Saturated!", nil)
		} else {
			log.Printf("Connection Pool Saturated!")
		}
		// mc := self.NewMC(self.servers, self.config)
		return nil, StorageError{"Connection Pool Saturated"}
		//		}
	}
}

func (self *Storage) GetPropConnect(uaid string) (connect string, err error) {
	prefix := self.propPrefix
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return "", err
	}
	err = mc.Get(prefix+uaid, &connect)
	return connect, err
}

func (self *Storage) SetPropConnect(uaid, connect string) (err error) {
	prefix := self.propPrefix
	mc, err := self.getMC()
	defer self.returnMC(mc)
	if err != nil {
		return err
	}
	return mc.Set(prefix+uaid, connect, 0)
}

func (self *Storage) clearPropConnect(uaid string) (err error) {
	prefix := self.propPrefix
	mc, err := self.getMC()
	defer self.returnMC(mc)
	return mc.Delete(prefix+uaid, time.Duration(0))
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
