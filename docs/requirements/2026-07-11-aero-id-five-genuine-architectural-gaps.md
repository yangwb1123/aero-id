# aero-id — 五处被低估的架构缺口

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 逐文件全量代码扫描（`internal/` 全部 23+ 子包、`migrations/` 全部 22 份 SQL、`configs/`、`api/proto/`、`Makefile`），对照 composition root（`internal/app/app.go`）的实际 wiring 验证。与 `docs/requirements/` 中现存 90+ 份分析文档交叉比对，确保以下 5 个方向**不是已有文档的简单重述**，而是从代码实际状态中提取出的、被先前分析系统性低估或遗漏的高价值方向。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据（文件路径 + 行号级引用）和双视角论证。

---

## 当前代码库成熟度快照

| 维度 | 已交付能力 |
|------|-----------|
| **传输** | HTTP (Gin) + gRPC 全对等 · SSE 实时推送 · 健康/就绪探测 + gRPC health |
| **功能** | 文件（CRUD/去重/分享/预签名直传/元数据修改）· 消息（1:1/收件箱/对话/已读回执/反应/编辑/软删）· 评论（两层嵌套/点赞/举报/审核/编辑/软删）· 通知（outbox 驱动/折叠/偏好/静默时段/实时推送）· 搜索（FTS + ILIKE CJK 回退）· 用户阻止 · 管理（角色/权限/审计日志查询） |
| **基础设施** | 事务性 Outbox + 重试/退避/死信 · 幂等键（DB 持久化 + 自动清理）· SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning · OpenTelemetry 追踪 · RED 指标 · Token Bucket 限流 · 冷启动 warmup · 引用计数的存储 GC（含孤儿 blob 清扫） |
| **存储** | Local / S3（MinIO 兼容）· 内容去重 · 引用计数 · MIME/大小校验 · 预签名直传 |
| **数据** | 22 次可逆迁移 · UUIDv7 主键 · 软删除 · cursor-based 分页 · GORM |
| **部署** | Docker / docker-compose · 优雅关停 · 可选的 auto-migrate · 23 项配置覆盖所有 feature flag |

**结论**: aero-id 已经是一个功能完善、基础设施扎实的后端服务。以下不是基础功能缺失，而是**架构级的设计缺口**——它们在单实例开发/演示中不会暴露，但在生产规模、多实例部署、数据治理合规场景下会成为关键瓶颈。

---

## 方向一：跨实体引用完整性 & 孤儿数据生命周期

### 问题陈述

aero-id 的实体之间通过多态外键和可选引用相连，但**没有任何机制维护这些跨实体引用的完整性**。三个具体场景：

1. **Comment → 目标实体（多态）**: 评论通过 `target_type` (`"file"` / `"message"`) + `target_id` 引用其所属的实体。`assertTargetReadable` 在创建时验证目标存在且可读，但**创建之后**，如果目标文件/消息被软删除，评论成为"悬空引用"——GORM 的软删作用域让 `Get` 返回 `ErrNotFound`，但评论记录仍然存在且指向一个不可见的实体。不存在任何一致性检查或清理机制。

2. **Message → File 附件**: 消息通过 `file_id`（可选外键）引用文件。服务层在发送消息时验证文件存在（`s.files.Get(ctx, *in.FileID)`），但文件被删除后，消息的 `file_id` 指向一个已软删的记录。当前消息没有任何字段来冗余存储附件元信息快照（文件名、大小等）以应对文件删除后的展示需求——设计文档中提到了这个意图（`meta` 内冗余存原文件名/size 快照），但**代码中没有实现**。

3. **反向引用查询不存在**: 没有任何 API 或服务方法可以回答"哪些实体引用了这个文件/消息"——即查看一个文件被哪些评论提及、被哪些消息附件引用。这对删除前的"引用预览"、合规审计、数据清理前的影响分析都是基本需求。

### 代码级证据

**证据 1: `assertTargetReadable` 只检查创建时刻**

