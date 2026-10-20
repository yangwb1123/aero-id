package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

// These are the actual PostgreSQL CHECK and function definitions, not only
// names/word matches: a forged watermark or weakened guard must fail closed.
const commandIntentStateCheck = `CHECK (((((status)::text = 'prepared'::text) AND (version = 1) AND (receipt_id IS NULL) ` +
	`AND ((receipt_kind)::text = ''::text) AND (abandoned_at IS NULL)) ` +
	`OR (((status)::text = 'executed'::text) AND (version = 2) AND (receipt_id IS NOT NULL) ` +
	`AND ((receipt_kind)::text <> ''::text) AND (abandoned_at IS NULL)) ` +
	`OR (((status)::text = 'abandoned'::text) AND (version = 2) AND (receipt_id IS NULL) ` +
	`AND ((receipt_kind)::text = ''::text) AND (abandoned_at IS NOT NULL) AND (abandoned_at >= created_at))))`
const commandIntentTerminalGuard = `
BEGIN
    IF OLD.status IN ('executed', 'abandoned') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal command intent is immutable';
    END IF;
    RETURN NEW;
END;
`

func verifyCommandIntentDisposition(db *gorm.DB) error {
	var valid bool
	err := db.Raw(`SELECT EXISTS (SELECT 1 FROM pg_constraint
		WHERE conrelid='public.command_intents'::regclass AND conname='command_intents_state_check'
		AND contype='c' AND convalidated AND pg_get_constraintdef(oid)=?)
		AND EXISTS (SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
		JOIN pg_language l ON l.oid=p.prolang
		WHERE t.tgrelid='public.command_intents'::regclass AND t.tgname='command_intents_terminal_guard'
		AND NOT t.tgisinternal AND t.tgenabled='O' AND t.tgtype=19 AND t.tgnargs=0 AND t.tgqual IS NULL
		AND p.pronamespace='public'::regnamespace AND p.proname='guard_terminal_command_intent'
		AND p.pronargs=0 AND NOT p.prosecdef AND l.lanname='plpgsql' AND p.prosrc=?)`,
		commandIntentStateCheck, commandIntentTerminalGuard).Scan(&valid).Error
	if err != nil {
		return fmt.Errorf("check command intent disposition: %w", err)
	}
	if !valid {
		return fmt.Errorf("command intent disposition schema is incompatible; run 'migrate up' before starting")
	}
	return nil
}
