package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type eraseOutcomeRecoveryFixture struct {
	repository.SyncJobRepository
	repository.SyncJobRecoveryRepository
	outcomes     []repository.EraseOutcome
	ensureCalls  int
	ensureInputs []repository.EraseOutcomeInput
	listCalls    int
	checkpoints  []repository.EraseOutcomeCheckpoint
}

func (f *eraseOutcomeRecoveryFixture) EnsureEraseOutcomes(_ context.Context, _ domain.ID, _ string,
	inputs []repository.EraseOutcomeInput) error {
	f.ensureCalls++
	f.ensureInputs = append([]repository.EraseOutcomeInput(nil), inputs...)
	return nil
}

func (f *eraseOutcomeRecoveryFixture) ListEraseOutcomes(context.Context, domain.ID) ([]repository.EraseOutcome, error) {
	f.listCalls++
	return append([]repository.EraseOutcome(nil), f.outcomes...), nil
}

func (f *eraseOutcomeRecoveryFixture) CheckpointEraseOutcome(_ context.Context, checkpoint repository.EraseOutcomeCheckpoint) error {
	f.checkpoints = append(f.checkpoints, checkpoint)
	for index := range f.outcomes {
		if f.outcomes[index].Source != checkpoint.Source {
			continue
		}
		f.outcomes[index].Status = checkpoint.Status
		f.outcomes[index].SourceOperationID = checkpoint.SourceOperationID
		f.outcomes[index].ErrorCode = checkpoint.ErrorCode
		f.outcomes[index].Result = checkpoint.Result
		return nil
	}
	return repository.ErrSyncOutcomeConflict
}

func TestDurableEraseSourcesImportsOnlyPendingLegacyAndKeepsDurableMetadata(t *testing.T) {
	recovery := &eraseOutcomeRecoveryFixture{outcomes: []repository.EraseOutcome{
		{Source: string(connector.SourceAeroIM), Status: "pending"},
		{Source: string(connector.SourceAeroVault), Status: "unknown", SourceOperationID: "durable-operation",
			ErrorCode: "durable.unknown", Result: map[string]any{"status": "completed", "source_operation_id": "stale-operation",
				"error_code": "stale.error", "detail": "durable detail"}},
	}}
	service := &syncJobService{repo: recovery}
	job := &domain.SyncJob{ID: domain.ID{1}, LeaseOwner: "worker-1", Result: map[string]any{"sources": map[string]any{
		string(connector.SourceAeroIM): map[string]any{"status": "not_configured", "source_operation_id": "legacy-operation",
			"error_code": "source.not_configured", "detail": "legacy detail", "password": "must redact"},
		string(connector.SourceAeroVault): map[string]any{"status": "completed", "source_operation_id": "stale-operation"},
	}}}
	required := map[connector.Source][]domain.AccountDataset{
		connector.SourceAeroIM:    {"aero-im.profile"},
		connector.SourceAeroVault: {"aero-vault.tenants"},
	}

	got, err := service.durableEraseSources(t.Context(), job, required)
	if err != nil {
		t.Fatalf("durableEraseSources: %v", err)
	}
	if recovery.ensureCalls != 1 || len(recovery.ensureInputs) != 2 || recovery.listCalls != 2 {
		t.Fatalf("ensure inputs/calls and list calls = %d/%d/%d, want 2/1/2", len(recovery.ensureInputs), recovery.ensureCalls, recovery.listCalls)
	}
	if len(recovery.checkpoints) != 1 {
		t.Fatalf("legacy checkpoints = %d, want only the pending aero-im row", len(recovery.checkpoints))
	}
	checkpoint := recovery.checkpoints[0]
	if checkpoint.Source != string(connector.SourceAeroIM) || checkpoint.Status != "failed" ||
		checkpoint.SourceOperationID != "legacy-operation" || checkpoint.ErrorCode != "source.not_configured" {
		t.Fatalf("legacy checkpoint = %+v, want converted pending outcome", checkpoint)
	}
	if _, ok := checkpoint.Result["password"]; ok {
		t.Fatal("legacy sensitive result field was not redacted")
	}

	sources := got
	im, ok := sources[string(connector.SourceAeroIM)].(map[string]any)
	if !ok || im["status"] != "failed" || im["detail"] != "legacy detail" || im["source_operation_id"] != "legacy-operation" {
		t.Fatalf("imported aero-im projection = %#v", sources[string(connector.SourceAeroIM)])
	}
	if _, ok := im["password"]; ok {
		t.Fatal("sensitive legacy value leaked into the durable summary")
	}
	vault, ok := sources[string(connector.SourceAeroVault)].(map[string]any)
	if !ok || vault["status"] != "unknown" || vault["source_operation_id"] != "durable-operation" ||
		vault["error_code"] != "durable.unknown" || vault["detail"] != "durable detail" {
		t.Fatalf("authoritative aero-vault projection = %#v", sources[string(connector.SourceAeroVault)])
	}
}

func TestDurableEraseSourcesIsUnavailableWithoutRecoveryRepository(t *testing.T) {
	service := &syncJobService{}
	got, err := service.durableEraseSources(t.Context(), &domain.SyncJob{}, nil)
	if err != nil || got != nil {
		t.Fatalf("durableEraseSources = %#v/%v, want nil/nil without the optional repository", got, err)
	}
}
