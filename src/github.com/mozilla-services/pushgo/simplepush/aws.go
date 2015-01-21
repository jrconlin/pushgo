/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	AMZ_DATE      string = "20060102T150405Z"
	AMZ_SHORTDATE string = "20060102"
	AMZ_HASH_ALGO string = "AWS4-HMAC-SHA256"
)

var (
	ErrNoElastiCache      StorageError = "ElastiCache returned no endpoints"
	ErrElastiCacheTimeout StorageError = "ElastiCache query timed out"
	ErrInvalidSignature   AWSError     = "Invalid value specified for header"
	awsHash               hash.Hash    = sha256.New()
)

type AWSCache struct {
	expry    int64
	sha256   hash.Hash
	region   string
	service  string
	secret   string
	shortNow string
	signKey  []byte
}

func (r *AWSCache) checkDate() boolean {
	return time.Now().UTC().Unix() > r.expry
}

func NewAWSCache(secret, region, service) (*AWSCache, error) {
	y, m, d := time.Now().UTC().Date()
	tomorrow := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC).Unix()
	return &AWSCache{
		shortNow: time.Now().UTC().Format(AMZ_SHORTDATE),
		secret:   secret,
		sha256:   sha256.New(),
		service:  service,
		expry:    tomorrow,
	}, nil
}

type AWSError string

func (e AWSError) Error() string {
	return fmt.Sprintf("AWSError: %s", string(e))
}

// InstanceInfo returns information about the current instance.
type InstanceInfo interface {
	InstanceID() (id string, err error)
	PublicHostname() (hostname string, err error)
}

// LocalInfo returns static instance info.
type LocalInfo struct {
	Hostname string
}

func (l LocalInfo) InstanceID() (string, error)     { return "", nil }
func (l LocalInfo) PublicHostname() (string, error) { return l.Hostname, nil }

// EC2Info fetches instance info from the EC2 metadata service. See
// http://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-metadata.html
type EC2Info struct {
	http.Client
}

func (e *EC2Info) Get(item string) (body string, err error) {
	resp, err := e.Client.Do(&http.Request{
		Method: "GET",
		URL: &url.URL{
			Scheme: "http",
			Host:   "169.254.169.254",
			Path:   path.Join("/latest/meta-data", item),
		},
	})
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(ioutil.Discard, resp.Body)
		err = fmt.Errorf("Unexpected status code: %d", resp.StatusCode)
		return
	}
	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}
	return string(respBytes), nil
}

// Get the EC2 instance ID for this machine.
func (e *EC2Info) InstanceID() (id string, err error) {
	return e.Get("instance-id")
}

// Get the public AWS hostname for this machine.
func (e *EC2Info) PublicHostname() (hostname string, err error) {
	return e.Get("public-hostname")
}

// GetElastiCacheEndpoints queries the ElastiCache Auto Discovery service
// for a list of memcached nodes in the cache cluster, using the given seed
// node.
func GetElastiCacheEndpoints(configEndpoint string) ([]string, error) {
	c, err := net.Dial("tcp", configEndpoint)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	// http://docs.aws.amazon.com/AmazonElastiCache/latest/UserGuide/AutoDiscovery.AddingToYourClientLibrary.html
	reader, writer := bufio.NewReader(c), bufio.NewWriter(c)
	writer.Write([]byte("config get cluster\r\n"))
	writer.Flush()

	reader.ReadString('\n')
	reader.ReadString('\n')
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	hosts := strings.Split(line, " ")
	if len(hosts) == 0 {
		return nil, ErrNoElastiCache
	}

	endpoints := make([]string, 0, len(hosts))
	for _, v := range hosts {
		authority := strings.Split(strings.Map(dropSpace, v), "|")
		if len(authority) < 3 {
			continue
		}
		endpoints = append(endpoints, fmt.Sprintf("%s:%s", authority[1], authority[2]))
	}
	return endpoints, nil
}

// GetElastiCacheEndpointsTimeout returns a list of memcached nodes, using the
// given seed and timeout.
func GetElastiCacheEndpointsTimeout(configEndpoint string, timeout time.Duration) (endpoints []string, err error) {
	results, errors := make(chan []string, 1), make(chan error, 1)
	go func() {
		endpoints, err := GetElastiCacheEndpoints(configEndpoint)
		if err != nil {
			errors <- err
			return
		}
		results <- endpoints
	}()
	select {
	case endpoints = <-results:
	case err = <-errors:
	case <-time.After(timeout):
		err = ErrElastiCacheTimeout
	}
	return
}

// A mapping function that drops ASCII control characters and Unicode
// whitespace characters.
func dropSpace(r rune) rune {
	if r <= ' ' || unicode.IsSpace(r) {
		return -1
	}
	return r
}

