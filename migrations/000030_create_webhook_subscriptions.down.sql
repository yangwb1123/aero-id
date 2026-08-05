-- 000030_create_webhook_subscriptions.down.sql
DROP INDEX IF EXISTS idx_webhook_deliveries_sub;
DROP INDEX IF EXISTS idx_webhook_subscriptions_enabled;
DROP INDEX IF EXISTS idx_webhook_subscriptions_owner;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_subscriptions;
