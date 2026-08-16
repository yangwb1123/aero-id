package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/middleware"
)

// memStore is an in-memory idempotency.Store for middleware tests. failComplete
// and failRelease inject store errors; when set, the corresponding call leaves
// the map untouched (the durable receipt/reservation is never written).
type memStore struct {
	mu            sync.Mutex
	records       map[string]*idempotency.Record
	failComplete  error
	failRelease   error
	completeCalls int
	releaseCalls  int
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
	m.completeCalls++
	if m.failComplete != nil {
		return m.failComplete // receipt is never persisted
	}
	rec.Completed = true
	m.records[m.key(uid, endpoint, k)] = &rec
	return nil
}

func (m *memStore) Release(_ context.Context, uid, endpoint, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseCalls++
	if m.failRelease != nil {
		return m.failRelease // reservation stays stranded
	}
	delete(m.records, m.key(uid, endpoint, k))
	return nil
}

func (m *memStore) held(uid, endpoint, k string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.records[m.key(uid, endpoint, k)]
	return ok
}

func (m *memStore) completed(uid, endpoint, k string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[m.key(uid, endpoint, k)]
	return ok && r.Completed
}

// recordingObserver captures IdempotencyObserver callbacks for assertions.
type recordingObserver struct {
	mu          sync.Mutex
	complete    int
	release     int
	overflow    int
	completeErr error
	releaseErr  error
}

func (o *recordingObserver) CompleteFailed(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.complete++
	o.completeErr = err
}

func (o *recordingObserver) ReleaseFailed(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.release++
	o.releaseErr = err
}

func (o *recordingObserver) Overflowed() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.overflow++
}

// panickingObserver proves an observer bug cannot alter the request path.
type panickingObserver struct{}

func (panickingObserver) CompleteFailed(error) { panic("observer bug") }
func (panickingObserver) ReleaseFailed(error)  {}
func (panickingObserver) Overflowed()          {}

func setupIdemRouter(store idempotency.Store, obs middleware.IdempotencyObserver, calls *int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) { c.Set("actor_uid", "u1"); c.Next() }) // emulate auth
	e.Use(middleware.Idempotency(store, time.Hour, obs))
	e.POST("/v1/messages", func(c *gin.Context) {
		*calls++
		c.JSON(http.StatusCreated, gin.H{"id": "msg-1", "n": *calls})
	})
	return e
}

// setupIdem5xxRouter wires a handler that returns 500 to exercise the Release path.
func setupIdem5xxRouter(store idempotency.Store, obs middleware.IdempotencyObserver, calls *int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) { c.Set("actor_uid", "u1"); c.Next() }) // emulate auth
	e.Use(middleware.Idempotency(store, time.Hour, obs))
	e.POST("/v1/messages", func(c *gin.Context) {
		*calls++
		c.Status(http.StatusInternalServerError)
	})
	return e
}

// setupIdemBodyRouter wires a handler that writes size bytes via c.Data.
func setupIdemBodyRouter(store idempotency.Store, obs middleware.IdempotencyObserver, calls *int, size int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) { c.Set("actor_uid", "u1"); c.Next() }) // emulate auth
	e.Use(middleware.Idempotency(store, time.Hour, obs))
	payload := bytes.Repeat([]byte{'x'}, size)
	e.POST("/v1/export", func(c *gin.Context) {
		*calls++
		c.Data(http.StatusOK, "application/octet-stream", payload)
	})
	return e
}

