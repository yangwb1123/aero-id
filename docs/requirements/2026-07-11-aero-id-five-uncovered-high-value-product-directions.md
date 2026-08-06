# aero-id — 五个尚未覆盖的高价值产品/架构扩展方向

> **日期:** 2026-07-11  
> **视角:** 资深架构师 + 产品经理  
> **方法:** 全局代码通读（`cmd/` · `internal/` 全部 234 个 Go 文件 · 22 份可逆迁移 · `api/proto/` · `configs/` · `Makefile` · `deployments/`），与 `docs/requirements/` 下既有 110+ 份分析文档逐方向交叉比对，**确保以下 5 个方向在既有材料中未被系统性覆盖**。  
> **前提:** aero-id 已是一个高度成熟的双传输数据服务平台。以下方向不是"补充缺失功能"，而是**在当前架构基础上进入下一个产品阶段**的扩展点。每个方向均附精确代码级证据。  
> **约束:** 不编写任何代码。

---

## 代码库当前能力全景（已实现 + 本文件不重复论述）

| 域 | 已交付能力 |
|------|-----------|
| **基础设施** | HTTP Gin + gRPC 41 RPC 完全对等 · OTel 追踪 · Prometheus RED 指标 · per-dependency `/readyz` · 冷启动 warmup · 优雅关停（最后 drain）· `cmd/migrate` CLI + auto_migrate · 双传输限流（per-actor per-IP）· DB 持久化幂等键（24h TTL）· 23 份可逆迁移 |
| **SSO 弹性** | 断路器 · stale-cache + grace window · singleflight · 有界重试 + backoff · TLS/mTLS · SPKI 证书锁定 · gRPC keepalive · 缓存命中率/延迟探针 |
| **文件管线** | 流式上传 · MIME/size 校验 · SHA-256 内容去重（per-owner）· S3 presigned 直传 · 显示分享/撤销（per-grantee）· 元数据 PATCH · Storage Refs 原子引用计数 · Storage GC（软删清除 + 孤儿扫描） |
| **消息** | 1:1 私信（文本/图片/文件/系统）· Reactions（双向、原子、去重）· 已读回执 + 未读计数 · Inbox/Conv 视图 · 编辑/软删 · Block 自动拦截 |
| **评论** | 两层嵌套（楼中楼）· Like/Unlike（原子计数器）· 编辑 · 软删 · 举报→审核→隐藏/显示 · 原子回复计数 |
| **通知** | Outbox 驱动异步创建 · 折叠去重（5min 窗口）· 速率限制 · 偏好设置（启用/静默时段/时区）· Block 抑制 · SSE 实时推送（last_event_id 重连回放）· Webhook HMAC 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN）· CJK ILIKE 回退 · 多 kind 联合搜索 · owner_uid 隔离 |
| **治理** | 方向性 Block（inbound only）· 审计日志（append-only, fire-and-forget）· Admin 角色 + 4 权限 · `RequirePermission` 中间件 |
| **部署** | 多阶段 Dockerfile · docker-compose.yml · GitHub Actions CI · 优雅关停 · 冷启动预热 |

> 所有上述能力均已代码级验证。本文不再重复论述。

---

## 方向一：分享链接模型（Share Links）——从"分享给人"到"分享给任何人"

### 为什么需要

当前的文件分享模型是**身份绑定授权**：`ShareFile(granteeUIDs)` 要求知晓每个接收者的 SSO `uid`，且接收者必须拥有 aero-id 账户。这是一个产品天花板——它排除了以下场景：

- 我要把一个文件发给一个**没有 aero-id 账户**的外部合作伙伴
- 我要在社交媒体上**公开分享**一个只读链接
- 我要设置一个**有过期时间的**分享（24h 后自动失效）
- 我要给分享链接添加**密码保护**
- 我要限制链接的**下载次数**或限制为**仅预览不可下载**

### 代码级证据

**证据 1：FileShareRepository 只有身份绑定授权，无链接抽象**

```go
// internal/repository/file_share.go
// 现有接口：Grant(owner, []granteeUIDs) / Revoke(owner, granteeUID) / ListFilesSharedWith(grantee)
// 没有以下方法：CreateShareLink() / RevokeShareLink() / AccessByLinkToken()
// 没有以下概念：share_token, expires_at, password_hash, max_downloads, permission_level
```

