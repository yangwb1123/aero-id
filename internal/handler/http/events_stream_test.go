package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const sseTestWait = 2 * time.Second

// The handler owns the recorder until done closes. Tests observe only immutable
// snapshots sent by Flush, never the recorder's mutable header/body/status.
type sseTestRecorder struct {
	*httptest.ResponseRecorder
	ctx       context.Context
	ready     chan struct{}
	readyOnce sync.Once
	flushed   chan string
}

func (w *sseTestRecorder) Header() http.Header {
	header := w.ResponseRecorder.Header()
	// EventsHandler subscribes before setting Content-Type. A subsequent
	// Header call observes that assignment on the handler goroutine, so the
	// test can publish a live marker without guessing when subscription exists.
	if header.Get("Content-Type") == "text/event-stream" {
		w.readyOnce.Do(func() { close(w.ready) })
	}
	return header
}

func (w *sseTestRecorder) Flush() {
	w.ResponseRecorder.Flush()
	select {
	case w.flushed <- w.Body.String():
	case <-w.ctx.Done(): // Failed assertions must not strand the handler here.
	}
}

type sseTestStream struct {
	recorder *sseTestRecorder
	done     chan struct{}
	cancel   context.CancelFunc
}

func startSSETestStream(t *testing.T, handler http.Handler, req *http.Request) *sseTestStream {
	t.Helper()
	ctx, cancel := context.WithCancel(req.Context())
	stream := &sseTestStream{
		recorder: &sseTestRecorder{
			ResponseRecorder: httptest.NewRecorder(), ctx: ctx,
			ready: make(chan struct{}), flushed: make(chan string),
		},
		done: make(chan struct{}), cancel: cancel,
	}
	t.Cleanup(func() { stream.stop(t) })
	go func() {
		defer close(stream.done)
		handler.ServeHTTP(stream.recorder, req.WithContext(ctx))
	}()
	return stream
}

func (s *sseTestStream) waitReady(t *testing.T) {
	t.Helper()
	select {
	case <-s.recorder.ready:
	case <-s.done:
		t.Fatal("handler returned before SSE subscription was ready")
	case <-time.After(sseTestWait):
		t.Fatal("timed out waiting for SSE subscription")
	}
}

func (s *sseTestStream) nextFlush(t *testing.T) string {
	t.Helper()
	select {
	case body := <-s.recorder.flushed:
		return body
	case <-s.done:
		t.Fatal("handler returned before expected SSE flush")
	case <-time.After(sseTestWait):
		t.Fatal("timed out waiting for SSE flush")
	}
	return ""
}

func (s *sseTestStream) stop(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(sseTestWait):
		t.Fatal("handler did not return after cancel")
	}
}

func TestSSETestStreamCancelUnconsumedFlush(t *testing.T) {
	// An unbuffered snapshot has no receiver. Cancellation must still release
	// Flush and join the handler, including when another assertion fails first.
	entered := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		w.(http.Flusher).Flush()
	})
	stream := startSSETestStream(t, handler, httptest.NewRequest(http.MethodGet, "/", nil))
	select {
	case <-entered:
	case <-time.After(sseTestWait):
		t.Fatal("handler did not start")
	}
	stream.stop(t)
	if !stream.recorder.Flushed {
		t.Fatal("handler never reached Flush before returning")
	}
}
