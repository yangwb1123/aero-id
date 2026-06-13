package grpc_test

// file_edge_test.go covers gaps not addressed by parity_test.go / parity_edit_test.go:
//
//   - UpdateFileMetadata – PermissionDenied (non-owner) path
//   - ShareFile          – invalid file_id path
//   - UnshareFile        – success + PermissionDenied + invalid file_id
//   - ListSharedWithMe   – error propagation path

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

// fileEdgeSvc wires only the methods exercised in this file.
type fileEdgeSvc struct {
	service.FileService
	updateFn  func(context.Context, domain.ID, service.UpdateFileMetadataInput) (*domain.File, error)
	shareFn   func(context.Context, domain.ID, []string) error
	unshareFn func(context.Context, domain.ID, string) error
	sharedFn  func(context.Context, domain.Page) (*service.FileList, error)
}

func (f fileEdgeSvc) UpdateMetadata(ctx context.Context, id domain.ID, in service.UpdateFileMetadataInput) (*domain.File, error) {
	return f.updateFn(ctx, id, in)
}
func (f fileEdgeSvc) ShareFile(ctx context.Context, id domain.ID, uids []string) error {
	return f.shareFn(ctx, id, uids)
}
func (f fileEdgeSvc) UnshareFile(ctx context.Context, id domain.ID, uid string) error {
	return f.unshareFn(ctx, id, uid)
}
func (f fileEdgeSvc) ListSharedWithMe(ctx context.Context, p domain.Page) (*service.FileList, error) {
	return f.sharedFn(ctx, p)
}

// ---- UpdateFileMetadata – PermissionDenied ----

func TestGRPC_UpdateFileMetadata_PermissionDenied(t *testing.T) {
	svc := fileEdgeSvc{updateFn: func(context.Context, domain.ID, service.UpdateFileMetadataInput) (*domain.File, error) {
		return nil, apperr.Forbidden("file.forbidden", "not owner")
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.UpdateFileMetadata(authCtx(), &aeropb.UpdateFileMetadataRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

// ---- ShareFile – invalid file_id ----

func TestGRPC_ShareFile_InvalidFileID(t *testing.T) {
	conn := dial(t, grpcsrv.NewFileServer(fileEdgeSvc{}))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.ShareFile(authCtx(), &aeropb.ShareFileRequest{FileId: "not-a-uuid", GranteeUids: []string{"a"}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- UnshareFile ----

func TestGRPC_UnshareFile_Success(t *testing.T) {
	var gotGrantee string
	svc := fileEdgeSvc{unshareFn: func(_ context.Context, _ domain.ID, uid string) error {
		gotGrantee = uid
		return nil
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)

	if _, err := client.UnshareFile(authCtx(), &aeropb.UnshareFileRequest{
		FileId:     idgen.NewID().String(),
		GranteeUid: "u2",
	}); err != nil {
		t.Fatal(err)
	}
	if gotGrantee != "u2" {
		t.Fatalf("grantee = %q, want u2", gotGrantee)
	}
}

func TestGRPC_UnshareFile_PermissionDenied(t *testing.T) {
	svc := fileEdgeSvc{unshareFn: func(context.Context, domain.ID, string) error {
		return apperr.Forbidden("file.forbidden", "not owner")
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.UnshareFile(authCtx(), &aeropb.UnshareFileRequest{
		FileId:     idgen.NewID().String(),
		GranteeUid: "u2",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestGRPC_UnshareFile_InvalidFileID(t *testing.T) {
	conn := dial(t, grpcsrv.NewFileServer(fileEdgeSvc{}))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.UnshareFile(authCtx(), &aeropb.UnshareFileRequest{FileId: "bad", GranteeUid: "u2"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

// ---- ListSharedWithMe error propagation ----

func TestGRPC_ListSharedWithMe_ErrorPropagated(t *testing.T) {
	svc := fileEdgeSvc{sharedFn: func(context.Context, domain.Page) (*service.FileList, error) {
		return nil, apperr.Internal("file.err", "db down")
	}}
	conn := dial(t, grpcsrv.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.ListSharedWithMe(authCtx(), &aeropb.ListSharedWithMeRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}
