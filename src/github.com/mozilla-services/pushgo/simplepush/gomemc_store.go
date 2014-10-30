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
	ElastiCacheConfigEndpoint string           `toml:"elasticache_config_endpoint" env:"elasticache_discovery"`
	MaxChannels               int              `toml:"max_channels" env:"max_channels"`
	Driver                    GomemcDriverConf `toml:"memcache" env:"mc"`
	Db                        DbConf
}

// ConfigStruct returns a configuration object with defaults. Implements
// HasConfigStruct.ConfigStruct().
func (*GomemcStore) ConfigStruct() interface{} {
	return &GomemcConf{
		MaxChannels: 200,
		Driver: GomemcDriverConf{
			Hosts: []string{"127.0.0.1:11211"},
		},
		Db: DbConf{
			TimeoutLive:   3 * 24 * 60 * 60,
			TimeoutReg:    3 * 60 * 60,
			TimeoutDel:    24 * 60 * 60,
			HandleTimeout: "5s",
			PingPrefix:    "_pc-",
		},
	}
}

// Init initializes the memcached adapter with the given configuration.
// Implements HasConfigStruct.Init().
func (s *GomemcStore) Init(app *Application, config interface{}) (err error) {
	conf := config.(*GomemcConf)
	s.logger = app.Logger()
	s.defaultHost = app.Hostname()
	s.maxChannels = conf.MaxChannels

	if len(conf.ElastiCacheConfigEndpoint) == 0 {
		s.Hosts = conf.Driver.Hosts
	} else {
		endpoints, err := GetElastiCacheEndpointsTimeout(conf.ElastiCacheConfigEndpoint, 2*time.Second)
		if err != nil {
			s.logger.Alert("storage", "Failed to retrieve ElastiCache nodes",
				LogFields{"error": err.Error()})
			return err
		}
		s.Hosts = endpoints
	}

	serverList := new(mc.ServerList)
	if err = serverList.SetServers(s.Hosts...); err != nil {
		s.logger.Alert("gomemc", "Failed to set server host list", LogFields{"error": err.Error()})
		return err
	}

	s.PingPrefix = conf.Db.PingPrefix

	if s.HandleTimeout, err = time.ParseDuration(conf.Db.HandleTimeout); err != nil {
		s.logger.Alert("gomemc", "Db.HandleTimeout must be a valid duration", LogFields{"error": err.Error()})
		return err
	}

	s.TimeoutLive = time.Duration(conf.Db.TimeoutLive) * time.Second
	s.TimeoutReg = time.Duration(conf.Db.TimeoutReg) * time.Second
	s.TimeoutDel = time.Duration(conf.Db.TimeoutReg) * time.Second

	s.client = mc.NewFromSelector(serverList)
	s.client.Timeout = s.HandleTimeout

	return nil
}

// CanStore indicates whether the specified number of channel registrations
// are allowed per client. Implements Store.CanStore().
func (s *GomemcStore) CanStore(channels int) bool {
	return channels < s.maxChannels
}

// Close closes the connection pool and unblocks all pending operations with
// errors. Safe to call multiple times. Implements Store.Close().
func (s *GomemcStore) Close() (err error) {
	return
}

// KeyToIDs extracts the hex-encoded device and channel IDs from a user-
// readable primary key. Implements Store.KeyToIDs().
func (s *GomemcStore) KeyToIDs(key string) (suaid, schid string, ok bool) {
	items := strings.SplitN(key, ".", 2)
	if len(items) < 2 {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc", "Invalid Key, returning blank IDs",
				LogFields{"key": key})
		}
		return "", "", false
	}
	return items[0], items[1], true
}

// IDsToKey generates a user-readable primary key from a (device ID, channel
// ID) tuple. The primary key is encoded in the push endpoint URI. Implements
// Store.IDsToKey().
func (s *GomemcStore) IDsToKey(suaid, schid string) (string, bool) {
	if len(suaid) == 0 || len(schid) == 0 {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc", "Invalid IDs, returning blank Key",
				LogFields{"uaid": suaid, "chid": schid})
		}
		return "", false
	}
	return fmt.Sprintf("%s.%s", suaid, schid), true
}

