/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/mozilla-services/pushgo/retry"
)

// ===
// Google Cloud Messaging Proprietary Ping interface
// NOTE: This is still experimental.
func NewGCMPing() (r *GCMPing) {
	r = &GCMPing{
		closeSignal: make(chan bool),
	}
	return r
}

type GCMClient interface {
	// for testing, based off minimial requirements from http.Client
	Do(*http.Request) (*http.Response, error)
}

type GCMPing struct {
	logger      *SimpleLogger
	metrics     Statistician
	store       Store
	client      GCMClient
	url         string
	collapseKey string
	dryRun      bool
	apiKey      string
	ttl         uint64
	rh          *retry.Helper
	closeOnce   Once
	closeSignal chan bool
}

type GCMPingConfig struct {
	APIKey      string `toml:"api_key" env:"gcm_api_key"` //GCM Dev API Key
	CollapseKey string `toml:"collapse_key" env:"gcm_collapse_key"`
	DryRun      bool   `toml:"dry_run" env:"gcm_dry_run"`
	TTL         string
	URL         string //GCM URL
	IdleConns   int    `toml:"idle_conns" env:"gcm_idle_conns"`
	Retry       retry.Config
}

type GCMRequest struct {
	Regs        [1]string `json:"registration_ids"`
	CollapseKey string    `json:"collapse_key"`
	TTL         uint64    `json:"time_to_live"`
	DryRun      bool      `json:"dry_run"`
	Data        *GCMData  `json:"data,omitempty"`
}

type GCMPingData struct {
	RegID string `json:"regid"`
}

type GCMData struct {
	Msg string `json:"msg"`
}

func (r *GCMPing) ConfigStruct() interface{} {
	return &GCMPingConfig{
		URL:         "https://android.googleapis.com/gcm/send",
		APIKey:      "YOUR_API_KEY",
		CollapseKey: "simplepush",
		DryRun:      false,
		TTL:         "72h",
		IdleConns:   50,
		Retry: retry.Config{
			Retries:   5,
			Delay:     "200ms",
			MaxDelay:  "5s",
			MaxJitter: "400ms",
		},
	}
}

func (r *GCMPing) Init(app *Application, config interface{}) (err error) {
	r.logger = app.Logger()
	r.metrics = app.Metrics()
	r.store = app.Store()
	conf := config.(*GCMPingConfig)

	r.url = conf.URL
	r.collapseKey = conf.CollapseKey
	r.dryRun = conf.DryRun

	if r.apiKey = conf.APIKey; len(r.apiKey) == 0 {
		r.logger.Panic("propping", "Missing GCM API key", nil)
		return ConfigurationErr
	}

	ttl, err := time.ParseDuration(conf.TTL)
	if err != nil {
		r.logger.Panic("propping", "Could not parse TTL",
			LogFields{"error": err.Error(), "ttl": conf.TTL})
		return err
	}
	r.ttl = uint64(ttl / time.Second)

	if r.rh, err = conf.Retry.NewHelper(); err != nil {
		r.logger.Panic("propping", "Error configuring retry helper",
			LogFields{"error": err.Error()})
		return err
	}
	r.rh.CloseNotifier = r
	r.rh.CanRetry = IsPingerTemporary

	r.client = &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: conf.IdleConns,
		},
	}
	return nil
}

func (r *GCMPing) CanBypassWebsocket() bool {
	// GCM can work even if the client's websocket connection
	// has timed out or closed. We do not need to try to send the
	// message on both channels.
	return true
}

func (r *GCMPing) Register(uaid string, pingData []byte) (err error) {
	if r.logger.ShouldLog(INFO) {
		r.logger.Debug("propping", "Storing connect data",
			LogFields{"connect": string(pingData)})
	}
	if err = r.store.PutPing(uaid, pingData); err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not store GCM registration data",
				LogFields{"error": err.Error()})
		}
		return err
	}
	return nil
}

