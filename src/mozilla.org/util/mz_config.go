/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package util

import (
	"bufio"
	"io"
	"log"
	"os"
	"strings"
)

/* Craptastic typeless parser to read config values (use until things
   settle and you can use something more efficent like TOML)
*/

func MzGetConfig(filename string) JsMap {
	config := make(JsMap)
	// Yay for no equivalent to readln
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	reader := bufio.NewReader(file)
	for line, err := reader.ReadString('\n'); err == nil; line, err = reader.ReadString('\n') {
		// skip lines beginning with '#/;'
		if strings.Contains("#/;", string(line[0])) {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) < 2 {
			continue
		}
		config[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	if err != nil && err != io.EOF {
		log.Panic(err)
	}
	return config
}

func MzGet(ma JsMap, key string, def string) string {
	val, ok := ma[key].(string)
	if !ok {
		val = def
	}
	return val
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
