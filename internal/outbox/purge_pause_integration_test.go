//go:build integration

package outbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/repository/testpg"
)

// TestOutboxRepo_PurgeDeadLettersPauseGate (T-5.2 mandatory): the destructive
// SQL boundary is certified against real Postgres — a paused tick deletes
// zero rows; the unpaused twin deletes exactly the cutoff-eligible rows and
// nothing else. tickOnce is the package-internal seam the loop drives.
func TestOutboxRepo_PurgeDeadLettersPauseGate(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	repo := NewGormRepository(db)

	seed := func(dead bool, age time.Duration) *Event {
		e := &Event{ID: uuid.New(), AggregateType: "message", AggregateID: uuid.New().String(),
			EventType: "message.sent", Payload: []byte(`{"k":"v"}`), CreatedAt: time.Now().UTC().Add(-age)}
		if !dead {
			published := time.Now().UTC().Add(-age)
			e.PublishedAt = &published
		}
		if err := repo.Append(ctx, db, e); err != nil {
			t.Fatal(err)
		}
		// Append persists the row without the dead-letter flag (the relay's
		// MarkFailed sets it); mark it the same way for the fixture.
		if dead {
			if err := db.Exec(`UPDATE outbox_events SET dead_letter = true WHERE id = ?`, e.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
		return e
	}
	oldDead := seed(true, 10*24*time.Hour) // dead letter past dead-letter cutoff
	inRetDead := seed(true, time.Hour)     // dead letter inside cutoff
	oldPub := seed(false, 40*24*time.Hour) // published past published cutoff
	live := seed(false, time.Hour)         // published inside cutoff

	count := func() (dead, published int64) {
		t.Helper()
		if err := db.Raw(`SELECT count(*) FROM outbox_events WHERE dead_letter = true`).Scan(&dead).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Raw(`SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`).Scan(&published).Error; err != nil {
			t.Fatal(err)
		}
		return dead, published
	}
	fixed := time.Now().UTC().Truncate(time.Second)
	if err := db.Exec(`UPDATE outbox_events SET created_at = ? WHERE id = ?`, fixed.Add(-10*24*time.Hour), oldDead.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE outbox_events SET created_at = ? WHERE id = ?`, fixed.Add(-40*24*time.Hour), oldPub.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dead, pub := count(); dead != 2 || pub != 2 {
		t.Fatalf("seed counts = %d/%d, want 2/2", dead, pub)
	}

	// Paused tick: no purge work, zero rows deleted.
	paused := &outboxCleaner{repo: repo, deadLetterRetention: 7 * 24 * time.Hour,
		publishedRetention: 30 * 24 * time.Hour, purgePaused: true}
	deleted, err := tickOnce(ctx, paused, fixed)
	if err != nil || deleted != 0 {
		t.Fatalf("paused tick = %d, %v; want 0, nil", deleted, err)
	}
	if dead, pub := count(); dead != 2 || pub != 2 {
		t.Fatalf("paused tick mutated rows: dead/pub = %d/%d, want 2/2", dead, pub)
	}

	// Unpaused twin: exactly the two past-retention rows are deleted.
	unpaused := &outboxCleaner{repo: repo, deadLetterRetention: 7 * 24 * time.Hour,
		publishedRetention: 30 * 24 * time.Hour}
	deleted, err = tickOnce(ctx, unpaused, fixed)
	if err != nil || deleted != 2 {
		t.Fatalf("unpaused tick = %d, %v; want 2, nil", deleted, err)
	}
	if dead, pub := count(); dead != 1 || pub != 1 {
		t.Fatalf("unpaused tick counts = %d/%d, want 1/1 (in-retention and live kept)", dead, pub)
	}
	var remaining int64
	if err := db.Raw(`SELECT count(*) FROM outbox_events WHERE id IN (?, ?)`, inRetDead.ID, live.ID).Scan(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("in-retention rows = %d, want 2", remaining)
	}
}
