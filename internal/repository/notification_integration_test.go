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

func setupNotificationRepo(t *testing.T) repository.NotificationRepository {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return repository.NewNotificationRepository(db)
}

func TestNotificationRepo_CreateListCountRead(t *testing.T) {
	ctx := context.Background()
	repo := setupNotificationRepo(t)
	now := time.Now().UTC()

	mk := func(recipient string) *domain.Notification {
		return &domain.Notification{
			ID: idgen.NewID(), RecipientUID: recipient, Type: "message.received",
			ActorUID: "alice", SubjectType: "message", SubjectID: "m1", CreatedAt: now,
		}
	}
	for _, r := range []string{"bob", "bob", "carol"} { // bob: 2, carol: 1
		if err := repo.Create(ctx, mk(r)); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := repo.CountUnread(ctx, "bob"); err != nil || n != 2 {
		t.Fatalf("bob unread = %d err=%v, want 2", n, err)
	}

	list, _, err := repo.ListByRecipient(ctx, "bob", domain.Page{Limit: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("bob list len=%d err=%v, want 2", len(list), err)
	}

	if err := repo.MarkRead(ctx, list[0].ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.CountUnread(ctx, "bob"); err != nil || n != 1 {
		t.Fatalf("bob unread after read = %d err=%v, want 1", n, err)
	}

	if err := repo.MarkAllRead(ctx, "bob", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.CountUnread(ctx, "bob"); err != nil || n != 0 {
		t.Fatalf("bob unread after mark-all = %d err=%v, want 0", n, err)
	}

	// Other recipients are untouched.
	if n, err := repo.CountUnread(ctx, "carol"); err != nil || n != 1 {
		t.Fatalf("carol unread = %d err=%v, want 1", n, err)
	}
	if n, err := repo.CountUnread(ctx, "nobody"); err != nil || n != 0 {
		t.Fatalf("nobody unread = %d err=%v, want 0", n, err)
	}
}

func TestNotificationRepo_ListPagination(t *testing.T) {
	ctx := context.Background()
	repo := setupNotificationRepo(t)

	for i := 0; i < 3; i++ {
		if err := repo.Create(ctx, &domain.Notification{
			ID: idgen.NewID(), RecipientUID: "dan", Type: "message.received",
			SubjectType: "message", SubjectID: "m", CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	page1, cursor1, err := repo.ListByRecipient(ctx, "dan", domain.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || cursor1 == "" {
		t.Fatalf("page1 len=%d cursor=%q, want 2 and non-empty cursor", len(page1), cursor1)
	}

	page2, cursor2, err := repo.ListByRecipient(ctx, "dan", domain.Page{Limit: 2, Cursor: cursor1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Fatalf("page2 len=%d cursor=%q, want 1 and empty cursor", len(page2), cursor2)
	}
}
