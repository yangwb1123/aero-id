# aero-id — 全局代码扫描：5 个高价值扩展方向

> **日期**: 2026-07-11  
> **作者**: 资深架构师 / 产品经理  
> **方法**: 完整扫描 `/home/u1/aero-id` 的当前工作副本（非 worktrees），读取所有 `internal/` 层的 Go 源文件、domain 定义、proto 定义、config、migrations 以及 ROADMAP/TODO 文档，对比既有分析以避免重复。

---

## 代码库状态快照（已实现功能）

在分析新方向之前，必须认清当前 codebase 已经达到的成熟度：

| 领域 | 已实现 |
|------|--------|
| **文件** | CRUD + visibility + 预设上传 + content-dedup + storage-ref 引用计数 + metadata 更新 + GC reconciliation sweep + S3/local 双后端 |
| **消息** | 1:1 收发 + typed/system kinds + reactions + edit + soft-delete + 全量 read-receipt + conversation mark-all-read + 每会话 inbox |
| **评论** | 两层嵌套 + like/unlike + report/moderation (triage lifecycle) + hide/unhide + edit + soft-delete |
| **通知** | outbox 驱动 + SSE 实时推送 + 通知偏好 (quiet hours + enable/disable) + 合并折叠 + per-type 限速 + last_event_id 重连 |
| **搜索** | PostgreSQL tsvector + GIN 索引 + CJK ILIKE 回退 + 三实体（文件/消息/评论）联合搜索 |
| **安全** | SSO token validate + user cache + circuit breaker + serve-stale + TLS/mTLS + cert-pinning + directional block |
| **治理** | 角色权限 (moderator/admin) + permissions 中间件 + audit log + comment moderation queue |
| **运维** | /metrics (RED + SSO + outbox) + OpenTelemetry tracing + rate limiting + idempotency + healthz/readyz + gRPC reflection + migration CLI + 优雅关闭 |
| **正确性** | transactional outbox + 原子 reply-count + idempotency key + soft-delete + storage GC |

---

## 方向一：水平扩展基础设施 — 分布式缓存与跨实例通信

### 为什么要做

当前所有实例级状态都存储在进程内存中，缺少跨实例共享机制。一旦运行超过 1 个副本（生产 HA 必须），以下功能全部失效：

| 组件 | 当前实现 | 多实例问题 |
|------|---------|-----------|
| **SSE Hub** (`internal/hub/hub.go`) | `sync.Map`——每个 uid 的 subscriber channel 在内存中 | 用户连到实例 A，消息推送到实例 B → 看不到实时通知 |
| **Rate Limiter** (`internal/pkg/ratelimit`) | per-instance token bucket | 用户发 N 个请求，每个实例各自算额度 → 实际限速放大 N 倍 |
| **SSO Token/User Cache** (`internal/sso/token_cache.go`, `user_cache.go`) | in-memory map + TTL sweeper | 每个实例冷启动 → 全部 cache miss → SSO 压力放大 N 倍 |
| **Outbox Relay** (`internal/outbox/relay.go`) | 每个实例独立轮询 | 所有实例竞争同一批事件 → 重复处理 / 死信 |
| **Idempotency Key Store** (`internal/idempotency/store.go`) | Postgres-backed ✅ | 无问题——已共享 |
| **可观测性 Metrics** (`internal/pkg/metrics`) | in-memory Registry | 每个实例暴露片面的指标 → 需要 Prometheus 聚合实例级标签 |

代码级证据：

```
// internal/app/app.go:258
// and disabled by default; per-instance (distributed limiting would need shared
// state, e.g. Redis) and is out of scope.
```

```
// internal/config/config.go:28
// RPS up to Burst. Limiting is PER-INSTANCE; distributed limiting would need
```

```
// internal/hub/hub.go
type Hub struct {
    subs   sync.Map // uid → *uidSubs
    ...
}
// 每个 Hub 实例完全独立
```

### 建议方案

引入轻量级分布式层，不必全量迁移：

1. **Redis Pub/Sub** 替换跨实例 SSE 推送。Hub 在本地 subscribe + 同时 publish 到 Redis channel；其他实例通过 Redis Pub/Sub 收到后推送给本地的 subscriber。
2. **Redis Rate Limiter** 用 `SLIDING WINDOW` 或 `TOKEN BUCKET (INCR + EXPIRE)` 替换 per-instance token bucket。
3. **Redis Cache** 替代 in-memory SSO cache（或作为共享 L2，in-memory 做 L1）。
4. **Outbox Relay Leader Election** 用 Redis `SET NX EX` 或 PostgreSQL advisory lock 确保只有一个实例是 active drainer。

