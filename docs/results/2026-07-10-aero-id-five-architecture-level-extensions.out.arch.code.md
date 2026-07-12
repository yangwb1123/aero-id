所有 25 个任务现已全面实施。以下是完整报告。

---

# 代码实现报告

## 实现概述

2026-07-12 实施计划中的所有 5 个方向、25 个任务均已全面实施。该代码库在所有单元测试、构建、go vet 和文件大小检查中均通过。以下是对照计划任务的逐项审计。

## 文件清单

### 方向一：发布工程（5/5 已完成）

| 任务 | 文件 | 状态 |
|------|------|--------|
| TASK-001 — Goreleaser 配置 | `.goreleaser.yaml` | ✅ 已实施 |
| TASK-002 — Docker CI 工作流 | `.github/workflows/docker.yml` | ✅ 已实施 |
| TASK-003 — 版本标签策略与发布 | `.github/workflows/release.yml` | ✅ 已实施 |
| TASK-004 — CHANGELOG 自动化 | `cliff.toml` + `docs/CHANGELOG.md` | ✅ 已实施 |
| TASK-005 — SBOM 与签名 | `.goreleaser.yaml`（syft + cosign 部分） | ✅ 已实施 |

### 方向二：可观测性成本（4/4 已完成）

| 任务 | 文件 | 状态 |
|------|------|--------|
| TASK-006 — AccessLog 采样器 | `internal/middleware/sample.go` + `access_log.go` | ✅ 已实施 |
| TASK-007 — 自适应追踪降采样 | `internal/pkg/tracing/tracing.go`（DynamicSampler） | ✅ 已实施 |
| TASK-008 — Relay 日志聚合 | `internal/outbox/relay.go` | ✅ 已实施 |
| TASK-009 — 指标写入放大优化 | `internal/pkg/metrics/metrics.go` | ✅ 已实施 |

### 方向三：降级契约（5/5 已完成）

| 任务 | 文件 | 状态 |
|------|------|--------|
| TASK-010 — 降级矩阵文档 | `docs/DEGRADATION_MATRIX.md` | ✅ 已实施 |
| TASK-011 — 降级状态管理器 | `internal/middleware/degradation.go` | ✅ 已实施 |
| TASK-012 — HTTP + gRPC 中间件 | `internal/middleware/degradation.go` + `internal/handler/grpc/server.go` | ✅ 已实施 |
| TASK-013 — Handler 适配 | 各文件中 `AbortIfDegraded()` 的用法 | ✅ 已实施 |
| TASK-014 — 降级集成测试 | `internal/middleware/degradation_test.go` | ✅ 已实施 |

### 方向四：传输协议加固（7/7 已完成）

| 任务 | 文件 | 状态 |
|------|------|--------|
| TASK-015 — gRPC keepalive 执行策略 | `internal/app/new.go`（keepalive.EnforcementPolicy） | ✅ 已实施 |
| TASK-016 — gRPC MaxConcurrentStreams | `internal/app/new.go`（`grpclib.MaxConcurrentStreams`） | ✅ 已实施 |
| TASK-017 — gRPC 消息大小限制 | `internal/app/new.go`（`MaxRecvMsgSize`/`MaxSendMsgSize`） | ✅ 已实施 |
| TASK-018 — HTTP ReadHeaderTimeout | `internal/app/new.go`（`http.Server.ReadHeaderTimeout`） | ✅ 已实施 |
| TASK-019 — gRPC 初始窗口大小 | `internal/app/new.go`（`InitialWindowSize`/`InitialConnWindowSize`） | ✅ 已实施 |
| TASK-020 — 配置暴露与校验 | `internal/config/config.go` | ✅ 已实施 |
| TASK-021 — gRPC 服务器单元测试 | `internal/handler/grpc/server_test.go` | ✅ 已实施 |

### 方向五：数据生命周期（4/4 已完成）

| 任务 | 文件 | 状态 |
|------|------|--------|
| TASK-022 — audit_events 清理器 | `internal/repository/audit.go`（`PurgeOlderThan` + `RunAuditCleaner`） | ✅ 已实施 |
| TASK-023 — 通知清理器 | `internal/repository/notification.go`（`PurgeReadOlderThan` + `RunNotificationCleaner`） | ✅ 已实施 |
| TASK-024 — outbox_events 死信清理 | `internal/outbox/repository.go`（`PurgeDeadLetters` + `RunOutboxCleaner`） | ✅ 已实施 |
| TASK-025 — 数据保留文档 | `docs/DATA_RETENTION.md` | ✅ 已实施 |
| 应用程序中的布线 | `internal/app/new.go`（所有 3 个清理器都已连接） | ✅ 已实施 |

## 核心实现细节

### 方向一：发布工程

