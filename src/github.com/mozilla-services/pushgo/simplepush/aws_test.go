package simplepush

import (
	"net/http"
	"testing"
)

func Test_AWSSignature(t *testing.T) {
	testRequest, _ := http.NewRequest("POST", "https://iam.amazonaws.com", nil)
	testPayload := "Action=ListUsers&Version=2010-05-08"
	testRequest.Header.Set("X-Amz-Date", "20110909T233600Z")
	testRequest.Header.Set("Host", "iam.amazonaws.com")
	testRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	testSecret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testRegion := "us-east-1"
	testService := "iam"
	header, sig, err := AWSSignature(testRequest,
		testSecret,
		testRegion,
		testService,
		[]byte(testPayload))
	if err != nil {
		t.Error(err.Error)
	}
	t.Logf("Header: %s, sig: %s\n", header, sig)
	if sig != "ced6826de92d2bdeed8f846f0bf508e8559e98e4b0199114b84c54174deb456c" {
		t.Error("Signature failed to match")
	}
	if header != "content-type;host;x-amz-date" {
		t.Error("Header list failed to match")
	}
}
