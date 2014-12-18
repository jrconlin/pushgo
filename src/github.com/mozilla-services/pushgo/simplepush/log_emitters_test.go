/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/mozilla-services/pushgo/id"
)

func installMocks() {
	osGetPid = func() int { return 1234 }
	timeNow = func() time.Time { return time.Unix(1257894000, 0).UTC() }
	idGenerateBytes = func() ([]byte, error) {
		// d1c7c768-b1be-4c70-93a6-9b52910d4baa.
		return []byte{0xd1, 0xc7, 0xc7, 0x68, 0xb1, 0xbe, 0x4c, 0x70, 0x93,
			0xa6, 0x9b, 0x52, 0x91, 0x0d, 0x4b, 0xaa}, nil
	}
}

func revertMocks() {
	osGetPid = os.Getpid
	timeNow = time.Now
	idGenerateBytes = id.GenerateBytes
}

func BenchmarkNewMessage(b *testing.B) {
	for i := 0; i < b.N; i++ {
		msgID, err := idGenerateBytes()
		if err != nil {
			b.Fatalf("Error generating message ID: %s", err)
		}
		hm := newHekaMessage()
		hm.msg.SetID(msgID)
		hm.msg.SetTimestamp(time.Now().UnixNano())
		hm.msg.SetLogger("test-benchmark")
		hm.msg.SetSeverity(7)
		hm.msg.SetPayload("Hiya")
		hm.msg.SetEnvVersion("2")
		hm.msg.SetPid(1234)
		hm.msg.SetHostname("example.com")
		hm.msg.AddStringField("c", "d")
		hm.msg.AddStringField("a", "b")
		hm.msg.SortFields()
		if _, err := hm.marshalFrame(); err != nil {
			b.Fatalf("Error encoding log message: %s", err)
		}
		hm.free()
	}
}

func TestTextEmitter(t *testing.T) {
	installMocks()
	defer revertMocks()

	buf := new(bytes.Buffer)
	te := NewTextEmitter(buf)
	expected := []byte(
		"2009-11-10 23:00:00 +0000 [    INFO] test \"Howdy\" a:b, c:d, e:f\n")
	err := te.Emit(INFO, "test", "Howdy",
		LogFields{"c": "d", "a": "b", "e": "f"}) // Log fields should be sorted.
	if err != nil {
		t.Errorf("Error marshaling text log message: %s", err)
	}
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("Malformed text log message: got %#v; want %#v",
			buf.Bytes(), expected)
	}
}

func TestJSONEmitter(t *testing.T) {
	installMocks()
	defer revertMocks()

	buf := new(bytes.Buffer)
	je := NewJSONEmitter(buf, "2", "example.com", "test-json-emitter")
	expected := []byte(`{
		"uuid": "0cfHaLG+THCTpptSkQ1Lqg==",
		"timestamp": 1257894000000000000,
		"type": "test",
		"logger": "test-json-emitter",
		"severity": 6,
		"payload": "Howdy",
		"env_version": "2",
		"pid": 1234,
		"hostname": "example.com",
		"fields": [{
			"name": "a",
			"value_type": 0,
			"representation": "",
			"value_string": ["b"]
		}, {
			"name": "c",
			"value_type": 0,
			"representation": "",
			"value_string": ["d"]
		}, {
			"name": "e",
			"value_type": 0,
			"representation": "",
			"value_string": ["f"]
		}]
	}`)
	expectedBuf := new(bytes.Buffer)
	json.Compact(expectedBuf, expected)
	expectedBuf.WriteByte('\n')
	err := je.Emit(INFO, "test", "Howdy",
		LogFields{"c": "d", "a": "b", "e": "f"})
	if err != nil {
		t.Errorf("Error marshaling JSON log message: %s", err)
	}
	if !bytes.Equal(buf.Bytes(), expectedBuf.Bytes()) {
		t.Errorf("Malformed JSON log message: got %#v; want %#v",
			buf.Bytes(), expectedBuf.Bytes())
	}
}

func TestProtobufEmitter(t *testing.T) {
	installMocks()
	defer revertMocks()

	buf := new(bytes.Buffer)
	pe := NewProtobufEmitter(buf, "2", "example.com", "test-json-emitter")
	expected := []byte{
		0x1e, 0x2,
		// Header.
		0x8, 0x75,
		0x1f,
		// Message.
		0xa, 0x10, 0xd1, 0xc7, 0xc7, 0x68, 0xb1, 0xbe, 0x4c, 0x70, 0x93, 0xa6, 0x9b,
		0x52, 0x91, 0xd, 0x4b, 0xaa, 0x10, 0x80, 0xc0, 0xe1, 0xd8, 0xda, 0xfd, 0xbb,
		0xba, 0x11, 0x1a, 0x4, 0x74, 0x65, 0x73, 0x74, 0x22, 0x11, 0x74, 0x65, 0x73,
		0x74, 0x2d, 0x6a, 0x73, 0x6f, 0x6e, 0x2d, 0x65, 0x6d, 0x69, 0x74, 0x74, 0x65,
		0x72, 0x28, 0x6, 0x32, 0x5, 0x48, 0x6f, 0x77, 0x64, 0x79, 0x3a, 0x1, 0x32,
		0x40, 0xd2, 0x9, 0x4a, 0xb, 0x65, 0x78, 0x61, 0x6d, 0x70, 0x6c, 0x65, 0x2e,
		0x63, 0x6f, 0x6d, 0x52, 0xa, 0xa, 0x1, 0x61, 0x10, 0x0, 0x1a, 0x0, 0x22,
		0x1, 0x62, 0x52, 0xa, 0xa, 0x1, 0x63, 0x10, 0x0, 0x1a, 0x0, 0x22, 0x1, 0x64,
		0x52, 0xa, 0xa, 0x1, 0x65, 0x10, 0x0, 0x1a, 0x0, 0x22, 0x1, 0x66,
	}
	err := pe.Emit(INFO, "test", "Howdy",
		LogFields{"c": "d", "a": "b", "e": "f"})
	if err != nil {
		t.Errorf("Error marshaling framed log message: %s", err)
	}
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("Malformed framed log message: got %#v; want %#v",
			buf.Bytes(), expected)
	}
}
