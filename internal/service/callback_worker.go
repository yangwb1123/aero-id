package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

// callback_worker.go owns the delivery/publication worker loop (Start), the
// dead-letter retention purge (purgeDeadLetters), the deliveries drain
// (drain), and every delivery-attempt helper (markDelivery, deliver,
// retryDelay, decrypt, httpClient, and the endpoint validation policy).
// The request path (Register/List/Delete/ReplayDelivery) lives in
// callback.go; the audit publication drain lives in callback_audit.go.

func (s *callbackService) Start(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Second
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-ticker.C:
				s.drainPublications(workerCtx)
				s.drain(workerCtx)
				s.purgeDeadLetters(workerCtx)
			case <-workerCtx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

// purgeDeadLetters is the bounded dead-letter retention scan (B2-5 WORKER-1):
// one keyset-ordered batch per kind per tick. It is a no-op unless a positive
// retention is configured and purge_pause is clear; paused ticks perform no
// SELECT and no DELETE, so dead-letter rows are preserved bit-for-bit while a
// replay run (cmd/audit-replay) recovers them. The purge predicates exclude
// leased rows at the SQL boundary (lease_owner empty and lease expiry).
func (s *callbackService) purgeDeadLetters(ctx context.Context) {
	if s.cfg.DeadLetterRetention <= 0 || s.cfg.PurgePause {
		return
	}
	cutoff := time.Now().UTC().Add(-s.cfg.DeadLetterRetention)
	if s.publications != nil {
		purged, err := s.publications.PurgeDeadLetterPublications(ctx, cutoff, 50)
		if err != nil {
			s.metrics.ObserveAuditPublicationPurgeFailed("publication", err)
		} else if purged > 0 {
			s.metrics.ObserveAuditPublicationPurged("publication", purged)
		}
	}
	purged, err := s.repo.PurgeDeadLetterDeliveries(ctx, cutoff, 50)
	if err != nil {
		s.metrics.ObserveAuditPublicationPurgeFailed("delivery", err)
	} else if purged > 0 {
		s.metrics.ObserveAuditPublicationPurged("delivery", purged)
	}
}

func (s *callbackService) drain(ctx context.Context) {
	deliveries, err := s.repo.FetchDueDeliveries(ctx, 50, s.worker, 5*time.Minute)
	if err != nil {
		return
	}
	// P6-06 bulkhead: concurrent deliveries are bounded by WorkerConcurrency;
	// each delivery row is leased to this worker, so parallel marking is safe.
	var wg sync.WaitGroup
	for _, delivery := range deliveries {
		delivery := delivery
		if err := s.bulkhead.Acquire(ctx); err != nil {
			break // context cancelled; remaining rows stay due for the next tick
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer s.bulkhead.Release()
			s.markDelivery(ctx, delivery)
		}()
	}
	wg.Wait()
}

func (s *callbackService) markDelivery(ctx context.Context, delivery *domain.CallbackDelivery) {
	statusCode, response, deliverErr := s.deliver(ctx, delivery)
	status := "success"
	errorCode := ""
	next := time.Now().UTC()
	if deliverErr != nil || statusCode < 200 || statusCode >= 300 {
		status = "pending"
		errorCode = "callback.delivery_failed"
		if delivery.Attempt+1 >= s.cfg.MaxAttempts {
			status = "dead_letter"
		}
		next = next.Add(s.retryDelay(delivery.Attempt + 1))
	}
	if err := s.repo.MarkDelivery(ctx, delivery.ID, s.worker, status, statusCode, response, errorCode, next); err != nil {
		// Settlement failed: keep the row delivering+leased (the existing
		// 5-minute lease re-fetch re-claims it with attempt unchanged) and
		// observe the failure on the outcome counter — never report
		// success/pending/dead_letter for a settlement that did not happen.
		s.metrics.ObserveCallbackOutcome("mark_failed")
		return
	}
	s.metrics.ObserveCallbackOutcome(status)
}

func (s *callbackService) deliver(ctx context.Context, delivery *domain.CallbackDelivery) (int, string, error) {
	if err := validateCallbackEndpoint(delivery.Endpoint, s.cfg.AllowPrivateTarget); err != nil {
		return 0, "", err
	}
	secret, err := s.decrypt(delivery.SecretCiphertext)
	if err != nil {
		return 0, "", err
	}
	body, err := json.Marshal(delivery.Payload)
	if err != nil {
		return 0, "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, delivery.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Aero-Callback-Schema-Version", "1")
	request.Header.Set("X-Aero-Callback-Event-ID", delivery.EventID.String())
	request.Header.Set("X-Aero-Callback-Attempt", fmt.Sprintf("%d", delivery.Attempt+1))
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	request.Header.Set("X-Aero-Callback-Timestamp", timestamp)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp + "\n"))
	_, _ = mac.Write(body)
	request.Header.Set("X-Aero-Callback-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	client, err := s.httpClient(delivery.Endpoint)
	if err != nil {
		return 0, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, s.cfg.MaxResponseBytes))
	return response.StatusCode, string(data), nil
}

func (s *callbackService) retryDelay(attempt int) time.Duration {
	delay := s.cfg.RetryBase
	for i := 1; i < attempt && delay < time.Hour; i++ {
		if delay > time.Hour/2 {
			delay = time.Hour
			break
		}
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	// Add bounded cryptographic jitter so many callback workers do not retry
	// the same endpoint in lockstep after an outage. Failure to obtain one
	// random byte is harmless: the deterministic cap remains safe.
	var randomByte [1]byte
	if _, err := rand.Read(randomByte[:]); err != nil {
		return delay
	}
	jitter := delay / 4 * time.Duration(randomByte[0]) / 255
	if delay > time.Hour-jitter {
		return time.Hour
	}
	return delay + jitter
}
func (s *callbackService) decrypt(value string) ([]byte, error) {
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(data) < gcm.NonceSize() {
		return nil, errors.New("invalid callback secret")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

//nolint:cyclop // endpoint validation is deliberately fail-closed across URL, DNS, and private-network checks.
func (s *callbackService) httpClient(endpoint string) (*http.Client, error) {
	client, err := newCallbackHTTPClient(endpoint, s.cfg)
	if err != nil {
		return nil, err
	}
	if s.roundTripper != nil {
		client.Transport = s.roundTripper
	}
	return client, nil
}

func validateCallbackEndpoint(raw string, allowPrivate bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("endpoint must be an https URL without userinfo")
	}
	if allowPrivate {
		return nil
	}
	if isPrivateHost(parsed.Hostname()) {
		return errors.New("private callback targets are not allowed")
	}
	return validateCallbackResolvesPublic(parsed.Hostname())
}

// validateCallbackResolvesPublic rejects hostnames that do not resolve or that
// resolve to private addresses (SSRF guard: no DNS-rebinding vector).
func validateCallbackResolvesPublic(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return errors.New("callback hostname cannot be resolved")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return errors.New("callback resolves to a private address")
		}
	}
	return nil
}

func isPrivateHost(host string) bool {
	return strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local")
}
