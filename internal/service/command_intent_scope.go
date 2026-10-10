package service

import (
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// New previews require explicit same-source scope even where the historical
// Operation API permits a default summary expansion. No hidden all-source plan.
func validateIntentOperationScope(plan domain.CommandPlan) error {
	if len(plan.Steps) == 0 {
		return apperr.BadRequest("command.invalid_scope", "workflow steps are required")
	}
	for _, step := range plan.Steps {
		values, explicit := operationDatasetValues(step.RequestPayload["datasets"])
		if !explicit || len(values) == 0 {
			return invalidOperationDatasetScope()
		}
		if _, err := parseOperationDatasetsForSource(step.RequestPayload, connector.Source(strings.TrimSpace(step.Source))); err != nil {
			return err
		}
	}
	return nil
}

func sameIntentDatasets(a, b []domain.AccountDataset) bool {
	values, err := parsePersistedDatasets(map[string]any{"datasets": a}, true)
	if err != nil || len(values) != len(b) {
		return false
	}
	seen := make(map[domain.AccountDataset]bool, len(values))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range b {
		if !seen[value] {
			return false
		}
	}
	return true
}
