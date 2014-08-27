/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"net/url"
	"sync"
)

type TestServer struct {
	sync.Mutex
	ServAddr   string
	RouterAddr string
	LogLevel   int
	Contacts   []string
	app        *Application
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

func (t *TestServer) load() (*Application, error) {
	loaders := PluginLoaders{
		PluginApp: func(app *Application) (HasConfigStruct, error) {
			appConf := app.ConfigStruct().(*ApplicationConfig)
			if err := app.Init(app, appConf); err != nil {
				return nil, fmt.Errorf("Error initializing application: %#v", err)
			}
			return app, nil
		},
		PluginLogger: func(app *Application) (HasConfigStruct, error) {
			logger := new(StdOutLogger)
			loggerConf := logger.ConfigStruct().(*StdOutLoggerConfig)
			loggerConf.Filter = -1
			if err := logger.Init(app, loggerConf); err != nil {
				return nil, fmt.Errorf("Error initializing logger: %#v", err)
			}
			return logger, nil
		},
		PluginPinger: func(app *Application) (HasConfigStruct, error) {
			pinger := new(NoopPing)
			pingerConf := pinger.ConfigStruct().(*NoopPingConfig)
			if err := pinger.Init(app, pingerConf); err != nil {
				return nil, fmt.Errorf("Error initializing proprietary pinger: %#v", err)
			}
			return pinger, nil
		},
		PluginMetrics: func(app *Application) (HasConfigStruct, error) {
			metrics := new(Metrics)
			metricsConf := metrics.ConfigStruct().(*MetricsConfig)
			if err := metrics.Init(app, metricsConf); err != nil {
				return nil, fmt.Errorf("Error initializing metrics: %#v", err)
			}
			return metrics, nil
		},
		PluginStore: func(app *Application) (HasConfigStruct, error) {
			store := new(NoStore)
			storeConf := store.ConfigStruct().(*NoStoreConfig)
			if err := store.Init(app, storeConf); err != nil {
				return nil, fmt.Errorf("Error initializing store: %#v", err)
			}
			return store, nil
		},
		PluginRouter: func(app *Application) (HasConfigStruct, error) {
			router := NewRouter()
			routerConf := router.ConfigStruct().(*RouterConfig)
			routerConf.Addr = t.RouterAddr
			if len(routerConf.Addr) == 0 {
				routerConf.Addr = ""
			}
			if err := router.Init(app, routerConf); err != nil {
				return nil, fmt.Errorf("Error initializing router: %#v", err)
			}
			return router, nil
		},
		PluginLocator: func(app *Application) (HasConfigStruct, error) {
			locator := new(StaticLocator)
			locatorConf := locator.ConfigStruct().(*StaticLocatorConf)
			locatorConf.Contacts = t.Contacts
			if err := locator.Init(app, locatorConf); err != nil {
				return nil, fmt.Errorf("Error initializing locator: %#v", err)
			}
			return locator, nil
		},
		PluginServer: func(app *Application) (HasConfigStruct, error) {
			serv := new(Serv)
			servConf := serv.ConfigStruct().(*ServerConfig)
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
		PluginHandlers: func(app *Application) (HasConfigStruct, error) {
			handlers := new(Handler)
			if err := handlers.Init(app, handlers.ConfigStruct()); err != nil {
				return nil, fmt.Errorf("Error initializing handlers: %#v", err)
			}
			return handlers, nil
		},
	}
	return loaders.Load(t.LogLevel)
}

func (t *TestServer) Listen() (app *Application, err error) {
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
