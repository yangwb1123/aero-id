I've reviewed the project — it's a Go service called **aero-id** with a comprehensive implementation plan covering 5 directions and 25 tasks. Here's a quick summary of what's ready:

| Direction | Tasks | Priority | Description |
|---|---|---|---|
| **四 — 传输协议加固** | TASK-015~021 | **P1** | gRPC keepalive, MaxConcurrentStreams, msg size limits, HTTP ReadHeaderTimeout, config exposure |
| **一 — 发布工程** | TASK-001~005 | P1 | Goreleaser, Docker CI, version tagging, CHANGELOG, SBOM |
| **三 — 降级契约** | TASK-010~014 | P1(文档)/P2(代码) | Degradation matrix doc, state manager, middleware, handler adaption |
| **二 — 可观测性成本** | TASK-006~009 | P2 | Log sampling, adaptive trace sampling, relay aggregation, metrics optimization |
| **五 — 数据生命周期** | TASK-022~025 | P2 | TTL cleanup for audit_events, notifications, outbox_events |

You've shared the **Code Implementer role prompt** — but I need to know **which specific task(s)** you'd like me to implement. 

Could you tell me:
1. **Which direction/task(s) should I start with?** (e.g., "方向四 TASK-015~021" as recommended by the plan)
2. **Any specific requirements or constraints** I should be aware of beyond what's in the plan?
