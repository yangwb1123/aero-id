package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type connectorOperationRunner struct {
	accounts    repository.AccountRepository
	application repository.SummaryApplication
	clients     map[connector.Source]connector.Connector
}

func NewConnectorOperationRunner(accounts repository.AccountRepository, application repository.SummaryApplication,
	clients ...connector.Connector) OperationRunner {
	bySource := make(map[connector.Source]connector.Connector, len(clients))
	for _, client := range clients {
		if client != nil {
			bySource[client.Source()] = client
		}
	}
	return &connectorOperationRunner{accounts: accounts, application: application, clients: bySource}
}

func (r *connectorOperationRunner) Run(ctx context.Context, operation *domain.Operation, step *domain.OperationStep) (string, map[string]any, error) {
	return r.run(ctx, operation, step, "")
}

func (r *connectorOperationRunner) RunCompensation(ctx context.Context, operation *domain.Operation,
	step *domain.OperationStep) (string, map[string]any, error) {
	if step == nil || strings.TrimSpace(step.CompensationAction) == "" {
		return "", nil, fmt.Errorf("operation compensation action is required")
	}
	compensationStep := *step
	compensationStep.Action = step.CompensationAction
	compensationStep.SourceOperationID = step.CompensationSourceOperationID
	return r.run(ctx, operation, &compensationStep, ":compensation")
}

func (r *connectorOperationRunner) ReconcileCompensation(ctx context.Context, operation *domain.Operation,
	step *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error) {
	if step == nil {
		return domain.StepUnknown, "", nil, fmt.Errorf("operation step is required")
	}
	compensationStep := *step
	compensationStep.Action = step.CompensationAction
	compensationStep.SourceOperationID = step.CompensationSourceOperationID
	return r.reconcile(ctx, operation, &compensationStep,
		operationCompensationReconcileIdempotencyKey(operation, step))
}

func (r *connectorOperationRunner) run(ctx context.Context, operation *domain.Operation,
	step *domain.OperationStep, suffix string) (string, map[string]any, error) {
	if operation == nil || step == nil || operation.AccountID == (domain.ID{}) {
		return "", nil, fmt.Errorf("operation account is required")
	}
	action := strings.ToLower(step.Action)
	source := connector.Source(step.Source)
	datasets, err := operationDatasetsForStep(step, source)
	if err != nil {
		return "", nil, err
	}
	client, ok := r.clients[source]
	if !ok {
		return "", nil, fmt.Errorf("source connector is not configured")
	}
	account, err := r.accounts.GetByID(ctx, operation.TenantID, operation.AccountID)
	if err != nil {
		return "", nil, fmt.Errorf("load operation account: %w", err)
	}
	switch action {
	case "erase", "delete", "source.erase":
		return r.erase(ctx, operation, step, client, account, datasets, suffix)
	case "sync", "summary", "source.summary":
		return r.sync(ctx, operation, step, client, account, datasets)
	default:
		return "", nil, fmt.Errorf("unsupported source action")
	}
}

//nolint:cyclop // source status normalization is an explicit remote outcome matrix.
func (r *connectorOperationRunner) Reconcile(ctx context.Context, operation *domain.Operation,
	step *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error) {
	return r.reconcile(ctx, operation, step, operationReconcileIdempotencyKey(operation, step))
}

func operationDatasetsForStep(step *domain.OperationStep, source connector.Source) ([]domain.AccountDataset, error) {
	if step == nil || connector.Source(strings.TrimSpace(step.Source)) != source {
		return nil, invalidOperationDatasetScope()
	}
	action := strings.ToLower(strings.TrimSpace(step.Action))
	if operationActionIsDestructive(action) {
		return resolveOperationDatasetScope(step.RequestPayload, source, connector.DatasetScopeStored)
	}
	switch action {
	case "sync", "summary", "source.summary":
		if source == connector.SourceAeroIM || source == connector.SourceAeroVault {
			return resolveOperationDatasetScope(step.RequestPayload, source, connector.DatasetScopeStored)
		}
		return operationDatasets(step.RequestPayload, source)
	default:
		if source == connector.SourceAeroIM || source == connector.SourceAeroVault {
			return nil, invalidOperationDatasetScope()
		}
		return nil, nil
	}
}

