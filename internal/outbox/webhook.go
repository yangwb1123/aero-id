package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// WebhookConfig configures delivery of outbox events to an external HTTP endpoint.
// When EventTypes is non-empty, only matching event types are delivered. An empty
// slice means all events are delivered. Headers are injected into every request.
type WebhookConfig struct {
	URL        string            `mapstructure:"url"`
	SigningKey string            `mapstructure:"signing_key"`
	Timeout    time.Duration     `mapstructure:"timeout"`
	EventTypes []string          `mapstructure:"event_types"`
	Headers    map[string]string `mapstructure:"headers"` // additional HTTP headers sent with every request
}

type webhookPayload struct {
	ID            string          `json:"id"`
	EventType     string          `json:"event_type"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"created_at"`
}

// WebhookHandler delivers outbox events to an external HTTP endpoint.
// It signs the JSON body with HMAC-SHA256 (X-Event-Signature header) so the
// receiver can verify authenticity. Non-2xx responses and timeouts cause the
// event to be retried on the next relay tick.
type WebhookHandler struct {
	cfg    WebhookConfig
	client *http.Client
	log    *zap.Logger
}

func NewWebhookHandler(cfg WebhookConfig, log *zap.Logger) *WebhookHandler {
	if log == nil {
		log = zap.NewNop()
	}
	return &WebhookHandler{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		log:    log,
	}
}

func (h *WebhookHandler) Handle(ctx context.Context, e *Event) error {
	if !h.matches(e.EventType) {
		return nil
	}
	payload := webhookPayload{
		ID:            e.ID.String(),
		EventType:     e.EventType,
		AggregateType: e.AggregateType,
		AggregateID:   e.AggregateID,
		Payload:       json.RawMessage(e.Payload),
		CreatedAt:     e.CreatedAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook marshal: %w", err)
	}

	sig := h.sign(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Signature", sig)
	for k, v := range h.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.log.Warn("webhook non-2xx response",
			zap.String("event_id", e.ID.String()),
			zap.Int("status", resp.StatusCode))
		return fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	return nil
}

// matches reports whether the event type should be delivered. An empty
// EventTypes filter matches everything.
func (h *WebhookHandler) matches(eventType string) bool {
	if len(h.cfg.EventTypes) == 0 {
		return true
	}
	for _, et := range h.cfg.EventTypes {
		if et == eventType {
			return true
		}
	}
	return false
}

func (h *WebhookHandler) sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(h.cfg.SigningKey))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
