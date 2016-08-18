/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mozilla-services/pushgo/id"
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
	CommonLogTime = "02/Jan/2006:15:04:05 -0700"
)

type LogFields map[string]string

type Logger interface {
	Log(level LogLevel, messageType, payload string, fields LogFields) error
	SetFilter(level LogLevel)
	ShouldLog(level LogLevel) bool
	Close() error
}

var AvailableLoggers = make(AvailableExtensions)

type SimpleLogger struct {
	Logger
}

type LoggerConfig interface {
	Open() (io.Writer, error)
	GetName() string
	GetEnvVersion() string
}

var logEmitters = map[string]func(*Application, LoggerConfig) (
	LogEmitter, error){

	"json": func(app *Application, conf LoggerConfig) (LogEmitter, error) {
		w, err := conf.Open()
		if err != nil {
			return nil, err
		}
		hostname := app.Hostname()
		loggerName := fmt.Sprintf("%s-%s", conf.GetName(), VERSION)
		return NewJSONEmitter(w, conf.GetEnvVersion(),
			hostname, loggerName), nil
	},
	"protobuf": func(app *Application, conf LoggerConfig) (LogEmitter, error) {
		w, err := conf.Open()
		if err != nil {
			return nil, err
		}
		hostname := app.Hostname()
		loggerName := fmt.Sprintf("%s-%s", conf.GetName(), VERSION)
		return NewProtobufEmitter(w, conf.GetEnvVersion(),
			hostname, loggerName), nil
	},
	"text": func(_ *Application, conf LoggerConfig) (LogEmitter, error) {
		w, err := conf.Open()
		if err != nil {
			return nil, err
		}
		return NewTextEmitter(w), nil
	},
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

func (sl *SimpleLogger) Notice(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(NOTICE, mtype, msg, fields)
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

func (sl *SimpleLogger) Alert(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(ALERT, mtype, msg, fields)
}

func (sl *SimpleLogger) Panic(mtype, msg string, fields LogFields) error {
	return sl.Logger.Log(EMERGENCY, mtype, msg, fields)
}

type NetworkLoggerConfig struct {
	Format     string
	Proto      string
	Addr       string
	UseTLS     bool   `toml:"use_tls" env:"use_tls"`
	EnvVersion string `toml:"env_version" env:"env_version"`
	Name       string `toml:"name" env:"name"`
	Filter     int32
}

func (conf *NetworkLoggerConfig) Open() (io.Writer, error) {
	if len(conf.Addr) == 0 {
		return nil, fmt.Errorf("Missing remote host")
	}
	if conf.UseTLS {
		conn, err := tls.Dial(conf.Proto, conf.Addr, nil)
		if err != nil {
			return nil, fmt.Errorf("Error dialing TLS host %q: %s",
				conf.Addr, err)
		}
		return conn, nil
	}
	conn, err := net.Dial(conf.Proto, conf.Addr)
	if err != nil {
		return nil, fmt.Errorf("Error dialing host %q: %s", conf.Addr, err)
	}
	return conn, nil
}

func (conf *NetworkLoggerConfig) GetName() string { return conf.Name }

func (conf *NetworkLoggerConfig) GetEnvVersion() string {
	return conf.EnvVersion
}

// A NetworkLogger sends log messages to a remote Heka instance over TCP,
// UDP, or a Unix domain socket.
type NetworkLogger struct {
	LogEmitter
	filter LogLevel
}

func (nl *NetworkLogger) ConfigStruct() interface{} {
	return &NetworkLoggerConfig{
		Format:     "protobuf",
		Proto:      "tcp",
		EnvVersion: "2",
		Name:       "pushgo",
		Filter:     0,
	}
}

func (nl *NetworkLogger) Init(app *Application, config interface{}) (err error) {
	conf := config.(*NetworkLoggerConfig)
	f, ok := logEmitters[conf.Format]
	if !ok {
		return fmt.Errorf("NetworkLogger: Unrecognized log format %q",
			conf.Format)
	}
	if nl.LogEmitter, err = f(app, conf); err != nil {
		return fmt.Errorf("NetworkLogger: %s", err)
	}
	nl.filter = LogLevel(conf.Filter)
	return nil
}

func (nl *NetworkLogger) ShouldLog(level LogLevel) bool {
	return level <= nl.filter
}

func (nl *NetworkLogger) SetFilter(level LogLevel) {
	nl.filter = level
}

func (nl *NetworkLogger) Log(level LogLevel, messageType, payload string, fields LogFields) (err error) {
	if !nl.ShouldLog(level) {
		return
	}
	return nl.Emit(level, messageType, payload, fields)
}

type FileLoggerConfig struct {
	Format     string
	Path       string
	EnvVersion string `toml:"env_version" env:"env_version"`
	Name       string `toml:"name" env:"name"`
	Filter     int32
}

func (conf *FileLoggerConfig) Open() (io.Writer, error) {
	if len(conf.Path) == 0 {
		return nil, fmt.Errorf("Missing log file path")
	}
	logFile, err := os.OpenFile(conf.Path,
		os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return nil, fmt.Errorf("Error opening log file %q: %s",
			conf.Path, err)
	}
	return logFile, nil
}

func (conf *FileLoggerConfig) GetName() string { return conf.Name }

func (conf *FileLoggerConfig) GetEnvVersion() string {
	return conf.EnvVersion
}

// A FileLogger writes log messages to a file.
type FileLogger struct {
	LogEmitter
	filter LogLevel
}

func (fl *FileLogger) ConfigStruct() interface{} {
	return &FileLoggerConfig{
		Format:     "protobuf",
		EnvVersion: "2",
		Filter:     0,
		Name:       "pushgo",
	}
}

func (fl *FileLogger) Init(app *Application, config interface{}) (err error) {
	conf := config.(*FileLoggerConfig)
	f, ok := logEmitters[conf.Format]
	if !ok {
		return fmt.Errorf("FileLogger: Unrecognized log format %q",
			conf.Format)
	}
	if fl.LogEmitter, err = f(app, conf); err != nil {
		return fmt.Errorf("FileLogger: %s", err)
	}
	fl.filter = LogLevel(conf.Filter)
	return nil
}

func (fl *FileLogger) ShouldLog(level LogLevel) bool {
	return level <= fl.filter
}

func (fl *FileLogger) SetFilter(level LogLevel) {
	fl.filter = level
}

func (fl *FileLogger) Log(level LogLevel, messageType, payload string, fields LogFields) (err error) {
	if !fl.ShouldLog(level) {
		return
	}
	return fl.Emit(level, messageType, payload, fields)
}

type StdOutLoggerConfig struct {
	Format     string
	EnvVersion string `toml:"env_version" env:"env_version"`
	Filter     int32
	Name       string `toml:"name" env:"name"`
}

func (*StdOutLoggerConfig) Open() (io.Writer, error) {
	return writerOnly{os.Stdout}, nil
}

func (conf *StdOutLoggerConfig) GetName() string { return conf.Name }

func (conf *StdOutLoggerConfig) GetEnvVersion() string {
	return conf.EnvVersion
}

// StdOutLogger writes log messages to standard output.
type StdOutLogger struct {
	LogEmitter
	filter LogLevel
}

func (ml *StdOutLogger) ConfigStruct() interface{} {
	return &StdOutLoggerConfig{
		Format:     "protobuf",
		EnvVersion: "2",
		Filter:     0,
		Name:       "pushgo",
	}
}

func (ml *StdOutLogger) Init(app *Application, config interface{}) (err error) {
	conf := config.(*StdOutLoggerConfig)
	f, ok := logEmitters[conf.Format]
	if !ok {
		return fmt.Errorf("StdOutLogger: Unrecognized log format %q",
			conf.Format)
	}
	if ml.LogEmitter, err = f(app, conf); err != nil {
		return fmt.Errorf("StdOutLogger: %s", err)
	}
	ml.filter = LogLevel(conf.Filter)
	return nil
}

func (ml *StdOutLogger) ShouldLog(level LogLevel) bool {
	return level <= ml.filter
}

func (ml *StdOutLogger) SetFilter(level LogLevel) {
	ml.filter = level
}

func (ml *StdOutLogger) Log(level LogLevel, messageType, payload string, fields LogFields) (err error) {
	// Return ASAP if we shouldn't be logging
	if !ml.ShouldLog(level) {
		return
	}
	if err = ml.Emit(level, messageType, payload, fields); err != nil {
		log.Fatal(err)
	}
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

// writerOnly hides the optional ReadFrom and Close methods of an io.Writer.
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
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, io.EOF
	}
	if !w.wroteHeader {
		w.wroteHeader = true
		w.setStatus(http.StatusSwitchingProtocols)
	}
	return hj.Hijack()
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
	http.Handler
	Log *SimpleLogger
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
	forwardedFor := req.Header[http.CanonicalHeaderKey("X-Forwarded-For")]
	remoteAddrs := make([]string, len(forwardedFor)+1)
	copy(remoteAddrs, forwardedFor)
	remoteAddrs[len(remoteAddrs)-1], _, _ = net.SplitHostPort(req.RemoteAddr)
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

// TestLogger is used by the go test functions.

type TBLoggingInterface interface {
	Logf(string, ...interface{})
}

type TestLogger struct {
	filter LogLevel
	t      TBLoggingInterface
}

func (r *TestLogger) Init(app *Application, config interface{}) (err error) {
	return nil
}

func (r *TestLogger) ShouldLog(level LogLevel) bool {
	return level <= r.filter
}
func (r *TestLogger) SetFilter(level LogLevel) {
	r.filter = level
}

func (r *TestLogger) ConfigStruct() interface{} {
	return struct{}{}
}

func (r *TestLogger) Close() error { return nil }

func (r *TestLogger) Log(level LogLevel, mType, payload string, fields LogFields) (err error) {
	r.t.Logf("[% 8s] %s:%s %+v", levelNames[level], mType, payload, fields)
	return nil
}

// LogWriter implements the io.Writer interface for log messages. Defined for
// compatibility with package log.
type LogWriter struct {
	Logger
	Name  string
	Level LogLevel
}

// Write implements io.Writer.Write. Each write corresponds to a single log
// message.
func (lw *LogWriter) Write(payload []byte) (written int, err error) {
	if err = lw.Log(lw.Level, lw.Name, string(payload), nil); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func init() {
	AvailableLoggers["stdout"] = func() HasConfigStruct { return new(StdOutLogger) }
	AvailableLoggers["net"] = func() HasConfigStruct { return new(NetworkLogger) }
	AvailableLoggers["file"] = func() HasConfigStruct { return new(FileLogger) }
	AvailableLoggers.SetDefault("stdout")
}
