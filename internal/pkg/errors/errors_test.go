package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestGRPCError_nil(t *testing.T) {
	if GRPCError(nil) != nil {
		t.Fatal("GRPCError(nil) must be nil")
	}
}

func TestGRPCError_appErrorCarriesCodeAndSafeMessage(t *testing.T) {
	err := NotFound("sync.not_found", "sync not found").Wrap(stderrors.New("db: secret detail"))
	st := status.Convert(GRPCError(err))
	if st.Code() != codes.NotFound || st.Message() != "sync not found" {
		t.Fatalf("status = %v %q", st.Code(), st.Message())
	}
	if strings.Contains(st.Message(), "secret") || strings.Contains(st.Message(), "cause") {
		t.Fatalf("status message leaked internal detail: %q", st.Message())
	}
	details := st.Details()
	if len(details) != 1 {
		t.Fatalf("details = %d, want one", len(details))
	}
	info, ok := details[0].(*errdetails.ErrorInfo)
	if !ok || info.Reason != "sync.not_found" || info.Domain != "aero-id" {
		t.Fatalf("detail = %#v, want aero-id/sync.not_found", details[0])
	}
}

func TestGRPCError_deadlineAndCancel(t *testing.T) {
	if status.Code(GRPCError(context.DeadlineExceeded)) != codes.DeadlineExceeded {
		t.Fatal("deadline must map to DeadlineExceeded")
	}
	if status.Code(GRPCError(context.Canceled)) != codes.Canceled {
		t.Fatal("cancel must map to Canceled")
	}
	if status.Code(GRPCError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded))) != codes.DeadlineExceeded {
		t.Fatal("wrapped deadline must map to DeadlineExceeded")
	}
}

func TestGRPCError_genericInternal(t *testing.T) {
	st := status.Convert(GRPCError(stderrors.New("boom")))
	if st.Code() != codes.Internal || st.Message() != "internal server error" {
		t.Fatalf("status = %v %q", st.Code(), st.Message())
	}
}
