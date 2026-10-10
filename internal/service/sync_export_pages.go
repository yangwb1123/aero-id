package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// Inline V1 exports are intentionally bounded. Larger accounts need the
// separately planned chunked/resumable format, never an apparently complete
// first page. The byte limit remains the configured maxExport budget.
const maxInlineExportReferences = 10000

type exportBudget struct {
	bytes   int64
	limit   int64
	scanned int
}

func (b *exportBudget) add(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return apperr.Internal("export.serialize_failed", "failed to serialize export manifest")
	}
	b.bytes += int64(len(encoded)) + 1
	if b.bytes > b.limit {
		return exportTooLarge()
	}
	return nil
}

func exportTooLarge() error {
	return apperr.Conflict("export.too_large", "export exceeds the bounded inline artifact limit")
}

type exportReferencePage struct {
	items   []map[string]any
	next    string
	lastID  string
	scanned int
}

func readExportPages(budget *exportBudget, load func(domain.Page) (exportReferencePage, error)) ([]map[string]any, error) {
	result := make([]map[string]any, 0)
	cursor := ""
	for {
		page, err := load(domain.Page{Limit: domain.MaxPageLimit, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if err := validateExportPage(page, cursor); err != nil {
			return nil, err
		}
		budget.scanned += page.scanned
		if budget.scanned > maxInlineExportReferences {
			return nil, exportTooLarge()
		}
		for _, item := range page.items {
			if err := budget.add(item); err != nil {
				return nil, err
			}
			result = append(result, item)
		}
		if page.next == "" {
			return result, nil
		}
		cursor = page.next
	}
}

func validateExportPage(page exportReferencePage, cursor string) error {
	if page.scanned > domain.MaxPageLimit || len(page.items) > page.scanned {
		return apperr.Internal("export.incomplete", "export pagination exceeded its page bound")
	}
	if page.next == "" {
		return nil
	}
	if page.scanned == 0 || page.next != page.lastID ||
		(cursor != "" && strings.Compare(page.next, cursor) >= 0) {
		return apperr.Internal("export.incomplete", "export pagination did not make progress")
	}
	return nil
}

func (s *syncJobService) exportSourcePage(ctx context.Context, job *domain.SyncJob,
	datasets []domain.AccountDataset, page domain.Page) (exportReferencePage, error) {
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return exportReferencePage{}, err
	}
	var rows []*domain.AccountSource
	var next string
	var err error
	if scoped, ok := s.accountRepo.(repository.AccountExportRepository); ok {
		rows, next, err = scoped.ListExportSources(ctx, job.AccountID, datasets, page)
	} else {
		rows, next, err = s.accountRepo.ListSources(ctx, job.AccountID, page)
	}
	if err != nil {
		return exportReferencePage{}, apperr.Internal("export.sources_load_failed", "failed to load export sources").Wrap(err)
	}
	result := exportReferencePage{scanned: len(rows), next: next}
	selected := datasetsBySource(datasets)
	for _, row := range rows {
		if row == nil || row.AccountID != job.AccountID {
			return exportReferencePage{}, invalidExportScope()
		}
		result.lastID = row.ID.String()
		if len(selected[connector.Source(row.Source)]) > 0 {
			result.items = append(result.items, exportSources([]*domain.AccountSource{row})...)
		}
	}
	return result, nil
}

func (s *syncJobService) exportMembershipPage(ctx context.Context, job *domain.SyncJob,
	datasets []domain.AccountDataset, page domain.Page) (exportReferencePage, error) {
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return exportReferencePage{}, err
	}
	var rows []*domain.AccountMembership
	var next string
	var err error
	if scoped, ok := s.accountRepo.(repository.AccountExportRepository); ok {
		rows, next, err = scoped.ListExportMemberships(ctx, job.AccountID, datasets, page)
	} else {
		rows, next, err = s.accountRepo.ListMemberships(ctx, job.AccountID, page)
	}
	if err != nil {
		return exportReferencePage{}, apperr.Internal("export.memberships_load_failed", "failed to load export memberships").Wrap(err)
	}
	result := exportReferencePage{scanned: len(rows), next: next}
	selected := datasetsBySource(datasets)
	for _, row := range rows {
		if row == nil || row.AccountID != job.AccountID {
			return exportReferencePage{}, invalidExportScope()
		}
		result.lastID = row.ID.String()
		if len(selected[connector.Source(row.Source)]) > 0 {
			result.items = append(result.items, exportMemberships([]*domain.AccountMembership{row})...)
		}
	}
	return result, nil
}
