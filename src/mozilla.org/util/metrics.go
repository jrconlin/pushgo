/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package util

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	//	"github.com/cactus/go-statsd-client/statsd"
)

var metrex sync.Mutex

type Metrics struct {
	dict   map[string]int64
    timer  map[string]float64
	prefix string
	logger *HekaLogger
	//  statsdc *statsd.Client
}

func NewMetrics(prefix string, logger *HekaLogger) (self *Metrics) {
	self = &Metrics{
		dict:   make(map[string]int64),
        timer:  make(map[string]float64),
		prefix: prefix,
		logger: logger,
	}
	return self
}

func (self *Metrics) Prefix(newPrefix string) {
	self.prefix = strings.TrimRight(newPrefix, ".")
}

/*
func (self *Metrics) StatsdTarget(target string) (err error) {
	self.statsdc, err = statsd.New(target, self.prefix)
	if err != nil {
		return
	}
	return
}
*/
func (self *Metrics) Snapshot() map[string]interface{} {
	defer metrex.Unlock()
	metrex.Lock()
	oldMetrics := make(map[string]interface{})
	// copy the old metrics
	for k, v := range self.dict {
		oldMetrics["counter." + k] = v
	}
    for k, v := range self.timer {
        oldMetrics["avg." + k] = v
    }
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
    if m, ok := self.timer[metric]; !ok {
        self.timer[metric] = float64(value)
    } else {
        // calculate running average
        fv := float64(value)
        dm := (fv - m)/2
        switch {
        case fv < m:
            self.timer[metric] = m - dm
        case fv > m:
            self.timer[metric] = m + dm
        }
    }

	if self.logger != nil {
		self.logger.Info("metrics", "timer."+metric,
			Fields{"value": strconv.FormatInt(value, 10),
				"type": "timer"})
	}
}
