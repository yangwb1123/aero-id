package errors

import (
	"net/http"

	"google.golang.org/grpc/codes"
)

// AppError is the unified error shape for service/handler boundaries.
// Repositories return raw errors; service code converts them via constructors here.
type AppError struct {
	Code    string
	Message string
	HTTP    int
	GRPC    codes.Code
	Cause   error
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Message + " (cause: " + e.Cause.Error() + ")"
	}
	return e.Code + ": " + e.Message
}

func (e *AppError) Unwrap() error { return e.Cause }

func (e *AppError) Wrap(cause error) *AppError {
	cp := *e
	cp.Cause = cause
	return &cp
}

func BadRequest(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusBadRequest, GRPC: codes.InvalidArgument}
}
func Unauthorized(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusUnauthorized, GRPC: codes.Unauthenticated}
}
func Forbidden(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusForbidden, GRPC: codes.PermissionDenied}
}
func NotFound(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusNotFound, GRPC: codes.NotFound}
}
func Conflict(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusConflict, GRPC: codes.AlreadyExists}
}
func Internal(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusInternalServerError, GRPC: codes.Internal}
}
