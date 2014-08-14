package simplepush

import (
	"encoding/hex"
	"fmt"
	"time"
)

var (
	ErrInvalidID    StorageError = "Invalid UUID"
	AvailableStores              = make(AvailableExtensions)
)

// StorageError represents an adapter storage error.
type StorageError string

// Error implements the `error` interface.
func (err StorageError) Error() string {
	return "StorageError: " + string(err)
}

// Update represents a notification sent on a channel.
type Update struct {
	ChannelID string `json:"channelID"`
	Version   uint64 `json:"version"`
}

// Store describes a storage adapter.
type Store interface {
	// MaxChannels returns the maximum number of channel registrations allowed
	// per client.
	MaxChannels() int

	// Close closes a storage adapter. Any resources (e.g., connections, open
	// files) associated with the adapter should be cleaned up, and all pending
	// operations unblocked.
	Close() error

	// KeyToIDs extracts the device and channel IDs from a storage key.
	KeyToIDs(key string) (suaid, schid string, ok bool)

	// IDsToKey encodes the device and channel IDs into a composite key.
	IDsToKey(suaid, schid string) (key string, ok bool)

	// Status indicates whether the adapter's backing store is healthy.
	Status() (bool, error)

	// Exists determines whether a device has previously registered with the
	// Simple Push server.
	Exists(suaid string) bool

	// Register creates a channel record in the backing store.
	Register(suaid, schid string, version int64) error

	// Update updates the channel record version.
	Update(key string, version int64) error

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
	FetchPing(suaid string) (string, error)

	// PutPing stores a proprietary ping info blob for the given device.
	PutPing(suaid, connect string) error

	// DropPing removes all proprietary ping info for the given device.
	DropPing(suaid string) error

	// FetchHost returns the host name of the Simple Push server that currently
	// maintains a connection to the device.
	FetchHost(suaid string) (host string, err error)

	// PutHost updates the host name associated with the device ID.
	PutHost(suaid, host string) error

	// DropHost removes the host mapping for the given device
	DropHost(suaid string) error
}

// EncodeID converts a UUID into a hex-encoded string.
func EncodeID(bytes []byte) (string, error) {
	if len(bytes) != 16 {
		return "", ErrInvalidID
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}

// DecodeID decodes a hyphenated or non-hyphenated UUID into a byte slice.
// Returns an error if the UUID is invalid.
func DecodeID(id string) ([]byte, error) {
	if !(len(id) == 32 || (len(id) == 36 && id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-')) {
		return nil, ErrInvalidID
	}
	destination := make([]byte, 16)
	source := make([]byte, 32)
	sourceIndex := 0
	for index := 0; index < len(id); index++ {
		if len(id) == 36 && (index == 8 || index == 13 || index == 18 || index == 23) {
			if id[index] != '-' {
				return nil, ErrInvalidID
			}
			continue
		}
		source[sourceIndex] = id[index]
		sourceIndex++
	}
	if _, err := hex.Decode(destination, source); err != nil {
		return nil, err
	}
	return destination, nil
}