`internal/service/comment.go:60-79`:
```go
func (s *commentService) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
    switch t {
    case domain.TargetTypeFile:
        f, err := s.files.Get(ctx, target)
        if stderrors.Is(err, repository.ErrNotFound) {
            return apperr.NotFound("comment.target_not_found", "target file not found")
        }
        ...
    }
}
```
这个方法只在 `Create` 路径调用。删除文件/消息后，已有的评论不会收到任何通知或级联操作。

**证据 2: Message 附件无快照字段**

`internal/domain/message.go`（完整文件）:
```go
type Message struct {
    ...
    FileID    *ID              // 指向 files 表的外键
    Meta      map[string]any   // 声称用于冗余存储，但当前没写任何附件快照
    ...
}
```
对比设计文档第 3.3 节的声明：
> 附件删除策略：`messages.file_id` 不级联——文件被 owner 删除后，消息保留（`meta` 内会冗余存原文件名/size 快照）

但 `internal/service/message.go:65-99` 的 `Send` 方法并没有往 `Meta` 中写入附件元信息：
```go
if in.FileID != nil {
    f, err := s.files.Get(ctx, *in.FileID)
    if err != nil {
        return nil, apperr.NotFound("message.file_not_found", "attached file not found")
    }
    // ... 没有 meta["original_name"] = f.Name, meta["original_size"] = f.Size ...
}
```

**证据 3: 没有反向引用查询**

检查所有 `repository` 接口和 `service` 接口——没有任何方法签名包含 "references"、"referenced_by"、"where_used" 或类似的语义。唯一的反向引用路径是通过 `ListByTarget`（`internal/repository/comment.go:23`），但这限于已知 target_type + target_id 的情况——没有通用的"查找所有引用此文件的实体"方法。

### 架构师视角

这是**数据完整性债务**。今天项目规模小不会出问题，但随着时间推移，软删除的记录积累、用户删除操作增加，悬空引用会扩散。对于任何需要经过合规审计（SOC2、ISO 27001）的系统，引用完整性是一项基本要求。更直接的问题是：当文件被删除后，引用它的消息界面会展示一个"无内容"的附件占位符，用户会看到断裂的引用。

### 产品经理视角

用户场景：Alice 给 Bob 发了一条带附件的消息。三个月后 Alice 删除了那个文件。Bob 打开旧消息看到附件区空白——他不知道原本是什么文件，会认为系统出了 bug。这是一个**信任损伤**。修复方案不是禁止删除（用户有权删除自己的文件），而是在消息中保留附件的元信息快照。

另一个场景：管理员准备删除一个违规文件，但不确定会影响到多少用户——聊天消息里引用了这个文件，评论区也引用了它。没有任何"删除预览"功能告诉管理员级联影响的范围。

### 建议方向

- 消息发送时，将 `FileID` 引用的文件元信息（name, size, mime_type）快照写入 `Meta`
- 增加 `ReferencedBy` 查询接口（给定文件/消息 id，返回引用它的评论和消息列表）
- 删除文件/消息时可选地执行"引用影响检查"（返回引用计数 + 预览）或级联清理/标记
- 定期后台扫描检测悬空引用（`comments.target_id` 指向已软删实体）

---

## 方向二：死信队列（DLQ）可观测性 & 管理面

### 问题陈述

事务性 outbox 已经实现了死信机制：事件超过 `maxRetries` 次失败后 `dead_letter` 置为 `true`，relay 不再拾取。但**整个 outbox 系统缺乏给运维人员的管理面**：

1. **无查询接口**: 无法列出、按事件类型筛选、或搜索死信事件。
2. **无重放能力**: 死信事件一旦标记就永久搁置，没有任何 API 可以重放某个或某批死信。
3. **无丢弃能力**: 运维人员确认某个死信不需再处理后，无法将其标记为已处理/已清除。
4. **无预警**: 没有在死信数量增长时触发告警的机制（当前只有 `/metrics` 上的 pending 和 lag 指标，但不区分死信 vs 等待重试）。
5. **每事件类型无差异化策略**: `maxRetries` 是全局配置（`outbox.max_retries`），无法为 `message.sent` 和 `comment.replied` 设置不同的重试次数或退避策略。

### 代码级证据

**证据 1: FetchUnpublished 显式排除死信**

