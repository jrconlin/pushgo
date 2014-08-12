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

var metrex sync.Mutex

type trec struct {
	Count uint64
	Avg   float64
}

type JsMap map[string]interface{}

type timer map[string]trec

type Metrics struct {
	dict   map[string]int64 // counters
	timer  timer            // timers
	prefix string           // prefix for
	logger *SimpleLogger
	statsd *statsd.Client
	born   time.Time
}

func NewMetrics(prefix string, logger *SimpleLogger, config *MzConfig) (self *Metrics) {

	var statsdc *statsd.Client
	if server := config.Get("statsd.server", ""); server != "" {
		name := strings.ToLower(config.Get("statsd.name", "undef"))
		client, err := statsd.New(server, name)
		if err != nil {
			logger.Error("metrics", "Could not init statsd connection",
				Fields{"error": err.Error()})
		} else {
			statsdc = client
		}
	}

	self = &Metrics{
		dict:   make(map[string]int64),
		timer:  make(timer),
		prefix: prefix,
		logger: logger,
		statsd: statsdc,
		born:   time.Now(),
	}
	return self
}

func (self *Metrics) Prefix(newPrefix string) {
	self.prefix = strings.TrimRight(newPrefix, ".")
	if self.statsd != nil {
		self.statsd.SetPrefix(newPrefix)
	}
}

func (self *Metrics) Snapshot() map[string]interface{} {
	defer metrex.Unlock()
	metrex.Lock()
	var pfx string
	if len(self.prefix) > 0 {
		pfx = self.prefix + "."
	}
	oldMetrics := make(map[string]interface{})
	// copy the old metrics
	for k, v := range self.dict {
		oldMetrics[pfx+"counter."+k] = v
	}
	for k, v := range self.timer {
		oldMetrics[pfx+"avg."+k] = v.Avg
	}
	oldMetrics[pfx+"server.age"] = time.Now().Unix() - self.born.Unix()
	return oldMetrics
}

func (self *Metrics) IncrementBy(metric string, count int) {
	defer metrex.Unlock()
	metrex.Lock()
	m, ok := self.dict[metric]
	if !ok {
		self.dict[metric] = int64(0)
		m = self.dict[metric]
	}
	atomic.AddInt64(&m, int64(count))
	self.dict[metric] = m
	if self.logger != nil {
		self.logger.Info("metrics", "counter."+metric,
			Fields{"value": strconv.FormatInt(m, 10),
				"type": "counter"})
	}
	if self.statsd != nil {
		if count >= 0 {
			self.statsd.Inc(metric, int64(count), 1.0)
		} else {
			self.statsd.Dec(metric, int64(count), 1.0)
		}
	}
}

func (self *Metrics) Increment(metric string) {
	self.IncrementBy(metric, 1)
}

func (self *Metrics) Decrement(metric string) {
	self.IncrementBy(metric, -1)
}

func (self *Metrics) Timer(metric string, value int64) {
	defer metrex.Unlock()
	metrex.Lock()
	if t, ok := self.timer[metric]; !ok {
		self.timer[metric] = trec{
			Count: uint64(1),
			Avg:   float64(value),
		}
	} else {
		// calculate running average
		t.Count = t.Count + 1
		t.Avg = t.Avg + (float64(value)-t.Avg)/float64(t.Count)
		self.timer[metric] = t
	}

	if self.logger != nil {
		self.logger.Info("metrics", "timer."+metric,
			Fields{"value": strconv.FormatInt(value, 10),
				"type": "timer"})
	}
	if self.statsd != nil {
		self.statsd.Timing(metric, value, 1.0)
	}
}
