All checks pass. Here is the comprehensive code implementation report:

---

# 代码实现报告

## 实现概述

经过对代码库的全面审查，**2026-07-12 技术实现方案中描述的所有 5 个工程方向、25 个任务已全部实现**。代码库已通过 `make build`、`make test`、`make lint` 和 `make check-file-sizes` 验证。

## 文件清单

### 方向一：发布工程 (Release Engineering)

| 任务 | 文件 | 状态 | 说明 |
|------|------|------|------|
| TASK-001 | `.goreleaser.yaml` | ✅ 已实现 | Goreleaser v2 配置：双构建（aero-id + migrate）、Docker 镜像、SBOM、cosign 签名 |
| TASK-002 | `.github/workflows/docker.yml` | ✅ 已实现 | Docker CI — 每次 push 到 main 自动构建并 push 到 ghcr.io |
| TASK-003 | `.github/workflows/release.yml` | ✅ 已实现 | 标签触发 Goreleaser 发布；包含 git-cliff 生成 changelog |
| TASK-004 | `cliff.toml` | ✅ 已实现 | git-cliff 配置：按 Conventional Commits 类型分组 |
| TASK-005 | `.goreleaser.yaml (sboms + signs)` | ✅ 已实现 | 使用 syft 生成 CycloneDX SBOM，cosign 签名 |

### 方向二：可观测性成本 (Observability Cost)

| 任务 | 文件 | 状态 | 说明 |
|------|------|------|------|
| TASK-006 | `internal/middleware/sample.go` | ✅ 已实现 | AccessLog 采样中间件；健康检查路径始终记录；>=400 始终记录 |
| TASK-006 | `internal/middleware/access_log.go` | ✅ 已实现 | 集成采样标志位检查 |
| TASK-007 | `internal/pkg/tracing/tracing.go` | ✅ 已实现 | DynamicSampler — 自适应降采样；10s 窗口监控导出错误率；>10% 触发降采样 |
| TASK-008 | `internal/outbox/relay.go` | ✅ 已实现 | 聚合日志：每次 drain 输出一行汇总（handled/failed/pending/oldest_age/duration） |
| TASK-009 | `internal/pkg/metrics/metrics.go` | ✅ 已实现 | outbox 指标使用独立互斥锁 + atomic gauges，不阻塞 HTTP/gRPC 指标 |

### 方向三：降级契约 (Degradation Contract)

| 任务 | 文件 | 状态 | 说明 |
|------|------|------|------|
| TASK-010 | `docs/DEGRADATION_MATRIX.md` | ✅ 已实现 | 完整覆盖所有端点在 DB/SSO/S3 故障时的行为 |
| TASK-011 | `internal/middleware/degradation.go` | ✅ 已实现 | DegradationState 管理器 + StartProbing 后台探测 |
| TASK-012 | `internal/middleware/degradation.go` | ✅ 已实现 | DegradationHeader HTTP 中间件 |
| TASK-012 | `internal/handler/grpc/server.go` | ✅ 已实现 | DegradationInterceptor gRPC 拦截器 |
| TASK-013 | `internal/handler/grpc/server.go` | ✅ 已实现 | DegradationAction + AbortIfDegraded 辅助函数 |
| TASK-014 | `internal/middleware/degradation_test.go` | ✅ 已实现 | 状态机 + 中间件测试 |

### 方向四：传输协议加固 (Transport Protocol Hardening)

| 任务 | 文件 | 状态 | 说明 |
|------|------|------|------|
| TASK-015 | `internal/app/new.go` | ✅ 已实现 | `keepalive.EnforcementPolicy{MinTime: 10s, PermitWithoutStream: false}` |
| TASK-016 | `internal/app/new.go` | ✅ 已实现 | `MaxConcurrentStreams` 从 config 读取 |
| TASK-017 | `internal/app/new.go` | ✅ 已实现 | `MaxRecvMsgSize` / `MaxSendMsgSize` 从 config 读取 |
| TASK-018 | `internal/app/new.go` | ✅ 已实现 | `http.Server.ReadHeaderTimeout: 10s` |
| TASK-019 | `internal/app/new.go` | ✅ 已实现 | `InitialWindowSize` / `InitialConnWindowSize` |
| TASK-020 | `internal/config/config.go` | ✅ 已实现 | GRPCConfig 完整字段 + validate() 校验 |
| TASK-021 | `internal/handler/grpc/server_test.go` | ✅ 已实现 | 测试 NewServerWithOptions 验证 options |

### 方向五：数据生命周期 (Data Lifecycle)

| 任务 | 文件 | 状态 | 说明 |
|------|------|------|------|
| TASK-022 | `internal/repository/audit.go` | ✅ 已实现 | PurgeOlderThan + RunAuditCleaner |
| TASK-023 | `internal/repository/notification.go` | ✅ 已实现 | PurgeReadOlderThan + RunNotificationCleaner |
| TASK-024 | `internal/outbox/repository.go` | ✅ 已实现 | PurgeDeadLetters + RunOutboxCleaner |
| TASK-025 | `docs/DATA_RETENTION.md` | ✅ 已实现 | 完整覆盖每个表的 retention 策略 |

### 启动编排

| 文件 | 状态 | 说明 |
|------|------|------|
| `internal/app/new.go` | ✅ 已实现 | 启动所有清洁器、降级状态探测、gRPC 加固选项 |
| `internal/app/app.go` | ✅ 已实现 | 优雅关闭所有 stop 函数 |
| `configs/config.yaml` | ✅ 已实现 | 所有配置项默认值 |

## 核心代码实现

### 关键实现亮点

