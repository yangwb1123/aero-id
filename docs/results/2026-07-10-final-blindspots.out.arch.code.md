我已阅读了 `docs/imp plan/2026-07-12-implementation-plan.md`，包含了 5 个方向共 25 个任务的详细实现计划。

请问您希望我实现哪个具体方向或任务？例如：

1. **方向四：传输协议加固** (P1) — gRPC keepalive enforcement、MaxConcurrentStreams、消息大小限制、HTTP ReadHeaderTimeout 等（TASK-015~021）
2. **方向一：发布工程** (P1) — Goreleaser 配置、Docker CI、版本标签策略、CHANGELOG 自动化、SBOM（TASK-001~005）
3. **方向二：可观测性成本** (P2) — AccessLog 采样器、追踪自适应降采样、Relay 日志聚合、指标写放大优化（TASK-006~009）
4. **方向三：降级契约** (P1文档/P2代码) — 降级矩阵文档、状态管理器、中间件、Handler 适配（TASK-010~014）
5. **方向五：数据生命周期** (P2) — audit_events、notifications、outbox_events 清洁器（TASK-022~025）

或者您希望我实现全部/多个方向？请说明具体范围，我将按照 Code Implementer 角色严格执行实现。
