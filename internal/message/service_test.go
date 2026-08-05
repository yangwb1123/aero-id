package message_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/message"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestMessageService_SendRequiresToUID(t *testing.T) {
	svc := message.NewService(&mockMessageRepo{}, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, message.SendMessageInput{Content: "hi"})
	assertAppCode(t, err, 400)
}

func TestMessageService_SendDefaultsKindAndPersists(t *testing.T) {
	var saved *domain.Message
	var evt *outbox.Event
	repo := &mockMessageRepo{CreateFn: func(_ context.Context, m *domain.Message, e *outbox.Event) error {
		saved = m
		evt = e
		return nil
	}}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	m, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if m.FromUID != "a" || m.ToUID != "b" || m.Kind != domain.MessageKindText {
		t.Fatalf("unexpected message: %+v", m)
	}
	if saved == nil {
		t.Fatal("repo.Create was not called")
	}
	if evt == nil || evt.EventType != "message.sent" || evt.AggregateType != "message" {
		t.Fatalf("expected message.sent event, got %+v", evt)
	}
}

func TestMessageService_SendRejectsForeignFileAttachment(t *testing.T) {
	fid := idgen.NewID()
	files := &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "someone-else"}, nil
	}}
	svc := message.NewService(&mockMessageRepo{}, files, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
	assertAppCode(t, err, 403)
}

func TestMessageService_SendRejectsMissingFile(t *testing.T) {
	fid := idgen.NewID()
	files := &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return nil, domain.ErrNotFound
	}}
	svc := message.NewService(&mockMessageRepo{}, files, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
	assertAppCode(t, err, 400)
}

func TestMessageService_SendRejectsUnknownKind(t *testing.T) {
	svc := message.NewService(&mockMessageRepo{}, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Kind: domain.MessageKind(99), Content: "hi"})
	assertAppCode(t, err, 400)
}

func TestMessageService_SendRejectsUserSystemKind(t *testing.T) {
	svc := message.NewService(&mockMessageRepo{}, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Kind: domain.MessageKindSystem, Content: "hi"})
	assertAppCode(t, err, 403)
}

func TestMessageService_SendFileKindRequiresFileOwnership(t *testing.T) {
	fid := idgen.NewID()
	files := &mockFileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "a"}, nil
	}}
	var saved *domain.Message
	repo := &mockMessageRepo{CreateFn: func(_ context.Context, m *domain.Message, _ *outbox.Event) error {
		saved = m
		return nil
	}}
	svc := message.NewService(repo, files, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	m, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || m.Kind != domain.MessageKindFile || m.FileID == nil {
		t.Fatalf("unexpected file message: %+v", m)
	}
}

func TestMessageService_SendSystemCreatesSystemKind(t *testing.T) {
	var saved *domain.Message
	var evt *outbox.Event
	repo := &mockMessageRepo{CreateFn: func(_ context.Context, m *domain.Message, e *outbox.Event) error {
		saved = m
		evt = e
		return nil
	}}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	m, err := svc.SendSystem(context.Background(), "b", "welcome", map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil {
		t.Fatal("repo.Create was not called")
	}
	if m.Kind != domain.MessageKindSystem {
		t.Fatalf("kind = %d, want system", m.Kind)
	}
	if m.FromUID != domain.SystemSenderUID {
		t.Fatalf("from_uid = %q, want %q", m.FromUID, domain.SystemSenderUID)
	}
	if m.ToUID != "b" || m.Content != "welcome" {
		t.Fatalf("unexpected system message: %+v", m)
	}
	if evt == nil || evt.EventType != "message.sent" {
		t.Fatalf("expected message.sent event, got %+v", evt)
	}
}

func TestMessageService_SendSystemRequiresToAndContent(t *testing.T) {
	svc := message.NewService(&mockMessageRepo{}, &mockFileRepo{}, &mockUserDir{})
	if _, err := svc.SendSystem(context.Background(), "", "hi", nil); err == nil {
		t.Fatal("expected error for empty to_uid")
	} else {
		assertAppCode(t, err, 400)
	}
	if _, err := svc.SendSystem(context.Background(), "b", "", nil); err == nil {
		t.Fatal("expected error for empty content")
	} else {
		assertAppCode(t, err, 400)
	}
}

func TestMessageService_SendBlockedRecipientForbidden(t *testing.T) {
	blocks := &mockBlockChecker{IsBlockedFn: func(_ context.Context, blocker, blocked string) (bool, error) {
		return blocker == "b" && blocked == "a", nil
	}}
	created := false
	repo := &mockMessageRepo{CreateFn: func(context.Context, *domain.Message, *outbox.Event) error {
		created = true
		return nil
	}}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{}, message.WithBlockChecker(blocks))
	ctx := domain.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Content: "hi"})
	assertAppCode(t, err, 403)
	if created {
		t.Fatal("blocked message must not be persisted")
	}
}

