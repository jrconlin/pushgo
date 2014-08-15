/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bufio"
	"bytes"
	"container/list"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mc "github.com/ianoshen/gomc"

	"mozilla.org/simplepush/sperrors"
)

// Channel record status constants.
const (
	DELETED = iota
	LIVE
	REGISTERED
)

// Common adapter errors.
var (
	ErrPoolSaturated      StorageError = "Connection pool saturated"
	ErrStatusFailed       StorageError = "Invalid value returned"
	ErrUnknownUAID        StorageError = "Unknown UAID for host"
	ErrNoElastiCache      StorageError = "ElastiCache returned no endpoints"
	ErrElastiCacheTimeout StorageError = "ElastiCache query timed out"
)

var noWhitespace = strings.NewReplacer(" ", "",
	"\x08", "",
	"\x09", "",
	"\x0a", "",
	"\x0b", "",
	"\x0c", "",
	"\x0d", "")

// cr is a channel record. The short type and field names are used to reduce
// the size of the encoded Gob structure.
type cr struct {
	S int8   //State
	V uint64 // Version
	L int64  // Last touched
}

// ia is a list of decoded channel IDs, so named because the binary Gob encoder
// stores type names.
type ia [][]byte

// Len returns the length of the channel ID slice. Implements
// `sort.Interface.Len()`.
func (channelIDs ia) Len() int {
	return len(channelIDs)
}

// Swap swaps two channel ID slices at the corresponding indices. Implements
// `sort.Interface.Swap()`.
func (channelIDs ia) Swap(i, j int) {
	channelIDs[i], channelIDs[j] = channelIDs[j], channelIDs[i]
}

// Less indicates whether one channel ID slice lexicographically precedes the
// other. Implements `sort.Interface.Less()`.
func (channelIDs ia) Less(i, j int) bool {
	return bytes.Compare(channelIDs[i], channelIDs[j]) < 0
}

// IndexOf returns the location of a channel ID slice in the slice of channel
// IDs, or -1 if the ID isn't present in the containing slice.
func (channelIDs ia) IndexOf(val []byte) int {
	for index, v := range channelIDs {
		if bytes.Equal(v, val) {
			return index
		}
	}
	return -1
}

// Returns a new slice with the string at position pos removed or
// an equivalent slice if the pos is not in the bounds of the slice
func remove(list [][]byte, pos int) (res [][]byte) {
	if pos < 0 || pos == len(list) {
		return list
	}
	return append(list[:pos], list[pos+1:]...)
}

// Use the AWS system to query for the endpoints to use.
// (Allows for dynamic endpoint assignments)
func getElastiCacheEndpoints(configEndpoint string) (string, error) {
	c, err := net.Dial("tcp", configEndpoint)
	if err != nil {
		return "", err
	}
	defer c.Close()

	reader, writer := bufio.NewReader(c), bufio.NewWriter(c)
	writer.Write([]byte("config get cluster\r\n"))
	writer.Flush()

	reader.ReadString('\n')
	reader.ReadString('\n')
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", nil
	}

	endPoints := strings.Split(line, " ")
	if len(endPoints) < 1 {
		return "", ErrNoElastiCache
	}

	var retEndpoints []string
	for _, v := range endPoints {
		endPoint := strings.Split(v, "|")
		if len(endPoint) < 3 {
			continue
		}
		retEndpoints = append(retEndpoints, fmt.Sprintf("%s:%s", endPoint[1], strings.TrimSpace(endPoint[2])))
	}
	return strings.Join(retEndpoints, ","), nil
}

func getElastiCacheEndpointsTimeout(configEndpoint string, seconds int) (string, error) {
	type strErr struct {
		ep  string
		err error
	}

	ch := make(chan strErr, 1)

	go func() {
		ep, err := getElastiCacheEndpoints(configEndpoint)
		ch <- strErr{ep, err}
	}()
	select {
	case se := <-ch:
		return se.ep, se.err
	case <-time.After(time.Duration(seconds) * time.Second):
		return "", ErrElastiCacheTimeout
	}

}

