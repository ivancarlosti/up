package services

import (
	"errors"
	"net/http"

	"github.com/ivancarlosti/up/internal/i18n"
)

// APIError is the error type returned by every service. Handlers translate it
// into a JSON body containing the stable "code" that the frontend translates.
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// NewError builds an APIError.
func NewError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// Common errors ------------------------------------------------------------

// ErrNotFound is returned when an entity does not exist.
func ErrNotFound(code, message string) *APIError {
	return NewError(http.StatusNotFound, code, message)
}

// ErrBadRequest is returned for invalid payloads.
func ErrBadRequest(code, message string) *APIError {
	return NewError(http.StatusBadRequest, code, message)
}

// ErrForbidden is returned when the caller is not allowed to do something.
func ErrForbidden(code, message string) *APIError {
	return NewError(http.StatusForbidden, code, message)
}

// ErrConflict is returned for duplicates.
func ErrConflict(code, message string) *APIError {
	return NewError(http.StatusConflict, code, message)
}

// ErrInternal wraps an unexpected failure.
//
// A nested *APIError keeps its own status and code: a validation performed
// inside a transaction must reach the caller as a 400 with its stable code
// instead of being flattened into a 500 "ERR_INTERNAL" (which is how a group_ids
// validation used to look from the outside).
func ErrInternal(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return NewError(http.StatusInternalServerError, i18n.CodeInternal, err.Error())
}

// AsAPIError converts any error into an *APIError, defaulting to 500.
func AsAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return ErrInternal(err)
}
