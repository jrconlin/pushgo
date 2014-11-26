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
		Retries:   conf.Retries,
		Delay:     delay,
		MaxDelay:  maxDelay,
		MaxJitter: maxJitter,
	}
	return r, nil
}

type Helper struct {
	CloseNotifier
	Filter    func(error) bool
	Retries   int
	Delay     time.Duration
	MaxDelay  time.Duration
	MaxJitter time.Duration
}

func (r *Helper) closeNotify() <-chan bool {
	if cn := r.CloseNotifier; cn != nil {
		return cn.CloseNotify()
	}
	return nil
}

func (r *Helper) filter(err error) bool {
	if filter := r.Filter; filter != nil {
		return filter(err)
	}
	return true
}

func (r *Helper) RetryFunc(f func() error) (retries int, err error) {
	retryDelay := r.Delay
	for ok := true; ok; {
		if err = f(); err != nil {
			if !r.filter(err) || retries >= r.Retries {
				break
			}
			retries++
			delay := r.withJitter(retryDelay)
			select {
			case <-r.closeNotify():
				ok = false
			case <-time.After(delay):
				retryDelay *= 2
			}
			continue
		}
		break
	}
	return
}

func (r *Helper) RetryAttempt(attempt, multiplier int, err error) bool {
	if attempt > r.Retries*multiplier {
		return false
	}
	if !r.filter(err) {
		return true
	}
	var retryDelay time.Duration
	if attempt > 1 {
		retryDelay = time.Duration(int64(r.Delay) * (1 << uint(attempt-1)))
	} else {
		retryDelay = r.Delay
	}
	delay := r.withJitter(retryDelay)
	select {
	case <-r.closeNotify():
		return false
	case <-time.After(delay):
	}
	return true
}

func (r *Helper) withJitter(delay time.Duration) time.Duration {
	if delay > r.MaxDelay {
		delay = r.MaxDelay
	}
	jitter := time.Duration(rand.Int63n(int64(r.MaxJitter)))
	return delay + jitter
}
