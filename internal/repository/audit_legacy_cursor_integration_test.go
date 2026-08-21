//go:build integration

package repository_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestLegacyAuditListsUseCreatedAtAndIDCursor(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	audit := repository.NewAuditRepository(db)
	whenNew := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	whenOld := whenNew.Add(-time.Minute)
	newID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	oldID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	for _, event := range []*domain.AuditEvent{
		{ID: newID, ActorUID: "u1", Action: "new", SubjectType: "file", SubjectID: "new", CreatedAt: whenNew},
		{ID: oldID, ActorUID: "u1", Action: "old", SubjectType: "file", SubjectID: "old", CreatedAt: whenOld},
	} {
		if err := audit.Append(t.Context(), event); err != nil {
			t.Fatalf("append legacy audit event: %v", err)
		}
	}
	first, cursor, err := audit.List(t.Context(), "", "", domain.Page{Limit: 1})
	if err != nil {
		t.Fatalf("first audit page: %v", err)
	}
	if len(first) != 1 || first[0].ID != newID || cursor == "" {
		t.Fatalf("first page = %#v cursor=%q", first, cursor)
	}
	second, next, err := audit.List(t.Context(), "", "", domain.Page{Limit: 1, Cursor: cursor})
	if err != nil {
		t.Fatalf("second audit page: %v", err)
	}
	if len(second) != 1 || second[0].ID != oldID || next != "" {
		t.Fatalf("second page = %#v cursor=%q; compound cursor skipped older row", second, next)
	}

	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	admin := repository.NewAdminRepository(db, ledger)
	adminFirst, adminCursor, err := admin.ListAudit(t.Context(), "", domain.Page{Limit: 1})
	if err != nil {
		t.Fatalf("first admin audit page: %v", err)
	}
	if len(adminFirst) != 1 || adminFirst[0].ID != newID || adminCursor == "" {
		t.Fatalf("first admin page = %#v cursor=%q", adminFirst, adminCursor)
	}
	adminSecond, adminNext, err := admin.ListAudit(t.Context(), "", domain.Page{Limit: 1, Cursor: adminCursor})
	if err != nil {
		t.Fatalf("second admin audit page: %v", err)
	}
	if len(adminSecond) != 1 || adminSecond[0].ID != oldID || adminNext != "" {
		t.Fatalf("second admin page = %#v cursor=%q; compound cursor skipped older row", adminSecond, adminNext)
	}
}
