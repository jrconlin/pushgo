package client

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	ws "code.google.com/p/go.net/websocket"
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

// TODO: Extract the spooling logic (`pending{Lock}, messages, SpoolAll`) into
// a higher-level client that supports streaming notifications, automatic
// reconnects, etc.

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
	pending       []Update
	pendingLock   sync.Mutex
	messages      chan Update
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
	Requests map[interface{}]Request
	Fields   map[string]interface{}
)

type Decoder interface {
	Decode(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error)
}

type DecoderFunc func(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error)

func (d DecoderFunc) Decode(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error) {
	return d(c, fields, statusCode, errorText)
}

var DefaultDecoders = Decoders{
	"hello":        DecoderFunc(decodeHelo),
	"register":     DecoderFunc(decodeRegister),
	"notification": DecoderFunc(decodeNotification),
}

func Dial(origin string) (*Conn, error) {
	deviceId, err := GenerateId()
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
		messages:      make(chan Update),
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

// RegisterDecoder registers a decoder `d` for the specified `messageType`.
// Message types are case-insensitive.
func (c *Conn) RegisterDecoder(messageType string, d Decoder) {
	defer c.decoderLock.Unlock()
	c.decoderLock.Lock()
	c.decoders[strings.ToLower(messageType)] = d
}

// Decoder returns a registered custom decoder, or the default decoder if
// `DecodeDefault` is `true`.
func (c *Conn) Decoder(messageType string) (d Decoder) {
	defer c.decoderLock.RUnlock()
	c.decoderLock.RLock()
	name := strings.ToLower(messageType)
	if d = c.decoders[name]; d == nil && c.DecodeDefault {
		d = DefaultDecoders[name]
	}
	return
}

// UnregisterDecoder deregisters a custom decoder.
func (c *Conn) UnregisterDecoder(messageType string) {
	defer c.decoderLock.Unlock()
	c.decoderLock.Lock()
	delete(c.decoders, strings.ToLower(messageType))
}

// Close closes the connection to the push server, unblocking all
// `ReadMessage()`, `AcceptBatch()`, and `AcceptUpdate()` calls.
// All registrations and pending updates will be dropped.
func (c *Conn) Close() (err error) {
	err, ok := c.stop()
	if !ok {
		return err
	}
	c.closeWait.Wait()
	c.removeAllChannels()
	c.removeAllPending()
	return
}

// Acquires `c.closeLock`, closes the socket, and releases the lock, recording
// the error in the `lastErr` field.
func (c *Conn) fatal(err error) {
	defer c.closeLock.Unlock()
	c.closeLock.Lock()
	c.signalClose()
	if c.lastErr == nil {
		c.lastErr = err
	}
}

// Acquires `c.closeLock`, closes the socket, and releases the lock, reporting
// any errors to the caller. The Boolean specifies whether the caller should
// wait for the socket to close before returning.
func (c *Conn) stop() (error, bool) {
	defer c.closeLock.Unlock()
	c.closeLock.Lock()
	if c.isClosing {
		return c.lastErr, false
	}
	return c.signalClose(), true
}

// Closes the underlying socket and unblocks the read and write loops. Assumes
// the caller holds `c.closeLock`.
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
		reply, err := c.readMessage()
		if err != nil {
			c.fatal(err)
			break
		}
		var (
			replies  chan Reply
			update   Update
			messages chan Update
		)
		if reply != nil && reply.HasRequest() {
			// This is a reply to a client request.
			replies = c.replies
		}
		c.pendingLock.Lock()
		if len(c.pending) > 0 {
			update = c.pending[0]
			messages = c.messages
		}
		select {
		case ok = <-c.signalChan:
		case messages <- update:
			c.pending = c.pending[1:]

		case replies <- reply:
		}
		c.pendingLock.Unlock()
	}
	close(c.messages)
}

// Cancels a pending request.
func cancel(request Request) {
	request.Error(io.EOF)
	request.Close()
}

