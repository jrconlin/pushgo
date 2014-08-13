package storage

import (
	"fmt"
	"strings"
	"time"
)

func NewNoStore() Adapter {
	return new(NoStore)
}

type NoStore struct{}

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

func (*NoStore) ConfigStruct() interface{}                              { return nil }
func (*NoStore) Init(config interface{}) error                          { return nil }
func (*NoStore) Close() error                                           { return nil }
func (*NoStore) Status() (bool, error)                                  { return true, nil }
func (*NoStore) Exists([]byte) bool                                     { return true }
func (*NoStore) Register([]byte, []byte, int64) error                   { return nil }
func (*NoStore) Update([]byte, []byte, int64) error                     { return nil }
func (*NoStore) Unregister([]byte, []byte) error                        { return nil }
func (*NoStore) Drop([]byte, []byte) error                              { return nil }
func (*NoStore) FetchAll([]byte, time.Time) ([]Update, [][]byte, error) { return nil, nil, nil }
func (*NoStore) DropAll([]byte) error                                   { return nil }
func (*NoStore) FetchPing([]byte) (string, error)                       { return "", nil }
func (*NoStore) PutPing([]byte, string) error                           { return nil }
func (*NoStore) DropPing([]byte) error                                  { return nil }
func (*NoStore) FetchHost([]byte) (string, error)                       { return "", nil }
func (*NoStore) PutHost([]byte, string) error                           { return nil }
func (*NoStore) DropHost([]byte) error                                  { return nil }
