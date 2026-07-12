现在我已经对整个代码库有了透彻的理解。以下是我的架构分析。

---

# 架构分析：aero-id

## 1. 架构评估

### 1.1 优势

当前架构质量非常高——比大多数生产 Go 服务都要好。原因如下：

**严格的单向依赖。** `handler → service → repository → Postgres` 以及 `handler/{http,grpc}` 共享同一个 `service` 层，这强制 HTTP 和 gRPC 传输层行为完全一致。这并非理论上的——`internal/service/service.go` 中的 `FileService` 接口被两个传输层使用。没有"gRPC 优先，HTTP 次之"或反之的偏差。实际证据：两个传输层的 handler 文件都调用相同的 `service.Create(ctx, in)` 方法。

**事务性发件箱是真正的亮点。** `internal/outbox/outbox.go` 中的实现是经过深思熟虑的：事件通过调用者提供的 `*gorm.DB` 写入，因此它们与领域行一起提交（或回滚）。重放安全通过 `published_at` + `retry_count` + `backoff()` 实现。死信通道防止队列被单个失败事件阻塞。消费链（`internal/outbox/chain.go`）允许多个 handler 串联。

**SSO 弹性层是非玩具级别的。** 代码库包含：
- 令牌和用户的陈旧缓存（用于 SSO 宕机时的优雅降级）
- 带有半开探测的`breaker`（`internal/sso/breaker.go`）
- 令牌验证的`singleflight`（消除缓存未命中时的高并发冲击）
- 可配置的重试 + 退避
- mTLS + 证书锁定
- 冷启动预热探测

**存储抽象是干净的。** `FileStorage` 接口（`internal/storage/storage.go`）有 8 个方法，包括可选的 `Lister` 接口。本地和 S3 后端都完全实现了它。`NewKey` + `PresignPut` 模式实现了无服务器的直接上传流程——客户端从 S3 获取预签名 URL，上传，然后调用 `CompleteUpload`。aero-id 从未看到文件字节；这对于大型文件至关重要。

**限流器虽小但很有效。** `internal/pkg/ratelimit/ratelimit.go` 中的令牌桶实现是隔离的、带空闲淘汰的并发安全，并且干净地集成到 HTTP 中间件和 gRPC 拦截器中。`RunCleaner` 函数解决了用户提出的未过期空闲 actor 无限增长的问题。

### 1.2 局限性

**SSO 缓存无限增长。** TokenCache（`internal/sso/token_cache.go`）和 UserCache（`internal/sso/user_cache.go`）都使用未绑定的 `map[string]...` 且没有 `maxSize` 驱逐。TokenCache 的 `SweepExpired` 在 TTL + 过期窗口后移除条目，但在此期间，拥有大量唯一令牌的用户（或令牌旋转攻击者）会无限增长 map。文件中的 `RunCleaner` 提供了节奏但未强制执行大小上限。

**UserCache 中存在锁定竞态条件。** 在 `GetMany` 中（`user_cache.go:44-74`）：代码在写锁下读取映射以检测缺失项，释放读锁，调用 `fetcher`（无锁），然后重新获取写锁以存储结果。在 `fetcher` 运行时，另一个 goroutine 可能会为相同的 uid 调用 `GetMany`，看到相同的缺失并通过另一个远程调用触发。文档中提到的"额外风险"是真实的——对于被高并发请求的冷 uid，存在缓存高并发冲击。

**发件箱中继缺乏优雅关闭。** 尽管代码库已交付 P0-4（ROADMAP），但 `relay.Start()` 中的 goroutine 在主循环退出后没有执行最终 drain。`stopRelay()` 关闭管道，goroutine 退出。在关闭期间快速重启会留下未发布的事件，直到下一个 tick 间隔。这已经在 `internal/outbox/relay.go` 中确认，其中 `select` 在 `<-done` 上返回，没有调用 `drain`。

**发件箱消费顺序。** `Relay.drain` 在单个 goroutine 中顺序处理事件（`internal/outbox/relay.go:91-120`）。如果一个事件 handler（例如，一个慢速 webhook）阻塞，所有后续事件都会延迟。该问题在分析文档中被标记为中等严重性。

**Prometheus 路径基数。** `internal/pkg/metrics/metrics.go` 使用 `path` 作为 `http_requests_total` 的一个标签。对于预签名 URL（路径包含文件 ID），这是高基数的。分析文档正确地标记了这一点。

