/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	mc "github.com/bradfitz/gomemcache/memcache"

	"github.com/mozilla-services/pushgo/id"
	"github.com/mozilla-services/pushgo/simplepush/sperrors"
)

// NewGomemc creates an unconfigured memcached adapter.
func NewGomemc() *GomemcStore {
	s := &GomemcStore{}
	return s
}

// GomemcDriverConf specifies memcached driver options.
type GomemcDriverConf struct {
	// Hosts is a list of memcached nodes.
	Hosts []string `toml:"server"`
}

// GomemcStore is a memcached adapter.
type GomemcStore struct {
	Hosts         []string
	PingPrefix    string
	TimeoutLive   time.Duration
	TimeoutReg    time.Duration
	TimeoutDel    time.Duration
	HandleTimeout time.Duration
	maxChannels   int
	defaultHost   string
	logger        *SimpleLogger
	client        *mc.Client
}

// GomemcConf specifies memcached adapter options.
type GomemcConf struct {
	ElastiCacheConfigEndpoint string           `toml:"elasticache_config_endpoint"`
	Driver                    GomemcDriverConf `toml:"memcache"`
	Db                        DbConf
}

// ConfigStruct returns a configuration object with defaults. Implements
// `HasConfigStruct.ConfigStruct()`.
func (*GomemcStore) ConfigStruct() interface{} {
	return &GomemcConf{
		Driver: GomemcDriverConf{
			Hosts: []string{"127.0.0.1:11211"},
		},
		Db: DbConf{
			TimeoutLive:   3 * 24 * 60 * 60,
			TimeoutReg:    3 * 60 * 60,
			TimeoutDel:    24 * 60 * 60,
			HandleTimeout: "5s",
			PingPrefix:    "_pc-",
			MaxChannels:   200,
		},
	}
}

// Init initializes the memcached adapter with the given configuration.
// Implements `HasConfigStruct.Init()`.
func (s *GomemcStore) Init(app *Application, config interface{}) (err error) {
	conf := config.(*GomemcConf)
	s.logger = app.Logger()
	s.defaultHost = app.Hostname()
	if len(conf.ElastiCacheConfigEndpoint) == 0 {
		s.Hosts = conf.Driver.Hosts
	} else {
		endpoints, err := GetElastiCacheEndpointsTimeout(conf.ElastiCacheConfigEndpoint, 2*time.Second)
		if err != nil {
			s.logger.Error("storage", "Failed to retrieve ElastiCache nodes",
				LogFields{"error": err.Error()})
			return err
		}
		s.Hosts = endpoints
	}

	serverList := new(mc.ServerList)
	if err = serverList.SetServers(strings.Join(s.Hosts, ",")); err != nil {
		s.logger.Error("gomemc", "Failed to set server host list", LogFields{"error": err.Error()})
		return err
	}

	s.PingPrefix = conf.Db.PingPrefix
	if s.HandleTimeout, err = time.ParseDuration(conf.Db.HandleTimeout); err != nil {
		s.logger.Error("gomemc", "Db.HandleTimeout must be a valid duration", LogFields{"error": err.Error()})
		return err
	}

	s.TimeoutLive = time.Duration(conf.Db.TimeoutLive) * time.Second
	s.TimeoutReg = time.Duration(conf.Db.TimeoutReg) * time.Second
	s.TimeoutDel = time.Duration(conf.Db.TimeoutReg) * time.Second

	s.client = mc.NewFromSelector(serverList)
	s.client.Timeout = s.HandleTimeout

	return nil
}

// MaxChannels returns the maximum number of channel registrations allowed per
// client. Implements `Store.MaxChannels()`.
func (s *GomemcStore) MaxChannels() int {
	// this is a stub.
	return 400
}

// Close closes the connection pool and unblocks all pending operations with
// errors. Safe to call multiple times. Implements `Store.Close()`.
func (s *GomemcStore) Close() (err error) {
	return
}

// KeyToIDs extracts the hex-encoded device and channel IDs from a user-
// readable primary key. Implements `Store.KeyToIDs()`.
func (*GomemcStore) KeyToIDs(key string) (suaid, schid string, ok bool) {
	items := strings.SplitN(key, ".", 2)
	if len(items) < 2 {
		return "", "", false
	}
	return items[0], items[1], true
}

