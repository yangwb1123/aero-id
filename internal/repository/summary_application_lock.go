package repository

import (
	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// lockSummaryRowsTx serializes summaries for an account before any projection
// row is inserted or updated. Row locks alone cannot protect a natural-key gap:
// an older summary could otherwise mark a missing key and a concurrent lower
// version could insert that key after the omission update. The account lock
// closes that gap and also gives every summary one deadlock-free lock order.
func lockSummaryRowsTx(tx *gorm.DB, accountID domain.ID, source string) error {
	if err := lockSummaryAccountTx(tx, accountID); err != nil {
		return err
	}
	if err := lockSummarySnapshotsTx(tx, accountID, source); err != nil {
		return err
	}
	if err := lockSummarySourcesTx(tx, accountID, source); err != nil {
		return err
	}
	return lockSummaryMembershipsTx(tx, accountID, source)
}

func lockSummaryAccountTx(tx *gorm.DB, accountID domain.ID) error {
	var row accountRow
	return tx.Model(&accountRow{}).Select("id").Where("id = ?", accountID).
		Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
}

func lockSummarySnapshotsTx(tx *gorm.DB, accountID domain.ID, source string) error {
	var rows []accountSnapshotRow
	return tx.Model(&accountSnapshotRow{}).Select("id").
		Where("account_id = ? AND source = ?", accountID, source).
		Order("dataset ASC").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error
}

func lockSummarySourcesTx(tx *gorm.DB, accountID domain.ID, source string) error {
	var rows []accountSourceRow
	return tx.Model(&accountSourceRow{}).Select("id").
		Where("account_id = ? AND source = ?", accountID, source).
		Order("source_account_id ASC, scope_type ASC, scope_id ASC").
		Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error
}

func lockSummaryMembershipsTx(tx *gorm.DB, accountID domain.ID, source string) error {
	var rows []accountMembershipRow
	return tx.Model(&accountMembershipRow{}).Select("id").
		Where("account_id = ? AND source = ?", accountID, source).
		Order("scope_type ASC, scope_id ASC, source_member_id ASC").
		Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error
}