### 1.3 架构债务

**搜索不检查文件共享。** `SearchComments` 按 `owner_uid = ?` 限定范围。用户无法搜索其他人文件上的评论。这是故意的但有限制性。修复会需要 `file_shares` 连接（用于共享文件）加上公共可见性检查。当共享访问模式更广泛时，这会成为用户摩擦点。

**消息模型达到了 1:1 的天花板。** `messages.(from_uid, to_uid)` 索引对于直接消息很好，但不支持群组频道。`messages.channel_id` 列不存在。发件箱 + 通知基础设施是频道就绪的，但模式需要迁移。

**审计：部分启用。** 分享操作被审计（`service_share.go:44`），上传/删除路径没有。评论审核有审计。这种不均等的覆盖意味着"谁做了什么"的取证会留下缺口。

---

## 2. 扩展方向

### 方向 1：群组频道（将 1:1 消息模型升级为通用频道）

**为什么：** 当前消息模型是 1:1 的上限。对于群组对话、项目空间或频道广播，您需要 `channel_members` + `last_read_at` + `channel.type`（direct/group/broadcast）。这是产品上最大的跃升——它使 aero-id 成为协作平台而不仅仅是消息传递后端。

**核心挑战：**
- **迁移策略：** 分析文档正确地建议不要在同一张表上混合使用旧 1:1 消息和新频道消息。旧的 `(from_uid, to_uid)` 行应迁移到名为 `direct`、`member_count=2` 的频道。一次性迁移脚本 `000022_migrate_messages_to_channels.up.sql` 是正确的方法。收件箱查询变得简单：`WHERE channel_id IN (SELECT channel_id FROM channel_members WHERE uid = ?)`。
- **`last_read_at` 写入放大：** 分析文档建议使用逻辑时钟（单调的 `channel.message_seq`）而不是时间戳。我同意这个分析。对于 500 人的频道，每次成员查看对话框都会更新 `channel_members.last_read_at`。基于序列的方法（存储在内存中，异步持久化）会好得多。
- **离开频道后匿名化（GDPR）：** `from_uid` 保留是可追溯的。分析文档的 `anonymize_on_leave` 配置参数是必要的补充。

**预期的架构变更：**
```
internal/
  channel/              # 新特性包
    channel.go          # domain.Channel 实体
    service.go          # ChannelService（创建/加入/离开/发送/列出）
    repository.go       # channel + channel_members + 频道消息的 GORM 查询
    handler_http.go     # HTTP 端点
    handler_grpc.go     # gRPC RPC
  message/
    message.go          # 扩展 domain.Message 以包含 ChannelID（可选）
```

**对现有系统的影响：** 中等。通知调度器可以直接复用——`channel_members` 上的粉丝输出会生成 `message.sent` 事件（与 1:1 相同的事件类型）。SSE hub 处理程序不变。主要影响在消息仓库，它需要处理新旧消息格式。

---

### 方向 2：统一活动流（⭐⭐⭐⭐⭐——最推荐的第一个方向）

**为什么：** 零模式变更，为每个其他方向提供入口，并为用户提供"发生了什么"的统一视图。分析文档正确地将其评为最高分。

**核心挑战：**
- **4 表 UNION ALL 性能：** `SELECT ... FROM messages UNION ALL SELECT ... FROM files UNION ALL SELECT ... FROM comments UNION ALL SELECT ... FROM notifications ORDER BY created_at DESC LIMIT 21` 对单个用户有效，但对管理员的全局时间线会扫描大量数据。分析文档建议使用物化视图——我同意。增量物化视图（PostgreSQL `REFRESH MATERIALIZED VIEW CONCURRENTLY`）对于生产负载是必要的。
- **折叠策略：** 分析文档建议在 5 分钟窗口内按 `(actor, domain, action)` 折叠。`NotificationDispatcher` 已经实现了折叠逻辑（`WithDispatcherCollapse`）。这种模式可以复用，但活动流折叠应按 `(actor_uid, domain, action)` 而不是按通知收件人来键控。
- **SSE 事件标准化：** 分析文档正确地指出 SSE 事件 payload 需要标准化。当前的 `SSEEvent` 有 `{ID, Type, Data}`。活动流事件需要在 `Data` JSON 中包含 `domain`、`object_id`、`summary_text`，以便客户端无需额外 API 调用即可渲染。

