/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"os"
)

type ApplicationConfig struct {
	hostname string `toml:"current_host"`
	host     string
	port     int
}

type Application struct {
	config *ApplicationConfig
	log    *SimpleLogger
}

func (a *Application) ConfigStruct() interface{} {
	defaultHost := os.Hostname()
	return &ApplicationConfig{
		hostname: defaultHost,
		host:     "0.0.0.0",
		port:     8080,
	}
}
