package migrate

import (
	"fmt"
	"gorm.io/gorm"
)

const MinCommandIntentVersion uint64 = 75

func VerifyCommandIntents(db *gorm.DB) error {
	if err := RequireVersion(db, MinCommandIntentVersion); err != nil {
		return err
	}
	var count int64
	if err := db.Raw(`SELECT count(*) FROM information_schema.columns WHERE table_schema='public'
		AND table_name='command_intents' AND ((is_nullable='NO'
		AND ((column_name IN ('id','account_id') AND data_type='uuid')
		OR (column_name IN ('tenant_id','actor_uid','client_id','idempotency_key') AND data_type='character varying' AND character_maximum_length=200)
		OR (column_name IN ('issuer','subject') AND data_type='text')
		OR (column_name='request_fingerprint' AND data_type='character varying' AND character_maximum_length=67)
		OR (column_name='version' AND data_type='bigint')
		OR (column_name IN ('status','receipt_kind') AND data_type='character varying' AND character_maximum_length=16)
		OR (column_name='document' AND data_type='jsonb')
		OR (column_name IN ('created_at','expires_at') AND data_type='timestamp with time zone')))
		OR (column_name='receipt_id' AND is_nullable='YES' AND data_type='uuid')
		OR (column_name='abandoned_at' AND is_nullable='YES' AND data_type='timestamp with time zone'))`).Scan(&count).Error; err != nil {
		return fmt.Errorf("check command intent schema: %w", err)
	}
	if count != 17 {
		return fmt.Errorf("command intent schema is incompatible; run 'migrate up' before starting")
	}
	if err := verifyCommandIntentUniqueness(db); err != nil {
		return err
	}
	if err := verifyCommandIntentDisposition(db); err != nil {
		return err
	}
	return verifyCallbackReplaySchema(db)
}

func verifyCommandIntentUniqueness(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT count(*) FROM pg_constraint WHERE conrelid='public.command_intents'::regclass
		AND contype='u' AND pg_get_constraintdef(oid)='UNIQUE (tenant_id, issuer, subject, client_id, idempotency_key)'`).Scan(&count).Error; err != nil {
		return fmt.Errorf("check command intent uniqueness: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("command intent uniqueness is incompatible; run 'migrate up' before starting")
	}
	return nil
}

func (m *Migrator) VerifyCommandIntents() error { return VerifyCommandIntents(m.db) }
