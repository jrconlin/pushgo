package sperrors

import (
	"errors"
	"net/http"
)

var (
	ChannelExistsError     = errors.New("Channel Already Exists")
	InvalidChannelError    = errors.New("No Channel ID Specified")
	InvalidCommandError    = errors.New("Invalid Command")
	InvalidDataError       = errors.New("An Invalid value was specified")
	InvalidPrimaryKeyError = errors.New("Invalid Primary Key Value")
	MissingDataError       = errors.New("Missing required fields for command")
	NoChannelError         = errors.New("No Channel ID Specified")
	NoDataToStoreError     = errors.New("No Data to Store")
	NoRecordWarning        = errors.New("No record found")
	ServerError            = errors.New("An unknown Error occured")
	UnknownCommandError    = errors.New("Unknown command")
	TooManyPingsError      = errors.New("Client sent too many pings")
)

// Transform an error into a HTTP status int and message suitable for
// printing to the web
func ErrToStatus(err error) (status int, message string) {
	status = 200
	if err != nil {
		switch err {
		case ChannelExistsError,
			NoDataToStoreError,
			InvalidChannelError,
			NoRecordWarning:
			status = http.StatusServiceUnavailable
			message = "Service Unavailable"
		case MissingDataError,
			NoChannelError,
			InvalidCommandError,
			InvalidDataError,
			UnknownCommandError,
			TooManyPingsError:
			status = 401
			message = "Invalid Command"
		default:
			status = 500
			message = "An unexpected error occurred"
		}
	}
	return status, message
}

// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
