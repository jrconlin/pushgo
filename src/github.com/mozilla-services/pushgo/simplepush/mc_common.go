/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/base64"
)

// ChannelState represents the state of a channel record.
type ChannelState int8

// Channel record states.
const (
	StateDeleted ChannelState = iota
	StateLive
	StateRegistered
)

func (s ChannelState) String() string {
	switch s {
	case StateDeleted:
		return "deleted"
	case StateLive:
		return "live"
	case StateRegistered:
		return "registered"
	}
	return ""
}

// Common adapter errors.
var (
	ErrPoolSaturated  StorageError = "Connection pool saturated"
	ErrMemcacheStatus StorageError = "memcached returned unexpected health check result"
	ErrUnknownUAID    StorageError = "Unknown UAID for host"
	ErrNoNodes        StorageError = "No memcached nodes available"
)

// ChannelRecord represents a channel record persisted to memcached.
type ChannelRecord struct {
	State       ChannelState
	Version     uint64
	LastTouched int64
}

// ChannelIDs is a list of decoded channel IDs.
type ChannelIDs []string

// Len returns the length of the channel ID slice. Implements
// sort.Interface.Len().
func (l ChannelIDs) Len() int {
	return len(l)
}

// Swap swaps two channel ID slices at the corresponding indices. Implements
// sort.Interface.Swap().
func (l ChannelIDs) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

// Less indicates whether one channel ID slice lexicographically precedes the
// other. Implements sort.Interface.Less().
func (l ChannelIDs) Less(i, j int) bool {
	return l[i] < l[j]
}

// IndexOf returns the location of a channel ID slice in the slice of channel
// IDs, or -1 if the ID isn't present in the containing slice.
func (l ChannelIDs) IndexOf(val string) int {
	for index, v := range l {
		if v == val {
			return index
		}
	}
	return -1
}

// Returns a new slice with the string at position pos removed or
// an equivalent slice if the pos is not in the bounds of the slice
func remove(list []string, pos int) (res []string) {
	if pos < 0 || pos == len(list) {
		return list
	}
	return append(list[:pos], list[pos+1:]...)
}

// Converts a (device ID, channel ID) tuple to a binary primary key.
func toBinaryKey(uaid, chid []byte) ([]byte, error) {
	key := make([]byte, 32)
	aoff := 16 - len(uaid)
	if aoff < 0 {
		aoff = 0
	}
	boff := 32 - len(chid)
	if boff < 16 {
		boff = 16
	}
	copy(key[aoff:], uaid)
	copy(key[boff:], chid)
	return key, nil
}

// Converts a binary primary key into a Base64-encoded string suitable for
// storage in memcached.
func encodeKey(key []byte) string {
	// Sadly, can't use full byte chars for key values, so have to encode
	// to base64. Ideally, this would just be
	// return string(key)
	return base64.StdEncoding.EncodeToString(key)
}
