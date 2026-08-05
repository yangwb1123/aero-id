//go:build integration

package notification_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/notification"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func startPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db
}

func TestNotificationRepo_CreateAndList(t *testing.T) {
	db := startPostgres(t)
	repo := notification.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	n := &domain.Notification{
		ID:           domain.ID{},
		RecipientUID: "alice",
		Type:         "message.received",
		ActorUID:     "bob",
		SubjectType:  "message",
		SubjectID:    "msg-1",
		CreatedAt:    now,
	}
	if err := repo.Create(ctx, n); err != nil {
		t.Fatal(err)
	}

	list, _, err := repo.ListByRecipient(ctx, "alice", domain.Page{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(list))
	}
	if list[0].Type != "message.received" {
		t.Errorf("type = %s", list[0].Type)
	}
}

func TestNotificationRepo_MarkRead(t *testing.T) {
	db := startPostgres(t)
	repo := notification.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	n := &domain.Notification{
		ID:           domain.ID{},
		RecipientUID: "alice",
		Type:         "comment.reply",
		ActorUID:     "carol",
		SubjectType:  "comment",
		SubjectID:    "cmt-1",
		CreatedAt:    now,
	}
	if err := repo.Create(ctx, n); err != nil {
		t.Fatal(err)
	}

	count, err := repo.CountUnread(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 unread, got %d", count)
	}

	if err := repo.MarkRead(ctx, n.ID, now); err != nil {
		t.Fatal(err)
	}

	count, err = repo.CountUnread(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unread after mark read, got %d", count)
	}
}

func TestNotificationRepo_MarkAllRead(t *testing.T) {
	db := startPostgres(t)
	repo := notification.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		n := &domain.Notification{
			ID:           domain.ID{},
			RecipientUID: "alice",
			Type:         "message.received",
			ActorUID:     fmt.Sprintf("user%d", i),
			SubjectType:  "message",
			SubjectID:    fmt.Sprintf("msg-%d", i),
			CreatedAt:    now,
		}
		if err := repo.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	count, err := repo.CountUnread(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 unread, got %d", count)
	}

	if err := repo.MarkAllRead(ctx, "alice", now); err != nil {
		t.Fatal(err)
	}

	count, err = repo.CountUnread(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 unread after mark all read, got %d", count)
	}
}

func TestNotificationRepo_ListPagination(t *testing.T) {
	db := startPostgres(t)
	repo := notification.NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		n := &domain.Notification{
			ID:           domain.ID{},
			RecipientUID: "alice",
			Type:         "message.received",
			ActorUID:     fmt.Sprintf("user%d", i),
			SubjectType:  "message",
			SubjectID:    fmt.Sprintf("msg-%d", i),
			CreatedAt:    now.Add(time.Duration(i) * time.Second),
		}
		if err := repo.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	list, cursor, err := repo.ListByRecipient(ctx, "alice", domain.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("page size = %d, want 2", len(list))
	}
	if cursor == "" {
		t.Fatal("expected cursor for more pages")
	}

	list2, cursor2, err := repo.ListByRecipient(ctx, "alice", domain.Page{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(list2) != 2 {
		t.Fatalf("page size = %d, want 2", len(list2))
	}
	if cursor2 == "" {
		t.Fatal("expected cursor for more pages")
	}
}
