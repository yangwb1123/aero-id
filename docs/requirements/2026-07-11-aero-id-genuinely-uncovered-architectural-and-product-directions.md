# aero-id — 资深架构师/产品经理视角：五个代码级证据驱动的战略性扩展方向

> **日期:** 2026-07-11  
> **视角:** 资深架构师 + 产品经理  
> **方法:** 全量代码逐文件扫描 — `internal/` 全子包（~200 Go 源文件）、`migrations/` 23 份可逆 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`、`.github/workflows/ci.yml`。  
> **约束:** 不编写任何代码。每个方向提供精确的代码级证据、架构师与产品经理双视角论证。  
> **核心承诺:** 与 `docs/requirements/` 下 **116+ 份既有分析文档**做逐方向交叉比对，确保以下五个方向在既有材料中 **零系统性覆盖**。

---

## 前提：代码库当前状态

aero-id 是一个高度成熟的双传输数据服务。以下能力在 `internal/` 中均有可验证的实现：

| 域 | 已交付能力 |
|----|-----------|
| **基础设施** | HTTP (Gin) + gRPC 全对等（38+ RPCs）、SSE 实时推送、Webhook 事件分发、GORM + PG16、本地/S3 双存储 |
| **可靠性** | 事务性 Outbox + 死信 + 指数退避 + Webhook HMAC、幂等键 + 预占、速率限制（双传输）、SSO 断路器 + stale-cache + singleflight + mTLS + 证书锁定、存储 GC + 引用计数 |
| **可观测性** | OTel 追踪（含 SSO hop）、RED 指标（HTTP + gRPC + SSO + Outbox）、per-dependency `/readyz`、冷启动 warmup、优雅关停 |
| **产品能力** | 文件流式上传/去重/分享、1:1 私信 + Reactions + 已读回执、两层嵌套评论 + Like + 举报→审核、通知折叠 + 偏好 + SSE 推送、跨实体 FTS 搜索、方向性 Block 拦截、RBAC 管理、审计日志 |
| **数据模型** | 23 次可逆迁移（含性能索引、FTS、通知折叠、Outbox 重试、Storage Ref 引用计数、Admin 角色） |

在此坚实基础上，以下方向瞄准的是 **将 aero-id 从单实例可运维服务提升为可演化、可观察、可合约化交付的生产级平台** 所需的核心能力。每个方向都聚焦于**既有代码中缺失但关键的模式或机制**，而非简单的功能追加。

---

## 方向一：零停机数据库迁移策略（Zero-Downtime Schema Evolution）

### 问题陈述

代码库有 **23 个可逆迁移文件**，Go 迁移工具 `cmd/migrate` 完备，`auto_migrate` 可选。但**没有任何迁移策略能保证在生产环境零停机执行 schema 变更**。对于双副本（Kubernetes 滚动更新）或更大规模部署，直接执行 `ALTER TABLE ADD COLUMN NOT NULL` 或 `CREATE INDEX CONCURRENTLY` 以外的迁移几乎必然造成停机或数据不一致。

### 代码级证据

**证据 1：全部迁移都是直接 DDL，无 expand-contract 适配代码**

以 `internal/repository/db.go` 的 GORM 映射结构体为例：

```go
// internal/repository/db.go:149-162
type messageRow struct {
    ID        uuid.UUID         `gorm:"column:id;primaryKey"`
    FromUID   string            `gorm:"column:from_uid"`
    ToUID     string            `gorm:"column:to_uid"`
    Kind      int16             `gorm:"column:kind"`
    Content   string            `gorm:"column:content"`
    FileID    *uuid.UUID        `gorm:"column:file_id"`
    ReadAt    *time.Time        `gorm:"column:read_at"`
    EditedAt  *time.Time        `gorm:"column:edited_at"`
    Meta      datatypes.JSONMap `gorm:"column:meta"`
    CreatedAt time.Time         `gorm:"column:created_at"`
    DeletedAt gorm.DeletedAt    `gorm:"column:deleted_at;index"`
}
```

如果未来需要为 `Message` 添加 `conversation_id` 列（`UUID NOT NULL`），标准的 `ALTER TABLE ADD COLUMN conversation_id UUID NOT NULL` 会：
- 锁住整张表（PG 上写锁，但 `NOT NULL` 加默认值需要全表扫描）
- 旧版本代码（滚动更新中仍在运行的实例）不知道这个列，INSERT 不提供值 → 写入失败
- 必须分多步：先 `ADD COLUMN`（允许 NULL）→ 部署新代码（所有路径都写该列）→ `ALTER COLUMN SET NOT NULL`

**证据 2：所有索引迁移不是 `CONCURRENTLY` 创建的**

```sql
-- migrations/000015_perf_indexes.up.sql
CREATE INDEX IF NOT EXISTS idx_messages_from_to ON messages(from_uid, to_uid);
CREATE INDEX IF NOT EXISTS idx_notifications_recipient_read ON notifications(recipient_uid, (read_at IS NULL), created_at DESC);
```

在 PG 上，`CREATE INDEX` 默认获取 `ACCESS EXCLUSIVE` 锁，阻止所有读写。对于 1000 万行以上的表，这个锁持有时间可能达到分钟级 → 生产事故。

```sql
-- 应当改为 CONCURRENTLY：
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_from_to ON messages(from_uid, to_uid);
```

**证据 3：没有向后兼容的 read/write 过渡代码**

```go
// internal/repository/message.go — 以 ListInbox 为例
func (r *messageRepo) ListInbox(ctx context.Context, uid string, page domain.Page) ([]*domain.Message, string, error) {
    q := r.db.WithContext(ctx).
        Model(&messageRow{}).
        Where("deleted_at IS NULL").
        Where("(from_uid = ? OR to_uid = ?)", uid, uid).
        Order("created_at DESC")
    // ...
}
```

如果在滚动更新期间新增了 `conversation_id` 列，旧代码的 `messageRow` 结构体没有该字段，GORM 的 `.Model(&messageRow{})` 不会在 SELECT 中返回它——这不会报错。但如果新代码向旧表中 INSERT 时提供了该字段，而旧表的列还不存在（多步迁移的中间状态），就会失败。

**证据 4：`cmd/migrate/main.go` 无锁定/重入保护**

```go
// cmd/migrate/main.go
m := migrate.New(gdb, dir, log)
if err := m.Up(); err != nil { ... }
```

两个并发 `migrate-up` 调用会同时执行迁移——没有迁移锁（advisory lock 或 `schema_migrations` 表锁）。在 K8s 多副本环境中，所有 Pod 同时 `auto_migrate` 会竞态执行相同的 DDL。

### 产品经理视角

- **部署恐惧症**：每次数据库变更都意味着"我们要不要发公告？会不会有机？"——这直接拖慢迭代速度。
- **不可逆操作风险**：当前迁移虽然可逆（有 `.down.sql`），但大表上回滚一个 `ADD COLUMN NOT NULL` 需要重建整列，时间成本不可接受。
- **API 演进受限**：担心 schema 变更的部署风险，会导致团队规避有价值的模式变更（如群聊需要的 `conversation_id`）。

### 架构师建议

**Phase 1 — 迁移基础设施加固（P0，低工作量）**
1. 将全部 `CREATE INDEX` 改为 `CREATE INDEX CONCURRENTLY`（PG 要求不能在事务内执行，需要修改迁移引擎支持非事务执行）。
2. `cmd/migrate` 增加 advisory lock（`pg_advisory_lock`）防止并发迁移。
3. 为 `cmd/migrate` 增加 `--dry-run` 模式，输出将要执行的 SQL 但不执行。
4. CI 中增加迁移测试：对每个迁移执行 `up` → `down` → `up`，验证可逆性和幂等性。

**Phase 2 — 零停机迁移模式文档 + 代码模板（P1，中工作量）**
1. 定义 expand-contract 三步模式：
   - **Expand**：ADD COLUMN（允许 NULL）/ CREATE INDEX CONCURRENTLY / ADD CONSTRAINT（NOT VALID）
   - **Migrate**：部署新代码，双写新老列
   - **Contract**：ALTER COLUMN SET NOT NULL / VALIDATE CONSTRAINT / DROP OLD COLUMN
2. 在 `migrations/` 目录下增加 `TEMPLATE_expand.up.sql`、`TEMPLATE_contract.up.sql` 作为团队参考。
3. 在 `docs/` 中增加 `ZERO_DOWNTIME_MIGRATIONS.md` 记录完整流程。

---

## 方向二：API 合约化与版本化生命周期（API Contract & Versioning Lifecycle）

### 问题陈述

aero-id 有 HTTP REST（Gin）和 gRPC 两套传输，`api/proto/aero_id/v1/aero_id.proto` 是唯一的 API 合约。但整个代码库**没有任何 API 版本化策略、向后兼容性检查、弃用标记或自动合约导出机制**。随着服务演进，这会成为团队协作和外部集成的核心瓶颈。

### 代码级证据

**证据 1：Proto 文件无 `package` 版本管理外的任何版本化机制**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
package aero_id.v1;  // 仅有包名版本

service AeroID {
    rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
    // 当 SendMessageRequest 需要添加 from_conversation_id 时，
    // 必须保证向前兼容（新增 optional 字段），但 proto 中无文档说明兼容性承诺
}
```

