现在我已全面掌握代码库的上下文。以下是我的架构分析。

---

# 架构分析：aero-id 五个高价值扩展方向

## 1. 架构评估

### 1.1 当前架构优势

aero-id 的架构成熟度在同等规模的服务中表现突出，值得肯定的关键设计决策：

| 优势 | 证据 | 价值 |
|------|------|------|
| **严格单向分层** | `handler → service → repository/storage/sso`，无循环依赖 | 每个方向的新增代码都能精确插入对应层，不会"不知道放哪" |
| **双传输共享 service** (ADR-001) | HTTP 和 gRPC handler 调用同一组方法签名 | 五个方向的 API 都只需实现一次业务逻辑，两边自动获得 |
| **事务性 Outbox** (ADR-003) | 写操作与事件发布在同一个 DB 事务内 | 方向二（导出）、方向五（动态）都可复用同一事件溯源机制 |
| **统一错误模型** | `AppError` 跨 HTTP/gRPC 渲染 | 新端点获得一致错误处理，无需额外工作 |
| **SSO 客户端弹性** | circuit breaker + serve-stale + retry/backoff | 方向一（匿名分享无需 token 校验）之外的场景直接受益 |

### 1.2 当前架构的五个局限性

这些局限直接对应到您识别的五个方向：

**局限 ① 读写 ACL 模型过短**
`domain.CanReadFile` 仅有的两种状态（owner / public）是二值逻辑。没有"具有时效的授权"、"密码保护"、"操作者身份未知"等中间状态。`file_shares` 表也没有 `share_token` / `expires_at` / `password_hash` / `max_access_count` 等列。

→ **方向一**（匿名外部分享）必须重构 ACL 模型。

**局限 ② 软删除是基础设施而非用户功能**
`gorm.DeletedAt` 对所有实体都是开发层面的软删除约定，但不是用户可见的功能。没有：
- `Restore` API
- `?trashed=true` 过滤参数
- "永久删除"端点
- 回收站 TTL 自动清理

→ **方向三**（回收站）需要将基础设施层面的软删除提升为用户功能。

**局限 ③ 存储层是传输终结者**
`storage.FileStorage` 接口的 `PresignGet` 返回最终 URL，而非可被 CDN 包裹的中间 URL。没有 `PurgeCache`、`SetCDNDomain`、`WrapWithCDN` 等方法。

→ **方向四**（CDN）需要存储层接口的横向扩展。

**局限 ④ 事件流无持久消费者视图**
outbox 事件在 relay 消费后标记 `published_at`，之后不可查询。没有"活动表"来持久化用户可见的聚合事件序列。

→ **方向五**（活动提要）需要从 outbox 的"投递即忘"模式扩展到"投递 + 持久化"。

**局限 ⑤ 无跨实体聚合接口**
三个实体（File / Message / Comment）各有独立的 `ListByOwner`，但没有统一的 `ExportByOwner` 或 `TimelineByOwner`。

→ **方向二**（GDPR 导出）和方向五（活动提要）都需要聚合层。

### 1.3 架构债务

| 债务项 | 位置 | 影响 | 建议忍耐度 |
|--------|------|------|-----------|
| **GC 引用计数竞态** | `file/service_crud.go` 的去重逻辑与 GC sweeper 之间有窗口 | 可能误删 live blob | 低 — 可在方向一或四的实现中顺手修复 |
| **SSE 无 last_event_id** | `hub/hub.go` | 重连丢失通知 | 低 — 方向五之前修复即可 |
| **per-instance 限流** | `pkg/ratelimit` | 多副本时有效容量膨胀 | 中 — 若马上要扩容则优先处理 |

---

## 2. 扩展方向分析

### 方向一：匿名外部分享链接（分享链接 → 匿名外部分享）

#### 为什么需要

