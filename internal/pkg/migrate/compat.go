package migrate

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// MinOutboxRetryEligibilityVersion is the first schema version understood by
// the current relay. It is deliberately separate from the migration runner's
// implementation so deployment tooling and application startup share one gate.
const MinOutboxRetryEligibilityVersion uint64 = 69

// CurrentVersion reads the migration watermark without creating or changing
// schema_migrations. A missing table is an un-migrated database (version 0).
func CurrentVersion(db *gorm.DB) (uint64, error) {
	var exists bool
	if err := db.Raw(
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = ?)`, migrationsTable,
	).Scan(&exists).Error; err != nil {
		return 0, fmt.Errorf("check %s: %w", migrationsTable, err)
	}
	if !exists {
		return 0, nil
	}
	var version *uint64
	if err := db.Table(migrationsTable).Select("MAX(version)").Scan(&version).Error; err != nil {
		return 0, fmt.Errorf("read current version: %w", err)
	}
	if version == nil {
		return 0, nil
	}
	return *version, nil
}

// RequireVersion fails closed when the database watermark is below min. It is
// read-only: operators must run migrations explicitly rather than having an
// application process alter the schema while serving.
func RequireVersion(db *gorm.DB, min uint64) error {
	version, err := CurrentVersion(db)
	if err != nil {
		return fmt.Errorf("check schema version: %w", err)
	}
	if version < min {
		return errors.New(schemaVersionMessage(version, min))
	}
	return nil
}

func schemaVersionMessage(got, want uint64) string {
	return fmt.Sprintf("database schema at version %06d is below required version %06d — run 'migrate up' (make migrate-up) before starting", got, want)
}

// CurrentVersion returns the watermark using the migrator's database handle
// without creating schema_migrations.
func (m *Migrator) CurrentVersion() (uint64, error) { return CurrentVersion(m.db) }

// VerifyOutboxRetryEligibility checks both the migration watermark and the
// catalog shape used by the new relay. The watermark catches skipped or
// partially rolled-back deployments; the catalog check catches manually
// altered schemas or forged bookkeeping rows.
func VerifyOutboxRetryEligibility(db *gorm.DB) error {
	if err := RequireVersion(db, MinOutboxRetryEligibilityVersion); err != nil {
		return err
	}
	var shape struct {
		ColumnOK bool `gorm:"column:column_ok"`
		IndexOK  bool `gorm:"column:index_ok"`
	}
	if err := db.Raw(`
SELECT EXISTS (
	SELECT 1 FROM information_schema.columns
	WHERE table_schema = 'public' AND table_name = 'outbox_events'
	  AND column_name = 'next_attempt_at'
	  AND is_nullable = 'NO'
	  AND column_default IS NOT NULL
) AS column_ok,
EXISTS (
	SELECT 1 FROM pg_indexes
	WHERE schemaname = 'public' AND tablename = 'outbox_events'
	  AND indexname = 'idx_outbox_unpublished'
	  AND lower(indexdef) LIKE '%next_attempt_at%'
	  AND lower(indexdef) LIKE '%created_at%'
	  AND lower(indexdef) LIKE '%id%'
	  AND lower(indexdef) LIKE '%published_at is null%'
	  AND lower(indexdef) LIKE '%not dead_letter%'
) AS index_ok`).Scan(&shape).Error; err != nil {
		return fmt.Errorf("check outbox schema shape: %w", err)
	}
	if !shape.ColumnOK || !shape.IndexOK {
		return fmt.Errorf(
			"outbox schema is incompatible with relay retry eligibility; run 'migrate up' (column_ok=%t index_ok=%t)",
			shape.ColumnOK, shape.IndexOK,
		)
	}
	return nil
}

// VerifyOutboxRetryEligibility checks the schema attached to this migrator.
func (m *Migrator) VerifyOutboxRetryEligibility() error {
	return VerifyOutboxRetryEligibility(m.db)
}