func (c *Conn) Send() {
	defer c.closeWait.Done()
	requests, outboxes := make(map[PacketType]Request), make(map[PacketType]Requests)
	for ok := true; ok; {
		select {
		case ok = <-c.signalChan:
		case request := <-c.requests:
			var (
				outbox    Requests
				hasOutbox bool
				id        interface{}
				err       error
			)
			if request.CanReply() {
				if id = request.Id(); id == nil {
					cancel(request)
					c.fatal(ErrNoId)
					break
				}
				if request.Sync() {
					pending, isPending := requests[request.Type()]
					// Multiple synchronous requests (e.g., `Helo` handshakes and pings) with
					// the same ID should be idempotent. Synchronous requests with different
					// IDs are not supported.
					if isPending {
						if pending.Id() != id {
							request.Error(ErrMismatchedIds)
						}
						request.Close()
						break
					}
				} else if outbox, hasOutbox = outboxes[request.Type()]; hasOutbox {
					// Reject duplicate pending requests.
					if _, isPending := outbox[id]; isPending {
						request.Error(ErrDuplicateRequest)
						request.Close()
						break
					}
				}
			}
			var data []byte
			if withMarshal, ok := request.(requestWithMarshal); ok {
				data, err = withMarshal.MarshalJSON()
			} else {
				data, err = json.Marshal(request)
			}
			if err == nil {
				err = ws.Message.Send(c.Socket, data)
			}
			if err != nil {
				request.Error(err)
				request.Close()
				break
			}
			if !request.CanReply() {
				request.Close()
				break
			}
			// Track replies.
			if request.Sync() {
				requests[request.Type()] = request
				break
			}
			if !hasOutbox {
				outboxes[request.Type()] = make(Requests)
				outbox = outboxes[request.Type()]
			}
			outbox[id] = request

		case reply := <-c.replies:
			var id interface{}
			if id = reply.Id(); id == nil {
				c.fatal(ErrNoId)
				break
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
				c.fatal(ErrInvalidState)
				break
			}
			request.Reply(reply)
			request.Close()
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

func (c *Conn) removeAllPending() {
	defer c.pendingLock.Unlock()
	c.pendingLock.Lock()
	c.pending = c.pending[:0:0]
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
	request := &ClientHelo{
		DeviceId:   deviceId,
		ChannelIds: channelIds,
		replies:    make(chan Reply),
		errors:     make(chan error),
	}
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
	channelId, err = GenerateId()
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
	request := &ClientRegister{
		ChannelId: channelId,
		replies:   make(chan Reply),
		errors:    make(chan error),
	}
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
	request := &ClientUnregister{
		ChannelId: channelId,
		errors:    make(chan error),
	}
	if _, err = c.WriteRequest(request); err != nil {
		return
	}
	c.removeChannel(channelId)
	return
}

// Purge hints the push server to prune all channel registrations for the
// connection. This is a no-op if purging is disabled.
func (c *Conn) Purge() (err error) {
	request := make(ClientPurge)
	_, err = c.WriteRequest(request)
	return
}

// Messages returns the client's update channel.
func (c *Conn) Messages() <-chan Update {
	return c.messages
}

// ReadMessage consumes a pending message sent by the push server. Returns
// `io.EOF` if the client is closed.
func (c *Conn) ReadMessage() (update Update, err error) {
	select {
	case update := <-c.messages:
		return update, nil
	case <-c.signalChan:
	}
	return Update{}, io.EOF
}

// AcceptBatch accepts multiple updates sent to the client. Clients should
// acknowledge updates within the specified window (the spec suggests 60 sec.
// for reference implementations) to avoid retransmission.
func (c *Conn) AcceptBatch(updates []Update) (err error) {
	request := &ClientACK{
		Updates: updates,
		errors:  make(chan error),
	}
	_, err = c.WriteRequest(request)
	return
}

// AcceptUpdate accepts an update sent to the client. Returns `io.EOF` if the
// client is closed, or a socket error if the write failed.
func (c *Conn) AcceptUpdate(update Update) (err error) {
	return c.AcceptBatch([]Update{update})
}

func (c *Conn) readMessage() (reply Reply, err error) {
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
	// Extract the `status`, `error`, and `messageType` fields. Per RFC 7159,
	// Go's JSON library represents numbers as 64-bit floats when decoding into
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
		// Missing or empty `messageType` field. Likely an error response; use the
		// `error` and `status` fields to construct the error reply, if present.
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
		// Typed error response. Construct an error reply with the `messageType`
		// and `error` fields, and `status` if present.
		return nil, &ServerError{messageType, c.Origin(), errorText, statusCode}
	}
	return nil, nil
}

func (c *Conn) decodeUpdate(field interface{}) (result *Update, err error) {
	update, ok := field.(map[string]interface{})
	if !ok {
		return nil, &IncompleteError{"notification", c.Origin(), "update"}
	}
	channelId, ok := update["channelID"].(string)
	if !ok {
		return nil, &IncompleteError{"notification", c.Origin(), "pushEndpoint"}
	}
	var version int64
	if asFloat, ok := update["version"].(float64); ok {
		version = int64(asFloat)
	} else {
		return nil, &IncompleteError{"notification", c.Origin(), "version"}
	}
	result = &Update{
		ChannelId: channelId,
		Version:   version,
	}
	return
}

func decodeHelo(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error) {
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

func decodeRegister(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error) {
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

func decodeNotification(c *Conn, fields Fields, statusCode int, errorText string) (Reply, error) {
	if statusCode != NoStatus || len(errorText) > 0 {
		return nil, &ServerError{"notification", c.Origin(), errorText, statusCode}
	}
	updates, hasUpdates := fields["updates"].([]interface{})
	if !hasUpdates {
		return nil, &IncompleteError{"notification", c.Origin(), "updates"}
	}
	defer c.pendingLock.Unlock()
	c.pendingLock.Lock()
	for _, field := range updates {
		update, err := c.decodeUpdate(field)
		if err != nil {
			return nil, err
		}
		if !c.SpoolAll && !c.Registered(update.ChannelId) {
			continue
		}
		c.pending = append(c.pending, *update)
	}
	return nil, nil
}
