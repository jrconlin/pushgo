/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"sync"
)

type StaticBalancerConf struct {
	Threshold float64
	Redirects []string
}

type StaticBalancer struct {
	sync.Mutex
	redirects    []string
	threshold    float64
	workerCount  func() int
	maxWorkers   int
	currentIndex int
}

func (*StaticBalancer) ConfigStruct() interface{} {
	return new(StaticBalancerConf)
}

func (b *StaticBalancer) Init(app *Application, config interface{}) error {
	conf := config.(*StaticBalancerConf)
	b.redirects = conf.Redirects
	b.threshold = conf.Threshold

	b.workerCount = app.WorkerCount
	b.maxWorkers = app.SocketHandler().MaxConns()

	return nil
}

func (*StaticBalancer) Close() error          { return nil }
func (*StaticBalancer) Status() (bool, error) { return true, nil }

func (b *StaticBalancer) shouldRedirect() (currentWorkers int64, ok bool) {
	currentWorkers = int64(b.workerCount())
	ok = float64(currentWorkers+1)/float64(b.maxWorkers) >= b.threshold
	return
}

func (b *StaticBalancer) RedirectURL() (url string, ok bool, err error) {
	if len(b.redirects) == 0 {
		return
	}
	if _, ok = b.shouldRedirect(); !ok {
		return
	}
	b.Lock()
	nextIndex := (b.currentIndex + 1) % len(b.redirects)
	b.currentIndex = nextIndex
	b.Unlock()
	return b.redirects[nextIndex], true, nil
}
