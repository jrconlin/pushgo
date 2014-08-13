package storage

import (
	"encoding/hex"
	"fmt"
	"mozilla.org/simplepush/sperrors"
	"mozilla.org/util"
	"time"
)

// StorageError represents an adapter storage error.
type StorageError string

// Error implements the `error` interface.
func (err StorageError) Error() string {
	return "StorageError: " + string(err)
}

var ErrInvalidID StorageError = "Invalid UUID"

type adapterFunc func() Adapter

// Update represents a notification sent on a channel.
type Update struct {
	ChannelID string `json:"channelID"`
	Version   uint64 `json:"version"`
}

// Store wraps a storage adapter.
type Store struct {
	adapter Adapter
}

func EncodeID(bytes []byte) (string, error) {
	if len(bytes) != 16 {
		return "", ErrInvalidID
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}

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

// Adapter describes a storage adapter.
type Adapter interface {
	// ConfigStruct returns a configuration object with defaults. TODO: Migrate
	// to `HasConfigStruct` once bbangert's pull request is ready.
	ConfigStruct() interface{}

	// Init initializes an adapter and prepares the adapter for use. TODO: Migrate
	// to the `NeedsInit` interface.
	Init(config interface{}) error

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
	Exists(uaid []byte) bool

	// Register creates a channel record in the backing store.
	Register(uaid, chid []byte, version int64) error

	// Update updates the channel record version.
	Update(uaid, chid []byte, version int64) error

	// Unregister marks a channel record as inactive.
	Unregister(uaid, chid []byte) error

	// Drop removes a channel record from the backing store.
	Drop(uaid, chid []byte) error

	// FetchAll returns all channel updates and expired channels for a device
	// since the specified cutoff time. If the cutoff time is 0, all pending
	// updates will be retrieved.
	FetchAll(uaid []byte, since time.Time) (updates []Update, expired [][]byte, err error)

	// DropAll removes all channel records for a device from the backing store.
	DropAll(uaid []byte) error

	// FetchPing retrieves proprietary ping information (e.g., GCM request data)
	// for the given device. The returned value is an opaque string parsed by the
	// ping handler.
	FetchPing(uaid []byte) (string, error)

	// PutPing stores a proprietary ping info blob for the given device.
	PutPing(uaid []byte, connect string) error

	// DropPing removes all proprietary ping info for the given device.
	DropPing(uaid []byte) error

	// FetchHost returns the host name of the Simple Push server that currently
	// maintains a connection to the device.
	FetchHost(uaid []byte) (host string, err error)

	// PutHost updates the host name associated with the device ID.
	PutHost(uaid []byte, host string) error

	// DropHost removes the host mapping for the given device
	DropHost(uaid []byte) error
}

func NewStore(config *util.MzConfig, logger *util.MzLogger) *Store {
	adapter := NewNoStore()
	return &Store{
		adapter: adapter,
	}
}

func (s *Store) ResolvePK(pk string) (suaid, schid string, err error) {
	suaid, schid, ok := s.adapter.KeyToIDs(pk)
	if !ok {
		return "", "", sperrors.InvalidPrimaryKeyError
	}
	return
}

func (s *Store) GenPK(suaid, schid string) (pk string, err error) {
	pk, ok := s.adapter.IDsToKey(suaid, schid)
	if !ok {
		return "", sperrors.InvalidPrimaryKeyError
	}
	return
}

func (s *Store) Close() error {
	return s.adapter.Close()
}

func (s *Store) UpdateChannel(key string, version int64) (err error) {
	suaid, schid, ok := s.adapter.KeyToIDs(key)
	if !ok {
		return sperrors.InvalidPrimaryKeyError
	}
	// Normalize the device and channel IDs.
	var uaid, chid []byte
	if uaid, err = DecodeID(suaid); err != nil {
		return sperrors.InvalidDataError
	}
	if chid, err = DecodeID(schid); err != nil {
		return sperrors.InvalidChannelError
	}
	return s.adapter.Update(uaid, chid, version)
}

func (s *Store) RegisterAppID(suaid, schid string, version int64) (err error) {
	var uaid, chid []byte
	if uaid, err = DecodeID(suaid); err != nil {
		return sperrors.InvalidDataError
	}
	if chid, err = DecodeID(schid); err != nil {
		return sperrors.InvalidChannelError
	}
	return s.adapter.Register(uaid, chid, version)
}

func (s *Store) DeleteAppID(suaid, schid string, clearOnly bool) (err error) {
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	var uaid, chid []byte
	if uaid, err = DecodeID(suaid); err != nil {
		return sperrors.InvalidDataError
	}
	if chid, err = DecodeID(schid); err != nil {
		return sperrors.InvalidChannelError
	}
	return s.adapter.Unregister(uaid, chid)
}

func (s *Store) IsKnownUaid(suaid string) bool {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return false
	}
	return s.adapter.Exists(uaid)
}