没有任何字段标记为 `deprecated`，没有版本头（`x-api-version`），proto 文件内无版本策略文档。当前 `v1` 是唯一切面——从 `v1` 演进到 `v2` 的路径完全空白。

**证据 2：HTTP 路由版本号硬编码在路由注册中**

```go
// internal/handler/http/router.go:71-74
routes := Routes{
    Public: e.Group("", publicMW...),
    Auth:   e.Group("/v1", authMW...),  // 版本号是魔法字符串
}
```

要添加 `/v2/` 路由组需要复制整个路由注册逻辑。没有路由版本匹配/路由版本协商中间件。

**证据 3：Proto 生成的 pb.go 文件无版本兼容性校验**

```go
// internal/handler/grpc/aeropb/aero_id.pb.go — 这是自动生成的
// 没有任何工具在 CI 中检查新 proto 变更是否向后兼容
// buf breaking 或 protolint 未被使用
```

当前 CI 流程（`.github/workflows/ci.yml`）运行 `go vet`、`golangci-lint`、`go build`、`go test`——**没有 proto 向后兼容性检查**。

**证据 4：Response 结构体无 Deprecation 信息**

```go
// internal/handler/http/message.go:145-155
type messageResponse struct {
    ID        string         `json:"id"`
    FromUID   string         `json:"from_uid"`
    ToUID     string         `json:"to_uid"`
    Kind      int16          `json:"kind"`
    Content   string         `json:"content"`
    FileID    *string        `json:"file_id,omitempty"`
    ReadAt    *time.Time     `json:"read_at,omitempty"`
    EditedAt  *time.Time     `json:"edited_at,omitempty"`
    Meta      map[string]any `json:"meta,omitempty"`
    CreatedAt time.Time      `json:"created_at"`
}
```