func (r *GCMPing) retryAfter(header string) (ok bool) {
	d, ok := ParseRetryAfter(header)
	if !ok {
		return true
	}
	select {
	case <-r.closeSignal:
		return false
	case <-time.After(d):
	}
	return true
}

func (r *GCMPing) Send(uaid string, vers int64, data string) (ok bool, err error) {
	pingData, err := r.store.FetchPing(uaid)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not fetch GCM registration data",
				LogFields{"error": err.Error(), "uaid": uaid})
		}
		return false, err
	}
	if len(pingData) == 0 {
		if r.logger.ShouldLog(INFO) {
			r.logger.Info("propping", "No GCM registration data for device",
				LogFields{"uaid": uaid})
		}
		return false, nil
	}
	ping := new(GCMPingData)
	if err = json.Unmarshal(pingData, ping); err != nil {
		if r.logger.ShouldLog(WARNING) {
			r.logger.Warn("propping", "Could not parse GCM registration data",
				LogFields{"error": err.Error(), "uaid": uaid})
		}
		return false, err
	}
	if len(ping.RegID) == 0 {
		if r.logger.ShouldLog(INFO) {
			r.logger.Info("propping", "Missing GCM registration ID",
				LogFields{"uaid": uaid})
		}
		return false, nil
	}
	request := &GCMRequest{
		// google docs lie. You MUST send the regid as an array, even if it's one
		// element.
		Regs:        [1]string{ping.RegID},
		CollapseKey: r.collapseKey,
		TTL:         r.ttl,
		DryRun:      r.dryRun,
		Data: &GCMData{
			Msg: data,
		},
	}
	if r.logger.ShouldLog(DEBUG) {
		r.logger.Debug("propping", "GCM Ping data",
			LogFields{"connect": string(pingData)})
	}
	body, err := json.Marshal(request)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not marshal GCM request",
				LogFields{"error": err.Error(), "uaid": uaid})
		}
		return false, err
	}
	sendOnce := func() (err error) {
		req, err := http.NewRequest("POST", r.url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Add("Authorization", fmt.Sprintf("key=%s", r.apiKey))
		req.Header.Add("Content-Type", "application/json")
		if r.logger.ShouldLog(DEBUG) {
			r.logger.Debug("propping", "#### Sending GCM update",
				LogFields{
					"url":           r.url,
					"headers":       fmt.Sprintf("%+v", req.Header),
					"authorization": fmt.Sprintf("key=%s", r.apiKey),
					"body":          string(body),
					"data":          string(data),
				})
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		// Consume the response body so the underlying TCP connection can be reused.
		io.Copy(ioutil.Discard, resp.Body)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if r.logger.ShouldLog(DEBUG) {
				r.logger.Debug("propping", "Ping message sent successfully.", nil)
			}
			return nil
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			ok := r.retryAfter(resp.Header.Get("Retry-After"))
			if !ok {
				return PingerClosedErr
			}
			return &PingerError{fmt.Sprintf(
				"Retrying after receiving status code: %d", resp.StatusCode), true}
		}
		return &PingerError{fmt.Sprintf(
			"Unexpected status code: %d", resp.StatusCode), false}
	}
	retries, err := r.rh.RetryFunc(sendOnce)
	r.metrics.IncrementBy("ping.gcm.retry", int64(retries))
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Failed to send GCM message",
				LogFields{"error": err.Error(), "uaid": uaid})
		}
		r.metrics.Increment("ping.gcm.error")
		return false, err
	}
	r.metrics.Increment("ping.gcm.success")
	return true, nil
}

func (r *GCMPing) Status() (ok bool, err error) {
	return true, nil
}

func (r *GCMPing) CloseNotify() <-chan bool {
	return r.closeSignal
}

func (r *GCMPing) Close() error {
	close(r.closeSignal)
	return nil
}

func init() {
	AvailablePings["gcm"] = func() HasConfigStruct { return new(GCMPing) }
}
