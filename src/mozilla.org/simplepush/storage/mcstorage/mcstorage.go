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
		"\x08", "",
		"\x09", "",
		"\x0a", "",
		"\x0b", "",
		"\x0c", "",
		"\x0d", "")
)

type Storage struct {
	config     util.JsMap
	mcs        chan gomc.Client
	logger     *util.HekaLogger
	mc_timeout time.Duration
	servers    []string
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
	err   string
}

func (e StorageError) Error() string {
	// foo call so that import log doesn't complain
	_ = log.Flags()
	return "StorageError: " + e.err
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
					util.Fields{"error": err.Error()})
			}
		}
	}

	if _, ok = config["memcache.server"]; !ok {
		config["memcache.server"] = "127.0.0.1:11211"
	}

	if _, ok = config["memcache.pool_size"]; !ok {
		config["memcache.pool_size"] = "100"
	}
	timeout, err := time.ParseDuration(util.MzGet(config, "db.handle_timeout", "5s"))
	if err != nil {
		if logger != nil {
			logger.Error("storage", "Could not parse db.handle_timeout",
				util.Fields{"err": err.Error()})
		}
		timeout = 10 * time.Second
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
		mcs <- newMC(servers, config, logger)
	}

	log.Printf("############# NEW HANDLER ")

	return &Storage{
		mcs:        mcs,
		config:     config,
		logger:     logger,
		mc_timeout: timeout,
		servers:    servers,
	}
}

