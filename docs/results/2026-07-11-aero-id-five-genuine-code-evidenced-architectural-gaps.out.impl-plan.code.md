所有代码都已实现并验证。这是完整的状态报告：

---

# 代码实现报告

## 实现概述

实施计划中概述的所有5个工程方向（`docs/imp plan/2026-07-12-implementation-plan.md`）已**完全实现**。代码编译通过（`go vet ./...`），所有单元测试通过（`go test ./... -count=1`）。无需额外工作。

## 文件清单

### 方向一：发布工程
- `.goreleaser.yaml` — GoReleaser 构建配置，包含 Docker、SBOM 和签名
- `.github/workflows/docker.yml` — 自动 Docker 镜像构建 CI
- `.github/workflows/release.yml` — 标签触发发布流程
- `cliff.toml` — git-cliff 变更日志生成配置

### 方向二：可观测性成本
- `internal/middleware/sample.go` — AccessLog 概率采样器（跳过健康检查路径，状态码 >= 400 始终记录）
- `internal/middleware/access_log.go` — 集成了采样标志检查
- `internal/pkg/tracing/tracing.go` — 带有 `DynamicSampler` 的自适应追踪降采样（在导出错误率 > 10% 时自动降级）
- `internal/outbox/relay.go` — 每次 drain 输出的聚合日志（`outbox drain: handled=X failed=Y pending=Z...`）
- `internal/pkg/metrics/metrics.go` — Outbox 指标收集、SSO 缓存/延迟直方图

### 方向三：降级契约
- `docs/DEGRADATION_MATRIX.md` — 覆盖所有 HTTP + gRPC 端点的降级矩阵文档
- `internal/middleware/degradation.go` — `DegradationState`（原子位掩码）、探针、`DegradationAction` 策略函数、`DegradationHeader` HTTP 中间件
- `internal/middleware/degradation_test.go` — 状态机、并发安全、中间件的单元测试
- `internal/handler/grpc/server.go` — 所有 gRPC 端点的 `DegradationInterceptor`（设置 `x-aero-degraded` 尾部元数据）
- `internal/handler/grpc/server_test.go` — 降级拦截器集成测试（含 bufconn 健康检查）

### 方向四：传输协议加固
- `internal/app/new.go` — 配置驱动的 gRPC hardening：`KeepaliveEnforcementPolicy`（最短 10s）、`MaxConcurrentStreams`（100）、`MaxRecvMsgSize`/`MaxSendMsgSize`（4 MB）、`InitialWindowSize`（64 KiB）、`InitialConnWindowSize`（256 KiB），以及 HTTP `ReadHeaderTimeout`（10s）
- `internal/config/config.go` — `GRPCConfig` 包含所有 hardening 字段 + 验证（`MinTime >= 1s`、`MaxRecvMsgSize >= 1KB`、`ReadHeaderTimeout >= 1s`）
- `configs/config.yaml` — 所有 hardening 参数的默认值
- `internal/handler/grpc/server_test.go` — 验证选项不会 panic 并且服务器可运行的测试

### 方向五：数据生命周期
- `internal/repository/audit.go` — `PurgeOlderThan`（每批 1000 行，100ms 间隔）+ `RunAuditCleaner`（后台定时器，遵循 idempotency cleaner 模式）
- `internal/repository/notification.go` — `PurgeReadOlderThan`（已读截止 + 未读截止，每批 500 行）+ `RunNotificationCleaner`
- `internal/outbox/repository.go` — `PurgeDeadLetters`（死信截止 + 已发布截止，每批 500 行）+ `RunOutboxCleaner`
- `docs/DATA_RETENTION.md` — 覆盖所有 8 个表（保留策略、清理 SQL、监控指标、排除表）
- `internal/app/new.go` — 启动时挂接所有三个清洁器（在 `New()` 中，在关闭时停止）

## 验证

```
$ make lint
go vet ./...     # 通过

$ make test
go test ./... -count=1    # 全部通过（20 个包，0 个失败）
```

## 实施说明

实现严格遵循实施计划：
- **方向二**：`DynamicSampler` 实现了自定义的 `sdktrace.Sampler`，监控导出错误率并通过 `atomic.Uint64` 调整比率——没有使用 OTel 内部 API
- **方向三**：`DegradationAction()` 策略函数作为所有 handler 适配的单一事实来源，消除了遍布 8 个 HTTP + 8 个 gRPC handler 的重复降级检查
- **方向四**：所有 gRPC server 选项从 config 读取，通过 `WithGRPCServerOptions(...)` 传递——保留测试中 `NewServerWithOptions` 的现有 API
- **方向五**：清洁器使用 `DELETE ... WHERE id IN (SELECT id ... LIMIT N)` 分批处理，批次间有暂停，以避免 DB I/O 抖动。所有清洁器均为**选择加入**（仅当 `clean_interval > 0 && retention > 0` 时启动）