func (s *Store) GetUpdates(suaid string, lastAccessed int64) (results util.JsMap, err error) {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return nil, sperrors.InvalidDataError
	}
	since := time.Unix(lastAccessed, 0)
	updates, expired, err := s.adapter.FetchAll(uaid, since)
	if err != nil {
		return nil, sperrors.ServerError
	}
	if len(updates) == 0 && len(expired) == 0 {
		return nil, nil
	}
	expiredIds := make([]string, len(expired))
	for index, chid := range expired {
		if expiredIds[index], err = EncodeID(chid); err != nil {
			return nil, sperrors.InvalidChannelError
		}
	}
	toUpdates := make([]map[string]interface{}, len(updates))
	for index, update := range updates {
		toUpdates[index] = map[string]interface{}{
			"channelID": update.ChannelID,
			"version":   update.Version,
		}
	}
	results = util.JsMap{
		"expired": expiredIds,
		"updates": toUpdates,
	}
	return results, nil
}

func (s *Store) Ack(suaid string, ackPacket map[string]interface{}) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	if fields, _ := ackPacket["expired"].([]interface{}); len(fields) > 0 {
		for _, field := range fields {
			schid, ok := field.(string)
			if !ok {
				continue
			}
			chid, err := DecodeID(schid)
			if err != nil {
				return sperrors.InvalidChannelError
			}
			if err := s.adapter.Drop(uaid, chid); err != nil {
				return sperrors.ServerError
			}
		}
	}
	if fields, _ := ackPacket["updates"].([]interface{}); len(fields) > 0 {
		for _, field := range fields {
			update, ok := field.(map[string]interface{})
			if !ok {
				continue
			}
			schid, ok := update["channelID"].(string)
			if !ok {
				continue
			}
			chid, err := DecodeID(schid)
			if err != nil {
				return sperrors.InvalidDataError
			}
			if err := s.adapter.Drop(uaid, chid); err != nil {
				return sperrors.ServerError
			}
		}
	}
	return nil
}

func (s *Store) Status() (bool, error) {
	return s.adapter.Status()
}

func (s *Store) GetPropConnect(suaid string) (string, error) {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return "", sperrors.InvalidDataError
	}
	return s.adapter.FetchPing(uaid)
}

func (s *Store) SetPropConnect(suaid, connect string) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	return s.adapter.PutPing(uaid, connect)
}

func (s *Store) clearPropConnect(suaid string) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	return s.adapter.DropPing(uaid)
}

func (s *Store) GetUAIDHost(suaid string) (host string, err error) {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return "", sperrors.InvalidDataError
	}
	return s.adapter.FetchHost(uaid)
}

func (s *Store) SetUAIDHost(suaid, host string) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	return s.adapter.PutHost(uaid, host)
}

func (s *Store) DelUAIDHost(suaid string) (err error) {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	return s.adapter.DropHost(uaid)
}

func (s *Store) PurgeUAID(suaid string) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	return s.adapter.DropAll(uaid)
}
