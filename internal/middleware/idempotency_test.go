package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/middleware"
)

// memStore is an in-memory idempotency.Store for middleware tests.
type memStore struct {
	mu      sync.Mutex
	records map[string]*idempotency.Record
}

func newMemStore() *memStore { return &memStore{records: map[string]*idempotency.Record{}} }

func (m *memStore) key(uid, endpoint, k string) string { return uid + "|" + endpoint + "|" + k }

func (m *memStore) Reserve(_ context.Context, uid, endpoint, k string) (*idempotency.Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.records[m.key(uid, endpoint, k)]; ok {
		return r, false, nil
	}
	m.records[m.key(uid, endpoint, k)] = &idempotency.Record{} // in-progress
	return nil, true, nil
}

func (m *memStore) Complete(_ context.Context, uid, endpoint, k string, rec idempotency.Record, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec.Completed = true
	m.records[m.key(uid, endpoint, k)] = &rec
	return nil
}

func (m *memStore) Release(_ context.Context, uid, endpoint, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, m.key(uid, endpoint, k))
	return nil
}

func setupIdemRouter(store idempotency.Store, calls *int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) { c.Set("actor_uid", "u1"); c.Next() }) // emulate auth
	e.Use(middleware.Idempotency(store, time.Hour))
	e.POST("/v1/messages", func(c *gin.Context) {
		*calls++
		c.JSON(http.StatusCreated, gin.H{"id": "msg-1", "n": *calls})
	})
	return e
}

func postWithKey(e *gin.Engine, key string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	e.ServeHTTP(w, req)
	return w
}

func TestIdempotency_ReplaysStoredResponse(t *testing.T) {
	calls := 0
	e := setupIdemRouter(newMemStore(), &calls)

	first := postWithKey(e, "abc")
	second := postWithKey(e, "abc")

	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 (retry must replay)", calls)
	}
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("codes: first=%d second=%d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("replayed body differs:\n first=%s\n second=%s", first.Body.String(), second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replayed response should carry Idempotent-Replayed: true")
	}
}

func TestIdempotency_NoKeyRunsEachTime(t *testing.T) {
	calls := 0
	e := setupIdemRouter(newMemStore(), &calls)
	postWithKey(e, "")
	postWithKey(e, "")
	if calls != 2 {
		t.Fatalf("without a key the handler must run each time, ran %d", calls)
	}
}

func TestIdempotency_InProgressConflicts(t *testing.T) {
	store := newMemStore()
	// Pre-reserve to simulate an in-flight leader.
	if _, reserved, _ := store.Reserve(context.Background(), "u1", "POST /v1/messages", "abc"); !reserved {
		t.Fatal("setup: expected to reserve the key")
	}
	calls := 0
	e := setupIdemRouter(store, &calls)

	w := postWithKey(e, "abc")
	if w.Code != http.StatusConflict {
		t.Fatalf("in-progress duplicate = %d, want 409", w.Code)
	}
	if calls != 0 {
		t.Fatalf("handler must not run for an in-progress duplicate, ran %d", calls)
	}
}
