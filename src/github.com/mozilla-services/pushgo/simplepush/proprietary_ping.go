/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type PropPinger interface {
	HasConfigStruct
	Register(uaid string, pingData []byte) error
	Send(uaid string, vers int64) (ok bool, err error)
	CanBypassWebsocket() bool
	Status() (bool, error)
}

var UnsupportedProtocolErr = errors.New("Unsupported Ping Request")
var ConfigurationErr = errors.New("Configuration Error")
var ProtocolErr = errors.New("A protocol error occurred. See logs for details.")

var AvailablePings = make(AvailableExtensions)

func init() {
	AvailablePings["noop"] = func() HasConfigStruct { return new(NoopPing) }
	AvailablePings["udp"] = func() HasConfigStruct { return new(UDPPing) }
	AvailablePings["gcm"] = func() HasConfigStruct { return new(GCMPing) }
	AvailablePings["default"] = AvailablePings["noop"]
}

// NoOp ping

type NoopPing struct {
	app    *Application
	config *NoopPingConfig
}

type NoopPingConfig struct{}

func (ml *NoopPing) ConfigStruct() interface{} {
	return &NoopPingConfig{}
}

// Generic configuration for an Ping
func (r *NoopPing) Init(app *Application, config interface{}) (err error) {
	conf := config.(*NoopPingConfig)
	r.config = conf
	return nil
}

// Register the ping to a user
func (r *NoopPing) Register(string, []byte) error {
	return nil
}

// Can the ping bypass telling the device on the websocket?
func (r *NoopPing) CanBypassWebsocket() bool {
	return false
}

// try to send the ping.
func (r *NoopPing) Send(string, int64) (bool, error) {
	return false, nil
}

func (r *NoopPing) Status() (bool, error) {
	return true, nil
}

//===
// "UDP" ping uses remote Carrier provided URL to establish a UDP
// based "ping" to the device. This UDP ping is contained within
// the carrier's network.
type UDPPing struct {
	config *UDPPingConfig
	app    *Application
}

type UDPPingConfig struct {
	URL string `toml:"url" env:"url"` //carrier UDP Proxy URL
	// Additional Carrier required elements here.
}

func (ml *UDPPing) ConfigStruct() interface{} {
	return &UDPPingConfig{
		URL: "https://example.com",
	}
}

func (r *UDPPing) Init(app *Application, config interface{}) error {
	r.app = app
	r.config = config.(*UDPPingConfig)
	return nil
}

func (r *UDPPing) Register(uaid string, pingData []byte) (err error) {
	if err = r.app.Store().PutPing(uaid, pingData); err != nil {
		r.app.Logger().Error("propping", "Could not store connect",
			LogFields{"error": err.Error()})
	}
	return nil
}

func (r *UDPPing) CanBypassWebsocket() bool {
	// If the Ping does not require communication to the client via
	// websocket, return true. If the ping should still attempt to
	// try using the client's websocket connection, return false.
	return false
}

// Send the version info to the Proprietary ping URL provided
// by the carrier.
func (r *UDPPing) Send(string, int64) (bool, error) {
	// Obviously, this needs to be filled out with the appropriate
	// setup and calls to communicate to the remote server.
	// Since UDP is not actually defined, we're returning this
	// error.
	return false, UnsupportedProtocolErr
}

func (r *UDPPing) Status() (bool, error) {
	return false, UnsupportedProtocolErr
}

// ===
// Google Cloud Messaging Proprietary Ping interface
// NOTE: This is still experimental.
type GCMPing struct {
	logger      *SimpleLogger
	metrics     *Metrics
	store       Store
	client      *http.Client
	url         string
	collapseKey string
	dryRun      bool
	apiKey      string
	ttl         uint64
	lastErr     error
	errLock     sync.RWMutex
}

type GCMPingConfig struct {
	APIKey      string `toml:"api_key" env:"api_key"` //GCM Dev API Key
	CollapseKey string `toml:"collapse_key" env:"collapse_key"`
	DryRun      bool   `toml:"dry_run" env:"dry_run"`
	TTL         string `toml:"ttl" env:"ttl"`
	URL         string `toml:"url" env:"url"` //GCM URL
}

