/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

var (
	ErrNoElastiCache      StorageError = "ElastiCache returned no endpoints"
	ErrElastiCacheTimeout StorageError = "ElastiCache query timed out"
)

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
