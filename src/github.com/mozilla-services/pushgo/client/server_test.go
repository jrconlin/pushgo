/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"fmt"
	"net/url"
	"sync"

	server "github.com/mozilla-services/pushgo/simplepush"
)

type TestServer struct {
	sync.Mutex
	ServAddr   string
	RouterAddr string
	LogLevel   int
	app        *server.Application
	lastErr    error
	isStopping bool
}

func (t *TestServer) fatal(err error) {
	defer t.Unlock()
	t.Lock()
	if !t.isStopping {
		t.app.Stop()
		t.isStopping = true
	}
	if t.lastErr == nil {
		t.lastErr = err
	}
}

func (t *TestServer) run() {
	err := <-t.app.Run()
	if err != nil {
		t.fatal(err)
	}
}

func (t *TestServer) load() (*server.Application, error) {
	loaders := server.PluginLoaders{
		server.PluginApp: func(app *server.Application) (server.HasConfigStruct, error) {
			appConf := app.ConfigStruct().(*server.ApplicationConfig)
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
			routerConf.Addr = t.RouterAddr
			if len(routerConf.Addr) == 0 {
				routerConf.Addr = ""
			}
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
			servConf.Addr = t.ServAddr
			if len(servConf.Addr) == 0 {
				// Listen on a random port for testing.
				servConf.Addr = ""
			}
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
	return loaders.Load(t.LogLevel)
}

func (t *TestServer) Listen() (app *server.Application, err error) {
	defer t.Unlock()
	t.Lock()
	if t.isStopping {
		err = t.lastErr
		return
	}
	if t.app != nil {
		return t.app, nil
	}
	if t.app, err = t.load(); err != nil {
		return nil, err
	}
	go t.run()
	return t.app, nil
}

func (t *TestServer) Origin() (string, error) {
	app, err := t.Listen()
	if err != nil {
		return "", err
	}
	server := app.Server()
	if server == nil {
		return "", nil
	}
	origin, err := url.Parse(server.FullHostname())
	switch origin.Scheme {
	case "http":
		origin.Scheme = "ws"

	case "https":
		origin.Scheme = "wss"
	}
	return origin.String(), nil
}
