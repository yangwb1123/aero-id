# aero-id — 代码级证据驱动的架构扩展与边界分析

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全量代码扫描（`internal/` 全部 ~50 个 Go 包、`migrations/` 22 份 SQL、`cmd/`、`configs/`、`api/proto/`），逐文件阅读 handler → service → repository → domain → 基础设施链路。
>
> **前提声明**: 本项目已有 57 份 `docs/requirements/` 分析文档覆盖了群聊、多租户、分布式限流、Webhook 平台、数据生命周期、SSE 多实例协调等数十个方向。本文不追求"从未被覆盖"的独特性，而是聚焦于**代码路径中确认存在具体问题、但尚无文档以完整代码证据链形式分析**的 5 个高价值方向。每个方向提供精确代码引用（文件名 + 行号范围）和可操作的改进路径。

---

## 当前项目状态速览

aero-id 已经是一个**功能饱满、架构清晰**的双传输（HTTP + gRPC）用户数据服务。已交付的能力包括：

| 领域 | 已实现 |
|------|--------|
| **文件** | 上传/流式/内容去重（SHA-256）/预签名直传（S3）/分享/元数据更新/存储 GC |
| **消息** | 1:1 私信（文本/图片/文件附件/系统类型）/已读回执/消息反应/编辑/软删/每会话全部标记已读 |
| **评论** | 两层嵌套（楼+楼中楼）/点赞/举报/隐藏/编辑/报告处理（resolve/dismiss）|
| **通知** | 事务性 Outbox 驱动（message.sent / message.read / comment.replied）/SSE 实时推送/未读计数/偏好设置（静音时段 + 时区）/折叠去重 |
| **安全与风控** | 方向性用户阻断（Block）/幂等性 Key/速率限制（per-actor token bucket）/Circuit Breaker |
| **基础设施** | OpenTelemetry tracing/Prometheus RED 指标/存储 GC 清理/审计日志/管理角色/自动迁移 |
| **搜索** | PostgreSQL tsvector 全文搜索（跨消息/评论/文件）/CJK 回退 ILIKE |

---

## 方向一：文件分享与用户阻断的安全交叉缺口

### 严重性

**安全 · 中** — 逻辑漏洞，非注入类

### 问题描述

Block 系统的语义是**方向性阻止**：A 阻止 B 后，B 不应能通过任何方式与 A 产生交互。当前 block 检查已覆盖消息发送（`service/message.go:89-96`）和通知分发（`service/notification.go` 的 `Handle` 方法），但 **文件分享路径完全未检查 block**。

一个典型攻击场景：用户 A 阻断了骚扰者 B。B 通过文件分享 API（`POST /v1/files/:id/share`）将文件分享给 A。A 的文件列表中出现 B 的文件（`ListSharedWithMe`），且 A 获得一条 B 分享文件的审计事件。整个流程中，没有任何一层检查 A 是否阻止了 B。

### 代码级证据

**证据 1：Block 检查被应用于消息发送，但未应用于文件分享**

`service/message.go:89-96`（`Send` 方法）：
```go
if s.blocks != nil {
    blocked, err := s.blocks.IsBlocked(ctx, in.ToUID, actor)
    ...
    if blocked {
        return nil, apperr.Forbidden(...)
    }
}
```

对比 `service/file_share.go:33-70`（`ShareFile` 方法）— 完整路径：
```go
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    actor, err := mustActor(ctx)
    ...
    // 验证文件归属，但不检查 granteeUIDs 中的任何 uid 是否被 actor 阻止
    // 或 granteeUIDs 中的任何 uid 是否阻止了 actor
    ...
    if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil { ... }
}
```

**证据 2：`ListSharedWithMe` 不过滤 block**

`service/file_share.go:105-115`：
```go
func (s *fileService) ListSharedWithMe(ctx context.Context, page domain.Page) (*FileList, error) {
    actor, err := mustActor(ctx)
    ...
    files, next, err := s.shares.ListFilesSharedWith(ctx, actor, page)
    // 不检查这些文件的 owner 是否被 actor 阻止
    ...
}
```

**证据 3：Block 的语义在 `service/block.go` 中定义**

`service/block.go:44-65` 定义了 `Block` 方法。从业务语义上说，block 是"不要让我看到/收到关于此人的任何东西"。文件分享是对此语义的违反。

