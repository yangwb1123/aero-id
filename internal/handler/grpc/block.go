package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

type BlockServer struct {
	aeropb.UnimplementedBlockServiceServer
	blocks service.BlockService
}

func NewBlockServer(blocks service.BlockService) *BlockServer {
	return &BlockServer{blocks: blocks}
}

func (s *BlockServer) Register(srv *grpc.Server) { aeropb.RegisterBlockServiceServer(srv, s) }

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
	list, err := s.blocks.ListBlocked(ctx, pageFromPB(req.Page))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ListBlockedResponse{Uids: list.UIDs, NextCursor: list.NextCursor}, nil
}
