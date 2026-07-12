经过对代码库进行彻底审查，我发现实现计划（`docs/imp plan/2026-07-12-implementation-plan.md`）中描述的大部分任务实际上**已经实现了**。以下是五个方向的当前状态总结：

## 实现状态 - 五个方向

### ✅ 方向一：发布工程
- `.goreleaser.yaml` - ✅ 已完成
- `cliff.toml` - ✅ 已完成（git-cliff 配置）
- `.github/workflows/docker.yml` - ✅ 已完成
- `.github/workflows/release.yml` - ✅ 已完成

### ✅ 方向二：可观测性成本
- `internal/middleware/sample.go` - ✅ 概率采样实现
- `internal/middleware/access_log.go` - ✅ 与采样器集成
- `internal/pkg/tracing/tracing.go` - ✅ 自适应 DynamicSampler，含监控goroutine
- `internal/outbox/relay.go` - ✅ 聚合日志（单行汇总）
- `internal/pkg/metrics/metrics.go` - ✅ Outbox 指标已实现

### ✅ 方向三：降级契约
- `docs/DEGRADATION_MATRIX.md` - ✅ 已存在（8391 字节）
- `internal/middleware/degradation.go` - ✅ 完整实现：状态管理器、Prober、HTTP/gRPC 中间件、策略函数、DegradationAction
- `internal/middleware/degradation_test.go` - ✅ 已存在

### ✅ 方向四：传输协议加固
- `internal/config/config.go` - ✅ 所有 gRPC/HTTP 配置字段已存在
- `configs/config.yaml` - ✅ 所有默认值已配置
- `internal/app/new.go` - ✅ ReadHeaderTimeout + keepalive enforcement + MaxConcurrentStreams + MaxRecvMsgSize + InitialWindowSize + InitialConnWindowSize 全部已接线
- `internal/handler/grpc/server.go` - ✅ WithGRPCServerOptions、DegradationInterceptor 已实现

### ⚠️ 方向五：数据生命周期 — 代码已完成
- `internal/repository/audit.go` - ✅ PurgeOlderThan + RunAuditCleaner
- `internal/repository/notification.go` - ✅ PurgeReadOlderThan + RunNotificationCleaner
- `internal/outbox/repository.go` - ✅ PurgeDeadLetters + RunOutboxCleaner
- `internal/app/new.go` - ✅ 所有三个清洁器已接线
- `docs/DATA_RETENTION.md` - ✅ 已存在

### ❌ 唯一未实现项：TASK-013 — Handler 降级适配
`DegradationAction`、`DegradedResponse`、`DegradedStaleOK`/`DegradedFail503`/`DegradedProceed` 已在 `degradation.go` 中定义，但**没有任何 handler（HTTP 或 gRPC）实际调用它们**。这是实现计划中唯一未完成的代码任务。

---

请问您希望我实现哪个具体的任务？根据审查结果，主要的剩余工作是 **TASK-013：Handler 适配降级逻辑** — 让每个 HTTP 和 gRPC handler 根据当前降级状态和契约决定返回 503/200(stale)。如果您有其他新的需求文档、架构设计或实现计划，也请附上。
