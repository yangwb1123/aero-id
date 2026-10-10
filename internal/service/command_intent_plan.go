package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func (s *commandIntentService) prepareIntentCommand(ctx context.Context, intent *domain.CommandIntent,
	plan domain.CommandPlan) (domain.CommandPlan, *domain.Operation, *domain.SyncJob, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	plan.Kind = strings.ToLower(strings.TrimSpace(plan.Kind))
	if err := commandPlanBudget(plan); err != nil {
		return plan, nil, nil, err
	}
	if plan.Kind == "operation" {
		return s.prepareIntentOperation(ctx, intent, plan)
	}
	if plan.OperationType != "" || len(plan.Steps) > 0 || len(plan.RequestPayload) > 0 {
		return plan, nil, nil, apperr.BadRequest("command.invalid_plan", "sync commands cannot contain workflow parameters")
	}
	planner, ok := s.syncJobs.(syncCommandPlanner)
	if !ok {
		return plan, nil, nil, apperr.Unavailable("command.planner_unavailable", "command planner is unavailable")
	}
	job, err := planner.PrepareSyncJobCommand(ctx, SyncJobInput{AccountID: intent.AccountID, JobType: domain.SyncJobType(plan.Kind),
		Datasets: plan.Datasets, IdempotencyKey: commandIntentReceiptKey(intent.ID)})
	if err != nil {
		return plan, nil, nil, err
	}
	plan.Datasets = datasetsFromRequest(job.Request)
	sort.Slice(plan.Datasets, func(i, j int) bool { return plan.Datasets[i] < plan.Datasets[j] })
	job.Request["datasets"] = plan.Datasets
	if err := s.authorizeIntentPlan(ctx, intent, plan); err != nil {
		return plan, nil, nil, err
	}
	return plan, nil, job, nil
}

func (s *commandIntentService) prepareIntentOperation(ctx context.Context, intent *domain.CommandIntent,
	plan domain.CommandPlan) (domain.CommandPlan, *domain.Operation, *domain.SyncJob, error) {
	if err := validateIntentOperationScope(plan); err != nil {
		return plan, nil, nil, err
	}
	expectedDatasets := plan.Datasets
	planner, ok := s.operations.(operationCommandPlanner)
	if !ok {
		return plan, nil, nil, apperr.Unavailable("command.planner_unavailable", "command planner is unavailable")
	}
	input := operationInputFromCommandPlan(intent, plan)
	operation, err := planner.PrepareOperationCommand(ctx, input)
	if err != nil {
		return plan, nil, nil, err
	}
	plan.OperationType, plan.RequestPayload = operation.OperationType, operation.RequestPayload
	plan.Steps = make([]domain.CommandPlanStep, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		plan.Steps = append(plan.Steps, domain.CommandPlanStep{StepKey: step.StepKey, Source: step.Source, Action: step.Action,
			RequestPayload: fingerprintPayload(step.RequestPayload), CompensationAction: step.CompensationAction})
	}
	plan.Datasets = commandOperationDatasets(plan.Steps)
	if len(expectedDatasets) > 0 && !sameIntentDatasets(expectedDatasets, plan.Datasets) {
		return plan, nil, nil, apperr.BadRequest("command.invalid_scope", "workflow datasets differ from its steps")
	}
	if err := commandPlanBudget(plan); err != nil {
		return plan, nil, nil, err
	}
	if err := s.authorizeIntentPlan(ctx, intent, plan); err != nil {
		return plan, nil, nil, err
	}
	return plan, operation, nil, nil
}

func operationInputFromCommandPlan(intent *domain.CommandIntent, plan domain.CommandPlan) OperationInput {
	input := OperationInput{AccountID: intent.AccountID, OperationType: plan.OperationType,
		IdempotencyKey: commandIntentReceiptKey(intent.ID), RequestPayload: plan.RequestPayload}
	for _, step := range plan.Steps {
		input.Steps = append(input.Steps, OperationStepInput{StepKey: step.StepKey, Source: step.Source, Action: step.Action,
			RequestPayload: step.RequestPayload, CompensationAction: step.CompensationAction})
	}
	return input
}

func commandOperationDatasets(steps []domain.CommandPlanStep) []domain.AccountDataset {
	seen := make(map[domain.AccountDataset]bool)
	for _, step := range steps {
		values, explicit := operationDatasetValues(step.RequestPayload["datasets"])
		if explicit {
			for _, value := range values {
				seen[value] = true
			}
			continue
		}
		for _, descriptor := range domain.AccountDatasetRegistry() {
			if descriptor.Source == step.Source {
				seen[descriptor.Name] = true
			}
		}
	}
	values := make([]domain.AccountDataset, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func (s *commandIntentService) authorizeIntentPlan(ctx context.Context, intent *domain.CommandIntent, plan domain.CommandPlan) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if plan.Kind == "operation" {
		checker := &operationService{accounts: s.accountRepo, permission: s.permission}
		if err := checker.authorizeOperationCreate(ctx, intent.Principal.ActorUID, operationInputFromCommandPlan(intent, plan)); err != nil {
			return err
		}
	} else if err := authorizeAccountTarget(ctx, s.accountRepo, s.permission, intent.AccountID,
		intent.Principal.ActorUID, syncPermission(domain.SyncJobType(plan.Kind))); err != nil {
		return err
	}
	if plan.Kind == "export" && !s.config.ExportEnabled {
		return apperr.Unavailable("export.unavailable", "export storage is not configured")
	}
	return s.requireIntentSources(plan)
}

func (s *commandIntentService) requireIntentSources(plan domain.CommandPlan) error {
	if plan.Kind == "export" {
		return nil
	} // Explicit historical local projection export.
	for _, dataset := range plan.Datasets {
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok {
			return invalidSyncJobDatasetScope()
		}
		if descriptor.Source == "aero-id" {
			continue
		}
		flags := s.config.Sources[descriptor.Source]
		if plan.Kind == "erase" && !flags.EraseEnabled {
			return unavailableIntentSource()
		}
		if plan.Kind == "sync" && !flags.SummaryEnabled {
			return unavailableIntentSource()
		}
	}
	return s.requireIntentStepSources(plan.Steps)
}

func (s *commandIntentService) requireIntentStepSources(steps []domain.CommandPlanStep) error {
	for _, step := range steps {
		flags := s.config.Sources[step.Source]
		for _, action := range []string{step.Action, step.CompensationAction} {
			if action == "" {
				continue
			}
			if operationActionIsDestructive(action) {
				if !flags.EraseEnabled {
					return unavailableIntentSource()
				}
			} else if !flags.SummaryEnabled {
				return unavailableIntentSource()
			}
		}
	}
	return nil
}

func unavailableIntentSource() error {
	return apperr.Unavailable("command.source_unavailable", "command source capability is not enabled")
}

func commandPlanBudget(plan domain.CommandPlan) error {
	data, err := json.Marshal(plan)
	if err != nil || len(data) > 256*1024 || len(plan.Steps) > 32 || len(plan.Datasets) > len(domain.AccountDatasetRegistry()) {
		return apperr.BadRequest("command.invalid_plan", "command plan is invalid or exceeds its budget")
	}
	return nil
}

func commandIntentReceiptKey(id domain.ID) string { return "command-intent:" + id.String() }

func commandPlanDestructive(plan domain.CommandPlan) bool {
	if plan.Kind == "erase" {
		return true
	}
	for _, step := range plan.Steps {
		if operationActionIsDestructive(step.Action) || operationActionIsDestructive(step.CompensationAction) {
			return true
		}
	}
	return false
}
