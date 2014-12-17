/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"math/rand"
	"sort"
	"testing"
)

var connCounts = map[string]int64{
	"ws://localhost:8080": 50,
	"ws://localhost:8083": 40,
	"ws://localhost:8082": 50,
	"ws://localhost:8087": 50,
	"ws://localhost:8088": 51,
	"ws://localhost:8086": 50,
}

func etcdPeers() (peers *EtcdPeers) {
	peers = new(EtcdPeers)
	for url, freeConns := range connCounts {
		peers.Append(EtcdPeer{url, freeConns})
	}
	sort.Sort(peers)
	return peers
}

func BenchmarkChoose(b *testing.B) {
	peers := etcdPeers()
	rand.Seed(1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		peers.Choose()
	}
}