`internal/outbox/repository.go:72-73`:
```go
Where("published_at IS NULL AND NOT dead_letter").
```
死信事件一旦标记，永久不可见——没有 `FetchDeadLetters` 或类似方法。

**证据 2: MarkFailed 将死信标记为不可逆状态**

`internal/outbox/repository.go:107-108`:
```go
if retryCount >= maxRetries {
    updates["dead_letter"] = true
}
```
标记 `dead_letter = true` 后，没有任何 `UnmarkDeadLetter` 或 `ResetEvent` 方法。

**证据 3: AdminRepository 和 AdminHandler 只暴露审计日志**

`internal/repository/admin.go` 和 `internal/handler/http/admin.go` 中只有 `HasPermission`、`ListAudit`、`GrantRole`、`RevokeRole`——没有任何 outbox/DLQ 的查询或操作方法。

**证据 4: 指标系统无死信专属指标**

`internal/pkg/metrics/metrics.go` 中有 `ObserveOutboxPublished`、`ObserveOutboxFailed`、`ObserveOutboxDrain`，但没有 `ObserveOutboxDeadLettered` 或 `GaugeOutboxDeadLetterCount`。运维人员无法从 `/metrics` 发现死信积累。

### 架构师视角

死信是"沉默的错误"。没有 DLQ 管理面意味着：
- 业务逻辑 bug 导致的事件死信（例如 `comment.replied` 反序列化持续失败）会永久丢失，运维不知道
- SSO 短暂故障期间批量死信的事件无法批量恢复
- on-call 工程师收到告警后没有自助工具查看/重放死信，只能直接操作数据库

一个健康的 outbox 模式实现应该包含 `ListDeadLetters`、`ReplayDeadLetter`、`DiscardDeadLetter` 这些运维 API，以及死信数量的监控指标。

### 产品经理视角

对最终用户不可见，但直接影响**数据交付可靠性**。如果通知事件持续进入死信，用户收不到"新消息"或"评论回复"通知。从用户视角看，系统"偶尔漏通知"——这是最难以排查的体验问题之一。DLQ 管理面直接服务于 SRE/运维团队的事故响应效率。

### 建议方向

- 增加 `GET /v1/admin/outbox/dead-letters`（列表 + 按 event_type 筛选 + 分页）
- `POST /v1/admin/outbox/dead-letters/:id/replay`（重放单个死信）
- `POST /v1/admin/outbox/dead-letters/:id/discard`（确认丢弃）
- `POST /v1/admin/outbox/dead-letters/replay-all`（批量重放）
- 指标系统增加 `outbox_dead_letter_total` counter 和 `outbox_dead_letter_count` gauge
- 配置支持 per-event-type `max_retries` 覆盖

---

## 方向三：并发访问控制 & 丢失更新防护

### 问题陈述

尽管 aero-id 在写路径上实现了事务性 outbox 和幂等键，**单个实体的更新操作没有任何并发控制**。三个具体风险：

1. **文件元数据 PATCH 丢失更新**: `UpdateFileMetadataInput` 采用字段掩码风格（nil 字段不变），但 GET → 修改 → PATCH 这个"读取-修改-写入"周期在多并发下必然出现丢失更新：两个协程同时 GET 到同一文件，各自修改不同字段然后先后 PATCH，后完成的覆盖先完成的。

2. **计数器并发（like_count / reply_count）**: 评论的点赞和回复计数是通过独立的 `IncLikeCount` / `IncReplyCount` 更新操作实现的。虽在事务中执行，但它们**基于当前值而非快照**做增量（`UPDATE comments SET like_count = like_count + 1`），这在 MySQL/PostgreSQL 层面是原子的，但如果上层业务逻辑需要条件判断（如"只能取消点赞自己的点赞"），并发下可能出问题。更隐蔽的问题是：`UpdateMetadata` 使用的 `map[string]any` 覆盖策略与其他并发写操作可能冲突。

