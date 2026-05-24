package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

type fakeMessageService struct {
	sendFn         func(context.Context, service.SendMessageInput) (*domain.Message, error)
	getFn          func(context.Context, domain.ID) (*domain.Message, error)
	inboxFn        func(context.Context, domain.Page, bool) (*service.MessageList, error)
	conversationFn func(context.Context, string, domain.Page) (*service.MessageList, error)
	markReadFn     func(context.Context, domain.ID) error
}

func (f *fakeMessageService) Send(ctx context.Context, in service.SendMessageInput) (*domain.Message, error) {
	return f.sendFn(ctx, in)
}
func (f *fakeMessageService) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	return f.getFn(ctx, id)
}
func (f *fakeMessageService) Inbox(ctx context.Context, p domain.Page, unread bool) (*service.MessageList, error) {
	return f.inboxFn(ctx, p, unread)
}
func (f *fakeMessageService) Conversation(ctx context.Context, with string, p domain.Page) (*service.MessageList, error) {
	return f.conversationFn(ctx, with, p)
}
func (f *fakeMessageService) MarkRead(ctx context.Context, id domain.ID) error {
	return f.markReadFn(ctx, id)
}

func TestMessageHandler_Send(t *testing.T) {
	svc := &fakeMessageService{sendFn: func(_ context.Context, in service.SendMessageInput) (*domain.Message, error) {
		if in.ToUID != "b" || in.Content != "hi" {
			t.Errorf("input not bound: %+v", in)
		}
		return &domain.Message{ID: idgen.NewID(), FromUID: "a", ToUID: in.ToUID, Content: in.Content}, nil
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"to_uid":"b","content":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", w.Code, w.Body.String())
	}
}

func TestMessageHandler_InboxIncludesUsers(t *testing.T) {
	svc := &fakeMessageService{inboxFn: func(context.Context, domain.Page, bool) (*service.MessageList, error) {
		return &service.MessageList{
			Messages:   []*domain.Message{{ID: idgen.NewID(), FromUID: "x", ToUID: "a"}},
			NextCursor: "next",
			Users:      map[string]sso.User{"x": {UID: "x", Nickname: "ex"}},
		}, nil
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/messages?unread=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data struct {
			Users      map[string]map[string]any `json:"users"`
			NextCursor string                    `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.NextCursor != "next" || resp.Data.Users["x"]["nickname"] != "ex" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestMessageHandler_MarkRead204(t *testing.T) {
	svc := &fakeMessageService{markReadFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages/"+idgen.NewID().String()+"/read", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestMessageHandler_GetForbiddenMapsTo403(t *testing.T) {
	svc := &fakeMessageService{getFn: func(context.Context, domain.ID) (*domain.Message, error) {
		return nil, apperr.Forbidden("message.forbidden", "nope")
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/messages/"+idgen.NewID().String(), nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
