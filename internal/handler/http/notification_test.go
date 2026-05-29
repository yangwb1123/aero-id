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
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

type fakeNotificationService struct {
	listFn        func(context.Context, domain.Page) (*service.NotificationList, error)
	markReadFn    func(context.Context, domain.ID) error
	markAllReadFn func(context.Context) error
	unreadCountFn func(context.Context) (int64, error)
}

func (f *fakeNotificationService) List(ctx context.Context, p domain.Page) (*service.NotificationList, error) {
	return f.listFn(ctx, p)
}
func (f *fakeNotificationService) MarkRead(ctx context.Context, id domain.ID) error {
	return f.markReadFn(ctx, id)
}
func (f *fakeNotificationService) MarkAllRead(ctx context.Context) error {
	return f.markAllReadFn(ctx)
}
func (f *fakeNotificationService) UnreadCount(ctx context.Context) (int64, error) {
	return f.unreadCountFn(ctx)
}

func TestNotificationHandler_UnreadCount(t *testing.T) {
	svc := &fakeNotificationService{unreadCountFn: func(context.Context) (int64, error) { return 3, nil }}
	e := serveWithActor(httpapi.NewNotificationHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/notifications/unread_count", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"unread_count":3`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestNotificationHandler_ListIncludesUsers(t *testing.T) {
	svc := &fakeNotificationService{listFn: func(context.Context, domain.Page) (*service.NotificationList, error) {
		return &service.NotificationList{
			Notifications: []*domain.Notification{{ID: idgen.NewID(), RecipientUID: "u1", ActorUID: "a", Type: "message.received"}},
			NextCursor:    "next",
			Users:         map[string]sso.User{"a": {UID: "a", Nickname: "Ann"}},
		}, nil
	}}
	e := serveWithActor(httpapi.NewNotificationHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/notifications", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Notifications []map[string]any          `json:"notifications"`
			Users         map[string]map[string]any `json:"users"`
			NextCursor    string                    `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.NextCursor != "next" || len(resp.Data.Notifications) != 1 {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	if resp.Data.Notifications[0]["type"] != "message.received" || resp.Data.Users["a"]["nickname"] != "Ann" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestNotificationHandler_MarkRead204(t *testing.T) {
	svc := &fakeNotificationService{markReadFn: func(context.Context, domain.ID) error { return nil }}
	e := serveWithActor(httpapi.NewNotificationHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/notifications/"+idgen.NewID().String()+"/read", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
}

func TestNotificationHandler_MarkAllRead204(t *testing.T) {
	called := false
	svc := &fakeNotificationService{markAllReadFn: func(context.Context) error { called = true; return nil }}
	e := serveWithActor(httpapi.NewNotificationHandler(svc), "u1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/notifications/read-all", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body=%s)", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("MarkAllRead was not called")
	}
}
