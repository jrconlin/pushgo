/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"net"
	"testing"
	"time"
	//mc "github.com/bradfitz/gomemcache/memcache"
)

const (
	TESTUAID = "deadbeef-0123-4567-89ab-cdef01234567"
	TESTCHID = "decafbad-0123-4567-89ab-cdef01234567"
)

func Test_ConfigStruct(t *testing.T) {
	// passing endpoints via config because of limits in
	//how the testing libs work
	testGm := NewGomemc()
	if _, ok := testGm.ConfigStruct().(*GomemcConf); !ok {
		t.Error("GomemStore invalid configuration struct")
	}

}

func Test_Init(t *testing.T) {
	testGm := NewGomemc()
	gmCfg := testGm.ConfigStruct()
	testApp := &Application{}

	err := testGm.Init(testApp, gmCfg)
	if err != nil {
		t.Error("GomemStore Init reported error: %s", err)
	}

}

func Test_KeyToIDs(t *testing.T) {
	testGm := NewGomemc()
	testGm.logger, _ = NewLogger(&TestLogger{DEBUG, t})

	tu, tc, ok := testGm.KeyToIDs("alpha.beta")
	if tu != "alpha" || tc != "beta" || !ok {
		t.Error("GomemStore KeyToIDs failed to convert")
	}
	tu, tc, ok = testGm.KeyToIDs("invalid")
	if ok {
		t.Error("GomemStore KeyToIDs accepted invalid content")
	}

	tu, tc, ok = testGm.KeyToIDs("alpha.beta.gamma")
	if tu != "alpha" || tc != "beta.gamma" || !ok {
		t.Error("GomemStore KeyToIDs failed to properly break key")
	}

}

func Test_IDsToKey(t *testing.T) {
	testGm := NewGomemc()
	testGm.logger, _ = NewLogger(&TestLogger{DEBUG, t})

	if key, ok := testGm.IDsToKey("alpha", "beta"); !ok || key != "alpha.beta" {
		t.Error("GomemStore IDsToKey unable to generate proper key")
	}
	if _, ok := testGm.IDsToKey("", "beta"); ok {
		t.Error("GomemStore IDsToKey created invalid key")
	}
	if _, ok := testGm.IDsToKey("alpha", ""); ok {
		t.Error("GomemStore IDsToKey created invalid key")
	}
}

func setup(t *testing.T) (testGm *GomemcStore, connected bool) {
	testGm = NewGomemc()
	gmCfg := testGm.ConfigStruct()
	testApp := &Application{}
	testGm.Init(testApp, gmCfg)
	testGm.logger, _ = NewLogger(&TestLogger{DEBUG, t})
	c, err := net.Dial("tcp", testGm.Hosts[0])
	if err != nil {
		return testGm, false
	}
	c.Close()
	return testGm, true
}

func Test_Status(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Status check, no server.")
		return
	}

	success, err := testGm.Status()
	if err != nil {
		t.Errorf("Status returned error: %s", err.Error())
	}
	if success != true {
		t.Error("Status failed to return true.")
	}
}

func Test_Exists(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping exists check, no server.")
		return
	}

	err := testGm.Register(TESTUAID, TESTCHID, 12345)
	if err != nil {
		t.Errorf("Test_Exists registration failed: %v", err)
		return
	}

	if !testGm.Exists(TESTUAID) {
		t.Error("Test_Exists Could not locate record")
	}

	if testGm.Exists("InvalidUAID") {
		t.Error("Test_Exists Failed to reject invalid record")
	}
	if testGm.Exists(TESTCHID) {
		t.Error("Test_Exists found possibly incorrect record")
	}
	testGm.DropAll(TESTUAID)
}

func Test_storeRegister(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping storeRegister check, no server.")
	}

	var err error

	err = testGm.storeRegister(TESTUAID, TESTCHID, 12345)
	if err != nil {
		t.Error("Test_storeRegister returned error: %v", err)
		return
	}

	testGm.DropAll(TESTUAID)
}

func Test_Register(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error

	if err = testGm.Register(TESTUAID, TESTCHID, 12345); err != nil {
		t.Error("Register returned error: %v", err)
	}
	if testGm.Register("", TESTCHID, 12345) != ErrNoID {
		t.Error("Register failed to reject empty UAID")
	}
	if testGm.Register(TESTUAID, "", 12345) != ErrNoChannel {
		t.Error("Register failed to reject empty CHID")
	}
	if testGm.Register("Invalid", TESTCHID, 12345) != ErrInvalidID {
		t.Error("Register failed to reject invalid UAID")
	}
	if testGm.Register(TESTUAID, "Invalid", 12345) != ErrInvalidChannel {
		t.Error("Register failed to reject invalid Channel")
	}

	testGm.DropAll(TESTUAID)
}

func Test_storeUpdate(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error

	err = testGm.storeUpdate(TESTUAID, TESTCHID, 12345)
	if err != nil {
		t.Errorf("storeUpdate returned error: %v", err)
	}

	err = testGm.storeUpdate(TESTUAID, TESTCHID, 67890)
	if err != nil {
		t.Errorf("storeUpdate update returned error: %v", err)
	}
	key, _ := testGm.IDsToKey(TESTUAID, TESTCHID)
	rec, err := testGm.fetchRec(key)
	if err != nil || rec.Version != 67890 {
		t.Error("storeUpdate failed to store value.")
	}

	err = testGm.storeUpdate("", TESTCHID, 12345)
	if err == nil {
		t.Error("storeUpdate failed to reject invalid key")
	}
	testGm.DropAll(TESTUAID)
}

