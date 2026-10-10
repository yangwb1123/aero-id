package repository

import (
	"context"
	"fmt"
	"sort"

	"github.com/aero/aero-id/internal/domain"
)

// AccountExportRepository narrows reference scans to the submitted sources,
// rather than consuming another source's potentially unbounded memberships.
// Selection applies to snapshot datasets; core account/profile and references
// for the selected sources are explicitly included in an account export.
type AccountExportRepository interface {
	ListExportSources(context.Context, domain.ID, []domain.AccountDataset, domain.Page) ([]*domain.AccountSource, string, error)
	ListExportMemberships(context.Context, domain.ID, []domain.AccountDataset, domain.Page) ([]*domain.AccountMembership, string, error)
}

func (r *accountRepo) ListExportSources(ctx context.Context, id domain.ID,
	datasets []domain.AccountDataset, page domain.Page) ([]*domain.AccountSource, string, error) {
	sources, err := exportProjectionSources(datasets)
	if err != nil {
		return nil, "", err
	}
	return r.listSources(ctx, id, sources, page)
}

func (r *accountRepo) ListExportMemberships(ctx context.Context, id domain.ID,
	datasets []domain.AccountDataset, page domain.Page) ([]*domain.AccountMembership, string, error) {
	sources, err := exportProjectionSources(datasets)
	if err != nil {
		return nil, "", err
	}
	return r.listMemberships(ctx, id, sources, page)
}

func exportProjectionSources(datasets []domain.AccountDataset) ([]string, error) {
	if len(datasets) == 0 {
		return nil, fmt.Errorf("export dataset scope is required")
	}
	seen := make(map[string]bool)
	for _, dataset := range datasets {
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok {
			return nil, fmt.Errorf("export dataset scope is invalid")
		}
		seen[descriptor.Source] = true
	}
	sources := make([]string, 0, len(seen))
	for source := range seen {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return sources, nil
}

var _ AccountExportRepository = (*accountRepo)(nil)
