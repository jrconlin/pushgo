/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package mcstorage

// thin memcache wrapper

/** TODO: Support multiple memcache nodes.
 *      * Need to be able to discover and shard to each node.
 */

import (
	"github.com/ianoshen/gomc"

	"mozilla.org/simplepush/sperrors"
	"mozilla.org/util"

	"encoding/json"
	"errors"
	"fmt"
	//"log"
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
	mc     gomc.Client
	logger *util.HekaLogger
	thrash int64
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
func indexOf(list []string, val string) (index int) {
	for index, v := range list {
		if v == val {
			return index
		}
	}
	return -1
}

// Returns a new slice with the string at position pos removed or
// an equivilant slice if the pos is not in the bounds of the slice
func remove(list []string, pos int) (res []string) {
	if pos < 0 || pos == len(list) {
		return list
	}
	return append(list[:pos], list[pos+1:]...)
}

func ResolvePK(pk string) (uaid, appid string, err error) {
	items := strings.SplitN(pk, ".", 2)
	if len(items) < 2 {
		return pk, "", nil
	}
	return items[0], items[1], nil
}

func GenPK(uaid, appid string) (pk string, err error) {
	pk = fmt.Sprintf("%s.%s", uaid, appid)
	return pk, nil
}

func New(opts util.JsMap, logger *util.HekaLogger) *Storage {
	config = opts
	var ok bool

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
	mc, err := gomc.NewClient(servers,
		int(config["memcache.pool_size"].(int64)),
		gomc.ENCODING_JSON)
	if err != nil {
		logger.Critical("storage", "CRITICAL HIT!",
			util.JsMap{"error": err})
	}
	// internally hash key using MD5 (for key distribution)
	mc.SetBehavior(gomc.BEHAVIOR_KETAMA_HASH, 1)
	mc.SetBehavior(gomc.BEHAVIOR_BINARY_PROTOCOL, 1)
	mc.SetBehavior(gomc.BEHAVIOR_NO_BLOCK, 1)
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
	return &Storage{mc: mc,
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

func (self *Storage) fetchRec(pk string) (result util.JsMap, err error) {
	if pk == "" {
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

	mc := self.mc
	//mc.Timeout = time.Second * 10
	var item string
	err = mc.Get(string(pk), &item)
	if err != nil {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage",
				"Get Failed",
				util.JsMap{"primarykey": pk,
					"error": err})
		}
		return nil, err
	}

	json.Unmarshal([]byte(item), &result)

	if result == nil {
		return nil, err
	}

	if self.logger != nil {
		self.logger.Debug("storage",
			"Fetched",
			util.JsMap{"primarykey": pk,
				"value": item})
	}
	return result, err
}

func (self *Storage) fetchAppIDArray(uaid string) (result []string, err error) {
	if uaid == "" {
		return result, nil
	}
	mc := self.mc
	//mc.Timeout = time.Second * 10
	var raw string
	err = mc.Get(uaid, &raw)
	if err != nil {
		self.isFatal(err)
		return nil, err
	}
	result = strings.Split(raw, ",")
	return result, err
}

func (self *Storage) storeAppIDArray(uaid string, arr sort.StringSlice) (err error) {
	arr.Sort()
	mc := self.mc
	//mc.Timeout = time.Second * 10
	err = mc.Set(uaid, strings.Join(arr, ","), 0)
	if err != nil {
		self.isFatal(err)
	}
	return err
}

func (self *Storage) storeRec(pk string, rec util.JsMap) (err error) {
	if pk == "" {
		return sperrors.InvalidPrimaryKeyError
	}

	if rec == nil {
		err = sperrors.NoDataToStoreError
		return err
	}

	raw, err := json.Marshal(rec)

	if err != nil {
		if self.logger != nil {
			self.logger.Error("storage",
				"storeRec marshalling failure",
				util.JsMap{"error": err,
					"primarykey": pk,
					"record":     rec})
		}
		return err
	}

	var ttls string
	switch rec["s"] {
	case DELETED:
		ttls = config["db.timeout_del"].(string)
	case REGISTERED:
		ttls = config["db.timeout_reg"].(string)
	default:
		ttls = config["db.timeout_live"].(string)
	}
	rec["l"] = time.Now().UTC().Unix()

	ttl, err := strconv.ParseInt(ttls, 0, 0)
	if self.logger != nil {
		self.logger.Debug("storage",
			"Storing record",
			util.JsMap{"primarykey": pk,
				"record": raw})
	}

	err = self.mc.Set(pk, raw, time.Duration(ttl)*time.Second)
	if err != nil {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage",
				fmt.Sprintf("Failure to set item %s {%s}", pk, raw),
				nil)
		}
	}
	return err
}

