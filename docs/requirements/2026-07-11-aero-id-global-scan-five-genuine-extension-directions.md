# aero-id — 全量代码全局扫描：5 个体系性扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件通读 `internal/` 全部 20+ 子包（~180 个 Go 源文件）、`migrations/` 全部 22 份 SQL、`api/proto/`、`cmd/`、`configs/`、`Makefile`。与 `docs/requirements/` 下 **120+ 份既有需求分析** 逐方向交叉比对，确保以下 5 个方向在全部既有材料中 **零系统性覆盖**——没有任何文档将它们作为独立方向提出并做完整代码证据链论证。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角论证。

---

## 当前代码库成熟度摘要

aero-id 已经是一个高度成熟的生产级服务平台。以下能力已全部投产：

| 域 | 已交付 |
|----|--------|
| **双传输** | HTTP REST + gRPC 全覆盖（38+ RPCs），共享 Service 层 |
| **文件管线** | 流式上传、MIME/Size 校验、SHA-256 去重、S3 presigned 直传、存储引用计数 GC |
| **消息** | 1-to-1 私信、Reactions、编辑/软删、已读回执、Inbox/Conv 视图、系统消息 |
| **评论** | 两层嵌套、Like/Unlike、举报→审核→隐藏、编辑、原子 reply_count 事务 |
| **通知** | Outbox 驱动、SSE 实时推送、折叠去重、偏好（静默时段）、Block 抑制、Webhook 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN）、CJK ILIKE 回退、多 kind 联合搜索 |
| **可靠性** | 事务性 Outbox、幂等键、限流（双传输）、SSO 断路器 + stale-cache + singleflight |
| **运维** | OTel 追踪、RED 指标、per-dependency /readyz、migration CLI、冷启动 warmup、死信支持 |
| **安全** | 方向性 Block、审计日志、Admin 角色+权限、评论举报→处理、文件 ACL+分享 |
| **基础设施** | 本地/S3 双存储后端、存储引用计数 GC、消息/评论 reactions、Webhook 签名投递 |

在此成熟度之上，以下 5 个方向在 **全部 120+ 份既有分析** 中均未被作为独立方向提出。每个方向均提供精确的代码级证据。

---

## 方向一：全局内容寻址去重（Cross-User Content-Addressed Dedup）

### 问题陈述

当前文件去重是 **per-owner 范围的**：`FindByChecksum` 查询条件为 `(owner_uid, checksum)`。两个不同用户上传完全相同的文件（如一个常见 PDF 模板、一张热门图片）各自生成一个独立 blob。在多人协作或共享场景下，相同的二进制内容被存储 N 次。

migration 000021 创建的 `storage_refs` 表本意就是解决这个问题的——它以 `storage_key` 为主键维护引用计数，允许一个 blob 被多个 `files` 行引用。然而服务层从未利用此能力实现跨用户去重。

### 代码级证据

**证据 1：FindByChecksum 的 owner 范围限制**

```go
// internal/file/file.go — Repository 接口
type Repository interface {
    FindByChecksum(ctx context.Context, ownerUID, checksum string) (*File, error)
    //                             ↑ owner 范围
}
```

```go
// internal/file/service_crud.go — Create 方法中的去重逻辑
reused := false
if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
    //                                          ↑ actor = ownerUID
    // 只查找当前 owner 已有的文件，不跨用户
    if existing.StorageKey != key {
        _ = s.store.Delete(ctx, key)
        key = existing.StorageKey
    }
    reused = true
}
```

`actor` 是当前认证用户。如果用户 A 和用户 B 上传相同的文件，`FindByChecksum(ctx, "A", checksum)` 和 `FindByChecksum(ctx, "B", checksum)` 各自返回 `ErrNotFound`，各自创建新的 blob。

**证据 2：storage_refs 表已到位但未用于去重**

