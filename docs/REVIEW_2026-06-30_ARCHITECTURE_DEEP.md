# aero-id — 架构深层审查：双实现、代码孤儿、基础设施缺口

> 日期: 2026-06-30
> 前置说明: 之前已从产品缺口和架构审查角度输出两篇分析。本文聚焦于**代码库内部的架构不一致问题**和**基础设施层面的隐形缺口**。

---

## 0. 前置发现：代码库中存在两套并行实现

这是本次审查最重要的发现。代码库中存在**两套功能等价但包结构不同的实现**：

### 套件 A — 当前被 `app.go` 实际使用的（旧/平铺结构）

```
internal/handler/http/{file,message,comment,notification,block,search}.go
internal/handler/grpc/{file,message,comment,notification,block,search}.go
internal/service/{file,message,comment,notification,block,search}.go
internal/repository/{file,message,comment,notification,block,search}.go
```

包结构: 按层分包（handler/service/repository），每个包内含所有领域模块。

### 套件 B — 未被 `app.go` 引用的（新/领域分包）

```
internal/file/{file,handler_http,handler_grpc,service_crud,service_share,service_upload,repository}.go
internal/comment/{comment,handler_http,handler_grpc,service,repository}.go
internal/message/{message,handler_http,handler_grpc,service,repository}.go
internal/notification/{notification,handler_http,handler_grpc,dispatcher,service,repository}.go
internal/block/{block,handler_http,handler_grpc,repository}.go
internal/search/{search,handler_http,handler_grpc}.go
```

包结构: 按领域分包（file/comment/message 等），每个包内含自己的 handler + service + repository。

### 严重程度

| 问题 | 级别 |
|------|------|
| 套件 B 的所有代码**完全未被 `app.go` 引用**，是代码孤儿（dead code） | **Critical** |
| 套件 B 有自己的 `fileRow`、`messageRow`、`commentRow` 等 GORM 结构体定义，与套件 A 完全重复 | **High** |
| 两套代码各自有单元测试，但只有套件 A 被集成测试覆盖 | **Medium** |
| 任何新功能需要在两套中都实现，否则不一致 | **Blocking** |

**判定：** 看起来套件 B 是领域驱动重构（domain-oriented package）的初步尝试，但重构在完成前就停止了。套件 B 的代码是可编译的（Go 允许包级孤儿代码存在，只要不被 import），但**运行时零执行路径经过它**。

**行动建议：** 这是**最高优先级的技术债**。必须决定：
- **方案 A:** 删除套件 B 的所有代码，保留平铺结构（减少维护负担）
- **方案 B:** 完成重构——将 `app.go` 从套件 A 切换到套件 B，然后删除套件 A
- **方案 C:** 保持现状——但需在 AGENTS.md / HARNESS.md 中明确记录这是有意为之（"渐进式重构中"）

无论哪种方案，当前状态（双实现 + 孤儿代码）都不可接受。

---

## 1. 已实现但未接入的功能

### 1.1 `idempotency.RunCleaner` — 存在但从未启动

**文件：** `internal/idempotency/store.go:126`

```go
func (s *GormStore) RunCleaner(interval time.Duration) (stop func()) { ... }
func (s *GormStore) purgeExpired() { ... }
```

**现象：** `RunCleaner` 和 `purgeExpired` 完整实现——后台 goroutine 定期删除过期行。但 `app.go` 中从未调用 `idemStore.RunCleaner()`。

```go
// app.go:266  — 创建 store
idemStore := idempotency.NewGormStore(gdb)
// app.go:275  — 传入 router，但从未启动 cleaner
```

对照：`TokenCache.RunCleaner`（在 `sso/client.go:126`）、`RateLimiter.RunCleaner`（在 `app.go:257`）、GC sweeper `Start`（在 `app.go:248`）都正确启动了。Idempotency cleaner 是唯一一个有实现但没启动的后台 goroutine。

**影响：** `idempotency_keys` 表的 `expires_at` 列形同虚设——过期行永远不会被删除。在长时间运行的生产环境中，该表持续增长，最终影响 `INSERT ... ON CONFLICT` 的性能。

