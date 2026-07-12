现在我掌握了整个架构的全面情况。以下是架构分析。

---

# 架构分析：aero-id 服务

## 1. 架构评估

### 1.1 优势

**分层清晰，职责分离出色。** `handler → service → repository → DB` 的单一方向依赖严格执行，没有跨层短路。HTTP 和 gRPC 传输层共享同一个 `service` 接口集，这在 `app.go` 的组合根中得到了验证，是服务在双协议下行为一致的正确模式。

**领域模型集中。** `internal/domain/` 包包含了所有实体（`File`、`Message`、`Comment`、`Notification`）以及关键的不变约束（如 `ValidateNewComment` 强制两层嵌套规则）。该包不依赖于任何基础设施，可以作为独立库进行测试。

**基础设施使用良好。** UUIDv7 主键在 `idgen` 包中实现，自然按时间排序，再加上 `(created_at DESC, id DESC)` 排序使它们成为分页游标的很好选择。Outbox 模式通过 `outbox.Event` → `NotificationDispatcher` 实现同步事件处理，在消息创建等写路径中提供可靠的一次性通知传递——比最终一致的事件轮询更好。

**函数式选项模式。** 服务构造函数（`NewFileService`、`NewMessageService` 等）通过 `FileOption`、`MessageOption` 变体接受可选依赖项，使得可以在不更改公共 API 的情况下干净地切换共享、存储引用计数、审计等功能。

### 1.2 局限性

**双重服务层——最关键的架构债务。** 代码库在以下位置包含两套独立的文件、消息和评论实现：

| 域 | 旧包 | 新包 |
|---|---|---|
| File | `internal/file/service*.go` | `internal/service/file*.go` |
| Message | `internal/message/service.go` | `internal/service/message.go` |
| Comment | `internal/comment/service.go` | `internal/service/comment.go` |

组合根使用新的服务，但旧包仍然存在、已编译、可导入。这带来了严重问题：

- `internal/file/service_crud.go` 中的旧 `Delete` 不使用 `storage_refs` ——它回退到 `CountByStorageKey`。如果任何代码路径（哪怕是测试代码）错误地连接到旧服务，它将跳过引用计数，并可能在引用计数仍为正数时错误地删除 blob。
- `domain.MustActor`（旧包使用的）与 `service.MustActor`（新包使用的）不同。它们各自具有不同的错误代码，这是对 DRY 原则的违反，可能导致不一致的 401 响应。
- 架构约束（<500 行文件、<50 行函数）正在被旧包违反。例如，`internal/comment/service.go` 很可能会超过这些限制。

**设计文档（2026-05-22）与当前代码库之间的漂移。** 设计文档明确标记以下内容为"超出范围"：outbox、SSE、全文搜索、Prometheus metrics、OpenTelemetry tracing。实际代码库已经实现了所有这些。设计文档是一个假设性文档——并非强制执行——但文档和代码之间 100% 的差异意味着新团队成员的入职材料与实际系统几乎没有重叠。

**搜索子系统缺少全局排序。** 搜索是针对每个仓库并行发起三个查询，然后对结果进行简单的 `append`，不进行跨实体去重、不进行分数归一化、不进行全局排序。其核心缺失的是一个顶级搜索接口，该接口对所有命中的实体应用统一的排序和分页策略。

**存储引用计数存在缺口。** `CompleteUpload`（`internal/service/file_presign.go`）明确跳过 `refs.Increment`。如果一个通过预签名上传创建的 blob 与后续的普通上传通过内容去重共享相同的存储键，则引用计数永远不会代表正确的共享状态。GC 可能会错误地回收该 blob。

### 1.3 设计决策是否合理

**决策：无本地账户表，仅通过 SSO 引用 uid（D1）— ✅ 合理。** 这正确地实现了"SSO 拥有身份"这一不可变约束。将 SSO 验证的 uid 作为纯字符串通过上下文传递，避免了与外部系统的数据一致性问题。UserDirectory 接口提供了对 SSO 用户信息的抽象，并且缓存层（token cache + user cache）在 TTL 和资源占用之间取得了适当的平衡。

**决策：GORM 而非 sqlx（D3）— ⚠️ 需要重新审视。** 设计决策声明"CRUD 多于复杂查询"。然而，实际系统现在包括全文搜索（`SearchMessages`、`SearchComments`、`SearchFiles`），它们使用原始 SQL 绕过了 GORM。通知折叠（`FindRecentByTypeAndActor`）和分页列表本应是范围查询，但 GORM 的 API 掩盖了底层索引的行为。随着系统的发展，GORM 在选择性列更新映射（`UpdateMetadata` 中的 `datatypes.JSONMap`）和事务处理之上并没有提供很多价值。

