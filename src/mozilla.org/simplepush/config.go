/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
// Most of this is copied straight from heka's TOML config setup

package simplepush

import (
	"fmt"
	"reflect"
	"regexp"

	"github.com/bbangert/toml"
)

// Extensible sections
type AvailableExtensions map[string]func() HasConfigStruct

type ExtensibleGlobals struct {
	Typ string `toml:"type"`
}

// Generic config section with any set of options
type ConfigFile map[string]toml.Primitive

// Interface for something that needs a set of config options
type HasConfigStruct interface {
	// Returns a default-value-populated configuration structure into which
	// the plugin's TOML configuration will be deserialized.
	ConfigStruct() interface{}
	// The configuration loaded after ConfigStruct will be passed in
	// Throwing an error here will cause the application to stop loading
	Init(app *Application, config interface{}) error
}

var unknownOptionRegex = regexp.MustCompile("^Configuration contains key \\[(?P<key>\\S+)\\]")

func LoadConfigStruct(config toml.Primitive, configable HasConfigStruct) (
	configStruct interface{}, err error) {

	configStruct = configable.ConfigStruct()

	// Global section options
	// SimplePush defines some common parameters
	// that are defined in the ExtensibleGlobals struct.
	// Use reflection to extract the ExtensibleGlobals fields or TOML tag
	// name if available
	spParams := make(map[string]interface{})
	pg := ExtensibleGlobals{}
	rt := reflect.ValueOf(pg).Type()
	for i := 0; i < rt.NumField(); i++ {
		sft := rt.Field(i)
		kname := sft.Tag.Get("toml")
		if len(kname) == 0 {
			kname = sft.Name
		}
		spParams[kname] = true
	}

	if err = toml.PrimitiveDecodeStrict(config, configStruct, spParams); err != nil {
		configStruct = nil
		matches := unknownOptionRegex.FindStringSubmatch(err.Error())
		if len(matches) == 2 {
			// We've got an unrecognized config option.
			err = fmt.Errorf("Unknown config setting: %s", matches[1])
		}
	}
	return
}

// Loads the config for a section supplied, configures the supplied object, and initializes
func LoadConfigForSection(app *Application, sectionName string, obj HasConfigStruct,
	configFile ConfigFile) (err error) {

	conf, ok := configFile[sectionName]
	if !ok {
		return fmt.Errorf("Error loading config file, section: %s", sectionName)
	}
	confStruct := obj.ConfigStruct()
	if err = toml.PrimitiveDecode(conf, confStruct); err != nil {
		err = fmt.Errorf("Unable to decode config for section '%s': %s",
			sectionName, err)
		return
	}
	err = obj.Init(app, confStruct)
	return
}

// Load an extensible section that has a type keyword
func LoadExtensibleSection(app *Application, sectionName string,
	extensions AvailableExtensions, configFile ConfigFile) (interface{}, error) {
	var err error

	confSection := new(ExtensibleGlobals)

	conf, ok := configFile[sectionName]
	if !ok {
		return nil, fmt.Errorf("Error loading config file, section: %s", sectionName)
	}

	if err = toml.PrimitiveDecode(conf, confSection); err != nil {
		return nil, err
	}
	ext, ok := extensions[confSection.Typ]
	if !ok {
		return nil, fmt.Errorf("No type '%s' available to load for section '%s'",
			confSection.Typ, sectionName)
	}

	obj := ext()
	loadedConfig, err := LoadConfigStruct(conf, obj)
	if err != nil {
		return nil, err
	}

	err = obj.Init(app, loadedConfig)
	return obj, err
}

// Handles reading a TOML based configuration file, and loading an
// initialized Application, ready to Run
func LoadApplicationFromFileName(filename string) (app *Application, err error) {
	var (
		obj        interface{}
		configFile ConfigFile
	)

	if _, err = toml.DecodeFile(filename, &configFile); err != nil {
		return nil, fmt.Errorf("Error decoding config file: %s", err)
	}

	// We have a somewhat convoluted setup process to ensure prerequisites are
	// available on the Application at each stage of application setup

	// Setup the base application first
	app = new(Application)
	if err = LoadConfigForSection(nil, "default", app, configFile); err != nil {
		return
	}

	// Next, many things require the logger, and logger has no other deps
	if obj, err = LoadExtensibleSection(app, "logging", AvailableLoggers, configFile); err != nil {
		return
	}
	logger := obj.(Logger)
	if err = app.SetLogger(logger); err != nil {
		return
	}

	// Next, metrics, Deps: Logger
	metrics := new(Metrics)
	if err = LoadConfigForSection(app, "metrics", metrics, configFile); err != nil {
		return
	}
	if err = app.SetMetrics(metrics); err != nil {
		return
	}

	// Next, storage, Deps: Logger, Metrics
	if obj, err = LoadExtensibleSection(app, "storage", AvailableStores, configFile); err != nil {
		return
	}
	store := obj.(Store)
	if err = app.SetStore(store); err != nil {
		return
	}

	// Next, setup the router, Deps: Logger, Metrics
	router := new(Router)
	if err = LoadConfigForSection(app, "router", router, configFile); err != nil {
		return
	}
	if err = app.SetRouter(router); err != nil {
		return
	}

	// Finally, setup the handlers, Deps: Logger, Metrics
	serv := new(Serv)
	configStruct := serv.ConfigStruct()
	if err = toml.PrimitiveDecode(configFile["default"], configStruct); err != nil {
		return
	}
	serv.Init(app, configStruct)
	app.SetServer(serv)

	handlers := new(Handler)
	handlers.Init(app, nil)
	app.SetHandlers(handlers)
	return
}