**修复：** 一行代码：在 `app.go` 中 `idemStore.RunCleaner(5 * time.Minute)` 并 `stopIdemCleaner` 加入 shutdown。

### 1.2 `fileRepo.HardDelete` — 存在但删除操作不级联清理 `file_shares`

**文件：** `internal/service/file.go:Delete()` / `internal/repository/file.go:Delete()`

**现象：** `Delete()` 做软删除，`GC sweeper` 做硬删除。但 `file_shares` 表在文件被硬删除后仍然保留行——留下"共享指向 ghost 文件"的脏数据。

**影响：** `ListSharedWithMe` 可能返回已删除文件的共享记录，然后 `Get()` 返回 404，造成前端状态不一致。

**建议：** 硬删除文件时级联删除 `file_shares`（或软删除时也软删除共享记录）。

### 1.3 `context.Background()` 在后台 goroutine 中的使用

**文件：**
- `internal/storage/gc/sweeper.go:234` — `ctx := context.Background()`
- `internal/outbox/relay.go:114,121` — `context.Background()`
- `internal/sso/client.go:236` — `ctx := context.Background()`

**问题：** 这三个后台 goroutine 使用 `context.Background()` 而不是从 `app.Run()` 派生的 context。结果：
1. OpenTelemetry trace 在后台 goroutine 中是根 span（orphan），不与任何请求关联
2. `app.shutdown()` 无法通过 context cancellation 通知这些 goroutine 停止——它们依赖独立的 `stop` channel 机制

**影响：** 追踪能力受损——outbox relay 的每次 drain 创建的 span 没有父 span，无法在追踪系统中关联到触发它的请求。GC sweeper 同理。

**建议：** 为后台 goroutine 创建一个命名的根 tracer span（如 `app.shutdown` 时结束），或至少注入一个可取消的 `ctx` 使得 graceful shutdown 可以通过统一 context 传播。

---

## 2. 代码库结构问题

### 2.1 GORM 行结构体重复定义

两套实现各自定义了完全相同的 GORM 模型：

| 实体 | 套件 A 定义位置 | 套件 B 定义位置 |
|------|----------------|----------------|
| `fileRow` | `internal/repository/db.go:35` | `internal/file/repository.go:13` |
| `messageRow` | `internal/repository/db.go:92` | `internal/message/repository.go` (未检) |
| `commentRow` | `internal/repository/db.go:142` | `internal/comment/repository.go` (未检) |
| `fileShareRow` | `internal/repository/db.go:215` | `internal/file/repository_share.go` (未检) |
| `notificationRow` | `internal/repository/db.go:237` | `internal/notification/repository.go` (未检) |
| `auditEventRow` | `internal/repository/db.go:261` | `internal/audit/repository.go` (未检) |

**风险：** 如果两套定义不同步（例如套件 B 加了新列但套件 A 没有），同一张表可能出现不一致的行结构体映射。Go 编译不报错（因为不同的 package），但运行时行为异常。

### 2.2 文件行结构体嵌套（`internal/file` vs `internal/repository`）

`internal/domain/file.go` 的 `File` 结构体使用 `ID` 类型（`idgen.ID` = `uuid.UUID` 别名）。

两套 repository 都做 `fileRow ↔ domain.File` 的转换。转换函数在套件 A 和套件 B 中各写了一份完全相同的代码——违反 DRY 原则，也是维护陷阱。

### 2.3 Handler 层也有两套

| 模块 | 套件 A | 套件 B（未接入） |
|------|--------|----------------|
| File HTTP | `internal/handler/http/file.go` | `internal/file/handler_http.go` |
| File gRPC | `internal/handler/grpc/file.go` | `internal/file/handler_grpc.go` |
| Comment HTTP | `internal/handler/http/comment.go` | `internal/comment/handler_http.go` |
| … | … | … |

套件 B 的 handler 代码风格类似（接收 same service interface，调同名方法），但包路径和类型引用不同。这意味着若在套件 A 中加一个新的 API 端点（如之前的所有功能交付），套件 B 的等效端点**不存在**。

---

## 3. 边界情况补遗（前两篇未覆盖）

