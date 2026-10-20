package service

import (
	"context"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"testing"
)

type callbackIntentMemory struct {
	*intentMemoryRepo
	captured int
	facts    []*domain.AuditFact
}

func (r *callbackIntentMemory) CaptureCallbackReplay(context.Context, *domain.CommandIntent) (domain.CommandPreconditions, error) {
	r.captured++
	return r.preconditions, nil
}
func (r *callbackIntentMemory) Execute(_ context.Context, input repository.CommandIntentExecution) (*domain.CommandIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.intent.Status == "abandoned" {
		return nil, repository.ErrCommandIntentAbandoned
	}
	if r.intent.Status != "executed" {
		if input.Operation != nil || input.Job != nil || input.RequestedFact == nil {
			panic("callback borrowed a workflow receipt")
		}
		r.executions++
		r.facts = append(r.facts, input.RequestedFact)
		r.intent.Status, r.intent.Version, r.intent.ReceiptKind, r.intent.ReceiptID = "executed", 2, "callback_replay", r.intent.ID
	}
	copy := *r.intent
	return &copy, nil
}

func newCallbackIntentFixture(t *testing.T) (*commandIntentService, *callbackIntentMemory, *intentAccountRepo, CommandIntentInput) {
	svc, base, accounts, input := newIntentFixture(t)
	repo := &callbackIntentMemory{intentMemoryRepo: base}
	svc.repo, svc.config.CallbackReplayEnabled = repo, true
	input.Plan = domain.CommandPlan{Kind: "callback_replay", Datasets: []domain.AccountDataset{}, Callback: &domain.CommandCallbackTarget{
		SubscriptionID: idgen.NewID(), DeliveryID: idgen.NewID(), EventID: idgen.NewID(), Revision: 7, LeaseGeneration: 4}}
	return svc, repo, accounts, input
}

func TestCallbackIntentUsesExistingPreviewReceiptRecoveryAndFreshAudit(t *testing.T) {
	svc, repo, _, input := newCallbackIntentFixture(t)
	ctx := fingerprintContext(t, "console")
	preview, err := svc.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Destructive || repo.executions != 0 || repo.captured != 1 || preview.Plan.Callback.Revision != 7 {
		t.Fatal("callback preview executed or lost scope")
	}
	receipt, err := svc.Execute(ctx, preview.ID, 1, preview.ConfirmationDigest)
	if err != nil || receipt.ReceiptID != preview.ID || receipt.ReceiptKind != "callback_replay" {
		t.Fatal("queue receipt", err)
	}
	retry, err := svc.Execute(ctx, preview.ID, 1, preview.ConfirmationDigest)
	if err != nil || retry.ReceiptID != receipt.ReceiptID || repo.executions != 1 {
		t.Fatal("callback confirmation duplicated", err)
	}
	fact := repo.facts[0]
	if fact.EventID != fact.ID || fact.EventID == input.Plan.Callback.DeliveryID || fact.Action != "callback.delivery.replayed" {
		t.Fatal("resource-wide replay dedup")
	}
	found, err := svc.FindByKey(ctx, input.IdempotencyKey)
	if err != nil || found.ReceiptID != receipt.ReceiptID {
		t.Fatal("original-key recovery", err)
	}
	input.Plan.Callback.Revision++
	if _, err := svc.Create(ctx, input); requireAppError(t, err).Code != "command.idempotency_conflict" {
		t.Fatal("different version reused key", err)
	}
}

func TestCallbackIntentRequiresCurrentGlobalGrantOwnerAndConfiguration(t *testing.T) {
	for _, change := range []string{"denied", "missing", "foreign-owner", "paused"} {
		t.Run(change, func(t *testing.T) {
			svc, repo, account, input := newCallbackIntentFixture(t)
			ctx := fingerprintContext(t, "console")
			intent, err := svc.Create(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "denied":
				svc.permission = &callbackReadPermission{check: func(context.Context, string, string) (bool, error) { return false, nil }}
			case "missing":
				svc.permission = nil
			case "foreign-owner":
				account.account.CanonicalUID = "other-owner"
			case "paused":
				svc.config.CallbackReplayEnabled = false
			}
			if _, err := svc.Execute(ctx, intent.ID, 1, intent.ConfirmationDigest); err == nil || repo.executions != 0 {
				t.Fatal("callback borrowed old permission", err)
			}
		})
	}
}

func TestCallbackIntentRejectsEmptyOrMixedScopeBeforeCapture(t *testing.T) {
	for _, change := range []string{"missing", "zero-event", "zero-revision", "negative-generation", "datasets", "workflow", "wrong-kind"} {
		t.Run(change, func(t *testing.T) {
			svc, repo, _, input := newCallbackIntentFixture(t)
			switch change {
			case "missing":
				input.Plan.Callback = nil
			case "zero-event":
				input.Plan.Callback.EventID = domain.ID{}
			case "zero-revision":
				input.Plan.Callback.Revision = 0
			case "negative-generation":
				input.Plan.Callback.LeaseGeneration = -1
			case "datasets":
				input.Plan.Datasets = []domain.AccountDataset{"aero-vault.usage"}
			case "workflow":
				input.Plan.OperationType = "sync"
			case "wrong-kind":
				input.Plan.Kind = "sync"
			}
			if _, err := svc.Create(fingerprintContext(t, "console"), input); err == nil || repo.captured != 0 {
				t.Fatal("invalid callback scope captured", err)
			}
		})
	}
}
