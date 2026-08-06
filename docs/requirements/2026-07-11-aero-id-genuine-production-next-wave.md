# aero-id — 生产级代码扫描：下一波高价值扩展方向（2026-07-11）

> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码库通读（`internal/` 全部 Go 包、22 份可逆迁移、`api/proto/`、`cmd/`、`configs/`、`deployments/`、`Makefile`）。每个方向提供精确代码级证据（文件名 + 关键逻辑），不编写代码。  
> **前提**: 代码库已达到极高成熟度 —— 双传输完全对等（HTTP 36+ 路由 + gRPC 41 RPC）、事务性 outbox、SSE 实时推送、全文搜索、SSO 断路器 + stale-cache + mTLS、storage GC、审计日志、角色权限、21 份需求分析阶段已论证的方向 **均已完成交付**。

---

## 代码库当前能力全景

| 领域 | 当前状态 |
|------|---------|
| **传输** | HTTP Gin + gRPC 完全对等，SSE 实时推送含 last_event_id 重连回放，HMAC 签名下载 |
| **文件** | 流式上传 + S3 presigned 直传 + SHA-256 内容去重 + 原子引用计数 + MIME/Size 校验 + 显式分享/撤销 + 存储 GC（软删清除 + 孤儿扫描）+ 元数据 PATCH |
| **消息** | 1:1 私信 + Reactions + 编辑/软删 + 已读回执 + 未读计数 + Inbox/Conv 视图 + 系统消息 + 全文搜索（FTS tsvector + CJK ILIKE 回退）|
| **评论** | 两层嵌套 + Like/Unlike + 原子计数 + 举报→审核→隐藏/显示 + 编辑 + 原子回复计数 |
| **通知** | Outbox 驱动 + SSE 实时推送 + 折叠去重（5min 窗口）+ 偏好设置（启用/静默时段）+ Block 抑制 + 限速 + Webhook HMAC 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN）+ CJK ILIKE 回退 + 多 kind 联合搜索 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 + 幂等键（DB 持久化 + 24h TTL）+ 双传输限流 + SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning + 冷启动 warmup + 优雅关停 + 最后 drain |
| **治理** | 方向性 Block + 审计日志（append-only）+ Admin 角色（4 权限）+ RequirePermission 中间件 |
| **基础设施** | 迁移 CLI + 自动迁移选项 + 22 份可逆迁移 + OTel 追踪 + Prometheus RED 指标 |

---

## 方向一：复杂对话模型 —— 群组消息与频道抽象

### 为什么做

当前消息模型是严格的 **1:1 私信**。`messages` 表通过 `(from_uid, to_uid)` 二元组标识一条消息，`Conversation` 端点实际上是对 `(actor, with_uid)` 的对称查询。没有 `conversations` 表，没有群组概念，没有成员列表。

但代码库中已经存在大量为多参与方消息准备的 **基础设施**：

| 基础设施 | 当前用途 | 群组消息复用点 |
|----------|---------|---------------|
| `messages.kind` 枚举 | 保留 `System`(4) 等 kind | 增加 `GroupMessage` kind → 路由到群组逻辑 |
| `messages.meta` JSONB | 存储额外元数据 | 存储 `group_id`, `mention_uids` |
| Outbox relay + NotificationDispatcher | 发送 `message.sent` 事件 | 群组消息分发 → 为每个成员创建 notification |
| SSE Hub | 实时推送通知 | 群组成员实时收到新消息推送 |
| Block 系统 | 抑制 1:1 消息 | 群组内屏蔽某成员的消息提醒 |
| `Comment` 系统 | 评论支持 `target_type`/`target_id` 多态 | 可对群组消息做评论 |

### 代码级证据

**证据 1：当前消息模型严格 1:1，无群组抽象**

