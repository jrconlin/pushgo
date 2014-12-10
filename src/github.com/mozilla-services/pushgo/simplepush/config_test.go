/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"net/url"
	"reflect"
	"testing"

	"github.com/bbangert/toml"
	"github.com/kitcambridge/envconf"
)

var configSource = `
[default]
current_host = "push.services.mozilla.com"
use_aws_host = true
origins = ["https://push.services.mozilla.com",
           "https://loop.services.mozilla.com"]

    [default.websocket]
    addr = ":8080"
    max_connections = 25000

    [default.endpoint]
    addr = ":8081"
    max_connections = 3000

[logging]
type = "stdout"
format = "json"
filter = 7

[metrics]

[storage]

[propping]

[router]
bucket_size = 15
pool_size = 250

    [router.listener]
    addr = ":3000"
    max_connections = 6000

[discovery]

[handlers]
max_data_len = 256
`

var env = envconf.New([]string{
	"PUSHGO_DEFAULT_CURRENT_HOST=push.services.mozilla.com",
	"pushgo_default_use_aws=0",
	"PushGo_Default_Origins=https://push.services.mozilla.com",
	"PUSHGO_DEFAULT_WS_ADDR=",
	"PUSHGO_DEFAULT_WS_MAX_CONNS=25000",
	"pushgo_default_endpoint_addr=",
	"pushgo_default_endpoint_max_conns=6000",
	"PUSHGO_logging_TYPE=stdout",
	"pushgo_LOGGING_FORMAT=text",
	"pushgo_logging_FILTER=0",
	"PUSHGO_PROPPING_TYPE=udp",
	"PUSHGO_PROPPING_URL=http://push.services.mozilla.com/ping",
	"PushGo_Router_Bucket_Size=15",
	"PushGo_Router_Pool_Size=250",
	"PUSHGO_ROUTER_LISTENER_ADDR=",
	"PUSHGO_ROUTER_LISTENER_MAX_CONNS=12000",
	"PUSHGO_HANDLERS_MAX_DATA_LEN=512",
})

func TestConfigFile(t *testing.T) {
	var configFile ConfigFile
	if _, err := toml.Decode(configSource, &configFile); err != nil {
		t.Fatalf("Error decoding config: %s", err)
	}
	app, err := LoadApplication(configFile, env, 0)
	if err != nil {
		t.Fatalf("Error initializing app: %s", err)
	}
	defer app.Stop()
	hostname := "push.services.mozilla.com"
	if app.Hostname() != hostname {
		t.Errorf("Mismatched hostname: got %#v; want %#v", app.Hostname(), hostname)
	}
	origin, _ := url.ParseRequestURI("https://push.services.mozilla.com")
	origins := []*url.URL{origin}
	if !reflect.DeepEqual(origins, app.origins) {
		t.Errorf("Mismatched origins: got %#v; want %#v", app.origins, origins)
	}
	logger := app.Logger()
	if stdOutLogger, ok := logger.Logger.(*StdOutLogger); ok {
		if stdOutLogger.filter != 0 {
			t.Errorf("Mismatched log levels: got %#v; want 0", stdOutLogger.filter)
		}
		emitter := stdOutLogger.LogEmitter
		if _, ok := emitter.(*TextEmitter); !ok {
			t.Errorf("Log emitter type assertion failed: %#v", emitter)
		}
	} else {
		t.Errorf("Logger type assertion failed: %#v", logger)
	}
	store := app.Store()
	if noStore, ok := store.(*NoStore); ok {
		defaultChans := noStore.ConfigStruct().(*NoStoreConfig).MaxChannels
		if noStore.maxChannels != defaultChans {
			t.Errorf("Wrong default channel count for storage: got %d; want %d",
				noStore.maxChannels, defaultChans)
		}
	} else {
		t.Errorf("Storage type assertion failed: %#v", store)
	}
	pinger := app.PropPinger()
	if udpPing, ok := pinger.(*UDPPing); ok {
		if udpPing.app != app {
			t.Errorf("Wrong app instance for pinger: got %#v; want %#v",
				udpPing.app, app)
		}
		url := "http://push.services.mozilla.com/ping"
		if udpPing.config.URL != url {
			t.Errorf("Wrong pinger URL: got %#v; want %#v",
				udpPing.config.URL, url)
		}
	} else {
		t.Errorf("Pinger type assertion failed: %#v", pinger)
	}
	handlers := app.Handlers()
	if handlers.maxDataLen != 512 {
		t.Errorf("Wrong maximum data size: got %d; want 512", handlers.maxDataLen)
	}
}