如果要弃用 `Content` 并用 `ContentHTML` 替代，客户端无法通过 HTTP 头或响应体标记感知到"content 已弃用，请迁移到 content_html"。

### 产品经理视角

- **外部集成风险**：如果有第三方服务集成了 aero-id 的 API，一次"破坏性变更"（重命名字段、修改枚举值、改变必填性）会无声地破坏集成——而且直到集成方发现之前没人知道。
- **多客户端适配成本**：移动端 + Web + 第三方集成都需要对每个版本维护适配层。没有版本协商机制意味着所有客户端必须同时升级。
- **开发者体验**：新团队成员或外部贡献者无法快速了解 API 的稳定性和弃用状态。

### 架构师建议

**Phase 1 — Proto 合约校验（P0，低工作量）**
1. CI 集成 `buf breaking`（`buf.build`）或 `protolint` 检查 proto 变更的向后兼容性。
2. 在 proto 中为所有字段添加 `deprecated` 注释规划。
3. 维护 `api/proto/ breaking-changes.txt` 日志记录每次破坏性变更。

**Phase 2 — HTTP 版本协商 + 弃用头（P1，中工作量）**
1. 路由组支持通过 `Accept-Version` 头或 URL 前缀（`/v1/`、`/v2/`）选择版本。
2. 中间件注入 `Deprecation: true` 和 `Sunset: <date>` HTTP 头到已弃用 API 的响应中。
3. 响应中增加 `X-API-Version` 头。

