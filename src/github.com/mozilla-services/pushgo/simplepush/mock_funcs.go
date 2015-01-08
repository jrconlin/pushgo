/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/rand"
	"os"
	"time"

	"github.com/mozilla-services/pushgo/id"
)

// Global aliases to functions that rely on external state. This allows
// tests to override these functions with deterministic mocks.
var (
	cryptoRandRead  func([]byte) (int, error)
	idGenerate      func() (string, error)
	idGenerateBytes func() ([]byte, error)
	osGetPid        func() int
	timeNow         func() time.Time
)

// useStdFuncs sets the non-deterministic function aliases to their default
// values. This can be used by the tests to revert the mocks.
func useStdFuncs() {
	cryptoRandRead = rand.Read
	idGenerate = id.Generate
	idGenerateBytes = id.GenerateBytes
	osGetPid = os.Getpid
	timeNow = time.Now
}

func init() {
	useStdFuncs()
}
