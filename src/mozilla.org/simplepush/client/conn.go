package client

import (
	ws "code.google.com/p/go.net/websocket"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

var (
	ErrMismatchedIds    = &ClientError{"Attempted duplicate handshake with different device ID."}
	ErrUnknownType      = &ClientError{"Unknown request type."}
	ErrDuplicateRequest = &ClientError{"Duplicate request."}
	ErrInvalidState     = &ClientError{"Invalid client state."}
)

type Conn struct {
	sync.Mutex
	Socket     *ws.Conn
	requests   chan Request
	replies    chan Reply
	messages   chan Update
	signalChan chan bool
	closeWait  sync.WaitGroup
	lastErr    error
	isClosing  bool
}

type Fields map[string]interface{}

type Decoder func(c *Conn, statusCode int, fields Fields) (Reply, error)

var Decoders = map[string]Decoder{
	"ping":         decodePing,
	"hello":        decodeHelo,
	"register":     decodeRegister,
	"notification": decodeNotification,
}

func Dial(origin string) (*Conn, error) {
	deviceId, err := GenerateId()
	if err != nil {
		return nil, err
	}
	conn, err := DialId(origin, &deviceId, make([]string, 0))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func DialId(origin string, deviceId *string, channelIds []string) (*Conn, error) {
	socket, err := ws.Dial(origin, "", origin)
	if err != nil {
		return nil, err
	}
	conn := NewConn(socket)
	resetId, err := conn.WriteHelo(*deviceId, channelIds)
	if err != nil {
		return nil, err
	}
	*deviceId = resetId
	return conn, nil
}

func NewConn(socket *ws.Conn) *Conn {
	conn := &Conn{
		Socket:     socket,
		requests:   make(chan Request),
		replies:    make(chan Reply),
		messages:   make(chan Update),
		signalChan: make(chan bool),
	}
	conn.closeWait.Add(2)
	go conn.Send()
	go conn.Receive()
	return conn
}

// Close closes the connection to the push server, unblocking all
// `ReadMessage()`, `AcceptBatch()`, and `AcceptUpdate()` calls.
func (c *Conn) Close() (err error) {
	err, ok := c.stop()
	if !ok {
		return err
	}
	c.closeWait.Wait()
	return
}

// Acquires `c.Mutex`, closes the socket, and releases the lock, recording
// the error in the `lastErr` field.
func (c *Conn) fatal(err error) {
	defer c.Unlock()
	c.Lock()
	c.signalClose()
	c.lastErr = err
}

// Acquires `c.Mutex`, closes the socket, and releases the lock, reporting
// any errors to the caller. The Boolean specifies whether the caller should
// wait for the socket to close before returning.
func (c *Conn) stop() (error, bool) {
	defer c.Unlock()
	c.Lock()
	if c.isClosing {
		return c.lastErr, false
	}
	return c.signalClose(), true
}

// Closes the underlying socket and unblocks the read and write loops. Assumes
// the caller holds `c.Mutex`.
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
	var pending []Update
	subscriptions := make(map[string]bool)
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
		if reply.Type() == Helo || reply.Type() == Register {
			// This is a reply to a client request.
			replies = c.replies
			if reply.Type() == Register {
				// This is a subscription acknowledgement.
				subscriptions[reply.Id()] = true
			}
		} else if updates, ok := reply.(ServerUpdates); ok {
			for _, update = range updates {
				if subscriptions[update.ChannelId] {
					// This is an incoming update on a subscribed channel.
					pending = append(pending, update)
				}
			}
		}
		if len(pending) > 0 {
			update = pending[0]
			messages = c.messages
		}
		select {
		case ok = <-c.signalChan:
		case messages <- update:
			pending = pending[1:]

		case replies <- reply:
		}
	}
}

func (c *Conn) Send() {
	defer c.closeWait.Done()
	var helo Request
	registrations := make(map[string]Request)
	for ok := true; ok; {
		select {
		case ok = <-c.signalChan:
		case request := <-c.requests:
			if request.Type() == Helo && helo != nil {
				// Multiple handshake attempts with the same device ID will be ignored by
				// the server. Handshakes with different IDs are not supported.
				if request.Id() != helo.Id() {
					request.Error(ErrMismatchedIds)
				}
				request.Close()
				break
			}
			if err := ws.JSON.Send(c.Socket, request); err != nil {
				request.Error(err)
				request.Close()
				break
			}
			// Track replies for `Helo` and `Register` packets.
			if request.Type() == Helo {
				helo = request
				break
			}
			if request.Type() == Register {
				if _, ok := registrations[request.Id()]; ok {
					// Duplicate registration attempt for an in-progress registration.
					request.Error(ErrDuplicateRequest)
					break
				}
				registrations[request.Id()] = request
				break
			}
			request.Close()

		case reply := <-c.replies:
			if reply.Type() != Helo && reply.Type() != Register {
				c.fatal(ErrInvalidState)
				break
			}
			var request Request
			if reply.Type() == Helo {
				if helo == nil {
					// Unsolicited `Helo` reply.
					c.fatal(ErrInvalidState)
					break
				}
				request = helo
				helo = nil
			} else if reply.Type() == Register {
				ok := false
				if request, ok = registrations[reply.Id()]; !ok {
					// Unsolicited `Register` reply.
					c.fatal(ErrInvalidState)
					break
				}
				delete(registrations, request.Id())
			}
			request.Reply(reply)
			request.Close()
		}
	}
	// Unblock pending registrations.
	if helo != nil {
		helo.Error(io.EOF)
		helo.Close()
	}
	for _, request := range registrations {
		request.Error(io.EOF)
		request.Close()
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
func (c *Conn) WriteHelo(deviceId string, channelIds []string) (actualId string, err error) {
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
			return "", &IncompleteError{"hello", c.Socket.RemoteAddr(), "redirect"}
		}
		return "", &RedirectError{helo.Redirect, helo.StatusCode}
	}
	if helo.StatusCode >= 200 && helo.StatusCode < 300 {
		return helo.DeviceId, nil
	}
	return "", &ServerError{"hello", c.Socket.RemoteAddr(), "Unexpected status code.", helo.StatusCode}
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
			return "", &IncompleteError{"register", c.Socket.RemoteAddr(), "endpoint"}
		}
		return register.Endpoint, nil
	}
	if register.StatusCode == 409 {
		return "", &ClientError{"The channel ID `" + channelId + "` is in use."}
	}
	return "", &ServerError{"register", c.Socket.RemoteAddr(), "Unexpected status code.", register.StatusCode}
}

