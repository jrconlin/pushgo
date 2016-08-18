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
		name      string
		hostname  string
		prefix    string
		metric    string
		tags      []string
		asCounter string
		asTimer   string
		asGauge   string
	}{
		{
			name:      "Empty hostname and prefix",
			metric:    "goroutines",
			asCounter: "goroutines",
			asTimer:   "goroutines",
			asGauge:   "goroutines",
		},
		{
			name:      "Hostname; empty prefix",
			hostname:  "localhost",
			metric:    "socket.connect",
			asCounter: "socket.connect",
			asTimer:   "socket.connect",
			asGauge:   "socket.connect.localhost",
		},
		{
			name:      "FQDN; empty prefix",
			hostname:  "test.mozilla.org",
			metric:    "socket.disconnect",
			asCounter: "socket.disconnect",
			asTimer:   "socket.disconnect",
			asGauge:   "socket.disconnect.test-mozilla-org",
		},
		{
			name:      "Empty hostname; prefix",
			prefix:    "pushgo.hello",
			metric:    "updates.rejected",
			asCounter: "pushgo.hello.updates.rejected",
			asTimer:   "pushgo.hello.updates.rejected",
			asGauge:   "pushgo.hello.updates.rejected",
		},
		{
			name:      "IPv4; prefix",
			hostname:  "127.0.0.1",
			prefix:    "pushgo.gcm",
			metric:    "updates.received",
			asCounter: "pushgo.gcm.updates.received",
			asTimer:   "pushgo.gcm.updates.received",
			asGauge:   "pushgo.gcm.updates.received.127-0-0-1",
		},
		{
			name:      "IPv6; tags",
			hostname:  "::1",
			metric:    "ping.success",
			tags:      []string{"stats", "counter"},
			asCounter: "stats.counter.ping.success",
			asTimer:   "stats.counter.ping.success",
			asGauge:   "stats.counter.ping.success.--1",
		},
		{
			name:      "FQDN; prefix; tags",
			hostname:  "test.mozilla.org",
			prefix:    "pushgo.simplepush",
			metric:    "updates.routed.outgoing",
			tags:      []string{"counter"},
			asCounter: "pushgo.simplepush.counter.updates.routed.outgoing",
			asTimer:   "pushgo.simplepush.counter.updates.routed.outgoing",
			asGauge:   "pushgo.simplepush.counter.updates.routed.outgoing.test-mozilla-org",
		},
	}
	for _, test := range tests {
		app := NewApplication()
		app.hostname = test.hostname
		app.SetLogger(mckLogger)
		m := new(Metrics)
		m.setApp(app)
		// Test the default affixes.
		conf := m.ConfigStruct().(*MetricsConfig)
		err := m.setCounterAffixes(test.prefix, conf.Counters.Suffix)
		if err != nil {
			t.Errorf("On test %s, error setting counter name affixes: %s",
				test.name, err)
			continue
		}
		if err = m.setTimerAffixes(test.prefix, conf.Timers.Suffix); err != nil {
			t.Errorf("On test %s, error setting timer name affixes: %s",
				test.name, err)
			continue
		}
		if err = m.setGaugeAffixes(test.prefix, conf.Gauges.Suffix); err != nil {
			t.Errorf("On test %s, error setting gauge name affixes: %s",
				test.name, err)
			continue
		}
		app.SetMetrics(m)
		asCounter := m.formatCounter(test.metric, test.tags...)
		if asCounter != test.asCounter {
			t.Errorf("On test %s, wrong counter stat name: got %q; want %q",
				test.name, asCounter, test.asCounter)
		}
		asTimer := m.formatTimer(test.metric, test.tags...)
		if asTimer != test.asTimer {
			t.Errorf("On test %s, wrong timer stat name: got %q; want %q",
				test.name, asTimer, test.asTimer)
		}
		asGauge := m.formatGauge(test.metric, test.tags...)
		if asGauge != test.asGauge {
			t.Errorf("On test %s, wrong gauge stat name: got %q; want %q",
				test.name, asGauge, test.asGauge)
		}
	}
}