### 3.1 并发的 `MarkConversationRead` 与 `SendMessage`

**场景：** 用户 B 在用户 A 发送消息 M2 的同时，调用 `MarkConversationRead(A)`。由于 `MarkConversationRead` 的 `WHERE to_uid = B AND from_uid = A AND read_at IS NULL` 语句，M2 可能在事务序中：
- 先于 `MarkConversationRead` 提交 → 正确标记已读
- 后于 `MarkConversationRead` 提交 → M2 保持未读（正确行为）

但如果 M2 的 `SendMessage` 事务与 `MarkConversationRead` 事务交错（read committed 隔离级别），`MarkConversationRead` 的 SELECT 可能看不到 M2（MVCC 快照在事务开始时已固定），而 M2 后续提交导致 M2 `read_at IS NULL`——会话已被"标记为全部已读"但 M2 仍显示未读。

**影响：** `MarkConversationRead` 不是"我承诺未来消息也自动已读"，而只是一个批量 UPDATE——语义上这是正确的，但用户期望可能是"标记到目前为止的所有消息已读"。当前的无事务快照行为可能导致刚发的消息在标记已读后仍然未读。

**建议：** 记录 `MarkConversationRead` 的时间戳，在 inbox 查询中把这个时间戳传给客户端，前端可以据此判断哪些消息是"在标记已读之前"的。或者更简单：在文档/API 描述中明确语义。

### 3.2 Comment SoftDelete 与 ReplyCount 的漂移

**场景：** 评论 C1 有 3 个回复。C1 被软删除。但回复的计数不会递减——`reply_count` 在创建时 +1，软删除时不 -1。

**影响：** 如果 C1 被 `SetStatus(hidden)` 或软删除后又被硬删除（GC），其子评论成为 orphan——`parent_id` 指向一个不存在的行。`ListReplies` 按 `root_id` 查询（而非 `parent_id`），所以这些 orphan reply 仍然出现在返回结果中（因为没有检查 parent_id 对应的行是否存在）。

**建议：** 软删除评论时级联删除（或级联隐藏）所有子评论。或者在 `ListReplies` 查询中 JOIN `parent_id` 检查父行状态。

### 3.3 审计日志的异步写入丢失

**文件：** `internal/audit/repository.go` 和 `internal/repository/audit.go`

**设计：** 审计日志是 fire-and-forget——`Log()` 方法在 service 层被调用时通过 `_ = s.audit.Log(ctx, ...)` 忽略错误。这意味着审计日志写入失败**永远不会被感知**。

**场景：** 数据库连接池耗尽、磁盘满、网络故障——审计日志静默丢失，但主操作（block、share、resolve report）成功返回。事后调查时，审计线索不完整。

**建议：**
- 至少为审计日志写入失败打印一条 WARN 级别的日志
- 考虑将关键的审计事件也写入 outbox（与主操作同事务保证交付）
- 或者在 `audit_repository` 中使用带超时和有限重试的写入，而不是裸 `_ = `

### 3.4 文件分块上传（大文件支持）的缺失

**当前设计：** `PresignUpload` 返回单个 presigned PUT URL。对于超大文件（>100MB），单次 PUT 有诸多问题：
- 网络中断后必须从头重传（没有断点续传）
- 客户端超时（尤其是移动网络）
- 无法并行加速

**影响：** 虽然配置了 `max_upload_bytes: 50MB`，但 S3 的 presigned URL 默认没有分块上传支持。对于接近上限的大文件，用户体验很差。

**建议：** 实现 S3 Multipart Upload API 路径：
- `POST /v1/files/multipart/start` → 返回 `UploadID`
- `POST /v1/files/multipart/upload-part` → presign per-part URL
- `POST /v1/files/multipart/complete` → 组装
- 或者在客户端 SDK 中封装已有 S3 SDK 的 multipart 能力（但 aero-id 需要先提供 `UploadID`）

---

## 4. 测试与 QA 缺口

### 4.1 集成测试只覆盖套件 A

`internal/repository/*_integration_test.go` 中所有的集成测试（testcontainers）都测试套件 A 的实现。套件 B 的各 `internal/*/repository.go` 没有集成测试覆盖。

