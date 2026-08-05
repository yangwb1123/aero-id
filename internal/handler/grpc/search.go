package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	"github.com/aero/aero-id/internal/service"
)

// SearchServer implements aeropb.SearchServiceServer.
type SearchServer struct {
	aeropb.UnimplementedSearchServiceServer
	svc service.SearchService
}

func NewSearchServer(svc service.SearchService) *SearchServer {
	return &SearchServer{svc: svc}
}

func (s *SearchServer) Register(srv *grpc.Server) { aeropb.RegisterSearchServiceServer(srv, s) }

func (s *SearchServer) Search(ctx context.Context, req *aeropb.SearchRequest) (*aeropb.SearchResponse, error) {
	var kinds []domain.SearchKind
	for _, k := range req.Kinds {
		switch k {
		case string(domain.SearchKindMessages):
			kinds = append(kinds, domain.SearchKindMessages)
		case string(domain.SearchKindComments):
			kinds = append(kinds, domain.SearchKindComments)
		case string(domain.SearchKindFiles):
			kinds = append(kinds, domain.SearchKindFiles)
		}
	}

	hits, err := s.svc.Search(ctx, req.Query, kinds, int(req.Limit))
	if err != nil {
		return nil, grpcError(err)
	}

	pbHits := make([]*aeropb.SearchHit, 0, len(hits))
	for _, h := range hits {
		pbHit := &aeropb.SearchHit{
			Kind:    string(h.Kind),
			Snippet: h.Snippet,
		}
		switch h.Kind {
		case domain.SearchKindMessages:
			pbHit.Message = msgToPB(h.Message)
		case domain.SearchKindComments:
			pbHit.Comment = commentToPB(h.Comment)
		case domain.SearchKindFiles:
			pbHit.File = fileToPB(h.File)
		}
		pbHits = append(pbHits, pbHit)
	}
	return &aeropb.SearchResponse{Hits: pbHits}, nil
}
