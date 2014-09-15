/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mozilla-services/pushgo/id"
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

const (
	HeaderID      = "X-Request-Id"
	CommonLogTime = "02/Jan/2006:15:04:05 -0700"
)

type LogFields map[string]string

type Logger interface {
	HasConfigStruct
	Log(level LogLevel, messageType, payload string, fields LogFields)
	SetFilter(level LogLevel)
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
	return level <= ml.filter
}

func (ml *StdOutLogger) SetFilter(level LogLevel) {
	ml.filter = level
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
		dump = fmt.Sprintf("%s {%s}", dump, strings.Join(fld, ", "))
	}
	if len(caller) > 0 {
		dump = fmt.Sprintf("%s [%s:%s %s]", dump, caller["file"],
			caller["line"], caller["name"])
	}
	log.Printf(dump)
	return
}

// logResponseWriter wraps an http.ResponseWriter, recording its status code,
// content length, and response time.
type logResponseWriter struct {
	http.ResponseWriter
	wroteHeader   bool
	StatusCode    int
	ContentLength int
	RespondedAt   time.Time
}

// writerOnly hides the optional ReadFrom method of an io.Writer from io.Copy.
// Taken from package net/http, copyright 2009, The Go Authors.
type writerOnly struct {
	io.Writer
}

// ReadFrom calls the ReadFrom method of the underlying ResponseWriter. Defined
// for compatibility with *response.ReadFrom from package net/http.
func (w *logResponseWriter) ReadFrom(reader io.Reader) (written int64, err error) {
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(reader)
	}
	return io.Copy(writerOnly{w}, reader)
}

// WriteHeader records the response time and status code, then calls the
// WriteHeader method of the underlying ResponseWriter.
func (w *logResponseWriter) WriteHeader(statusCode int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.setStatus(statusCode)
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

// setStatus records the response time and status code.
func (w *logResponseWriter) setStatus(statusCode int) {
	w.RespondedAt = time.Now()
	w.StatusCode = statusCode
}

// Write calls the Write method of the underlying ResponseWriter and records
// the number of bytes written.
func (w *logResponseWriter) Write(bytes []byte) (written int, err error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	written, err = w.ResponseWriter.Write(bytes)
	w.ContentLength += len(bytes)
	return
}

// Hijack calls the Hijack method of the underlying ResponseWriter, allowing a
// custom protocol handler (e.g., WebSockets) to take over the connection.
func (w *logResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, io.EOF
}

// CloseNotify calls the CloseNotify method of the underlying ResponseWriter,
// or returns a nil channel if the operation is not supported.
func (w *logResponseWriter) CloseNotify() <-chan bool {
	if notifier, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	return nil
}

// Flush calls the Flush method of the underlying ResponseWriter, recording a
// successful response if WriteHeader was not called.
func (w *logResponseWriter) Flush() {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.setStatus(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// LogHandler logs the result of an HTTP request.
type LogHandler struct {
	Logger *SimpleLogger
	http.Handler
	TrustProxy bool
}

// formatRequest generates a Common Log Format request line.
func (h *LogHandler) formatRequest(writer *logResponseWriter, request *http.Request, remoteAddrs []string) string {
	remoteAddr := "-"
	if len(remoteAddrs) > 0 {
		remoteAddr = remoteAddrs[0]
	}
	requestInfo := fmt.Sprintf("%s %s %s", request.Method, request.URL.Path, request.Proto)
	return fmt.Sprintf(`%s - - [%s] %s %d %d`, remoteAddr, writer.RespondedAt.Format(CommonLogTime),
		strconv.Quote(requestInfo), writer.StatusCode, writer.ContentLength)
}

// logResponse logs a response to an HTTP request.
func (h *LogHandler) logResponse(writer *logResponseWriter, request *http.Request, requestID string, receivedAt time.Time) {
	if !h.Logger.ShouldLog(INFO) {
		return
	}
	var remoteAddrs []string
	if h.TrustProxy {
		remoteAddrs = append(remoteAddrs, request.Header[http.CanonicalHeaderKey("X-Forwarded-For")]...)
	}
	remoteAddr, _, _ := net.SplitHostPort(request.RemoteAddr)
	remoteAddrs = append(remoteAddrs, remoteAddr)
	h.Logger.Info("http", h.formatRequest(writer, request, remoteAddrs), LogFields{
		"rid":                requestID,
		"agent":              request.Header.Get("User-Agent"),
		"path":               request.URL.Path,
		"method":             request.Method,
		"code":               strconv.Itoa(writer.StatusCode),
		"remoteAddressChain": fmt.Sprintf("[%s]", strings.Join(remoteAddrs, ", ")),
		"t":                  strconv.FormatInt(int64(writer.RespondedAt.Sub(receivedAt)/time.Millisecond), 10)})
}

// ServeHTTP implements http.Handler.ServeHTTP.
func (h *LogHandler) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	receivedAt := time.Now()

	// The `X-Request-Id` header is used by Heroku, restify, etc. to correlate
	// logs for the same request.
	requestID := request.Header.Get(HeaderID)
	if !id.Valid(requestID) {
		requestID, _ = id.Generate()
		request.Header.Set(HeaderID, requestID)
	}

	writer := &logResponseWriter{ResponseWriter: responseWriter, StatusCode: http.StatusOK}
	defer h.logResponse(writer, request, requestID, receivedAt)

	h.Handler.ServeHTTP(writer, request)
}

func init() {
	AvailableLoggers["stdout"] = func() HasConfigStruct { return new(StdOutLogger) }
	AvailableLoggers["default"] = AvailableLoggers["stdout"]
}
