package sperrors

import (
	"errors"
	"net/http"
)

var ChannelExistsError = errors.New("Channel Already Exists")
var InvalidChannelError = errors.New("No Channel ID Specified")
var InvalidCommandError = errors.New("Invalid command")
var InvalidPrimaryKeyError = errors.New("Invalid Primary Key Value")
var MissingDataError = errors.New("Missing required fields for command")
var NoChannelError = errors.New("No Channel ID Specified")
var NoDataToStoreError = errors.New("No Data to Store")
var NoRecordWarning = errors.New("No record found")
var ServerError = errors.New("An unknown Error occured")
var UnknownCommandError = errors.New("Unknown command")

func ErrToStatus(err error) (status int) {
	status = 200
	if err != nil {
		switch err {
		case ChannelExistsError,
            InvalidChannelError,
			NoDataToStoreError,
            NoChannelError,
			NoRecordWarning:
			status = http.StatusServiceUnavailable
		case UnknownCommandError,
             MissingDataError,
             InvalidCommandError:
			status = 401
		default:
			status = 500
		}
	}
	return status
}
// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