**预期的架构变更：** 没有新的特性包。添加一个 `internal/activity/` 包，协调仓库调用，执行折叠，并发出 SSE 事件。没有新的数据库表。

---

### 方向 3：内容生命周期策略引擎（⭐⭐⭐——高价值但复杂）

**为什么：** 生产系统需要配额、保留策略和垃圾回收。存储 GC 已经存在（`internal/storage/gc/sweeper.go`），但缺少配额执行和策略驱动的删除。

**核心挑战：**
- **配额一致性问题：** 分析文档建议将配额更新和文件 INSERT 放在同一个事务中。我同意这个分析。当前的代码路径：
  ```
  BEGIN;
    INSERT INTO files (...);
    UPDATE storage_quota SET used_bytes = used_bytes + $size WHERE uid = $uid;
    IF used_bytes > quota_max_bytes THEN ROLLBACK;
  COMMIT;
  ```
  这消除了分析文档标记的所有不一致场景。
- **硬删除 + S3 幽灵 blob：** 当 DB 记录已删除但 S3 对象仍然存在时（由于 GC 时序或网络分区），`hard_delete` 无法删除 blob。分析文档的补偿机制（重试队列 + 死信）是必要的。
- **原子引用计数：** 文件去重意味着多个 `.Create` 操作可能共享一个存储键。`StorageRefRepository` 接口已经存在，但需要有竞争力地去实现。否则，当只有一个引用者被删除时，去重的 blob 会被意外删除。

**对现有系统的影响：** 低到中等。`storage_quota` 表，配额中间件以在 INSERT 前拒绝大型上传。大部分逻辑驻留在 `service/file.go` 服务层。

---

### 方向 4：协作审核工作台（⭐⭐⭐⭐——最高用户价值）

**为什么：** 审核不是可选的——对于包含用户生成内容的平台，这是合规性和信任的基础。

**核心挑战：**
- **审核员匿名化：** 分析文档建议使用化名（`moderator-{hash}`）而不是暴露真实姓名。这是审核中偏见减少的最佳实践。审核员的真实 uid 应该仍然在审计日志中可用，但永远不要在前端显示。
- **AutoModerator 规则引擎：** 分析文档建议使用 **CEL（通用表达式语言）** 用于可热加载的规则。我同意。规则作为 JSONB 存储，例如 `{"condition": "file.size > 10MB || comment.content.contains('spam')", "action": "flag"}`。编译后的规则在 AutoModerator 中缓存。
- **二阶段审核 SLA：** 分析文档建议首次目标 < 15 分钟，申诉 < 4 小时。需要 `AuditSLA` 仪表板（扩展 `metrics` 包：`moderation_resolution_time_seconds` 直方图）。

**对现有系统的影响：** 中等。新表：`moderation_queue`、`moderation_actions`、`automoderator_rules`。新包：`internal/moderation/`。审核员的审核视图需要新端点但不需要新的服务层方法——`CommentService.Hide`/`Unhide`/`ResolveReport` 已经存在。

---

### 方向 5：文件预览加工管线（⭐⭐⭐⭐）

**为什么：** 用户上传文件。他们需要缩略图、病毒扫描和文档预览。这直接影响了文件服务的体验。

**核心挑战：**
- **外部命令依赖的隔离：** 分析文档建议使用独立的 `cmd/pipeline-worker/` 二进制文件。我完全同意——`ffmpeg`、`pdftoppm`、`libreoffice` 和 `clamd` 是重型依赖项，不应该嵌入主进程。gRPC 是 worker 和主服务之间的正确协议。
- **流式病毒扫描：** 分析文档建议使用 clamd `INSTREAM` 命令，而不是先下载完整文件。aero-id 的 `storage.Reader(ctx, key)` 方法支持流式传输到 clamd TCP 连接。这是正确的做法——在内存受限的环境中，为 2GB 文件缓冲 RAM 是不可行的。
- **预签名直接上传冲突：** 当文件直接进入 S3 时（通过 `PresignUpload` + `CompleteUpload`），aero-id 从未看到文件字节。缩略图生成需要**再次从 S3 下载文件**。分析文档建议对 <10MB 的文件进行同步处理——这是一个合理的优化，以避免额外的 S3 往返。

