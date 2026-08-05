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
	"github.com/aero/aero-id/internal/pkg/bulkhead"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type CallbackService interface {
	Register(context.Context, CallbackInput) (*domain.CallbackSubscription, string, error)
	List(context.Context, domain.Page) ([]*domain.CallbackSubscription, string, error)
	Delete(context.Context, domain.ID) error
	Publish(context.Context, *domain.AuditFact) error
	ReplayDelivery(context.Context, domain.ID) error
	Start(context.Context, time.Duration) func()
}

type CallbackInput struct {
	Endpoint   string
	TenantID   string
	EventTypes []string
}

type CallbackConfig struct {
	MasterKey          string
	RequestTimeout     time.Duration
	MaxResponseBytes   int64
	MaxAttempts        int
	RetryBase          time.Duration
	AllowPrivateTarget bool
	WorkerConcurrency  int // in-flight deliveries/publications (P6-06); <=0 => serial
}

type callbackService struct {
	repo         repository.CallbackRepository
	cfg          CallbackConfig
	key          []byte
	worker       string
	audit        AuditLedgerService
	publications repository.AuditPublicationRepository
	publishers   []AuditPublisher
	bulkhead     *bulkhead.Bulkhead
	roundTripper http.RoundTripper // test seam; nil = default transport
}

func NewCallbackService(repo repository.CallbackRepository, cfg CallbackConfig) CallbackService {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 64 * 1024
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 8
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = time.Second
	}
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 1
	}
	if cfg.WorkerConcurrency > 64 {
		cfg.WorkerConcurrency = 64
	}
	sum := sha256.Sum256([]byte(cfg.MasterKey))
	return &callbackService{repo: repo, cfg: cfg, key: sum[:], worker: "callback-" + idgen.NewID().String(),
		bulkhead: bulkhead.New(cfg.WorkerConcurrency)}
}

// SetAuditLogger is called by the composition root after the callback service
// and durable audit service are constructed. The setter avoids a construction
// cycle: audit publishes through callbacks, while callback administration is
// itself audited.
func (s *callbackService) SetAuditLogger(audit AuditLedgerService) { s.audit = audit }

func (s *callbackService) SetAuditPublicationRepository(publications repository.AuditPublicationRepository) {
	s.publications = publications
}

func (s *callbackService) SetAuditPublishers(publishers ...AuditPublisher) {
	s.publishers = append([]AuditPublisher(nil), publishers...)
}

//nolint:cyclop // callback registration combines endpoint policy, secret protection, persistence, and audit.
func (s *callbackService) Register(ctx context.Context, input CallbackInput) (*domain.CallbackSubscription, string, error) {
	owner, err := mustActor(ctx)
	if err != nil {
		return nil, "", err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, "", err
	}
	if requestedTenant := strings.TrimSpace(input.TenantID); requestedTenant != "" && requestedTenant != tenantID {
		return nil, "", apperr.Forbidden("callback.tenant_forbidden", "callback tenant does not match authenticated tenant")
	}
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if input.Endpoint == "" || len(input.Endpoint) > 2048 || len(s.cfg.MasterKey) == 0 {
		return nil, "", apperr.BadRequest("callback.not_configured", "callback endpoint or encryption key is not configured")
	}
	eventTypes, err := normalizeCallbackEventTypes(input.EventTypes)
	if err != nil {
		return nil, "", err
	}
	if err := validateCallbackEndpoint(input.Endpoint, s.cfg.AllowPrivateTarget); err != nil {
		return nil, "", apperr.BadRequest("callback.endpoint_invalid", "callback endpoint is not allowed").Wrap(err)
	}
	challenge, err := randomChallenge()
	if err != nil {
		return nil, "", apperr.Internal("callback.challenge_failed", "failed to create endpoint challenge").Wrap(err)
	}
	if err := verifyCallbackEndpoint(ctx, input.Endpoint, challenge, s.cfg); err != nil {
		return nil, "", apperr.BadRequest("callback.verification_failed", "callback endpoint verification failed")
	}
	secret, err := randomSecret()
	if err != nil {
		return nil, "", apperr.Internal("callback.secret_failed", "failed to create callback secret").Wrap(err)
	}
	ciphertext, err := s.encrypt(secret)
	if err != nil {
		return nil, "", apperr.Internal("callback.secret_failed", "failed to protect callback secret").Wrap(err)
	}
	subscription := &domain.CallbackSubscription{OwnerUID: owner, TenantID: tenantID, Endpoint: input.Endpoint,
		EventTypes: eventTypes, SecretCiphertext: ciphertext, Enabled: true, Verified: true}
	if err := s.repo.CreateSubscription(ctx, subscription); err != nil {
		return nil, "", apperr.Internal("callback.create_failed", "failed to create callback subscription").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: subscription.ID, ActorUID: owner,
			TenantID: tenantID, Action: "callback.registered", ResourceType: "callback_subscription",
			ResourceID: subscription.ID.String(), Payload: map[string]any{
				"endpoint_sha256": hashAuditBytes([]byte(input.Endpoint)), "event_types": eventTypes,
			}})
	}
	return subscription, hex.EncodeToString(secret), nil
}