3. **无乐观锁/版本号**: 没有任何实体定义 `version` 或 `updated_at` 用于乐观并发控制（OCC）。HTTP 端也完全缺失 `If-Match` / `If-None-Match` 头支持。这意味着客户端永远无法安全地实现"编辑并保存"模式——用户编辑一条消息/评论时，后台可能已被别的操作修改，用户无法知晓。

### 代码级证据

**证据 1: UpdateMetadata 使用无条件覆盖**

`internal/service/file.go`（`UpdateMetadata` 方法，约第 190-220 行）:
```go
// 将 in.Name, in.Visibility, in.Meta 映射到 fields map，
// 然后调用 files.UpdateMetadata(ctx, id, fields)
```
`internal/repository/file.go` 中的 `UpdateMetadata` 执行:
```go
func (r *fileRepo) UpdateMetadata(ctx context.Context, id domain.ID, fields map[string]any) error {
    return r.db.WithContext(ctx).Model(&fileRow{}).
        Where("id = ?", id).
        Updates(fields).Error
}
```
没有任何 `WHERE version = ?` 或 `WHERE updated_at = ?` 条件，最后一次写无条件覆盖前面的写。

**证据 2: 所有实体无版本字段**

全代码库搜索 `version`（排除迁移版本、S3 ETag、metrics content-type）：
```bash
# 结果: 没有任何业务实体定义乐观锁版本字段
```
`internal/repository/db.go` 中定义的 `fileRow`（第 25-45 行）、`messageRow`（第 80-105 行）、`commentRow`（第 130-165 行）均没有 `Version` 字段。

**证据 3: HTTP PATCH 无条件请求头支持**

`internal/handler/http/file.go` 中 `UpdateMetadata` 的 handler（约第 120-140 行）:
```go
// 没有检查 If-Match / If-None-Match
// 没有解析 ETag / 版本号
```
Gin 的 router 注册也没有任何 ETag 中间件。

**证据 4: commentLike 的唯一约束是唯一并发保护**

`internal/repository/db.go` 中的 `commentLikeRow`（约第 175 行）有 `(comment_id, owner_uid)` 唯一约束，但这只防止重复点赞，不保护 `like_count` 在并发 INC 时的业务一致性。

### 架构师视角

丢失更新是一类经典的并发 bug，在单实例测试中很难触发，但在生产流量下必然出现。对于 aero-id 的核心使用场景——用户编辑自己的文件元数据、编辑消息/评论内容——丢失更新意味着用户认为保存成功的数据被静默覆盖。这比"保存失败"更糟糕（用户会重试），因为用户以为操作成功了。

标准的防御手段是**乐观并发控制（OCC）**：每个实体加增量的 `version` 字段，更新时 `WHERE version = ?` 检查，版本不匹配时返回 409 Conflict + 当前最新版本。客户端可以重新读取然后重试。

### 产品经理视角

用户场景：Alice 在 Web 端和手机端同时打开了同一个文件的元信息编辑页。她在 Web 端把文件从"私有"改为"公开"并保存成功。半分钟后，她在手机端修改文件名并保存——结果文件的可见性被改回了"私有"（因为手机端保存时提交的是它读取时的旧可见性）。Alice 以为文件是公开的，但实际是私有的。这是一个数据静默损坏问题。

丢失更新对**协作场景**尤为致命。现在 aero-id 只有单用户操作自己的数据，但未来如果引入文件协作编辑、共享消息草稿等能力，丢失更新的危害会指数级放大。

### 建议方向

- 为 `files`、`messages`、`comments` 添加 `version` 列（单调递增整数）
- 所有 `Update` 操作添加 `WHERE version = ?` 条件
- HTTP 返回 `ETag` 头（`"<version>"`），接受 `If-Match` 条件头
- 版本冲突时返回 `409 Conflict` + 当前最新实体
- gRPC 同理：在响应中返回 `version`，在更新请求中要求 `expected_version`

---

## 方向四：多实例部署架构缺口

### 问题陈述

aero-id 的设计和实现 **隐含地假设单实例部署**。多个关键组件使用进程内内存状态，在多副本/水平扩展部署中会出现功能正确性问题：

