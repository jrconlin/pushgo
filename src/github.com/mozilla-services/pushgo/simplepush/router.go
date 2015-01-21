/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
// HTTP version of the cross machine router
// Fetch a list of peers (via an etcd store, DHT, or static list), divvy them
// up into buckets, proxy the update to the servers, stopping once you've
// gotten a successful return.
// PROS:
//  Very simple to implement
//  hosts can autoannounce
//  no AWS dependencies
// CONS:
//  fair bit of cross traffic (try to minimize)

// Obviously a PubSub would be better, but might require more server
// state management (because of duplicate messages)
package simplepush

import (
	"time"
)

var (
	AvailableRouters = make(AvailableExtensions)
)

type Router interface {
	// Start the router
	Start(chan<- error)

	// Close down the router
	Close() error

	// Route a notification
	Route(cancelSignal <-chan bool, uaid, chid string, version int64,
		sentAt time.Time, logID string, data string) (bool, error)

	// Register handling for a uaid, this func may be called concurrently
	Register(uaid string) error

	// Unregister a uaid for routing, this func may be called concurrently
	Unregister(uaid string) error

	// Unique identifier used by locator's for this node
	URL() string

	// Indicate status of the router, error if there's a problem
	Status() (bool, error)
}
