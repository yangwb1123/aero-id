package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// WebhookSubscription represents a registered webhook endpoint that receives
// outbox events. Each subscription is owned by an SSO user and can filter by
// event types. The signing key is used for HMAC-SHA256 signature headers on
// delivery requests.
type WebhookSubscription struct {
	ID                     ID
	OwnerUID               string
	URL                    string
	EventTypes             []string // empty = all events
	SigningKey             string   // HMAC-SHA256 key (plaintext stored, encrypted at rest)
	Secret                 string   // optional additional secret for custom auth headers
	Enabled                bool
	MaxConsecutiveFailures int // auto-disable after this many consecutive failures (0 = never)
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// GenerateSigningKey creates a cryptographically random 32-byte hex-encoded key
// suitable for HMAC-SHA256 webhook signing. Returns a 64-character hex string.
func GenerateSigningKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// WebhookDelivery records one delivery attempt for an outbox event to a webhook
// subscription. Used for observability, debugging, and audit.
type WebhookDelivery struct {
	ID             ID
	SubscriptionID ID
	EventID        string // outbox event id
	Status         string // "success" or "failed"
	StatusCode     int    // HTTP response status code
	ResponseBody   string // truncated to 4KB
	Attempt        int
	DurationMs     int64
	CreatedAt      time.Time
}
