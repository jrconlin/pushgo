/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"os"
	"time"

	"github.com/mozilla-services/pushgo/id"
)

var (
	idGenerate      func() (string, error)
	idGenerateBytes func() ([]byte, error)
	osGetPid        func() int
	timeNow         func() time.Time
)

var testID = "d1c7c768-b1be-4c70-93a6-9b52910d4baa"

func installMocks() {
	idGenerate = func() (string, error) { return testID, nil }
	idGenerateBytes = func() ([]byte, error) {
		// d1c7c768-b1be-4c70-93a6-9b52910d4baa.
		return []byte{0xd1, 0xc7, 0xc7, 0x68, 0xb1, 0xbe, 0x4c, 0x70, 0x93,
			0xa6, 0x9b, 0x52, 0x91, 0x0d, 0x4b, 0xaa}, nil
	}
	osGetPid = func() int { return 1234 }
	timeNow = func() time.Time { return time.Unix(1257894000, 0).UTC() }
}

func revertMocks() {
	idGenerate = id.Generate
	idGenerateBytes = id.GenerateBytes
	osGetPid = os.Getpid
	timeNow = time.Now
}

func init() {
	revertMocks()
}