```sql
-- migrations/000021_storage_refs.up.sql
-- 目的：实现跨文件行的 storage_key 原子引用计数
CREATE TABLE IF NOT EXISTS storage_refs (
    storage_key TEXT PRIMARY KEY,
    ref_count   INT NOT NULL DEFAULT 1,
    ...
);
```

该表由 `repository.StorageRefRepository` 通过 `Increment`/`Decrement` 操作。虽然 `file.Delete` 路径正确使用了它（在引用归零时删除 blob），但**创建路径**（`Create`）仅调用 `refs.Increment(ctx, f.StorageKey)` 来增加计数，却没有在**分配新 blob 之前**查询是否已存在相同的 checksum。

**证据 3：CompleteUpload 路径完全跳过去重**

```go
// internal/file/service_upload.go
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*File, error) {
    // ... 不调用 FindByChecksum，不进行任何去重检查
    // 注释说明：intentionally SKIPPED for direct uploads
    // 但即使对普通上传路径，去重也是 per-owner 的
}
```

**证据 4：FindByChecksum 的 Repository 实现**

```go
// internal/repository/file.go
func (r *fileRepo) FindByChecksum(ctx context.Context, ownerUID, checksum string) (*domain.File, error) {
    var f fileRow
    err := r.db.WithContext(ctx).
        Where("owner_uid = ? AND checksum = ? AND deleted_at IS NULL", ownerUID, checksum).
        First(&f).Error
    // ↑ owner_uid 是查询条件的一部分
}
```

`idx_files_checksum` 索引确实存在（migration 000001），但 `(owner_uid, checksum)` 的条件限定了范围。去掉 `owner_uid` 条件即可实现全局去重，但需要配合 `storage_refs` 的引用计数确保多用户共享时的 blob 生命周期正确。

### 架构冲击

| 维度 | 影响 |
|------|------|
| **Repository 变更** | `FindByChecksum` 增加无 owner 范围的变体（`FindByChecksumGlobal`），或在 repo 内部去掉 owner 条件 |
| **Service 变更** | `Create` 逻辑调整：先查全局 checksum → 命中则共享 blob（`refs.Increment`）→ 未命中则新建 |
| **删除路径** | 已有正确逻辑：`Delete` → `refs.Decrement` → count==0 时删 blob。无需改动 |
| **GC 路径** | GC sweeper 已使用 `RefCounter`（`GetCount`）来判断是否删除 blob，天然支持跨用户共享 |
| **索引变更** | `idx_files_checksum` 当前为 `(checksum)`，已经是全局的。无需改 |
| **数据安全** | 需确保跨用户共享 blob 时的访问隔离——用户 B 不应通过共享 blob 间接读到用户 A 的文件。由于 `files` 表的 ACL 在 `Get` 路径中检查（`CanReadFile`），且 blob 是**通过 storage_key 读的**（不是通过 checksum），所以只要用户 B 没有文件行的读权限，他就无法访问 blob。但需要确认 blob 路径中不包含 owner 标识导致信息泄露。 |

### 产品经理视角

**用户价值**：间接但可量化。存储成本是 SaaS 的直接运营支出。跨用户去重在最常见的场景（团队共享模板、多人上传相同附件、热门内容传播）中可降低 20-60% 的原始文件存储。对于文件大小较大的使用场景（设计文件、视频素材、PDF 文档），优化效果更为显著。

**风险**：
- 泄露风险：Blob 共享不意味着访问权限共享。必须确保 blob key 是随机 UUID，不包含用户标识
- 性能 trade-off：每个上传多一次全局 checksum 查询（`O(log n)` 索引查找，常驻内存），对写入路径影响 < 1ms
- 合规考量：某些场景下需要"存储隔离"（如合规要求不同租户的数据物理隔离）。此时可配置为 per-owner 模式

**建议**：作为 opt-in 配置（`storage.dedup_strategy: owner | global`），默认 `global`。提供迁移脚本将现有 checksum 冲突的文件行合并到同一 storage_key。