1. **限流器是 per-instance 的**: `ratelimit.Limiter` 在进程内维护 token bucket map。如果部署 N 个副本，每个副本的限流器独立运行，用户实际可达到的请求速率是配置值的 N 倍。更严重的是，有经验的攻击者可以通过轮换 IP 或使用多个 token 来轻易绕过限流。

2. **SSO Token/User 缓存是 per-instance 的**: `TokenCache` 和 `UserCache` 使用 `sync.Map` + 后台 goroutine 清扫。每个实例单独缓存，冷启动时每个实例独立穿透到 SSO。部署扩容时，新实例的缓存完全空白，立即产生 SSO 流量尖峰。

3. **SSE Hub 是 per-instance 的**: `hub.Hub` 维护进程内的 subscriber 映射。如果用户连接到实例 A 订阅 SSE，但触发通知的事件被 outbox relay 在实例 B 上处理，实例 A 上的用户不会收到该通知——因为事件发布只发生在处理事件的这个实例上。解决方案要么是 sticky session（引入运维复杂性），要么是共享事件总线（如 Redis Pub/Sub）。

4. **幂等键存储是中心化的（DB），但这是好事**: 这一点已经正确——`idempotency_keys` 表在 PostgreSQL 中，所有实例共享，是正确设计。

### 代码级证据

**证据 1: 文档明确声明限流器是 per-instance**

`internal/pkg/ratelimit/ratelimit.go:4-7`:
```go
// The limiter is PER-INSTANCE: each running aero-id process keeps its own
// buckets in memory. Distributed/global rate limiting would need shared state
// (e.g. Redis) and is intentionally out of scope here.
```

**证据 2: SSO 缓存没有外部存储**

`internal/sso/token_cache.go` 和 `internal/sso/user_cache.go` 的实现：
- 两者都使用 `sync.Map` 存储
- `RunCleaner` 使用进程内 goroutine
- 没有 `Load` / `Store` 到外部存储的路径

**证据 3: SSE Hub 没有跨实例桥接**

`internal/hub/hub.go:28-32`:
```go
type Hub struct {
    subs   sync.Map // uid → *uidSubs
    nextID atomic.Int64
    closed atomic.Bool
}
```
发布事件只在调用 `pub` 的实例上可见。

**证据 4: outbox relay 在每个实例上独立运行**

`internal/app/app.go:150-153`:
```go
relay := outbox.NewRelay(outboxRepo, relayHandler, cfg.Outbox.MaxRetries, log, metricsReg)
stopRelay := relay.Start(pollInterval)
```
每个实例都运行自己的 relay，都从 DB 拉取同一批未发布事件。这导致**重复处理**——多个实例的 relay 可能同时获取同一批事件（因为 `FetchUnpublished` 没有实例级别的租约/锁定）。PostgreSQL 的行锁 (`SELECT ... FOR UPDATE SKIP LOCKED`) 可以防止重复处理，但当前实现只是 `SELECT ... WHERE`——没有任何锁机制。

**证据 5: FetchUnpublished 没有锁**

`internal/outbox/repository.go:71-74`:
```go
func (r *gormRepo) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    var rows []outboxEventRow
    err := r.db.WithContext(ctx).
        Where("published_at IS NULL AND NOT dead_letter").
        Order("created_at ASC").Limit(limit).Find(&rows).Error
```
没有 `FOR UPDATE SKIP LOCKED`，多个 relay 实例会挣抢同一批事件。

### 架构师视角

这五个问题合在一起意味着 aero-id **目前不能安全地水平扩展**。多实例部署要么导致功能错误（SSE 通知丢失、限流失效），要么导致数据处理错误（outbox 重复处理、幂等键重复占用）。对于要上生产的服务，这可能是最关键的单点限制。

修复路径：
- Outbox relay: `FOR UPDATE SKIP LOCKED` 或 leader election（一次只有一个实例运行 relay）
- 限流器: 可选的外部存储后端（Redis）或"尽力而为"的 per-instance 限流（接受 N× 偏差）
- SSE: 共享事件总线适配器（Redis Pub/Sub / NATS），或者 sticky session + 负载均衡器配置
- 缓存: 可选的外部缓存后端，或接受 per-instance 缓存的额外 SSO 开销

