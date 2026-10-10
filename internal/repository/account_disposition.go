package repository

import (
	"errors"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrAccountErased is a persistence disposition, translated by services. Source
// versions never override the locally authoritative account-data tombstone.
var ErrAccountErased = errors.New("account data is erased")

func lockAccountDispositionTx(tx *gorm.DB, accountID domain.ID) (*accountRow, error) {
	var row accountRow
	if err := tx.Model(&accountRow{}).Where("id = ?", accountID).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Take(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Every profile/projection writer takes the account lock before any child row.
// Erase takes the same lock, making check-and-write indivisible with disposition.
func lockWritableAccountTx(tx *gorm.DB, accountID domain.ID) error {
	row, err := lockAccountDispositionTx(tx, accountID)
	if err != nil {
		return err
	}
	if row.Status == string(domain.AccountErased) {
		return ErrAccountErased
	}
	return nil
}