### 影响/工作量

- **高业务价值** —— 没有它，生产多实例 = 功能破碎
- **中等工作量** —— Redis 层可渐进引入：先做 Hub 跨实例推送（最有痛感），再收割限速和 cache
- **向后兼容** —— 无 Redis 时退化成当前 per-instance 行为

---

## 方向二：多租户 / 协作抽象层 — Group / Organization

### 为什么要做

当前的领域模型完全围绕**单个 uid** 构建：

```
File   → OwnerUID        (单用户)
Message → FromUID/ToUID   (1:1)
Comment → TargetID         (附着于单个实体)
Block  → blocker/blocked  (单用户方向)
```

不存在以下概念：团队、组织、工作空间、群聊、共享文件夹。这是当前产品模型中最大的空白。

代码级证据：

```
// internal/domain/file.go
type File struct {
    OwnerUID    string    // 只有 Owner，没有 Group
    Visibility  Visibility // Public 或 Private，没有团队可见
    ...
}

// internal/domain/message.go  
type Message struct {
    FromUID   string        // 只能 1:1
    ToUID     string
    ...
}

// internal/service/service.go
// CanReadFile / CanReadMessage 只检查 owner 或 participant
func canReadFile(actor string, f *domain.File) bool {
    return f.Visibility == domain.VisibilityPublic || f.OwnerUID == actor
}
```

产品能力缺口：
- **无团队文件共享** —— 目前只能逐个分享给 named uid，没有 "项目团队" + 自动继承
- **无群聊/频道** —— 只有 1:1 消息，无法做 team channel 或 topic-based room
- **无组织级资源管理** —— 无法审计团队所有文件、无法设置团队级别的可见性
- **无跨实体权限继承** —— "加入 workspace 自动获取其中所有文件的读权限" 不可行

### 建议方案

1. 新增 `Group` 实体（`groups` 表）：id, name, owner_uid, visibility, member_count
2. 新增 `GroupMember`（`group_members` 表）：group_id, uid, role (admin/member), joined_at
3. 扩展 `File.Visibility` 为三态：Private / Public / GroupVisible（新增 `group_id` 可选字段）
4. 新增 `GroupMessage` 或扩展 `Message` 支持 `group_id`：群聊消息的 to_uid 可为空，group_id 非空
5. 扩展 `canReadFile`/`canReadMessage` 检查：actor ∈ group.members

### 影响/工作量

- **极高产品价值** —— 这是从单用户工具到协作平台的质变
- **大工作量** —— 涉及 domain 新实体、repository、service、handler、migration、proto、权限模型
- **建议分期** —— Phase 1: Group + GroupMember CRUD; Phase 2: group-visible 文件; Phase 3: 群聊

---

## 方向三：多渠道通知投递 — Email / Push / Webhook 泛化

### 为什么要做

当前通知只走两条路：
1. `notifications` 表 —— 通过 pull API (ListNotifications) 消费
2. SSE Hub —— 实时推送到已连接的浏览器

没有任何**离线**或**外部**渠道。产品后果：

- 用户关掉浏览器 → 得不到任何通知
- 无邮件摘要 → 无法召回日活
- 无移动推送 → 不可能做移动端应用
- 无 Slack/钉钉 webhook → 无法与企业工作流集成

代码级证据：

```
// internal/notification/notification.go
type Service interface {
    List(...)       // pull
    MarkRead(...)   // read receipt
    MarkAllRead(...)
    UnreadCount(...)
}
// 没有任何 Email / Push / Webhook 抽象
```

```
// internal/service/notification.go
type NotificationPusher interface {
    Publish(uid, eventType, data string, eventID int64)
}
// 目前只有 *hub.Hub 实现了 Pusher
// hub.Publish 直接发 SSE → 不持久化、不跨渠道
```

```
// internal/outbox/webhook.go
// WebhookHandler 存在但只向外发送完整 outbox 事件（给外部系统）。
// 这不是通知渠道——它是 event-driven integration endpoint。
// 没有为终端用户提供邮件/推送通道。
```

### 建议方案

利用现有的 outbox 基础设施 + 通知 dispatch 管道，增加「通道抽象层」：

1. 定义 `DeliveryChannel` 接口：
   ```go
   type DeliveryChannel interface {
       Name() string
       Deliver(ctx context.Context, n *domain.Notification, recipient *sso.User) error
   }
   ```
2. 内置实现：
   - **EmailChannel** —— 使用模板引擎 + SMTP / SES。用户偏好中可开关 email 通知
   - **PushChannel** —— FCM / APNs 集成。需新增 `device_tokens` 表
   - **WebhookChannel** —— 用户级别的自定义 webhook（区别于现在的 system-level outbox webhook）
