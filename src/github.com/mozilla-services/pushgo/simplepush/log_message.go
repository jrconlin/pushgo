/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"sort"

	"github.com/mozilla-services/pushgo/id"
)

// Convenience functions and framing constants from Heka.

type SortedFields []*Field

func (sf SortedFields) Len() int      { return len(sf) }
func (sf SortedFields) Swap(i, j int) { sf[i], sf[j] = sf[j], sf[i] }

func (sf SortedFields) Less(i, j int) bool {
	return sf[i].Name != nil && sf[j].Name != nil && *sf[i].Name < *sf[j].Name
}

const (
	MAX_MESSAGE_SIZE      = 1 << 16
	MAX_HEADER_SIZE       = 1<<8 - 1
	HEADER_FRAMING_SIZE   = HEADER_DELIMITER_SIZE + 1 // unit separator
	RECORD_SEPARATOR      = byte(0x1e)
	HEADER_DELIMITER_SIZE = 2 // record separator + len
	UNIT_SEPARATOR        = byte(0x1f)
)

func (h *Header) SetMessageLength(v uint32) {
	if h.MessageLength == nil {
		h.MessageLength = new(uint32)
	}
	*h.MessageLength = v
}

func (m *Message) Identify() (err error) {
	return id.GenerateInto(&m.Uuid)
}

func (m *Message) SetTimestamp(v int64) {
	if m.Timestamp == nil {
		m.Timestamp = new(int64)
	}
	*m.Timestamp = v
}

func (m *Message) SetType(v string) {
	if m.Type == nil {
		m.Type = new(string)
	}
	*m.Type = v
}

func (m *Message) SetLogger(v string) {
	if m.Logger == nil {
		m.Logger = new(string)
	}
	*m.Logger = v
}

func (m *Message) SetSeverity(v int32) {
	if m.Severity == nil {
		m.Severity = new(int32)
	}
	*m.Severity = v
}

func (m *Message) SetPayload(v string) {
	if m.Payload == nil {
		m.Payload = new(string)
	}
	*m.Payload = v
}

func (m *Message) SetEnvVersion(v string) {
	if m.EnvVersion == nil {
		m.EnvVersion = new(string)
	}
	*m.EnvVersion = v
}

func (m *Message) SetPid(v int32) {
	if m.Pid == nil {
		m.Pid = new(int32)
	}
	*m.Pid = v
}

func (m *Message) SetHostname(v string) {
	if m.Hostname == nil {
		m.Hostname = new(string)
	}
	*m.Hostname = v
}

func (m *Message) SortFields() {
	sort.Sort(SortedFields(m.Fields))
}

func (m *Message) AddStringField(name, val string) {
	f := &Field{
		Name:           &name,
		ValueType:      new(Field_ValueType),
		Representation: new(string),
		ValueString:    []string{val},
	}
	*f.ValueType = Field_STRING
	m.Fields = append(m.Fields, f)
}
