/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

var AvailableLocators = make(AvailableExtensions)

// Locator describes a contact discovery service.
type Locator interface {
	// Close stops and releases any resources associated with the Locator.
	Close() error

	// Contacts returns a slice of candidate peers for the router to probe. For
	// an etcd-based Locator, the slice may contain all nodes in the cluster;
	// for a DHT-based Locator, the slice may contain either a single node or a
	// short list of the closest nodes.
	Contacts(uaid string) ([]string, error)

	// Status indicates whether the discovery service is healthy.
	Status() (bool, error)
}
