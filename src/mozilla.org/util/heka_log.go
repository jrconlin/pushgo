package util

import (
    "code.google.com/p/go-uuid/uuid"
    "github.com/mozilla-services/heka/client"
    "github.com/mozilla-services/heka/message"
    "log"
    "time"
    "os"
)



type HekaLogger struct {
    client client.Client
    encoder client.Encoder
    sender client.Sender
    logname string
    pid int32
    hostname string
}

const (
    CRITICAL = iota
    ERROR
    WARNING
    INFO
    DEBUG )

func NewHekaLogger(conf JsMap) *HekaLogger{
    //Preflight
    var ok bool
    var err error
    if _,ok = conf["heka_sender"]; !ok {
        conf["heka_sender"] = "tcp"
    }
    if _,ok = conf["heka_server_addr"]; !ok {
        conf["heka_server_addr"] = "127.0.0.1:5565"
    }
    if _,ok = conf["heka_logger_name"]; !ok {
        conf["heka_logger_name"] = "simplepush"
    }

    encoder := client.NewJsonEncoder(nil)
    sender, err := client.NewNetworkSender(conf["heka_sender"].(string),
                                          conf["heka_server_addr"].(string))
    if err != nil {
        log.Panic("Could not create sender ", err)
    }
    logname := conf["heka_logger_name"].(string)
    pid := int32(os.Getpid())
    hostname, err := os.Hostname()
    return &HekaLogger{encoder: encoder,
                       sender: sender,
                       logname: logname,
                       pid: pid,
                       hostname:hostname}
}

//TODO: Change the last arg to be something like fields ...interface{}
func (self HekaLogger) Log(level int32, mtype, payload string, fields JsMap) (err error) {

    var stream []byte

    msg := &message.Message{}
    msg.SetTimestamp(time.Now().UnixNano())
    msg.SetUuid(uuid.NewRandom())
    msg.SetLogger(self.logname)
    msg.SetType(mtype)
    msg.SetPid(self.pid)
    msg.SetSeverity(level)
    msg.SetHostname(self.hostname)
    if (len(payload) > 0) {
        msg.SetPayload(payload)
    }
    for key, ival := range fields {
        if ival == nil {
            continue
        }
        if key == "" {
            continue
        }
        field, err := message.NewField(key, ival, message.Field_RAW)
        if err != nil {
            log.Fatal("ERROR: Could not log field %s:%s (%s)", field,
                        ival.(string), err)
            return err
        }
        msg.AddField(field)
    }
    err = self.encoder.EncodeMessageStream(msg, &stream)
    if err != nil {
        log.Fatal("ERROR: Could not encode log message (%s)", err)
        return err
    }
    err = self.sender.SendMessage(stream)
    if err != nil {
        log.Fatal("ERROR: Could not send message (%s)", err)
        return err
    }
    log.Printf("[%d]% 7s: %s",level, mtype, payload)
    return nil
}

func (self HekaLogger) Info(mtype, msg string, fields JsMap) (err error){
    return self.Log(INFO, mtype, msg, fields)
}

func (self HekaLogger) Debug(mtype, msg string, fields JsMap) (err error){
    return self.Log(DEBUG, mtype, msg, fields)
}

func (self HekaLogger) Warn(mtype, msg string, fields JsMap) (err error){
    return self.Log(WARNING, mtype, msg, fields)
}

func (self HekaLogger) Error(mtype, msg string, fields JsMap) (err error){
    return self.Log(ERROR, mtype, msg, fields)
}

func (self HekaLogger) Critical(mtype, msg string, fields JsMap) (err error){
    return self.Log(CRITICAL, mtype, msg, fields)
}

