/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"fmt"
	"testing"
)

type errTest struct {
	name       string
	err        error
	statusCode int
	message    string
}

func (t errTest) Test() error {
	status, message := ErrToStatus(t.err)
	if status != t.statusCode {
		return fmt.Errorf("On test %s, wrong status code: got %d; want %d",
			t.name, status, t.statusCode)
	}
	if message != t.message {
		return fmt.Errorf("On test %s, wrong message: got %q; want %q",
			t.name, message, t.message)
	}
	return nil
}

func TestErrToStatus(t *testing.T) {
	tests := []errTest{
		{"No error", nil, 200, ""},
		{"401 status code", ErrNoID, 401, "Invalid Command"},
		{"503 status code", ErrExistingID, 503, "Service Unavailable"},
		{"413 status code", ErrDataTooLong, 500, "An unexpected error occurred"},
		{"Custom error", errors.New("oops"), 500, "An unexpected error occurred"},
	}
	for _, test := range tests {
		if err := test.Test(); err != nil {
			t.Error(err)
		}
	}
}
