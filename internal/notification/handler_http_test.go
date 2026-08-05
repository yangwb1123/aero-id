package notification_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httphandler "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/notification"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/sso"
)

type fakeNotificationService struct {
	listFn        func(ctx context.Context, page domain.Page) (*notification.NotificationList, error)
	markReadFn    func(ctx context.Context, id domain.ID) error
	markAllReadFn func(ctx context.Context) error
	unreadCountFn func(ctx context.Context) (int64, error)
}

func (f *fakeNotificationService) List(ctx context.Context, page domain.Page) (*notification.NotificationList, error) {
	if f.listFn != nil {
		return f.listFn(ctx, page)
	}
	return nil, nil
}

func (f *fakeNotificationService) MarkRead(ctx context.Context, id domain.ID) error {
	if f.markReadFn != nil {
		return f.markReadFn(ctx, id)
	}
	return nil
}

func (f *fakeNotificationService) MarkAllRead(ctx context.Context) error {
	if f.markAllReadFn != nil {
		return f.markAllReadFn(ctx)
	}
	return nil
}

func (f *fakeNotificationService) UnreadCount(ctx context.Context) (int64, error) {
	if f.unreadCountFn != nil {
		return f.unreadCountFn(ctx)
	}
	return 0, nil
}

func setupNotificationRouter(svc notification.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	h := notification.NewNotificationHandler(svc)
	routes := httphandler.Routes{
		Auth: e.Group("/v1", func(c *gin.Context) {
			c.Request = c.Request.WithContext(domain.WithActor(c.Request.Context(), "u1"))
			c.Next()
		}),
	}
	h.RegisterRoutes(routes)
	return e
}

func TestNotificationHandler_List(t *testing.T) {
	svc := &fakeNotificationService{
		listFn: func(_ context.Context, page domain.Page) (*notification.NotificationList, error) {
			return &notification.NotificationList{
				Notifications: []*domain.Notification{
					{ID: idgen.NewID(), Type: "message.received", ActorUID: "bob", SubjectID: "msg-1"},
				},
				Users:      map[string]sso.User{"bob": {UID: "bob", Nickname: "Bob", Avatar: "avatar.jpg"}},
				NextCursor: "next",
			}, nil
		},
	}
	e := setupNotificationRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/notifications", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data struct {
			Notifications []map[string]any `json:"notifications"`
			Users         map[string]any   `json:"users"`
			NextCursor    string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(resp.Data.Notifications))
	}
	if resp.Data.NextCursor != "next" {
		t.Errorf("next_cursor = %q", resp.Data.NextCursor)
	}
}

func TestNotificationHandler_MarkRead(t *testing.T) {
	svc := &fakeNotificationService{
		markReadFn: func(_ context.Context, id domain.ID) error {
			if id == (domain.ID{}) {
				t.Error("id should not be empty")
			}
			return nil
		},
	}
	e := setupNotificationRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/notifications/00000000-0000-0000-0000-000000000001/read", nil)
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
}

func TestNotificationHandler_MarkAllRead(t *testing.T) {
	called := false
	svc := &fakeNotificationService{
		markAllReadFn: func(_ context.Context) error {
			called = true
			return nil
		},
	}
	e := setupNotificationRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("POST", "/v1/notifications/read-all", nil))
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	if !called {
		t.Fatal("MarkAllRead not called")
	}
}

func TestNotificationHandler_UnreadCount(t *testing.T) {
	svc := &fakeNotificationService{
		unreadCountFn: func(_ context.Context) (int64, error) {
			return 42, nil
		},
	}
	e := setupNotificationRouter(svc)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/notifications/unread_count", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data struct {
			UnreadCount int64 `json:"unread_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.UnreadCount != 42 {
		t.Errorf("unread_count = %d, want 42", resp.Data.UnreadCount)
	}
}
