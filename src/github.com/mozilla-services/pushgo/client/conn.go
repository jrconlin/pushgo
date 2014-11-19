/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	ws "golang.org/x/net/websocket"

	"github.com/mozilla-services/pushgo/id"
)

const (
	NoStatus = -1
)

var (
	ErrMismatchedIds    = &ClientError{"Mismatched synchronous request ID."}
	ErrUnknownType      = &ClientError{"Unknown request type."}
	ErrDuplicateRequest = &ClientError{"Duplicate request."}
	ErrInvalidState     = &ClientError{"Invalid client state."}
	ErrNoId             = &ClientError{"Anonymous packet type."}
)

// TODO: Separate command (`hello`, `register`, `unregister`) and
// message (`notification`) decoders. Add typed error decoders.

type Conn struct {
	Socket        *ws.Conn      // The underlying WebSocket connection.
	PingInterval  time.Duration // The amount of time the connection may remain idle before sending a ping.
	PingDeadlime  time.Duration // The amount of time to wait for a pong before closing the connection.
	DecodeDefault bool          // Use the default message decoders if a custom decoder is not registered.
	SpoolAll      bool          // Spool incoming notifications sent on deregistered channels.
	requests      chan Request
	replies       chan Reply
	Packets       chan Packet
	channels      Channels
	channelLock   sync.RWMutex
	decoders      Decoders
	decoderLock   sync.RWMutex
	signalChan    chan bool
	closeLock     sync.Mutex
	closeWait     sync.WaitGroup
	lastErr       error
	isClosing     bool
	isClosed      bool
}

type (
	Channels map[string]bool
	Decoders map[string]Decoder
	Outbox   map[interface{}]Request
	Requests map[PacketType]Request
	Outboxes map[PacketType]Outbox
	Fields   map[string]interface{}
)

type Decoder interface {
	Decode(c *Conn, fields Fields, statusCode int, errorText string) (Packet, error)
}

type DecoderFunc func(c *Conn, fields Fields, statusCode int, errorText string) (Packet, error)

func (d DecoderFunc) Decode(c *Conn, fields Fields, statusCode int, errorText string) (Packet, error) {
	return d(c, fields, statusCode, errorText)
}

var DefaultDecoders = Decoders{
	"hello":        DecoderFunc(decodeHelo),
	"register":     DecoderFunc(decodeRegister),
	"notification": DecoderFunc(decodeNotification),
}

func Dial(origin string) (conn *Conn, deviceId string, err error) {
	if deviceId, err = id.Generate(); err != nil {
		return nil, "", err
	}
	if conn, err = DialId(origin, &deviceId); err != nil {
		return nil, "", err
	}
	return conn, deviceId, nil
}

func DialId(origin string, deviceId *string, channelIds ...string) (*Conn, error) {
	conn, err := DialOrigin(origin)
	if err != nil {
		return nil, err
	}
	actualId, err := conn.WriteHelo(*deviceId, channelIds...)
	if err != nil {
		if err := conn.Close(); err != nil {
			return nil, err
		}
		return nil, err
	}
	*deviceId = actualId
	return conn, nil
}

func DialOrigin(origin string) (*Conn, error) {
	socket, err := ws.Dial(origin, "", origin)
	if err != nil {
		return nil, err
	}
	return NewConn(socket, false), nil
}

func NewConn(socket *ws.Conn, spoolAll bool) *Conn {
	conn := &Conn{
		Socket:        socket,
		PingInterval:  30 * time.Minute,
		PingDeadlime:  10 * time.Second,
		DecodeDefault: true,
		SpoolAll:      spoolAll,
		requests:      make(chan Request),
		replies:       make(chan Reply),
		Packets:       make(chan Packet),
		channels:      make(Channels),
		decoders:      make(Decoders),
		signalChan:    make(chan bool),
	}
	conn.closeWait.Add(2)
	go conn.Send()
	go conn.Receive()
	return conn
}

// Origin returns the origin of the Simple Push server.
func (c *Conn) Origin() string {
	return c.Socket.RemoteAddr().String()
}

// RegisterDecoder registers a decoder for the specified message type. Message
// types are case-insensitive.
func (c *Conn) RegisterDecoder(messageType string, d Decoder) {
	name := strings.ToLower(messageType)
	c.decoderLock.Lock()
	c.decoders[name] = d
	c.decoderLock.Unlock()
}

// Decoder returns a registered custom decoder, or the default decoder if
// c.DecodeDefault == true.
func (c *Conn) Decoder(messageType string) (d Decoder) {
	name := strings.ToLower(messageType)
	c.decoderLock.RLock()
	d = c.decoders[name]
	c.decoderLock.RUnlock()
	if d == nil && c.DecodeDefault {
		d = DefaultDecoders[name]
	}
	return
}

