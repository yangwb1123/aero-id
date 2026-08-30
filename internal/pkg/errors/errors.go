package errors

import (
	"context"
	stdErrors "errors"
	"net/http"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// HTTPStatus exposes the mapped HTTP status for transient-vs-deterministic
// classification (e.g. broker redelivery policy).
func (e *AppError) HTTPStatus() int { return e.HTTP }

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
func Unavailable(code, msg string) *AppError {
	return &AppError{Code: code, Message: msg, HTTP: http.StatusServiceUnavailable, GRPC: codes.Unavailable}
}

// RemoteRejected describes a response which proves that the remote cannot
// accept this event. The supplied status is retained only for safe 4xx values;
// malformed or non-client statuses are rendered as a generic bad request.
func RemoteRejected(statusCode int) *AppError {
	if statusCode < http.StatusBadRequest || statusCode >= http.StatusInternalServerError {
		statusCode = http.StatusBadRequest
	}
	return &AppError{Code: "event.remote_rejected", Message: "remote event ingest rejected the event",
		HTTP: statusCode, GRPC: codes.InvalidArgument}
}

// RemoteUnavailable is deliberately cause-free: transport errors frequently
// contain the destination URL and must never cross a service boundary.
func RemoteUnavailable() *AppError {
	return &AppError{Code: "event.remote_unavailable", Message: "remote event ingest is unavailable",
		HTTP: http.StatusServiceUnavailable, GRPC: codes.Unavailable}
}

// GRPCError converts a service error to a gRPC status error. An AppError keeps
// its safe message and carries its stable dotted code as an ErrorInfo detail;
// deadline and cancellation errors retain their canonical gRPC status. Other
// errors become Internal so dependency details are never exposed.
func GRPCError(err error) error {
	if err == nil {
		return nil
	}
	var ae *AppError
	if stdErrors.As(err, &ae) {
		st, detailErr := status.New(ae.GRPC, ae.Message).WithDetails(
			&errdetails.ErrorInfo{Reason: ae.Code, Domain: "aero-id"},
		)
		if detailErr != nil {
			return status.New(ae.GRPC, ae.Message).Err()
		}
		return st.Err()
	}
	switch {
	case stdErrors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	case stdErrors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	}
	return status.Error(codes.Internal, "internal server error")
}
