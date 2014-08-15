/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

// very light weight, unformatted UUID4 generator/parser. Because sometimes
// you just need a reasonably unique hash
// taken from http://www.ashishbanerjee.com/home/go/go-generate-uuid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

func GenUUID4() (string, error) {
	// Generate a non-hyphenated UUID4 string.
	// (this makes for smaller URLs)
	uuid := make([]byte, 16)
	n, err := rand.Read(uuid)
	if n != len(uuid) || err != nil {
		return "", err
	}

	uuid[8] = 0x80
	uuid[4] = 0x40

	return hex.EncodeToString(uuid), nil
}

func ScanUUID(uuids string) ([]byte, error) {
	// scan a UUID and return it as byte array
	trimmed := strings.TrimSpace(strings.Replace(uuids, "-", "", -1))
	return hex.DecodeString(trimmed)
}

// EncodeID converts a UUID into a hex-encoded string.
func EncodeID(bytes []byte) (string, error) {
	if len(bytes) != 16 {
		return "", ErrInvalidID
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}

// DecodeID decodes a hyphenated or non-hyphenated UUID into a byte slice.
// Returns an error if the UUID is invalid.
func DecodeID(id string) ([]byte, error) {
	if !(len(id) == 32 || (len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-')) {
		return nil, ErrInvalidID
	}
	destination := make([]byte, 16)
	source := make([]byte, 32)
	sourceIndex := 0
	for index := 0; index < len(id); index++ {
		if len(id) == 36 && (index == 8 || index == 13 || index == 18 || index == 23) {
			if id[index] != '-' {
				return nil, ErrInvalidID
			}
			continue
		}
		source[sourceIndex] = id[index]
		sourceIndex++
	}
	if _, err := hex.Decode(destination, source); err != nil {
		return nil, err
	}
	return destination, nil
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
