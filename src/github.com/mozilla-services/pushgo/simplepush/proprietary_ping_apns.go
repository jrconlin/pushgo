/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

// TODO: Need to build service to pull from Feedback Service and terminate
// clients that are "dead".

package simplepush

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/mozilla-services/pushgo/retry"
)

// ===
// Apple Push Notification System interface
// NOTE: This is still experimental

/* I am very tempted to haul this out and make it a stand alone service.
This is because APNS wants to have a long lived socket connection that you
feed binary data though. In essence the stand alone would be a queue that
took messages and sent them on, retrying on it's own.
*/

type APNSSocket struct {
	addr     string
	conn     net.Conn
	tlsconn  *tls.Conn
	Host     string
	CertFile string
	KeyFile  string
	Timeout  time.Duration
}

type APNSPing struct {
	logger      *SimpleLogger
	metrics     Statistician
	store       Store
	client      GCMClient
	host        string
	port        uint
	ttl         uint64
	rh          *retry.Helper
	closeOnce   Once
	closeSignal chan bool
	conn        *APNSSocket
}

type APNSPingConfig struct {
	Host     string `toml:"host" env:"apns_host"`
	Port     uint   `toml:"port" env:"apns_port"`
	Timeout  string `toml:"timeout" env:"apns_timeout"`
	CertFile string `toml:"certfile" env:"apns_certfile"`
	KeyFile  string `toml:"keyfile" env:"apns_keyfile"`
	Retry    retry.Config
}

type APNSPingData struct {
	Version int64  `json:"version"`
	Data    string `json:"data",omitempty`
}

type APNSPayload struct {
	Alert            *APNSPingData `json:"alert"`
	Badge            uint64        `json:"badge,omitempty"`
	Sound            string        `json:"sound,omitempty"`
	ContentAvailable uint8         `json:"content-available"`
}
type APNSStatus struct {
	Command    uint8
	Status     uint8
	Identifier uint32
}

const (
	APNS_SEND_IMMEDIATE = 10
	APNS_SEND_DELAY     = 5
	//Ok, "payload" includes the version data and JSON content, so... little
	// less than 2048*1024.
	APNS_MAX_PAYLOAD_LEN = 2048 * 1024
)

var (
	ErrAPNSPayloadTooLarge = errors.New("Payload Data limited to less than 2048K")
	ErrAPNSUnavailable     = errors.New("APNS is unavailable")
)

func (r *APNSSocket) Dial(addr string) (err error) {
	cert, err := tls.LoadX509KeyPair(r.CertFile, r.KeyFile)
	if err != nil {
		return
	}
	tlsConf := &tls.Config{
		ServerName:   r.Host,
		Certificates: []tls.Certificate{cert},
	}
	if r.Timeout != 0 {
		r.conn, err = net.DialTimeout("tcp", addr, r.Timeout)
		if err != nil {
			return
		}
	} else {
		r.conn, err = net.Dial("tcp", addr)
		if err != nil {
			return
		}
	}
	r.tlsconn = tls.Client(r.conn, tlsConf)
	err = r.tlsconn.Handshake()
	if err != nil {
	}
	return
}

func (r *APNSSocket) Send(token string, data *APNSPingData) (err error) {
	// Convert the data block into a datastream
	if int64(len(data.Data)) > APNS_MAX_PAYLOAD_LEN {
		return ErrAPNSPayloadTooLarge
	}
	btoken, err := hex.DecodeString(token)
	if err != nil {
		return err
	}
	payload := &APNSPayload{
		Alert: data,
		//Specify "Content Available = 1" for silent alerts
		ContentAvailable: 1,
	}
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if int64(len(payloadBody)) > APNS_MAX_PAYLOAD_LEN {
		return ErrAPNSPayloadTooLarge
	}

	// TODO: Wrap with retry logic.
	// Build item
	item := bytes.NewBuffer([]byte{})
	// Device Token
	binary.Write(item, binary.BigEndian, uint8(1))
	binary.Write(item, binary.BigEndian, uint16(len(btoken)))
	binary.Write(item, binary.BigEndian, btoken)
	// Payload
	binary.Write(item, binary.BigEndian, uint8(2))
	binary.Write(item, binary.BigEndian, uint16(len(payloadBody)))
	binary.Write(item, binary.BigEndian, payloadBody)
	// Notification ID (currently, just "1")
	binary.Write(item, binary.BigEndian, uint8(3))
	binary.Write(item, binary.BigEndian, uint16(1))
	binary.Write(item, binary.BigEndian, 1)
	// Expiration Date (currently now + 3 days)
	exp := time.Now().UTC().Add(time.Hour * 72).Unix()
	binary.Write(item, binary.BigEndian, uint8(4))
	binary.Write(item, binary.BigEndian, uint16(4))
	binary.Write(item, binary.BigEndian, uint32(exp))
	// Priority
	binary.Write(item, binary.BigEndian, uint8(5))
	binary.Write(item, binary.BigEndian, uint16(1))
	binary.Write(item, binary.BigEndian, APNS_SEND_IMMEDIATE)

	// Build the wrapper frame
	frame := bytes.NewBuffer([]byte{})
	// frame command is always 2
	binary.Write(frame, binary.BigEndian, uint8(2))
	binary.Write(frame, binary.BigEndian, uint32(len(item.Bytes())))
	binary.Write(frame, binary.BigEndian, item.Bytes())

	// try sending...
	_, err = r.tlsconn.Write(frame.Bytes())
	if err != nil {
		return
	}
	// If we hear nothing, all went well.
	// which, kinda sucks, because it means we have no idea until the
	// connection times out.
	r.tlsconn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply := make([]byte, 6)
	n, err := r.tlsconn.Read(reply[:])
	if err != nil && err != io.EOF {
		return
	}
	if n != 0 {
		status := &APNSStatus{}
		binary.Read(bytes.NewBuffer(reply), binary.BigEndian, &status)
		if status.Status != 0 {
			msg := fmt.Sprintf("Resend request: code %d, ID %x",
				status.Status,
				status.Identifier)
			return errors.New(msg)
		}
		err = nil
	}
	return
}

