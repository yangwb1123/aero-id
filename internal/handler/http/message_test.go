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
	sendFn                 func(context.Context, service.SendMessageInput) (*domain.Message, error)
	getFn                  func(context.Context, domain.ID) (*domain.Message, error)
	inboxFn                func(context.Context, domain.Page, bool) (*service.MessageList, error)
	conversationFn         func(context.Context, string, domain.Page) (*service.MessageList, error)
	markReadFn             func(context.Context, domain.ID) error
	markConversationReadFn func(context.Context, string) error
	editFn                 func(context.Context, domain.ID, string) (*domain.Message, error)
	deleteFn               func(context.Context, domain.ID) error
	unreadCountFn          func(context.Context) (int64, error)
}

func (f *fakeMessageService) Send(ctx context.Context, in service.SendMessageInput) (*domain.Message, error) {
	return f.sendFn(ctx, in)
}
func (f *fakeMessageService) SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error) {
	return nil, nil
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
func (f *fakeMessageService) MarkConversationRead(ctx context.Context, with string) error {
	return f.markConversationReadFn(ctx, with)
}
func (f *fakeMessageService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
	return f.editFn(ctx, id, content)
}
func (f *fakeMessageService) Delete(ctx context.Context, id domain.ID) error {
	return f.deleteFn(ctx, id)
}
func (f *fakeMessageService) UnreadCount(ctx context.Context) (int64, error) {
	return f.unreadCountFn(ctx)
}

func TestMessageHandler_UnreadCount(t *testing.T) {
	svc := &fakeMessageService{unreadCountFn: func(context.Context) (int64, error) { return 5, nil }}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/messages/unread_count", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"unread_count":5`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
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

func TestMessageHandler_Edit(t *testing.T) {
	id := idgen.NewID()
	svc := &fakeMessageService{editFn: func(_ context.Context, gotID domain.ID, content string) (*domain.Message, error) {
		if gotID != id || content != "fixed" {
			t.Errorf("edit args not bound: id=%v content=%q", gotID, content)
		}
		return &domain.Message{ID: gotID, FromUID: "a", ToUID: "b", Content: content}, nil
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	req := httptest.NewRequest(http.MethodPatch, "/v1/messages/"+id.String(), strings.NewReader(`{"content":"fixed"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"content":"fixed"`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestMessageHandler_EditForbiddenMapsTo403(t *testing.T) {
	svc := &fakeMessageService{editFn: func(context.Context, domain.ID, string) (*domain.Message, error) {
		return nil, apperr.Forbidden("message.forbidden", "nope")
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	req := httptest.NewRequest(http.MethodPatch, "/v1/messages/"+idgen.NewID().String(), strings.NewReader(`{"content":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestMessageHandler_Delete204(t *testing.T) {
	id := idgen.NewID()
	called := false
	svc := &fakeMessageService{deleteFn: func(_ context.Context, gotID domain.ID) error {
		called = true
		if gotID != id {
			t.Errorf("delete id = %v, want %v", gotID, id)
		}
		return nil
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/messages/"+id.String(), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("Delete was not called")
	}
}

func TestMessageHandler_MarkConversationRead204(t *testing.T) {
	var gotPeer string
	svc := &fakeMessageService{markConversationReadFn: func(_ context.Context, with string) error {
		gotPeer = with
		return nil
	}}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/conversations/peer-uid/read", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if gotPeer != "peer-uid" {
		t.Fatalf("peer = %q, want peer-uid", gotPeer)
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

func TestMessageHandler_Conversation(t *testing.T) {
	msgID := idgen.NewID()
	svc := &fakeMessageService{
		conversationFn: func(_ context.Context, with string, _ domain.Page) (*service.MessageList, error) {
			return &service.MessageList{
				Messages: []*domain.Message{{
					ID:      msgID,
					FromUID: "a",
					ToUID:   "peer-uid",
					Content: "hey",
				}},
				NextCursor: "",
				Users: map[string]sso.User{
					"a":        {UID: "a", Nickname: "Alice"},
					"peer-uid": {UID: "peer-uid", Nickname: "Peer"},
				},
			}, nil
		},
	}
	e := serveWithActor(httpapi.NewMessageHandler(svc), "a")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/conversations/peer-uid", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Messages []map[string]any          `json:"messages"`
			Users    map[string]map[string]any `json:"users"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Data.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d (body=%s)", len(resp.Data.Messages), w.Body.String())
	}
	if resp.Data.Messages[0]["content"] != "hey" {
		t.Fatalf("unexpected message content: %v", resp.Data.Messages[0]["content"])
	}
	if resp.Data.Users["peer-uid"]["nickname"] != "Peer" {
		t.Fatalf("unexpected user: %v", resp.Data.Users["peer-uid"])
	}
}
