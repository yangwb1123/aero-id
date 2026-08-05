package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	"github.com/aero/aero-id/internal/service"
)

type AggregationServer struct {
	aeropb.UnimplementedAggregationServiceServer
	aggregation service.AggregationService
}

func NewAggregationServer(aggregation service.AggregationService) *AggregationServer {
	return &AggregationServer{aggregation: aggregation}
}

func (s *AggregationServer) Register(server *grpc.Server) {
	aeropb.RegisterAggregationServiceServer(server, s)
}

var _ aeropb.AggregationServiceServer = (*AggregationServer)(nil)

func (s *AggregationServer) GetMeOverview(ctx context.Context, req *aeropb.GetMeOverviewRequest) (*aeropb.GetMeOverviewResponse, error) {
	view, err := s.aggregation.GetMeOverview(ctx, aggregationInputFromProto(req.GetConsistency(), req.GetDatasets(), req.GetMaxAgeSeconds()))
	if err != nil {
		return nil, grpcError(err)
	}
	result, err := aggregateViewToProto(view)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetMeOverviewResponse{View: result}, nil
}

func (s *AggregationServer) GetAccountOverview(ctx context.Context, req *aeropb.GetAccountOverviewRequest) (*aeropb.GetAccountOverviewResponse, error) {
	id, err := parseID(req.GetAccountId(), "account_id")
	if err != nil {
		return nil, err
	}
	view, err := s.aggregation.GetAccountOverview(ctx, id, aggregationInputFromProto(req.GetConsistency(), req.GetDatasets(), req.GetMaxAgeSeconds()))
	if err != nil {
		return nil, grpcError(err)
	}
	result, err := aggregateViewToProto(view)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetAccountOverviewResponse{View: result}, nil
}

func aggregationInputFromProto(consistency string, datasets []string, maxAgeSeconds int64) service.AggregationInput {
	names := make([]domain.AccountDataset, len(datasets))
	for i, dataset := range datasets {
		names[i] = domain.AccountDataset(dataset)
	}
	var maxAge time.Duration
	if maxAgeSeconds > 0 && maxAgeSeconds <= 365*24*60*60 {
		maxAge = time.Duration(maxAgeSeconds) * time.Second
	}
	return service.AggregationInput{
		Consistency: domain.ConsistencyMode(consistency),
		Datasets:    names,
		MaxAge:      maxAge,
	}
}
