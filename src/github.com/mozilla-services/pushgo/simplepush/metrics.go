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

type timer map[string]trec

type MetricsConfig struct {
	StoreSnapshots bool   `toml:"store_snapshots" env:"store_snapshots"`
	Prefix         string `env:"prefix"`
	StatsdServer   string `toml:"statsd_server" env:"statsd_server"`
	StatsdName     string `toml:"statsd_name" env:"statsd_name"`
}

type Statistician interface {
	Init(*Application, interface{}) error
	Prefix(string)
	Snapshot() map[string]interface{}
	IncrementBy(string, int64)
	Increment(string)
	Decrement(string)
	Timer(string, time.Duration)
	Gauge(string, int64)
	GaugeDelta(string, int64)
}

type Metrics struct {
	sync.RWMutex
	counter        map[string]int64 // counters
	timer          timer            // timers
	gauge          map[string]int64
	prefix         string // prefix for
	logger         *SimpleLogger
	statsd         *statsd.Client
	born           time.Time
	storeSnapshots bool
}

func (m *Metrics) ConfigStruct() interface{} {
	return &MetricsConfig{
		StoreSnapshots: true,
		Prefix:         "simplepush",
		StatsdName:     "undef",
	}
}

func (m *Metrics) Init(app *Application, config interface{}) (err error) {
	conf := config.(*MetricsConfig)

	m.logger = app.Logger()

	if conf.StatsdServer != "" {
		name := strings.ToLower(conf.StatsdName)
		if m.statsd, err = statsd.New(conf.StatsdServer, name); err != nil {
			m.logger.Panic("metrics", "Could not init statsd connection",
				LogFields{"error": err.Error()})
			return err
		}
	}

	m.prefix = conf.Prefix
	m.born = time.Now()

	if m.storeSnapshots = conf.StoreSnapshots; m.storeSnapshots {
		m.counter = make(map[string]int64)
		m.timer = make(timer)
		m.gauge = make(map[string]int64)
	}

	return nil
}

func (m *Metrics) Prefix(newPrefix string) {
	m.prefix = strings.TrimRight(newPrefix, ".")
	if m.statsd != nil {
		m.statsd.SetPrefix(newPrefix)
	}
}

func (m *Metrics) Snapshot() map[string]interface{} {
	if !m.storeSnapshots {
		return nil
	}
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
	if m.storeSnapshots {
		m.Lock()
		met := m.counter[metric] + count
		m.counter[metric] = met
		m.Unlock()
	}

	if m.logger.ShouldLog(DEBUG) {
		m.logger.Debug("metrics", "counter."+metric,
			LogFields{"delta": strconv.FormatInt(count, 10),
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
	// statsd supports millisecond granularity.
	value := int64(duration / time.Millisecond)

	if m.storeSnapshots {
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
	}

	if m.logger.ShouldLog(DEBUG) {
		m.logger.Debug("metrics", "timer."+metric,
			LogFields{"value": strconv.FormatInt(value, 10),
				"type": "timer"})
	}
	if m.statsd != nil {
		m.statsd.Timing(metric, value, 1.0)
	}
}

func (m *Metrics) Gauge(metric string, value int64) {
	if m.storeSnapshots {
		m.Lock()
		m.gauge[metric] = value
		m.Unlock()
	}

	if statsd := m.statsd; statsd != nil {
		if value >= 0 {
			statsd.Gauge(metric, value, 1.0)
			return
		}
		// Gauges cannot be set to negative values; sign prefixes indicate deltas.
		if err := statsd.Gauge(metric, 0, 1.0); err != nil {
			return
		}
		statsd.GaugeDelta(metric, value, 1.0)
	}
}

func (m *Metrics) GaugeDelta(metric string, delta int64) {
	if m.storeSnapshots {
		m.Lock()
		gauge := m.gauge[metric]
		m.gauge[metric] = gauge + delta
		m.Unlock()
	}

	if m.statsd != nil {
		m.statsd.GaugeDelta(metric, delta, 1.0)
	}
}

// == provide just enough metrics for testing.
type TestMetrics struct {
	sync.RWMutex
	Counters map[string]int64
	Gauges   map[string]int64
}

func (r *TestMetrics) Init(app *Application, config interface{}) (err error) {
	r.Counters = make(map[string]int64)
	r.Gauges = make(map[string]int64)
	return
}

func (r *TestMetrics) Prefix(string) {}
func (r *TestMetrics) Snapshot() map[string]interface{} {
	return make(map[string]interface{})
}
func (r *TestMetrics) IncrementBy(metric string, count int64) {
	r.Lock()
	defer r.Unlock()
	r.Counters[metric] = r.Counters[metric] + count
}
func (r *TestMetrics) Increment(metric string) {
	r.IncrementBy(metric, 1)
}
func (r *TestMetrics) Decrement(metric string) {
	r.IncrementBy(metric, -1)
}
func (r *TestMetrics) Timer(metric string, duration time.Duration) {}
func (r *TestMetrics) Gauge(metric string, val int64) {
	r.Lock()
	defer r.Unlock()
	r.Gauges[metric] = val
}
func (r *TestMetrics) GaugeDelta(metric string, delta int64) {
	r.Lock()
	defer r.Unlock()
	if m, ok := r.Gauges[metric]; ok {
		r.Gauges[metric] = m + delta
	} else {
		r.Gauges[metric] = delta
	}
}