func normalizeCallbackEventTypes(values []string) ([]string, error) {
	if len(values) > 64 {
		return nil, apperr.BadRequest("callback.event_types_too_many", "too many callback event filters")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 {
			return nil, apperr.BadRequest("callback.event_type_invalid", "callback event filter is invalid")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (s *callbackService) List(ctx context.Context, page domain.Page) ([]*domain.CallbackSubscription, string, error) {
	owner, err := mustActor(ctx)
	if err != nil {
		return nil, "", err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, "", err
	}
	items, next, err := s.repo.ListSubscriptions(ctx, owner, tenantID, page)
	if err != nil {
		return nil, "", apperr.Internal("callback.list_failed", "failed to list callback subscriptions").Wrap(err)
	}
	return items, next, nil
}

func (s *callbackService) Delete(ctx context.Context, id domain.ID) error {
	owner, err := mustActor(ctx)
	if err != nil {
		return err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteSubscription(ctx, owner, tenantID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("callback.not_found", "callback subscription not found")
		}
		return apperr.Internal("callback.delete_failed", "failed to delete callback subscription").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: id, ActorUID: owner,
			TenantID: tenantID, Action: "callback.deleted", ResourceType: "callback_subscription", ResourceID: id.String()})
	}
	return nil
}

func (s *callbackService) ReplayDelivery(ctx context.Context, id domain.ID) error {
	owner, err := mustActor(ctx)
	if err != nil {
		return err
	}
	delivery, err := s.repo.GetDelivery(ctx, id)
	if err != nil {
		return apperr.NotFound("callback.delivery_not_found", "callback delivery not found")
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return err
	}
	if delivery.OwnerUID != owner || delivery.TenantID != tenantID {
		return apperr.NotFound("callback.delivery_not_found", "callback delivery not found")
	}
	if err := s.repo.ResetDelivery(ctx, id); err != nil {
		return apperr.Internal("callback.delivery_replay_failed", "failed to replay callback delivery").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: id, ActorUID: owner,
			Action: "callback.delivery.replayed", ResourceType: "callback_delivery", ResourceID: id.String(),
			Payload: map[string]any{"action": delivery.Action}})
	}
	return nil
}

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
			case <-workerCtx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
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
	_ = s.repo.MarkDelivery(ctx, delivery.ID, s.worker, status, statusCode, response, errorCode, next)
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

func (s *callbackService) encrypt(value []byte) (string, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(append(nonce, gcm.Seal(nil, nonce, value, nil)...)), nil
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
	ips, err := net.LookupIP(parsed.Hostname())
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

func randomSecret() ([]byte, error) {
	value := make([]byte, 32)
	_, err := rand.Read(value)
	return value, err
}