// Unregister signals that the client is no longer interested in receiving
// updates for a particular channel. The server never replies to this message.
func (c *Conn) Unregister(channelId string) (err error) {
	request := &ClientUnregister{
		ChannelId: channelId,
		errors:    make(chan error),
	}
	_, err = c.WriteRequest(request)
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
	if len(data) == 2 && data[0] == '{' && data[1] == '}' {
		// Avoid processing pings.
		return nil, nil
	}
	fields := make(Fields)
	if err = json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, nil
	}
	// Extract the `status`, `error`, and `messageType` fields. Per RFC 7159,
	// Go's JSON library represents numbers as 64-bit floats when decoding into
	// an untyped map.
	statusCode := -1
	if asFloat, ok := fields["status"].(float64); ok {
		statusCode = int(asFloat)
	}
	errorText, hasErrorText := fields["error"].(string)
	if len(errorText) == 0 {
		hasErrorText = false
	}
	messageType, hasMessageType := fields["messageType"].(string)
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
		return nil, &ServerError{"internal", c.Socket.RemoteAddr(), message, statusCode}
	}
	if hasErrorText {
		// Typed error response. Construct an error reply with the `messageType`
		// and `error` fields, and `status` if present.
		return nil, &ServerError{messageType, c.Socket.RemoteAddr(), errorText, statusCode}
	}
	if decoder, ok := Decoders[strings.ToLower(messageType)]; ok {
		return decoder(c, statusCode, fields)
	}
	return nil, nil
}

func decodePing(c *Conn, statusCode int, fields Fields) (Reply, error) {
	return nil, nil
}

func decodeHelo(c *Conn, statusCode int, fields Fields) (Reply, error) {
	deviceId, hasDeviceId := fields["uaid"].(string)
	if !hasDeviceId {
		return nil, &IncompleteError{"hello", c.Socket.RemoteAddr(), "uaid"}
	}
	redirect, _ := fields["redirect"].(string)
	reply := &ServerHelo{
		StatusCode: statusCode,
		DeviceId:   deviceId,
		Redirect:   redirect,
	}
	return reply, nil
}

func decodeRegister(c *Conn, statusCode int, fields Fields) (Reply, error) {
	channelId, hasChannelId := fields["channelID"].(string)
	if !hasChannelId {
		return nil, &IncompleteError{"register", c.Socket.RemoteAddr(), "channelID"}
	}
	endpoint, hasEndpoint := fields["pushEndpoint"].(string)
	if !hasEndpoint {
		return nil, &IncompleteError{"register", c.Socket.RemoteAddr(), "pushEndpoint"}
	}
	reply := &ServerRegister{
		StatusCode: statusCode,
		ChannelId:  channelId,
		Endpoint:   endpoint,
	}
	return reply, nil
}

func decodeNotification(c *Conn, statusCode int, fields Fields) (Reply, error) {
	updates, hasUpdates := fields["updates"].([]interface{})
	if !hasUpdates {
		return nil, &IncompleteError{"notification", c.Socket.RemoteAddr(), "updates"}
	}
	reply := make(ServerUpdates, len(updates))
	for index, field := range updates {
		update, hasUpdate := field.(map[string]interface{})
		if !hasUpdate {
			return nil, &IncompleteError{"notification", c.Socket.RemoteAddr(), "update"}
		}
		channelId, hasChannelId := update["channelID"].(string)
		if !hasChannelId {
			return nil, &IncompleteError{"notification", c.Socket.RemoteAddr(), "pushEndpoint"}
		}
		var version int64
		if asFloat, ok := update["version"].(float64); ok {
			version = int64(asFloat)
		} else {
			return nil, &IncompleteError{"notification", c.Socket.RemoteAddr(), "version"}
		}
		reply[index] = Update{
			ChannelId: channelId,
			Version:   version,
		}
	}
	return reply, nil
}
