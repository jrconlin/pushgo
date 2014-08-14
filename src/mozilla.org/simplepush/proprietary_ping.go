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

type IPropPing interface {
	HasConfigStruct
	Send(vers int64) error
	CanBypassWebsocket() bool
}

var UnsupportedProtocolErr = errors.New("Unsupported Ping Request")
var ConfigurationErr = errors.New("Configuration Error")
var ProtocolErr = errors.New("A protocol error occurred. See logs for details.")

//===
// Placeholder Proprietary Ping.
type PropPing struct {
	IPropPing
	connect JsMap
	app     *Application
}

type PropPingConfig struct {
}

func NewPropPing(connect string, uaid string, app *Application) (*PropPing, error) {
	return &PropPing{
		app: app,
	}, nil

}

func (ml *UDPPing) ConfigStruct() interface{} {
	return &PropPingConfig{}
}

func (self *PropPing) Send(vers int64) error {
	return UnsupportedProtocolErr
}

func (self *PropPing) CanBypassWebsocket() bool {
	return false
}

//===
// "UDP" ping uses remote Carrier provided URL to establish a UDP
// based "ping" to the device. This UDP ping is contained within
// the carrier's network.
type UDPPing struct {
	IPropPing
	connect JsMap
	app     *Application
}

type UDPPingConfig struct {
	URL string `toml:"udp_url"` //carrier UDP Proxy URL
	// Additional Carrier required elements here.
}

func NewUDPPing(connect string, uaid string, app *Application) (*UDPPing, error) {
	var err error
	var c_js JsMap = make(JsMap)

	if len(connect) == 0 {
		return nil, nil
	}

	err = json.Unmarshal([]byte(connect), &c_js)
	if err != nil {
		return nil, err
	}

	if err = app.Storage().SetPropConnect(uaid, connect); err != nil {
		app.Logger().Error("propping", "Could not store connect",
			LogFields{"error": err.Error()})
	}

	// Additional configuration information here.
	return &UDPPing{
		connect: c_js,
		app:     app,
		// Additional default values for UDP here.
	}, nil
}

func (ml *UDPPing) ConfigStruct() interface{} {
	return &UDPPingConfig{
		URL:         "https://android.googleapis.com/gcm/send",
		APIKey:      "YOUR_API_KEY",
		CollapseKey: "simplepush",
		DryRun:      false,
		TTL:         259200,
	}
}

// Send the version info to the Proprietary ping URL provided
// by the carrier.
func (self *UDPPing) Send(vers int64) error {
	// Obviously, this needs to be filled out with the appropriate
	// setup and calls to communicate to the remote server.
	// Since UDP is not actually defined, we're returning this
	// error.
	return UnsupportedProtocolErr
}

func (self *UDPPing) CanBypassWebsocket() bool {
	// If the Ping does not require communication to the client via
	// websocket, return true. If the ping should still attempt to
	// try using the client's websocket connection, return false.
	return false
}

// ===
// Google Cloud Messaging Proprietary Ping interface
// NOTE: This is still experimental.
type GCMPing struct {
	IPropPing
	connect JsMap
	app     *Application
}

type GCMPingConfig struct {
	APIKey      string  `toml:"gcm_apikey"` //GCM Dev API Key
	CollapseKey string  `toml:"gcm_collapsekey"`
	DryRun      boolean `toml:"gcm_dryrun"`
	ProjectID   string  `toml:"gcm_projectid"`
	TTL         uint64  `toml:"gcm_ttl"`
	URL         string  `toml:"gcm_url"` //GCM URL
}

func (ml *GCMPing) ConfigStruct() interface{} {
	return &GCMPingConfig{
		URL:         "https://android.googleapis.com/gcm/send",
		APIKey:      "YOUR_API_KEY",
		CollapseKey: "simplepush",
		DryRun:      false,
		TTL:         259200,
	}
}

func (self *GCMPing) CanBypassWebsocket() bool {
	// GCM can work even if the client's websocket connection
	// has timed out or closed. We do not need to try to send the
	// message on both channels.
	return true
}

func (self *GCMPing) Send(vers int64) error {
	// google docs lie. You MUST send the regid as an array, even if it's one
	// element.
	regs := [1]string{self.connect["regid"].(string)}
	data, err := json.Marshal(JsMap{
		"registration_ids": regs,
		"collapse_key":     self.connect["collapse_key"],
		"time_to_live":     self.connect["ttl"],
		"dry_run":          self.connect["dry_run"],
	})
	if err != nil {
		self.logger.Error("propping",
			"Could not marshal request for GCM post",
			LogFields{"error": err.Error()})
		return err
	}
	req, err := http.NewRequest("POST",
		self.connect["gcm_url"].(string),
		bytes.NewBuffer(data))
	if err != nil {
		self.logger.Error("propping",
			"Could not create request for GCM Post",
			LogFields{"error": err.Error()})
		return err
	}
	req.Header.Add("Authorization", "key="+self.connect["api_key"].(string))
	req.Header.Add("Project_id", self.connect["project_id"].(string))
	req.Header.Add("Content-Type", "application/json")
	self.app.Metrics().Increment("propretary.ping.gcm")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		self.app.Logger().Error("propping",
			"Failed to send GCM message",
			LogFields{"error": err.Error()})
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		self.app.Logger().Error("propping",
			"GCM returned non success message",
			LogFields{"error": resp.Status})
		return ProtocolErr
	}
	return nil
}

func NewGCMPing(connect *JsMap, logger *SimpleLogger, gcmConfig *GCMConfig) (*GCMPing, error) {
	var err error
	var c_js JsMap = make(JsMap)
	var kind string

	if len(connect) == 0 {
		return nil, nil
	}
	if err = json.Unmarshal([]byte(connect), &c_js); err != nil {
		logger.Error("gcmping", "Invalid connect string",
			LogFields{"error": err.Error()})
		return nil, err
	}

	if gcmConfig.ApiKey == "" {
		logger.Error("gcmping",
			"No gcm.api_key defined in config file. Cannot send message.",
			nil)
		return nil, ConfigurationErr
	}

	(*connect)["collapse_key"] = gcmConfig.CollapseKey
	(*connect)["dry_run"] = gcmConfig.DryRun
	(*connect)["api_key"] = gcmConfig.ApiKey
	(*connect)["gcm_url"] = gcmConfig.Url
	(*connect)["ttl"] = gcmConfig.TTL
	(*connect)["project_id"] = gcmConfig.ProjectId
	if err = app.Storage().SetPropConnect(uaid, connect); err != nil {
		logger.Error("gcmping", "Could not store connect",
			LogFields{"error": err.Error()})
	}
	return &GCMPing{
		connect, c_js,
		logger:  app.Logger(),
		store:   app.Storage(),
		metrics: app.Metrics(),
	}, nil
}
