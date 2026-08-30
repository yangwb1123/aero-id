//go:build integration

package outbox_test

import (
	"context"
	stderrors "errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupOutboxRepo(t *testing.T) (*gorm.DB, *outbox.GormRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db, outbox.NewGormRepository(db)
}

func newEvent(eventType string) *outbox.Event {
	return &outbox.Event{
		ID:            uuid.New(),
		AggregateType: "message",
		AggregateID:   uuid.New().String(),
		EventType:     eventType,
		Payload:       []byte(`{"k":"v"}`),
		CreatedAt:     time.Now().UTC(),
	}
}

func TestOutboxRepo_AppendFetchMarkPublished(t *testing.T) {
	ctx := context.Background()
	db, repo := setupOutboxRepo(t)

	e := newEvent("message.sent")
	// Append within a caller-supplied transaction (mirrors how the message/comment
	// repos compose row writes with the outbox).
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return repo.Append(ctx, tx, e)
	}); err != nil {
		t.Fatalf("append in tx: %v", err)
	}

	got, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Event.ID != e.ID || got[0].Event.EventType != "message.sent" {
		t.Fatalf("claim batch = %+v, want the appended event", got)
	}
	wantNextAttempt := e.CreatedAt.Truncate(time.Microsecond)
	if !got[0].Event.NextAttemptAt.Equal(wantNextAttempt) {
		t.Fatalf("claimed next_attempt_at = %v, want append CreatedAt %v", got[0].Event.NextAttemptAt, wantNextAttempt)
	}

	if err := repo.MarkPublished(ctx, []outbox.Claim{got[0].Claim}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("after MarkPublished, unpublished = %+v, want none", got)
	}
}

func TestOutboxRepo_EligibilityStatsUsesDueAndLeasePredicates(t *testing.T) {
	ctx := context.Background()
	db, repo := setupOutboxRepo(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	due := newEvent("due")
	due.CreatedAt = now.Add(-time.Minute)
	due.NextAttemptAt = now.Add(-time.Second)
	future := newEvent("future")
	future.NextAttemptAt = now.Add(time.Hour)
	for _, e := range []*outbox.Event{due, future} {
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return repo.Append(ctx, tx, e)
		}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := repo.GetEligibilityStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.OldestCreatedAt == nil || stats.ObservedAt.IsZero() {
		t.Fatalf("eligibility stats = %+v, want one due event, oldest timestamp, and DB observation time", stats)
	}
	if !stats.OldestCreatedAt.Equal(due.CreatedAt) {
		t.Fatalf("oldest eligible = %v, want %v", *stats.OldestCreatedAt, due.CreatedAt)
	}
	claimed, err := repo.ClaimBatch(ctx, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Event.ID != due.ID {
		t.Fatalf("claim due events = %v, %v; want only due event", claimed, err)
	}
	stats, err = repo.GetEligibilityStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 || stats.OldestCreatedAt != nil {
		t.Fatalf("claimed eligibility stats = %+v, want empty", stats)
	}
}

func TestOutboxRepo_FailedTxRollsBackEvent(t *testing.T) {
	ctx := context.Background()
	db, repo := setupOutboxRepo(t)

	e := newEvent("message.sent")
	wantErr := stderrors.New("simulated downstream failure")
	// A transaction that appends the event then returns an error must roll back the
	// event row too — the outbox row shares the fate of the surrounding tx.
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := repo.Append(ctx, tx, e); err != nil {
			return err
		}
		return wantErr
	})
	if !stderrors.Is(err, wantErr) {
		t.Fatalf("tx err = %v, want %v", err, wantErr)
	}

	got, err := repo.ClaimBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("event must be rolled back with the tx, got %+v", got)
	}
}