**决策：not-outbox（设计中的 D9）— ⚠️ 与当前实现冲突。** 设计文档明确排除了 outbox（"超出范围：后台 worker / 消息队列 / outbox"）。代码库现在拥有完整的 outbox 实现（`internal/outbox/`），并且 `NotificationDispatcher` 是作为 `outbox.Handler` 挂钩的。添加 outbox 是正确的架构决策（它使通知传递成为可靠的一次性操作，而不是尽力而为）。然而，文档并未反映这一决策。新团队成员只能在阅读代码而不是设计文档时发现这一点。

**决策：启动时不自动迁移（D10）— ✅ 合理。** 容器化部署中避免迁移竞争是一个经过验证的模式。`auto_migrate` 配置选项是可选的，默认情况下关闭。`cmd/migrate` 作为一个单独的入口点存在，可以被 CI/CD 管道独立调用。

**决策：函数式选项用于可选依赖 — ✅ 合理。** 对于像 `storage_refs`、`audit`、`shares` 这样的功能，它们在变更通过业务路径时添加或移除，函数式选项模式允许干净的向后兼容。旧代码库文件（如 `internal/notification/service.go`）已经自然消亡，因为组合根新代码使用了新模式。

### 1.4 架构债务总结

| 债务 | 影响 | 修复成本 |
|---|---|---|
| 双重服务层 | 已编译的 3 个包的未使用代码；架构约束检查被绕过 | 中（删除旧文件 + 重定向测试） |
| 设计文档与代码库之间的漂移 | 入职混淆；错误的决策参考 | 低（更新 .md 或将其降级为历史记录） |
| CompleteUpload 中缺失 refs.Increment | blob 可能在创建后立即被 GC 回收 | 低（添加 3 行） |
| 搜索缺少全局排序/去重 | 搜索结果对于跨实体查询没有意义 | 高（需要新的搜索基础设施） |
| FindByChecksum 在旧服务中为每个所有者一条，但索引是全局的 | 去重无法按预期扩展 | 低（将查询改为全局 + 迁移索引） |
| 通知折叠竞态 | 罕见但会产生僵尸通知行 | 中（需要基于 SQL 的数据驱动折叠） |

---

## 2. 扩展方向

### 方向 A：资源引用完整性——确保引用边界的正确语义

**为什么需要（业务价值）。** 这是一个 P0 方向，因为悬挂的文件引用是一个功能性错误：删除一条评论引用的文件会阻止所有评论被列出（`assertTargetReadable` → `files.Get` → 404 → 整个列表失败）。删除一条作为附件附加到消息中的文件会使该消息在客户端呈现为损坏（`file_id` 返回 404）。这与"软删除"的语义完全背道而驰，软删除中删除的是对资源的访问权限，而不是资源本身。

**核心挑战和技术难点。**

1. **软删除引用有三条路径，每条都有不同的语义：**
   - *文件 → 消息附件*：`messages.file_id` 引用 `files(id)`。当文件被软删除时，消息应保留（其 `meta` 可能缓存了文件名/大小快照），但下载应优雅地失败（"此附件不再可用"），而不是以 404 崩溃。
   - *文件 → 评论目标*：`comments.target_id` + `target_type='file'`。当被删除时，评论列表应显示现有评论而不检查目标的是否存活。目标检查仅应在评论创建时强制执行（这是正确的——用户无法对看不见的内容进行评论），而不是在列表创建时。
   - *评论 → 父评论*：`comments.parent_id REFERENCES comments(id)`。由于默认的 `NO ACTION` 外键，软删除不会失败，但列表查询必须排除其父级已被删除的子评论。

2. **目标可访问性检查与可见性检查是不同的概念。** 当前代码库将两者混为一谈：`assertTargetReadable` 在列出和创建时都检查目标的存在性和可见性。目标的存在性检查应仅在创建时执行。在列出时，应跳过目标存在性检查，或者列出的结果应优雅地指示目标缺失（如 YouTube 的"[视频不可用]"占位符），而不是使整个列表崩溃。

3. **`Comment.List`** 是受影响最严重的路径：`assertTargetReadable` 调用 `files.Get`，当目标已软删除时返回 `ErrNotFound`，这会将 `assertTargetReadable` 转换为 `NotFound("comment.target_not_found")`。`Comment.List` 透明地传播这个错误。评论是软删除文件上的独立实体；它们应保持可访问。

**预期的架构变更。**