**Phase 3 — 客户端 SDK 生成（P2，中-高工作量）**
1. 从 proto 自动生成 Go / TypeScript / Python 客户端 SDK。
2. SDK 内置重试、超时、错误解析、版本协商。
3. 在 `Makefile` 中增加 `make gen-sdk` 目标。

---

## 方向三：异步操作的可观测性断裂（Observability Gap in Async Paths）

### 问题陈述

OpenTelemetry 追踪已实现并覆盖 HTTP 和 gRPC 同步请求路径（含 SSO gRPC hop），但所有**后台异步操作**（outbox relay、storage GC sweep、idempotency key cleanup、SSE hub 推送、SSO token/user cache warmup）都在 `context.Background()` 中运行，完全没有追踪上下文。这意味着：
- 一个 outbox 事件处理失败——没有追踪跨度可以查看
- 存储 GC 在清理 blob 时出错——没有追踪跨度可以定位
- 用户投诉"我收到了重复通知"——无法通过追踪回溯事件处理链

异步路径的失败在生产环境中是静默的、不可观察的。

### 代码级证据

**证据 1：Outbox relay 使用 `context.Background()` 作为根上下文**

```go
// internal/outbox/relay.go:145-153
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()  // ← 无追踪上下文！
        for {
            select {
            case <-t.C:
                r.drain(ctx)
```

`drain` 中处理每个事件时，`handler.Handle(ctx, e)` 传入的 `ctx` 是 `context.Background()`——没有父 span、没有 trace ID。这意味着：

```go
// internal/notification/dispatcher.go（通过 outbox handler chain 调用）
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // 此处的 ctx 是 context.Background()，没有追踪上下文
    // 所有内部操作（DB 写入通知、block 检查、SSE push）都不会产生 trace span
    ...
    if d.pusher != nil {
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload))  // SSE 推送无追踪
    }
    return nil
}
```

**证据 2：Storage GC sweeper 同样使用 `context.Background()`**

```go
// internal/storage/gc/sweeper.go:226-233
func (s *Sweeper) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()  // ← 无追踪上下文！
        for {
            select {
            case <-t.C:
                _, _ = s.Run(ctx)
```

**证据 3：Idempotency key cleaner 和 SSO cache cleaner 也是同样模式**

```go
// internal/idempotency/store.go:151-163
func (s *GormStore) RunCleaner(interval time.Duration) (stop func()) {
    // ...
    go func() {
        for {
            select {
            case <-t.C:
                s.purgeExpired()  // 完全无上下文
```

```go
// internal/sso/token_cache.go — RunCleaner 同样使用 context.Background()
// internal/sso/user_cache.go — RunCleaner 同样情况
```

**证据 4：SSO 客户端的 singleflight 和 stale-cache 回填路径也无追踪**

