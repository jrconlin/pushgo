/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
// Most of this is copied straight from heka's TOML config setup

package simplepush

import (
	"github.com/bbangert/toml"
	"reflect"
)

// Generic config section with any set of options
type ConfigFile map[string]toml.Primitive

type ConfigSection struct {
	typ         string `toml:"type"`
	tomlSection toml.Primitive
}

// Interface for something that needs a set of config options
type HasConfigStruct interface {
	// Returns a default-value-populated configuration structure into which
	// the plugin's TOML configuration will be deserialized.
	ConfigStruct() interface{}
}

// If `configable` supports the `HasConfigStruct` interface this will use said
// interface to fetch a config struct object and populate it w/ the values in
// provided `config`. If not, simply returns `config` unchanged.
func LoadConfigStruct(config toml.Primitive, configable interface{}) (
	configStruct interface{}, err error) {
	var md toml.MetaData

	// On two lines for scoping reasons.
	hasConfigStruct, ok := configable.(HasConfigStruct)
	if !ok {
		return
	}

	configStruct = hasConfigStruct.ConfigStruct()

	// Global section options
	sg_params := make(map[string]interface{})

	if err = toml.PrimitiveDecodeStrict(config, configStruct, sg_params); err != nil {
		configStruct = nil
		matches := unknownOptionRegex.FindStringSubmatch(err.Error())
		if len(matches) == 2 {
			// We've got an unrecognized config option.
			err = fmt.Errorf("Unknown config setting: %s", matches[1])
		}
	}
	return
}

// Basic global server options
type ServerConfig struct {
	host           string
	port           int
	maxConnections int
	sslCertFile    string `toml:"ssl_cert_file"`
	sslKeyFile     string `toml:"ssl_key_file"`
	pushEndpoint   string `toml:"push_endpoint"`
	pushLongPongs  int    `toml:"push_long_ponts"`
}