func (self *Storage) Close() {
	self.mc.Close()
}

//TODO: Optimize this to decode the PK for updates
func (self *Storage) UpdateChannel(pk string, vers int64) (err error) {

	var rec util.JsMap

	if len(pk) == 0 {
		return sperrors.InvalidPrimaryKeyError
	}

	rec, err = self.fetchRec(pk)

	if err != nil && err.Error() != "NOT FOUND" {
		if self.logger != nil {
			self.logger.Error("storage",
				fmt.Sprintf("fetchRec %s err %s", pk, err), nil)
		}
		return err
	}

	if rec != nil {
		if self.logger != nil {
			self.logger.Debug("storage", fmt.Sprintf("Found record for %s", pk), nil)
		}
		if rec["s"] != DELETED {
			newRecord := make(util.JsMap)
			newRecord["v"] = vers
			newRecord["s"] = LIVE
			newRecord["l"] = time.Now().UTC().Unix()
			err = self.storeRec(pk, newRecord)
			if err != nil {
				return err
			}
			return nil
		}
	}
	// No record found or the record setting was DELETED
	uaid, appid, err := ResolvePK(pk)
	if self.logger != nil {
		self.logger.Debug("storage",
			"Registering channel",
			util.JsMap{"uaid": uaid,
				"channelID": appid,
				"version":   vers})
	}
	err = self.RegisterAppID(uaid, appid, vers)
	if err == sperrors.ChannelExistsError {
		pk, err = GenPK(uaid, appid)
		if err != nil {
			return err
		}
		return self.UpdateChannel(pk, vers)
	}
	return err
}

func (self *Storage) RegisterAppID(uaid, appid string, vers int64) (err error) {

	var rec util.JsMap

	if len(appid) == 0 {
		return sperrors.NoChannelError
	}

	appIDArray, err := self.fetchAppIDArray(uaid)
	// Yep, this should eventually be optimized to a faster scan.
	if appIDArray != nil {
		appIDArray = remove(appIDArray, indexOf(appIDArray, appid))
	}
	err = self.storeAppIDArray(uaid, append(appIDArray, appid))
	if err != nil {
		return err
	}

	rec = make(util.JsMap)
	rec["s"] = REGISTERED
	rec["l"] = time.Now().UTC().Unix()
	if vers != 0 {
		rec["v"] = vers
		rec["s"] = LIVE
	}

	pk, err := GenPK(uaid, appid)
	if err != nil {
		return err
	}

	err = self.storeRec(pk, rec)
	if err != nil {
		return err
	}
	return nil
}

func (self *Storage) DeleteAppID(uaid, appid string, clearOnly bool) (err error) {

	if len(appid) == 0 {
		return sperrors.NoChannelError
	}

	appIDArray, err := self.fetchAppIDArray(uaid)
	if err != nil {
		return err
	}
	pos := sort.SearchStrings(appIDArray, appid)
	if pos > -1 {
		self.storeAppIDArray(uaid, remove(appIDArray, pos))
		pk, err := GenPK(uaid, appid)
		if err != nil {
			return err
		}
		for x := 0; x < 3; x++ {
			rec, err := self.fetchRec(pk)
			if err == nil {
				rec["s"] = DELETED
				err = self.storeRec(pk, rec)
				break
			} else {
				if self.logger != nil {
					self.logger.Error("storage",
						"Could not delete Channel",
						util.JsMap{"primarykey": pk, "error": err})
				}
			}
		}
	} else {
		err = sperrors.InvalidChannelError
	}
	return err
}

func (self *Storage) IsKnownUaid(uaid string) bool {
	if self.logger != nil {
		self.logger.Debug("storage", "IsKnownUaid", util.JsMap{"uaid": uaid})
	}
	_, err := self.fetchAppIDArray(uaid)
	if err == nil {
		return true
	}
	return false
}