```
当前路径：
List(ctx, targetType=file, targetID=X)
  → assertTargetReadable(actor, file, X)
    → files.Get(X) → 如果 deleted_at 则 ErrNotFound
      → 返回 NotFound("comment.target_not_found") ← 列表完全崩溃

期望路径：
List(ctx, targetType=file, targetID=X)
  → 跳过目标存在性检查
  → comments.ListByTarget(targetType, targetID) WITH deleted_at IS NULL
  → 返回评论，不检查文件是否软删除
```

具体变更：

- `commentService.List` 和 `commentService.Replies`：用仅可见性检查（"我可以读取此目标吗？"）替换 `assertTargetReadable`，跳过存在性检查（"此目标是否存在？"）。或者，添加一个单独的 `canAccessTarget` 方法，仅检查可见性。
- 引入一个 `assertTargetVisible(ctx, actor, targetType, targetID) error`，它只检查可见性（对于文件：`files.Get` 如果软删除则跳过；对于消息：检查参与状态）。
- 添加 API 响应字段以指示附件的目标不可用：`{"message": {...}, "file": null, "file_unavailable": true}`。
- `messages.file_id`：在消息响应中返回文件元数据的快照（文件名、大小），这样即使文件后来被删除，消息渲染仍然有效。

**对现有系统的影响。**

- 低影响。评论和消息列表的所有测试都需要更新，以期望新行为（不崩溃）。
- 没有新的依赖项。
- 向后兼容：API 响应添加了 `file_unavailable` 字段，但如果没有文件，则设置为 `false`；现有客户端在解包时不会崩溃（它们已经必须处理可空的 `file_id` 字段）。

---

### 方向 B：读取审计——用于合规责任的可审计读取操作

**为什么需要（业务价值）。** 该服务为身份敏感资源（文件、消息、评论）处理私有的、每个用户的数据。对于合规性（SOC 2、GDPR 审计线索），服务需要回答"谁在何时看到了什么？"当前审计仅记录 8 个变异动作，没有读取动作。在存在 SSO 断言身份的情况下，每个读取操作最终都可能受到合规审计。

**核心挑战和技术难点。**

1. **匿名下载路径（`/files/dl`）缺少用户上下文。** 匿名端点通过 HMAC 签名进行身份验证，但不建立经过身份验证的用户上下文。要将读取事件归属于用户，签名者必须将用户 UID 嵌入签名有效负载中，修改后的 `SignedDownloader.VerifyPresignedURL` 将其恢复。这将从根本上改变下载 URL 的生成方式（需要每个用户、每个文件的签名而不是每个文件的签名）。或者，匿名读取可能被批量审计（"这些 N 个文件在 T 时刻被未知的 actor 读取"），但对于合规目的来说，这并不理想。

2. **读取事件量很大。** 与写入不同，读取在每次页面加载时成批发生（列出 20 条消息 → 20 次"读取"审计操作）。审计日志实现必须是非阻塞的（当前 `AuditLogger` 接口规定"日志记录必须从调用方角度是非阻塞的；日志失败不得导致主要操作失败"）。批量异步日志记录（通道 + 工作池）是必需的，以避免在审计日志记录上阻塞请求。

3. **审计事件的去重和顺序很重要。** 审计跟踪必须具有有意义的顺序。批量检索使排序变得复杂。此外，由于读取经常发生，为每个读取事件写入单独的行可能会产生大量的审计表。需要批次化（在 N 次读取或窗口到期时刷新）。

**预期的架构变更。**

- 添加读取审计常量（`AuditFileRead`、`AuditMessageRead`、`AuditConversationRead`、`AuditCommentList`、`AuditNotifList`、`AuditSearchQuery`）。
- 引入一个异步 `AuditBus`（通道 + 工作池），服务将审计事件推送到其中，并以批次形式写入 `audit_log` 表。
- 通过修改签名有效负载以包含 `actor_uid`，使匿名下载路径支持经过身份验证的审计。
- 添加可配置的采样率（"仅审计 N% 的随机读取"）以控制生产中的读取审计量的增长。

**对现有系统的影响。**

- 中高影响。匿名下载路径需要签名方案更改。所有列表端点都需要添加 `audit.Log` 调用。
- 新的依赖项：无（通道就在标准库中）。
- 向后兼容：如果未启用审计，则为无操作。新的 `AuditBus` 组件默认情况下是可选的（零配置 == 无操作）。

---

### 方向 C：可组合的搜索基础设施——全局排序和分页的搜索

**为什么需要（业务价值）。** 当前搜索连接来自三个并行查询的结果，而不进行全局排序、去重或分数归一化。如果一条消息和一条评论都匹配，则它们的顺序本质上是根据查询执行顺序随机的。没有跨实体的搜索——用户无法同时搜索他们的文件和消息并查看按相关性排序的结果。

**核心挑战和技术难点。**

