/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	storage "mozilla.org/simplepush/storage/mcstorage"

	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

type PropPing struct {
	connect util.JsMap
	config  *util.MzConfig
	logger  *util.MzLogger
	store   *storage.Storage
	metrics *util.Metrics
}

var UnsupportedProtocolErr = errors.New("Unsupported Ping Request")
var ConfigurationErr = errors.New("Configuration Error")
var ProtocolErr = errors.New("A protocol error occurred. See logs for details.")

func NewPropPing(connect string, uaid string, config *util.MzConfig, logger *util.MzLogger, store *storage.Storage, metrics *util.Metrics) (*PropPing, error) {

	var err error
	var c_js util.JsMap = make(util.JsMap)
	var kind string

	if len(connect) == 0 {
		return nil, nil
	}

	err = json.Unmarshal([]byte(connect), &c_js)
	if err != nil {
		return nil, err
	}

	if val, ok := c_js["type"]; ok {
		kind = val.(string)
	} else {
		c_js["type"] = "udp"
		kind = "udp"
	}

	switch kind {
	case "gcm":
		init_gcm(&c_js, config, logger)
	}

	if err = store.SetPropConnect(uaid, connect); err != nil {
		logger.Error("propping", "Could not store connect",
			LogFields{"error": err.Error()})
	}

	return &PropPing{
		connect: c_js,
		config:  config,
		logger:  logger,
		store:   store,
		metrics: metrics,
	}, nil
}

func init_gcm(connect *util.JsMap, config *util.MzConfig, logger *util.MzLogger) error {
	ttl, err := strconv.ParseInt(config.Get("gcm.ttl", config.Get("db.timeout_live", "259200")), 10, 0)
	if err != nil {
		ttl = 259200
		logger.Warn("propping",
			"Could not parse config option time, using 259200",
			LogFields{"error": err.Error()})
	}
	collapse_key := config.Get("gcm.collapse_key", "simplepush")
	project_id := config.Get("gcm.project_id", "simplepush-gcm")
	dry_run := config.GetFlag("gcm.dry_run")
	api_key := config.Get("gcm.api_key", "")
	gcm_url := config.Get("gcm.url", "https://android.googleapis.com/gcm/send")
	if api_key == "" {
		logger.Error("propping",
			"No gcm.api_key defined in config file. Cannot send message.",
			nil)
		return ConfigurationErr
	}

	(*connect)["collapse_key"] = collapse_key
	(*connect)["dry_run"] = dry_run
	(*connect)["api_key"] = api_key
	(*connect)["gcm_url"] = gcm_url
	(*connect)["ttl"] = ttl
	(*connect)["project_id"] = project_id
	return nil
}

func (self *PropPing) Send(vers int64) error {

	switch self.connect["type"].(string) {
	case "gcm":
		self.metrics.Increment("propretary.ping.gcm")
		return self.send_gcm(vers)
	default:
		return UnsupportedProtocolErr
	}

}

func (self *PropPing) send_gcm(vers int64) error {
	// google docs lie. You MUST send the regid as an array, even if it's one.
	regs := [1]string{self.connect["regid"].(string)}
	data, err := json.Marshal(util.JsMap{
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
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		self.logger.Error("propping",
			"Failed to send GCM message",
			LogFields{"error": err.Error()})
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		self.logger.Error("propping",
			"GCM returned non success message",
			LogFields{"error": resp.Status})
		return ProtocolErr
	}
	return nil
}

func (self *PropPing) CanBypassWebsocket() bool {
	switch self.connect["type"] {
	case "gcm":
		return true
	}

	return false
}