```
internal/domain/message.go
  type Message struct {
      FromUID string     // 发送者
      ToUID   string     // 接收者 —— 只能是一个 uid
      Kind    MessageKind
      ...
  }
  // 没有 GroupID 字段，没有 conversation_id 概念
```

**证据 2：Conversation 查询是对称的 1:1**

```
internal/message/repository.go
  // ListConversation(ctx, actor, withUID, page) — 只有两个参与者
  // 查询: (from_uid=? AND to_uid=?) OR (from_uid=? AND to_uid=?)
```

**证据 3：`messages.kind` 已预留扩展空间**

```
internal/domain/shared.go
  const (
      MessageKindText   MessageKind = 1
      MessageKindImage  MessageKind = 2
      MessageKindFile   MessageKind = 3
      MessageKindSystem MessageKind = 4
  )
  // Kind 5+ 可用于 GroupMessage, ChannelPost 等
```

**证据 4：NotificationDispatcher 已实现多接收方分发模式**

```
internal/notification/dispatcher.go
  // 当前每个 event 对应一个 notification（一个 recipient）。
  // 群组场景：一个 message.sent event → N 个 notification rows
  // 只需在 dispatcher 中迭代群组成员即可。
```

### 建议方案

1. **`conversations` 表**（新迁移）：`id, type(1:1|group|channel), name, avatar, created_by, created_at, archived_at`
2. **`conversation_members` 表**：`conversation_id, uid, role(admin|member), joined_at, last_read_at`
3. **消息模型进化**：`messages.conversation_id`（替代 `to_uid` 语义）+ `messages.sender_uid`（替代 `from_uid`）
4. **群组消息发送**：`POST /v1/conversations/:id/messages` → 1 条插入 + N 条 notification（per member）
5. **实时推送**：现有 SSE Hub 直接支持群组成员的消息推送

### 影响评估

- **业务价值**: 高 —— 从 1:1 私信到群组/频道是最自然的产品演进
- **工程复杂度**: 中 —— 新表 + 迁移 + service 逻辑，但基础设施全部就位
- **向后兼容**: 可逐步引入 —— 现有 1:1 消息自然继承为 2 人 conversation

---

## 方向二：SSO 合约进化 —— 用户数据联合查询与事件驱动同步

### 为什么做

当前 aero-id 与 SSO 的接口仅有两个 RPC：`ValidateToken`（验证 token 换取 uid）和 `GetUsers`（批量查询用户昵称/头像）。随着 aero-id 功能膨胀（通知、搜索、共享、管理后台），这个最小合约正在成为瓶颈。

实际代码中暴露的痛点：

| 痛点 | 影响 | 代码证据 |
|------|------|---------|
| 每个列表响应都需要额外调用 SSO 解析用户信息 | 延迟叠加，缓存穿透 | `service/service.go:enrichUsers()` 在**每个** List 端点被调用 |
| SSO 不提供用户搜索 | 搜索命中只能返回 uid，前端不可用 | `search/*.go` 搜索结果不含用户信息 |
| SSO 不提供用户在线状态 | 实时推送场景无法展示"在线" | `hub/hub.go` SSE 推送里含 `ActorUID` 但无状态 |
| SSO 不推送用户变更事件 | 用户改名/删号 aero-id 侧数据孤立 | 无 `UserUpdated`/`UserDeleted` 事件通道 |
| 只有 uid 没有用户类型 | 无法区分"真实用户"vs"bot"vs"系统" | `SystemSenderUID = "system"` 是硬编码字符串 |

### 代码级证据

**证据 1：每个列表端点都做 SSO enrich**

