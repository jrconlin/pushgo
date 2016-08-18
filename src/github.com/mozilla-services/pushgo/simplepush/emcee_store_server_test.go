// +build memcached_server_test
// +build cgo,libmemcached

/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"

	"github.com/kitcambridge/envconf"
)

// testServer is a memcached-backed test Simple Push server, using the
// gomc driver.
var testServer = &TestServer{
	LogLevel: 0,
	NewStore: func() (store ConfigStore, configStruct interface{}, err error) {
		store = NewEmcee()
		configStruct = store.ConfigStruct()
		env := envconf.Load()
		if err = env.DecodeStrict(toEnvName("test_storage"), EnvSep, configStruct, nil); err != nil {
			return nil, nil, fmt.Errorf("Invalid environment variable: %s", err)
		}
		return store, configStruct, nil
	},
}
