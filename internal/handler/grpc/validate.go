package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// parseID parses a UUID string and returns an InvalidArgument status error on
// failure. fieldName is included in the error message (e.g. "comment_id") so
// callers get a precise diagnostic without any boilerplate.
func parseID(raw, fieldName string) (idgen.ID, error) {
	id, err := idgen.Parse(raw)
	if err != nil {
		return idgen.ID{}, status.Errorf(codes.InvalidArgument, "invalid %s: %s", fieldName, err.Error())
	}
	return id, nil
}

// requireNonEmpty returns an InvalidArgument error when s is the empty string.
// fieldName is used in the error message (e.g. "content").
func requireNonEmpty(s, fieldName string) error {
	if s == "" {
		return status.Errorf(codes.InvalidArgument, "%s must not be empty", fieldName)
	}
	return nil
}

// actorUID extracts the authenticated uid stored in ctx by the auth interceptor.
// It returns an Unauthenticated status error when the uid is absent, which should
// only happen if a handler is invoked outside the normal interceptor chain.
// Use only when a handler needs to re-read the actor after the interceptor already
// validated it (e.g. to build a service input field directly from context).
func actorUID(ctx context.Context) (string, error) {
	uid, ok := service.ActorFrom(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "unauthenticated: missing actor uid")
	}
	return uid, nil
}
