package service

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

func TestCommandIntentAbandonmentClosesAdmissionWithoutSourceOrRenewedGenerationGrant(t *testing.T) {
	svc, repo, accounts, input := newIntentFixture(t)
	ctx := fingerprintContext(t, "console")
	intent, err := svc.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	repo.intent.ExpiresAt = time.Now().Add(-time.Second)
	accounts.account.Status = domain.AccountErased
	svc.config.Sources = nil
	svc.permission = capabilityPermissions(func(context.Context, string, string) (bool, error) {
		t.Fatal("withdrawal tried to renew generation permission")
		return false, nil
	})
	closed, err := svc.Abandon(ctx, intent.ID, 1, intent.ConfirmationDigest)
	if err != nil || closed.Status != "abandoned" || closed.Version != 2 || closed.AbandonedAt == nil || repo.executions != 0 {
		t.Fatalf("closure=%+v err=%v", closed, err)
	}
	retry, err := svc.Abandon(ctx, intent.ID, 1, intent.ConfirmationDigest)
	if err != nil || !retry.AbandonedAt.Equal(*closed.AbandonedAt) {
		t.Fatal("closure was replaced", err)
	}
	if _, err := svc.Execute(ctx, intent.ID, 1, intent.ConfirmationDigest); requireAppError(t, err).Code != "command.abandoned" {
		t.Fatal("closed preview executed")
	}
}

func TestCommandIntentAbandonmentPreservesAnAlreadyAdmittedReceipt(t *testing.T) {
	svc, repo, _, input := newIntentFixture(t)
	ctx := fingerprintContext(t, "console")
	intent, err := svc.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	executed, err := svc.Execute(ctx, intent.ID, 1, intent.ConfirmationDigest)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Abandon(ctx, intent.ID, 1, intent.ConfirmationDigest)
	if err != nil || result.Status != "executed" || result.ReceiptID != executed.ReceiptID || result.AbandonedAt != nil || repo.executions != 1 {
		t.Fatal("withdrawal claimed to cancel/replace an admitted receipt", err)
	}
}

func TestCommandIntentDispositionAndOriginalKeyLookupArePrincipalBound(t *testing.T) {
	svc, repo, _, input := newIntentFixture(t)
	ctx := fingerprintContext(t, "console")
	intent, err := svc.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	found, err := svc.FindByKey(ctx, input.IdempotencyKey)
	if err != nil || found.ID != intent.ID || repo.creates != 1 {
		t.Fatal("lookup created another intent", err)
	}
	for _, other := range []context.Context{WithActor(t.Context(), "actor-1"), fingerprintContext(t, "other-console")} {
		if _, err := svc.FindByKey(other, input.IdempotencyKey); err == nil {
			t.Fatal("foreign lookup")
		}
		if _, err := svc.Abandon(other, intent.ID, 1, intent.ConfirmationDigest); err == nil {
			t.Fatal("foreign withdrawal")
		}
	}
	for _, version := range []int64{0, 2} {
		if _, err := svc.Abandon(ctx, intent.ID, version, intent.ConfirmationDigest); err == nil {
			t.Fatal("wrong version closed intent")
		}
	}
	if _, err := svc.Abandon(ctx, intent.ID, 1, "wrong"); err == nil || repo.intent.Status != "prepared" {
		t.Fatal("wrong digest closed intent")
	}
	if _, err := svc.FindByKey(ctx, ""); err == nil {
		t.Fatal("implicit key lookup")
	}
	if _, err := svc.FindByKey(ctx, "missing"); requireAppError(t, err).Code != "command.not_found" || repo.creates != 1 {
		t.Fatal("missing lookup generated a new intent")
	}
}
