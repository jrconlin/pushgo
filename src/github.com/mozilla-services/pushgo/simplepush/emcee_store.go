// +build cgo,libmemcached

/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"container/list"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mc "github.com/ianoshen/gomc"

	"github.com/mozilla-services/pushgo/id"
)

// Wraps a memcached client with a flag to signal whether the connection is
// bad and should not be returned to the pool.
type release struct {
	mc.Client
	isFailed bool
}

// Determines whether the given error is a connection-level error. Connections
// that emit these errors will not be released to the pool.
func isFatalError(err error) bool {
	if err == nil {
		return false
	}
	// Error strings taken from `libmemcached/strerror.cc`.
	switch err.Error() {
	case "NOT STORED":
	case "NOT FOUND":
	case "A KEY LENGTH OF ZERO WAS PROVIDED":
	case "A BAD KEY WAS PROVIDED/CHARACTERS OUT OF RANGE":
		return false
	}
	return true
}

// Determines whether the given error is a memcached "missing key" error.
func isMissing(err error) bool {
	return strings.Contains("NOT FOUND", err.Error())
}

// NewEmcee creates an unconfigured memcached adapter.
func NewEmcee() *EmceeStore {
	s := &EmceeStore{
		closeSignal:  make(chan bool),
		releases:     make(chan release),
		acquisitions: make(chan chan mc.Client),
	}
	s.closeWait.Add(1)
	go s.run()
	return s
}

// EmceeDriverConf specifies memcached driver options.
type EmceeDriverConf struct {
	// Hosts is a list of memcached nodes.
	Hosts []string `toml:"server"`

	// MaxConns is the maximum number of open connections managed by the pool.
	// All returned connections that exceed this limit will be closed. Defaults
	// to 400.
	MaxConns int `toml:"max_connections" env:"max_conns"`

	// RecvTimeout is the socket receive timeout (SO_RCVTIMEO) used by the
	// memcached driver. Supports microsecond granularity; defaults to 5 seconds.
	RecvTimeout string `toml:"recv_timeout" env:"recv_timeout"`

	// SendTimeout is the socket send timeout (SO_SNDTIMEO) used by the
	// memcached driver. Supports microsecond granularity; defaults to 5 seconds.
	SendTimeout string `toml:"send_timeout" env:"send_timeout"`

	// PollTimeout is the poll(2) timeout used by the memcached driver. Supports
	// millisecond granularity; defaults to 5 seconds.
	PollTimeout string `toml:"poll_timeout" env:"poll_timeout"`

	// RetryTimeout is the time to wait before retrying a request on an unhealthy
	// memcached node. Supports second granularity; defaults to 5 seconds.
	RetryTimeout string `toml:"retry_timeout" env:"retry_timeout"`
}

// EmceeStore is a memcached adapter.
type EmceeStore struct {
	Hosts         []string
	MaxConns      int
	PingPrefix    string
	recvTimeout   uint64
	sendTimeout   uint64
	pollTimeout   uint64
	retryTimeout  uint64
	TimeoutLive   time.Duration
	TimeoutReg    time.Duration
	TimeoutDel    time.Duration
	HandleTimeout time.Duration
	maxChannels   int
	defaultHost   string
	logger        *SimpleLogger
	closeWait     sync.WaitGroup
	closeSignal   chan bool
	closeLock     sync.Mutex
	isClosing     bool
	releases      chan release
	acquisitions  chan chan mc.Client
	lastErr       error
}

// EmceeConf specifies memcached adapter options.
type EmceeConf struct {
	ElastiCacheConfigEndpoint string          `toml:"elasticache_config_endpoint" env:"elasticache_discovery"`
	MaxChannels               int             `toml:"max_channels" env:"max_channels"`
	Driver                    EmceeDriverConf `toml:"memcache" env:"mc"`
	Db                        DbConf
}