// Generate a new Memcache Client
func newMC(servers []string, config util.JsMap, logger *util.HekaLogger) (mc gomc.Client) {
	var err error
	mc, err = gomc.NewClient(servers, 1, gomc.ENCODING_GOB)
	if err != nil {
		logger.Critical("storage", "CRITICAL HIT!",
			util.Fields{"error": err.Error()})
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
		if self.logger != nil {
			self.logger.Critical("storage", "CRITICAL HIT! RESTARTING!",
				util.Fields{"error": err.Error()})
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
					util.Fields{"primarykey": keycode(pk),
						"error": err.(error).Error()})
			}
		}
	}()

	mc, err := self.getMC()
	if err != nil {
		return nil, err
	}
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
				util.Fields{"primarykey": hex.EncodeToString(pk),
					"error": err.Error()})
		}
		return nil, err
	}

	if self.logger != nil {
		self.logger.Debug("storage",
			"Fetched",
			util.Fields{"primarykey": hex.EncodeToString(pk),
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
	if err != nil {
		return result, err
	}
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
	if err != nil {
		return nil
	}
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
			util.Fields{"primarykey": keycode(pk),
				"record": fmt.Sprintf("%d,%d,%d", rec.L, rec.S, rec.V)})
	}
	mc, err := self.getMC()
	if err != nil {
		return err
	}
	defer func() { self.mcs <- mc }()

	err = mc.Set(keycode(pk), rec, time.Duration(ttl)*time.Second)
	if err != nil {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage",
				"Failure to set item",
				util.Fields{"primarykey": keycode(pk),
					"error": err.Error()})
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
				"fetchRec error",
				util.Fields{"primarykey": pks,
					"error": err.Error()})
		}
		return err
	}

	if cRec != nil {
		if self.logger != nil {
			self.logger.Debug("storage",
				"Replacing record",
				util.Fields{"primarykey": pks})
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
			util.Fields{"uaid": suaid,
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
				if self.logger != nil {
					self.logger.Error("storage",
						"Could not delete Channel",
						util.Fields{"primarykey": hex.EncodeToString(pk),
							"error": err.Error()})
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
		self.logger.Debug("storage", "IsKnownUaid", util.Fields{"uaid": suaid})
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
	if self.logger != nil {
		self.logger.Debug("storage",
			"Fetching items",
			util.Fields{"uaid": keycode(uaid),
				"items": "[" + strings.Join(items, ", ") + "]"})
	}
	mc, err := self.getMC()
	if err != nil {
		return nil, err
	}
	defer func() { self.mcs <- mc }()

	// Apparently, GetMulti is broken.
	/*
		    recs, err := mc.GetMulti(items)
			if err != nil {
				if strings.Contains("NOT FOUND", err.Error()) {
					err = nil
				} else {
					self.isFatal(err)
					if self.logger != nil {
						self.logger.Error("storage", "GetUpdate failed",
							util.Fields{"uaid": suaid,
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
						"GetUpdates No records found", util.Fields{"uaid": suaid})
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
		if self.logger != nil {
			self.logger.Debug("storage",
				"GetUpdates Fetched record ",
				util.Fields{"uaid": suaid,
					"chid":  schid,
					"value": fmt.Sprintf("%d,%d,%d", val.L, val.S, val.V)})
		}
		if err != nil {
			return nil, err
		}
		if val.L < lastAccessed {
			if self.logger != nil {
				self.logger.Debug("storage", "Skipping record...",
					util.Fields{"uaid": suaid, "chid": schid})
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
					util.Fields{"uaid": suaid, "chid": schid})
			}
			newRec["version"] = vers
			updates = append(updates, newRec)
		case DELETED:
			if self.logger != nil {
				self.logger.Info("storage",
					"GetUpdates Deleting record",
					util.Fields{"uaid": suaid, "chid": schid})
			}
			expired = append(expired, schid)
		case REGISTERED:
			// Item registered, but not yet active. Ignore it.
		default:
			if self.logger != nil {
				self.logger.Warn("storage",
					"Unknown state",
					util.Fields{"uaid": suaid, "chid": schid})
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

	mc, err := self.getMC()
	if err != nil {
		return err
	}
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

func (self *Storage) SetUAIDHost(uaid, host string) (err error) {
	if host == "" {
		host = self.config["shard.current_host"].(string)
	}
	prefix := self.config["shard.prefix"].(string)

	if uaid == "" {
		return sperrors.MissingDataError
	}

	if self.logger != nil {
		self.logger.Debug("storage",
			"SetUAIDHost",
			util.Fields{"uaid": uaid, "host": host})
	}
	ttl, _ := strconv.ParseInt(self.config["db.timeout_live"].(string), 0, 0)
	mc, err := self.getMC()
	if err != nil {
		return err
	}
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
					util.Fields{"uaid": uaid,
						"error": err.(error).Error()})
			}
		}
	}(defaultHost)

	mc, err := self.getMC()
	if err != nil {
		return defaultHost, err
	}
	defer func() { self.mcs <- mc }()
	var val string
	err = mc.Get(prefix+uaid, &val)
	if err != nil && err != errors.New("NOT FOUND") {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage",
				"GetUAIDHost Fetch error",
				util.Fields{"uaid": uaid,
					"item":  val,
					"error": err.Error()})
		}
		return defaultHost, err
	}
	if val == "" {
		val = defaultHost
	}
	if self.logger != nil {
		self.logger.Debug("storage",
			"GetUAIDHost",
			util.Fields{"uaid": uaid,
				"host": val})
	}
	// reinforce the link.
	self.SetUAIDHost(uaid, val)
	return string(val), nil
}

func (self *Storage) PurgeUAID(suaid string) (err error) {
	uaid := cleanID(suaid)
	appIDArray, err := self.fetchAppIDArray(uaid)
	mc, err := self.getMC()
	if err != nil {
		return err
	}
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
	mc, err := self.getMC()
	if err != nil {
		return err
	}
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
	mc, err := self.getMC()
	if err != nil {
		return false, err
	}
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

func (self *Storage) getMC() (gomc.Client, error) {
	select {
	case mc := <-self.mcs:
		return mc, nil
	case <-time.After(self.mc_timeout):
		if self.logger != nil {
			self.logger.Error("storage", "Connection Pool Saturated!", nil)
		}
		// mc := self.NewMC(self.servers, self.config)
		return nil, StorageError{"Connection Pool Saturated"}
	}
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
