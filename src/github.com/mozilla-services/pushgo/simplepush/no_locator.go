/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import ()

// No-op locator, used for testing
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

func (l *NoLocator) Close() (err error) {
	return nil
}

func (l *NoLocator) Contacts(string) (contacts []string, err error) {
	return contacts, nil
}

func (l *NoLocator) Status() (ok bool, err error) {
	return true, nil
}

func (l *NoLocator) Register() (err error) {
	return nil
}

func init() {
	AvailableLocators["test"] = func() HasConfigStruct {
		return NewNoLocator()
	}
}
