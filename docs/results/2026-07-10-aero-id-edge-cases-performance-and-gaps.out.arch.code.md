I've carefully reviewed the project context. Here's my understanding:

## Current State

1. **Modified working tree** — 40 files changed with 3571 insertions/620 deletions in a WIP state
2. **Latest commit**: `961e896 [pi-batch] Stage: technical-design`
3. **Implementation plan**: `docs/imp plan/2026-07-12-implementation-plan.md` — covering 5 engineering directions (传输协议加固, 发布工程, 降级契约, 可观测性成本, 数据生命周期)
4. **Previous analysis files**: Multiple `.arch.md` and `.impl-plan.md` files exist in `docs/results/`

However, you've only provided the **Code Implementer role prompt** without specifying **which task(s)** you want me to implement.

## What I Need

To begin implementation, please clarify which of the following you'd like me to work on:

1. **Direction 四 (传输协议加固)** — P1 priority, all 7 tasks (T015–T021): gRPC keepalive enforcement, MaxConcurrentStreams, message size limits, HTTP ReadHeaderTimeout, config exposure + tests
2. **Direction 一 (发布工程)** — P1 priority, 5 tasks (T001–T005): Goreleaser, Docker CI, version tagging, CHANGELOG automation, SBOM generation
3. **Direction 三 (降级契约)** — P1/P2, 5 tasks (T010–T014): Degradation matrix doc, state manager, middleware, handler adaptation, integration tests
4. **Direction 二 (可观测性成本)** — P2, 4 tasks (T006–T009): AccessLog sampling, adaptive trace sampling, relay log aggregation, metrics write amplification
5. **Direction 五 (数据生命周期)** — P2, 4 tasks (T022–T025): TTL cleaners for audit_events, notifications, outbox_events + lifecycle policy doc
6. **Continue from the existing WIP** (the working tree has changes already in progress)
7. **A specific subset** (e.g., just T015–T018, or just the config changes)

Please point me to the specific `.arch.md` and/or `.impl-plan.md` files I should use, or describe the concrete implementation task. Once I have clear requirements, I'll produce a full code implementation report.
