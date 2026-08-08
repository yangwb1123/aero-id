package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
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
	// PublicationTimeout bounds one governance publish (M-5); 0 = 5s default
	// (matches audit_governance.timeout's connector default). It sizes the
	// drain lease: lease = max(5m, 2 × 50 × timeout / concurrency) so a slow
	// batch can never outlive its own lease.
	PublicationTimeout time.Duration
	// PurgePause suspends the dead-letter purge scan (B2-5 WORKER-1): while
	// set, no dead_letter publication or delivery row is selected or deleted.
	PurgePause bool
	// DeadLetterRetention bounds dead-letter survival before the drain loop
	// purges the row (0 = never purge — the default).
	DeadLetterRetention time.Duration
}

type callbackService struct {
	repo         repository.CallbackRepository
	cfg          CallbackConfig
	key          []byte
	worker       string
	publications repository.AuditPublicationRepository
	publishers   []AuditPublisher
	bulkhead     *bulkhead.Bulkhead
	roundTripper http.RoundTripper // test seam; nil = default transport
	metrics      ControlPlaneMetrics
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
		bulkhead: bulkhead.New(cfg.WorkerConcurrency), metrics: noopControlPlaneMetrics{}}
}

// SetControlPlaneMetrics attaches the observability sink (called by the
// composition root; safe before Start).
func (s *callbackService) SetControlPlaneMetrics(metrics ControlPlaneMetrics) {
	if metrics != nil {
		s.metrics = metrics
	}
}

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
	// Pre-assign the ID so the audit fact (EventID = subscription.ID) is known
	// before the transaction opens (repo ID-when-empty precedent makes this safe).
	subscription.ID = idgen.NewID()
	if err := s.registerWithAudit(ctx, subscription, owner, tenantID, input.Endpoint, eventTypes); err != nil {
		return nil, "", apperr.Internal("callback.create_failed", "failed to create callback subscription").Wrap(err)
	}
	return subscription, hex.EncodeToString(secret), nil
}

// registerWithAudit persists the subscription and its callback.registered fact
// in one transaction (the callback repo owns the ledger; a failed assert is a
// test-wiring error and fails closed — never a silent plain write).
func (s *callbackService) registerWithAudit(ctx context.Context, subscription *domain.CallbackSubscription,
	owner, tenantID, endpoint string, eventTypes []string) error {
	repo, ok := s.repo.(repository.CallbackAuditRepository)
	if !ok {
		return apperr.Internal("callback.audit_unavailable", "callback audit persistence is unavailable")
	}
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id", EventID: subscription.ID, ActorUID: owner,
		TenantID: tenantID, Action: "callback.registered", ResourceType: "callback_subscription",
		ResourceID: subscription.ID.String(), Payload: map[string]any{
			"endpoint_sha256": hashAuditBytes([]byte(endpoint)), "event_types": eventTypes,
		}})
	return repo.CreateSubscriptionWithAudit(ctx, subscription, fact)
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
	repo, ok := s.repo.(repository.CallbackAuditRepository)
	if !ok {
		return apperr.Internal("callback.audit_unavailable", "callback audit persistence is unavailable")
	}
	if err := repo.DeleteSubscriptionWithAudit(ctx, owner, tenantID, id, buildAuditFact(ctx, AuditFactInput{Source: "aero-id", EventID: id, ActorUID: owner,
		TenantID: tenantID, Action: "callback.deleted", ResourceType: "callback_subscription", ResourceID: id.String()})); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("callback.not_found", "callback subscription not found")
		}
		if errors.Is(err, repository.ErrAuditDuplicate) {
			return nil
		}
		return apperr.Internal("callback.delete_failed", "failed to delete callback subscription").Wrap(err)
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
	repo, ok := s.repo.(repository.CallbackAuditRepository)
	if !ok {
		return apperr.Internal("callback.audit_unavailable", "callback audit persistence is unavailable")
	}
	// C2: fresh EventID per replay intent (precedent: operationAuditFact) — a
	// second replay of a re-failed delivery is a new user intent that must
	// re-apply the reset and record its own fact; anchoring EventID to the
	// delivery ID would roll the reset back with ErrAuditDuplicate.
	if err := repo.ResetDeliveryWithAudit(ctx, id, buildAuditFact(ctx, AuditFactInput{Source: "aero-id", EventID: idgen.NewID(), ActorUID: owner,
		TenantID: tenantID, Action: "callback.delivery.replayed", ResourceType: "callback_delivery", ResourceID: id.String(),
		Payload: map[string]any{"action": delivery.Action}})); err != nil {
		return apperr.Internal("callback.delivery_replay_failed", "failed to replay callback delivery").Wrap(err)
	}
	return nil
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

func randomSecret() ([]byte, error) {
	value := make([]byte, 32)
	_, err := rand.Read(value)
	return value, err
}