虽然功能等价，但如果未来切换到套件 B，需要确保其 SQL 查询与套件 A 行为完全一致（尤其是搜索、分页、事务行为）。

### 4.2 后台 goroutine 的测试覆盖

生产中有 4 个后台 goroutine：

| Goroutine | 启动于 | 有单元测试？ | 有集成测试？ |
|-----------|--------|------------|------------|
| Outbox relay | `app.go` | ✅ `relay_test.go` | ❌ 需要真实数据库 |
| GC sweeper | `app.go` (opt-in) | ✅ `sweeper_test.go` | ❌ 需要真实存储 |
| Rate limit cleaner | `app.go` (opt-in) | ✅ `ratelimit_test.go` | N/A |
| SSE Hub（无后台 goroutine，只是数据结构） | N/A | ✅ `hub_test.go` | N/A |
| **Idempotency cleaner** | **未启动** | ✅ `store_test.go` 有 | ❌ |

`outbox relay` 的单元测试使用 mock `Repository` 和 `Handler`，但**没有集成测试验证它与真实 PostgreSQL 的交互**（比如事务提交后事件可见性、并发 drain 场景）。

### 4.3 并发场景的测试缺失

整个测试套件中缺乏对并发竞态的测试：
- 同一个评论的并发 like/unlike
- 同一个文件的并发上传 + 删除
- 发送消息 + 对方同时 block 发送者
- 并发 idempotency key 保留 + 完成

虽然代码层面做了 `ON CONFLICT DO NOTHING` 和事务保护，但**没有测试验证这些竞态的行为是否符合预期**。

---

## 5. 性能优化补遗

### 5.1 `ListInbox` 的全表扫描风险

`ListInbox` 按 `to_uid` 查询 + `ORDER BY created_at DESC`。随着用户收到的消息数增长（假设一个活跃用户 10 万条消息），cursor 分页需要扫描大量行才能找到 cursor 位置。

虽然 `(to_uid, created_at DESC)` 索引存在（`000015`），但 `read_at IS NULL` 过滤（`unreadOnly=true`）可能让查询计划选择 `idx_messages_unread` 部分索引再 filter。需要 `EXPLAIN ANALYZE` 验证。

### 5.2 通知批量写入

`NotificationDispatcher.Handle()` 对每条通知调用一次 `notifs.Create()`。如果群聊场景下一条消息需要写入 1000 条通知，1000 次独立 INSERT 的事务开销很大。

当前 `NotificationRepository`（套件 A 的 `internal/repository/notification.go` 和套件 B 的 `internal/notification/repository.go`）都只有单行 Create 方法。批量插入（`INSERT INTO notifications (...) VALUES (...), (...), (...)`）可以大幅降低事务开销和连接往返。

### 5.3 审计日志的写放大

每个敏感操作（share、block、report resolve、message delete）产生一条审计日志。`audit_events` 表是 append-only，没有清理策略。在长时间运行的系统中，该表会成为最大表之一。如果没有索引或清理策略：
- `audit_events` 的查询（目前没有对外暴露，但未来 Direction 3 的管理员查询面板需要）会越来越慢
- 存储成本线性增长

**建议：** 为 `audit_events` 增加 TTL 清理策略（如保留 90 天），或支持导出后归档。

---

## 6. 高价值扩展方向（5 个）

以下方向与前两篇分析完全不重复。

---

### 🧭 方向 A：文件版本控制

**概念：** 允许用户对同一文件名的多次上传产生版本历史。`files` 表增加 `version_number` 列，每次更新创建新版本行但共享同一 `parent_file_id`。

**为什么需要它：**
1. **当前的文件更新是原地覆盖。** `UpdateMetadata` 允许改名/改可见性，但文件内容无法更新——要更新必须先删再传，丢失旧内容。这不是"文件管理"，这是"文件上传"。
2. **产品预期差。** 用户上传"设计稿 v3.png"，然后想回退到 v2——当前架构不支持。用户会期待"版本历史"。
3. **技术实现清晰。** 只需要在 `files` 表加 `file_group_id` 和 `version` 列。下载 API 返回最新版本或指定版本。旧版本的 blob 保留在 storage 中，直到所有版本被删除。
4. **与去重互补。** 如果 v2 和 v3 内容完全相同（SHA256 匹配），可以共享 storage_key（已在做），只是行不同。

