All tests pass and `make lint` is clean. Here's my implementation report:

---

# 代码实现报告

## 实现概述

基于 `docs/imp plan/2026-07-12-implementation-plan.md` 实施计划，完成了推荐的第一次迭代范围内的所有代码任务（方向一、二、四、五的核心部分）以及降级矩阵文档（方向三）。

## 已实现任务

| 任务 | 标题 | 方向 | 状态 |
|------|------|------|------|
| TASK-015 | gRPC keepalive enforcement | 四 | ✅ |
| TASK-016 | gRPC MaxConcurrentStreams 限流 | 四 | ✅ |
| TASK-017 | gRPC 消息大小限制 | 四 | ✅ |
| TASK-018 | HTTP ReadHeaderTimeout 加固 | 四 | ✅ |
| TASK-019 | gRPC 初始窗口大小配置 | 四 | ✅ |
| TASK-020 | 配置项暴露与校验 | 四 | ✅ |
| TASK-021 | gRPC server 单元测试 | 四 | ✅ |
| TASK-006 | AccessLog 采样器 | 二 | ✅ |
| TASK-008 | Relay 日志聚合 | 二 | ✅ |
| TASK-022 | audit_events 表 TTL 清洁器 | 五 | ✅ |
| TASK-010 | 降级矩阵文档 | 三 | ✅ |

## 文件清单

### 新增文件
- `internal/app/new.go` — `New()` 组合根函数（从 app.go 拆分）
- `internal/app/storage.go` — `buildStorage()` 存储后端选择（从 app.go 拆分）
- `internal/app/warmup.go` — `warmup()` 启动预热（从 app.go 拆分）
- `internal/middleware/sample.go` — AccessLog 采样中间件
- `docs/DEGRADATION_MATRIX.md` — 降级矩阵文档

### 修改文件
- `internal/config/config.go` — 新增 gRPC/HTTP 加固配置字段、DataRetentionConfig、Log.SampleRatio、配置校验
- `internal/handler/grpc/server.go` — 新增 `WithGRPCServerOptions`、`keepalive` 导入、grpc.NewServer 选项合并
- `internal/app/app.go` — 新增 `ReadHeaderTimeout`、gRPC 加固选项、audit cleaner 启动/停止、SampleRatio 配线
- `internal/middleware/access_log.go` — 集成采样逻辑（status >= 400 始终记录）
- `internal/handler/http/router.go` — 新增 `SampleRatio` 配置字段、`Sample` 中间件配线
- `internal/outbox/relay.go` — 新增聚合日志输出（`outbox drain: handled=X failed=Y pending=Z...`）
- `internal/repository/audit.go` — 新增 `PurgeOlderThan`、`RunAuditCleaner` 批量 TTL 清理
- `internal/handler/grpc/server_test.go` — 新增 gRPC 选项测试
- `configs/config.yaml` — 新增所有配置默认值
- `go.mod` — 通过 `go mod tidy` 自动更新

## 核心代码实现

### 1. 传输协议加固（方向四）

#### gRPC 服务端选项（T015-T019, T020）

通过 `WithGRPCServerOptions` 函数选项模式传递底层 gRPC 选项，保持 API 向后兼容：

```go
// internal/handler/grpc/server.go
func WithGRPCServerOptions(opts ...grpc.ServerOption) ServerOption {
    return func(o *serverOptions) { o.grpcOpts = append(o.grpcOpts, opts...) }
}
```

在 `NewServerWithOptions` 中合并：

```go
allOpts := []grpc.ServerOption{grpc.ChainUnaryInterceptor(interceptors...)}
allOpts = append(allOpts, o.grpcOpts...)
s := grpc.NewServer(allOpts...)
```

在 `app.go` 中配置所有安全参数：

```go
grpcKeepalive := keepalive.EnforcementPolicy{
    MinTime:             10 * time.Second,
    PermitWithoutStream: false,
}
var grpcOpts []grpclib.ServerOption
grpcOpts = append(grpcOpts, grpclib.KeepaliveEnforcementPolicy(grpcKeepalive))
// MaxConcurrentStreams, MaxRecvMsgSize, MaxSendMsgSize, InitialWindowSize, InitialConnWindowSize
```

#### HTTP ReadHeaderTimeout（T018）

```go
httpServer := &http.Server{
    ...
    ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout, // 新增：防止 Slowloris
}
```

### 2. AccessLog 采样器（T006）

新增 `internal/middleware/sample.go`：