当前 `file_shares` 要求分享对象必须是已知 uid（SSO 用户）。这在以下场景中完全不可用：
- 向外部合作伙伴（无 SSO 账号）分享文件
- 生成可嵌入网页的公开下载链接
- 创建有时效性的限次分享（"24 小时后过期"）

#### 核心挑战

1. **ACL 模型扩展**：`CanReadFile` 必须从二值逻辑扩展为多级判定链
2. **匿名身份**：下载者没有 `actor_uid`（`/files/dl` 已支持无 token 访问），但需要按 `share_token` 判定权限
3. **密码保护**：如果支持密码，密码哈希的存储和验证该在哪一层？service 层负责校验，但 HMAC token 本身已经可以承载"一次性"语义
4. **与现有分享的互斥**：一个文件可以同时有 uid-based 分享和 token-based 分享吗？建议可以——两个是不同维度

#### 架构变更

```
当前 ACL 判定链：
  actor == owner? → 是 → allow
  visibility == public? → 是 → allow
  → deny

新 ACL 判定链：
  actor == owner? → 是 → allow
  visibility == public? → 是 → allow
  share 表有 actor 的记录? → 是 → allow
  share_token 有效（未过期、未超次、密码正确）? → 是 → allow
  → deny
```

#### 文件变更范围

| 影响区域 | 变更类型 |
|----------|---------|
| `domain/access.go` | 重构 `CanReadFile` → 引入 `AccessChecker` 接口 |
| `domain/file_share.go` | 新增 `ShareLink` 实体（token, expires_at, password_hash, max_access_count, access_count） |
| `repository/file_share.go` | 新增 `share_links` 表 + CRUD |
| `service/file_presign.go` | `DownloadURL` 扩展为支持 share_token |
| `service/file_share.go` | 新增 `CreateShareLink` / `RevokeShareLink` / `ListShareLinks` |
| `storage/storage.go` | 无变更（存储层不关心 ACL） |

#### 对 AGENTS.md 约束的影响

- `domain/access.go` 当前约 30 行，扩展后约 80-100 行 → ✅ 不超 500
- `service/file_share.go` 当前约 200 行，加上 share link 逻辑约 350 行 → ✅ 不超 500
- 但如果把所有 share 逻辑放进一个文件，可能在 400 行后需要拆 `share_link.go` → **做好在 400 行时拆的准备**

#### 选项与权衡

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| A. 独立 token（推荐） | `share_links` 表独立于 `file_shares`，各有自己的逻辑 | 现有分享代码零修改 | 两个"分享"概念需要清晰文档 |
| B. 扩展 file_shares | 给 `file_shares` 加 `share_token` 可空列 | 一个概念统一 | 现有代码需要迁移 NULL 兼容判断 |
| C. 纯 HMAC 无 DB | 分享链接完全由 SIGNING_KEY 签名，不落库 | 零存储成本 | 无法撤销（只能等过期） |

---

### 方向三：回收站自我管理（软删 → 用户可管理回收站）

#### 为什么需要

当前 `Delete` API 是"不可逆的软删"——行被标记 `deleted_at`，但用户无法：
- 看到自己删了什么（没有 `?trashed=true` 过滤）
- 恢复误删的内容
- 选择"立即永久删除"或"自动清理 30 天前的"

#### 核心挑战

1. **所有查询默认过滤 `deleted_at IS NULL`**（GORM 的软删默认行为）。要添加 `?trashed=true`，意味着每个 List 查询都需要一个 GORM scope 切换
2. **文件引用的 blob 在 GC 清理周期之前不会被物理删除**。如果用户点击"永久删除"，立即可删除 blob 吗？还是进入 GC 队列？
3. **级联删除**：评论删除后，子评论的 `deleted_at` 设置可能是级联的。恢复时是否递归恢复？
4. **与 GC sweeper 的关系**：回收站 TTL（如 30 天）语义上应该由应用层管理，而非由 GC retention 窗口控制

#### 架构变更

