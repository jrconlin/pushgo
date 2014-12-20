/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	cryptoRand "crypto/rand"
	"fmt"
	"math/big"
	"math/rand"
)

func init() {
	n, err := cryptoRand.Int(cryptoRand.Reader, big.NewInt(1<<63-1))
	if err != nil {
		panic(fmt.Sprintf("Error seeding PRNG: %s", err))
	}
	rand.Seed(n.Int64())
}
