// +build smoke

/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"net/url"
	"testing"

	"github.com/mozilla-services/pushgo/client"
)

func TestBroadcastStaticLocator(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping router test in short mode")
	}

	cServer := &TestServer{Name: "c", LogLevel: 0}
	cApp, cConn, err := cServer.Dial()
	if err != nil {
		t.Fatalf("Error starting test application %q: %s", cServer.Name, err)
	}
	defer cServer.Stop()
	defer cConn.Close()
	defer cConn.Purge()

	bServer := &TestServer{
		Name:     "b",
		LogLevel: 0,
		Contacts: []string{cApp.Router().URL()},
	}
	bApp, bConn, err := bServer.Dial()
	if err != nil {
		t.Fatalf("Error starting test application %q: %s", bServer.Name, err)
	}
	defer bServer.Stop()
	defer bConn.Close()
	defer bConn.Purge()

	aServer := &TestServer{
		Name:     "a",
		LogLevel: 0,
		Contacts: []string{
			cApp.Router().URL(),
			bApp.Router().URL(),
		},
	}
	aApp, aConn, err := aServer.Dial()
	if err != nil {
		t.Fatalf("Error starting test application %q: %s", aServer.Name, err)
	}
	defer aServer.Stop()
	defer aConn.Close()
	defer aConn.Purge()

	// Subscribe to a channel on c.
	cChan, cEndpoint, err := cConn.Subscribe()
	if err != nil {
		t.Fatalf("Error subscribing to channel: %s", err)
	}

	stopChan := make(chan bool)
	defer close(stopChan)
	errChan := make(chan error)
	notifyApp := func(app *Application, channelId, endpoint string, count int) {
		uri, err := url.Parse(endpoint)
		if err != nil {
			select {
			case <-stopChan:
			case errChan <- err:
			}
			return
		}
		uri.Host = app.EndpointHandler().Listener().Addr().String()
		for i := 1; i <= count; i++ {
			if err = client.Notify(uri.String(), int64(i)); err != nil {
				break
			}
		}
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}

	// Wait for updates on c.
	go func() {
		var err error
		for i := 0; i < 10; i++ {
			var updates []client.Update
			if updates, err = cConn.ReadBatch(); err != nil {
				break
			}
			if err = cConn.AcceptBatch(updates); err != nil {
				break
			}
		}
		select {
		case <-stopChan:
		case errChan <- err:
		}
	}()

	// Send an update via a.
	go notifyApp(aApp, cChan, cEndpoint, 5)

	// Send an update via b.
	go notifyApp(bApp, cChan, cEndpoint, 5)

	for i := 0; i < 3; i++ {
		if err := <-errChan; err != nil {
			t.Fatal(err)
		}
	}
}