/** trim start, end and multiple internal spaces, unless they're quoted.
*
* kinda taking a short cut here by not building a full quote state machine, but
* i've not seen a lot of ' ' and as this sentence shows, dealing with
* apostrophes are a pain.
 */
func awsTrimSpace(in string) (out string) {
	const sp byte = byte(' ')
	const quote byte = byte('"')
	i := 0
	prev := sp
	inq := false
	outb := make([]byte, len(in))
	for _, c := range []byte(in) {
		if c == quote {
			inq = !inq
		}
		if !inq && c == sp && c == prev {
			continue
		}
		outb[i] = c
		i++
		prev = c
	}
	if len(outb) > 0 && outb[i-1] == sp {
		outb[i-1] = 0
	}
	return string(outb)
}

func awsCanonicalHeaders(headers http.Header) (result string, headerList string, err error) {

	// TODO: Make sure Host is set.
	var list []string
	var hList []string

	for k, v := range headers {
		var val string
		k = strings.ToLower(awsTrimSpace(k))
		hList = append(hList, k)
		if len(v) > 1 {
			var args []string
			for _, v1 := range v {
				args = append(args, awsTrimSpace(v1))
			}
			val = strings.Join(args, ",")
		} else {
			val = awsTrimSpace(v[0])
		}
		list = append(list, fmt.Sprintf("%s:%s", k, val))

	}
	sort.Strings(list)
	sort.Strings(hList)
	result = strings.Join(list, "\n")
	headerList = strings.Join(hList, ";")
	return
}

func awsCanonicalArgs(queryString string) (result string, err error) {
	values, err := url.ParseQuery(queryString)
	if err != nil {
		return
	}

	var list []string
	for k, v := range values {
		k = strings.Trim(k, " ?")
		for _, v1 := range v {
			if len(v1) > 0 {
				// Not sure if I need to do the awsTrimSpace for these.
				list = append(list, fmt.Sprintf("%s=%s",
					url.QueryEscape(k),
					url.QueryEscape(strings.TrimSpace(v1))))
			} else {
				list = append(list, url.QueryEscape(k))
			}
		}
	}
	sort.Strings(list)
	result = strings.Join(list, "&")
	return
}

func awsHash(in []byte) string {
	// reuse the hash object, since it never changes.
	awsHash.Reset()
	hash.Write(in)
	return strings.ToLower(hex.EncodeToString(hash.Sum(nil)))
}

func awsHMac(key, data []byte) []byte {
	// the key changes often, so no reuse for you!
	hmac := hmac.New(sha256.New, key)
	hmac.Write(data)
	return hmac.Sum(nil)
}

func AWSSignature(req *http.Request, kSecret, region, service string, payload []byte) (signedHeaders, signature string, err error) {
	var canQuery string
	var now time.Time

	if rdate := req.Header.Get("Date"); rdate != "" {
		now, err = time.Parse(time.RFC1123, rdate)
		if err != nil {
			return
		}
	}
	if rdate := req.Header.Get("x-amz-date"); rdate != "" {
		now, err = time.Parse(AMZ_DATE, rdate)
		if err != nil {
			return
		}
	}
	if now.IsZero() {
		now = time.Now().UTC()
		req.Header.Set("x-amz-date", time.Now().Format(AMZ_DATE))
	}
	shortNow := now.Format(AMZ_SHORTDATE)

	if len(req.URL.RawQuery) > 0 {
		canQuery, err = awsCanonicalArgs(req.URL.RawQuery)
		if err != nil {
			return
		}
	}
	canHeaders, canHeaderList, err := awsCanonicalHeaders(req.Header)
	if err != nil {
		return
	}

	awsTermString := "aws4_request"
	hashPayload := awsHash(payload)
	path := req.URL.Path
	if len(path) == 0 {
		path = "/"
	}

	canonicalRequest := fmt.Sprintf(
		"%s\n%s\n%s\n%s\n\n%s\n%s", // double \n to append to canHeaders
		strings.ToUpper(req.Method),
		path,
		canQuery,
		canHeaders,
		canHeaderList,
		hashPayload)
	requestSignature := awsHash([]byte(canonicalRequest))
	canonicalSig := fmt.Sprintf(
		"%s\n%s\n%s\n%s",
		AMZ_HASH_ALGO,
		now.Format(AMZ_DATE),
		fmt.Sprintf("%s/%s/%s/%s",
			shortNow,
			strings.ToLower(region),
			strings.ToLower(service),
			awsTermString),
		requestSignature)
	kDate := awsHMac([]byte("AWS4"+kSecret), []byte(shortNow))
	kRegion := awsHMac(kDate, []byte(region))
	kService := awsHMac(kRegion, []byte(service))
	kSigning := awsHMac(kService, []byte(awsTermString))
	sigKey := strings.ToLower(hex.EncodeToString(awsHMac(kSigning,
		[]byte(canonicalSig))))
	return canHeaderList, sigKey, nil
}
