package block_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aero/aero-id/internal/block"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func TestBlockServer_Block(t *testing.T) {
	called := false
	svc := &mockBlockService{
		BlockFn: func(_ context.Context, uid string) error {
			called = true
			if uid != "u2" {
				t.Fatalf("uid = %q", uid)
			}
			return nil
		},
	}
	srv := block.NewBlockServer(svc)
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := srv.Block(ctx, &aeropb.BlockRequest{Uid: "u2"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Block not called")
	}
}

func TestBlockServer_BlockError(t *testing.T) {
	svc := &mockBlockService{
		BlockFn: func(_ context.Context, _ string) error {
			return apperr.Forbidden("block.self", "cannot block yourself")
		},
	}
	srv := block.NewBlockServer(svc)
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := srv.Block(ctx, &aeropb.BlockRequest{Uid: "u1"})
	if err == nil {
		t.Fatal("expected error")
	}
	st := status.Convert(err)
	if st.Code() != codes.PermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", st.Code())
	}
}

func TestBlockServer_Unblock(t *testing.T) {
	called := false
	svc := &mockBlockService{
		UnblockFn: func(_ context.Context, uid string) error {
			called = true
			if uid != "u2" {
				t.Fatalf("uid = %q", uid)
			}
			return nil
		},
	}
	srv := block.NewBlockServer(svc)
	ctx := domain.WithActor(context.Background(), "u1")
	_, err := srv.Unblock(ctx, &aeropb.UnblockRequest{Uid: "u2"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Unblock not called")
	}
}

func TestBlockServer_ListBlocked(t *testing.T) {
	svc := &mockBlockService{
		ListBlockedFn: func(_ context.Context, page domain.Page) (*block.BlockList, error) {
			return &block.BlockList{UIDs: []string{"a", "b"}, NextCursor: "n"}, nil
		},
	}
	srv := block.NewBlockServer(svc)
	ctx := domain.WithActor(context.Background(), "u1")
	resp, err := srv.ListBlocked(ctx, &aeropb.ListBlockedRequest{
		Page: &aeropb.Page{Limit: 10, Cursor: "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Uids) != 2 || resp.NextCursor != "n" {
		t.Errorf("uids=%v cursor=%q", resp.Uids, resp.NextCursor)
	}
}