```
当前状态：
  Delete API → 软删 (deleted_at = now) → GC sweeper 在 retention 后清理 blob

新状态：
  Delete API → 软删 (deleted_at = now)
  TrashList API → SELECT * WHERE deleted_at IS NOT NULL ORDER BY deleted_at DESC
  Restore API → UPDATE deleted_at = NULL
  PermanentDelete API → 硬删行 + 扣减 refcount → GC 当 refcount==0 时清理 blob
  GC sweeper → 只清理回收站 TTL 已过的行 + refcount==0 的 blob
```

#### 文件变更范围

| 影响区域 | 变更类型 |
|----------|---------|
| `domain/file.go` / `domain/message.go` / `domain/comment.go` | 无（DeletedAt 字段已存在） |
| `repository/file.go` — `ListTrashed` | 新增 trashed-only 分页查询 |
| `service/file_crud.go` — `Restore` / `PermanentDelete` | 新增方法 |
| `service/comment.go` — `Restore` / `PermanentDelete` | 新增方法（子评论级联问题需决策） |
| `service/message.go` — `Restore` / `PermanentDelete` | 新增方法 |

#### 关键设计决策

**决策 1: 回收站范围** — 三个实体（文件/消息/评论）都支持回收站，还是只看文件？
- **建议**：文件必须有回收站（价值最高，误删成本高）。消息和评论可以放在后续迭代（用户通常不"恢复"一条消息）。

**决策 2: 级联恢复** — 用户恢复一条评论，子评论是否自动恢复？
- **建议**：不级联。子评论在父评论删除时已失去上下文，恢复后也无意义。在 UI 上标记"父评论已删除"即可。

**决策 3: 永久删除与 GC 的关系**
- **建议**：永久删除 = 硬删行 + 立即扣减存储引用计数。但 blob 物理删除仍由 GC sweeper 异步执行（避免阻塞 API 响应）。用户得到"已从回收站清除"的即时反馈，存储空间在 GC 周期后释放。

#### AGENTS.md 约束检查

- 回收站的每个 Restore/PermanentDelete 方法约 15-25 行 → ✅
- 每个 ListTrashed 查询约 20-30 行 → ✅
- 关键是 service 文件大小：`service/file_crud.go` 当前约 125 行，加上 TrashList/Restore/PermanentDelete 约 200 行 → ✅
- 但 `service/comment.go` 已接近 350 行 → **开始监控，接近 400 行时拆出 `service/comment_moderation.go`**

---

### 方向五：活动提要（通知 → 持久活动日志）

#### 为什么需要

当前通知系统是 **push 模式**——事件发生 → 写入 notification 表 → SSE 推送 → 用户读后标记已读。但缺少：
- 用户想看"我上周的活动"——当前通知不持久保留（读后即焚或短 TTL）
- 用户想看在另一个应用中的活动上下文（"张三评论了你的文件 X 的截图"比"张三评论了你的文件"更丰富）
- 第三方开发者想消费活动流（Webhook 以外的方式）

#### 核心挑战

1. **outbox 事件在 relay 消费后标记 `published_at`，但事件 payload 保留在 outbox 表中**。问题在于 outbox 表是工作队列，不是查询优化的历史表
2. **活动需要去重聚合**：用户点赞了同一文件 3 次 → 活动流显示 "张三点赞了你的文件" (1) 而非 3 条重复
3. **活动需要跨实体关联**：`file.uploaded` + `comment.created` + `message.received` 按时间线混排
4. **可见性过滤**：活动流必须应用 Block + 权限检查，与现有搜索可见性逻辑一致

#### 数据模型建议

```sql
CREATE TABLE activity_events (
  id              UUID PRIMARY KEY,
  owner_uid       TEXT NOT NULL,        -- 活动所属用户（"谁的活动"）
  event_type      TEXT NOT NULL,        -- 'file.uploaded', 'comment.replied', ...
  actor_uid       TEXT NOT NULL,        -- 触发者
  aggregate_type  TEXT NOT NULL,        -- 'file', 'message', 'comment'
  aggregate_id    UUID NOT NULL,
  summary         TEXT NOT NULL,        -- 预渲染的摘要文本
  payload         JSONB,               -- 原始 payload（可扩展）
  created_at      TIMESTAMPTZ NOT NULL,
  -- 可选：ttl 字段或用 created_at 推断
);

CREATE INDEX idx_activity_owner ON activity_events(owner_uid, created_at DESC);
```

