package client

import (
	"fmt"
	"testing"

	server "github.com/mozilla-services/pushgo/simplepush"
)

func NewServer() (*server.Application, error) {
	loaders := server.PluginLoaders{
		server.PluginApp: func(app *server.Application) (server.HasConfigStruct, error) {
			appConf := app.ConfigStruct().(*server.ApplicationConfig)
			appConf.Hostname = "localhost"
			if err := app.Init(app, appConf); err != nil {
				return nil, fmt.Errorf("Error initializing application: %#v", err)
			}
			return app, nil
		},
		server.PluginLogger: func(app *server.Application) (server.HasConfigStruct, error) {
			logger := new(server.StdOutLogger)
			loggerConf := logger.ConfigStruct().(*server.StdOutLoggerConfig)
			if err := logger.Init(app, loggerConf); err != nil {
				return nil, fmt.Errorf("Error initializing logger: %#v", err)
			}
			return logger, nil
		},
		server.PluginPinger: func(app *server.Application) (server.HasConfigStruct, error) {
			pinger := new(server.NoopPing)
			pingerConf := pinger.ConfigStruct().(*server.NoopPingConfig)
			if err := pinger.Init(app, pingerConf); err != nil {
				return nil, fmt.Errorf("Error initializing proprietary pinger: %#v", err)
			}
			return pinger, nil
		},
		server.PluginMetrics: func(app *server.Application) (server.HasConfigStruct, error) {
			metrics := new(server.Metrics)
			metricsConf := metrics.ConfigStruct().(*server.MetricsConfig)
			if err := metrics.Init(app, metricsConf); err != nil {
				return nil, fmt.Errorf("Error initializing metrics: %#v", err)
			}
			return metrics, nil
		},
		server.PluginStore: func(app *server.Application) (server.HasConfigStruct, error) {
			store := new(server.NoStore)
			storeConf := store.ConfigStruct().(*server.NoStoreConfig)
			if err := store.Init(app, storeConf); err != nil {
				return nil, fmt.Errorf("Error initializing store: %#v", err)
			}
			return store, nil
		},
		server.PluginRouter: func(app *server.Application) (server.HasConfigStruct, error) {
			router := server.NewRouter()
			routerConf := router.ConfigStruct().(*server.RouterConfig)
			if err := router.Init(app, routerConf); err != nil {
				return nil, fmt.Errorf("Error initializing router: %#v", err)
			}
			return router, nil
		},
		server.PluginLocator: func(app *server.Application) (server.HasConfigStruct, error) {
			locator := new(server.StaticLocator)
			locatorConf := locator.ConfigStruct().(*server.StaticLocatorConf)
			if err := locator.Init(app, locatorConf); err != nil {
				return nil, fmt.Errorf("Error initializing locator: %#v", err)
			}
			return locator, nil
		},
		server.PluginServer: func(app *server.Application) (server.HasConfigStruct, error) {
			serv := new(server.Serv)
			servConf := serv.ConfigStruct().(*server.ServerConfig)
			if err := serv.Init(app, servConf); err != nil {
				return nil, fmt.Errorf("Error initializing server: %#v", err)
			}
			return serv, nil
		},
		server.PluginHandlers: func(app *server.Application) (server.HasConfigStruct, error) {
			handlers := new(server.Handler)
			if err := handlers.Init(app, handlers.ConfigStruct()); err != nil {
				return nil, fmt.Errorf("Error initializing handlers: %#v", err)
			}
			return handlers, nil
		},
	}
	return loaders.Load(5)
}

func TestNewServer(t *testing.T) {
	_, err := NewServer()
	if err != nil {
		t.Fatalf("Error initializing application: %#v", err)
	}
	// ...
}