```go
// internal/sso/client.go — ValidateToken
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
    // ctx 来自 HTTP/gRPC handler，有父 span
    // 但如果走 stale-cache 回填（后台 goroutine）：
    if c.cfg.StaleTTL > 0 && entry != nil && time.Since(entry.CreatedAt) > c.cfg.TokenCacheTTL {
        go c.refreshToken(context.Background(), token, entry.UID)  // ← 后台刷新无追踪
    }
}
```

**证据 5：App 启动时的 warmup 路径**

```go
// internal/app/app.go:233-249
func (a *App) warmup(ctx context.Context) {
    go func() {
        warnCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        var n int
        if err := a.sqlDB.QueryRowContext(warnCtx, "SELECT 1").Scan(&n); err != nil {
            a.log.Warn("warmup: db ping failed", zap.Error(err))
        }
        cancel()
    }()
}
```

虽然 `warmup` 接收了 `ctx`（从 `Run` 传入），但 goroutine 内部的操作没有创建子 span，所以 warmup 阶段的 DB ping 不在任何追踪中。

### 产品经理视角

- **事故排查效率**：当用户说"10 分钟前我收到了一条消息的通知，但点击后发现消息不存在"——同步路径的追踪只能看到用户点击，看不到 outbox 事件创建和分发的异步路径。排查这类问题只能靠日志 grep，耗时 10× 以上。
- **SLO 测量残缺**：当前只能测量同步请求的延迟和错误率。outbox 事件分发延迟（从写入到通知到达 SSE）完全不可测量。这意味着"用户从发送消息到收到通知的端到端延迟"没有 SLO。
- **告警盲区**：outbox 死信累积、GC 清理失败、SSO 缓存刷新失败——这些都是静默错误，当前无告警。

### 架构师建议

**Phase 1 — 异步起点注入追踪上下文（P0，低工作量）**
1. 为每个后台循环（outbox relay、sweeper、cleaner）创建命名 tracer provider 的根 span，标记为 `kind=INTERNAL`。
2. 将所有后台循环的 `context.Background()` 替换为带根 span 的 `context.Context`。
3. 在 Start/stop 生命周期中创建/结束根 span，使 trace 包含完整的循环生命周期。

**Phase 2 — 每个事件/任务一个子 span（P1，中工作量）**
1. Outbox relay：`drain` 是一个 span，每 `handler.Handle` 是一个子 span。
2. GC sweeper：`purgeSoftDeleted` 和 `sweepOrphans` 各为子 span，每批 500 行一个子 span。
3. cache refresh：后台刷新 goroutine 创建独立的追踪 span。

**Phase 3 — 异步 KPI 导出到指标（P1，低-中工作量）**
1. Outbox 事件处理延迟：从事件 CreatedAt 到 PublishedAt 的延迟，作为 gauge 或 histogram 导出。
2. 通知端到端延迟：从 outbox 事件创建到 SSE 客户端接收的延迟（SSE 事件 ID 包含 notification CreatedAt）。
3. GC pass 的 rows purged / orphans swept 作为 gauge 导出。

---

## 方向四：配置热重载与特性开关（Dynamic Configuration & Feature Flags）

### 问题陈述

aero-id 的所有配置（`config.yaml` + 环境变量覆盖）在**启动时一次性加载**，任何配置变更都需要**完全重启服务**。对于生产运维，这带来几个具体问题：
- 需要临时调高日志级别排查问题 → 必须重启 → 丢失当前连接（SSE）和缓存
- 需要临时关闭 rate limit 应对突发流量 → 必须重启
- 需要动态切换存储后端（local → S3 诊断）→ 必须重启
- 新功能通过代码上线后无法渐进式灰度（feature flag） → 全量发布风险高

### 代码级证据

**证据 1：`config.Load` 在 `main.go` 中只调用一次，此后所有配置不可变**

```go
// cmd/server/main.go
cfg, err := config.Load(cfgPath)
// ...

app, err := app.New(ctx, cfg, log)
// ...
```

