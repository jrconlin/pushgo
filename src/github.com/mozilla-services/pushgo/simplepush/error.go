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

// ErrorCode represents a service error code. Error codes are used for logging,
// and may be exposed to clients in the future.
type ErrorCode int

// Service error codes.
const (
	ErrUnknownCommand     ErrorCode = 101
	ErrInvalidCommand     ErrorCode = 102
	ErrNoID               ErrorCode = 103
	ErrInvalidID          ErrorCode = 104
	ErrExistingID         ErrorCode = 105
	ErrNoChannel          ErrorCode = 106
	ErrInvalidChannel     ErrorCode = 107
	ErrExistingChannel    ErrorCode = 108
	ErrNonexistentChannel ErrorCode = 109
	ErrNoKey              ErrorCode = 110
	ErrInvalidKey         ErrorCode = 111
	ErrNoParams           ErrorCode = 112
	ErrInvalidParams      ErrorCode = 113
	ErrNoData             ErrorCode = 114
	ErrNonexistentRecord  ErrorCode = 115
	ErrRecordUpdateFailed ErrorCode = 116
	ErrBadPayload         ErrorCode = 117

	ErrTooManyPings ErrorCode = 201

	ErrBadVersion  ErrorCode = 301
	ErrDataTooLong ErrorCode = 302

	ErrServerError ErrorCode = 999
)

// Error returns a human-readable error message.
func (c ErrorCode) Error() string {
	return codeToError[c].Message
}

// Status returns the HTTP status code associated with the error code.
func (c ErrorCode) Status() int {
	return codeToError[c].StatusCode
}

// ErrToStatus converts an error into an HTTP status code and message. The
// message should not expose implementation details.
func ErrToStatus(err error) (status int, message string) {
	if err == nil {
		return http.StatusOK, ""
	}
	if code, ok := err.(ErrorCode); ok {
		switch status = code.Status(); status {
		case http.StatusServiceUnavailable:
			return status, "Service Unavailable"
		case http.StatusUnauthorized:
			return status, "Invalid Command"
		}
	}
	return http.StatusInternalServerError, "An unexpected error occurred"
}

type serviceError struct {
	StatusCode int
	Message    string
}

var codeToError = map[ErrorCode]serviceError{
	ErrUnknownCommand:     {http.StatusUnauthorized, "Unknown command"},
	ErrInvalidCommand:     {http.StatusUnauthorized, "Invalid Command"},
	ErrNoID:               {http.StatusUnauthorized, "Missing device ID"},
	ErrInvalidID:          {http.StatusServiceUnavailable, "Invalid device ID"},
	ErrExistingID:         {http.StatusServiceUnavailable, "Device ID already assigned"},
	ErrNoChannel:          {http.StatusUnauthorized, "No Channel ID Specified"},
	ErrInvalidChannel:     {http.StatusServiceUnavailable, "Invalid Channel ID Specified"},
	ErrExistingChannel:    {http.StatusServiceUnavailable, "Channel Already Exists"},
	ErrNonexistentChannel: {http.StatusServiceUnavailable, "Nonexistent channel ID"},
	ErrNoKey:              {http.StatusUnauthorized, "No primary key value specified"},
	ErrInvalidKey:         {http.StatusInternalServerError, "Invalid Primary Key Value"},
	ErrNoParams:           {http.StatusUnauthorized, "Missing required fields for command"},
	ErrInvalidParams:      {http.StatusUnauthorized, "An Invalid value was specified"},
	ErrNoData:             {http.StatusServiceUnavailable, "No Data to Store"},
	ErrNonexistentRecord:  {http.StatusServiceUnavailable, "No record found"},
	ErrRecordUpdateFailed: {http.StatusServiceUnavailable, "Error updating channel record"},
	ErrTooManyPings:       {http.StatusUnauthorized, "Client sent too many pings"},
	ErrServerError:        {http.StatusInternalServerError, "An unknown Error occured"},
}
