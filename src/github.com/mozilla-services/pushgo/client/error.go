/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package client

import (
	"fmt"
	"strconv"
)

type Error interface {
	error
	Status() int
	Host() string
}

type ClientError struct {
	Message string
}

func (err *ClientError) Error() string { return err.Message }
func (err *ClientError) Status() int   { return -1 }
func (err *ClientError) Host() string  { return "*" }

type ServerError struct {
	MessageType string
	Origin      string
	Message     string
	StatusCode  int
}

func (err *ServerError) Error() string {
	result := strconv.Itoa(err.StatusCode)
	host := err.Host()
	if len(host) > 0 {
		result += "; " + host
	}
	return fmt.Sprintf("%s (%s): %s", err.MessageType, result, err.Message)
}

func (err *ServerError) Status() int { return err.StatusCode }

func (err *ServerError) Host() string {
	if len(err.Origin) == 0 {
		return "*"
	}
	return err.Origin
}

type IncompleteError struct {
	MessageType string
	Origin      string
	Field       string
}

func (err *IncompleteError) Error() string {
	result := err.MessageType
	host := err.Host()
	if len(host) > 0 {
		result += " (" + host + ")"
	}
	return fmt.Sprintf("%s: Missing field `%s`.", result, err.Field)
}

func (err *IncompleteError) Status() int { return -1 }

func (err *IncompleteError) Host() string {
	if len(err.Origin) == 0 {
		return "*"
	}
	return err.Origin
}

type RedirectError struct {
	URL        string
	StatusCode int
}

func (err *RedirectError) Error() string {
	return fmt.Sprintf("%d redirect to `%s`.", err.StatusCode, err.URL)
}

func (err *RedirectError) Status() int  { return err.StatusCode }
func (err *RedirectError) Host() string { return err.URL }
