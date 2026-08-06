# aero-id — 五条高价值扩展方向（全局代码扫描 + 产品视角）

> 审核日期：2026-07-11  
> 审核范围：`main` 分支全量源代码（internal/ 全部 Go 文件、22 个迁移、proto、config、部署文件）  
> 方法：逐文件阅读全部非测试源文件，识别架构中存在的**抽象缺口**、**产品空白**和**可扩展的设计 seam**  
> 前提：本分析面向已经成熟的生产级服务，聚焦于下一阶段的架构演进

---

## 当前能力基线

经过多轮迭代，aero-id 已具备以下成熟能力（供上下文参考，非重点）：

| 层 | 已交付 |
|----|--------|
| 数据域 | 文件 CRUD+共享+版本去重+预签名直传+GC 回收；消息 1:1+reactions+已读回执+编辑删除；评论两层级嵌套+点赞+举报审核+隐藏；全文搜索（FTS） |
| 可靠写路径 | 事务性 outbox、幂等键、原子引用计数、审计日志、软删除 |
| 通知 | Outbox 驱动异步通知、SSE 实时推送、偏好设置（开关/免打扰/时区）、折叠、速率抑制 |
| 安全 | 方向性黑名单、速率限制（per-actor/per-IP）、断路器（serve-stale、singleflight、重试+退避）、TLS/mTLS+证书固定 |
| 可观测 | RED 指标、OTel 追踪（gRPC handler + SSO 跳转）、per-dependency readyz、outbox/SSO 健康仪表 |
| 运营 | 迁移 CLI + 自动迁移、角色权限（admin/moderator）、审计日志查询、优雅关闭 |

> **核心判断**：aero-id 在"做正确的事"上基本完整。下面的方向聚焦于**从"功能完整"到"平台成熟"的跨越**。

---

## 方向一：文件协作工作流（File Collaboration Workflows）

### 为什么需要

当前文件共享模型是**静态且单向的**：Owner 授予 Grantee 读取权限，然后就没有后续了。代码中文件共享的完整链路是：

```
internal/file/service_share.go  →  Grant(ctx, fileID, ownerUID, granteeUIDs)
internal/file/repository_share.go →  INSERT INTO file_shares
internal/domain/audit.go          →  AuditFileShareGrant / AuditFileShareRevoke
internal/handler/http/file.go     →  POST /v1/files/:id/share
```

但共享之后的能力完全空白：

| 能力 | 代码证据 | 现状 |
|------|----------|------|
| 共享权限粒度 | `file_shares` 表只有 `(file_id, grantee_uid)` | 仅有读权限，没有写/管理/评论权限分化 |
| 共享通知 | 没有 `file.shared` outbox 事件 | Grantee 不会收到"有人给你分享了文件"通知 |
| 共享过期 | `file_shares` 没有 `expires_at` 列 | 共享永久有效，无法设置临时访问 |
| 共享链路追踪 | `file.ShareFile` 审计日志仅记录授予操作 | 无法查询"此文件当前共享给谁"的管理端点 |
| 请求共享 | 没有从 Grantee 发起的共享请求流程 | 用户无法"申请访问"一个私有文件 |
| 评论/审阅 | 没有 file-scoped 审阅线程 | Granttee 看到文件但无法留下审阅意见 |

### 架构影响

**中等**。需要在 `file_shares` 表上增加权限/过期列，新增 `share_requests` 表，扩展 outbox 事件类型，但所有基础设施（通知、审计、事件）已就位。

### 建议分批

```
Batch A: 共享权限分化 + 过期共享 + 共享通知
Batch B: 共享请求流程 + 文件级审阅线程
```

---

## 方向二：资源关系图与引用完整性治理（Resource Relationship Graph & Referential Integrity）

### 为什么需要

当前代码库中，资源之间的关系是**弱引用且脆弱的**。以下是扫描发现的引用完整性问题：

**1. 评论指向已删除文件/消息**

```go
// internal/comment/service.go - assertTargetReadable()
case domain.TargetTypeFile:
    f, err := s.files.Get(ctx, target)     // 文件可能已被软删除
    if stderrors.Is(err, domain.ErrNotFound) {
        return apperr.NotFound(...)         // 评论变成孤儿
    }
```

评论创建后，如果目标文件被删除，评论变成**幽灵数据**——存在但无法访问（返回 404）。没有任何迁移或级联策略来处理这种情况。

**2. 消息附件孤立**

```go
// internal/domain/message.go
type Message struct {
    FileID    *ID        // 指向一个文件的弱引用
}
```

