package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/service"
)

// TestParseID_valid checks that a well-formed UUID string is parsed without error.
func TestParseID_valid(t *testing.T) {
	const raw = "018f4c1a-2b3d-7e4f-8a5b-6c7d8e9f0a1b"
	id, err := parseID(raw, "target_id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.String() != raw {
		t.Fatalf("got %s, want %s", id.String(), raw)
	}
}

// TestParseID_empty checks that an empty string yields an InvalidArgument status.
func TestParseID_empty(t *testing.T) {
	_, err := parseID("", "comment_id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", st.Code())
	}
}

// TestParseID_malformed checks that a non-UUID string yields an InvalidArgument status.
func TestParseID_malformed(t *testing.T) {
	_, err := parseID("not-a-uuid", "file_id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", st.Code())
	}
}

// TestRequireNonEmpty_empty checks that an empty value returns InvalidArgument.
func TestRequireNonEmpty_empty(t *testing.T) {
	err := requireNonEmpty("", "content")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", st.Code())
	}
}

// TestRequireNonEmpty_nonempty checks that a non-empty value returns nil.
func TestRequireNonEmpty_nonempty(t *testing.T) {
	err := requireNonEmpty("hello", "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestActorUID_present checks that a context populated by service.WithActor returns the uid.
func TestActorUID_present(t *testing.T) {
	const wantUID = "user-abc-123"
	ctx := service.WithActor(context.Background(), wantUID)
	uid, err := actorUID(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != wantUID {
		t.Fatalf("got %q, want %q", uid, wantUID)
	}
}

// TestActorUID_missing checks that a plain context.Background() returns Unauthenticated.
func TestActorUID_missing(t *testing.T) {
	_, err := actorUID(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", st.Code())
	}
}