注意：不是所有场景都需要分布式版本。上述建议应该**可选地**引入，保持单实例部署的简单性。

### 产品经理视角

对用户直接影响：当系统部署多实例时，SSE 实时通知可能随机丢失（用户连接到实例 A，事件在实例 B 处理）。限流器失效意味着恶意用户更容易实施 DoS。Outbox 重复处理意味着用户可能收到**重复的通知**（同一事件被处理两次，产生两条通知记录）。

对于内部工具平台（aero-id 是对内服务），扩缩容通常是响应用户增长或流量峰值的关键操作。一个不能安全扩缩容的服务会在业务增长时成为瓶颈。

### 建议方向

- Outbox relay: 改用 `SELECT ... FOR UPDATE SKIP LOCKED` 防止多实例竞争；或引入 lease 机制（一次只有一个 leader 运行 relay）
- 限流器: 保留 per-instance 作为默认（简单），添加可选 Redis 后端实现
- SSE Hub: 添加可选的事件总线适配器接口，支持 Redis Pub/Sub 作为跨实例桥接
- 缓存: 添加可选的外部缓存接口（Redis）
- 每个组件保持"本地优先 + 可选分布式"的设计原则

---

## 方向五：数据可移植性 & 用户数据全量删除

### 问题陈述

aero-id 不拥有账户主数据（SSO 是 identity provider），但它持有与账户关联的几乎所有业务数据——文件、消息、评论、通知、偏好设置、阻止列表。当前**没有任何机制让用户批量导出或删除自己的所有数据**。

1. **无数据导出**: 没有端点可以一次性导出用户的所有文件内容、消息历史、评论记录、通知历史。对于 GDPR 第 20 条规定的**数据可移植权**（right to data portability），这是硬性要求。即使不考虑 GDPR，用户迁移到其他平台时也需要这个能力。

2. **无全量数据删除**: 当用户决定离开系统（或 SSO 侧账户被删除）时，aero-id 没有"删除用户所有数据"的端点。目前的删除操作是逐实体进行的：删除每个文件、每条消息、每条评论都不在同一次请求中完成。如果 SSO 通知 aero-id "用户已注销"，aero-id 无法通过一个原子操作清理该用户的所有数据。

3. **软删除不等于真正删除**: 所有的删除都是软删除（`deleted_at` 设置时间戳）。GC 扫表只处理文件的 blob 数据和文件行（且是 opt-in 的），**消息、评论、通知、阻止记录、审计日志等一旦软删除就永久保留**——没有任何机制可以真正物理删除它们。

4. **无审计跟踪**: 当用户请求数据删除时，没有记录"谁在何时请求了数据删除"、"哪些数据被删除了"的审计事件。

### 代码级证据

**证据 1: 没有任何批量导出的接口**

全代码库搜索 `export`、`portability`、`bulk.*download`、`archive`——没有任何匹配的业务代码。所有列表 API 都是分页的、面向浏览的，没有适合批量导出的流式接口。

**证据 2: 没有任何"按用户删除"的批量操作**

搜索所有 `Delete` 方法——它们都是单个实体删除：
- `internal/service/file.go`: `Delete(ctx, id domain.ID)` — 单个文件
- `internal/service/message.go`: `Delete(ctx, id domain.ID)` — 单条消息
- `internal/service/comment.go`: `Delete(ctx, id domain.ID)` — 单条评论

没有任何 `DeleteAllByOwner(ctx, ownerUID)` 或 `DeleteAllByParticipant(ctx, uid)` 方法。

**证据 3: 软删除的实体永久保留**

`migrations/` 中所有 `.up.sql` 都只定义了 `deleted_at` 列，没有任何额外的清理策略。GC sweep 只处理文件：
- `internal/storage/gc/sweeper.go` — 只处理 `files` 表
- 没有 `messages_gc.go`、`comments_gc.go`、`notifications_gc.go`

**证据 4: 审计日志记录了细粒度操作但没有数据删除事件**

`internal/domain/audit.go` 中的 `Audit*` 常量列表：
```go
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"
)
```
没有 `AuditDataExport`、`AuditAccountDataDelete`、`AuditBulkDelete`。

