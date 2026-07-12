这是一份经过充分研究的文档。我已对照代码库（`internal/` 下 127 个源文件 + 44 个迁移文件 + 双协议实现）对每个主张进行了交叉验证。以下是逐项审核。

---

## 代码验证结果

### 📊 数量勘误

| 文档声称 | 实际情况 | 影响 |
|---------|---------|------|
| `internal/` 85 个源文件 | 127 个（排除 `_test.go` 和 `.pb.go`） | 低估约 50%。可能文档作者只计了特定子集，不影响分析质量 |
| 38 个 RPC | 43 个（7 个 service 接口） | 低估 5 个。遗漏：`SearchService.Search`(1) + `PreferenceService.Get/Set`(2) + 此前可能未计入的 RPC。方向判断不受影响 |
| 22 个迁移 | ✅ 22 up + 22 down，完全匹配 | 正确 |

### ✅ 确认准确的主张

| 主张 | 代码证据 | 评估 |
|------|---------|------|
| **消息模型严格 1对1** | `message.go`/`service.go` 中所有接口（`Send`、`Conversation`、`Inbox`）操作 `from_uid ↔ to_uid`，无 `conversation_id` 概念。"Conversation" 是指双向对话，非群组 | ✅ 准确 |
| **审核 API 最小** | `handler/http/admin.go` 仅 1 个路由：`GET /v1/admin/audit-log`。`handler/grpc/ops.go` 仅有 `GrantRole`/`RevokeRole` | ✅ 准确 |
| **无 bans 端点** | `domain/admin.go` 定义了 `PermUserBan = "user.ban"`，migration 000022 seed 了该权限，但**没有端点、没有表、没有 service 方法**实现封禁 | ✅ 准确 |
| **限流为进程内实现** | `pkg/ratelimit/ratelimit.go` 使用 `sync.Mutex` + `map[string]*bucket`，注释明确说 "per-instance" | ✅ 准确 |
| **SSE Hub 为进程内** | `hub/hub.go` 使用 `sync.Map` + channel，无跨实例中继 | ✅ 准确 |
| **评论嵌套 2 层限制** | `domain/comment.go:ValidateNewComment` + migration schema 中 `parent_id` → `root_id` 关系，第三层返回 `ErrNestingTooDeep` | ✅ 准确 |
| **FTS 跨三实体** | migration 000016：`messages`、`comments`、`files` 各有 `tsvector` + GIN 索引，使用 `simple` 词典 | ✅ 准确 |
| **通知折叠/静默时段** | migration 000020 添加 `collapsed_count`；`service/preference.go` 实现 `QuietHoursStart/End` + `Timezone` 检查，含跨午夜逻辑 | ✅ 准确 |
| **Block 感知通知** | `notification/dispatcher.go:Handle` 中 dispatch 前检查 `blocks.IsBlocked`，被 blocked 时静默丢弃通知 | ✅ 准确 |
| **SSE 断线重放 limit=50** | `handler/http/events.go:99` 硬编码 `h.fetcher.ListSince(..., 50)` | ✅ 准确 |
| **Webhook 为单例配置** | `outbox/webhook.go` 的 `WebhookConfig` 为单一 URL + 单一 signing key。无数据库表支持动态注册 | ✅ 准确 |
| **消息反应** | `internal/message/message.go` 接口含 `ReactToMessage`/`UnreactMessage`，migration 000013 `message_reactions` | ✅ 准确 |
| **存储 GC / refcount** | migration 000021 `storage_refs` 表 + `storage/gc/sweeper.go` 的 `RefCounter` 接口 | ✅ 准确 |
| **文件分享** | `file/repository_share.go` 实现 `Grant`/`Revoke`/`ListSharedWithMe`/`IsSharedWith`，migration 000006 `file_shares` | ✅ 准确 |
| **消息已读回执** | `message/service.go:MarkRead` + `outbox` event → notification dispatcher 产生 `message.read_receipt` | ✅ 准确 |
| **无 accounts 表** | 全部 22 个 migration 中无 `accounts` 表，无 FK 引用 uid | ✅ 准确 |
| **SSO 缓存/熔断/重试/stale-fallback** | `sso/token_cache.go`（TTL 过期）、`sso/breaker.go`（circuit breaker）、`sso/client.go`（`ValidateToken` 重试+ fallback） | ✅ 准确 |
| **内容去重 + StorageRef 原子计数** | migration 000021 + `internal/file/service_upload.go` 中 `CompleteUpload` 内的 refcount 更新 | ✅ 准确 |

### ⚠️ 需要补充或重要但未提及

