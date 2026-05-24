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
	unread, _, _ := repo.ListInbox(ctx, "bob", domain.Page{}, true)
	if len(unread) != 0 {
		t.Fatalf("expected 0 unread, got %d", len(unread))
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
		_ = repo.Create(ctx, &domain.Message{
			ID: idgen.NewID(), FromUID: p.from, ToUID: p.to,
			Kind: domain.MessageKindText, Content: "x", CreatedAt: now,
		})
		time.Sleep(2 * time.Millisecond)
	}
	conv, _, err := repo.ListConversation(ctx, "a", "b", domain.Page{Limit: 10})
	if err != nil || len(conv) != 2 {
		t.Fatalf("conv len=%d err=%v", len(conv), err)
	}
}
