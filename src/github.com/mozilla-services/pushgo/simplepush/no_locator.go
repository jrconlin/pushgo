/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import ()

// NoLocator stores routing endpoints in etcd and polls for new contacts.
type NoLocator struct {
	logger *SimpleLogger
}

func NewNoLocator() *NoLocator {
	return &NoLocator{}
}

func (*NoLocator) ConfigStruct() interface{} {
	return &struct{}{}
}

func (l *NoLocator) Init(app *Application, config interface{}) (err error) {
	return nil
}

// Close stops the locator and closes the etcd client connection. Implements
// Locator.Close().
func (l *NoLocator) Close() (err error) {
	return nil
}

// Contacts returns a shuffled list of all nodes in the Simple Push cluster.
// Implements Locator.Contacts().
func (l *NoLocator) Contacts(string) (contacts []string, err error) {
	return contacts, nil
}

// Status determines whether etcd can respond to requests. Implements
// Locator.Status().
func (l *NoLocator) Status() (ok bool, err error) {
	return true, nil
}

// Register registers the server to the etcd cluster.
func (l *NoLocator) Register() (err error) {
	return nil
}

func init() {
	AvailableLocators["test"] = func() HasConfigStruct {
		return NewNoLocator()
	}
}