**对现有系统的影响：** 中等。新建 `cmd/pipeline-worker/`。为 `FileStorage` 添加 `Reader(ctx, key)` 方法（已存在）。在 `CompleteUpload` 路径中为小型文件添加可选的同步处理。病毒扫描结果存储在新的 `file_scans` 表中。

---

### 方向 6（补充）：Webhook / 出站事件通知

**为什么：** SSE 仅适用于活跃连接。对于服务器到服务器的集成（CI/CD 触发、聊天机器人、外部审核工具），需要可配置的 webhook 订阅。分析文档说得很对："Webhook 的方向与 5 个方向正交，并且是其余 5 个方向的外部集成出口。"

**验证：** 我已经确认 `internal/outbox/webhook.go` **已经**存在——它是一个将出站事件作为 HMAC 签名的 JSON POST 消息传递到配置的 URL 的 handler。但这是**整体配置**，而不是用户可管理的订阅。缺少的是：

- `webhook_subscriptions` 表（每个订阅指定 URL、事件类型白名单、HMAC 密钥、重试策略）
- 用于管理订阅的 CRUD 端点
- 一个 `WebhookRouter`，将出站事件与活跃订阅匹配并按配置调度传递

**影响：** 低。复用现有的 `WebhookHandler`，但使其成为事件匹配策略下的订阅感知。

---

## 3. 接口设计建议

### 3.1 关键迭代点

**1. NotificationDispatcher → ActivityBroadcaster。** 当前的 dispatcher 可以处理通知，但不能处理常规活动事件。考虑通过事件类型区分：

```
interface ActivityHandler {
    HandleActivity(ctx, event) error  // 用于活动流（面向用户的事件）
    HandleNotification(ctx, event) error  // 用于通知（警报/推送）
}
```

当前的 dispatcher 已经有 `notificationFor()` 将事件类型映射到通知。活动流 handler 会为**所有**事件类型做同样的事，但会写入一个 `activity_events` 表（或视图）而不是 `notifications`。

**2. ChannelService 接口。** 频道不需要自己的写入表——它们复用了 `messages` 表，但语义不同：

```
interface ChannelService {
    Create(ctx, name, memberUIDs) (*Channel, error)
    Join(ctx, channelID) error
    Leave(ctx, channelID) error
    Send(ctx, channelID, content) (*Message, error)
    List(ctx, page) (*ChannelList, error)
    SetLastRead(ctx, channelID, messageSeq) error
}
```

`Send` 方法通过发件箱广播 `channel.message.sent` 事件。通知调度器使用频道成员资格来确定收件人。

**3. 审核规则引擎接口。** 规则应该是可插入的，而不是硬编码的：

```
interface RuleEvaluator {
    Evaluate(ctx, event ModerationEvent) (RuleMatch, error)
}

// 使用通用表达式语言（CEL）或 govaluate 的规则
type CELRule struct {
    ID        domain.ID
    Name      string
    Condition string  // CEL 表达式，例如 "file.size > 10MB"
    Action    string  // "flag" | "hide" | "block" | "notify"
    Priority  int
}
```

### 3.2 新的抽象层

**活动折叠器。** 在 notification dispatcher 中，折叠逻辑嵌入在 `Handle()` 中。对于活动流，它值得拥有自己的层：

```
interface EventCollapser {
    ShouldCollapse(existing, incoming Event) bool
    Merge(existing, incoming Event) Event
}
```

这让不同的折叠策略（基于时间、基于计数、基于内容）作为独立的实现注入。

### 3.3 向后兼容性

**频道。** `messages.channel_id` 可作为 `NULL`，所以现有行不受影响。迁移带来了"1:1 对话作为频道"的视图。查询保持不变——`ListInbox` 检查 `to_uid`（旧路径）和 `channel_id IN (SELECT ...)`（新路径）的 OR 条件。这会产生 OR 条件（分析文档标记得正确），但迁移会一次性修复它。

**Webhook。** 当前的整体 webhook 配置（`outbox.webhook.url`）在迁移到订阅模型后仍可作为"默认 webhook"。这确保了在部署订阅 CRUD 端点之前配置了整体 webhook 的现有部署不受影响。

---

## 4. 技术选型

### 4.1 新依赖项

