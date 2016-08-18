/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
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
	return &GomemcStore{}
}

// GomemcDriverConf specifies memcached driver options.
type GomemcDriverConf struct {
	// Hosts is a list of memcached nodes.
	Hosts []string `toml:"server" env:"server"`
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
	ElastiCacheConfigEndpoint string           `toml:"elasticache_config_endpoint" env:"elasticache_config_endpoint"`
	MaxChannels               int              `toml:"max_channels" env:"max_channels"`
	Driver                    GomemcDriverConf `toml:"memcache" env:"memcache"`
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
			s.logger.Panic("storage", "Failed to retrieve ElastiCache nodes",
				LogFields{"error": err.Error()})
			return err
		}
		s.Hosts = endpoints
	}

	serverList := new(mc.ServerList)
	if err = serverList.SetServers(s.Hosts...); err != nil {
		s.logger.Panic("gomemc", "Failed to set server host list",
			LogFields{"error": err.Error()})
		return err
	}

	s.PingPrefix = conf.Db.PingPrefix

	if s.HandleTimeout, err = time.ParseDuration(conf.Db.HandleTimeout); err != nil {
		s.logger.Panic("gomemc", "Db.HandleTimeout must be a valid duration",
			LogFields{"error": err.Error()})
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
	return channels <= s.maxChannels
}

// Close closes the connection pool and unblocks all pending operations with
// errors. Safe to call multiple times. Implements Store.Close().
func (s *GomemcStore) Close() (err error) {
	return
}

// KeyToIDs extracts the hex-encoded device and channel IDs from a user-
// readable primary key. Implements Store.KeyToIDs().
func (s *GomemcStore) KeyToIDs(key string) (uaid, chid string, err error) {
	if uaid, chid, err = splitIDs(key); err != nil {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc", "Invalid key",
				LogFields{"error": err.Error(), "key": key})
		}
		return "", "", ErrInvalidKey
	}
	return
}

// IDsToKey generates a user-readable primary key from a (device ID, channel
// ID) tuple. The primary key is encoded in the push endpoint URI. Implements
// Store.IDsToKey().
func (s *GomemcStore) IDsToKey(uaid, chid string) (string, error) {
	logWarning := s.logger.ShouldLog(WARNING)
	if len(uaid) == 0 {
		if logWarning {
			s.logger.Warn("gomemc", "Missing device ID",
				LogFields{"uaid": uaid, "chid": chid})
		}
		return "", ErrInvalidKey
	}
	if len(chid) == 0 {
		if logWarning {
			s.logger.Warn("gomemc", "Missing channel ID",
				LogFields{"uaid": uaid, "chid": chid})
		}
		return "", ErrInvalidKey
	}
	return joinIDs(uaid, chid), nil
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
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Error storing health check key",
				LogFields{"error": err.Error(), "key": key})
		}
		return false, err
	}
	raw, err := s.client.Get(key)
	if err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Error fetching health check key",
				LogFields{"error": err.Error(), "key": key})
		}
		return false, err
	}
	if !bytes.Equal(raw.Value, expected) {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Unexpected health check result",
				LogFields{"expected": string(expected), "actual": string(raw.Value)})
		}
		return false, ErrMemcacheStatus
	}
	s.client.Delete(key)
	return true, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements Store.Exists().
func (s *GomemcStore) Exists(uaid string) bool {
	if ok, hasID := hasExistsHook(uaid); hasID {
		return ok
	}
	var err error
	if !id.Valid(uaid) {
		return false
	}
	if _, err = s.client.Get(uaid); err != nil && err != mc.ErrCacheMiss {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Exists encountered unknown error",
				LogFields{"uaid": uaid, "error": err.Error()})
		}
	}
	return err == nil
}

// Stores a new channel record in memcached.
func (s *GomemcStore) storeRegister(uaid, chid string, version int64) error {
	key := joinIDs(uaid, chid)
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
func (s *GomemcStore) Register(uaid, chid string, version int64) (err error) {
	if len(uaid) == 0 {
		return ErrNoID
	}
	if len(chid) == 0 {
		return ErrNoChannel
	}
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	if !id.Valid(chid) {
		return ErrInvalidChannel
	}
	return s.storeRegister(uaid, chid, version)
}

// Updates a channel record in memcached.
func (s *GomemcStore) storeUpdate(uaid, chid string, version int64) error {
	key := joinIDs(uaid, chid)
	cRec, err := s.fetchRec(key)
	if err != nil && err != mc.ErrCacheMiss {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Update error", LogFields{
				"pk":    key,
				"error": err.Error(),
			})
		}
		return err
	}
	if cRec != nil {
		if s.logger.ShouldLog(DEBUG) {
			s.logger.Debug("gomemc", "Replacing record", LogFields{"pk": key})
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
			"uaid":      uaid,
			"channelID": chid,
			"version":   strconv.FormatInt(version, 10),
		})
	}
	return s.storeRegister(uaid, chid, version)
}