**实现要点：**
- `files` 表加 `file_group_id` (UUID, 首次上传时生成) + `version` (int, 自增)
- `ListFiles` 返回每个 file_group 的最新版本
- `GET /files/:id?version=2` 获取历史版本
- 新增 `POST /files/:id/versions` 上传新版本
- GC 清理：仅当 file_group 的所有版本都被软删除后才释放 blob

---

### 🧭 方向 B：定时/延迟消息（Schedule Send）

**概念：** 允许用户创建一条消息，指定在未来某个时间点送达。类似 Slack 的 "Schedule message"。

**为什么需要它：**
1. **产品差异点。** 1:1 消息功能在竞品中高度同质化。定时消息是一个简单但能明显提升用户感知的功能。
2. **技术基础已存在。** Outbox 机制是天然的延迟投递队列——只需要在 `outbox_events` 增加 `scheduled_at` 列，`FetchUnpublished` 过滤条件改为 `published_at IS NULL AND (scheduled_at IS NULL OR scheduled_at <= now())`。
3. **实现成本极低。** 不涉及新表、新索引——只需要修改已有的 outbox pipeline 和消息发送路径。
4. **为后续工作流自动化奠基。** 定时消息是未来"消息机器人"、"自动回复"、"提醒"等功能的基础。

**实现要点：**
- `messages` 加 `scheduled_at` 列（NULL=立即发送）
- `SendMessage` 增加 `scheduled_at` 参数，当设置时，写入消息 + 延迟的 outbox event
- Outbox relay 和 NotificationDispatcher 不需要改动——它们只消费已到期的 event
- 客户端 API：`POST /v1/messages { ..., "scheduled_at": "2026-07-01T10:00:00Z" }`
- 取消定时：`DELETE /v1/messages/:id?cancel_scheduled=true`（软删除消息 + 标记 outbox event 为 dead_letter）

---

### 🧭 方向 C：消息转发 / 引用回复

**概念：** 允许用户转发一条消息给另一个用户（"张三说: ..."），或在回复时引用原消息（类似 Telegram 的回复引用）。

**为什么需要它：**
1. **用户的自然沟通模式。** "我把 A 说的话转发给 B"是最常见的协作场景之一。当前只能复制粘贴。
2. **引用回复是防止上下文丢失的关键。** 在长对话中，"你刚才说的那个…"很快会丢失上下文。引用回复 UI 在客户端是标准交互。
3. **数据模型简单。** `messages` 加 `forwarded_from_id` 和 `reply_to_id` 两个可空列即可。`content` 保持不变（引用内容在客户端渲染），服务端存储时保留原始 message ID。
4. **与 Block 机制集成。** 如果原始消息的发送方已被接收方 Block，转发或引用时应该显示"消息不可用"（或直接拒绝转发）。

**实现要点：**
- `messages` 加 `reply_to_id` (UUID, NULLable) + `forwarded_from_id` (UUID, NULLable)
- 验证：`reply_to_id` 和 `forwarded_from_id` 必须指向当前用户能读取的消息（`canReadMessage`）
- 通知：转发时 emit `message.forwarded` event → 目标用户收到转发通知（显示"张三转发了一条消息"）
- 当原始消息被删除时，引用/转发显示为"[消息已被删除]"

---

### 🧭 方向 D：文件标签 / 自定义分类

**概念：** 允许用户为文件打标签（tags / labels），并按标签筛选和检索文件。

**为什么需要它：**
1. **文件管理的基本需求。** 当前文件列表是平坦的——`ListByOwner` 返回所有文件按时间排序。用户无法按项目、类型、状态分类。标签是最简单的分类方式。
2. **与搜索形成互补。** 当前搜索只对文件名和元数据做 tsvector 索引。标签可以提供额外的"人工标注"维度，提高搜索精准度。
3. **与共享集成。** 共享文件时也共享标签（接收方看到同样的标签结构），或接收方有自己的标签视图。
4. **实现独立且轻量。** 只需要 `file_tags` 表（`file_id, tag, created_at`）+ 一个 `tag` 索引 + CRUD API。

