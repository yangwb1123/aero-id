-- Withdrawal is an immutable local admission outcome, not cancellation of a
-- Job/Operation, TTL renewal, source erasure or retention/purge authorization.
ALTER TABLE command_intents
    ADD COLUMN abandoned_at timestamptz,
    DROP CONSTRAINT command_intents_status_check,
    DROP CONSTRAINT command_intents_check1,
    ADD CONSTRAINT command_intents_status_check CHECK (status IN ('prepared', 'executed', 'abandoned')),
    ADD CONSTRAINT command_intents_state_check CHECK (
        (status = 'prepared' AND version = 1 AND receipt_id IS NULL AND receipt_kind = '' AND abandoned_at IS NULL) OR
        (status = 'executed' AND version = 2 AND receipt_id IS NOT NULL AND receipt_kind <> '' AND abandoned_at IS NULL) OR
        (status = 'abandoned' AND version = 2 AND receipt_id IS NULL AND receipt_kind = '' AND abandoned_at IS NOT NULL AND abandoned_at >= created_at)
    );

-- Fence late/older application writers too: a terminal intent cannot be
-- turned back into a preparation or assigned a different receipt/disposition.
CREATE FUNCTION guard_terminal_command_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('executed', 'abandoned') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal command intent is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER command_intents_terminal_guard
    BEFORE UPDATE ON command_intents
    FOR EACH ROW EXECUTE FUNCTION guard_terminal_command_intent();
