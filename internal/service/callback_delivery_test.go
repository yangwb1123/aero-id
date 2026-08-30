package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// callbackFakeRepo records deliveries and their outcomes.
type callbackFakeRepo struct {
	mu         sync.Mutex
	deliveries []*domain.CallbackDelivery
	marked     []markRecord
	resetCount int
	subs       []*domain.CallbackSubscription
	facts      []*domain.AuditFact // facts appended via the WithAudit methods
	failAudit  error               // non-nil => WithAudit methods fail with this
	failMark   error               // non-nil => MarkDelivery fails without settling (drain-swallow regression seam)
}

type markRecord struct {
	id         domain.ID
	status     string
	statusCode int
	errorCode  string
	next       time.Time
}

func (f *callbackFakeRepo) CreateSubscription(ctx context.Context, s *domain.CallbackSubscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, s)
	return nil
}

// WithAudit variants: record the fact (or fail) and delegate to the plain impl.
func (f *callbackFakeRepo) CreateSubscriptionWithAudit(ctx context.Context, s *domain.CallbackSubscription, fact *domain.AuditFact) error {
	f.mu.Lock()
	if f.failAudit != nil {
		err := f.failAudit
		f.mu.Unlock()
		return err
	}
	f.facts = append(f.facts, fact)
	f.mu.Unlock()
	return f.CreateSubscription(ctx, s)
}
func (f *callbackFakeRepo) DeleteSubscriptionWithAudit(ctx context.Context, tenant, owner string, id domain.ID, fact *domain.AuditFact) error {
	f.mu.Lock()
	if f.failAudit != nil {
		err := f.failAudit
		f.mu.Unlock()
		return err
	}
	f.facts = append(f.facts, fact)
	f.mu.Unlock()
	return f.DeleteSubscription(ctx, tenant, owner, id)
}
func (f *callbackFakeRepo) ResetDeliveryWithAudit(ctx context.Context, id domain.ID, fact *domain.AuditFact) error {
	f.mu.Lock()
	if f.failAudit != nil {
		err := f.failAudit
		f.mu.Unlock()
		return err
	}
	f.facts = append(f.facts, fact)
	f.resetCount++
	f.mu.Unlock()
	return nil
}
func (f *callbackFakeRepo) GetSubscription(ctx context.Context, id domain.ID) (*domain.CallbackSubscription, error) {
	return nil, repository.ErrNotFound
}
func (f *callbackFakeRepo) ListSubscriptions(ctx context.Context, tenant, owner string, page domain.Page) ([]*domain.CallbackSubscription, string, error) {
	return nil, "", nil
}
func (f *callbackFakeRepo) DeleteSubscription(ctx context.Context, tenant, owner string, id domain.ID) error {
	return nil
}
func (f *callbackFakeRepo) ListEnabledByAction(ctx context.Context, tenant, action string) ([]*domain.CallbackSubscription, error) {
	return nil, nil
}
func (f *callbackFakeRepo) EnqueueDelivery(ctx context.Context, d *domain.CallbackDelivery) error {
	return nil
}
func (f *callbackFakeRepo) FetchDueDeliveries(ctx context.Context, limit int, worker string, lease time.Duration) ([]*domain.CallbackDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.CallbackDelivery, 0, len(f.deliveries))
	for _, d := range f.deliveries {
		if d.NextAttemptAt.Before(time.Now().UTC()) {
			out = append(out, d)
		}
	}
	return out, nil
}
func (f *callbackFakeRepo) MarkDelivery(ctx context.Context, id domain.ID, worker string, generation int64, status string, statusCode int, response, errorCode string, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMark != nil {
		// A failed settlement records nothing: the row stays delivering+leased
		// and the attempt is NOT incremented (mirrors the real UPDATE that
		// errors before commit). The drain must observe mark_failed instead.
		return f.failMark
	}
	f.marked = append(f.marked, markRecord{id: id, status: status, statusCode: statusCode, errorCode: errorCode, next: next})
	return nil
}
func (f *callbackFakeRepo) ResetDelivery(ctx context.Context, id domain.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetCount++
	return nil
}

