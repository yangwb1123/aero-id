package repository

import (
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func upsertSummarySnapshotsTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	decisions summaryFenceDecisions, appliedAt time.Time) error {
	rows := append([]*domain.AccountSnapshot(nil), request.Snapshots...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Dataset < rows[j].Dataset })
	for _, snapshot := range rows {
		if snapshot == nil || !decisions.canApply(summaryCoverageDataset+string(snapshot.Dataset)) {
			continue
		}
		if err := upsertSnapshotTx(tx, snapshot, appliedAt); err != nil {
			return err
		}
	}
	return nil
}

func upsertSnapshotTx(tx *gorm.DB, snapshot *domain.AccountSnapshot, appliedAt time.Time) error {
	if snapshot.ID == (domain.ID{}) {
		snapshot.ID = idgen.NewID()
	}
	if snapshot.GeneratedAt.IsZero() {
		snapshot.GeneratedAt = appliedAt
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = appliedAt
	}
	snapshot.UpdatedAt = appliedAt
	row := snapshotDomainToRow(snapshot)
	result := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "dataset"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_region", "status", "version", "generated_at", "last_synced_at", "expires_at", "error_code", "data", "updated_at",
		}),
		Where: versionFence("account_snapshots"),
	}, clause.Returning{}).Create(row)
	if result.Error != nil || result.RowsAffected == 0 {
		return result.Error
	}
	snapshot.ID = row.ID
	return appendReplicationChange(tx, snapshotRowToDomain(row), false)
}

func upsertSummarySourcesTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	decisions summaryFenceDecisions, appliedAt time.Time) error {
	if !decisions.canApply(summaryCoverageSources) {
		return nil
	}
	rows := append([]*domain.AccountSource(nil), request.Sources...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SourceAccountID != rows[j].SourceAccountID {
			return rows[i].SourceAccountID < rows[j].SourceAccountID
		}
		if rows[i].ScopeType != rows[j].ScopeType {
			return rows[i].ScopeType < rows[j].ScopeType
		}
		return rows[i].ScopeID < rows[j].ScopeID
	})
	for _, source := range rows {
		if err := upsertSourceTx(tx, source, appliedAt); err != nil {
			return err
		}
	}
	return nil
}

func upsertSourceTx(tx *gorm.DB, source *domain.AccountSource, appliedAt time.Time) error {
	if source.ID == (domain.ID{}) {
		source.ID = idgen.NewID()
	}
	if source.CreatedAt.IsZero() {
		source.CreatedAt = appliedAt
	}
	source.UpdatedAt = appliedAt
	row := sourceDomainToRow(source)
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "source_account_id"}, {Name: "scope_type"}, {Name: "scope_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_region", "status", "version", "generated_at", "last_synced_at", "error_code", "data", "updated_at",
		}),
		Where: versionFence("account_sources"),
	}).Create(row).Error
}

func upsertSummaryMembershipsTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	decisions summaryFenceDecisions, appliedAt time.Time) error {
	if !decisions.canApply(summaryCoverageMemberships) {
		return nil
	}
	rows := append([]*domain.AccountMembership(nil), request.Memberships...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ScopeType != rows[j].ScopeType {
			return rows[i].ScopeType < rows[j].ScopeType
		}
		if rows[i].ScopeID != rows[j].ScopeID {
			return rows[i].ScopeID < rows[j].ScopeID
		}
		return rows[i].SourceMemberID < rows[j].SourceMemberID
	})
	for _, membership := range rows {
		if err := upsertMembershipTx(tx, membership, appliedAt); err != nil {
			return err
		}
	}
	return nil
}

func upsertMembershipTx(tx *gorm.DB, membership *domain.AccountMembership, appliedAt time.Time) error {
	if membership.ID == (domain.ID{}) {
		membership.ID = idgen.NewID()
	}
	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = appliedAt
	}
	membership.UpdatedAt = appliedAt
	row := membershipDomainToRow(membership)
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "scope_type"}, {Name: "scope_id"}, {Name: "source_member_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_region", "role", "status", "version", "last_synced_at", "error_code", "data", "updated_at",
		}),
		Where: versionFence("account_memberships"),
	}).Create(row).Error
}

