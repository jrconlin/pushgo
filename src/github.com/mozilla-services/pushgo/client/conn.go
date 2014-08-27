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

	ws "code.google.com/p/go.net/websocket"

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
	Packets       chan HasType
	channels      Channels
	channelLock   sync.RWMutex
	decoders      Decoders
	decoderLock   sync.RWMutex
	signalChan    chan bool
	closeLock     sync.Mutex
	closeWait     sync.WaitGroup
	lastErr       error
	isClosing     bool
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
	Decode(c *Conn, fields Fields, statusCode int, errorText string) (HasType, error)
}

type DecoderFunc func(c *Conn, fields Fields, statusCode int, errorText string) (HasType, error)

func (d DecoderFunc) Decode(c *Conn, fields Fields, statusCode int, errorText string) (HasType, error) {
	return d(c, fields, statusCode, errorText)
}

var DefaultDecoders = Decoders{
	"hello":        DecoderFunc(decodeHelo),
	"register":     DecoderFunc(decodeRegister),
	"notification": DecoderFunc(decodeNotification),
}

func Dial(origin string) (*Conn, error) {
	deviceId, err := id.Generate()
	if err != nil {
		return nil, err
	}
	conn, err := DialId(origin, &deviceId)
	if err != nil {
		return nil, err
	}
	return conn, nil
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
		Packets:       make(chan HasType),
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
	defer c.decoderLock.Unlock()
	c.decoderLock.Lock()
	c.decoders[strings.ToLower(messageType)] = d
}

// Decoder returns a registered custom decoder, or the default decoder if
// c.DecodeDefault == true.
func (c *Conn) Decoder(messageType string) (d Decoder) {
	defer c.decoderLock.RUnlock()
	c.decoderLock.RLock()
	name := strings.ToLower(messageType)
	if d = c.decoders[name]; d == nil && c.DecodeDefault {
		d = DefaultDecoders[name]
	}
	return
}

// Close closes the connection to the push server, unblocking all
// ReadMessage(), AcceptBatch(), and AcceptUpdate() calls. All registrations
// and pending updates will be dropped.
func (c *Conn) Close() (err error) {
	err, ok := c.stop()
	if !ok {
		return err
	}
	c.closeWait.Wait()
	c.removeAllChannels()
	return
}

// Acquires c.closeLock, closes the socket, and releases the lock, recording
// the error in c.lastErr.
func (c *Conn) fatal(err error) {
	defer c.closeLock.Unlock()
	c.closeLock.Lock()
	c.signalClose()
	if c.lastErr == nil {
		c.lastErr = err
	}
}

// Acquires c.closeLock, closes the socket, and releases the lock, reporting
// any errors to the caller. ok specifies whether the caller should wait for
// the socket to close before returning.
func (c *Conn) stop() (err error, ok bool) {
	defer c.closeLock.Unlock()
	c.closeLock.Lock()
	if c.isClosing {
		return c.lastErr, false
	}
	return c.signalClose(), true
}

// Closes the underlying socket and unblocks the read and write loops. Assumes
// the caller holds c.closeLock.
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
			packets chan HasType
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

// Cancels a pending request.
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

// Attempts to send an outgoing request, returning an error if the connection
// has been closed.
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
	helo, ok := reply.(*ServerHelo)
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
	channelId, err = id.Generate()
	if err != nil {
		return "", "", err
	}
	endpoint, err = c.Register(channelId)
	if err != nil {
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
	register, ok := reply.(*ServerRegister)
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
func (c *Conn) Registered(channelId string) bool {
	defer c.channelLock.RUnlock()
	c.channelLock.RLock()
	return c.channels[channelId]
}

func (c *Conn) addChannel(channelId string) {
	defer c.channelLock.Unlock()
	c.channelLock.Lock()
	c.channels[channelId] = true
}

func (c *Conn) removeChannel(channelId string) {
	defer c.channelLock.Unlock()
	c.channelLock.Lock()
	delete(c.channels, channelId)
}

func (c *Conn) removeAllChannels() {
	defer c.channelLock.Unlock()
	c.channelLock.Lock()
	for id := range c.channels {
		delete(c.channels, id)
	}
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

func (c *Conn) readPacket() (packet HasType, err error) {
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

func (c *Conn) decodeUpdate(field interface{}) (result Update, err error) {
	update, ok := field.(map[string]interface{})
	if !ok {
		err = &IncompleteError{"notification", c.Origin(), "update"}
		return
	}
	channelId, ok := update["channelID"].(string)
	if !ok {
		err = &IncompleteError{"notification", c.Origin(), "pushEndpoint"}
		return
	}
	var version int64
	if asFloat, ok := update["version"].(float64); ok {
		version = int64(asFloat)
	} else {
		err = &IncompleteError{"notification", c.Origin(), "version"}
		return
	}
	result = Update{
		ChannelId: channelId,
		Version:   version,
	}
	return
}

func decodeHelo(c *Conn, fields Fields, statusCode int, errorText string) (HasType, error) {
	if len(errorText) > 0 {
		return nil, &ServerError{"hello", c.Origin(), errorText, statusCode}
	}
	deviceId, hasDeviceId := fields["uaid"].(string)
	if !hasDeviceId {
		return nil, &IncompleteError{"hello", c.Origin(), "uaid"}
	}
	redirect, _ := fields["redirect"].(string)
	reply := &ServerHelo{
		StatusCode: statusCode,
		DeviceId:   deviceId,
		Redirect:   redirect,
	}
	return reply, nil
}

func decodeRegister(c *Conn, fields Fields, statusCode int, errorText string) (HasType, error) {
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
	reply := &ServerRegister{
		StatusCode: statusCode,
		ChannelId:  channelId,
		Endpoint:   endpoint,
	}
	return reply, nil
}

func decodeNotification(c *Conn, fields Fields, statusCode int, errorText string) (HasType, error) {
	if statusCode != NoStatus || len(errorText) > 0 {
		return nil, &ServerError{"notification", c.Origin(), errorText, statusCode}
	}
	updates, hasUpdates := fields["updates"].([]interface{})
	if !hasUpdates {
		return nil, &IncompleteError{"notification", c.Origin(), "updates"}
	}
	var packet ServerUpdates
	for _, field := range updates {
		update, err := c.decodeUpdate(field)
		if err != nil {
			return nil, err
		}
		if !c.SpoolAll && !c.Registered(update.ChannelId) {
			continue
		}
		packet = append(packet, update)
	}
	return packet, nil
}
