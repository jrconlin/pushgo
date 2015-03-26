// +build smoke

/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"testing"

	"github.com/mozilla-services/pushgo/client"
)

func TestRedirect(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping balancer test in short mode")
	}
	bServer := &TestServer{
		Name:      "b",
		LogLevel:  0,
		Threshold: 1,
	}
	defer bServer.Stop()
	bHost, err := bServer.Origin()
	if err != nil {
		t.Fatalf("Error starting test server %q: %s", bServer.Name, err)
	}

	aServer := &TestServer{
		Name:      "a",
		LogLevel:  0,
		Threshold: 0,
		Redirects: []string{bHost},
	}
	defer aServer.Stop()
	_, aConn, err := aServer.Dial()
	if err != nil {
		clientErr, ok := err.(client.Error)
		if !ok {
			t.Errorf("Type assertion failed for handshake error: %#v", err)
		} else if status := clientErr.Status(); status != 307 {
			t.Errorf("Wrong redirect status: got %d; want 307", status)
		} else if host := clientErr.Host(); host != bHost {
			t.Errorf("Wrong target host: got %q; want %q", host, bHost)
		}
	} else {
		t.Errorf("Expected error starting test application %q", aServer.Name)
		aConn.Purge()
		aConn.Close()
	}
}
