package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func commandIntentFromRow(row *commandIntentRow) (*domain.CommandIntent, error) {
	if len(row.Document) > 256*1024 {
		return nil, ErrCommandIntentConflict
	}
	var intent domain.CommandIntent
	decoder := json.NewDecoder(bytes.NewReader(row.Document))
	decoder.UseNumber()
	if err := decoder.Decode(&intent); err != nil {
		return nil, err
	}
	intent.ID, intent.AccountID, intent.Version, intent.Status = row.ID, row.AccountID, row.Version, row.Status
	intent.Principal = domain.CommandPrincipal{ActorUID: row.ActorUID, TenantID: row.TenantID, Issuer: row.Issuer,
		Subject: row.Subject, ClientID: row.ClientID}
	intent.IdempotencyKey, intent.RequestFingerprint = row.IdempotencyKey, row.RequestFingerprint
	intent.CreatedAt, intent.ExpiresAt, intent.ReceiptKind = row.CreatedAt, row.ExpiresAt, row.ReceiptKind
	intent.AbandonedAt = row.AbandonedAt
	if row.ReceiptID != nil {
		intent.ReceiptID = *row.ReceiptID
	}
	return &intent, nil
}

func (r *commandIntentRepo) Capture(ctx context.Context, tenant string, accountID domain.ID,
	datasets []domain.AccountDataset) (domain.CommandPreconditions, error) {
	var result domain.CommandPreconditions
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if len(datasets) == 0 {
			return ErrCommandIntentConflict
		}
		result, err = captureCommandPreconditionsTx(tx, tenant, accountID, datasets)
		return err
	})
	return result, err
}

// Promotion locks directory before account; projection writers lock account
// before children. Planning follows both orders and never reads projection Data.
func captureCommandPreconditionsTx(tx *gorm.DB, tenant string, accountID domain.ID,
	datasets []domain.AccountDataset) (domain.CommandPreconditions, error) {
	var directory regionDirectoryRow
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("tenant_id = ? AND account_id = ?", tenant, accountID).Take(&directory).Error; err != nil {
		return domain.CommandPreconditions{}, err
	}
	var epoch regionEpochRow
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("region = ?", directory.HomeRegion).Take(&epoch).Error; err != nil {
		return domain.CommandPreconditions{}, err
	}
	account, err := lockAccountDispositionTx(tx, accountID)
	if err != nil {
		return domain.CommandPreconditions{}, err
	}
	if account.TenantID != tenant {
		return domain.CommandPreconditions{}, ErrNotFound
	}
	if account.Status == string(domain.AccountErased) {
		return domain.CommandPreconditions{}, ErrAccountErased
	}
	if account.HomeRegion != directory.HomeRegion {
		return domain.CommandPreconditions{}, ErrCommandIntentStale
	}
	versions := []domain.CommandSnapshotVersion{}
	if datasets != nil {
		versions, err = captureCommandSnapshotsTx(tx, accountID, datasets)
	}
	return domain.CommandPreconditions{AccountStatus: domain.AccountStatus(account.Status), HomeRegion: directory.HomeRegion,
		WriterEpoch: epoch.Epoch, ResolverVersion: directory.ResolverVersion, Snapshots: versions}, err
}

func captureCommandSnapshotsTx(tx *gorm.DB, accountID domain.ID, datasets []domain.AccountDataset) ([]domain.CommandSnapshotVersion, error) {
	if len(datasets) == 0 || len(datasets) > len(domain.AccountDatasetRegistry()) {
		return nil, ErrCommandIntentConflict
	}
	for _, dataset := range datasets {
		if _, ok := domain.DatasetDescriptorOf(dataset); !ok {
			return nil, ErrCommandIntentConflict
		}
	}
	var rows []accountSnapshotRow
	err := tx.Select("id", "source", "source_region", "dataset", "status", "version", "generated_at", "last_synced_at", "expires_at").
		Where("account_id = ? AND dataset IN ?", accountID, datasets).Order("dataset, source, id").
		Limit(len(domain.AccountDatasetRegistry()) + 1).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > len(domain.AccountDatasetRegistry()) {
		return nil, fmt.Errorf("command metadata scope overflow")
	}
	values := make([]domain.CommandSnapshotVersion, 0, len(rows))
	for _, row := range rows {
		generated := row.GeneratedAt
		values = append(values, domain.CommandSnapshotVersion{ID: row.ID, Dataset: domain.AccountDataset(row.Dataset),
			Source: row.Source, SourceRegion: row.SourceRegion, Status: row.Status, Version: row.Version,
			GeneratedAt: &generated, LastSyncedAt: row.LastSyncedAt, ExpiresAt: row.ExpiresAt})
	}
	return values, nil
}

func sameCommandPreconditions(a, b domain.CommandPreconditions) bool {
	// JSON canonicalizes time location/monotonic components and nil pointers,
	// while typed decoding keeps int64 source versions exact.
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}