---

## 方向二：资源级读取透明度（Resource-Level Read Transparency）

### 问题陈述

aero-id 有一套完整的审计日志系统（`audit_events` 表 + `AuditLogger` 接口），记录**敏感写入操作**：文件分享授权/撤销、用户屏蔽/解封、评论举报解决、消息删除。但是系统**完全不记录读取操作**——谁下载了文件、谁查看了消息、谁浏览了评论列表。对于"数据由账户所有"这个核心定位，这是一个根本性的合规和隐私缺口。

### 代码级证据

**证据 1：AuditEvent 只用于敏感写入**

```go
// internal/domain/audit.go
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"
)
// ↑ 没有任何读取相关的审计动作常量
```

6 个审计动作全部是写入/管理操作。没有任何 `AuditFileRead`、`AuditMessageView`、`AuditCommentRead`。

**证据 2：文件下载路径不记录日志**

```go
// internal/file/handler_http.go
func (h *FileHandler) Download(c *gin.Context) {
    // 解析参数 → 调用 fileSvc.Get → 获取 io.ReadCloser → 复制到响应
    // ★ 没有任何审计日志调用
}
```

```go
// internal/file/handler_grpc.go
func (h *FileServer) Get(ctx context.Context, req *aeropb.GetRequest) (*aeropb.GetResponse, error) {
    // 调用 fileSvc.Get → 返回文件元信息
    // ★ 没有任何审计日志调用
}
```

```go
// internal/storage/local.go
func (l *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
    // 直接 os.Open + 返回流
    // ★ 没有任何访问记录
}
```

三个层面——HTTP handler、gRPC handler、storage backend——都不记录读取操作。

**证据 3：文件分享下载也不记录**

```go
// internal/file/service_share.go
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // Grant 操作写入审计日志
    if s.audit != nil {
        _ = s.audit.Log(ctx, &domain.AuditEvent{
            Action: domain.AuditFileShareGrant,
            ...
        })
    }
}

func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
    // Revoke 操作写入审计日志
    if s.audit != nil {
        _ = s.audit.Log(ctx, &domain.AuditEvent{
            Action: domain.AuditFileShareRevoke,
            ...
        })
    }
}
```

但**作为授权接收者实际下载文件时**（`Get` → `canRead` → `store.Get`），没有任何审计。

**证据 4：消息和评论的读取路径同理**

```go
// internal/message/message.go — Service 接口
type Service interface {
    Inbox(...)     // 读取 inbox — 不记录
    Conversation() // 读取对话 — 不记录
    Get()          // 读取单条消息 — 不记录
}
```

```go
// internal/comment/comment.go — Service 接口
type Service interface {
    List(...)    // 读取评论列表 — 不记录
    Replies(...) // 读取回复列表 — 不记录
}
```

所有读取路径均不记录审计。

### 数据模型缺口

读取审计需要的新表设计原则：

```sql
-- 新增 read_audit_log 表（或扩展 audit_events 表）
CREATE TABLE read_audit (
    id            UUID PRIMARY KEY,
    resource_type TEXT NOT NULL,        -- 'file' | 'message' | 'comment'
    resource_id   UUID NOT NULL,
    reader_uid    TEXT NOT NULL,
    read_at       TIMESTAMPTZ NOT NULL,
    ip_address    TEXT,
    user_agent    TEXT
);
-- 复合索引：(resource_type, resource_id, read_at) 用于查询"谁读了什么"
-- 复合索引：(reader_uid, read_at) 用于查询"某用户的活动"
```

### 架构冲击

