//go:build integration

package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestOutboxQueryPlansUseDueIndex is a machine-readable plan regression gate.
// The test disables sequential and bitmap scans only to make index usability
// visible on a throwaway database; it does not require the production planner
// to choose an index for a tiny table. The due index cannot provide global
// created_at order while next_attempt_at is a range predicate, so a bounded
// sort is valid and is intentionally not rejected.
func TestOutboxQueryPlansUseDueIndex(t *testing.T) {
	ctx := context.Background()
	db, _ := setupClaimRepo(t)
	if err := db.WithContext(ctx).Exec(`
INSERT INTO outbox_events
    (id, aggregate_type, aggregate_id, event_type, payload, created_at,
     next_attempt_at, dead_letter)
SELECT gen_random_uuid(), 'plan', gen_random_uuid()::text, 'plan.future', '{}'::jsonb,
       now() - (g || ' seconds')::interval, now() + interval '1 hour', false
FROM generate_series(1, 20000) AS g`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
INSERT INTO outbox_events
    (id, aggregate_type, aggregate_id, event_type, payload, created_at,
     next_attempt_at, dead_letter)
VALUES (gen_random_uuid(), 'plan', gen_random_uuid()::text, 'plan.due', '{}'::jsonb,
        now() - interval '2 days', now() - interval '1 second', false)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`ANALYZE outbox_events`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`SET enable_seqscan = off`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`SET enable_bitmapscan = off`).Error; err != nil {
		t.Fatal(err)
	}

	claimPlan := explainPlan(t, db, `
SELECT id, event_type, payload
FROM outbox_events
WHERE published_at IS NULL
  AND NOT dead_letter
  AND next_attempt_at <= now()
  AND (claimed_at IS NULL OR claimed_at < now() - interval '30 seconds')
ORDER BY created_at ASC, id ASC
LIMIT 100
FOR UPDATE SKIP LOCKED`)
	assertIndexPlan(t, "claim", claimPlan)
	if planContainsNode(claimPlan, "Seq Scan") {
		t.Fatalf("claim plan regressed to sequential scan: %s", claimPlan)
	}

	statsPlan := explainPlan(t, db, `
SELECT count(*), min(created_at)
FROM outbox_events
WHERE published_at IS NULL
  AND NOT dead_letter
  AND next_attempt_at <= now()
  AND (claimed_at IS NULL OR claimed_at < now() - interval '30 seconds')`)
	assertIndexPlan(t, "eligibility stats", statsPlan)
	if planContainsNode(statsPlan, "Seq Scan") {
		t.Fatalf("eligibility stats plan regressed to sequential scan: %s", statsPlan)
	}
}

func explainPlan(t *testing.T, db interface {
	Raw(string, ...interface{}) *gorm.DB
}, query string) []map[string]any {
	t.Helper()
	var raw string
	statement := fmt.Sprintf("EXPLAIN (FORMAT JSON, COSTS OFF) %s", query)
	if err := db.Raw(statement).Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	var plan []map[string]any
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("decode JSON plan: %v; raw=%q", err, raw)
	}
	return plan
}

func assertIndexPlan(t *testing.T, name string, plan []map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "idx_outbox_unpublished") {
		t.Fatalf("%s plan does not reference idx_outbox_unpublished: %s", name, encoded)
	}
}

func planContainsNode(plan []map[string]any, node string) bool {
	encoded, _ := json.Marshal(plan)
	return strings.Contains(string(encoded), `"Node Type":"`+node+`"`)
}
