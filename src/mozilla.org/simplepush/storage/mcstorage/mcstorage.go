/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package mcstorage

// thin memcache wrapper

/** This library uses libmemcache (via gomc) to do most of it's work.
 * libmemcache handles key sharding, multiple nodes, node balancing, etc.
 */

import (
	"github.com/ianoshen/gomc"

	"mozilla.org/simplepush/sperrors"
	"mozilla.org/util"

	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	//	"log"
	"bufio"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DELETED = iota
	LIVE
	REGISTERED
)

var (
	config        util.JsMap
	no_whitespace *strings.Replacer = strings.NewReplacer(" ", "",
		"\x09", "",
		"\x0a", "",
		"\x0b", "",
		"\x0c", "",
		"\x0d", "")
)

type Storage struct {
	config util.JsMap
	mcs    chan gomc.Client
	logger *util.HekaLogger
	thrash int64
}

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
	err   error
	retry int
}

func (e *StorageError) Error() string {
	return "StorageError: " + e.err.Error()
}

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
	copy(pk, uaid)
	copy(pk[16:], chid)
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

func New(opts util.JsMap, logger *util.HekaLogger) *Storage {
	config = opts
	var ok bool

	if configEndpoint, ok := config["elasticache.config_endpoint"]; ok {
		memcacheEndpoint, err := getElastiCacheEndpointsTimeout(configEndpoint.(string), 2)
		if err == nil {
			config["memcache.server"] = memcacheEndpoint
		} else {
			fmt.Println(err)
			if logger != nil {
				logger.Error("storage", "Elastisearch error.",
					util.JsMap{"error": err})
			}
		}
	}

	if _, ok = config["memcache.server"]; !ok {
		config["memcache.server"] = "127.0.0.1:11211"
	}

	if _, ok = config["memcache.pool_size"]; !ok {
		config["memcache.pool_size"] = "100"
	}
	config["memcache.pool_size"], _ =
		strconv.ParseInt(config["memcache.pool_size"].(string), 0, 0)
	if _, ok = config["db.timeout_live"]; !ok {
		config["db.timeout_live"] = "259200"
	}

	if _, ok = config["db.timeout_reg"]; !ok {
		config["db.timeout_reg"] = "10800"
	}

	if _, ok = config["db.timeout_del"]; !ok {
		config["db.timeout_del"] = "86400"
	}
	if _, ok = config["shard.default_host"]; !ok {
		config["shard.default_host"] = "localhost"
	}
	if _, ok = config["shard.current_host"]; !ok {
		config["shard.current_host"] = config["shard.default_host"]
	}
	if _, ok = config["shard.prefix"]; !ok {
		config["shard.prefix"] = "_h-"
	}

	if logger != nil {
		logger.Info("storage", "Creating new gomc handler", nil)
	}
	// do NOT include any spaces
	servers := strings.Split(
		no_whitespace.Replace(config["memcache.server"].(string)),
		",")

	poolSize := int(config["memcache.pool_size"].(int64))
	mcs := make(chan gomc.Client, poolSize)
	for i := 0; i < poolSize; i++ {
		mc, err := gomc.NewClient(servers, 1, gomc.ENCODING_GOB)
		if err != nil {
			logger.Critical("storage", "CRITICAL HIT!",
				util.JsMap{"error": err})
		}
		// internally hash key using MD5 (for key distribution)
		mc.SetBehavior(gomc.BEHAVIOR_KETAMA_HASH, 1)
		// Use the binary protocol, which allows us faster data xfer
		// and better data storage (can use full UTF-8 char space)
		mc.SetBehavior(gomc.BEHAVIOR_BINARY_PROTOCOL, 1)
		//mc.SetBehavior(gomc.BEHAVIOR_NO_BLOCK, 1)
		// NOTE! do NOT set BEHAVIOR_NOREPLY + Binary. This will cause
		// libmemcache to drop into an infinite loop.
		if v, ok := config["memcache.recv_timeout"]; ok {
			d, err := time.ParseDuration(v.(string))
			if err == nil {
				mc.SetBehavior(gomc.BEHAVIOR_SND_TIMEOUT,
					uint64(d.Nanoseconds()*1000))
			}
		}
		if v, ok := config["memcache.send_timeout"]; ok {
			d, err := time.ParseDuration(v.(string))
			if err == nil {
				mc.SetBehavior(gomc.BEHAVIOR_RCV_TIMEOUT,
					uint64(d.Nanoseconds()*1000))
			}
		}
		if v, ok := config["memcache.poll_timeout"]; ok {
			d, err := time.ParseDuration(v.(string))
			if err == nil {
				mc.SetBehavior(gomc.BEHAVIOR_POLL_TIMEOUT,
					uint64(d.Nanoseconds()*1000))
			}
		}
		if v, ok := config["memcache.retry_timeout"]; ok {
			d, err := time.ParseDuration(v.(string))
			if err == nil {
				mc.SetBehavior(gomc.BEHAVIOR_RETRY_TIMEOUT,
					uint64(d.Nanoseconds()*1000))
			}
		}
		mcs <- mc
	}

	return &Storage{
		mcs:    mcs,
		config: config,
		logger: logger,
		thrash: 0}
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
		if self.logger != nil {
			self.logger.Critical("storage", "CRITICAL HIT! RESTARTING!",
				util.JsMap{"error": err})
		}
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
			if self.logger != nil {
				self.logger.Error("storage",
					fmt.Sprintf("could not fetch record for %s", pk),
					util.JsMap{"primarykey": pk, "error": err})
			}
		}
	}()

	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
	//mc.Timeout = time.Second * 10
	err = mc.Get(keycode(pk), result)
	if err != nil && strings.Contains("NOT FOUND", err.Error()) {
		err = nil
	}
	if err != nil {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage",
				"Get Failed",
				util.JsMap{"primarykey": hex.EncodeToString(pk),
					"error": err})
		}
		return nil, err
	}

	if self.logger != nil {
		self.logger.Debug("storage",
			"Fetched",
			util.JsMap{"primarykey": hex.EncodeToString(pk),
				"value": result})
	}
	return result, err
}

