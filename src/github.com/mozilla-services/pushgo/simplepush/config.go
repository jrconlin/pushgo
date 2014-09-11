/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
// Most of this is copied straight from heka's TOML config setup

package simplepush

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/bbangert/toml"
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

// EnvPrefix is the configuration environment variable prefix.
const EnvPrefix = "pushgo_"

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

// An Environment holds and decodes environment variables.
type Environment map[string]string

// Get returns the value of an environment variable, performing a
// case-insensitive search if an exact match is not found.
func (env Environment) Get(name string) (value string, ok bool) {
	var key string
	if value, ok = env[name]; ok {
		goto formatValue
	}
	for key, value = range env {
		if strings.EqualFold(key, name) {
			goto formatValue
		}
	}
	return "", false
formatValue:
	return strings.TrimSpace(value), true
}

// Unmarshal decodes configuration options specified via environment variables
// into the value pointed to by configStruct.
func (env Environment) Unmarshal(sectionName string, configStruct interface{}) error {
	value := reflect.ValueOf(configStruct)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return fmt.Errorf("Non-pointer type %s", value.Type())
	}
	return env.decodeEnvField(sectionName, indirect(value))
}

// decodeEnvField decodes an environment variable into a struct field. Literals
// are decoded directly into the value; structs are decoded recursively.
func (env Environment) decodeEnvField(name string, value reflect.Value) error {
	typ := value.Type()
	if typ.Kind() != reflect.Struct {
		if source, ok := env.Get(name); ok {
			return decodeEnvLiteral(source, value)
		}
		return nil
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("env")
		if tag == "-" {
			continue
		}
		if len(tag) == 0 {
			tag = field.Name
		}
		if err := env.decodeEnvField(name+"_"+tag, value.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

// decodeEnvLiteral decodes a source string into a value. Only integers,
// floats, Booleans, slices, and strings are supported.
func decodeEnvLiteral(source string, value reflect.Value) error {
	kind := value.Type().Kind()
	if kind >= reflect.Int && kind <= reflect.Int64 {
		result, err := strconv.ParseInt(source, 0, value.Type().Bits())
		if err != nil {
			return err
		}
		value.SetInt(result)
		return nil
	}
	if kind >= reflect.Uint && kind <= reflect.Uint64 {
		result, err := strconv.ParseUint(source, 0, value.Type().Bits())
		if err != nil {
			return err
		}
		value.SetUint(result)
		return nil
	}
	if kind >= reflect.Float32 && kind <= reflect.Float64 {
		result, err := strconv.ParseFloat(source, value.Type().Bits())
		if err != nil {
			return err
		}
		value.SetFloat(result)
		return nil
	}
	switch kind {
	case reflect.Bool:
		result, err := strconv.ParseBool(source)
		if err != nil {
			return err
		}
		value.SetBool(result)
		return nil

	case reflect.Slice:
		return decodeEnvSlice(source, value)

	case reflect.String:
		value.SetString(source)
		return nil
	}
	return fmt.Errorf("Unsupported type %s", kind)
}

// splitList splits a comma-separated list into a slice of strings, accounting
// for escape characters.
func splitList(source string) (results []string) {
	var (
		isEscaped        bool
		lastIndex, index int
	)
	for ; index < len(source); index++ {
		if isEscaped {
			isEscaped = false
			continue
		}
		switch source[index] {
		case '\\':
			isEscaped = true

		case ',':
			results = append(results, source[lastIndex:index])
			lastIndex = index + 1
		}
	}
	if lastIndex < index {
		results = append(results, source[lastIndex:])
	}
	return results
}

// decodeEnvSlice decodes a comma-separated list of values into a slice.
// Slices are decoded recursively.
func decodeEnvSlice(source string, value reflect.Value) error {
	sources := splitList(source)
	value.SetLen(0)
	for _, source := range sources {
		element := indirect(reflect.New(value.Type().Elem()))
		if err := decodeEnvLiteral(source, element); err != nil {
			return err
		}
		value.Set(reflect.Append(value, element))
	}
	return nil
}

// indirect returns the value pointed to by a pointer, allocating zero values
// for nil pointers.
func indirect(value reflect.Value) reflect.Value {
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		value = reflect.Indirect(value)
	}
	return value
}

// LoadEnvironment returns an Environment populated with configuration options.
// Variable names are case-insensitive.
func LoadEnvironment() (env Environment) {
	env = make(Environment)
	for _, v := range os.Environ() {
		value := strings.SplitN(v, "=", 2)
		key := value[0]
		if l := len(EnvPrefix); len(key) >= l && strings.EqualFold(key[:l], EnvPrefix) {
			env[key[l:]] = value[1]
		}
	}
	return
}

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
	env Environment, configFile ConfigFile) (err error) {

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

	if err = env.Unmarshal(sectionName, confStruct); err != nil {
		err = fmt.Errorf("Invalid environment variable for section '%s': %s",
			sectionName, err)
		return
	}

	err = obj.Init(app, confStruct)
	return
}

// Load an extensible section that has a type keyword
func LoadExtensibleSection(app *Application, sectionName string,
	extensions AvailableExtensions, env Environment, configFile ConfigFile) (HasConfigStruct, error) {
	var err error

	confSection := new(ExtensibleGlobals)

	conf, ok := configFile[sectionName]
	if !ok {
		return nil, fmt.Errorf("Error loading config file, section: %s", sectionName)
	}

	if err = toml.PrimitiveDecode(conf, confSection); err != nil {
		return nil, err
	}
	if err = env.Unmarshal(sectionName, confSection); err != nil {
		return nil, err
	}
	ext, ok := extensions[confSection.Typ]
	if !ok {
		ext, ok = extensions["default"]
		if !ok {
			return nil, fmt.Errorf("No type '%s' available to load for section '%s'",
				confSection.Typ, sectionName)
		}
		//TODO: Add log info to indicate using "default"
	}

	obj := ext()
	loadedConfig, err := LoadConfigStruct(conf, obj)
	if err != nil {
		return nil, err
	}

	if err = env.Unmarshal(sectionName, loadedConfig); err != nil {
		return nil, fmt.Errorf("Invalid environment variable for section '%s': %s", sectionName, err)
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
	env := LoadEnvironment()

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