// IDsToKey generates a user-readable primary key from a (device ID, channel
// ID) tuple. The primary key is encoded in the push endpoint URI. Implements
// `Store.IDsToKey()`.
func (*GomemcStore) IDsToKey(suaid, schid string) (string, bool) {
	if len(suaid) == 0 || len(schid) == 0 {
		return "", false
	}
	return fmt.Sprintf("%s.%s", suaid, schid), true
}

// Status queries whether memcached is available for reading and writing.
// Implements `Store.Status()`.
func (s *GomemcStore) Status() (success bool, err error) {
	test := []byte("test")
	fakeID, err := id.Generate()
	if err != nil {
		return false, err
	}
	key := "status_" + fakeID
	err = s.client.Set(
		&mc.Item{
			Key:        key,
			Value:      test,
			Expiration: 6,
		})
	if err != nil {
		return false, err
	}
	raw, err := s.client.Get(key)
	if err != nil || !bytes.Equal(raw.Value, test) {
		return false, ErrStatusFailed
	}
	s.client.Delete(key)
	return true, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements `Store.Exists()`.
func (s *GomemcStore) Exists(suaid string) bool {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return false
	}
	_, err = s.client.Get(encodeKey(uaid))
	if err != nil && err != mc.ErrCacheMiss {
		s.logger.Warn("gomemc", "Exists encountered unknown error",
			LogFields{"error": err.Error()})
	}
	return err == nil
}

// Stores a new channel record in memcached.
func (s *GomemcStore) storeRegister(uaid, chid []byte, version int64) error {
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return sperrors.InvalidPrimaryKeyError
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil {
		return err
	}
	if chids.IndexOf(chid) < 0 {
		err = s.storeAppIDArray(uaid, append(chids, chid))
		if err != nil {
			return err
		}
	}
	rec := &ChannelRecord{
		State:       StateRegistered,
		LastTouched: time.Now().UTC().Unix(),
	}
	if version != 0 {
		rec.State = StateLive
		rec.Version = uint64(version)
	}
	err = s.storeRec(key, rec)
	if err != nil {
		return err
	}
	return nil
}

// Register creates and stores a channel record for the given device ID and
// channel ID. If the channel `version` is > 0, the record will be marked as
// active. Implements `Store.Register()`.
func (s *GomemcStore) Register(suaid, schid string, version int64) (err error) {
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	return s.storeRegister(uaid, chid, version)
}

// Updates a channel record in memcached.
func (s *GomemcStore) storeUpdate(uaid, chid []byte, version int64) error {
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return sperrors.InvalidPrimaryKeyError
	}
	keyString := hex.EncodeToString(key)
	cRec, err := s.fetchRec(key)
	if err != nil && err != mc.ErrCacheMiss {
		s.logger.Error("gomemc", "Update error", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
		return err
	}
	if cRec != nil {
		s.logger.Debug("gomemc", "Replacing record", LogFields{"primarykey": keyString})
		if cRec.State != StateDeleted {
			newRecord := &ChannelRecord{
				State:       StateLive,
				Version:     uint64(version),
				LastTouched: time.Now().UTC().Unix(),
			}
			err = s.storeRec(key, newRecord)
			if err != nil {
				return err
			}
			return nil
		}
	}
	// No record found or the record setting was DELETED
	s.logger.Debug("gomemc", "Registering channel", LogFields{
		"uaid":      hex.EncodeToString(uaid),
		"channelID": hex.EncodeToString(chid),
		"version":   strconv.FormatInt(version, 10),
	})
	err = s.storeRegister(uaid, chid, version)
	if err != nil {
		return err
	}
	return nil
}

// Update updates the version for the given device ID and channel ID.
// Implements `Store.Update()`.
func (s *GomemcStore) Update(key string, version int64) (err error) {
	suaid, schid, ok := s.KeyToIDs(key)
	if !ok {
		return sperrors.InvalidPrimaryKeyError
	}
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	// Normalize the device and channel IDs.
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	return s.storeUpdate(uaid, chid, version)
}

// Marks a memcached channel record as expired.
func (s *GomemcStore) storeUnregister(uaid, chid []byte) error {
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return err
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil {
		return err
	}
	pos := chids.IndexOf(chid)
	if pos < 0 {
		return sperrors.InvalidChannelError
	}
	if err := s.storeAppIDArray(uaid, remove(chids, pos)); err != nil {
		return err
	}
	// TODO: Allow `MaxRetries` to be configurable.
	for x := 0; x < 3; x++ {
		channel, err := s.fetchRec(key)
		if err != nil {
			s.logger.Warn("gomemc", "Could not delete Channel", LogFields{
				"primarykey": hex.EncodeToString(key),
				"error":      err.Error(),
			})
			continue
		}
		channel.State = StateDeleted
		err = s.storeRec(key, channel)
		break
	}
	// TODO: Propagate errors.
	return nil
}