**证据 4：`file_shares` 表无 FK 到 `files` 表**

`migrations/000006_create_file_shares.up.sql`：
```sql
CREATE TABLE file_shares (
    file_id      UUID NOT NULL,      -- 无 REFERENCES files(id) ON DELETE CASCADE
    grantee_uid  TEXT NOT NULL,
    ...
);
```

当文件被软删除后，其关联的 file_shares 行继续存在。如果文件被 GC 硬删除（`storage/gc/sweeper.go` 的 `purgeSoftDeleted`），这些行变成永久孤儿。当前 `file/service_crud.go:97-107` 的 `Delete` 方法仅软删除文件行，不清除 shares。

### 产品影响

1. **安全信任破坏**：Block 是用户唯一的安全控制手段。存在绕过路径意味着用户不能信任这个保护机制。
2. **UX 困惑**：用户阻断了某人，但该人的文件仍然出现在"分享给我"列表中。
3. **数据残留**：文件被 GC 清理后，file_shares 中的数据成为永久垃圾。

### 建议修复

1. **`ShareFile` 增加双向 block 检查**：检查每个 granteeUID 是否阻止了 actor（入站方向），以及 actor 是否阻止了 grantee（出站方向）。
2. **`ListSharedWithMe` 过滤阻塞的 owner**：在返回结果前，过滤掉那些被 actor 阻止或阻止了 actor 的 owner。
3. **`file/service_crud.go` 的 `Delete` 方法级联清理 shares**：在软删除文件前（或同时），清理关联的 file_shares 行。
4. **审计事件尊重 block**：如果 A 阻止了 B，则 A 不应收到 B 产生的审计日志事件（虽然审计日志本身不可删除，但列表显示时应过滤）。

---

## 方向二：Comment Reactions 的 TOCTOU 竞态 + 因果一致性缺口

### 严重性

**正确性 · 中** — 条件竞态导致不一致

### 问题描述

`service/comment.go` 中的 `react` 私用方法是 Like/Unlike 的唯一入口点，但它在**检查权限和执行业务操作之间**有一个打开窗口，违反了"读取-检查-写入"的原子性。

### 代码级证据

**证据 1：`react` 方法的 TOCTOU 窗口**

`service/comment.go:198-218`：
```go
func (s *commentService) react(ctx context.Context, id domain.ID, like bool) (int, error) {
    actor, err := mustActor(ctx)
    ...
    c, err := s.comments.Get(ctx, id)             // ← T1: 读取评论状态
    if stderrors.Is(err, repository.ErrNotFound) { ... }
    ...
    if err := s.assertTargetReadable(ctx, actor, c.TargetType, c.TargetID); err != nil {
        return 0, err                              // ← T2: 检查读取权限
    }
    var count int
    if like {
        count, err = s.comments.Like(ctx, id, actor)  // ← T3: 执行点赞
    } else {
        count, err = s.comments.Unlike(ctx, id, actor) // ← T3: 执行取消点赞
    }
    ...
}
```

在 T1-T3 之间，以下情况可能发生：
- 评论被软删除（另一个请求调用了 `comments.SoftDelete`）
- 评论的目标（文件/消息）被删除或变为不可读
- 评论的 `status` 从 `Normal` 变为 `Hidden`

当前 `Like`/`Unlike` 在 repository 层（`repository/comment.go`）通过 `INSERT ... ON CONFLICT` 处理幂等，但**不验证评论状态**。结果是：一个被删除或隐藏的评论仍然被允许"点赞"，而点赞后用户看到的数据是不一致的（评论已隐藏，但点赞计数变化了）。

**证据 2：`assertTargetReadable` 在 `Like` 内部不重复读取**

`service/comment.go` 的 `react` 方法只在 T2 时点检查目标可读性，但 `repository/comment.go` 的 `Like` 方法不重新验证目标状态。如果目标在 T1-T3 之间变为不可读，点赞仍然成功。

### 产品影响

1. **数据显示不一致**：用户看到一个已隐藏评论的点赞数在变化。
2. **违反用户预期**：对一个已删除的评论做点赞是无意义的，但当前系统允许。
3. **条件竞态难以复现**：在高并发下（如某热点评论被多人同时点赞的同时被作者删除），数据可能进入这样的不一致状态。