```
internal/service/comment.go
  func (s *commentService) List(...) (*CommentList, error) {
      ...
      users, err := enrichUsers(ctx, s.dir, authorUIDs(cs))  // ← 每次列表请求
      ...
  }

internal/service/message.go
  func (s *messageService) Inbox(...) (*MessageList, error) {
      ...
      users, err := enrichUsers(ctx, s.dir, senderUIDs(msgs))  // ← 同一模式
      ...
  }

internal/service/notification.go
  func (s *notificationService) List(...) (*NotificationList, error) {
      ...
      users, err := enrichUsers(ctx, s.dir, actorUIDs(ns))  // ← 同一模式
      ...
  }

internal/service/service.go
  func enrichUsers(ctx context.Context, dir UserDirectory, uids []string) (map[string]sso.User, error) {
      return dir.GetUsers(ctx, uids)
  }
```

每次 GetUsers 调用经过 `user_cache`（TTL 5min）→ `singleflight` → gRPC 请求。在列表页面频繁翻页的场景下，缓存穿透仍有 SSO 压力。

**证据 2：SSO proto 仅含最简用户模型**

```
api/proto/sso/v1/sso.proto
  service SSO {
      rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);
      rpc GetUsers(GetUsersRequest) returns (GetUsersResponse);
  }

  message User {
      string uid      = 1;
      string nickname = 2;
      string avatar   = 3;  // ← 仅有 3 个字段
  }

  // 缺乏：email, is_bot, is_active, last_seen, locale, timezone
```

**证据 3：用户变更无法同步，导致数据脏读**

```
internal/domain/actor.go
  const SystemSenderUID = "system"  // 唯一的"特殊用户"
  // 没有从 SSO 同步用户删除/lifecycle 事件的机制
  // 如果 SSO 删除了一个用户，aero-id 里的消息/文件/评论/通知仍然引用其 uid
  // 审计日志中的 actor_uid 也无法反解
```

**证据 4：服务层缓存基础设施已就位，可承载更丰富数据**

```
internal/sso/token_cache.go  → TTL + 过期清理 + serve-stale 窗口
internal/sso/user_cache.go   → TTL + singleflight + 批量查询
internal/sso/breaker.go      → 断路器保护 SSO 后端
internal/sso/client.go       → 重试 + 退避 + keepalive + 指标
```

这套缓存 + 容错基础设施已超过大多数微服务对 downstream 依赖的保护水平，但上层数据模型（SSO proto）没有充分利用。

### 建议方案

1. **SSO proto 扩展**：`GetUsersResponse.User` 增加 `display_name`, `email`, `is_bot`, `is_active`, `locale`, `last_seen_at`
2. **批量用户搜索 RPC**：`SearchUsers(query, limit)` → 可供 aero-id 搜索页面用于"@提及"用户
3. **用户事件通道**：SSO 通过 outbox/webhook 推送 `user.updated`, `user.deleted`, `user.suspended` 事件 → aero-id 可清理/更新引用
4. **用户状态缓存层**：在 user_cache 之上增加 `UserPresence`（在线/离线/离开），通过 Redis pub/sub 或 SSO gRPC 流推送
5. **离线消息补推**：当用户重新上线时，通过 SSO 的 `last_seen_at` 计算未推送的事件，做一次回补

### 影响评估

- **业务价值**: 高 —— 每个列表页面的用户体验改善 + 运营能力（管理用户、清理孤立数据）
- **工程复杂度**: 中 —— proto 扩展向后兼容；事件通道可复用现有 outbox/webhook 机制
- **依赖**: 需要 SSO 团队配合（proto 变更 + 服务端实现）

---

## 方向三：管理员控制平面 —— 角色/权限/审核 API 的全面外露

### 为什么做

当前 admin 能力严重受限，与实际投入生产的运营需求之间存在巨大落差。具体来说：