1. **PostgreSQL 全文搜索与跨实体排序：** `ts_rank` 分数在实体类型之间不可比较。一条长度为 5 的匹配消息的 `ts_rank` 可能远低于一条长度为 200 的匹配评论，仅仅因为评论有更多的匹配空间。引入标准化（`ts_rank / GREATEST(length, min_len)`）很脆弱，并且因查询而异。

2. **搜索范围的 ACL：** 文件范围受 `owner_uid` 限制（用户只能搜索自己的文件）。消息范围受参与方限制（搜索 `(from_uid, to_uid)` 交集）。注释范围受目标可访问性限制。一个统一的搜索必须将所有这些 ACL 条件组合成一个单一的查询，这可能是复杂的。当前方法（并行查询，每个查询都正确应用 ACL）更容易推理，但放弃了全局排序。

3. **类型 + 去重：** 跨实体类型没有固有的"去重"概念——没有"搜索命中"的全局主键。去重需基于匹配的文本/内容进行，这引入了近似匹配的逻辑。

**预期的架构变更。**

**选项 A（推荐，中等复杂度）：实体特定的搜索 + 应用程序级别的合并。**
- 保留每个实体的 FTS 查询（它们是正确且范围受限的）。
- 添加一个 `search_rank` 列，在每个实体级别计算（例如，在 `messages` 表中有一个 `search_rank` 字段，在写入/更新时预先计算 `ts_rank`）。
- 应用程序级别的搜索协调器：
  1. 并行查询 N 个实体，每个实体最多 `limit` 条结果。
  2. 将结果合并到一个全局列表中（按 `search_rank DESC, id DESC` 排序）。
  3. 截断为 `limit` 条结果并返回。

**选项 B（高影响，但正确）：专用的搜索索引表。**
- 创建一个 `search_entries(message_id, comment_id, file_id, content, owner_uid, search_vector)` 表，该表使用触发器或应用程序逻辑使所有实体保持同步。
- 对统一表进行单个 FTS 查询，通过 `owner_uid` 过滤以进行 ACL 检查。
- 这允许跨实体排序，但需要维护一个投影表，该投影表与源表保持同步（棘手的删除/更新传播）。

**对现有系统的影响。**

- 中影响（选项 A）到高影响（选项 B）。
- 选项 A 可以逐步实现：首先，添加全局排序（在返回之前对结果进行排序，不进行去重）。然后，添加分数归一化。最后，添加去重。每个步骤向后兼容。

---

### 方向 D：无竞态的数据库内通知折叠

**为什么需要（业务价值）。** 通知折叠减少了收件箱的杂乱：来自同一 actor 的重复通知（同一用户发送 5 条消息 → 一条折叠的通知，显示"5 条新消息"）。当前实现在 `Create` 之后、在 `FindRecentByTypeAndActor` 和 `IncrementCollapsedCount` 之前有一个竞态窗口。同时发生的事件会创建孤立的重复行，这些重复行永远不会被清理。

**核心挑战和技术难点。**

1. **使折叠成为数据驱动的操作：** 当前模式（`Create` → `FindRecent` → `Update`）本质上是脆弱的。正确的方法是使用 `ON CONFLICT` 在单个 SQL 语句中执行折叠。"如果存在匹配的未读通知，则递增其计数；否则，插入一条新的。"这消除了竞态窗口。

2. **PostgreSQL 的 `ON CONFLICT` 需要一个唯一约束：** `(recipient_uid, type, actor_uid, read_at IS NULL)` 上的部分唯一索引在 SQL 中不能作为 `ON CONFLICT` 目标使用。`ON CONFLICT` 仅适用于具有`WHERE`子句的唯一索引，而 GORM/PostgreSQL 不支持部分唯一索引。解决方案是使用一个包含 `read_at` 的复合唯一索引，并将折叠限制为尚未读取的通知。

3. **时间窗口：** 折叠在时间窗口内（例如，5 分钟）。数据库实现需要计算开始时间。这可以是一个带有 `(recipient_uid, type, actor_uid)` 的 `ON CONFLICT DO UPDATE` 和一个检查 `created_at >= NOW() - window` 的 `WHERE` 子句。

4. **清理孤立的行：** 即使使用数据库内折叠，也可能发生竞态（两个并发请求同时 `INSERT`，两者都发现没有匹配，然后两者都提交）。需要一个后台清理器（REINDEX，`DELETE WHERE collapsed_count = 1 AND id NOT IN (SELECT id FROM ...)`）或一个 `UNIQUE` 约束 + `ON CONFLICT DO NOTHING` 来处理这种情况。

**预期的架构变更。**

