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

// Get returns an extension with the given name, or the default
// extension if one has not been registered.
func (e AvailableExtensions) Get(name string) (
	ext func() HasConfigStruct, ok bool) {

	if ext, ok = e[name]; !ok {
		ext, ok = e["default"]
	}
	return
}

// SetDefault sets the extension with the given name as the default. If a
// default has already been set or the extension has not been registered,
// SetDefault panics.
func (e AvailableExtensions) SetDefault(name string) {
	ext, ok := e[name]
	if !ok {
		panic(fmt.Sprintf("AvailableExtensions: '%s' not registered", name))
	}
	if _, ok = e["default"]; ok {
		panic("AvailableExtensions: Default already set")
	}
	e["default"] = ext
}

type ExtensibleGlobals struct {
	Typ string `toml:"type" env:"type"`
}

// Generic config section with any set of options
type ConfigFile map[string]toml.Primitive

// Interface for something that needs a set of config options
type HasConfigStruct interface {
	HasInit
	// Returns a default-value-populated configuration structure into which
	// the plugin's TOML configuration will be deserialized.
	ConfigStruct() interface{}
}

// Interface for something that can be initialized
type HasInit interface {
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
	PluginBalancer
	PluginServer
	PluginSocket
	PluginEndpoint
	PluginHealth
	PluginProfile
)