| 项目 | 说明 |
|------|------|
| **未提及 `PreferenceService`** | 文档列出 38 个 RPC 但未计入 Preference 的 2 个 RPC（`GetPreferences`/`SetPreferences`）。不影响分析方向 |
| **未提及 `SearchService`** | 同样是遗漏的 RPC（`Search`）。FTS 在附录中提到了索引，但未将其作为独立方向 |
| **Block N+1 问题** | 附录正确指出了 `checkSendBlock` 的单条查询，但**遗漏了 Inbox 端的批量检查缺失**：`service/message.go:Inbox` 对返回的每条消息没有批量 block check。这是真正的 N+1 |
| **SSE 断线重放的 limit=50 是硬限制** | 附录说"改为 limit=50 但配合 Last-Event-ID 递进"，但当前 `replayMissed` 是一次性查询 limit=50，并未实现递进分页。**高流量用户断线后可能丢失 >50 条事件** |

---

## 对五个方向的深度评审

### 方向一：群组/多人会话

**价值判断：** ⭐⭐⭐⭐（稍低于文档的 ⭐⭐⭐⭐⭐）

文档定位为最高产品价值，但我要提出两个架构约束：

1. **当前 message 模型高度优化为 1对1**：`Send` 接口签名 `(to_uid, content, ...)` 贯穿整个堆栈。添加 `conversation_id` 不是简单的"加个新字段"——现有通知 dispatcher（`message.sent`）不感知群组，`@mention` 和 `@all` 需要全新的通知分片逻辑。

2. **Block 语义在群组场景未定义**：附录正确指出了这点。但影响比附录描述的更深：当前 `IsBlocked(blocker, blocked)` 是方向性检查（A blocked B → B 不能给 A 发消息）。在群组中，如果 A blocked B，B 所在的群组 A 还能看到 B 的消息吗？这不是小需求，会影响 schema（`conversation_members.visibility`）、消息列表过滤、通知 dispatch。

**修正估算：** 3-4 周偏乐观。考虑到通知改造 + block 语义定义 + 迁移兼容 + gRPC 对等，更现实的估算是 **5-6 周**。

### 方向二：全局审核工作台

**价值判断：** ⭐⭐⭐⭐⭐（与文档一致）

这是**真正的 P0**。实际上代码库中已存在未使用的轮子：

```
migrations/000022  → roles + permissions     ✅ seed
repositories/admin.go → HasPermission         ✅ 已实现
domain/admin.go     → PermUserBan             ✅ 已定义（但无 endpoint）
```

**添加的代码工作量：**
- `bans` 表 + repository → **~80 行**
- `POST /bans` / `POST /unbans` handler + service → **~100 行**
- 全局评论/消息隐藏端点 → 复用现有 `service` 层的 `Hide`/`Delete`（只额外加权限检查）→ **~60 行**
- 举报队列端点 → 现有 `comment_reports` + `ListReports`/`ResolveReport` 已存在，只需添加**跨作者过滤**→ **~40 行**

**估算修正：** 1-2 周（低于文档的 2-3 周），因为超过一半的基础设施已就位。

### 方向三：文件版本控制

**价值判断：** ⭐⭐⭐⭐（文档 ⭐⭐⭐⭐，一致）

**关键工程细节：** 文件版本化不仅仅是加一张 `file_versions` 表。它还需要：

1. **Shared 机制的适配**：当前 `file_shares` 关联 `file_id`，如果共享指向特定版本，需要加 `version` 列
2. **Presign URL 的版本感知**：`DownloadURL` 当前返回最新版本的存储 key，需要可选参数指定版本
3. **GC 适配**：当前 GC 以 `file` 行为单位清理引用计数，版本化后需要改为以 `file_version` 行为单位。migration 000021 的 `storage_refs` 表已经为此做好了准备（refcount 按 storage_key 计数，不关心 file_id），所以 GC 适配的改动实际上比文档评估的**更小**
4. **引用断裂问题**：文档说"旧消息仍指向旧 file_id"——实际上当前消息、评论存储的是 `file_id`（不是 storage_key），版本化后旧消息的 `file_id` 不变，`DownloadURL` 默认返回最新版本，导致旧消息指向新内容。这不是 bug，是设计选择，但需要文档明确说明

**估算修正：** 3 周（低于文档的 3-4 周），主要因为 storage_refs 已支持 refcounting per-key，无需大改。

### 方向四：Webhook 订阅管理

**价值判断：** ⭐⭐⭐⭐（略低于文档的 ⭐⭐⭐⭐⭐）

我同意 outbox 基础设施提供了 80% 的已有投资。但有两个现实约束：

1. **当前 webhook handler 是同步调用**：`outbox/webhook.go` 的 `Handle` 是同步 HTTP POST。为 N 个订阅做扇出，如果其中的 2-3 个慢/失败，会阻塞 relay tick → 影响所有事件的投递延迟。扇出需要改为**每个订阅一个 goroutine**（带独立超时 + 上下文取消）。

2. **投递历史记录会增加 outbox 模式的复杂度**：文档的 `deliveries` 表（可选）如果实现，需要通过 outbox 自身的 event 来记录——这要求在 webhook handler 中 side-write 数据库，这会污染 handler 的单一职责。

**把扇出改为异步 goroutine pool** 不是大改动（约 100 行），但会影响 relay 的 `drain` 语义：当前 relay 等待每个事件被 handler 处理完才 `MarkPublished`。扇出后需要等待所有订阅处理完（或各自超时），然后做部分 success 标记。