// Status queries whether memcached is available for reading and writing.
// Implements Store.Status().
func (s *GomemcStore) Status() (success bool, err error) {
	fakeID, err := id.Generate()
	if err != nil {
		return false, err
	}
	key, expected := "status_"+fakeID, []byte("test")
	err = s.client.Set(
		&mc.Item{
			Key:        key,
			Value:      expected,
			Expiration: 6,
		})
	if err != nil {
		return false, err
	}
	raw, err := s.client.Get(key)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(raw.Value, expected) {
		s.logger.Error("gomemc", "Unexpected health check result",
			LogFields{"expected": string(expected), "actual": string(raw.Value)})
		return false, ErrMemcacheStatus
	}
	s.client.Delete(key)
	return true, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements Store.Exists().
func (s *GomemcStore) Exists(suaid string) bool {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return false
	}
	if _, err = s.client.Get(encodeKey(uaid)); err != nil && err != mc.ErrCacheMiss {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc", "Exists encountered unknown error",
				LogFields{"error": err.Error()})
		}
	}
	return err == nil
}

// Stores a new channel record in memcached.
func (s *GomemcStore) storeRegister(uaid, chid []byte, version int64) error {
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return ErrInvalidKey
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && err != mc.ErrCacheMiss {
		return err
	}
	if chids.IndexOf(chid) < 0 {
		if err = s.storeAppIDArray(uaid, append(chids, chid)); err != nil {
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
	if err = s.storeRec(key, rec); err != nil {
		return err
	}
	return nil
}

// Register creates and stores a channel record for the given device ID and
// channel ID. If version > 0, the record will be marked as active. Implements
// Store.Register().
func (s *GomemcStore) Register(suaid, schid string, version int64) (err error) {
	if len(suaid) == 0 {
		return ErrNoID
	}
	if len(schid) == 0 {
		return ErrNoChannel
	}
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return ErrInvalidID
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return ErrInvalidChannel
	}
	return s.storeRegister(uaid, chid, version)
}

// Updates a channel record in memcached.
func (s *GomemcStore) storeUpdate(uaid, chid []byte, version int64) error {
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return ErrInvalidKey
	}
	cRec, err := s.fetchRec(key)
	if err != nil && err != mc.ErrCacheMiss {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc", "Update error", LogFields{
				"pk":    hex.EncodeToString(key),
				"error": err.Error(),
			})
		}
		return err
	}
	if cRec != nil {
		if s.logger.ShouldLog(DEBUG) {
			s.logger.Debug("gomemc", "Replacing record", LogFields{"pk": hex.EncodeToString(key)})
		}
		if cRec.State != StateDeleted {
			newRecord := &ChannelRecord{
				State:       StateLive,
				Version:     uint64(version),
				LastTouched: time.Now().UTC().Unix(),
			}
			return s.storeRec(key, newRecord)
		}
	}
	// No record found or the record setting was DELETED
	if s.logger.ShouldLog(DEBUG) {
		s.logger.Debug("gomemc", "Registering channel", LogFields{
			"uaid":      hex.EncodeToString(uaid),
			"channelID": hex.EncodeToString(chid),
			"version":   strconv.FormatInt(version, 10),
		})
	}
	return s.storeRegister(uaid, chid, version)
}

// Update updates the version for the given device ID and channel ID.
// Implements Store.Update().
func (s *GomemcStore) Update(key string, version int64) (err error) {
	suaid, schid, ok := s.KeyToIDs(key)
	if !ok {
		return ErrInvalidKey
	}
	if len(suaid) == 0 {
		return ErrNoID
	}
	if len(schid) == 0 {
		return ErrNoChannel
	}
	// Normalize the device and channel IDs.
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return ErrInvalidID
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return ErrInvalidChannel
	}
	return s.storeUpdate(uaid, chid, version)
}

// Marks a memcached channel record as expired.
func (s *GomemcStore) storeUnregister(uaid, chid []byte) error {
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return ErrInvalidKey
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && err != mc.ErrCacheMiss {
		return err
	}
	pos := chids.IndexOf(chid)
	if pos < 0 {
		return ErrNonexistentChannel
	}
	if err := s.storeAppIDArray(uaid, remove(chids, pos)); err != nil {
		return err
	}
	channel, err := s.fetchRec(key)
	if err != nil {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc", "Could not delete Channel",
				LogFields{
					"pk":    hex.EncodeToString(key),
					"error": err.Error(),
				})
		}
		return ErrRecordUpdateFailed
	}
	channel.State = StateDeleted
	if err = s.storeRec(key, channel); err != nil {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc", "Could not store deleted Channel",
				LogFields{
					"pk":    hex.EncodeToString(key),
					"error": err.Error(),
				})
		}
		return ErrRecordUpdateFailed
	}
	return nil
}

// Unregister marks the channel ID associated with the given device ID
// as inactive. Implements Store.Unregister().
func (s *GomemcStore) Unregister(suaid, schid string) (err error) {
	if len(suaid) == 0 {
		return ErrNoID
	}
	if len(schid) == 0 {
		return ErrNoChannel
	}
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return ErrInvalidID
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return ErrInvalidChannel
	}
	return s.storeUnregister(uaid, chid)
}

