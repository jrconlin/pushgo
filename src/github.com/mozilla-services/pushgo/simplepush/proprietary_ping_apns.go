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
	"strings"
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
	title       string
	body        string
}

type APNSPingConfig struct {
	Host     string `toml:"host" env:"apns_host"`
	Port     uint   `toml:"port" env:"apns_port"`
	Timeout  string `toml:"timeout" env:"apns_timeout"`
	CertFile string `toml:"certfile" env:"apns_certfile"`
	KeyFile  string `toml:"keyfile" env:"apns_keyfile"`
	Retry    retry.Config
	Title    string `toml:"default_title" env:"apns_default_title"`
	Body     string `toml:"default_body" env:"apns_default_body"`
}

type APNSPingData struct {
	Version int64
	Data    string
	Title   string
	Body    string
}

/*
// Future use
type APNSAlert struct {
	Title 		string `json:"title"`
	Body		string `json:"body"`
	TitleLocKey string `json:"title-loc-key"`
	TitleLocArgs []string `json:"title-loc-args"`
	ActionLocKey string `json:"action-loc-key"`
	LocKey string `json:"loc-key"`
	LocArgs []string `json:"loc-args"`
	LaunchImage string `json:"launch-image"`
}
*/

type APNSAlert struct {
	// Alert is either an APNSAlert or a string containing the alert body
	//Alert            *APNSAlert `json:"alert"`
	Title            string `json:"title"`
	Body             string `json:"body"`
	Badge            uint64 `json:"badge,omitempty"`
	Sound            string `json:"sound,omitempty"`
	ContentAvailable uint8  `json:"content-available"`
}

type APNSPayload struct {
	APS     *APNSAlert `json:"aps"`
	Version int64      `json:"version"`
	Data    string     `json:"data",omitempty"`
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
	notifNum               = uint64(1)
)

func (r *APNSSocket) Dial(addr string) (err error) {
	if addr == "" {
		addr = r.addr
	}
	fmt.Printf("!!! Trying %s %s\n", r.CertFile, r.KeyFile)
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
	if r.addr == "" {
		r.addr = addr
	}
	r.tlsconn = tls.Client(r.conn, tlsConf)
	err = r.tlsconn.Handshake()
	if err != nil {
	}
	return
}

func (r *APNSSocket) Send(token string, data *APNSPingData) (err error) {
	btoken, err := hex.DecodeString(token)
	if err != nil {
		return err
	}
	payload := &APNSPayload{
		APS: &APNSAlert{
			Title:            data.Title,
			Body:             data.Body,
			ContentAvailable: 1,
			Sound:            "default",
		},
		Version: data.Version,
		Data:    data.Data,
	}
	payloadBody, err := json.Marshal(payload)
	fmt.Printf("\n>> Payload:\n%s\n", payloadBody)
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
	binary.Write(item, binary.BigEndian, uint8(0))
	binary.Write(item, binary.BigEndian, uint16(len(btoken)))
	binary.Write(item, binary.BigEndian, btoken)
	// Payload
	binary.Write(item, binary.BigEndian, uint16(len(payloadBody)))
	binary.Write(item, binary.BigEndian, payloadBody)
	// try sending...
	fmt.Printf("\n >>>> Sending APNS #%d to %s[%s]\n%v\n", notifNum, r.addr, token, item.Bytes())
	notifNum++
	for retry := 2; retry != 0; retry-- {
		_, err = r.tlsconn.Write(item.Bytes())
		if err != nil {
			fmt.Printf("\n !!! Got Error %+v %T\n", err, err)
			fmt.Printf("\n\n### Redialing to %s\n", r.addr)
			err = r.Dial(r.addr)
			if err != nil {
				fmt.Printf("\n\n### Dial failed...%s\n", err.Error)
				return
			}
			fmt.Printf("\n\n### Retrying send...\n")
			continue
		}
		fmt.Printf("\n >>>> Success!\n")
		err = nil
		break
	}
	// If we hear nothing, all went well.
	// which, kinda sucks, because it means we have no idea until the
	// connection times out.
	r.tlsconn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply := make([]byte, 6)
	fmt.Printf("\n>>>> Checking read queue\n")
	n, err := r.tlsconn.Read(reply[:])
	if err != nil && err != io.EOF {
		fmt.Printf("\n >>>> Read failed %+v\n", err)
		return
	}
	if n != 0 {
		status := &APNSStatus{}
		binary.Read(bytes.NewBuffer(reply), binary.BigEndian, &status)
		if status.Status != 0 {
			msg := "Resend request: code " +
				strconv.FormatInt(status.Status, 10) +
				" ID " + status.Identifier
			return errors.New(msg)
		}
		fmt.Printf("\nSuccess:: %+v [%v]\n", status, reply)
	} else {
		fmt.Printf(" --- Reader returned 0 length\n")
	}
	fmt.Printf("\n ==== Done \n")
	return nil
}

