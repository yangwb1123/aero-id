-- Old binaries cannot interpret withdrawal proof. Never discard that proof
-- to roll back the schema, even when its preview TTL has expired.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM command_intents WHERE status = 'abandoned' OR abandoned_at IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove command intent disposition with abandoned intents';
    END IF;
END;
$$;
DROP TRIGGER command_intents_terminal_guard ON command_intents;
DROP FUNCTION guard_terminal_command_intent();
ALTER TABLE command_intents
    DROP CONSTRAINT command_intents_state_check,
    DROP CONSTRAINT command_intents_status_check,
    DROP COLUMN abandoned_at,
    ADD CONSTRAINT command_intents_status_check CHECK (status IN ('prepared', 'executed')),
    ADD CONSTRAINT command_intents_check1 CHECK (
        (status = 'prepared' AND receipt_id IS NULL AND receipt_kind = '') OR
        (status = 'executed' AND receipt_id IS NOT NULL AND receipt_kind <> '')
    );