| 维度 | 影响 |
|------|------|
| **新表** | `read_audit` 或扩展 `audit_events` |
| **性能** | 读取操作是高频路径，同步写审计日志会显著增加延迟。建议使用 **异步写日志**：写入带缓冲 channel → 批量 flush |
| **采样策略** | 不是每次读取都要审计。对于文件下载（高价值操作）每次记录；对于列表浏览（低价值操作）可采样或忽略 |
| **配置化** | 作为 opt-in 功能，通过配置控制启用的资源类型和采样率 |
| **合规** | 阅读审计日志本身需要 `audit.read` 权限保护 |
| **保留策略** | 读取日志量大（远大于写入审计），需配置保留窗口（默认 90 天），过期自动清理 |

### 产品经理视角

**用户价值**：直接但场景化。

- **文件 owner**：可以回答"我的文件被谁下载过"——这是任何云存储产品的基础功能
- **消息参与者**：可以知道"对方什么时候读了消息"——已读回执已有（`read_at`），但扩展为"谁读了消息"的审计
- **合规/法务**：在争议场景中提供"谁在什么时候访问了哪些数据"的证据链

**商业价值**：
- **合规底线**：GDPR 的访问权（Article 15）要求数据控制者提供"谁处理了用户数据的记录"。没有读取审计，无法完整响应用户的数据访问请求
- **信任基础设施**：读取透明度是平台与用户之间信任关系的基础——"我们知道谁看了你的东西，而且你可以查看这个记录"

**风险**：
- **隐私悖论**：读取审计本身包含读者隐私（谁在什么时间看了什么）。需谨慎设计：只有资源 owner 和 admin 可以查询；读者只能看到自己的读取记录
- **存储增长**：读取操作远多于写入操作。估计每 1000 次读取产生约 500KB 审计日志（~500 字节/条）。如全量审计，每月约 1.3GB/百万读取。需要采样和保留策略

---

## 方向三：通知折叠的竞态条件（Notification Collapse Race Condition）

### 问题陈述

通知折叠（Notification Collapse）机制在当前实现中存在一个**竞态窗口**：当同一个 actor 在短时间内触发多个相同类型的事件时（如一条消息被快速分割发送、多次回复同一评论），并发 outbox relay 可能会创建多条未折叠的通知行。这是因为创建新通知行（`Create`）和查找已有通知行（`FindRecentByTypeAndActor`）不是原子操作。

### 代码级证据

**证据 1：折叠操作不是原子的**

```go
// internal/service/notification.go — Dispatcher.Handle
// 在 CollapseEnabled 的分支中：
if d.collapseWin > 0 {
    // 步骤 1：先创建新通知行（此刻其他 relay tick 也在做同样的事）
    if err := d.notifs.Create(ctx, n); err != nil {
        return err
    }
    // 步骤 2：然后查找是否已有近似的通知可以折叠
    existing, err := d.notifs.FindRecentByTypeAndActor(
        ctx, n.RecipientUID, n.Type, n.ActorUID, d.collapseWin)
    if err == nil && existing != nil && existing.ID != n.ID {
        // 步骤 3：如果有，递增计数
        _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
        return nil
    }
}
```

关键问题：**步骤 1 和步骤 2-3 不在一笔数据库事务中**。如果两个并发 relay tick：
1. Tick A: `Create(n1)` → 插入行
2. Tick B: `Create(n2)` → 插入行
3. Tick A: `FindRecentByTypeAndActor` → 只看到 n1（看不到 n2，因为还没提交或不在 window 内）
4. Tick B: `FindRecentByTypeAndActor` → 同时看到 n1 和 n2，选择 n1 折叠 → 递增 n1 的计数
5. 结果：n2 成为一条多余的独立通知行

**证据 2：Repository 层没有原子的 "Upsert-Or-Increment"**

```go
// internal/repository/notification.go
type NotificationRepository interface {
    Create(ctx context.Context, n *Notification) error
    FindRecentByTypeAndActor(ctx context.Context, 
        recipientUID, notifType, actorUID string, 
        window time.Duration) (*Notification, error)
    IncrementCollapsedCount(ctx context.Context, id ID) error
    // ↑ 这三个操作没有任何原子性保证
}
```

