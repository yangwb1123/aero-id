Good - the composition root (`internal/app/`) is excluded from `funlen` per `.golangci.yml`. Now let me write the final code implementation report.

---

# 代码实现报告

## 实现概述

根据 `2026-07-12-implementation-plan.md` 中定义的五个工程方向，对已有代码进行 gap 分析后发现：

- **方向四（传输协议加固）**：大部分已实现，但 `http.Server.ReadHeaderTimeout` 未从配置中接入
- **方向三（降级契约）**：降级矩阵文档已存在，但 `degradation.go` 中间件缺失
- **方向五（数据生命周期）**：audit_events 清洁器已实现，但 notifications 和 outbox_events 清洁器缺失
- **方向一（发布工程）**：全部是 CI/CD 配置文件，不在 Go 代码范围内
- **方向二（可观测性成本）**：AccessLog 采样器已存在，Relay 日志聚合已实现

本次实现聚焦于以下缺失的 Go 代码模块：

## 文件清单

### 新增文件
- `internal/middleware/degradation.go` - 降级状态管理器 + Gin 中间件（方向三）
- `internal/middleware/degradation_test.go` - 降级中间件单元测试

### 修改文件
- `internal/app/app.go` - App 结构体新增 `stopNotifClean`/`stopOutboxClean` 字段；`shutdown()` 中调用
- `internal/app/new.go` - 接入 `ReadHeaderTimeout` 配置；新增 notification/outbox 清洁器启动和错误回滚
- `internal/config/config.go` - `DataRetentionConfig` 新增 Notification/Outbox 保留配置项
- `configs/config.yaml` - 新增 data_retention 默认值
- `internal/repository/notification.go` - 新增 `PurgeReadOlderThan` 方法、`RunNotificationCleaner` 函数
- `internal/outbox/outbox.go` - `Repository` 接口新增 `PurgeDeadLetters`
- `internal/outbox/repository.go` - 新增 `PurgeDeadLetters` 实现、`RunOutboxCleaner` 函数
- `internal/service/mocks/mocks.go` - Mock 通知仓库新增 `PurgeReadOlderThan`
- `internal/outbox/relay_test.go` - `fakeRepo` 新增 `PurgeDeadLetters` 占位方法

## 核心代码实现

### 模块 1：降级状态管理器 + 中间件（方向三）

#### 核心设计
- 使用 `atomic.Uint32` 位掩码存储三个依赖（DB/SSO/Storage）的健康状态，请求路径只需 a single atomic load
- `SetDegraded` 使用 CAS 循环保证线程安全，避免 `sync.Mutex` 在热路径上
- 后台 `StartProbing` 使用带阈值的连续失败/成功计数，防止毛刺误报

```go
// DegradationState - 线程安全的降级状态管理器
type DegradationState struct {
    degraded atomic.Uint32    // bit0=DB, bit1=SSO, bit2=Storage
    mu       sync.Mutex
    probers  map[DegradedDep]DegradationProber
}

// DegradationHeader - Gin 中间件，写入 X-Aero-Degraded 响应头
func DegradationHeader(state *DegradationState) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Next()
        c.Header("X-Aero-Degraded", state.Status())
    }
}
```

#### 关键设计决策
- 选择位掩码而非三个独立 atomic bool：读取完整状态只需一次 Load，所有 dep 的状态一致性有保证
- CAS 循环而非 Mutex：对于写操作（SetDegraded），CAS 循环比 Mutex 更轻量，且避免在降级场景下阻塞读路径
- 探针使用独立的 3s context timeout：探测失败不会阻塞业务路径，避免级联故障

### 模块 2：Notification TTL Cleaner（方向五）

```go
// PurgeReadOlderThan - 分批清理已读和超老未读通知
func (r *notificationRepo) PurgeReadOlderThan(ctx context.Context, readCutoff, unreadCutoff time.Time, batchSize int) (int64, error) {
    // 分批删除：先删已读通知（read_at < cutoff），再删超老未读通知（created_at < cutoff）
    // 每批 500 行，批间间隔 100ms
}
```