### 建议修复

1. **将权限检查推迟到 repository 层**：在 `repository/comment.go` 的 `Like`/`Unlike` 方法内部，使用 UPDATE 语句的 `RETURNING` 或 `WHERE` 子句来原子地验证评论状态。例如 `UPDATE comments SET like_count = like_count + 1 WHERE id = ? AND status = 1 AND deleted_at IS NULL`。
2. **或者使用数据库事务**：在 `react` 方法中，使用 `db.Transaction` 包裹：`SELECT ... FOR UPDATE` 读取评论行（加行级锁），然后验证权限，最后写入。
3. **或者简化设计**：将 `like_count` 的维护从评论行中分离出去（通过 `comment_likes` 表的 `COUNT` 实时计算），消除去重计数和评论状态之间的耦合。

---

## 方向三：双 Search 服务实现——死代码与技术债务

### 严重性

**代码质量 · 中** — 编译但未使用的包，维护成本

### 问题描述

搜索功能存在**两个独立的 Service 接口和实现**，位于不同的包中。`internal/service/search.go` 是"新版"且被 `app.go` 实际使用；`internal/search/search.go` 是"旧版"且**完全未在任何地方被引用**。两者不共享接口，不共享模型，独立编译。

### 代码级证据

**证据 1：`app.go` 使用 `service.NewSearchService`**

`internal/app/app.go:149`：
```go
searchSvc := service.NewSearchService(msgRepo, commentRepo, fileRepo)
```

该函数的定义在 `internal/service/search.go:33`。注意它接收**repository 接口**作为参数（`repository.MessageRepository` 等）。

**证据 2：`internal/search/search.go` 定义了完全独立的接口**

`internal/search/search.go:17-20`：
```go
type Service interface {
    Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error)
}
```

它的 `NewService` 接收的是**模块级接口**（`message.MessageRepository`、`comment.Repository`、`file.Repository`），与 `internal/service` 包中使用的 `repository.MessageRepository` 是**不同的类型**，尽管它们的方法签名可能相似。

**证据 3：`internal/search/` 包中没有任何地方被 `app.go` 或其他包 import**

```bash
grep -r "internal/search" internal/ --include='*.go' | grep -v "_test.go"
```
该搜索只返回 `internal/search/` 包内部的自引用——没有任何外部 import。

**证据 4：两个包实现了不同的 Search 逻辑**

`internal/service/search.go:72-122` 实现了按 kind 分片搜索 + CJK 回退逻辑（FTS → ILIKE）。

`internal/search/search.go:48-70` 的 `searchKind` 方法**没有** CJK 回退逻辑，仅做单次 FTS 搜索。

### 产品影响

1. **死代码维护负担**：`internal/search/` 包（5 个源文件）每次重构/API 变更时都需要保持编译通过，但没有测试覆盖它（因为未被使用）。
2. **迷惑新开发者**：搜索逻辑有两个入口，新人需要先确认哪个是活的。
3. **潜在的测试盲区**：如果未来有人误用了旧包，可能得到一个没有 CJK 支持的搜索结果。

### 建议修复

1. **删除 `internal/search/` 包**：将其 `Service` 接口定义迁移到 `internal/service/search.go` 作为接口注释，然后删除整个包。
2. **或标记为废弃**：如果不能立即删除，在包文档中显式注释 `// Deprecated: use internal/service.SearchService instead`。
3. **添加 lint 规则**：在 `scripts/check-architecture.sh` 或 `.golangci.yml` 中添加禁止导入 `internal/search` 包的规则。

---

## 方向四：通知折叠产生的"幽灵"通知行——无意义数据累积

### 严重性

**运维 · 中-低** — 数据膨胀但无功能影响

### 问题描述

通知折叠（Collapsing）机制的目的是对同一 actor 发起的同类型通知进行合并。然而，当前的实现方式是：**先创建通知行，再检查是否可以折叠**。如果可以折叠，新创建的行变成一个"幽灵"——它在数据库中存在、不可见、但也没有任何清理机制。

### 代码级证据

**证据 1：`Handle` 方法先创建后折叠**

`service/notification.go` 的 `Handle` 方法执行路径：