// RequeueDead, DeadLetterTenants and PurgeDeadLetterDeliveries are the
// operator recovery surface (B2-5); service tests never drive it — record and
// fail if invoked so a regression that calls it from the drain path surfaces
// immediately.
func (f *callbackFakeRepo) RequeueDead(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, errors.New("callbackFakeRepo.RequeueDead invoked in service test")
}
func (f *callbackFakeRepo) PurgeDeadLetterDeliveries(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("callbackFakeRepo.PurgeDeadLetterDeliveries invoked in service test")
}
func (f *callbackFakeRepo) CountDeadLetterDeliveries(context.Context, []domain.ID, int, string) (int, error) {
	return 0, errors.New("callbackFakeRepo.CountDeadLetterDeliveries invoked in service test")
}
func (f *callbackFakeRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, errors.New("callbackFakeRepo.DeadLetterTenants invoked in service test")
}
func (f *callbackFakeRepo) GetDelivery(ctx context.Context, id domain.ID) (*domain.CallbackDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deliveries {
		if d.ID == id {
			return d, nil
		}
	}
	return nil, repository.ErrNotFound
}

func newCallbackFakeRepo(deliveries ...*domain.CallbackDelivery) *callbackFakeRepo {
	repo := &callbackFakeRepo{}
	now := time.Now().UTC()
	for _, d := range deliveries {
		d.NextAttemptAt = now.Add(-time.Minute)
		repo.deliveries = append(repo.deliveries, d)
	}
	return repo
}

// callbackDelivery builds a delivery whose endpoint passes validation when
// AllowPrivateTarget is on (https, no userinfo) and whose secret ciphertext is
// derived from the same master key the test service uses.
func callbackDelivery(endpoint string, attempt int) *domain.CallbackDelivery {
	const masterKey = "test-master-key-0123456789abcdef"
	sum := sha256.Sum256([]byte(masterKey))
	svc := &callbackService{key: sum[:]}
	secret, err := svc.encrypt(sum[:])
	if err != nil {
		panic(err)
	}
	return &domain.CallbackDelivery{
		ID: idgen.NewID(), SubscriptionID: idgen.NewID(), EventID: idgen.NewID(),
		TenantID: "tenant-a", Action: "vault.object.created", Payload: map[string]any{"schema_version": 1},
		Status: "pending", Attempt: attempt, Endpoint: endpoint, SecretCiphertext: secret,
		OwnerUID: "u-1",
	}
}

// cannedTransport returns a fixed status for every request and records headers.
type cannedTransport struct {
	mu     sync.Mutex
	status int
	body   []byte
	calls  int
	header http.Header
}

func (t *cannedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.header = request.Header.Clone()
	_, _ = io.ReadAll(request.Body)
	return &http.Response{
		StatusCode: t.status, Status: http.StatusText(t.status),
		Header: http.Header{}, Body: io.NopCloser(bytesReader(t.body)), Request: request,
	}, nil
}

func (t *cannedTransport) snapshot() (int, http.Header) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls, t.header
}

func newCallbackTestService(repo repository.CallbackRepository, cfg CallbackConfig, transport http.RoundTripper) *callbackService {
	cfg.MasterKey = "test-master-key-0123456789abcdef"
	svc := NewCallbackService(repo, cfg).(*callbackService)
	svc.roundTripper = transport
	return svc
}

const callbackTestEndpoint = "https://cb.example.test/hook"

// TestCallbackDelivery_TransientFailureRetriesThenDeadLetters covers the
// release-gate scenario "callback outage": a 503 response keeps the delivery
// pending with a retry backoff instead of dropping it.
func TestCallbackDelivery_TransientFailureRetriesThenDeadLetters(t *testing.T) {
	transport := &cannedTransport{status: http.StatusServiceUnavailable}
	svc := newCallbackTestService(newCallbackFakeRepo(callbackDelivery(callbackTestEndpoint, 1)),
		CallbackConfig{RequestTimeout: 2 * time.Second, MaxResponseBytes: 4096, MaxAttempts: 3, RetryBase: time.Millisecond,
			AllowPrivateTarget: true}, transport)
	svc.drain(context.Background())

	repo := svc.repo.(*callbackFakeRepo)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.marked) != 1 {
		t.Fatalf("marked = %d, want 1", len(repo.marked))
	}
	mark := repo.marked[0]
	if mark.status != "pending" {
		t.Errorf("mark status = %s, want pending (attempt 1+1 < MaxAttempts 3)", mark.status)
	}
	if mark.errorCode != "callback.delivery_failed" {
		t.Errorf("error code = %s, want callback.delivery_failed", mark.errorCode)
	}
	if !mark.next.After(time.Now()) {
		t.Error("retry must be scheduled in the future (backoff)")
	}
}

