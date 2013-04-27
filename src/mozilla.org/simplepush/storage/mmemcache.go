package storage

// thin memcache wrapper

import (
    "github.com/bradfitz/gomemcache/memcache"

    "errors"
    "log"
    "fmt"
    "encoding/json"
    "sort"
    "strings"
    "strconv"
    "time"
)

const (
    DELETED = iota
    LIVE
    REGISTERED
)

var config map[string]string

type record map[string]interface{}

type Storage struct {
    config map[string]string
    mc *memcache.Client
}

func indexOf(list []string, val string) (index int) {
    for index, v := range list {
        if (v == val) {
            return index
        }
    }
    return -1
}

func resolvePK(pk string) (uaid, appid string) {
    items := strings.SplitN(pk, ".", 2)
    return items[0], items[1]
}

func genPK(uaid, appid string) (pk string){
    pk = fmt.Sprintf("%s.%s", uaid, appid)
    return pk
}

func (self *Storage) fetchRec(pk string) (result record, err error){
    result = nil
    if pk == "" {
        err = errors.New("Invalid Primary Key Value")
        return result, err
    }

    raw, err := self.mc.Get(pk)

    err = json.Unmarshal(raw.Value, result)
    if err == nil {
        return nil, err
    }

    return result, err
}

func (self *Storage) fetchAppIDArray(uaid string) (result []string, err error) {
    raw, err := self.mc.Get(uaid)
    if err != nil {
        return nil, err
    }
    result = strings.Split(",", string(raw.Value))
    return result, err
}

func (self *Storage) storeAppIDArray(uaid string, arr sort.StringSlice) (err error) {
    arr.Sort()
    err = self.mc.Set(&memcache.Item{Key: uaid,
        Value: []byte(strings.Join(arr, ","))})
    return err
}

func (self *Storage) storeRec(pk string, rec record) (err error) {
    if pk == "" {
        err = errors.New("Invalid Primary Key Value")
        return err
    }

    if rec == nil {
        err = errors.New("No data to store")
        return err
    }

    raw, err := json.Marshal(rec)

    if err != nil {
        return err
    }

    var ttls string
    switch rec["s"] {
        case DELETED:
            ttls = config["db.timeout_del"]
        case REGISTERED:
            ttls = config["db.timeout_reg"]
        default:
            ttls = config["db.timeout_live"]
    }
    rec["l"] = time.Now()

    ttl, err := strconv.ParseInt(ttls, 0, 0)
    err = self.mc.Set(&memcache.Item{Key: pk,
                 Value: []byte(raw),
                 Expiration: int32(ttl)})
    return err
}


func New(opts map[string]string) *Storage {

    config = opts
    var ok bool

    _, ok = config["memcache.servers"]
    if !ok {
        config["memcache.servers"] = "[\"localhost:11211\"]"
    }

    _, ok = config["db.timeout_live"]
    if !ok {
        config["db.timeout_live"] = "259200"
    }

    _, ok = config["db.timeout_reg"]
    if !ok {
        config["db.timeout_reg"] = "10800"
    }

    _, ok = config["db.timeout_del"]
    if !ok {
        config["db.timeout_del"] = "86400"
    }

    log.Printf("Creating new memcache handler")
    return &Storage{mc: memcache.New(config["memcache.servers"])}
}

func (self *Storage) UpdateChannel(pk, vers string) (err error) {
    var rec record

    if len(pk) == 0 {
        return errors.New("Invalid Primary Key Value")
    }

    rec, err = self.fetchRec(pk)

    if rec != nil {
       if rec["s"] != DELETED {
           newRecord := make(record)
           newRecord["v"] = vers
           newRecord["s"] = LIVE
           newRecord["l"] = time.Now()
           err := self.storeRec(pk, newRecord)
           return err
        }
    }
    // No record found or the record setting was DELETED
    uaid, appid := resolvePK(pk)
    return self.RegisterAppID(uaid, appid, vers)
}


func (self *Storage) RegisterAppID(uaid, appid, vers string) (err error) {

    var rec record

    appIDArray, err := self.fetchAppIDArray(uaid)
    // Yep, this should eventually be optimized to a faster scan.
    if appIDArray != nil {
        if indexOf(appIDArray, appid) >= 0 {
                return errors.New("Already registered")
        }
    }

    err = self.storeAppIDArray(uaid, append(appIDArray, appid))
    if err != nil {
        return err
    }

    rec = make(record)
    rec["s"] = REGISTERED
    rec["l"] = time.Now()
    if vers != "" {
        rec["v"] = vers
        rec["s"] = LIVE
    }

    return self.storeRec(genPK(uaid, appid), rec)
}

func (self *Storage) DeleteAppID(uaid, appid string, clearOnly bool) (err error) {

    appIDArray, err := self.fetchAppIDArray(uaid)
    pos := sort.SearchStrings(appIDArray, appid)
    if pos > -1 {
        self.storeAppIDArray(uaid, append(appIDArray[:pos], appIDArray[pos+1:]...))
        pk := genPK(uaid, appid)
        rec, err := self.fetchRec(pk)
        if err != nil {
            rec["s"] = DELETED
            err = self.storeRec(pk, rec)
        }
    }
    return err
}


func (self *Storage) GetUpdates(uaid string, lastAccessed int64) (results map[string]interface{}, err error) {
    appIDArray, err := self.fetchAppIDArray(uaid)

    var updates []map[string]interface{}
    var expired []string
    var items []string

    for _, appid := range appIDArray {
        items = append(items, genPK(uaid, appid))
    }

    recs, err := self.mc.GetMulti(items)
    if err != nil {
        return nil, err
    }

    var update record
    for _, rec := range recs {
        uaid, appid := resolvePK(rec.Key)
        err = json.Unmarshal(rec.Value, update)
        if err != nil {
            return nil, err
        }
        if update["l"].(int64) < lastAccessed {
            continue
        }
        if update["s"] == LIVE {
            newRec := make(map[string]interface{})
            newRec["channelID"] = appid
            newRec["version"] = update["v"]
            updates = append(updates, newRec)
        }
        if update["s"] == DELETED {
            expired = append(expired, uaid)
        }
    }
    results["expired"] = expired
    results["updates"] = updates
    return results, err
}

func (self *Storage) Ack(uaid string, ackPacket map[string]interface{}) (err error) {
    //TODO, go through the results and nuke what's there, then call flush

    if _, ok := ackPacket["expired"]; ok {
        expired := make([]string, strings.Count(ackPacket["expired"].(string), ",")+1)
        json.Unmarshal(ackPacket["expired"].([]byte), expired)
        for _, appid := range expired {
            err = self.mc.Delete(genPK(uaid, appid))
        }
    }
    if _, ok := ackPacket["updates"]; ok {
        type update map[string]interface{}
        rcnt := strings.Count(ackPacket["updates"].(string), "}") + 1
        updates := make([]update,rcnt)
        for _, rec := range updates {
            err = self.mc.Delete(genPK(uaid, rec["channelID"].(string)))
        }
    }

    return err
}

func (self *Storage) ReloadData(uaid string, updates []string) (err error){
    //TODO: This is not really required.
    _, _ = uaid, updates
    return nil
}