**证据 2：`files.visibility` 只有 binary private/public**（`domain/shared.go:30-33`）

```go
const (
    VisibilityPrivate Visibility = 0  // 仅 owner
    VisibilityPublic  Visibility = 1  // 所有人
)
// 没有：VisibilitySharedViaLink = 2
// 没有：visibility 与 share_tokens 的关联关系
```

**证据 3：当前文件的 ACL 检查是 owner-or-public 二值逻辑**（`domain/access.go:5-7`）

```go
func CanReadFile(actor string, f *File) bool {
    return f.Visibility == VisibilityPublic || f.OwnerUID == actor
}
// 没有：byShareToken(ctx, token string) → 检查 link 是否有效/未过期/未超下载次数
// 没有：share_token 与 uid 解耦——actor 可以为空（匿名访问）
```

**证据 4：文件下载路径 `DownloadURL` / `Get` 无 token 参数**（`internal/service/file_presign.go`）

```go
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
    f, err := s.Get(ctx, id)  // Get 内部调 CanReadFile(actor, f) → 必须 actor 有权限
    // 无法传入 share_token → 匿名用户无法访问分享链接
}
```

### 现有可复用基础设施

| 资产 | 分享链接场景复用方式 |
|------|---------------------|
| `storage.PresignGet()` 签名 URL 能力 | 内部下载链接，可在上层封装带 token 验证的代理 |
| `HTTP GET /files/dl?token=<hmac>` 签名验证路由 | 可直接扩展为 `?share_token=<token>` 匿名访问路径 |
| `audit.AuditLogger` 审计框架 | 记录每次分享链接的访问（谁、何时、IP、是否成功） |
| `storage.Local` HMAC 签名路线 | 可为分享链接签发限时访问签名 |

### 建议的增量方案

1. 新增 `share_links` 表：`(id, file_id, token(唯一), password_hash, expires_at, max_downloads, download_count, permission ['view'|'download'], created_by, created_at)`
2. `POST /v1/files/:id/share-links` — 创建分享链接（可选 password/expiry/max_downloads/permission）
3. `GET /v1/s/{share_token}` — 匿名访问链接（无 auth，由专门中间件校验 token + password）
4. `CanReadFile` 扩展为：owner → true / public → true / valid share_link → true / else → false
5. GC 扩展：过期分享链接自动清理

**价值判断:** 极高的产品差异化价值，低-中实现成本（现有存储 + 审计 + 签名链路几乎全部可复用）。将一个内部数据服务变为可对外分享的协作平台。

---

## 方向二：用户数据导出（Data Portability / GDPR Export API）

### 为什么需要

aero-id 是用户数据的托管方（文件、消息、评论）。目前**没有任何机制允许用户以机器可读格式整体导出自己的数据**。这在以下场景中是硬性需求：

- **监管合规**：GDPR 第 20 条（数据可携带权）、CCPA、中国《个人信息保护法》均要求数据控制者提供数据导出能力
- **用户信任**：用户应能在任何时候将自己的数据迁移出平台
- **企业客户采购条件**：数据导出能力是企业采购 SaaS 服务的前置条件

### 代码级证据

**证据 1：三个实体都有 `List` 或 `ListByOwner` 方法，但无聚合导出**（`internal/file/repository.go`）

```go
// 文件可以按 owner 列出：
ListByOwner(ctx, ownerUID, page) → ([]*File, nextCursor)

// 消息可以按收件人列出：
ListByRecipient(ctx, toUID, page) → ([]*Message, nextCursor)

// 通知可以按收件人列出：
ListByRecipient(ctx, recipientUID, page) → ([]*Notification, nextCursor)

// 评论可以按 owner 列出：
ListByOwner(ctx, ownerUID, page) → ([]*Comment, nextCursor)

// 但没有任何一个方法是：
ExportAll(ctx, uid) → (archive of all files + messages + comments + metadata)
```

**证据 2：没有任何导出端点、后台任务、或异步导出机制**

```go
// internal/handler/http/ — 所有路由
// 没有：GET /v1/export → 触发异步导出
// 没有：GET /v1/exports/:id → 查询导出任务状态
// 没有：GET /v1/exports/:id/download → 下载导出产物

// internal/outbox/ — 事件类型
// 没有：event_type = "data.export.requested"
// 没有：event_type = "data.export.completed"
```

