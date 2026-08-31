package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/middleware"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type EventIngestHandler struct {
	service      service.EventIngestService
	resolver     event.SourceBindingResolver
	permissions  middleware.PermissionChecker
	signingKey   []byte
	sourceKeys   map[string][]byte
	requireStamp bool
	maxBodyBytes int64
	clockSkew    time.Duration
	now          func() time.Time
}

const maxEventBatch = 100

type EventIngestOption func(*EventIngestHandler)

func WithEventSourceKeys(values map[string]string) EventIngestOption {
	return func(h *EventIngestHandler) {
		h.sourceKeys = make(map[string][]byte, len(values))
		for source, key := range values {
			if strings.TrimSpace(source) != "" && strings.TrimSpace(key) != "" {
				h.sourceKeys[strings.TrimSpace(source)] = []byte(key)
			}
		}
	}
}

func WithEventTimestampRequired(required bool) EventIngestOption {
	return func(h *EventIngestHandler) { h.requireStamp = required }
}

func WithEventSourceBindings(bindings event.SourceBindings) EventIngestOption {
	return func(h *EventIngestHandler) { h.resolver = bindings }
}

func WithEventSourceBindingResolver(resolver event.SourceBindingResolver) EventIngestOption {
	return func(h *EventIngestHandler) { h.resolver = resolver }
}

func WithEventPermissionChecker(checker middleware.PermissionChecker) EventIngestOption {
	return func(h *EventIngestHandler) { h.permissions = checker }
}

func NewEventIngestHandler(ingest service.EventIngestService, signingKey string, maxBodyBytes int64, clockSkew time.Duration,
	options ...EventIngestOption) *EventIngestHandler {
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20
	}
	if clockSkew <= 0 {
		clockSkew = 5 * time.Minute
	}
	h := &EventIngestHandler{
		service: ingest, signingKey: []byte(signingKey), maxBodyBytes: maxBodyBytes,
		clockSkew: clockSkew, now: time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	return h
}

func (h *EventIngestHandler) RegisterRoutes(r Routes) {
	group := r.InternalControlPlane.Group("/internal/v1/events")
	if h.permissions != nil {
		group.Use(middleware.RequirePermission(domain.PermSourceEventIngest, h.permissions))
	}
	group.POST("", h.Ingest)
}

func (h *EventIngestHandler) Ingest(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, h.maxBodyBytes+1))
	if err != nil || int64(len(body)) > h.maxBodyBytes {
		httpx.WriteError(c, apperr.BadRequest("event.body_invalid", "event body is invalid or too large"))
		return
	}
	events, err := decodeEvents(body)
	if err != nil || len(events) == 0 || len(events) > maxEventBatch {
		httpx.WriteError(c, apperr.BadRequest("event.batch_invalid", "event batch must contain one to one hundred events"))
		return
	}
	if err := h.bindSource(c, events); err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.verify(c, body, events); err != nil {
		httpx.WriteError(c, err)
		return
	}
	results := make([]*service.EventIngestResult, 0, len(events))
	for i := range events {
		result, err := h.service.Ingest(c.Request.Context(), &events[i])
		if err != nil {
			httpx.WriteError(c, err)
			return
		}
		results = append(results, result)
	}
	httpx.OK(c, gin.H{"events": results})
}

func (h *EventIngestHandler) bindSource(c *gin.Context, events []event.Envelope) error {
	identity, _ := service.IdentityFrom(c.Request.Context())
	if h.resolver == nil {
		return apperr.Forbidden("event.source_unauthorized", "event source is not authorized")
	}
	binding, err := h.resolver.Resolve(identity)
	if err != nil {
		return apperr.Forbidden("event.source_unauthorized", "event source is not authorized")
	}
	for index := range events {
		if err := binding.Apply(&events[index]); err != nil {
			return apperr.Forbidden("event.source_unauthorized", "event source is not authorized")
		}
	}
	return nil
}

func (h *EventIngestHandler) verify(c *gin.Context, body []byte, events []event.Envelope) error {
	key, err := h.signatureKey(events)
	if err != nil {
		return err
	}
	provided, err := decodeEventSignature(c.GetHeader("X-Aero-Event-Signature"))
	if err != nil {
		return apperr.Unauthorized("event.signature_invalid", "event signature is invalid")
	}
	raw := strings.TrimSpace(c.GetHeader("X-Aero-Event-Timestamp"))
	if raw == "" && h.requireStamp {
		return apperr.Unauthorized("event.timestamp_required", "event timestamp is required")
	}
	if !validEventRequestSignature(provided, key, body, raw, h.requireStamp) {
		return apperr.Unauthorized("event.signature_invalid", "event signature is invalid")
	}
	if raw != "" && !validEventTimestamp(raw, h.now(), h.clockSkew) {
		return apperr.Unauthorized("event.timestamp_invalid", "event timestamp is outside the acceptance window")
	}
	return nil
}

func decodeEventSignature(raw string) ([]byte, error) {
	signature := strings.TrimSpace(raw)
	if strings.HasPrefix(signature, "sha256=") {
		signature = strings.TrimPrefix(signature, "sha256=")
	}
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != sha256.Size {
		return nil, apperr.Unauthorized("event.signature_invalid", "event signature is invalid")
	}
	return provided, nil
}

func validEventRequestSignature(provided, key, body []byte, timestamp string, requireTimestamp bool) bool {
	message := body
	if timestamp != "" {
		message = append([]byte(timestamp+"."), body...)
	}
	if validEventSignature(provided, key, message) {
		return true
	}
	return !requireTimestamp && timestamp != "" && validEventSignature(provided, key, body)
}

func validEventTimestamp(raw string, now time.Time, skew time.Duration) bool {
	stamp, err := time.Parse(time.RFC3339, raw)
	return err == nil && now.Sub(stamp) <= skew && stamp.Sub(now) <= skew
}

func validEventSignature(provided, key, message []byte) bool {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	return subtle.ConstantTimeCompare(provided, mac.Sum(nil)) == 1
}

func (h *EventIngestHandler) signatureKey(events []event.Envelope) ([]byte, error) {
	if len(h.sourceKeys) == 0 {
		if len(h.signingKey) == 0 {
			return nil, apperr.Unauthorized("event.signature_not_configured", "event signature is not configured")
		}
		return h.signingKey, nil
	}
	source := strings.TrimSpace(events[0].Source)
	if source == "" {
		return nil, apperr.Unauthorized("event.source_signature_invalid", "event source is required")
	}
	key, ok := h.sourceKeys[source]
	if !ok || len(key) == 0 {
		return nil, apperr.Unauthorized("event.signature_not_configured", "event source signature is not configured")
	}
	for _, item := range events[1:] {
		if strings.TrimSpace(item.Source) != source {
			return nil, apperr.BadRequest("event.mixed_sources", "a signed event batch must contain one source")
		}
	}
	return key, nil
}

func decodeEvents(body []byte) ([]event.Envelope, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var result []event.Envelope
		return result, json.Unmarshal(body, &result)
	}
	var wrapper struct {
		Events []event.Envelope `json:"events"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Events != nil {
		return wrapper.Events, nil
	}
	var single event.Envelope
	if err := json.Unmarshal(body, &single); err != nil {
		return nil, err
	}
	return []event.Envelope{single}, nil
}
