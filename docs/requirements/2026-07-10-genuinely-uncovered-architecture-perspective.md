# aero-id — 资深架构师视角：五组代码级证据驱动的扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全局代码扫描（`internal/` 全部 ~40 个包、22 份迁移、`cmd/`、`configs/`、`api/proto/`）后，与 `docs/requirements/` 下现有 20+ 份文档（累计覆盖 ~60 个方向）逐主题交叉比对，定位在**所有前述分析中零系统覆盖**的 5 个方向  
> **核心原则**: 每个方向必须有明确的**代码级证据**（具体文件名 + 行号 + 关键逻辑），而非产品层面的 wishlist  
> **无代码变更**

---

## 前置：当前代码库状态摘要

aero-id 已实现的功能远超典型 MVP。下面是**扫描摘要**（仅列与本文缺口分析相关的关键模块）：

| 领域 | 已实现 | 缺少（本文聚焦） |
|------|--------|-----------------|
| **文件存储** | 双后端（local/S3）、流式上传、预签名直传、内容去重、MIME 校验、GC 扫描 | 后端间迁移工具、多区域存储路由 |
| **消息** | 全 CRUD、Reactions、编辑、软删除、已读回执、对话查询、全文搜索 | 删除级联（Reactions/附件到删除消息的引用） |
| **评论** | 两层嵌套、Like/Unlike、举报、审核/隐藏、编辑 | 目标实体删除后评论的可见性策略 |
| **通知** | Outbox 驱动、偏好设置、静默时段、合并去重、SSE 推送 | 平台级推送（APNs/FCM） |
| **搜索** | PostgreSQL FTS（tsvector + ILIKE 回退）、三实体联合搜索 | 跨类型相关性排序、搜索结果去重、高亮 |
| **可靠性** | Outbox 事务性事件、幂等性 Key、限流、SSO 断路器 | Webhook 外部投递的至多一次语义 |
| **运维** | RED 指标、OTel 追踪、优雅关闭、自动迁移、健康检查 | 配置漂移检测、运行态配置校验 |
| **治理** | 单文件大小限制、MIME 类型白名单 | 无用户级存量配额、无存储后端正交迁移 |

代码库健康度：`make test && make lint` 绿灯，无 TODO/FIXME 残留，22 个迁移全部可逆，双传输层经过 parity 测试验证。

以下 5 个方向在现有 `docs/requirements/` 中**未被任何文档系统性地分析过**（证实方法见文末附录）。

---

## 方向一：存储后端迁移与多区域路由（Storage Migration & Multi-Region Routing）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/app/app.go:270-290` | `buildStorage()` 根据 `cfg.Storage.Type` 在启动时选择后端，**仅执行一次**。无运行时切换能力 |
| `configs/config.yaml` | `storage.type: s3` — 单后端配置，无"副本后端"或"迁移目标"概念 |
| `internal/storage/storage.go:20-55` | `FileStorage` 接口无 `List` 方法（`Lister` 是可选接口），无法枚举一个后端的所有对象 |
| `internal/storage/local.go:1-150` | Local 实现的 `Put` 写入 `<root>/<owner_uid>/<yyyy>/<mm>/<uuid>` 路径。如果切换到 S3，已存储的文件在新后端上不可访问 |
| `internal/storage/s3.go:1-200` | S3 实现使用 `<owner_uid>/<yyyy>/<mm>/<uuid>` 的对象键。与 Local 路径格式不同 |
| `internal/file/repository.go` | `files` 表记录 `storage_key` + `storage_type` — 迁移时需要批量更新这两个字段 |
| `internal/storage/gc/sweeper.go` | GC 只操作**当前活动后端**。如果文件存储在旧后端，GC 不会清理那里的孤儿对象 |
| `migrations/` 全部 22 个 | 无存储后端映射表或双写支持 |

### 问题

aero-id **锁定** 了存储后端。配置 `storage.type = local` → 所有文件写入本地磁盘。切换到 `s3` → 新文件写入 S3，但旧文件还在本地。没有机制平滑迁移。

这种情况在生产中很常见：
- 开发阶段用 local（零成本），上线后切到 S3（需要迁移）
- S3 切换区域（比如从 `us-east-1` 搬到 `ap-southeast-1` 降低延迟）
- 本地磁盘空间不足需要扩容
- 从自建 MinIO 迁移到 AWS S3

**当前架构漏斗：** `FileStorage` 接口只有 `Put/Get/Delete/PresignGet/Stat/NewKey/PresignPut`，没有 `Copy`（跨后端复制）、没有 `ListAll`（批量枚举）、没有 `Move`。这意味着即使手写迁移脚本也只能逐一 Get → Put → Delete，在大规模场景下不可行。

### 建议方案

#### 1. `FileStorage` 接口增加迁移原语

```go
type FileStorage interface {
    // 现有方法...
    Put(ctx, ownerUID, name, reader) → (key, size, err)
    Get(ctx, key) → (io.ReadCloser, error)
    Delete(ctx, key) → error
    PresignGet(ctx, key, ttl) → (string, error)
    Stat(ctx, key) → (*ObjectInfo, error)

    // 新增迁移原语
    Copy(ctx, srcKey, dstKey string) error        // 后端内部复制
    List(ctx, prefix string) → ([]ObjectInfo, error) // 批量枚举（可选接口强化）
}
```

