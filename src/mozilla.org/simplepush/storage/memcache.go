/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package storage

// thin memcache wrapper

import (
    "github.com/bradfitz/gomemcache/memcache"
    "mozilla.org/simplepush/sperrors"
    "mozilla.org/util"

    "encoding/json"
    "fmt"
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

var config util.JsMap

type Storage struct {
    config util.JsMap
    mc *memcache.Client
    log *util.HekaLogger
}

func indexOf(list []string, val string) (index int) {
    for index, v := range list {
        if (v == val) {
            return index
        }
    }
    return -1
}

func ResolvePK(pk string) (uaid, appid string, err error) {
    items := strings.SplitN(pk, ".", 2)
    if len(items) < 2 {
        return pk, "", nil
    }
    return items[0], items[1], nil
}

func GenPK(uaid, appid string) (pk string, err error){
    pk = fmt.Sprintf("%s.%s", uaid, appid)
    return pk, nil
}

func (self *Storage) fetchRec(pk string) (result util.JsMap, err error){
    if pk == "" {
        err = sperrors.InvalidPrimaryKeyError
        return nil, err
    }

    defer func () {
        if err := recover(); err != nil {
            self.log.Error("storage",
                  fmt.Sprintf("could not fetch record for %s: %s", pk, err),
                  nil)
        }
    }()

    item, err := self.mc.Get(string(pk))
    if err != nil {
        self.log.Error("storage",
                        fmt.Sprintf("Fetch item:: [%s][%s] Error: %s", pk, item, err),
                        nil)
        return nil, err
    }

    self.log.Debug("storage",
                   fmt.Sprintf("Fetch item:: %s, item.Value: %s", item, item.Value),
                   nil)

    json.Unmarshal(item.Value, &result)

    if result == nil {
        return nil, err
    }

    self.log.Info("storage", fmt.Sprintf("%s => %s", pk, result), nil)
    return result, err
}

func (self *Storage) fetchAppIDArray(uaid string) (result []string, err error) {
    raw, err := self.mc.Get(uaid)
    if err != nil {
        return nil, err
    }
    result = strings.Split(string(raw.Value), ",")
    return result, err
}

func (self *Storage) storeAppIDArray(uaid string, arr sort.StringSlice) (err error) {
    arr.Sort()
    err = self.mc.Set(&memcache.Item{Key: uaid,
        Value: []byte(strings.Join(arr, ","))})
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
        self.log.Error("storage",
                       fmt.Sprintf("cannot marshal record [%s], %s", rec, err),
                       nil)
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
    self.log.Info("storage",
                  fmt.Sprintf("Storing record %s => %s", pk, raw), nil)
    item := &memcache.Item{Key: pk,
                 Value: []byte(raw),
                 Expiration: int32(ttl)}
    err = self.mc.Set(item)
    if err != nil {
        self.log.Error("storage",
                        fmt.Sprintf("Failure to set item %s {%s}", pk, item),
                        nil)
    }
    return err
}


func New(opts util.JsMap, log *util.HekaLogger) *Storage {

    config = opts
    var ok bool

    if _, ok = config["memcache.server"]; !ok {
        config["memcache.server"] = "127.0.0.1:11211"
    }

    if _, ok = config["db.timeout_live"]; !ok {
        config["db.timeout_live"] = "259200"
    }

    if _, ok = config["db.timeout_reg"]; !ok {
        config["db.timeout_reg"] = "10800"
    }

    if _, ok = config["db.timeout_del"]; !ok {
        config["db.timeout_del"] = "86400"
    }

    log.Info("storage", "Creating new memcache handler", nil)
    return &Storage{mc: memcache.New(config["memcache.server"].(string)),
                    log: log}
}


//TODO: Optimize this to decode the PK for updates
func (self *Storage) UpdateChannel(pk, vers string) (err error) {
    var rec util.JsMap

    if len(pk) == 0 {
        return sperrors.InvalidPrimaryKeyError
    }

    rec, err = self.fetchRec(pk)

    if err != nil && err != memcache.ErrCacheMiss {
        return err
    }

    if rec != nil {
        self.log.Debug("storage", fmt.Sprintf("Found record for %s", pk), nil)
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
    fmt.Printf("Registering %s %s %s\n", uaid, appid, vers)
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


func (self *Storage) RegisterAppID(uaid, appid, vers string) (err error) {

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
    if vers != "" {
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

func remove(list []string, pos int) (res []string) {
    if(pos < 0) {
        return list
    }
    if pos == len(list) {
        return list[:pos]
    }
    return append(list[:pos], list[pos+1:]...)
}

func (self *Storage) DeleteAppID(uaid, appid string, clearOnly bool) (err error) {

    appIDArray, err := self.fetchAppIDArray(uaid)
    pos := sort.SearchStrings(appIDArray, appid)
    if pos > -1 {
        self.storeAppIDArray(uaid, remove(appIDArray, pos))
        pk, err := GenPK(uaid, appid)
        if err != nil {
            return err
        }
        rec, err := self.fetchRec(pk)
        if err == nil {
            rec["s"] = DELETED
            err = self.storeRec(pk, rec)
        } else {
            self.log.Error("storage", fmt.Sprintf("Could not delete %s, %s", pk, err), nil)
        }
    }
    return err
}

func (self *Storage) GetUpdates(uaid string, lastAccessed int64) (results util.JsMap, err error) {
    appIDArray, err := self.fetchAppIDArray(uaid)

    var updates []map[string]interface{}
    var expired []string
    var items []string

    for _, appid := range appIDArray {
        pk,_ := GenPK(uaid, appid)
        // TODO: Puke on error
        items = append(items, pk)
    }
    self.log.Info("storage", fmt.Sprintf("Fetching items %s", items), nil)

    recs, err := self.mc.GetMulti(items)
    if err != nil {
        self.log.Error("storage", fmt.Sprintf("%s", err), nil)
        return nil, err
    }

    var update util.JsMap
    if len(recs) == 0 {
        self.log.Info("storage",
                      fmt.Sprintf("No records found for %s", uaid),
                      nil)
        return nil, err
    }
    for _, rec := range recs {
        uaid, appid, err := ResolvePK(rec.Key)
        self.log.Info("storage",
                      fmt.Sprintf("Fetched %s record %s", uaid, rec.Value),
                      nil)
        err = json.Unmarshal(rec.Value, &update)
        if err != nil {
            return nil, err
        }
        if int64(update["l"].(float64)) < lastAccessed {
            self.log.Info("storage", "Skipping record...", nil)
            continue
        }
        // Yay! Go translates numeric interface values as float64s
        // Apparently float64(1) != int(1).
        switch update["s"] {
        case float64(LIVE):
            // log.Printf("Adding record... %s", appid)
            newRec := make(util.JsMap)
            newRec["channelID"] = appid
            newRec["version"] = update["v"]
            updates = append(updates, newRec)
        case float64(DELETED):
            self.log.Info("storage",
                          fmt.Sprintf("Deleting record... %s", appid), nil)
            expired = append(expired, appid)
        default:
            self.log.Warn("storage",
                          fmt.Sprintf("WARN : Unknown state %d", update["s"]),
                          nil)
        }

    }
    results = make(util.JsMap)
    results["expired"] = expired
    results["updates"] = updates
    return results, err
}

func (self *Storage) Ack(uaid string, ackPacket map[string]interface{}) (err error) {
    //TODO, go through the results and nuke what's there, then call flush

    if _, ok := ackPacket["expired"]; ok {
        if ackPacket["expired"] != nil {
            expired := make([]string, strings.Count(ackPacket["expired"].(string), ",")+1)
            json.Unmarshal(ackPacket["expired"].([]byte), &expired)
            for _, appid := range expired {
                pk, _ := GenPK(uaid, appid)
                err = self.mc.Delete(pk)
            }
        }
    }
    if _, ok := ackPacket["updates"]; ok {
        if ackPacket["updates"] != nil {
            // unspool the loaded records.
            for _, rec := range ackPacket["updates"].([]interface{}) {
                recmap := rec.(map[string]interface{})
                pk, _ := GenPK(uaid, recmap["channelID"].(string))
                err = self.mc.Delete(pk)
            }
        }
    }

    if err != nil {
        return err
    }
    return nil
}

func (self *Storage) ReloadData(uaid string, updates []string) (err error){
    //TODO: This is not really required.
    _, _ = uaid, updates
    return nil
}