3. 在 NotificationDispatcher 中串联 channels：写完 notification row 后，异步（或同步）遍历激活的 channel 列表
4. 新增 `notification_preferences` 扩展字段：`email_enabled`, `push_enabled`, `digest_frequency`

### 影响/工作量

- **高产品价值** —— 离线通知是任何社交/沟通产品的基本能力
- **中高工作量** —— Email 模板 + SMTP 集成本身不大；Push 需要 device token 管理 + FCM SDK
- **向后兼容** —— 新增 channels 不影响现有 pull/SSE 消费端
- **可测试** —— 每个 channel 独立测试；集成测试可用 MailHog + 模拟 FCM

---

## 方向四：智能媒体管线 — 预览缩略图 / 病毒扫描 / CDN

### 为什么要做

aero-id 自称文件服务（MessageKind 支持 Image 和 File），但文件上传后的处理管线是空的：

```
Upload → (checksum dedup) → 存储到 backend
                                                → 结束
```

没有：
- **图片缩略图** —— 用户上传 4K 照片后，聊天/文件列表仍加载原图。带宽和加载速度灾难
- **病毒扫描** —— 任何用户可上传任意文件，系统做一次 ClamAV 扫描才是生产基线
- **CDN 分发** —— 下载 URL 直接指向存储后端（S3/local），没有 CDN 缓存层
- **图片/视频转码** —— 无 WebP/AVIF 转换、无 HLS/DASH 视频流化

代码级证据：

```
// internal/storage/storage.go
type FileStorage interface {
    Put(...)
    Get(...)
    Delete(...)
    PresignGet(...)
    PresignPut(...)
    Stat(...)
    NewKey(...)
}
// 没有任何处理/转换步骤
```

```
// internal/service/file.go
// Create 和 CompleteUpload 只做 stat + persist row
// 没有 post-upload 处理 hook
```

```
// internal/domain/file.go
type File struct {
    MimeType    string            // 存储了 MIME，但没有后续处理
    Checksum    string            // 只用于 dedup，不用于 pipeline
    ...
}
```

```
// 事实：MessageKindImage (2) 和 MessageKindFile (3) 已定义
// internal/domain/shared.go
// 但 handler 中没有任何图片特定处理
```

### 建议方案

引入异步媒体处理管线，在 upload complete 后通过 outbox 触发：

1. **Thumbnail Generator** —— 图片上传后自动生成 256x256 / 512x512 缩略图 + WebP 转换
2. **Virus Scanner** —— 用 outbox event 触发 ClamAV 扫描，结果写回 `file.meta['scan_status']`；阳性文件自动隐藏
3. **CDN Integration** —— 在 PresignGet/DownloadURL 中优先返回 CDN URL（如 CloudFront），CDN miss 时回源
4. **Transcoding Pipeline** —— 视频上传 → FFmpeg 转 HLS → 分片存储
5. 新增 `file_processing` 状态追踪：pending → processing → completed/failed

### 影响/工作量

- **中高业务价值** —— "文件服务"不能只存不处理
- **高工作量** —— 缩略图简单，病毒扫描依赖 ClamAV 容器，视频转码复杂
- **建议分期** —— Phase 1: 缩略图（纯图片预览，24h 工作量）；Phase 2: ClamAV（安全基线）；Phase 3: CDN

---

## 方向五：合规与数据生命周期管理 — 导出 / 清理 / 级联删除

### 为什么要做

当前的数据生命周期只有「软删除 + GC sweep」：

```
创建 → 软删除 → (等待 retention) → GC hard-delete → blob 清理
```

缺少以下关键能力：

| 能力 | 是否存在 | 合规风险 |
|------|---------|---------|
| **用户数据导出** (GDPR Art. 20) | ❌ | 用户有权获取其所有数据的机器可读副本 |
| **账户删除级联** | ❌ | SSO 侧删除账户后，aero-id 中的文件/消息/评论/通知永远残留在 DB 中 |
| **消息保留策略** (Data Retention) | ❌ | 消息永久保留，无过期自动清理 |
| **通知自动过期** | ❌ | 通知 table 只靠 idempotency + notification 没有 TTL 清理 |
| **批量操作** (批量删除/迁移) | ❌ | 无管理 UI/API 做租户级别的数据操作 |

代码级证据：

```
// 没有 export/bulk 相关代码
grep -rn "export\|backup\|gdpr\|portability\|data.*download\|bulk" internal/ --include="*.go"
// → 0 结果
```

```
// 软删除不会级联
// internal/domain/file.go: soft-delete only deletes the file row
// comments & messages referencing that file are NOT cleaned up
// shared file permissions are NOT revoked
```

