/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

var (
	ErrNoElastiCache      StorageError = "ElastiCache returned no endpoints"
	ErrElastiCacheTimeout StorageError = "ElastiCache query timed out"
)

/* Get the public AWS hostname for this machine.
 * TODO: Make this a generic utility for getting public info from
 * the aws meta server?
 */
func GetAWSPublicHostname() (hostname string, err error) {
	req := &http.Request{Method: "GET",
		URL: &url.URL{
			Scheme: "http",
			Host:   "169.254.169.254",
			Path:   "/latest/meta-data/public-hostname"}}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var hostBytes []byte
		hostBytes, err = ioutil.ReadAll(resp.Body)
		if err == nil {
			hostname = string(hostBytes)
		}
	}
	return
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
