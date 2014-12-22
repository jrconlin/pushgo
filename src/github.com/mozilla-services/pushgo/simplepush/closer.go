/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"sync/atomic"
)

type CloserOnce interface {
	CloseOnce() error
}

type Closable struct {
	CloserOnce
	closed int32 // Accessed atomically.
}

func (c *Closable) IsClosed() bool {
	return atomic.LoadInt32(&c.closed) == 1
}

func (c *Closable) Close() error {
	if atomic.CompareAndSwapInt32(&c.closed, 0, 1) {
		return c.CloseOnce()
	}
	return nil
}
