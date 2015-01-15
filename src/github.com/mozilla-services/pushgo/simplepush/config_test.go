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

[websocket]
    origins = ["https://push.services.mozilla.com",
               "https://loop.services.mozilla.com"]

    [websocket.listener]
    addr = ":8080"
    max_connections = 25000

[endpoint]
    max_data_len = 256

    [endpoint.listener]
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

    [router.listener]
    addr = ":3000"
    max_connections = 6000

[discovery]

[balancer]
type = "static"
`

var env = envconf.New([]string{
	"PUSHGO_DEFAULT_CURRENT_HOST=push.services.mozilla.com",
	"pushgo_default_use_aws=0",
	"PushGo_WebSocket_Origins=https://push.services.mozilla.com",
	"PUSHGO_WEBSOCKET_LISTENER_ADDR=",
	"PUSHGO_WEBSOCKET_LISTENER_MAX_CONNS=25000",
	"pushgo_endpoint_listener_addr=",
	"pushgo_endpoint_listener_max_conns=6000",
	"PUSHGO_logging_TYPE=stdout",
	"pushgo_LOGGING_FORMAT=text",
	"pushgo_logging_FILTER=0",
	"PUSHGO_PROPPING_TYPE=udp",
	"PUSHGO_PROPPING_URL=http://push.services.mozilla.com/ping",
	"PushGo_Router_Bucket_Size=15",
	"PUSHGO_ROUTER_LISTENER_ADDR=",
	"PUSHGO_ROUTER_LISTENER_MAX_CONNS=12000",
	"PUSHGO_ENDPOINT_MAX_DATA_LEN=512",
	"PUSHGO_BALANCER_TYPE=none",
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
	defer app.Close()
	hostname := "push.services.mozilla.com"
	if app.Hostname() != hostname {
		t.Errorf("Mismatched hostname: got %#v; want %#v", app.Hostname(), hostname)
	}
	origin, _ := url.ParseRequestURI("https://push.services.mozilla.com")
	origins := []*url.URL{origin}
	socketHandler := app.SocketHandler()
	if sh, ok := socketHandler.(*SocketHandler); ok {
		if !reflect.DeepEqual(origins, sh.origins) {
			t.Errorf("Mismatched origins: got %#v; want %#v",
				sh.origins, origins)
		}
	} else {
		t.Errorf("WebSocket handler type assertion failed: %#v",
			app.SocketHandler())
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
	endpointHandler := app.EndpointHandler()
	if eh, ok := endpointHandler.(*EndpointHandler); ok {
		if eh.maxDataLen != 512 {
			t.Errorf("Wrong maximum data size: got %d; want 512", eh.maxDataLen)
		}
	} else {
		t.Errorf("Update handler type assertion failed: %#v",
			endpointHandler)
	}
	balancer := app.Balancer()
	if _, ok := balancer.(*NoBalancer); !ok {
		t.Errorf("Balancer type assertion failed: %#v", balancer)
	}
}

func TestLoad(t *testing.T) {
	var (
		appInst                                                    *Application
		mockApp, mockMetrics, mockRouter, mockServ                 *mockPlugin
		mockSocket, mockEndpoint, mockHealth, mockProfile          *mockPlugin
		mockLogger, mockStore, mockPing, mockLocator, mockBalancer *mockPlugin
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
		PluginBalancer: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics, mockServ); err != nil {
				return nil, err
			}
			balancer := &NoBalancer{}
			mockBalancer = newMockPlugin(PluginBalancer, balancer)
			if err := loadEnvConfig(env, "balancer", app, mockBalancer); err != nil {
				return nil, fmt.Errorf("Error initializing server: %#v", err)
			}
			return balancer, nil
		},
		PluginSocket: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics, mockStore); err != nil {
				return nil, err
			}
			h := NewSocketHandler()
			mockSocket = newMockPlugin(PluginSocket, h)
			if err := loadEnvConfig(env, "websocket", app, mockSocket); err != nil {
				return nil, fmt.Errorf("Error initializing WebSocket handler: %s", err)
			}
			return h, nil
		},
		PluginEndpoint: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger, mockMetrics, mockStore, mockRouter,
				mockPing, mockBalancer); err != nil {

				return nil, err
			}
			h := NewEndpointHandler()
			mockEndpoint = newMockPlugin(PluginSocket, h)
			if err := loadEnvConfig(env, "endpoint", app, mockEndpoint); err != nil {
				return nil, fmt.Errorf("Error initializing update handler: %s", err)
			}
			return h, nil
		},
		PluginHealth: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockSocket, mockEndpoint); err != nil {
				return nil, err
			}
			h := NewHealthHandlers()
			mockHealth = newMockPlugin(PluginHealth, h)
			if err := mockHealth.Init(app, mockHealth.ConfigStruct()); err != nil {
				return nil, fmt.Errorf("Error initializing health handlers: %s", err)
			}
			return h, nil
		},
		PluginProfile: func(app *Application) (HasConfigStruct, error) {
			if err := isReady(mockLogger); err != nil {
				return nil, err
			}
			h := new(ProfileHandlers)
			mockProfile = newMockPlugin(PluginProfile, h)
			if err := mockProfile.Init(app, mockProfile.ConfigStruct()); err != nil {
				return nil, fmt.Errorf("Error initializing profiling handlers: %s", err)
			}
			return h, nil
		},
	}
	app, err := loader.Load(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := isReady(mockHealth); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app != appInst {
		t.Errorf("Mismatched app instances: got %#v; want %#v", app, appInst)
	}
}

func loadEnvConfig(env envconf.Environment, section string,
	app *Application, configable HasConfigStruct) (err error) {

	confStruct := configable.ConfigStruct()
	if confStruct != nil {
		if err = env.Decode(toEnvName(section), EnvSep, confStruct); err != nil {
			return fmt.Errorf("Error decoding config for section %q: %s",
				section, err)
		}
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
		return fmt.Errorf("Mismatched ConfigStruct() calls for %s: want 1; got %d",
			mp.typ, mp.configStructCalls)
	}
	if mp.initCalls != 1 {
		return fmt.Errorf("Mismatched Init() calls for %s: want 1; got %d",
			mp.typ, mp.initCalls)
	}
	return nil
}