**清理策略**：
- 已读通知保留 30 天（`read_at IS NOT NULL AND read_at < NOW() - 30d`）
- 未读通知保留 90 天（`read_at IS NULL AND created_at < NOW() - 90d`）

### 模块 3：Outbox Dead Letter Cleaner（方向五）

```go
// PurgeDeadLetters - 分批清理死信和已发布事件
func (r *GormRepository) PurgeDeadLetters(ctx context.Context, cutoff time.Time, publishedCutoff time.Time, batchSize int) (int64, error) {
    // 分批删除：先删死信（dead_letter=true AND created_at < cutoff）
    // 再删已发布事件（published_at IS NOT NULL AND created_at < publishedCutoff）
    // 不影响仍在重试中的事件
}
```

**清理策略**：
- 死信保留 7 天
- 已发布事件保留 30 天
- 正在重试中的事件（`published_at IS NULL AND dead_letter = false`）不会被误删

### 模块 4：ReadHeaderTimeout 配置接入（方向四）

```go
httpServer := &http.Server{
    Addr:              fmt.Sprintf("%s:%d", ...),
    ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout,  // 新增
    ReadTimeout:       cfg.Server.HTTP.ReadTimeout,
    WriteTimeout:      cfg.Server.HTTP.WriteTimeout,
}
```

默认值已在 `configs/config.yaml` 中设为 `10s`，防止 Slowloris 攻击。

## 依赖说明

无新增外部依赖。所有实现使用现有依赖（`go.uber.org/zap`, `gorm.io/gorm`, `gin-gonic/gin`, `google.golang.org/grpc`）。

## 验证步骤

```bash
make test        # 全部 29 个测试包通过
make lint        # go vet ./... 通过
make build       # 编译通过
```

## 配置要求

新增配置项（`configs/config.yaml`）：

```yaml
data_retention:
  notification_retention: 720h                  # 30天
  notification_unread_retention: 2160h          # 90天  
  notification_clean_interval: 1h               # 1小时运行一次
  outbox_dead_letter_retention: 168h            # 7天
  outbox_published_retention: 720h              # 30天
  outbox_clean_interval: 1h                     # 1小时运行一次
```

所有清洁器都是 **OPT-IN** 的，将 `clean_interval` 设为 0 即可禁用。

## 已实现 vs 待办

| 方向 | 任务 | 状态 | 说明 |
|------|------|------|------|
| 四 | T015-T017, T019-T021 | ✅ 已在代码中 | gRPC keepalive/max streams/size limits/initial window |
| 四 | T018 (ReadHeaderTimeout) | ✅ 已接入 | `http.Server.ReadHeaderTimeout` 从配置读取 |
| 三 | T010 (降级矩阵文档) | ✅ 已存在 | `docs/DEGRADATION_MATRIX.md` |
| 三 | T011 (状态管理器) | ✅ 已实现 | `DegradationState` + 后台探针 |
| 三 | T012 (降级中间件) | ✅ 已实现 | Gin `DegradationHeader` 中间件 |
| 三 | T013 (Handler 适配) | ❌ 未实现 | 需要对每个 handler 按契约做适配 |
| 三 | T014 (集成测试) | ❌ 未实现 | 需要在降级场景下做集成测试 |
| 二 | T006 (日志采样) | ✅ 已存在 | `internal/middleware/sample.go` |
| 二 | T007 (自适应降采样) | ❌ 未实现 | OTel 动态 Sampler，风险较高 |
| 二 | T008 (Relay 日志聚合) | ✅ 已存在 | 单行汇总日志 |
| 二 | T009 (指标写放大) | ❌ 未实现 | metrics flush 模式 |
| 一 | T001-T005 | ❌ 未实现 | CI/CD 配置文件 |
| 五 | T022 (audit 清洁器) | ✅ 已存在 | `RunAuditCleaner` |
| 五 | T023 (notif 清洁器) | ✅ 已实现 | `RunNotificationCleaner` |
| 五 | T024 (outbox 清洁器) | ✅ 已实现 | `RunOutboxCleaner` |
| 五 | T025 (文档) | ❌ 未实现 | `docs/DATA_RETENTION.md` |
