//go:build integration

package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// These tests cover the two newest repo features (message_reactions / audit_events,
// migrations 000013 / 000014) whose raw GORM struct<->table mappings had NO
// integration coverage. A column or schema mismatch in those mappings only ever
// surfaces against a real database, so we exercise them here.

func TestMessageReactionsRepo(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	repo := repository.NewMessageRepository(db, outbox.NewGormRepository(db), repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))

	// A reaction row FK-references messages(id), so seed a real message first.
	msg := &domain.Message{
		ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
		Kind: domain.MessageKindText, Content: "react to me", CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, msg, nil); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	countReactions := func(uid string) int64 {
		t.Helper()
		q := db.Table("message_reactions").Where("message_id = ?", msg.ID)
		if uid != "" {
			q = q.Where("uid = ?", uid)
		}
		var n int64
		if err := q.Count(&n).Error; err != nil {
			t.Fatalf("count reactions: %v", err)
		}
		return n
	}

	// React, then verify exactly one row maps into message_reactions.
	if err := repo.ReactToMessage(ctx, msg.ID, "bob"); err != nil {
		t.Fatalf("react: %v", err)
	}
	if got := countReactions("bob"); got != 1 {
		t.Fatalf("after react: bob rows = %d, want 1", got)
	}

	// Reacting again is idempotent (ON CONFLICT DO NOTHING on the (message_id, uid) PK).
	if err := repo.ReactToMessage(ctx, msg.ID, "bob"); err != nil {
		t.Fatalf("duplicate react: %v", err)
	}
	if got := countReactions("bob"); got != 1 {
		t.Fatalf("after duplicate react: bob rows = %d, want 1", got)
	}

	// A different user's reaction is an additional row.
	if err := repo.ReactToMessage(ctx, msg.ID, "alice"); err != nil {
		t.Fatalf("second reactor: %v", err)
	}
	if got := countReactions(""); got != 2 {
		t.Fatalf("total reactions = %d, want 2", got)
	}

	// Reacting to a message that does not exist is rejected before any insert.
	if err := repo.ReactToMessage(ctx, idgen.NewID(), "bob"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("react to missing message: err = %v, want ErrNotFound", err)
	}

	// Unreact removes only that (message, uid) row; repeating it is a no-op.
	if err := repo.UnreactMessage(ctx, msg.ID, "bob"); err != nil {
		t.Fatalf("unreact: %v", err)
	}
	if got := countReactions("bob"); got != 0 {
		t.Fatalf("after unreact: bob rows = %d, want 0", got)
	}
	if got := countReactions(""); got != 1 {
		t.Fatalf("after unreact: total rows = %d, want 1 (alice remains)", got)
	}
	if err := repo.UnreactMessage(ctx, msg.ID, "bob"); err != nil {
		t.Fatalf("idempotent unreact: %v", err)
	}
}

func TestAuditRepo_AppendAndLog(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	repo := repository.NewAuditRepository(db)

	countEvents := func(action string) int64 {
		t.Helper()
		var n int64
		if err := db.Table("audit_events").Where("action = ?", action).Count(&n).Error; err != nil {
			t.Fatalf("count audit events: %v", err)
		}
		return n
	}

	// Append an event carrying a Meta map — the exact shape commentService.ResolveReport
	// now emits — to prove the JSONB column mapping round-trips against real Postgres.
	ev := &domain.AuditEvent{
		ActorUID: "u1", Action: domain.AuditReportResolve,
		SubjectType: "comment_report", SubjectID: idgen.NewID().String(),
		TargetUID: "u2",
		Meta:      map[string]any{"comment_id": "c-123", "status": "resolved"},
	}
	if err := repo.Append(ctx, ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Append fills in a generated id + timestamp when unset.
	if ev.ID == (uuid.UUID{}) {
		t.Fatal("append did not assign an id")
	}
	if ev.CreatedAt.IsZero() {
		t.Fatal("append did not stamp created_at")
	}
	if got := countEvents(domain.AuditReportResolve); got != 1 {
		t.Fatalf("resolve events = %d, want 1", got)
	}
	// Verify the JSONB Meta actually persisted and is queryable.
	var status string
	if err := db.Raw(
		`SELECT meta->>'status' FROM audit_events WHERE id = ?`, ev.ID,
	).Scan(&status).Error; err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if status != "resolved" {
		t.Fatalf("meta.status = %q, want \"resolved\"", status)
	}

	// Log is the AuditLogger-facing alias for Append; it must persist too, including
	// an event with no Meta (the meta column is nullable).
	if err := repo.Log(ctx, &domain.AuditEvent{
		ActorUID: "u1", Action: domain.AuditUserBlock,
		SubjectType: "user", SubjectID: "u2", TargetUID: "u2",
	}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if got := countEvents(domain.AuditUserBlock); got != 1 {
		t.Fatalf("block events = %d, want 1", got)
	}
}
