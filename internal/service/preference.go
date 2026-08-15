package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// PreferenceService exposes the notification-preference CRUD surface. Users
// read and write their own preferences; there is no admin override.
type PreferenceService interface {
	// List returns every preference row for the authenticated actor.
	List(ctx context.Context) ([]*domain.NotificationPreference, error)
	// Set replaces one or more preference rows for the authenticated actor.
	// Unknown notification types are silently accepted (the known-type list is
	// a moving target, not a server-side enum).
	Set(ctx context.Context, prefs []*domain.NotificationPreference) error
}

// ScopeRef identifies a resource scope for preference lookups.
// An empty Type signals the global (unscoped) preference.
type ScopeRef struct {
	Type string // "file", "conversation", or "" (global)
	ID   string // the scoped resource id; ignored when Type is ""
}

// PreferenceChecker is the narrow read-only view used by the
// NotificationDispatcher to decide whether to write a notification row for a
// given recipient+type combination. A nil checker means all notifications pass.
type PreferenceChecker interface {
	// IsAllowed reports whether the recipient's preferences allow a notification
	// of the given type. When the recipient has no preference row for that type
	// (or no preferences at all) the answer is true (backward-compatible default).
	// Optional scopes narrow the lookup to a specific resource (e.g. file, conversation);
	// if a scoped preference exists it is used; otherwise falls back to global.
	IsAllowed(ctx context.Context, recipientUID, notificationType string, scopes ...ScopeRef) (bool, error)
}

type preferenceService struct {
	prefs repository.PreferenceRepository
	audit AuditLogger // nil => audit inactive
}

func NewPreferenceService(prefs repository.PreferenceRepository, opts ...func(*preferenceService)) PreferenceService {
	s := &preferenceService{prefs: prefs}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithPreferenceAuditLogger wires an AuditLogger into the preference service.
func WithPreferenceAuditLogger(a AuditLogger) func(*preferenceService) {
	return func(s *preferenceService) { s.audit = a }
}

// compile-time assertions
var _ PreferenceService = (*preferenceService)(nil)

func (s *preferenceService) List(ctx context.Context) ([]*domain.NotificationPreference, error) {
	uid, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.prefs.ListAll(ctx, uid)
	if err != nil {
		return nil, apperr.Internal("preference.list_failed", "failed to list notification preferences").Wrap(err)
	}
	return out, nil
}

func (s *preferenceService) Set(ctx context.Context, prefs []*domain.NotificationPreference) error {
	uid, err := mustActor(ctx)
	if err != nil {
		return err
	}
	for i := range prefs {
		prefs[i].UID = uid
		// Strip timezone to IANA name; reject empty only when quiet hours are set.
		if prefs[i].Timezone == "" {
			prefs[i].Timezone = "UTC"
		}
		if prefs[i].QuietHoursStart != "" || prefs[i].QuietHoursEnd != "" {
			if _, err := time.Parse("15:04", prefs[i].QuietHoursStart); err != nil {
				return apperr.BadRequest("preference.invalid_quiet_hours", "quiet_hours_start must be HH:MM")
			}
			if _, err := time.Parse("15:04", prefs[i].QuietHoursEnd); err != nil {
				return apperr.BadRequest("preference.invalid_quiet_hours", "quiet_hours_end must be HH:MM")
			}
		}
	}
	if err := s.prefs.Upsert(ctx, prefs); err != nil {
		return apperr.Internal("preference.upsert_failed", "failed to save notification preferences").Wrap(err)
	}
	return s.auditPreferenceSet(ctx, uid, prefs)
}

// auditPreferenceSet records the preference.set fact with a deterministic
// anchor (retry dedup; distinct payloads stay distinct). Returns nil when no
// audit logger is wired (nil => audit inactive).
//
// audit-site: standalone-fact — preference.set, deterministic anchor.
func (s *preferenceService) auditPreferenceSet(ctx context.Context, uid string, prefs []*domain.NotificationPreference) error {
	if s.audit == nil {
		return nil
	}
	if err := s.audit.Log(ctx, &domain.AuditEvent{ID: preferenceSetEventID(TenantFrom(ctx), uid, prefs),
		ActorUID: uid, Action: domain.AuditPreferenceSet, SubjectType: "preference", SubjectID: uid,
		Meta: map[string]any{"count": len(prefs)}}); err != nil {
		return apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)
	}
	return nil
}

