package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
	"github.com/aero/aero-id/internal/sso"
)

func TestMessageService_SendRequiresToUID(t *testing.T) {
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{Content: "hi"})
	assertAppCode(t, err, 400)
}

func TestMessageService_SendDefaultsKindAndPersists(t *testing.T) {
	var saved *domain.Message
	var evt *outbox.Event
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, m *domain.Message, e *outbox.Event) error {
		saved = m
		evt = e
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	m, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if m.FromUID != "a" || m.ToUID != "b" || m.Kind != domain.MessageKindText {
		t.Fatalf("unexpected message: %+v", m)
	}
	if saved == nil {
		t.Fatal("repo.Create was not called")
	}
	// Send must emit a "message.sent" outbox event transactionally with the insert.
	if evt == nil || evt.EventType != "message.sent" || evt.AggregateType != "message" {
		t.Fatalf("expected message.sent event, got %+v", evt)
	}
}

func TestMessageService_SendRejectsForeignFileAttachment(t *testing.T) {
	fid := idgen.NewID()
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "someone-else"}, nil
	}}
	svc := service.NewMessageService(&mocks.MessageRepo{}, files, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
	assertAppCode(t, err, 403)
}

func TestMessageService_SendRejectsMissingFile(t *testing.T) {
	fid := idgen.NewID()
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewMessageService(&mocks.MessageRepo{}, files, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
	assertAppCode(t, err, 400)
}

func TestMessageService_SendRejectsUnknownKind(t *testing.T) {
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Kind: domain.MessageKind(99), Content: "hi"})
	assertAppCode(t, err, 400)
}

func TestMessageService_SendRejectsUserSystemKind(t *testing.T) {
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Kind: domain.MessageKindSystem, Content: "hi"})
	assertAppCode(t, err, 403)
}

func TestMessageService_SendFileKindRequiresFileOwnership(t *testing.T) {
	// File-kind with a file_id owned by the actor still succeeds, unchanged from before.
	fid := idgen.NewID()
	files := &mocks.FileRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.File, error) {
		return &domain.File{OwnerUID: "a"}, nil
	}}
	var saved *domain.Message
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, m *domain.Message, _ *outbox.Event) error {
		saved = m
		return nil
	}}
	svc := service.NewMessageService(repo, files, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	m, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Kind: domain.MessageKindFile, FileID: &fid})
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
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, m *domain.Message, e *outbox.Event) error {
		saved = m
		evt = e
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	// No actor in context: SendSystem is an internal emit path.
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
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
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
	// Recipient "b" has blocked sender "a": IsBlocked(blocker=b, blocked=a) == true.
	blocks := &mocks.BlockRepo{IsBlockedFn: func(_ context.Context, blocker, blocked string) (bool, error) {
		return blocker == "b" && blocked == "a", nil
	}}
	created := false
	repo := &mocks.MessageRepo{CreateFn: func(context.Context, *domain.Message, *outbox.Event) error {
		created = true
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithBlockChecker(blocks))
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Content: "hi"})
	assertAppCode(t, err, 403)
	if created {
		t.Fatal("blocked message must not be persisted")
	}
}

func TestMessageService_SendAllowedWhenNotBlocked(t *testing.T) {
	blocks := &mocks.BlockRepo{IsBlockedFn: func(context.Context, string, string) (bool, error) {
		return false, nil
	}}
	var saved *domain.Message
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, m *domain.Message, _ *outbox.Event) error {
		saved = m
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithBlockChecker(blocks))
	ctx := service.WithActor(context.Background(), "a")
	if _, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if saved == nil {
		t.Fatal("message must be persisted when not blocked")
	}
}

func TestMessageService_GetForbiddenForNonParticipant(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "c")
	_, err := svc.Get(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestMessageService_MarkReadOnlyRecipient(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a") // sender, not recipient
	err := svc.MarkRead(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestMessageService_MarkReadEmitsOutboxEvent(t *testing.T) {
	id := idgen.NewID()
	var capturedEvt *outbox.Event
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "sender", ToUID: "reader"}, nil
		},
		MarkReadWithEventFn: func(_ context.Context, _ domain.ID, _ time.Time, evt *outbox.Event) error {
			capturedEvt = evt
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "reader")
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
	// Payload must carry from_uid (sender) and to_uid (reader).
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
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: id, FromUID: "a", ToUID: "b", ReadAt: &now}, nil
		},
		MarkReadFn: func(_ context.Context, _ domain.ID, _ time.Time) error { called = true; return nil },
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "b")
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
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b", Kind: domain.MessageKindText, Content: "old"}, nil
		},
		UpdateContentFn: func(_ context.Context, _ domain.ID, content string, at time.Time) error {
			gotContent = content
			stamped = !at.IsZero()
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
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
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b", Kind: domain.MessageKindText}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "b") // recipient, not sender
	_, err := svc.Edit(ctx, idgen.NewID(), "new")
	assertAppCode(t, err, 403)
}