// Determines whether the given error is a memcached "missing key" error.
func isMissing(err error) bool {
	return strings.Contains("NOT FOUND", err.Error())
}

// Converts a `(uaid, chid)` tuple to a binary primary key.
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

// FreeClient wraps a memcached connection with pool information.
type FreeClient struct {
	mc.Client
	releases chan *FreeClient
}

// NewEmcee creates an unconfigured memcached adapter.
func NewEmcee() *EmceeStore {
	s := &EmceeStore{
		closeSignal:  make(chan bool),
		clients:      make(chan mc.Client),
		releases:     make(chan *FreeClient),
		acquisitions: make(chan chan *FreeClient),
	}
	s.closeWait.Add(1)
	go s.run()
	return s
}

// EmceeDriverConf specifies memcached driver options.
type EmceeDriverConf struct {
	// Hosts is a list of memcached nodes.
	Hosts []string `toml:"server"`

	// MinConns is the desired number of initial connections. Defaults to 100.
	MinConns int `toml:"pool_size"`

	// MaxConns is the maximum number of open connections managed by the pool.
	// All returned connections that exceed this limit will be closed. Defaults
	// to 400.
	MaxConns int `toml:"max_pool"`

	// RecvTimeout is the socket receive timeout (`SO_RCVTIMEO`) used by the
	// memcached driver. Supports microsecond granularity; defaults to 5 seconds.
	RecvTimeout string `toml:"recv_timeout"`

	// SendTimeout is the socket send timeout (`SO_SNDTIMEO`) used by the
	// memcached driver. Supports microsecond granularity; defaults to 5 seconds.
	SendTimeout string `toml:"send_timeout"`

	// PollTimeout is the `poll()` timeout used by the memcached driver. Supports
	// millisecond granularity; defaults to 5 seconds.
	PollTimeout string `toml:"poll_timeout"`

	// RetryTimeout is the time to wait before retrying a request on an unhealthy
	// memcached node. Supports second granularity; defaults to 5 seconds.
	RetryTimeout string `toml:"retry_timeout"`
}

// EmceeStore is a memcached adapter.
type EmceeStore struct {
	Hosts         []string
	MinConns      int
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
	clients       chan mc.Client
	releases      chan *FreeClient
	acquisitions  chan chan *FreeClient
	lastErr       error
}

// EmceeConf specifies memcached adapter options.
type EmceeConf struct {
	ElastiCacheConfigEndpoint string          `toml:"elasticache_config_endpoint"`
	Driver                    EmceeDriverConf `toml:"memcache"`
	Db                        DbConf
}

type emceeTimeout string

// Timeout configuration field names, used by `parseTimeout()` for logging and
// providing default values.
const (
	EmceeRecv   emceeTimeout = "storage.memcache.recv_timeout"
	EmceeSend   emceeTimeout = "storage.memcache.send_timeout"
	EmceePoll   emceeTimeout = "storage.memcache.poll_timeout"
	EmceeRetry  emceeTimeout = "storage.memcache.retry_timeout"
	EmceeHandle emceeTimeout = "storage.db.handle_timeout"
)

// Default adapter timeouts.
var defaultEmceeTimeouts = map[emceeTimeout]time.Duration{
	EmceeRecv:   1 * time.Second,
	EmceeSend:   1 * time.Second,
	EmceePoll:   10 * time.Millisecond,
	EmceeRetry:  1 * time.Second,
	EmceeHandle: 5 * time.Second,
}

// Parses a timeout configuration field value, returning a default value if
// the field's contents could not be parsed.
func parseTimeout(kind emceeTimeout, val string, logger *SimpleLogger) (t time.Duration) {
	t, err := time.ParseDuration(val)
	if err != nil {
		logger.Error("emcee", fmt.Sprintf("Could not parse %s", kind),
			LogFields{"error": err.Error()})
		return defaultEmceeTimeouts[kind]
	}
	return
}