// Update updates the version for the given device ID and channel ID.
// Implements Store.Update().
func (s *GomemcStore) Update(uaid, chid string, version int64) (err error) {
	if len(uaid) == 0 {
		return ErrNoID
	}
	if len(chid) == 0 {
		return ErrNoChannel
	}
	// Normalize the device and channel IDs.
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	if !id.Valid(chid) {
		return ErrInvalidChannel
	}
	return s.storeUpdate(uaid, chid, version)
}

// Marks a memcached channel record as expired.
func (s *GomemcStore) storeUnregister(uaid, chid string) error {
	key := joinIDs(uaid, chid)
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
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Could not delete Channel",
				LogFields{
					"pk":    key,
					"error": err.Error(),
				})
		}
		return ErrRecordUpdateFailed
	}
	channel.State = StateDeleted
	if err = s.storeRec(key, channel); err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Could not store deleted Channel",
				LogFields{
					"pk":    key,
					"error": err.Error(),
				})
		}
		return ErrRecordUpdateFailed
	}
	return nil
}

// Unregister marks the channel ID associated with the given device ID
// as inactive. Implements Store.Unregister().
func (s *GomemcStore) Unregister(uaid, chid string) (err error) {
	if len(uaid) == 0 {
		return ErrNoID
	}
	if len(chid) == 0 {
		return ErrNoChannel
	}
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	if !id.Valid(chid) {
		return ErrInvalidChannel
	}
	return s.storeUnregister(uaid, chid)
}

// Drop removes a channel ID associated with the given device ID from
// memcached. Deregistration calls should call s.Unregister() instead.
// Implements Store.Drop().
func (s *GomemcStore) Drop(uaid, chid string) (err error) {
	if len(uaid) == 0 {
		return ErrNoID
	}
	if len(chid) == 0 {
		return ErrNoChannel
	}
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	if !id.Valid(chid) {
		return ErrInvalidChannel
	}
	key := joinIDs(uaid, chid)
	if err = s.client.Delete(key); err != nil && err != mc.ErrCacheMiss {
		return err
	}
	return nil
}