func TestMessageService_EditEmptyContentBadRequest(t *testing.T) {
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Edit(ctx, idgen.NewID(), "")
	assertAppCode(t, err, 400)
}

func TestMessageService_EditSystemMessageForbidden(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b", Kind: domain.MessageKindSystem}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Edit(ctx, idgen.NewID(), "new")
	assertAppCode(t, err, 403)
}

func TestMessageService_DeleteBySender(t *testing.T) {
	called := false
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
		},
		SoftDeleteWithEventFn: func(_ context.Context, _ domain.ID, _ *outbox.Event) error { called = true; return nil },
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("SoftDeleteWithEvent was not called")
	}
}

func TestMessageService_DeleteByNonSenderForbidden(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, id domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: id, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "b") // recipient, not sender
	err := svc.Delete(ctx, idgen.NewID())
	assertAppCode(t, err, 403)
}

func TestMessageService_DeleteMissingIsIdempotent(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.Message, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatalf("expected idempotent no-op, got %v", err)
	}
}

func TestMessageService_MarkConversationReadCallsRepo(t *testing.T) {
	var gotTo, gotPeer string
	repo := &mocks.MessageRepo{MarkConversationReadFn: func(_ context.Context, toUID, peer string, at time.Time) error {
		gotTo = toUID
		gotPeer = peer
		if at.IsZero() {
			t.Error("at must be set")
		}
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "alice")
	if err := svc.MarkConversationRead(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if gotTo != "alice" || gotPeer != "bob" {
		t.Fatalf("repo args = (to=%q, peer=%q), want (alice, bob)", gotTo, gotPeer)
	}
}

func TestMessageService_MarkConversationReadEmptyPeer(t *testing.T) {
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "alice")
	err := svc.MarkConversationRead(ctx, "")
	assertAppCode(t, err, 400)
}

func TestMessageService_ConversationHappyPath(t *testing.T) {
	repo := &mocks.MessageRepo{
		ListConversationFn: func(_ context.Context, uidA, uidB string, _ domain.Page) ([]*domain.Message, string, error) {
			return []*domain.Message{{FromUID: uidA, ToUID: uidB}, {FromUID: uidB, ToUID: uidA}}, "cur", nil
		},
	}
	var requested []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		out := map[string]sso.User{}
		for _, u := range uids {
			out[u] = sso.User{UID: u}
		}
		return out, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, dir)
	ctx := service.WithActor(context.Background(), "alice")
	list, err := svc.Conversation(ctx, "bob", domain.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(list.Messages))
	}
	if list.NextCursor != "cur" {
		t.Fatalf("cursor = %q, want cur", list.NextCursor)
	}
	// Both actor and peer must be passed to the directory for enrichment.
	found := map[string]bool{}
	for _, u := range requested {
		found[u] = true
	}
	if !found["alice"] || !found["bob"] {
		t.Fatalf("enrichment uids = %v; want both alice and bob", requested)
	}
}

func TestMessageService_ConversationEmptyPeer(t *testing.T) {
	svc := service.NewMessageService(&mocks.MessageRepo{}, &mocks.FileRepo{}, &mocks.UserDir{})
	ctx := service.WithActor(context.Background(), "alice")
	_, err := svc.Conversation(ctx, "", domain.Page{})
	assertAppCode(t, err, 400)
}