| 能力 | 当前状态 | 代码证据 |
|------|---------|---------|
| 角色/权限系统 | 数据库中有 `roles` + `user_roles` 表 + 种子数据 | `migrations/000022_admin_roles.up.sql` |
| 权限检查中间件 | `RequirePermission()` 已实现 | `internal/middleware/permission.go` |
| 审计日志读取 | 仅有 `GET /admin/audit-log`（HTTP-only，gRPC 无对应） | `internal/handler/http/admin.go` |
| 评论全局审核 | ❌ 无全局 moderation queue API | 评论审核当前是 **作者自审**（`comment_author_scoped`） |
| 用户封禁 | 角色表定义了 `user.ban` 权限但 **无 API** | `domain/admin.go` 含 `PermUserBan` 常量但无端点 |
| 角色管理 | 角色无法通过 API 授予/撤销 | 种子数据仅通过迁移文件插入 |
| 系统配置 | `system.config` 权限定义但无从修改 | `domain/admin.go` 含 `PermSystemConfig` 常量但无实现 |

### 代码级证据

**证据 1：角色存在数据库里，但没有任何 API 可以管理它**

```
migrations/000022_admin_roles.up.sql
  CREATE TABLE roles (
      code        TEXT PRIMARY KEY,
      name        TEXT NOT NULL,
      permissions TEXT[] NOT NULL DEFAULT '{}',
      created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  CREATE TABLE user_roles (
      uid         TEXT NOT NULL,
      role_code   TEXT NOT NULL REFERENCES roles(code),
      granted_by  TEXT NOT NULL DEFAULT '',
      created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
      PRIMARY KEY (uid, role_code)
  );
  INSERT INTO roles (...) VALUES ('moderator', ...), ('admin', ...);

  // ⚠️ 但 internal/handler/http/admin.go 只有一条路由：
  r.Auth.GET("/admin/audit-log", h.ListAudit)
  // 没有 POST /admin/roles, 没有 POST /admin/user-roles, 没有 DELETE
```

**证据 2：权限常量定义了但无调用者**

```
internal/domain/admin.go
  const (
      PermContentModerate = "content.moderate"
      PermAuditRead       = "audit.read"
      PermUserBan         = "user.ban"
      PermSystemConfig    = "system.config"
  )

// 只有 PermAuditRead 被实际使用（在 admin.go handler 中）
// PermUserBan 和 PermSystemConfig 没有任何消费代码
$ grep -rn "PermUserBan\|PermSystemConfig\|PermContentModerate" internal/ --include="*.go"
// 只有 domain/admin.go 中的常量定义，没有引用
```

**证据 3：审核中间件已就位但无路由使用**

```
internal/middleware/permission.go
  func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
      // 检查 uid 持有 perm 权限
  }
  // 当前仅 admin.go 的 ListAudit 使用了权限检查（硬编码 audit.read）
  // 没有任何路由挂载 RequirePermission(PermContentModerate)
```

**证据 4：评论审核是作者自审，缺少全局 moderation 视角**

```
internal/service/comment.go
  func (s *commentService) ListReports(ctx, commentID, status, page) {
      c, _ := s.comments.Get(ctx, commentID)
      if c.OwnerUID != actor {
          return nil, apperr.Forbidden(...)  // ← 只有作者能看自己评论的举报
      }
  }
  // 审核员无法跨用户查看所有待处理的举报
```

### 建议方案

1. **角色管理 API**：
   - `GET /v1/admin/roles` — 列出所有角色及权限
   - `POST /v1/admin/user-roles` — 为用户授予角色
   - `DELETE /v1/admin/user-roles/:uid/:role` — 撤销用户角色
   - `GET /v1/admin/users?role=...` — 按角色查询用户

2. **全局 Moderation Queue**：
   - `GET /v1/admin/moderation-queue?status=open` — 所有待处理的举报（跨用户）
   - `POST /v1/admin/moderation-queue/:id/resolve` — 审核员可以 resolve（不再限制为评论作者）
   - `POST /v1/admin/comments/:id/hide` — 全局隐藏/显示（需要 `content.moderate`）

3. **用户封禁 API**：
   - `POST /v1/admin/users/:uid/ban` — 封禁用户（需要 `user.ban`）
   - `POST /v1/admin/users/:uid/unban` — 解封
   - `GET /v1/admin/banned-users` — 列出被封禁用户
   - 封禁效果：拦截该用户的消息发送、评论创建、文件上传

