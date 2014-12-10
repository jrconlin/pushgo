/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package retry

import (
	"fmt"
	"math/rand"
	"time"
)

type StatusError int

func (err StatusError) Error() string {
	return fmt.Sprintf("Unexpected status code: %d", err)
}

type CloseNotifier interface {
	CloseNotify() <-chan bool
}

type Config struct {
	// Retries is the number of times to retry failed requests.
	Retries int

	// Delay is the initial amount of time to wait before retrying requests.
	Delay string

	// MaxDelay is the maximum amount of time to wait before retrying requests.
	MaxDelay string `toml:"max_delay" env:"max_delay"`

	// MaxJitter is the maximum per-retry randomized delay.
	MaxJitter string `toml:"max_jitter" env:"max_jitter"`
}

func (conf *Config) NewHelper() (r *Helper, err error) {
	delay, err := time.ParseDuration(conf.Delay)
	if err != nil {
		return nil, fmt.Errorf("Invalid retry delay (%s): %s",
			conf.Delay, err)
	}
	maxDelay, err := time.ParseDuration(conf.MaxDelay)
	if err != nil {
		return nil, fmt.Errorf("Invalid maximum delay (%s): %s",
			conf.MaxDelay, err)
	}
	maxJitter, err := time.ParseDuration(conf.MaxJitter)
	if err != nil {
		return nil, fmt.Errorf("Invalid jitter (%s): %s",
			conf.MaxJitter, err)
	}
	r = &Helper{
		Backoff:   2,
		Retries:   conf.Retries,
		Delay:     delay,
		MaxDelay:  maxDelay,
		MaxJitter: maxJitter,
	}
	return r, nil
}

type Helper struct {
	// CloseNotifier can be used to cancel all in-progress retries.
	CloseNotifier

	// CanRetry indicates whether an error is temporary. Operations that return
	// non-temporary errors will not be retried.
	CanRetry func(error) bool

	Backoff   int           // Backoff multiplier.
	Retries   int           // Maximum retry attempts.
	Delay     time.Duration // Initial retry delay.
	MaxDelay  time.Duration // Maximum retry delay.
	MaxJitter time.Duration // Maximum additional randomized delay.
}

func (r *Helper) closeNotify() <-chan bool {
	if r.CloseNotifier != nil {
		return r.CloseNotifier.CloseNotify()
	}
	return nil
}

func (r *Helper) canRetry(err error) bool {
	if r.CanRetry != nil {
		return r.CanRetry(err)
	}
	return true
}

// RetryFunc calls the function f until it returns a non-temporary error
// or exceeds the maximum number of retry attempts.
func (r *Helper) RetryFunc(f func() error) (retries int, err error) {
	retryDelay := r.Delay
	for ok := true; ok; {
		if err = f(); err != nil {
			if !r.canRetry(err) || retries >= r.Retries {
				break
			}
			delay := r.withJitter(retryDelay)
			select {
			case <-r.closeNotify():
				ok = false
			case <-time.After(delay):
				retries++
				retryDelay *= time.Duration(r.Backoff)
			}
			continue
		}
		break
	}
	return
}

// RetryAttempt indicates whether an operation that returned err can be
// retried. It's useful for operations that already keep track of the attempt
// count, such as the CheckRetry mechanism used by go-etcd. The multiplier
// controls the number of attempts per node.
func (r *Helper) RetryAttempt(attempt, multiplier int, err error) (
	delay time.Duration, ok bool) {

	if attempt > r.Retries*multiplier {
		return 0, false
	}
	if !r.canRetry(err) {
		return 0, false
	}
	retryDelay := r.Delay * time.Duration(pow(r.Backoff, attempt-1))
	delay = r.withJitter(retryDelay)
	select {
	case <-r.closeNotify():
		return delay, false
	case <-time.After(delay):
	}
	return delay, true
}

func (r *Helper) withJitter(delay time.Duration) time.Duration {
	if delay > r.MaxDelay {
		delay = r.MaxDelay
	}
	var jitter int64
	if r.MaxJitter > 0 {
		jitter = rand.Int63n(int64(r.MaxJitter))
	}
	return delay + time.Duration(jitter)
}

// Binary powering algorithm for integers. Go's math.Pow only works for
// floats. See https://groups.google.com/d/topic/golang-nuts/PnLnr4bc9Wo/discussion.
func pow(x, y int) (r int) {
	r = 1
	for y > 0 {
		if y%2 != 0 {
			r *= x
		}
		y >>= 1
		x *= x
	}
	return
}