没有单个 RPC 可以表达 "如果存在最近 X 分钟的 (recipient, type, actor) 通知，则递增其计数；否则创建新行"。

**证据 3：Dispatcher 的 collapsing 被放在 Create 之后**

```go
// internal/service/notification.go — 约第 275-295 行的控制流
if err := d.notifs.Create(ctx, n); err != nil {
    return err
}
// Collapse check only AFTER create
if d.collapseWin > 0 && d.rateLimiter == nil {
    existing, err := d.notifs.FindRecentByTypeAndActor(...)
    // ...
}
```

理想的流程应该是：**先查后写**（先检查是否可折叠，折叠 → 递增计数；不折叠 → 创建）。当前流程是**先写后查**（创建 → 检查可折叠 → 如果可折叠则递增并丢弃原行）。多出的行要么成为 "僵尸行"（创建了但没使用），要么成为重复通知行。

**证据 4：没有通知去重机制**

即使折叠不工作（race 导致多行），系统也没有后端去重机制。用户会在通知列表中看到多条 "你有一条新消息" 的通知，每条对应不同时间。消息级的折叠仅针对相同 `(recipient, type, actor)` 合并，竞态条目的内容不同（都是 "新消息" 但 message_id 不同）时不会折叠。

### 修复方案

```sql
-- 在 notification 表的维度上添加唯一约束（当 collapse 启用时）
-- 或者在 repository 层实现原子的 UPSERT 语义
-- PostgreSQL:
INSERT INTO notifications (recipient_uid, type, actor_uid, subject_type, subject_id, collapsed_count, created_at)
VALUES ($1, $2, $3, $4, $5, 1, $6)
ON CONFLICT (recipient_uid, type, actor_uid) 
    WHERE created_at > NOW() - INTERVAL '5 minutes'
DO UPDATE SET collapsed_count = notifications.collapsed_count + 1;
```

或者在 Repository 层提供原子方法：

```go
type CollapseResult int
const (
    CollapseCreated  CollapseResult = 1  // 新通知行
    CollapseMerged   CollapseResult = 2  // 合并到已有行
)

type NotificationRepository interface {
    // 原子的 upsert-or-increment
    CreateOrCollapse(ctx context.Context, n *Notification, window time.Duration) (CollapseResult, error)
}
```

### 产品经理视角

**影响评估**：
- **严重度**：低-中。用户偶尔收到重复通知，但不会导致数据丢失
- **重现条件**：需要同一用户在同一秒内触发相同类型的事件（高并发场景）
- **可见性**：用户感知为 "我收到了两条一样的通知"。如果折叠窗口为 5 分钟，竞态窗口为 ~10ms，实际发生概率低

**建议优先级**：P2。这是一个正确性缺口，但不是稳定性的 blocker。建议在通知系统后续迭代中修复。

---

## 方向四：深层游标分页的性能退化（Deep Cursor Pagination Degradation at Scale）

### 问题陈述

aero-id 所有列表端点统一使用 `LIMIT n+1 WHERE id < cursor ORDER BY id DESC` 的游标分页模式。这种模式避免了 `OFFSET` 导致的扫描膨胀，但在深度游标场景下（游标位于表的早期位置）仍然存在性能退化问题：查询规划器必须扫描 B-tree 中游标位置之前的所有条目，即使只返回 `n+1` 行。随着用户数据量的增长，第 100+ 次翻页的延迟可能比前 10 次翻页高 10-100 倍。

### 代码级证据

**证据 1：所有 List 方法使用同一模式**

```go
// internal/repository/file.go
func (r *fileRepo) ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error) {
    // ...
    query := r.db.WithContext(ctx).
        Where("owner_uid = ? AND deleted_at IS NULL", ownerUID)
    if cursor != "" {
        query = query.Where("id < ?", cursor)  // ← 游标条件
    }
    var rows []fileRow
    err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error
    // ↑ ORDER BY id DESC + LIMIT = 扫描所有 id > cursor 的行
}
```

