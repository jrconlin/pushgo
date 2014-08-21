/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package id

import (
	"bytes"
	"testing"
)

var (
	hyphenatedId   = "e281b9498a924443b0c85465ba439a76"
	encodedId      = "e281b949-8a92-4443-b0c8-5465ba439a76"
	decodedId      = []byte{0xe2, 0x81, 0xb9, 0x49, 0x8a, 0x92, 0x44, 0x43, 0xb0, 0xc8, 0x54, 0x65, 0xba, 0x43, 0x9a, 0x76}
	encodedShortId = "e281b949"
	shortId        = []byte{0xe2, 0x81, 0xb9, 0x49}
	encodedLongId  = "e281b9498a924443b0c85465ba439a7601"
	longId         = []byte{0xe2, 0x81, 0xb9, 0x49, 0x8a, 0x92, 0x44, 0x43, 0xb0, 0xc8, 0x54, 0x65, 0xba, 0x43, 0x9a, 0x76, 0x01}
)

var validTests = map[string]bool{
	hyphenatedId:                           true,
	encodedId:                              true,
	encodedShortId:                         false,
	encodedLongId:                          false,
	"--e281b9498a924443b0c85465ba439a76--": false,
	"e281b9498a92-4443-b0c85465ba439a76":   false,
}

func TestValid(t *testing.T) {
	for id, isValid := range validTests {
		result := Valid(id)
		if result != isValid {
			t.Errorf("Valid(%#v): got %#v, want %#v", id, result, isValid)
		}
	}
}

func TestDecode(t *testing.T) {
	idBytes := make([]byte, 16)
	if err := Decode(hyphenatedId, idBytes); err != nil {
		t.Fatalf("Error decoding ID %#v: %#v", hyphenatedId, err)
	}
	if !bytes.Equal(decodedId, idBytes) {
		t.Errorf("Decode() decoded ID incorrectly: want %#v; got %#v", decodedId, idBytes)
	}
}

func TestGenerate(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Failed to generate ID string: %#v", err)
	}
	if len(id) != 32 {
		t.Errorf("Mismatched ID length for %#v: want 32; got %#v", id, len(id))
	}
	if !Valid(id) {
		t.Errorf("Generate() returned invalid ID: %#v", id)
	}
}
