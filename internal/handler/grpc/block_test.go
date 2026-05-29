package grpc_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

// blockSvc fakes service.BlockService; only the exercised methods are implemented.
type blockSvc struct {
	service.BlockService
	blockFn func(context.Context, string) error
	listFn  func(context.Context, domain.Page) (*service.BlockList, error)
}

func (b blockSvc) Block(ctx context.Context, uid string) error { return b.blockFn(ctx, uid) }

func (b blockSvc) ListBlocked(ctx context.Context, page domain.Page) (*service.BlockList, error) {
	return b.listFn(ctx, page)
}

func TestGRPC_Block(t *testing.T) {
	var got string
	svc := blockSvc{blockFn: func(_ context.Context, uid string) error {
		got = uid
		return nil
	}}
	conn := dial(t, grpcsrv.NewBlockServer(svc))
	client := aeropb.NewBlockServiceClient(conn)

	if _, err := client.Block(authCtx(), &aeropb.BlockRequest{Uid: "b"}); err != nil {
		t.Fatal(err)
	}
	if got != "b" {
		t.Fatalf("blocked uid = %q, want %q", got, "b")
	}
}

func TestGRPC_BlockInvalidArgument(t *testing.T) {
	// Mirrors the service's BadRequest mapping for self/blank uid.
	svc := blockSvc{blockFn: func(context.Context, string) error {
		return apperr.BadRequest("block.invalid", "cannot block yourself")
	}}
	conn := dial(t, grpcsrv.NewBlockServer(svc))
	client := aeropb.NewBlockServiceClient(conn)

	_, err := client.Block(authCtx(), &aeropb.BlockRequest{Uid: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestGRPC_ListBlocked(t *testing.T) {
	svc := blockSvc{listFn: func(context.Context, domain.Page) (*service.BlockList, error) {
		return &service.BlockList{UIDs: []string{"x", "y"}, NextCursor: "cur"}, nil
	}}
	conn := dial(t, grpcsrv.NewBlockServer(svc))
	client := aeropb.NewBlockServiceClient(conn)

	resp, err := client.ListBlocked(authCtx(), &aeropb.ListBlockedRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Uids) != 2 || resp.Uids[0] != "x" || resp.Uids[1] != "y" {
		t.Fatalf("uids = %v, want [x y]", resp.Uids)
	}
	if resp.NextCursor != "cur" {
		t.Fatalf("next_cursor = %q, want %q", resp.NextCursor, "cur")
	}
}