#### 架构变更

```
当前 outbox 消费路径：
  写操作 → outbox.Append → OutboxRelay → NotificationDispatcher → 写 notification 表 → SSE

新活动持久化路径（并行）：
  写操作 → outbox.Append → OutboxRelay → ActivityWriter → 写 activity_events 表
                                    ↕
                            NotificationDispatcher（现有路径不变）
```

#### 文件变更范围

| 影响区域 | 变更类型 |
|----------|---------|
| `domain/activity.go` | 新增 `ActivityEvent` 实体 + 事件类型常量 |
| `repository/activity.go` | 新增 `activity_events` 表的 CRUD |
| `service/activity.go` | 新增 `ActivityService`（ListByOwner, 聚合） |
| `outbox/relay.go` | 扩展 relay 链，新增 ActivityWriter handler |
| `handler/http/activity.go` | 新增活动流 HTTP 端点 |
| `handler/grpc/activity.go` | 新增活动流 gRPC 端点 |

#### 关于现有通知系统的关系

活动表与通知表是**互补**关系：

| 维度 | Notification | ActivityEvent |
|------|-------------|---------------|
| **生命周期** | 读后标记已读，30 天后可清理 | 持久保留（TBD，建议 90 天） |
| **语义** | "你有新消息需要处理" | "你所有活动的历史记录" |
| **触发者** | 别人对你做的 | 你自己 + 别人对你做的 |
| **推送** | SSE 实时推送 | 无推送（需要时轮询） |
| **聚合** | 可折叠但不持久 | 按类型+聚合体去重 |

#### 选项与权衡

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| A. 从 outbox 直接写活动表（推荐） | relay 链新增一个 handler | 复用现有事务保证、不侵入现有通知 | outbox 表承载两个消费者 |
| B. 从 notification 表派生 | 通知标记已读后转为活动记录 | 无需新事件消费者 | 通知 TTL 短会丢失活动，且通知不全（用户自己的操作不产生通知） |
| C. 使用 outbox 事件 + 物化视图 | 在 outbox 表上建物化视图做活动查询 | 无新写入路径 | 查询性能差，ttl 管理复杂 |

---

### 方向二：数据导出（GDPR / 用户数据可移植性）

#### 为什么需要

如果 aero-id 承载的是用户数据，需要满足：
- **合规**：GDPR 数据可移植权（Article 20）要求用户以结构化常用格式获取其数据
- **信任**：用户应能随时拿回自己的文件/消息/评论，而不必逐一手动下载
- **产品需求**：大文件列表导出（如"下载我 2024 年的全部文件"）是基本用户预期

#### 核心挑战

1. **数据量**：一个活跃用户可能有上千条消息、数百个文件、数百条评论。导出不能是实时聚合的——必须异步生成
2. **存储格式**：JSON? CSV? 文件本身是 binary 原文件还是引用链接？文件是否包含原始 blob？
3. **跨实体关联**：评论元数据（like_count、reply_count）是否导出？消息的 reactions 是否包含？
4. **大导出文件的分发**：结果文件怎么给用户？放在 storage 里然后生成 presigned URL？
5. **导出中的权限过滤**：用户导出的"文件"只包括自己 `owner_uid` 的，不包含别人分享给他的——这需要明确的用户理解

#### 架构变更

```
新组件：
  export_jobs 表（id, owner_uid, status, job_type, created_at, completed_at, download_url, expires_at）

新增路径：
  POST /v1/exports → CreateExportJob → 异步 worker → 聚合数据 → 写入 storage（JSON/CSV）
  GET /v1/exports/:id → 查询状态 + 下载 URL
```

