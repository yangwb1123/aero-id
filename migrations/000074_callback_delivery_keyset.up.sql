-- Operational delivery pages use descending UUID keysets, independently of
-- creation timestamps. The historical (subscription_id, created_at, id) index
-- cannot supply this order. These are performance indexes, not a new authority.
CREATE INDEX idx_callback_deliveries_subscription_keyset
    ON callback_deliveries (subscription_id, id DESC);
CREATE INDEX idx_callback_deliveries_subscription_status_keyset
    ON callback_deliveries (subscription_id, status, id DESC);