func (r *connectorOperationRunner) reconcile(ctx context.Context, operation *domain.Operation,
	step *domain.OperationStep, idempotencyKey string) (domain.OperationStepStatus, string, map[string]any, error) {
	if operation == nil || step == nil || operation.AccountID == (domain.ID{}) {
		return domain.StepUnknown, "", nil, fmt.Errorf("operation account is required")
	}
	// Reconciliation must apply the same local validation as forward execution.
	if _, err := operationDatasetsForStep(step, connector.Source(step.Source)); err != nil {
		return domain.StepFailed, "", nil, err
	}
	client, ok := r.clients[connector.Source(step.Source)]
	if !ok {
		return domain.StepUnknown, "", nil, fmt.Errorf("source connector is not configured")
	}
	statusClient, ok := client.(connector.OperationStatusConnector)
	if !ok {
		return domain.StepUnknown, "", nil, nil
	}
	if client.Source() == connector.SourceSnaplink && strings.TrimSpace(step.SourceOperationID) == "" {
		return domain.StepUnknown, "", nil, &connector.RemoteError{Source: client.Source(), Kind: connector.ErrorUnknown,
			Code: "source.operation_id_missing", Temporary: true}
	}
	account, err := r.accounts.GetByID(ctx, operation.TenantID, operation.AccountID)
	if err != nil {
		return domain.StepUnknown, "", nil, fmt.Errorf("load operation account: %w", err)
	}
	return reconcileSourceStatus(ctx, operation, step, idempotencyKey, client, statusClient, account)
}

func reconcileSourceStatus(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	idempotencyKey string, client connector.Connector, statusClient connector.OperationStatusConnector,
	account *domain.Account) (domain.OperationStepStatus, string, map[string]any, error) {
	result, err := statusClient.OperationStatus(ctx, connector.OperationStatusRequest{
		AccountID: account.ID, CanonicalUID: account.CanonicalUID, ActorUID: operation.ActorUID,
		TenantID: operation.TenantID, RequestID: operation.RequestID, Region: account.HomeRegion, OperationID: operation.ID.String(),
		SourceOperationID: step.SourceOperationID, IdempotencyKey: idempotencyKey,
	})
	if result != nil {
		result.Result = connector.RedactSummaryData(result.Result)
	}
	if err != nil {
		if result == nil {
			return domain.StepUnknown, "", nil, err
		}
		return domain.StepUnknown, trustedSourceOperationID(client.Source(), operation, step, result), result.Result, err
	}
	if result == nil {
		return domain.StepUnknown, "", nil, &connector.RemoteError{Source: client.Source(), Kind: connector.ErrorUnknown,
			Code: "source.empty_operation_status", Temporary: true}
	}
	if client.Source() == connector.SourceSnaplink {
		if err := validateSnaplinkCommandResult(operation, step, result); err != nil {
			return domain.StepUnknown, trustedSourceOperationID(client.Source(), operation, step, result), result.Result, err
		}
	}
	return normalizedSourceStatus(client.Source(), operation, step, result)
}

func normalizedSourceStatus(source connector.Source, operation *domain.Operation, step *domain.OperationStep,
	result *connector.CommandResult) (domain.OperationStepStatus, string, map[string]any, error) {
	sourceOperationID := trustedSourceOperationID(source, operation, step, result)
	switch strings.ToLower(strings.TrimSpace(result.Status)) {
	case "succeeded", "success", "completed", "complete", "done":
		return domain.StepSucceeded, sourceOperationID, result.Result, nil
	case "failed", "failure", "rejected", "cancelled", "canceled":
		return domain.StepFailed, sourceOperationID, result.Result, nil
	default:
		return domain.StepUnknown, sourceOperationID, result.Result, nil
	}
}

