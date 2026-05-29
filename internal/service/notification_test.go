package service_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
	"github.com/aero/aero-id/internal/sso"
)

func event(t *testing.T, eventType string, payload map[string]any) *outbox.Event {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &outbox.Event{ID: idgen.NewID(), EventType: eventType, Payload: b}
}

func TestNotificationDispatcher_MessageSentNotifiesRecipient(t *testing.T) {
	var created *domain.Notification
	repo := &mocks.NotificationRepo{CreateFn: func(_ context.Context, n *domain.Notification) error {
		created = n
		return nil
	}}
	d := service.NewNotificationDispatcher(repo)
	e := event(t, "message.sent", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "b"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("expected a notification to be created")
	}
	if created.RecipientUID != "b" || created.ActorUID != "a" || created.Type != "message.received" {
		t.Fatalf("unexpected notification: %+v", created)
	}
	if created.SubjectType != "message" || created.SubjectID != "m1" {
		t.Fatalf("unexpected subject: %+v", created)
	}
}

func TestNotificationDispatcher_SelfMessageCreatesNone(t *testing.T) {
	called := false
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error {
		called = true
		return nil
	}}
	d := service.NewNotificationDispatcher(repo)
	e := event(t, "message.sent", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "a"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("self-message must not create a notification")
	}
}

func TestNotificationDispatcher_CommentRepliedNotifiesParentAuthor(t *testing.T) {
	var created *domain.Notification
	repo := &mocks.NotificationRepo{CreateFn: func(_ context.Context, n *domain.Notification) error {
		created = n
		return nil
	}}
	d := service.NewNotificationDispatcher(repo)
	e := event(t, "comment.replied", map[string]any{"comment_id": "c1", "parent_author_uid": "author"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if created == nil || created.RecipientUID != "author" || created.Type != "comment.reply" {
		t.Fatalf("unexpected notification: %+v", created)
	}
	if created.SubjectType != "comment" || created.SubjectID != "c1" {
		t.Fatalf("unexpected subject: %+v", created)
	}
}

func TestNotificationDispatcher_CommentSelfReplySkipped(t *testing.T) {
	called := false
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error {
		called = true
		return nil
	}}
	d := service.NewNotificationDispatcher(repo)
	e := event(t, "comment.replied", map[string]any{"comment_id": "c1", "parent_author_uid": "author", "replier_uid": "author"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("self-reply must not create a notification")
	}
}

func TestNotificationDispatcher_MalformedPayloadIsNoOp(t *testing.T) {
	called := false
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error {
		called = true
		return nil
	}}
	d := service.NewNotificationDispatcher(repo)
	bad := &outbox.Event{ID: idgen.NewID(), EventType: "message.sent", Payload: []byte("{not json")}
	if err := d.Handle(context.Background(), bad); err != nil {
		t.Fatalf("malformed payload must not error: %v", err)
	}
	if called {
		t.Fatal("malformed payload must not create a notification")
	}
	// Unknown event types are a no-op too.
	if err := d.Handle(context.Background(), event(t, "file.deleted", map[string]any{"x": 1})); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("unknown event must not create a notification")
	}
}

func TestNotificationService_RequiresActor(t *testing.T) {
	svc := service.NewNotificationService(&mocks.NotificationRepo{}, &mocks.UserDir{})
	if _, err := svc.List(context.Background(), domain.Page{}); err == nil {
		t.Fatal("expected unauthorized without actor")
	}
}

func TestNotificationService_ListEnrichesActors(t *testing.T) {
	repo := &mocks.NotificationRepo{ListByRecipientFn: func(_ context.Context, uid string, _ domain.Page) ([]*domain.Notification, string, error) {
		if uid != "me" {
			t.Fatalf("expected recipient me, got %q", uid)
		}
		return []*domain.Notification{{ID: idgen.NewID(), RecipientUID: "me", ActorUID: "a", Type: "message.received"}}, "next", nil
	}}
	var asked []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		asked = uids
		return map[string]sso.User{"a": {UID: "a", Nickname: "Ann"}}, nil
	}}
	svc := service.NewNotificationService(repo, dir)
	ctx := service.WithActor(context.Background(), "me")
	list, err := svc.List(ctx, domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if list.NextCursor != "next" || len(list.Notifications) != 1 {
		t.Fatalf("unexpected list: %+v", list)
	}
	if len(asked) != 1 || asked[0] != "a" || list.Users["a"].Nickname != "Ann" {
		t.Fatalf("expected actor a enriched, asked=%v users=%v", asked, list.Users)
	}
}