// Close closes the connection to the push server and waits for the read and
// write loops to exit. Any pending ReadMessage(), AcceptBatch(), or
// AcceptUpdate() calls will be unblocked with errors. All registrations and
// pending updates will be dropped.
func (c *Conn) Close() (err error) {
	c.closeLock.Lock()
	err = c.lastErr
	if c.isClosed {
		c.closeLock.Unlock()
		return err
	}
	c.isClosed = true
	c.lastErr = c.signalClose()
	c.closeLock.Unlock()
	c.removeAllChannels()
	return
}

// CloseNotify returns a receive-only channel that is closed when the
// underlying socket is closed.
func (c *Conn) CloseNotify() <-chan bool {
	return c.signalChan
}

// fatal acquires c.closeLock, closes the socket, and releases the lock,
// recording the error in c.lastErr.
func (c *Conn) fatal(err error) {
	c.closeLock.Lock()
	c.signalClose()
	c.lastErr = err
	c.closeLock.Unlock()
}

// stop closes the socket without waiting for the read and write loops to
// complete.
func (c *Conn) stop() (err error) {
	c.closeLock.Lock()
	err = c.signalClose()
	c.closeLock.Unlock()
	return
}

// signalClose closes the underlying socket. The caller must hold c.closeLock.
func (c *Conn) signalClose() (err error) {
	if c.isClosing {
		return
	}
	err = c.Socket.Close()
	close(c.signalChan)
	c.isClosing = true
	return
}

func (c *Conn) Receive() {
	defer c.closeWait.Done()
	for ok := true; ok; {
		packet, err := c.readPacket()
		if err != nil {
			c.fatal(err)
			break
		}
		var (
			reply   Reply
			ok      bool
			replies chan Reply
			packets chan Packet
		)
		if reply, ok = packet.(Reply); ok {
			// This is a reply to a client request.
			replies = c.replies
		} else {
			packets = c.Packets
		}
		select {
		case ok = <-c.signalChan:
		case packets <- packet:
		case replies <- reply:
		}
	}
	close(c.Packets)
}

// cancel cancels a pending request.
func cancel(request Request) {
	request.Error(io.EOF)
	request.Close()
}

func (*Conn) processReply(requests Requests, outboxes Outboxes, reply Reply) error {
	var id interface{}
	if id = reply.Id(); id == nil {
		return ErrNoId
	}
	var (
		request   Request
		isPending bool
	)
	if reply.Sync() {
		request, isPending = requests[reply.Type()]
		if isPending {
			delete(requests, reply.Type())
		}
	} else if outbox, ok := outboxes[reply.Type()]; ok {
		request, isPending = outbox[id]
		if isPending {
			delete(outbox, id)
		}
	}
	if !isPending {
		// Unsolicited reply.
		return ErrInvalidState
	}
	request.Reply(reply)
	request.Close()
	return nil
}

func (c *Conn) processRequest(requests Requests, outboxes Outboxes, request Request) error {
	var (
		outbox    Outbox
		hasOutbox bool
		id        interface{}
		err       error
	)
	if !request.CanReply() {
		goto Send
	}
	if id = request.Id(); id == nil {
		request.Error(ErrNoId)
		request.Close()
		return nil
	}
	if request.Sync() {
		pending, isPending := requests[request.Type()]
		if !isPending {
			goto Send
		}
		// Multiple synchronous requests (e.g., Helo handshakes and pings) with the
		// same ID should be idempotent. Synchronous requests with different IDs are
		// not supported.
		if pending.Id() != id {
			request.Error(ErrMismatchedIds)
		}
		request.Close()
		return nil
	}
	if outbox, hasOutbox = outboxes[request.Type()]; hasOutbox {
		if _, isPending := outbox[id]; !isPending {
			goto Send
		}
		// Reject duplicate pending requests.
		request.Error(ErrDuplicateRequest)
		request.Close()
		return nil
	}
Send:
	if err = ws.JSON.Send(c.Socket, request); err != nil {
		request.Error(err)
		request.Close()
		return nil
	}
	if !request.CanReply() {
		request.Close()
		return nil
	}
	// Track replies.
	if request.Sync() {
		requests[request.Type()] = request
		return nil
	}
	if !hasOutbox {
		outboxes[request.Type()] = make(Outbox)
		outbox = outboxes[request.Type()]
	}
	outbox[id] = request
	return nil
}

func (c *Conn) Send() {
	defer c.closeWait.Done()
	requests, outboxes := make(Requests), make(Outboxes)
	for ok := true; ok; {
		var err error
		select {
		case ok = <-c.signalChan:
		case request := <-c.requests:
			err = c.processRequest(requests, outboxes, request)

		case reply := <-c.replies:
			err = c.processReply(requests, outboxes, reply)
		}
		if err != nil {
			c.fatal(err)
			break
		}
	}
	// Unblock pending requests.
	for requestType, request := range requests {
		delete(requests, requestType)
		cancel(request)
	}
	for requestType, outbox := range outboxes {
		delete(outboxes, requestType)
		for id, request := range outbox {
			delete(outbox, id)
			cancel(request)
		}
	}
}