// Drop removes a channel ID associated with the given device ID from
// memcached. Deregistration calls should call s.Unregister() instead.
// Implements Store.Drop().
func (s *GomemcStore) Drop(suaid, schid string) (err error) {
	if len(suaid) == 0 {
		return ErrNoID
	}
	if len(schid) == 0 {
		return ErrNoChannel
	}
	var uaid, chid []byte
	if uaid, err = id.DecodeString(suaid); err != nil || len(uaid) == 0 {
		return ErrInvalidID
	}
	if chid, err = id.DecodeString(schid); err != nil || len(chid) == 0 {
		return ErrInvalidChannel
	}
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return ErrInvalidKey
	}
	if err = s.client.Delete(encodeKey(key)); err != nil && err != mc.ErrCacheMiss {
		return err
	}
	return nil
}

// FetchAll returns all channel updates and expired channels for a device ID
// since the specified cutoff time. Implements Store.FetchAll().
func (s *GomemcStore) FetchAll(suaid string, since time.Time) ([]Update, []string, error) {
	if len(suaid) == 0 {
		return nil, nil, ErrNoID
	}
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return nil, nil, err
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && err != mc.ErrCacheMiss {
		return nil, nil, err
	}

	updates := make([]Update, 0, 20)
	expired := make([]string, 0, 20)
	keys := make([]string, 0, 20)

	for _, chid := range chids {
		key, _ := toBinaryKey(uaid, chid)
		keys = append(keys, encodeKey(key))
	}
	if s.logger.ShouldLog(INFO) {
		s.logger.Info("gomemc", "Fetching items", LogFields{
			"uaid":  hex.EncodeToString(uaid),
			"items": fmt.Sprintf("[%s]", strings.Join(keys, ", ")),
		})
	}

	sinceUnix := since.Unix()
	for index, key := range keys {
		channel := new(ChannelRecord)
		raw, err := s.client.Get(key)
		if err != nil {
			continue
		}
		if err = json.Unmarshal(raw.Value, &channel); err != nil {
			continue
		}
		chid := chids[index]
		channelString := hex.EncodeToString(chid)
		if s.logger.ShouldLog(DEBUG) {
			s.logger.Debug("gomemc", "FetchAll Fetched record ", LogFields{
				"uaid":  hex.EncodeToString(uaid),
				"chid":  channelString,
				"value": fmt.Sprintf("%d,%s,%d", channel.LastTouched, channel.State, channel.Version),
			})
		}
		if channel.LastTouched < sinceUnix {
			if s.logger.ShouldLog(DEBUG) {
				s.logger.Debug("gomemc", "Skipping record...", LogFields{
					"uaid": hex.EncodeToString(uaid),
					"chid": channelString,
				})
			}
			continue
		}
		switch channel.State {
		case StateLive:
			version := channel.Version
			if version == 0 {
				version = uint64(time.Now().UTC().Unix())
				if s.logger.ShouldLog(DEBUG) {
					s.logger.Debug("gomemc", "FetchAll Using Timestamp", LogFields{
						"uaid": hex.EncodeToString(uaid),
						"chid": channelString,
					})
				}
			}
			update := Update{
				ChannelID: channelString,
				Version:   version,
			}
			updates = append(updates, update)
		case StateDeleted:
			if s.logger.ShouldLog(DEBUG) {
				s.logger.Debug("gomemc", "FetchAll Deleting record", LogFields{
					"uaid": hex.EncodeToString(uaid),
					"chid": channelString,
				})
			}
			schid, err := id.Encode(chid)
			if err != nil {
				if s.logger.ShouldLog(WARNING) {
					s.logger.Warn("gomemc", "FetchAll Failed to encode channel ID", LogFields{
						"uaid": hex.EncodeToString(uaid),
						"chid": channelString,
					})
				}
				continue
			}
			expired = append(expired, schid)
		case StateRegistered:
			// Item registered, but not yet active. Ignore it.
		default:
			if s.logger.ShouldLog(WARNING) {
				s.logger.Warn("gomemc", "Unknown state", LogFields{
					"uaid": hex.EncodeToString(uaid),
					"chid": channelString,
				})
			}
		}
	}
	return updates, expired, nil
}