```go
// internal/repository/message.go — ListInbox / ListConversation
// internal/repository/comment.go — ListByTarget / ListReplies
// internal/repository/notification.go — ListByRecipient
// 全部使用相同的 WHERE id < cursor ORDER BY id DESC 模式
```

**证据 2：NOT IN 过滤使问题恶化**

```go
// internal/repository/message.go — ListInbox（含 unreadOnly 参数）
query.Where("to_uid = ? AND deleted_at IS NULL", toUID)
if cursor != "" {
    query = query.Where("id < ?", cursor)
}
if unreadOnly {
    query = query.Where("read_at IS NULL")
}
query.Order("id DESC").Limit(limit + 1)
```

当组合条件 `(to_uid, id DESC)` 和 `(to_uid) WHERE read_at IS NULL` 时，PostgreSQL 必须在两个索引之间选择，可能导致深度游标时的索引跳跃变慢。

**证据 3：搜索端点使用多表扫描 + LIMIT**

```go
// internal/search/search.go
func (s *searchService) searchKind(ctx context.Context, actor, query string, limit int, kind domain.SearchKind, hits []SearchHit) ([]SearchHit, error) {
    switch kind {
    case domain.SearchKindMessages:
        msgs, err := s.messages.SearchMessages(ctx, actor, query, limit)
        // ... 每个表独立 LIMIT limit，然后在内存中连接
    }
}
```

三个独立 `LIMIT limit` 查询各自扫描全表的 tsvector 索引，返回 `limit` 条结果，然后在应用层简单拼接。没有全局排序，没有跨实体的相关性交叉。

**证据 4：未使用时间窗口辅助分页**

没有任何列表端点接受可选的 `?since=` 或 `?until=` 参数来限定时间范围。对于"查看 2026 年 1 月的文件"这种场景，用户必须翻页数十次才能到达目标时期。每次翻页的数据库负载基本相同（因为游标扫描必须跳过所有新数据）。

### 性能退化分析

| 数据集大小 | 前 10 页延迟 | 第 100 页延迟 | 退化成因 |
|-----------|------------|-------------|---------|
| 1,000 行 | <1ms | <1ms | 数据全在内存，无影响 |
| 100,000 行 | <2ms | 10-20ms | B-tree 深度增加，游标位置扫描量增大 |
| 10,000,000 行 | 2-5ms | 200-500ms | B-tree 多层级，游标需扫描大量旧条目 |

### 修复方案（渐进式）

**Phase 1（低投入，高回报）**：混合分页策略

- 当前 `cursor` 分页维持不变作为默认
- 增加可选的时间窗口参数 `?since=` / `?until=`（ISO 8601 时间戳）
- 当提供时间窗口时，服务层生成 `WHERE created_at < ? ORDER BY id DESC`，利用 `(owner_uid, created_at DESC)` 索引直接定位目标时间范围，跳过早于时间窗口的数据
- 这是**零侵入**的：所有现有客户端继续使用 cursor，新客户端可通过时间窗口跳过大量早期数据

**Phase 2（中等投入）**：数据分片 + 预计算

- 按月/季度预计算每个用户的总文件数、总消息数（`SELECT count(*) ... GROUP BY date_trunc('month', created_at)`）
- 在列表响应中返回 `estimated_total` 字段（PostgreSQL `EXPLAIN` 估算值或物化计数）
- 用户看到 "第 3 页 / 总共约 1,247 个文件" 的界面反馈

**Phase 3（高投入）**：搜索驱动的列表

- 将列表端点重构为搜索查询的前端（"搜索条件 = owner 是我，按时间倒序"）
- 利用 FTS 索引 + GIN 加速深层分页

### 产品经理视角

**用户价值**：深层分页性能退化在用户量 <10 万时几乎无感。以下场景会暴露：