func (r *connectorOperationRunner) erase(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	client connector.Connector, account *domain.Account, datasets []domain.AccountDataset, suffix string) (string, map[string]any, error) {
	command, ok := client.(connector.CommandConnector)
	if !ok {
		return "", nil, fmt.Errorf("source erase command is not configured")
	}
	result, err := command.Erase(ctx, connector.EraseRequest{AccountID: account.ID, CanonicalUID: account.CanonicalUID,
		ActorUID: operation.ActorUID, TenantID: operation.TenantID, RequestID: operation.RequestID, Region: account.HomeRegion, Datasets: datasets,
		IdempotencyKey: operationEraseIdempotencyKey(operation, step, suffix), OperationID: operation.ID.String()})
	if result == nil {
		if err != nil {
			return "", nil, err
		}
		return "", nil, &connector.RemoteError{Source: client.Source(), Kind: connector.ErrorUnknown,
			Code: "source.empty_command_result", Temporary: true}
	}
	result.Result = connector.RedactSummaryData(result.Result)
	sourceOperationID := trustedSourceOperationID(client.Source(), operation, step, result)
	outcome := commandResultOutcomePayload(result, sourceOperationID)
	if err == nil && client.Source() == connector.SourceSnaplink {
		if correlationErr := validateSnaplinkCommandResult(operation, step, result); correlationErr != nil {
			outcome["status"] = string(domain.StepUnknown)
			return sourceOperationID, outcome, correlationErr
		}
	}
	if err != nil {
		// A source may return a terminal-looking payload together with an error
		// (for example, a correlation mismatch). The durable outcome must expose
		// unknown rather than copying that untrusted status into a success-like
		// result. Preserve only a correlated identifier so a late or foreign
		// response cannot redirect reconciliation.
		outcome["status"] = string(domain.StepUnknown)
		return sourceOperationID, outcome, err
	}
	if err := commandResultOutcome(client.Source(), result); err != nil {
		return sourceOperationID, outcome, err
	}
	return sourceOperationID, outcome, nil
}

func operationEraseIdempotencyKey(operation *domain.Operation, step *domain.OperationStep, suffix string) string {
	if operation == nil || step == nil {
		return ""
	}
	return operation.ID.String() + ":" + step.ID.String() + suffix
}

func operationReconcileIdempotencyKey(operation *domain.Operation, step *domain.OperationStep) string {
	return operationEraseIdempotencyKey(operation, step, "")
}

func operationCompensationReconcileIdempotencyKey(operation *domain.Operation, step *domain.OperationStep) string {
	return operationEraseIdempotencyKey(operation, step, ":compensation")
}

// validateSnaplinkCommandResult compares the aero-id-owned correlation id by
// identity: a source may re-render the UUID aero-id sent in any form
// idgen.Parse accepts (upper case, urn:uuid:, 32-hex, braced) and that is the
// same operation, not a contradiction. The source-owned source_operation_id
// below stays exact-string compared.
func validateSnaplinkCommandResult(operation *domain.Operation, step *domain.OperationStep,
	result *connector.CommandResult) error {
	if operation == nil || result == nil || strings.TrimSpace(result.OperationID) == "" ||
		!idgen.CanonicalEqual(operation.ID, result.OperationID) {
		return &connector.RemoteError{Source: connector.SourceSnaplink, Kind: connector.ErrorUnknown,
			Code: "source.operation_id_mismatch", Temporary: true}
	}
	if strings.TrimSpace(result.SourceOperationID) == "" {
		return &connector.RemoteError{Source: connector.SourceSnaplink, Kind: connector.ErrorUnknown,
			Code: "source.source_operation_id_missing", Temporary: true}
	}
	if step != nil && step.SourceOperationID != "" && result.SourceOperationID != step.SourceOperationID {
		return &connector.RemoteError{Source: connector.SourceSnaplink, Kind: connector.ErrorUnknown,
			Code: "source.source_operation_id_mismatch", Temporary: true}
	}
	return nil
}

func commandResultOutcomePayload(result *connector.CommandResult, sourceOperationID string) map[string]any {
	outcome := map[string]any{"status": result.Status}
	if sourceOperationID != "" {
		outcome["source_operation_id"] = sourceOperationID
	}
	return outcome
}

func trustedSourceOperationID(source connector.Source, operation *domain.Operation, step *domain.OperationStep,
	result *connector.CommandResult) string {
	if result == nil {
		return ""
	}
	if source != connector.SourceSnaplink {
		return result.SourceOperationID
	}
	if operation == nil || !idgen.CanonicalEqual(operation.ID, result.OperationID) {
		if step != nil {
			return step.SourceOperationID
		}
		return ""
	}
	if step != nil && step.SourceOperationID != "" && result.SourceOperationID != step.SourceOperationID {
		return step.SourceOperationID
	}
	return result.SourceOperationID
}

func commandResultOutcome(source connector.Source, result *connector.CommandResult) error {
	status := normalizedCommandStatus(result.Status)
	if commandSucceeded(status) {
		return nil
	}
	if eraseFailureStatus(status) {
		return &connector.RemoteError{Source: source, Kind: connector.ErrorInvalid, Code: "source.operation_failed"}
	}
	return &connector.RemoteError{Source: source, Kind: connector.ErrorUnknown, Code: "source.operation_pending", Temporary: true}
}