#### 文件变更范围

| 影响区域 | 变更类型 |
|----------|---------|
| `domain/export.go` | 新增 ExportJob 实体 + ExportType 枚举 |
| `repository/export.go` | 新增 export_jobs CRUD |
| `service/export.go` | 新增 ExportService（异步 job 管理） |
| `handler/http/export.go` | 新增导出 API 端点 |
| `handler/grpc/export.go` | 新增导出 gRPC 端点 |

#### 关于并行性

三个实体（File / Message / Comment）各自已经有了 `ListByOwner`，但：
- `ListByOwner` 是分页的，export 需要的不是分页而是全量
- 需要新增 `ListAllByOwner`（无分页限制，或使用 server-side cursor 遍历）

#### 选项与权衡

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| A. 全 JSON 导出（推荐） | 所有数据以 JSON 格式打包。文件 blob 以 presigned URL 引用 | 实现简单、存储小 | 用户不能直接"下载文件" |
| B. ZIP + 原文件 | 文件 blob 加入 ZIP 包 | 用户获得原文件 | 存储开销巨大（N 个用户导出 = N 份副本） |
| C. 分步导出 | 元数据 JSON + 文件 URL 列表（用户自己按需下载） | 灵活 | UX 复杂 |

---

### 方向四：CDN 边缘分发

#### 为什么需要

当前 `PresignGet` 返回的 URL 直指存储后端：
- S3 原生 URL（如 `https://s3.us-east-1.amazonaws.com/bucket/key`）
- Local 则是 aero-id 自己的 `/files/dl`

没有 CDN 层意味着：
- 大文件下载直接命中存储后端，无法利用边缘缓存
- 高并发下载（如热门文件）导致 S3 出口费用增加
- 用户在不同地理位置的下载延迟差异巨大

#### 核心挑战

1. **存储接口抽象**：`FileStorage.PresignGet` 当前返回最终 URL。要支持 CDN，要么在 storage 层包裹 CDN 域名，要么在 service/handler 层后处理
2. **缓存失效**：文件被删除/更新后，CDN 边缘节点可能有缓存。需要 `PurgeCache` 方法
3. **私有文件**：CDN 需要处理私有文件（带签名的 URL）不能裸缓存
4. **与 s3 的 PresignPut 集成**：上传路径也可能需要 CDN

#### 架构变更

不建议修改 `FileStorage` 接口本身（这会违背接口隔离原则）。更好的方式是：

**选项 A：URL 后处理（推荐）**
```
service.DownloadURL → storage.PresignGet → raw URL
                                         → CDNURLRewriter.Wrap(rawURL, ttl) → CDN URL
```

`CDNURLRewriter` 是一个可选的后处理器，配置了 CDN 域名就包裹，否则返回原始 URL。

**选项 B：Storage 接口扩展**
```go
type FileStorage interface {
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    // 新增：
    PresignGetWithCDN(ctx context.Context, key string, ttl time.Duration) (string, error)
    PurgeCache(ctx context.Context, key string) error
}
```

**我的建议**：选择 A（URL 后处理）。理由：
- 不破坏现有接口
- CDN 是基础设施关注点，不是存储后端关注点
- 可以轻松切换 / 关闭 CDN
- 不引入 `ErrNotSupported` 的第二种含义

#### 文件变更范围

| 影响区域 | 变更类型 |
|----------|---------|
| `storage/cdn.go` | 新增 `CDNURLRewriter` 结构 + 工厂 |
| `service/file_presign.go` | `DownloadURL` 方法末尾加 `CDNURLRewriter.Wrap` |
| `config/config.go` | 新增 `cdn.domain`、`cdn.enabled`、`cdn.purge_api_key` |
| `storage/storage.go` | 无变更 |

---

## 3. 接口设计建议

### 3.1 新接口的总体原则

