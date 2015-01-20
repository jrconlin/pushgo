package simplepush

import (
	"net/http"
	"strings"
)

type MultipleError []error

func (err MultipleError) Error() string {
	messages := make([]string, len(err))
	for i, e := range err {
		messages[i] = e.Error()
	}
	return strings.Join(messages, ", ")
}

// ServiceError represents a service error. Each error corresponds to a stable
// error code, HTTP status, and message. This matches FxA error conventions.
type ServiceError struct {
	ErrorCode  int    `json:"errno"` // Error code; may be exposed to clients.
	StatusCode int    `json:"code"`  // HTTP status code.
	Message    string `json:"message"`
}

// Error returns a human-readable error message.
func (err *ServiceError) Error() string {
	return err.Message
}

// Status returns the HTTP status code associated with the error.
func (err *ServiceError) Status() int {
	return err.StatusCode
}

// 100-class errors indicate bad client input (e.g., invalid JSON, missing or
// invalid command fields).
var (
	ErrInvalidHeader   = &ServiceError{101, http.StatusUnauthorized, "Invalid command header"}
	ErrUnsupportedType = &ServiceError{102, http.StatusUnauthorized, "Unsupported command type"}
	ErrNoID            = &ServiceError{103, http.StatusUnauthorized, "Missing device ID"}
	ErrInvalidID       = &ServiceError{104, http.StatusServiceUnavailable, "Invalid device ID"}
	ErrNoChannel       = &ServiceError{105, http.StatusUnauthorized, "Missing channel ID"}
	ErrInvalidChannel  = &ServiceError{106, http.StatusServiceUnavailable, "Invalid channel ID"}
	ErrNoParams        = &ServiceError{107, http.StatusUnauthorized, "Missing one or more required command fields"}
	ErrInvalidParams   = &ServiceError{108, http.StatusUnauthorized, "Command contains one or more invalid fields"}
)

// 200-class errors indicate bad client behavior (e.g., sending a command
// without completing the opening handshake, sending too many pings, etc).
var (
	ErrNoHandshake        = &ServiceError{201, http.StatusUnauthorized, "Command requires handshake"}
	ErrExistingID         = &ServiceError{202, http.StatusServiceUnavailable, "Device ID already assigned to this client"}
	ErrTooManyPings       = &ServiceError{203, http.StatusUnauthorized, "Client sent too many pings"}
	ErrNonexistentChannel = &ServiceError{204, http.StatusServiceUnavailable, "The specified channel ID does not exist"}
)

// 300-class errors indicate bad app server input (e.g., invalid update
// version, oversized payload).
var (
	ErrBadVersion  = &ServiceError{301, http.StatusBadRequest, "Invalid update version"}
	ErrDataTooLong = &ServiceError{302, http.StatusRequestEntityTooLarge, "Request payload too large"}
)

// 400-class errors indicate problems with upstream services (e.g.,
// memcached, etcd).
var (
	ErrInvalidKey         = &ServiceError{401, http.StatusInternalServerError, "Invalid channel primary key"}
	ErrRecordUpdateFailed = &ServiceError{402, http.StatusServiceUnavailable, "Error updating channel record"}
)

// ErrServerError is a catch-all service error.
var ErrServerError = &ServiceError{999, http.StatusInternalServerError, "Internal server error"}

// ErrToStatus converts an error into an HTTP status code and message.
func ErrToStatus(err error) (status int, message string) {
	if err == nil {
		return http.StatusOK, ""
	}
	serviceErr, ok := err.(*ServiceError)
	if !ok {
		serviceErr = ErrServerError
	}
	return serviceErr.Status(), serviceErr.Error()
}