// WriteRequest sends an outgoing request, returning io.EOF if the connection
// is closed.
func (c *Conn) WriteRequest(request Request) (reply Reply, err error) {
	select {
	case c.requests <- request:
		return request.Do()
	case <-c.signalChan:
	}
	return nil, io.EOF
}

// WriteHelo initiates a handshake with the server. Duplicate handshakes are
// permitted if the device ID is identical to the prior ID, or left empty;
// otherwise, the server will close the connection.
func (c *Conn) WriteHelo(deviceId string, channelIds ...string) (actualId string, err error) {
	request := NewHelo(deviceId, channelIds)
	reply, err := c.WriteRequest(request)
	if err != nil {
		return
	}
	helo, ok := reply.(ServerHelo)
	if !ok {
		// Unexpected reply packet from server.
		return "", ErrInvalidState
	}
	if helo.StatusCode >= 300 && helo.StatusCode < 400 {
		if len(helo.Redirect) == 0 {
			return "", &IncompleteError{"hello", c.Origin(), "redirect"}
		}
		return "", &RedirectError{helo.Redirect, helo.StatusCode}
	}
	if helo.StatusCode >= 200 && helo.StatusCode < 300 {
		return helo.DeviceId, nil
	}
	return "", &ServerError{"hello", c.Origin(), "Unexpected status code.", helo.StatusCode}
}

// Subscribe subscribes a client to a new channel.
func (c *Conn) Subscribe() (channelId, endpoint string, err error) {
	if channelId, err = id.Generate(); err != nil {
		return "", "", err
	}
	if endpoint, err = c.Register(channelId); err != nil {
		return "", "", err
	}
	return
}

// Register subscribes a client to the specified channel. The reply is not
// read synchronously because the push server may interleave other replies and
// notification requests before fulfilling the registration.
func (c *Conn) Register(channelId string) (endpoint string, err error) {
	request := NewRegister(channelId)
	reply, err := c.WriteRequest(request)
	if err != nil {
		return
	}
	register, ok := reply.(ServerRegister)
	if !ok {
		return "", ErrInvalidState
	}
	if register.StatusCode >= 200 && register.StatusCode < 300 {
		if len(register.Endpoint) == 0 {
			return "", &IncompleteError{"register", c.Origin(), "endpoint"}
		}
		c.addChannel(channelId)
		return register.Endpoint, nil
	}
	if register.StatusCode == 409 {
		return "", &ClientError{fmt.Sprintf("The channel ID `%s` is in use.", channelId)}
	}
	return "", &ServerError{"register", c.Origin(), "Unexpected status code.", register.StatusCode}
}

// Registered indicates whether the client is subscribed to the specified
// channel.
func (c *Conn) Registered(channelId string) (ok bool) {
	c.channelLock.RLock()
	ok = c.channels[channelId]
	c.channelLock.RUnlock()
	return
}

func (c *Conn) addChannel(channelId string) {
	c.channelLock.Lock()
	c.channels[channelId] = true
	c.channelLock.Unlock()
}

func (c *Conn) removeChannel(channelId string) {
	c.channelLock.Lock()
	delete(c.channels, channelId)
	c.channelLock.Unlock()
}

func (c *Conn) removeAllChannels() {
	c.channelLock.Lock()
	for id := range c.channels {
		delete(c.channels, id)
	}
	c.channelLock.Unlock()
}

// Unregister signals that the client is no longer interested in receiving
// updates for a particular channel. The server never replies to this message.
func (c *Conn) Unregister(channelId string) (err error) {
	request := NewUnregister(channelId, false)
	if _, err = c.WriteRequest(request); err != nil {
		return
	}
	c.removeChannel(channelId)
	return
}

// Purge hints the push server to prune all channel registrations for the
// connection. This is a no-op if purging is disabled.
func (c *Conn) Purge() (err error) {
	request := NewPurge(false)
	_, err = c.WriteRequest(request)
	return
}

// ReadBatch consumes a batch of messages sent by the push server. Returns
// io.EOF if the client is closed.
func (c *Conn) ReadBatch() ([]Update, error) {
	for packet := range c.Packets {
		if updates, ok := packet.(ServerUpdates); ok {
			return updates, nil
		}
	}
	return nil, io.EOF
}

