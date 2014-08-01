package client

import (
	"net"
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
	host        net.Addr
	message     string
	statusCode  int
}

func (err *ServerError) Error() string {
	result := err.messageType + " (" + strconv.Itoa(err.statusCode)
	host := err.Host()
	if len(host) > 0 {
		result += "; " + host
	}
	return result + "): " + err.message
}

func (err *ServerError) Status() int { return err.statusCode }

func (err *ServerError) Host() string {
	if err.host == nil {
		return "*"
	}
	return err.host.String()
}

type IncompleteError struct {
	messageType string
	host        net.Addr
	field       string
}

func (err *IncompleteError) Error() string {
	result := err.messageType
	host := err.Host()
	if len(host) > 0 {
		result += " (" + host + ")"
	}
	return result + ": Missing field `" + err.field + "`."
}

func (err *IncompleteError) Status() int { return -1 }

func (err *IncompleteError) Host() string {
	if err.host == nil {
		return "*"
	}
	return err.host.String()
}

type RedirectError struct {
	url        string
	statusCode int
}

func (err *RedirectError) Error() string {
	return strconv.Itoa(err.statusCode) + " redirect to `" + err.url + "`."
}

func (err *RedirectError) Status() int  { return err.statusCode }
func (err *RedirectError) Host() string { return err.url }