func TestMessageService_InboxDedupesAndEnrichesSenders(t *testing.T) {
	repo := &mocks.MessageRepo{ListInboxFn: func(_ context.Context, to string, _ domain.Page, _ bool) ([]*domain.Message, string, error) {
		if to != "b" {
			t.Errorf("inbox owner = %q, want b", to)
		}
		return []*domain.Message{{FromUID: "a"}, {FromUID: "a"}, {FromUID: "c"}}, "", nil
	}}
	var requested []string
	dir := &mocks.UserDir{GetUsersFn: func(_ context.Context, uids []string) (map[string]sso.User, error) {
		requested = uids
		out := map[string]sso.User{}
		for _, u := range uids {
			out[u] = sso.User{UID: u, Nickname: "n-" + u}
		}
		return out, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, dir)
	ctx := service.WithActor(context.Background(), "b")
	list, err := svc.Inbox(ctx, domain.Page{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 {
		t.Fatalf("expected 2 deduped sender uids, got %v", requested)
	}
	if list.Users["a"].Nickname != "n-a" {
		t.Fatalf("missing enrichment for a: %+v", list.Users)
	}
}

func TestMessageService_ReactByParticipantCallsRepo(t *testing.T) {
	id := idgen.NewID()
	var gotID domain.ID
	var gotUID string
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		ReactToMessageFn: func(_ context.Context, messageID domain.ID, uid string) error {
			gotID = messageID
			gotUID = uid
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	// "b" is the recipient — a participant — so the reaction is allowed.
	if err := svc.ReactToMessage(context.Background(), id, "b"); err != nil {
		t.Fatal(err)
	}
	if gotID != id || gotUID != "b" {
		t.Fatalf("repo react args = (id=%v, uid=%q), want (%v, b)", gotID, gotUID, id)
	}
}

func TestMessageService_UnreactByParticipantCallsRepo(t *testing.T) {
	id := idgen.NewID()
	var gotID domain.ID
	var gotUID string
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		UnreactMessageFn: func(_ context.Context, messageID domain.ID, uid string) error {
			gotID = messageID
			gotUID = uid
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	// "a" is the sender — also a participant.
	if err := svc.UnreactMessage(context.Background(), id, "a"); err != nil {
		t.Fatal(err)
	}
	if gotID != id || gotUID != "a" {
		t.Fatalf("repo unreact args = (id=%v, uid=%q), want (%v, a)", gotID, gotUID, id)
	}
}

func TestMessageService_ReactByNonParticipantForbidden(t *testing.T) {
	reacted := false
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		ReactToMessageFn: func(_ context.Context, _ domain.ID, _ string) error { reacted = true; return nil },
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	err := svc.ReactToMessage(context.Background(), idgen.NewID(), "c") // not a participant
	assertAppCode(t, err, 403)
	if reacted {
		t.Fatal("a non-participant reaction must not reach the repo")
	}
}

func TestMessageService_UnreactByNonParticipantForbidden(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
		return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	err := svc.UnreactMessage(context.Background(), idgen.NewID(), "c") // not a participant
	assertAppCode(t, err, 403)
}

func TestMessageService_ReactToMissingMessageNotFound(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.Message, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	err := svc.ReactToMessage(context.Background(), idgen.NewID(), "a")
	assertAppCode(t, err, 404)
}

func TestMessageService_UnreactToMissingMessageNotFound(t *testing.T) {
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.Message, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{})
	err := svc.UnreactMessage(context.Background(), idgen.NewID(), "a")
	assertAppCode(t, err, 404)
}

func TestMessageService_DeleteEmitsAuditEvent(t *testing.T) {
	id := idgen.NewID()
	var logged *domain.AuditEvent
	calls := 0
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		calls++
		return nil
	}}
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		SoftDeleteFn: func(_ context.Context, _ domain.ID) error { return nil },
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("audit Log called %d times, want 1", calls)
	}
	if logged.Action != domain.AuditMessageDelete {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditMessageDelete)
	}
	if logged.ActorUID != "a" {
		t.Fatalf("actor = %q, want a", logged.ActorUID)
	}
	if logged.SubjectType != "message" || logged.SubjectID != id.String() {
		t.Fatalf("subject = (%q,%q), want (message,%s)", logged.SubjectType, logged.SubjectID, id.String())
	}
	if logged.TargetUID != "b" {
		t.Fatalf("target = %q, want b (recipient)", logged.TargetUID)
	}
}

func TestMessageService_DeleteMissingDoesNotAudit(t *testing.T) {
	calls := 0
	audit := &mocks.Auditor{LogFn: func(_ context.Context, _ *domain.AuditEvent) error { calls++; return nil }}
	repo := &mocks.MessageRepo{GetFn: func(_ context.Context, _ domain.ID) (*domain.Message, error) {
		return nil, repository.ErrNotFound
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	if err := svc.Delete(ctx, idgen.NewID()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("audit must not fire for an idempotent no-op delete, called %d times", calls)
	}
}