// ConfigStruct returns a configuration object with defaults. Implements
// `HasConfigStruct.ConfigStruct()`.
func (*EmceeStore) ConfigStruct() interface{} {
	return &EmceeConf{
		Driver: EmceeDriverConf{
			Hosts:        []string{"127.0.0.1:11211"},
			MinConns:     100,
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
			MaxChannels:   200,
		},
	}
}

// Init initializes the memcached adapter with the given configuration and
// seeds the pool with `MinConns` connections. Implements
// `HasConfigStruct.Init()`.
func (s *EmceeStore) Init(app *Application, config interface{}) error {
	conf := config.(*EmceeConf)
	s.logger = app.Logger()
	s.defaultHost = app.Hostname()
	if len(conf.ElastiCacheConfigEndpoint) == 0 {
		s.Hosts = conf.Driver.Hosts
	} else {
		memcacheEndpoint, err := getElastiCacheEndpointsTimeout(conf.ElastiCacheConfigEndpoint, 2)
		if err != nil {
			s.logger.Error("storage", "Elastisearch error.",
				LogFields{"error": err.Error()})
			return err
		}
		// do NOT include any spaces
		s.Hosts = strings.Split(noWhitespace.Replace(memcacheEndpoint), ",")
	}

	s.MinConns = conf.Driver.MinConns
	s.MaxConns = conf.Driver.MaxConns
	s.maxChannels = conf.Db.MaxChannels
	s.PingPrefix = conf.Db.PingPrefix
	s.HandleTimeout = parseTimeout(EmceeHandle, conf.Db.HandleTimeout, s.logger)

	// The send and receive timeouts are expressed in microseconds.
	s.recvTimeout = uint64(parseTimeout(EmceeRecv, conf.Driver.RecvTimeout, s.logger) / time.Microsecond)
	s.sendTimeout = uint64(parseTimeout(EmceeSend, conf.Driver.SendTimeout, s.logger) / time.Microsecond)
	// `poll(2)` accepts a millisecond timeout.
	s.pollTimeout = uint64(parseTimeout(EmceePoll, conf.Driver.PollTimeout, s.logger) / time.Millisecond)
	// The memcached retry timeout is expressed in seconds.
	s.retryTimeout = uint64(parseTimeout(EmceeRetry, conf.Driver.RetryTimeout, s.logger) / time.Second)

	s.TimeoutLive = time.Duration(conf.Db.TimeoutLive) * time.Second
	s.TimeoutReg = time.Duration(conf.Db.TimeoutReg) * time.Second
	s.TimeoutDel = time.Duration(conf.Db.TimeoutReg) * time.Second

	for index := 0; index < s.MinConns; index++ {
		client, err := s.newClient()
		if err != nil {
			s.fatal(err)
			return err
		}
		s.clients <- client
	}
	return nil
}

// MaxChannels returns the maximum number of channel registrations allowed per
// client. Implements `Store.MaxChannels()`.
func (s *EmceeStore) MaxChannels() int {
	return s.maxChannels
}

// Close closes the connection pool and unblocks all pending operations with
// errors. Safe to call multiple times. Implements `Store.Close()`.
func (s *EmceeStore) Close() (err error) {
	err, ok := s.stop()
	if !ok {
		return err
	}
	s.closeWait.Wait()
	return
}

// KeyToIDs extracts the hex-encoded device and channel IDs from a user-
// readable primary key. Implements `Store.KeyToIDs()`.
func (*EmceeStore) KeyToIDs(key string) (suaid, schid string, ok bool) {
	items := strings.SplitN(key, ".", 2)
	if len(items) < 2 {
		return "", "", false
	}
	return items[0], items[1], true
}

