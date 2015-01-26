/* test for:
Connection
Register
Send
Back-off
*/
/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/rafrombrc/gomock/gomock"
	. "github.com/smartystreets/goconvey/convey"
)

// Create a mock http.Client that matches the interface limits.
type mockGCMClient struct {
	reply *http.Response
	req   *http.Request
	t     *testing.T
	err   error
}

func (r *mockGCMClient) Do(req *http.Request) (resp *http.Response, err error) {
	r.req = req
	// r.t.Log("Calling do :%s", fmt.Sprintf("%+v", req))
	resp = r.reply

	if resp != nil {
		resp.Request = req
	} else {
		resp = &http.Response{
			Request:    req,
			StatusCode: 200,
			Body:       respBody("Ok"),
		}
	}
	// reset after to avoid retry loop
	r.reset()
	return
}

func (r *mockGCMClient) reset() {
	r.reply = nil
	r.err = nil
}

// fake response body
type rbody struct {
	io.Reader
}

func (r rbody) Close() error {
	return nil
}

// Generate a fake body ReadCloser
func respBody(data string) io.ReadCloser {
	return rbody{
		io.MultiReader(bytes.NewReader([]byte(data))),
	}
}

// Remember, you can add methods to package "local" structs that
// can then expose or alter internal behaviors of those structs.
// useful for testing.
func (r *GCMPing) ReplaceClient(c GCMClient) {
	r.client = c
}

func Test_GCMSend(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).AnyTimes()
	mckStat := &TestMetrics{}
	mckStat.Init(nil, nil)
	mckStore := NewMockStore(mockCtrl)
	mckEndHandler := NewMockHandler(mockCtrl)

	mckGCMClient := &mockGCMClient{t: t}
	Convey("GCM Proprietary Ping", t, func() {
		uaid := "deadbeef00000000000000000000"
		vers := int64(1)
		data := "i'm a little teapot"
		fakeConnect := []byte(`{"regid":"testing"}`)
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetEndpointHandler(mckEndHandler)

		mckGCMClient.reset()
		headers := make(http.Header, 1)
		headers.Set("Retry-After", "1")
		mckGCMClient.reply = &http.Response{
			Body:       respBody("Ok"),
			StatusCode: 503,
			Header:     headers,
		}

		mckStore.EXPECT().FetchPing(uaid).Return(fakeConnect, nil)

		testGcm := &GCMPing{
			logger:  app.Logger(),
			metrics: mckStat,
			store:   mckStore,
			apiKey:  "test_api_key",
		}
		conf := testGcm.ConfigStruct()
		err := testGcm.Init(app, conf)
		So(err, ShouldBeNil)
		testGcm.ReplaceClient(mckGCMClient)

		// For this test, we first respond with a fake 503 message that
		// requests a "retry after", we follow up with a second message
		// that returns success.
		//
		// Not sure why but when using gomock, calls to Increment are
		// not being registered, so falling back to older testMetrics
		// object.
		ok, err := testGcm.Send(uaid, vers, data)
		t.Logf("Request: %+v\n", mckGCMClient)
		t.Logf("mckStat: %+v\n", mckStat)
		So(err, ShouldBeNil)
		So(ok, ShouldEqual, true)
		So(mckStat.Counters["ping.gcm.retry"], ShouldEqual, 1)
		So(mckStat.Counters["ping.gcm.success"], ShouldEqual, 1)
	})
}
