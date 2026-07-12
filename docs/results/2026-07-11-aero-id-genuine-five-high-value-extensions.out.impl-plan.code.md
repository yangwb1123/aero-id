Based on my analysis, I can see:

1. A comprehensive **implementation plan** at `docs/imp plan/2026-07-12-implementation-plan.md` covering **5 directions** with **25 tasks**
2. **Many source files** have already been generated (untracked) in directories like `internal/audit/`, `internal/file/`, `internal/notification/`, `internal/middleware/`, etc.
3. The pipeline (`pi-batch`) has progressed through requirement-analysis → technical-design stages

However, I need your direction to proceed effectively.

**Could you please specify:**

1. **Which direction(s) or specific task(s)** from the implementation plan you want me to implement? Options:
   - **Direction 四** — Transport Protocol Hardening (TASK-015~021, P1, security)
   - **Direction 一** — Release Engineering (TASK-001~005, P1, CI/CD)
   - **Direction 三** — Degradation Contract (TASK-010~014, P1 doc / P2 code)
   - **Direction 二** — Observability Cost (TASK-006~009, P2)
   - **Direction 五** — Data Lifecycle (TASK-022~025, P2)

2. Or, do you want me to **verify and integrate the already-generated code** (the untracked files) to ensure it compiles and passes `make test && make lint`?

Please let me know your priorities so I can start implementing.