| 依赖项 | 方向 | 理由 | 评估 |
|---------|------|-------|------|
| **CEL（通用表达式语言）** | 方向 4（规则引擎） | 用于审核规则的可热加载的安全无副作用的表达式。经过谷歌实战测试，原生支持 Go，无需反射。 | 通过文件大小 ~500KB，是 protobuf 依赖项。引入延迟可忽略。 |
| **cel-go** | 方向 4 | 用于 CEL 的 Go 运行时。 | 成熟项目，定期发布。 |
| **ffmpeg（系统包）** | 方向 5（预览） | 缩略图生成、转码、流媒体。 | 不嵌入。使用 docker 镜像。 |
| **clamd（系统包）** | 方向 5（安全） | 病毒扫描。 | 不嵌入。使用 docker 镜像。使用 `INSTREAM` 命令。 |
| **pdftoppm / libreoffice（系统包）** | 方向 5（文档预览） | 将文档转换为图像用于预览。 | 不嵌入。在 pipeline-worker 容器中运行。 |

### 4.2 自建 vs 采购的决策

| 能力 | 决策 | 理由 |
|------|------|------|
| 审核规则引擎 | **自建**（CEL 之上的薄层） | 域特定（文件大小限制、内容模式、用户信誉）。现成的解决方案（例如，全面的审核平台）对于这个用例来说过于重且集成成本高。CEL 提供了正确的表达能力和安全保证。 |
| 内容处理管道 | **采购**（FFmpeg + ClamAV + LibreOffice） | 不需要定制。这些都是成熟的、经过实战测试的工具。将它们作为 sidecar 进程运行（而不是库内嵌）是正确的方式。 |
| 出站 webhook | **自建** | 缺少的是一个订阅路由层，位于现有的 `WebhookHandler` 之上。无第三方依赖。 |
| 活动流 | **自建** | 纯 SQL（4 表 UNION ALL + 视图）。无第三方依赖。 |

### 4.3 技术禁忌清单

- **没有 Redis。** 在需要分布式限流或缓存之前，这会增加不必要的运营复杂性。Per-instance 限流和内存缓存对于此规模的单实例部署来说已经足够。当达到 Redis 门槛时，**那时**再添加，不要提前添加。
- **没有 Kafka / 消息队列。** 发件箱模式 + PostgreSQL 的 `LISTEN/NOTIFY` 可以处理预期的吞吐量。外部消息队列是一个过早的优化点。
- **没有外部搜索引擎。** PostgreSQL FTS（tsvector + GIN）用于初始部署。CJK 回退（ILIKE）确实有效。在索引数亿行之前不需要 Elasticsearch。
- **没有 GraphQL。** REST + gRPC（dual-transport）提供了所有需要的。GraphQL 会增加另一种解析/验证路径，而不会提升可用性。

---

## 5. 实施路线图

### 阶段 1：基础强化（2 周）

| 优先级 | 项目 | 工作量 |
|--------|------|--------|
| **P0** | 方向 2——统一活动流 | 5 天 |
| **P0** | 修复 SSO 缓存 maxSize 驱逐（TokenCache + UserCache） | 1 天 |
| **P0** | 将 `searchTimeout` 从硬编码常量移到配置 | 0.5 天 |
| **P1** | 在文件上传和删除路径中添加审计日志 | 1 天 |
| **P1** | 修复 UserCache 中 `GetMany` 高并发冲击竞态条件 | 1 天 |
| **P1** | 修复 relay 优雅关闭（关闭前最终 drain） | 1 天 |
| **P2** | 在 `metrics` 中为预签名 URL 添加路径 sanitizer | 0.5 天 |

**里程碑：** 活动流 + 附录发现已修复。

### 阶段 2：群组频道 + 配额（6 周）

| 优先级 | 项目 | 工作量 |
|--------|------|--------|
| **P0** | 频道模式 + 迁移（`channel_members` 表） | 3 天 |
| **P0** | 频道 CRUD 操作（服务 + handler + 仓库） | 5 天 |
| **P0** | 频道消息（`messages.channel_id` + 收件箱查询） | 5 天 |
| **P1** | `last_read_seq` 逻辑时钟（代替 `last_read_at`） | 3 天 |
| **P1** | 引用完整性 + 成员资格管理 | 2 天 |
| **P2** | `anonymize_on_leave`（用于遵守 GDPR 删除权） | 1 天 |
| | | |
| **P1** | `storage_quota` 表 + 上传前事务性配额检查 | 3 天 |
| **P1** | `used_bytes` 原子递增/递减 | 1 天 |
| **P2** | 默认保留策略（超期自动软删除） | 2 天 |

