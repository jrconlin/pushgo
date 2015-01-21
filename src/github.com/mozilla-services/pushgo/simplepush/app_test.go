/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"crypto/aes"
	"testing"

	"github.com/rafrombrc/gomock/gomock"
	. "github.com/smartystreets/goconvey/convey"
)

func TestCreateEndpoint(t *testing.T) {
	useMockFuncs()
	defer useStdFuncs()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mckEndHandler := NewMockHandler(mockCtrl)

	Convey("Should allocate update endpoints", t, func() {
		app := NewApplication()
		app.endpointTemplate = testEndpointTemplate

		Convey("Should not encrypt endpoints without a key", func() {
			app.SetTokenKey("")
			app.SetEndpointHandler(mckEndHandler)

			mckEndHandler.EXPECT().URL().Return("https://example.com")

			endpoint, err := app.CreateEndpoint("123")
			So(err, ShouldBeNil)
			So(endpoint, ShouldEqual, "https://example.com/123")
		})

		Convey("Should encrypt endpoints with a key", func() {
			app.SetTokenKey("HVozKz_n-DPopP5W877DpRKQOW_dylVf")
			app.SetEndpointHandler(mckEndHandler)

			mckEndHandler.EXPECT().URL().Return("https://example.com")

			endpoint, err := app.CreateEndpoint("456")
			So(err, ShouldBeNil)
			So(endpoint, ShouldEqual,
				"https://example.com/AAAAAAAAAAAAAAAAAAAAAGMKig==")
		})

		Convey("Should reject invalid keys", func() {
			app.SetTokenKey("lLyhlLk8qus1ky4ER8yjN5o=")
			app.SetEndpointHandler(mckEndHandler)

			_, err := app.CreateEndpoint("123")
			So(err, ShouldEqual, aes.KeySizeError(17))
		})

		Convey("Should return a relative URL without an update handler", func() {
			app.SetTokenKey("O03rpLsdafhIhJEjEJt-CgVHyqHI650oy0pZZvplKDc=")
			endpoint, err := app.CreateEndpoint("789")
			So(err, ShouldBeNil)
			So(endpoint, ShouldEqual, "/AAAAAAAAAAAAAAAAAAAAAPfdsA==")
		})
	})
}

func BenchmarkCreateEndpoint(b *testing.B) {
	mockCtrl := gomock.NewController(b)
	defer mockCtrl.Finish()

	app := NewApplication()
	app.endpointTemplate = testEndpointTemplate
	app.SetTokenKey("")
	mckEndHandler := NewMockHandler(mockCtrl)
	mckEndHandler.EXPECT().URL().Return("https://example.com").Times(b.N)
	app.SetEndpointHandler(mckEndHandler)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		endpoint, err := app.CreateEndpoint("123")
		if err != nil {
			b.Fatalf("Error generating update endpoint: %s", err)
		}
		expected := "https://example.com/123"
		if endpoint != expected {
			b.Fatalf("Wrong endpoint: got %q; want %q", endpoint, expected)
		}
	}
}