// setupIdemWriteStringRouter wires a handler that exceeds the capture cap via
// c.Writer.WriteString to prove the cap applies on that path too.
func setupIdemWriteStringRouter(store idempotency.Store, obs middleware.IdempotencyObserver, calls *int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(func(c *gin.Context) { c.Set("actor_uid", "u1"); c.Next() }) // emulate auth
	e.Use(middleware.Idempotency(store, time.Hour, obs))
	e.POST("/v1/export", func(c *gin.Context) {
		*calls++
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.WriteString(strings.Repeat("x", 2*(1<<20))) // 2 MiB via WriteString
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

func postExportWithKey(e *gin.Engine, key string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/export", nil)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	e.ServeHTTP(w, req)
	return w
}

func TestIdempotency_ReplaysStoredResponse(t *testing.T) {
	calls := 0
	e := setupIdemRouter(newMemStore(), nil, &calls)

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
	e := setupIdemRouter(newMemStore(), nil, &calls)
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
	e := setupIdemRouter(store, nil, &calls)

	w := postWithKey(e, "abc")
	if w.Code != http.StatusConflict {
		t.Fatalf("in-progress duplicate = %d, want 409", w.Code)
	}
	if calls != 0 {
		t.Fatalf("handler must not run for an in-progress duplicate, ran %d", calls)
	}
}

func TestIdempotency_CompleteFailureObserved(t *testing.T) {
	sentinel := errors.New("complete boom")
	store := newMemStore()
	store.failComplete = sentinel
	obs := &recordingObserver{}
	calls := 0
	e := setupIdemRouter(store, obs, &calls)

	w := postWithKey(e, "abc")
	if w.Code != http.StatusCreated {
		t.Fatalf("complete failure must not change the response: got %d, want 201", w.Code)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1", calls)
	}
	// The delivered body must be byte-identical to a run against a healthy store.
	refCalls := 0
	ref := postWithKey(setupIdemRouter(newMemStore(), nil, &refCalls), "abc")
	if w.Body.String() != ref.Body.String() {
		t.Errorf("body changed by store failure:\n got=%q\nwant=%q", w.Body.String(), ref.Body.String())
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.complete != 1 || !errors.Is(obs.completeErr, sentinel) {
		t.Fatalf("CompleteFailed: called %d times err=%v, want once with the sentinel", obs.complete, obs.completeErr)
	}
	if obs.release != 0 || obs.overflow != 0 {
		t.Fatalf("unexpected callbacks: release=%d overflow=%d, want 0", obs.release, obs.overflow)
	}
	if store.completed("u1", "POST /v1/messages", "abc") {
		t.Error("receipt must not be persisted when Complete fails")
	}
}

func TestIdempotency_ReleaseFailureOn5xxObserved(t *testing.T) {
	sentinel := errors.New("release boom")
	store := newMemStore()
	store.failRelease = sentinel
	obs := &recordingObserver{}
	calls := 0
	e := setupIdem5xxRouter(store, obs, &calls)

	w := postWithKey(e, "abc")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("release failure must not change the 5xx response: got %d, want 500", w.Code)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1", calls)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.release != 1 || !errors.Is(obs.releaseErr, sentinel) {
		t.Fatalf("ReleaseFailed: called %d times err=%v, want once with the sentinel", obs.release, obs.releaseErr)
	}
	if obs.complete != 0 || obs.overflow != 0 {
		t.Fatalf("unexpected callbacks: complete=%d overflow=%d, want 0", obs.complete, obs.overflow)
	}
	if store.completeCalls != 0 {
		t.Fatalf("store.Complete called %d times on the 5xx path, want 0", store.completeCalls)
	}
}

func TestIdempotency_OversizedResponseReleasedAndRetryReexecutes(t *testing.T) {
	store := newMemStore()
	obs := &recordingObserver{}
	calls := 0
	e := setupIdemBodyRouter(store, obs, &calls, 2*(1<<20)) // 2 MiB > 1 MiB cap

	first := postExportWithKey(e, "big")
	if first.Code != http.StatusOK {
		t.Fatalf("first oversized POST = %d, want 200", first.Code)
	}
	if first.Body.Len() != 2*(1<<20) {
		t.Fatalf("oversized body not delivered intact: %d bytes, want %d", first.Body.Len(), 2*(1<<20))
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times after first POST, want 1", calls)
	}
	if store.held("u1", "POST /v1/export", "big") {
		t.Error("reservation must be released after overflow")
	}
	if store.completeCalls != 0 {
		t.Fatalf("store.Complete called %d times on the overflow path, want 0 (no partial body may be stored)", store.completeCalls)
	}

	obs.mu.Lock()
	overflow := obs.overflow
	obs.mu.Unlock()
	if overflow != 1 {
		t.Fatalf("Overflowed fired %d times after first POST, want exactly 1", overflow)
	}

	// Retry with the same key must re-execute (reservation gone), not replay or 409.
	second := postExportWithKey(e, "big")
	if second.Code != http.StatusOK {
		t.Fatalf("retry after overflow = %d, want 200", second.Code)
	}
	if calls != 2 {
		t.Fatalf("retry must re-execute the handler; ran %d times total, want 2", calls)
	}
	if second.Body.Len() != 2*(1<<20) {
		t.Fatalf("retry body truncated: %d bytes, want %d", second.Body.Len(), 2*(1<<20))
	}
}

func TestIdempotency_CaptureBoundaryExactlyCap(t *testing.T) {
	store := newMemStore()
	obs := &recordingObserver{}
	calls := 0
	e := setupIdemBodyRouter(store, obs, &calls, 1<<20) // exactly the 1 MiB cap

	first := postExportWithKey(e, "cap")
	if first.Code != http.StatusOK {
		t.Fatalf("exactly-at-cap POST = %d, want 200", first.Code)
	}
	if first.Body.Len() != 1<<20 {
		t.Fatalf("exactly-at-cap body truncated: %d bytes, want %d", first.Body.Len(), 1<<20)
	}

	obs.mu.Lock()
	overflow := obs.overflow
	obs.mu.Unlock()
	if overflow != 0 {
		t.Fatalf("Overflowed fired %d times for exactly-at-cap response, want 0", overflow)
	}
	if store.completeCalls != 1 {
		t.Fatalf("store.Complete called %d times, want 1 (exactly-at-cap is in-bounds)", store.completeCalls)
	}
	if !store.completed("u1", "POST /v1/export", "cap") {
		t.Error("exactly-at-cap response must be stored for replay")
	}

	second := postExportWithKey(e, "cap")
	if calls != 1 {
		t.Fatalf("retry must replay the stored response; handler ran %d times, want 1", calls)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replay should carry Idempotent-Replayed: true")
	}
}

func TestIdempotency_CaptureCapViaWriteString(t *testing.T) {
	store := newMemStore()
	obs := &recordingObserver{}
	calls := 0
	e := setupIdemWriteStringRouter(store, obs, &calls)

	first := postExportWithKey(e, "ws")
	if first.Code != http.StatusOK {
		t.Fatalf("WriteString oversized POST = %d, want 200", first.Code)
	}
	if first.Body.Len() != 2*(1<<20) {
		t.Fatalf("WriteString body not delivered intact: %d bytes, want %d", first.Body.Len(), 2*(1<<20))
	}

	obs.mu.Lock()
	overflow := obs.overflow
	obs.mu.Unlock()
	if overflow != 1 {
		t.Fatalf("Overflowed fired %d times, want 1", overflow)
	}
	if store.completeCalls != 0 {
		t.Fatalf("store.Complete called %d times on WriteString overflow, want 0", store.completeCalls)
	}
	if store.held("u1", "POST /v1/export", "ws") {
		t.Error("reservation must be released after WriteString overflow")
	}
}

func TestIdempotency_ObserverPanicDoesNotAffectResponse(t *testing.T) {
	store := newMemStore()
	store.failComplete = errors.New("boom") // force the observer callback to run
	calls := 0
	e := setupIdemRouter(store, panickingObserver{}, &calls)

	w := postWithKey(e, "abc")
	if w.Code != http.StatusCreated {
		t.Fatalf("observer panic must not change the response: got %d, want 201", w.Code)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1", calls)
	}
}
