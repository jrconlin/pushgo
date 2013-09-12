/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"sync"
	"sync/atomic"
)

var (
	Metrics map[string]int64
	metrex  sync.Mutex
)

func init() {
	Metrics = make(map[string]int64)
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
