package service_test

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

func TestBlockService_BlockSelfBadRequest(t *testing.T) {
	svc := service.NewBlockService(&mocks.BlockRepo{})
	ctx := service.WithActor(context.Background(), "a")
	err := svc.Block(ctx, "a")
	assertAppCode(t, err, 400)
}

func TestBlockService_BlockBlankUIDBadRequest(t *testing.T) {
	svc := service.NewBlockService(&mocks.BlockRepo{})
	ctx := service.WithActor(context.Background(), "a")
	assertAppCode(t, svc.Block(ctx, ""), 400)
	assertAppCode(t, svc.Unblock(ctx, ""), 400)
}

func TestBlockService_BlockRequiresActor(t *testing.T) {
	svc := service.NewBlockService(&mocks.BlockRepo{})
	if err := svc.Block(context.Background(), "b"); err == nil {
		t.Fatal("expected unauthorized without actor")
	}
}

func TestBlockService_BlockHappyPath(t *testing.T) {
	var gotBlocker, gotBlocked string
	repo := &mocks.BlockRepo{BlockFn: func(_ context.Context, blocker, blocked string) error {
		gotBlocker, gotBlocked = blocker, blocked
		return nil
	}}
	svc := service.NewBlockService(repo)
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Block(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if gotBlocker != "a" || gotBlocked != "b" {
		t.Fatalf("repo args = (blocker=%q, blocked=%q), want (a, b)", gotBlocker, gotBlocked)
	}
}

func TestBlockService_UnblockHappyPath(t *testing.T) {
	var gotBlocker, gotBlocked string
	repo := &mocks.BlockRepo{UnblockFn: func(_ context.Context, blocker, blocked string) error {
		gotBlocker, gotBlocked = blocker, blocked
		return nil
	}}
	svc := service.NewBlockService(repo)
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Unblock(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if gotBlocker != "a" || gotBlocked != "b" {
		t.Fatalf("repo args = (blocker=%q, blocked=%q), want (a, b)", gotBlocker, gotBlocked)
	}
}

func TestBlockService_BlockEmitsAuditEvent(t *testing.T) {
	var logged *domain.AuditEvent
	calls := 0
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		calls++
		return nil
	}}
	repo := &mocks.BlockRepo{BlockFn: func(context.Context, string, string) error { return nil }}
	svc := service.NewBlockService(repo, service.WithBlockAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Block(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("audit Log called %d times, want 1", calls)
	}
	if logged.Action != domain.AuditUserBlock {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditUserBlock)
	}
	if logged.ActorUID != "a" {
		t.Fatalf("actor = %q, want a", logged.ActorUID)
	}
	if logged.SubjectType != "user" || logged.SubjectID != "b" || logged.TargetUID != "b" {
		t.Fatalf("subject/target wrong: type=%q id=%q target=%q", logged.SubjectType, logged.SubjectID, logged.TargetUID)
	}
}

func TestBlockService_UnblockEmitsAuditEvent(t *testing.T) {
	var logged *domain.AuditEvent
	calls := 0
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		calls++
		return nil
	}}
	repo := &mocks.BlockRepo{UnblockFn: func(context.Context, string, string) error { return nil }}
	svc := service.NewBlockService(repo, service.WithBlockAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Unblock(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("audit Log called %d times, want 1", calls)
	}
	if logged.Action != domain.AuditUserUnblock {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditUserUnblock)
	}
	if logged.ActorUID != "a" {
		t.Fatalf("actor = %q, want a", logged.ActorUID)
	}
	if logged.SubjectType != "user" || logged.SubjectID != "b" || logged.TargetUID != "b" {
		t.Fatalf("subject/target wrong: type=%q id=%q target=%q", logged.SubjectType, logged.SubjectID, logged.TargetUID)
	}
}

func TestBlockService_ListBlockedHappyPath(t *testing.T) {
	repo := &mocks.BlockRepo{ListBlockedFn: func(_ context.Context, blocker string, _ domain.Page) ([]string, string, error) {
		if blocker != "a" {
			t.Fatalf("blocker = %q, want a", blocker)
		}
		return []string{"b", "c"}, "cur", nil
	}}
	svc := service.NewBlockService(repo)
	ctx := service.WithActor(context.Background(), "a")
	list, err := svc.ListBlocked(ctx, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.UIDs) != 2 || list.UIDs[0] != "b" || list.UIDs[1] != "c" {
		t.Fatalf("uids = %v, want [b c]", list.UIDs)
	}
	if list.NextCursor != "cur" {
		t.Fatalf("cursor = %q, want cur", list.NextCursor)
	}
}
