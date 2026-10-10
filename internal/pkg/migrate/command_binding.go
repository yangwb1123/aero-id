package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

const MinCommandRequestBindingVersion uint64 = 71

// VerifyCommandRequestBinding fails before serving commands against a database
// without durable request binding. A watermark alone does not detect a manually
// removed column; both authoritative receipt tables must have the expected shape.
func VerifyCommandRequestBinding(db *gorm.DB) error {
	if err := RequireVersion(db, MinCommandRequestBindingVersion); err != nil {
		return err
	}
	var count int64
	if err := db.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name IN ('operations', 'sync_jobs')
		AND column_name = 'request_fingerprint' AND is_nullable = 'NO'
		AND data_type = 'character varying' AND character_maximum_length = 67
		AND column_default IS NOT NULL`).Scan(&count).Error; err != nil {
		return fmt.Errorf("check command request binding schema: %w", err)
	}
	if count != 2 {
		return fmt.Errorf("command request binding schema is incompatible; run 'migrate up' before starting")
	}
	return nil
}

func (m *Migrator) VerifyCommandRequestBinding() error {
	return VerifyCommandRequestBinding(m.db)
}