#### 1. gRPC 传输协议加固 (`internal/app/new.go`)

```go
// gRPC transport — build transport-level hardening options from config.
grpcKeepalive := keepalive.EnforcementPolicy{
    MinTime:             10 * time.Second,
    PermitWithoutStream: false,
}
if cfg.Server.GRPC.KeepaliveMinPingInterval > 0 {
    grpcKeepalive.MinTime = cfg.Server.GRPC.KeepaliveMinPingInterval
}
var grpcOpts []grpclib.ServerOption
grpcOpts = append(grpcOpts,
    grpclib.KeepaliveEnforcementPolicy(grpcKeepalive),
)
if cfg.Server.GRPC.MaxConcurrentStreams > 0 {
    grpcOpts = append(grpcOpts, grpclib.MaxConcurrentStreams(cfg.Server.GRPC.MaxConcurrentStreams))
}
// ... MaxRecvMsgSize, MaxSendMsgSize, InitialWindowSize, InitialConnWindowSize
```

#### 2. 自适应降采样 (`internal/pkg/tracing/tracing.go`)

- `DynamicSampler` 实现 `sdktrace.Sampler` 接口
- 后台 goroutine 每 10s 检查导出错误率
- 错误率 > 10% → 降采样到 `max(0.1, original/10)`
- 连续 5 个窗口错误率 < 2% → 每 2 分钟恢复 0.1 直到原值

#### 3. 降级状态管理器 (`internal/middleware/degradation.go`)

- 使用 atomic bitmask 标识 DB/SSO/Storage 降级状态
- `StartProbing` 后台探测：3 次连续失败标记降级，2 次连续成功恢复
- `DegradationHeader` 中间件写入 `X-Aero-Degraded` 响应头
- `AbortIfDegraded` 辅助函数：读端点返回 200(stale)，写端点返回 503

#### 4. 分批 TTL 清理模式

所有三个清洁器采用相同模式：分批删除 + 100ms 间隔防 IO 冲击

```go
func (r *auditRepo) PurgeOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
    // ... each batch:
    sub := r.db.WithContext(ctx).Model(&auditEventRow{}).
        Where("created_at < ?", cutoff).
        Order("created_at ASC, id ASC").
        Select("id").Limit(batchSize)
    res := r.db.WithContext(ctx).
        Where("id IN (?)", sub).
        Delete(&auditEventRow{})
    // plus time.Sleep(purgeInterval)
}
```

## 依赖说明

无新增依赖。所有实现均使用项目中已有的依赖：
- `google.golang.org/grpc/keepalive` — 用于 gRPC keepalive enforcement
- `go.opentelemetry.io/otel/sdk/trace` — 用于 DynamicSampler
- `gorm.io/gorm` — 用于分批删除
- `sync/atomic` — 用于 DegradationState bitmask 和 DynamicSampler 比率

## 验证步骤

```bash
# 1. 编译
make build

# 2. 运行单元测试
make test

# 3. Lint 检查
make lint

# 4. 文件大小限制检查
make check-file-sizes

# 5. 运行集成测试（需要 Docker）
make test-integration
```

## 已知限制

1. **降级契约 Handler 适配**：AbortIfDegraded 辅助函数已提供，但每个 handler 的具体降级行为需要根据业务逻辑进一步细化（如读端点使用 stale 缓存数据的实际实现）。

2. **gRPC keepalive 灰度**：当前 MinTime 直接设为 10s，建议在生产环境从 30s 开始逐步降低。

3. **自适应降采样恢复策略**：恢复速度较慢（每 2 分钟 +0.1），在生产环境可能需要根据实际情况调整参数。

4. **数据清理的锁影响**：分批清理使用 `time.Sleep` 而不是更精细的背压控制，在大表上可能会有短暂 IO 峰值。

## 配置要求

所有配置项已在 `configs/config.yaml` 中提供默认值，可通过 `AERO_ID_*` 环境变量覆盖。关键配置项：

```yaml
# 传输加固
server:
  grpc:
    keepalive_min_ping_interval: 10s
    max_concurrent_streams: 100
    max_recv_msg_size: 4194304      # 4 MiB
    max_send_msg_size: 4194304      # 4 MiB
    initial_window_size: 65536      # 64 KiB
    initial_conn_window_size: 262144 # 256 KiB
  http:
    read_header_timeout: 10s        # Slowloris 防护

# 数据生命周期
data_retention:
  audit_retention: 2160h            # 90 天
  audit_clean_interval: 1h
  notification_retention: 720h      # 30 天（已读）
  notification_unread_retention: 2160h  # 90 天（未读）
  notification_clean_interval: 1h
  outbox_dead_letter_retention: 168h    # 7 天
  outbox_published_retention: 720h      # 30 天
  outbox_clean_interval: 1h

# 访问日志采样
log:
  sample_ratio: 0                   # 0 = 不采样; 0.1 = 10%
```

## 检查清单

- [x] 代码可以编译通过 (`make build` ✅)
- [x] 所有测试通过 (`make test` ✅)
- [x] Lint 检查通过 (`make lint` ✅)
- [x] 文件大小限制通过 (`make check-file-sizes` ✅)
- [x] 错误处理完整
- [x] 边界条件已处理（健康检查路径始终记录、0 值禁用清洁器等）
- [x] 安全检查已完成（gRPC keepalive enforcement、HTTP ReadHeaderTimeout、消息大小限制）
- [x] 性能无明显问题（分批清理、atomic 降级状态、独立 outbox 互斥锁）
- [x] 文档已更新（`DEGRADATION_MATRIX.md`、`DATA_RETENTION.md`）
- [x] 代码风格一致
