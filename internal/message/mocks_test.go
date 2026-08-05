package message_test

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/message"
	"github.com/aero/aero-id/internal/outbox"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/sso"
)

func assertAppCode(t *testing.T, err error, wantHTTP int) {
	t.Helper()
	var ae *apperr.AppError
	if !stderrors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %v", err)
	}
	if ae.HTTP != wantHTTP {
		t.Fatalf("HTTP = %d, want %d (code=%s)", ae.HTTP, wantHTTP, ae.Code)
	}
}

type mockMessageRepo struct {
	CreateFn               func(ctx context.Context, m *domain.Message, evt *outbox.Event) error
	GetFn                  func(ctx context.Context, id domain.ID) (*domain.Message, error)
	ListInboxFn            func(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversationFn     func(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	ListConversationsFn    func(ctx context.Context, uid string, page domain.Page) ([]*message.ConversationSummary, string, error)
	MarkReadFn             func(ctx context.Context, id domain.ID, at time.Time) error
	MarkReadWithEventFn    func(ctx context.Context, id domain.ID, at time.Time, evt *outbox.Event) error
	CountUnreadFn          func(ctx context.Context, toUID string) (int64, error)
	CountUnreadFromFn      func(ctx context.Context, toUID, fromUID string) (int64, error)
	UpdateContentFn        func(ctx context.Context, id domain.ID, content string, at time.Time) error
	SoftDeleteFn           func(ctx context.Context, id domain.ID) error
	MarkConversationReadFn func(ctx context.Context, toUID, peer string, at time.Time) error
	ReactToMessageFn       func(ctx context.Context, messageID domain.ID, uid string) error
	UnreactMessageFn       func(ctx context.Context, messageID domain.ID, uid string) error
	SearchMessagesFn       func(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error)
}

func (m *mockMessageRepo) Create(ctx context.Context, msg *domain.Message, evt *outbox.Event) error {
	if m.CreateFn == nil {
		return nil
	}
	return m.CreateFn(ctx, msg, evt)
}

func (m *mockMessageRepo) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	return m.GetFn(ctx, id)
}

func (m *mockMessageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
	return m.ListInboxFn(ctx, toUID, page, unreadOnly)
}

func (m *mockMessageRepo) ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error) {
	return m.ListConversationFn(ctx, uidA, uidB, page)
}

func (m *mockMessageRepo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
	return m.MarkReadFn(ctx, id, at)
}

func (m *mockMessageRepo) MarkReadWithEvent(ctx context.Context, id domain.ID, at time.Time, evt *outbox.Event) error {
	if m.MarkReadWithEventFn == nil {
		return nil
	}
	return m.MarkReadWithEventFn(ctx, id, at, evt)
}

func (m *mockMessageRepo) CountUnread(ctx context.Context, toUID string) (int64, error) {
	return m.CountUnreadFn(ctx, toUID)
}

func (m *mockMessageRepo) CountUnreadFrom(ctx context.Context, toUID, fromUID string) (int64, error) {
	if m.CountUnreadFromFn == nil {
		return 0, nil
	}
	return m.CountUnreadFromFn(ctx, toUID, fromUID)
}

func (m *mockMessageRepo) ListConversations(ctx context.Context, uid string, page domain.Page) ([]*message.ConversationSummary, string, error) {
	if m.ListConversationsFn == nil {
		return nil, "", nil
	}
	return m.ListConversationsFn(ctx, uid, page)
}

func (m *mockMessageRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
	if m.UpdateContentFn == nil {
		return nil
	}
	return m.UpdateContentFn(ctx, id, content, at)
}

func (m *mockMessageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	if m.SoftDeleteFn == nil {
		return nil
	}
	return m.SoftDeleteFn(ctx, id)
}

func (m *mockMessageRepo) MarkConversationRead(ctx context.Context, toUID, peer string, at time.Time) error {
	if m.MarkConversationReadFn == nil {
		return nil
	}
	return m.MarkConversationReadFn(ctx, toUID, peer, at)
}

func (m *mockMessageRepo) ReactToMessage(ctx context.Context, messageID domain.ID, uid string) error {
	if m.ReactToMessageFn == nil {
		return nil
	}
	return m.ReactToMessageFn(ctx, messageID, uid)
}

func (m *mockMessageRepo) UnreactMessage(ctx context.Context, messageID domain.ID, uid string) error {
	if m.UnreactMessageFn == nil {
		return nil
	}
	return m.UnreactMessageFn(ctx, messageID, uid)
}

func (m *mockMessageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
	if m.SearchMessagesFn == nil {
		return nil, nil
	}
	return m.SearchMessagesFn(ctx, uid, query, limit)
}

type mockFileRepo struct {
	GetFn func(ctx context.Context, id domain.ID) (*domain.File, error)
}

func (m *mockFileRepo) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	return m.GetFn(ctx, id)
}

type mockUserDir struct {
	GetUsersFn func(ctx context.Context, uids []string) (map[string]sso.User, error)
}

func (m *mockUserDir) GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error) {
	return m.GetUsersFn(ctx, uids)
}

type mockBlockChecker struct {
	IsBlockedFn func(ctx context.Context, blocker, blocked string) (bool, error)
}

func (m *mockBlockChecker) IsBlocked(ctx context.Context, blocker, blocked string) (bool, error) {
	return m.IsBlockedFn(ctx, blocker, blocked)
}