// TestCallbackDelivery_DeadLettersAfterMaxAttempts: the last allowed attempt
// lands the delivery in the DLQ state instead of scheduling another retry.
func TestCallbackDelivery_DeadLettersAfterMaxAttempts(t *testing.T) {
	transport := &cannedTransport{status: http.StatusInternalServerError}
	svc := newCallbackTestService(newCallbackFakeRepo(callbackDelivery(callbackTestEndpoint, 3)),
		CallbackConfig{RequestTimeout: 2 * time.Second, MaxResponseBytes: 4096, MaxAttempts: 3, RetryBase: time.Millisecond,
			AllowPrivateTarget: true}, transport)
	svc.drain(context.Background())

	repo := svc.repo.(*callbackFakeRepo)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.marked) != 1 {
		t.Fatalf("marked = %d, want 1", len(repo.marked))
	}
	if repo.marked[0].status != "dead_letter" {
		t.Errorf("status = %s, want dead_letter after MaxAttempts", repo.marked[0].status)
	}
}

// TestCallbackDelivery_SignatureAndHeaders: the delivered request carries the
// HMAC signature, event id and attempt headers; a 2xx response marks success.
func TestCallbackDelivery_SignatureAndHeaders(t *testing.T) {
	transport := &cannedTransport{status: http.StatusAccepted}
	svc := newCallbackTestService(newCallbackFakeRepo(callbackDelivery(callbackTestEndpoint, 1)),
		CallbackConfig{RequestTimeout: 2 * time.Second, MaxResponseBytes: 4096, MaxAttempts: 5, RetryBase: time.Second,
			AllowPrivateTarget: true}, transport)
	svc.drain(context.Background())

	calls, header := transport.snapshot()
	if calls != 1 {
		t.Fatalf("endpoint calls = %d, want 1", calls)
	}
	if sig := header.Get("X-Aero-Callback-Signature"); sig == "" || sig[:7] != "sha256=" {
		t.Errorf("signature header = %q, want sha256=...", sig)
	}
	if got := header.Get("X-Aero-Callback-Attempt"); got != "2" {
		t.Errorf("attempt header = %q, want 2 (attempt+1)", got)
	}
	if header.Get("X-Aero-Callback-Timestamp") == "" || header.Get("X-Aero-Callback-Event-ID") == "" {
		t.Error("timestamp/event-id headers missing")
	}
	repo := svc.repo.(*callbackFakeRepo)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.marked) != 1 || repo.marked[0].status != "success" {
		t.Errorf("marks = %+v, want one success", repo.marked)
	}
}

func TestCallbackDelivery_RejectsOversizedResponse(t *testing.T) {
	transport := &cannedTransport{status: http.StatusAccepted, body: []byte("12345")}
	svc := newCallbackTestService(newCallbackFakeRepo(callbackDelivery(callbackTestEndpoint, 1)),
		CallbackConfig{RequestTimeout: 2 * time.Second, MaxResponseBytes: 4, MaxAttempts: 3, RetryBase: time.Millisecond,
			AllowPrivateTarget: true}, transport)
	svc.drain(context.Background())

	repo := svc.repo.(*callbackFakeRepo)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.marked) != 1 {
		t.Fatalf("marked = %d, want 1", len(repo.marked))
	}
	if repo.marked[0].status != "pending" || repo.marked[0].statusCode != http.StatusAccepted {
		t.Fatalf("mark = %+v, want pending with accepted status", repo.marked[0])
	}
}