| 原则 | 说明 |
|------|------|
| **保持 ADR-001** | 所有新 service 方法签名必须与传输无关（无 `gin.Context`、无 `grpc.ServerStream`） |
| **CRUD 风格一致** | 新实体的接口设计遵循现有模式：`Create`/`Get`/`List`/`Delete` 四件套 |
| **Cursor 分页统一** | 所有 List 方法统一使用 `domain.Page` + 返回 `nextCursor` |
| **错误互通** | 新 service 返回 `*AppError`，repository 返回 raw 错误 |

### 3.2 各方向的接口建议

**方向一（匿名分享）：**

```go
// ShareLinkService handles anonymous share links (different from uid-based shares).
type ShareLinkService interface {
    // CreateShareLink generates a token-based share for a file the caller owns.
    CreateShareLink(ctx context.Context, fileID domain.ID, ttl time.Duration, 
        password string, maxAccessCount int) (*domain.ShareLink, error)
    // RevokeShareLink invalidates a share link by token.
    RevokeShareLink(ctx context.Context, token string) error
    // AccessShareLink validates a share token (optionally with password) and returns a download URL.
    AccessShareLink(ctx context.Context, token string, password string) (*ShareLinkAccess, error)
    // ListShareLinks lists active share links for a file.
    ListShareLinks(ctx context.Context, fileID domain.ID) ([]*domain.ShareLink, error)
}
```

**方向三（回收站）：**

```go
// 在现有 FileService / MessageService / CommentService 中扩展：
type FileService interface {
    // ... 现有方法
    // ListTrashed lists soft-deleted files belonging to the caller.
    ListTrashed(ctx context.Context, page domain.Page) (*FileList, error)
    // RestoreFile brings a soft-deleted file back.
    RestoreFile(ctx context.Context, id domain.ID) error
    // PermanentDelete hard-deletes a file and decrements its storage ref count.
    PermanentDelete(ctx context.Context, id domain.ID) error
}
```

注意：在文件层面暴露 `PermanentDelete` 但不在消息/评论层面暴露相同功能——理由是消息的"永久删除"已经是 `Delete` + GC 覆盖，用户不需要手动触发。

**方向五（活动提要）：**

```go
type ActivityService interface {
    // List returns the caller's activity timeline, newest first.
    List(ctx context.Context, page domain.Page, opts ActivityListOpts) (*ActivityList, error)
}

type ActivityListOpts struct {
    EventTypes  []string // filter: nil = all
    Since       *time.Time // only events after this timestamp
    AggregateOnly bool // if true, deduplicate consecutive same-type same-aggregate
}
```

### 3.3 是否需要新的抽象层

| 方向 | 是否需要新抽象层 | 理由 |
|------|----------------|------|
| 匿名分享 | **需要** `AccessChecker` 接口 | `CanReadFile` 当前是包级函数，扩展为多级判定后应成为可测试的接口 |
| 回收站 | **不需要** | 只是现有 service 的扩展方法，不涉及新领域 |
| 活动提要 | **需要** `EventSummarizer` | 不同的 event_type 需要不同的摘要模板（如 `file.uploaded` → "你上传了 {name}"） |
| 数据导出 | **不需要** | 异步 job 模式足够，导出格式由 service 内聚实现 |
| CDN | **不需要**（但需要新工具组件） | `CDNURLRewriter` 是一个工具类，不是新抽象层 |

---

## 4. 技术选型

### 4.1 新依赖评估

| 方向 | 潜在新依赖 | 是否推荐 | 理由 |
|------|-----------|---------|------|
| 匿名分享 | 无 | ✅ | 纯 DB + HMAC（`crypto/hmac` 已引入） |
| 回收站 | 无 | ✅ | 纯 GORM 查询 |
| 活动提要 | 无 | ✅ | 纯 DB 写入 + 读取 |
| 数据导出 | 无 | ✅ | 使用现有 storage 后端存导出产物 |
| CDN | CDN provider SDK（如 Cloudflare API、Akamai API） | ⚠️ 可选 | 如果 CDN 提供商有 HTTP API 则不需要 SDK；Go 标准库 `net/http` 足够 |

