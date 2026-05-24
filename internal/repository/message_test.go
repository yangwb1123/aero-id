//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func setupMsgRepo(t *testing.T) repository.MessageRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewMessageRepository(db)
}

func TestMessageRepo_CreateAndRead(t *testing.T) {
	ctx := context.Background()
	repo := setupMsgRepo(t)

	now := time.Now().UTC().Truncate(time.Microsecond)
	m := &domain.Message{
		ID: idgen.NewID(), FromUID: "alice", ToUID: "bob",
		Kind: domain.MessageKindText, Content: "hi", CreatedAt: now,
	}
	if err := repo.Create(ctx, m); err != nil {
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
		}); err != nil {
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
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	conv, _, err := repo.ListConversation(ctx, "a", "b", domain.Page{Limit: 10})
	if err != nil || len(conv) != 2 {
		t.Fatalf("conv len=%d err=%v", len(conv), err)
	}
}