//nolint:cyclop // projection persistence handles each allow-listed source dataset in one operation step.
func (r *connectorOperationRunner) sync(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	client connector.Connector, account *domain.Account, datasets []domain.AccountDataset) (string, map[string]any, error) {
	if client.Source() != connector.SourceAeroIM && client.Source() != connector.SourceAeroVault && len(datasets) == 0 {
		datasets = datasetsForSource(allDatasets(), string(client.Source()))
	}
	var (
		request connector.SummaryRequest
		err     error
	)
	if client.Source() == connector.SourceAeroIM || client.Source() == connector.SourceAeroVault {
		request, err = connector.NewTrustedSummaryRequest(ctx, r.accounts, connector.TrustedSummaryRequestInput{
			Source: client.Source(), TenantID: operation.TenantID, AccountID: operation.AccountID,
			ActorUID: operation.ActorUID, RequestID: operation.RequestID, OperationID: operation.ID.String(),
			Datasets: datasets,
		})
	} else {
		request, err = compatibilitySummaryRequest(client.Source(), account, operation.ActorUID, operation.TenantID,
			operation.RequestID, operation.ID.String(), datasets, false)
	}
	if err != nil {
		return "", nil, err
	}
	summary, err := client.Summary(ctx, request)
	if err != nil {
		return "", nil, err
	}
	if summary == nil {
		return "", nil, &connector.RemoteError{Source: client.Source(), Kind: connector.ErrorInvalid, Code: "source.empty_summary"}
	}
	if r.application == nil {
		return "", nil, repository.ErrSummaryApplicationUnavailable
	}
	accountID := request.AccountID
	if accountID == (domain.ID{}) && account != nil {
		accountID = account.ID
	}
	persistCtx, persistCancel := operationPersistenceContext(ctx)
	applicationResult, err := r.application.ApplySummary(persistCtx, domain.SummaryApplicationRequest{
		AccountID: accountID, Source: string(client.Source()), SourceRegion: summary.SourceRegion, Version: summary.Version,
		RequestedDatasets: append([]domain.AccountDataset(nil), datasets...), Coverage: summary.Coverage,
		Complete: summary.Complete, SkippedDatasets: append([]string(nil), summary.SkippedDatasets...),
		Snapshots: summary.Snapshots, Sources: summary.Sources, Memberships: summary.Memberships,
	})
	persistCancel()
	if err != nil {
		return "", nil, err
	}
	return "", syncOperationResult(summary, applicationResult), nil
}

func syncOperationResult(summary *connector.Summary, applicationResult domain.SummaryApplicationResult) map[string]any {
	status := "synced"
	result := map[string]any{"status": status, "count": len(summary.Snapshots), "complete": summaryIsComplete(summary),
		"application_outcome": string(applicationResult.Outcome)}
	if !summaryIsComplete(summary) {
		result["status"] = string(domain.OperationPartiallyCompleted)
		result["error_code"] = "source.summary_incomplete"
	}
	if len(summary.SkippedDatasets) > 0 {
		result["skipped_datasets"] = summary.SkippedDatasets
	}
	return result
}

func operationDatasets(payload map[string]any, source connector.Source) ([]domain.AccountDataset, error) {
	if source == connector.SourceAeroIM || source == connector.SourceAeroVault {
		return resolveOperationDatasetScope(payload, source, connector.DatasetScopeStored)
	}
	return legacyOperationDatasets(payload), nil
}

func legacyOperationDatasets(payload map[string]any) []domain.AccountDataset {
	raw := payload["datasets"]
	result := make([]domain.AccountDataset, 0)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				result = appendLegacyDataset(result, text)
			}
		}
	case []string:
		for _, text := range values {
			result = appendLegacyDataset(result, text)
		}
	}
	return result
}

func appendLegacyDataset(values []domain.AccountDataset, text string) []domain.AccountDataset {
	dataset := domain.AccountDataset(text)
	if _, valid := domain.DatasetDescriptorOf(dataset); valid {
		return append(values, dataset)
	}
	return values
}

func allDatasets() []domain.AccountDataset {
	values := domain.AccountDatasetRegistry()
	result := make([]domain.AccountDataset, len(values))
	for i, value := range values {
		result[i] = value.Name
	}
	return result
}