// TestCallbackDelivery_ReplayResetsDelivery: manual replay resets the delivery
// so the worker picks it up again (P5-06 manual replay).
func TestCallbackDelivery_ReplayResetsDelivery(t *testing.T) {
	svc := newCallbackTestService(newCallbackFakeRepo(callbackDelivery(callbackTestEndpoint, 1)),
		CallbackConfig{RequestTimeout: 2 * time.Second, MaxResponseBytes: 4096, MaxAttempts: 5, RetryBase: time.Second,
			AllowPrivateTarget: true}, &cannedTransport{status: http.StatusAccepted})
	delivery := svc.repo.(*callbackFakeRepo).deliveries[0]
	ctx := WithIdentity(context.Background(), &domain.TokenIdentity{TenantID: "tenant-a", Subject: "u-1", UID: "u-1"})
	if err := svc.ReplayDelivery(ctx, delivery.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	repo := svc.repo.(*callbackFakeRepo)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.resetCount != 1 {
		t.Errorf("resets = %d, want 1", repo.resetCount)
	}
}

// TestCallbackDelivery_ConcurrentDrainRespectsBulkhead: with a concurrency
// limit of 2, at most 2 deliveries are in flight at once even under a slow
// endpoint (P6-06).
func TestCallbackDelivery_ConcurrentDrainRespectsBulkhead(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	slow := http.RoundTripper(&slowTransport{onStart: func() {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
	}, onDone: func() {
		mu.Lock()
		inFlight--
		mu.Unlock()
	}})
	repo := newCallbackFakeRepo(
		callbackDelivery(callbackTestEndpoint, 1),
		callbackDelivery(callbackTestEndpoint, 1),
		callbackDelivery(callbackTestEndpoint, 1),
		callbackDelivery(callbackTestEndpoint, 1),
	)
	svc := newCallbackTestService(repo,
		CallbackConfig{RequestTimeout: 5 * time.Second, MaxResponseBytes: 4096, MaxAttempts: 3, RetryBase: time.Millisecond,
			AllowPrivateTarget: true, WorkerConcurrency: 2}, slow)
	svc.drain(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 2 {
		t.Errorf("max in-flight deliveries = %d, want <= 2 (bulkhead)", maxInFlight)
	}
	if len(repo.marked) != 4 {
		t.Errorf("marked = %d, want 4", len(repo.marked))
	}
}

// slowTransport delays each request to widen the concurrency window.
type slowTransport struct {
	onStart func()
	onDone  func()
}

func (t *slowTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.onStart != nil {
		t.onStart()
	}
	defer func() {
		if t.onDone != nil {
			t.onDone()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	return &http.Response{StatusCode: http.StatusAccepted, Status: "202 Accepted",
		Header: http.Header{}, Body: io.NopCloser(bytesReader(nil)), Request: request}, nil
}

func bytesReader(b []byte) *bytesReaderT { return &bytesReaderT{data: b} }

// TestCallbackDelivery_MarkFailureEmitsMarkFailed (drain-swallow regression):
// when the settlement UPDATE fails, the drain must not report a false outcome
// (success/pending/dead_letter), must record nothing in the repo (no phantom
// settlement), and must leave the retry state untouched — the row stays
// delivering+leased and the existing lease re-fetch re-claims it with the
// attempt unchanged. Exactly one mark_failed observation is the whole
// observable contract (the service has no logger; the status-keyed counter is
// the mandated seam).
func TestCallbackDelivery_MarkFailureEmitsMarkFailed(t *testing.T) {
	markErr := errors.New("mark delivery: connection lost")
	delivery := callbackDelivery(callbackTestEndpoint, 1)
	repo := newCallbackFakeRepo(delivery)
	repo.failMark = markErr
	metrics := &metricSpy{}
	svc := newCallbackTestService(repo,
		CallbackConfig{RequestTimeout: 2 * time.Second, MaxResponseBytes: 4096, MaxAttempts: 3, RetryBase: time.Millisecond,
			AllowPrivateTarget: true}, &cannedTransport{status: http.StatusAccepted})
	svc.metrics = metrics
	attemptBefore, nextBefore := delivery.Attempt, delivery.NextAttemptAt

	svc.drain(context.Background())

	if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "mark_failed" {
		t.Fatalf("outcome observations = %v, want exactly [mark_failed] (no false settlement)", metrics.callbackOutcomes)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.marked) != 0 {
		t.Fatalf("settlements recorded = %+v, want none (failed MarkDelivery must not settle)", repo.marked)
	}
	if delivery.Attempt != attemptBefore || !delivery.NextAttemptAt.Equal(nextBefore) {
		t.Errorf("retry state mutated: attempt %d->%d, next_attempt_at %v->%v — failed settlement must leave the row untouched",
			attemptBefore, delivery.Attempt, nextBefore, delivery.NextAttemptAt)
	}
}

// TestCallbackDelivery_MarkSuccessObservesStatus pins the success path as
// byte-identical: a settled delivery observes its real outcome label and the
// repo records exactly one mark (the fix must not alter the happy path).
func TestCallbackDelivery_MarkSuccessObservesStatus(t *testing.T) {
	metrics := &metricSpy{}
	svc := newCallbackTestService(newCallbackFakeRepo(callbackDelivery(callbackTestEndpoint, 1)),
		CallbackConfig{RequestTimeout: 2 * time.Second, MaxResponseBytes: 4096, MaxAttempts: 5, RetryBase: time.Second,
			AllowPrivateTarget: true}, &cannedTransport{status: http.StatusAccepted})
	svc.metrics = metrics

	svc.drain(context.Background())

	if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "success" {
		t.Fatalf("outcome observations = %v, want exactly [success]", metrics.callbackOutcomes)
	}
	repo := svc.repo.(*callbackFakeRepo)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.marked) != 1 || repo.marked[0].status != "success" {
		t.Errorf("marks = %+v, want one success", repo.marked)
	}
}

type bytesReaderT struct {
	data []byte
	pos  int
}

func (r *bytesReaderT) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