#### 2. 迁移协调器

```go
// StorageMigrator 跨后端迁移单个文件
type StorageMigrator interface {
    MigrateOne(ctx, file *domain.File, src, dst FileStorage) error
}

// 执行流程：
// 1. src.Get(key) → reader
// 2. dst.Put(ownerUID, name, reader) → newKey
// 3. 事务更新 files.storage_key + files.storage_type
// 4. src.Delete(key)
// 5. 记录迁移到 audit_events
```

#### 3. 双读策略（迁移期间的零停机）

迁移过程中，新文件写入新后端，旧文件逐步搬运。需要一个**回退读逻辑**：

```go
// readWithFallback 先读新后端，NotFound 则回退到旧后端
func (s *fileService) readWithFallback(ctx, id) {
    f, _ := s.files.Get(ctx, id)
    switch f.StorageType {
    case "s3":
        reader, err := s.s3.Get(ctx, f.StorageKey)
        if err == nil { return reader }
        // s3 miss → 尝试 local（可能是迁移中漏掉的）
        localKey := migrateKey(f) // 转换 storage_key 格式
        return s.local.Get(ctx, localKey)
    case "local":
        // 同理
    }
}
```

#### 4. 迁移命令

```bash
# 新 CLI 命令
aero-id migrate storage --from local --to s3 --batch 100 --dry-run
aero-id migrate storage --status  # 查看迁移进度
```

#### 5. 数据模型扩展

```sql
ALTER TABLE files ADD COLUMN migration_status VARCHAR(16) NOT NULL DEFAULT ''; 
-- '' = 已完成, 'pending' = 待迁移, 'in_progress' = 迁移中
```

### 性能 / 边界影响

| 场景 | 影响 |
|------|------|
| **迁移期间新文件写入** | 直接写入目标后端，不受影响 |
| **迁移期间读旧文件** | 回退读（双后端查询），延迟增加 1 次额外 Get 调用 |
| **大文件迁移** | 通过 `PresignGet` + `PresignPut` 直接在后端间复制，不经过 app 进程 |
| **迁移中断恢复** | `migration_status` 标记使迁移可中断恢复 |
| **GC 在迁移期间的孤儿检测** | GC 需要扫描两个后端，或迁移完成后统一清理源后端 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 架构杠杆 | 🔥🔥🔥🔥🔥 — 解除存储后端锁定，为多云/多区域部署铺路 |
| 用户感知价值 | 🔥 — 用户不关心存储后端，但关心数据不会丢 |
| 运维价值 | 🔥🔥🔥🔥🔥 — 无迁移工具=生产级运维负债 |
| 实现成本 | 中-高（接口扩展 + CLI + 双读逻辑 + 原子性保证） |
| **建议** | **P1** — 在上线前完成。如果在 local 上跑通上线，未来切 S3 将是一场灾难 |

---

## 方向二：Outbox Webhook 投递的端到端可靠性（End-to-End Webhook Delivery Guarantees）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/outbox/relay.go:65-90` | `processOne()`: fetch → handler.Handle → MarkPublished。**如果 Handle 成功（handler 返回 nil）但 MarkPublished 失败**（DB 临时不可用），事件不会在当前批次被标记为已发布，下次轮询会再次投递 |
| `internal/outbox/webhook.go:40-80` | `WebhookHandler.Handle()` 发送 HTTP POST，2xx 返回 nil（成功）。**外部端点接收成功 ≠ DB MarkPublished 成功** |
| `internal/outbox/relay.go:45-55` | Relay 使用 `FetchUnpublished` 轮询——该查询不检查事件是否曾被投递过。如果 handler 幂等，重复是安全的；webhook handler **不幂等**——外部端点的 POST 可能产生副作用 |
| `internal/outbox/chain.go:1-20` | `Chain.Handle()` — 按序执行。NotificationDispatcher 成功后 WebhookHandler 再执行。如果 NotificationDispatcher 成功但 WebhookHandler 超时，链返回 error → 事件**不会** MarkPublished → 重试时 NotificationDispatcher 再次执行 → 产生重复通知行 |
| `internal/outbox/outbox.go:40-55` | `Event` 结构无 `delivery_id` / `dedup_key`，无法让下游 handler 识别重复投递 |
| `internal/notification/dispatcher.go:70-80` | `NotificationDispatcher.Handle()` 调用 `notifs.Create()`。如果 dispatcher 成功写入通知行但 webhook 失败导致链返回 error，下次重试会创建**另一条重复的通知行** |
| 无 | Outbox 事件无 **dedup 机制**：`published_at` 只是时间戳，不是投递确认 ID。handler 无法判断"这是一个新投递"还是"这是已投递的事件的重试" |

### 问题

这是一个经典的**双重写入（dual-write）**问题：outbox 模式保证了本地 DB 写入（通知行）的 at-least-once 语义，但当扩展到**外部 webhook** 时，at-least-once 变成了 at-least-once + 至少一次 = **可能多次**。

