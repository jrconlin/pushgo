/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/mozilla-services/pushgo/id"
)

// GenerateIdSize generates a hex-encoded string of the specified decoded size,
// with an optional prefix.
func GenerateIdSize(prefix string, size int) (result string, err error) {
	bytes := make([]byte, size)
	if _, err = io.ReadFull(rand.Reader, bytes); err != nil {
		return
	}
	results := make([]byte, len(prefix)+hex.EncodedLen(len(bytes)))
	copy(results[:len(prefix)], prefix)
	hex.Encode(results[len(prefix):], bytes)
	return string(results), nil
}

// MustGenerateIds returns a slice containing the specified number of random
// IDs, panicking if an error occurs. This simplifies generating random data
// for running smoke tests.
func MustGenerateIds(size int) []string {
	results := make([]string, size)
	for index := range results {
		bytes, err := id.GenerateBytes()
		if err == nil {
			results[index], err = id.Encode(bytes)
		}
		if err != nil {
			panic(fmt.Sprintf("GenerateIds: Error generating ID: %s", err))
		}
	}
	return results
}
