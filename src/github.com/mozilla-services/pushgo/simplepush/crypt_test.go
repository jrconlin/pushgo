/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
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
