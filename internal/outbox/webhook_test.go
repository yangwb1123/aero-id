package outbox_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/outbox"
)

func TestWebhookHandler_PostsEvent(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{
		ID:            uuid.Must(uuid.NewV7()),
		EventType:     "message.sent",
		AggregateType: "message",
		AggregateID:   "m1",
		Payload:       []byte(`{"from":"u1","to":"u2"}`),
		CreatedAt:     time.Now().UTC(),
	}

	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Event-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:        srv.URL,
		SigningKey: "test-key",
		Timeout:    time.Second,
	}, nil)
	if err := h.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID != e.ID.String() {
		t.Errorf("id = %q, want %q", parsed.ID, e.ID.String())
	}

	mac := hmac.New(sha256.New, []byte("test-key"))
	mac.Write(gotBody)
	wantSig := hex.EncodeToString(mac.Sum(nil))
	if gotSig != wantSig {
		t.Errorf("signature mismatch: got %q, want %q", gotSig, wantSig)
	}
}

func TestWebhookHandler_Non2xxRetries(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "test"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:     srv.URL,
		Timeout: time.Second,
	}, nil)
	if err := h.Handle(ctx, e); err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestWebhookHandler_Timeout(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "test"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:     srv.URL,
		Timeout: 10 * time.Millisecond,
	}, nil)
	if err := h.Handle(ctx, e); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWebhookHandler_FiltersEventType(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "message.sent"}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:        srv.URL,
		EventTypes: []string{"comment.replied"},
		Timeout:    time.Second,
	}, nil)
	if err := h.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("handler should not be called for non-matching event type")
	}
}

func TestWebhookHandler_MatchingEventTypeDelivers(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "comment.replied"}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:        srv.URL,
		EventTypes: []string{"comment.replied", "message.sent"},
		Timeout:    time.Second,
	}, nil)
	if err := h.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler should be called for matching event type")
	}
}

func TestWebhookHandler_EmptyEventTypesDeliversAll(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "unknown.event"}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:     srv.URL,
		Timeout: time.Second,
	}, nil)
	if err := h.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler should be called when EventTypes is empty")
	}
}

func TestWebhookHandler_CustomHeaders(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "test"}
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer tok-secret"},
		Timeout: time.Second,
	}, nil)
	if err := h.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestWebhookHandler_EmptyHeadersNoop(t *testing.T) {
	ctx := context.Background()
	e := &outbox.Event{ID: uuid.Must(uuid.NewV7()), EventType: "test"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("Authorization"); v != "" {
			t.Errorf("unexpected Authorization: %q", v)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:     srv.URL,
		Timeout: time.Second,
	}, nil)
	if err := h.Handle(ctx, e); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookHandler_EmptyURL(t *testing.T) {
	// A handler with an empty URL is still created; it will fail on dispatch.
	h := outbox.NewWebhookHandler(outbox.WebhookConfig{
		URL:     "",
		Timeout: time.Second,
	}, nil)
	if err := h.Handle(context.Background(), &outbox.Event{ID: uuid.Must(uuid.NewV7())}); err == nil {
		t.Fatal("expected error for empty URL")
	}
}
