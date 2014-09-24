/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

type StaticLocatorConf struct {
	Contacts []string `env:"contacts"`
}

type StaticLocator struct {
	logger   *SimpleLogger
	metrics  *Metrics
	contacts []string
}

func (*StaticLocator) ConfigStruct() interface{} {
	return new(StaticLocatorConf)
}

func (l *StaticLocator) Init(app *Application, config interface{}) error {
	conf := config.(*StaticLocatorConf)
	l.logger = app.Logger()
	l.metrics = app.Metrics()
	l.contacts = conf.Contacts
	return nil
}

func (l *StaticLocator) Close() error                      { return nil }
func (l *StaticLocator) Contacts(string) ([]string, error) { return l.contacts, nil }
func (l *StaticLocator) Status() (bool, error)             { return true, nil }

func init() {
	AvailableLocators["static"] = func() HasConfigStruct { return new(StaticLocator) }
}