// Unregister marks the channel ID associated with the given device ID
// as inactive. Implements `Store.Unregister()`.
func (s *GomemcStore) Unregister(suaid, schid string) (err error) {
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	return s.storeUnregister(uaid, chid)
}

// Drop removes a channel ID associated with the given device ID from
// memcached. Deregistration calls should use `Unregister()` instead.
// Implements `Store.Drop()`.
func (s *GomemcStore) Drop(suaid, schid string) (err error) {
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return err
	}
	err = s.client.Delete(encodeKey(key))
	if err == nil || err != mc.ErrCacheMiss {
		return nil
	}
	return err
}

// FetchAll returns all channel updates and expired channels for a device ID
// since the specified cutoff time. Implements `Store.FetchAll()`.
func (s *GomemcStore) FetchAll(suaid string, since time.Time) ([]Update, []string, error) {
	if len(suaid) == 0 {
		return nil, nil, sperrors.InvalidDataError
	}
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return nil, nil, err
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil {
		return nil, nil, err
	}

	updates := make([]Update, 0, 20)
	expired := make([]string, 0, 20)
	keys := make([]string, 0, 20)

	for _, chid := range chids {
		key, _ := toBinaryKey(uaid, chid)
		keys = append(keys, encodeKey(key))
	}
	deviceString := hex.EncodeToString(uaid)
	s.logger.Debug("gomemc", "Fetching items", LogFields{
		"uaid":  deviceString,
		"items": fmt.Sprintf("[%s]", strings.Join(keys, ", ")),
	})

	sinceUnix := since.Unix()
	for index, key := range keys {
		channel := new(ChannelRecord)
		raw, err := s.client.Get(key)
		if err != nil {
			continue
		}
		err = json.Unmarshal(raw.Value, &channel)
		if err != nil {
			continue
		}
		chid := chids[index]
		channelString := hex.EncodeToString(chid)
		s.logger.Debug("gomemc", "FetchAll Fetched record ", LogFields{
			"uaid":  deviceString,
			"chid":  channelString,
			"value": fmt.Sprintf("%d,%s,%d", channel.LastTouched, channel.State, channel.Version),
		})
		if channel.LastTouched < sinceUnix {
			s.logger.Debug("gomemc", "Skipping record...", LogFields{
				"uaid": deviceString,
				"chid": channelString,
			})
			continue
		}
		// Yay! Go translates numeric interface values as float64s
		// Apparently float64(1) != int(1).
		switch channel.State {
		case StateLive:
			version := channel.Version
			if version == 0 {
				version = uint64(time.Now().UTC().Unix())
				s.logger.Debug("gomemc", "FetchAll Using Timestamp", LogFields{
					"uaid": deviceString,
					"chid": channelString,
				})
			}
			update := Update{
				ChannelID: channelString,
				Version:   version,
			}
			updates = append(updates, update)
		case StateDeleted:
			s.logger.Debug("gomemc", "FetchAll Deleting record", LogFields{
				"uaid": deviceString,
				"chid": channelString,
			})
			schid, err := id.Encode(chid)
			if err != nil {
				s.logger.Warn("gomemc", "FetchAll Failed to encode channel ID", LogFields{
					"uaid": deviceString,
					"chid": channelString,
				})
				continue
			}
			expired = append(expired, schid)
		case StateRegistered:
			// Item registered, but not yet active. Ignore it.
		default:
			s.logger.Warn("gomemc", "Unknown state", LogFields{
				"uaid": deviceString,
				"chid": channelString,
			})
		}
	}
	return updates, expired, nil
}

// DropAll removes all channel records for the given device ID. Implements
// `Store.DropAll()`.
func (s *GomemcStore) DropAll(suaid string) error {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return err
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil {
		return err
	}
	for _, chid := range chids {
		key, err := toBinaryKey(uaid, chid)
		if err != nil {
			return err
		}
		s.client.Delete(encodeKey(key))
	}
	if err = s.client.Delete(encodeKey(uaid)); err != nil && err != mc.ErrCacheMiss {
		return err
	}
	return nil
}

