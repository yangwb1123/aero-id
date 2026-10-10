package repository

import (
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Keep opaque linkage keys as minimal tombstone references; erase presentation,
// extension data and source-role copies. This does not mutate source authorities
// or erase immutable compliance evidence under its separate retention policy.
func eraseAccountReferencesTx(tx *gorm.DB, id domain.ID, now time.Time) error {
	updates := map[string]any{"status": "unavailable", "data": datatypes.JSONMap{},
		"error_code": "account.erased", "updated_at": now}
	if err := tx.Model(&accountSourceRow{}).Where("account_id = ?", id).Updates(updates).Error; err != nil {
		return err
	}
	updates["role"] = ""
	return tx.Model(&accountMembershipRow{}).Where("account_id = ?", id).Updates(updates).Error
}

func eraseAccountSnapshotsTx(tx *gorm.DB, id domain.ID, now time.Time) error {
	var rows []accountSnapshotRow
	if err := tx.Where("account_id = ?", id).Order("source ASC, dataset ASC").Find(&rows).Error; err != nil {
		return err
	}
	if err := tx.Model(&accountSnapshotRow{}).Where("account_id = ?", id).Updates(map[string]any{
		"status": string(domain.SnapshotUnavailable), "data": datatypes.JSONMap{},
		"expires_at": nil, "error_code": "account.erased", "updated_at": now,
	}).Error; err != nil {
		return err
	}
	for i := range rows {
		snapshot := snapshotRowToDomain(&rows[i])
		snapshot.Status, snapshot.Data = domain.SnapshotUnavailable, map[string]any{}
		snapshot.ErrorCode, snapshot.UpdatedAt, snapshot.ExpiresAt = "account.erased", now, nil
		if err := appendReplicationChange(tx, snapshot, false); err != nil {
			return err
		}
	}
	return nil
}
