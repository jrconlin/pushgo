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
    "net/http"
)

const (
    DELETED = iota
    LIVE
    REGISTERED
)

var config JsMap


type Result struct {
    Success bool
    Err error
    Status int
}

type JsMap map[string]interface{}

type Storage struct {
    config JsMap
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

func ResolvePK(pk string) (uaid, appid string) {
    items := strings.SplitN(pk, ".", 2)
    if len(items) < 2 {
        return pk, ""
    }
    return items[0], items[1]
}

func GenPK(uaid, appid string) (pk string){
    pk = fmt.Sprintf("%s.%s", uaid, appid)
    return pk
}

func (self *Storage) fetchRec(pk string) (result JsMap, err error){
    result = nil
    if pk == "" {
        err = errors.New("Invalid Primary Key Value")
        return result, err
    }

    defer func () {
        if err := recover(); err != nil {
            log.Printf("ERROR: could not fetch record for %s: %s", pk, err)
        }
    }()

    item, err := self.mc.Get(pk)

    log.Printf("DEBUG: Fetch item:: %s, item.Value: %s", item, item.Value)

    json.Unmarshal(item.Value, &result)

    if result == nil {
        return nil, err
    }

    log.Printf("%s => %s", pk, result)
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

func (self *Storage) storeRec(pk string, rec JsMap) (err error) {
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
            ttls = config["db.timeout_del"].(string)
        case REGISTERED:
            ttls = config["db.timeout_reg"].(string)
        default:
            ttls = config["db.timeout_live"].(string)
    }
    rec["l"] = time.Now().UTC().Unix()

    ttl, err := strconv.ParseInt(ttls, 0, 0)
    log.Printf("INFO: Storing record %s => %s", pk, raw)
    err = self.mc.Set(&memcache.Item{Key: pk,
                 Value: []byte(raw),
                 Expiration: int32(ttl)})
    return err
}


func New(opts JsMap) *Storage {

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

    log.Printf("INFO: Creating new memcache handler")
    // (...)(strings.Split(config["memcache.servers"].(string), ","))
    return &Storage{mc: memcache.New(config["memcache.server"].(string))}
}

func (self *Storage) UpdateChannel(pk, vers string) (res *Result) {
    var rec JsMap

    if len(pk) == 0 {
        return &Result{
            Success: false,
            Err: errors.New("Invalid Primary Key Value"),
            Status: http.StatusServiceUnavailable}
    }

    rec, err := self.fetchRec(pk)

    if err != nil {
        return &Result {
            Success: false,
            Err: errors.New(fmt.Sprintf("Cannot fetch record %s", err.Error())),
            Status: http.StatusServiceUnavailable }
    }


    if rec != nil {
        log.Printf("DEBUG: Found record for %s", pk)
        if rec["s"] != DELETED {
            newRecord := make(JsMap)
            newRecord["v"] = vers
            newRecord["s"] = LIVE
            newRecord["l"] = time.Now().UTC().Unix()
            err := self.storeRec(pk, newRecord)
            if err != nil {
                return &Result {
                    Success: false,
                    Err: err,
                    Status: http.StatusServiceUnavailable }
            }
            return &Result{Success: true, Err: nil, Status: 200}
        }
    }
    // No record found or the record setting was DELETED
    uaid, appid := ResolvePK(pk)
    return self.RegisterAppID(uaid, appid, vers)
}


func (self *Storage) RegisterAppID(uaid, appid, vers string) (res *Result) {

    var rec JsMap

    if len(appid) == 0 {
        return &Result {
            Success: false,
            Err: errors.New("No Channel Specified"),
            Status: http.StatusServiceUnavailable }
    }

    appIDArray, err := self.fetchAppIDArray(uaid)
    // Yep, this should eventually be optimized to a faster scan.
    if appIDArray != nil {
        if indexOf(appIDArray, appid) >= 0 {
                return &Result {
                    Success: false,
                    Err: errors.New("Already registered"),
                    Status: http.StatusServiceUnavailable }
        }
    }

    err = self.storeAppIDArray(uaid, append(appIDArray, appid))
    if err != nil {
        return &Result{
            Success: false,
            Err: err,
            Status: http.StatusServiceUnavailable }
    }

    rec = make(JsMap)
    rec["s"] = REGISTERED
    rec["l"] = time.Now().UTC().Unix()
    if vers != "" {
        rec["v"] = vers
        rec["s"] = LIVE
    }

    err = self.storeRec(GenPK(uaid, appid), rec)
    if (err != nil) {
        return &Result {
            Success: false,
            Err: err,
            Status: http.StatusServiceUnavailable}
    }
    return &Result {
        Success: true,
        Err: err,
        Status: http.StatusOK}
}

func (self *Storage) DeleteAppID(uaid, appid string, clearOnly bool) (res *Result) {

    appIDArray, err := self.fetchAppIDArray(uaid)
    pos := sort.SearchStrings(appIDArray, appid)
    if pos > -1 {
        self.storeAppIDArray(uaid, append(appIDArray[:pos], appIDArray[pos+1:]...))
        pk := GenPK(uaid, appid)
        rec, err := self.fetchRec(pk)
        if err != nil {
            rec["s"] = DELETED
            err = self.storeRec(pk, rec)
        }
    }
    if err != nil {
        return &Result {
            Success: false,
            Err: err,
            Status: http.StatusServiceUnavailable }
        }
    return &Result {
        Success: true,
        Err: nil,
        Status: http.StatusOK }
}


func (self *Storage) GetUpdates(uaid string, lastAccessed int64) (results JsMap, err error) {
    appIDArray, err := self.fetchAppIDArray(uaid)

    var updates []map[string]interface{}
    var expired []string
    var items []string

    for _, appid := range appIDArray {
        items = append(items, GenPK(uaid, appid))
    }
    log.Printf("Fetching items %s", items)

    recs, err := self.mc.GetMulti(items)
    if err != nil {
        log.Printf("ERROR: %s", err)
        return nil, err
    }

    var update JsMap
    if len(recs) == 0 {
        log.Printf("INFO: No records found for %s", uaid)
    }
    for _, rec := range recs {
        uaid, appid := ResolvePK(rec.Key)
        log.Printf("INFO: Fetched %s record %s", uaid, rec.Value)
        err = json.Unmarshal(rec.Value, &update)
        if err != nil {
            return nil, err
        }
        if int64(update["l"].(float64)) < lastAccessed {
            log.Printf("Skipping record...")
            continue
        }
        // Yay! Go translates numeric interface values as float64s
        // Apparently float64(1) != int(1).
        switch update["s"] {
        case float64(LIVE):
            log.Printf("INFO: Adding record... %s", appid)
            newRec := make(JsMap)
            newRec["channelID"] = appid
            newRec["version"] = update["v"]
            updates = append(updates, newRec)
        case float64(DELETED):
            log.Printf("INFO: Deleting record... %s", appid)
            expired = append(expired, appid)
        default:
            log.Printf("INFO: UNknown state %d", update["s"])
        }

    }
    results = make(JsMap)
    results["expired"] = expired
    results["updates"] = updates
    return results, err
}

func (self *Storage) Ack(uaid string, ackPacket map[string]interface{}) (res *Result) {
    //TODO, go through the results and nuke what's there, then call flush

    var err error

    if _, ok := ackPacket["expired"]; ok {
        expired := make([]string, strings.Count(ackPacket["expired"].(string), ",")+1)
        json.Unmarshal(ackPacket["expired"].([]byte), &expired)
        for _, appid := range expired {
            err = self.mc.Delete(GenPK(uaid, appid))
        }
    }
    if _, ok := ackPacket["updates"]; ok {
        type update map[string]interface{}
        rcnt := strings.Count(ackPacket["updates"].(string), "}") + 1
        updates := make([]update,rcnt)
        for _, rec := range updates {
            err = self.mc.Delete(GenPK(uaid, rec["channelID"].(string)))
        }
    }

    if err != nil {
        return &Result {
            Success: false,
            Err: err,
            Status: http.StatusServiceUnavailable }
    }
    return &Result {
        Success: true,
        Err: nil,
        Status: http.StatusOK }
}

func (self *Storage) ReloadData(uaid string, updates []string) (err error){
    //TODO: This is not really required.
    _, _ = uaid, updates
    return nil
}

