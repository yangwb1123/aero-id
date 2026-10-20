-- All writers, including legacy reset/worker paths, invalidate stale previews.
ALTER TABLE callback_deliveries ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);
CREATE FUNCTION advance_callback_delivery_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.revision = 9223372036854775807 THEN
        RAISE EXCEPTION 'callback delivery revision exhausted';
    END IF;
    NEW.revision := OLD.revision + 1;
    RETURN NEW;
END;
$$;
CREATE TRIGGER callback_delivery_revision_guard BEFORE UPDATE ON callback_deliveries
FOR EACH ROW EXECUTE FUNCTION advance_callback_delivery_revision();
ALTER TABLE command_intents DROP CONSTRAINT command_intents_receipt_kind_check;
ALTER TABLE command_intents ADD CONSTRAINT command_intents_receipt_kind_check
    CHECK (receipt_kind IN ('', 'sync_job', 'operation', 'callback_replay'));
-- The existing immutable terminal intent is the durable queue receipt; never
-- use the delivery UUID itself as the identity of a new replay execution.
ALTER TABLE command_intents ADD CONSTRAINT command_intents_callback_receipt_check
    CHECK (receipt_kind <> 'callback_replay' OR (receipt_id = id AND COALESCE(document->'plan'->>'kind', '') = 'callback_replay'));