// preferenceSetEventID derives the deterministic EventID of a preference.set
// fact: a retry of the same payload re-derives the same ID (ledger dedup via
// (tenant, source, event_id)), while a distinct payload derives a distinct ID
// so a later legitimate Set is never silently dropped (the multi-occurrence
// compliance invariant). A 4-part (action, tenant, uid) anchor without the
// digest was rejected: it would dedup every later Set by the same user
// regardless of content.
func preferenceSetEventID(tenantID, uid string, prefs []*domain.NotificationPreference) domain.ID {
	return idgen.Anchor("audit", "preference.set", tenantID, uid, preferenceSetDigest(prefs))
}

// preferenceSetDigest is a sha256 over the canonical JSON of the settable
// preference fields (stable struct field order). UID is excluded (it is
// already an anchor part) and CreatedAt/UpdatedAt are excluded so a retry of
// the same payload stays byte-identical. Any change to a settable field
// changes the digest.
func preferenceSetDigest(prefs []*domain.NotificationPreference) string {
	type digestRow struct {
		NotificationType string
		ScopeType        string
		ScopeID          string
		Enabled          bool
		QuietHoursStart  string
		QuietHoursEnd    string
		Timezone         string
	}
	rows := make([]digestRow, 0, len(prefs))
	for _, p := range prefs {
		rows = append(rows, digestRow{NotificationType: p.NotificationType, ScopeType: p.ScopeType,
			ScopeID: p.ScopeID, Enabled: p.Enabled, QuietHoursStart: p.QuietHoursStart,
			QuietHoursEnd: p.QuietHoursEnd, Timezone: p.Timezone})
	}
	raw, _ := json.Marshal(rows)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// prefChecker wraps a PreferenceRepository to implement PreferenceChecker.
type prefChecker struct {
	prefs repository.PreferenceRepository
}

func NewPreferenceChecker(prefs repository.PreferenceRepository) PreferenceChecker {
	return &prefChecker{prefs: prefs}
}

func (c *prefChecker) IsAllowed(ctx context.Context, recipientUID, notificationType string, scopes ...ScopeRef) (bool, error) {
	// When scopes are provided, try the scoped preference first.
	if len(scopes) > 0 && scopes[0].Type != "" {
		p, err := c.prefs.GetScoped(ctx, recipientUID, notificationType, scopes[0].Type, scopes[0].ID)
		if err != nil {
			return false, fmt.Errorf("preference check (scoped): %w", err)
		}
		if p != nil {
			return p.Enabled && checkQuietHours(p), nil
		}
		// No scoped row: fall through to global check.
	}
	p, err := c.prefs.Get(ctx, recipientUID, notificationType)
	if err != nil {
		return false, fmt.Errorf("preference check: %w", err)
	}
	if p == nil {
		return true, nil // no row = enabled by default
	}
	return p.Enabled && checkQuietHours(p), nil
}

// checkQuietHours reports whether the current time is outside the quiet-hours
// window defined in p. When quiet hours are not set (both start and end are
// empty) it always returns true (not in quiet hours).
func checkQuietHours(p *domain.NotificationPreference) bool {
	if p.QuietHoursStart == "" || p.QuietHoursEnd == "" {
		return true // no quiet hours configured
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	start, _ := time.Parse("15:04", p.QuietHoursStart)
	end, _ := time.Parse("15:04", p.QuietHoursEnd)
	nowTime := time.Date(0, 1, 1, now.Hour(), now.Minute(), 0, 0, time.UTC)
	startTime := time.Date(0, 1, 1, start.Hour(), start.Minute(), 0, 0, time.UTC)
	endTime := time.Date(0, 1, 1, end.Hour(), end.Minute(), 0, 0, time.UTC)
	if startTime.Before(endTime) {
		// Normal range: quiet hours from start to end.
		// Not quiet = before start OR after end.
		return nowTime.Before(startTime) || nowTime.After(endTime)
	}
	// Wraps past midnight: quiet hours from start to midnight AND from midnight to end.
	// Not quiet = after end AND before start.
	return nowTime.After(endTime) && nowTime.Before(startTime)
}
