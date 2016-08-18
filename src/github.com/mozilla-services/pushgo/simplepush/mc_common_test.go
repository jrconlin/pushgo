/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"testing"
)

func Test_ChannelState(t *testing.T) {
	if StateDeleted.String() != "deleted" {
		t.Error("ChannelState deleted returned wrong string")
	}
	if StateLive.String() != "live" {
		t.Error("ChannelState live returned wrong string")
	}
	if StateRegistered.String() != "registered" {
		t.Error("ChannelState registered returned wrong string")
	}
}

func Test_ChannelIDs(t *testing.T) {
	cs := ChannelIDs{"foo", "bar"}
	if cs.Len() != 2 {
		t.Error("ChannelIDs returned wrong length")
	}
	if cs.Swap(0, 1); cs[0] != "bar" {
		t.Error("ChannelIDs swap failed")
	}
	if !cs.Less(0, 1) {
		t.Error("ChannelIDs bar not less than foo")
	}
	if cs.IndexOf("bar") != 0 {
		t.Error("ChannelID did not find bar at right location")
	}
	if cs.IndexOf("invalid") != -1 {
		t.Error("ChannelID found unfindable")
	}
}

func Test_remove(t *testing.T) {
	cs := []string{"apple", "banana", "cantelope", "durian"}
	nl := remove(cs, 0)
	if cs[0] == "apple" {
		t.Errorf("Failed to remove first item")
	}
	nl = remove(cs, -1)
	if len(nl) != len(cs) {
		t.Errorf("removed invalid position")
	}
}
