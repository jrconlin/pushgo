/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

/*
 * Generate a string that can be used as a token_key
 */

package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
)

var validKeys = [3]int{16, 24, 32}
var keySize int

func genKey(strength int) []byte {
	k := make([]byte, strength)
	if _, err := rand.Read(k); err != nil {
		fmt.Printf("genKey Error: %s", err)
		return nil
	}
	return k
}

func main() {
	flag.IntVar(&keySize, "keysize", 16, "Crypt Key size (16, 24, 32 bytes)")
	flag.IntVar(&keySize, "k", 16, "Crypt Key size (16, 24, 32 bytes)")
	flag.Parse()
	fmt.Printf("# Using keySize: %d", keySize)
	for _, val := range validKeys {
		if val == keySize {
			fmt.Printf("\ntoken-key = %s\n",
				base64.URLEncoding.EncodeToString(genKey(keySize)))
			return
		}
	}
	fmt.Printf("Error: Invalid keysize specified. Please use 16, 24, or 32\n")
}

// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
