package service

import (
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

// OperationDatasetScope is a read projection, not an execution plan or grant.
// Only explicit registered same-source names leave the service; the rest of a
// stored request payload is never copied into this metadata.
type OperationDatasetScope struct {
	Status   string
	Datasets []string
}

// DescribeOperationStepScope must only be rendered after the normal Operation
// read/action authorization. It does not expand legacy summary defaults, infer
// a scope from the root request/results/audit, or modify durable execution input.
func DescribeOperationStepScope(step *domain.OperationStep) OperationDatasetScope {
	result := OperationDatasetScope{Status: "missing", Datasets: []string{}}
	if step == nil {
		return result
	}
	raw, present := step.RequestPayload["datasets"]
	if !present {
		return result
	}
	result.Status = "invalid"
	values, err := connector.ResolveDatasetScope(connector.Source(step.Source), raw, connector.DatasetScopeStored)
	if err != nil || len(values) == 0 {
		return result
	}
	result.Status, result.Datasets = "explicit", datasetNames(values)
	return result
}