// Use v2 of send (which apparently doesn't work according to the docs)
func (r *APNSSocket) Send2(token string, data *APNSPingData) (err error) {
	// Convert the data block into a datastream
	if int64(len(data.Data)) > APNS_MAX_PAYLOAD_LEN {
		return ErrAPNSPayloadTooLarge
	}
	btoken, err := hex.DecodeString(token)
	if err != nil {
		return err
	}
	payload := &APNSPayload{
		APS: &APNSAlert{
			Title:            data.Title,
			Body:             data.Body,
			ContentAvailable: 1,
		},
		Version: data.Version,
		Data:    data.Data,
	}
	payloadBody, err := json.Marshal(payload)
	fmt.Printf("\n>> Payload:\n%s\n", payloadBody)
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
	// Notification ID
	binary.Write(item, binary.BigEndian, uint8(3))
	binary.Write(item, binary.BigEndian, uint16(4))
	binary.Write(item, binary.BigEndian, uint32(notifNum))
	// Expiration Date (currently now + 3 days)
	exp := time.Now().UTC().Add(time.Minute * 5).Unix()
	binary.Write(item, binary.BigEndian, uint8(4))
	binary.Write(item, binary.BigEndian, uint16(4))
	binary.Write(item, binary.BigEndian, uint32(exp))
	// Priority
	binary.Write(item, binary.BigEndian, uint8(5))
	binary.Write(item, binary.BigEndian, uint16(1))
	binary.Write(item, binary.BigEndian, uint8(APNS_SEND_IMMEDIATE))

	// Build the wrapper frame
	frame := bytes.NewBuffer([]byte{})
	// frame command is always 2
	binary.Write(frame, binary.BigEndian, uint8(2))
	binary.Write(frame, binary.BigEndian, uint32(len(item.Bytes())))
	binary.Write(frame, binary.BigEndian, item.Bytes())

	// try sending...
	fmt.Printf("\n >>>> Sending APNS #%d to %s[%s]\n%x\n", notifNum, r.addr, token, frame.Bytes())
	notifNum++
	for retry := 2; retry != 0; retry-- {
		_, err = r.tlsconn.Write(frame.Bytes())
		if err != nil {
			err = r.Dial(r.addr)
			if err != nil {
				return
			}
			continue
		}
		err = nil
		break
	}
	// If we hear nothing, all went well.
	// which, kinda sucks, because it means we have no idea until the
	// connection times out.
	r.tlsconn.SetReadDeadline(time.Now().Add(r.Timeout))
	reply := make([]byte, 6)
	n, err := r.tlsconn.Read(reply[:])
	if err != nil && !strings.Contains(err.Error(), "i/o timeout") {
		return
	}
	if n != 0 {
		status := &APNSStatus{}
		binary.Read(bytes.NewBuffer(reply), binary.BigEndian, &status)
		if status.Status != 0 {
			msg := "Resend request: code " + strconv.FormatInt(status.Status, 10) + " ID: " + status.Identifier
			return errors.New(msg)
		}
	}
	return nil
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
	r.title = conf.Title
	r.body = conf.Body
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
	if err = r.conn.Dial(r.host + ":" +
		strconv.FormatInt(r.port, 10)); err != nil {
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
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
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
	if err = r.store.PutPing(uaid, pingData); err != nil {
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

	pingData := &apnsConnectData{}
	pingStore, err := r.store.FetchPing(uaid)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not fetch ping data for device",
				LogFields{"uaid": uaid, "error": err.Error()})
		}
	}
	if len(pingStore) == 0 {
		if r.logger.ShouldLog(INFO) {
			r.logger.Info("propping", "No APNS registration data for device",
				LogFields{"uaid": uaid})
		}
		return false, nil
	}
	err = json.Unmarshal(pingStore, pingData)
	if err != nil {
		if r.logger.ShouldLog(ERROR) {
			r.logger.Error("propping", "Could not fetch APNS registration data",
				LogFields{"error": err.Error(), "uaid": uaid})
		}
		return false, err
	}
	if r.conn == nil {
		return false, ErrAPNSUnavailable
	}
	ping := &APNSPingData{
		Title:   pingData.Title,
		Body:    pingData.Body,
		Version: vers,
		Data:    data,
	}
	err = r.conn.Send2(string(pingData.Token), ping)
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
