package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/hub"
)

func TestEventsHandler_NewAndRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := hub.New()
	defer h.Close()

	handler := httpapi.NewEventsHandler(h, nil)
	if handler == nil {
		t.Fatal("handler should not be nil")
	}

	log, _ := zap.NewDevelopment()
	health := httpapi.NewHealthHandler()
	router := httpapi.NewRouter(log, httpapi.RouterConfig{}, nil, health, handler)

	routes := router.Routes()
	var eventsRouteFound bool
	for _, rt := range routes {
		// The route is mounted under /v1 group (auth group).
		if rt.Path == "/v1/events" && rt.Method == "GET" {
			eventsRouteFound = true
			break
		}
	}
	if !eventsRouteFound {
		t.Errorf("expected GET /v1/events route; got %v", routes)
	}
}

func TestEventsHandler_Stream_Unauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := hub.New()
	defer h.Close()

	handler := httpapi.NewEventsHandler(h, nil)
	log, _ := zap.NewDevelopment()
	health := httpapi.NewHealthHandler()
	router := httpapi.NewRouter(log, httpapi.RouterConfig{}, nil, health, handler)

	// /events is under /v1 auth group, so the path is /v1/events.
	req := httptest.NewRequest("GET", "/v1/events", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// fakeNotifFetcher is a deterministic MissedNotifFetcher double used by the SSE
// replay tests. It records the arguments it was called with and returns a fixed
// notification list, so the replay contract can be asserted exactly.
type fakeNotifFetcher struct {
	notifs   []*domain.Notification
	gotUID   string
	gotSince time.Time
	gotLimit int
	calls    int
}

func (f *fakeNotifFetcher) ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error) {
	f.calls++
	f.gotUID = recipientUID
	f.gotSince = since
	f.gotLimit = limit
	return f.notifs, nil
}

func TestEventsHandler_Stream_Replay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := hub.New()
	defer h.Close()

	// Fixed unix-millis values keep the cursor/t1/t2 ordering and the
	// since-equality assertion exact and timezone-independent.
	const cursorMillis = 1700000000000
	t1 := time.UnixMilli(1700000000123)
	t2 := time.UnixMilli(1700000000456)

	n1 := &domain.Notification{
		RecipientUID: "u1",
		Type:         "a.b",
		ActorUID:     "bob",
		SubjectType:  "message",
		SubjectID:    "subject-1",
		CreatedAt:    t1,
	}
	n2 := &domain.Notification{
		RecipientUID: "u1",
		Type:         "a.c",
		ActorUID:     "carol",
		SubjectType:  "message",
		SubjectID:    "subject-2",
		CreatedAt:    t2,
	}

	fake := &fakeNotifFetcher{notifs: []*domain.Notification{n1, n2}}
	handler := httpapi.NewEventsHandler(h, fake)
	health := httpapi.NewHealthHandler()
	router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, health, handler)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/v1/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Last-Event-ID", strconv.FormatInt(cursorMillis, 10))
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(w, req)
		close(done)
	}()

	// Poll until both replay blocks are visible in the response body.
	want1 := "id: 1700000000123"
	want2 := "id: 1700000000456"
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(w.Body.String(), want1) || !strings.Contains(w.Body.String(), want2) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for replay blocks; body:\n%s", w.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	body := w.Body.String()
	idx1 := strings.Index(body, want1)
	idx2 := strings.Index(body, want2)
	if idx1 < 0 || idx2 < 0 || idx1 >= idx2 {
		t.Fatalf("replay blocks out of order: n1 at %d, n2 at %d; body:\n%s", idx1, idx2, body)
	}

	// Each block carries event: and data: lines matching the marshaled
	// notification, in the fetcher's returned order.
	data1, _ := json.Marshal(n1)
	data2, _ := json.Marshal(n2)
	if !strings.Contains(body, "event: a.b\n") || !strings.Contains(body, "data: "+string(data1)+"\n\n") {
		t.Fatalf("n1 replay block missing event/data lines; body:\n%s", body)
	}
	if !strings.Contains(body, "event: a.c\n") || !strings.Contains(body, "data: "+string(data2)+"\n\n") {
		t.Fatalf("n2 replay block missing event/data lines; body:\n%s", body)
	}

	// The fetcher must have been called exactly once, with the authenticated
	// actor, the cursor-derived since, and the fixed batch limit.
	if fake.calls != 1 {
		t.Fatalf("ListSince calls = %d, want 1", fake.calls)
	}
	if fake.gotUID != "u1" {
		t.Fatalf("ListSince recipientUID = %q, want %q", fake.gotUID, "u1")
	}
	if fake.gotSince.UnixMilli() != cursorMillis {
		t.Fatalf("ListSince since = %d, want %d", fake.gotSince.UnixMilli(), cursorMillis)
	}
	if fake.gotLimit != 50 {
		t.Fatalf("ListSince limit = %d, want 50", fake.gotLimit)
	}

	// Live-stream events must be delivered after the replay blocks.
	h.Publish("u1", "live", "live-data", 9000)
	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(w.Body.String(), "id: 9000") {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for live event; body:\n%s", w.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if idxLive := strings.Index(w.Body.String(), "id: 9000"); idxLive < idx2 {
		t.Fatalf("live event written before replay blocks; body:\n%s", w.Body.String())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after cancel")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestEventsHandler_Stream_ReplayInvalidCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := hub.New()
	defer h.Close()

	fake := &fakeNotifFetcher{}
	handler := httpapi.NewEventsHandler(h, fake)
	health := httpapi.NewHealthHandler()
	router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, health, handler)

	for _, cursor := range []string{"abc", "0", "-5"} {
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest("GET", "/v1/events", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer t")
		req.Header.Set("Last-Event-ID", cursor)
		w := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			router.ServeHTTP(w, req)
			close(done)
		}()

		// Let the handler reach the live loop. Replay is attempted
		// synchronously before it, so any fetch would already have happened.
		time.Sleep(100 * time.Millisecond)

		if fake.calls != 0 {
			t.Fatalf("cursor %q: ListSince called %d times, want 0 (replay must be skipped)", cursor, fake.calls)
		}
		if body := w.Body.String(); strings.Contains(body, "id: ") {
			t.Fatalf("cursor %q: body contains a replay block, want none:\n%s", cursor, body)
		}

		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("cursor %q: handler did not return after cancel", cursor)
		}

		if w.Code != http.StatusOK {
			t.Fatalf("cursor %q: status = %d, want %d", cursor, w.Code, http.StatusOK)
		}
	}
}
