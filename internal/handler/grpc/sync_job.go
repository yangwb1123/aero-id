package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

type SyncJobServer struct {
	aeropb.UnimplementedSyncJobServiceServer
	jobs service.SyncJobService
}

func NewSyncJobServer(jobs service.SyncJobService) *SyncJobServer {
	return &SyncJobServer{jobs: jobs}
}

func (s *SyncJobServer) Register(server *grpc.Server) {
	aeropb.RegisterSyncJobServiceServer(server, s)
}

var _ aeropb.SyncJobServiceServer = (*SyncJobServer)(nil)

func (s *SyncJobServer) CreateSyncJob(ctx context.Context, req *aeropb.CreateSyncJobRequest) (*aeropb.CreateSyncJobResponse, error) {
	accountID, err := parseOptionalOperationID(req.GetAccountId(), "account_id")
	if err != nil {
		return nil, err
	}
	datasets := make([]domain.AccountDataset, len(req.GetDatasets()))
	for i, dataset := range req.GetDatasets() {
		datasets[i] = domain.AccountDataset(dataset)
	}
	job, err := s.jobs.Create(ctx, service.SyncJobInput{AccountID: accountID, JobType: domain.SyncJobType(req.GetJobType()),
		Datasets: datasets, IdempotencyKey: req.GetIdempotencyKey()})
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result, err := syncJobToProto(job)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.CreateSyncJobResponse{Job: result}, nil
}

func (s *SyncJobServer) GetSyncJob(ctx context.Context, req *aeropb.GetSyncJobRequest) (*aeropb.GetSyncJobResponse, error) {
	id, err := parseID(req.GetId(), "sync_job_id")
	if err != nil {
		return nil, err
	}
	job, err := s.jobs.Get(ctx, id)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result, err := syncJobToProto(job)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.GetSyncJobResponse{Job: result}, nil
}

func (s *SyncJobServer) ListSyncJobs(ctx context.Context, req *aeropb.ListSyncJobsRequest) (*aeropb.ListSyncJobsResponse, error) {
	items, next, err := s.jobs.List(ctx, pageFromProto(req.GetPage()))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result := make([]*aeropb.SyncJob, 0, len(items))
	for _, item := range items {
		converted, err := syncJobToProto(item)
		if err != nil {
			return nil, apperr.GRPCError(err)
		}
		result = append(result, converted)
	}
	return &aeropb.ListSyncJobsResponse{Jobs: result, NextCursor: next}, nil
}

func (s *SyncJobServer) ReconcileSyncJob(ctx context.Context, req *aeropb.ReconcileSyncJobRequest) (*aeropb.ReconcileSyncJobResponse, error) {
	id, err := parseID(req.GetId(), "sync_job_id")
	if err != nil {
		return nil, err
	}
	job, err := s.jobs.Reconcile(ctx, id, req.GetWorkerId())
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result, err := syncJobToProto(job)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ReconcileSyncJobResponse{Job: result}, nil
}

func syncJobToProto(job *domain.SyncJob) (*aeropb.SyncJob, error) {
	request, err := structFromMap(job.Request)
	if err != nil {
		return nil, err
	}
	result, err := structFromMap(publicSyncResult(job.Result))
	if err != nil {
		return nil, err
	}
	return &aeropb.SyncJob{Id: job.ID.String(), AccountId: job.AccountID.String(), ActorUid: job.ActorUID,
		TenantId: job.TenantID, RequestId: job.RequestID, JobType: string(job.JobType),
		Status: string(job.Status), IdempotencyKey: job.IdempotencyKey, Request: request, Result: result, ErrorCode: job.ErrorCode,
		ErrorMessage: job.ErrorMessage, Attempt: protoCount(job.Attempt), CreatedAt: timestamppb.New(job.CreatedAt),
		UpdatedAt: timestamppb.New(job.UpdatedAt), CompletedAt: timestampOf(job.CompletedAt)}, nil
}

func publicSyncResult(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		if len(key) > 0 && key[0] == '_' {
			continue
		}
		result[key] = item
	}
	return result
}