消息可以附加文件（`file_id`），但：
- 没有 `ON DELETE SET NULL` 外键约束（message 表没有 FK）
- 文件删除后，消息中的 `FileID` 变成死引用
- 没有 GC 扫描 orphaned message-file 引用的机制

**3. 评论举报与评论生命周期脱节**

```go
// internal/comment/service.go - ResolveReport()
if status == domain.ReportStatusResolved && c.Status == domain.CommentStatusNormal {
    if err := s.comments.SetStatus(ctx, rep.CommentID, domain.CommentStatusHidden); err != nil {
```

评论被 resolve 后自动隐藏。但如果评论又被作者删除（`SoftDelete`），隐藏状态与删除状态冲突——当前没有转换规则（deleted > hidden > normal）。

**4. 缺乏跨资源查询能力**

```go
// internal/domain/file.go - File has no relationship to comments or messages
type File struct {
    ID          ID
    OwnerUID    string
    // no Comments, no Messages, no Shares
}
```

当前无法查询"一个文件上的所有评论"（通过 `target_type + target_id` 查询属于业务逻辑层的拼凑），也没有统一的关系图查询接口。

### 架构影响

**低-中**。不需要大规模重构，而需要：

1. **定义一个级联策略枚举**（`CascadePolicy: None | SoftDelete | HardDelete | Orphan`），在 service 层执行
2. **新增 `resource_graph` 视图或轻量表**，以统一方式查询目标→依赖资源
3. **评论/附件 GC 扩展**：在 `storage/gc/sweeper.go` 中添加 `resourceGC` 阶段，清理孤立的跨域引用
4. **事件增强**：文件删除时发送 `file.deleted` outbox 事件，下游 handler 清理或标记依赖资源

### 边界情况

- **循环引用**：理论上不会出现（comment → file, message → file），但需防御性设计
- **宽限期**：文件删除后，其上的评论不应立即消失——可以先标记 "target deleted"，保留 30 天

---

## 方向三：存储配额引擎与用量计量（Storage Quota Engine & Usage Metering）

### 为什么需要

aero-id 是一个以文件为核心资产的服务，但**没有任何存储配额或用量管理**。以下是代码证据：

```go
// internal/file/service_crud.go - Create()
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    // 检查 MIME 类型、大小上限——但从不检查 owner 的已用存储量
    // 没有对 s.maxBytes 的 per-user 配额检查
    // 没有 usage_metrics 表的更新
}
```

```go
// internal/config/config.go - StorageConfig
type StorageConfig struct {
    MaxUploadBytes   int64    // 单文件上限 — 但这是 global 的，不是 per-user 的
    AllowedMimeTypes []string // MIME 白名单
    // 没有 PerUserQuota, 没有 TieredLimits, 没有 UsageAlerts
}
```

```go
// internal/file/repository.go - Repository interface
type Repository interface {
    ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*File, string, error)
    // 没有 CountByOwner, 没有 SumStorageByOwner, 没有 QuotaUsage
}
```

缺少的配额能力：

| 能力 | 缺失证据 | 影响 |
|------|----------|------|
| Per-user 存储限额 | 无 `user_quotas` 表或等效逻辑 | 恶意用户可以无限上传，成本不可控 |
| 用量统计 API | 无 `GET /v1/quota` 端点 | 用户无法查看已用/总限额 |
| 按文件大小分级的存储策略 | 无 `storage_tiers` 概念 | 大文件（>100MB）与小文件存储在同一后端，成本结构不优化 |
| 用量告警 | 无接近限额时的通知 | 用户直到上传被拒（max upload bytes 限制）才知道超限 |
| 临时/访客用户限额 | 所有用户共享全局 `MaxUploadBytes` | 无法对不同角色设置不同策略 |

### 架构影响

**中等。** 需要：

1. **新增 `user_usages` 或 `user_quotas` 表**（uid, storage_bytes, file_count, quota_bytes, updated_at）
2. **写入路径增强**：在 `file.Create()` / `file.CompleteUpload()` 中检查并扣减配额；软删除时释放配额
3. **后台定期对账**：与 `storage.GC` 类似的后台 job，定期扫描实际用量与记录用量是否一致（应对并发竞态或数据不一致）
4. **配置扩展**：`storage.quotas` 配置块，支持 default_quota、per-tier 覆盖
5. **API 端点**：`GET /v1/quota`（当前用量）、`POST /v1/admin/users/:uid/quota`（管理员调整）

### 设计原则

