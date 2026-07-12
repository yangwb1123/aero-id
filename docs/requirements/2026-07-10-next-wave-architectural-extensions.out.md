This is a well-researched analysis. I've reviewed it against my knowledge of the codebase (from AGENTS.md and the evidence cited). A few observations:

**Accuracy check against the code:**
- Direction 1 (pub/sub) — correct. The Hub is purely local `sync.Map` per the code.
- Direction 2 (token revocation) — correct. `TokenCache` has no push invalidation.
- Direction 3 (groups) — correct. The message schema literally has `from_uid TEXT, to_uid TEXT` with no grouping concept.
- Direction 4 (job queue) — correct. Outbox relay is single-threaded poll, no cron/delay.
- Direction 5 (quotas) — correct. `maxBytes` is per-file only, no per-user aggregate.

**One nuance worth adding to Direction 2:** The `sso.Client.ValidateToken` already uses `singleflight` which means concurrent validation requests for the same token coalesce into one RPC. An eviction call during that RPC could race. The section mentions it ("document that revocation is best-effort") which is the right call.

**For Direction 4:** The existing `internal/outbox/transaction.go` pattern (`WithinTransaction` + outbox insert) already proves that enqueuing work atomically with a business transaction works. A job queue could reuse the same `EventBus` interface pattern.

**A missing cross-cutting concern not in any direction:** Test coverage for the horizontal-scaling scenario. There are no integration tests that simulate two instances sharing a DB. If Direction 1 (pub/sub) is implemented, an integration test with two `Hub` instances connected to the same bus would be valuable.

---

How would you like to proceed? Options:

1. **Save this document** properly to `docs/requirements/` (it appears to already be there)
2. **Start implementing** one of the directions — based on your suggested order (2 → 5 → 1 → 4 → 3)
3. **Deep-dive review** — I can read specific files the document references and verify every claim
4. **Discuss/triage** — prioritize against competing work