- 将 `Notification.Create` 替换为原子折叠操作：
  ```sql
  WITH existing AS (
    SELECT id, collapsed_count FROM notifications
    WHERE recipient_uid = $1
      AND type = $2
      AND actor_uid = $3
      AND read_at IS NULL
      AND created_at >= NOW() - $4::interval
    ORDER BY created_at DESC
    LIMIT 1
  ), update_existing AS (
    UPDATE notifications SET collapsed_count = collapsed_count + 1, created_at = NOW()
    WHERE id = (SELECT id FROM existing)
    RETURNING id, collapsed_count
  ), insert_new AS (
    INSERT INTO notifications (id, recipient_uid, type, actor_uid, subject_type, subject_id, collapsed_count, created_at)
    SELECT $5, $1, $2, $3, $6, $7, 1, NOW()
    WHERE NOT EXISTS (SELECT 1 FROM existing)
    RETURNING id, collapsed_count
  )
  SELECT id, collapsed_count FROM update_existing
  UNION ALL
  SELECT id, collapsed_count FROM insert_new;
  ```
- 保留后台清理任务以删除孤立的行（`collapsed_count = 1` 且存在匹配的折叠行）。
- 如果行在折叠后被读取，则创建一个新行（读取会退出折叠窗口）。

**对现有系统的影响。**

- 影响中等。`NotificationDispatcher.Handle` 的实现需要重写以使用原子 SQL。同时发生的操作将停止产生孤立的行。无竞态的折叠需要新的迁移，但不会改变 API 表面。
- 向后兼容：现有未折叠的通知会保留下来。

---

### 方向 E：全局内容寻址去重——跨所有者的存储键去重

**为什么需要（业务价值）。** 存储成本：如果一个用户上传了文件 *A*，而另一个用户上传了相同的文件 *B*（相同的内容摘要），则存储两次 blob 会浪费存储空间。对于面向媒体的系统（图像、视频），去重可以显著降低存储成本。当前的 `FindByChecksum` 是按所有者进行查询的（`WHERE owner_uid = ? AND checksum = ?`），这阻止了跨所有者去重。此外，`idx_files_checksum` 索引是 `(checksum)`，而不是 `(owner_uid, checksum)`，这表明最初的迁移设计意图是全局去重——只是服务层从来没有做到。

**核心挑战和技术难点。**

1. **授权复杂性：** 跨所有者去重引入了授权问题：当用户 A 上传一个与用户 B 之前上传的文件摘要匹配的文件时，A 的 File 行应该引用 B 的 `storage_key`。B 不能删除该 blob，因为这意味着 A 的引用也会被删除。当前基于所有者的 `FindByChecksum` 避免了这个问题，因为每个所有者都拥有自己的 blob 副本。

2. **引用计数必须是正确的：** 当前的 `storage_refs` 实现通过 `Increment`/`Decrement` 进行原子引用计数。当启用全局去重时，每个 `Create` 必须 `Increment`（不是仅通过 dedup 命中时），而每个 `Delete` 必须 `Decrement`。当前代码仅在新创建的文件通过 dedup 匹配时递增，这是一个缺口。

3. **迁移现有的 blob：** 在启用全局去重之前，必须迁移所有现有 blob 以拥有准确的引用计数。`storage_refs` 表必须以每个现有 blob 的正确引用计数为种子。

4. **`CompleteUpload` 路径：** 直接通过预签名 URL 上传到 S3 的文件的摘要未知。对于这些文件，去重不可能实现。它们应始终获取自己的新 `storage_key`。

**预期的架构变更。**

- 将 `FindByChecksum` 从 `WHERE owner_uid = ? AND checksum = ?` 更改为 `WHERE checksum = ?`（仅摘要）。这立即启用跨所有者去重。
- 使 `refs.Increment` 成为所有 `Create` 路径（包括 `CompleteUpload` 之后）的必需步骤，而不仅仅是通过 dedup 命中的路径。
- 添加一个迁移以为 `storage_refs` 表播种所有现有 blob 的准确计数。
- 在 `Delete` 中更新 blob 清除逻辑，以使用引用计数（当前代码对旧路径使用 `CountByStorageKey` 回退，对新路径使用 `refs.Decrement`）。
- 向 `PresignUpload` 结果添加 `reused bool` 标志，这样客户端就知道 blob 被共享了，还是为他们专门创建的。

**对现有系统的影响。**

- 中度影响。API 表面未变（`POST /files` 仍然返回 `File`）。内部 dedup 行为从"所有者可以引用他们自己的上传"变为"文件引用任何所有者的匹配上传"。删除权限保持不变（只有所有者可以删除，但删除现在仅解除引用，如果引用计数降至 0 则实际移除 blob）。
- 存储成本：减少，因为具有相同内容摘要的文件共享一个 blob。
- 授权：删除一个文件不再"隐藏"它——它只是解除引用。如果一个 blob 被两个所有者引用，删除一个不会删除该 blob。这是正确的。

