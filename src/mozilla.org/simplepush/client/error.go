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
	message string
}

func (err *ClientError) Error() string { return err.message }
func (err *ClientError) Status() int   { return -1 }
func (err *ClientError) Host() string  { return "*" }

type ServerError struct {
	messageType string
	host        string
	message     string
	statusCode  int
}

func (err *ServerError) Error() string {
	result := strconv.Itoa(err.statusCode)
	host := err.Host()
	if len(host) > 0 {
		result += "; " + host
	}
	return fmt.Sprintf("%s (%s): %s", err.messageType, result, err.message)
}

func (err *ServerError) Status() int { return err.statusCode }

func (err *ServerError) Host() string {
	if len(err.host) == 0 {
		return "*"
	}
	return err.host
}

type IncompleteError struct {
	messageType string
	host        string
	field       string
}

func (err *IncompleteError) Error() string {
	result := err.messageType
	host := err.Host()
	if len(host) > 0 {
		result += " (" + host + ")"
	}
	return fmt.Sprintf("%s: Missing field `%s`.", result, err.field)
}

func (err *IncompleteError) Status() int { return -1 }

func (err *IncompleteError) Host() string {
	if len(err.host) == 0 {
		return "*"
	}
	return err.host
}

type RedirectError struct {
	url        string
	statusCode int
}

func (err *RedirectError) Error() string {
	return fmt.Sprintf("%d redirect to `%s`.", err.statusCode, err.url)
}

func (err *RedirectError) Status() int  { return err.statusCode }
func (err *RedirectError) Host() string { return err.url }