// ConfigStruct returns a configuration object with defaults. Implements
// HasConfigStruct.ConfigStruct().
func (*EmceeStore) ConfigStruct() interface{} {
	return &EmceeConf{
		MaxChannels: 200,
		Driver: EmceeDriverConf{
			Hosts:        []string{"127.0.0.1:11211"},
			MaxConns:     400,
			RecvTimeout:  "1s",
			SendTimeout:  "1s",
			PollTimeout:  "10ms",
			RetryTimeout: "1s",
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
func (s *EmceeStore) Init(app *Application, config interface{}) (err error) {
	conf := config.(*EmceeConf)
	s.logger = app.Logger()

	s.defaultHost = app.Hostname()
	s.maxChannels = conf.MaxChannels

	if len(conf.ElastiCacheConfigEndpoint) == 0 {
		s.Hosts = conf.Driver.Hosts
	} else {
		endpoints, err := GetElastiCacheEndpointsTimeout(
			conf.ElastiCacheConfigEndpoint, 2*time.Second)
		if err != nil {
			s.logger.Panic("storage", "Failed to retrieve ElastiCache nodes",
				LogFields{"error": err.Error()})
			return err
		}
		s.Hosts = endpoints
	}

	s.MaxConns = conf.Driver.MaxConns
	s.PingPrefix = conf.Db.PingPrefix

	if s.HandleTimeout, err = time.ParseDuration(conf.Db.HandleTimeout); err != nil {
		s.logger.Panic("emcee", "Db.HandleTimeout must be a valid duration",
			LogFields{"error": err.Error()})
		return err
	}

	// The send and receive timeouts are expressed in microseconds.
	var recvTimeout, sendTimeout time.Duration
	if recvTimeout, err = time.ParseDuration(conf.Driver.RecvTimeout); err != nil {
		s.logger.Panic("emcee", "Driver.RecvTimeout must be a valid duration",
			LogFields{"error": err.Error()})
		return err
	}
	if sendTimeout, err = time.ParseDuration(conf.Driver.SendTimeout); err != nil {
		s.logger.Panic("emcee", "Driver.SendTimeout must be a valid duration",
			LogFields{"error": err.Error()})
		return err
	}
	s.recvTimeout = uint64(recvTimeout / time.Microsecond)
	s.sendTimeout = uint64(sendTimeout / time.Microsecond)

	// `poll(2)` accepts a millisecond timeout.
	var pollTimeout time.Duration
	if pollTimeout, err = time.ParseDuration(conf.Driver.PollTimeout); err != nil {
		s.logger.Panic("emcee", "Driver.PollTimeout must be a valid duration",
			LogFields{"error": err.Error()})
		return err
	}
	s.pollTimeout = uint64(pollTimeout / time.Millisecond)

	// The memcached retry timeout is expressed in seconds.
	var retryTimeout time.Duration
	if retryTimeout, err = time.ParseDuration(conf.Driver.RetryTimeout); err != nil {
		s.logger.Panic("emcee", "Driver.RetryTimeout must be a valid duration",
			LogFields{"error": err.Error()})
		return err
	}
	s.retryTimeout = uint64(retryTimeout / time.Second)

	s.TimeoutLive = time.Duration(conf.Db.TimeoutLive) * time.Second
	s.TimeoutReg = time.Duration(conf.Db.TimeoutReg) * time.Second
	s.TimeoutDel = time.Duration(conf.Db.TimeoutReg) * time.Second

	// Open a connection to ensure the settings are valid. If the connection
	// succeeds, add it to the pool.
	client, err := s.newClient()
	if err != nil {
		s.fatal(err)
		return err
	}
	defer s.releaseWithout(client, &err)

	return nil
}

// CanStore indicates whether the specified number of channel registrations
// are allowed per client. Implements Store.CanStore().
func (s *EmceeStore) CanStore(channels int) bool {
	return channels <= s.maxChannels
}

// Close closes the connection pool and unblocks all pending operations with
// errors. Safe to call multiple times. Implements Store.Close().
func (s *EmceeStore) Close() (err error) {
	err, ok := s.stop()
	if !ok {
		return err
	}
	s.closeWait.Wait()
	return
}

// KeyToIDs extracts the hex-encoded device and channel IDs from a user-
// readable primary key. Implements Store.KeyToIDs().
func (*EmceeStore) KeyToIDs(key string) (uaid, chid string, ok bool) {
	items := strings.SplitN(key, ".", 2)
	if len(items) < 2 {
		return "", "", false
	}
	return items[0], items[1], true
}

// IDsToKey generates a user-readable primary key from a (device ID, channel
// ID) tuple. The primary key is encoded in the push endpoint URI. Implements
// Store.IDsToKey().
func (*EmceeStore) IDsToKey(uaid, chid string) (string, bool) {
	if len(uaid) == 0 || len(chid) == 0 {
		return "", false
	}
	return fmt.Sprintf("%s.%s", uaid, chid), true
}

// Status queries whether memcached is available for reading and writing.
// Implements Store.Status().
func (s *EmceeStore) Status() (success bool, err error) {
	fakeID, err := id.Generate()
	if err != nil {
		return false, err
	}
	key, expected := "status_"+fakeID, "test"
	client, err := s.getClient()
	if err != nil {
		return false, err
	}
	defer s.releaseWithout(client, &err)
	if err = client.Set(key, expected, 6*time.Second); err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("emcee", "Error storing health check key",
				LogFields{"error": err.Error(), "key": key})
		}
		return false, err
	}
	var actual string
	if err = client.Get(key, &actual); err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("emcee", "Error fetching health check key",
				LogFields{"error": err.Error(), "key": key})
		}
		return false, err
	}
	if expected != actual {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("emcee", "Unexpected health check result", LogFields{
				"key": key, "expected": expected, "actual": actual})
		}
		return false, ErrMemcacheStatus
	}
	client.Delete(key, 0)
	return true, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements Store.Exists().