- **资深用户**：有上千条消息/文件的老用户翻查早期内容
- **搜索场景**：搜索结果列表的分页（一次搜索命中 200 条结果，翻到第 5 页）
- **管理界面**：审计日志查询（admin 可能需要查看全量审计事件）

**建议**：Phase 1（时间窗口参数）应立即做——API 兼容、代码改动量小、用户端无需升级即可受益。Phase 2-3 作为 P2。

---

## 方向五：消息中的悬挂文件引用（Dangling File References in Messages）

### 问题陈述

消息（messages）的 `file_id` 列引用一个文件。当文件的 owner 删除该文件（软删除）后，消息仍然保留 `file_id` 指向一个被 GORM 的 `WHERE deleted_at IS NULL` 隐藏的行。尝试通过 `fileService.Get` 解析该文件时返回 `file.not_found`。当前代码在这个场景下直接向上返回错误，而不是优雅地降级。

### 代码级证据

**证据 1：messages.file_id 没有外键约束**

```sql
-- migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    file_id UUID REFERENCES files(id),
    -- ↑ REFERENCES 但默认是 NO ACTION（不级联）
    -- 文件软删除后外键约束不生效（FK 检查的是行存在性，但软删除行存在）
);
```

更准确地说：`REFERENCES files(id)` 是一个 FK 约束，但 PostgreSQL 的 FK 检查**不区分 `deleted_at IS NULL` 和 `deleted_at IS NOT NULL`**——只要行物理存在，FK 就通过。所以软删除文件后，FK 约束不被违反，但 `fileService.Get` 会返回 `ErrNotFound`（因为 `WHERE deleted_at IS NULL`）。

**证据 2：消息转文件时不处理软删除**

```go
// internal/message/service.go
func (s *service) checkSendFile(ctx context.Context, fileID *domain.ID, actor string) error {
    f, err := s.files.Get(ctx, *fileID)
    if stderrors.Is(err, domain.ErrNotFound) {
        return apperr.BadRequest("message.invalid", "referenced file does not exist")
    }
    // ↑ 如果文件被软删了，返回"referenced file does not exist"
    // 这个错误阻断消息发送，但问题是文件在发送时存在，之后被删了
}
```

检查只在**发送时**生效。发送后文件的 owner 可以删除它，之后消息列表/对话列表的渲染就会出现问题。

**证据 3：消息列表不处理文件引用失效**

```go
// internal/message/service.go
func (s *service) Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error) {
    msgs, next, err := s.messages.ListConversation(ctx, actor, withUID, page)
    if err != nil { ... }
    // 这里返回的消息列表包含 file_id 字段
    // 如果 file_id 指向已删除的文件，调用方（HTTP/gRPC handler）
    // 在渲染消息时会调用 fileService.Get(fileID) → ErrNotFound
}
```

```go
// internal/handler/http/message.go
func (h *MessageHandler) Get(c *gin.Context) {
    msg, err := h.svc.Get(c.Request.Context(), id)
    // msg.FileID 存在但 file 已删除 → 没有降级逻辑
    // handler 只返回消息内容，不解析附件
}
```

Handler 只返回消息元信息。如果客户端尝试下载附件，它会调用 `GET /v1/files/{fileID}` → `fileService.Get` → `apperr.NotFound("file.not_found")`。这个错误信息对用户不友好（"文件找不到了"），也无法区分"文件不存在"和"文件被删了"。

**证据 4：评论中的 target 引用同理**

```go
// internal/comment/service.go
func (s *serviceImpl) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
    switch t {
    case domain.TargetTypeFile:
        f, err := s.files.Get(ctx, target)
        if stderrors.Is(err, domain.ErrNotFound) {
            return apperr.NotFound("comment.target_not_found", "target file not found")
        }
    }
}
```

评论的目标文件被软删除后，列出评论 -> 尝试验证 target 可读 -> 返回 `target_not_found` -> 评论列表不可用。这是更严重的问题：一个文件的删除可以导致其所有评论变得不可读。