具体场景：
1. `Outbox Relay` 提取事件 E
2. `Chain.Handle(E)`: `NotificationDispatcher` 成功 → 写入通知行 → `WebhookHandler` 成功 → POST 2xx
3. `MarkPublished(E)` 因 DB 连接池耗尽而失败
4. 下次轮询：E 再次被提取
5. `NotificationDispatcher` 创建**第二条通知行**
6. `WebhookHandler` 向外部端点发送**第二个重复请求**

对于通知行，重复意味着用户看到两条完全相同的通知。对于 webhook，重复意味着外部系统收到两次相同的事件。在非幂等的 webhook 消费者（如发送邮件、扣减积分）中，这是一个数据一致性漏洞。

### 建议方案

#### 1. 为 Outbox Handler 引入幂等投递 ID

```go
// Event 增加投递追踪字段
type Event struct {
    // 现有字段...
    LatestDeliveryID uuid.UUID  // 每次投递生成唯一 ID
}
```

#### 2. 两阶段确认（Prepare → Confirm）

将 `Chain.Handle` 的执行拆为两阶段，确保 MarkPublished 成功后才视为"已投递"：

```go
// 方案：先标记为"正在投递" (delivery_in_progress = true)
// 投递链执行
// 链返回后：标记为 published_at = now, delivery_in_progress = false
// 
// 如果 crash：下次 fetch 过滤 delivery_in_progress = true 且 
// delivery_started_at < (now - 2*timeout) 的事件，重新投递
```

#### 3. NotificationDispatcher 去重

当 `NotificationDispatcher` 收到一个它已经处理过的事件（通过检查 `event.ID` 是否已有对应的通知行），它应该返回 nil（成功）而不是创建重复行。这需要通知行存储 `outbox_event_id`：

```sql
ALTER TABLE notifications ADD COLUMN outbox_event_id UUID;
CREATE UNIQUE INDEX idx_notif_outbox_event ON notifications(outbox_event_id) 
    WHERE outbox_event_id IS NOT NULL;
```

#### 4. WebhookHandler 去重

外部系统应该通过 `X-Event-Delivery-ID` header 获得唯一投递 ID，并在自身侧去重。这是**服务端无法单方面解决的**——但 aero-id 可以输出正确格式的 header。

```go
// WebhookHandler 发送时携带投递确认 ID
req.Header.Set("X-Event-Delivery-ID", e.LatestDeliveryID.String())
req.Header.Set("X-Event-ID", e.ID.String())
req.Header.Set("X-Idempotency-Key", fmt.Sprintf("%s/%d", e.ID.String(), e.RetryCount))
```

### 边界情况

| 场景 | 处理 |
|------|------|
| **Webhook 2xx 后 MarkPublished 失败** | 增加重试限制（`max_confirm_retries = 3`）+ 最终死信通道 |
| **Notifications 重复行** | 通过 `outbox_event_id` 唯一约束去重 |
| **链中部分 handler 成功、部分失败** | 引入 Prepare → Commit 模式：所有 handler 的副作用在链全部成功后才 commit（需要 handler 支持"回滚"或"幂等"） |
| **外部 webhook 永远 5xx** | 现有 MaxRetries 机制 → DeadLetter。但 DeadLetter 事件不可从 API 层面复活（见方向三） |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 数据一致性影响 | 🔥🔥🔥🔥🔥 — Webhook 下游的非幂等消费者（邮件、通知、扣费）会产生重复操作 |
| 架构杠杆 | 🔥🔥🔥🔥 — 修复 outbox 的最后一个已知一致性缺陷 |
| 触发概率 | 🔥🔥🔥 — 需要 DB 瞬时不可用 + webhook enabled 才能触发 |
| 实现成本 | 中（投递 ID + 通知行去重 + 两阶段确认） |
| **建议** | **P1** — 如果 webhook 被启用，这是必须修复的 bug。如果 webhook 未被启用（当前默认关闭的），可以推迟 |

---

## 方向三：软删除语义的完备性与级联策略（Soft-Delete Semantics Completeness）

### 代码级证据

#### 3.1 消息软删除后 Reactions 成为孤儿

| 位置 | 证据 |
|------|------|
| `internal/message/repository.go:123-130` | `SoftDelete()` 通过 GORM `.Delete()` 设置 `deleted_at`。**不级联删除 `message_reactions` 行** |
| `migrations/000013_message_reactions.up.sql` | `message_reactions` 表有 `message_id` FK → `messages(id)`，但 FK 约束无 `ON DELETE CASCADE` |
| `internal/domain/message.go` | `Message` 结构无 `Reactions` 字段——无加载 reactions 的机制 |
| `internal/service/message.go:260-280` | `ReactToMessage()` 和 `UnreactMessage()` 直接写入 `message_reactions`，不检查消息的 `deleted_at` |

**后果：** 消息 A 有 3 个 Reactions。用户软删除 A。Reactions 行仍然存在于 `message_reactions` 表中 —— 成为孤儿数据。消息通过 GORM 的默认 scope 不可见（`WHERE deleted_at IS NULL`），但 Reactions 行可以自由增长，占用空间。

#### 3.2 文件软删除后关联实体被悬空

