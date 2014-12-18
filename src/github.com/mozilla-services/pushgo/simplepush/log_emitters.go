/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"

	"github.com/mozilla-services/pushgo/id"
)

const TextLogTime = "2006-01-02 15:04:05 -0700"

var (
	idGenerateBytes = id.GenerateBytes
	osGetPid        = os.Getpid
	timeNow         = time.Now
)

// TryClose closes w, returning nil if w does not implement io.Closer.
func TryClose(w io.Writer) (err error) {
	if c, ok := w.(io.Closer); ok {
		err = c.Close()
	}
	return
}

// hekaMessagePool holds recycled Heka message objects and encoding buffers.
var hekaMessagePool = sync.Pool{New: func() interface{} {
	return &hekaMessage{
		header: new(Header),
		msg:    new(Message),
		buf:    proto.NewBuffer(nil),
	}
}}

func newHekaMessage() *hekaMessage {
	return hekaMessagePool.Get().(*hekaMessage)
}

type hekaMessage struct {
	header   *Header
	msg      *Message
	buf      *proto.Buffer
	outBytes []byte
}

func (hm *hekaMessage) free() {
	if cap(hm.outBytes) > 1024 {
		return
	}
	hm.buf.Reset()
	hm.outBytes = hm.outBytes[:0]
	hm.msg.Fields = nil
	hekaMessagePool.Put(hm)
}

func (hm *hekaMessage) marshalFrame() ([]byte, error) {
	msgSize := hm.msg.Size()
	if msgSize > MAX_MESSAGE_SIZE {
		return nil, fmt.Errorf("Message size %d exceeds maximum size %d",
			msgSize, MAX_MESSAGE_SIZE)
	}
	hm.header.SetMessageLength(uint32(msgSize))
	headerSize := hm.header.Size()
	if headerSize > MAX_HEADER_SIZE {
		return nil, fmt.Errorf("Header size %d exceeds maximum size %d",
			headerSize, MAX_HEADER_SIZE)
	}
	totalSize := headerSize + HEADER_FRAMING_SIZE + msgSize
	if cap(hm.outBytes) < totalSize {
		hm.outBytes = make([]byte, totalSize)
	} else {
		hm.outBytes = hm.outBytes[:totalSize]
	}
	hm.outBytes[0] = RECORD_SEPARATOR
	hm.outBytes[1] = byte(headerSize)
	hm.buf.SetBuf(hm.outBytes[HEADER_DELIMITER_SIZE:HEADER_DELIMITER_SIZE])
	if err := hm.buf.Marshal(hm.header); err != nil {
		return nil, err
	}
	hm.outBytes[headerSize+HEADER_DELIMITER_SIZE] = UNIT_SEPARATOR
	hm.buf.SetBuf(hm.outBytes[headerSize+HEADER_FRAMING_SIZE : headerSize+HEADER_FRAMING_SIZE])
	if err := hm.buf.Marshal(hm.msg); err != nil {
		return nil, err
	}
	return hm.outBytes, nil
}

// LogEmitter is the interface implemented by log message emitters.
type LogEmitter interface {
	Emit(level LogLevel, messageType, payload string, fields LogFields) error
	Close() error
}

// NewTextEmitter creates a human-readable log message emitter.
func NewTextEmitter(writer io.Writer) *TextEmitter {
	return &TextEmitter{Writer: writer}
}

// A TextEmitter emits human-readable log messages.
type TextEmitter struct {
	io.Writer
}

// Emit emits a text log message. Implements LogEmitter.Emit.
func (te *TextEmitter) Emit(level LogLevel, messageType, payload string,
	fields LogFields) (err error) {

	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, "%s [% 8s] %s %q", timeNow().Format(TextLogTime),
		level, messageType, payload)

	if len(fields) > 0 {
		buf.WriteByte(' ')
		i := 0
		names := make([]string, len(fields))
		for name := range fields {
			names[i] = fmt.Sprintf("%s:%s", name, fields[name])
			i++
		}
		sort.Strings(names)
		buf.WriteString(strings.Join(names, ", "))
	}

	buf.WriteByte('\n')
	_, err = buf.WriteTo(te.Writer)
	return
}