- **宽松扣减**（eventual consistency）：允许短暂超限，后台对账后软限制
- **拒绝写入**（hard limit）：`storage_bytes > quota_bytes` 时，`Create` 返回 `QuotaExceeded` 错误
- **共享文件不算 Grantee 配额**：只计入 Owner 的配额（与当前归属模型一致）

---

## 方向四：平台级 API 管理体系（Platform-grade API Management）

### 为什么需要

目前 aero-id 的 API 面向**直接客户端应用**（第一方前端/移动端）。但作为一个管理用户数据的通用服务，它的架构天然适合作为**内部微服务平台的基座**。以下缺口阻碍了平台化：

**1. 没有应用/客户端注册**

```go
// internal/middleware/auth.go - AuthInterceptor()
func AuthInterceptor(v TokenValidator) grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
        token := bearerFromMD(ctx)
        uid, err := v.ValidateToken(ctx, token)
        // 只验证了 uid，没有"此 token 对应的客户端应用是谁"的概念
```

认证只解析出 uid，没有客户端上下文。这意味着：
- 无法区分"用户通过第一方 App"还是"用户通过第三方 API key"发起的操作
- 无法按客户端应用设置不同的速率限制或配额
- 没有 API key 管理体系

**2. 速率限制是全局的**

```go
// internal/middleware/ratelimit.go
// limiter 是 per-actor uid 的，所有请求共享相同的 RPS/burst
type Limiter struct {
    // 没有 per-client, per-endpoint, per-plan 的分层限制
}
```

一个客户端占用全部额度会影响其他客户端的正常请求。

**3. 没有 API 访问日志（超越审计日志的粒度）**

```go
// migrations/000014_create_audit_log.up.sql
// audit_events 记录敏感操作（文件共享、举报处理等）
// 但不记录普通 API 调用（GET /files, GET /messages 等）
```

对于平台运营，需要每个 API 调用的完整访问日志（谁、什么客户端、哪个端点、延迟、状态码）用于计费、分析和故障排查。

**4. 没有 Webhook 管理 API**

```go
// internal/config/config.go - WebhookConfig
type WebhookConfig struct {
    URL        string   `mapstructure:"url"`         // 只有一个全局 webhook
    SigningKey string   `mapstructure:"signing_key"`
    // 没有 per-user 的 webhook，没有 webhook 管理 API
    // 没有事件类型选择（per-webhook）
}
```

Webhook 是全局配置的，不能由用户自行管理。平台化时需要支持：
- 用户创建自己的 webhook（`POST /v1/webhooks`）
- 选择监听的事件类型（`event_types: ["message.received", "comment.replied"]`）
- webhook 投递日志和重试管理

### 架构影响

**中高。** 但可以渐进实现：

```
Phase 1: API Key 管理 + 客户端注册
  ├── api_keys 表 (key_hash, client_name, uid, permissions, quota, expires_at)
  ├── 认证中间件扩展：支持 Bearer token 或 X-API-Key
  └── 审计日志扩展：增加 client_id 字段

Phase 2: 分层速率限制
  ├── 第一层：全局（当前）
  ├── 第二层：per-client（基于 api_key）
  └── 第三层：per-endpoint（读写分离限制）

Phase 3: 用户自管理 Webhook
  ├── webhook_subscriptions 表
  ├── CRUD API + 投递日志 API
  └── 事件类型注册表（event_registry）
```

### 代码影响

最小。认证中间件是现有的 seam（`internal/middleware/auth.go`），只需新增一个 `APIKeyValidator` 实现 `TokenValidator` 接口。速率限制已有接口抽象。

---

## 方向五：结构化数据生命周期策略（Structured Data Lifecycle Policy）

### 为什么需要

当前的数据生命周期是**随意且不一致的**：

**1. 软删除策略不统一**

```go
// 文件：internal/file/service_crud.go
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // 软删除 + storage ref 计数减一
    // GC 在 retention 窗口后硬删除
}

// 消息：internal/message/message.go
func (s *messageService) Delete(ctx context.Context, id domain.ID) error {
    // 软删除 — 但 GC 从来不清理软删除的消息！
}

// 评论：internal/comment/service.go
func (s *commentService) Delete(ctx context.Context, id domain.ID) error {
    // 软删除 — 同样没有 GC 清理！
    // 回复计数需要递减但当前是原子操作
}
```

消息和评论没有对应的 GC/purge 机制。它们被软删除后永久保留在数据库中。

**2. 没有数据保留策略**

```go
// migrations/000015_perf_indexes.up.sql  — 只添加了性能索引
// migrations/000017_notification_indexes.up.sql — 通知索引
// 没有任何 TTL 或自动清理策略
```

