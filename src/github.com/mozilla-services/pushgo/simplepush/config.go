/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
// Most of this is copied straight from heka's TOML config setup

package simplepush

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/bbangert/toml"
	"github.com/kitcambridge/envconf"
)

// Extensible sections
type AvailableExtensions map[string]func() HasConfigStruct

type ExtensibleGlobals struct {
	Typ string `toml:"type" env:"type"`
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

// The configuration environment variable prefix and section separator.
const (
	EnvPrefix = "pushgo"
	EnvSep    = "_"
)

var unknownOptionRegex = regexp.MustCompile("^Configuration contains key \\[(?P<key>\\S+)\\]")

// Plugin types
type PluginType int

const (
	PluginApp PluginType = iota + 1
	PluginLogger
	PluginPinger
	PluginMetrics
	PluginStore
	PluginRouter
	PluginLocator
	PluginServer
	PluginHandlers
)

var pluginNames = map[PluginType]string{
	PluginApp:      "app",
	PluginLogger:   "logger",
	PluginPinger:   "pinger",
	PluginMetrics:  "metrics",
	PluginStore:    "store",
	PluginLocator:  "locator",
	PluginServer:   "server",
	PluginHandlers: "handlers",
}

func (t PluginType) String() string {
	return pluginNames[t]
}

// Plugin loaders
type PluginLoaders map[PluginType]func(*Application) (HasConfigStruct, error)

func (l PluginLoaders) loadPlugin(plugin PluginType, app *Application) (HasConfigStruct, error) {
	if loader, ok := l[plugin]; ok {
		return loader(app)
	}
	return nil, fmt.Errorf("Missing plugin loader for %s", plugin)
}

func (l PluginLoaders) Load(logging int) (*Application, error) {
	var (
		obj HasConfigStruct
		err error
	)

	// We have a somewhat convoluted setup process to ensure prerequisites are
	// available on the Application at each stage of application setup

	// Setup the base application first
	app := new(Application)
	if obj, err = l.loadPlugin(PluginApp, app); err != nil {
		return nil, err
	}

	// Next, many things require the logger, and logger has no other deps
	if obj, err = l.loadPlugin(PluginLogger, app); err != nil {
		return nil, err
	}
	logger := obj.(Logger)
	if logging > 0 {
		logger.SetFilter(LogLevel(logging))
		logger.Log(LogLevel(logging), "config", "Setting minimum logging level from CLI",
			LogFields{"level": fmt.Sprintf("%d", LogLevel(logging))})
	}
	if err = app.SetLogger(logger); err != nil {
		return nil, err
	}

	// Next, metrics, Deps: Logger
	if obj, err = l.loadPlugin(PluginMetrics, app); err != nil {
		return nil, err
	}
	metrics := obj.(*Metrics)
	if err = app.SetMetrics(metrics); err != nil {
		return nil, err
	}

	// Next, storage, Deps: Logger, Metrics
	if obj, err = l.loadPlugin(PluginStore, app); err != nil {
		return nil, err
	}
	store := obj.(Store)
	if err = app.SetStore(store); err != nil {
		return nil, err
	}

	// Load the Proprietary Ping element. Deps: Logger, Metrics, Storage
	if obj, err = l.loadPlugin(PluginPinger, app); err != nil {
		return nil, err
	}
	propping := obj.(PropPinger)
	if err = app.SetPropPinger(propping); err != nil {
		return nil, err
	}

	// Next, setup the router, Deps: Logger, Metrics
	if obj, err = l.loadPlugin(PluginRouter, app); err != nil {
		return nil, err
	}
	router := obj.(*Router)
	if err = app.SetRouter(router); err != nil {
		return nil, err
	}

	// Set up the node discovery mechanism. Deps: Logger, Metrics, Router.
	if obj, err = l.loadPlugin(PluginLocator, app); err != nil {
		return nil, err
	}
	locator := obj.(Locator)
	if err = router.SetLocator(locator); err != nil {
		return nil, err
	}

	// Finally, setup the handlers, Deps: Logger, Metrics
	if obj, err = l.loadPlugin(PluginServer, app); err != nil {
		return nil, err
	}
	serv := obj.(*Serv)
	app.SetServer(serv)
	if obj, err = l.loadPlugin(PluginHandlers, app); err != nil {
		return nil, err
	}
	handlers := obj.(*Handler)
	app.SetHandlers(handlers)

	return app, nil
}

func LoadConfigStruct(sectionName string, env envconf.Environment,
	config toml.Primitive, configable HasConfigStruct) (
	configStruct interface{}, err error) {

	if configStruct = configable.ConfigStruct(); configStruct == nil {
		return configStruct, nil
	}

	// Global section options
	// SimplePush defines some common parameters
	// that are defined in the ExtensibleGlobals struct.
	// Use reflection to extract the tagged ExtensibleGlobals fields.
	var pg ExtensibleGlobals
	rt := reflect.TypeOf(pg)

	ignoreConfig := make(map[string]interface{}, rt.NumField())
	ignoreEnv := make(map[string]interface{}, rt.NumField())

	for i := 0; i < rt.NumField(); i++ {
		sft := rt.Field(i)
		kname := sft.Tag.Get("toml")
		if len(kname) == 0 {
			kname = sft.Name
		}
		ignoreConfig[kname] = true
		if kname = sft.Tag.Get("env"); len(kname) == 0 {
			kname = sft.Name
		}
		ignoreEnv[toEnvName(sectionName, kname)] = true
	}

	if err = toml.PrimitiveDecodeStrict(config, configStruct, ignoreConfig); err != nil {
		matches := unknownOptionRegex.FindStringSubmatch(err.Error())
		if len(matches) == 2 {
			// We've got an unrecognized config option.
			err = fmt.Errorf("Unknown config setting for section '%s': %s",
				sectionName, matches[1])
		}
		return nil, err
	}

	if err = env.DecodeStrict(toEnvName(sectionName), EnvSep, configStruct, ignoreEnv); err != nil {
		return nil, fmt.Errorf("Invalid environment variable for section '%s': %s",
			sectionName, err)
	}

	return configStruct, nil
}

// Loads the config for a section supplied, configures the supplied object, and initializes
func LoadConfigForSection(app *Application, sectionName string, obj HasConfigStruct,
	env envconf.Environment, configFile ConfigFile) (err error) {

	conf, ok := configFile[sectionName]
	if !ok {
		return fmt.Errorf("Error loading config file, section: %s", sectionName)
	}
	confStruct := obj.ConfigStruct()
	if confStruct == nil {
		return nil
	}

	if err = toml.PrimitiveDecode(conf, confStruct); err != nil {
		return fmt.Errorf("Unable to decode config for section '%s': %s",
			sectionName, err)
	}

	if err = env.Decode(toEnvName(sectionName), EnvSep, confStruct); err != nil {
		return fmt.Errorf("Invalid environment variable for section '%s': %s",
			sectionName, err)
	}

	err = obj.Init(app, confStruct)
	return
}

// Load an extensible section that has a type keyword
func LoadExtensibleSection(app *Application, sectionName string,
	extensions AvailableExtensions, env envconf.Environment,
	configFile ConfigFile) (obj HasConfigStruct, err error) {

	confSection := new(ExtensibleGlobals)

	conf, ok := configFile[sectionName]
	if !ok {
		return nil, fmt.Errorf("Error loading config file, section: %s", sectionName)
	}

	if err = toml.PrimitiveDecode(conf, confSection); err != nil {
		return nil, err
	}
	if err = env.Decode(toEnvName(sectionName), EnvSep, confSection); err != nil {
		return nil, err
	}
	ext, ok := extensions[confSection.Typ]
	if !ok {
		if ext, ok = extensions["default"]; !ok {
			return nil, fmt.Errorf("No type '%s' available to load for section '%s'",
				confSection.Typ, sectionName)
		}
		//TODO: Add log info to indicate using "default"
	}

	obj = ext()
	loadedConfig, err := LoadConfigStruct(sectionName, env, conf, obj)
	if err != nil {
		return nil, err
	}

	err = obj.Init(app, loadedConfig)
	return obj, err
}

// Handles reading a TOML based configuration file, and loading an
// initialized Application, ready to Run
func LoadApplicationFromFileName(filename string, logging int) (app *Application, err error) {
	var configFile ConfigFile

	if _, err = toml.DecodeFile(filename, &configFile); err != nil {
		return nil, fmt.Errorf("Error decoding config file: %s", err)
	}
	env := envconf.Load()

	loaders := PluginLoaders{
		PluginApp: func(app *Application) (HasConfigStruct, error) {
			return nil, LoadConfigForSection(nil, "default", app, env, configFile)
		},
		PluginLogger: func(app *Application) (HasConfigStruct, error) {
			return LoadExtensibleSection(app, "logging", AvailableLoggers, env, configFile)
		},
		PluginPinger: func(app *Application) (HasConfigStruct, error) {
			return LoadExtensibleSection(app, "propping", AvailablePings, env, configFile)
		},
		PluginMetrics: func(app *Application) (HasConfigStruct, error) {
			metrics := new(Metrics)
			if err := LoadConfigForSection(app, "metrics", metrics, env, configFile); err != nil {
				return nil, err
			}
			return metrics, nil
		},
		PluginStore: func(app *Application) (HasConfigStruct, error) {
			return LoadExtensibleSection(app, "storage", AvailableStores, env, configFile)
		},
		PluginRouter: func(app *Application) (HasConfigStruct, error) {
			router := NewRouter()
			if err := LoadConfigForSection(app, "router", router, env, configFile); err != nil {
				return nil, err
			}
			return router, nil
		},
		PluginLocator: func(app *Application) (HasConfigStruct, error) {
			return LoadExtensibleSection(app, "discovery", AvailableLocators, env, configFile)
		},
		PluginServer: func(app *Application) (HasConfigStruct, error) {
			serv := NewServer()
			if err := LoadConfigForSection(app, "default", serv, env, configFile); err != nil {
				return nil, err
			}
			return serv, nil
		},
		PluginHandlers: func(app *Application) (HasConfigStruct, error) {
			handlers := new(Handler)
			if err := handlers.Init(app, nil); err != nil {
				return nil, err
			}
			return handlers, nil
		},
	}

	return loaders.Load(logging)
}

func toEnvName(params ...string) string {
	return strings.Join(append([]string{EnvPrefix}, params...), EnvSep)
}