### 4.2 自建 vs 采购的决策矩阵

| 决策 | 场景 | 建议 |
|------|------|------|
| **CDN 提供商** | 选择了哪个 CDN 厂商？ | **已有决策**：根据已部署的云厂商选择配套 CDN。S3 → CloudFront，GCS → Cloud CDN，其他 → Cloudflare 等 |
| **密码哈希** | 方向一的密码保护 | **自建**：`bcrypt`（Go 标准库 `golang.org/x/crypto/bcrypt` 已广泛使用），不超过 73 字节密码 |
| **导出格式序列化** | JSON vs CSV vs others | **自建**：`encoding/json` 标准库足够，CSV 用 `encoding/csv` |
| **活动摘要模板** | 事件 → 人类可读文本 | **自建**：Go 的 `text/template` 或简单的 switch-case（符合 cyclomatic ≤10 约束） |

### 4.3 关于引入 Redis

您识别的 5 个方向中，**没有一个需要 Redis**。这与 `expansion-directions.md` 的方向二（Redis 共享状态）形成对比。

给出判断依据：

| 方向 | 是否需要 Redis | 原因 |
|------|-------------|------|
| 匿名分享 | ❌ | share_token 验证是 DB lookup（实际应用中分享链接的 QPS 远低于常规 API） |
| 回收站 | ❌ | 单纯的 DB 查询，用户操作频率低 |
| 活动提要 | ❌ | 写入频率低（事件驱动），读取可用 DB 索引 |
| 数据导出 | ❌ | 异步 job，DB + storage 足够 |
| CDN | ❌ | CDN 缓存与 aero-id 无关 |

Redis 的价值场景是**多实例共享状态**（限流、幂等、缓存一致性）。如果当前没有多实例部署压力，不推荐为了这些方向引入 Redis。

---

## 5. 实施路线图

### 5.1 优先级排序

基于业务价值 + 技术依赖关系 + 实现成本的三维评估：

| 优先级 | 方向 | 业务价值 | 实现成本 | 依赖前置 |
|--------|------|---------|---------|---------|
| **P0** | 方向一：匿名分享 | 🌟🌟🌟🌟🌟 | 中（约 400 行） | 无 |
| **P0** | 方向三：回收站 | 🌟🌟🌟🌟 | 低（约 250 行） | 无 |
| **P1** | 方向五：活动提要 | 🌟🌟🌟🌟 | 中（约 500 行） | 无 |
| **P1** | 方向二：数据导出 | 🌟🌟🌟 | 中（约 400 行） | 无 |
| **P2** | 方向四：CDN | 🌟🌟🌟 | 低（约 150 行） | CDN 提供商合同确定 |

**排序理由**：

- **方向一 P0**：分享链接是最直接的"缺失功能"——文件服务不支持外部分享是一个明显的产品缺口。技术上没有外部依赖，实现相对可控。
- **方向三 P0**：回收站成本最低（大量复用现有软删基础设施），用户感知价值高。误删恢复是用户信任的基础功能。
- **方向五 P1**：活动提要依赖 outbox 扩展，但 outbox 已稳定运行。注意与通知系统的关系需要产品定义。
- **方向二 P1**：导出功能虽然是合规需要，但 GDPR 的 30 天响应窗口给了充裕的实现时间。
- **方向四 P2**：CDN 的价值前提是"下载流量已经大到需要 CDN"，这是一个 scaling 问题而非功能缺口。先有用户再优化。

### 5.2 阶段划分