4. **系统配置端点**：
   - `GET /v1/admin/config` — 读取运行时配置（只读）
   - `PUT /v1/admin/config` — 修改配置项（需要 `system.config`）
   - 配置项可包括：注册开放/关闭、上传上限动态调整、通知默认阈值

### 影响评估

- **业务价值**: 中-高 —— 运营团队无法管理用户/内容是一个真实的生产缺口
- **工程复杂度**: 低-中 —— 大部分基础设施（表、中间件、权限常量）已存在，仅需暴露 API
- **优先级**: P1 —— 上线前至少需要角色管理和 moderation queue

---

## 方向四：数据生命周期管理 —— 自动过期、归档与合规导出

### 为什么做

aero-id 定位为"account-owned data service"（用户数据服务），但当前对数据的 **生命周期终点** 几乎不做管理：

| 数据类型 | 创建 | 读取 | 删除 | 自动过期 | 批量导出 | 合规清理 |
|----------|------|------|------|---------|---------|---------|
| 文件 | ✅ | ✅ | ✅（软删） | ✅（GC 回收软删文件） | ❌ | ❌ |
| 消息 | ✅ | ✅ | ✅（软删） | ❌ | ❌ | ❌ |
| 评论 | ✅ | ✅ | ✅（软删） | ❌ | ❌ | ❌ |
| 通知 | ✅ | ✅ | ✅（MarkRead） | ❌（累积无限） | ❌ | ❌ |
| Outbox 事件 | ✅ | ✅ | ✅（published_at） | ❌（published 后永不清除） | ❌ | ❌ |
| 审计日志 | ✅ | ✅（admin） | ❌ | ❌（append-only 永久增长） | ❌ | ❌ |

### 代码级证据

**证据 1：没有针对消息/评论/通知的 retention 策略**

```bash
$ grep -rn "retention\|expir\|auto.*delete\|ttl" internal/ --include="*.go" | grep -vi "token_cache\|user_cache\|idempot\|key.*ttl\|cache.*ttl\|stale_ttl\|breaker_cooldown\|max_retries\|backoff\|cleanup\|idle_ttl\|presign\|min_orphan\|conn_max"
# 结果：没有。只有存储 GC 有 retention（file-only）
```

唯一的数据 retention 是 storage GC 对**文件软删记录**的清除（`storage.gc.retention: 168h`）。消息、评论、通知、审计日志、已发布的 outbox 事件 **永不清除**。

**证据 2：审计日志 append-only 且无限增长**

```
internal/domain/audit.go
  type AuditEvent struct {
      ...
      Meta map[string]any
      CreatedAt time.Time
  }
  // 没有过期/归档策略。每条文件分享、举报处理、消息删除都产生一条记录。
  // 在高流量生产环境中，几周内可积累数百万行。
```

**证据 3：已发布的 outbox 事件永不清除**

```
internal/outbox/repository.go
  // MarkPublished 只设置 published_at，不删除行。
  // 迁移 000008 的建表语句也没有计划删除策略。
  // 已发布的事件（尤其是死信）占用存储且无法通过 API 查询。
```

**证据 4：没有数据导出能力（GDPR / 用户数据可移植性）**

```
internal/ -- 没有任何与"export"或"gdpr"相关的代码
// 用户无法下载自己的所有数据（消息历史、文件列表、评论记录）
// 也没有用户注销→清理所有关联数据的流程
```

**证据 5：通知表缺少清理，未读通知列表会越来越慢**

```
migrations/000017_notification_indexes.up.sql
  CREATE INDEX idx_notifications_recipient_created
      ON notifications(recipient_uid, created_at DESC);
  // 这个索引会随通知行数线性增长。虽然 B-tree 是 O(log n)，
  // 但 1 亿行 + 高并发写入的维护代价不可忽略。
  // 缺少：旧已读通知的自动归档或硬删策略。
```