var pluginNames = map[PluginType]string{
	PluginApp:      "app",
	PluginLogger:   "logger",
	PluginPinger:   "pinger",
	PluginMetrics:  "metrics",
	PluginStore:    "store",
	PluginRouter:   "router",
	PluginLocator:  "locator",
	PluginBalancer: "balancer",
	PluginServer:   "server",
	PluginSocket:   "socket",
	PluginEndpoint: "endpoint",
	PluginHealth:   "health",
	PluginProfile:  "profile",
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
	app := NewApplication()
	if _, err = l.loadPlugin(PluginApp, app); err != nil {
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

	// Next, metrics.
	// Deps: PluginLogger.
	if obj, err = l.loadPlugin(PluginMetrics, app); err != nil {
		return nil, err
	}
	metrics := obj.(Statistician)
	if err = app.SetMetrics(metrics); err != nil {
		return nil, err
	}

	// Next, storage.
	// Deps: PluginLogger.
	if obj, err = l.loadPlugin(PluginStore, app); err != nil {
		return nil, err
	}
	store := obj.(Store)
	if err = app.SetStore(store); err != nil {
		return nil, err
	}

	// Load the Proprietary Ping element.
	// Deps: PluginLogger, PluginMetrics, PluginStore.
	if obj, err = l.loadPlugin(PluginPinger, app); err != nil {
		return nil, err
	}
	propping := obj.(PropPinger)
	if err = app.SetPropPinger(propping); err != nil {
		return nil, err
	}

	// Next, setup the router.
	// Deps: PluginLogger, PluginMetrics.
	if obj, err = l.loadPlugin(PluginRouter, app); err != nil {
		return nil, err
	}
	router := obj.(Router)
	if err = app.SetRouter(router); err != nil {
		return nil, err
	}

	// Set up the node discovery mechanism.
	// Deps: PluginLogger, PluginMetrics, PluginRouter.
	if obj, err = l.loadPlugin(PluginLocator, app); err != nil {
		return nil, err
	}
	locator := obj.(Locator)
	if err = app.SetLocator(locator); err != nil {
		return nil, err
	}

	// Set up the server.
	// Deps: PluginLogger, PluginMetrics, PluginStore, PluginPinger.
	if obj, err = l.loadPlugin(PluginServer, app); err != nil {
		return nil, err
	}
	serv := obj.(Server)
	app.SetServer(serv)

	// Set up the WebSocket handler.
	// Deps: PluginLogger, PluginMetrics, PluginStore, PluginServer.
	if obj, err = l.loadPlugin(PluginSocket, app); err != nil {
		return nil, err
	}
	sh := obj.(Handler)
	app.SetSocketHandler(sh)

	// Set up the balancer.
	// Deps: PluginLogger, PluginMetrics, PluginServer.
	if obj, err = l.loadPlugin(PluginBalancer, app); err != nil {
		return nil, err
	}
	balancer := obj.(Balancer)
	if err = app.SetBalancer(balancer); err != nil {
		return nil, err
	}

	// Set up the HTTP update handler.
	// Deps: PluginLogger, PluginMetrics, PluginStore, PluginRouter,
	// PluginPinger, PluginBalancer, PluginServer.
	if obj, err = l.loadPlugin(PluginEndpoint, app); err != nil {
		return nil, err
	}
	eh := obj.(Handler)
	app.SetEndpointHandler(eh)

	// Attach the health handlers to PluginSocket and PluginEndpoint.
	// Loaded for side effects only.
	if _, err = l.loadPlugin(PluginHealth, app); err != nil {
		return nil, err
	}

	// Set up the performance profiling handlers.
	// Deps: PluginLogger.
	if obj, err = l.loadPlugin(PluginProfile, app); err != nil {
		return nil, err
	}
	ph := obj.(Handler)
	app.SetProfileHandlers(ph)

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

// Applies environment variable overrides to confStruct and initializes obj
func LoadConfigFromEnvironment(app *Application, sectionName string,
	obj HasInit, env envconf.Environment, confStruct interface{}) (err error) {

	if confStruct == nil {
		return nil
	}
	if err = env.Decode(toEnvName(sectionName), EnvSep, confStruct); err != nil {
		return fmt.Errorf("Invalid environment variable for section '%s': %s",
			sectionName, err)
	}
	return obj.Init(app, confStruct)
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

	return LoadConfigFromEnvironment(app, sectionName, obj, env, confStruct)
}

// Load an extensible section that has a type keyword
func LoadExtensibleSection(app *Application, sectionName string,
	extensions AvailableExtensions, env envconf.Environment,
	configFile ConfigFile) (obj HasConfigStruct, err error) {

	confSection := new(ExtensibleGlobals)

	conf, ok := configFile[sectionName]
	if !ok {
		return nil, fmt.Errorf("Missing section '%s'", sectionName)
	}

	if err = toml.PrimitiveDecode(conf, confSection); err != nil {
		return nil, err
	}
	if err = env.Decode(toEnvName(sectionName), EnvSep, confSection); err != nil {
		return nil, err
	}
	ext, ok := extensions.Get(confSection.Typ)
	if !ok {
		//TODO: Add log info to indicate using "default"
		return nil, fmt.Errorf("No type '%s' available to load for section '%s'",
			confSection.Typ, sectionName)
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
func LoadApplicationFromFileName(filename string, logging int) (
	app *Application, err error) {

	var configFile ConfigFile
	if _, err = toml.DecodeFile(filename, &configFile); err != nil {
		return nil, fmt.Errorf("Error decoding config file: %s", err)
	}
	env := envconf.Load()
	return LoadApplication(configFile, env, logging)
}

func LoadApplication(configFile ConfigFile, env envconf.Environment,
	logging int) (app *Application, err error) {

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
			return LoadExtensibleSection(app, "router", AvailableRouters, env, configFile)
		},
		PluginLocator: func(app *Application) (HasConfigStruct, error) {
			return LoadExtensibleSection(app, "discovery", AvailableLocators, env, configFile)
		},
		PluginBalancer: func(app *Application) (HasConfigStruct, error) {
			return LoadExtensibleSection(app, "balancer", AvailableBalancers, env, configFile)
		},
		PluginServer: func(app *Application) (HasConfigStruct, error) {
			serv := NewServer()
			if err := LoadConfigForSection(app, "default", serv, env, configFile); err != nil {
				return nil, err
			}
			return serv, nil
		},
		PluginSocket: func(app *Application) (HasConfigStruct, error) {
			h := NewSocketHandler()
			if err := LoadConfigForSection(app, "websocket", h, env, configFile); err != nil {
				return nil, err
			}
			return h, nil
		},
		PluginEndpoint: func(app *Application) (HasConfigStruct, error) {
			h := NewEndpointHandler()
			if err := LoadConfigForSection(app, "endpoint", h, env, configFile); err != nil {
				return nil, err
			}
			return h, nil
		},
		PluginHealth: func(app *Application) (HasConfigStruct, error) {
			h := NewHealthHandlers()
			if err := h.Init(app, h.ConfigStruct()); err != nil {
				return nil, err
			}
			return h, nil
		},
		PluginProfile: func(app *Application) (plugin HasConfigStruct, err error) {
			h := new(ProfileHandlers)
			sectionName := "profile"
			if _, ok := configFile[sectionName]; ok {
				// Performance profiling is optional and disabled by default.
				err = LoadConfigForSection(app, sectionName, h, env, configFile)
			} else {
				confStruct := h.ConfigStruct()
				err = LoadConfigFromEnvironment(app, sectionName, h, env, confStruct)
			}
			if err != nil {
				return nil, err
			}
			return h, nil
		},
	}

	return loaders.Load(logging)
}

func toEnvName(params ...string) string {
	return strings.Join(append([]string{EnvPrefix}, params...), EnvSep)
}