- 通知：阅读后保留多久？30 天后是否自动清理？
- 审计日志：是否需要保留策略（如 GDPR 要求仅保留 90 天）？
- 会话/一次性的操作（幂等键）：`idempotency_keys` 由 `RunCleaner` 清理，但其他表没有对应逻辑

**3. 没有冷热数据分离**

所有表在同一个 Postgres 实例中，没有：
- 按创建时间分区（`created_at` 分区表）
- 旧数据自动迁移到归档表或列式存储
- 通知/审计日志的分区保留窗口

### 架构影响

**中。** 需要统一的声明式策略框架：

```go
// 设想的数据生命周期策略
type LifecyclePolicy struct {
    Table       string        // "notifications" | "audit_events" | "messages" | "comments"
    SoftDelete  time.Duration // 软删除后保留多久
    HardDelete  time.Duration // 永久删除前保留多久
    Archive     time.Duration // 多久后迁移到归档存储
    PartitionBy string        // "created_at" — 支持按月/按周分区
}
```

### 建议实现路径

1. **审计日志保留**：添加 `audit_events` 分区（按月）和自动清理（`deleted_at IS NOT NULL AND created_at < now() - 90 days`）
2. **通知 GC**：扩展 `storage/gc/sweeper.go` 为通用的 `gc.Cleaner` 接口，通知、消息、评论各有一个 cleaner 实现
3. **幂等键清理增强**：当前 `idemStore.RunCleaner(time.Hour)` 每小时清理，但 TTL 应从配置读取而不是写死在 wiring 中

### 边界情况

- **软删除恢复窗口**：用户希望"撤销删除"的能力。消息和评论的软删除没有像文件一样的 retention 窗口。如果引入 GC，需要确保有合理的恢复期（如 30 天）。
- **法律保留（Legal Hold）**：在诉讼或合规调查期间，某些用户的数据必须**豁免于自动删除策略**。需要 `legal_holds` 表（uid, reason, created_by, expires_at），GC 扫描时跳过受保护的用户。

---

## 综合优先级建议

| # | 方向 | 产品价值 | 架构价值 | 实现成本 | 建议时机 |
|---|------|----------|----------|----------|----------|
| 1 | **文件协作工作流** | ⭐⭐⭐⭐ | ⭐⭐⭐ | 中 | P1 — 产品差异化 |
| 2 | **资源关系图与引用完整性** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 低-中 | P0 — 数据正确性基石 |
| 3 | **存储配额与用量计量** | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 中 | P1 — 成本控制前提 |
| 4 | **平台级 API 管理** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 高 | P2 — 平台化最后一公里 |
| 5 | **数据生命周期策略** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 中 | P1 — 运维成熟度标志 |

### 依赖关系

```
方向 2 (引用完整性) ← 方向 5 (生命周期) 依赖其级联策略
方向 3 (配额) ← 方向 4 (API 管理) 可独立实现
方向 1 (协作) ← 方向 2 (引用完整性) 依赖其关系模型
```

### 推荐执行顺序

```
Batch A: 引用完整性治理 (方向 2) + 生命周期策略 (方向 5) — 数据正确性 + 运维成熟度
Batch B: 存储配额 (方向 3) — 成本可见性
Batch C: 协作工作流 (方向 1) — 产品体验
Batch D: API 管理 (方向 4) — 平台化
```

---

## 与现有能力的关联

| 现有关键组件 | 方向一 | 方向二 | 方向三 | 方向四 | 方向五 |
|--------------|--------|--------|--------|--------|--------|
| Outbox 事件 | 新增 `file.shared` 事件 | 新增 `file.deleted` 级联事件 | 用于对账检查 | 用于 webhook 事件 | 清理通知 |
| 审计日志 | 记录共享变更 | 记录级联操作 | 记录配额变更 | 记录 API 调用 | 审计自身保留 |
| 通知系统 | 共享通知 | 删除通知 | 用量告警 | — | 生命周期通知 |
| 幂等键 | 共享请求幂等 | 级联操作幂等 | 配额扣减幂等 | API 调用幂等 | — |
| 事务性 outbox | 共享通知异步化 | 级联删除异步化 | — | webhook 异步投递 | 清理任务异步化 |
| 存储 GC | 引用计数兼容 | 资源级联 GC | 对账触发 | — | 表级清理 |
| 角色权限 | 审阅权限 | — | 管理员调整配额 | API key 权限 | legal hold 豁免 |
