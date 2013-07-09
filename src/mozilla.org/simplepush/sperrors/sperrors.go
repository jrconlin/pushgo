package sperrors

import (
	"errors"
	"net/http"
)

var ChannelExistsError = errors.New("Channel Already Exists")
var InvalidChannelError = errors.New("No Channel ID Specified")
var InvalidCommandError = errors.New("Invalid Command")
var InvalidDataError = errors.New("An Invalid value was specified")
var InvalidPrimaryKeyError = errors.New("Invalid Primary Key Value")
var MissingDataError = errors.New("Missing required fields for command")
var NoChannelError = errors.New("No Channel ID Specified")
var NoDataToStoreError = errors.New("No Data to Store")
var NoRecordWarning = errors.New("No record found")
var ServerError = errors.New("An unknown Error occured")
var UnknownCommandError = errors.New("Unknown command")

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
			UnknownCommandError:
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