// IDsToKey generates a user-readable primary key from a (device ID, channel
// ID) tuple. The primary key is encoded in the push endpoint URI. Implements
// `Store.IDsToKey()`.
func (*EmceeStore) IDsToKey(suaid, schid string) (string, bool) {
	if len(suaid) == 0 || len(schid) == 0 {
		return "", false
	}
	return fmt.Sprintf("%s.%s", suaid, schid), true
}

// Status queries whether memcached is available for reading and writing.
// Implements `Store.Status()`.
func (s *EmceeStore) Status() (success bool, err error) {
	defer func() {
		if recv := recover(); recv != nil {
			success = false
			err = recv.(error)
			return
		}
	}()
	fakeID, _ := GenUUID4()
	key := "status_" + fakeID
	client, err := s.getClient()
	if err != nil {
		return false, err
	}
	defer s.releaseClient(client)
	err = client.Set(key, "test", 6*time.Second)
	if err != nil {
		return false, err
	}
	var val string
	err = client.Get(key, &val)
	if err != nil || val != "test" {
		return false, ErrStatusFailed
	}
	client.Delete(key, 0)
	return true, nil
}

// Exists returns a Boolean indicating whether a device has previously
// registered with the Simple Push server. Implements `Store.Exists()`.
func (s *EmceeStore) Exists(suaid string) bool {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return false
	}
	_, err = s.fetchAppIDArray(uaid)
	return err == nil
}