**估算修正：** 2-3 周，与文档一致。但建议**扇出设计先做技术选型**（fan-out goroutine vs event bus）。

### 方向五：分布式限流

**价值判断：** ⭐⭐⭐（与文档一致）

**P0 的关键发现：** 文档建议 PG advisory lock 替代进程内存限流。但仔细看代码：

```go
// ratelimit.go: middleware 在每个请求路径调用 Allow(key)
// 这是**每个请求一次**的同步检查
```

用 PG 实现 P0 限流意味着**每个请求一次数据库查询**——对于 `/v1/events` SSE 长连接的心跳路径和 `/v1/healthz` 公共路径，这意味着不必要的开销。更好的 P0 方案：

- **公共路由 `/healthz`, `/readyz`**：跳过限流（当前已跳过）✅
- **认证路由**：用 PG advisory lock 做限流，但只在**写路径**（Send、Create、Upload 等）激活，读路径（List、Get、Inbox）保持进程内存限流
- 这样 P0 的 PG 开销只影响写路径

**P1（Redis）的兼容路径：** 如果先实现了 PG 方案，切换 Redis 时需要对 `Limiter` 做抽象——当前 `ratelimit.Limiter` 是具体类型，不是接口。建议 P0 时就把 `Limiter` 提取为接口。

---

## 附录验证

| 附录条目 | 代码证据 | 评估 |
|---------|---------|------|
| 评论嵌套 3 层拒绝 | `domain/comment.go:ValidateNewComment` - 正确 | ✅ |
| 自 blocking 拒绝 | `block/block.go:Block` 中的 `cannot block yourself` 检查 | ✅ |
| 并发点赞幂等 | migration 000005 的 UNIQUE(comment_id, uid) + `clause.OnConflict{DoNothing: true}` | ✅ |
| outbox 快重启最终 drain | `relay.go:Start` 的 stop func 中 `drain(context.Background())` | ✅ |
| SSO 完全不可用 | `sso/client.go:ValidateToken` 中的 stale token fallback + `breaker.go` 熔断 | ✅ |
| 自己给自己发消息 | `service/message.go:send` 未禁止 from_uid == to_uid；通知 dispatcher 检查 `n.ActorUID == n.RecipientUID` 后跳过 | ✅ |
| 文件被引用时删除 | GC sweeper 的 retention window + refcount 保护 | ✅ |
| 消息已读回执风暴 | 通知 collapse 5min + rate limit 10/type/min | ✅ |
| Outbox 轮询索引 | migration 000015 确认 `idx_outbox_unpublished` 覆盖 `(created_at, id) WHERE published_at IS NULL` | ✅ |
| SSE 断线重放 limit=50 | 硬编码，**无递进分页** | ⚠️ **确认风险** |
| GORM N+1 block 检查 | Inbox 路径无批量 block check | ⚠️ **确认风险** |
| Admin audit log cursor | `ORDER BY created_at DESC, id DESC` + `WHERE id < ?` — UUIDv4 (`uuid.New()`) 不满足时间序，如果 `created_at` 有重复，光标分页可能跳过记录 | ⚠️ **低风险，但正确** |

---

## 执行序列调整建议

结合代码验证结果，建议调整如下：

```
Phase A（2026-07 ~ 08, 2 周）
  ├── 方向二（审核工作台）—— 安全运营基线，已有 60% 基础设施
  ├── 方向五 P0（PG 限流 + 读/写路径分离）—— 低投入、高安全收益
  └── 附录修复：SSE replay 递进分页 + Inbox 批量 block check

Phase B（2026-08 ~ 09, 3 周）
  ├── 方向三（文件版本化）—— storage_refs 已就位，核心数据能力
  ├── SSO 缓存 LRU 上限 + 预热（附录性能优化 2-3）
  └── Admin audit log 分页用 `created_at DESC, id DESC` + `WHERE (created_at, id) < (?, ?)`
        (复合游标，修复 UUIDv4 非时序问题)

Phase C（2026-09 ~ 10, 5-6 周）
  ├── 方向一（群组会话）—— 产品方向，但工程难度最大
  └── 方向四（Webhook 平台化）—— 扇出架构先行

Phase D（2026-10 ~ 11）
  ├── 方向五 P1（Redis 共享层）—— 按需启动，取决于多实例需求
  └── 通知未读计数物化小表（附录性能优化 1）
```

**与文档原方案的差异：**
1. **方向四推迟到 Phase C**（而非 Phase B）—— 群组的通知模型改造可以为 webhook 的扇出架构提供参考经验，两者共享 "一对多分发" 的核心挑战
2. **附录的 SSE 分页 + Inbox N+1 修复提前到 Phase A**—— 这些是现有 bug，不需要架构变更
3. **方向一从 Phase C 提前评估**—— 如果产品规划有群组需求，Phase A 结束时应该完成技术选型（直接加字段 vs 新表），避免 Phase C 被动决策