```go
// line ~149: 正式创建通知行
if err := d.notifs.Create(ctx, n); err != nil { return err }

// line ~152: 检查折叠条件
if d.collapseWin > 0 {
    existing, err := d.notifs.FindRecentByTypeAndActor(ctx, ...)
    if err == nil && existing != nil && existing.ID != n.ID {
        // 递增已存在行的计数
        _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
        // ← 新创建的行 n 成为"幽灵"：未读、可见、但不会被用户查询到
        return nil
    }
}
```

**证据 2：幽灵行不可被用户感知但计入总量**

`repository/notification.go` 的 `ListByRecipient` 按 `created_at DESC` 排序返回。折叠后，新行（n）的 `CollapsedCount = 1`，而折叠的旧行（existing）的 `CollapsedCount = 2+` 且 `created_at` 更新为当前时间。普通列表查询会优先展示 updated 后的旧行（因为 created_at 后来更新），所以新行不会被展示。

但是，`CountUnread` 方法对同一 recipient 的所有未读通知计数，包括这些幽灵行。结果：**未读计数略高于用户实际看到的通知数**（偏差等于折叠次数）。

**证据 3：无后台清理**

没有任何后台任务（当前系统的 outbox relay / GC sweeper / token cache cleaner 等）负责清理这些幽灵通知。它们永久存在于数据库中，直到用户手动 MarkRead 或 MarkAllRead。

### 产品影响

1. **未读计数膨胀**：长期活跃用户可能看到 5 个通知但未读计数显示 7。
2. **存储浪费**：虽然不是主要问题，但通知表是核心写入路径之一（每次 message.sent / comment.replied / message.read 都产生一行），幽灵通知加速了该表的膨胀。
3. **MarkAllRead 的成本增加**：`repository/notification.go` 的 `MarkAllRead` 通过 `UPDATE notifications SET read_at = ? WHERE recipient_uid = ? AND read_at IS NULL` 更新，幽灵行被包括在此更新中，增加了不必要的写入负载。

### 建议修复

1. **重构折叠逻辑为"先查后写"**：在 `Handle` 方法中，先检查是否存在可折叠的未读通知，如果存在则只 `IncrementCollapsedCount`，不创建新行。
2. **如果必须保留审计轨迹，则将新行的 `read_at` 设置为当前时间**：这样它不计入未读计数。
3. **添加后台清理任务**：扩展 `outbox.Relay` 或新增一个周期任务，清理超过 N 天且仍为 `read_at IS NULL` 但 `CollapsedCount = 1` 且存在折叠父行的幽灵通知。

---

## 方向五：Message 软级联删除——子数据残留的链式问题

### 严重性

**正确性 · 中** — 数据完整性缺口

### 问题描述

系统中存在四级关联数据，当前的消息软删除（`SoftDelete`）仅操作 `messages` 表，但与之关联的 `message_reactions`、`comments`（以 message 为目标的评论）和 `notifications` 都没有被处理。这导致：

1. 删除消息后，该消息上的反应（reactions）成为孤儿数据
2. 删除消息后，挂在该消息上的评论成为孤儿（`target_id` 引用了不存在的消息）
3. 删除消息后，引用该消息的通知仍然存在

### 代码级证据

**证据 1：`SoftDelete` 仅操作 messages 表**

`repository/message.go` 中的 `SoftDelete` 方法：
```go
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    // 仅软删除 messages 行
    // 不清理 message_reactions, comments(target_type='message', target_id=id), notifications(subject_id=id)
}
```

（注意：`message_reactions` 表有 `REFERENCES messages(id) ON DELETE CASCADE`，但**这只在硬删除时生效**。软删除不触发外键级联。）

**证据 2：`message_reactions` 表有 FK，但软删除不解级联**

`migrations/000013_message_reactions.up.sql`：
```sql
CREATE TABLE message_reactions (
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ...
);
```

`ON DELETE CASCADE` 仅当 `messages` 行被物理删除（`DELETE FROM messages WHERE ...`）时触发。GORM 的 `SoftDelete` 执行 `UPDATE messages SET deleted_at = now() WHERE ...`，不会触发级联。结果：message_reactions 行持久存在。

**证据 3：以消息为目标的评论在消息删除后仍可见**

