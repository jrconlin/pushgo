/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

// taken from http://www.ashishbanerjee.com/home/go/go-generate-uuid

import (
    "crypto/rand"
    "encoding/hex"
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
