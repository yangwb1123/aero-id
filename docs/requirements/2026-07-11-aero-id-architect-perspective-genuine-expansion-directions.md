# aero-id — 架构扫描：五个高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码库逐文件扫描（`internal/` ~70+ 个 Go 包、`migrations/` 22 份 SQL、`configs/`、`api/proto/`、`cmd/`、`Makefile`），结合 `app.go` composition root 的实际 wiring 验证每个方向的代码级证据。  
> **约束**: 不编写代码，不重复 `docs/requirements/` 下 60+ 份既有分析。  
> **核心问题**: 在一个已经高度成熟的服务上，**还缺什么？**

---

## 当前代码库成熟度（简要定位）

aero-id 已完成远超 MVP 的能力交付：

| 维度 | 已实现 |
|------|--------|
| **双传输** | HTTP REST + gRPC 全覆盖（38+ RPCs），同构业务逻辑 |
| **文件** | 双后端、流上传、内容去重、MIME/Size 校验、直传预签名、原子引用计数 GC |
| **消息** | 全 CRUD、Reactions、编辑、软删除、已读回执、Inbox/Conversation/FTS、系统消息 |
| **评论** | 两层嵌套、Like、举报/审核/隐藏、编辑、原子计数 |
| **通知** | Outbox 驱动、偏好（静默时段）、合并去重、SSE 实时推送、未读计数 |
| **可靠性** | 事务性 Outbox、幂等键、限流、SSO 断路器 + stale-cache + singleflight、优雅关闭 |
| **运维** | OTel 追踪、RED 指标、per-dependency 健康检查、migration CLI |

然而，以下五个方向是 **系统性盲区**——它们不是修补缺陷，而是解锁新的产品域或基础设施能力。每个方向均提供精确的代码级证据说明为什么它当前不存在，以及为什么需要它。

---

## 方向一：群聊 / 多参与者会话（Group Conversation / Multi-Party Chat）

### 代码级证据

当前消息模型是 **严格的 1-to-1 对偶**：

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string     // ← 单一收件人，无多参与者概念
    ...
}
```

```sql
-- migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    id          UUID PRIMARY KEY,
    from_uid    TEXT NOT NULL,
    to_uid      TEXT NOT NULL,     -- 单一 TEXT，不是 TEXT[]
    ...
);
```

授权逻辑同样硬编码了对偶性：

```go
// internal/domain/access.go
func CanReadMessage(actor string, m *Message) bool {
    return m.FromUID == actor || m.ToUID == actor  // ← 固定两个参与者
}
```

没有任何 `conversations` 表、`conversation_members` 关联表或 `thread_id` 列。消息的读取权限完全由 `from_uid`/`to_uid` 决定。如果要支持群聊，需要引入全新的数据模型。

### 为什么需要它

从 **产品视角**，1-to-1 消息是基础的起点，但在实际使用场景中群聊几乎是刚需——团队协作、项目群、家庭群。当前的设计意味着：

1. 一个消息只能发给一个人——如果要发公告，需要 N 次 API 调用（N 条独立消息行）
2. 没有 "已读/未读成员" 概念——群聊中谁读了消息是核心功能
3. 没有群组成员管理——加入/离开/踢出/角色
4. 通知系统 `message.received` 只发送给 `ToUID`——对于群聊需要发送给所有成员

从 **架构视角** 看，这是当前最难反向兼容的扩展之一，因为消息的授权模型、存储模型、通知模型都需要系统性调整。越早做，改造成本越低。

### 影响范围

- **Schema**: 新增 `conversations` 表 + `conversation_members` 表；`messages` 增加 `conversation_id` 列
- **Domain**: 新增 `Conversation` 实体；修改 `CanReadMessage` 授权逻辑
- **Service**: 新增 ConversationService（Create/Join/Leave/ListMembers/Send/ReadStatus）
- **Notification**: `message.sent` 事件需 fan-out 到所有成员
- **Search**: 搜索范围需扩展到群聊消息
- **Block**: 成员屏蔽需在群聊中过滤

### 建议策略

先不加群聊编辑/管理员/权限子功能，MVP 为：创建群聊 → 添加成员 → 群聊内发消息 → 成员看到所有消息 → 已读跟踪（谁读了）。后续迭代再加角色/禁言/置顶/历史导入。

---

## 方向二：分布式缓存与限速基础设施（Distributed Cache & Rate Limiting）

### 代码级证据

**Token 缓存**（`internal/sso/token_cache.go`）、**User 缓存**（`internal/sso/user_cache.go`）、**限速器**（`internal/pkg/ratelimit/ratelimit.go`）全部是 **进程内内存**：

```go
// internal/sso/token_cache.go
type TokenCache struct {
    mu    sync.RWMutex
    items map[string]*tokenEntry  // ← 纯内存，非共享
    ...
}

// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu     sync.Mutex
    limit  float64
    burst  int
    buckets map[string]*bucket  // ← 纯内存，per-instance
    ...
}
```

`app.go` 的 wiring 确认了这一点：限速器是一个本地对象直接注入中间件，没有共享存储层。

```go
// internal/app/app.go
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
```

**问题放大路径**：当部署 3 个副本时：
- Token 缓存：滚动部署后，实例 A warm → 实例 B 冷启动 → B 的每个请求都穿透到 SSO → SSO 负载飙升 → 断路器触发 → 所有实例回退到 stale-cache
- 限速器：用户 A 在实例 1 上消耗了 burst→实例 2 完全不认识该用户→允许额外 burst 请求→实际限速阈值为 `RPS × N`（N=副本数），而非配置值
- 缓存放大：3 个实例各存一份相同 token/user 数据，总内存占用 3×，命中率却不增

### 为什么需要它

在生产多副本部署中，**这是唯一一个有明确"不正确"行为的方向**——限速器在 N 个副本下会允许 N 倍于配置的流量。缓存冷启动导致 SSO 雪崩的风险真实存在。其他方向都是产品功能缺失，这是 **正确性问题**。

### 建议策略

不一定要引入 Redis——可以分阶段：

1. **限速器优先修复**: 限速器改为 advisory（每个实例独立限速 `RPS/N`），或引入共享后端（Redis 计数器）
2. **缓存可选共享**: Token 和 User 缓存可以继续保持 per-instance（TTL 短，成本低），但增加实例数感知的缓存预热的启动序列（当前 `warmup()` 只有 db ping，无 cache warming）
3. **长期**: 引入独立的 Redis 层，成为 token cache、user cache、rate limiter 的统一共享后端

---

## 方向三：文件后处理管线（File Post-Processing Pipeline）

### 代码级证据

文件上传管线在 `internal/file/service_upload.go` 中结束于 `store.Put`——**没有任何后处理步骤**：

```go
// internal/file/service_upload.go (关键路径)
func (s *FileService) persistUpload(ctx context.Context, ...) (*domain.File, error) {
    ...
    key, err := s.store.Put(ctx, storageKey, reader, in.MimeType, size)
    if err != nil {
        return nil, s.wrapStorageError(err)
    }
    // ← 没有后处理钩子，没有回调
    return s.saveFileRow(ctx, ...)
}
```

配置中也没有任何与后处理相关的配置项：

```yaml
# configs/config.yaml
storage:
  type: local
  max_upload_bytes: 52428800
  allowed_mime_types: []       # MIME 校验存在，但仅用于拒绝/接受
  # 无: thumbnail.enabled, virus_scan, content_moderation, ocr, ...
```

虽然 `outbox` 系统存在（可以触发异步事件），但 `file.uploaded` 事件并没有被写入 outbox——当前的事件类型只有 `message.sent`、`message.read`、`comment.replied`：

```go
// internal/notification/dispatcher.go
func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent", "message.read", "comment.replied":
        // ← 没有 file.uploaded
    }
}
```

### 为什么需要它

对于以文件存储为核心的服务，**输入的是一张图片/文档，输出的应该不只是保存**：

| 后处理类型 | 产品价值 | 技术可行性 |
|-----------|---------|-----------|
| **缩略图生成** | 文件列表预览避免全量下载 | 用 outbox 事件触发异步生成 |
| **病毒扫描** | 安全基线，防止恶意文件分发 | outbox → 外部 ClamAV 服务 |
| **内容审核** | NSFW/敏感内容检测 | 第三方 API / AI 模型 |
| **文档 OCR** | 文档内容可搜索（当前 FTS 只索引文件名） | Tesseract / 云服务 |
| **格式转换** | 统一存储格式（如所有图片转 WebP） | 异步处理 |

当前系统对文件的后处理能力为零。文件存进去什么样，拿出来就什么样。

### 建议策略

引入 `file.uploaded` outbox 事件，注册一个异步处理器链：

```
Upload → store.Put → outbox.Append(file.uploaded)
                        ↓
                   Outbox Relay
                        ↓
               ┌────────────────┐
               │  Pipeline Chain │
               │  ┌──────────┐  │
               │  │Thumbnail │  │
               │  ├──────────┤  │
               │  │Virus Scan│  │
               │  ├──────────┤  │
               │  │DM/Content│  │
               │  └──────────┘  │
               └────────────────┘
                        ↓
               file.thumbnail_{small,medium,large}_key,
               file.virus_scan_status, file.content_tags