// FetchPing retrieves proprietary ping information for the given device ID
// from memcached. Implements `Store.FetchPing()`.
func (s *GomemcStore) FetchPing(suaid string) (connect string, err error) {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return "", sperrors.InvalidDataError
	}
	raw, err := s.client.Get(s.PingPrefix + hex.EncodeToString(uaid))
	if err != nil {
		return "", err
	}
	return string(raw.Value), nil
}

// PutPing stores the proprietary ping info blob for the given device ID in
// memcached. Implements `Store.PutPing()`.
func (s *GomemcStore) PutPing(suaid string, connect string) error {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return err
	}
	return s.client.Set(&mc.Item{
		Key:        s.PingPrefix + hex.EncodeToString(uaid),
		Value:      []byte(connect),
		Expiration: 0})
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements `Store.DropPing()`.
func (s *GomemcStore) DropPing(suaid string) error {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	return s.client.Delete(s.PingPrefix + hex.EncodeToString(uaid))
}

// Returns a duplicate-free list of subscriptions associated with the device
// ID.
func (s *GomemcStore) fetchAppIDArray(uaid []byte) (result ChannelIDs, err error) {
	if len(uaid) == 0 {
		return nil, nil
	}
	raw, err := s.client.Get(encodeKey(uaid))
	if err != nil {
		if err == mc.ErrCacheMiss {
			s.logger.Warn("gomemc",
				"No channels found for UAID, dropping.",
				LogFields{"uaid": string(uaid)})
			return nil, nil
		}
		return nil, err
	}
	err = json.Unmarshal(raw.Value, &result)
	if err != nil {
		return result, err
	}
	// pare out duplicates.
	for i, chid := range result {
		if dup := result[i+1:].IndexOf(chid); dup > -1 {
			result = remove(result, i+dup)
		}
	}
	return
}

// Writes an updated subscription list for the given device ID to memcached.
// The channel IDs are sorted in-place.
func (s *GomemcStore) storeAppIDArray(uaid []byte, chids ChannelIDs) error {
	if len(uaid) == 0 {
		return sperrors.MissingDataError
	}
	// sort the array
	sort.Sort(chids)
	raw, err := json.Marshal(chids)
	if err != nil {
		s.logger.Error("gomemc", "Could not marshal AppIDArray", LogFields{"error": err.Error()})
		return err
	}
	return s.client.Set(&mc.Item{Key: encodeKey(uaid), Value: raw, Expiration: 0})
}

// Retrieves a channel record from memcached.
func (s *GomemcStore) fetchRec(pk []byte) (*ChannelRecord, error) {
	if len(pk) == 0 {
		return nil, sperrors.InvalidPrimaryKeyError
	}
	keyString := encodeKey(pk)
	result := new(ChannelRecord)
	raw, err := s.client.Get(keyString)
	if err != nil && err != mc.ErrCacheMiss {
		s.logger.Error("gomemc", "Get Failed", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
		return nil, err
	}
	err = json.Unmarshal(raw.Value, result)
	if err != nil {
		s.logger.Error("gomemc", "Could not unmarshal rec", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
		return nil, err
	}
	s.logger.Debug("gomemc", "Fetched", LogFields{
		"primarykey": keyString,
		"result":     fmt.Sprintf("state: %s, vers: %d, last: %d", result.State, result.Version, result.LastTouched),
	})
	return result, nil
}

// Stores an updated channel record in memcached.
func (s *GomemcStore) storeRec(pk []byte, rec *ChannelRecord) error {
	if len(pk) == 0 {
		return sperrors.InvalidPrimaryKeyError
	}
	if rec == nil {
		return sperrors.NoDataToStoreError
	}
	var ttl time.Duration
	switch rec.State {
	case StateDeleted:
		ttl = s.TimeoutDel
	case StateRegistered:
		ttl = s.TimeoutReg
	default:
		ttl = s.TimeoutLive
	}
	rec.LastTouched = time.Now().UTC().Unix()
	keyString := encodeKey(pk)
	raw, err := json.Marshal(rec)
	if err != nil {
		s.logger.Error("gomemc", "Failure to marshal item", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
		return err
	}
	err = s.client.Set(&mc.Item{
		Key:        keyString,
		Value:      raw,
		Expiration: int32(ttl.Seconds()),
	})
	if err != nil {
		s.logger.Warn("gomemc", "Failure to set item", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
	}
	return nil
}

func init() {
	AvailableStores["memcache_memcachego"] = func() HasConfigStruct { return NewGomemc() }
}