func (s *EmceeStore) Exists(uaid string) bool {
	if ok, hasID := hasExistsHook(uaid); hasID {
		return ok
	}
	var err error
	if !id.Valid(uaid) {
		return false
	}
	if _, err = s.fetchAppIDArray(uaid); err != nil && !isMissing(err) {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("emcee", "Exists encountered unknown error",
				LogFields{"error": err.Error()})
		}
	}
	return err == nil
}

// Stores a new channel record in memcached.
func (s *EmceeStore) storeRegister(uaid, chid string, version int64) error {
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && !isMissing(err) {
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
	key, ok := s.IDsToKey(uaid, chid)
	if !ok {
		return ErrInvalidKey
	}
	if err = s.storeRec(key, rec); err != nil {
		return err
	}
	return nil
}

// Register creates and stores a channel record for the given device ID and
// channel ID. If version > 0, the record will be marked as active. Implements
// Store.Register().
func (s *EmceeStore) Register(uaid, chid string, version int64) (err error) {
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
func (s *EmceeStore) storeUpdate(uaid, chid string, version int64) error {
	key, ok := s.IDsToKey(uaid, chid)
	if !ok {
		return ErrInvalidKey
	}
	cRec, err := s.fetchRec(key)
	if err != nil && !isMissing(err) {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("emcee", "Update error", LogFields{
				"pk":    key,
				"error": err.Error(),
			})
		}
		return err
	}
	if cRec != nil {
		if s.logger.ShouldLog(DEBUG) {
			s.logger.Debug("emcee", "Replacing record", LogFields{
				"pk":      key,
				"uaid":    uaid,
				"chid":    chid,
				"version": fmt.Sprintf("%d", version)})
		}
		if cRec.State != StateDeleted {
			newRecord := &ChannelRecord{
				State:       StateLive,
				Version:     uint64(version),
				LastTouched: time.Now().UTC().Unix(),
			}
			if err = s.storeRec(key, newRecord); err != nil {
				return err
			}
			return nil
		}
	}
	// No record found or the record setting was DELETED
	if s.logger.ShouldLog(DEBUG) {
		s.logger.Debug("emcee", "Registering channel", LogFields{
			"uaid":      uaid,
			"channelID": chid,
			"version":   strconv.FormatInt(version, 10),
		})
	}
	if err = s.storeRegister(uaid, chid, version); err != nil {
		return err
	}
	return nil
}

// Update updates the version for the given device ID and channel ID.
// Implements Store.Update().
func (s *EmceeStore) Update(key string, version int64) (err error) {
	uaid, chid, ok := s.KeyToIDs(key)
	if !ok {
		return ErrInvalidKey
	}
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
func (s *EmceeStore) storeUnregister(uaid, chid string) error {
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && !isMissing(err) {
		return err
	}
	pos := chids.IndexOf(chid)
	if pos < 0 {
		return ErrNonexistentChannel
	}
	key, ok := s.IDsToKey(uaid, chid)
	if !ok {
		return ErrInvalidKey
	}
	if err := s.storeAppIDArray(uaid, remove(chids, pos)); err != nil {
		return err
	}
	// TODO: Allow MaxRetries to be configurable.
	for x := 0; x < 3; x++ {
		channel, err := s.fetchRec(key)
		if err != nil {
			if s.logger.ShouldLog(WARNING) {
				s.logger.Warn("emcee", "Could not delete Channel", LogFields{
					"pk":    key,
					"error": err.Error(),
				})
			}
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
// as inactive. Implements Store.Unregister().
func (s *EmceeStore) Unregister(uaid, chid string) (err error) {
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
func (s *EmceeStore) Drop(uaid, chid string) (err error) {
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
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseWithout(client, &err)
	key, ok := s.IDsToKey(uaid, chid)
	if !ok {
		return ErrInvalidKey
	}
	if err = client.Delete(key, 0); err == nil || isMissing(err) {
		return nil
	}
	return err
}

// FetchAll returns all channel updates and expired channels for a device ID
// since the specified cutoff time. Implements Store.FetchAll().
func (s *EmceeStore) FetchAll(uaid string, since time.Time) ([]Update, []string, error) {
	var err error
	if len(uaid) == 0 {
		return nil, nil, ErrNoID
	}
	if !id.Valid(uaid) {
		return nil, nil, err
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && !isMissing(err) {
		return nil, nil, err
	}

	updates := make([]Update, 0, 20)
	expired := make([]string, 0, 20)
	keys := make([]string, 0, 20)

	for _, chid := range chids {
		key, _ := s.IDsToKey(uaid, chid)
		keys = append(keys, key)
	}
	if s.logger.ShouldLog(INFO) {
		s.logger.Info("emcee", "Fetching items", LogFields{
			"uaid":  uaid,
			"items": fmt.Sprintf("[%s]", strings.Join(keys, ", ")),
		})
	}
	client, err := s.getClient()
	if err != nil {
		return nil, nil, err
	}
	defer s.releaseWithout(client, &err)

	sinceUnix := since.Unix()
	for index, key := range keys {
		channel := new(ChannelRecord)
		if err := client.Get(key, channel); err != nil {
			continue
		}
		chid := chids[index]
		channelString := chid
		if s.logger.ShouldLog(DEBUG) {
			s.logger.Debug("emcee", "FetchAll Fetched record ", LogFields{
				"uaid":  uaid,
				"chid":  channelString,
				"value": fmt.Sprintf("%d,%s,%d", channel.LastTouched, channel.State, channel.Version),
			})
		}
		if channel.LastTouched < sinceUnix {
			if s.logger.ShouldLog(DEBUG) {
				s.logger.Debug("emcee", "Skipping record...", LogFields{
					"uaid": uaid,
					"chid": channelString,
				})
			}
			continue
		}
		// Yay! Go translates numeric interface values as float64s
		// Apparently float64(1) != int(1).
		switch channel.State {
		case StateLive:
			version := channel.Version
			if version == 0 {
				version = uint64(time.Now().UTC().Unix())
				if s.logger.ShouldLog(DEBUG) {
					s.logger.Debug("emcee", "FetchAll Using Timestamp", LogFields{
						"uaid": uaid,
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
				s.logger.Debug("emcee", "FetchAll Deleting record", LogFields{
					"uaid": uaid,
					"chid": channelString,
				})
			}
			expired = append(expired, chid)
		case StateRegistered:
			// Item registered, but not yet active. Ignore it.
		default:
			if s.logger.ShouldLog(WARNING) {
				s.logger.Warn("emcee", "Unknown state", LogFields{
					"uaid": uaid,
					"chid": channelString,
				})
			}
		}
	}
	return updates, expired, nil
}

// DropAll removes all channel records for the given device ID. Implements
// Store.DropAll().
func (s *EmceeStore) DropAll(uaid string) error {
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && !isMissing(err) {
		return err
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseWithout(client, &err)
	for _, chid := range chids {
		key, ok := s.IDsToKey(uaid, chid)
		if !ok {
			return ErrInvalidKey
		}
		client.Delete(key, 0)
	}
	if err = client.Delete(uaid, 0); err != nil && !isMissing(err) {
		return err
	}
	return nil
}

// FetchPing retrieves proprietary ping information for the given device ID
// from memcached. Implements Store.FetchPing().
func (s *EmceeStore) FetchPing(uaid string) (pingData []byte, err error) {
	if len(uaid) == 0 {
		return nil, ErrNoID
	}
	if !id.Valid(uaid) {
		return nil, ErrInvalidID
	}
	client, err := s.getClient()
	if err != nil {
		return
	}
	defer s.releaseWithout(client, &err)
	err = client.Get(s.PingPrefix+uaid, &pingData)
	return
}

// PutPing stores the proprietary ping info blob for the given device ID in
// memcached. Implements Store.PutPing().
func (s *EmceeStore) PutPing(uaid string, pingData []byte) error {
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseWithout(client, &err)
	return client.Set(s.PingPrefix+uaid, pingData, 0)
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements Store.DropPing().
func (s *EmceeStore) DropPing(uaid string) error {
	if len(uaid) == 0 {
		return ErrNoID
	}
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseWithout(client, &err)
	return client.Delete(s.PingPrefix+uaid, 0)
}

// Queries memcached for a list of current subscriptions associated with the
// given device ID.
func (s *EmceeStore) fetchChannelIDs(uaid string) (result ChannelIDs, err error) {
	if len(uaid) == 0 {
		return nil, nil
	}
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	defer s.releaseWithout(client, &err)
	if err = client.Get(uaid, &result); err != nil {
		return nil, err
	}
	return
}

// Returns a duplicate-free list of subscriptions associated with the device
// ID.
func (s *EmceeStore) fetchAppIDArray(uaid string) (result ChannelIDs, err error) {
	if result, err = s.fetchChannelIDs(uaid); err != nil {
		return
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
func (s *EmceeStore) storeAppIDArray(uaid string, chids ChannelIDs) error {
	if len(uaid) == 0 {
		return ErrNoID
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseWithout(client, &err)
	// sort the array
	sort.Sort(chids)
	return client.Set(uaid, chids, 0)
}

// Retrieves a channel record from memcached.
func (s *EmceeStore) fetchRec(pk string) (*ChannelRecord, error) {
	if len(pk) == 0 {
		return nil, ErrNoKey
	}
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	defer s.releaseWithout(client, &err)
	result := new(ChannelRecord)
	if err = client.Get(pk, result); err != nil && !isMissing(err) {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("emcee", "Get Failed", LogFields{
				"pk":    pk,
				"error": err.Error(),
			})
		}
		return nil, err
	}
	if s.logger.ShouldLog(DEBUG) {
		s.logger.Debug("emcee", "Fetched", LogFields{
			"pk":     pk,
			"result": fmt.Sprintf("state: %s, vers: %d, last: %d", result.State, result.Version, result.LastTouched),
		})
	}
	return result, nil
}

// Stores an updated channel record in memcached.
func (s *EmceeStore) storeRec(pk string, rec *ChannelRecord) error {
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
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseWithout(client, &err)
	if err = client.Set(pk, rec, ttl); err != nil {
		if s.logger.ShouldLog(ERROR) {
			s.logger.Error("emcee", "Failure to set item", LogFields{
				"pk":    pk,
				"error": err.Error(),
			})
		}
	}
	return nil
}

// Releases an acquired memcached connection.
func (s *EmceeStore) releaseWithout(client mc.Client, err *error) {
	if client == nil {
		return
	}
	s.releases <- release{client, err != nil && isFatalError(*err)}
}

// Acquires a memcached connection from the connection pool.
func (s *EmceeStore) getClient() (mc.Client, error) {
	clients := make(chan mc.Client)
	select {
	case <-s.closeSignal:
		return nil, io.EOF
	case s.acquisitions <- clients:
		if client := <-clients; client != nil {
			return client, nil
		}
	case <-time.After(s.HandleTimeout):
	}
	return nil, ErrPoolSaturated
}

// Creates and configures a memcached client connection.
func (s *EmceeStore) newClient() (mc.Client, error) {
	if len(s.Hosts) == 0 {
		return nil, ErrNoNodes
	}
	client, err := mc.NewClient(s.Hosts, 1, mc.ENCODING_GOB)
	if err != nil {
		return nil, err
	}
	// internally hash key using MD5 (for key distribution)
	if err := client.SetBehavior(mc.BEHAVIOR_KETAMA_HASH, 1); err != nil {
		client.Close()
		return nil, err
	}
	// Use the binary protocol, which allows us faster data xfer
	// and better data storage (can use full UTF-8 char space)
	if err := client.SetBehavior(mc.BEHAVIOR_BINARY_PROTOCOL, 1); err != nil {
		client.Close()
		return nil, err
	}
	// `SetBehavior()` wraps libmemcached's `memcached_behavior_set()` call.
	if err := client.SetBehavior(mc.BEHAVIOR_SND_TIMEOUT, s.sendTimeout); err != nil {
		client.Close()
		return nil, err
	}
	if err := client.SetBehavior(mc.BEHAVIOR_RCV_TIMEOUT, s.recvTimeout); err != nil {
		client.Close()
		return nil, err
	}
	if err := client.SetBehavior(mc.BEHAVIOR_POLL_TIMEOUT, s.pollTimeout); err != nil {
		client.Close()
		return nil, err
	}
	if err = client.SetBehavior(mc.BEHAVIOR_RETRY_TIMEOUT, s.retryTimeout); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// The store run loop.
func (s *EmceeStore) run() {
	defer s.closeWait.Done()
	clients := list.New()
	capacity := 0
	for ok := true; ok; {
		select {
		case ok = <-s.closeSignal:
		case release := <-s.releases:
			if release.isFailed || capacity >= s.MaxConns {
				// Maximum pool size exceeded (e.g., connection manually added to the pool
				// via `newClient()` and `releaseClient()`).
				release.Close()
				if release.isFailed {
					capacity--
				}
				break
			}
			clients.PushBack(release.Client)

		case acquisition := <-s.acquisitions:
			if clients.Len() > 0 {
				// Return the first available connection from the pool.
				if client, ok := clients.Remove(clients.Front()).(mc.Client); ok {
					acquisition <- client
				}
				close(acquisition)
				break
			}
			if capacity < s.MaxConns {
				// All connections are in use, but the pool has not reached its maximum
				// capacity.
				client, err := s.newClient()
				if err != nil {
					s.fatal(err)
					close(acquisition)
					break
				}
				acquisition <- client
				capacity++
				close(acquisition)
				break
			}
			// Pool saturated.
			close(acquisition)
		}
	}
	// Shut down all connections in the pool.
	for element := clients.Front(); element != nil; element = element.Next() {
		if client, ok := element.Value.(mc.Client); ok {
			client.Close()
		}
	}
}

// Acquires s.closeLock, closes the pool, and releases the lock, reporting
// any errors to the caller. ok indicates whether the caller should wait
// for the pool to close before returning.
func (s *EmceeStore) stop() (err error, ok bool) {
	defer s.closeLock.Unlock()
	s.closeLock.Lock()
	if s.isClosing {
		return s.lastErr, false
	}
	return s.signalClose(), true
}

// Acquires s.closeLock, closes the connection pool, and releases the lock,
// storing the given error in s.lastErr.
func (s *EmceeStore) fatal(err error) {
	defer s.closeLock.Unlock()
	s.closeLock.Lock()
	s.signalClose()
	if s.lastErr == nil {
		s.lastErr = err
	}
}

// Closes the pool and exits the run loop. Assumes the caller holds
// s.closeLock.
func (s *EmceeStore) signalClose() (err error) {
	if s.isClosing {
		return
	}
	close(s.closeSignal)
	s.isClosing = true
	return nil
}

func init() {
	AvailableStores["memcache_gomc"] = func() HasConfigStruct { return NewEmcee() }
}