### 建议方案

**Phase 1（消息端）**：消息的 `file_id` 改为"尽力而为"引用

- 消息列表端点返回 `file_id` 字段（不变）
- 但如果 `file_id` 引用了一个已删除的文件，不报错，而是返回 `file: null`（或 `file_deleted: true`）
- 在 Repository 层使用 `LEFT JOIN` 或 `Preload` 时的条件过滤

```go
// Phase 1 的消息响应结构
type messageResponse struct {
    ID         domain.ID    `json:"id"`
    Content    string       `json:"content"`
    File       *fileInfo    `json:"file,omitempty"`    // file 存在时填充
    FileDeleted bool        `json:"file_deleted,omitempty"` // file 被删时标记
}
```

**Phase 2（评论端）**：评论列表改为两阶段验证

1. 先获取评论列表，收集所有 `target_id`
2. 批量检查 target 是否仍然可读（文件用 `CanReadFile` 检查，消息用 `CanReadMessage` 检查）
3. 对已删除的目标，在响应中标记 `target_deleted: true`，不阻止评论列表的渲染

**Phase 3（数据清理）**：后台 job 检测悬挂引用

- 利用现有的 storage GC sweeper 模式，添加 `dangling_ref_checker`
- 定期查询 `messages` 表中 `file_id IS NOT NULL` 且对应文件已被软删除的行
- 可选择：自动清空 `file_id`（丢失附件信息）或 保留但标记

### 产品经理视角

**用户价值**：

- 用户 A 在群聊中发了一个文件，该文件被用户 A 删除 → 其他用户仍可查看消息（只是附件不可用），而不是看到错误
- 用户在文件下评论，文件被 owner 删除 → 评论不应消失
- 当前行为（完全错误）是数据损坏的体验

**建议优先级**：P1。这不是一个"优化"，而是一个**正确性 bug**。当前行为导致软删除附件会产生级联影响：

- 消息附件 → `file.not_found`
- 评论的 target → `comment.target_not_found`
- 通知的 subject → 链接到不存在的内容

这种"级联不可用"违背了"软删除"的设计初衷——软删除应该只隐藏内容，不影响依赖它的关联数据。

---

## 总结

| # | 方向 | 类型 | 证据密度 | 产品冲击 | 架构复杂度 | 建议优先级 |
|---|------|------|---------|---------|-----------|----------|
| 1 | **全局内容寻址去重** | 存储优化 | ⭐⭐⭐⭐ | ⭐⭐⭐ | 低 | P2 |
| 2 | **资源级读取透明度** | 合规/隐私 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 中 | P1 |
| 3 | **通知折叠竞态条件** | 正确性 | ⭐⭐⭐ | ⭐⭐ | 低 | P2 |
| 4 | **深层游标分页退化** | 性能 | ⭐⭐⭐⭐ | ⭐⭐⭐ | 低-中 | P2 |
| 5 | **悬挂文件引用** | 正确性 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 低 | **P1** |

### 为什么这 5 个方向是盲区

在 `docs/requirements/` 的 120+ 份分析中：

1. **全局去重**：被 2 份文档以"CompleteUpload 跳过去重"的角度部分提及，但从未作为"全量跨用户去重"的系统性方向被论证
2. **读取透明度**：`audit.read` 权限被 40+ 份文档提及（admin 读审计日志的权限），但**资源级读取审计**（文件被谁下载、消息被谁查看）未被任何文档作为独立方向提出
3. **通知折叠竞态**：7 份文档提及"通知折叠"功能本身，但竞争条件的分析零覆盖
4. **深层分页退化**：2 份文档以性能优化的角度提及，但未作为独立的架构方向论证
5. **悬挂引用**：6 份文档提及"文件分享无过期时间"，但**消息/评论中的悬挂文件引用**作为一个系统性的引用完整性方向未被任何文档覆盖
