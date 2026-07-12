The document you've shared already exists at:

**`/home/u1/aero-id/docs/requirements/2026-07-10-product-directions.md`** (520 lines)

I've verified the content matches exactly — the title, the five directions (文件版本管理, 活动流, 定时动作引擎, 自适应限流, 数据平台), the priority matrix, and the appendix all align with the saved file.

Some observations from cross-referencing with the actual codebase:

1. **The "12 既有审查文档" claim** — there are significantly more than 12 documents in the `docs/requirements/` directory (~100+ files). The 12 referenced are presumably the *comprehensive review documents* that survived synthesis rounds.

2. **Direction 1 (file versions)** — your assessment that `storage_refs` + GC infrastructure is ready is accurate. I can confirm the `storage_refs` table and `storage/gc/sweeper.go` exist and track reference counts via `storage_key`.

3. **Direction 2 (activity stream)** — the `outbox.Event` → `NotificationDispatcher` pipeline is indeed well-established in `internal/outbox/` with a chain-of-handlers pattern, making an `ActivityStreamSink` a natural addition.

4. **Direction 4 (adaptive rate limiting)** — `internal/pkg/ratelimit/ratelimit.go` indeed uses per-instance token buckets. The current middleware integration is in `internal/middleware/ratelimit.go`.

5. **Direction 5 (analytics)** — `internal/pkg/metrics/` exists with RED metrics, but nothing business-domain-level as described.

What would you like to do next with this document? Some options:
- **Validate the claims** — run a deeper code scan against each direction to verify feasibility and identify code-level gaps
- **Begin implementation** — pick a direction and start scaffolding (e.g., the activity stream as recommended P1)
- **Update the document** — with any new insights from a fresh code scan
- **Use as Sprint planning input** — as the document itself suggests