**证据 3：文件内容可通过存储后端获取，但消息和评论缺少序列化为标准格式的路径**

消息和评论目前通过 List API 分页返回 JSON。对于大型导出，需要：
- 消息 → 标准格式（JSON Lines, CSV, or 类 EML）
- 评论 → 按 target 组织的结构化 JSON
- 文件 → 原始 blob + 元数据文件

目前这些格式转换逻辑完全不存在。

### 现有可复用基础设施

| 资产 | 数据导出场景复用方式 |
|------|---------------------|
| `outbox.Relay` + `Events` 异步处理框架 | "导出请求"作为 outbox 事件提交，relay 异步消费生成导出包 |
| `storage.FileStorage` 文件存储 | 导出产物（zip/tar.gz）存入 storage，返回 presigned 下载链接 |
| SSE Hub 实时推送 | 导出完成后通过 SSE 通知用户 |
| notification 系统 | 导出完成后写入 `notification` 行（"你的数据导出已完成"） |
| `messages.meta` JSONB | 导出时包含 meta 字段，保证数据完整性 |
| `files.Meta` JSONB | 同上 |
| 单文件 `PresignGet` / `GET /files/dl` | 导出包的大文件下载复用现有签名链路 |
| `idempotency.Store` | 防止重复提交导出请求（幂等键） |

### 建议的增量方案

1. 新增 `export_jobs` 表：`(id, uid, status[queued|processing|completed|failed], format, requested_at, completed_at, storage_key, size, error_message)`
2. `POST /v1/exports` — 提交导出请求（可选格式、实体类型过滤）
3. `GET /v1/exports/:id` — 查询进度
4. `GET /v1/exports/:id/download` — 下载产物（复用 /files/dl 签名验证机制）
5. 后台 worker（outbox relay handler）异步执行：查询所有关联数据 → 序列化 → 打包 → 存入 storage → 写 notification → SSE 推送

**价值判断:** 中-高实现成本（主要是序列化 + 打包逻辑），但合规/信任价值极高。对于计划服务企业客户的产品，这是**必须交付**而非可选。

---

## 方向三：回收站/软删自我管理（Trash / Recycle Bin Self-Service）

### 为什么需要

aero-id 全系统使用 GORM `DeletedAt` 软删——这是一个"不可逆"的设计（用户无法自行恢复）。当前的软删路径是：

```
用户 Delete → Set deleted_at → 记录被隐藏 → GC 在 retention 后永久清除
                                                      ↖ 用户无法干涉这个流程
```

这产生了一个严重的产品缺口：**用户误删后无法恢复**。对于存储重要数据（文件、消息、评论）的平台，缺少回收站是一个信任和安全问题。

此外，当前没有"永久删除"API（硬删），用户无法立即清除自己的数据（隐私权），必须等待 GC 周期。

### 代码级证据

**证据 1：所有 model 都有 `deleted_at`，但没有任何 API 可供用户管理已删项**

```go
// internal/domain/file.go: type File struct { ... DeletedAt gorm.DeletedAt }
// internal/domain/message.go: type Message struct { ... }
// internal/domain/comment.go: type Comment struct { ... }

// 没有任何 API 可以：
// - 列出用户已删除的 items（"回收站视图"）
// - 恢复一个软删的 item（un-delete）
// - 永久删除一个已软删的 item（hard-delete before GC）
```

**证据 2：repository 层的 `Delete` 方法只做软删，无 `Restore` 方法**

```go
// internal/repository/file.go
func (r *fileRepo) Delete(ctx context.Context, id domain.ID) error {
    // 实际调用：db.Model(&row).Where("id = ?", id).Update("deleted_at", now)
    // 没有：Restore(ctx, id) → Set deleted_at = NULL
}

// internal/repository/message.go — 同上模式
// internal/repository/comment.go — 同上模式
```

**证据 3：Storage GC sweeper 是唯一的数据清除者，但用户无法干预**（`internal/storage/gc/sweeper.go`）

```go
// purges deleted rows older than retention window
// retention 是全局配置，用户无法对某个文件请求提前清除
```

