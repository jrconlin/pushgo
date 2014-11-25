/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"fmt"
	"time"

	"github.com/coreos/go-etcd/etcd"

	"github.com/mozilla-services/pushgo/id"
	"github.com/mozilla-services/pushgo/retry"
)

// IsEtcdKeyExist indicates whether the given error reports that an etcd key
// already exists.
func IsEtcdKeyExist(err error) bool {
	clientErr, ok := err.(*etcd.EtcdError)
	return ok && clientErr.ErrorCode == 105
}

// EtcdWalk walks an etcd directory tree rooted at root, calling walkFn for
// each etcd file node. If walkFn returns an error for a given node, the
// remaining siblings will not be traversed.
func EtcdWalk(root *etcd.Node, walkFn func(*etcd.Node) error) (err error) {
	if len(root.Nodes) == 0 {
		return walkFn(root)
	}
	for _, node := range root.Nodes {
		if err = EtcdWalk(node, walkFn); err != nil {
			return err
		}
	}
	return nil
}

// IsEtcdTemporary indicates whether the given error is a temporary
// etcd error.
func IsEtcdTemporary(err error) bool {
	switch typ := err.(type) {
	case *etcd.EtcdError:
		// Raft (300-class) and internal (400-class) errors are temporary.
		return typ.ErrorCode >= 300 && typ.ErrorCode < 500
	case retry.StatusError:
		return typ >= 500
	}
	return false
}

// IsEtcdHealthy indicates whether etcd can respond to requests.
func IsEtcdHealthy(client *etcd.Client) (ok bool, err error) {
	fakeID, err := id.Generate()
	if err != nil {
		return false, fmt.Errorf("Error generating health check key: %s", err)
	}
	key, expected := "status_"+fakeID, "test"
	if _, err = client.Set(key, expected, uint64(3*time.Second)); err != nil {
		return false, fmt.Errorf("Error storing health check key %#v: %s",
			key, err)
	}
	resp, err := client.Get(key, false, false)
	if err != nil {
		return false, fmt.Errorf("Error fetching health check key %#v: %s",
			key, err)
	}
	if resp.Node.Value != expected {
		return false, fmt.Errorf(
			"Unexpected value for health check key %#v: got %s; want %s",
			key, resp.Node.Value, expected)
	}
	return true, nil
}
