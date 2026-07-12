Based on thorough analysis of the codebase against the architecture documents (`next-iteration-analysis.out.arch.md`) and implementation plan (`next-iteration-analysis.out.impl-plan.md`), here's a clear picture:

---

## Current Implementation Status

### ✅ Already Implemented (Phase 1 & 2 — Ban System)

| Task | Component | Status |
|------|-----------|--------|
| TASK-001 | `user_bans` migration (000024) + `domain/ban.go` | ✅ Done |
| TASK-002 | `repository/ban.go` — BanRepository CRUD + IsBanned | ✅ Done |
| TASK-003 | `service/ban.go` — BanService with audit logging | ✅ Done |
| TASK-004 | `middleware/ban.go` — BanCheck middleware (Auth→Ban→RateLimit) | ✅ Done |
| TASK-005 | HTTP `admin.go` + gRPC `admin.go` — ban endpoints + SSE Disconnect | ✅ Done |
| — | `hub.Hub.Disconnect(uid)` — SSE force-disconnect on ban | ✅ Done |
| TASK-006 | `data_export_jobs` migration (000025) + `domain/export.go` | ✅ Done |
| TASK-011 | Search with `ts_headline` snippets (already in `SearchHit.Snippet`) | ✅ Done |

The app composition root (`internal/app/new.go`) fully wires: `banRepo → banSvc → BanChecker` through router config, `AdminHandler` with `HubDisconnector`, and gRPC `AdminServer` with bans.

### ❌ Remaining to Implement

| Task | Component | Priority |
|------|-----------|----------|
| **TASK-007** | `repository/export.go` — ExportJobRepository (CRUD + atomic status machine) | **P0** |
| **TASK-008** | `service/export.go` + `export_worker.go` — ExportService + async worker | **P0** |
| **TASK-009** | `handler/http/export.go` + `handler/grpc/export.go` — endpoints + download | **P0** |
| **TASK-010** | `service/deletion.go` — Account deletion with cascade (14+ tables) | **P0** |
| TASK-012 | Middleware unit test expansion (more edge cases) | P1 |
| TASK-013 | GitHub issue templates / TODO.md | P1 |

---

What would you like me to implement next? The highest-value remaining items are **TASK-007 → TASK-008 → TASK-009** (data export repository, service with async worker, and HTTP/gRPC endpoints), which together form Phase 3 (data export) from the implementation plan.
