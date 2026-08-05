package service

import (
	"context"
	"strings"
	"time"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// LegacyCutoverService drives the per-tenant legacy cutover registry (P8-04)
// and the cleanup-readiness checklist (P8-05). Default is 'legacy': the
// compatibility surface keeps serving until an admin explicitly flips a tenant.
type LegacyCutoverService interface {
	// Get returns the tenant's status; tenants with no decision are 'legacy'.
	Get(ctx context.Context, tenantID string) (repository.LegacyCutoverStatus, error)
	// Set applies a validated transition (shadow -> cut_over -> rolled_back).
	Set(ctx context.Context, tenantID string, status repository.LegacyCutoverStatus, note string) (*repository.LegacyCutover, error)
	// CleanupReadiness returns the P8-05 checklist for removing the legacy
	// write path. Read-only; never deletes.
	CleanupReadiness(ctx context.Context, tenantID string) ([]repository.CleanupReadinessItem, bool, error)
	// IsCutOver implements the facade guard contract (P8-04): true when the
	// tenant's legacy surface must answer "moved". Checker outages report an
	// error and the guard fails open.
	IsCutOver(ctx context.Context, tenantID string) (bool, error)
}

type legacyCutoverService struct {
	cutover     repository.LegacyCutoverRepository
	cleanup     repository.LegacyCleanupRepository
	audit       AuditLedgerService
	actor       func(ctx context.Context) string
	rollbackWindow time.Duration
}

func NewLegacyCutoverService(cutover repository.LegacyCutoverRepository,
	cleanup repository.LegacyCleanupRepository, audit AuditLedgerService) LegacyCutoverService {
	return &legacyCutoverService{cutover: cutover, cleanup: cleanup, audit: audit,
		actor: func(ctx context.Context) string {
			actor, ok := ActorFrom(ctx)
			if !ok {
				return ""
			}
			return actor
		}, rollbackWindow: 30 * 24 * time.Hour}
}

func (s *legacyCutoverService) IsCutOver(ctx context.Context, tenantID string) (bool, error) {
	status, err := s.Get(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return status == repository.LegacyCutoverStatusCutOver, nil
}

func (s *legacyCutoverService) Get(ctx context.Context, tenantID string) (repository.LegacyCutoverStatus, error) {
	entry, err := s.cutover.GetCutover(ctx, tenantID)
	if err != nil {
		if err == repository.ErrNotFound {
			return repository.LegacyCutoverStatusLegacy, nil
		}
		return "", apperr.Internal("cutover.lookup_failed", "failed to read the cutover registry").Wrap(err)
	}
	return entry.Status, nil
}

func (s *legacyCutoverService) Set(ctx context.Context, tenantID string,
	status repository.LegacyCutoverStatus, note string) (*repository.LegacyCutover, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, apperr.BadRequest("cutover.tenant_required", "tenant_id is required")
	}
	current, err := s.Get(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !repository.ValidCutoverTransition(current, status) {
		return nil, apperr.Conflict("cutover.invalid_transition",
			"cannot move tenant from "+string(current)+" to "+string(status))
	}
	entry := &repository.LegacyCutover{TenantID: tenantID, Status: status, DecidedBy: s.actor(ctx), Note: note}
	if err := s.cutover.SetCutover(ctx, entry); err != nil {
		return nil, apperr.Internal("cutover.save_failed", "failed to record the cutover decision").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: idgen.NewID(), ActorUID: entry.DecidedBy,
			TenantID: tenantID, Action: "legacy.cutover.set", ResourceType: "tenant", ResourceID: tenantID,
			Payload: map[string]any{"status": string(status), "previous": string(current), "note": note}})
	}
	return entry, nil
}

func (s *legacyCutoverService) CleanupReadiness(ctx context.Context, tenantID string) ([]repository.CleanupReadinessItem, bool, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, false, apperr.BadRequest("cutover.tenant_required", "tenant_id is required")
	}
	items, err := s.cleanup.CleanupReadiness(ctx, tenantID)
	if err != nil {
		return nil, false, apperr.Internal("cutover.readiness_failed", "failed to evaluate cleanup readiness").Wrap(err)
	}
	// Evaluate the rollback-window item against the service policy. If the
	// repository did not include the item (fakes or older deployments), append
	// the evaluated decision so the checklist is always complete.
	entry, getErr := s.cutover.GetCutover(ctx, tenantID)
	windowReady := false
	windowDetails := "no cutover decision recorded for tenant"
	if getErr == nil && entry != nil && entry.Status == repository.LegacyCutoverStatusCutOver {
		elapsed := time.Since(entry.DecidedAt)
		windowReady = elapsed >= s.rollbackWindow
		windowDetails = "cut_over " + elapsed.Round(time.Hour).String() + " ago; window " + s.rollbackWindow.String()
	}
	foundWindow := false
	ready := true
	for i := range items {
		if items[i].Check == "rollback_window_elapsed" {
			foundWindow = true
			items[i].Ready = windowReady
			items[i].Details = windowDetails
		}
		if !items[i].Ready {
			ready = false
		}
	}
	if !foundWindow {
		items = append(items, repository.CleanupReadinessItem{
			Check: "rollback_window_elapsed", Ready: windowReady, Details: windowDetails,
		})
	}
	if !windowReady {
		ready = false
	}
	return items, ready, nil
}


