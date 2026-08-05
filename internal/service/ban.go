package service

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// BanService provides ban/unban/list operations for admin users. Every mutation
// records an audit event. The IsBanned query is intentionally kept in the
// repository layer so the BanCheck middleware can call it directly without
// going through the service (avoiding re-validation of the caller's actor).
type BanService interface {
	// Ban marks uid as banned. The caller must hold PermUserBan. Duration is
	// the ban lifetime; zero means permanent. An empty reason is rejected.
	Ban(ctx context.Context, uid string, reason string, duration time.Duration) error
	// Unban lifts the ban on uid. The caller must hold PermUserBan.
	Unban(ctx context.Context, uid string) error
	// ListBanned returns active bans, cursor-paged.
	ListBanned(ctx context.Context, page domain.Page) ([]*domain.UserBan, string, error)
}

type banService struct {
	bans  repository.BanRepository
	admin AdminService
	audit AuditLogger // nil => audit is inactive
}

// BanOption configures optional banService behaviour.
type BanOption func(*banService)

// WithBanAuditLogger wires an AuditLogger into the ban service.
func WithBanAuditLogger(a AuditLogger) BanOption {
	return func(s *banService) { s.audit = a }
}

// NewBanService builds a BanService.
func NewBanService(bans repository.BanRepository, admin AdminService, opts ...BanOption) BanService {
	s := &banService{bans: bans, admin: admin}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *banService) Ban(ctx context.Context, uid string, reason string, duration time.Duration) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if uid == "" {
		return apperr.BadRequest("ban.invalid_uid", "uid must not be blank")
	}
	if reason == "" {
		return apperr.BadRequest("ban.invalid_reason", "reason is required")
	}
	if uid == actor {
		return apperr.BadRequest("ban.self", "cannot ban yourself")
	}

	// Check permission: caller must hold PermUserBan.
	ok, err := s.admin.HasPermission(ctx, actor, domain.PermUserBan)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Forbidden("ban.denied", "insufficient permissions to ban users")
	}

	var expiresAt *time.Time
	if duration > 0 {
		t := time.Now().UTC().Add(duration)
		expiresAt = &t
	}

	if err := s.bans.Ban(ctx, uid, actor, reason, expiresAt); err != nil {
		return apperr.Internal("ban.failed", "failed to ban user").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &domain.AuditEvent{
			ActorUID:    actor,
			Action:      domain.AuditUserBan,
			SubjectType: "user",
			SubjectID:   uid,
			TargetUID:   uid,
			Meta: map[string]any{
				"reason":    reason,
				"expiresAt": expiresAt,
			},
		})
	}
	return nil
}

func (s *banService) Unban(ctx context.Context, uid string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if uid == "" {
		return apperr.BadRequest("ban.invalid_uid", "uid must not be blank")
	}

	ok, err := s.admin.HasPermission(ctx, actor, domain.PermUserBan)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Forbidden("ban.denied", "insufficient permissions to unban users")
	}

	if err := s.bans.Unban(ctx, uid); err != nil {
		return apperr.Internal("ban.unban_failed", "failed to unban user").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &domain.AuditEvent{
			ActorUID:    actor,
			Action:      domain.AuditUserUnban,
			SubjectType: "user",
			SubjectID:   uid,
			TargetUID:   uid,
		})
	}
	return nil
}

func (s *banService) ListBanned(ctx context.Context, page domain.Page) ([]*domain.UserBan, string, error) {
	if _, err := mustActor(ctx); err != nil {
		return nil, "", err
	}
	bans, next, err := s.bans.ListBanned(ctx, page)
	if err != nil {
		return nil, "", apperr.Internal("ban.list_failed", "failed to list banned users").Wrap(err)
	}
	return bans, next, nil
}
