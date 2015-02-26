/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
)

type PropPinger interface {
	Register(uaid string, pingData []byte) error
	Send(uaid string, vers int64, data string) (ok bool, err error)
	CanBypassWebsocket() bool
	Status() (bool, error)
	Close() error
}

var (
	UnsupportedProtocolErr = errors.New("Unsupported Ping Request")
	ConfigurationErr       = errors.New("Configuration Error")
	ProtocolErr            = errors.New("A protocol error occurred. See logs for details.")
	PingerClosedErr        = &PingerError{"Pinger closed", false}
)

var AvailablePings = make(AvailableExtensions)

func init() {
	AvailablePings["noop"] = func() HasConfigStruct { return new(NoopPing) }
	AvailablePings["udp"] = func() HasConfigStruct { return new(UDPPing) }
	AvailablePings.SetDefault("noop")
}

// IsPingerTemporary indicates whether the given error is a temporary
// pinger error.
func IsPingerTemporary(err error) bool {
	pingErr, ok := err.(*PingerError)
	return !ok || pingErr.Temporary
}

type PingerError struct {
	Message   string
	Temporary bool
}

func (err *PingerError) Error() string {
	return err.Message
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
func (r *NoopPing) Send(string, int64, string) (bool, error) {
	return false, nil
}

func (r *NoopPing) Status() (bool, error) {
	return true, nil
}

func (r *NoopPing) Close() error {
	return nil
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
	URL string //carrier UDP Proxy URL
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
func (r *UDPPing) Send(string, int64, string) (bool, error) {
	// Obviously, this needs to be filled out with the appropriate
	// setup and calls to communicate to the remote server.
	// Since UDP is not actually defined, we're returning this
	// error.
	return false, UnsupportedProtocolErr
}

func (r *UDPPing) Status() (bool, error) {
	return false, UnsupportedProtocolErr
}

func (r *UDPPing) Close() error {
	return nil
}