```

每个步骤是独立的 Handler（类似 `outbox.Chain`），失败时 outbox 自动重试/死信。这样既不影响上传延迟，又能逐步添加后处理能力。

---

## 方向四：数据生命周期与合规框架（Data Lifecycle & Compliance Framework）

### 代码级证据

**没有数据导出 API**：任何 `/v1/export` 或 `/v1/data-portability` 端点都不存在。用户无法通过 API 导出自己的消息、文件、评论、通知数据。

**没有用户数据清除 API**：删除消息/文件/评论是逐条进行的。没有 `DELETE /v1/user/data` 来清除一个用户的所有数据（SaaS 退订 / GDPR 删除请求场景）。

**没有 TTL 保留策略**：软删除的消息和评论永久存在：

```go
// migrations/ — 消息和评论没有类似 storage GC 的硬删除机制
// 文件有 storage_refs + GC sweeper，但 messages/comments 没有
```

```sql
-- migrations/000015_perf_indexes.up.sql — 只添加了搜索和通知索引
-- 没有消息/评论的清理机制
```

**审计日志永久增长**：`audit_events` 表有索引但没有清理策略——在活跃生产环境中审计日志会线性增长。

```sql
-- migrations/000014_create_audit_log.up.sql
CREATE TABLE IF NOT EXISTS audit_events (
    ...
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- 无 TTL、无保留窗口、无清理
```

### 为什么需要它

这是 **合规需求而非功能需求**，但忽略它将带来实际的法律和商业风险：

1. **GDPR / CCPA / 中国个人信息保护法**: 用户有权要求导出所有个人数据、要求删除所有个人数据。当前没有任何端到端支持
2. **数据膨胀**: 软删除数据永久积累。消息和评论没有类似 `storage.GC` 的机制。审计日志没有保留窗口
3. **SaaS 多租户场景**: 租户退租时需要彻底清除该租户所有用户的数据
4. **存储成本**: 软删除数据 + 审计日志是最大的隐含存储增长源

### 建议策略

这个方向可以分解为三个可独立交付的阶段：

**Phase 1 — 审计日志保留策略**（低成本，高收益）
- 为 `audit_events` 表增加保留窗口配置
- 已有 `storage.GC` 模式可复用：一个 `AuditLogSweeper` 定时清理超期审计记录
- 同理，为软删除的消息和评论增加清理器（类似 `file.GC` 但更简单——直接硬删除）

**Phase 2 — 用户数据导出**
- 新增 `GET /v1/export` 端点
- 后台异步构建包含用户所有数据的归档包（消息、文件元数据、评论、通知偏好）
- 归档完成后通知用户下载（复用通知系统）
- 使用 outbox 触发导出任务（需要先有方向三的通用 job 能力）

**Phase 3 — 用户数据清除**
- 新增 `DELETE /v1/user/data` 端点（保留账户标识，清除内容数据）
- 级联软删除该用户的所有消息、文件、评论
- 等待 GC 周期硬删除
- 审计日志中记录清除操作（存 root cause 而非全部删除细节）

---

## 方向五：消息投递管线增强——Mobile Push 与交付语义（Message Delivery Pipeline & Mobile Push）

### 代码级证据

**没有移动推送集成**：通知的实时投递只有 SSE 一条路径：

```go
// internal/handler/http/events.go — SSE endpoint, HTTP-only, long-lived connection
r.Auth.GET("/events", h.Stream)
```

SSE 要求客户端保持与服务器的长连接。这对 Web 端可行，但对 **移动端完全不适用**——移动应用不能保持常开 HTTP 连接，需要 FCM（Firebase Cloud Messaging）/ APNS（Apple Push Notification Service）通道。

**没有发送交付状态**：消息从 `created_at` 到 `read_at` 之间只有一个 undefined "已发送但未读"状态：

```go
// internal/domain/message.go
type Message struct {
    ...
    ReadAt    *time.Time    // 仅跟踪"已读"
    // 无: DeliveredAt, SentAt
}
```

**没有消息撤回功能**：软删除 (`deleted_at`) 只会隐藏消息，不会通知接收方"发送者撤回了消息"：

```go
// internal/service/message.go
func (s *MessageService) Delete(ctx context.Context, id ID) error {
    // 只设置 deleted_at，不产生撤回事件
    // 接收方如果已经加载了该消息，可能仍然可见
}
```

### 为什么需要它

从 **产品角度**，消息应用的基础体验包括：

1. **移动推送**：用户离开 Web 时仍然能收到新消息/评论/通知的通知。没有移动推送，aero-id 只能算"半在线"服务
2. **发送状态**：三条短线 → 两条短线 → 蓝色对勾（类 WhatsApp/Telegram）。用户需要知道消息是否已送达、已读
3. **消息撤回**：发错了 > 撤回 > 对方看到"xxx 撤回了一条消息"——这是消息应用的标配

从 **架构角度**，推送和交付状态的引入对消息写入路径影响较大，需要在 outbox 中新增事件类型：

- `message.delivered`：推送确认回执
- `message.recalled`：发送方撤回消息（需要通知所有已读取的接收方）

### 建议策略

**Phase 1 — 交付状态扩展**
- 为 `messages` 表增加 `delivered_at` 列（或直接使用 outbox 事件追踪）
- 在 MarkRead 已有基础上增加 `Deliver` 语义——当消息被推送给客户端时标注 `delivered_at`
- 通过 outbox 产生 `message.delivered` 事件通知发送方

**Phase 2 — 消息撤回**
- 为 `messages` 表增加 `recalled_at` 列
- 召回后 receiver 端的查询过滤 `recalled_at IS NULL` 的消息
- 如果 receiver 已经拉取过该消息，通过 SSE（以及未来的 mobile push）推送撤回事件
- 对应的通知事件：`message.recalled`

**Phase 3 — 移动推送网关**
- 新增 `internal/push/` 包，抽象 `PushSender` 接口（FCM / APNS）
- 在 `notification.Dispatcher` 中增加推送分支：当 SSE 不可达时（用户不在线），通过 push 网关投递
- 或者独立于 notification 系统，在 outbox 中链入 `PushHandler`

---

## 建议实施优先级

| 方向 | 优先级 | 难度 | 核心收益 | 建议时机 |
|------|--------|------|---------|---------|
| 分布式缓存与限速 | **P0** | 中 | 正确性（多副本下限速不正确） | 现在 |
| 文件后处理管线 | **P1** | 中 | 产品力（文件平台必备） | 下一个版本 |
| 数据生命周期与合规 | **P1** | 中 | 合规 + 成本控制 | 并行进行 |
| 消息投递管线增强 | **P2** | 高 | 用户体验（移动端刚需） | 确定移动端计划时 |
| 群聊系统 | **P2** | 高 | 产品广度扩展 | 有明确需求时 |

### 为什么没有把群聊放在 P0

群聊的架构影响面极大（schema、authorization、notification、search 全部需要改），但它的**启动条件**是业务上确认需要群聊产品特性。与限速不正确（这是 bug）不同，缺少群聊并不是错误——它是一个 consciously deferred product decision。在决定做之前，建议先完成缓存/限速的分布式改造、文件管线扩展和合规框架，这些是群聊的依赖基础设施。

---

## 写在最后

aero-id 已经是一个非常成熟的服务。这五个方向不是修复缺陷，而是解锁新的产品域和基础设施能力——它们的共同特征是需要**系统性设计**而非局部修补，一旦决定实施，可以复用现有的 Outbox、SSE、缓存抽象等基础设施快速落地。

建议在下一次架构评审中，将这五个方向与 ROADMAP 中的既有项目（SSE、审计日志、全文搜索——均已实现）一同重新排序，形成 **v2.0 路线图**。
