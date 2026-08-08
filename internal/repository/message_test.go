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

func setupMsgRepo(t *testing.T) repository.MessageRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewMessageRepository(db, outbox.NewGormRepository(db), repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))
}

func TestMessageRepo_CountUnread(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)
	now := time.Now().UTC()

	mk := func(to string) *domain.Message {
		return &domain.Message{ID: idgen.NewID(), FromUID: "alice", ToUID: to, Kind: domain.MessageKindText, Content: "hi", CreatedAt: now}
	}
	for _, to := range []string{"bob", "bob", "carol"} { // bob: 2 unread, carol: 1
		if err := repo.Create(ctx, mk(to), nil); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := repo.CountUnread(ctx, "bob"); err != nil || n != 2 {
		t.Fatalf("bob unread = %d err=%v, want 2", n, err)
	}

	// Reading one of bob's messages drops his unread count to 1.
	inbox, _, err := repo.ListInbox(ctx, "bob", domain.Page{Limit: 10}, false)
	if err != nil || len(inbox) == 0 {
		t.Fatalf("inbox err=%v len=%d", err, len(inbox))
	}
	if err := repo.MarkRead(ctx, inbox[0].ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.CountUnread(ctx, "bob"); err != nil || n != 1 {
		t.Fatalf("bob unread after read = %d err=%v, want 1", n, err)
	}
	// A user with no messages has zero unread.
	if n, err := repo.CountUnread(ctx, "nobody"); err != nil || n != 0 {
		t.Fatalf("nobody unread = %d err=%v, want 0", n, err)
	}
}

func TestMessageRepo_CreateAndRead(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	m := &domain.Message{
		ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
		Kind: domain.MessageKindText, Content: "hi", CreatedAt: now,
	}
	if err := repo.Create(ctx, m, nil); err != nil {
		t.Fatal(err)
	}

	inbox, _, err := repo.ListInbox(ctx, "bob", domain.Page{}, true)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox len=%d err=%v", len(inbox), err)
	}

	if err := repo.MarkRead(ctx, m.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	unread, _, err2 := repo.ListInbox(ctx, "bob", domain.Page{}, true)
	if err2 != nil {
		t.Fatal(err2)
	}
	if len(unread) != 0 {
		t.Fatalf("expected 0 unread, got %d", len(unread))
	}
}

func TestMessageRepo_ListInboxPagination(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	for i := 0; i < 3; i++ {
		if err := repo.Create(ctx, &domain.Message{
			ID: idgen.NewID(), FromUID: "sender", ToUID: "carol",
			Kind: domain.MessageKindText, Content: "msg",
			CreatedAt: time.Now().UTC(),
		}, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	page1, cursor1, err := repo.ListInbox(ctx, "carol", domain.Page{Limit: 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || cursor1 == "" {
		t.Fatalf("page1 len=%d cursor=%q, want 2 messages and non-empty cursor", len(page1), cursor1)
	}

	page2, cursor2, err := repo.ListInbox(ctx, "carol", domain.Page{Limit: 2, Cursor: cursor1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("page2 len=%d cursor=%q, want 1 message and empty cursor", len(page2), cursor2)
	}
}

func TestMessageRepo_Conversation(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	now := time.Now().UTC()
	pairs := []struct{ from, to string }{
		{"a", "b"}, {"b", "a"}, {"a", "c"},
	}
	for _, p := range pairs {
		if err := repo.Create(ctx, &domain.Message{
			ID: idgen.NewID(), FromUID: p.from, ToUID: p.to,
			Kind: domain.MessageKindText, Content: "x", CreatedAt: now,
		}, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	conv, _, err := repo.ListConversation(ctx, "a", "b", domain.Page{Limit: 10})
	if err != nil || len(conv) != 2 {
		t.Fatalf("conv len=%d err=%v", len(conv), err)
	}
}

func TestMessageRepo_UpdateContent(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	m := &domain.Message{
		ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
		Kind: domain.MessageKindText, Content: "old", CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, m, nil); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.UpdateContent(ctx, m.ID, "new", at); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "new" {
		t.Fatalf("content = %q, want new", got.Content)
	}
	if got.EditedAt == nil {
		t.Fatal("edited_at was not stamped")
	}
}

func TestMessageRepo_SoftDeleteExcludedFromList(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	m := &domain.Message{
		ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
		Kind: domain.MessageKindText, Content: "hi", CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, m, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDelete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	// Soft-deleted rows must be excluded from inbox listings and Get.
	inbox, _, err := repo.ListInbox(ctx, "bob", domain.Page{Limit: 10}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Fatalf("expected 0 messages after soft delete, got %d", len(inbox))
	}
	if _, err := repo.Get(ctx, m.ID); err != repository.ErrNotFound {
		t.Fatalf("Get after soft delete = %v, want ErrNotFound", err)
	}
	// SoftDelete is idempotent.
	if err := repo.SoftDelete(ctx, m.ID); err != nil {
		t.Fatalf("second SoftDelete should be a no-op, got %v", err)
	}
}

func TestMessageRepo_GetIncludingDeleted(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	m := &domain.Message{
		ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
		Kind: domain.MessageKindText, Content: "hi", CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, m, nil); err != nil {
		t.Fatal(err)
	}

	// Live row: both Get and GetIncludingDeleted return it.
	live, err := repo.GetIncludingDeleted(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetIncludingDeleted on live row: %v", err)
	}
	if live == nil || live.FromUID != "alice" || live.ToUID != "bob" {
		t.Fatalf("unexpected live row: %+v", live)
	}

	// After the soft delete, scoped Get misses but GetIncludingDeleted still
	// returns the row — the distinction the delete retry path relies on.
	if err := repo.SoftDelete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, m.ID); err != repository.ErrNotFound {
		t.Fatalf("Get after soft delete = %v, want ErrNotFound", err)
	}
	deleted, err := repo.GetIncludingDeleted(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetIncludingDeleted after soft delete: %v", err)
	}
	if deleted == nil || deleted.ID != m.ID || deleted.FromUID != "alice" {
		t.Fatalf("soft-deleted row not returned: %+v", deleted)
	}

	// A never-existing id stays ErrNotFound in both scopes.
	if _, err := repo.GetIncludingDeleted(ctx, idgen.NewID()); err != repository.ErrNotFound {
		t.Fatalf("GetIncludingDeleted on missing id = %v, want ErrNotFound", err)
	}
}

func TestMessageRepo_MarkConversationRead(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)
	now := time.Now().UTC()

	// Two messages bob->alice (the target conversation) and one carol->alice (unrelated).
	for _, p := range []struct{ from, to string }{
		{"bob", "alice"}, {"bob", "alice"}, {"carol", "alice"},
	} {
		if err := repo.Create(ctx, &domain.Message{
			ID: idgen.NewID(), FromUID: p.from, ToUID: p.to,
			Kind: domain.MessageKindText, Content: "x", CreatedAt: now,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.MarkConversationRead(ctx, "alice", "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Only carol's message remains unread for alice.
	if n, err := repo.CountUnread(ctx, "alice"); err != nil || n != 1 {
		t.Fatalf("alice unread = %d err=%v, want 1", n, err)
	}
}
