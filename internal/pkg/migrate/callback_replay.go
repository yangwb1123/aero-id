package migrate

import (
	"fmt"
	"gorm.io/gorm"
)

const callbackRevisionGuard = `
BEGIN
    IF OLD.revision = 9223372036854775807 THEN
        RAISE EXCEPTION 'callback delivery revision exhausted';
    END IF;
    NEW.revision := OLD.revision + 1;
    RETURN NEW;
END;
`

const callbackReceiptCheck = `CHECK ((((receipt_kind)::text <> 'callback_replay'::text) OR ((receipt_id = id) ` +
	`AND (COALESCE(((document -> 'plan'::text) ->> 'kind'::text), ''::text) = 'callback_replay'::text))))`
const callbackReceiptKinds = `CHECK (((receipt_kind)::text = ANY ((ARRAY[''::character varying, 'sync_job'::character varying, ` +
	`'operation'::character varying, 'callback_replay'::character varying])::text[])))`

func verifyCallbackReplaySchema(db *gorm.DB) error {
	var valid bool
	err := db.Raw(`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public'
  AND table_name='callback_deliveries' AND column_name='revision' AND data_type='bigint' AND is_nullable='NO')
  AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.callback_deliveries'::regclass
  AND contype='c' AND convalidated AND pg_get_constraintdef(oid)='CHECK ((revision > 0))')
  AND EXISTS (SELECT 1 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_language l ON l.oid=p.prolang
  WHERE t.tgrelid='public.callback_deliveries'::regclass AND t.tgname='callback_delivery_revision_guard'
  AND NOT t.tgisinternal AND t.tgenabled='O' AND t.tgtype=19 AND t.tgnargs=0 AND t.tgqual IS NULL
  AND p.pronamespace='public'::regnamespace AND p.proname='advance_callback_delivery_revision'
  AND p.pronargs=0 AND NOT p.prosecdef AND l.lanname='plpgsql' AND p.prosrc=?)
  AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.command_intents'::regclass
  AND conname='command_intents_callback_receipt_check' AND contype='c' AND convalidated AND pg_get_constraintdef(oid)=?)
  AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.command_intents'::regclass
  AND conname='command_intents_receipt_kind_check' AND contype='c' AND convalidated
  AND pg_get_constraintdef(oid)=?)`, callbackRevisionGuard, callbackReceiptCheck, callbackReceiptKinds).Scan(&valid).Error
	if err != nil {
		return fmt.Errorf("check callback replay schema: %w", err)
	}
	if !valid {
		return fmt.Errorf("callback replay schema is incompatible; run 'migrate up' before starting")
	}
	return nil
}
