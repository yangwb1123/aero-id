package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

type callbackSecretDeferralRepo struct {
	*callbackFakeRepo
	deferred     int
	errorCode    string
	deferErr     error
	liveContext  bool
	contextBound bool
}

func (r *callbackSecretDeferralRepo) DeferDelivery(ctx context.Context, id domain.ID, _ string,
	_ int64, code string, delay time.Duration) error {
	r.deferred++
	r.liveContext = ctx.Err() == nil
	_, r.contextBound = ctx.Deadline()
	if r.deferErr != nil {
		return r.deferErr
	}
	r.errorCode = code
	for _, d := range r.deliveries {
		if d.ID == id {
			d.Status = "pending"
			d.NextAttemptAt = time.Now().Add(delay)
		}
	}
	return nil
}

func TestCallbackDeliverySecretFailureDefersWithoutSpendingAttempt(t *testing.T) {
	for _, ciphertext := range []string{
		legacyCallbackCiphertext(t, oldCallbackMaster, []byte("secret")),
		"v1:missing-key:payload", "corrupt-secret-envelope",
	} {
		delivery := callbackDelivery(callbackTestEndpoint, 7)
		delivery.SecretCiphertext = ciphertext
		repo := &callbackSecretDeferralRepo{callbackFakeRepo: newCallbackFakeRepo(delivery)}
		transport := &cannedTransport{status: http.StatusAccepted}
		svc := newCallbackTestService(repo, CallbackConfig{MaxAttempts: 8, AllowPrivateTarget: true}, transport)
		metrics := &metricSpy{}
		svc.metrics = metrics
		svc.drain(t.Context())
		calls, _ := transport.snapshot()
		if calls != 0 || len(repo.marked) != 0 || repo.deferred != 1 {
			t.Fatalf("network/marks/deferrals = %d/%d/%d, want 0/0/1", calls, len(repo.marked), repo.deferred)
		}
		if delivery.Attempt != 7 || delivery.Status != "pending" || !delivery.NextAttemptAt.After(time.Now()) {
			t.Fatalf("local secret failure consumed budget or lost row: %+v", delivery)
		}
		if repo.errorCode != "callback.secret_unavailable" || len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "secret_unavailable" {
			t.Fatalf("deferral code/outcome = %q/%v", repo.errorCode, metrics.callbackOutcomes)
		}
	}
}

func TestCallbackSecretFailurePrecedesEndpointPolicy(t *testing.T) {
	delivery := callbackDelivery("http://127.0.0.1/blocked", 7)
	delivery.SecretCiphertext = "v1:missing-key:payload"
	repo := &callbackSecretDeferralRepo{callbackFakeRepo: newCallbackFakeRepo(delivery)}
	transport := &cannedTransport{status: http.StatusAccepted}
	svc := newCallbackTestService(repo, CallbackConfig{}, transport)
	svc.drain(t.Context())
	calls, _ := transport.snapshot()
	if repo.deferred != 1 || len(repo.marked) != 0 || calls != 0 || delivery.Attempt != 7 {
		t.Fatal("endpoint failure consumed the retry budget before the missing key was deferred")
	}
}

func TestCallbackSecretDeferralUsesDetachedBoundedContext(t *testing.T) {
	delivery := callbackDelivery(callbackTestEndpoint, 7)
	delivery.SecretCiphertext = "v1:missing:payload"
	repo := &callbackSecretDeferralRepo{callbackFakeRepo: newCallbackFakeRepo(delivery)}
	svc := newCallbackTestService(repo, CallbackConfig{AllowPrivateTarget: true}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	svc.markDelivery(ctx, delivery)
	if repo.deferred != 1 || !repo.liveContext || !repo.contextBound {
		t.Fatal("secret deferral did not get a live, bounded persistence context")
	}
}

func TestCallbackSecretDeferralFailureNeverFallsBackToMark(t *testing.T) {
	for _, wired := range []bool{false, true} {
		delivery := callbackDelivery(callbackTestEndpoint, 7)
		delivery.SecretCiphertext = "v1:missing:payload"
		base := newCallbackFakeRepo(delivery)
		var repo repository.CallbackRepository = base
		if wired {
			repo = &callbackSecretDeferralRepo{callbackFakeRepo: base, deferErr: repository.ErrCallbackDeliveryLeaseLost}
		}
		svc := newCallbackTestService(repo, CallbackConfig{AllowPrivateTarget: true}, nil)
		metrics := &metricSpy{}
		svc.metrics = metrics
		svc.markDelivery(t.Context(), delivery)
		if len(base.marked) != 0 || delivery.Attempt != 7 {
			t.Fatal("deferral failure fell back to a remote-attempt settlement")
		}
		if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "defer_failed" {
			t.Fatalf("deferral failure outcomes = %v", metrics.callbackOutcomes)
		}
	}
}

func TestCallbackPausedOrMissingKeyNeverClaims(t *testing.T) {
	for _, cfg := range []CallbackConfig{{}, {MasterKey: newCallbackMaster, DeliveryPaused: true}} {
		// A nil repo would panic if any claim is attempted.
		svc := NewCallbackService(nil, cfg).(*callbackService)
		metrics := &metricSpy{}
		svc.metrics = metrics
		svc.drain(t.Context())
		want := "key_unavailable"
		if cfg.DeliveryPaused {
			want = "paused"
		}
		if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != want {
			t.Fatalf("skipped drain outcomes = %v, want [%s]", metrics.callbackOutcomes, want)
		}
	}
}

func TestCallbackPausePreservesDeliveryRetentionAndAuditWorker(t *testing.T) {
	callbackRepo := &recordingPurgeCallbackRepo{}
	publicationRepo := &recordingPurgePublicationRepo{}
	svc := NewCallbackService(callbackRepo, CallbackConfig{MasterKey: newCallbackMaster,
		DeliveryPaused: true, DeadLetterRetention: time.Hour}).(*callbackService)
	svc.publications = publicationRepo
	svc.purgeDeadLetters(t.Context())
	if len(publicationRepo.purges) != 1 || len(callbackRepo.purges) != 0 {
		t.Fatal("callback pause either stopped audit retention or purged callback deliveries")
	}
	claims := &recordingClaimRepo{}
	svc.publications = claims
	svc.drainPublications(t.Context())
	if claims.count() != 1 {
		t.Fatal("callback pause stopped the independent audit publication drain")
	}
	_, _, err := svc.Register(replayIdentityContext(), CallbackInput{Endpoint: callbackTestEndpoint})
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != "callback.paused" || appError.HTTP != http.StatusServiceUnavailable {
		t.Fatalf("paused registration error = %v", err)
	}
}

func replayIdentityContext() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{UID: "u-1", TenantID: "tenant-a", Subject: "u-1"})
}
