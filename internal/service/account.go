package service

import (
	"context"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/region"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

const (
	DefaultAccountIssuer    = "snaplink"
	DefaultAccountRegion    = "local"
	AccountReadPermission   = "account.read"
	AccountManagePermission = "account.manage"
)

// AccountPermissionChecker is intentionally narrow so the account service can
// use Snaplink-backed authorization later without depending on middleware.
type AccountPermissionChecker interface {
	HasPermission(ctx context.Context, uid, permission string) (bool, error)
}

type AccountService interface {
	GetMe(ctx context.Context) (*AccountView, error)
	GetProfile(ctx context.Context) (*domain.AccountProfile, error)
	UpdateProfile(ctx context.Context, input UpdateAccountProfileInput) (*domain.AccountProfile, error)
	ListSources(ctx context.Context, page domain.Page) (*AccountSourceList, error)
	ListMemberships(ctx context.Context, page domain.Page) (*AccountMembershipList, error)
	UpsertSource(context.Context, *domain.AccountSource) error
	UpsertMembership(context.Context, *domain.AccountMembership) error
	GetAccount(ctx context.Context, id domain.ID) (*AccountView, error)
	ListAccounts(ctx context.Context, input ListAccountsInput) (*AccountList, error)
	BackfillLegacyIdentities(ctx context.Context, limit int) (*BackfillResult, error)
}

type UpdateAccountProfileInput struct {
	DisplayName *string
	AvatarURL   *string
	Locale      *string
	Timezone    *string
	Preferences map[string]any
}

type ListAccountsInput struct {
	Query      string
	Status     string
	HomeRegion string
	Page       domain.Page
}

type AccountView struct {
	Account     *domain.Account
	Profile     *domain.AccountProfile
	Sources     []*domain.AccountSource
	Memberships []*domain.AccountMembership
}

type AccountSourceList struct {
	Sources    []*domain.AccountSource
	NextCursor string
}

type AccountMembershipList struct {
	Memberships []*domain.AccountMembership
	NextCursor  string
}

type AccountList struct {
	Accounts   []*domain.Account
	NextCursor string
}

type accountService struct {
	repo           repository.AccountRepository
	permission     AccountPermissionChecker
	audit          AuditLedgerService
	quota          AccountQuota
	issuer         string
	region         string
	home           *region.Resolver
	backfillTenant string
	directory     repository.RegionDirectoryRepository // optional durable fencing guard (P6-02)
	homeVersion   string
}

type AccountOption func(*accountService)

func WithAccountPermissionChecker(checker AccountPermissionChecker) AccountOption {
	return func(s *accountService) { s.permission = checker }
}

func WithAccountIssuer(issuer string) AccountOption {
	return func(s *accountService) {
		if strings.TrimSpace(issuer) != "" {
			s.issuer = strings.TrimSpace(issuer)
		}
	}
}

func WithAccountRegion(region string) AccountOption {
	return func(s *accountService) {
		if strings.TrimSpace(region) != "" {
			s.region = strings.TrimSpace(region)
		}
	}
}

func WithAccountHomeRegionResolver(resolver *region.Resolver) AccountOption {
	return func(s *accountService) { s.home = resolver }
}

func WithAccountAudit(audit AuditLedgerService) AccountOption {
	return func(s *accountService) { s.audit = audit }
}

func WithAccountQuota(quota AccountQuota) AccountOption {
	return func(s *accountService) { s.quota = quota }
}

func NewAccountService(repo repository.AccountRepository, opts ...AccountOption) AccountService {
	s := &accountService{repo: repo, issuer: DefaultAccountIssuer, region: DefaultAccountRegion,
		backfillTenant: LegacyBackfillTenant, homeVersion: "v1"}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *accountService) GetMe(ctx context.Context) (*AccountView, error) {
	account, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, account)
}

func (s *accountService) GetAccount(ctx context.Context, id domain.ID) (*AccountView, error) {
	if err := s.requirePermission(ctx, AccountReadPermission); err != nil {
		return nil, err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	account, err := s.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, mapAccountError(err, "account.not_found", "account not found")
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", ActorUID: actorFromContext(ctx), AccountID: id,
			TenantID: TenantFrom(ctx), Action: "account.viewed", ResourceType: "account", ResourceID: id.String(),
			Payload: map[string]any{"mode": "admin"}})
	}
	return s.view(ctx, account)
}

