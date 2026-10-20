DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM command_intents WHERE document->'plan'->>'kind' = 'callback_replay') THEN
        RAISE EXCEPTION 'cannot remove callback replay guards while callback intents exist';
    END IF;
END;
$$;
ALTER TABLE command_intents DROP CONSTRAINT command_intents_callback_receipt_check;
ALTER TABLE command_intents DROP CONSTRAINT command_intents_receipt_kind_check;
ALTER TABLE command_intents ADD CONSTRAINT command_intents_receipt_kind_check CHECK (receipt_kind IN ('', 'sync_job', 'operation'));
DROP TRIGGER callback_delivery_revision_guard ON callback_deliveries;
DROP FUNCTION advance_callback_delivery_revision();
ALTER TABLE callback_deliveries DROP COLUMN revision;