// Close closes the underlying write stream. Implements LogEmitter.Close.
func (te *TextEmitter) Close() error {
	return TryClose(te.Writer)
}

// NewJSONEmitter creates a JSON-encoded log message emitter.
func NewJSONEmitter(writer io.Writer, envVersion,
	hostname, loggerName string) *JSONEmitter {

	return &JSONEmitter{
		Writer:     writer,
		enc:        json.NewEncoder(writer),
		LogName:    loggerName,
		Pid:        int32(osGetPid()),
		EnvVersion: envVersion,
		Hostname:   hostname,
	}
}

// A JSONEmitter emits JSON-encoded log messages.
type JSONEmitter struct {
	io.Writer
	enc        *json.Encoder
	LogName    string
	Pid        int32
	EnvVersion string
	Hostname   string
}

// Emit encodes and sends a JSON log message. Implements LogEmitter.Emit.
func (je *JSONEmitter) Emit(level LogLevel, messageType, payload string,
	fields LogFields) (err error) {

	msgID, err := idGenerateBytes()
	if err != nil {
		return fmt.Errorf("Error generating JSON log message ID: %s", err)
	}

	hm := newHekaMessage()
	defer hm.free()

	hm.msg.SetID(msgID)
	hm.msg.SetTimestamp(timeNow().UnixNano())
	hm.msg.SetType(messageType)
	hm.msg.SetLogger(je.LogName)
	hm.msg.SetSeverity(int32(level))
	hm.msg.SetPayload(payload)
	hm.msg.SetEnvVersion(je.EnvVersion)
	hm.msg.SetPid(je.Pid)
	hm.msg.SetHostname(je.Hostname)
	for name, val := range fields {
		hm.msg.AddStringField(name, val)
	}
	hm.msg.SortFields()

	if err = je.enc.Encode(hm.msg); err != nil {
		return fmt.Errorf("Error sending JSON log message: %s", err)
	}
	return nil
}

// Close closes the underlying write stream. Implements LogEmitter.Close.
func (je *JSONEmitter) Close() error {
	return TryClose(je.Writer)
}

// NewProtobufEmitter creates a Protobuf-encoded Heka log message emitter.
// Protobuf encoding should be preferred over JSON for efficiency.
func NewProtobufEmitter(writer io.Writer, envVersion,
	hostname, loggerName string) *ProtobufEmitter {

	return &ProtobufEmitter{
		Writer:     writer,
		LogName:    loggerName,
		Pid:        int32(osGetPid()),
		EnvVersion: envVersion,
		Hostname:   hostname,
	}
}

// A ProtobufEmitter emits framed, Protobuf-encoded log messages.
type ProtobufEmitter struct {
	io.Writer
	LogName    string
	Pid        int32
	EnvVersion string
	Hostname   string
}

// Emit encodes and sends a framed log message. Implements LogEmitter.Emit.
func (pe *ProtobufEmitter) Emit(level LogLevel, messageType, payload string,
	fields LogFields) (err error) {

	msgID, err := idGenerateBytes()
	if err != nil {
		return fmt.Errorf("Error generating Protobuf log message ID: %s", err)
	}

	hm := newHekaMessage()
	defer hm.free()

	hm.msg.SetID(msgID)
	hm.msg.SetTimestamp(timeNow().UnixNano())
	hm.msg.SetType(messageType)
	hm.msg.SetLogger(pe.LogName)
	hm.msg.SetSeverity(int32(level))
	hm.msg.SetPayload(payload)
	hm.msg.SetEnvVersion(pe.EnvVersion)
	hm.msg.SetPid(pe.Pid)
	hm.msg.SetHostname(pe.Hostname)
	for name, val := range fields {
		hm.msg.AddStringField(name, val)
	}
	hm.msg.SortFields()

	outBytes, err := hm.marshalFrame()
	if err != nil {
		return fmt.Errorf("Error encoding Protobuf log message: %s", err)
	}
	if _, err = pe.Writer.Write(outBytes); err != nil {
		return fmt.Errorf("Error sending Protobuf log message: %s", err)
	}
	return nil
}

// Close closes the underlying write stream. Implements LogEmitter.Close.
func (pe *ProtobufEmitter) Close() error {
	return TryClose(pe.Writer)
}