// FetchAll returns all channel updates and expired channels for a device ID
// since the specified cutoff time. Implements Store.FetchAll().
func (s *GomemcStore) FetchAll(uaid string, since time.Time) ([]Update, []string, error) {
	if len(uaid) == 0 {
		return nil, nil, ErrNoID
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && err != mc.ErrCacheMiss {
		return nil, nil, err
	}

	updates := make([]Update, 0, 20)
	expired := make([]string, 0, 20)

	keys := make([]string, len(chids))
	for i, chid := range chids {
		keys[i] = joinIDs(uaid, chid)
	}
	if s.logger.ShouldLog(INFO) {
		s.logger.Info("gomemc", "Fetching items", LogFields{
			"uaid":  uaid,
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
		if s.logger.ShouldLog(DEBUG) {
			s.logger.Debug("gomemc", "FetchAll Fetched record ", LogFields{
				"uaid":  uaid,
				"chid":  chid,
				"value": fmt.Sprintf("%d,%s,%d", channel.LastTouched, channel.State, channel.Version),
			})
		}
		if channel.LastTouched < sinceUnix {
			if s.logger.ShouldLog(DEBUG) {
				s.logger.Debug("gomemc", "Skipping record...", LogFields{
					"uaid": uaid,
					"chid": chid,
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
						"uaid": uaid,
						"chid": chid,
					})
				}
			}
			update := Update{
				ChannelID: chid,
				Version:   version,
			}
			updates = append(updates, update)
		case StateDeleted:
			if s.logger.ShouldLog(DEBUG) {
				s.logger.Debug("gomemc", "FetchAll Deleting record", LogFields{
					"uaid": uaid,
					"chid": chid,
				})
			}
			expired = append(expired, chid)
		case StateRegistered:
			// Item registered, but not yet active. Ignore it.
		default:
			if s.logger.ShouldLog(WARNING) {
				s.logger.Warn("gomemc", "Unknown state", LogFields{
					"uaid": uaid,
					"chid": chid,
				})
			}
		}
	}
	return updates, expired, nil
}

// DropAll removes all channel records for the given device ID. Implements
// Store.DropAll().
func (s *GomemcStore) DropAll(uaid string) error {
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && err != mc.ErrCacheMiss {
		return err
	}
	for _, chid := range chids {
		key := joinIDs(uaid, chid)
		s.client.Delete(key)
	}
	if err = s.client.Delete(uaid); err != nil && err != mc.ErrCacheMiss {
		return err
	}
	return nil
}

// FetchPing retrieves proprietary ping information for the given device ID
// from memcached. Implements Store.FetchPing().
func (s *GomemcStore) FetchPing(uaid string) (pingData []byte, err error) {
	if len(uaid) == 0 {
		return nil, ErrNoID
	}
	if !id.Valid(uaid) {
		return nil, ErrInvalidID
	}
	raw, err := s.client.Get(s.PingPrefix + uaid)
	if err != nil {
		return nil, err
	}
	return raw.Value, nil
}

// PutPing stores the proprietary ping info blob for the given device ID in
// memcached. Implements Store.PutPing().
func (s *GomemcStore) PutPing(uaid string, pingData []byte) error {
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	return s.client.Set(&mc.Item{
		Key:        s.PingPrefix + uaid,
		Value:      pingData,
		Expiration: 0})
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements Store.DropPing().
func (s *GomemcStore) DropPing(uaid string) error {
	if len(uaid) == 0 {
		return ErrNoID
	}
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	return s.client.Delete(s.PingPrefix + uaid)
}

// Returns a duplicate-free list of subscriptions associated with the device
// ID.
func (s *GomemcStore) fetchAppIDArray(uaid string) (result ChannelIDs, err error) {
	if len(uaid) == 0 {
		return nil, nil
	}
	raw, err := s.client.Get(uaid)
	if err != nil {
		if err != mc.ErrCacheMiss {
			if s.logger.ShouldLog(ERROR) {
				s.logger.Error("gomemc",
					"Error fetching channels for UAID",
					LogFields{"uaid": uaid, "error": err.Error()})
			}
			return nil, err
		}
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("gomemc",
				"No channels found for UAID, dropping.",
				LogFields{"uaid": uaid})
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
func (s *GomemcStore) storeAppIDArray(uaid string, chids ChannelIDs) error {
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
	return s.client.Set(&mc.Item{Key: uaid, Value: raw, Expiration: 0})
}

// Retrieves a channel record from memcached. Returns an empty record if the
// channel does not exist.
func (s *GomemcStore) fetchRec(pk string) (*ChannelRecord, error) {
	result := new(ChannelRecord)
	raw, err := s.client.Get(pk)
	if err != nil {
		if err != mc.ErrCacheMiss {
			if s.logger.ShouldLog(ERROR) {
				s.logger.Error("gomemc", "Get Failed", LogFields{
					"pk":    pk,
					"error": err.Error(),
				})
			}
			return nil, err
		}
	} else if err = json.Unmarshal(raw.Value, result); err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Could not unmarshal rec", LogFields{
				"pk":    pk,
				"error": err.Error(),
			})
		}
		return nil, err
	}
	if s.logger.ShouldLog(DEBUG) {
		s.logger.Debug("gomemc", "Fetched", LogFields{
			"pk":     pk,
			"result": fmt.Sprintf("state: %s, vers: %d, last: %d", result.State, result.Version, result.LastTouched),
		})
	}
	return result, nil
}

// Stores an updated channel record in memcached.
func (s *GomemcStore) storeRec(pk string, rec *ChannelRecord) error {
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
	raw, err := json.Marshal(rec)
	if err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Failure to marshal item", LogFields{
				"pk":    pk,
				"error": err.Error(),
			})
		}
		return err
	}
	err = s.client.Set(&mc.Item{
		Key:        pk,
		Value:      raw,
		Expiration: int32(ttl.Seconds()),
	})
	if err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("gomemc", "Failure to set item", LogFields{
				"pk":    pk,
				"error": err.Error(),
			})
		}
	}
	return nil
}

func init() {
	AvailableStores["memcache_memcachego"] = func() HasConfigStruct { return NewGomemc() }
}