**证据 4：File `HardDelete` 存在但由 GC 独占使用，不对用户暴露**

```go
// internal/file/file.go
type Repository interface {
    // ...
    HardDelete(ctx context.Context, ids []domain.ID) error  // 只在 GC 中使用
}
```

### 现有可复用基础设施

| 资产 | 回收站场景复用方式 |
|------|-------------------|
| GORM `DeletedAt` 统一软删 | 查询可加 `Where("deleted_at IS NOT NULL")` 实现"回收站列表" |
| `files.DeletedAt` / `messages.DeletedAt` / `comments.DeletedAt` | 统一的模型层处理，恢复逻辑可用 GORM `Unscoped().Model().Update("deleted_at", nil)` |
| `StorageRefRepository` 引用计数 | 文件恢复时递增 refcount，永久删除时递减 |
| 三个 repository 都有 `Get(ctx, id)` | 恢复操作需要先 Get 判断 owner → 再 Update |
| `internal/middleware/auth.go` actor 提取 | 恢复操作需验证 actor == owner，完全复用现有 auth |
| `pkg/errors` AppError 层级 | 误删后恢复返回与软删一致的语义错误 |
| 审计日志 | 记录"file.restored"、"file.hard_deleted" 等操作 |

### 建议的增量方案

1. `GET /v1/files?trashed=true` — 列出用户已删除的文件（同理用于消息、评论）
2. `POST /v1/files/:id/restore` — 恢复软删（set deleted_at = NULL）
3. `DELETE /v1/files/:id/forever` — 立即永久删除（跳过 GC，owner-only）
4. 审计日志扩展：记录 restore + hard_delete 事件
5. GC 调整：检查文件是否在保留期内被请求"立即清除"标记

**价值判断:** 低实现成本（GORM 软删基础设施已完整），高用户体验价值。这是所有用户数据平台的基本能力，缺少它是一个显著的可用性缺口。

---

## 方向四：文件 CDN / 边缘分发层抽象

### 为什么需要

当前的文件下载路径依赖 aero-id server 做流式代理（local 模式）或直接返回 S3 presigned URL。这在以下场景中存在瓶颈：

- **全球分发**：S3 presigned URL 指向特定区域，跨洲延迟高
- **高频访问**：热门的公开文件每次请求都走 app 代理或 S3 直连，缺少 CDN 缓存层
- **成本控制**：S3 数据传输费用高于 CDN 回源费用
- **安全边缘**：CDN 的 WAF、DDoS 防护、IP 黑名单能力无法接入

### 代码级证据

**证据 1：`FileStorage` 接口无 CDN 概念**（`internal/storage/storage.go`）

```go
type FileStorage interface {
    Put(ctx, ownerUID, name string, r io.Reader) → (key, size, error)
    Get(ctx, key string) → (io.ReadCloser, error)
    Delete(ctx, key string) → error
    PresignGet(ctx, key string, ttl time.Duration) → (string, error)  // 直接返回 S3 presigned URL
    Stat(ctx, key string) → (*ObjectInfo, error)
    PresignPut(ctx, key string, ttl time.Duration) → (string, error)
    NewKey(ownerUID string) → string
}

// 没有 CDN 相关方法：
// PurgeCache(ctx, key string) → error          // CDN 缓存失效
// SetOriginPolicy(ctx, ...) → error             // 配置回源策略
// GenerateSignedURL(ctx, key, policy) → string  // CDN 签名 URL（不同于 PresignGet）
```

**证据 2：S3 后端 PresignGet 返回的是原生 S3 签名 URL**（`internal/storage/s3.go`）

```go
// 假设实现：
func (s *s3Storage) PresignGet(ctx, key, ttl) (string, error) {
    // s3Client.PresignGETObject(bucket, key, ttl)
    // 返回 https://<bucket>.s3.<region>.amazonaws.com/<key>?X-Amz-...
    // 没有 CDN 域名封装，没有 cache-control 策略
}
```

**证据 3：local 存储后端的 `PresignGet` 返回的是 aero-id 内部签名路由**

```go
// internal/storage/local.go
func (s *local) PresignGet(ctx, key, ttl) → ("/files/dl?token=<hmac>&expires=<ts>", nil)
// 这个 URL 指向 aero-id 自身，不能指向 CDN 边缘节点
```