### 建议方案

1. **消息 Retention 策略**：
   - 支持两种模式：`retain_forever`（默认，当前行为）和 `auto_delete_after_days`
   - 可选在 conversation 级别设置（`conversations.retention_days`）
   - 后台 job（复用 sweeper/relay 模式）扫描过期会话并硬删消息
   - 硬删前触发 outbox 事件 `message.expired`（通过 webhook 通知外部）

2. **通知归档**：
   - 已读超过 N 天的通知自动移至 `notification_archive` 分区表
   - 未读通知保留在热表（`notifications`）以保持查询性能
   - `MarkAllRead` 可选触发归档

3. **审计日志 Rotation**：
   - 审计日志 TTL（默认 90 天，可配置）
   - 周期性 job 将过期记录转储到低成本存储（S3/对象存储）并软删
   - 转储格式为 JSON Lines 或 Parquet，支持外部分析工具读取

4. **用户数据导出**：
   - `POST /v1/export` — 创建数据导出任务（异步，outjob pattern）
   - 任务收集用户的消息、文件列表、评论、通知 → 打包为 ZIP
   - 完成后通知用户（通过现有通知系统）并提供下载链接（使用文件系统的 presigned URL）
   - TTL 到期（7天）后自动删除导出产物

5. **用户注销/数据删除流程**：
   - `POST /v1/account/delete` — 发起注销请求
   - 异步 job 级联删除：消息（作为发送方和接收方）→ 评论 → 文件（物理删除 blob）→ 通知 → 审计事件脱敏
   - 完成后再触发 outbox 事件通知 SSO

### 影响评估

- **业务价值**: 高 —— 面向合规（GDPR/CCPA）、运维成本（存储膨胀）、用户信任（数据控制权）
- **工程复杂度**: 中 —— 可增量实施：先做导出，再做 retention，最后做 account deletion
- **优先级**: P1 —— 数据导出在用户提需求前就应在路线图上

---

## 方向五：存储层进化 —— 多区域、缓存、CDN 与带宽管理

### 为什么做

文件是 aero-id 的核心资产（数据模型中文件表是第一张创建的，`000001_create_files`）。当前存储层虽已具备本地+S3 双后端、content-dedup、presigned 直传、GC 清扫等能力，但在**分布式的文件交付**上存在显著缺口。

### 代码级证据

**证据 1：文件下载全部走单一后端，无 CDN 层**

```
internal/storage/storage.go
  type FileStorage interface {
      Put(ctx, ownerUID, name, io.Reader) → (key, size, err)
      Get(ctx, key) → (io.ReadCloser, err)
      Delete(ctx, key) → error
      Stat(ctx, key) → (ObjectInfo, error)
  }
  // 没有 GetURL(ctx, key) → 返回 CDN/pre-signed URL
  // 对于 local 后端，下载走 aero-id 进程本身（带宽受限）
  // 对于 S3 后端，DownloadURL handler 生成 presigned URL
```

```
internal/file/handler_http.go
  func (h *FileHandler) DownloadURL(c *gin.Context) {
      url, err := h.files.DownloadURL(c.Request.Context(), id, ttl)
      // 对于 S3：生成 S3 presigned URL → 客户端直连 S3
      // 对于 local：生成 HMAC-signed URL → 走 aero-id 进程
  }
  // 缺少：CloudFront / CloudFlare / Akamai 签名 URL
  // 缺少：地理位置感知的路由（用户从亚洲下载，从亚洲区域提供）
```

**证据 2：纯本地模式没有可扩展性**

```
internal/storage/local.go
  func (s *Local) Get(ctx, key) (io.ReadCloser, error) {
      // 从本地磁盘读取文件
      // 单机瓶颈：磁盘 I/O、网络带宽、存储容量
  }
  // 多副本部署时，文件只存在于写入了的实例上
  // 没有 NFS / 分布式文件系统挂载，回源请求打到错误实例则失败
```

