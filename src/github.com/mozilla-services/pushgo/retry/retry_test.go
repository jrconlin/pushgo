/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package retry

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func newTimedCloser(delay time.Duration) *timedCloser {
	closeChan := make(chan bool)
	timer := time.AfterFunc(delay, func() {
		close(closeChan)
	})
	return &timedCloser{timer, closeChan}
}

type timedCloser struct {
	*time.Timer
	closeChan chan bool
}

func (tc *timedCloser) CloseNotify() <-chan bool {
	return tc.closeChan
}

type retryErr struct {
	attempt   int
	temporary bool
}

func (err *retryErr) Error() string {
	return fmt.Sprintf("%d (Temporary: %t)", err.attempt, err.temporary)
}

func isTemporaryErr(err error) bool {
	re, ok := err.(*retryErr)
	return ok && re.temporary
}

func TestRetry(t *testing.T) {
	defer func() {
		timeAfter = time.After
	}()
	rh := &Helper{
		Backoff:   2,
		Retries:   5,
		Delay:     300 * time.Millisecond,
		MaxDelay:  2 * time.Second,
		MaxJitter: 5 * time.Millisecond,
	}
	durations := []time.Duration{
		300 * time.Millisecond,
		600 * time.Millisecond,
		1200 * time.Millisecond,
		2 * time.Second,
		2 * time.Second,
	}
	rand.Seed(1)
	for i := range durations {
		jitter := rand.Int63n(int64(rh.MaxJitter))
		durations[i] += time.Duration(jitter)
	}
	actualRetries := 0
	timeAfter = func(d time.Duration) <-chan time.Time {
		if actualRetries >= len(durations) {
			t.Fatalf("Maximum retry attempts exceeded: got %d", actualRetries)
		}
		if d != durations[actualRetries] {
			t.Errorf("Mismatched duration: got %s; want %s", d, durations[actualRetries])
		}
		actualRetries++
		c := make(chan time.Time, 1)
		c <- time.Now().Add(d)
		return c
	}
	noErrFunc := func() error { return nil }
	rand.Seed(1)
	retries, err := rh.RetryFunc(noErrFunc)
	if err != nil {
		t.Errorf("RetryFunc returned error for non-error function: %s", err)
	}
	if retries != 0 {
		t.Errorf("Mismatched retry attempt count: got %d", retries)
	}
	lastErr := &retryErr{5, true}
	errFunc := func() (err error) {
		return lastErr
	}
	rand.Seed(1)
	retries, err = rh.RetryFunc(errFunc)
	if retries != actualRetries {
		t.Errorf("Mismatched retry attempt count: got %d; want %d", retries, actualRetries)
	}
	if err != lastErr {
		t.Errorf("Unexpected retry error: got %q; want %q", err, lastErr)
	}
}

func TestRetryAttempt(t *testing.T) {
	defer func() {
		timeAfter = time.After
	}()
	timeAfter = func(d time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Now().Add(d)
		return c
	}
	rh := &Helper{
		Backoff:   3,
		Retries:   5,
		Delay:     150 * time.Millisecond,
		MaxDelay:  5 * time.Second,
		MaxJitter: 300 * time.Millisecond,
	}
	expectedDelays := []time.Duration{
		150 * time.Millisecond,
		450 * time.Millisecond,
		1350 * time.Millisecond,
		4050 * time.Millisecond,
		5 * time.Second,
	}
	// Add expected jitter.
	rand.Seed(1)
	for i := range expectedDelays {
		expectedDelays[i] += time.Duration(rand.Int63n(int64(rh.MaxJitter)))
	}
	rand.Seed(1)
	for i, expected := range expectedDelays {
		attempt := i + 1
		actual, ok := rh.RetryAttempt(attempt, 1, nil)
		if !ok {
			t.Errorf("Retry attempt %d failed: %s", attempt, actual)
		}
		if actual != expected {
			t.Errorf("Wrong delay for attempt %d: got %d; want %d", attempt, actual, expected)
		}
	}
	// Exceeds maximum retry attempts; should return immediately.
	delay, ok := rh.RetryAttempt(6, 1, nil)
	if ok {
		t.Errorf("Retry attempt 6 succeeded")
	}
	if delay != 0 {
		t.Errorf("Got delay %s for retry attempt 6", delay)
	}
}

func TestCanRetry(t *testing.T) {
	defer func() {
		timeAfter = time.After
	}()
	timeAfter = func(d time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Now().Add(d)
		return c
	}
	expected := 20 * time.Millisecond
	rh := &Helper{
		CanRetry: isTemporaryErr,
		Backoff:  1,
		Retries:  5,
		Delay:    expected,
		MaxDelay: 1 * time.Second,
	}
	attempt := 0
	errFunc := func() (err error) {
		attempt++
		return &retryErr{attempt, attempt != 3}
	}
	retries, err := rh.RetryFunc(errFunc)
	if isTemporaryErr(err) {
		t.Errorf("Want non-temporary error; got %s", err)
	}
	if retries != 2 {
		t.Errorf("Wrong retry count: got %d; want 2", retries)
	}
	actual, ok := rh.RetryAttempt(1, 1, &retryErr{1, true})
	if !ok {
		t.Errorf("Retry temporary error failed: %s", actual)
	}
	if actual != expected {
		t.Errorf("Wrong delay for temporary error: got %d; want %d", actual, expected)
	}
	actual, ok = rh.RetryAttempt(1, 1, &retryErr{1, false})
	if ok {
		t.Errorf("Retry non-temporary error succeeded")
	}
	if actual != 0 {
		t.Errorf("Got delay %s for non-temporary error", actual)
	}
}

func TestCloseNotifier(t *testing.T) {
	defer func() {
		timeAfter = time.After
	}()
	retryDelay := 50 * time.Millisecond
	timeAfter = func(d time.Duration) <-chan time.Time {
		if d != retryDelay {
			t.Fatalf("Wrong initial delay: got %s; want %s", d, retryDelay)
		}
		return nil
	}
	rh := &Helper{
		Backoff:   2,
		Retries:   5,
		Delay:     retryDelay,
		MaxDelay:  1 * time.Second,
		MaxJitter: 0,
	}
	rh.CloseNotifier = newTimedCloser(100 * time.Millisecond)
	lastErr := &retryErr{0, true}
	errFunc := func() (err error) {
		return lastErr
	}
	retries, err := rh.RetryFunc(errFunc)
	if err != lastErr {
		t.Errorf("Wrong retry error: got %s; want %s", err, lastErr)
	}
	if retries != 0 {
		t.Errorf("Wrong retry count: got %d; want 0", retries)
	}
	rh.CloseNotifier = newTimedCloser(100 * time.Millisecond)
	delay, ok := rh.RetryAttempt(1, 1, lastErr)
	if ok {
		t.Errorf("Retry attempt succeeded")
	}
	if delay != retryDelay {
		t.Errorf("Wrong retry delay: got %s; want %s", delay, retryDelay)
	}
}
