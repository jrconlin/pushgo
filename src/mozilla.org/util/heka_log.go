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
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
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
	tracer   bool
	filter   int64
}

// Message levels
const (
	CRITICAL = iota
	ERROR
	WARNING
	INFO
	DEBUG
)

// The fields to relay. NOTE: object reflection is VERY CPU expensive.
// I specify strings here to reduce that as much as possible. Please do
// not change this to something like map[string]interface{} since that
// can dramatically increase server load.
type Fields map[string]string

// Create a new Heka logging interface.
func NewHekaLogger(conf JsMap) *HekaLogger {
	//Preflight
	var ok bool
	var encoder client.Encoder = nil
	var sender client.Sender = nil
	var logname string = ""
	var err error
	var tracer bool = false
	var filter int64

	pid := int32(os.Getpid())

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
	if _, ok = conf["heka.show_caller"]; ok {
		tracer, _ = strconv.ParseBool(conf["heka.show_caller"].(string))
	}
    if _, ok = conf["heka.encoder"]; !ok {
        conf["heka.encoder"] = "protobuf"
    }
	filter, _ = strconv.ParseInt(MzGet(conf, "logger.filter", "10"), 0, 0)
	if MzGetFlag(conf, "heka.use") {
        // Yeah, so for now there's just the two. Someday there may be more
        encoder = client.NewProtobufEncoder(nil)
        // Options: NewJsonEncoder; NewProtobufEncoder
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
		conf:     conf,
		tracer:   tracer,
		filter:   filter}
}

// Fields are additional logging data passed to Heka. They are technically
// undefined, but searchable and actionable.
func addFields(msg *message.Message, fields Fields) (err error) {
	for key, ival := range fields {
		var field *message.Field
		if ival == "" {
			ival = "*empty*"
		}
		if key == "" {
			continue
		}
		field, err = message.NewField(key, ival, ival)
		if err != nil {
			return err
		}
		msg.AddField(field)
	}
	return err
}

// Logging workhorse function. Chances are you're not going to call this
// directly, but via one of the helper methods. of Info() .. Critical()
// level - One of the defined logging CONST values
// mtype - Message type, Short class identifier for the message
// payload - Main error message
// fields - additional optional key/value data associated with the message.
// dash - force message to be logged (it's for the dashboard)
func (self HekaLogger) Log(level int32, mtype, payload string, fields Fields) (err error) {

	var caller Fields
	// add in go language tracing. (Also CPU intensive, but REALLY helpful
	// when dev/debugging)
	if self.tracer {
		if pc, file, line, ok := runtime.Caller(2); ok {
			funk := runtime.FuncForPC(pc)
			caller = Fields{
				"file": file,
				"line": strconv.FormatInt(int64(line), 0),
				"name": funk.Name()}
		}
	}

	// Only print out the debug message if it's for the dashboard or
    // less than the filter.
	if (strings.ToLower(mtype) == "dash") || (int64(level) < self.filter) {
		dump := fmt.Sprintf("[%d]% 7s: %s", level, mtype, payload)
		if len(fields) > 0 {
			var fld []string
			for key, val := range fields {
				fld = append(fld, key+": "+val)
			}
			dump += " {" + strings.Join(fld, ", ") + "}"
		}
		if len(caller) > 0 {
			dump += fmt.Sprintf(" [%s:%d %s]", caller["file"],
				caller["line"], caller["name"])
		}
		log.Printf(dump)

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
		err = addFields(msg, fields)
		if err != nil {
			return err
		}
		err = addFields(msg, caller)
		if err != nil {
			return err
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
	}
	return nil
}

// record the lowest priority message
func (self HekaLogger) Info(mtype, msg string, fields Fields) (err error) {
	return self.Log(INFO, mtype, msg, fields)
}

func (self HekaLogger) Debug(mtype, msg string, fields Fields) (err error) {
	return self.Log(DEBUG, mtype, msg, fields)
}

func (self HekaLogger) Warn(mtype, msg string, fields Fields) (err error) {
	return self.Log(WARNING, mtype, msg, fields)
}

func (self HekaLogger) Error(mtype, msg string, fields Fields) (err error) {
	return self.Log(ERROR, mtype, msg, fields)
}

// record the Highest priority message, and include a printstack to STDERR
func (self HekaLogger) Critical(mtype, msg string, fields Fields) (err error) {
	debug.PrintStack()
	return self.Log(CRITICAL, mtype, msg, fields)
}


// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