func TestMessageService_SendAllowedWhenNotBlocked(t *testing.T) {
	blocks := &mockBlockChecker{IsBlockedFn: func(context.Context, string, string) (bool, error) {
		return false, nil
	}}
	var saved *domain.Message
	repo := &mockMessageRepo{CreateFn: func(_ context.Context, m *domain.Message, _ *outbox.Event) error {
		saved = m
		return nil
	}}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{}, message.WithBlockChecker(blocks))
	ctx := domain.WithActor(context.Background(), "a")
	if _, err := svc.Send(ctx, message.SendMessageInput{ToUID: "b", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if saved == nil {
		t.Fatal("message must be persisted when not blocked")
	}
}

func TestMessageService_GetForbiddenForNonParticipant(t *testing.T) {
	repo := &mockMessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "c")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestMessageService_MarkReadOnlyRecipient(t *testing.T) {
	repo := &mockMessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	err := svc.MarkRead(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestMessageService_MarkReadEmitsOutboxEvent(t *testing.T) {
	id := idgen.NewID()
	var capturedEvt *outbox.Event
	repo := &mockMessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "sender", ToUID: "reader"}, nil
		},
		MarkReadWithEventFn: func(_ context.Context, _ domain.ID, _ time.Time, evt *outbox.Event) error {
			capturedEvt = evt
			return nil
		},
	}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "reader")
	if err := svc.MarkRead(ctx, id); err != nil {
		t.Fatal(err)
	}
	if capturedEvt == nil {
		t.Fatal("MarkReadWithEvent was not called")
	}
	if capturedEvt.EventType != "message.read" {
		t.Fatalf("event type = %q, want message.read", capturedEvt.EventType)
	}
	if capturedEvt.AggregateType != "message" {
		t.Fatalf("aggregate type = %q, want message", capturedEvt.AggregateType)
	}
	var payload map[string]any
	if err := json.Unmarshal(capturedEvt.Payload, &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if payload["from_uid"] != "sender" {
		t.Fatalf("from_uid = %v, want sender", payload["from_uid"])
	}
	if payload["to_uid"] != "reader" {
		t.Fatalf("to_uid = %v, want reader", payload["to_uid"])
	}
}

func TestMessageService_MarkReadIdempotent(t *testing.T) {
	now := time.Now()
	called := false
	repo := &mockMessageRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: id, FromUID: "a", ToUID: "b", ReadAt: &now}, nil
		},
		MarkReadFn: func(_ context.Context, _ domain.ID, _ time.Time) error { called = true; return nil },
	}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "b")
	if err := svc.MarkRead(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("MarkRead repo must not be called when already read")
	}
}

func TestMessageService_EditBySenderPersists(t *testing.T) {
	id := idgen.NewID()
	var gotContent string
	var stamped bool
	repo := &mockMessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b", Kind: domain.MessageKindText, Content: "old"}, nil
		},
		UpdateContentFn: func(_ context.Context, _ domain.ID, content string, at time.Time) error {
			gotContent = content
			stamped = !at.IsZero()
			return nil
		},
	}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "a")
	m, err := svc.Edit(ctx, id, "new")
	if err != nil {
		t.Fatal(err)
	}
	if gotContent != "new" || m.Content != "new" {
		t.Fatalf("content not updated: repo=%q msg=%q", gotContent, m.Content)
	}
	if !stamped || m.EditedAt == nil {
		t.Fatalf("edited_at not stamped: stamped=%v editedAt=%v", stamped, m.EditedAt)
	}
}

func TestMessageService_EditByNonSenderForbidden(t *testing.T) {
	repo := &mockMessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b", Kind: domain.MessageKindText}, nil
	}}
	svc := message.NewService(repo, &mockFileRepo{}, &mockUserDir{})
	ctx := domain.WithActor(context.Background(), "b")
	_, err := svc.Edit(ctx, idgen.NewID(), "new")
	assertAppCode(t, err, 403)
}