`app.New` 接收 `*Config` 指针，但所有子组件（service、repository、sso client）都在构造函数中读取并持有配置值的副本：

```go
// internal/sso/client.go — NewClient
func NewClient(ctx context.Context, endpoint string, tlsCfg TLSConfig, cfg ClientConfig) (*Client, error) {
    // cfg.TokenCacheTTL 在构造时被读取并硬编码到 TokenCache 中
    // 后续无法动态修改
}
```

**证据 2：日志级别在启动时固定**

```go
// internal/pkg/logger/logger.go
func New(cfg LogConfig) (*zap.Logger, error) {
    // cfg.Level 在 New 时确定
    lvl := zap.NewAtomicLevel()
    if err := lvl.UnmarshalText([]byte(cfg.Level)); err != nil {
        return nil, err
    }
    // ...
}
```

虽然 `zap.AtomicLevel` 本身支持运行时修改（`lvl.SetLevel`），但当前没有任何 API 或信号处理程序利用它。运维人员无法动态提升日志级别。

**证据 3：Rate limit 配置加固死——不支持动态调整**

```go
// internal/app/app.go
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
```

`ratelimit.Limiter` 在构造时确定了 RPS 和 Burst。突发流量场景下，运维人员无法通过 API 或配置中心临时调高限流阈值——只能重启。

**证据 4：无特性开关（Feature Flag）基础设施**

所有服务行为都是"一次发布，全球生效"。没有以下能力：
- 按 uid 灰度（"给内部测试人员先开启群聊功能"）
- 按比例灰度（"5% 用户看到新版通知 UI"）
- 按租户隔离（SaaS 场景下不同的客户有不同的功能开关）
- Kill switch（"发现群聊有 bug，立刻全局关闭"而不需要回滚代码）

### 产品经理视角

- **发布风险**：每个新功能上线都是"大爆炸"模式——要么全开，要么全不开。无法做 A/B 测试、无法渐进式灰度、无法在发现问题时快速关闭。
- **运维操作成本**：修改日志级别需要审批变更、执行重启、观察启动日志确认生效——一个本应 10 秒的操作变成 30 分钟流程。
- **事故响应延迟**：SSO 出现问题时想临时关闭 rate limit 让更多请求穿透——但需要重启，会断掉所有现有 SSE 连接，进一步加剧问题。

### 架构师建议

**Phase 1 — 可动态重载的配置值（P0，低-中工作量）**
1. 将 `config.Load` 返回的 `*Config` 包装为原子指针（`atomic.Pointer[Config]`），支持运行时替换。
2. 关键配置项（`Log.Level`、`RateLimit.RPS`、`RateLimit.Burst`）单独包装为原子值或 channel-driven 更新。
3. 注册 `SIGHUP` 信号处理程序：收到 `SIGHUP` 时重新读取配置并原子替换。
4. 可选：增加 `POST /-/reload` 端点（仅本地回环或 admin 权限）触发配置重载。

**Phase 2 — 日志级别运行时修改（P1，低工作量）**
1. 将 logger 的 `AtomicLevel` 指针暴露给 admin handler。通过 `PUT /v1/admin/log-level` 动态修改。
2. 修改 Persist：新的日志级别持久化到 DB 或仅运行时生效（按需）。

**Phase 3 — 特性开关基础设施（P2，中-高工作量）**
1. 新增 `feature_flags` 表（`flag_name`、`enabled`、`uid_filter`、`percentage`、`updated_at`）。
2. 新增 `internal/featureflag/` 包，提供 `IsEnabled(ctx, flagName) bool` 接口。
3. 所有新功能开关检查使用 `featureflag.IsEnabled(ctx, "group_chat")` 而不是硬编码。
4. Admin API：`GET/PUT /v1/admin/feature-flags/:name`。

---