type GCMRequest struct {
	Regs        [1]string `json:"registration_ids"`
	CollapseKey string    `json:"collapse_key"`
	TTL         uint64    `json:"time_to_live"`
	DryRun      bool      `json:"dry_run"`
}

type GCMPingData struct {
	RegID string `json:"regid"`
}

type GCMError int

func (err GCMError) Error() string {
	switch err {
	case 400:
		return "Invalid request payload"
	case 401:
		return "Error authenticating sender account"
	default:
		return fmt.Sprintf("Unexpected status code: %d", err)
	}
}

func (r *GCMPing) ConfigStruct() interface{} {
	return &GCMPingConfig{
		URL:         "https://android.googleapis.com/gcm/send",
		APIKey:      "YOUR_API_KEY",
		CollapseKey: "simplepush",
		DryRun:      false,
		TTL:         "72h",
	}
}

func (r *GCMPing) Init(app *Application, config interface{}) error {
	r.logger = app.Logger()
	r.metrics = app.Metrics()
	r.store = app.Store()
	conf := config.(*GCMPingConfig)

	r.url = conf.URL
	r.collapseKey = conf.CollapseKey
	r.dryRun = conf.DryRun

	if r.apiKey = conf.APIKey; len(r.apiKey) == 0 {
		r.logger.Panic("gcmping", "Missing GCM API key", nil)
		return ConfigurationErr
	}

	ttl, err := time.ParseDuration(conf.TTL)
	if err != nil {
		r.logger.Panic("gcmping", "Could not parse TTL",
			LogFields{"error": err.Error(), "ttl": conf.TTL})
		return err
	}
	r.ttl = uint64(ttl / time.Second)

	r.client = new(http.Client)
	return nil
}

func (r *GCMPing) CanBypassWebsocket() bool {
	// GCM can work even if the client's websocket connection
	// has timed out or closed. We do not need to try to send the
	// message on both channels.
	return true
}

func (r *GCMPing) Register(uaid string, pingData []byte) (err error) {
	ping := new(GCMPingData)
	if err = json.Unmarshal(pingData, ping); err != nil {
		return err
	}
	if len(ping.RegID) == 0 {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("gcmping",
				"No user registration ID present. Cannot send message",
				nil)
		}
		return ConfigurationErr
	}
	request := &GCMRequest{
		// google docs lie. You MUST send the regid as an array, even if it's one
		// element.
		Regs:        [1]string{ping.RegID},
		CollapseKey: r.collapseKey,
		TTL:         r.ttl,
		DryRun:      r.dryRun,
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("gcmping", "Could not marshal connection string for storage",
				LogFields{"error": err.Error()})
		}
		return err
	}
	if err = r.store.PutPing(uaid, requestData); err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("gcmping", "Could not store connect",
				LogFields{"error": err.Error()})
		}
		return err
	}
	return nil
}

func (r *GCMPing) Send(uaid string, vers int64) (ok bool, err error) {
	pingData, err := r.store.FetchPing(uaid)
	if err != nil {
		return false, err
	}
	if len(pingData) == 0 {
		return false, nil
	}
	req, err := http.NewRequest("POST", r.url, bytes.NewBuffer(pingData))
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping",
				"Could not create request for GCM Post",
				LogFields{"error": err.Error()})
		}
		return false, err
	}
	req.Header.Add("Authorization", "key="+r.apiKey)
	req.Header.Add("Content-Type", "application/json")
	r.metrics.Increment("propretary.ping.gcm")
	resp, err := r.client.Do(req)
	defer resp.Body.Close()
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping",
				"Failed to send GCM message",
				LogFields{"error": err.Error()})
		}
		return false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if r.logger.ShouldLog(WARNING) {
			r.logger.Warn("propping",
				"GCM returned non success message",
				LogFields{"error": resp.Status})
		}
		r.setLastErr(GCMError(resp.StatusCode))
		return false, ProtocolErr
	}
	return true, nil
}

func (r *GCMPing) Status() (ok bool, err error) {
	if err = r.getLastErr(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *GCMPing) setLastErr(err error) {
	r.errLock.Lock()
	r.lastErr = err
	r.errLock.Unlock()
}

func (r *GCMPing) getLastErr() (err error) {
	r.errLock.RLock()
	err = r.lastErr
	r.errLock.RUnlock()
	return
}
