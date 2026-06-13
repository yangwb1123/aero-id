//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// setupSearchRepos starts one throwaway Postgres, applies every migration (so the
// 000016 search_vector generated columns + GIN indexes exist), and returns the
// three repositories whose full-text Search* methods run raw tsvector SQL. A single
// container is shared across the subtests below to keep the suite fast.
func setupSearchRepos(t *testing.T) (repository.MessageRepository, repository.CommentRepository, repository.FileRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewMessageRepository(db, outbox.NewGormRepository(db)),
		repository.NewCommentRepository(db, outbox.NewGormRepository(db)),
		repository.NewFileRepository(db)
}

// TestSearchRepo_FullText exercises the migration-000016 full-text search path
// against real Postgres: that plainto_tsquery actually matches the generated
// tsvector columns, and — the security-critical invariant — that every Search*
// method scopes results to the caller so one user can never see another's data.
func TestSearchRepo_FullText(t *testing.T) {
	ctx := context.Background()
	msgRepo, commentRepo, fileRepo := setupSearchRepos(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// ---- messages: scoped to participants (from_uid = ? OR to_uid = ?) ----
	mk := func(from, to, content string) {
		t.Helper()
		m := &domain.Message{
			ID: idgen.NewID(), FromUID: from, ToUID: to,
			Kind: domain.MessageKindText, Content: content, CreatedAt: now,
		}
		if err := msgRepo.Create(ctx, m, nil); err != nil {
			t.Fatalf("seed message %s->%s: %v", from, to, err)
		}
	}
	mk("alice", "bob", "the peregrine falcon dives fast")   // alice is a participant
	mk("bob", "alice", "another peregrine sighting today")  // alice is a participant
	mk("carol", "dave", "peregrine nesting on the cliff")   // alice is NOT a participant

	t.Run("messages match and are participant-scoped", func(t *testing.T) {
		hits, err := msgRepo.SearchMessages(ctx, "alice", "peregrine", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 2 {
			t.Fatalf("alice should see exactly her 2 matching messages, got %d", len(hits))
		}
		// Data-leak guard: every hit must have alice as sender or recipient.
		for _, h := range hits {
			if h.FromUID != "alice" && h.ToUID != "alice" {
				t.Fatalf("leaked a message alice is not party to: from=%s to=%s", h.FromUID, h.ToUID)
			}
		}
	})

	t.Run("messages a user is not party to are invisible even on match", func(t *testing.T) {
		// carol↔dave's message contains "peregrine" too, but alice must never get it.
		hits, err := msgRepo.SearchMessages(ctx, "alice", "cliff", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 0 {
			t.Fatalf("alice must not see the carol↔dave message, got %d", len(hits))
		}
		// ...whereas a participant of that conversation can.
		hits, err = msgRepo.SearchMessages(ctx, "carol", "cliff", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 {
			t.Fatalf("carol should see her 1 matching message, got %d", len(hits))
		}
	})

	t.Run("no match returns empty without error", func(t *testing.T) {
		hits, err := msgRepo.SearchMessages(ctx, "alice", "supercalifragilistic", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 0 {
			t.Fatalf("expected no matches, got %d", len(hits))
		}
	})

	t.Run("soft-deleted messages are excluded", func(t *testing.T) {
		m := &domain.Message{
			ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
			Kind: domain.MessageKindText, Content: "ephemeral albatross note", CreatedAt: now,
		}
		if err := msgRepo.Create(ctx, m, nil); err != nil {
			t.Fatal(err)
		}
		if hits, err := msgRepo.SearchMessages(ctx, "alice", "albatross", 10); err != nil || len(hits) != 1 {
			t.Fatalf("pre-delete: hits=%d err=%v, want 1", len(hits), err)
		}
		if err := msgRepo.SoftDelete(ctx, m.ID); err != nil {
			t.Fatal(err)
		}
		if hits, err := msgRepo.SearchMessages(ctx, "alice", "albatross", 10); err != nil || len(hits) != 0 {
			t.Fatalf("post-delete: hits=%d err=%v, want 0 (deleted_at IS NULL filter)", len(hits), err)
		}
	})

	// ---- comments: scoped to owner_uid ----
	mkComment := func(owner, content string) {
		t.Helper()
		c := &domain.Comment{
			ID: idgen.NewID(), OwnerUID: owner, TargetType: domain.TargetTypeFile,
			TargetID: idgen.NewID(), Content: content, Status: domain.CommentStatusNormal,
			CreatedAt: now, UpdatedAt: now,
		}
		c.RootID = c.ID
		if err := commentRepo.Create(ctx, c); err != nil {
			t.Fatalf("seed comment for %s: %v", owner, err)
		}
	}
	mkComment("u1", "delicious mango chutney recipe")
	mkComment("u2", "mango trees in the orchard")

	t.Run("comments match and are owner-scoped", func(t *testing.T) {
		hits, err := commentRepo.SearchComments(ctx, "u1", "mango", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 {
			t.Fatalf("u1 should see only their own matching comment, got %d", len(hits))
		}
		if hits[0].OwnerUID != "u1" {
			t.Fatalf("leaked a comment owned by %s", hits[0].OwnerUID)
		}
	})

	// ---- files: scoped to owner_uid, matched on name ----
	mkFile := func(owner, name string) {
		t.Helper()
		f := &domain.File{
			ID: idgen.NewID(), OwnerUID: owner, Name: name, Size: 1,
			MimeType: "text/plain", StorageKey: owner + "/" + name, StorageType: "local",
			Visibility: domain.VisibilityPrivate, CreatedAt: now, UpdatedAt: now,
		}
		if err := fileRepo.Create(ctx, f); err != nil {
			t.Fatalf("seed file for %s: %v", owner, err)
		}
	}
	mkFile("u1", "quarterly tax summary")
	mkFile("u2", "quarterly travel log")

	t.Run("files match on name and are owner-scoped", func(t *testing.T) {
		hits, err := fileRepo.SearchFiles(ctx, "u1", "quarterly", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 {
			t.Fatalf("u1 should see only their own matching file, got %d", len(hits))
		}
		if hits[0].OwnerUID != "u1" || hits[0].Name != "quarterly tax summary" {
			t.Fatalf("unexpected file hit: owner=%s name=%q", hits[0].OwnerUID, hits[0].Name)
		}
	})
}