## 方向五：客户端 SDK 缺失与 API 消费者生态断裂（Client SDK & Consumer Ecosystem Gap）

### 问题陈述

aero-id 有清晰的服务端架构、完备的 proto 定义、双传输 API 和完整的测试覆盖。但是**没有任何面向 API 消费者的客户端 SDK、API 文档站点、或 consumer-driven contract tests**。这意味着：
- 每个集成了 aero-id 的服务/客户端都自己写 HTTP 调用 → 错误处理、重试逻辑、认证方式各自实现
- Proto 是唯一 API 合约，但外部团队无法或不愿使用 gRPC → 退化到 HTTP → 失去合约保障
- 没有 OpenAPI/Swagger 规范 → 无法自动生成文档、Mock 服务器、API 测试
- 没有 API changelog → 消费者无法跟踪 API 变化

### 代码级证据

**证据 1：Makefile 和 CI 中没有客户端代码生成**

```makefile
# Makefile — 只有 proto 生成服务端代码
proto:
    mkdir -p internal/sso/ssopb internal/handler/grpc/aeropb
    protoc \
      --proto_path=api/proto \
      --go_out=. --go_opt=module=github.com/aero/aero-id \
      --go-grpc_out=. --go-grpc_opt=module=github.com/aero/aero-id \
      api/proto/sso/v1/sso.proto \
      api/proto/aero_id/v1/aero_id.proto
```

没有 `--go_out=...` 指向外部 client 目录，没有 `--openapiv2_out=...`，没有 `buf generate` 配置。

