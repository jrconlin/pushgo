/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

var AvailableBalancers = make(AvailableExtensions)

// A Balancer redirects clients to different hosts if the current host is
// overloaded.
type Balancer interface {
	// RedirectURL redirects connecting clients to a peer host. Clients should
	// detect and handle redirect loops.
	RedirectURL() (origin string, ok bool, err error)

	// Status indicates whether the balancer is healthy.
	Status() (ok bool, err error)

	// Close stops and releases any resources associated with the balancer.
	Close() error
}
