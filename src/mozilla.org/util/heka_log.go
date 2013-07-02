/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package util

import (
	"code.google.com/p/go-uuid/uuid"
	"github.com/mozilla-services/heka/client"
	"github.com/mozilla-services/heka/message"

	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"time"
)

type HekaLogger struct {
	client   client.Client
	encoder  client.Encoder
	sender   client.Sender
	logname  string
	pid      int32
	hostname string
	conf     JsMap
}

const (
	CRITICAL = iota
	ERROR
	WARNING
	INFO
	DEBUG
)

func NewHekaLogger(conf JsMap) *HekaLogger {
	//Preflight
	var ok bool
	var encoder client.Encoder
	var sender client.Sender
	var logname string
	var err error
	pid := int32(os.Getpid())
	encoder = nil
	sender = nil
	logname = ""

	if _, ok = conf["heka.sender"]; !ok {
		conf["heka.sender"] = "tcp"
	}
	if _, ok = conf["heka.server_addr"]; !ok {
		conf["heka.server_addr"] = "127.0.0.1:5565"
	}
	if _, ok = conf["heka.logger_name"]; !ok {
		conf["heka.logger_name"] = "simplepush"
	}
	if _, ok = conf["heka.current_host"]; !ok {
		conf["heka.current_host"], _ = os.Hostname()
	}
	if MzGetFlag(conf, "heka.use") {
		encoder = client.NewJsonEncoder(nil)
		sender, err = client.NewNetworkSender(conf["heka.sender"].(string),
			conf["heka.server_addr"].(string))
		if err != nil {
			log.Panic("Could not create sender ", err)
		}
		logname = conf["heka.logger_name"].(string)
	}
	return &HekaLogger{encoder: encoder,
		sender:   sender,
		logname:  logname,
		pid:      pid,
		hostname: conf["heka.current_host"].(string),
		conf:     conf}
}

//TODO: Change the last arg to be something like fields ...interface{}
func (self HekaLogger) Log(level int32, mtype, payload string, fields JsMap) (err error) {

	var base_level int64

	base_level, _ = strconv.ParseInt(MzGet(self.conf, "log.filter", "10"), 10, 0)

	if int(level) <= int(base_level) {
		if len(fields) > 0 {
			log.Printf("[%d]% 7s: %s %s", level, mtype, payload, fields)
		} else {
			log.Printf("[%d]% 7s: %s", level, mtype, payload)
		}
	}

	// Don't send an error if there's nothing to do
	if self.sender == nil {
		return nil
	}

	var stream []byte

	msg := &message.Message{}
	msg.SetTimestamp(time.Now().UnixNano())
	msg.SetUuid(uuid.NewRandom())
	msg.SetLogger(self.logname)
	msg.SetType(mtype)
	msg.SetPid(self.pid)
	msg.SetSeverity(level)
	msg.SetHostname(self.hostname)
	if len(payload) > 0 {
		msg.SetPayload(payload)
	}
	for key, ival := range fields {
        var field *message.Field
        var err error
		if ival == nil {
			continue
		}
		if key == "" {
			continue
		}
        field, err = message.NewField(key, ival, message.Field_RAW)
        if err != nil {
            field, err = message.NewField(key, fmt.Sprintf("%s", ival), message.Field_RAW)
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
	return nil
}

func (self HekaLogger) Info(mtype, msg string, fields JsMap) (err error) {
	return self.Log(INFO, mtype, msg, fields)
}

func (self HekaLogger) Debug(mtype, msg string, fields JsMap) (err error) {
	return self.Log(DEBUG, mtype, msg, fields)
}

func (self HekaLogger) Warn(mtype, msg string, fields JsMap) (err error) {
	return self.Log(WARNING, mtype, msg, fields)
}

func (self HekaLogger) Error(mtype, msg string, fields JsMap) (err error) {
	return self.Log(ERROR, mtype, msg, fields)
}

func (self HekaLogger) Critical(mtype, msg string, fields JsMap) (err error) {
	debug.PrintStack()
	return self.Log(CRITICAL, mtype, msg, fields)
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