**证据 2：proto 文件中没有 gRPC HTTP 注解用于生成 REST/OpenAPI**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
service AeroID {
    rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
    // 没有 google.api.http 注解：
    //   option (google.api.http) = {
    //     post: "/v1/messages"
    //     body: "*"
    //   };
}
```

这意味着即使有人想从 proto 生成 OpenAPI 规范（通过 `protoc-gen-openapiv2`），也无法得到正确的 HTTP 路由映射——这些映射隐式藏在 `internal/handler/http/message.go` 的路由注册中。

**证据 3：HTTP handler 的请求/响应类型是私有的，无法自动导出为合约**

```go
// internal/handler/http/message.go — 请求体类型包内私有
type sendMessageRequest struct {
    ToUID   string         `json:"to_uid"`
    Kind    int16          `json:"kind"`
    Content string         `json:"content"`
    FileID  string         `json:"file_id"`
    Meta    map[string]any `json:"meta"`
}
```

`sendMessageRequest` 是未导出的，`messageResponse` 也是未导出的——没有中央"API 规范"用于为外部消费者生成文档。

**证据 4：没有消费者驱动的契约测试**

```go
// internal/handler/grpc/parity_test.go — 现有测试覆盖 HTTP↔gRPC 对等性
// 但没有任何测试覆盖"如果客户端按 proto 格式发送请求，服务端是否能正确响应"
// 更没有"如果服务端做了一个破坏性变更，客户端会怎样"的契约测试
```

### 产品经理视角

- **集成门槛高**：想与 aero-id 集成的团队需要阅读 Go 代码理解 API 签名，然后自己实现 HTTP 客户端。没有 SDK，集成时间从"几小时"变成"几天"。
- **文档缺失**：没有 API 参考文档，没有交互式 playground（Swagger UI），没有 Postman 集合。开发者体验远低于行业标准。
- **合作伙伴集成障碍**：如果 aero-id 要开放给第三方合作伙伴（Webhook 已有 + API 集成），没有 SDK 和 OAuth 流程是商业化障碍。
- **每次 API 变更都需要手动通知消费者**：没有 changelog、没有版本化、没有破坏性变更提醒——集成方只能通过"生产突然出问题"来发现 API 变了。

### 架构师建议

**Phase 1 — 从 Proto 生成 OpenAPI 规范（P0，低工作量）**
1. 在 proto 中添加 `google.api.http` 注解，将每个 gRPC RPC 映射到对应的 HTTP 路由和方法。
2. 集成 `buf` 工具链，配置 `buf.gen.yaml` 同时生成 Go 服务端桩 + OpenAPI v3 规范 + gRPC 客户端。
3. CI 中验证生成的 OpenAPI 规范是否与 proto 一致。
4. 将生成的 OpenAPI 规范发布到内部文档站点或 S3。

**Phase 2 — 生成官方 Go 客户端 SDK（P1，中工作量）**
1. 使用 `buf generate` 从 proto 生成独立的 `aeroid-go-client` Go module。
2. SDK 封装：认证头注入、重试策略、错误类型转换、分页助手、流式 SSE decoding。
3. 将 SDK publish 到内部 Go module 仓库或 GitHub。
4. 写一个 README 示例：10 行代码完成"发送消息 + 监听通知"的完整流程。

**Phase 3 — TypeScript / Python SDK + 消费者契约测试（P2，高工作量）**
1. 从 OpenAPI 规范生成 TypeScript（`openapi-typescript`）或 Python（`openapi-generator`）客户端。
2. SDK 自动处理：令牌刷新、重试、请求跟踪（注入 `X-Request-ID` 和 trace context）。
3. 消费者契约测试（Pact 或 Spring Cloud Contract）：模拟消费者期望，CI 中验证服务端未破坏合约。
4. 建立 API Changelog：每个版本记录新增/弃用/移除的 API，通过 CI 自动生成。

---

## 总结：优先级与依赖关系

| # | 方向 | 类型 | 优先级 | 工作量 | 为什么现在做 |
|---|------|------|--------|--------|------------|
| 1 | 零停机迁移策略 | 运维/正确性 | **P0** | 中 | 阻止团队安全地演进 schema；群聊等新功能因担心迁移风险而推迟 |
| 2 | API 合约化与版本化 | 架构/合约 | **P1** | 中 | 外部集成和 API 演进的前提；越早建立规范，迁移成本越低 |
| 3 | 异步可观测性 | 可观测性 | **P1** | 低-中 | 异步路径是当前观测盲区；排查生产问题效率低；告警不完整 |
| 4 | 配置热重载与特性开关 | 运维/灵活性 | **P2** | 中-高 | 显著改善运维体验，但功能开关价值需要 FE 配合才能发挥 |
| 5 | 客户端 SDK 生态 | 开发者体验 | **P2** | 中-高 | 提高集成效率，但属于"有更好，没有也能活"的范畴 |

### 跨方向关系

- **方向一（迁移）** 和 **方向二（API 合约）** 共同构成服务可演化的基础：一个管理数据库层面、一个管理 API 层面。
- **方向三（异步观测）** 不依赖其他方向，且与方向二的 API Changelog 配合可以覆盖"API 变更 → 异步处理失败 → 追踪发现"的闭环。
- **方向四（配置热重载）** 是实现方向二（API 版本协商）中"动态启用/禁用实验性 API"的底层支持。
- **方向五（SDK）** 依赖方向二的 proto 注解和 OpenAPI 产出，建议在方向二 Phase 2 之后开始。

### 建议执行顺序

```
迭代 1: 方向三 Phase 1（异步注入追踪）+ 方向四 Phase 1（SIGHUP 重载 + 日志级别热切换）
迭代 2: 方向一 Phase 1（CONCURRENTLY + 迁移锁 + dry-run）
迭代 3: 方向二 Phase 1（buf breaking + proto deprecation）+ Phase 2（HTTP 版本头）
迭代 4: 方向三 Phase 2+3（每个任务一 span + 异步 KPI）
迭代 5: 方向五 Phase 1（OpenAPI 从 proto 生成）
迭代 6: 方向四 Phase 3（特性开关）+ 方向五 Phase 2（Go SDK）
迭代 7: 方向一 Phase 2（expand-contract 模式文档）
```
