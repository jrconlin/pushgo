/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rafrombrc/gomock/gomock"
	. "github.com/smartystreets/goconvey/convey"
)

type keyTest struct {
	name     string
	key      string
	expected bool
}

func (t keyTest) Test() error {
	actual := validPK(t.key)
	if actual != t.expected {
		return fmt.Errorf("On test %s, got %s; want %s", t.name, actual,
			t.expected)
	}
	return nil
}

func formReader(vals url.Values) io.ReadCloser {
	return ioutil.NopCloser(strings.NewReader(vals.Encode()))
}

func getJSON(header http.Header, body *bytes.Buffer) (*bytes.Buffer, bool) {
	if header.Get("Content-Type") != "application/json" {
		return nil, false
	}
	buf := new(bytes.Buffer)
	if err := json.Compact(buf, body.Bytes()); err != nil {
		return nil, false
	}
	return buf, true
}

func TestEndpointValidKey(t *testing.T) {
	tests := []keyTest{
		{"Hyphenated key", "bb817e78-c568-4780-90c3-471f3b74f055.29f72d42-7a2a-42e4-897b-dc2c8a7cb676", true},
		{"Non-hyphenated key", "bb817e78c568478090c3471f3b74f055.29f72d427a2a42e4897bdc2c8a7cb676", true},
		{"Non-hyphenated, case-insensitive key", "BB817E78C568478090C3471F3B74F055.29F72D427A2A42E4897BDC2C8A7CB676", true},
		{"Valid Base64-encoded key", "u4F+eMVoR4CQw0cfO3TwVQ==.KfctQnoqQuSJe9wsiny2dg==", false},
		{"Invalid UUID; valid key", "pQrS-tUv-1234.wXyZ-5678", true},
		{"Non-alphanumeric characters", "\t\r\n", false},
		{"Extraneous punctuators", "_=!@#$%^&*()[]", false},
	}
	for _, test := range tests {
		if err := test.Test(); err != nil {
			t.Error(err)
		}
	}
}

func TestEndpointResolveKey(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()

	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)

	Convey("Endpoint tokens", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)

		Convey("Should return a 404 for invalid tokens", func() {
			app.SetTokenKey("c3v0AlmmxXu_LSfdZY3l3eayLsIwkX48")
			eh := NewEndpointHandler()
			eh.setApp(app)
			app.SetEndpointHandler(eh)

			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL: &url.URL{
					Path: "/update/j1bqzFq9WiwFZbqay-y7xVlfSvtO1eY="}, // "123.456"
			}
			gomock.InOrder(
				mckStore.EXPECT().KeyToIDs("123.456").Return("", "", ErrInvalidKey),
				mckStat.EXPECT().Increment("updates.appserver.invalid"),
			)
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 404)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, `"Invalid Token"`)
		})

		Convey("Should not decode plaintext tokens without a key", func() {
			var err error

			app.SetTokenKey("")
			eh := NewEndpointHandler()
			eh.setApp(app)
			app.SetEndpointHandler(eh)

			_, err = eh.decodePK("")
			So(err, ShouldNotBeNil)

			pk, err := eh.decodePK("123.456")
			So(pk, ShouldEqual, "123.456")
		})

		Convey("Should normalize decoded tokens", func() {
			app.SetTokenKey("LM1xDImCx0rB46LCnx-3v4-Iyfk1LeKJbx9wuvx_z3U=")
			eh := NewEndpointHandler()
			eh.setApp(app)
			app.SetEndpointHandler(eh)

			// Hyphenated IDs should be normalized.
			uaid := "dbda2ba2-004c-491f-9e3d-c5950aee93de"
			chid := "848cd568-3f2a-4108-9ce4-bd0d928ecad4"
			// " \t%s.%s\r\n" % (uaid, chid)
			encodedKey := "qfGSdZzwf20GXiYubmZfIXj11Rx4RGJujFsjSQGdF4LRBhHbB_vt3hdW7cRvL9Fq_t_guMBGkDgebOoa5gRd1GGLN-Cv6h5hkpRTbdju8Tk-hMyC91BP4CEres_8"

			// decodePK should trim whitespace from encoded keys.
			mckStore.EXPECT().KeyToIDs(
				fmt.Sprintf("%s.%s", uaid, chid)).Return(uaid, chid, nil)
			actualUAID, actualCHID, err := eh.resolvePK(encodedKey)
			So(err, ShouldBeNil)
			So(actualUAID, ShouldEqual, uaid)
			So(actualCHID, ShouldEqual, chid)
		})

		Convey("Should reject invalid tokens", func() {
			var err error

			app.SetTokenKey("IhnNwMNbsFWiafTXSgF4Ag==")
			eh := NewEndpointHandler()
			eh.setApp(app)
			app.SetEndpointHandler(eh)

			invalidKey := "b54QOw2omSWBiEq0IuyfBGxHBIR7AI9YhCMA0lP9" // "_=!@#$%^&*()[]"
			uaid := "82398a648c834f8b838cb3945eceaf29"
			chid := "af445ad07e5f46b7a6c858150fc5aa92"
			validKey := fmt.Sprintf("%s.%s", uaid, chid)
			encodedKey := "swKSH8P2qprRt5y0J4Wi7ybl-qzFv1j09WPOfuabpEJmVUqwUpxjprXc2R3Yw0ITbqc_Swntw9_EpCgo_XuRTn7Q7opQYoQUgMPhCgT0EGbK"

			_, _, err = eh.resolvePK(invalidKey[:8])
			So(err, ShouldNotBeNil)

			_, _, err = eh.resolvePK(invalidKey)
			So(err, ShouldNotBeNil)

			// Reject plaintext tokens if a key is specified.
			_, _, err = eh.resolvePK(validKey)
			So(err, ShouldNotBeNil)

			mckStore.EXPECT().KeyToIDs(validKey).Return("", "", ErrInvalidKey)
			_, _, err = eh.resolvePK(encodedKey)
			So(err, ShouldNotBeNil)

			mckStore.EXPECT().KeyToIDs(validKey).Return(uaid, chid, nil)
			actualUAID, actualCHID, err := eh.resolvePK(encodedKey)
			So(err, ShouldBeNil)
			So(actualUAID, ShouldEqual, uaid)
			So(actualCHID, ShouldEqual, chid)
		})
	})
}

