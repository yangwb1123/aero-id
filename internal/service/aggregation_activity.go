package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// ListMeActivity pages the bounded, redacted local activity projection. It
// never turns pagination into a synchronous source call.
func (s *aggregationService) ListMeActivity(ctx context.Context, input AggregationInput,
	page domain.Page) (*domain.AccountActivityPage, error) {
	if len(input.Datasets) > 0 {
		if _, err := normalizeDatasets(input.Datasets); err != nil {
			return nil, err
		}
	}
	input.Datasets = []domain.AccountDataset{domain.DatasetAeroIMActivitySummary}
	overview, err := s.GetMeOverview(ctx, input)
	if err != nil {
		return nil, err
	}
	events, err := activityEvents(overview.Snapshots)
	if err != nil {
		return nil, err
	}
	page.Normalize()
	start, err := activityPageStart(events, page.Cursor)
	if err != nil {
		return nil, err
	}
	end := min(start+page.Limit, len(events))
	items := append([]map[string]any(nil), events[start:end]...)
	next := ""
	if end < len(events) && len(items) > 0 {
		next, err = activityEventID(items[len(items)-1])
		if err != nil {
			return nil, err
		}
	}
	return &domain.AccountActivityPage{Events: items, NextCursor: next, Partial: overview.Partial,
		GeneratedAt: overview.GeneratedAt, SourceErrors: overview.SourceErrors,
		StaleDatasets: overview.StaleDatasets}, nil
}

func activityEvents(snapshots []*domain.AccountSnapshot) ([]map[string]any, error) {
	for _, snapshot := range snapshots {
		if snapshot == nil || snapshot.Dataset != domain.DatasetAeroIMActivitySummary {
			continue
		}
		raw := snapshot.Data["events"]
		if raw == nil {
			raw = snapshot.Data["items"]
		}
		return normalizeActivityEvents(raw)
	}
	return []map[string]any{}, nil
}

func normalizeActivityEvents(raw any) ([]map[string]any, error) {
	if raw == nil {
		return []map[string]any{}, nil
	}
	if typed, ok := raw.([]map[string]any); ok {
		return typed, validateActivityEvents(typed)
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, invalidActivityProjection()
	}
	result := make([]map[string]any, len(values))
	for i, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, invalidActivityProjection()
		}
		result[i] = item
	}
	return result, validateActivityEvents(result)
}

func validateActivityEvents(events []map[string]any) error {
	for _, event := range events {
		if _, err := activityEventID(event); err != nil {
			return err
		}
	}
	return nil
}

func activityPageStart(events []map[string]any, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	for i, event := range events {
		id, err := activityEventID(event)
		if err != nil {
			return 0, err
		}
		if id == cursor {
			return i + 1, nil
		}
	}
	return 0, apperr.BadRequest("activity.invalid_cursor", "activity cursor is invalid")
}

func activityEventID(event map[string]any) (string, error) {
	id, ok := event["event_id"].(string)
	if !ok || id == "" {
		return "", invalidActivityProjection()
	}
	return id, nil
}

func invalidActivityProjection() error {
	return apperr.Unavailable("activity.projection_invalid", "activity projection is unavailable")
}
