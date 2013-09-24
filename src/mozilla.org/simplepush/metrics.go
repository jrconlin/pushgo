/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cactus/go-statsd-client/statsd"
)

var (
	Metrics map[string]int64
	metrex  sync.Mutex
	prefix  string
	statsdc *statsd.Client
)

func init() {
	Metrics = make(map[string]int64)
	prefix  = "simplepush"
}

func MetricsPrefix(newPrefix string) {
	prefix = strings.TrimRight(newPrefix, ".")
}

func MetricsStatsdTarget(target string) (err error) {
	statsdc, err = statsd.New(target, prefix)
	if err != nil {
		return
	}
	return
}

func MetricsSnapshot() map[string]int64 {
	defer metrex.Unlock()
	metrex.Lock()
	var oldMetrics map[string]int64
	// copy the old metrics
	for k, v := range Metrics {
		oldMetrics[k] = v
	}
	return oldMetrics
}

func get(metric string) (m int64) {
	if m, exists := Metrics[metric]; !exists {
		defer metrex.Unlock()
		metrex.Lock()
		m = int64(0)
		Metrics[metric] = m
	}
	return
}

func MetricIncrementBy(metric string, count int) {
	m := get(metric)
	atomic.AddInt64(&m, int64(count))
	if statsdc != nil {
		statsdc.Inc(metric, int64(count), 1.0)
	}
}

func MetricIncrement(metric string) {
	MetricIncrementBy(metric, 1)
}

func MetricDecrement(metric string) {
	MetricIncrementBy(metric, -1)
}

/*
type MetricsLogger struct {
	logger *util.HekaLogger
}

func NewMetricsLogger(logger *util.HekaLogger) (mlogger *MetricsLogger) {
	mlogger = &MetricsLogger{logger: logger}
	return
}

func (self *MetricsLogger) incr(value int64) int64 {
	atomic.AddInt64(&value, int64(1))
	return value
}
*/