```
Phase 1（当前 Sprint + 下一 Sprint: ~2 周）
├── 方向一：匿名分享（核心：CreateShareLink + AccessShareLink）
│   ├── share_links 表 migration
│   ├── ShareLinkRepository (CRUD)
│   ├── AccessChecker 接口重构
│   ├── ShareLinkService + handler
│   └── 集成测试
└── 方向三：回收站（核心：TrashList + Restore）
    ├── 文件回收站：TrashList + Restore + PermanentDelete
    └── 消息/评论回收站（如果时间允许）

Phase 2（下一个 Sprint: ~1 周）
├── 方向五：活动提要
│   ├── activity_events 表 migration
│   ├── ActivityRepository + ActivityWriter
│   ├── outbox relay 链扩展
│   ├── ActivityService + handler
│   ├── EventSummarizer（事件→摘要）
│   └── 集成测试
└── 方向三续：消息/评论回收站（如果 Phase 1 未完成）

Phase 3（再下一 Sprint: ~1 周）
└── 方向二：数据导出
    ├── export_jobs 表 migration
    ├── ExportRepository + ExportService
    ├── 数据聚合器（全量 ListByOwner + format）
    └── handler + 集成测试

Phase 4（按需）
└── 方向四：CDN
    ├── CDNURLRewriter 组件
    ├── config 扩展
    └── 集成测试
```

### 5.3 风险点与缓解策略

| 风险 | 方向 | 概率 | 影响 | 缓解 |
|------|------|------|------|------|
| **AccessChecker 接口过度抽象** | 一 | 中 | 高 | 从具体的 `canReadFile` 函数起步，有第二个实现时才提取接口 |
| **回收站级联问题** | 三 | 高 | 中 | 明确不做级联恢复，在文档和 API 响应中说明 |
| **活动表写入量过大** | 五 | 低 | 高 | 活动事件按需采样（如只记录关键事件，忽略 `reaction.added`），设置默认 TTL |
| **大导出 OOM** | 二 | 中 | 中 | 流式写入 storage（不构建全量内存结构），参考现有的流式上传模式 |
| **CDN 缓存未及时失效** | 四 | 中 | 低 | 文件删除时调用 CDN Purge API，但以 404 回源为兜底（CDN 容错设计） |
| **与现有 outbox 消费者的并发冲突** | 五 | 低 | 高 | `outbox.Chain` 已经是顺序执行，新增 handler 在链中不会冲突 |

### 5.4 每个方向的增量可交付

每个方向都应该能**独立交付**而不阻塞其他方向，且每个方向交付后立即在 `make test && make lint` 中通过。

**建议的测试策略增加**：

| 方向 | 测试类型 | 说明 |
|------|---------|------|
| 一 | 单元 + 集成 | ShareLinkRepository 需要真实 DB 验证 token 唯一性、密码哈希 |
| 三 | 单元 + 集成 | `PermanentDelete` + refcount 扣减在集成测试中验证 |
| 五 | 单元 + 集成 | outbox relay 扩展需要验证链中两个 handler 都执行 |
| 二 | 单元 + 集成 | 导出 job 的生命周期测试（create → process → ready → download） |
| 四 | 单元 | CDNURLRewriter 是纯函数，单元测试即可 |

**每个方向交付时的 gate**：`make lint && make test && make test-integration`

---

## 总结

五个方向按实施建议排序：

```
Phase 1                  Phase 2                  Phase 3                  Phase 4
┌─────────────┐    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│ 匿名分享(P0) │ →  │ 活动提要(P1) │    │ 数据导出(P1) │    │  CDN (P2)   │
│ 回收站  (P0) │    │             │    │             │    │             │
└─────────────┘    └─────────────┘    └─────────────┘    └─────────────┘
      ↓                   ↓                  ↓                  ↓
  make test ✅         make test ✅        make test ✅        make test ✅
  make lint ✅         make lint ✅        make lint ✅        make lint ✅
```

每个方向的架构影响都是**自包含的**——不改变其他方向的核心路径，不引入新的基础设施依赖（Redis / 消息队列），不违反 AGENTS.md 的 500 行 / 50 函数行 / cyclomatic ≤10 约束。
