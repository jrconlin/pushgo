/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"text/template"
	"time"
)

// testEndpointTemplate is a prepared push endpoint template.
var testEndpointTemplate = template.Must(template.New("Push").Parse(
	"{{.CurrentHost}}/{{.Token}}"))

// testID is a UUID string returned by idGenerate.
var testID = "d1c7c768b1be4c7093a69b52910d4baa"

// useMockFuncs replaces all non-deterministic functions with mocks: time is
// frozen, the process ID is fixed, and the UUID generation functions return
// predictable IDs. useMockFuncs is only exposed to tests.
func useMockFuncs() {
	cryptoRandRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0
		}
		return len(b), nil
	}
	idGenerate = func() (string, error) { return testID, nil }
	idGenerateBytes = func() ([]byte, error) {
		// d1c7c768-b1be-4c70-93a6-9b52910d4baa.
		return []byte{0xd1, 0xc7, 0xc7, 0x68, 0xb1, 0xbe, 0x4c, 0x70, 0x93,
			0xa6, 0x9b, 0x52, 0x91, 0x0d, 0x4b, 0xaa}, nil
	}
	osGetPid = func() int { return 1234 }
	timeNow = func() time.Time {
		// 2009-11-10 23:00:00 UTC; matches the Go Playground.
		return time.Unix(1257894000, 0).UTC()
	}
}
