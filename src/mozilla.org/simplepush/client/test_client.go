package client

import (
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrTimedOut   = &ClientError{"Notification test timed out."}
	ErrChanClosed = &ClientError{"The notification channel was closed by the connection."}
)

type Endpoint struct {
	URI string
	Version int64
}

type Endpoints map[string]Endpoint

func DoTest(origin string, channels, updates int) (err error) {
	currentRedirect, maxRedirects := 0, 10
	var conn *Conn
	for {
		conn, err = DialOrigin(origin)
		if err != nil {
			return
		}
		_, err := conn.WriteHelo("", []string{})
		if err == nil {
			break
		}
		clientErr, ok := err.(Error)
		if !ok || clientErr.Status() < 300 || clientErr.Status() >= 400 {
			return err
		}
		currentRedirect++
		if currentRedirect >= maxRedirects {
			return err
		}
		origin = clientErr.Host()
	}
	return PushThrough(conn, channels, updates)
}

func PushThrough(conn *Conn, channels, updates int) error {
	t := &TestClient{
		Timeout: 1 * time.Minute,
		Channels: channels,
		Updates: updates,
		MaxRegisters: 1,
		conn: conn,
		endpoints: make(Endpoints, channels),
	}
	return t.Do()
}

func Notify(endpoint string, version int64) (err error) {
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
		return &ServerError{"internal", endpoint, "Unexpected status code.", response.StatusCode}
	}
	return
}

type TestClient struct {
	sync.RWMutex
	Timeout         time.Duration // The deadline for receiving all notifications.
	Channels        int           // The number of channels to create.
	Updates         int           // The number of notifications to send on each channel.
	MaxRegisters    int           // The number of times to retry failed registrations.
	accept bool
	pendingTimeout time.Duration
	conn *Conn
	endpoints Endpoints
}

func (t *TestClient) Do() error {
	if err := t.subscribe(); err != nil {
		return err
	}
	if err := t.notifyAll(); err != nil {
		return err
	}
	return nil
}

func (t *TestClient) subscribe() error {
	for index := 0; index < t.Channels; index++ {
		currentRegister := 0
		for {
			channelId, uri, err := t.conn.Subscribe()
			if err == nil {
				t.endpoints[channelId] = Endpoint{uri, 1}
				break
			}
			currentRegister++
			if currentRegister >= t.MaxRegisters {
				return err
			}
			clientErr, ok := err.(Error)
			if !ok || clientErr.Status() != 409 {
				return err
			}
			// If the channel already exists, deregister and re-register.
			if err := t.conn.Unregister(channelId); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *TestClient) notifyAll() error {
	var notifyWait sync.WaitGroup
	signal, errors := make(chan bool), make(chan error)
	// Listen for incoming messages.
	notifyWait.Add(1)
	go func() {
		defer notifyWait.Done()
		select {
		case <-signal:
		case errors <- t.waitAll(signal):
		}
	}()
	// Notify each registered channel.
	notifyOne := func(endpoint string, version int64) {
		defer notifyWait.Done()
		select {
		case <-signal:
		case errors <- Notify(endpoint, version):
		}
	}
	t.Lock()
	notifyWait.Add(len(t.endpoints) * t.Updates)
	for channelId, endpoint := range t.endpoints {
		for index := 0; index < t.Updates; index++ {
			nextVersion := endpoint.Version + int64(index)
			t.endpoints[channelId] = Endpoint{endpoint.URI, nextVersion}
			go notifyOne(endpoint.URI, nextVersion)
		}
	}
	t.Unlock()
	go func() {
		notifyWait.Wait()
		close(errors)
	}()
	for err := range errors {
		if err != nil {
			close(signal)
			return err
		}
	}
	return nil
}

func (t *TestClient) waitAll(signal chan bool) (err error) {
	defer t.RUnlock()
	t.RLock()
	timer := time.After(t.Timeout)
	updates := make([]Update, 0, len(t.endpoints))
	// Wait for all updates, but only acknowledge the latest version. This can
	// be changed to exit as soon as the latest version is received.
	expected, actual := len(t.endpoints)*t.Updates, 0
	for ok := true; ok; {
		var update Update
		if actual >= expected {
			break
		}
		select {
		case ok = <-signal:
		case <-timer:
			ok = false
			err = ErrTimedOut

		case update, ok = <-t.conn.Messages():
			if !ok {
				break
			}
			if endpoint, ok := t.endpoints[update.ChannelId]; ok {
				actual++
				// Only acknowledge the latest version.
				if update.Version >= endpoint.Version {
					updates = append(updates, update)
				}
			}
		}
	}
	if err := t.conn.AcceptBatch(updates); err != nil {
		return err
	}
	if len(updates) != len(t.endpoints) {
		return ErrChanClosed
	}
	return
}