func (r *APNSSocket) Close() {
	r.tlsconn.Close()
}

func NewAPNSPing() (r *APNSPing) {
	r = &APNSPing{
		closeSignal: make(chan bool),
	}
	return r
}

func (r *APNSPing) ConfigStruct() interface{} {
	return &APNSPingConfig{
		// Point to dev sandbox. Production is at
		// gateway.push.apple.com
		Host:     "gateway.sandbox.push.apple.com",
		Port:     2195,
		CertFile: "apns.cert",
		KeyFile:  "apns.key",
		Timeout:  "3s",
		Retry: retry.Config{
			Retries:   5,
			Delay:     "200ms",
			MaxDelay:  "5s",
			MaxJitter: "400ms",
		},
	}
}

func (r *APNSPing) Init(app *Application, config interface{}) (err error) {
	r.logger = app.Logger()
	r.metrics = app.Metrics()
	r.store = app.Store()
	conf := config.(*APNSPingConfig)

	r.host = conf.Host
	r.port = conf.Port
	timeout, err := time.ParseDuration(conf.Timeout)
	if err != nil {
		r.logger.Error("propping", "Could not parse timeout, using default",
			LogFields{"error": err.Error()})
		timeout = 3 * time.Second
	}
	if r.rh, err = conf.Retry.NewHelper(); err != nil {
		r.logger.Panic("propping",
			"Error configuring APNS retry helper",
			LogFields{"error": err.Error()},
		)
		return
	}

	r.conn = &APNSSocket{Timeout: timeout,
		Host:     conf.Host,
		CertFile: conf.CertFile,
		KeyFile:  conf.KeyFile,
	}
	if r.port == 0 {
		r.port = 2195
	}
	if err = r.conn.Dial(fmt.Sprintf("%s:%d", r.host, r.port)); err != nil {
		r.logger.Panic("propping",
			"Could not connect to APNS service",
			LogFields{"error": err.Error()})
		return
	}
	r.rh.CloseNotifier = r
	r.rh.CanRetry = IsPingerTemporary
	return
}

func (r *APNSPing) CanBypassWebsocket() bool {
	return true
}

type apnsConnectData struct {
	Type  string `json:"type,omitempty"`
	Token string `json:"token"`
}

func (r *APNSPing) Register(uaid string, pingData []byte) (err error) {
	if r.logger.ShouldLog(INFO) {
		r.logger.Info("propping", "Storing connect data",
			LogFields{"connect": string(pingData)})
	}
	// only need the token
	ping := &apnsConnectData{}
	if err = json.Unmarshal(pingData, ping); err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not parse connection data",
				LogFields{"connectData": string(pingData),
					"error": err.Error()})
		}
		return err
	}
	if err = r.store.PutPing(uaid, []byte(ping.Token)); err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not store APNS registration data",
				LogFields{"error": err.Error()})
		}
		return err
	}
	return nil
}

func (r *APNSPing) Send(uaid string, vers int64, data string) (ok bool, err error) {
	if r.conn == nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Trying to send to closed socket",
				LogFields{"uaid": uaid})
		}
		return false, ErrAPNSUnavailable
	}

	token, err := r.store.FetchPing(uaid)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not fetch APNS registration data",
				LogFields{"error": err.Error(), "uaid": uaid})
		}
		return false, err
	}
	if len(token) == 0 {
		if r.logger.ShouldLog(INFO) {
			r.logger.Info("propping", "No APNS registration data for device",
				LogFields{"uaid": uaid})
		}
		return false, nil
	}
	if r.conn == nil {
		return false, ErrAPNSUnavailable
	}
	ping := &APNSPingData{
		Version: vers,
		Data:    data,
	}
	err = r.conn.Send(string(token), ping)
	if err != nil {
		if r.logger.ShouldLog(WARNING) {
			r.logger.Warn("proppring", "Could not send APNS ping",
				LogFields{"uaid": uaid,
					"error": err.Error()})
		}
		return false, err
	}
	return true, nil
}

func (r *APNSPing) Status() (ok bool, err error) {
	return r.conn != nil, nil
}

func (r *APNSPing) CloseNotify() <-chan bool {
	return r.closeSignal
}

func (r *APNSPing) Close() error {
	r.conn.Close()
	close(r.closeSignal)
	return nil
}

func init() {
	AvailablePings["apns"] = func() HasConfigStruct { return new(APNSPing) }
}
