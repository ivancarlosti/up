// Package api contains the tiny helpers shared by the HTTP handlers and the
// middlewares to write consistent JSON responses.
//
// Every error body follows the same shape:
//
//	{"code":"ERR_MONITOR_NOT_FOUND","message":"monitor 42 does not exist"}
//
// The frontend translates the code with vue-i18n, so the server never has to
// know about languages.
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/services"
)

// ErrorBody is the JSON payload of every failed request.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError sends an error response.
func WriteError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorBody{Code: code, Message: message})
}

// WriteServiceError maps a service error into the JSON contract.
func WriteServiceError(c *gin.Context, err error) {
	var apiErr *services.APIError
	if errors.As(err, &apiErr) {
		WriteError(c, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	WriteError(c, http.StatusInternalServerError, i18n.CodeInternal, err.Error())
}

// OK sends a 200 response.
func OK(c *gin.Context, data any) { c.JSON(http.StatusOK, data) }

// Created sends a 201 response.
func Created(c *gin.Context, data any) { c.JSON(http.StatusCreated, data) }

// NoContent sends a 204 response.
func NoContent(c *gin.Context) { c.Status(http.StatusNoContent) }

// BadRequest reports an invalid payload.
func BadRequest(c *gin.Context, message string) {
	WriteError(c, http.StatusBadRequest, i18n.CodeInvalidPayload, message)
}

// Unauthorized reports a missing or invalid session.
func Unauthorized(c *gin.Context, message string) {
	WriteError(c, http.StatusUnauthorized, i18n.CodeAuthRequired, message)
}

// Forbidden reports an authorization failure.
func Forbidden(c *gin.Context, code, message string) {
	WriteError(c, http.StatusForbidden, code, message)
}

// NotFound reports a missing resource or route.
func NotFound(c *gin.Context, code, message string) {
	WriteError(c, http.StatusNotFound, code, message)
}
