package grpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	"github.com/aero/aero-id/internal/service"
)

// AccountServer exposes the account control-plane contract over internal gRPC.
// All account rules remain in the shared service so HTTP and gRPC have the same
// authorization and consistency behavior.
type AccountServer struct {
	aeropb.UnimplementedAccountServiceServer
	accounts service.AccountService
}

func NewAccountServer(accounts service.AccountService) *AccountServer {
	return &AccountServer{accounts: accounts}
}

func (s *AccountServer) Register(server *grpc.Server) {
	aeropb.RegisterAccountServiceServer(server, s)
}

var _ aeropb.AccountServiceServer = (*AccountServer)(nil)

func (s *AccountServer) GetMe(ctx context.Context, _ *aeropb.GetMeRequest) (*aeropb.GetMeResponse, error) {
	view, err := s.accounts.GetMe(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	result, err := accountViewToProto(view)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetMeResponse{View: result}, nil
}

func (s *AccountServer) GetProfile(ctx context.Context, _ *aeropb.GetProfileRequest) (*aeropb.GetProfileResponse, error) {
	profile, err := s.accounts.GetProfile(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	result, err := accountProfileToProto(profile)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetProfileResponse{Profile: result}, nil
}

func (s *AccountServer) UpdateProfile(ctx context.Context, req *aeropb.UpdateProfileRequest) (*aeropb.UpdateProfileResponse, error) {
	profile, err := s.accounts.UpdateProfile(ctx, updateProfileInput(req))
	if err != nil {
		return nil, grpcError(err)
	}
	result, err := accountProfileToProto(profile)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.UpdateProfileResponse{Profile: result}, nil
}

func (s *AccountServer) ListSources(ctx context.Context, req *aeropb.ListAccountSourcesRequest) (*aeropb.ListAccountSourcesResponse, error) {
	result, err := s.accounts.ListSources(ctx, pageFromProto(req.GetPage()))
	if err != nil {
		return nil, grpcError(err)
	}
	sources, err := accountSourcesToProto(result.Sources)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListAccountSourcesResponse{Sources: sources, NextCursor: result.NextCursor}, nil
}

func (s *AccountServer) ListMemberships(ctx context.Context, req *aeropb.ListAccountMembershipsRequest) (*aeropb.ListAccountMembershipsResponse, error) {
	result, err := s.accounts.ListMemberships(ctx, pageFromProto(req.GetPage()))
	if err != nil {
		return nil, grpcError(err)
	}
	memberships, err := accountMembershipsToProto(result.Memberships)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListAccountMembershipsResponse{Memberships: memberships, NextCursor: result.NextCursor}, nil
}

func (s *AccountServer) GetAccount(ctx context.Context, req *aeropb.GetAccountRequest) (*aeropb.GetAccountResponse, error) {
	id, err := parseID(req.GetAccountId(), "account_id")
	if err != nil {
		return nil, err
	}
	view, err := s.accounts.GetAccount(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	result, err := accountViewToProto(view)
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.GetAccountResponse{View: result}, nil
}

func (s *AccountServer) ListAccounts(ctx context.Context, req *aeropb.ListAccountsRequest) (*aeropb.ListAccountsResponse, error) {
	result, err := s.accounts.ListAccounts(ctx, service.ListAccountsInput{
		Query:      req.GetQuery(),
		Status:     req.GetStatus(),
		HomeRegion: req.GetHomeRegion(),
		Page:       pageFromProto(req.GetPage()),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &aeropb.ListAccountsResponse{
		Accounts:   accountsToProto(result.Accounts),
		NextCursor: result.NextCursor,
	}, nil
}

func pageFromProto(page *aeropb.Page) domain.Page {
	if page == nil {
		return domain.Page{}
	}
	return domain.Page{Limit: int(page.GetLimit()), Cursor: page.GetCursor()}
}
