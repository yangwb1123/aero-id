package file_test

import (
	"context"
	"net"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"

	"github.com/aero/aero-id/internal/file"
)

type validator struct{ uid string }

func (v validator) ValidateToken(context.Context, string) (string, error) { return v.uid, nil }

func dial(t *testing.T, regs ...grpcsrv.ServiceRegistrar) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcsrv.NewServer(validator{uid: "u1"}, zap.NewNop(), nil, regs...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func authCtx() context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer t"))
}

type fileEdgeSvc struct {
	file.Service
	updateFn  func(context.Context, domain.ID, file.UpdateFileMetadataInput) (*domain.File, error)
	shareFn   func(context.Context, domain.ID, []string) error
	unshareFn func(context.Context, domain.ID, string) error
	sharedFn  func(context.Context, domain.Page) (*file.FileList, error)
}

func (f fileEdgeSvc) UpdateMetadata(ctx context.Context, id domain.ID, in file.UpdateFileMetadataInput) (*domain.File, error) {
	return f.updateFn(ctx, id, in)
}
func (f fileEdgeSvc) ShareFile(ctx context.Context, id domain.ID, uids []string) error {
	return f.shareFn(ctx, id, uids)
}
func (f fileEdgeSvc) UnshareFile(ctx context.Context, id domain.ID, uid string) error {
	return f.unshareFn(ctx, id, uid)
}
func (f fileEdgeSvc) ListSharedWithMe(ctx context.Context, p domain.Page) (*file.FileList, error) {
	return f.sharedFn(ctx, p)
}

func TestGRPC_UpdateFileMetadata_PermissionDenied(t *testing.T) {
	svc := fileEdgeSvc{updateFn: func(context.Context, domain.ID, file.UpdateFileMetadataInput) (*domain.File, error) {
		return nil, apperr.Forbidden("file.forbidden", "not owner")
	}}
	conn := dial(t, file.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.UpdateFileMetadata(authCtx(), &aeropb.UpdateFileMetadataRequest{Id: idgen.NewID().String()})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestGRPC_ShareFile_InvalidFileID(t *testing.T) {
	conn := dial(t, file.NewFileServer(fileEdgeSvc{}))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.ShareFile(authCtx(), &aeropb.ShareFileRequest{FileId: "not-a-uuid", GranteeUids: []string{"a"}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_UnshareFile_Success(t *testing.T) {
	var gotGrantee string
	svc := fileEdgeSvc{unshareFn: func(_ context.Context, _ domain.ID, uid string) error {
		gotGrantee = uid
		return nil
	}}
	conn := dial(t, file.NewFileServer(svc))
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
	conn := dial(t, file.NewFileServer(svc))
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
	conn := dial(t, file.NewFileServer(fileEdgeSvc{}))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.UnshareFile(authCtx(), &aeropb.UnshareFileRequest{FileId: "bad", GranteeUid: "u2"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_ListSharedWithMe_ErrorPropagated(t *testing.T) {
	svc := fileEdgeSvc{sharedFn: func(context.Context, domain.Page) (*file.FileList, error) {
		return nil, apperr.Internal("file.err", "db down")
	}}
	conn := dial(t, file.NewFileServer(svc))
	client := aeropb.NewFileServiceClient(conn)

	_, err := client.ListSharedWithMe(authCtx(), &aeropb.ListSharedWithMeRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}