func (self *Storage) GetUpdates(uaid string, lastAccessed int64) (results util.JsMap, err error) {
	appIDArray, err := self.fetchAppIDArray(uaid)

	var updates []map[string]interface{}

	expired := make([]string, 0, 20)
	items := make([]string, 0, 20)

	for _, appid := range appIDArray {
		pk, _ := GenPK(uaid, appid)
		// TODO: Puke on error
		items = append(items, pk)
	}
	if self.logger != nil {
		self.logger.Debug("storage",
			"Fetching items",
			util.JsMap{"uaid": uaid,
				"items": items})
	}
	mc := self.mc
	recs, err := mc.GetMulti(items)
	if err != nil {
		self.isFatal(err)
		if self.logger != nil {
			self.logger.Error("storage", "GetUpdate failed",
				util.JsMap{"uaid": uaid,
					"error": err})
		}
		return nil, err
	}

	// Result has no len or counter.
	resCount := 0
	var i string
	for _, key := range items {
		if err := recs.Get(key, &i); err == nil {
			resCount = resCount + 1
		}
	}

	var update util.JsMap
	if resCount == 0 {
		if self.logger != nil {
			self.logger.Debug("storage",
				"GetUpdates No records found", util.JsMap{"uaid": uaid})
		}
		return nil, err
	}
	for _, key := range items {
		var val string
		err := recs.Get(key, &val)
		if err != nil {
			continue
		}
		uaid, appid, err := ResolvePK(key)
		if self.logger != nil {
			self.logger.Debug("storage",
				"GetUpdates Fetched record ",
				util.JsMap{"uaid": uaid,
					"value": val})
		}
		err = json.Unmarshal([]byte(val), &update)
		if err != nil {
			return nil, err
		}
		if int64(update["l"].(float64)) < lastAccessed {
			if self.logger != nil {
				self.logger.Debug("storage", "Skipping record...", util.JsMap{"rec": update})
			}
			continue
		}
		// Yay! Go translates numeric interface values as float64s
		// Apparently float64(1) != int(1).
		switch update["s"] {
		case float64(LIVE):
			var fvers float64
			var ok bool
			newRec := make(util.JsMap)
			newRec["channelID"] = appid
			fvers, ok = update["v"].(float64)
			if !ok {
				var cerr error
				if self.logger != nil {
					self.logger.Warn("storage",
						"GetUpdates Possibly bad version",
						util.JsMap{"update": update})
				}
				fvers, cerr = strconv.ParseFloat(update["v"].(string), 0)
				if cerr != nil {
					if self.logger != nil {
						self.logger.Error("storage",
							"GetUpdates Using Timestamp",
							util.JsMap{"update": update})
					}
					fvers = float64(time.Now().UTC().Unix())
				}
			}
			newRec["version"] = int64(fvers)
			updates = append(updates, newRec)
		case float64(DELETED):
			if self.logger != nil {
				self.logger.Info("storage",
					"GetUpdates Deleting record",
					util.JsMap{"update": update})
			}
			expired = append(expired, appid)
		case float64(REGISTERED):
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

	mc := self.mc
	if _, ok := ackPacket["expired"]; ok {
		if ackPacket["expired"] != nil {
			if cnt := len(ackPacket["expired"].([]interface{})); cnt > 0 {
				expired := make([]string, cnt)
				json.Unmarshal(ackPacket["expired"].([]byte), &expired)
				for _, appid := range expired {
					pk, _ := GenPK(uaid, appid)
					err = mc.Delete(pk, time.Duration(0))
					if err != nil {
						self.isFatal(err)
					}
				}
			}
		}
	}
	if _, ok := ackPacket["updates"]; ok {
		if ackPacket["updates"] != nil {
			// unspool the loaded records.
			for _, rec := range ackPacket["updates"].([]interface{}) {
				recmap := rec.(map[string]interface{})
				pk, _ := GenPK(uaid, recmap["channelID"].(string))
				err = mc.Delete(pk, time.Duration(0))
				if err != nil {
					self.isFatal(err)
				}
			}
		}
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
	mc := self.mc
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

	mc := self.mc
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

func (self *Storage) PurgeUAID(uaid string) (err error) {
	appIDArray, err := self.fetchAppIDArray(uaid)
	mc := self.mc
	if err == nil && len(appIDArray) > 0 {
		for _, appid := range appIDArray {
			pk, _ := GenPK(uaid, appid)
			err = mc.Delete(pk, time.Duration(0))
		}
	}
	err = mc.Delete(uaid, time.Duration(0))
	self.DelUAIDHost(uaid)
	return nil
}

func (self *Storage) DelUAIDHost(uaid string) (err error) {
	prefix := self.config["shard.prefix"].(string)
	mc := self.mc
	//mc.Timeout = time.Second * 10
	err = mc.Delete(prefix+uaid, time.Duration(0))
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
	mc := self.mc
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