```go
func Sample(ratio float64) gin.HandlerFunc {
    // ratio <= 0: 全部记录（向后兼容）
    // ratio >= 1: 全部记录
    // 0 < ratio < 1: 概率采样
    // /healthz, /readyz 始终记录
    // status >= 400 在 AccessLog 中始终记录（不受采样影响）
}
```

在 `AccessLog` 中检查采样标记：

```go
if c.Writer.Status() < 400 {
    sampled, ok := c.Get("sampled")
    if ok && !sampled.(bool) {
        return // 跳过采样请求的日志
    }
}
```

### 3. Relay 日志聚合（T008）

修改 `drain` 方法，在每次 drain 结束时输出一行聚合日志：

```go
r.log.Info("outbox drain",
    zap.Int("handled", len(published)),
    zap.Int("failed", failed),
    zap.Int("pending", len(events)),
    zap.Duration("oldest_age", oldestAge),
    zap.Duration("duration", dur),
)
```

每个事件的 handler 失败仍保留在 warn 级别不变。

### 4. audit_events TTL 清洁器（T022）

新增 `PurgeOlderThan` 方法，使用分批删除策略：

```go
func (r *auditRepo) PurgeOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
    // 每次删除 1000 行，间隔 100ms，避免 IO 冲击
    // 使用子查询 `WHERE id IN (SELECT id FROM ... LIMIT batchSize)`
}
```

新增 `RunAuditCleaner` 后台 goroutine 模式（与 `idemStore.RunCleaner` 一致），通过 `data_retention` 配置控制。

### 5. 配置结构扩展（T020）

| 新增字段 | 类型 | 默认值 |
|----------|------|--------|
| `server.http.read_header_timeout` | Duration | 10s |
| `server.grpc.keepalive_min_ping_interval` | Duration | 10s |
| `server.grpc.max_concurrent_streams` | uint32 | 100 |
| `server.grpc.max_recv_msg_size` | int | 4MB |
| `server.grpc.max_send_msg_size` | int | 4MB |
| `server.grpc.initial_window_size` | int32 | 64KB |
| `server.grpc.initial_conn_window_size` | int32 | 256KB |
| `log.sample_ratio` | float64 | 0 (不采样) |
| `data_retention.audit_retention` | Duration | 90d |
| `data_retention.audit_clean_interval` | Duration | 1h |

### 6. 代码拆分重构

`internal/app/app.go` 从 410 行增长到 515 行后，按职责拆分为 4 个文件：

| 文件 | 职责 | 行数 |
|------|------|------|
| `app.go` | App 结构体、生命周期方法（Run/shutdown/Close） | 139 |
| `new.go` | 组合根 `New()` 函数 | 380 |
| `storage.go` | `buildStorage()` 存储后端选择 | 40 |
| `warmup.go` | `warmup()` 启动预热 | 33 |

### 7. 降级矩阵文档（T010）

创建 `docs/DEGRADATION_MATRIX.md`，覆盖所有 HTTP/gRPC 端点，定义了在 DB/SSO/S3 故障时的预期行为，包含响应头和状态码约定。

## 验证结果

- `make test` — ✅ 所有测试通过
- `make lint` (go vet) — ✅ 无错误
- `make check-file-sizes` — ✅ 所有文件 ≤ 500 行
- `go build ./...` — ✅ 编译通过

## 关键设计决策

1. **函数选项模式**：`WithGRPCServerOptions` 作为 `ServerOption` 传递底层 gRPC 配置，不破坏现有的 `NewServerWithOptions` API，向后兼容
2. **采样中间件前置**：`Sample` 中间件在 `AccessLog` 之前运行，仅设置 `sampled` 上下文标记，`AccessLog` 根据标记和状态码决定是否输出。分离职责清晰
3. **`RunAuditCleaner` 作为包级函数**：与 `GormStore.RunCleaner` 模式一致，但设计为接受 `AuditRepository` 接口而非具体类型，便于测试
4. **分批删除**：使用 `WHERE id IN (SELECT id FROM ... LIMIT batchSize)` 子查询 + 100ms 间隔，避免长事务和 IO 冲击

## 遗留任务（后续 Sprint）

以下任务未被本次实现覆盖，建议安排到后续迭代：
- TASK-001~005: Goreleaser 配置、Docker CI、版本标签、CHANGELOG 自动化、SBOM（需要 GitHub Actions 基础设施）
- TASK-007: 追踪自适应降采样（OTel Sampler 动态切换较复杂）
- TASK-009: 指标写放大优化（需要修改 metrics register 的内部结构）
- TASK-011~014: 降级契约代码实现（状态管理器+中间件+Handler 适配+集成测试，估时 30h）
- TASK-023~025: notifications 清洁器、outbox 死信清理、数据生命周期文档
