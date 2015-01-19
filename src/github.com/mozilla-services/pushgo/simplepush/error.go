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

// Service errors.
var (
	ErrUnknownCommand     = &ServiceError{101, http.StatusUnauthorized, "Unknown command"}
	ErrInvalidCommand     = &ServiceError{102, http.StatusUnauthorized, "Invalid Command"}
	ErrNoID               = &ServiceError{103, http.StatusUnauthorized, "Missing device ID"}
	ErrInvalidID          = &ServiceError{104, http.StatusServiceUnavailable, "Invalid device ID"}
	ErrExistingID         = &ServiceError{105, http.StatusServiceUnavailable, "Device ID already assigned"}
	ErrNoChannel          = &ServiceError{106, http.StatusUnauthorized, "No Channel ID Specified"}
	ErrInvalidChannel     = &ServiceError{107, http.StatusServiceUnavailable, "Invalid Channel ID Specified"}
	ErrExistingChannel    = &ServiceError{108, http.StatusServiceUnavailable, "Channel Already Exists"}
	ErrNonexistentChannel = &ServiceError{109, http.StatusServiceUnavailable, "Nonexistent channel ID"}
	ErrNoKey              = &ServiceError{110, http.StatusUnauthorized, "No primary key value specified"}
	ErrInvalidKey         = &ServiceError{111, http.StatusInternalServerError, "Invalid Primary Key Value"}
	ErrNoParams           = &ServiceError{112, http.StatusUnauthorized, "Missing required fields for command"}
	ErrInvalidParams      = &ServiceError{113, http.StatusUnauthorized, "An Invalid value was specified"}
	ErrNoData             = &ServiceError{114, http.StatusServiceUnavailable, "No Data to Store"}
	ErrNonexistentRecord  = &ServiceError{115, http.StatusServiceUnavailable, "No record found"}
	ErrRecordUpdateFailed = &ServiceError{116, http.StatusServiceUnavailable, "Error updating channel record"}

	ErrTooManyPings = &ServiceError{201, http.StatusUnauthorized, "Client sent too many pings"}

	ErrBadVersion  = &ServiceError{301, http.StatusBadRequest, "Invalid update version"}
	ErrDataTooLong = &ServiceError{302, http.StatusRequestEntityTooLarge, "Request payload too large"}

	ErrServerError = &ServiceError{999, http.StatusInternalServerError, "Internal server error"}
)

// ErrToStatus converts an error into an HTTP status code and message. The
// message should not expose implementation details.
func ErrToStatus(err error) (status int, message string) {
	if err == nil {
		return http.StatusOK, ""
	}
	if se, ok := err.(*ServiceError); ok {
		switch status = se.Status(); status {
		case http.StatusServiceUnavailable:
			return status, "Service Unavailable"
		case http.StatusUnauthorized:
			return status, "Invalid Command"
		}
	}
	return http.StatusInternalServerError, "An unexpected error occurred"
}
