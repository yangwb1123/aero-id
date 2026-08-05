package notification_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/notification"
	"github.com/aero/aero-id/internal/outbox"
)

type mockNotifRepo struct {
	CreateFn func(ctx context.Context, n *domain.Notification) error
}

func (m *mockNotifRepo) Create(ctx context.Context, n *domain.Notification) error {
	if m.CreateFn != nil {
		return m.CreateFn(ctx, n)
	}
	return nil
}
func (m *mockNotifRepo) ListByRecipient(_ context.Context, _ string, _ domain.Page) ([]*domain.Notification, string, error) {
	return nil, "", nil
}
func (m *mockNotifRepo) MarkRead(_ context.Context, _ domain.ID, _ time.Time) error { return nil }
func (m *mockNotifRepo) MarkAllRead(_ context.Context, _ string, _ time.Time) error { return nil }
func (m *mockNotifRepo) CountUnread(_ context.Context, _ string) (int64, error)     { return 0, nil }

type mockBlockChecker struct {
	IsBlockedFn func(ctx context.Context, blocker, blocked string) (bool, error)
}

func (m *mockBlockChecker) IsBlocked(ctx context.Context, blocker, blocked string) (bool, error) {
	if m.IsBlockedFn != nil {
		return m.IsBlockedFn(ctx, blocker, blocked)
	}
	return false, nil
}

type mockPusher struct {
	PublishFn func(uid, eventType, data string)
	calls     []struct{ uid, eventType, data string }
}

func (m *mockPusher) Publish(uid, eventType, data string) {
	m.calls = append(m.calls, struct{ uid, eventType, data string }{uid, eventType, data})
	if m.PublishFn != nil {
		m.PublishFn(uid, eventType, data)
	}
}

func newEvent(eventType string, payload any) *outbox.Event {
	b, _ := json.Marshal(payload)
	return &outbox.Event{
		ID:        uuid.Must(uuid.NewV7()),
		EventType: eventType,
		Payload:   b,
		CreatedAt: time.Now().UTC(),
	}
}

func TestDispatcher_MessageSentNotifiesRecipient(t *testing.T) {
	repo := &mockNotifRepo{CreateFn: func(_ context.Context, n *domain.Notification) error {
		if n.RecipientUID != "recipient" {
			t.Fatalf("recipient = %q, want %q", n.RecipientUID, "recipient")
		}
		if n.Type != "message.received" {
			t.Fatalf("type = %q, want %q", n.Type, "message.received")
		}
		return nil
	}}
	d := notification.NewDispatcher(repo)
	e := newEvent("message.sent", map[string]any{"message_id": "m1", "from_uid": "sender", "to_uid": "recipient"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_SelfMessageCreatesNone(t *testing.T) {
	repo := &mockNotifRepo{CreateFn: func(_ context.Context, _ *domain.Notification) error {
		t.Fatal("should not create notification for self-message")
		return nil
	}}
	d := notification.NewDispatcher(repo)
	e := newEvent("message.sent", map[string]any{"message_id": "m1", "from_uid": "me", "to_uid": "me"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_MessageReadNotifiesSender(t *testing.T) {
	repo := &mockNotifRepo{CreateFn: func(_ context.Context, n *domain.Notification) error {
		if n.RecipientUID != "sender" {
			t.Fatalf("recipient = %q, want %q", n.RecipientUID, "sender")
		}
		if n.Type != "message.read_receipt" {
			t.Fatalf("type = %q, want %q", n.Type, "message.read_receipt")
		}
		return nil
	}}
	d := notification.NewDispatcher(repo)
	e := newEvent("message.read", map[string]any{"message_id": "m1", "from_uid": "sender", "to_uid": "reader"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_CommentRepliedNotifiesParentAuthor(t *testing.T) {
	repo := &mockNotifRepo{CreateFn: func(_ context.Context, n *domain.Notification) error {
		if n.RecipientUID != "parent-author" {
			t.Fatalf("recipient = %q, want %q", n.RecipientUID, "parent-author")
		}
		if n.Type != "comment.reply" {
			t.Fatalf("type = %q, want %q", n.Type, "comment.reply")
		}
		return nil
	}}
	d := notification.NewDispatcher(repo)
	e := newEvent("comment.replied", map[string]any{"comment_id": "c1", "parent_author_uid": "parent-author"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_UnknownEventIsNoOp(t *testing.T) {
	repo := &mockNotifRepo{CreateFn: func(_ context.Context, _ *domain.Notification) error {
		t.Fatal("should not create notification for unknown event")
		return nil
	}}
	d := notification.NewDispatcher(repo)
	e := newEvent("unknown.event", map[string]any{})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_MalformedPayloadIsNoOp(t *testing.T) {
	repo := &mockNotifRepo{CreateFn: func(_ context.Context, _ *domain.Notification) error {
		t.Fatal("should not create notification with malformed payload")
		return nil
	}}
	d := notification.NewDispatcher(repo)
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "message.sent", Payload: []byte("not json"), CreatedAt: time.Now()}
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_SkipsBlockedRecipient(t *testing.T) {
	repo := &mockNotifRepo{CreateFn: func(_ context.Context, _ *domain.Notification) error {
		t.Fatal("should skip blocked recipient")
		return nil
	}}
	blocks := &mockBlockChecker{IsBlockedFn: func(_ context.Context, blocker, blocked string) (bool, error) {
		return true, nil
	}}
	d := notification.NewDispatcher(repo, notification.WithDispatcherBlockChecker(blocks))
	e := newEvent("message.sent", map[string]any{"message_id": "m1", "from_uid": "sender", "to_uid": "recipient"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_PusherReceivesNotification(t *testing.T) {
	pusher := &mockPusher{}
	repo := &mockNotifRepo{}
	d := notification.NewDispatcher(repo, notification.WithDispatcherPusher(pusher))
	e := newEvent("message.sent", map[string]any{"message_id": "m1", "from_uid": "sender", "to_uid": "recipient"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if len(pusher.calls) != 1 {
		t.Fatalf("pusher calls = %d, want 1", len(pusher.calls))
	}
	if pusher.calls[0].eventType != "message.received" {
		t.Fatalf("pusher event = %q, want %q", pusher.calls[0].eventType, "message.received")
	}
}

func TestDispatcher_NoPusherStillSucceeds(t *testing.T) {
	repo := &mockNotifRepo{}
	d := notification.NewDispatcher(repo)
	e := newEvent("message.sent", map[string]any{"message_id": "m1", "from_uid": "sender", "to_uid": "recipient"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcher_BlockedRecipientNotPushed(t *testing.T) {
	pusher := &mockPusher{}
	repo := &mockNotifRepo{}
	blocks := &mockBlockChecker{IsBlockedFn: func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}}
	d := notification.NewDispatcher(repo,
		notification.WithDispatcherBlockChecker(blocks),
		notification.WithDispatcherPusher(pusher),
	)
	e := newEvent("message.sent", map[string]any{"message_id": "m1", "from_uid": "sender", "to_uid": "recipient"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if len(pusher.calls) != 0 {
		t.Fatal("pusher should not receive notification for blocked recipient")
	}
}
