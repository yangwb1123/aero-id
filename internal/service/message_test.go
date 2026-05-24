package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
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
	repo := &mocks.MessageRepo{CreateFn: func(_ context.Context, m *domain.Message) error { saved = m; return nil }}
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
