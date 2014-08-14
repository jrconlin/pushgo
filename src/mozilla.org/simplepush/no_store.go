/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"strings"
	"time"
)

// NoStoreConfig is required for reflection; the TOML parser will panic if
// `NoStore.ConfigStruct()` returns `nil`.
type NoStoreConfig struct{}

type NoStore struct {
	logger *SimpleLogger
}

func (*NoStore) KeyToIDs(key string) (suaid, schid string, ok bool) {
	items := strings.SplitN(key, ".", 2)
	if ok = len(items) == 2; ok {
		suaid, schid = items[0], items[1]
	}
	return
}

func (*NoStore) IDsToKey(suaid, schid string) (key string, ok bool) {
	if ok = len(suaid) > 0 && len(schid) > 0; ok {
		key = fmt.Sprintf("%s.%s", suaid, schid)
	}
	return
}

func (*NoStore) ConfigStruct() interface{} { return new(NoStoreConfig) }
func (n *NoStore) Init(app *Application, config interface{}) error {
	n.logger = app.Logger()
	return nil
}

func (*NoStore) MaxChannels() int                                       { return 0 }
func (*NoStore) Close() error                                           { return nil }
func (*NoStore) Status() (bool, error)                                  { return true, nil }
func (*NoStore) Exists(string) bool                                     { return true }
func (*NoStore) Register(string, string, int64) error                   { return nil }
func (*NoStore) Update(string, int64) error                             { return nil }
func (*NoStore) Unregister(string, string) error                        { return nil }
func (*NoStore) Drop(string, string) error                              { return nil }
func (*NoStore) FetchAll(string, time.Time) ([]Update, []string, error) { return nil, nil, nil }
func (*NoStore) DropAll(string) error                                   { return nil }
func (*NoStore) FetchPing(string) (string, error)                       { return "", nil }
func (*NoStore) PutPing(string, string) error                           { return nil }
func (*NoStore) DropPing(string) error                                  { return nil }
func (*NoStore) FetchHost(string) (string, error)                       { return "", nil }
func (*NoStore) PutHost(string, string) error                           { return nil }
func (*NoStore) DropHost(string) error                                  { return nil }

func init() {
	AvailableStores["none"] = func() HasConfigStruct { return new(NoStore) }
}