| 位置 | 证据 |
|------|------|
| `internal/file/repository.go:55-70` | `Delete()` 执行 `db.Where("id = ?", id).Delete(&fileRow{})` — 软删文件行 |
| `internal/comment/repository.go` | `CommentRepository` 的 CRUD 不检查 `target_id` 对应的目标实体是否已被软删除 |
| `internal/service/comment.go:45-60` | `assertTargetReadable()` 检查文件是否存在——通过 `fileRepo.Get()`，该查询带 GORM 默认 scope，已软删除的返回 `ErrNotFound`。评论列表显示已删除文件的评论？还是自动隐藏？当前：`assertTargetReadable` 返回 NotFound → 评论 API 正常拒绝访问 |
| `migrations/000006_create_file_shares.up.sql` | `file_shares` 表无 ON DELETE CASCADE |

**后果：** 场景：用户 A 上传了一个文件 F，用户 B 对 F 发表了评论 C。A 删除 F。此时 C 的 `(target_type='file', target_id=F)` 指向一个已删除的资源。B 尝试列出评论时得到 NotFound 错误。

#### 3.3 评论软删除后子评论成为孤儿

| 位置 | 证据 |
|------|------|
| `internal/comment/repository.go:55-70` | `SoftDelete()` 只设置 `comments.deleted_at`，不更新子评论的 `parent_id` |
| `internal/domain/comment.go:35-50` | `ValidateNewComment()` 只检查深度不超过 2 层，不检查父评论是否已删除 |

**后果：** 根评论 R 被软删除。子评论 C1、C2 的 `parent_id = R` 指向一个已删除的资源。当其他用户浏览评论时，系统会加载 R（被 scope 过滤掉），然后 R 的子评论孤立——但 `ListReplies(rootID=R)` 可以通过 root_id 找到它们。

#### 3.4 文件 GC 硬删除后共享记录的级联

| 位置 | 证据 |
|------|------|
| `internal/storage/gc/sweeper.go:90-110` | `purgeSoftDeleted()` 执行 `HardDelete()` 删除文件行——**不级联删除 `file_shares` 行** |
| `migrations/000006_create_file_shares.up.sql` | `file_shares` 的 `file_id` 列无 `ON DELETE CASCADE` |
| `internal/repository/file_share.go` | `Grant()` 和 `Revoke()` 不检查文件是否存在 |

**后果：** 文件被 GC 硬删除后，`file_shares` 表仍保留已删除文件的共享记录。当用户查询"分享给我的文件"（`ListFilesSharedWith`）时，这些记录可能返回 404 或者需要额外的过滤。

### 建议方案

#### 原则

软删除不应该是事务性的（不希望因为更新 10000 条 Reactions 而阻塞消息删除），但应当通过**惰性清理 + 查询时过滤**的组合保持语义完整性。

#### 方案 A：查询时过滤（推荐，低风险）

在查询时自动过滤掉引用已删除实体的行：

```go
// message_reactions 加载时过滤已删除消息的 reactions
func (r *messageRepo) ListReactions(ctx, messageID) {
    // 隐式 join 检查 messages.deleted_at
    db.Joins("INNER JOIN messages ON messages.id = message_reactions.message_id AND messages.deleted_at IS NULL").
       Where("message_reactions.message_id = ?", messageID)
}
```

```sql
-- file_shares 列表过滤已硬删除的文件
SELECT fs.* FROM file_shares fs
INNER JOIN files f ON f.id = fs.file_id
WHERE fs.grantee_uid = ? AND f.deleted_at IS NULL;
```

#### 方案 B：惰性清理 Job（推荐，低风险）

定期运行的清理 Job，类似于 storage GC：

```
Cleaner Job:
  1. 扫描 message_reactions → 找到 messages.deleted_at IS NOT NULL 的行 → 删除
  2. 扫描 file_shares → 找到 files.deleted_at IS NOT NULL 的行 → 删除
  3. 扫描 comments → 找到 target 已软删除的 → 标记 comments.status = CommentStatusHidden
  4. 扫描 comments → 找到 parent_id 指向已软删除评论的子评论 → 置 parent_id = NULL（升级为顶层）
```

#### 方案 C：有选择的级联删除（高成本，不推荐）

在软删除时同步更新相关行：

```go
func SoftDeleteMessage(ctx, id) {
    db.Transaction(func(tx) {
        tx.Model(&messageRow{}).Where("id = ?", id).Update("deleted_at", now)
        tx.Model(&messageReactionRow{}).Where("message_id = ?", id).Delete() // 硬删 reactions
    })
}
```

**不推荐原因：** 如果消息有 10 万 Reactions，软删除操作会长时间占用连接。而且用户删除消息时不应该等待 Reactions 清理。

#### 具体建议组合

| 悬空场景 | 方案 | 优先级 |
|-----------|------|--------|
| `message_reactions` 对已删除消息 | **查询时过滤** + 惰性清理 Job | P2 |
| `file_shares` 对已硬删除的文件 | **查询时过滤**（INNER JOIN） | P1（防止 ListSharedWithMe 返回死链接） |
| `comments` 对已软删除的目标 | **查询时状态检查**：target 不存在时 status = Hidden | P1 |
| `comments` 的子评论对已删除的父评论 | **惰性清理**：`parent_id` 指向已删除评论的，置 NULL | P3 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 数据完整性影响 | 🔥🔥🔥🔥 — 孤儿数据不影响当前功能，但随时间积累导致不一致 |
| 用户感知影响 | 🔥🔥🔥 — 用户在"分享给我的文件"中看到失效文件；在评论中看到孤立的子评论 |
| 实现成本 | 低-中（查询时加 Join/过滤 + 1 个定期 Cleaner Job） |
| **建议** | **P1** — 在数据积累到影响查询性能前完成。当前风险可控但不可持续 |

