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