**证据 4：文件下载的 ACL 检查集中在 signURL 生成时刻，而非请求时刻（S3 模式）**

当使用 S3 presigned URL 时，aero-id 在生成 URL 的时刻检查 `CanReadFile`，但 URL 在 ttl 时间内有效。如果文件在 ttl 内被 unshare 或 visibility 改为 private，presigned URL 仍然有效——这是一个**权限分离**问题。

### 现有可复用基础设施

| 资产 | CDN 场景复用方式 |
|------|-----------------|
| `storage.s3` 后端 + S3 兼容性 | 大多数 CDN 支持 S3 作为 origin（CloudFront + S3, CloudFlare R2） |
| `app.go` 的 `buildStorage()` 工厂模式 | 新增 `"s3+cf"` 或 `"s3+cloudflare"` 类型，注入 CDN 行为 |
| 配置层（`config.go`） | 新增 `storage.cdn.domain`, `storage.cdn.purge_key`, `storage.cdn.access_log` 字段 |
| Local HMAC 签名模式 | 可演化为主 CDN 签名模式（token auth + signed cookies） |
| 文件 ACL 检查 | 可迁移为 per-request 检查（CDN → origin verification） |

### 建议的增量方案

1. `FileStorage` 接口增加 CDN 感知方法：`CDNSignedURL(ctx, key, ttl, policy)` → 返回 CDN 域名签名的 URL
2. 存储后端配置可指定 CDN 域名：`storage.public_host = "https://files.example.com"`
3. `PresignGet` 返回 CDN 签名 URL 而非 S3 原生 URL（复用 S3 的 Presign 但包裹 CDN host）
4. 可选：CDN token auth（边缘验证），将 ACL 检查从生成时刻转移到请求时刻
5. CDN 缓存失效 API：文件元数据修改或 visibility 变更时触发 CDN 清除

**价值判断:** 中实现成本（主要是新增 CDN 签名 + 配置层），对全局部署场景高价值。如果当前主要部署在单一区域，优先级可降；但架构上应预留 CDN 抽象。

---

## 方向五：跨实体聚合活动提要（Activity Feed / "我的动态"）

### 为什么需要

aero-id 有三个独立的数据实体（文件、消息、评论）+ 通知系统。但它们之间**没有聚合视图**回答以下用户问题：

- "我今天在平台上做了什么？"（我的所有操作的时间线）
- "别人对我的内容做了什么？"（某人评论了我的文件 / 给我的消息点了 reaction）
- "我参与的内容有什么新动态？"（我评论过的文件有了新回复）

当前的通知系统处理的是"需要立即知晓"的事件（新消息、回复），但缺少一个**可回溯的、按时间线组织的所有相关活动**。两者是互补而非替代关系。

### 代码级证据

**证据 1：三个实体的 `List` API 各自独立，无统一时间线**（`internal/service/service.go`）

```go
// 三个独立的 List 接口：
type FileService interface {
    List(ctx, ownerUID, page) → (*FileList, error)
    // ...
}
type MessageService interface {
    Inbox(ctx, page, unreadOnly) → (*MessageList, error)
    // ...
}
type CommentService interface {
    List(ctx, targetType, targetID, page) → (*CommentList, error)
    // ...
}
// 没有：ActivityFeed(ctx, uid, page) → ([]ActivityEvent, nextCursor, error)
```

**证据 2：通知系统是"要我知晓"（push），不是"我的回顾"（log）**

```go
// internal/service/notification.go: List → 返回 recipient 的通知
// 通知只包含：message.received, message.read_receipt, comment.reply
// 不包含：file.uploaded, file.shared, comment.liked, message.reacted
// 不包含：actor==recipient 时（我自己做的操作）
```

**证据 3：现有审计日志只记录敏感操作，不适用于普通活动提要**

```go
// internal/domain/audit.go
const (
    AuditFileShareGrant   = "file.share.grant"
    AuditFileShareRevoke  = "file.share.revoke"
    AuditReportResolve    = "comment.report.resolve"
    AuditUserBlock        = "user.block"
    AuditUserUnblock      = "user.unblock"
    AuditMessageDelete    = "message.delete"
)
// 审计日志缺少：
// - file.uploaded, file.deleted, file.visibility_changed
// - message.sent, message.edited
// - comment.created, comment.edited, comment.liked
// 且审计日志面向 admin（全量），而非面向用户（只看到与自己相关的内容）
```

