/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/cactus/go-statsd-client/statsd"
)

// cleanMetricPart is a mapping function passed to strings.Map that replaces
// reserved characters in a metric key with hyphens.
func cleanMetricPart(r rune) rune {
	if r >= 'A' && r <= 'F' {
		r += 'a' - 'A'
	}
	if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
		return r
	}
	return '-'
}

type trec struct {
	Count uint64
	Avg   float64
}

type timer map[string]trec

type MetricConfig struct {
	Prefix string
	Suffix string
}

type MetricsConfig struct {
	StoreSnapshots bool   `toml:"store_snapshots" env:"store_snapshots"`
	StatsdServer   string `toml:"statsd_server" env:"statsd_server"`
	StatsdName     string `toml:"statsd_name" env:"statsd_name"`
	Counters       MetricConfig
	Timers         MetricConfig
	Gauges         MetricConfig
}

type Statistician interface {
	Init(*Application, interface{}) error
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

	counter       map[string]int64 // Counter snapshots.
	counterPrefix string           // Optional prefix for counter names.
	counterSuffix string           // Optional suffix for counter names.

	timer       timer // Timer snapshots.
	timerPrefix string
	timerSuffix string

	gauge       map[string]int64 // Gauge snapshots.
	gaugePrefix string
	gaugeSuffix string

	app            *Application
	logger         *SimpleLogger
	statsd         *statsd.Client
	born           time.Time
	storeSnapshots bool
}

func (m *Metrics) ConfigStruct() interface{} {
	return &MetricsConfig{
		StoreSnapshots: true,
		StatsdName:     "undef",
		Counters:       MetricConfig{Prefix: "simplepush"},
		Timers:         MetricConfig{Prefix: "simplepush"},
		Gauges:         MetricConfig{Prefix: "simplepush", Suffix: "{{.Host}}"},
	}
}

func (m *Metrics) Init(app *Application, config interface{}) (err error) {
	conf := config.(*MetricsConfig)
	m.setApp(app)

	if conf.StatsdServer != "" {
		name := strings.ToLower(conf.StatsdName)
		if m.statsd, err = statsd.New(conf.StatsdServer, name); err != nil {
			m.logger.Panic("metrics", "Could not init statsd connection",
				LogFields{"error": err.Error()})
			return err
		}
	}

	err = m.setCounterAffixes(conf.Counters.Prefix, conf.Counters.Suffix)
	if err != nil {
		m.logger.Panic("metrics", "Error setting counter name affixes",
			LogFields{"error": err.Error()})
		return err
	}
	err = m.setTimerAffixes(conf.Timers.Prefix, conf.Timers.Suffix)
	if err != nil {
		m.logger.Panic("metrics", "Error setting timer name affixes",
			LogFields{"error": err.Error()})
		return err
	}
	err = m.setGaugeAffixes(conf.Gauges.Prefix, conf.Gauges.Suffix)
	if err != nil {
		m.logger.Panic("metrics", "Error parsing gauge name affixes",
			LogFields{"error": err.Error()})
		return err
	}
	m.born = time.Now()

	if m.storeSnapshots = conf.StoreSnapshots; m.storeSnapshots {
		m.counter = make(map[string]int64)
		m.timer = make(timer)
		m.gauge = make(map[string]int64)
	}

	return nil
}

func (m *Metrics) setApp(app *Application) {
	m.app = app
	m.logger = app.Logger()
}

func (m *Metrics) setCounterAffixes(rawPrefix, rawSuffix string) (err error) {
	m.counterPrefix, err = m.formatAffix("counterPrefix", rawPrefix)
	if err != nil {
		return err
	}
	m.counterSuffix, err = m.formatAffix("counterSuffix", rawSuffix)
	if err != nil {
		return err
	}
	return nil
}

func (m *Metrics) setTimerAffixes(rawPrefix, rawSuffix string) (err error) {
	if m.timerPrefix, err = m.formatAffix("timerPrefix", rawPrefix); err != nil {
		return err
	}
	if m.timerSuffix, err = m.formatAffix("timerSuffix", rawSuffix); err != nil {
		return err
	}
	return nil
}

func (m *Metrics) setGaugeAffixes(rawPrefix, rawSuffix string) (err error) {
	if m.gaugePrefix, err = m.formatAffix("gaugePrefix", rawPrefix); err != nil {
		return err
	}
	if m.gaugeSuffix, err = m.formatAffix("gaugeSuffix", rawSuffix); err != nil {
		return err
	}
	return nil
}

// formatAffix parses a raw affix as a template, interpolating the hostname
// and server version into the resulting string.
func (m *Metrics) formatAffix(name, raw string) (string, error) {
	tmpl, err := template.New(name).Parse(raw)
	if err != nil {
		return "", err
	}
	affix := new(bytes.Buffer)
	host := strings.Map(cleanMetricPart, m.app.Hostname())
	params := struct {
		Host    string
		Version string
	}{host, VERSION}
	if err := tmpl.Execute(affix, params); err != nil {
		return "", err
	}
	cleanAffix := bytes.TrimRight(affix.Bytes(), ".")
	return string(cleanAffix), nil
}

func (m *Metrics) formatCounter(metric string, tags ...string) string {
	return m.formatMetric(metric, m.counterPrefix, m.counterSuffix, tags...)
}

func (m *Metrics) formatTimer(metric string, tags ...string) string {
	return m.formatMetric(metric, m.timerPrefix, m.timerSuffix, tags...)
}

func (m *Metrics) formatGauge(metric string, tags ...string) string {
	return m.formatMetric(metric, m.gaugePrefix, m.gaugeSuffix, tags...)
}

// formatMetric constructs a statsd key from the given metric name, prefix,
// and suffix. Additional tags are prepended to the suffix.
func (m *Metrics) formatMetric(metric string, prefix, suffix string,
	tags ...string) string {

	var parts []string
	if len(prefix) > 0 {
		parts = append(parts, prefix)
	}
	if len(tags) > 0 {
		parts = append(parts, tags...)
	}
	parts = append(parts, metric)
	if len(suffix) > 0 {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, ".")
}

func (m *Metrics) Snapshot() map[string]interface{} {
	if !m.storeSnapshots {
		return nil
	}
	oldMetrics := make(map[string]interface{})
	// copy the old metrics
	m.RLock()
	for k, v := range m.counter {
		oldMetrics[m.formatCounter(k, "counter")] = v
	}
	for k, v := range m.timer {
		oldMetrics[m.formatTimer(k, "avg")] = v.Avg
	}
	for k, v := range m.gauge {
		oldMetrics[m.formatGauge(k, "gauge")] = v
	}
	m.RUnlock()
	age := time.Now().Unix() - m.born.Unix()
	oldMetrics[m.formatMetric("server.age", "", "")] = age
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
			statsd.Inc(m.formatCounter(metric), count, 1.0)
		} else {
			statsd.Dec(m.formatCounter(metric), count, 1.0)
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
		m.statsd.Timing(m.formatTimer(metric), value, 1.0)
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
			statsd.Gauge(m.formatGauge(metric), value, 1.0)
			return
		}
		// Gauges cannot be set to negative values; sign prefixes indicate deltas.
		if err := statsd.Gauge(m.formatGauge(metric), 0, 1.0); err != nil {
			return
		}
		statsd.GaugeDelta(m.formatGauge(metric), value, 1.0)
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
		m.statsd.GaugeDelta(m.formatGauge(metric), delta, 1.0)
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