### 架构师视角

从合规角度来看，GDPR 第 17 条（被遗忘权/删除权）和 第 20 条（数据可移植权）是硬性要求，适用于**任何处理欧盟用户数据**的系统。即使当前业务不面向欧盟用户，这类合规需求通常被放在"未来再说"，但在架构设计阶段预留扩展点远比事后修补代价小。

技术上，全量用户数据删除需要小心处理：
- 文件需要先从存储后端删除，再删除数据库记录（类似当前单文件删除的流程，但批量执行）
- 消息涉及两个参与者——删除一个参与者的消息副本不应该影响另一个参与者看到历史消息（这就是为什么当前使用软删除——`deleted_at` 只屏蔽发送方视角）
- 评论涉及多人——删除用户自己的评论不影响其他用户的阅读（已删除的评论显示"[已删除]"即可）
- 审计日志可能需要在删除后保留（合规需求）但匿名化

### 产品经理视角

用户场景：Alice 决定离开平台。她在 SSO 侧删除了账户。SSO 可以通知 aero-id（通过 webhook 或定时 reconciliation），但 aero-id 目前无法响应这个通知——Alice 的文件、消息、评论仍然存在。这是**数据残留**问题。

另一个场景：Alice 想把自己的聊天记录迁移到另一个平台。她希望下载自己和 Bob 之间所有消息的历史存档（JSON 或 CSV 格式）。目前没有这样的功能。

数据可移植性不仅是合规要求，也是**用户信任**的信号——"你的数据是你的，随时可以带走"。对于面向开发者的工具平台（如 aero-id 的设计定位），这是一个重要的产品差异化能力。

### 建议方向

- 增加 `POST /v1/account/export` 端点：异步生成包含用户所有数据的归档文件（文件、消息、评论、通知、偏好设置、阻止列表）
- 增加 `DELETE /v1/account/data` 端点：全量删除用户数据（结合 SSO webhook 通知触发）
- 引入 `account_deletion_requests` 表记录删除请求的审计轨迹
- 为消息、评论、通知、偏好设置等实体添加物理清理策略（超过保留期的软删除记录可被批量硬删除）
- 所有软删除实体定义统一的 `PhysicalPurge` 接口，方便后台定时任务统一清理

---

## 汇总对比表

| 方向 | 架构影响 | 产品影响 | 实施风险 | 当前被先前分析覆盖程度 |
|------|---------|---------|---------|---------------------|
| **1. 跨实体引用完整性** | 数据正确性 · 架构完整性 | 用户可见的数据断裂 · 信任损伤 | 低-中（新查询 + 元信息快照） | 极少提及 |
| **2. DLQ 管理面** | 运维健康 · 数据交付可靠性 | 间接（通知可靠性） | 低（新 API + 指标） | 未作为独立方向讨论 |
| **3. 并发访问控制** | 数据一致性 · API 成熟度 | 静默数据损坏 · 协作基础 | 中（版本字段 + OCC 逻辑） | 极少提及 |
| **4. 多实例架构** | 水平扩展能力 · 部署拓扑 | 通知丢失 · 限流失效 · 重复处理 | 高（基础架构改造） | 仅个别提及分布式限流 |
| **5. 数据可移植性 & 全量删除** | 合规架构 · 数据生命周期 | GDPR 合规 · 用户信任 · 平台锁定 | 中（新端点 + 批量操作） | 未作为独立方向讨论 |

### 实施优先级建议

面向**单实例生产**的最优排序：
1. **方向三（并发控制）** — 影响数据正确性，改造成本低，建议立即实施
2. **方向二（DLQ 管理）** — 运维必须，改造成本低
3. **方向一（引用完整性）** — 增量改进，可以在功能开发中逐步解决
4. **方向五（数据可移植性）** — 合规需要，但可以规划在下一迭代
5. **方向四（多实例架构）** — 高影响但高成本，建议在确认需要水平扩展时再启动

---

*本文件基于 2026-07-11 代码库主分支（`main`）的全量扫描。所有代码引用均指向主线分支的当前提交，而非工作树或分支。*
