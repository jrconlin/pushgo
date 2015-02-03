/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"testing"

	"github.com/rafrombrc/gomock/gomock"
)

func TestMetricsFormat(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()

	tests := []struct {
		name     string
		hostname string
		prefix   string
		metric   string
		tags     []string
		expected string
	}{
		{
			name:     "Empty hostname and prefix",
			metric:   "goroutines",
			expected: "goroutines",
		},
		{
			name:     "Hostname; empty prefix",
			hostname: "localhost",
			metric:   "socket.connect",
			expected: "socket.connect.localhost",
		},
		{
			name:     "FQDN; empty prefix",
			hostname: "test.mozilla.org",
			metric:   "socket.disconnect",
			expected: "socket.disconnect.test-mozilla-org",
		},
		{
			name:     "Empty hostname; prefix",
			prefix:   "pushgo.hello",
			metric:   "updates.rejected",
			expected: "pushgo.hello.updates.rejected"},
		{
			name:     "IPv4; prefix",
			hostname: "127.0.0.1",
			prefix:   "pushgo.gcm",
			metric:   "updates.received",
			expected: "pushgo.gcm.updates.received.127-0-0-1"},
		{
			name:     "IPv6; tags",
			hostname: "::1",
			metric:   "ping.success",
			tags:     []string{"stats", "counter"},
			expected: "stats.counter.ping.success.--1"},
		{
			name:     "FQDN; prefix; tags",
			hostname: "test.mozilla.org",
			prefix:   "pushgo.simplepush",
			metric:   "updates.routed.outgoing",
			tags:     []string{"counter"},
			expected: "pushgo.simplepush.counter.updates.routed.outgoing.test-mozilla-org"},
	}
	for _, test := range tests {
		app := NewApplication()
		app.hostname = test.hostname
		app.SetLogger(mckLogger)
		m := new(Metrics)
		if err := m.Init(app, &MetricsConfig{StatsdServer: ""}); err != nil {
			t.Errorf("On test %s, error initializing metrics: %s", err)
			continue
		}
		m.Prefix(test.prefix)
		actual := m.formatMetric(test.metric, test.tags...)
		if actual != test.expected {
			t.Errorf("On test %s, wrong stat name: got %q; want %q",
				test.name, actual, test.expected)
		}
	}
}
