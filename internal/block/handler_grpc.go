package block

import (
	"context"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

type BlockServer struct {
	aeropb.UnimplementedBlockServiceServer
	blocks Service
}

func NewBlockServer(blocks Service) *BlockServer { return &BlockServer{blocks: blocks} }

func (s *BlockServer) Register(srv *grpc.Server) { aeropb.RegisterBlockServiceServer(srv, s) }

func pageFromPB(limit int32, cursor string) domain.Page {
	return domain.Page{Limit: int(limit), Cursor: cursor}
}

func (s *BlockServer) Block(ctx context.Context, req *aeropb.BlockRequest) (*aeropb.BlockResponse, error) {
	if err := s.blocks.Block(ctx, req.Uid); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.BlockResponse{}, nil
}

func (s *BlockServer) Unblock(ctx context.Context, req *aeropb.UnblockRequest) (*aeropb.UnblockResponse, error) {
	if err := s.blocks.Unblock(ctx, req.Uid); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UnblockResponse{}, nil
}

func (s *BlockServer) ListBlocked(ctx context.Context, req *aeropb.ListBlockedRequest) (*aeropb.ListBlockedResponse, error) {
	list, err := s.blocks.ListBlocked(ctx, pageFromPB(req.Page.GetLimit(), req.Page.GetCursor()))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ListBlockedResponse{Uids: list.UIDs, NextCursor: list.NextCursor}, nil
}