---

## 3. 接口设计原则

### 3.1 关键模块接口

**文件服务接口（`FileService`）是合适的。** 它将创建、读取、列出、删除、共享、上传和元数据操作暴露为单独的方法。每个方法都采用 `context.Context` + 输入 DTO 并产生一个明确的返回类型。这是一个好的 RESTful 设计。

**但是，`FileService.CompleteUpload` 缺少 `refs.Increment`。** 这是接口实现中的一个错误，而不是接口本身。修复方法是简单地在 `CompleteUpload` 中的 `s.files.Create` 之后添加 `s.refs.Increment(ctx, f.StorageKey)`。目前，没有接口更改。

**`SearchService.Search` 接口太天真了。**
```go
Search(ctx, query string, kinds []SearchKind, limit int) ([]SearchHit, error)
```
它返回一个无序的命中列表。实体缺失 `search_rank` 字段，并且返回类型不公开分数。更好的设计：
```go
Search(ctx, query string, opts SearchOptions) (*SearchResult, error)
type SearchOptions struct {
    Kinds []SearchKind
    Limit int
    Sort  SearchSort // relevance | date
}
type SearchResult struct {
    Hits      []SearchHit
    TotalHits int                // per-kind counts, for UI
    NextQuery string             // opaque cursor for pagination
}
type SearchHit struct {
    Kind    SearchKind
    Score   float64
    Message *MessageResponse
    Comment *CommentResponse
    File    *FileResponse
}
```

**审计接口（`AuditLogger`）适用于写入路径，但缺乏批量处理和采样。**
```go
type AuditLogger interface {
    Log(ctx context.Context, e *AuditEvent) error
}
```
对于读取审计，这不够好用：每个 HTTP 请求可能生成数十个读取审计事件。建议引入一个批处理审计接口：
```go
type AuditBus interface {
    Log(ctx context.Context, e *AuditEvent)            // one-off, for writes
    LogBatch(ctx context.Context, events []AuditEvent) // batch, for reads
}
```
实现使用内部的 `chan []AuditEvent` + 工作池将批次写入 `audit_log`。

### 3.2 新的抽象层

**需要：一个统一的搜索协调器。** 当前搜索跨三个实体进行分片。一个 `SearchCoordinator` 层可以封装跨实体查询、合并和评分逻辑，从而 `SearchHandler` 不必知道这些细节。

**不需要：每个实体的读取审计层。** 读取审计最好通过中间件处理（"每次读取操作完成后，记录一条审计日志"），而不是通过显式的服务调用。中间件可以拦截响应并在返回之前记录审计事件，这避免了污染服务层。

### 3.3 向后兼容性

对于方向 A（悬挂引用）：
- 对 `GET /messages/:id` 和 `GET /files/:id` 的攻击最小。这些端点已经返回了可空的 `file_id`；添加一个 `file_unavailable: true` 字段是一个向前兼容的更改。
- 评论列表不再对 `target_not_found` 返回 `404`，而是应返回剩余评论。当前客户端可能期望 `404` 并破坏 UI。建议的路径是：返回 `200`，但将 `target_available: false` 添加到响应中，以便客户端决定是否显示占位符或隐藏评论。

对于方向 D（折叠竞态）：
- 完全向后兼容。折叠逻辑是内部的。发送到线缆上的通知与以前具有相同的结构。唯一的区别是折叠窗口是原子的，并且不产生孤立的行。

对于方向 E（全局去重）：
- 向后兼容。`POST /files` 的返回类型没有变化。如果出于合规原因需要，客户端可以检查 `reused: true` 标志。
- 删除语义在与用户交互时有所变化（"删除"解除了引用，但不一定是物理移除）。这可能会影响对共享 blob 安全有要求的用户。应在 API 文档和 UI 中沟通这种行为。

---

## 4. 技术选型

### 4.1 新依赖项的评估标准

| 标准 | 权重 | 说明 |
|---|---|---|
| 成熟度 | 高 | 首选经过实战检验的库（在 Go 生态系统中存在 >3 年） |
| 依赖计数 | 中 | 零依赖或低可传递依赖的库 |
| 与 GORM 的兼容性 | 中 | 评估中的库不得与 GORM 冲突或需要数据库迁移 |
| 许可 | 高 | Apache 2.0、MIT、BSD。避免 AGPL |
| 云原生兼容性 | 低 | 不得与 kubernetes、无服务器部署冲突 |
| 学习曲线 | 低 | 库的概念必须易于由单人团队理解 |

