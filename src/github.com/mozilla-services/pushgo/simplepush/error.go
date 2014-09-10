package simplepush

import (
	"net/http"
)

// ErrorCode represents a service error code. Error codes are used for logging,
// and may be exposed to clients in the future.
type ErrorCode int

// Service error codes.
const (
	CodeUnknownCommand     ErrorCode = 101
	CodeInvalidCommand     ErrorCode = 102
	CodeNoID               ErrorCode = 103
	CodeInvalidID          ErrorCode = 104
	CodeExistingID         ErrorCode = 105
	CodeNoChannel          ErrorCode = 106
	CodeInvalidChannel     ErrorCode = 107
	CodeExistingChannel    ErrorCode = 108
	CodeNonexistentChannel ErrorCode = 109
	CodeNoKey              ErrorCode = 110
	CodeInvalidKey         ErrorCode = 111
	CodeNoParams           ErrorCode = 112
	CodeInvalidParams      ErrorCode = 113
	CodeNoData             ErrorCode = 114
	CodeNonexistentRecord  ErrorCode = 115
	CodeRecordUpdateFailed ErrorCode = 116
	CodeTooManyPings       ErrorCode = 201
	CodeServerError        ErrorCode = 999
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
	CodeUnknownCommand:     {http.StatusUnauthorized, "Unknown command"},
	CodeInvalidCommand:     {http.StatusUnauthorized, "Invalid Command"},
	CodeNoID:               {http.StatusUnauthorized, "Missing device ID"},
	CodeInvalidID:          {http.StatusServiceUnavailable, "Invalid device ID"},
	CodeExistingID:         {http.StatusServiceUnavailable, "Device ID already assigned"},
	CodeNoChannel:          {http.StatusUnauthorized, "No Channel ID Specified"},
	CodeInvalidChannel:     {http.StatusServiceUnavailable, "Invalid Channel ID Specified"},
	CodeExistingChannel:    {http.StatusServiceUnavailable, "Channel Already Exists"},
	CodeNonexistentChannel: {http.StatusServiceUnavailable, "Nonexistent channel ID"},
	CodeNoKey:              {http.StatusUnauthorized, "No primary key value specified"},
	CodeInvalidKey:         {http.StatusInternalServerError, "Invalid Primary Key Value"},
	CodeNoParams:           {http.StatusUnauthorized, "Missing required fields for command"},
	CodeInvalidParams:      {http.StatusUnauthorized, "An Invalid value was specified"},
	CodeNoData:             {http.StatusServiceUnavailable, "No Data to Store"},
	CodeNonexistentRecord:  {http.StatusServiceUnavailable, "No record found"},
	CodeRecordUpdateFailed: {http.StatusServiceUnavailable, "Error updating channel record"},
	CodeTooManyPings:       {http.StatusUnauthorized, "Client sent too many pings"},
	CodeServerError:        {http.StatusInternalServerError, "An unknown Error occured"},
}