`.goreleaser.yaml` 为 **linux/amd64** 和 **linux/arm64** 配置了两个构建产物（`aero-id` 服务器 + `migrate` CLI），使用现有的 `deployments/Dockerfile` 构建 Docker 镜像，使用 `syft` 生成 CycloneDX SBOM，并使用 `cosign` 进行签名。

Docker CI 工作流（`.github/workflows/docker.yml`）在每次推送到 main 时构建并推送，在 PR 上仅构建（不推送）。它使用 Docker Buildx、GitHub Actions 缓存，并标记 `sha-*`、`main` 和 `latest` 镜像。

Release 工作流（`.github/workflows/release.yml`）由 `git tag v*` 触发，运行 git-cliff 生成变更日志，然后运行 GoReleaser 构建二进制文件、Docker 镜像、SBOM 和签名。

### 方向二：可观测性成本

**AccessLog 采样（TASK-006）：** `middleware.Sample(ratio)` 中间件根据 `rand.Float64() < ratio` 概率性地在每个请求上下文中设置 `sampled` 标志。`AccessLog` 中间件检查此标志：如果 `sampled` 为 false 且状态码 < 400，则跳过日志行。健康检查路径（`/healthz`、`/readyz`）始终记录，错误也始终记录。

**自适应追踪降采样（TASK-007）：** `DynamicSampler` 实现了 `sdktrace.Sampler`，并在后台监控导出错误率。该采样器每 10 秒窗口检查一次。如果错误率 > 10%（连续 3 个窗口），它将比率降至 `max(0.1, current/10)`。恢复分两步进行：5 个连续的健康窗口后，比率每 2 分钟恢复 `+0.1`，直到达到原始值。使用原子操作保证线程安全。

**Relay 日志聚合（TASK-008）：** 每次清空传递结束时，`relay.go` 中的 `drain()` 方法现在记录一行汇总信息（`handled=X failed=Y pending=Z oldest_age=... duration=...`），而不是每个事件一行。单个处理程序故障仍记录在 warn 级别。

**指标写入放大优化（TASK-009）：** `outboxPending` 和 `outboxLagSecs` 是带有 `atomic.Uint64` 的 last-write 仪表盘（编码为 `math.Float64bits`）——无需锁。Drain 直方图由 `outboxMu` 保护，该互斥锁与 HTTP/gRPC 和 SSO 指标使用的 `mu` 完全分开。这消除了主清空路径（每 5 秒）上锁争用的根源。

### 方向三：降级契约

**降级状态管理器：** `DegradationState` 使用 `atomic.Uint32` 位掩码（bit 0 = DB、bit 1 = SSO、bit 2 = Storage）进行无锁状态读取。探针使用独立的 3 秒超时上下文运行。降级状态在连续 `failureThreshold` 次失败后转换，在连续 `successThreshold` 次成功时恢复。后台探测在启动时注册。

**HTTP 中间件：** `DegradationHeader(state)` 将 `X-Aero-Degraded` 标头写入每个响应，并将状态引用存储在 gin 上下文中（因此处理程序可以查询它）。

**gRPC 拦截器：** `DegradationInterceptor` 在响应尾随元数据中写入 `x-aero-degraded`。基础架构方法（健康、反射）始终被标记为 `"none"`。

**处理程序适配：** `AbortIfDegraded(c, deps, readOnly)` 是一个便捷辅助函数，供 HTTP 处理程序调用。它查询当前降级状态，如果策略要求，则返回 503 并中止。它具有三种响应模式：`DegradedFail503`（写入操作）、`DegradedStaleOK`（读取操作，DB 退化 - 可以返回陈旧缓存）和 `DegradedProceed`（不受影响）。

### 方向四：传输协议加固

**Keepalive 执行策略：** `keepalive.EnforcementPolicy{MinTime: 10s, PermitWithoutStream: false}` — 客户端 ping 频率不能超过每 10 秒一次，空闲连接不允许 ping。可通过 `server.grpc.keepalive_min_ping_interval` 配置。

**流限制：** `MaxConcurrentStreams: 100` — 第 101 个并发流收到 `Unavailable`。可通过 `server.grpc.max_concurrent_streams` 配置。

**消息大小限制：** `MaxRecvMsgSize: 4MB` 和 `MaxSendMsgSize: 4MB` — 可通过 `server.grpc.max_recv_msg_size` / `max_send_msg_size` 配置。验证器确保设定时 `>= 1KB`。

**HTTP Slowloris 防护：** `http.Server.ReadHeaderTimeout: 10s` — 可通过 `server.http.read_header_timeout` 配置。验证器确保设定时 `>= 1s`。

**流控窗口：** `InitialWindowSize: 64KB`、`InitialConnWindowSize: 256KB`。

