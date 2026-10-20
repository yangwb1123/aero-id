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
	cursor := strconv.FormatInt(1700000000000, 10)
	for _, tc := range []struct {
		name   string
		path   string
		header string
	}{
		{"header", "/v1/events", cursor},
		{"query", "/v1/events?last_event_id=" + cursor, ""},
		{"header_precedes_query", "/v1/events?last_event_id=1700000000999", cursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testEventsHandlerReplay(t, tc.path, tc.header)
		})
	}
}

func testEventsHandlerReplay(t *testing.T, path, header string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := hub.New()
	t.Cleanup(h.Close)

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

	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Last-Event-ID", header)
	stream := startSSETestStream(t, router, req)

	// Only the handler goroutine touches the recorder while streaming. Each
	// flush publishes an immutable body snapshot through a bounded handshake.
	want1 := "id: 1700000000123"
	want2 := "id: 1700000000456"
	body := stream.nextFlush(t)
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

	// Live-stream events must be delivered after the replay blocks.
	h.Publish("u1", "live", "live-data", 9000)
	liveBody := stream.nextFlush(t)
	liveBlock := "id: 9000\nevent: live\ndata: live-data\n\n"
	if liveBody != body+liveBlock {
		t.Fatalf("live event missing, duplicated or written before replay blocks; body:\n%s", liveBody)
	}

	stream.stop(t)
	// Join before inspecting fetcher fields or the final recorder, including
	// on failure via startSSETestStream's cleanup.
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
	if stream.recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", stream.recorder.Code, http.StatusOK)
	}
	if got := stream.recorder.Body.String(); got != liveBody {
		t.Fatalf("unexpected writes after live flush; body:\n%s", got)
	}
}

func TestEventsHandler_Stream_ReplayInvalidCursor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		header string
	}{
		{"header_malformed", "/v1/events", "abc"},
		{"header_zero", "/v1/events", "0"},
		{"header_negative", "/v1/events", "-5"},
		{"query_malformed", "/v1/events?last_event_id=abc", ""},
		{"query_zero", "/v1/events?last_event_id=0", ""},
		{"query_negative", "/v1/events?last_event_id=-5", ""},
		{"invalid_header_precedes_valid_query", "/v1/events?last_event_id=1700000000000", "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			h := hub.New()
			t.Cleanup(h.Close)
			fake := &fakeNotifFetcher{}
			handler := httpapi.NewEventsHandler(h, fake)
			health := httpapi.NewHealthHandler()
			router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, health, handler)

			req := httptest.NewRequest("GET", tc.path, nil)
			req.Header.Set("Authorization", "Bearer t")
			req.Header.Set("Last-Event-ID", tc.header)
			stream := startSSETestStream(t, router, req)

			// Header observation proves subscription exists before publishing;
			// the live flush proves the replay branch has actually finished.
			stream.waitReady(t)
			h.Publish("u1", "live", "live-data", 9000)
			body := stream.nextFlush(t)
			if body != "id: 9000\nevent: live\ndata: live-data\n\n" {
				t.Fatalf("invalid cursor emitted replay or lost live event; body:\n%s", body)
			}
			stream.stop(t)
			if fake.calls != 0 {
				t.Fatalf("ListSince called %d times, want 0 (replay must be skipped)", fake.calls)
			}
			if stream.recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", stream.recorder.Code, http.StatusOK)
			}
			if got := stream.recorder.Body.String(); got != body {
				t.Fatalf("unexpected writes after live flush; body:\n%s", got)
			}
		})
	}
}
