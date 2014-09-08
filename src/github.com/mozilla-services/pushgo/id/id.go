/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package id

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrInvalid = errors.New("Invalid ID")

// GenerateBytes generates a decoded UUID byte slice.
func GenerateBytes() (bytes []byte, err error) {
	bytes = make([]byte, 16)
	if _, err = rand.Read(bytes); err != nil {
		return nil, err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return bytes, nil
}

// Generate generates a non-hyphenated, hex-encoded UUID string.
func Generate() (string, error) {
	bytes, err := GenerateBytes()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// Encode converts a UUID into a hyphenated, hex-encoded string.
func Encode(bytes []byte) (string, error) {
	if len(bytes) != 16 {
		return "", ErrInvalid
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}

// Valid ensures that the given string is a valid UUID. Both the 32-byte
// (unhyphenated) and 36-byte (hyphenated) formats are accepted.
func Valid(id string) bool {
	if len(id) != 32 && !(len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-') {
		return false
	}
	for index := 0; index < len(id); index++ {
		b := id[index]
		if len(id) == 36 && (index == 8 || index == 13 || index == 18 || index == 23) {
			if b == '-' {
				continue
			}
			return false
		}
		if b >= 'A' && b <= 'F' {
			b += 'a' - 'A'
		}
		if (b < 'a' || b > 'f') && (b < '0' || b > '9') {
			return false
		}
	}
	return true
}

// Decode decodes a UUID string into the given slice, returning an error if
// the ID is malformed, and panicking if the destination slice is too small.
func Decode(id string, destination []byte) (err error) {
	if !Valid(id) {
		return ErrInvalid
	}
	source := make([]byte, 32)
	sourceIndex := 0
	for index := 0; index < len(id); index++ {
		if id[index] == '-' {
			continue
		}
		source[sourceIndex] = id[index]
		sourceIndex++
	}
	_, err = hex.Decode(destination, source)
	return
}

// DecodeString decodes a UUID string, returning a byte slice with the result.
func DecodeString(id string) ([]byte, error) {
	bytes := make([]byte, 16)
	if err := Decode(id, bytes); err != nil {
		return nil, err
	}
	return bytes, nil
}

// MustGenerate returns a slice containing the specified number of random
// IDs, panicking if an error occurs. This simplifies generating random data
// for running smoke tests.
func MustGenerate(count int) []string {
	results := make([]string, count)
	for index := range results {
		bytes, err := GenerateBytes()
		if err != nil {
			panic(fmt.Sprintf("MustGenerate: Error generating ID: %s", err))
		}
		results[index], _ = Encode(bytes)
	}
	return results
}