```
// GC Sweep 只处理文件 blob，不处理消息/评论/通知
// internal/storage/gc/sweeper.go
type Config struct {
    Retention    time.Duration  // soft-deleted file rows
    MinOrphanAge time.Duration  // orphan blobs
    PurgeBatch   int
    SweepOrphans bool
}
// 没有消息/评论/通知的 retention
```

```
// 通知表只有 idempotency key 有 cleaner
// internal/idempotency/store.go
func (s *GormStore) RunCleaner(interval time.Duration) // 清理 idempotency_keys
// notifications 表从来不过期
```

### 建议方案

1. **Data Export API** —— 异步任务，通过 outbox 触发。导出格式 JSON/CSV，打包为 zip 存储在 temp storage 中，提供限时下载 URL
   - 消息: `messages` 表按 `from_uid = uid OR to_uid = uid`
   - 文件: `files` 表按 `owner_uid = uid`
   - 评论: `comments` 表按 `owner_uid = uid`
   - 通知: `notifications` 表按 `recipient_uid = uid`

2. **Account Deletion Cascade** —— SSO 确认账户删除后，aero-id 的 webhook（已有 outbox webhook）接收 `account.deleted` 事件 → 执行全量软删除：
   - 所有 uid 拥有的文件。标记 `owner_uid` 为 `__deleted__` 或 owner 引用清空
   - 所有往来消息：from_uid / to_uid 匿名化
   - 所有评论: owner_uid 匿名化
   - 所有通知: recipient_uid 匿名化

3. **Retention Policies** —— 配置驱动：
   - `notifications.retention_days`：超过 N 天的已读通知自动硬删除
   - `messages.retention_days`：超过 N 天的已读消息自动硬删除（可配置 for 整个部署或 per-user）
   - 新建 `retention_configs` 表支持租户级策略

4. **Bulk Operations** —— 管理员 API：
   - `POST /v1/admin/bulk/delete-messages?from_uid=...&before=...`
   - `POST /v1/admin/bulk/export?uid=...&types=messages,files,comments`

### 影响/工作量

- **高合规价值** —— GDPR 合规是法律要求，不是产品功能
- **中工作量** —— Export 走 outbox 异步生成；Retention 加定时 job（类似 GC sweep）；Cascade 依赖 SSO 的 account.deleted webhook 事件
- **低风险** —— 所有操作都经过现有的审计日志

---

## 建议实施优先级总览

| # | 方向 | 价值 | 工作量 | 依赖 | 建议分期 |
|---|------|------|--------|------|---------|
| 1 | **水平扩展** (Redis 层/跨实例通信) | **P0** 运维必须 | 中 | 无 | Phase 1: Hub 跨实例; Phase 2: rate limiter; Phase 3: cache |
| 2 | **多租户/协作** (Group/Organization) | **P1** 产品质变 | 大 | 方向 1 (权限需要共享) | Phase 1: Group CRUD; Phase 2: Group-visible 文件; Phase 3: 群聊 |
| 3 | **多渠道通知投递** (Email/Push) | **P1** 用户留存 | 中高 | 无 | Phase 1: Email + 模板; Phase 2: Push (FCM); Phase 3: 用户级 webhook |
| 4 | **智能媒体管线** (缩略图/扫描/CDN) | **P2** 产品完善 | 高 | 方向 1 (处理异步化) | Phase 1: 缩略图; Phase 2: ClamAV; Phase 3: CDN + 转码 |
| 5 | **合规与数据生命周期** (Export/Retention/Cascade) | **P2** 法律必须 | 中 | 方向 3 (Email 通知 export ready) | Phase 1: Retention 定时 job; Phase 2: Export API; Phase 3: 账户删除级联 |

---

## 已覆盖但不再重复的既有方向

以下方向已在已有 70+ 份分析文档中充分讨论，本次不再展开：

- **Webhook / Outbox external delivery** —— 已实现 `outbox.WebhookHandler` + Chain
- **gRPC HTTP 双向 parity** —— 已全部完成 (38 RPCs)
- **Idempotency** —— 已实现
- **Comment like/report/moderation** —— 已实现
- **Message reactions** —— 已实现
- **File sharing** —— 已实现
- **SSO Resilience / CB / serve-stale** —— 已实现
- **Observability (RED / tracing)** —— 已实现
- **Notification preferences / quiet hours** —— 已实现
- **Storage GC / reconciliation** —— 已实现
- **Search** —— 已实现 (tsvector + CJK fallback)
- **Audit log** —— 已实现
- **SSE real-time push** —— 已实现