func (self *Storage) fetchAppIDArray(uaid []byte) (result ia, err error) {
	if uaid == nil {
		return result, nil
	}
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
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
	return result, err
}

func (self *Storage) storeAppIDArray(uaid []byte, arr ia) (err error) {
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
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

	var ttls string
	switch rec.S {
	case DELETED:
		ttls = config["db.timeout_del"].(string)
	case REGISTERED:
		ttls = config["db.timeout_reg"].(string)
	default:
		ttls = config["db.timeout_live"].(string)
	}
	rec.L = time.Now().UTC().Unix()

	ttl, err := strconv.ParseInt(ttls, 0, 0)
	if self.logger != nil {
		self.logger.Debug("storage",
			"Storing record",
			util.JsMap{"primarykey": keycode(pk),
				"record": fmt.Sprintf("%d,%d,%d", rec.L, rec.S, rec.V)})
	}
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()

	err = mc.Set(keycode(pk), rec, time.Duration(ttl)*time.Second)
	if err != nil {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage",
				fmt.Sprintf("Failure to set item %s {%v}", pk, rec),
				nil)
		}
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
		if self.logger != nil {
			self.logger.Error("storage",
				fmt.Sprintf("fetchRec %s err %s", pks, err), nil)
		}
		return err
	}

	if cRec != nil {
		if self.logger != nil {
			self.logger.Debug("storage", fmt.Sprintf("Found record for %s", pks), nil)
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
	if self.logger != nil {
		self.logger.Debug("storage",
			"Registering channel",
			util.JsMap{"uaid": suaid,
				"channelID": schid,
				"version":   vers})
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
	if appIDArray != nil {
		appIDArray = remove(appIDArray, indexOf(appIDArray, chid))
	}
	err = self.storeAppIDArray(uaid, append(appIDArray, chid))
	if err != nil {
		return err
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
	pos := sort.Search(len(appIDArray),
		func(i int) bool { return bytes.Compare(appIDArray[i], chid) >= 0 })
	if pos < len(appIDArray) && bytes.Equal(appIDArray[pos], chid) {
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
				if self.logger != nil {
					self.logger.Error("storage",
						"Could not delete Channel",
						util.JsMap{"primarykey": hex.EncodeToString(pk), "error": err})
				}
			}
		}
	} else {
		err = sperrors.InvalidChannelError
	}
	return err
}

func (self *Storage) IsKnownUaid(suaid string) bool {
	if self.logger != nil {
		self.logger.Debug("storage", "IsKnownUaid", util.JsMap{"uaid": suaid})
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
		// TODO: Puke on error
		items = append(items, keycode(pk))
	}
	if self.logger != nil {
		self.logger.Debug("storage",
			"Fetching items",
			util.JsMap{"uaid": fmt.Sprintf("%x", uaid),
				"items": items})
	}
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()

	recs, err := mc.GetMulti(items)
	if err != nil {
		if strings.Contains("NOT FOUND", err.Error()) {
			err = nil
		} else {
			self.isFatal(err)
			if self.logger != nil {
				self.logger.Error("storage", "GetUpdate failed",
					util.JsMap{"uaid": suaid,
						"error": err})
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

	var update util.JsMap
	if resCount == 0 {
		if self.logger != nil {
			self.logger.Debug("storage",
				"GetUpdates No records found", util.JsMap{"uaid": suaid})
		}
		return nil, err
	}
	for _, key := range items {
		var val cr
		err := recs.Get(key, &val)
		if err != nil {
			continue
		}
		uaid, chid, err := binResolvePK(keydecode(key))
		if self.logger != nil {
			self.logger.Debug("storage",
				"GetUpdates Fetched record ",
				util.JsMap{"uaid": keycode(uaid),
					"chid":  keycode(chid),
					"value": fmt.Sprintf("%d,%d,%d", val.L, val.S, val.V)})
		}
		if err != nil {
			return nil, err
		}
		if val.L < lastAccessed {
			if self.logger != nil {
				self.logger.Debug("storage", "Skipping record...", util.JsMap{"rec": update})
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
					util.JsMap{"update": update})
			}
			newRec["version"] = vers
			updates = append(updates, newRec)
		case DELETED:
			if self.logger != nil {
				self.logger.Info("storage",
					"GetUpdates Deleting record",
					util.JsMap{"update": update})
			}
			expired = append(expired, hex.EncodeToString(chid))
		case REGISTERED:
			// Item registered, but not yet active. Ignore it.
		default:
			if self.logger != nil {
				self.logger.Warn("storage",
					"Unknown state",
					util.JsMap{"update": update})
			}
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
	//TODO, go through the results and nuke what's there, then call flush

	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
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

func (self *Storage) SetUAIDHost(uaid string) (err error) {
	host := self.config["shard.current_host"].(string)
	prefix := self.config["shard.prefix"].(string)

	if uaid == "" {
		return sperrors.MissingDataError
	}

	if self.logger != nil {
		self.logger.Debug("storage",
			"SetUAIDHost",
			util.JsMap{"uaid": uaid, "host": host})
	}
	ttl, _ := strconv.ParseInt(self.config["db.timeout_live"].(string), 0, 0)
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
	err = mc.Set(prefix+uaid, host, time.Duration(ttl)*time.Second)
	if err != nil {
		if strings.Contains("NOT FOUND", err.Error()) {
			err = nil
		}
	}
	self.isFatal(err)
	return err
}

func (self *Storage) GetUAIDHost(uaid string) (host string, err error) {
	defaultHost := self.config["shard.default_host"].(string)
	prefix := self.config["shard.prefix"].(string)

	defer func(defaultHost string) {
		if err := recover(); err != nil {
			if self.logger != nil {
				self.logger.Error("storage",
					"GetUAIDHost no host",
					util.JsMap{"uaid": uaid,
						"error": err})
			}
		}
	}(defaultHost)

	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
	var val string
	err = mc.Get(prefix+uaid, &val)
	if err != nil && err != errors.New("NOT FOUND") {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage",
				"GetUAIDHost Fetch error",
				util.JsMap{"uaid": uaid,
					"item":  val,
					"error": err})
		}
		return defaultHost, err
	}
	if val == "" {
		val = defaultHost
	}
	if self.logger != nil {
		self.logger.Debug("storage",
			"GetUAIDHost",
			util.JsMap{"uaid": uaid,
				"host": val})
	}
	// reinforce the link.
	self.SetUAIDHost(val)
	return string(val), nil
}

func (self *Storage) PurgeUAID(suaid string) (err error) {
	uaid := cleanID(suaid)
	appIDArray, err := self.fetchAppIDArray(uaid)
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
	if err == nil && len(appIDArray) > 0 {
		for _, chid := range appIDArray {
			pk, _ := binGenPK(uaid, chid)
			err = mc.Delete(keycode(pk), time.Duration(0))
		}
	}
	err = mc.Delete(keycode(uaid), time.Duration(0))
	self.DelUAIDHost(suaid)
	return nil
}

func (self *Storage) DelUAIDHost(uaid string) (err error) {
	prefix := self.config["shard.prefix"].(string)
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
	//mc.Timeout = time.Second * 10
	err = mc.Delete(prefix+uaid, time.Duration(0))
	if err != nil && strings.Contains("NOT FOUND", err.Error()) {
		err = nil
	}
	self.isFatal(err)
	return err
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
	mc := <-self.mcs
	defer func() { self.mcs <- mc }()
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

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
