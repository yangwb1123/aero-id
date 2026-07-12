现在我全面了解了代码库的状态。以下是完整的分析：

## 代码库状态评估

我已经全面审阅了代码库，包括所有现有的**已提交**和**未提交的更改**。以下是《docs/imp plan/2026-07-12-implementation-plan.md》中五个方向的状态：

| 方向 | 状态 | 说明 |
|------|------|------|
| **四 — 传输协议加固** (TASK-015~021) | ✅ **已完成** | gRPC keepalive enforcement, MaxConcurrentStreams, MaxRecvMsgSize, InitialWindowSize, HTTP ReadHeaderTimeout, 配置校验, server_test.go |
| **五 — 数据生命周期** (TASK-022~025) | ✅ **已完成** | audit_events cleaner, notifications cleaner, outbox dead letter cleanup, DataRetentionConfig, 均已接入 app.go |
| **二 — 可观测性成本** (TASK-006~009) | ✅ **大部分已完成** | AccessLog 采样 (sample.go + access_log.go 集成), Relay 日志聚合 (聚合 outbox drain 日志), 指标注册表已实现, 静态追踪采样已实现 |
| **三 — 降级契约** (TASK-010~014) | ⚠️ **基础设施已完成** | 降级矩阵文档 (DEGRADATION_MATRIX.md), DegradationState 管理器, DegradationHeader HTTP 中间件已实现, **但未接入 app.go, 未实现 gRPC degradation metadata, 未做 handler 适配** |
| **一 — 发布工程** (TASK-001~005) | ❌ **未开始** | 无 .goreleaser.yaml, 无 Docker CI workflow, 无 release workflow, 无 cliff.toml, 无 SBOM |

**此外**，`next-iteration-analysis.out.arch.md` 和 `next-iteration-analysis.out.impl-plan.md` 还识别了下一阶段的工作（用户封禁、数据导出、账户删除、搜索增强），这些工作在现有代码中尚未实现。

构建通过 ✅ (`go build ./...`)、所有单元测试通过 ✅ (`go test ./... -count=1`)。

---

不过我注意到您只发送了 Code Implementer 角色提示词模板，但没有附带具体的 `.arch.md` 和 `.impl-plan.md` 文件。请问您希望我实现哪个方向/哪个任务？例如：

1. **方向一 — 发布工程** (Goreleaser + Docker CI + Release workflow)
2. **方向二 — 自适应性追踪降采样** (TASK-007)
3. **方向三 — 降级契约 Handler 适配** (TASK-013)
4. **方向三 — gRPC degradation metadata** (TASK-012 gRPC 部分)
5. **next-iteration-analysis — 用户封禁系统** (新功能)

或者您有特定的 `.arch.md` / `.impl-plan.md` 文件要提供？
