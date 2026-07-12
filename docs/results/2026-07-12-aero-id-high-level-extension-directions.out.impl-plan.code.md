所有测试都已通过，编译没问题。经过对整个代码库的彻底检查，我现在可以提供一个全面的实施报告。

---

# 代码实施报告

## 实施概述

我对当前代码库与 `docs/imp plan/2026-07-12-implementation-plan.md` 中的实施计划进行了全面对比。**该计划中所有 25 个任务已经全部实现。** 以下是逐项评估。

## 现状——任务完成矩阵

### 方向一：发布工程 (P1) — 全部完成 ✅

| 任务 | 状态 | 证据 |
|------|--------|---------|
| **TASK-001** — Goreleaser 配置 | ✅ | `.goreleaser.yaml` 存在且包含完整的构建配置（`builds`、`dockers`、`archives`、`sboms`、`signs`、`changelog`、`release`） |
| **TASK-002** — Docker CI 任务 | ✅ | `.github/workflows/docker.yml` 存在，使用 `docker/build-push-action@v6`，支持基于分支/标签的构建、PR 跳过推送、`ghcr.io` 推送、GHA 缓存 |
| **TASK-003** — 版本标签策略与稳定发布 | ✅ | `.github/workflows/release.yml` 存在，在标签 `v*` 上触发，运行 GoReleaser，构建镜像，创建 GitHub Release |
| **TASK-004** — CHANGELOG 自动化 | ✅ | `cliff.toml` 存在并配置了传统的提交解析器、按类型分组。`release.yml` 在 GoReleaser 之前运行 `git-cliff --unreleased --tag` |
| **TASK-005** — SBOM 生成与签名 | ✅ | `.goreleaser.yaml` 包含带有 `syft` 和 CycloneDX 格式的 `sboms` 部分，以及带有 `cosign sign-blob` 的 `signs` 部分 |

### 方向二：可观测性成本 (P2) — 全部完成 ✅

| 任务 | 状态 | 证据 |
|------|--------|---------|
| **TASK-006** — AccessLog 采样器 | ✅ | `internal/middleware/sample.go`：实现了基于比率的采样，健康检查路径始终记录，状态码 >=400 时跳过采样。`internal/middleware/access_log.go`：检查 `sampled` 上下文标志 |
| **TASK-007** — 追踪自适应降采样 | ✅ | `internal/pkg/tracing/tracing.go`：完整的 `DynamicSampler` 实现，基于导出错误率调整比率（10s 窗口，10% 阈值触发降级，2% 恢复阈值，最小比率 0.10，恢复周期 2 分钟） |
| **TASK-008** — Relay 日志聚合 | ✅ | `internal/outbox/relay.go`：`drain()` 方法在 info 级别输出一行聚合摘要（handled=X, failed=Y, pending=Z, oldest_age=..., duration=...）。各个处理器的错误仍在 warn 级别记录 |
| **TASK-009** — 指标写放大优化 | ✅ | `internal/pkg/metrics/metrics.go`：`outboxPending` 和 `outboxLagSecs` 使用 `atomic.Uint64` 实现无锁读取。`outboxMu` 保护特定于 outbox 的计数器，防止阻塞 HTTP/gRPC 指标 |

### 方向三：降级契约 (P1/P2) — 全部完成 ✅

| 任务 | 状态 | 证据 |
|------|--------|---------|
| **TASK-010** — 降级矩阵文档 | ✅ | `docs/DEGRADATION_MATRIX.md` 存在 |
| **TASK-011** — 降级状态管理器 | ✅ | `internal/middleware/degradation.go`：完整的 `DegradationState` 实现，`SetProber`、`StartProbing`（基于成功/失败阈值）、`NewDegradationStateFromConfig`、`DegradationConfig` |
| **TASK-012** — 降级中间件（HTTP + gRPC） | ✅ | HTTP：`DegradationHeader` 中间件写入 `X-Aero-Degraded` 头。gRPC：`DegradationInterceptor` 在尾部元数据中设置 `x-aero-degraded`，基础设施方法始终返回 `"none"` |
| **TASK-013** — Handler 适配降级逻辑 | ✅ | `DegradationAction`、`AbortIfDegraded`、`DepsForOp`、`DegradedResponse` 策略枚举、`DegradedStaleOK`/`DegradedFail503`/`DegradedProceed` 辅助函数 |
| **TASK-014** — 降级集成测试 | ✅ | `internal/middleware/degradation_test.go` 存在。`internal/handler/grpc/server_test.go` 包含广泛的降级拦截器单元测试和集成测试 |

### 方向四：传输协议加固 (P1) — 全部完成 ✅

