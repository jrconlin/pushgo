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
	"strconv"
	"sync"
	"time"

	"github.com/mozilla-services/heka/client"
	"github.com/mozilla-services/heka/message"
	"github.com/mozilla-services/pushgo/id"
)

const TextLogTime = "2006-01-02 15:04:05 -0700"

// hekaMessagePool holds recycled Heka message objects and encoding buffers.
var hekaMessagePool = sync.Pool{New: func() interface{} {
	return &hekaMessage{msg: new(message.Message)}
}}

func newHekaMessage() *hekaMessage {
	return hekaMessagePool.Get().(*hekaMessage)
}

type hekaMessage struct {
	msg      *message.Message
	outBytes []byte
}

func (hm *hekaMessage) free() {
	if cap(hm.outBytes) > 1024 {
		return
	}
	hm.outBytes = hm.outBytes[:0]
	hm.msg.Fields = nil
	hekaMessagePool.Put(hm)
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

	reply := new(bytes.Buffer)
	fmt.Fprintf(reply, "%s [% 8s] %s %s", time.Now().Format(TextLogTime),
		level, messageType, strconv.Quote(payload))

	if len(fields) > 0 {
		reply.WriteByte(' ')
		names := fields.Names()
		for i, name := range names {
			fmt.Fprintf(reply, "%s:%s", name, fields[name])
			if i < len(names)-1 {
				reply.WriteString(", ")
			}
		}
	}
	reply.WriteByte('\n')
	_, err = reply.WriteTo(te.Writer)
	return
}

// Close closes the underlying write stream. Implements LogEmitter.Close.
func (te *TextEmitter) Close() (err error) {
	if c, ok := te.Writer.(io.Closer); ok {
		err = c.Close()
	}
	return
}

// NewJSONEmitter creates a JSON-encoded log message emitter.
func NewJSONEmitter(sender client.Sender, envVersion,
	hostname, loggerName string) *HekaEmitter {

	return &HekaEmitter{
		Sender:        sender,
		StreamEncoder: hekaJSONEncoder,
		LogName:       fmt.Sprintf("%s-%s", loggerName, VERSION),
		Pid:           int32(os.Getpid()),
		EnvVersion:    envVersion,
		Hostname:      hostname,
	}
}

// NewProtobufEmitter creates a Protobuf-encoded Heka log message emitter.
// Protobuf encoding should be preferred over JSON for efficiency.
func NewProtobufEmitter(sender client.Sender, envVersion,
	hostname, loggerName string) *HekaEmitter {

	return &HekaEmitter{
		Sender:        sender,
		StreamEncoder: client.NewProtobufEncoder(nil),
		LogName:       fmt.Sprintf("%s-%s", loggerName, VERSION),
		Pid:           int32(os.Getpid()),
		EnvVersion:    envVersion,
		Hostname:      hostname,
	}
}

// A HekaEmitter emits encoded Heka messages.
type HekaEmitter struct {
	client.Sender
	client.StreamEncoder
	LogName    string
	Pid        int32
	EnvVersion string
	Hostname   string
}

// Emit encodes and sends a Heka log message. Implements LogEmitter.Emit.
func (he *HekaEmitter) Emit(level LogLevel, messageType, payload string,
	fields LogFields) (err error) {

	hm := newHekaMessage()
	defer hm.free()

	messageID, _ := id.GenerateBytes()
	hm.msg.SetUuid(messageID)
	hm.msg.SetTimestamp(time.Now().UnixNano())
	hm.msg.SetType(messageType)
	hm.msg.SetLogger(he.LogName)
	hm.msg.SetSeverity(int32(level))
	hm.msg.SetPayload(payload)
	hm.msg.SetEnvVersion(he.EnvVersion)
	hm.msg.SetPid(he.Pid)
	hm.msg.SetHostname(he.Hostname)
	for _, name := range fields.Names() {
		message.NewStringField(hm.msg, name, fields[name])
	}

	if err = he.StreamEncoder.EncodeMessageStream(hm.msg, &hm.outBytes); err != nil {
		return fmt.Errorf("Error encoding log message: %s", err)
	}
	if err = he.Sender.SendMessage(hm.outBytes); err != nil {
		return fmt.Errorf("Error sending log message: %s", err)
	}
	return nil
}

// Close closes the underlying sender. Implements LogEmitter.Close.
func (he *HekaEmitter) Close() error {
	he.Sender.Close()
	return nil
}

// A HekaSender writes serialized Heka messages to an underlying writer.
type HekaSender struct {
	io.Writer
}

// SendMessage writes a serialized Heka message. Implements
// client.Sender.SendMessage.
func (s *HekaSender) SendMessage(data []byte) (err error) {
	_, err = s.Writer.Write(data)
	return
}

// Close closes the underlying writer. Implements client.Sender.Close.
func (s *HekaSender) Close() {
	if c, ok := s.Writer.(io.Closer); ok {
		c.Close()
	}
}

// A HekaJSONEncoder encodes Heka messages to JSON.
type HekaJSONEncoder struct{}

// EncodeMessage returns the encoded form of m.
func (j HekaJSONEncoder) EncodeMessage(m *message.Message) ([]byte, error) {
	return json.Marshal(m)
}

// EncodeMessageStream appends the encoded form of m to dest.
func (j HekaJSONEncoder) EncodeMessageStream(m *message.Message,
	dest *[]byte) (err error) {

	if *dest, err = j.EncodeMessage(m); err != nil {
		return
	}
	*dest = append(*dest, '\n')
	return
}

var hekaJSONEncoder = HekaJSONEncoder{}
