/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"code.google.com/p/goprotobuf/proto"
	"github.com/mozilla-services/heka/message"
	"github.com/mozilla-services/pushgo/id"
)

// A HekaFormat specifies the Heka log message format. The binary Protobuf
// format is more efficient, and should be preferred over the JSON encoding
// when human readability is not a concern.
type HekaFormat int

// Heka message formats.
const (
	HekaProtobuf HekaFormat = iota
	HekaJSON
)

// A LogLevel represents a message log level.
type LogLevel int32

// syslog severity levels.
const (
	EMERGENCY LogLevel = iota
	ALERT
	CRITICAL
	ERROR
	WARNING
	NOTICE
	INFO
	DEBUG
)

var levelNames = map[LogLevel]string{
	EMERGENCY: "EMERGENCY",
	ALERT:     "ALERT",
	CRITICAL:  "CRITICAL",
	ERROR:     "ERROR",
	WARNING:   "WARNING",
	NOTICE:    "NOTICE",
	INFO:      "INFO",
	DEBUG:     "DEBUG",
}

func (l LogLevel) String() string {
	return levelNames[l]
}

const (
	HeaderID      = "X-Request-Id"
	TextLogTime   = "2006-01-02 15:04:05 -0700"
	CommonLogTime = "02/Jan/2006:15:04:05 -0700"
)

type LogFields map[string]string

type Logger interface {
	HasConfigStruct
	Log(level LogLevel, messageType, payload string, fields LogFields) error
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
	}
	return err.Error()
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
func (sl *SimpleLogger) Debug(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(DEBUG, mtype, msg, fields)
}

func (sl *SimpleLogger) Info(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(INFO, mtype, msg, fields)
}

func (sl *SimpleLogger) Warn(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(WARNING, mtype, msg, fields)
}

func (sl *SimpleLogger) Error(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(ERROR, mtype, msg, fields)
}

func (sl *SimpleLogger) Critical(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(CRITICAL, mtype, msg, fields)
}

// NewHekaLogger creates a logger that writes Protobuf or JSON-encoded log
// messages to standard output.
func NewHekaLogger(format HekaFormat) *HekaLogger {
	return &HekaLogger{format: format, writer: os.Stdout}
}

type HekaLoggerConfig struct {
	Name       string
	EnvVersion string `toml:"env_version"`
	Filter     int32
}

type HekaLogger struct {
	pid        int32
	logName    string
	envVersion string
	hostname   string
	filter     LogLevel
	format     HekaFormat
	writer     io.Writer
}

func (hl *HekaLogger) ConfigStruct() interface{} {
	return &HekaLoggerConfig{
		Name:       fmt.Sprintf("push-%s", VERSION),
		EnvVersion: "1",
		Filter:     0,
	}
}

func (hl *HekaLogger) Init(app *Application, config interface{}) (err error) {
	conf := config.(*HekaLoggerConfig)
	hl.pid = int32(os.Getpid())
	hl.hostname = app.Hostname()
	hl.logName = conf.Name
	hl.envVersion = conf.EnvVersion
	hl.filter = LogLevel(conf.Filter)
	return
}

func (hl *HekaLogger) ShouldLog(level LogLevel) bool {
	return level <= hl.filter
}

func (hl *HekaLogger) SetFilter(level LogLevel) {
	hl.filter = level
}

func (hl *HekaLogger) writeMessage(m *message.Message) (err error) {
	switch hl.format {
	case HekaProtobuf:
		var data []byte
		if data, err = proto.Marshal(m); err != nil {
			return
		}
		_, err = hl.writer.Write(data)

	case HekaJSON:
		encoder := json.NewEncoder(hl.writer)
		err = encoder.Encode(m)
	}
	return
}

func (hl *HekaLogger) Log(level LogLevel, messageType, payload string, fields LogFields) (err error) {
	if !hl.ShouldLog(level) {
		return
	}
	messageID, _ := id.GenerateBytes()
	m := new(message.Message)
	m.SetUuid(messageID)
	m.SetTimestamp(time.Now().UnixNano())
	m.SetType(messageType)
	m.SetLogger(hl.logName)
	m.SetSeverity(int32(level))
	m.SetPayload(payload)
	m.SetEnvVersion(hl.envVersion)
	m.SetPid(hl.pid)
	m.SetHostname(hl.hostname)
	for name, value := range fields {
		message.NewStringField(m, name, value)
	}
	return hl.writeMessage(m)
}

// NewTextLogger creates a logger that writes human-readable log messages to
// standard error.
func NewTextLogger() *TextLogger {
	return &TextLogger{writer: os.Stderr}
}

type TextLoggerConfig struct {
	Filter int32
	Trace  bool
}

type TextLogger struct {
	pid      int32
	hostname string
	trace    bool
	filter   LogLevel
	writer   io.Writer
}

func (tl *TextLogger) ConfigStruct() interface{} {
	return &TextLoggerConfig{
		Filter: 0,
		Trace:  false,
	}
}

func (tl *TextLogger) Init(app *Application, config interface{}) (err error) {
	conf := config.(*TextLoggerConfig)
	tl.pid = int32(os.Getpid())
	tl.hostname = app.Hostname()
	tl.trace = conf.Trace
	tl.filter = LogLevel(conf.Filter)
	return
}

