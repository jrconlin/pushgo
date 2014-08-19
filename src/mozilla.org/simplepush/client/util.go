/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

var ErrInvalidId = &ClientError{"Invalid UUID."}

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

// GenerateId generates a 16-byte hex-encoded string. This string is suitable
// for use as a unique device or channel ID, but is not a UUID; the reserved
// and version bits are not set.
func GenerateId() (string, error) {
	return GenerateIdSize("", 16)
}

// MustGenerateIds returns a slice containing the specified number of random
// IDs, panicking if an error occurs. This simplifies generating random data
// for running smoke tests.
func MustGenerateIds(size int) []string {
	var err error
	results := make([]string, size)
	for index := range results {
		if results[index], err = GenerateId(); err != nil {
			panic(fmt.Sprintf("GenerateIds: Error generating ID: %s", err))
		}
	}
	return results
}

// ValidId ensures that the given string is a valid UUID. Both the 32-byte
// (unhyphenated) and 36-byte (hyphenated) formats are accepted.
func ValidId(id string) bool {
	if !validIdLen(id) {
		return false
	}
	for index := 0; index < len(id); index++ {
		if !validIdRuneAt(id, index) {
			return false
		}
	}
	return true
}

// DecodeId decodes a UUID string into the given slice, returning an error if
// the ID is malformed, and panicking if the destination slice is too small.
func DecodeId(id string, destination []byte) (err error) {
	if !validIdLen(id) {
		return ErrInvalidId
	}
	source := make([]byte, 32)
	sourceIndex := 0
	for index := 0; index < len(id); index++ {
		if !validIdRuneAt(id, index) {
			return ErrInvalidId
		}
		source[sourceIndex] = id[index]
		sourceIndex++
	}
	_, err = hex.Decode(destination, source)
	return
}

func validIdLen(id string) bool {
	return len(id) == 32 || (len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-')
}

func validIdRuneAt(id string, index int) bool {
	r := id[index]
	if len(id) == 36 && (index == 8 || index == 13 || index == 18 || index == 23) {
		return r == '-'
	}
	if r >= 'A' && r <= 'F' {
		r += 'a' - 'A'
	}
	return r >= 'a' && r <= 'f' || r >= '0' && r <= '9'
}
