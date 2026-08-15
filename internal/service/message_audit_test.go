package service_test

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

// TestMessageService_SendAuditFactCommitted: message.send is a
// single-occurrence create fact anchored on the message ID (EventID = m.ID),
// so a retry dedups at the ledger. Actor = the sender; TargetUID = the
// recipient.
func TestMessageService_SendAuditFactCommitted(t *testing.T) {
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, _ *domain.Message, _ *outbox.Event) error {
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	m, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("message.send audit fact was not emitted")
	}
	if logged.ID != m.ID {
		t.Fatalf("event ID = %v, want %v (anchored on the message ID)", logged.ID, m.ID)
	}
	if logged.Action != domain.AuditMessageSend {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditMessageSend)
	}
	if logged.ActorUID != "a" {
		t.Fatalf("actor = %q, want a (sender)", logged.ActorUID)
	}
	if logged.SubjectType != "message" || logged.SubjectID != m.ID.String() {
		t.Fatalf("subject = (%q,%q), want (message,%s)", logged.SubjectType, logged.SubjectID, m.ID.String())
	}
	if logged.TargetUID != "b" {
		t.Fatalf("target = %q, want b (recipient)", logged.TargetUID)
	}
}

// TestMessageService_SendAuditFailureFailsClosed: an audit failure after the
// message insert surfaces as 503 audit.ingest_failed — the fact is never
// silently dropped.
func TestMessageService_SendAuditFailureFailsClosed(t *testing.T) {
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, _ *domain.Message, _ *outbox.Event) error {
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Send(ctx, service.SendMessageInput{ToUID: "b", Content: "hi"})
	assertAuditIngestFailed(t, err)
}

// TestMessageService_EditAuditFactCommitted: message.edit is multi-occurrence,
// so the EventID is zero (fresh per-execution row — never a silently dropped
// later edit via entity-anchor dedup).
func TestMessageService_EditAuditFactCommitted(t *testing.T) {
	id := idgen.NewID()
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		UpdateContentFn: func(_ context.Context, _ domain.ID, _ string, _ time.Time) error {
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	if _, err := svc.Edit(ctx, id, "new content"); err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("message.edit audit fact was not emitted")
	}
	if logged.Action != domain.AuditMessageEdit {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditMessageEdit)
	}
	if logged.ID != (domain.ID{}) {
		t.Fatalf("event ID = %v, want zero (fresh per-execution — multi-occurrence op)", logged.ID)
	}
	if logged.ActorUID != "a" || logged.TargetUID != "b" || logged.SubjectID != id.String() {
		t.Fatalf("unexpected edit fact: %+v", logged)
	}
}

// TestMessageService_EditAuditFailureFailsClosed: same fail-closed contract
// as every standalone site.
func TestMessageService_EditAuditFailureFailsClosed(t *testing.T) {
	id := idgen.NewID()
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		UpdateContentFn: func(_ context.Context, _ domain.ID, _ string, _ time.Time) error {
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	ctx := service.WithActor(context.Background(), "a")
	_, err := svc.Edit(ctx, id, "new content")
	assertAuditIngestFailed(t, err)
}

// TestMessageService_ReactToMessageAuditFactCommitted: message.react is
// multi-occurrence (zero EventID); the actor is the reacting actor param and
// TargetUID is the peer of the reacting actor.
func TestMessageService_ReactToMessageAuditFactCommitted(t *testing.T) {
	id := idgen.NewID()
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		ReactToMessageFn: func(_ context.Context, _ domain.ID, _ string) error {
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	if err := svc.ReactToMessage(context.Background(), id, "b"); err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("message.react audit fact was not emitted")
	}
	if logged.Action != domain.AuditMessageReact {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditMessageReact)
	}
	if logged.ID != (domain.ID{}) {
		t.Fatalf("event ID = %v, want zero (fresh per-execution — multi-occurrence op)", logged.ID)
	}
	if logged.ActorUID != "b" {
		t.Fatalf("actor = %q, want b (the reacting actor param)", logged.ActorUID)
	}
	if logged.TargetUID != "a" {
		t.Fatalf("target = %q, want a (the peer of the reacting actor)", logged.TargetUID)
	}
	if logged.SubjectID != id.String() {
		t.Fatalf("subject = %q, want %s", logged.SubjectID, id.String())
	}
}

