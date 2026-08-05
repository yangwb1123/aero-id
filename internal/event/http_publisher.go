package event

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
	"strings"
	"time"
)

// HTTPPublisher delivers canonical envelopes to a remote region's ingest
// endpoint using the same signature scheme the ingest handler verifies
// (X-Aero-Event-Signature with timestamp-prefixed body).
type HTTPPublisher struct {
	endpoint   string
	signingKey []byte
	client     *http.Client
}

// NewHTTPPublisher builds a publisher for remote HTTP ingest. signingKey must
// match the remote event_ingest signing_key (or per-source key for "aero-id").
func NewHTTPPublisher(endpoint string, signingKey string, client *http.Client) (*HTTPPublisher, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || signingKey == "" {
		return nil, fmt.Errorf("http publisher endpoint and signing key are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &HTTPPublisher{endpoint: endpoint, signingKey: []byte(signingKey), client: client}, nil
}

func (p *HTTPPublisher) Target() string { return "http:" + p.endpoint }

func (p *HTTPPublisher) Publish(ctx context.Context, envelope *Envelope) error {
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal canonical envelope: %w", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	message := append([]byte(timestamp+"."), body...)
	mac := hmac.New(sha256.New, p.signingKey)
	_, _ = mac.Write(message)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build publish request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Aero-Event-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-Aero-Event-Timestamp", timestamp)
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("publish projection: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("publish projection: remote ingest returned HTTP %d", response.StatusCode)
	}
	return nil
}
