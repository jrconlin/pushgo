/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	AvailableStores = make(AvailableExtensions)

	// testExistsHooks contains device IDs for which Store.Exists() always
	// returns the associated value.
	testExistsHooks map[string]bool = nil
	testExistsLock  sync.RWMutex
)

func hasExistsHook(id string) (ok bool, hasID bool) {
	if testExistsHooks != nil {
		testExistsLock.RLock()
		ok, hasID = testExistsHooks[id]
		testExistsLock.RUnlock()
	}
	return
}

func addExistsHook(id string, ok bool) {
	if testExistsHooks == nil {
		panic("addExistsHook: testExistsHooks not initialized")
	}
	testExistsLock.Lock()
	testExistsHooks[id] = ok
	testExistsLock.Unlock()
}

func removeExistsHook(id string) {
	if testExistsHooks == nil {
		panic("removeExistsHook: testExistsHooks not initialized")
	}
	testExistsLock.Lock()
	delete(testExistsHooks, id)
	testExistsLock.Unlock()
}

// StorageError represents an adapter storage error.
type StorageError string

// Error implements the error interface.
func (err StorageError) Error() string {
	return fmt.Sprintf("StorageError: %s", string(err))
}

// Update represents a notification sent on a channel.
type Update struct {
	ChannelID string `json:"channelID"`
	Version   uint64 `json:"version"`
	Data      string `json:"data"`
}

// DbConf specifies generic database adapter options.
type DbConf struct {
	// TimeoutLive is the active channel record timeout. Defaults to 3 days.
	TimeoutLive int64 `toml:"timeout_live" env:"timeout_live"`

	// TimeoutReg is the registered channel record timeout. Defaults to 3 hours;
	// an app server should send a notification on a registered channel before
	// this timeout.
	TimeoutReg int64 `toml:"timeout_reg" env:"timeout_reg"`

	// TimeoutDel is the deleted channel record timeout. Defaults to 1 day;
	// deleted records will be pruned after this timeout.
	TimeoutDel int64 `toml:"timeout_del" env:"timeout_del"`

	// HandleTimeout is the socket connection timeout. Supports second precision;
	// disabled if set to "" or "0". Defaults to 5 seconds.
	HandleTimeout string `toml:"handle_timeout" env:"handle_timeout"`

	// PingPrefix is the key prefix for proprietary (GCM, etc.) pings. Defaults to
	// "_pc-".
	PingPrefix string `toml:"prop_prefix" env:"prop_prefix"`
}

// Store describes a storage adapter.
type Store interface {
	// CanStore indicates whether the storage adapter can store the specified
	// number of channels per client.
	CanStore(channels int) bool

	// Close closes a storage adapter. Any resources (e.g., connections, open
	// files) associated with the adapter should be cleaned up, and all pending
	// operations unblocked.
	Close() error

	// KeyToIDs extracts the device and channel IDs from a storage key.
	KeyToIDs(key string) (suaid, schid string, err error)

	// IDsToKey encodes the device and channel IDs into a composite key.
	IDsToKey(suaid, schid string) (key string, err error)

	// Status indicates whether the adapter's backing store is healthy.
	Status() (bool, error)

	// Exists determines whether a device has previously registered with the
	// Simple Push server.
	Exists(suaid string) bool

	// Register creates a channel record in the backing store.
	Register(suaid, schid string, version int64) error

	// Update updates the channel record version.
	Update(suaid, schid string, version int64) error

	// Unregister marks a channel record as inactive.
	Unregister(suaid, schid string) error

	// Drop removes a channel record from the backing store.
	Drop(suaid, schid string) error

	// FetchAll returns all channel updates and expired channels for a device
	// since the specified cutoff time. If the cutoff time is 0, all pending
	// updates will be retrieved.
	FetchAll(suaid string, since time.Time) (updates []Update, expired []string, err error)

	// DropAll removes all channel records for a device from the backing store.
	DropAll(suaid string) error

	// FetchPing retrieves proprietary ping information (e.g., GCM request data)
	// for the given device. The returned value is an opaque string parsed by the
	// ping handler.
	FetchPing(suaid string) ([]byte, error)

	// PutPing stores a proprietary ping info blob for the given device.
	PutPing(suaid string, pingData []byte) error

	// DropPing removes all proprietary ping info for the given device.
	DropPing(suaid string) error
}

// keySep is the primary key separator.
var keySep = "."

// joinIDs constructs a primary key from the device ID uaid and
// channel ID chid.
func joinIDs(uaid, chid string) string {
	return uaid + keySep + chid
}

// splitIDs extracts the device ID uaid and channel ID chid from key.
func splitIDs(key string) (uaid, chid string, err error) {
	items := strings.SplitN(key, keySep, 2)
	if len(items) == 0 {
		return "", "", ErrNoID
	}
	uaid = items[0]
	if len(uaid) == 0 {
		return "", "", ErrNoID
	}
	if len(items) == 1 {
		return "", "", ErrNoChannel
	}
	chid = items[1]
	if len(chid) == 0 {
		return "", "", ErrNoChannel
	}
	return
}