**gRPC 服务器测试：** `server_test.go` 使用 `bufconn` 进行环回测试，并验证 auth 拦截器（缺失令牌、令牌无效、注入 actor）、速率限制拦截器（限制超额 = `ResourceExceeded`）、降级拦截器（`x-aero-degraded` 尾随元数据）以及健康/反射方法绕过。

### 方向五：数据生命周期

所有三个清理器都采用相同的模式：

1. **`PurgeOlderThan` / `PurgeReadOlderThan` / `PurgeDeadLetters`** — GORM 方法，使用子查询以 `ASC, id ASC` 顺序选择最多 `batchSize` 行，然后删除它们，在每批之间暂停 100 毫秒。所有操作均重复进行，直到 `RowsAffected == 0`。

2. **`Run*Cleaner`** — 后台 goroutine，在 config 驱动的周期上运行清理方法。清理行数被记录在 info 级别。非正周期返回空操作 `func()`。

3. **在 `app/new.go` 中布线** — 每个清理器都是 OPT-IN（当 `clean_interval <= 0` 时跳过）。停止函数存储在 `App` 结构体中，以便优雅关闭。

保留策略：
- **审计事件：** 90 天（`data_retention.audit_retention`），每小时清理
- **通知：** 已读 30 天，未读 90 天，每小时清理
- **Outbox 事件：** 死信 7 天，已发布 30 天，每小时清理

## 验证

所有验证门均已通过：

```
$ go build ./...                # 构建成功（零错误）
$ go test ./... -count=1        # 所有测试通过（28 个包）
$ make lint                     # go vet ./... 通过
$ make check-file-sizes         # ✓ 所有文件 ≤ 500 行
```

`make test` 输出：
```
ok  github.com/aero/aero-id/internal/audit        0.004s
ok  github.com/aero/aero-id/internal/block         0.008s
ok  github.com/aero/aero-id/internal/comment       0.020s
ok  github.com/aero/aero-id/internal/config        0.003s
ok  github.com/aero/aero-id/internal/domain        0.002s
ok  github.com/aero/aero-id/internal/file          0.017s
ok  github.com/aero/aero-id/internal/handler/grpc  0.037s
ok  github.com/aero/aero-id/internal/handler/http  0.012s
ok  github.com/aero/aero-id/internal/hub           0.005s
ok  github.com/aero/aero-id/internal/message       0.017s
ok  github.com/aero/aero-id/internal/middleware    0.183s
ok  github.com/aero/aero-id/internal/notification  0.010s
ok  github.com/aero/aero-id/internal/outbox        0.113s
ok  github.com/aero/aero-id/internal/pkg/tracing   0.007s
ok  github.com/aero/aero-id/internal/sso           0.217s
...（所有 28 个包通过）
```

## 配置要求

在 `configs/config.yaml` 中，所有配置均已就位：

```yaml
# 方向二 — 访问日志采样
log: { sample_ratio: 0 }  # 0..1; <1 采样 info 级别的日志

# 方向四 — 传输协议加固
server:
  http:
    read_header_timeout: 10s              # Slowloris 防护
  grpc:
    keepalive_min_ping_interval: 10s       # ping 间隔 ≥ 1s
    max_concurrent_streams: 100            # 0 = 无限
    max_recv_msg_size: 4194304             # 4 MiB
    max_send_msg_size: 4194304             # 4 MiB
    initial_window_size: 65536             # 64 KiB
    initial_conn_window_size: 262144       # 256 KiB

# 方向五 — 数据生命周期
data_retention:
  audit_retention: 2160h                   # 90 天
  audit_clean_interval: 1h
  notification_retention: 720h             # 30 天
  notification_unread_retention: 2160h     # 90 天
  notification_clean_interval: 1h
  outbox_dead_letter_retention: 168h       # 7 天
  outbox_published_retention: 720h         # 30 天
  outbox_clean_interval: 1h
```

## 总结

所有 **25 个计划任务** 均完全实施，通过编译、测试、lint 和文件大小检查。代码库可在不破坏现有功能的情况下投入生产使用。审核的重点是：

1. **可维护性：** 单一职责在每个包中得到维护；`make check-file-sizes` 通过；严格的单向依赖关系得到保留。
2. **线程安全：** 动态采样器、降级位掩码和 outbox 指标仪表盘都使用原子操作，以避免在可观测性路径上产生锁争用。
3. **安全：** gRPC keepalive 执行、消息大小限制、`ReadHeaderTimeout` 和可配置的流限制，消除了已识别的 DoS 向量。
4. **可观察性：** 访问日志采样、自适应追踪降采样和聚合 relay 日志将在高负载下显著降低日志成本和追踪后端压力。
5. **数据生命周期：** 所有三个清理器（审计、通知、outbox）均已接线并可配置，具有批量删除 + 间隙间隔以最大程度减少生产中的 IO 影响。
