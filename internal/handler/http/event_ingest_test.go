package http_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type fakeEventIngestService struct{ calls int }

func (f *fakeEventIngestService) Ingest(context.Context, *event.Envelope) (*service.EventIngestResult, error) {
	f.calls++
	return &service.EventIngestResult{EventID: "accepted", Status: "applied"}, nil
}

func TestEventIngestHandler_VerifiesTimestampBoundSourceSignature(t *testing.T) {
	fake := &fakeEventIngestService{}
	handler := httpapi.NewEventIngestHandler(fake, "", 1<<20, 5*time.Minute,
		httpapi.WithEventSourceKeys(map[string]string{"aero-im": "source-secret"}),
		httpapi.WithEventTimestampRequired(true), httpapi.WithEventSourceBindings(event.SourceBindings{
			"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
		}))
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithIdentity(c.Request.Context(), &domain.TokenIdentity{
			UID: "source-client", TenantID: "tenant-1", ClientID: "source-client",
		}))
		c.Next()
	})
	handler.RegisterRoutes(httpapi.Routes{InternalAuth: engine.Group("")})

	body := validEventBody(t, "aero-im")
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/events", bytes.NewReader(body))
	req.Header.Set("X-Aero-Event-Timestamp", timestamp)
	req.Header.Set("X-Aero-Event-Signature", signEvent("source-secret", timestamp, body))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("ingest calls = %d, want 1", fake.calls)
	}
}

func TestEventIngestHandler_RejectsUnsignedTimestampAndWrongSourceKey(t *testing.T) {
	handler := httpapi.NewEventIngestHandler(&fakeEventIngestService{}, "", 1<<20, 5*time.Minute,
		httpapi.WithEventSourceKeys(map[string]string{"aero-im": "source-secret"}),
		httpapi.WithEventTimestampRequired(true), httpapi.WithEventSourceBindings(event.SourceBindings{
			"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
		}))
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithIdentity(c.Request.Context(), &domain.TokenIdentity{
			UID: "source-client", TenantID: "tenant-1", ClientID: "source-client",
		}))
		c.Next()
	})
	handler.RegisterRoutes(httpapi.Routes{InternalAuth: engine.Group("")})
	body := validEventBody(t, "aero-im")
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)

	tests := []struct {
		name      string
		signature string
		timestamp string
	}{
		{name: "body only signature", signature: signBody("source-secret", body), timestamp: timestamp},
		{name: "wrong source key", signature: signEvent("wrong-secret", timestamp, body), timestamp: timestamp},
		{name: "missing timestamp", signature: signBody("source-secret", body)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/events", bytes.NewReader(body))
			req.Header.Set("X-Aero-Event-Signature", tc.signature)
			if tc.timestamp != "" {
				req.Header.Set("X-Aero-Event-Timestamp", tc.timestamp)
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", w.Code, w.Body.String())
			}
		})
	}
}

func validEventBody(t *testing.T, source string) []byte {
	t.Helper()
	value := event.Envelope{EventID: idgen.NewID(), Source: source, SourceRegion: "west", EventType: "account.updated",
		SchemaVersion: 1, AggregateType: "account", AggregateID: "source-account", AggregateVersion: 1,
		TenantID: "tenant-1", OccurredAt: time.Now().UTC(), Payload: map[string]any{
			"account_id": idgen.NewID().String(), "dataset": string(domain.AccountDataset("aero-im.profile")), "data": map[string]any{},
		}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func signEvent(key, timestamp string, body []byte) string {
	return signBytes(key, append([]byte(timestamp+"."), body...))
}

func signBody(key string, body []byte) string { return signBytes(key, body) }

func signBytes(key string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