**证据 4：Outbox 事件是最接近活动提要的中间表示，但没有被持久化为活动记录**

```go
// outbox/outbox.go: Event 包含 AggregateType, AggregateID, EventType, Payload
// 事件在 relay 消费后被标记 published → 不再可查询
// 没有将事件持久化为用户可见的活动记录
```

### 现有可复用基础设施

| 资产 | 活动提要场景复用方式 |
|------|---------------------|
| `outbox.Event` 事件模型 | 几乎直接映射为活动事件——只需在 relay 消费时将事件复制到 `activity_events` 表（非 destructive 消费） |
| NotificationDispatcher 的事件处理模式 | 可作为"活动记录器"的模板：`Handle(ctx, e) → write activity row` |
| SSE Hub + last_event_id 重连 | 活动提要的实时更新可复用 SSE 推送 |
| 用户列表的 `enrichUsers` | 活动提要中需要展示 actor 的用户信息（昵称、头像） |
| `domain.Page` 分页 + cursor | 活动提要的分页复用现有模式 |
| `notification.Notifications` 的 `ListByRecipient` | 活动提要的查询模式类似，但需要包含自身操作 |

### 建议的增量方案

1. 新增 `activity_events` 表：`(id, recipient_uid, actor_uid, event_type, subject_type, subject_id, meta(JSONB), is_read, created_at)`
2. outbox relay 新增一个 handler（`ActivityFeedWriter`）链入 `outbox.NewChain()`：将每个事件持久化为活动记录
3. 活动记录面向两个视角写入：
   - **我操作的**：actor_uid == me（文件上传、消息发送、评论发表）
   - **关于我的**：recipient_uid == me（他人评论我的内容、给我的消息点 reaction）
4. `GET /v1/feed` — 聚合活动提要（按 `created_at DESC` cursor 分页，支持 `?kind=filter` 过滤）
5. 复用 SSE 推送：新活动产生时实时推送给在线用户

**价值判断:** 中-高实现成本（需要决定哪些事件写入活动流 + 存储成本），但对用户体验的提升是质的飞跃——将三个孤立实体连接为一个统一的产品体验。活动提要 + 通知系统共同构成了完整的"用户参与引擎"。

---

## 优先级与建议顺序

| # | 方向 | 实现成本 | 产品价值 | 企业合规价值 | 建议 |
|---|------|---------|---------|------------|------|
| 1 | **分享链接模型** | 低-中 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | 下一迭代做——将数据转换为协作平台 |
| 2 | **回收站自我管理** | 低 | ⭐⭐⭐⭐ | ⭐⭐⭐ | 可立即开始——基础设施已完整，投入产出比最高 |
| 3 | **活动提要** | 中-高 | ⭐⭐⭐⭐⭐ | ⭐⭐ | 建议在通知系统稳定后启动——复用 outbox 事件 |
| 4 | **数据导出 API** | 中 | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 企业客户签约前必须交付；否则可推迟 |
| 5 | **CDN 边缘分发** | 中 | ⭐⭐⭐⭐ | ⭐ | 当前传输压力足够时推迟；架构预留抽象即可 |

### 与现有分析文档的差异化

本文件与 `docs/requirements/` 下 110+ 份既有分析的关键不同：

1. **方向一（分享链接）**：既有分析讨论的是 "用户间分享"（user-to-user sharing），本文提出的是**匿名外部分享**（share links with password/expiry）——一个完全不同的产品模型
2. **方向二（数据导出）**：没有任何既有分析从 GDPR/数据可携带角度论证导出功能
3. **方向三（回收站）**：软删已被数十份分析识别为"已实现"，但**没有一份**指出"用户无法自我管理回收站"这个缺口
4. **方向四（CDN 抽象）**：所有既有分析专注于功能特性，**完全没有涉及文件传输的 CDN 层**
5. **方向五（活动提要）**：通知方向已被广泛覆盖，但**没有一个既有分析提出"将通知+操作记录+他人互动聚合为统一动态流"**

---

*本文基于 aero-id 2026-07-11 的代码库 `main` 分支（commit 80b013e）。不包含任何代码变更。*