func (tl *TextLogger) ShouldLog(level LogLevel) bool {
	return level <= tl.filter
}

func (tl *TextLogger) SetFilter(level LogLevel) {
	tl.filter = level
}

func (tl *TextLogger) Log(level LogLevel, messageType, payload string, fields LogFields) (err error) {
	// Return ASAP if we shouldn't be logging
	if !tl.ShouldLog(level) {
		return
	}

	var (
		file, funcName string
		line           int
		hasTrace       bool
	)
	// add in go language tracing. (Also CPU intensive, but REALLY helpful
	// when dev/debugging)
	if tl.trace {
		var pc uintptr
		if pc, file, line, hasTrace = runtime.Caller(2); hasTrace {
			if funk := runtime.FuncForPC(pc); funk != nil {
				funcName = funk.Name()
			}
		}
	}

	reply := new(bytes.Buffer)
	fmt.Fprintf(reply, "%s [% 8s] %s %s", time.Now().Format(TextLogTime),
		level, messageType, strconv.Quote(payload))

	if len(fields) > 0 {
		reply.WriteByte(' ')
		i := 0
		for key, val := range fields {
			fmt.Fprintf(reply, "%s:%s", key, val)
			if i < len(fields)-1 {
				reply.WriteString(", ")
			}
			i++
		}
	}
	if hasTrace {
		reply.WriteByte(' ')
		fmt.Fprintf(reply, "[%s:%d %s]", file, line, funcName)
	}

	reply.WriteByte('\n')
	_, err = reply.WriteTo(tl.writer)

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
func (w *logResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if dest, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return dest.ReadFrom(r)
	}
	return io.Copy(writerOnly{w}, r)
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
func (w *logResponseWriter) Write(data []byte) (n int, err error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err = w.ResponseWriter.Write(data)
	w.ContentLength += len(data)
	return
}

// Hijack calls the Hijack method of the underlying ResponseWriter, allowing a
// custom protocol handler (e.g., WebSockets) to take over the connection.
func (w *logResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, io.EOF
}

// CloseNotify calls the CloseNotify method of the underlying ResponseWriter,
// or returns a nil channel if the operation is not supported.
func (w *logResponseWriter) CloseNotify() <-chan bool {
	if cn, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return cn.CloseNotify()
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
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// LogHandler logs the result of an HTTP request.
type LogHandler struct {
	Log *SimpleLogger
	http.Handler
	TrustProxy bool
}

// formatRequest generates a Common Log Format request line.
func (h *LogHandler) formatRequest(writer *logResponseWriter, req *http.Request, remoteAddrs []string) string {
	remoteAddr := "-"
	if len(remoteAddrs) > 0 {
		remoteAddr = remoteAddrs[0]
	}
	requestInfo := fmt.Sprintf("%s %s %s", req.Method, req.URL.Path, req.Proto)
	return fmt.Sprintf(`%s - - [%s] %s %d %d`, remoteAddr, writer.RespondedAt.Format(CommonLogTime),
		strconv.Quote(requestInfo), writer.StatusCode, writer.ContentLength)
}

// logResponse logs a response to an HTTP request.
func (h *LogHandler) logResponse(writer *logResponseWriter, req *http.Request, requestID string, receivedAt time.Time) {
	if !h.Log.ShouldLog(INFO) {
		return
	}
	var remoteAddrs []string
	if h.TrustProxy {
		remoteAddrs = append(remoteAddrs, req.Header[http.CanonicalHeaderKey("X-Forwarded-For")]...)
	}
	remoteAddr, _, _ := net.SplitHostPort(req.RemoteAddr)
	remoteAddrs = append(remoteAddrs, remoteAddr)
	h.Log.Info("http", h.formatRequest(writer, req, remoteAddrs), LogFields{
		"rid":                requestID,
		"agent":              req.Header.Get("User-Agent"),
		"path":               req.URL.Path,
		"method":             req.Method,
		"code":               strconv.Itoa(writer.StatusCode),
		"remoteAddressChain": fmt.Sprintf("[%s]", strings.Join(remoteAddrs, ", ")),
		"t":                  strconv.FormatInt(int64(writer.RespondedAt.Sub(receivedAt)/time.Millisecond), 10)})
}

// ServeHTTP implements http.Handler.ServeHTTP.
func (h *LogHandler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	receivedAt := time.Now()

	// The `X-Request-Id` header is used by Heroku, restify, etc. to correlate
	// logs for the same request.
	requestID := req.Header.Get(HeaderID)
	if !id.Valid(requestID) {
		requestID, _ = id.Generate()
		req.Header.Set(HeaderID, requestID)
	}

	writer := &logResponseWriter{ResponseWriter: res, StatusCode: http.StatusOK}
	defer h.logResponse(writer, req, requestID, receivedAt)

	h.Handler.ServeHTTP(writer, req)
}

func init() {
	AvailableLoggers["protobuf"] = func() HasConfigStruct { return NewHekaLogger(HekaProtobuf) }
	AvailableLoggers["json"] = func() HasConfigStruct { return NewHekaLogger(HekaJSON) }
	AvailableLoggers["text"] = func() HasConfigStruct { return NewTextLogger() }
	AvailableLoggers["default"] = AvailableLoggers["text"]
}