**证据 3：存储配置是启动时静态选择的，无法按文件分区**

```
internal/app/app.go
  func buildStorage(ctx, cfg) (FileStorage, SignedDownloader, error) {
      switch cfg.Storage.Type {
      case "local": return local, local, nil
      case "s3":     return s3, nil, nil
      }
  }
  // 存储后端全局单一选择，无法：按用户地域路由、按文件大小选择、按访问频率 tier
```

**证据 4：没有带宽管理 / 下载限速**

```
internal/middleware/ratelimit.go
  // 限流器按 uid 或 IP 限制请求速率
  // 但是**仅限请求速率**，无法控制大文件下载的带宽消耗

internal/storage/local.go
  // local.Get() 直接 io.Copy → 不做速率限制
  // 一个用户发起 10 个并发大文件下载可耗尽上行带宽
```

### 建议方案

1. **CDN 集成层**：
   - `FileStorage` 接口新增 `SignedURL(ctx, key, ttl) → (url, error)` 方法
   - CDN 实现：生成 CloudFront/CloudFlare 签名 URL（配置域名 + 密钥对）
   - Local 后端：继续走 HMAC-signed aero-id 进程提供
   - 多区域 S3：通过 S3 跨区域复制 + 区域签名 URL

2. **存储分层（Tiered Storage）**：
   - 近期（< 30天）文件 → 本地 SSD / S3 Standard
   - 中期（30-365天）文件 → S3 Standard-IA
   - 远期（> 365天）文件 → S3 Glacier Deep Archive
   - 后台 job 扫描 `files.updated_at`，按文件年龄 + 最后访问时间自动迁移
   - `files.storage_tier` 列（hot/warm/cold）记录当前层级

3. **带宽管理与 QoS**：
   - `storage.download_bandwidth_per_user` 配置项（bytes/sec）
   - 在 download handler 中实现 `rate.Limiter`（`golang.org/x/time/rate`）
   - 付费用户 + 免费用户不同带宽配额（为未来商业化预留）

4. **多区域部署**：
   - S3 多区域存储 + 区域前缀路由
   - `files.storage_region` 列记录文件所在区域
   - 下载时根据 `X-Forwarded-For` 或 GeoIP 选择最近区域的 presigned URL
   - 写操作始终写入主区域，通过 S3 跨区域复制同步

### 影响评估

- **业务价值**: 中-高 —— 直接影响用户文件下载速度体验 + 运营成本优化
- **工程复杂度**: 高 —— 涉及存储接口改变、数据迁移、基础设施协作
- **优先级**: P2 —— 在用户量达到稳定规模后做，前期 local+S3 已够用

---

## 汇总对比

| # | 方向 | 业务价值 | 工程复杂度 | 优先级 | 核心依赖 |
|---|------|---------|-----------|--------|---------|
| 1 | 群组消息与频道 | 高 | 中 | P1 | 无外部依赖，纯后端开发 |
| 2 | SSO 合约进化 | 高 | 中 | P0-P1 | 需要 SSO 团队协调 |
| 3 | Admin 控制平面 | 中-高 | 低-中 | P1 | 基础设施已就位，仅需 API 暴露 |
| 4 | 数据生命周期管理 | 高 | 中 | P1 | 合规驱动，需产品策略 |
| 5 | 存储层进化 | 中-高 | 高 | P2 | 需要 CDN/基础设施支持 |

**推荐执行顺序**：
- **立即**（P1）：方向三（Admin 控制平面）—— 基础设施已存在，最快投产
- **短期**（P1）：方向一（群组消息）+ 方向四（数据生命周期，先从导出做起）
- **中期**（P1）：方向二（SSO 合约进化）
- **长期**（P2）：方向五（存储层进化）
