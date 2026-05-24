package errors

import (
	stderrors "errors"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
)

func TestNotFound(t *testing.T) {
	e := NotFound("file.not_found", "file not found")
	if e.HTTP != http.StatusNotFound {
		t.Errorf("HTTP = %d, want 404", e.HTTP)
	}
	if e.GRPC != codes.NotFound {
		t.Errorf("GRPC = %v, want NotFound", e.GRPC)
	}
	if e.Code != "file.not_found" {
		t.Errorf("Code = %q", e.Code)
	}
}

func TestUnauthorized(t *testing.T) {
	e := Unauthorized("auth.unauthorized", "token invalid")
	if e.HTTP != http.StatusUnauthorized || e.GRPC != codes.Unauthenticated {
		t.Errorf("got HTTP=%d GRPC=%v", e.HTTP, e.GRPC)
	}
}

func TestWrap_preservesCause(t *testing.T) {
	root := stderrors.New("root cause")
	e := Internal("internal.unexpected", "boom").Wrap(root)
	if !stderrors.Is(e, root) {
		t.Fatal("errors.Is should unwrap to root cause")
	}
}

func TestAs(t *testing.T) {
	var ae *AppError
	if !stderrors.As(NotFound("x.y", "z"), &ae) {
		t.Fatal("errors.As should match *AppError")
	}
}
