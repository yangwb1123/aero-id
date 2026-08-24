package middleware

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	idempotencyReplayRequiredKey = "aero-id.idempotency.replay_required"
	idempotencyReplayBodyKey     = "aero-id.idempotency.replay_body"
)

// RequireIdempotencyReplayOnSuccess marks the current response as requiring an
// explicit secret-free replay representation. It is intentionally generic so
// other one-time-secret endpoints can use the same boundary.
func RequireIdempotencyReplayOnSuccess(c *gin.Context) {
	if c != nil {
		c.Set(idempotencyReplayRequiredKey, true)
	}
}

// SetIdempotencyReplayBody installs the body that may be persisted for a
// marked successful response. The middleware validates and copies it before
// the handler writes the live response; it never falls back to the captured
// body when this policy is active.
func SetIdempotencyReplayBody(c *gin.Context, body []byte) error {
	if err := validateReplayBody(body); err != nil {
		return err
	}
	if c == nil {
		return errors.New("idempotency replay context is nil")
	}
	c.Set(idempotencyReplayRequiredKey, true)
	c.Set(idempotencyReplayBodyKey, append([]byte(nil), body...))
	return nil
}

func idempotencyReplayPolicy(c *gin.Context) (required bool, body []byte) {
	if c == nil {
		return false, nil
	}
	value, ok := c.Get(idempotencyReplayRequiredKey)
	if !ok {
		return false, nil
	}
	required, _ = value.(bool)
	if !required {
		return false, nil
	}
	value, _ = c.Get(idempotencyReplayBodyKey)
	body, _ = value.([]byte)
	return true, append([]byte(nil), body...)
}

func validateReplayBody(body []byte) error {
	if len(body) == 0 {
		return errors.New("idempotency replay body is empty")
	}
	if len(body) > maxIdempotencyCaptureBytes {
		return fmt.Errorf("idempotency replay body exceeds %d bytes", maxIdempotencyCaptureBytes)
	}
	if !json.Valid(body) {
		return errors.New("idempotency replay body is not valid JSON")
	}
	envelope, err := decodeReplayEnvelope(body)
	if err != nil {
		return err
	}
	return validateReplayEnvelope(envelope)
}

func decodeReplayEnvelope(body []byte) (map[string]json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("idempotency replay body must be a JSON object")
	}
	return envelope, nil
}

func validateReplayEnvelope(envelope map[string]json.RawMessage) error {
	data, ok := envelope["data"]
	if !ok || len(data) == 0 {
		return errors.New("idempotency replay body is missing data")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil || len(payload) == 0 {
		return errors.New("idempotency replay body has invalid data")
	}
	if _, ok := payload["subscription"]; !ok {
		return errors.New("idempotency replay body is missing subscription")
	}
	if containsUnsafeReplayKey(envelope) {
		return errors.New("idempotency replay body contains secret-bearing fields")
	}
	return nil
}

func containsUnsafeReplayKey(value map[string]json.RawMessage) bool {
	for key, raw := range value {
		if unsafeReplayKey(key) {
			return true
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) == nil && containsUnsafeReplayKey(nested) {
			return true
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			for _, item := range items {
				if json.Unmarshal(item, &nested) == nil && containsUnsafeReplayKey(nested) {
					return true
				}
			}
		}
	}
	return false
}

func unsafeReplayKey(key string) bool {
	canonical := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(key))
	for _, part := range []string{"secret", "password", "access_token", "refreshtoken", "privatekey", "authorization", "cookie"} {
		part = strings.ReplaceAll(part, "_", "")
		if strings.Contains(canonical, part) {
			return true
		}
	}
	return canonical == "token" || canonical == "tokens"
}
