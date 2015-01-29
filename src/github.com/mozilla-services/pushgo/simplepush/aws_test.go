package simplepush

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

func Test_AWSSignature(t *testing.T) {
	bodyString := "Action=ListUsers&Version=2010-05-08"
	testPayload := bytes.NewBufferString(bodyString)
	testRequest, _ := http.NewRequest("POST",
		"https://iam.amazonaws.com",
		testPayload)
	testRequest.Header.Set("X-Amz-Date", "20110909T233600Z")
	testRequest.Header.Set("Host", "iam.amazonaws.com")
	testRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	testSecret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testRegion := "us-east-1"
	testService := "iam"
	awsHeaderInfo, err := AWSSignature(testRequest,
		testSecret,
		testRegion,
		testService)
	body := new(bytes.Buffer)
	body.ReadFrom(testRequest.Body)
	if bodyString != body.String() {
		t.Error("Body destroyed")
	}
	if err != nil {
		t.Error(err.Error)
	}
	if awsHeaderInfo.Signature != "ced6826de92d2bdeed8f846f0bf508e8559e98e4b0199114b84c54174deb456c" {
		t.Error("Signature failed to match", awsHeaderInfo.Signature)
	}
	if awsHeaderInfo.SignedHeaders != "content-type;host;x-amz-date" {
		t.Error("Header list failed to match")
	}
}

func Test_AWSCache(t *testing.T) {
	testSecret := "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testRegion := "us-east-1"
	testService := "iam"
	testNow := "20100508"
	retSig := []byte{91, 130, 236, 62, 156, 58, 160, 221, 200, 73,
		158, 236, 73, 132, 179, 27, 49, 118, 13, 182, 116, 19, 119,
		20, 111, 166, 54, 176, 126, 111, 151, 134}
	acache := NewAWSCache(testNow, testSecret, testRegion, testService)
	y, m, d := time.Now().UTC().Date()
	acache.expry = time.Date(y, m, d-1, 0, 0, 0, 0, time.UTC)
	if !acache.Expired() {
		t.Error("Failed to notice cache expired")
	}
	if !bytes.Equal(acache.SignKey, retSig) {
		t.Error("Signature doesn't match expected value")
		t.Logf("%v, %v\n", retSig, acache.SignKey)
	}
}