**里程碑：** 群组聊天功能 + 针对上传的配额执行。

### 阶段 3：审核 + 预览 + Webhook（10 周——并行开发）

| 轨道 A（审核） | 工作量 |
|----------------|--------|
| 审核员角色 + `moderation_queue` 表 | 3 天 |
| AutoModerator + CEL 规则引擎 | 5 天 |
| 审核员匿名化（化名选择器） | 1 天 |
| SLA 仪表板（`moderation_resolution_time_seconds`） | 2 天 |
| 二阶段审核工作流（首次 + 申诉） | 3 天 |

| 轨道 B（预览） | 工作量 |
|----------------|--------|
| `cmd/pipeline-worker/` 二进制文件 + gRPC 协议 | 5 天 |
| 缩略图生成（`ffmpeg` + `pdftoppm`） | 5 天 |
| 流式病毒扫描（cland `INSTREAM`） | 3 天 |
| 文档预览（通过 LibreOffice 转换） | 3 天 |
| 小文件处理优化（<10MB 同步） | 1 天 |

| 轨道 C（Webhook） | 工作量 |
|-------------------|--------|
| `webhook_subscriptions` 表 + CRUD 端点 | 2 天 |
| `WebhookRouter`（将出站事件与订阅匹配） | 3 天 |
| 订阅管理 HTTP/gRPC handler | 2 天 |

**里程碑：** 审核工作台 + 文件预览 + 开发者 webhook——全部交付。

### 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|------|----------|--------|------------|
| 群组频道迁移中的数据损坏（`000022_migrate_messages_to_channels.up.sql`） | 低 | 高 | 分阶段迁移：先创建频道表，然后将旧对话批量迁移到频道，在过渡期将旧查询保留为回退。在暂存环境中使用生产数据量进行测试。 |
| CEL 规则引擎中的无限递归（AutoModerator 规则触发其他规则） | 中 | 中 | 最大执行深度（10 级）。检测到超过阈值时触发断路器。 |
| S3 预签名直接上传 + 病毒扫描竞争条件（用户在扫描前下载了文件） | 中 | 中 | 阻止在扫描完成前提供文件下载。在 `CompleteUpload` 中返回 `file.under_scan` 状态。一旦扫描完成通过 SSE 推送状态更新。 |
| 活动流 UNION ALL 查询性能问题，在数据库写负载下表现不佳 | 中 | 中 | 使用 PostgreSQL 物化视图（`WITH NO DATA` + `REFRESH MATERIALIZED VIEW CONCURRENTLY`）针对高容量用户。添加 `(created_at DESC)` 索引到所有 4 张表。 |
| 审核 SLA 缺失会导致用户体验下降 | 中 | 低 | SLA 仪表板 + 针对违规的告警。自动上报在一段时间后未处理的审核。 |
| GDPR 合规性问题（删除权 + 数据处理记录）与频道关联数据冲突 | 中 | 高 | `anonymize_on_leave` 必须默认开启。审计日志在配置的期限内保留并自动过期。在仪表板中提供数据删除报告。 |

---

## 总结

代码库在架构上是干净的，在领域设计上是成熟的，并且已经实现了大多数预期的生产就绪项目。分析文档中的 5 个方向是有充分依据的，优先级排序也很合理。我同意以下调整：

1. **方向 2 最先**——零模式变更，为其他所有方向提供入口，修复了该过程中附录中发现的问题。
2. **阶段 2 将方向 1 与方向 3 的配额子集合并**——产品增长（频道）和运营必需品（配额）在大致相同的时间范围内解决。
3. **方向 4 和方向 5 可以并行进行**——它们是独立的领域。
4. **Webhook（方向 6）在阶段 3 与审核和预览并行交付**——它是他们所有人的外部集成出口。

实际代码验证确认了分析文档中的大部分发现，纠正了 3 个勘误（最重要的是 `like_count` 原子性——确实安全），并确认了 UserCache 锁定竞态条件的额外风险。
