package service

import (
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

// redactSummaryRequest copies source-owned data before it enters either the
// replay table or the local projection. The connector boundary is defensive,
// but recovery must be safe even for an older or imported connector response.
func redactSummaryRequest(request domain.SummaryApplicationRequest) domain.SummaryApplicationRequest {
	request.Snapshots = redactSnapshots(request.Snapshots)
	request.Sources = redactSources(request.Sources)
	request.Memberships = redactMemberships(request.Memberships)
	return request
}

func redactSnapshots(values []*domain.AccountSnapshot) []*domain.AccountSnapshot {
	result := make([]*domain.AccountSnapshot, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		copy := *value
		copy.Data = connector.RedactSummaryData(value.Data)
		result = append(result, &copy)
	}
	return result
}

func redactSources(values []*domain.AccountSource) []*domain.AccountSource {
	result := make([]*domain.AccountSource, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		copy := *value
		copy.Data = connector.RedactSummaryData(value.Data)
		result = append(result, &copy)
	}
	return result
}

func redactMemberships(values []*domain.AccountMembership) []*domain.AccountMembership {
	result := make([]*domain.AccountMembership, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		copy := *value
		copy.Data = connector.RedactSummaryData(value.Data)
		result = append(result, &copy)
	}
	return result
}