// Stores a new channel record in memcached.
func (s *EmceeStore) storeRegister(uaid, chid []byte, version int64) error {
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
	rec := &cr{
		S: REGISTERED,
		L: time.Now().UTC().Unix()}
	if version != 0 {
		rec.V = uint64(version)
		rec.S = LIVE
	}
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return sperrors.InvalidPrimaryKeyError
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
func (s *EmceeStore) Register(suaid, schid string, version int64) (err error) {
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	var uaid, chid []byte
	if uaid, err = DecodeID(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = DecodeID(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	return s.storeRegister(uaid, chid, version)
}

// Updates a channel record in memcached.
func (s *EmceeStore) storeUpdate(uaid, chid []byte, version int64) error {
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return sperrors.InvalidPrimaryKeyError
	}
	keyString := hex.EncodeToString(key)
	cRec, err := s.fetchRec(key)
	if err != nil && !isMissing(err) {
		s.logger.Error("emcee", "Update error", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
		return err
	}
	if cRec != nil {
		s.logger.Debug("emcee", "Replacing record", LogFields{"primarykey": keyString})
		if cRec.S != DELETED {
			newRecord := &cr{
				V: uint64(version),
				S: LIVE,
				L: time.Now().UTC().Unix()}
			err = s.storeRec(key, newRecord)
			if err != nil {
				return err
			}
			return nil
		}
	}
	// No record found or the record setting was DELETED
	s.logger.Debug("emcee", "Registering channel", LogFields{
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
func (s *EmceeStore) Update(key string, version int64) (err error) {
	suaid, schid, ok := s.KeyToIDs(key)
	if !ok {
		return sperrors.InvalidPrimaryKeyError
	}
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	// Normalize the device and channel IDs.
	var uaid, chid []byte
	if uaid, err = DecodeID(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = DecodeID(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	return s.storeUpdate(uaid, chid, version)
}

// Marks a memcached channel record as expired.
func (s *EmceeStore) storeUnregister(uaid, chid []byte) error {
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil {
		return err
	}
	pos := chids.IndexOf(chid)
	if pos < 0 {
		return sperrors.InvalidChannelError
	}
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return err
	}
	if err := s.storeAppIDArray(uaid, remove(chids, pos)); err != nil {
		return err
	}
	// TODO: Allow `MaxRetries` to be configurable.
	for x := 0; x < 3; x++ {
		channel, err := s.fetchRec(key)
		if err != nil {
			s.logger.Warn("emcee", "Could not delete Channel", LogFields{
				"primarykey": hex.EncodeToString(key),
				"error":      err.Error(),
			})
			continue
		}
		channel.S = DELETED
		err = s.storeRec(key, channel)
		break
	}
	// TODO: Propagate errors.
	return nil
}

// Unregister marks the channel ID associated with the given device ID
// as inactive. Implements `Store.Unregister()`.
func (s *EmceeStore) Unregister(suaid, schid string) (err error) {
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	var uaid, chid []byte
	if uaid, err = DecodeID(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = DecodeID(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	return s.storeUnregister(uaid, chid)
}

// Drop removes a channel ID associated with the given device ID from
// memcached. Deregistration calls should use `Unregister()` instead.
// Implements `Store.Drop()`.
func (s *EmceeStore) Drop(suaid, schid string) (err error) {
	if len(schid) == 0 {
		return sperrors.NoChannelError
	}
	var uaid, chid []byte
	if uaid, err = DecodeID(suaid); err != nil || len(uaid) == 0 {
		return sperrors.InvalidDataError
	}
	if chid, err = DecodeID(schid); err != nil || len(chid) == 0 {
		return sperrors.InvalidChannelError
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseClient(client)
	key, err := toBinaryKey(uaid, chid)
	if err != nil {
		return err
	}
	err = client.Delete(encodeKey(key), 0)
	if err == nil || isMissing(err) {
		return nil
	}
	return err
}

// FetchAll returns all channel updates and expired channels for a device ID
// since the specified cutoff time. Implements `Store.FetchAll()`.
func (s *EmceeStore) FetchAll(suaid string, since time.Time) ([]Update, []string, error) {
	if len(suaid) == 0 {
		return nil, nil, sperrors.InvalidDataError
	}
	uaid, err := DecodeID(suaid)
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
	s.logger.Debug("emcee", "Fetching items", LogFields{
		"uaid":  deviceString,
		"items": fmt.Sprintf("[%s]", strings.Join(keys, ", ")),
	})
	client, err := s.getClient()
	if err != nil {
		return nil, nil, err
	}
	defer s.releaseClient(client)

	sinceUnix := since.Unix()
	for index, key := range keys {
		channel := new(cr)
		if err := client.Get(key, channel); err != nil {
			continue
		}
		chid := chids[index]
		channelString := hex.EncodeToString(chid)
		s.logger.Debug("emcee", "FetchAll Fetched record ", LogFields{
			"uaid":  deviceString,
			"chid":  channelString,
			"value": fmt.Sprintf("%d,%d,%d", channel.L, channel.S, channel.V),
		})
		if channel.L < sinceUnix {
			s.logger.Debug("emcee", "Skipping record...", LogFields{
				"uaid": deviceString,
				"chid": channelString,
			})
			continue
		}
		// Yay! Go translates numeric interface values as float64s
		// Apparently float64(1) != int(1).
		switch channel.S {
		case LIVE:
			version := channel.V
			if version == 0 {
				version = uint64(time.Now().UTC().Unix())
				s.logger.Debug("emcee", "FetchAll Using Timestamp", LogFields{
					"uaid": deviceString,
					"chid": channelString,
				})
			}
			update := Update{
				ChannelID: channelString,
				Version:   version,
			}
			updates = append(updates, update)
		case DELETED:
			s.logger.Debug("emcee", "FetchAll Deleting record", LogFields{
				"uaid": deviceString,
				"chid": channelString,
			})
			schid, err := EncodeID(chid)
			if err != nil {
				s.logger.Warn("emcee", "FetchAll Failed to encode channel ID", LogFields{
					"uaid": deviceString,
					"chid": channelString,
				})
				continue
			}
			expired = append(expired, schid)
		case REGISTERED:
			// Item registered, but not yet active. Ignore it.
		default:
			s.logger.Warn("emcee", "Unknown state", LogFields{
				"uaid": deviceString,
				"chid": channelString,
			})
		}
	}
	return updates, expired, nil
}

// DropAll removes all channel records for the given device ID. Implements
// `Store.DropAll()`.
func (s *EmceeStore) DropAll(suaid string) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return err
	}
	chids, err := s.fetchAppIDArray(uaid)
	if err != nil {
		return err
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseClient(client)
	for _, chid := range chids {
		key, err := toBinaryKey(uaid, chid)
		if err != nil {
			return err
		}
		client.Delete(encodeKey(key), 0)
	}
	if err = client.Delete(encodeKey(uaid), 0); err != nil && !isMissing(err) {
		return err
	}
	return nil
}

// FetchPing retrieves proprietary ping information for the given device ID
// from memcached. Implements `Store.FetchPing()`.
func (s *EmceeStore) FetchPing(suaid string) (connect string, err error) {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return "", sperrors.InvalidDataError
	}
	client, err := s.getClient()
	if err != nil {
		return
	}
	defer s.releaseClient(client)
	err = client.Get(s.PingPrefix+hex.EncodeToString(uaid), &connect)
	return
}

// PutPing stores the proprietary ping info blob for the given device ID in
// memcached. Implements `Store.PutPing()`.
func (s *EmceeStore) PutPing(suaid string, connect string) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return err
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseClient(client)
	return client.Set(s.PingPrefix+hex.EncodeToString(uaid), connect, 0)
}

// DropPing removes all proprietary ping info for the given device ID.
// Implements `Store.DropPing()`.
func (s *EmceeStore) DropPing(suaid string) error {
	uaid, err := DecodeID(suaid)
	if err != nil {
		return sperrors.InvalidDataError
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseClient(client)
	return client.Delete(s.PingPrefix+hex.EncodeToString(uaid), 0)
}

// Queries memcached for a list of current subscriptions associated with the
// given device ID.
func (s *EmceeStore) fetchAppIDArray(uaid []byte) (result ia, err error) {
	if len(uaid) == 0 {
		return nil, nil
	}
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	defer s.releaseClient(client)
	err = client.Get(encodeKey(uaid), &result)
	if err != nil {
		// TODO: Returning successful responses for missing keys causes `Exists()` to
		// return `true` for all device IDs. Verify if correcting this behavior
		// breaks existing clients.
		if isMissing(err) {
			return nil, nil
		}
		return nil, err
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
// `arr` is sorted in-place.
func (s *EmceeStore) storeAppIDArray(uaid []byte, arr ia) error {
	if len(uaid) == 0 {
		return sperrors.MissingDataError
	}
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseClient(client)
	// sort the array
	sort.Sort(arr)
	err = client.Set(encodeKey(uaid), arr, 0)
	if err != nil {
		return err
	}
	return nil
}

// Retrieves a channel record from memcached.
func (s *EmceeStore) fetchRec(pk []byte) (*cr, error) {
	if len(pk) == 0 {
		return nil, sperrors.InvalidPrimaryKeyError
	}
	keyString := encodeKey(pk)
	defer func() {
		if err := recover(); err != nil {
			s.logger.Error("emcee", "could not fetch record", LogFields{
				"primarykey": keyString,
				"error":      err.(error).Error(),
			})
		}
	}()
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	defer s.releaseClient(client)
	result := new(cr)
	err = client.Get(keyString, result)
	if err != nil && !isMissing(err) {
		s.logger.Error("emcee", "Get Failed", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
		return nil, err
	}
	s.logger.Debug("emcee", "Fetched", LogFields{
		"primarykey": keyString,
		"result":     fmt.Sprintf("state: %d, vers: %d, last: %d", result.S, result.V, result.L),
	})
	return result, nil
}

// Stores an updated channel record in memcached.
func (s *EmceeStore) storeRec(pk []byte, rec *cr) error {
	if len(pk) == 0 {
		return sperrors.InvalidPrimaryKeyError
	}
	if rec == nil {
		return sperrors.NoDataToStoreError
	}
	var ttl time.Duration
	switch rec.S {
	case DELETED:
		ttl = s.TimeoutDel
	case REGISTERED:
		ttl = s.TimeoutReg
	default:
		ttl = s.TimeoutLive
	}
	rec.L = time.Now().UTC().Unix()
	client, err := s.getClient()
	if err != nil {
		return err
	}
	defer s.releaseClient(client)
	keyString := encodeKey(pk)
	err = client.Set(keyString, rec, ttl)
	if err != nil {
		s.logger.Warn("emcee", "Failure to set item", LogFields{
			"primarykey": keyString,
			"error":      err.Error(),
		})
	}
	return nil
}

// Releases an acquired memcached connection. TODO: The run loop should
// ensure that the `FreeClient` is in a valid state. If a connection error
// occurs, the client should be closed and a new connection opened as needed.
// Otherwise, `getClient()` may return a bad client connection.
func (s *EmceeStore) releaseClient(client *FreeClient) {
	if client == nil {
		return
	}
	client.releases <- client
}

// Acquires a memcached connection from the connection pool.
func (s *EmceeStore) getClient() (*FreeClient, error) {
	freeClients := make(chan *FreeClient)
	select {
	case <-s.closeSignal:
		return nil, io.EOF
	case s.acquisitions <- freeClients:
		if client := <-freeClients; client != nil {
			return client, nil
		}
	case <-time.After(s.HandleTimeout):
	}
	return nil, ErrPoolSaturated
}

// Creates and configures a memcached client connection.
func (s *EmceeStore) newClient() (mc.Client, error) {
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
		case client := <-s.clients:
			if capacity >= s.MaxConns {
				client.Close()
				break
			}
			clients.PushBack(&FreeClient{client, s.releases})
			capacity++

		case freeClient := <-s.releases:
			if capacity >= s.MaxConns {
				// Maximum pool size exceeded; close the connection.
				freeClient.Close()
				break
			}
			clients.PushBack(freeClient)

		case acquisition := <-s.acquisitions:
			if clients.Len() > 0 {
				// Return the first available connection from the pool.
				if client, ok := clients.Remove(clients.Front()).(*FreeClient); ok {
					acquisition <- client
				}
				close(acquisition)
				break
			}
			if capacity < s.MaxConns {
				// All connections are in use, but the pool has not reached its maximum
				// capacity. The caller should call `s.releaseClient()` to return the
				// connection to the pool. TODO: Spawning a separate Goroutine to handle
				// connections would avoid blocking the run loop.
				client, err := s.newClient()
				if err != nil {
					s.fatal(err)
					close(acquisition)
					break
				}
				acquisition <- &FreeClient{client, s.releases}
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
		if client, ok := element.Value.(*FreeClient); ok {
			client.Close()
		}
	}
}

// Acquires `s.closeLock`, closes the pool, and releases the lock, reporting
// any errors to the caller. `ok` indicates whether the caller should wait
// for the pool to close before returning.
func (s *EmceeStore) stop() (err error, ok bool) {
	defer s.closeLock.Unlock()
	s.closeLock.Lock()
	if s.isClosing {
		return s.lastErr, false
	}
	return s.signalClose(), true
}

// Acquires `s.closeLock`, closes the connection pool, and releases the lock,
// storing the given error in the `lastErr` field.
func (s *EmceeStore) fatal(err error) {
	defer s.closeLock.Unlock()
	s.closeLock.Lock()
	s.signalClose()
	if s.lastErr == nil {
		s.lastErr = err
	}
}

// Closes the pool and exits the run loop. Assumes the caller holds
// `s.closeLock`.
func (s *EmceeStore) signalClose() (err error) {
	if s.isClosing {
		return
	}
	close(s.closeSignal)
	s.isClosing = true
	return nil
}

func init() {
	AvailableStores["memcache"] = func() HasConfigStruct { return NewEmcee() }
}
