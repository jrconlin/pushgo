/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package simplepush

import (
	"errors"
	"sync"
	"testing"
)

type one int

func (o *one) Increment() error {
	*o++
	return nil
}

func run(t *testing.T, attempt int, once *Once, o *one, wg *sync.WaitGroup) {
	defer wg.Done()
	once.Do(o.Increment)
	if v := *o; v != 1 {
		t.Errorf("On test %d, got %d; want 1", attempt, v)
	}
}

func TestOnce(t *testing.T) {
	var (
		once Once
		o    one
		wg   sync.WaitGroup
	)
	attempts := 10
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go run(t, i, &once, &o, &wg)
	}
	wg.Wait()
	if o != 1 {
		t.Errorf("Increment called %d times; want 1", o)
	}
}

func TestOnceIsDone(t *testing.T) {
	i := 0
	expectedErr := errors.New("something went wrong")
	f := func() error {
		i++
		return expectedErr
	}
	var once Once
	if err := once.Do(f); err != expectedErr {
		t.Errorf("Got %#v for first call; want %#v", err, expectedErr)
	}
	if v := once.IsDone(); !v {
		t.Errorf("IsDone returned %s after first call", v)
	}
	if err := once.Do(f); err != nil {
		t.Errorf("Got %#v for second call; want nil", err)
	}
	if i != 1 {
		t.Errorf("f called %d times; want 1", i)
	}
}

func TestOncePanic(t *testing.T) {
	var once Once
	f := func() error {
		panic("omg, everything is exploding")
		return errors.New("f called multiple times")
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("f did not panic")
			}
		}()
		once.Do(f)
	}()
	if err := once.Do(f); err != nil {
		t.Error(err)
	}
}