func TestEndpointContentType(t *testing.T) {
	Convey("Content type handling", t, func() {
		app := NewApplication()
		eh := NewEndpointHandler()
		eh.setApp(app)
		eh.setMaxDataLen(4096)
		app.SetEndpointHandler(eh)

		Convey("Should supply a content type if omitted", func() {
			vals := make(url.Values)
			vals.Set("version", "123")
			vals.Set("data", randomText(eh.maxDataLen))

			version, data, err := eh.getUpdateParams(&http.Request{
				Method: "PUT",
				Header: http.Header{"Content-Type": {""}},
				URL:    &url.URL{Path: "/update/123"},
				Body:   formReader(vals),
			})
			So(err, ShouldBeNil)
			So(version, ShouldEqual, 123)
			So(len(data), ShouldEqual, eh.maxDataLen)
		})

		Convey("Should use query params if the body is omitted", func() {
			version, data, err := eh.getUpdateParams(&http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL: &url.URL{
					Path:     "/update/123",
					RawQuery: "version=7&data=Hello%2C%20world!",
				},
			})
			So(err, ShouldBeNil)
			So(version, ShouldEqual, 7)
			So(data, ShouldEqual, "Hello, world!")
		})

		Convey("Should accept multipart forms", func() {
			buf := new(bytes.Buffer)
			mw := multipart.NewWriter(buf)
			mw.SetBoundary("db74d732")
			mw.WriteField("version", "123")
			mw.WriteField("data", "Hello, world!")
			mw.Close()

			version, data, err := eh.getUpdateParams(&http.Request{
				Method: "PUT",
				Header: http.Header{"Content-Type": {
					"multipart/form-data; boundary=db74d732"}},
				URL:  &url.URL{Path: "/update/123"},
				Body: ioutil.NopCloser(buf),
			})
			So(err, ShouldBeNil)
			So(version, ShouldEqual, 123)
			So(data, ShouldEqual, "Hello, world!")
		})
	})
}

func TestEndpointInvalidParams(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()

	mckStat := NewMockStatistician(mockCtrl)

	Convey("Invalid update parameters", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)

		eh := NewEndpointHandler()
		eh.setApp(app)
		eh.setMaxDataLen(512)
		app.SetEndpointHandler(eh)

		Convey("Should require PUT requests", func() {
			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "POST",
				Header: http.Header{},
				URL:    &url.URL{Path: "/update/123"},
			}
			mckStat.EXPECT().Increment("updates.appserver.invalid")
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 405)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, `"Method Not Allowed"`)
		})

		Convey("Should reject negative versions", func() {
			vals := make(url.Values)
			vals.Set("version", "-1")

			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL:    &url.URL{Path: "/update/123"},
				Body:   formReader(vals),
			}
			mckStat.EXPECT().Increment("updates.appserver.invalid")
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 400)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, `"Invalid Version"`)
		})

		Convey("Should reject invalid versions", func() {
			vals := make(url.Values)
			vals.Set("version", "abc")

			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL:    &url.URL{Path: "/update/123"},
				Body:   formReader(vals),
			}
			mckStat.EXPECT().Increment("updates.appserver.invalid")
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 400)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, `"Invalid Version"`)
		})

		Convey("Should reject oversized payloads", func() {
			vals := make(url.Values)
			vals.Set("data", randomText(eh.maxDataLen+1))

			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL:    &url.URL{Path: "/update/123"},
				Body:   formReader(vals),
			}

			mckStat.EXPECT().Increment("updates.appserver.toolong")
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 413)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, `"Data exceeds max length of 512 bytes"`)
		})
	})
}

