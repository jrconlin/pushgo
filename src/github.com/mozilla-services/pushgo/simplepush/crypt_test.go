/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/base64"
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

func Test_Encode(t *testing.T) {

	// Encoding is destructive to the value. Since we want to test
	// against it, make a safe copy

	testString := []byte("I'm a little teapot short and stout, this is my handle, this is my spout.")
	original := make([]byte, len(testString))
	copy(original, testString)
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
	enc, err = Encode(key, testString)
	t.Logf("Str: %s\n", enc)
	dec, err := Decode(key, enc)
	if err != nil {
		t.Errorf("Decode returned error: %s", err.Error())
	}
	if bytes.Compare(dec, original) != 0 {
		t.Errorf("Failed to decode string %s :: %s", base64.URLEncoding.EncodeToString(dec), original)
	}

}