---

## 方向四：跨实体搜索相关性排序与合并（Cross-Entity Search Relevance & Result Merging）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/search/search.go:50-80` | `searchKind()` 对每种 `SearchKind` 独立查询，结果直接 `append` 到 `hits` 切片。**无跨类型排序逻辑** |
| `internal/search/search.go:25-35` | `Search()` 的 `kinds` 参数——当客户端传入多个 kind 时，结果按 `messages → comments → files` 的固定顺序排列，**不是按相关性** |
| `internal/message/repository.go:180-195` | `SearchMessages()` 使用 `ts_rank(search_vector, plainto_tsquery(...)) DESC` 排序——在类型内部有排序。但跨类型时消息永远排在前面 |
| `internal/comment/repository.go` | `SearchComments()` 逻辑类似——返回 `ORDER BY ts_rank DESC`。但无全局排名 |
| `internal/file/repository.go` | `SearchFiles()` 搜索文件名——使用 `ILIKE`，无 FTS 排名 |
| `internal/domain/search.go:1-20` | `SearchHit` 是 union 类型——每次只返回一个非 nil 字段。无法表达"一个消息和一个评论都匹配同一查询"的排名关系 |
| `migrations/000016_fts_indexes.up.sql` | 三张表各自独立的 `search_vector` 列 + GIN 索引。**无跨实体搜索视图** |

### 问题

当前搜索的工作方式：
1. 用户输入 `"project proposal"`
2. 并行查询 `messages.search_vector @@ plainto_tsquery('simple', 'project proposal')`
3. 并行查询 `comments.search_vector @@ plainto_tsquery('simple', 'project proposal')`  
4. 并行查询 `files.name ILIKE '%project%' OR files.name ILIKE '%proposal%'`
5. 结果按 `[messages结果...] + [comments结果...] + [files结果...]` 顺序拼接

**问题 1：不相关的结果排在前面。** 一个匹配度 0.9 的评论排在匹配度 0.1 的消息后面——只因为消息类型在评论之前处理。

**问题 2：无法搜索"文件名 + 内容"。** 文件的 `search_vector` 只从 `name` 列生成（`to_tsvector('simple', coalesce(name, ''))`）。用户无法通过文件内容搜索文件——即使文件是文本文件（如 `.md`、`.txt`）。这限制了文件搜索的价值。

**问题 3：没有结果去重。** 如果同一实体出现在多个 kind 查询中（理论上不会，因为每种 kind 对应不同表），或者一个消息同时匹配 FTS 和 ILIKE（当前消息只用 FTS），结果会被重复计算。当前虽然没有重复，但缺乏防御性去重。

**问题 4：无法高亮匹配内容。** `ts_headline()` 函数可将匹配词在结果中用标记包裹，但当前仓库层的查询不包含 `ts_headline` 调用。用户只能看到原始内容，不知道自己为什么匹配。

**问题 5：ILIKE 和 FTS 结果混排。** 文件搜索使用 ILIKE（`name ILIKE '%query%'`），没有 `ts_rank`——无法与 FTS 结果统一排序。文件总是排在最后。

### 建议方案

#### 1. 统一相关性分数（Normalized Relevance Score）

将每种查询的分数归一化到 [0, 1] 区间，然后用全局分数排序：

```go
type ScoredHit struct {
    Hit     SearchHit
    Score   float64  // [0, 1], 1=perfect match
}

// 跨类型排序
merged := merge(scoredHits...)  // 按 Score DESC 合并
```

**归一化策略：**
- FTS `ts_rank`：通过 `ts_rank(..., 32)`（归一化权重 32=文档长度归一化）输出 [0, 1]
- ILIKE：精确匹配 = 1.0，前缀匹配 = 0.8，子串匹配 = 0.6

#### 2. 搜索结果高亮

```go
// 仓库层增加高亮
rows := db.Raw(`
    SELECT id, from_uid, to_uid, kind, content, file_id, read_at, edited_at, meta, created_at,
           ts_headline('simple', content, plainto_tsquery('simple', ?),
                       'StartSel=<mark>, StopSel=</mark>, MaxWords=50, MinWords=20') AS headline
    FROM messages
    WHERE deleted_at IS NULL AND (from_uid = ? OR to_uid = ?)
      AND search_vector @@ plainto_tsquery('simple', ?)
    ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
    LIMIT ?`, query, uid, uid, query, query, limit)
```

`headline` 作为 `SearchHit` 的附加字段返回（当前 `SearchHit` 无此字段——需扩展）：

```go
type SearchHit struct {
    Kind     SearchKind
    Message  *Message
    Comment  *Comment
    File     *File
    Headline string   // 新增：带高标的匹配片段
}
```