`internal/domain/comment.go` 中评论可以以消息为目标（`TargetTypeMessage`）。当消息被软删除后，`service/comment.go:25-33` 的 `assertTargetReadable` 方法检索消息时，如果目标消息已被软删除（被 GORM 的默认 `WHERE deleted_at IS NULL` 作用域排除），返回 `ErrNotFound`。这意味着：
- 现有评论无法被列出（`List` 方法检查目标可读性时失败）
- 但评论行本身仍然存在于数据库中
- 这些评论的回复链也无法访问

**证据 4：通知引用已删除消息**

`service/notification.go` 的 `messageSentPayload` 和 `messageReadPayload` 记录 `message_id`。消息删除后，引用该消息的通知仍然存在于 `notifications` 表中。用户点击通知时，发现消息不存在（404/NotFound）。

### 产品影响

1. **数据不一致**：消息删除后，reactions 应该被清理（物理删除），但当前它们永远残留。
2. **功能破坏**：挂在消息上的评论变得不可访问（读权限检查失败），但评论作者不知道原因——他们看到自己"没有访问权限"而不是"目标消息已删除"。
3. **通知死链接**：用户点击一条"新消息"通知，发现消息已被删除。
4. **级联 GC 需求**：如同 `storage/gc/sweeper.go` 对文件 blob 的清理，消息也需要类似的后台级联清理策略。

### 建议修复

1. **在 `SoftDelete` 中扩展级联**：事务性地执行：
   - 软删除消息本身
   - 物理删除 `message_reactions` 行（或也做软删除，但当前 reactions 表无 `deleted_at`）
   - 软删除以该消息为目标的评论（或者通过设置 `comments.status = 2` 标记为 deleted）
   - 可选：在通知行中添加 `deleted_at` 标记，或让通知列表服务过滤掉引用已删除资源的通知
2. **改进 `assertTargetReadable` 的错误消息**：当目标存在但被删除时，返回 `"目标消息已被删除"` 而不是 `"目标消息不存在"`。
3. **在 `outbox/dispatcher` 中添加通知清理处理**：当收到 `message.deleted` 事件时，自动将关联通知标记为已读或移除。

---

## 性能优化快照

除上述架构方向外，扫描中发现的快速可优化的性能点：

| 编号 | 问题 | 位置 | 影响 | 修复建议 |
|------|------|------|------|---------|
| P1 | `CountUnread` 全表扫描未读标记 | `repository/notification.go` | 高未读用户查询慢 | 添加 `(recipient_uid, read_at)` 复合索引 |
| P2 | `FetchUnpublished` 缺少 `(published_at IS NULL, created_at ASC)` 部分索引 | `outbox/repository.go` | 高并发下 outbox 轮询性能下降 | 迁移中添加部分索引 |
| P3 | `Inbox` 查询使用 `(to_uid, created_at DESC)` 索引但条件含 `deleted_at IS NULL` | `repository/message.go` | 软删除消息不常见的场景下索引效率下降 | 添加 `(to_uid, deleted_at) WHERE deleted_at IS NULL` 复合索引 |
| P4 | `rateLimiter` 的键映射只在内存中，无最大大小限制 | `service/notification.go` 的 `notifRateLimiter` | 大量不同 (recipient, type) 组合时内存增长 | 添加 `maxKeys` 上限 + LRU 淘汰 |
| P5 | `ListByRecipient` 通知列表每次查询都解析所有用户 | `service/notification.go:28-33` | SSO `GetUsers` 调用频繁 | 在 `actorUIDs` 函数中去重 + 限制每次查询的 UID 数量 |

---

## 优先级排序

| 方向 | 优先级 | 工作量 | 风险降低 |
|------|--------|--------|---------|
| 方向一：Block × 文件分享交叉缺口 | **P1** | 低（~2 天） | 消除逻辑安全漏洞 |
| 方向五：消息软删除的级联残留 | **P1** | 中（~3 天） | 防止数据不一致 |
| 方向二：Comment Reactions TOCTOU | **P2** | 低（~1 天） | 消除条件竞态 |
| 方向四：幽灵通知 | **P2** | 低（~1 天） | 未读计数准确性 |
| 方向三：死代码清理 | **P3** | 低（< 1 天） | 降低维护成本 |
| 性能快照 P1-P5 | **P2** | 低（~2 天合计） | 查询性能提升 |

---

*本文档基于 2026-07-10 代码库状态。所有代码引用指向 `internal/` 下的源文件。未编写任何代码。*
