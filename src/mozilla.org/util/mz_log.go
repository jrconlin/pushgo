/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package util

import (
    "errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

type MzLogger struct {
	logname  string
	pid      int32
	hostname string
	conf     *MzConfig
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
func NewMzLogger(conf *MzConfig) *MzLogger {
	//Preflight
	var logname string = ""
	var tracer bool = false
	var filter int64

	pid := int32(os.Getpid())

	dhost, _ := os.Hostname()
	conf.SetDefaultFlag("heka.show_caller", false)
	conf.SetDefault("logger.filter", "10")
	filter, _ = strconv.ParseInt(conf.Get("logger.filter", "10"), 0, 0)
	return &MzLogger{logname: logname,
		pid:      pid,
		hostname: conf.Get("heka.current_host", dhost),
		conf:     conf,
		tracer:   tracer,
		filter:   filter}
}

// Logging workhorse function. Chances are you're not going to call this
// directly, but via one of the helper methods. of Info() .. Critical()
// level - One of the defined logging CONST values
// mtype - Message type, Short class identifier for the message
// payload - Main error message
// fields - additional optional key/value data associated with the message.
func (self *MzLogger) Log(level int32, mtype, payload string, fields Fields) (err error) {

	var caller Fields
	// add in go language tracing. (Also CPU intensive, but REALLY helpful
	// when dev/debugging)
	if self.tracer {
		if pc, file, line, ok := runtime.Caller(2); ok {
			funk := runtime.FuncForPC(pc)
			caller = Fields{
				"file": file,
				// defaults don't appear to work.: file,
				"line": strconv.FormatInt(int64(line), 0),
				"name": funk.Name()}
		}
	}

	// Only print out the debug message if it's less than the filter.
	if int64(level) < self.filter {
		dump := fmt.Sprintf("[%d]% 7s: %s", level, mtype, payload)
		if len(fields) > 0 {
			var fld []string
			for key, val := range fields {
				fld = append(fld, key+": "+val)
			}
			dump += " {" + strings.Join(fld, ", ") + "}"
		}
		if len(caller) > 0 {
			dump += fmt.Sprintf(" [%s:%s %s]", caller["file"],
				caller["line"], caller["name"])
		}
		log.Printf(dump)

	}
	return nil
}

// record the lowest priority message
func (self *MzLogger) Info(mtype, msg string, fields Fields) (err error) {
	return self.Log(INFO, mtype, msg, fields)
}

func (self *MzLogger) Debug(mtype, msg string, fields Fields) (err error) {
	return self.Log(DEBUG, mtype, msg, fields)
}

func (self *MzLogger) Warn(mtype, msg string, fields Fields) (err error) {
	return self.Log(WARNING, mtype, msg, fields)
}

func (self *MzLogger) Error(mtype, msg string, fields Fields) (err error) {
	return self.Log(ERROR, mtype, msg, fields)
}

// record the Highest priority message, and include a printstack to STDERR
func (self *MzLogger) Critical(mtype, msg string, fields Fields) (err error) {
	return self.Log(CRITICAL, mtype, msg, fields)
	debug.PrintStack()
    log.Fatal("Fatal condition encountered")
    return errors.New("Critical Error")
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