### 4.2 是否引入新的技术栈

**无需立即引入。** 当前的 Go 代码库可以对所有 5 个方向进行必要的更改，而无需新的第三方依赖项。以下具体方向可能会有不同的选择：

- **全文搜索：** 当前实现使用 PostgreSQL 的 `tsvector` + `ts_rank`。对于 MVU 来说这已经足够了。如果需要更高级的搜索（模糊匹配、同义词、跨实体搜索），则应评估 **Meilisearch** 或 **Typesense**（轻量级、低运维成本的搜索后端）。目前不需要——PostgreSQL FTS 已经足够好了。

- **通知折叠：** 所有必要逻辑都可以通过 PostgreSQL SQL（带 CTE 的 `ON CONFLICT DO UPDATE`）实现。无需新依赖项。

- **读取审计：** Go 标准库的 `chan` + `sync.WorkerPool` 模式无需外部依赖即可处理批量审计日志。如果审计卷在运营上变得显著，可以使用 **Loki**（Grafana 的日志系统）来摄取和查询审计日志，但这是运营方面的考虑，不是代码库的依赖项。

- **引用计数：** 当前 `Increment`/`Decrement` 实现使用带有 `ON CONFLICT DO UPDATE` 的 GORM。这是正确的做法，无需更改。

### 4.3 构建 vs 采购

| 方向 | 构建 | 采购 | 决策 |
|---|---|---|---|
| 读取审计 | 低工作量。基于通道的审计总线 < 200 行 | 对于此卷来说，不需要现成的审计日志系统 | **构建** |
| 搜索 | 手动跨实体合并 < 100 行 | Meilisearch 需要运行一个单独的服务 | **构建**，如果搜索要求变得高，则重新评估 |
| 通知折叠 | 用 CTE 替换 GORM 创建 < 50 行 | 完全没有市场 | **构建** |
| 全局去重 | 将 per-owner 查询替换为全局查询 < 20 行 | 存储去重通常是平台功能（MinIO/S3 版本控制），但在这里应用成本更高 | **构建** |
| 引用完整性 | 跳过空目标检查 + 返回占位符 < 50 行 | 无 | **构建** |

对于所有 5 个方向，"构建"都是明显正确的选择。学习曲线很低，代码变更量很小，并且没有现有的第三方库专门解决这些特定领域的问题。

---

## 5. 实施路线图

### 5.1 优先级排序（P0/P1/P2）

| 方向 | 优先级 | 理由 |
|---|---|---|
| **#5 悬挂文件引用** | **P0** | 功能性错误。软删除一个文件会级联破坏其所有评论的列表，并使消息附件呈现损坏。这是当前生产环境会发生的错误。 |
| **#1 全局去重缺口** | **P1** | `CompleteUpload` 中缺失的 `refs.Increment` 是数据完整性错误。blob 可能在用户最终进行引用它的 `Create` 之前被 GC 回收。 |
| **#2 读取透明度** | **P1** | 合规要求。对于身份敏感的数据，没有读取审计是 SOC 2/GDPR 审计的一个缺口。 |
| **#3 通知折叠竞态** | **P2** | 罕见，产生可清理的孤执行行。影响很小。 |
| **#4 深层分页退化** | **P2** | 搜索排序（缺少全局合并）比深层游标扫描更值得修复。 |

### 5.2 阶段划分和里程碑

**阶段 1：修复悬挂引用（P0）——第 1-2 周**

目标：使评论列表和消息附件对象能够优雅地处理软删除的目标。

里程碑：
- M1.1：`assertTargetReadable` 拆分为 `assertTargetExists`（创建时使用，保留）和 `assertTargetVisible`（列出时使用，跳过软删除检查）。
- M1.2：`commentService.List` 和 `commentService.Replies` 改用 `assertTargetVisible`，如果目标软删除则返回空评论列表（不是 404）。
- M1.3：将 `file_unavailable: true` 添加到消息响应中，如果它们的 `file_id` 指向一个软删除的文件。
- M1.4：更新所有受影响的测试。
- 门禁：`make test && make lint`。

**阶段 2：读取审计（P1）——第 3-4 周**

目标：所有读取操作都通过可配置的异步审计总线进行审计。

里程碑：
- M2.1：审计常量：`AuditFileRead`、`AuditMessageRead`、`AuditConversationRead`、`AuditCommentList`、`AuditNotifList`、`AuditSearchQuery`。
- M2.2：`AuditBus` 实现：带工作池的 `chan []AuditEvent`，可配置批处理大小和间隔。
- M2.3：`AuditBus` 通过函数选项（`WithFileReadAudit(auditBus)` 等）插入服务。
- M2.4：所有列表端点添加无操作的 `auditBus.Log` 调用。
- M2.5：更新 `SignedDownloader.VerifyPresignedURL` 以在签名中包含 `actor_uid`，启用经过身份验证的匿名下载审计。
- 门禁：`make test && make lint`。