// TestMessageService_ReactToMessageAuditFailureFailsClosed: the fact is
// emitted after the reaction commits and before the best-effort notification
// event; an audit failure still fails the request closed.
func TestMessageService_ReactToMessageAuditFailureFailsClosed(t *testing.T) {
	id := idgen.NewID()
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		ReactToMessageFn: func(_ context.Context, _ domain.ID, _ string) error {
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	err := svc.ReactToMessage(context.Background(), id, "b")
	assertAuditIngestFailed(t, err)
}

// TestMessageService_UnreactMessageAuditFactCommitted: unreact is the
// symmetric mutation to react and stays in scope for invariant completeness —
// same fresh-per-execution shape.
func TestMessageService_UnreactMessageAuditFactCommitted(t *testing.T) {
	id := idgen.NewID()
	var logged *domain.AuditEvent
	audit := &mocks.Auditor{LogFn: func(_ context.Context, e *domain.AuditEvent) error {
		logged = e
		return nil
	}}
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		UnreactMessageFn: func(_ context.Context, _ domain.ID, _ string) error {
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	if err := svc.UnreactMessage(context.Background(), id, "a"); err != nil {
		t.Fatal(err)
	}
	if logged == nil {
		t.Fatal("message.unreact audit fact was not emitted")
	}
	if logged.Action != domain.AuditMessageUnreact {
		t.Fatalf("action = %q, want %q", logged.Action, domain.AuditMessageUnreact)
	}
	if logged.ID != (domain.ID{}) {
		t.Fatalf("event ID = %v, want zero (fresh per-execution — multi-occurrence op)", logged.ID)
	}
	if logged.ActorUID != "a" || logged.TargetUID != "b" || logged.SubjectID != id.String() {
		t.Fatalf("unexpected unreact fact: %+v", logged)
	}
}

// TestMessageService_UnreactMessageAuditFailureFailsClosed: symmetric
// fail-closed contract.
func TestMessageService_UnreactMessageAuditFailureFailsClosed(t *testing.T) {
	id := idgen.NewID()
	audit := &mocks.Auditor{LogFn: func(context.Context, *domain.AuditEvent) error {
		return stderrors.New("ledger down")
	}}
	repo := &mocks.MessageRepo{
		GetFn: func(_ context.Context, gid domain.ID) (*domain.Message, error) {
			return &domain.Message{ID: gid, FromUID: "a", ToUID: "b"}, nil
		},
		UnreactMessageFn: func(_ context.Context, _ domain.ID, _ string) error {
			return nil
		},
	}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	err := svc.UnreactMessage(context.Background(), id, "a")
	assertAuditIngestFailed(t, err)
}

// TestMessageService_SendSystemNoAuditFact (documented exemption): SendSystem
// is a server-internal emit path with no authenticated actor and no tenant
// (the ledger rejects tenant-less facts) and zero production callers — the
// auditor must never fire. This test pins the exemption so a future migration
// of SendSystem into the audited surface requires deleting this pin.
func TestMessageService_SendSystemNoAuditFact(t *testing.T) {
	calls := 0
	audit := &mocks.Auditor{LogFn: func(_ context.Context, _ *domain.AuditEvent) error {
		calls++
		return nil
	}}
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, _ *domain.Message, _ *outbox.Event) error {
		return nil
	}}
	svc := service.NewMessageService(repo, &mocks.FileRepo{}, &mocks.UserDir{}, service.WithMessageAuditLogger(audit))
	if _, err := svc.SendSystem(context.Background(), "b", "server notice", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("SendSystem must not emit an audit fact (tenant-less server-internal emit path), called %d times", calls)
	}
}