func (s *accountService) ListAccounts(ctx context.Context, input ListAccountsInput) (*AccountList, error) {
	if err := s.requirePermission(ctx, AccountReadPermission); err != nil {
		return nil, err
	}
	if err := validateAccountListInput(&input); err != nil {
		return nil, err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.repo.ListAccounts(ctx, repository.AccountListFilter{
		TenantID: tenantID, Query: input.Query, Status: input.Status, HomeRegion: input.HomeRegion, Page: input.Page,
	})
	if err != nil {
		return nil, mapAccountError(err, "account.list_failed", "failed to list accounts")
	}
	if s.audit != nil {
		payload := map[string]any{"query_present": strings.TrimSpace(input.Query) != "", "status": input.Status, "home_region": input.HomeRegion}
		if strings.TrimSpace(input.Query) != "" {
			payload["query_sha256"] = hashAuditBytes([]byte(input.Query))
		}
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", ActorUID: actorFromContext(ctx), TenantID: TenantFrom(ctx),
			Action: "account.listed", ResourceType: "account", Payload: payload})
	}
	return &AccountList{Accounts: rows, NextCursor: next}, nil
}

func validateAccountListInput(input *ListAccountsInput) error {
	input.Query = strings.TrimSpace(input.Query)
	input.Status = strings.TrimSpace(input.Status)
	input.HomeRegion = strings.TrimSpace(input.HomeRegion)
	if len(input.Query) > 256 {
		return apperr.BadRequest("account.query_too_long", "account query is too long")
	}
	if len(input.HomeRegion) > 64 {
		return apperr.BadRequest("account.region_invalid", "home region is too long")
	}
	if !validAccountStatus(input.Status) {
		return apperr.BadRequest("account.status_invalid", "account status is not supported")
	}
	if len(input.Page.Cursor) > 256 {
		return apperr.BadRequest("account.cursor_invalid", "account cursor is too long")
	}
	return nil
}

func validAccountStatus(status string) bool {
	return status == "" || status == string(domain.AccountActive) ||
		status == string(domain.AccountSuspended) || status == string(domain.AccountErased)
}

func (s *accountService) UpsertSource(ctx context.Context, source *domain.AccountSource) error {
	if source == nil || !source.Valid() {
		return apperr.BadRequest("account.invalid_source", "source reference is invalid")
	}
	if err := s.repo.UpsertSource(ctx, source); err != nil {
		return mapAccountError(err, "account.source_upsert_failed", "failed to update source reference")
	}
	return nil
}

func (s *accountService) UpsertMembership(ctx context.Context, membership *domain.AccountMembership) error {
	if membership == nil || !membership.Valid() {
		return apperr.BadRequest("account.invalid_membership", "membership reference is invalid")
	}
	if err := s.repo.UpsertMembership(ctx, membership); err != nil {
		return mapAccountError(err, "account.membership_upsert_failed", "failed to update source membership")
	}
	return nil
}

func (s *accountService) view(ctx context.Context, account *domain.Account) (*AccountView, error) {
	profile, err := s.profileOrDefault(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	sources, _, err := s.repo.ListSources(ctx, account.ID, domain.Page{Limit: domain.MaxPageLimit})
	if err != nil {
		return nil, mapAccountError(err, "account.sources_failed", "failed to load account sources")
	}
	memberships, _, err := s.repo.ListMemberships(ctx, account.ID, domain.Page{Limit: domain.MaxPageLimit})
	if err != nil {
		return nil, mapAccountError(err, "account.memberships_failed", "failed to load account memberships")
	}
	return &AccountView{Account: account, Profile: profile, Sources: sources, Memberships: memberships}, nil
}

func (s *accountService) profileOrDefault(ctx context.Context, id domain.ID) (*domain.AccountProfile, error) {
	profile, err := s.repo.GetProfile(ctx, id)
	if err == nil {
		return profile, nil
	}
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
		return &domain.AccountProfile{AccountID: id, Timezone: "UTC", Preferences: map[string]any{}}, nil
	}
	return nil, mapAccountError(err, "account.profile_failed", "failed to load account profile")
}

func (s *accountService) requirePermission(ctx context.Context, permission string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if s.permission == nil {
		return apperr.Forbidden("account.permission_unavailable", "account permission is not configured")
	}
	ok, err := s.permission.HasPermission(ctx, actor, permission)
	if err != nil {
		return apperr.Internal("account.permission_check_failed", "failed to verify account permission").Wrap(err)
	}
	if !ok {
		return apperr.Forbidden("account.forbidden", "insufficient account permission")
	}
	return nil
}

func mapAccountError(err error, code, message string) error {
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound(code, message).Wrap(err)
	}
	return apperr.Internal(code, message).Wrap(err)
}
