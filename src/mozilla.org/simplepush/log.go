/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Log levels
type LogLevel int32

const (
	CRITICAL LogLevel = iota
	ERROR
	WARNING
	INFO
	DEBUG
)

type LogFields map[string]string

type Logger interface {
	HasConfigStruct
	Log(level LogLevel, messageType, payload string, fields LogFields)
	ShouldLog(level LogLevel) bool
}

var AvailableLoggers = make(AvailableExtensions)

type SimpleLogger struct {
	Logger
}

// Error string helper that ignores nil errors
func ErrStr(err error) string {
	if err == nil {
		return ""
	} else {
		return err.Error()
	}
}

// Attempt to convert an interface to a string, ignore failures
func IStr(i interface{}) (reply string) {
	if i == nil {
		return ""
	}
	if reply, ok := i.(string); ok {
		return reply
	}
	return "Undefined"
}

// SimplePush Logger implementation, utilizes the passed in Logger
func NewLogger(log Logger) (*SimpleLogger, error) {
	return &SimpleLogger{log}, nil
}

// Default logging calls for convenience
func (sl *SimpleLogger) Info(mtype, msg string, fields LogFields) {
	sl.Logger.Log(INFO, mtype, msg, fields)
}

func (sl *SimpleLogger) Debug(mtype, msg string, fields LogFields) {
	sl.Logger.Log(DEBUG, mtype, msg, fields)
}

func (sl *SimpleLogger) Warn(mtype, msg string, fields LogFields) {
	sl.Logger.Log(WARNING, mtype, msg, fields)
}

func (sl *SimpleLogger) Error(mtype, msg string, fields LogFields) {
	sl.Logger.Log(ERROR, mtype, msg, fields)
}

func (sl *SimpleLogger) Critical(mtype, msg string, fields LogFields) {
	sl.Logger.Log(CRITICAL, mtype, msg, fields)
}

// Standard output logger implementation

type StdOutLoggerConfig struct {
	Filter int32
	Trace  bool
}

type StdOutLogger struct {
	logname  string
	pid      int32
	hostname string
	tracer   bool
	filter   LogLevel
}

func (ml *StdOutLogger) ConfigStruct() interface{} {
	return &StdOutLoggerConfig{
		Filter: 0,
		Trace:  false,
	}
}

func (ml *StdOutLogger) Init(app *Application, config interface{}) (err error) {
	conf := config.(*StdOutLoggerConfig)
	ml.pid = int32(os.Getpid())
	ml.hostname = app.Hostname()
	ml.tracer = conf.Trace
	ml.filter = LogLevel(conf.Filter)
	return
}

func (ml *StdOutLogger) ShouldLog(level LogLevel) bool {
	return level < ml.filter
}

func (ml *StdOutLogger) Log(level LogLevel, messageType, payload string, fields LogFields) {
	// Return ASAP if we shouldn't be logging
	if !ml.ShouldLog(level) {
		return
	}

	var caller LogFields
	// add in go language tracing. (Also CPU intensive, but REALLY helpful
	// when dev/debugging)
	if ml.tracer {
		if pc, file, line, ok := runtime.Caller(2); ok {
			funk := runtime.FuncForPC(pc)
			caller = LogFields{
				"file": file,
				// defaults don't appear to work.: file,
				"line": strconv.FormatInt(int64(line), 0),
				"name": funk.Name()}
		}
	}

	dump := fmt.Sprintf("[%d]% 7s: %s", level, messageType, payload)
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
	return
}

func init() {
	AvailableLoggers["stdout"] = func() HasConfigStruct { return new(StdOutLogger) }
}