func TestEndpointPinger(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()

	mckStat := NewMockStatistician(mockCtrl)
	mckPinger := NewMockPropPinger(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckServ := NewMockServer(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	Convey("Proprietary pings", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetPropPinger(mckPinger)
		app.SetStore(mckStore)
		app.SetServer(mckServ)

		eh := NewEndpointHandler()
		eh.setApp(app)
		eh.setMaxDataLen(4096)
		app.SetEndpointHandler(eh)

		Convey("Should return early if the pinger can bypass the WebSocket", func() {
			uaid := "91357e1a34714cadacb3f13cf47a2736"
			app.AddWorker(uaid, mckWorker)

			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL:    &url.URL{Path: "/update/123"},
				Body:   nil,
			}
			gomock.InOrder(
				mckStore.EXPECT().KeyToIDs("123").Return(uaid, "456", nil),
				mckStat.EXPECT().Increment("updates.appserver.incoming"),
				mckPinger.EXPECT().Send(uaid, int64(1257894000), "").Return(true, nil),
				mckPinger.EXPECT().CanBypassWebsocket().Return(true),
				mckStat.EXPECT().Increment("updates.appserver.received"),
				mckStat.EXPECT().Timer("updates.handled", gomock.Any()),
			)
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 200)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, "{}")
		})

		Convey("Should continue if the pinger cannot bypass the WebSocket", func() {
			uaid := "e3fc2cf1dc44424685010148b076d08b"
			app.AddWorker(uaid, mckWorker)

			data := randomText(eh.maxDataLen)
			vals := make(url.Values)
			vals.Set("data", data)

			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL:    &url.URL{Path: "/update/123"},
				Body:   formReader(vals),
			}
			gomock.InOrder(
				mckStore.EXPECT().KeyToIDs("123").Return(uaid, "456", nil),
				mckStat.EXPECT().Increment("updates.appserver.incoming"),
				mckPinger.EXPECT().Send(uaid, int64(1257894000), data).Return(true, nil),
				mckPinger.EXPECT().CanBypassWebsocket().Return(false),
				mckStore.EXPECT().Update(uaid, "456", int64(1257894000)),
				mckServ.EXPECT().RequestFlush(mckWorker, "456", int64(1257894000), data),
				mckStat.EXPECT().Increment("updates.appserver.received"),
				mckStat.EXPECT().Timer("updates.handled", gomock.Any()),
			)
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 200)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, "{}")
		})

		Convey("Should continue if the pinger fails", func() {
			uaid := "8f412f5cb2384183bf60f7da26737271"
			app.AddWorker(uaid, mckWorker)

			vals := make(url.Values)
			vals.Set("version", "7")
			resp := httptest.NewRecorder()
			req := &http.Request{
				Method: "PUT",
				Header: http.Header{},
				URL:    &url.URL{Path: "/update/123"},
				Body:   formReader(vals),
			}
			gomock.InOrder(
				mckStore.EXPECT().KeyToIDs("123").Return(uaid, "456", nil),
				mckStat.EXPECT().Increment("updates.appserver.incoming"),
				mckPinger.EXPECT().Send(uaid, int64(7), "").Return(
					true, errors.New("oops")),
				mckStore.EXPECT().Update(uaid, "456", int64(7)),
				mckServ.EXPECT().RequestFlush(mckWorker, "456", int64(7), ""),
				mckStat.EXPECT().Increment("updates.appserver.received"),
				mckStat.EXPECT().Timer("updates.handled", gomock.Any()),
			)
			eh.ServeMux().ServeHTTP(resp, req)

			So(resp.Code, ShouldEqual, 200)
			body, isJSON := getJSON(resp.HeaderMap, resp.Body)
			So(isJSON, ShouldBeTrue)
			So(body.String(), ShouldEqual, "{}")
		})
	})
}

