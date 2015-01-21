/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"sync/atomic"
)

// Once executes a function exactly once. Unlike sync.Once, Once exposes
// its state, and allows the function to return an error.
type Once struct {
	done int32 // Accessed atomically.
}

// IsDone indicates whether Do has been called for this instance of Once.
// sync.Once does not expose this information.
func (o *Once) IsDone() bool {
	return atomic.LoadInt32(&o.done) == 1
}

// Do calls the function f if and only f Do has not been called for this
// instance of Once. Do propagates the return value of f if invoked for the
// first time, and returns nil for subsequent invocations.
func (o *Once) Do(f func() error) error {
	if atomic.CompareAndSwapInt32(&o.done, 0, 1) {
		return f()
	}
	return nil
}
