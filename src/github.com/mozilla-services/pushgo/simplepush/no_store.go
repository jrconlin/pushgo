/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"time"
)

type NoStoreConfig struct {
	UAIDExists  bool `toml:"uaid_exists" env:"uaid_exists"`
	MaxChannels int  `toml:"max_channels" env:"max_channels"`
}

type NoStore struct {
	logger      *SimpleLogger
	UAIDExists  bool
	maxChannels int
}

func (n *NoStore) KeyToIDs(key string) (suaid, schid string, err error) {
	if suaid, schid, err = splitIDs(key); err != nil {
		if n.logger.ShouldLog(WARNING) {
			n.logger.Warn("nostore", "Invalid key",
				LogFields{"error": err.Error(), "key": key})
		}
		return "", "", ErrInvalidKey
	}
	return
}

func (n *NoStore) IDsToKey(suaid, schid string) (string, error) {
	logWarning := n.logger.ShouldLog(WARNING)
	if len(suaid) == 0 {
		if logWarning {
			n.logger.Warn("nostore", "Missing device ID",
				LogFields{"uaid": suaid, "chid": schid})
		}
		return "", ErrInvalidKey
	}
	if len(schid) == 0 {
		if logWarning {
			n.logger.Warn("nostore", "Missing channel ID",
				LogFields{"uaid": suaid, "chid": schid})
		}
		return "", ErrInvalidKey
	}
	return joinIDs(suaid, schid), nil
}

func (*NoStore) ConfigStruct() interface{} {
	return &NoStoreConfig{
		UAIDExists:  true,
		MaxChannels: 200,
	}
}

func (n *NoStore) Init(app *Application, config interface{}) error {
	n.logger = app.Logger()
	n.maxChannels = config.(*NoStoreConfig).MaxChannels
	return nil
}

func (n *NoStore) CanStore(channels int) bool {
	return channels <= n.maxChannels
}

func (*NoStore) Close() error          { return nil }
func (*NoStore) Status() (bool, error) { return true, nil }

// return true in this case so that registration doesn't cause a new
// UAID to be issued
func (n *NoStore) Exists(uaid string) bool {
	if ok, hasID := hasExistsHook(uaid); hasID {
		return ok
	}
	return n.UAIDExists
}

func (*NoStore) Register(string, string, int64) error                   { return nil }
func (*NoStore) Update(string, string, int64) error                     { return nil }
func (*NoStore) Unregister(string, string) error                        { return nil }
func (*NoStore) Drop(string, string) error                              { return nil }
func (*NoStore) FetchAll(string, time.Time) ([]Update, []string, error) { return nil, nil, nil }
func (*NoStore) DropAll(string) error                                   { return nil }
func (*NoStore) FetchPing(string) ([]byte, error)                       { return nil, nil }
func (*NoStore) PutPing(string, []byte) error                           { return nil }
func (*NoStore) DropPing(string) error                                  { return nil }

func init() {
	AvailableStores["none"] = func() HasConfigStruct {
		return &NoStore{UAIDExists: true}
	}
	AvailableStores.SetDefault("none")
}
