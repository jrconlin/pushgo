/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	prefix       string
	statsdServer string `toml:"statsd_server"`
	statsdName   string `toml:"statsd_name"`
}

type Metrics struct {
	dict   map[string]int64 // counters
	timer  timer            // timers
	prefix string           // prefix for
	logger *SimpleLogger
	statsd *statsd.Client
	born   time.Time
	metrex *sync.Mutex
}

func (m *Metrics) ConfigStruct() interface{} {
	return &MetricsConfig{
		prefix:     "simplepush",
		statsdName: "undef",
	}
}

func (m *Metrics) Init(app *Application, config interface{}) (err error) {
	conf := config.(*MetricsConfig)

	m.logger = app.Logger()

	if conf.statsdServer != "" {
		name := strings.ToLower(conf.statsdName)
		client, err := statsd.New(conf.statsdServer, name)
		if err != nil {
			m.logger.Error("metrics", "Could not init statsd connection",
				LogFields{"error": err.Error()})
		} else {
			m.statsd = client
		}
	}

	m.dict = make(map[string]int64)
	m.timer = make(timer)
	m.prefix = conf.prefix
	m.born = time.Now()
	m.metrex = new(sync.Mutex)
	return
}

func (m *Metrics) Prefix(newPrefix string) {
	m.prefix = strings.TrimRight(newPrefix, ".")
	if m.statsd != nil {
		m.statsd.SetPrefix(newPrefix)
	}
}

func (m *Metrics) Snapshot() map[string]interface{} {
	defer m.metrex.Unlock()
	m.metrex.Lock()
	var pfx string
	if len(m.prefix) > 0 {
		pfx = m.prefix + "."
	}
	oldMetrics := make(map[string]interface{})
	// copy the old metrics
	for k, v := range m.dict {
		oldMetrics[pfx+"counter."+k] = v
	}
	for k, v := range m.timer {
		oldMetrics[pfx+"avg."+k] = v.Avg
	}
	oldMetrics[pfx+"server.age"] = time.Now().Unix() - m.born.Unix()
	return oldMetrics
}

func (m *Metrics) IncrementBy(metric string, count int) {
	defer m.metrex.Unlock()
	m.metrex.Lock()
	m, ok := m.dict[metric]
	if !ok {
		m.dict[metric] = int64(0)
		m = m.dict[metric]
	}
	atomic.AddInt64(&m, int64(count))
	m.dict[metric] = m
	m.logger.Info("metrics", "counter."+metric,
		Fields{"value": strconv.FormatInt(m, 10),
			"type": "counter"})
	if m.statsd != nil {
		if count >= 0 {
			m.statsd.Inc(metric, int64(count), 1.0)
		} else {
			m.statsd.Dec(metric, int64(count), 1.0)
		}
	}
}

func (m *Metrics) Increment(metric string) {
	m.IncrementBy(metric, 1)
}

func (m *Metrics) Decrement(metric string) {
	m.IncrementBy(metric, -1)
}

func (m *Metrics) Timer(metric string, value int64) {
	defer m.metrex.Unlock()
	m.metrex.Lock()
	if t, ok := m.timer[metric]; !ok {
		m.timer[metric] = trec{
			Count: uint64(1),
			Avg:   float64(value),
		}
	} else {
		// calculate running average
		t.Count = t.Count + 1
		t.Avg = t.Avg + (float64(value)-t.Avg)/float64(t.Count)
		m.timer[metric] = t
	}

	m.logger.Info("metrics", "timer."+metric,
		Fields{"value": strconv.FormatInt(value, 10),
			"type": "timer"})
	if m.statsd != nil {
		m.statsd.Timing(metric, value, 1.0)
	}
}
