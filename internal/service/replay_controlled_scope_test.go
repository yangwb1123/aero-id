package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type replayControlledOperations struct {
	origin      *domain.Operation
	created     OperationInput
	createCalls int
}

func (o *replayControlledOperations) Create(_ context.Context, input OperationInput) (*domain.Operation, error) {
	o.created = input
	o.createCalls++
	return &domain.Operation{ID: idgen.NewID(), AccountID: input.AccountID, TenantID: "tenant-a",
		Status: domain.OperationRequested}, nil
}

func (o *replayControlledOperations) Get(context.Context, domain.ID) (*domain.Operation, error) {
	return o.origin, nil
}

func (*replayControlledOperations) List(context.Context, domain.Page) ([]*domain.Operation, string, error) {
	return nil, "", nil
}

func (*replayControlledOperations) Run(context.Context, domain.ID, string) (*domain.Operation, error) {
	return nil, nil
}

func (*replayControlledOperations) Reconcile(context.Context, domain.ID, string) (*domain.Operation, error) {
	return nil, nil
}

type replayControlledAudit struct {
	last AuditFactInput
}

func (a *replayControlledAudit) Record(_ context.Context, input AuditFactInput) error {
	a.last = input
	return nil
}

func (*replayControlledAudit) Log(context.Context, *domain.AuditEvent) error { return nil }
func (*replayControlledAudit) List(context.Context, domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	return nil, "", nil
}
func (*replayControlledAudit) VerifyChain(context.Context, string, string) error { return nil }

func controlledReplayInput(accountID, operationID domain.ID) ReplayInput {
	return ReplayInput{Mode: "controlled", AccountID: accountID, OperationID: operationID,
		IdempotencyKey: "replay-key-1", Confirmed: true,
		Commands: []ReplayCommand{{Source: "aero-vault", Action: "summary", Payload: map[string]any{}}}}
}

func TestControlledReplayRejectsHistoricalOperationFromAnotherAccount(t *testing.T) {
	targetID, originID := idgen.NewID(), idgen.NewID()
	operations := &replayControlledOperations{origin: &domain.Operation{ID: originID, AccountID: idgen.NewID(),
		TenantID: "tenant-a"}}
	service := NewReplayService(&replayControlledAudit{}, operations)

	_, err := service.Create(replayCursorContext(), controlledReplayInput(targetID, originID))
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != "replay.scope_mismatch" {
		t.Fatalf("error = %v, want replay.scope_mismatch", err)
	}
	if operations.createCalls != 0 {
		t.Fatalf("Create calls = %d, want 0", operations.createCalls)
	}
}

func TestControlledReplayRecordsValidatedHistoricalOperationProvenance(t *testing.T) {
	accountID, originID := idgen.NewID(), idgen.NewID()
	operations := &replayControlledOperations{origin: &domain.Operation{ID: originID, AccountID: accountID,
		TenantID: "tenant-a"}}
	audit := &replayControlledAudit{}
	service := NewReplayService(audit, operations)

	_, err := service.Create(replayCursorContext(), controlledReplayInput(accountID, originID))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	want := originID.String()
	if operations.created.RequestPayload["historical_operation_id"] != want ||
		audit.last.Payload["historical_operation_id"] != want {
		t.Fatalf("operation payload = %#v, audit payload = %#v", operations.created.RequestPayload, audit.last.Payload)
	}
}