// DropAll removes all channel records for the given device ID. Implements
// Store.DropAll().
func (s *GomemcStore) DropAll(suaid string) error {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return err
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && err != mc.ErrCacheMiss {
		return err
	}
	for _, chid := range chids {
		key, err := toBinaryKey(uaid, chid)
		if err != nil {
			return ErrInvalidKey
		}
		s.client.Delete(encodeKey(key))
	}
	if err = s.client.Delete(encodeKey(uaid)); err != nil && err != mc.ErrCacheMiss {
		return err
	}
	return nil
}

// FetchPing retrieves proprietary ping information for the given device ID
// from memcached. Implements Store.FetchPing().
func (s *GomemcStore) FetchPing(suaid string) (pingData []byte, err error) {
	if len(suaid) == 0 {
		return nil, ErrNoID
	}
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return nil, ErrInvalidID
	}
	raw, err := s.client.Get(s.PingPrefix + hex.EncodeToString(uaid))
	if err != nil {
		return nil, err
	}
	return raw.Value, nil
}

// PutPing stores the proprietary ping info blob for the given device ID in
// memcached. Implements Store.PutPing().
func (s *GomemcStore) PutPing(suaid string, pingData []byte) error {
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return err
	}
	return s.client.Set(&mc.Item{
		Key:        s.PingPrefix + hex.EncodeToString(uaid),
		Value:      pingData,
		Expiration: 0})
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements Store.DropPing().
func (s *GomemcStore) DropPing(suaid string) error {
	if len(suaid) == 0 {
		return ErrNoID
	}
	uaid, err := id.DecodeString(suaid)
	if err != nil {
		return ErrInvalidID
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
		if err != mc.ErrCacheMiss {
			if s.logger.ShouldLog(ERROR) {
				s.logger.Error("gomemc",
					"Error fetching channels for UAID",
					LogFields{"uaid": hex.EncodeToString(uaid), "error": err.Error()})
			}
			return nil, err
		}
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc",
				"No channels found for UAID, dropping.",
				LogFields{"uaid": hex.EncodeToString(uaid)})
		}
		return nil, err
	}
	if err = json.Unmarshal(raw.Value, &result); err != nil {
		return result, err
	}
	return
}

// Writes an updated subscription list for the given device ID to memcached.
// The channel IDs are sorted in-place.
func (s *GomemcStore) storeAppIDArray(uaid []byte, chids ChannelIDs) error {
	if len(uaid) == 0 {
		return ErrNoID
	}
	// sort the array
	sort.Sort(chids)
	// pare out duplicates.
	for i, chid := range chids {
		if dup := chids[i+1:].IndexOf(chid); dup > -1 {
			chids = remove(chids, i+dup)
		}
	}
	raw, err := json.Marshal(chids)
	if err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Could not marshal AppIDArray", LogFields{"error": err.Error()})
		}
		return err
	}
	return s.client.Set(&mc.Item{Key: encodeKey(uaid), Value: raw, Expiration: 0})
}

// Retrieves a channel record from memcached.
func (s *GomemcStore) fetchRec(pk []byte) (*ChannelRecord, error) {
	if len(pk) == 0 {
		return nil, ErrNoKey
	}
	keyString := encodeKey(pk)
	result := new(ChannelRecord)
	raw, err := s.client.Get(keyString)
	if err != nil && err != mc.ErrCacheMiss {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Get Failed", LogFields{
				"pk":    keyString,
				"error": err.Error(),
			})
		}
		return nil, err
	}
	if err = json.Unmarshal(raw.Value, result); err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Could not unmarshal rec", LogFields{
				"pk":    keyString,
				"error": err.Error(),
			})
		}
		return nil, err
	}
	if s.logger.ShouldLog(DEBUG) {
		s.logger.Debug("gomemc", "Fetched", LogFields{
			"pk":     keyString,
			"result": fmt.Sprintf("state: %s, vers: %d, last: %d", result.State, result.Version, result.LastTouched),
		})
	}
	return result, nil
}

// Stores an updated channel record in memcached.
func (s *GomemcStore) storeRec(pk []byte, rec *ChannelRecord) error {
	if len(pk) == 0 {
		return ErrNoKey
	}
	if rec == nil {
		return ErrNoData
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
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Failure to marshal item", LogFields{
				"pk":    keyString,
				"error": err.Error(),
			})
		}
		return err
	}
	err = s.client.Set(&mc.Item{
		Key:        keyString,
		Value:      raw,
		Expiration: int32(ttl.Seconds()),
	})
	if err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Failure to set item", LogFields{
				"pk":    keyString,
				"error": err.Error(),
			})
		}
	}
	return nil
}

func init() {
	AvailableStores["memcache_memcachego"] = func() HasConfigStruct { return NewGomemc() }
}
