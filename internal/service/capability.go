package service

import (
	"context"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

type CapabilityService interface {
	GetMeCapabilities(context.Context) (*domain.CapabilityReport, error)
}

// CapabilityConfig contains only validated, non-secret runtime enablement facts.
// It intentionally has no source URLs, credentials, arbitrary policy or paths.
type CapabilityConfig struct {
	LocalRegion          string
	ExportEnabled        bool
	CallbacksEnabled     bool
	Sources              map[string]SourceCapabilityConfig
	AuthorizationTimeout time.Duration
}

type SourceCapabilityConfig struct {
	SummaryEnabled bool
	EraseEnabled   bool
	StatusEnabled  bool
}

type capabilityService struct {
	accounts   AccountService
	snapshots  repository.SnapshotRepository
	permission PermissionChecker
	config     CapabilityConfig
}

func NewCapabilityService(accounts AccountService, snapshots repository.SnapshotRepository,
	permission PermissionChecker, cfg CapabilityConfig) CapabilityService {
	if cfg.AuthorizationTimeout <= 0 {
		cfg.AuthorizationTimeout = 3 * time.Second
	}
	cfg.Sources = cloneSourceCapabilityConfig(cfg.Sources)
	return &capabilityService{accounts: accounts, snapshots: snapshots, permission: permission, config: cfg}
}

func cloneSourceCapabilityConfig(input map[string]SourceCapabilityConfig) map[string]SourceCapabilityConfig {
	result := make(map[string]SourceCapabilityConfig, len(input))
	for name, flags := range input {
		result[name] = flags
	}
	return result
}

func (s *capabilityService) GetMeCapabilities(ctx context.Context) (*domain.CapabilityReport, error) {
	identity, err := verifiedControlPlaneIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if s.accounts == nil {
		return nil, apperr.Unavailable("capability.account_unavailable", "account context is unavailable")
	}
	account, err := capabilityAccount(ctx, s.accounts)
	if err != nil {
		return nil, err
	}
	if account == nil || account.TenantID != identity.TenantID ||
		(account.Status != domain.AccountErased && account.CanonicalUID != identity.UID) {
		return nil, apperr.Forbidden("capability.account_mismatch", "account context is unavailable")
	}
	now := time.Now().UTC()
	decisions := s.authorizationDecisions(ctx, identity.UID)
	report := &domain.CapabilityReport{SchemaVersion: 1, AccountID: account.ID,
		TenantID: identity.TenantID, ClientID: identity.ClientID, Issuer: identity.Issuer,
		HomeRegion: account.HomeRegion, LocalRegion: s.config.LocalRegion, GeneratedAt: now,
		AuthorizationVersion: "unsupported", Capabilities: s.featureCapabilities(account, decisions)}
	report.Datasets, report.Partial = s.datasetCapabilities(ctx, account, now)
	for _, decision := range decisions {
		report.Partial = report.Partial || decision == domain.AccessUnknown
	}
	return report, nil
}

// AccountMetadataService resolves the verified identity without reading profile
// or source/membership content. The production account service implements it.
type AccountMetadataService interface {
	GetMeMetadata(context.Context) (*domain.Account, error)
}

func (s *accountService) GetMeMetadata(ctx context.Context) (*domain.Account, error) {
	if _, err := verifiedControlPlaneIdentity(ctx); err != nil {
		return nil, err
	}
	return s.ensure(ctx)
}

func capabilityAccount(ctx context.Context, accounts AccountService) (*domain.Account, error) {
	if metadata, ok := accounts.(AccountMetadataService); ok {
		return metadata.GetMeMetadata(ctx)
	}
	view, err := accounts.GetMe(ctx)
	if err != nil || view == nil {
		return nil, err
	}
	return view.Account, nil
}

func verifiedControlPlaneIdentity(ctx context.Context) (*domain.TokenIdentity, error) {
	identity, ok := IdentityFrom(ctx)
	if !ok || !completeVerifiedIdentity(identity) {
		return nil, apperr.Unauthorized("auth.claims_required", "verified identity claims are required")
	}
	if identity.UID != actorFromContext(ctx) || identity.TenantID != TenantFrom(ctx) {
		return nil, apperr.Unauthorized("auth.claims_mismatch", "verified identity context is inconsistent")
	}
	return identity, nil
}

func completeVerifiedIdentity(identity *domain.TokenIdentity) bool {
	if identity == nil || !identity.Verified {
		return false
	}
	for _, value := range []string{identity.UID, identity.Issuer, identity.Subject, identity.TenantID, identity.ClientID} {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}
