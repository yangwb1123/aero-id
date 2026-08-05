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

func TestNotificationDispatcher_MessageReadNotifiesSender(t *testing.T) {
	var created *domain.Notification
	repo := &mocks.NotificationRepo{CreateFn: func(_ context.Context, n *domain.Notification) error {
		created = n
		return nil
	}}
	d := service.NewNotificationDispatcher(repo)
	e := event(t, "message.read", map[string]any{"message_id": "m1", "from_uid": "sender", "to_uid": "reader"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if created == nil {
		t.Fatal("expected a read-receipt notification to be created")
	}
	if created.RecipientUID != "sender" {
		t.Fatalf("recipient = %q, want sender", created.RecipientUID)
	}
	if created.ActorUID != "reader" {
		t.Fatalf("actor = %q, want reader", created.ActorUID)
	}
	if created.Type != "message.read_receipt" {
		t.Fatalf("type = %q, want message.read_receipt", created.Type)
	}
	if created.SubjectType != "message" || created.SubjectID != "m1" {
		t.Fatalf("unexpected subject: type=%q id=%q", created.SubjectType, created.SubjectID)
	}
}

func TestNotificationDispatcher_MessageReadSelfReadSkipped(t *testing.T) {
	called := false
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error {
		called = true
		return nil
	}}
	d := service.NewNotificationDispatcher(repo)
	// from_uid == to_uid: self-read (e.g. sender reads their own sent message)
	e := event(t, "message.read", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "a"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("self-read must not create a notification")
	}
}

func TestNotificationDispatcher_MessageReadBlockedSenderSkipped(t *testing.T) {
	called := false
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error {
		called = true
		return nil
	}}
	// sender "a" has blocked reader "b": IsBlocked(blocker=a, blocked=b) == true.
	// The sender (recipient of the read-receipt) has blocked the actor (reader),
	// so no notification is delivered.
	blocks := &mocks.BlockRepo{IsBlockedFn: func(_ context.Context, blocker, blocked string) (bool, error) {
		return blocker == "a" && blocked == "b", nil
	}}
	d := service.NewNotificationDispatcher(repo, service.WithDispatcherBlockChecker(blocks))
	e := event(t, "message.read", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "b"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("blocked sender must not get a read-receipt notification")
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

func TestNotificationDispatcher_SkipsBlockedRecipient(t *testing.T) {
	called := false
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error {
		called = true
		return nil
	}}
	// Recipient "b" has blocked actor "a": IsBlocked(blocker=b, blocked=a) == true.
	blocks := &mocks.BlockRepo{IsBlockedFn: func(_ context.Context, blocker, blocked string) (bool, error) {
		return blocker == "b" && blocked == "a", nil
	}}
	d := service.NewNotificationDispatcher(repo, service.WithDispatcherBlockChecker(blocks))
	e := event(t, "message.sent", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "b"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("blocked recipient must not get a notification")
	}
}

func TestNotificationDispatcher_NotifiesNonBlockedRecipient(t *testing.T) {
	var created *domain.Notification
	repo := &mocks.NotificationRepo{CreateFn: func(_ context.Context, n *domain.Notification) error {
		created = n
		return nil
	}}
	blocks := &mocks.BlockRepo{IsBlockedFn: func(context.Context, string, string) (bool, error) {
		return false, nil
	}}
	d := service.NewNotificationDispatcher(repo, service.WithDispatcherBlockChecker(blocks))
	e := event(t, "message.sent", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "b"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if created == nil || created.RecipientUID != "b" || created.ActorUID != "a" {
		t.Fatalf("expected notification for b about a, got %+v", created)
	}
}

func TestNotificationDispatcher_PusherReceivesNotification(t *testing.T) {
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error { return nil }}
	var gotUID, gotType, gotData string
	pushes := 0
	pusher := &mocks.Pusher{PublishFn: func(uid, eventType, data string, _ int64) {
		gotUID, gotType, gotData = uid, eventType, data
		pushes++
	}}
	d := service.NewNotificationDispatcher(repo, service.WithDispatcherPusher(pusher))
	e := event(t, "message.sent", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "b"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if pushes != 1 {
		t.Fatalf("Publish called %d times, want 1", pushes)
	}
	// Recipient uid is the push target; the notification type is the SSE event type.
	if gotUID != "b" {
		t.Fatalf("push uid = %q, want b (recipient)", gotUID)
	}
	if gotType != "message.received" {
		t.Fatalf("push event type = %q, want message.received", gotType)
	}
	if gotData == "" {
		t.Fatal("push payload must be non-empty")
	}
	// The payload is the JSON-encoded notification; it must round-trip.
	var n domain.Notification
	if err := json.Unmarshal([]byte(gotData), &n); err != nil {
		t.Fatalf("push payload not valid notification JSON: %v", err)
	}
	if n.RecipientUID != "b" {
		t.Fatalf("payload recipient = %q, want b", n.RecipientUID)
	}
}

func TestNotificationDispatcher_NoPusherStillSucceeds(t *testing.T) {
	created := false
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error {
		created = true
		return nil
	}}
	// No WithDispatcherPusher: Handle must not panic and must still create the row.
	d := service.NewNotificationDispatcher(repo)
	e := event(t, "message.sent", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "b"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("notification must still be created without a pusher")
	}
}

func TestNotificationDispatcher_BlockedRecipientNotPushed(t *testing.T) {
	repo := &mocks.NotificationRepo{CreateFn: func(context.Context, *domain.Notification) error { return nil }}
	pushed := false
	pusher := &mocks.Pusher{PublishFn: func(string, string, string, int64) { pushed = true }}
	// Recipient "b" has blocked actor "a": the notification is suppressed entirely,
	// so the pusher must not fire either.
	blocks := &mocks.BlockRepo{IsBlockedFn: func(_ context.Context, blocker, blocked string) (bool, error) {
		return blocker == "b" && blocked == "a", nil
	}}
	d := service.NewNotificationDispatcher(repo, service.WithDispatcherBlockChecker(blocks), service.WithDispatcherPusher(pusher))
	e := event(t, "message.sent", map[string]any{"message_id": "m1", "from_uid": "a", "to_uid": "b"})
	if err := d.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if pushed {
		t.Fatal("a suppressed (blocked) notification must not be pushed")
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

func TestNotificationRateLimiter_AllowsUpToRate(t *testing.T) {
	rl := service.NewNotifRateLimiterForTest(3) // max 3 per minute
	uid, ntype := "bob", "message.received"

	for i := 0; i < 3; i++ {
		if !rl.Allow(uid, ntype) {
			t.Fatalf("call %d should be allowed", i+1)
		}
	}
	if rl.Allow(uid, ntype) {
		t.Fatal("4th call should be denied (rate exceeded)")
	}
}

func TestNotificationRateLimiter_DifferentTypesSeparate(t *testing.T) {
	rl := service.NewNotifRateLimiterForTest(2)
	uid := "bob"

	if !rl.Allow(uid, "message.received") {
		t.Fatal("message.received should be allowed")
	}
	if !rl.Allow(uid, "comment.reply") {
		t.Fatal("comment.reply should be allowed (separate counter)")
	}
	if !rl.Allow(uid, "message.received") {
		t.Fatal("message.received 2nd should be allowed (rate=2)")
	}
	if rl.Allow(uid, "message.received") {
		t.Fatal("message.received 3rd should be denied")
	}
}

func TestNotificationRateLimiter_DifferentRecipientsSeparate(t *testing.T) {
	rl := service.NewNotifRateLimiterForTest(1)
	if !rl.Allow("alice", "message.received") {
		t.Fatal("alice should be allowed")
	}
	if !rl.Allow("bob", "message.received") {
		t.Fatal("bob should be allowed (separate counter)")
	}
	if rl.Allow("alice", "message.received") {
		t.Fatal("alice 2nd should be denied")
	}
}
