/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

/*
 * The default behavior is for the push endpoint to contain the UserID and
 * ChannelID for a given device. This makes things delightfully stateless for
 * any number of scaling reasons, but does raise the possiblity that user
 * info could be easily correlated by endpoints.
 *
 * While this is by no means perfect, a simple bi-directional hash will allow
 * endpoint processors to determine the right values, but will prevent casual
 * correlation of user data.
 *
 * So, yeah, don't use this for anything else, since it will bite you.
 */

package simplepush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
)

func genKey(strength int) ([]byte, error) {
	k := make([]byte, strength)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// Encode is destructive to value. If you want to continue to use the unencoded
// value array, pass a copy.
func Encode(key, value []byte) (string, error) {
	// Keys can be 16, 24 or 32 []byte strings of cryptographically random
	// crap.
	if key == nil {
		return string(value), nil
	}
	// cleanup the val string
	if len(value) == 0 {
		return "", nil
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv, err := genKey(block.BlockSize())
	if err != nil {
		return "", err
	}
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(value, value)
	enc := append(iv, value...)
	return base64.URLEncoding.EncodeToString(enc), nil
}

func Decode(key []byte, rvalue string) ([]byte, error) {
	if key == nil || len(key) == 0 {
		return []byte(rvalue), nil
	}
	// NOTE: using the URLEncoding.Decode(...) seems to muck with the
	// returned value. Using string, which wants to return a cleaner
	// version.
	value, err := base64.URLEncoding.DecodeString(rvalue)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	iv := value[:blockSize]
	value = value[blockSize:]

	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(value, value)
	return value, nil
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
