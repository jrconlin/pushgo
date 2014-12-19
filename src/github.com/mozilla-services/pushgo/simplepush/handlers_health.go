/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
)

type StatusReport struct {
	Healthy          bool           `json:"ok"`
	Clients          int            `json:"clientCount"`
	MaxClientConns   int            `json:"maxClients"`
	MaxEndpointConns int            `json:"maxEndpointConns"`
	Plugins          []PluginReport `json:"plugins"`
	Goroutines       int            `json:"goroutines"`
	Version          string         `json:"version"`
}

// TODO: Remove; add a Typ() method to HasConfigStruct.
type PluginStatus struct {
	Typ    PluginType
	Plugin interface {
		Status() (bool, error)
	}
}

type PluginReport struct {
	Name    string `json:"name"`
	Healthy bool   `json:"ok"`
	Error   error  `json:"error,omitempty"`
}

func NewHealthHandlers() *HealthHandlers {
	return new(HealthHandlers)
}

type HealthHandlers struct {
	app              *Application
	logger           *SimpleLogger
	metrics          Statistician
	server           *Serv
	store            Store
	pinger           PropPinger
	router           Router
	balancer         Balancer
	socketHandlers   *SocketHandlers
	endpointHandlers *EndpointHandlers
}

func (h *HealthHandlers) ConfigStruct() interface{} {
	return nil
}

func (h *HealthHandlers) Init(app *Application, _ interface{}) error {
	h.app = app
	h.logger = app.Logger()
	h.metrics = app.Metrics()
	h.server = app.Server()
	h.store = app.Store()
	h.pinger = app.PropPinger()
	h.router = app.Router()
	h.balancer = app.Balancer()
	h.socketHandlers = app.SocketHandlers()
	h.endpointHandlers = app.EndpointHandlers()

	// Register health check handlers with muxes.
	clientMux := h.socketHandlers.ServeMux()
	clientMux.HandleFunc("/status/", h.StatusHandler)
	clientMux.HandleFunc("/realstatus/", h.RealStatusHandler)

	endpointMux := h.endpointHandlers.ServeMux()
	endpointMux.HandleFunc("/status/", h.StatusHandler)
	endpointMux.HandleFunc("/realstatus/", h.RealStatusHandler)
	endpointMux.HandleFunc("/metrics/", h.MetricsHandler)

	return nil
}

func (h *HealthHandlers) MetricsHandler(resp http.ResponseWriter, req *http.Request) {
	snapshot := h.metrics.Snapshot()
	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(snapshot)
	if err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handlers_health", "Could not generate metrics report",
				LogFields{"rid": req.Header.Get(HeaderID), "error": err.Error()})
		}
		resp.WriteHeader(http.StatusServiceUnavailable)
		resp.Write([]byte("{}"))
		return
	}
	if reply == nil {
		reply = []byte("{}")
	}
	resp.Write(reply)
}

// VIP response
func (h *HealthHandlers) StatusHandler(resp http.ResponseWriter,
	req *http.Request) {
	reply := []byte(fmt.Sprintf(`{"status":"OK","clients":%d,"version":"%s"}`,
		h.app.ClientCount(), VERSION))

	resp.Header().Set("Content-Type", "application/json")
	resp.Write(reply)
}

func (h *HealthHandlers) RealStatusHandler(resp http.ResponseWriter,
	req *http.Request) {

	status := StatusReport{
		MaxClientConns:   h.socketHandlers.MaxConns(),
		MaxEndpointConns: h.endpointHandlers.MaxConns(),
		Version:          VERSION,
	}

	healthy := true
	reports := []PluginStatus{
		{PluginStore, h.store},
		{PluginPinger, h.pinger},
		{PluginRouter, h.router},
		{PluginBalancer, h.balancer},
	}
	for _, r := range reports {
		if r.Plugin == nil {
			continue
		}
		info := PluginReport{Name: r.Typ.String()}
		if info.Healthy, info.Error = r.Plugin.Status(); !info.Healthy {
			healthy = false
		}
		status.Plugins = append(status.Plugins, info)
	}

	status.Healthy = healthy

	status.Clients = h.app.ClientCount()
	status.Goroutines = runtime.NumGoroutine()

	resp.Header().Set("Content-Type", "application/json")
	reply, err := json.Marshal(status)
	if err != nil {
		if h.logger.ShouldLog(ERROR) {
			h.logger.Error("handlers_health", "Could not generate status report",
				LogFields{"error": err.Error()})
		}
		resp.WriteHeader(http.StatusServiceUnavailable)
		resp.Write([]byte("{}"))
		return
	}

	if !status.Healthy {
		resp.WriteHeader(http.StatusServiceUnavailable)
	}
	resp.Write(reply)
}