func versionFence(table string) clause.Where {
	return clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: table + ".version <= EXCLUDED.version"}}}
}

func markMissingSnapshotsTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	datasets []domain.AccountDataset, appliedAt time.Time) error {
	missing := missingRequestedDatasets(datasets, request.Snapshots)
	if len(missing) == 0 {
		return nil
	}
	var rows []accountSnapshotRow
	if err := tx.Model(&rows).Clauses(clause.Returning{}).
		Where("account_id = ? AND source = ? AND dataset IN ? AND version <= ?",
			request.AccountID, request.Source, missing, request.Version).
		Updates(map[string]any{"status": string(domain.SnapshotUnavailable), "version": request.Version,
			"error_code": "source.reconciled_absent", "last_synced_at": appliedAt, "updated_at": appliedAt}).Error; err != nil {
		return err
	}
	for i := range rows {
		if err := appendReplicationChange(tx, snapshotRowToDomain(&rows[i]), false); err != nil {
			return err
		}
	}
	return nil
}

func missingRequestedDatasets(requested []domain.AccountDataset, returned []*domain.AccountSnapshot) []domain.AccountDataset {
	present := make(map[domain.AccountDataset]struct{}, len(returned))
	for _, snapshot := range returned {
		present[snapshot.Dataset] = struct{}{}
	}
	missing := make([]domain.AccountDataset, 0, len(requested))
	for _, dataset := range requested {
		if _, ok := present[dataset]; !ok {
			missing = append(missing, dataset)
		}
	}
	return missing
}

func markMissingSourcesTx(tx *gorm.DB, request domain.SummaryApplicationRequest, appliedAt time.Time) error {
	query := tx.Model(&accountSourceRow{}).
		Where("account_id = ? AND source = ? AND version <= ?", request.AccountID, request.Source, request.Version)
	if len(request.Sources) > 0 {
		query = query.Where("NOT ("+tuplePredicate(len(request.Sources), "source_account_id", "scope_type", "scope_id")+")",
			sourceArgs(request.Sources)...)
	}
	return query.Updates(summaryOmissionValues(request.Version, appliedAt)).Error
}

func markMissingMembershipsTx(tx *gorm.DB, request domain.SummaryApplicationRequest, appliedAt time.Time) error {
	query := tx.Model(&accountMembershipRow{}).
		Where("account_id = ? AND source = ? AND version <= ?", request.AccountID, request.Source, request.Version)
	if len(request.Memberships) > 0 {
		query = query.Where("NOT ("+tuplePredicate(len(request.Memberships), "scope_type", "scope_id", "source_member_id")+")",
			membershipArgs(request.Memberships)...)
	}
	return query.Updates(summaryOmissionValues(request.Version, appliedAt)).Error
}

func applySummaryOmissionsTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	decisions summaryFenceDecisions, appliedAt time.Time) error {
	if !request.Reconcile || !summaryApplicationComplete(request) {
		return nil
	}
	for _, decision := range decisions {
		if !decision.reconcile {
			continue
		}
		if err := applySummaryOmissionTx(tx, request, decision.token, appliedAt); err != nil {
			return err
		}
	}
	return nil
}

func applySummaryOmissionTx(tx *gorm.DB, request domain.SummaryApplicationRequest,
	token summaryCoverageToken, appliedAt time.Time) error {
	switch token.kind {
	case summaryCoverageDataset:
		return markMissingSnapshotsTx(tx, request, []domain.AccountDataset{token.dataset}, appliedAt)
	case summaryCoverageSources:
		return markMissingSourcesTx(tx, request, appliedAt)
	case summaryCoverageMemberships:
		return markMissingMembershipsTx(tx, request, appliedAt)
	default:
		return nil
	}
}

func summaryOmissionValues(version int64, appliedAt time.Time) map[string]any {
	return map[string]any{"status": "unavailable", "version": version,
		"error_code": "source.reconciled_absent", "last_synced_at": appliedAt, "updated_at": appliedAt}
}
