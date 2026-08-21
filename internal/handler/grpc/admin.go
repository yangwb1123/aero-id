package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

var errUnimplemented = status.Error(codes.Unimplemented, "not implemented")

func badRequest(code, msg string) error {
	return status.Errorf(codes.InvalidArgument, "%s: %s", code, msg)
}

// AdminServer implements the gRPC AdminService, delegating every call to the
// shared service layer. Permission checks are enforced by the service layer
// (the actor uid comes from the auth interceptor) and by a unary interceptor
// for the "audit.read" permission on ListAudit.
type AdminServer struct {
	aeropb.UnimplementedAdminServiceServer
	admin service.AdminService
	bans  service.BanService // nil => ban RPCs return Unimplemented
}

// NewAdminServer builds an AdminServer. The bans param is optional.
func NewAdminServer(admin service.AdminService, bans ...service.BanService) *AdminServer {
	s := &AdminServer{admin: admin}
	if len(bans) > 0 {
		s.bans = bans[0]
	}
	return s
}

func (s *AdminServer) Register(srv *grpc.Server) { aeropb.RegisterAdminServiceServer(srv, s) }

func (s *AdminServer) GrantRole(ctx context.Context, req *aeropb.GrantRoleRequest) (*aeropb.GrantRoleResponse, error) {
	if err := s.admin.GrantRole(ctx, req.Uid, req.RoleCode); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.GrantRoleResponse{}, nil
}

func (s *AdminServer) RevokeRole(ctx context.Context, req *aeropb.RevokeRoleRequest) (*aeropb.RevokeRoleResponse, error) {
	if err := s.admin.RevokeRole(ctx, req.Uid, req.RoleCode); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.RevokeRoleResponse{}, nil
}

func (s *AdminServer) ListRoles(ctx context.Context, req *aeropb.ListRolesRequest) (*aeropb.ListRolesResponse, error) {
	roles, err := s.admin.ListRoles(ctx)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	out := make([]*aeropb.Role, len(roles))
	for i, r := range roles {
		out[i] = &aeropb.Role{
			Code:        r.Code,
			Name:        r.Name,
			Permissions: r.Permissions,
		}
	}
	return &aeropb.ListRolesResponse{Roles: out}, nil
}

func (s *AdminServer) ListUserRoles(ctx context.Context, req *aeropb.ListUserRolesRequest) (*aeropb.ListUserRolesResponse, error) {
	roles, err := s.admin.ListUserRoles(ctx, req.Uid)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	out := make([]*aeropb.UserRole, len(roles))
	for i, r := range roles {
		out[i] = &aeropb.UserRole{
			Uid:       r.UID,
			RoleCode:  r.RoleCode,
			GrantedBy: r.GrantedBy,
		}
	}
	return &aeropb.ListUserRolesResponse{Roles: out}, nil
}

func (s *AdminServer) ListAudit(ctx context.Context, req *aeropb.ListAuditRequest) (*aeropb.ListAuditResponse, error) {
	page := domain.Page{
		Limit:  domain.DefaultPageLimit,
		Cursor: "",
	}
	if req.Page != nil {
		page.Limit = int(req.Page.Limit)
		page.Cursor = req.Page.Cursor
	}
	events, next, err := s.admin.ListAudit(ctx, req.Action, page)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	out := make([]*aeropb.AuditEvent, len(events))
	for i, e := range events {
		ts := timestamppb.New(e.CreatedAt)
		out[i] = &aeropb.AuditEvent{
			Id:          e.ID.String(),
			ActorUid:    e.ActorUID,
			Action:      e.Action,
			SubjectType: e.SubjectType,
			SubjectId:   e.SubjectID,
			TargetUid:   e.TargetUID,
			CreatedAt:   ts,
		}
	}
	return &aeropb.ListAuditResponse{Events: out, NextCursor: next}, nil
}

// ---- Ban RPCs ----

func (s *AdminServer) Ban(ctx context.Context, req *aeropb.BanRequest) (*aeropb.BanResponse, error) {
	if s.bans == nil {
		return nil, errUnimplemented
	}
	var d time.Duration
	if req.Duration != "" {
		var err error
		d, err = time.ParseDuration(req.Duration)
		if err != nil {
			return nil, apperr.GRPCError(badRequest("ban.invalid_duration", "invalid duration format"))
		}
	}
	if err := s.bans.Ban(ctx, req.Uid, req.Reason, d); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.BanResponse{}, nil
}

func (s *AdminServer) Unban(ctx context.Context, req *aeropb.UnbanRequest) (*aeropb.UnbanResponse, error) {
	if s.bans == nil {
		return nil, errUnimplemented
	}
	if err := s.bans.Unban(ctx, req.Uid); err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.UnbanResponse{}, nil
}

func (s *AdminServer) ListBans(ctx context.Context, req *aeropb.ListBansRequest) (*aeropb.ListBansResponse, error) {
	if s.bans == nil {
		return nil, errUnimplemented
	}
	page := domain.Page{Limit: domain.DefaultPageLimit}
	if req.Page != nil {
		page.Limit = int(req.Page.Limit)
		page.Cursor = req.Page.Cursor
	}
	bans, next, err := s.bans.ListBanned(ctx, page)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	out := make([]*aeropb.BanEvent, len(bans))
	for i, b := range bans {
		pb := &aeropb.BanEvent{
			Uid:      b.UID,
			ActorUid: b.ActorUID,
			Reason:   b.Reason,
		}
		if b.ExpiresAt != nil {
			pb.ExpiresAt = timestamppb.New(*b.ExpiresAt)
		}
		pb.CreatedAt = timestamppb.New(b.CreatedAt)
		out[i] = pb
	}
	return &aeropb.ListBansResponse{Bans: out, NextCursor: next}, nil
}
