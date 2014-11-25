/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

type NoBalancer struct{}

func (*NoBalancer) ConfigStruct() interface{}            { return nil }
func (*NoBalancer) Init(*Application, interface{}) error { return nil }
func (*NoBalancer) RedirectURL() (string, bool, error)   { return "", false, nil }
func (*NoBalancer) Status() (bool, error)                { return true, nil }
func (*NoBalancer) Close() error                         { return nil }

func init() {
	AvailableBalancers["none"] = func() HasConfigStruct { return new(NoBalancer) }
	AvailableBalancers.SetDefault("none")
}