#### 3. 文件内容搜索（对文本文件）

当文件的 MIME 类型是文本类（`text/*`, `application/json`, `application/xml`, `application/pdf` 等）时，提取文本内容到搜索向量：

```sql
-- 方法一：files 表增加 content_text 列（需要文件处理管线提取文本）
ALTER TABLE files ADD COLUMN content_text TEXT;
-- 然后：search_vector = to_tsvector('simple', coalesce(name, '') || ' ' || coalesce(content_text, ''))
```

这依赖方向二（文件处理管线）。在文件处理管线完成前，文件搜索维持当前行为。

#### 4. `SearchHit` 扩展

```go
type SearchHit struct {
    Kind      SearchKind
    Message   *Message
    Comment   *Comment
    File      *File
    Score     float64   // 新增：归一化相关性分数 [0,1]
    Headline  string    // 新增：高亮摘要片段
    MatchedBy string    // 新增：'fts' | 'like' | 'both'
}
```

#### 5. 分页适配

当前分页在每种 kind 内部独立进行，然后合并。这意味着第 1 页只能看到 messages 的前 N 条。改为统一分页：

```
搜索 "project" (limit=20):
  各库取 2*limit = 40 条
  统一按 Score DESC 排序
  取前 limit 条返回
  next_cursor = 第 limit 条的得分
```

### 边界情况

| 场景 | 处理 |
|------|------|
| **长查询词** | `plainto_tsquery` 对中文/日文等非空格分词语言无效——已有 `SearchLike` 回退。但回退结果无相关性排序 |
| **空结果集的高亮** | `ts_headline` 在无匹配时返回原文前 MaxWords 字符——可接受 |
| **安全隔离** | 搜索强制 `WHERE (from_uid=? OR to_uid=?)` 等 ACL，不需要额外改动 |
| **高亮泄露文件全名** | 文件名高亮可能暴露用户不希望暴露的信息。当前文件存储使用原始文件名——隐私风险需产品层面评估 |
| **性能** | `ts_headline` 是 CPU 密集型。建议在搜索量小时启用，或者在搜索结果条数 <= 20 时启用。生产者—消费者模式下可以通过 Outbox 预计算 headline |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户感知改进 | 🔥🔥🔥🔥🔥 — 搜索结果的相关性是搜索产品最核心的体验 |
| 架构杠杆 | 🔥🔥🔥 — 不需要新表或新索引，只需修改查询和结果合并逻辑 |
| 后端影响 | 中（仓库层查询改造 + handler 层字段扩展 + proto 兼容性） |
| gRPC 协议兼容性 | gRPC proto 需要增加 `Score`、`Headline`、`MatchedBy` 字段——向后兼容（新字段可选） |
| **建议** | **P2** — 当前搜索可用，只是体验差。在基本功能稳定后优化 |

---

## 方向五：运行时配置验证与漂移检测（Runtime Config Validation & Drift Detection）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/config/config.go:50-80` | `validate()` 方法只检查**启动时必需**的字段（DSN、SigningKey 等）。对配置值的**语义合理性**不做检查（如 `rate_limit.rps: -1`、`storage.gc.retention: 0`） |
| `internal/config/config.go:1-150` | viper 加载优先级：默认值 → yaml → env。**没有机制验证"当前运行值与预期值一致"** |
| `internal/app/app.go:100-200` | `New()` 读取 `cfg` 并构造所有组件。如果 `cfg` 在构造过程中被修改（如另一个 goroutine 修改了全局 viper 实例），不会触发告警 |
| `configs/config.yaml` | 配置文件仅启动时读取。运行中无法热更新 |
| `internal/storage/gc/sweeper.go:30-45` | `Config` 的 `Retention` 字段——零值会导致每 tick 清除所有刚删除的行。`validate()` 不检查 `Retention > 0` |
| `internal/middleware/ratelimit.go:20-35` | 如果 `cfg.RateLimit.Enabled = true` 但 `rps` 或 `burst` 为零，`ratelimit.New(0, 0, idleTTL)` 创建一个**拒绝所有请求**的限流器——这应该是配置错误 |
| `internal/pkg/ratelimit/ratelimit.go:30-45` | `New()` 只做简单赋值，对 `rate` 或 `burst` ≤ 0 的配置发出警告？没有——无声地创建了一个有问题的限流器 |
| `internal/sso/client.go:80-100` | `TokenCacheTTL` 的零值导致缓存永不过期。`StaleTTL` 零值导致 serve-stale 失效。`validate()` 不检查这些值 |
| 无 | 无 `GET /debug/config` 端点暴露当前**实际运行配置**（已应用 env overlay 后的最终值）。运维人员无法确认当前生效的配置 |
| 无 | 无配置健康度量（`/metrics` 无 `aeroid_config_hash`、`aeroid_config_last_reload` 指标） |

### 问题

#### 5.1 无声的配置错误

```
情景：运维人员在部署时误写：
rate_limit:
  enabled: true
  rps: 0    # 应该是 20
  burst: 0  # 应该是 40

结果：服务正常启动，/metrics 显示一切正常，但所有限流后的请求收到 429。
运维花 2 小时排查为什么用户无法正常使用。
```

