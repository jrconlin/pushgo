package client

import (
	ws "code.google.com/p/go.net/websocket"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrTimedOut = &ClientError{"Notification test timed out."}

func DoTest(origin string, channels, updates int) error {
	client := &TestClient{
		Timeout:       1 * time.Minute,
		Origin:        origin,
		Channels:      channels,
		Updates:       updates,
		MaxRegisters:  1,
		MaxRedirects:  0,
		pushEndpoints: make(map[string]string, channels),
		pushVersions:  make(map[string]int64, channels),
	}
	return client.Do()
}

type clientState func(*TestClient) (clientState, error)

type TestClient struct {
	sync.RWMutex
	Timeout         time.Duration // The deadline for receiving all notifications.
	Origin          string        // The Simple Push server URI.
	Channels        int           // The number of channels to create.
	Updates         int           // The number of notifications to send on each channel.
	MaxRegisters    int           // The number of times to retry failed registrations.
	MaxRedirects    int           // The number of redirects to follow during authentication.
	registerAttempt int
	currentRedirect int
	conn            *Conn
	pushEndpoints   map[string]string
	pushVersions    map[string]int64
	deviceId        string
}

func (t *TestClient) Do() (err error) {
	for state := helo; state != nil; {
		if state, err = state(t); err != nil {
			break
		}
	}
	if err := t.conn.Close(); err != nil {
		return err
	}
	return
}

func (t *TestClient) Notify(endpoint string, version int64) (err error) {
	values := make(url.Values)
	values.Add("version", strconv.FormatInt(version, 10))
	request, err := http.NewRequest("PUT", endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := new(http.Client)
	response, err := client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	io.Copy(ioutil.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ServerError{"internal", t.conn.Socket.RemoteAddr(), "Unexpected status code.", response.StatusCode}
	}
	return
}

func (t *TestClient) nextVersion(channelId string) int64 {
	defer t.Unlock()
	t.Lock()
	t.pushVersions[channelId]++
	return t.pushVersions[channelId]
}

func (t *TestClient) lastVersion(channelId string) int64 {
	defer t.RUnlock()
	t.RLock()
	return t.pushVersions[channelId]
}

func (t *TestClient) waitAll(signal chan bool) (err error) {
	timeout := time.After(t.Timeout)
	updates := make([]Update, 0, len(t.pushEndpoints))
	// Wait for all updates, but only acknowledge the latest version. This can
	// be changed to exit as soon as the latest version is received.
	expected, actual := len(t.pushEndpoints)*t.Updates, 0
	for ok := true; ok; {
		var update Update
		if actual >= expected {
			break
		}
		select {
		case ok = <-signal:
		case <-timeout:
			ok = false
			err = ErrTimedOut

		case update, ok = <-t.conn.Messages():
			if _, ok := t.pushEndpoints[update.ChannelId]; !ok {
				break
			}
			actual++
			if update.Version < t.lastVersion(update.ChannelId) {
				break
			}
			// Only acknowledge the latest version.
			updates = append(updates, update)
		}
	}
	if err := t.conn.AcceptBatch(updates); err != nil {
		return err
	}
	return
}

func helo(t *TestClient) (clientState, error) {
	socket, err := ws.Dial(t.Origin, "", t.Origin)
	if err != nil {
		return nil, err
	}
	t.conn = NewConn(socket)
	deviceId, err := t.conn.WriteHelo(t.deviceId, []string{})
	if err != nil {
		clientErr, ok := err.(Error)
		if !ok || clientErr.Status() < 300 || clientErr.Status() >= 400 {
			return nil, err
		}
		t.currentRedirect++
		if t.currentRedirect >= t.MaxRedirects {
			return nil, err
		}
		t.Origin = clientErr.Host()
		return helo, nil
	}
	t.deviceId = deviceId
	return register, nil
}

func register(t *TestClient) (clientState, error) {
	for index := 0; index < t.Channels; index++ {
		currentRegister := 0
		for {
			channelId, err := GenerateId()
			if err != nil {
				return nil, err
			}
			endpoint, err := t.conn.Register(channelId)
			if err == nil {
				t.pushEndpoints[channelId] = endpoint
				break
			}
			currentRegister++
			if currentRegister >= t.MaxRegisters {
				return nil, err
			}
			clientErr, ok := err.(Error)
			if !ok || clientErr.Status() != 409 {
				return nil, err
			}
			// If the channel already exists, unregister and re-register.
			if err := t.conn.Unregister(channelId); err != nil {
				return nil, err
			}
		}
	}
	return update, nil
}

func update(t *TestClient) (clientState, error) {
	var waitGroup sync.WaitGroup
	signal, errors := make(chan bool), make(chan error)
	// Listen for incoming messages.
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		select {
		case <-signal:
		case errors <- t.waitAll(signal):
		}
	}()
	// Notify each registered channel.
	notifyOne := func(endpoint string, version int64) {
		defer waitGroup.Done()
		select {
		case <-signal:
		case errors <- t.Notify(endpoint, version):
		}
	}
	waitGroup.Add(len(t.pushEndpoints) * t.Updates)
	for channelId, endpoint := range t.pushEndpoints {
		for index := 0; index < t.Updates; index++ {
			go notifyOne(endpoint, t.nextVersion(channelId))
		}
	}
	go func() {
		waitGroup.Wait()
		close(errors)
	}()
	for err := range errors {
		if err != nil {
			close(signal)
			return nil, err
		}
	}
	return nil, nil
}