func TestLoad(t *testing.T) {
	var (
		appInst                                                 *Application
		mockApp, mockMetrics, mockRouter, mockServ, mockHandler *mockPlugin
		mockLogger, mockStore, mockPing, mockLocator            *mockPlugin
	)
	loader := PluginLoaders{
		PluginApp: func(app *Application) (HasConfigStruct, error) {
			appInst = app
			mockApp = newMockPlugin(PluginApp, app)
			if err := loadEnvConfig(env, "default", app, mockApp); err != nil {
				return nil, fmt.Errorf("Error initializing app: %#v", err)
			}
			return app, nil
		},
		PluginLogger: func(app *Application) (HasConfigStruct, error) {
			log := &StdOutLogger{}
			mockLogger = newMockPlugin(PluginLogger, log)
			if err := loadEnvConfig(env, "logging", app, mockLogger); err != nil {
				return nil, fmt.Errorf("Error initializing logger: %#v", err)
			}
			return log, nil
		},
		PluginMetrics: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger); err != nil {
				return nil, err
			}
			metrics := &Metrics{}
			mockMetrics = newMockPlugin(PluginMetrics, metrics)
			if err := loadEnvConfig(env, "metrics", app, mockMetrics); err != nil {
				return nil, fmt.Errorf("Error initializing metrics: %#v", err)
			}
			return metrics, nil
		},
		PluginStore: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics); err != nil {
				return nil, err
			}
			store := &NoStore{}
			mockStore = newMockPlugin(PluginStore, store)
			if err := loadEnvConfig(env, "storage", app, mockStore); err != nil {
				return nil, fmt.Errorf("Error initializing storage: %#v", err)
			}
			return store, nil
		},
		PluginPinger: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics, mockStore); err != nil {
				return nil, err
			}
			pinger := &NoopPing{}
			mockPing = newMockPlugin(PluginPinger, pinger)
			if err := loadEnvConfig(env, "propping", app, mockPing); err != nil {
				return nil, fmt.Errorf("Error initializing pinger: %#v", err)
			}
			return pinger, nil
		},
		PluginRouter: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics); err != nil {
				return nil, err
			}
			router := NewBroadcastRouter()
			mockRouter = newMockPlugin(PluginRouter, router)
			if err := loadEnvConfig(env, "router", app, mockRouter); err != nil {
				return nil, fmt.Errorf("Error initializing router: %#v", err)
			}
			return router, nil
		},
		PluginLocator: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics, mockRouter); err != nil {
				return nil, err
			}
			locator := &StaticLocator{}
			mockLocator = newMockPlugin(PluginLocator, locator)
			if err := loadEnvConfig(env, "locator", app, mockLocator); err != nil {
				return nil, fmt.Errorf("Error initializing locator: %#v", err)
			}
			return locator, nil
		},
		PluginServer: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics); err != nil {
				return nil, err
			}
			srv := NewServer()
			mockServ = newMockPlugin(PluginServer, srv)
			if err := loadEnvConfig(env, "server", app, mockServ); err != nil {
				return nil, fmt.Errorf("Error initializing server: %#v", err)
			}
			return srv, nil
		},
		PluginHandlers: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics); err != nil {
				return nil, err
			}
			h := &Handler{}
			mockHandler = newMockPlugin(PluginHandlers, h)
			if err := loadEnvConfig(env, "handlers", app, mockHandler); err != nil {
				return nil, fmt.Errorf("Error initializing handlers: %#v", err)
			}
			return h, nil
		},
	}
	app, err := loader.Load(0)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Stop()
	if app != appInst {
		t.Errorf("Mismatched app instances: got %#v; want %#v", app, appInst)
	}
}

func loadEnvConfig(env envconf.Environment, section string,
	app *Application, configable HasConfigStruct) (err error) {

	confStruct := configable.ConfigStruct()
	if err = env.Decode(toEnvName(section), EnvSep, confStruct); err != nil {
		return fmt.Errorf("Error decoding config for section %q: %s",
			section, err)
	}
	return configable.Init(app, confStruct)
}

func isReady(pre ...*mockPlugin) (err error) {
	for _, plugin := range pre {
		if err := plugin.Ready(); err != nil {
			return err
		}
	}
	return nil
}

func newMockPlugin(typ PluginType, configable HasConfigStruct) *mockPlugin {
	return &mockPlugin{HasConfigStruct: configable, typ: typ}
}

type mockPlugin struct {
	HasConfigStruct
	typ               PluginType
	configStructCalls int
	initCalls         int
	ready             bool
}

func (mp *mockPlugin) ConfigStruct() interface{} {
	if mp == nil {
		return nil
	}
	mp.configStructCalls++
	return mp.HasConfigStruct.ConfigStruct()
}

func (mp *mockPlugin) Init(app *Application, config interface{}) error {
	if mp == nil {
		return nil
	}
	mp.initCalls++
	return mp.HasConfigStruct.Init(app, config)
}

func (mp *mockPlugin) Ready() (err error) {
	if mp == nil {
		return fmt.Errorf("Uninitialized plugin")
	}
	if mp.configStructCalls != 1 {
		return fmt.Errorf("Mismatched ConfigStruct() calls: want 1; got %d",
			mp.configStructCalls)
	}
	if mp.initCalls != 1 {
		return fmt.Errorf("Mismatched Init() calls: want 1; got %d",
			mp.initCalls)
	}
	return nil
}
