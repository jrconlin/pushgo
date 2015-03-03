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

	mc "github.com/varstr/gomc"

	"github.com/mozilla-services/pushgo/id"
)

// parseTimeout parses a duration string and ensures the value is within the
// given precision. Returns a uint64 for use with mc.Client.SetBehavior.
func parseTimeout(s string, precision time.Duration) (uint64, error) {
	if len(s) == 0 {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d == 0 {
		return 0, nil
	}
	if d < precision {
		return 0, fmt.Errorf("Timeout %s too short; must be at least %s",
			d, precision)
	}
	return uint64(d / precision), nil
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
func NewEmcee() (s *EmceeStore) {
	s = &EmceeStore{
		clients: list.New(),
	}
	s.cond.L = new(sync.Mutex)
	return s
}

// EmceeDriverConf specifies memcached driver options.
type EmceeDriverConf struct {
	// Hosts is a list of memcached nodes.
	Hosts []string `toml:"server" env:"server"`

	// MaxConns is the maximum number of open connections managed by the pool.
	// All returned connections that exceed this limit will be closed. Defaults
	// to 100.
	MaxConns int `toml:"max_connections" env:"max_connections"`

	// RecvTimeout is the socket receive timeout (SO_RCVTIMEO) used by the
	// memcached driver. Supports microsecond precision; disabled if set to
	// "" or "0". Defaults to 5 seconds.
	RecvTimeout string `toml:"recv_timeout" env:"recv_timeout"`

	// SendTimeout is the socket send timeout (SO_SNDTIMEO) used by the
	// memcached driver. Supports microsecond precision; disabled if set to
	// "" or "0". Defaults to 5 seconds.
	SendTimeout string `toml:"send_timeout" env:"send_timeout"`

	// PollTimeout is the poll(2) timeout used by the memcached driver. Supports
	// millisecond precision; disabled if set to "" or "0". No default timeout.
	PollTimeout string `toml:"poll_timeout" env:"poll_timeout"`

	// RetryTimeout is the time to wait before retrying a request on an unhealthy
	// memcached node. Supports second precision; disabled if set to "" or "0".
	// Defaults to 5 seconds.
	RetryTimeout string `toml:"retry_timeout" env:"retry_timeout"`
}

// EmceeStore is a memcached adapter.
type EmceeStore struct {
	Hosts          []string
	MaxConns       int
	PingPrefix     string
	connectTimeout uint64
	recvTimeout    uint64
	sendTimeout    uint64
	pollTimeout    uint64
	retryTimeout   uint64
	TimeoutLive    time.Duration
	TimeoutReg     time.Duration
	TimeoutDel     time.Duration
	maxChannels    int
	defaultHost    string
	logger         *SimpleLogger
	cond           sync.Cond
	clients        *list.List
	capacity       int
	isClosed       bool
}

// EmceeConf specifies memcached adapter options.
type EmceeConf struct {
	ElastiCacheConfigEndpoint string          `toml:"elasticache_config_endpoint" env:"elasticache_config_endpoint"`
	MaxChannels               int             `toml:"max_channels" env:"max_channels"`
	Driver                    EmceeDriverConf `toml:"memcache" env:"memcache"`
	Db                        DbConf
}

// ConfigStruct returns a configuration object with defaults. Implements
// HasConfigStruct.ConfigStruct().
func (*EmceeStore) ConfigStruct() interface{} {
	return &EmceeConf{
		MaxChannels: 200,
		Driver: EmceeDriverConf{
			Hosts:        []string{"127.0.0.1:11211"},
			MaxConns:     100,
			RecvTimeout:  "5s",
			SendTimeout:  "5s",
			RetryTimeout: "5s",
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

	// The socket connection timeout in milliseconds.
	if s.connectTimeout, err = parseTimeout(conf.Db.HandleTimeout, time.Millisecond); err != nil {
		s.logger.Panic("emcee", "Db.HandleTimeout must be a valid duration",
			LogFields{"error": err.Error(), "connectTimeout": conf.Db.HandleTimeout})
		return err
	}

	// The socket read timeout in microseconds.
	if s.recvTimeout, err = parseTimeout(conf.Driver.RecvTimeout, time.Microsecond); err != nil {
		s.logger.Panic("emcee", "Driver.RecvTimeout must be a valid duration",
			LogFields{"error": err.Error(), "recvTimeout": conf.Driver.RecvTimeout})
		return err
	}

	// The socket write timeout in microseconds.
	if s.sendTimeout, err = parseTimeout(conf.Driver.SendTimeout, time.Microsecond); err != nil {
		s.logger.Panic("emcee", "Driver.SendTimeout must be a valid duration",
			LogFields{"error": err.Error(), "sendTimeout": conf.Driver.SendTimeout})
		return err
	}

	// The poll(2) timeout in milliseconds.
	if s.pollTimeout, err = parseTimeout(conf.Driver.PollTimeout, time.Millisecond); err != nil {
		s.logger.Panic("emcee", "Driver.PollTimeout must be a valid duration",
			LogFields{"error": err.Error(), "pollTimeout": conf.Driver.PollTimeout})
		return err
	}

	// The retry timeout in seconds.
	if s.retryTimeout, err = parseTimeout(conf.Driver.RetryTimeout, time.Second); err != nil {
		s.logger.Panic("emcee", "Driver.RetryTimeout must be a valid duration",
			LogFields{"error": err.Error(), "retryTimeout": conf.Driver.RetryTimeout})
	}

	s.TimeoutLive = time.Duration(conf.Db.TimeoutLive) * time.Second
	s.TimeoutReg = time.Duration(conf.Db.TimeoutReg) * time.Second
	s.TimeoutDel = time.Duration(conf.Db.TimeoutReg) * time.Second

	return nil
}

// CanStore indicates whether the specified number of channel registrations
// are allowed per client. Implements Store.CanStore().
func (s *EmceeStore) CanStore(channels int) bool {
	return channels <= s.maxChannels
}

// Close closes the connection pool and unblocks all pending operations with
// errors. Safe to call multiple times. Implements Store.Close().
func (s *EmceeStore) Close() error {
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
	defer s.cond.Broadcast()
	// Shut down all connections in the pool.
	for element := s.clients.Front(); element != nil; element = element.Next() {
		element.Value.(mc.Client).Close()
	}
	// Clear the list and notify waiting goroutines.
	s.clients.Init()
	s.isClosed = true
	return nil
}

// KeyToIDs extracts the hex-encoded device and channel IDs from a user-
// readable primary key. Implements Store.KeyToIDs().
func (s *EmceeStore) KeyToIDs(key string) (uaid, chid string, err error) {
	if uaid, chid, err = splitIDs(key); err != nil {
		if s.logger.ShouldLog(WARNING) {
			s.logger.Warn("emcee", "Invalid key",
				LogFields{"error": err.Error(), "key": key})
		}
		return "", "", ErrInvalidKey
	}
	return
}

// IDsToKey generates a user-readable primary key from a (device ID, channel
// ID) tuple. The primary key is encoded in the push endpoint URI. Implements
// Store.IDsToKey().
func (s *EmceeStore) IDsToKey(uaid, chid string) (string, error) {
	logWarning := s.logger.ShouldLog(WARNING)
	if len(uaid) == 0 {
		if logWarning {
			s.logger.Warn("emcee", "Missing device ID",
				LogFields{"uaid": uaid, "chid": chid})
		}
		return "", ErrInvalidKey
	}
	if len(chid) == 0 {
		if logWarning {
			s.logger.Warn("emcee", "Missing channel ID",
				LogFields{"uaid": uaid, "chid": chid})
		}
		return "", ErrInvalidKey
	}
	return joinIDs(uaid, chid), nil
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
	defer s.releaseWithout(client, &err)
	if err != nil {
		return false, err
	}
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
	key := joinIDs(uaid, chid)
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
	key := joinIDs(uaid, chid)
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
func (s *EmceeStore) Update(uaid, chid string, version int64) (err error) {
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
	key := joinIDs(uaid, chid)
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
	defer s.releaseWithout(client, &err)
	if err != nil {
		return err
	}
	key := joinIDs(uaid, chid)
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

	keys := make([]string, len(chids))
	for i, chid := range chids {
		keys[i] = joinIDs(uaid, chid)
	}
	if s.logger.ShouldLog(INFO) {
		s.logger.Info("emcee", "Fetching items", LogFields{
			"uaid":  uaid,
			"items": fmt.Sprintf("[%s]", strings.Join(keys, ", ")),
		})
	}
	client, err := s.getClient()
	defer s.releaseWithout(client, &err)
	if err != nil {
		return nil, nil, err
	}

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
func (s *EmceeStore) DropAll(uaid string) (err error) {
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil && !isMissing(err) {
		return err
	}
	client, err := s.getClient()
	defer s.releaseWithout(client, &err)
	if err != nil {
		return err
	}
	for _, chid := range chids {
		key := joinIDs(uaid, chid)
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
	defer s.releaseWithout(client, &err)
	if err != nil {
		return
	}
	err = client.Get(s.PingPrefix+uaid, &pingData)
	return
}

// PutPing stores the proprietary ping info blob for the given device ID in
// memcached. Implements Store.PutPing().
func (s *EmceeStore) PutPing(uaid string, pingData []byte) (err error) {
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	client, err := s.getClient()
	defer s.releaseWithout(client, &err)
	if err != nil {
		return err
	}
	return client.Set(s.PingPrefix+uaid, pingData, 0)
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements Store.DropPing().
func (s *EmceeStore) DropPing(uaid string) (err error) {
	if len(uaid) == 0 {
		return ErrNoID
	}
	if !id.Valid(uaid) {
		return ErrInvalidID
	}
	client, err := s.getClient()
	defer s.releaseWithout(client, &err)
	if err != nil {
		return err
	}
	return client.Delete(s.PingPrefix+uaid, 0)
}

// Queries memcached for a list of current subscriptions associated with the
// given device ID.
func (s *EmceeStore) fetchChannelIDs(uaid string) (result ChannelIDs, err error) {
	if len(uaid) == 0 {
		return nil, nil
	}
	client, err := s.getClient()
	defer s.releaseWithout(client, &err)
	if err != nil {
		return nil, err
	}
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
func (s *EmceeStore) storeAppIDArray(uaid string, chids ChannelIDs) (err error) {
	if len(uaid) == 0 {
		return ErrNoID
	}
	client, err := s.getClient()
	defer s.releaseWithout(client, &err)
	if err != nil {
		return err
	}
	// sort the array
	sort.Sort(chids)
	return client.Set(uaid, chids, 0)
}

// Retrieves a channel record from memcached. Returns an empty record if the
// channel does not exist.
func (s *EmceeStore) fetchRec(pk string) (result *ChannelRecord, err error) {
	client, err := s.getClient()
	defer s.releaseWithout(client, &err)
	if err != nil {
		return nil, err
	}
	result = new(ChannelRecord)
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
			"pk": pk,
			"result": fmt.Sprintf("state: %s, vers: %d, last: %d",
				result.State, result.Version, result.LastTouched),
		})
	}
	return result, nil
}

// Stores an updated channel record in memcached.
func (s *EmceeStore) storeRec(pk string, rec *ChannelRecord) (err error) {
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
	defer s.releaseWithout(client, &err)
	if err != nil {
		return err
	}
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
	s.cond.L.Lock()
	defer s.cond.L.Unlock()
	defer s.cond.Signal()
	if err != nil {
		if isFatalError(*err) {
			s.capacity--
			if client != nil {
				client.Close()
			}
			return
		}
	}
	if s.capacity > s.MaxConns {
		// Maximum pool size exceeded.
		if client != nil {
			client.Close()
		}
		return
	}
	s.clients.PushBack(client)
}

// Acquires a memcached connection from the connection pool.
func (s *EmceeStore) getClient() (client mc.Client, err error) {
	s.cond.L.Lock()
	for {
		if s.isClosed || s.clients.Len() > 0 || s.capacity < s.MaxConns {
			break
		}
		s.cond.Wait()
	}
	if s.isClosed {
		s.cond.L.Unlock()
		return nil, io.EOF
	}
	if s.clients.Len() > 0 {
		// Return the first available connection from the pool.
		client = s.clients.Remove(s.clients.Front()).(mc.Client)
		s.cond.L.Unlock()
		return client, nil
	}
	// All connections are in use, but the pool has not reached its maximum
	// capacity.
	s.capacity++
	s.cond.L.Unlock()
	return s.newClient()
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
	client.SetBehavior(mc.BEHAVIOR_KETAMA_HASH, 1)
	// Use the binary protocol, which allows us faster data xfer
	// and better data storage (can use full UTF-8 char space)
	client.SetBehavior(mc.BEHAVIOR_BINARY_PROTOCOL, 1)
	if s.connectTimeout > 0 {
		client.SetBehavior(mc.BEHAVIOR_CONNECT_TIMEOUT, s.connectTimeout)
	}
	if s.sendTimeout > 0 {
		client.SetBehavior(mc.BEHAVIOR_SND_TIMEOUT, s.sendTimeout)
	}
	if s.recvTimeout > 0 {
		client.SetBehavior(mc.BEHAVIOR_RCV_TIMEOUT, s.recvTimeout)
	}
	if s.pollTimeout > 0 {
		client.SetBehavior(mc.BEHAVIOR_POLL_TIMEOUT, s.pollTimeout)
	}
	if s.retryTimeout > 0 {
		client.SetBehavior(mc.BEHAVIOR_RETRY_TIMEOUT, s.retryTimeout)
	}
	return client, nil
}

func init() {
	AvailableStores["memcache_gomc"] = func() HasConfigStruct { return NewEmcee() }
}