func Test_Update(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error
	validKey, _ := testGm.IDsToKey(TESTUAID, TESTCHID)

	err = testGm.Update(validKey, 12345)
	if err != nil {
		t.Error("Update returned error: %v", err)
	}
	err = testGm.Update("."+TESTCHID, 12345)
	if err == nil {
		t.Error("Update failed to reject empty UAID")
	}
	err = testGm.Update(TESTUAID+".", 12345)
	if err == nil {
		t.Error("Update failed to reject empty ChannelID")
	}
	err = testGm.Update("Invalid."+TESTCHID, 12345)
	if err == nil {
		t.Error("Update failed to reject invalid UAID")
	}
	err = testGm.Update(TESTUAID+".Invalid", 12345)
	if err == nil {
		t.Error("Update failed to reject invalid ChannelID")
	}
	testGm.DropAll(TESTUAID)
}

func Test_storeUnregister(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error

	testGm.Register(TESTUAID, TESTCHID, 12345)

	err = testGm.storeUnregister(TESTUAID, TESTCHID)
	if err != nil {
		t.Errorf("storeUnregister returned error: %v", err)
	}

	err = testGm.storeUnregister(TESTUAID, "Invalid")
	if err == nil {
		t.Error("storeUnregistered failed to reject invalid channel")
	}

	key, _ := testGm.IDsToKey(TESTUAID, TESTCHID)
	rec, err := testGm.fetchRec(key)
	if rec.State != StateDeleted {
		t.Error("storeUnregistered failed to remove key")
	}

	testGm.DropAll(TESTUAID)
}

func Test_Unregister(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error

	testGm.Register(TESTUAID, TESTCHID, 12345)
	err = testGm.Unregister(TESTUAID, TESTCHID)
	if err != nil {
		t.Errorf("Unregister returned error: %v", err)
	}
	if testGm.Unregister("", TESTCHID) != ErrNoID {
		t.Error("Unregister failed to reject empty UAID")
	}
	if testGm.Unregister(TESTUAID, "") != ErrNoChannel {
		t.Error("Unregister failed to reject empty CHID")
	}
	if testGm.Unregister("Invalid", TESTCHID) != ErrInvalidID {
		t.Error("Unregister failed to reject invalid UAID")
	}
	if testGm.Unregister(TESTUAID, "Invalid") != ErrInvalidChannel {
		t.Error("Unregister failed to reject invalid Channel")
	}
	testGm.DropAll(TESTUAID)
}

func Test_Drop(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error

	testGm.Register(TESTUAID, TESTCHID, 12345)
	if testGm.Drop("", TESTCHID) != ErrNoID {
		t.Error("Drop failed to reject empty UAID")
	}
	if testGm.Drop(TESTUAID, "") != ErrNoChannel {
		t.Error("Drop failed to reject empty CHID")
	}
	if testGm.Drop("Invalid", TESTCHID) != ErrInvalidID {
		t.Error("Drop failed to reject invalid UAID")
	}
	if testGm.Drop(TESTUAID, "Invalid") != ErrInvalidChannel {
		t.Error("Drop failed to reject invalid Channel")
	}
	err = testGm.Drop(TESTUAID, TESTCHID)
	if err != nil {
		t.Errorf("Drop returned error: %v", err)
	}
}

func Test_FetchAll(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error

	now := time.Now()
	testGm.Register(TESTUAID, TESTCHID, 12345)

	updates, _, err := testGm.FetchAll(TESTUAID, now)
	if err != nil {
		t.Errorf("FetchAll returned error: %v", err)
	}
	if len(updates) == 0 {
		t.Error("FetchAll failed to find record")
	}
	if updates[0].ChannelID != TESTCHID || updates[0].Version != 12345 {
		t.Error("FetchAll returned unexpected record")
	}

	testGm.Unregister(TESTUAID, TESTCHID)
	updates, _, err = testGm.FetchAll(TESTUAID, now)
	if len(updates) != 0 {
		t.Error("FetchAll found deleted record")
	}
	testGm.DropAll(TESTUAID)
}

func Test_Ping(t *testing.T) {
	testGm, connected := setup(t)
	if !connected {
		t.Skip("Skipping Register check, no server.")
	}

	var err error
	pingData := []byte("{stuff: \"This is a bunch of ping data\"}")

	if err = testGm.PutPing(TESTUAID, pingData); err != nil {
		t.Errorf("PutPing returned an error: %v", err)
	}
	rdata, err := testGm.FetchPing(TESTUAID)
	if err != nil {
		t.Errorf("FetchPing returned an error: %v", err)
	}
	if bytes.Compare(rdata, pingData) != 0 {
		t.Error("FetchPing did not return the data correctly")
	}

	if err = testGm.DropPing(TESTUAID); err != nil {
		t.Errorf("DropPing returned an error: %v", err)
	}
	if rdata, err = testGm.FetchPing(TESTUAID); err == nil {
		t.Error("FetchPing returned deleted ping")
	}
}