**实现要点：**
- `file_tags` 表：`(file_id, tag)` 复合主键，加 `created_at`
- `POST /v1/files/:id/tags { "tags": ["design", "v3", "approved"] }`（全量替换）
- `GET /v1/files?tags=design,approved` 按标签筛选（`WHERE file_id IN (SELECT file_id FROM file_tags WHERE tag = ANY(?))`）
- 自动补全：`GET /v1/tags?prefix=des` 返回当前用户使用过的以 "des" 开头的标签
- 注意：标签是全量替换而非增量追加，避免并发冲突

---

### 🧭 方向 E：跨区域/跨集群部署支持

**概念：** 使 aero-id 可部署在多个地理区域（如美东 + 欧西 + 亚太），每个区域部署独立的 aero-id 实例，但共享同一 SSO 和可选的跨区域消息路由。

**为什么需要它：**
1. **数据驻留合规。** GDPR 要求欧洲用户数据留在欧洲。当前的单数据库设计意味着所有数据在同一区域。多区域部署允许按 `tenant_id` 或 `owner_uid` 前缀路由数据到指定区域。
2. **低延迟全局访问。** 如果用户分布在多个大洲，单区域部署意味着非本区域用户的请求延迟 +200ms。多区域部署可以将数据放在离用户最近的位置。
3. **灾难恢复。** 单区域故障 = 全局不可用。多区域部署 + active-passive 故障切换可以将 RTO 从"重新部署"降低到"DNS 切换"。
4. **与方向 1（多租户）互补。** 企业客户通常要求数据在指定国家/地区存储。多区域 + 多租户是两个正交能力，组合后满足企业合规的大部分需求。

**实现要点：**
- 这不是一个"功能"，而是一个**部署架构模式**。需要的技术组件：
  - 配置层：每个区域独立的 `config.yaml`，指向本区域数据库 + 存储后端
  - 消息路由：如果用户 A 在美东、用户 B 在欧西，A 发给 B 的消息需要通过跨区域队列（如 Kafka MirrorMaker 或 PgLogical）
  - SSO 共享：SSO 是全局限一的（设计如此），所以跨区域认证不需要额外工作
  - **但 aero-id 当前的设计假设单数据库**——`outbox`、`idempotency_keys` 都是 per-database 的。跨区域消息路由需要额外的中间件或事件总线
- 建议最小可行：两个区域各自独立部署（不共享数据），通过 DNS 就近路由（geo-based routing）。用户在自己区域的数据库中读写。跨区域互不可见——先锁住数据驻留合规需求
- 后续演进：引入跨区域消息转发（通过全局 outbox relay + 区域代理）

---

## 7. 治建议汇总（按紧急程度）

| 紧急度 | 项目 | 修复时间估计 | 影响 |
|--------|------|------------|------|
| **P0** | 决定两套实现的去留（套件 A vs 套件 B） | 1 天决策 + 1-3 天执行 | 架构清晰度、维护成本 |
| **P0** | `app.go` 启动 `idemStore.RunCleaner()` | 15 分钟 | 表无限增长风险 |
| **P1** | 文件软删除时级联清理 `file_shares` | 半天 | 数据一致性 |
| **P1** | 审计日志写入失败添加日志告警（`_ =` → WARN log） | 1 小时 | 可观测性 |
| **P1** | 评论软删除时子评论处理策略（级联 vs 标记） | 半天 | 数据完整性 |
| **P2** | `context.Background()` → 命名 tracer span | 1 天 | 追踪完整性 |
| **P2** | `notificationRepository.BulkCreate` 批量接口 | 1 天 | 群聊场景性能 |
| **P2** | `audit_events` TTL 清理策略 | 半天 | 存储成本 |
| **P3** | 并发竞态测试用例 | 2-3 天 | 测试覆盖 |
| **P3** | 套件 B 的集成测试 | 2 天 | 测试覆盖 |