**根因：** `cfg.validate()` 不检查语义合理性（正数、有效范围、互斥条件），也不在启动时打印完整的生效配置。

#### 5.2 配置漂移不可检测

```yaml
# configs/config.yaml (git committed)
storage:
  gc:
    enabled: true
    retention: 720h  # 30天
```

```yaml
# 服务器上实际运行的 config.yaml（被手动修改过）
storage:
  gc:
    enabled: true
    retention: 1h  # 1小时——灾难性的配置漂移
```

**后果：** GC 每 tick 清除 1 小时前的软删除文件。运维没有被告知，直到用户报告"我的文件为什么不见了"。

**根因：** 没有运行态配置的 checksum 暴露（无法与 git 中的期望值比对），没有"配置就绪"度量，没有配置变更告警。

#### 5.3 没有运行中配置视图

当前运维排查问题流程：
1. SSH 到服务器
2. 检查 `configs/config.yaml`
3. 检查环境变量 `env | grep AERO_ID`
4. 心理合并两者

如果运行中的配置与文件/env 不一致（如热重载尚未实现但有人手动更新了文件），运维人员无法知道实际服务在用什么配置。

### 建议方案

#### 1. 启动时配置健康检查与日志

```go
func (cfg *Config) validate() error {
    // 现有检查...
    // 新增语义检查
    checks := []struct{
        name string
        ok   bool
        hint string
    }{
        {"rate_limit.rps > 0", cfg.RateLimit.RPS > 0, "zero RPS means deny all"},
        {"rate_limit.burst > 0", cfg.RateLimit.Burst > 0, "zero burst means deny all"},
        {"storage.max_upload_bytes > 0", cfg.Storage.MaxUploadBytes > 0, "zero means no limit (OOM risk)"},
        {"storage.gc.retention > 0", cfg.Storage.GC.Retention > 0, "zero retention purges all rows"},
        {"storage.gc.min_orphan_age > 0", cfg.Storage.GC.MinOrphanAge > 0, "zero risks reaping in-flight uploads"},
        {"sso.token_cache_ttl > 0", cfg.SSO.TokenCacheTTL > 0, "zero disables caching (excessive SSO load)"},
        {"sso.timeout > 0", cfg.SSO.Timeout > 0, "zero means no timeout (goroutine leak)"},
        {"server.http.read_timeout > 0", cfg.Server.HTTP.ReadTimeout > 0, "zero means no timeout"},
        {"server.http.write_timeout > 0", cfg.Server.HTTP.WriteTimeout > 0, "zero means no timeout"},
        // sso.breaker_threshold < 1 means circuit breaker disabled
        // This is a valid configuration, just log it
    }
    for _, c := range checks {
        if !c.ok {
            log.Warn("config validation: "+c.name, zap.String("hint", c.hint))
        }
    }
}
```

#### 2. 配置 Hash 暴露端点

```go
// 在启动时计算最终配置的 SHA256 hash
configHash := sha256.Sum256(cfgJSON)

// 在 metrics 中暴露
metricsReg.NewGauge("aeroid_config_hash", float64(binary.BigEndian.Uint64(configHash[:8])))
metricsReg.NewGauge("aeroid_config_version", cfg.Version)

// 在 debug 端点暴露完整配置（脱敏）
router.GET("/debug/config", func(c *gin.Context) {
    sanitized := sanitizeConfig(cfg) // 隐藏密码、密钥等敏感字段
    httpx.OK(c, sanitized)
})
```

#### 3. 配置漂移检测（外部工具配合）

```bash
# CI/CD 上线前置检查
# 1. 读取 git 中的 configs/config.yaml
# 2. 部署后 curl /debug/config
# 3. 计算两边的 hash
# 4. 如果不匹配，阻止上线

deploy_check.sh:
  EXPECTED_HASH=$(sha256sum configs/config.yaml | awk '{print $1}')
  ACTUAL_HASH=$(curl -s $HOST/debug/config | sha256sum | awk '{print $1}')
  if [ "$EXPECTED_HASH" != "$ACTUAL_HASH" ]; then
    echo "CONFIG DRIFT DETECTED"
    exit 1
  fi
```

#### 4. 轻度合理性断言（防误配置）

```go
// 关键配置项在 app.go 中做运行时断言
func assertConfig(cfg *config.Config) {
    if cfg.RateLimit.Enabled && cfg.RateLimit.RPS <= 0 {
        log.Panic("rate_limit.rps must be > 0 when rate_limit.enabled = true")
    }
    if cfg.Storage.GC.Enabled && cfg.Storage.GC.Retention <= 0 {
        log.Panic("storage.gc.retention must be > 0 when storage.gc.enabled = true")
    }
    if cfg.Storage.MaxUploadBytes > 0 && cfg.Storage.MaxUploadBytes < 1024 {
        log.Warn("storage.max_upload_bytes is set to a very low value (< 1KB)",
            zap.Int64("bytes", cfg.Storage.MaxUploadBytes))
    }
}
```

#### 5. Prometheus 告警规则建议（配套）

