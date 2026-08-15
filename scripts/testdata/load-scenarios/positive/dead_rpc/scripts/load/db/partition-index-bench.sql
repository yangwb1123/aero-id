-- Fixture bench (clean): exercises R3 extraction including the FOR UPDATE
-- SKIP LOCKED idiom and psql meta-command prose.
SELECT id FROM bench_items WHERE owner = 'x' ORDER BY id DESC LIMIT 21;
SELECT count(*) FROM bench_items;
INSERT INTO bench_items (id) VALUES ('a') ON CONFLICT DO NOTHING;
UPDATE bench_items SET owner = 'y' WHERE id = 'a';
SELECT id FROM bench_users
  FOR UPDATE SKIP LOCKED;
\echo '=== trap: FOR UPDATE SKIP LOCKED prose must be ignored ==='
