package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type replayCursorAudit struct {
	filter  domain.AuditFactFilter
	records int
}

func (a *replayCursorAudit) Record(context.Context, AuditFactInput) error {
	a.records++
	return nil
}

func (*replayCursorAudit) Log(context.Context, *domain.AuditEvent) error { return nil }

func (a *replayCursorAudit) List(_ context.Context,
	filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	a.filter = filter
	return []*domain.AuditFact{{Action: "account.viewed"}}, "opaque-window-3", nil
}

func (*replayCursorAudit) VerifyChain(context.Context, string, string) error { return nil }

type replayCursorPermission struct{}

func (replayCursorPermission) HasPermission(context.Context, string, string) (bool, error) {
	return true, nil
}

func replayCursorContext() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-a"})
}

func TestReplayForensicPassesOpaqueCursorToAuditQuery(t *testing.T) {
	audit := &replayCursorAudit{}
	service := NewReplayService(audit, nil, WithReplayPermission(replayCursorPermission{}))
	accountID := idgen.NewID()
	from := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	result, err := service.Create(replayCursorContext(), ReplayInput{Mode: "forensic", AccountID: accountID,
		From: &from, To: &to, Cursor: " opaque-window-2 ", Limit: 25})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if audit.filter.Page.Cursor != "opaque-window-2" || audit.filter.Page.Limit != 25 {
		t.Fatalf("page = %#v", audit.filter.Page)
	}
	if audit.filter.From != &from || audit.filter.To != &to {
		t.Fatalf("time window = %v to %v", audit.filter.From, audit.filter.To)
	}
	if result.NextCursor != "opaque-window-3" || len(result.Facts) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestReplayDryRunPassesOpaqueCursorAndAuditsWindow(t *testing.T) {
	audit := &replayCursorAudit{}
	service := NewReplayService(audit, nil, WithReplayPermission(replayCursorPermission{}))

	result, err := service.Create(replayCursorContext(), ReplayInput{Mode: "dry-run", AccountID: idgen.NewID(),
		Cursor: "opaque-window-2", Limit: 10})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if audit.filter.Page.Cursor != "opaque-window-2" {
		t.Fatalf("cursor = %q", audit.filter.Page.Cursor)
	}
	if audit.records != 1 || result.NextCursor != "opaque-window-3" {
		t.Fatalf("records = %d, result = %#v", audit.records, result)
	}
}

func TestReplayControlledRejectsCursorBeforeCreatingOperation(t *testing.T) {
	service := NewReplayService(&replayCursorAudit{}, nil, WithReplayPermission(replayCursorPermission{}))

	_, err := service.Create(replayCursorContext(), ReplayInput{Mode: "controlled", Cursor: "opaque-window-2"})
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != "replay.cursor_not_allowed" {
		t.Fatalf("error = %v, want replay.cursor_not_allowed", err)
	}
}

func TestReplayRejectsOversizedCursor(t *testing.T) {
	service := NewReplayService(&replayCursorAudit{}, nil, WithReplayPermission(replayCursorPermission{}))

	_, err := service.Create(replayCursorContext(), ReplayInput{Mode: "forensic", Cursor: strings.Repeat("x", 513)})
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != "replay.invalid_cursor" {
		t.Fatalf("error = %v, want replay.invalid_cursor", err)
	}
}

func TestReplayRejectsInvalidTimeRange(t *testing.T) {
	service := NewReplayService(&replayCursorAudit{}, nil, WithReplayPermission(replayCursorPermission{}))
	from := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	to := from

	_, err := service.Create(replayCursorContext(), ReplayInput{Mode: "forensic", From: &from, To: &to})
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != "replay.invalid_time_range" {
		t.Fatalf("error = %v, want replay.invalid_time_range", err)
	}
}

func TestReplayControlledRejectsTimeFilters(t *testing.T) {
	service := NewReplayService(&replayCursorAudit{}, nil, WithReplayPermission(replayCursorPermission{}))
	from := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	_, err := service.Create(replayCursorContext(), ReplayInput{Mode: "controlled", From: &from})
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != "replay.filters_not_allowed" {
		t.Fatalf("error = %v, want replay.filters_not_allowed", err)
	}
}