```yaml
# 当配置 hash 与预期值不一致时告警
- alert: ConfigDrift
  expr: aeroid_config_hash != ON(instance) group_left(expected_hash) vector($EXPECTED_HASH)
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: "Configuration drift detected on {{ $labels.instance }}"

# 当配置错误导致限流器误杀所有请求时告警
- alert: RateLimitKillingAllTraffic
  expr: rate(aeroid_rate_limit_denied_total[5m]) / (rate(aeroid_rate_limit_denied_total[5m]) + rate(aeroid_rate_limit_allowed_total[5m])) > 0.9
  for: 1m
  labels:
    severity: warning
  annotations:
    summary: "Rate limiter is denying >90% of requests — misconfiguration?"
```

### 边界情况

| 场景 | 处理 |
|------|------|
| **debug/config 暴露敏感字段** | `sanitizeConfig()` 用 `***` 替换 `password`、`secret`、`key` 结尾的字段 |
| **配置漂移告警噪波** | 正常部署时 hash 会变化。只在部署后 15 分钟窗口外触发告警 |
| **env overlay 使 hash 不稳定** | 计算 hash 时使用**最终合并后的配置**（含 env overlay），不是 yaml 文件本身 |
| **不暴露 config 给未授权用户** | `/debug/config` 需要 admin 权限（复用 `PermSystemConfig` 权限） |
| **多实例配置不同步** | 每个实例独立暴露 hash，Prometheus 多实例聚合后可以检测配置不一致 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 运维价值 | 🔥🔥🔥🔥🔥 — 无声的配置错误是 SRE 最大的噩梦 |
| 架构杠杆 | 🔥🔥🔥🔥 — 极少的代码变更（config validate 扩展 + 1 个路由 + 1 个 gauge） |
| 用户感知价值 | 🔥 — 用户不关心配置，但关心服务稳定 |
| 实现成本 | **低**（3-5 个函数，无新依赖） |
| **建议** | **P0** — 成本最低、收益最高。可以在一个下午内完成 |

---

## 路线图总结与交互关系

```
Phase 0 (P0 — 立即启动):
└── 方向五：运行时配置验证与漂移检测
    └── 成本最低（半天），收益最高（防止无声的配置灾难）
        独立于其他方向，无前置依赖

Phase 1 (P1 — 上线前完成):
├── 方向一：存储后端迁移工具
│   └── 如果上线后需要切换存储后端，当前无工具支持
├── 方向二：Outbox Webhook 投递可靠性
│   └── 前置条件：webhook 被启用。无 webhook 可推迟
└── 方向三：软删除语义完备性
    └── 无前置依赖，可独立启动。但建议在数据积累前完成

Phase 2 (P2 — 产品优化阶段):
└── 方向四：跨实体搜索相关性排序
    └── 无前置依赖。搜索当前可用，只是体验差
```

### 执行顺序逻辑

```
方向五 ════════════════════════════════════════╗
(运维安全，零依赖，独立交付)                    ║
                                                ║ 可并行
方向一 ═══ 方向二 ═══ 方向三 ═══════════════════╝
(存储迁移)  (outbox webhook)  (软删除语义)      ║
                                                ║ 独立
方向四 ═════════════════════════════════════════╝
(搜索体验)
```

---

## 附录：与现有分析文档的交叉比对

本文档提出的 5 个方向与 `docs/requirements/` 下全部 20+ 份文档的关系：

| 方向 | 在已有分析中的覆盖情况 |
|------|---------------------|
| **方向一：存储后端迁移** | 所有现有文档中**零系统分析**。`expansion-directions.md` 在"存储配额"一句话中提到了存储，但关注的是**配额**（用户能存多少），而非后端迁移。`architecture-extensions.md` 没有提到存储后端锁定问题。这是完全未被触及的架构缺口。 |
| **方向二：Outbox Webhook 可靠性** | 所有现有文档中**零分析**。`edge-cases-and-performance.md` 和 `production-scale-architectural-gaps.md` 讨论了 outbox 的性能和优雅关闭，但**从未分析 wbehook + MarkPublished 分离带来的双重写入问题**。 |
| **方向三：软删除语义完备性** | 所有现有文档中**零系统分析**。`code-level-implementation-gaps.md` 有`"Reactions 行在消息软删除后仍存在"`的单句描述（方向四），但未从全局软删除语义和级联策略角度分析。本文档从 `message_reactions`、`file_shares`、`comments` 和 `GC` 四个维度系统性分析了这个架构缺口。 |
| **方向四：跨实体搜索相关性排序** | 所有现有文档中**零系统分析**。搜索功能（`SearchService`）在多个文档中被作为"已实现功能"提到，但**从未分析其跨类型排序缺失、结果高亮缺失、文件内容搜索缺失**等问题。 |
| **方向五：运行时配置验证** | 所有现有文档中**零系统分析**。`production-scale-architectural-gaps.md` 提到了配置管理，但关注的是**分布式配置分发**（多实例配置同步），而非**单实例配置的语义验证和漂移检测**。两者正交。 |

**总结**：本文档的 5 个方向在所有 20+ 份现有分析文档中**最多有一句话提及**，没有一次被作为独立的架构缺口进行系统性分析。每个方向都有明确的代码级证据（具体函数/行号），覆盖了从存储运维到数据一致性、从搜索体验到运维安全的多个维度。