func TestEndpointDelivery(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckLogger := NewMockLogger(mockCtrl)
	mckLogger.EXPECT().ShouldLog(gomock.Any()).Return(true).AnyTimes()
	mckLogger.EXPECT().Log(gomock.Any(), gomock.Any(),
		gomock.Any(), gomock.Any()).AnyTimes()
	mckStat := NewMockStatistician(mockCtrl)
	mckStore := NewMockStore(mockCtrl)
	mckRouter := NewMockRouter(mockCtrl)
	mckServ := NewMockServer(mockCtrl)
	mckWorker := NewMockWorker(mockCtrl)

	Convey("Update delivery", t, func() {
		app := NewApplication()
		app.SetLogger(mckLogger)
		app.SetMetrics(mckStat)
		app.SetStore(mckStore)
		app.SetRouter(mckRouter)
		app.SetServer(mckServ)

		Convey("Should attempt local delivery if `AlwaysRoute` is disabled", func() {
			eh := NewEndpointHandler()
			eh.setApp(app)
			app.SetEndpointHandler(eh)

			Convey("Should route updates if the device is not connected", func() {
				uaid := "f7e9fc483f7344c398701b6fa0e85e4f"
				chid := "737b7a0d25674be4bb184f015fce02cf"
				gomock.InOrder(
					mckStat.EXPECT().Increment("updates.routed.outgoing"),
					mckRouter.EXPECT().Route(nil, uaid, chid, int64(3), timeNow().UTC(),
						"", "").Return(true, nil),
				)
				ok := eh.deliver(nil, uaid, chid, 3, "", "")
				So(ok, ShouldBeTrue)
			})

			Convey("Should return a 404 if routing fails", func() {
				resp := httptest.NewRecorder()
				req := &http.Request{
					Method: "PUT",
					Header: http.Header{HeaderID: {"reqID"}},
					URL:    &url.URL{Path: "/update/123"},
					Body:   formReader(url.Values{"version": {"1"}}),
				}
				gomock.InOrder(
					mckStore.EXPECT().KeyToIDs("123").Return("123", "456", nil),
					mckStat.EXPECT().Increment("updates.appserver.incoming"),
					mckStore.EXPECT().Update("123", "456", int64(1)).Return(nil),
					mckStat.EXPECT().Increment("updates.routed.outgoing"),
					mckRouter.EXPECT().Route(nil, "123", "456", int64(1),
						gomock.Any(), "reqID", "").Return(false, nil),
				)
				eh.ServeMux().ServeHTTP(resp, req)

				So(resp.Code, ShouldEqual, 404)
				body, isJSON := getJSON(resp.HeaderMap, resp.Body)
				So(isJSON, ShouldBeTrue)
				So(body.String(), ShouldEqual, "false")
			})

			Convey("Should return a 404 if local delivery fails", func() {
				uaid := "9e98d6415d8e4fd099ab1bad7178f750"
				chid := "0eecf572e99f4d508666d8da6c0b15a9"
				app.AddWorker(uaid, mckWorker)

				gomock.InOrder(
					mckServ.EXPECT().RequestFlush(mckWorker, chid, int64(3), "").Return(
						errors.New("client gone")),
					mckStat.EXPECT().Increment("updates.appserver.rejected"),
				)
				ok := eh.deliver(nil, uaid, chid, int64(3), "", "")
				So(ok, ShouldBeFalse)
			})

			Convey("Should return an error if storage is unavailable", func() {
				resp := httptest.NewRecorder()
				req := &http.Request{
					Method: "PUT",
					Header: http.Header{},
					URL:    &url.URL{Path: "/update/123"},
					Body:   formReader(url.Values{"version": {"2"}}),
				}
				updateErr := ErrInvalidChannel
				gomock.InOrder(
					mckStore.EXPECT().KeyToIDs("123").Return("123", "456", nil),
					mckStat.EXPECT().Increment("updates.appserver.incoming"),
					mckStore.EXPECT().Update("123", "456", int64(2)).Return(updateErr),
					mckStat.EXPECT().Increment("updates.appserver.error"),
				)
				eh.ServeMux().ServeHTTP(resp, req)

				So(resp.Code, ShouldEqual, updateErr.Status())
				body, isJSON := getJSON(resp.HeaderMap, resp.Body)
				So(isJSON, ShouldBeTrue)
				So(body.String(), ShouldEqual, `"Could not update channel version"`)
			})
		})

		Convey("Should always route updates if `AlwaysRoute` is enabled", func() {
			eh := NewEndpointHandler()
			eh.setApp(app)
			eh.alwaysRoute = true
			app.SetEndpointHandler(eh)

			uaid := "6952a68ee0e7444ebc54f935c4444b13"
			app.AddWorker(uaid, mckWorker)

			chid := "b7ede546585f4cc9b95e9340e3406951"
			version := int64(1)
			data := "Happy, happy, joy, joy!"

			gomock.InOrder(
				mckStat.EXPECT().Increment("updates.routed.outgoing"),
				mckRouter.EXPECT().Route(nil, uaid, chid, version,
					gomock.Any(), "", data).Return(false, nil),
				mckServ.EXPECT().RequestFlush(mckWorker, chid, version, data).Return(nil),
				mckStat.EXPECT().Increment("updates.appserver.received"),
			)

			ok := eh.deliver(nil, uaid, chid, version, "", data)
			So(ok, ShouldBeTrue)
		})
	})
}
