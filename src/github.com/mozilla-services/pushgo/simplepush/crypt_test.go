/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"fmt"
	"testing"
)

func Test_genKey(t *testing.T) {
	key, err := genKey(32)
	if err != nil {
		t.Errorf("genKey encountered error: %s", err.Error())
	}
	if len(key) != 32 {
		t.Errorf("genKey returned invalid key length: %d, expecting 32",
			len(key))
	}
}

type encodeTest struct {
	name       string
	keySize    int
	testString []byte
}

func (t encodeTest) Test() error {
	key, err := genKey(t.keySize)
	if err != nil {
		return fmt.Errorf("On test %s, error generating key: %s", t.name, err)
	}

	// Encoding is destructive to the value. Since we want to test
	// against it, make a safe copy

	src := make([]byte, len(t.testString))
	copy(src, t.testString)
	enc, err := Encode(key, src)
	if err != nil {
		return fmt.Errorf("On test %s, Encode returned error: %s",
			t.name, err.Error())
	}
	if !validPK(enc) {
		return fmt.Errorf("On test %s, Encode returned invalid value: %q",
			t.name, enc)
	}
	dec, err := Decode(key, enc)
	if err != nil {
		return fmt.Errorf("On test %s, Decode returned error: %s",
			t.name, err.Error())
	}
	if bytes.Compare(dec, t.testString) != 0 {
		return fmt.Errorf("On test %s, failed to decode string %s :: %s",
			t.name, base64.URLEncoding.EncodeToString(dec), t.testString)
	}
	return nil
}

func Test_Decode(t *testing.T) {
	var (
		decrypted, expected []byte
		err                 error
	)
	key := []byte{0xf8, 0x59, 0x4, 0x72, 0x1c, 0xa, 0xc, 0x85, 0x5b, 0x7a,
		0x61, 0x26, 0xa5, 0x5a, 0xe2, 0x3b}
	encrypted := "6MgxfnBKjWtSNm6Q9WunFbj2hcjmeDudKuWUAeU="

	if decrypted, err = Decode(key, encrypted); err != nil {
		t.Errorf("Error decoding value: %s", err)
	}
	expected = []byte("Hello, world!")
	if !bytes.Equal(decrypted, expected) {
		t.Errorf("Unexpected result decoding with key: want %#v; got %#v",
			expected, decrypted)
	}
	if _, err = Decode(key[:14], "6MgxfnBKjWtSNm6Q9WunFbj2hcjmeDudKuWUAeU="); err != aes.KeySizeError(14) {
		t.Errorf("Invalid key size: want aes.KeySizeError(14); got %s", err)
	}
	if _, err = Decode(key, "!@#$%^&*()-+[]{}"); err != base64.CorruptInputError(0) {
		t.Errorf("Invalid Base64: want base64.CorruptInputError(0); got %s", err)
	}
	if _, err = Decode(key, encrypted[:8]); err != ValueSizeError(6) {
		t.Errorf("Encrypted value too short: want ValueSizeError(6); got %s", err)
	}
	if decrypted, err = Decode(nil, encrypted); err != nil {
		t.Errorf("Error decoding without key: %s", err)
	}
	expected = []byte(encrypted)
	if !bytes.Equal(decrypted, expected) {
		t.Errorf("Unexpected result decoding without key: want %#v; got %#v",
			expected, decrypted)
	}
	if decrypted, err = Decode(key, ""); err != nil {
		t.Errorf("Error decoding empty string: %s", err)
	}
	if len(decrypted) != 0 {
		t.Errorf("Unexpected result decoding empty string: got %#v", decrypted)
	}
	// Empty payload with valid IV.
	if decrypted, err = Decode(key, "dEmnrPZHgiOgttx5lhkx4w=="); err != nil {
		t.Errorf("Error decoding empty payload: %s", err)
	}
	if len(decrypted) != 0 {
		t.Errorf("Unexpected result decoding empty payload: got %#v", decrypted)
	}
}

func Test_Encode(t *testing.T) {

	testString := []byte("I'm a little teapot short and stout, this is my handle, this is my spout.")
	sizeTests := []encodeTest{
		{"AES-128", 16, testString},
		{"AES-192", 24, testString},
		{"AES-256", 32, testString},
	}
	for _, sizeTest := range sizeTests {
		if err := sizeTest.Test(); err != nil {
			t.Error(err)
		}
	}

	key, _ := genKey(16)
	enc, err := Encode(key, []byte(""))
	if string(enc) != "" {
		t.Errorf("Failed to return empty string for empty value")
	}
	enc, err = Encode(nil, testString)
	if err != nil {
		t.Errorf("Encode returned error: %s", err.Error())
	}
	if enc != string(testString) {
		t.Error("Encode failed to pass unencrypted string.")
	}

}
