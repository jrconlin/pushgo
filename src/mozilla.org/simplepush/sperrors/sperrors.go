package sperrors

import (
	"errors"
	"net/http"
)

var InvalidPrimaryKeyError = errors.New("Invalid Primary Key Value")
var NoDataToStoreError = errors.New("No Data to Store")
var NoChannelError = errors.New("No Channel ID Specified")
var ChannelExistsError = errors.New("Channel Already Exists")
var NoRecordWarning = errors.New("No record found")
var UnknownCommandError = errors.New("Unknown command")
var ServerError = errors.New("An unknown Error occured")

func ErrToStatus(err error) (status int) {
	status = 200
	if err != nil {
		switch err {
		case ChannelExistsError,
			NoDataToStoreError,
			NoRecordWarning:
			status = http.StatusServiceUnavailable
		case UnknownCommandError:
			status = 401
		default:
			status = 500
		}
	}
	return status
}
// o4fs
// vim: set tabstab=4 softtabstop=4 shiftwidth=4 noexpandtab