// AcceptBatch accepts multiple updates sent to the client. Clients should
// acknowledge updates within the specified window (the spec suggests 60 sec.
// for reference implementations) to avoid retransmission.
func (c *Conn) AcceptBatch(updates []Update) (err error) {
	request := NewACK(updates, false)
	_, err = c.WriteRequest(request)
	return
}

// AcceptUpdate accepts an update sent to the client. Returns io.EOF if the
// client is closed, or a socket error if the write failed.
func (c *Conn) AcceptUpdate(update Update) (err error) {
	return c.AcceptBatch([]Update{update})
}

func (c *Conn) readPacket() (packet Packet, err error) {
	var data []byte
	if err = ws.Message.Receive(c.Socket, &data); err != nil {
		return nil, err
	}
	var (
		messageType, errorText       string
		hasMessageType, hasErrorText bool
	)
	fields := make(Fields)
	statusCode := NoStatus
	if len(data) == 2 && data[0] == '{' && data[1] == '}' {
		// Avoid processing pings.
		messageType, hasMessageType, statusCode = "ping", true, 200
		goto Decode
	}
	if err = json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		messageType, hasMessageType, statusCode = "ping", true, 200
		goto Decode
	}
	// Extract the status code, error string, and message type. Per RFC 7159,
	// Go's JSON library represents numbers as float64 values when decoding into
	// an untyped map.
	if asFloat, ok := fields["status"].(float64); ok && asFloat >= 0 {
		statusCode = int(asFloat)
	}
	errorText, hasErrorText = fields["error"].(string)
	if len(errorText) == 0 {
		hasErrorText = false
	}
	messageType, hasMessageType = fields["messageType"].(string)
	if len(messageType) == 0 {
		hasMessageType = false
	}
	if !hasMessageType {
		// Missing or empty message type. Likely an error response; use the
		// error text and status to construct the error reply, if present.
		var message string
		if hasErrorText {
			message = errorText
		} else {
			message = "Invalid message received from server."
		}
		return nil, &ServerError{"internal", c.Origin(), message, statusCode}
	}
Decode:
	if decoder := c.Decoder(messageType); decoder != nil {
		return decoder.Decode(c, fields, statusCode, errorText)
	}
	if hasErrorText {
		// Unprocessed typed error response.
		return nil, &ServerError{messageType, c.Origin(), errorText, statusCode}
	}
	return nil, nil
}

func decodeHelo(c *Conn, fields Fields, statusCode int, errorText string) (Packet, error) {
	if len(errorText) > 0 {
		return nil, &ServerError{"hello", c.Origin(), errorText, statusCode}
	}
	deviceId, hasDeviceId := fields["uaid"].(string)
	if !hasDeviceId {
		return nil, &IncompleteError{"hello", c.Origin(), "uaid"}
	}
	redirect, _ := fields["redirect"].(string)
	reply := ServerHelo{
		StatusCode: statusCode,
		DeviceId:   deviceId,
		Redirect:   redirect,
	}
	return reply, nil
}

func decodeRegister(c *Conn, fields Fields, statusCode int, errorText string) (Packet, error) {
	if len(errorText) > 0 {
		return nil, &ServerError{"register", c.Origin(), errorText, statusCode}
	}
	channelId, hasChannelId := fields["channelID"].(string)
	if !hasChannelId {
		return nil, &IncompleteError{"register", c.Origin(), "channelID"}
	}
	endpoint, hasEndpoint := fields["pushEndpoint"].(string)
	if !hasEndpoint {
		return nil, &IncompleteError{"register", c.Origin(), "pushEndpoint"}
	}
	reply := ServerRegister{
		StatusCode: statusCode,
		ChannelId:  channelId,
		Endpoint:   endpoint,
	}
	return reply, nil
}

func decodeNotification(c *Conn, fields Fields, statusCode int, errorText string) (Packet, error) {
	if statusCode != NoStatus || len(errorText) > 0 {
		return nil, &ServerError{"notification", c.Origin(), errorText, statusCode}
	}
	updates, hasUpdates := fields["updates"].([]interface{})
	if !hasUpdates {
		return nil, &IncompleteError{"notification", c.Origin(), "updates"}
	}
	var packet ServerUpdates
	for _, field := range updates {
		var (
			update    map[string]interface{}
			channelId string
			version   float64
			ok        bool
		)
		if update, ok = field.(map[string]interface{}); !ok {
			return nil, &IncompleteError{"notification", c.Origin(), "update"}
		}
		if channelId, ok = update["channelID"].(string); !ok {
			return nil, &IncompleteError{"notification", c.Origin(), "channelID"}
		}
		if !c.SpoolAll && !c.Registered(channelId) {
			continue
		}
		if version, ok = update["version"].(float64); !ok {
			return nil, &IncompleteError{"notification", c.Origin(), "version"}
		}
		packet = append(packet, Update{channelId, int64(version)})
	}
	return packet, nil
}