**阶段 3：全局去重 + 修复缺口（P1）——第 5-6 周**

目标：跨所有者内容去重，以及 `CompleteUpload` 中缺失的 `refs.Increment`。

里程碑：
- M3.1：`CompleteUpload` 中添加 `s.refs.Increment(ctx, f.StorageKey)`。
- M3.2：将所有 `FindByChecksum` 调用从 `WHERE owner_uid = ? AND checksum = ?` 更改为 `WHERE checksum = ?`。
- M3.3：迁移：为 `storage_refs` 播种现有 blob 的准确引用计数。
- M3.4：添加后台任务以验证引用计数与物理 blob 计数的一致性。
- M3.5：更新 API 文档以反映新的去重语义。
- 门禁：`make test && make lint && make test-integration`。

**阶段 4：原子折叠 + 搜索排序（P2）——第 7-8 周**

目标：无竞态的通知折叠和改进的搜索排序。

里程碑：
- M4.1：`NotificationDispatcher.Handle` 重写为使用原子 CTE 折叠（自包含 SQL，无竞态）。
- M4.2：添加后台清理器以删除孤立的折叠行。
- M4.3：搜索协调器：添加跨实体结果合并（按 `search_rank DESC` 排序）。
- 门禁：`make test && make lint && make test-integration`。

**阶段 5：深层分页（技术债务）——第 9-10 周**

目标：优化深层游标分页（如果 N+1 模式确实是一个有意义的问题，这取决于数据集规模）。

里程碑：
- M5.1：分析现实世界数据集上的 EXPLAIN ANALYZE 查询，以确认深层游标分页的性能退化。
- M5.2：如果确认，则添加覆盖索引（`(owner_uid, created_at DESC, id DESC) WHERE deleted_at IS NULL`）以消除回表查询。
- 门禁：`make test && make lint`。

**阶段 6：清理（架构债务）——第 11-12 周**

目标：删除双重服务层，更新设计文档。

里程碑：
- M6.1：检查旧包（`internal/file/`、`internal/message/`、`internal/comment/`）的导入。如果未导入，则删除目录和文件。
- M6.2：如果旧测试引用了旧包，则将它们重定向到新服务。
- M6.3：更新 `docs/superpowers/specs/2026-05-22-aero-id-design.md` 以反映当前实现（outbox、SSE、全文搜索、读取审计）。
- 门禁：`make test && make lint && make build`。

### 5.3 风险点和缓解策略

| 风险 | 可能性 | 影响 | 缓解措施 |
|---|---|---|---|
| P0 的"级联不可用"具有更广泛的根本原因 | 中 | 高 | 在实施之前，对所有端点的 `assertTargetReadable` 进行彻底的调用图分析。还有哪些其他端点具有类似的级联故障？ |
| 匿名下载的读取审计可能需要签名者更改（客户端影响） | 高 | 中 | 如果嵌入 `actor_uid` 更改了签名方案，则使匿名读取审计成为"尽最大努力"（没有 actor 上下文的日志记录为 `"actor_uid": "unknown"`）。评审团队是否接受无 uid 审计。 |
| 全局去重引入了新的删除语义（不再保证"删除 = 不可访问"） | 中 | 中 | 清晰地向用户传达新的删除语义。"删除"解除了引用；当引用计数达到 0 时，blob 被移除。与产品沟通此项变更。 |
| 通知折叠迁移可能耗时较长（大量现有行） | 低 | 中 | 分阶段迁移：首先启用折叠逻辑（新行使用原子折叠），然后清理孤执行。对于新系统来说，现有行不是问题。 |
| 使用较大数据集时搜索测试性能下降 | 中 | 低 | 使用最小数据集进行测试。对性能进行基准测试，以单独进行优化。 |

### 5.4 按版本划分的总体路线图

```
版本 1 (第 1-2 周)：P0 - 修复悬挂引用
版本 2 (第 3-4 周)：P1 - 读取审计 + CompleteUpload 缺口
版本 3 (第 5-6 周)：P1 - 全局去重
版本 4 (第 7-8 周)：P2 - 原子折叠 + 搜索排序
版本 5 (第 9-10 周)：P2 - 深层分页优化
版本 6 (第 11-12 周)：债务 - 双重服务层清理 + 文档更新
```

每个版本都通过 `make test && make lint && make build` 进行验证。第 3 版及以后版本也需要 `make test-integration`。
