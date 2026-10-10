-- Never discard authoritative previews/receipts during an automatic rollback.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM command_intents) THEN
        RAISE EXCEPTION 'cannot drop command_intents with live intent or execution receipts';
    END IF;
END $$;
DROP TABLE command_intents;
