/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cactus/go-statsd-client/statsd"
)

type trec struct {
	Count uint64
	Avg   float64
}

type JsMap map[string]interface{}

type timer map[string]trec

type MetricsConfig struct {
	Prefix       string
	StatsdServer string `toml:"statsd_server"`
	StatsdName   string `toml:"statsd_name"`
}

type Metrics struct {
	sync.RWMutex
	counter   map[string]int64 // counters
	timer     timer            // timers
	gauge     map[string]int64
	gaugeLock sync.Mutex
	prefix    string // prefix for
	logger    *SimpleLogger
	statsd    *statsd.Client
	born      time.Time
}

func (m *Metrics) ConfigStruct() interface{} {
	return &MetricsConfig{
		Prefix:     "simplepush",
		StatsdName: "undef",
	}
}

func (m *Metrics) Init(app *Application, config interface{}) (err error) {
	conf := config.(*MetricsConfig)

	m.logger = app.Logger()

	if conf.StatsdServer != "" {
		name := strings.ToLower(conf.StatsdName)
		client, err := statsd.New(conf.StatsdServer, name)
		if err != nil {
			m.logger.Error("metrics", "Could not init statsd connection",
				LogFields{"error": err.Error()})
		} else {
			m.statsd = client
		}
	}

	m.counter = make(map[string]int64)
	m.timer = make(timer)
	m.gauge = make(map[string]int64)
	m.prefix = conf.Prefix
	m.born = time.Now()
	return
}

func (m *Metrics) Prefix(newPrefix string) {
	m.prefix = strings.TrimRight(newPrefix, ".")
	if m.statsd != nil {
		m.statsd.SetPrefix(newPrefix)
	}
}

func (m *Metrics) Snapshot() map[string]interface{} {
	var pfx string
	if len(m.prefix) > 0 {
		pfx = m.prefix + "."
	}
	oldMetrics := make(map[string]interface{})
	// copy the old metrics
	m.RLock()
	for k, v := range m.counter {
		oldMetrics[pfx+"counter."+k] = v
	}
	for k, v := range m.timer {
		oldMetrics[pfx+"avg."+k] = v.Avg
	}
	for k, v := range m.gauge {
		oldMetrics[pfx+"gauge."+k] = v
	}
	m.RUnlock()
	oldMetrics[pfx+"server.age"] = time.Now().Unix() - m.born.Unix()
	return oldMetrics
}

func (m *Metrics) IncrementBy(metric string, count int64) {
	m.Lock()
	met := m.counter[metric] + count
	m.counter[metric] = met
	m.Unlock()

	if m.logger.ShouldLog(INFO) {
		m.logger.Info("metrics", "counter."+metric,
			LogFields{"value": strconv.FormatInt(met, 10),
				"type": "counter"})
	}

	if statsd := m.statsd; statsd != nil {
		if count >= 0 {
			statsd.Inc(metric, count, 1.0)
		} else {
			statsd.Dec(metric, count, 1.0)
		}
	}
}

func (m *Metrics) Increment(metric string) {
	m.IncrementBy(metric, 1)
}

func (m *Metrics) Decrement(metric string) {
	m.IncrementBy(metric, -1)
}

func (m *Metrics) Timer(metric string, duration time.Duration) {
	// etcd supports millisecond granularity.
	value := int64(duration / time.Millisecond)

	m.Lock()
	if t, ok := m.timer[metric]; !ok {
		m.timer[metric] = trec{
			Count: 1,
			Avg:   float64(value),
		}
	} else {
		// calculate running average
		t.Count = t.Count + 1
		t.Avg = t.Avg + (float64(value)-t.Avg)/float64(t.Count)
		m.timer[metric] = t
	}
	m.Unlock()

	if m.logger.ShouldLog(INFO) {
		m.logger.Info("metrics", "timer."+metric,
			LogFields{"value": strconv.FormatInt(value, 10),
				"type": "timer"})
	}
	if m.statsd != nil {
		m.statsd.Timing(metric, value, 1.0)
	}
}

func (m *Metrics) Gauge(metric string, value int64) {
	m.Lock()
	m.gauge[metric] = value
	m.Unlock()

	if m.logger.ShouldLog(INFO) {
		m.logger.Info("metrics", "gauge."+metric,
			LogFields{"value": strconv.FormatInt(value, 10),
				"type": "gauge"})
	}

	if statsd := m.statsd; statsd != nil {
		if value >= 0 {
			statsd.Gauge(metric, value, 1.0)
			return
		}
		// Gauges cannot be set to negative values; sign prefixes indicate deltas.
		defer m.gaugeLock.Unlock()
		m.gaugeLock.Lock()
		if err := statsd.Gauge(metric, 0, 1.0); err != nil {
			return
		}
		statsd.GaugeDelta(metric, value, 1.0)
	}
}

func (m *Metrics) GaugeDelta(metric string, delta int64) {
	m.Lock()
	gauge := m.gauge[metric]
	m.gauge[metric] = gauge + delta
	m.Unlock()

	if m.logger.ShouldLog(INFO) {
		m.logger.Info("metrics", "gauge."+metric,
			LogFields{"value": strconv.FormatInt(gauge, 10),
				"type": "gauge"})
	}

	if m.statsd != nil {
		m.statsd.GaugeDelta(metric, delta, 1.0)
	}
}
