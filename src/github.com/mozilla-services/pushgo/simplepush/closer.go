/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"sync/atomic"
)

// CloserOnce describes a closable resource.
type CloserOnce interface {
	CloseOnce() error
}

// Closable is an io.Closer that ensures the underlying resource is closed
// exactly once. It's similar to sync.Once, and safe for concurrent use.
type Closable struct {
	CloserOnce
	closed int32 // Accessed atomically.
}

// IsClosed indicates whether the resource is closed. sync.Once does not
// expose this information.
func (c *Closable) IsClosed() bool {
	return atomic.LoadInt32(&c.closed) == 1
}

// Close closes the resource, returning nil if invoked multiple times.
func (c *Closable) Close() error {
	if atomic.CompareAndSwapInt32(&c.closed, 0, 1) {
		return c.CloseOnce()
	}
	return nil
}
