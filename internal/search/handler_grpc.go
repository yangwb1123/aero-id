package search

import (
	"context"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/comment"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/file"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	"github.com/aero/aero-id/internal/message"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

type SearchServer struct {
	aeropb.UnimplementedSearchServiceServer
	svc Service
}

func NewSearchServer(svc Service) *SearchServer { return &SearchServer{svc: svc} }

func (s *SearchServer) Register(srv *grpc.Server) { aeropb.RegisterSearchServiceServer(srv, s) }

func (s *SearchServer) Search(ctx context.Context, req *aeropb.SearchRequest) (*aeropb.SearchResponse, error) {
	var kinds []domain.SearchKind
	for _, k := range req.Kinds {
		kinds = append(kinds, domain.SearchKind(k))
	}
	hits, err := s.svc.Search(ctx, req.Query, kinds, int(req.Limit))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	pbHits := make([]*aeropb.SearchHit, 0, len(hits))
	for _, hit := range hits {
		pb := &aeropb.SearchHit{Kind: string(hit.Kind)}
		switch hit.Kind {
		case domain.SearchKindMessages:
			pb.Message = message.MsgToPB(hit.Message)
		case domain.SearchKindComments:
			pb.Comment = comment.CommentToPB(hit.Comment)
		case domain.SearchKindFiles:
			pb.File = file.FileToPB(hit.File)
		}
		pbHits = append(pbHits, pb)
	}
	return &aeropb.SearchResponse{Hits: pbHits}, nil
}
