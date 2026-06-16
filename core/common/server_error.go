package common

import (
	"encoding/json"
	"time"
)

type ServerError struct {
	Code       int
	Message    string
	Data       map[string]string
	StatusCode int
	Created    time.Time
}

func (e ServerError) Error() string {
	jsonObject := J{"error": GATEWAY_ERROR_NAMES[e.Code],
		"Message": SERVER_ERROR_MESSAGES[e.Code],
		"Data":    e.Data,
		"Created": e.Created.Format("2006-01-02 15:04:05")}

	answerString, _ := json.Marshal(jsonObject)
	return string(answerString[:])
}

func NewServerError(code int, data map[string]string) *ServerError {
	return &ServerError{Code: code, Message: SERVER_ERROR_MESSAGES[code], Data: data, Created: time.Now()}
}

// error codes
const ( // iota is reset to 0
	SERVER_ERROR_CONNECTION     = iota // ERROR_NODE_LIMIT == 0
	SERVER_ERROR_FULL           = iota //
	SERVER_ERROR_UNEXPECTED     = iota //
	SERVER_ERROR_BAD_REQUEST    = iota //
	SERVER_ERROR_BAD_SESSION_ID = iota //
	SERVER_ERROR_NOT_FOUND      = iota //
	SERVER_ERROR_OUTPUTS_FULL   = iota //
	SERVER_ERROR_NODE_MISSING   = iota //
	SERVER_ERROR_READ_FAILURE   = iota //
	SERVER_ERROR_WRITE_FAILURE  = iota //
	SERVER_ERROR_DNS_FAILURE    = iota //
	SERVER_ERROR_DUPLICATE      = iota //
)

var GATEWAY_ERROR_NAMES = map[int]string{
	SERVER_ERROR_CONNECTION:     "SERVER_ERROR_CONNECTION",
	SERVER_ERROR_FULL:           "SERVER_ERROR_FULL",
	SERVER_ERROR_UNEXPECTED:     "SERVER_ERROR_UNEXPECTED",
	SERVER_ERROR_BAD_REQUEST:    "SERVER_ERROR_BAD_REQUEST",
	SERVER_ERROR_BAD_SESSION_ID: "SERVER_ERROR_BAD_SESSION_ID",
	SERVER_ERROR_NOT_FOUND:      "SERVER_ERROR_NOT_FOUND",
	SERVER_ERROR_OUTPUTS_FULL:   "SERVER_ERROR_OUTPUTS_FULL",
	SERVER_ERROR_NODE_MISSING:   "SERVER_ERROR_NODE_MISSING",
	SERVER_ERROR_READ_FAILURE:   "SERVER_ERROR_READ_FAILURE",
	SERVER_ERROR_WRITE_FAILURE:  "SERVER_ERROR_WRITE_FAILURE",
	SERVER_ERROR_DNS_FAILURE:    "SERVER_ERROR_DNS_FAILURE",
	SERVER_ERROR_DUPLICATE:      "SERVER_ERROR_DUPLICATE",
}

var SERVER_ERROR_MESSAGES = map[int]string{
	SERVER_ERROR_CONNECTION:     "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_FULL:           "This Panaudia space is full. Please try again later.",
	SERVER_ERROR_UNEXPECTED:     "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_BAD_REQUEST:    "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_BAD_SESSION_ID: "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_NOT_FOUND:      "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_OUTPUTS_FULL:   "This Panaudia space is full. Please try again later.",
	SERVER_ERROR_NODE_MISSING:   "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_READ_FAILURE:   "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_WRITE_FAILURE:  "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_DNS_FAILURE:    "Something went wrong connecting to the Panaudia space. Please try again.",
	SERVER_ERROR_DUPLICATE:      "Duplicate ID",
}
