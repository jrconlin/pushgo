// +build !gomemc_server_test

/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

// Server is a test Simple Push server without storage.
var Server = &TestServer{
	LogLevel: 0,
	NewStore: func() (store ConfigStore, configStruct interface{}, err error) {
		// UAIDExists will return "false" for registration checks for UAID
		// This is the default behavior for most storage engines.
		// Note that Loop (which also uses NoStore) will return "true"
		// for this, to prevent the UAID from being reassigned.
		store = &NoStore{UAIDExists: false}
		configStruct = store.ConfigStruct()
		return store, configStruct, nil
	},
}
