/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

type PropPing interface {
	HasConfigStruct
	Register(connect JsMap, uaid string) error
	Send(vers int64) error
	CanBypassWebsocket() bool
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
	PropPing
	app    *Application
	config *NoopPingConfig
}

type NoopPingConfig struct {
	config *NoopPingConfig
}

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
func (r *NoopPing) Register(connect JsMap, uaid string) (err error) {
	return nil
}

// Can the ping bypass telling the device on the websocket?
func (r *NoopPing) CanBypassWebsocket() bool {
	return false
}

// try to send the ping.
func (r *NoopPing) Send(vers int64) error {
	return UnsupportedProtocolErr
}

//===
// "UDP" ping uses remote Carrier provided URL to establish a UDP
// based "ping" to the device. This UDP ping is contained within
// the carrier's network.
type UDPPing struct {
	PropPing
	config *UDPPingConfig
	app    *Application
}

type UDPPingConfig struct {
	URL string `toml:"udp_url"` //carrier UDP Proxy URL
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

func (r *UDPPing) Register(connect JsMap, uaid string) (err error) {

	cstr, err := json.Marshal(connect)
	if err != nil {
		r.app.Logger().Error("udpping", "Could not marshal connection string for storage",
			LogFields{"error": err.Error()})
		return err
	}
	// TODO: Convert this to take either []byte or JsMap
	if err = r.app.Storage().SetPropConnect(uaid, string(cstr)); err != nil {
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
func (r *UDPPing) Send(vers int64) error {
	// Obviously, this needs to be filled out with the appropriate
	// setup and calls to communicate to the remote server.
	// Since UDP is not actually defined, we're returning this
	// error.
	return UnsupportedProtocolErr
}

// ===
// Google Cloud Messaging Proprietary Ping interface
// NOTE: This is still experimental.
type GCMPing struct {
	PropPing
	config *GCMPingConfig
	app    *Application
}

type GCMPingConfig struct {
	APIKey      string `toml:"gcm_apikey"` //GCM Dev API Key
	CollapseKey string `toml:"gcm_collapsekey"`
	DryRun      bool   `toml:"gcm_dryrun"`
	ProjectID   string `toml:"gcm_projectid"`
	TTL         uint64 `toml:"gcm_ttl"`
	URL         string `toml:"gcm_url"` //GCM URL
	RegID       string
	UAID        string
}

func (r *GCMPing) ConfigStruct() interface{} {
	return &GCMPingConfig{
		URL:         "https://android.googleapis.com/gcm/send",
		APIKey:      "YOUR_API_KEY",
		CollapseKey: "simplepush",
		ProjectID:   "simplepush_project",
		DryRun:      false,
		TTL:         259200,
	}
}

func (r *GCMPing) Init(app *Application, config interface{}) error {
	r.app = app
	r.config = config.(*GCMPingConfig)
	return nil
}

func (r *GCMPing) Register(connect JsMap, uaid string) (err error) {
	// already specified, no need to redo.
	if r.config.UAID == uaid {
		return nil
	}

	if r.config.APIKey == "" {
		r.app.Logger().Error("gcmping",
			"No gcm.api_key defined in config file. Cannot send message.",
			nil)
		return ConfigurationErr
	}

	regid, ok := connect["regid"]
	if !ok {
		r.app.Logger().Error("gcmping",
			"No user registration ID present. Cannot send message",
			nil)
		return ConfigurationErr
	}

	connect["collapse_key"] = r.config.CollapseKey
	connect["dry_run"] = r.config.DryRun
	connect["api_key"] = r.config.APIKey
	connect["gcm_url"] = r.config.URL
	connect["ttl"] = r.config.TTL
	connect["project_id"] = r.config.ProjectID
	r.config.RegID = regid.(string)
	r.config.UAID = uaid
	full_connect, err := json.Marshal(connect)
	if err != nil {
		r.app.Logger().Error("gcmping", "Could not marshal connection string for storage",
			LogFields{"error": err.Error()})
		return err
	}
	// TODO: convert this to take either []byte or JsMap
	if err = r.app.Storage().SetPropConnect(uaid, string(full_connect)); err != nil {
		r.app.Logger().Error("gcmping", "Could not store connect",
			LogFields{"error": err.Error()})
		return err
	}
	return nil
}

func (r *GCMPing) CanBypassWebsocket() bool {
	// GCM can work even if the client's websocket connection
	// has timed out or closed. We do not need to try to send the
	// message on both channels.
	return true
}

func (r *GCMPing) Send(vers int64) error {
	// google docs lie. You MUST send the regid as an array, even if it's one
	// element.
	regs := [1]string{r.config.RegID}
	data, err := json.Marshal(JsMap{
		"registration_ids": regs,
		"collapse_key":     r.config.CollapseKey,
		"time_to_live":     r.config.TTL,
		"dry_run":          r.config.DryRun,
	})
	if err != nil {
		r.app.Logger().Error("propping",
			"Could not marshal request for GCM post",
			LogFields{"error": err.Error()})
		return err
	}
	req, err := http.NewRequest("POST", r.config.URL, bytes.NewBuffer(data))
	if err != nil {
		r.app.Logger().Error("propping",
			"Could not create request for GCM Post",
			LogFields{"error": err.Error()})
		return err
	}
	req.Header.Add("Authorization", "key="+r.config.APIKey)
	req.Header.Add("Project_id", r.config.ProjectID)
	req.Header.Add("Content-Type", "application/json")
	r.app.Metrics().Increment("propretary.ping.gcm")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		r.app.Logger().Error("propping",
			"Failed to send GCM message",
			LogFields{"error": err.Error()})
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		r.app.Logger().Error("propping",
			"GCM returned non success message",
			LogFields{"error": resp.Status})
		return ProtocolErr
	}
	return nil
}