| 任务 | 状态 | 证据 |
|------|--------|---------|
| **TASK-015** — gRPC keepalive 强制执行策略 | ✅ | `GRPCConfig.KeepaliveMinPingInterval` 在 `config.go` 中。`new.go` 实现为 `keepalive.EnforcementPolicy{MinTime: ..., PermitWithoutStream: false}` |
| **TASK-016** — gRPC MaxConcurrentStreams | ✅ | `GRPCConfig.MaxConcurrentStreams` 在 `config.go` 中。`new.go` 实现为 `grpc.MaxConcurrentStreams(cfg.Server.GRPC.MaxConcurrentStreams)` |
| **TASK-017** — gRPC 消息大小限制 | ✅ | `GRPCConfig.MaxRecvMsgSize` 和 `MaxSendMsgSize` 在 `config.go` 中。`new.go` 实现 |
| **TASK-018** — HTTP ReadHeaderTimeout | ✅ | `HTTPConfig.ReadHeaderTimeout` 在 `config.go` 中。`config.yaml` 中设置为 `10s`。`new.go` 实现到 `http.Server` 中 |
| **TASK-019** — gRPC 初始窗口大小 | ✅ | `GRPCConfig.InitialWindowSize` 和 `InitialConnWindowSize` 在 `config.go` 中。`new.go` 实现 |
| **TASK-020** — 配置项暴露与校验 | ✅ | `validate()` 在 `config.go` 中：检查 keepalive >= 1s，ReadHeaderTimeout >= 1s，MaxRecvMsgSize >= 1KB，MaxSendMsgSize >= 1KB |
| **TASK-021** — gRPC 服务器单元测试 | ✅ | `internal/handler/grpc/server_test.go` 存在，包含 `TestNewServerWithOptions_acceptsGRPCServerOptions`、`TestGRPCServerOptions_doesNotPanic`、`TestDegradationInterceptor_*` 以及其他拦截器测试 |

### 方向五：数据生命周期 (P2) — 全部完成 ✅

| 任务 | 状态 | 证据 |
|------|--------|---------|
| **TASK-022** — audit_events TTL 清理器 | ✅ | `internal/repository/audit.go`：`PurgeOlderThan`（分批处理，每批间隔 100ms）和 `RunAuditCleaner`。`new.go` 中配置了 `stopAuditClean` |
| **TASK-023** — notifications TTL 清理器 | ✅ | `internal/repository/notification.go`：`PurgeReadOlderThan`（已读 + 未读清理，分批处理）和 `RunNotificationCleaner`。`new.go` 中配置了 `stopNotifClean` |
| **TASK-024** — outbox_events 死信清理 | ✅ | `internal/outbox/repository.go`：`PurgeDeadLetters`（死信 + 已发布清理，分批处理）和 `RunOutboxCleaner`。`new.go` 中配置了 `stopOutboxClean` |
| **TASK-025** — 生命周期策略文档 | ✅ | `docs/DATA_RETENTION.md` 存在 |

## 接线验证

`internal/app/new.go` 正确连接了所有组件：

1. **传输加固** — 从 `GRPCConfig` 构建 gRPC keepalive/MaxConcurrentStreams/大小限制/窗口，并通过 `WithGRPCServerOptions` 传递
2. **HTTP ReadHeaderTimeout** — 在 `http.Server.ReadHeaderTimeout` 字段中设置
3. **降级状态** — 创建 `DegradationState`，注册 DB 和 SSO 探测器，每 30 秒启动探测，通过 `WithDegradation` 传递给 HTTP 和 gRPC
4. **数据清理器** — 所有三个清理器（audit、notification、outbox）在 `cfg.DataRetention.*` 为正时被连接，`stop*` 函数存储在 `App` 结构体中
5. **追踪自适应采样** — `DynamicSampler` 在 `new.go` 中通过 `tracing.Init()` 创建，当 `SampleRatio` 在 0 和 1 之间时启用
6. **日志采样** — `SampleRatio` 从 `cfg.Log.SampleRatio` 传递到 HTTP 路由配置
7. **优雅关闭** — 所有 `stop*` 函数在 `shutdown()` 中以正确顺序调用

## 验证步骤

```bash
cd /home/u1/aero-id

# 编译检查
go build ./...

# 单元测试（全部通过）
make test

# Lint 检查
make lint

# 集成测试（需要 Docker）
make test-integration

# 文件大小限制检查
make check-file-sizes
```

## 结论

实施计划 `docs/imp plan/2026-07-12-implementation-plan.md` 中详述的所有 5 个方向、25 个任务**已经完全实现并到位**。代码库在 `make test && make lint` 下完全通过，组件间接线正确，符合架构要求。
