package storage

// thin memcache wrapper

import (
    "github.com/bradfitz/gomemcache/memcache"

    "log"
    "fmt"
    "encoding/json"
    "strings"
    "time"
)

const (
    DELETED = iota
    LIVE
    REGISTERED
)

var mc memcache
var config map[string]string

type record map[string]interface{}

func indexOf(list []string, val string) (index int) {
    for index, v := range list {
        if (v == string) {
            return index
        }
    }
    return -1
}

func resolvePK(pk string) (uaid, appid string) {
    uaid, appid = strings.SplitN(pk, ".", 2)
    return uaid, appid
}

func genPK(uaid, appid string) (pk string){
    pk = fmt.Sprintf("%s.%s", uaid, appid
    return pk
}

func fetchRec(pk string) (result record, err error){
    result = nil
    if pk == nil {
        err = error("Invalid Primary Key Value")
        return result, err
    }

    raw, err := mc.Get(pk)

    err = json.NewDecoder(string(raw.Value)).Decoder(&result)
        if err == nil {
            return nil, err
        }
    }

    return result, err
}

func fetchAppIDArray(uaid string) (result []string, err error) {
    raw, err := mc.Get(uaid)
    if err != nil {
        return nil, err
    }
    result = strings.Split(",", raw)
    return result, err
}

func storeAppIDArray(uaid string, arr []string) (err error) {
    sorted := arr.Sort()
    err = mc.Set(Key: uaid,
        Value: []byte(sorted.Join(arr, ",")))
    return err
}

func storeRec(pk string, rec record) (err error) {
    if pk == nil {
        err = error("Invalid Primary Key Value")
        return err
    }

    if rec == nil {
        err = error("No data to store")
        return err
    }

    raw, err := json.Marshal(rec)

    if err != nil {
        return err
    }

    var ttls string
    switch rec["s"] {
        case DELETED:
            ttls := config["db.timeout_del"]
        case REGISTERED:
            ttls := config["db.timeout_reg"]
        default:
            ttls := config["db.timeout_live"]
    }
    rec["l"] = time.Now()

    ttl := int(ttls)
    err = mc.Set(Key: pk,
                 Value: []byte(raw),
                 Expiration: ttl)
    return err
}


func New(opts map[string]string) (err error) {

    type servers []string

    config = opts
    _, ok := config["memcache.servers"]
    if !ok {
        config["memcache.servers"] = "[\"localhost:11211\"]"
    }

    _, ok := config["db.timeout_live"]
    if !ok {
        config["db.timeout_live"] = "259200"
    }

    _, ok := config["db.timeout_reg"]
    if !ok {
        config["db.timeout_reg"] = "10800"
    }

    _, ok := config["db.timeout_del"]
    if !ok {
        config["db.timeout_del"] = "86400"
    }

    err := json.NewDecoder(val).Decode(&servers)
    if err != nil {
        log.Fatal(err)
    }

    mc = memCache.New(servers)
    return err
}

func UpdateChannel(pk, vers string) (err error) {
    var rec record

    if pk == nil || len(pk) == 0 {
        return error("Invalid Primary Key Value")
    }

    rec, err := fetchRec(pk)

    if rec != nil {
       if rec["s"] != DELETED {
          newRecord = make(map[string]string)
          newRecord["v"] = vers.(string)
          newRecord["s"] = LIVE
          newRecord["l"] = time.Now()
          err := storeRec(pk, newRecord)
          return err
        }
    }
    // No record found or the record setting was DELETED
    return RegisterAppID(pk, vers)
}


func RegisterAppID(pk, vers string) (err error) {

    var rec record

    uaid, appid := resolvePK(pk)
    appIDArray, err := fetchAppIDArray(uaid)
    // Yep, this should eventually be optimized to a faster scan.
    if appIDArray != nil {
        if indexOf(appIDArray, appid) >= 0 {
                return error("Already registered")
        }
    }

    err := storeAppIDArray(uaid, append(appIDArray, appid))
    if err != nil {
        return err
    }

    rec = make(record)
    rec["s"] = REGISTERED
    rec["l"] = time.Now()
    if vers != nil {
        rec["v"] = vers
        rec["s"] = LIVE
    }

    return storeRec(pk, rec)

}

func DeleteAppID(uaid, appid string, clearOnly bool) (err error) {

    appIDArray, err := fetchAppIDArray(uaid)
    pos := appIDArray.Search(appid)
    if pos > -1 {
        storeAppIDArray(uaid, append(appIDArray[:pos], appIDArray[pos+1:]))
        pk := genPK(uaid, appid)
        rec, err := fetchRec(pk)
        if err != nil {
            rec["s"] = DELETED
            err = storeRec(pk, rec)
        }
    }
    return err
}


func GetUpdates(uaid, lastAccessed int) (results map[string]interface{}, err error) {
    appIDArray, err := fetchAppIDArray(uaid)

    var keys []string
    var updates []map(string)interface{}
    var expired []string

    for p, appid := range appIDArray {
        keys := append(items, genPK(uaid, appid))
    }

    recs, err = mc.GetMulti(items)
    if err != nil {
        return nil, err
    }

    var update record
    for p, rec := range recs {
        uaid, appid := resolvePK(rec.Key)
        err = json.NewDecoder(string(rec.Value)).Decoder(&update)
        if err != nil {
            return err
        }
        if rec["l"] < last_accessed {
            continue
        }
        if rec["s"] == LIVE {
            newRec := make(map[string]interface{})
            newRec["channelID"] = appid
            newRec["version"] = rec["v"]
            updates = append(updates, newRec)
        }
        if rec["s"] == DELETED {
            expired = append(expired, uaid)
        }
    }
    results["expired"] = expired
    results["updates"] = updates
    return results, err
}

func Ack(uaid string, ackPacket map[string]interface{}) (err error) {
    //TODO, go through the results and nuke what's there, then call flush


    if _, ok := ackPacket["expired"]; ok {
        for p, appid := range ackPacket["expired"] {
            err = mc.Delete(genPK(uaid, appid)
        }
    }
    if _, ok = ackPacket["updates"]; ok {
        for p, update := range ackPacket["updates"] {
            err = mc.Delete(genPK(uaid, update.Key))
        }
    }

    return err
}

func ReloadData(uaid string, updates []string) (err error){
    //TODO: This is not really required.
    _, _ = uaid, updates
    return nil
}

