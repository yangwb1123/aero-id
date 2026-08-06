# aero-id：资深架构师视角的下一波高价值扩展方向

> 日期：2026-07-10
> 范围：全代码库扫描，涵盖 internal/{app, block, comment, file, hub, idempotency, message, middleware, notification, outbox, search, storage/gc, sso, audit, domain, repository, service, pkg/*}、migrations 共计 22 个迁移、cmd、config、部署编排。
> 前提：ROADMAP 原 5 个方向及其后生产强化波次（可观测性、SSO 韧性、文件管道、写入正确性、产品面 + 限流/运维/内容安全）已全部交付。

---

## 扫描结论

经过全面扫描，当前代码库已具备成熟度远超典型 MVP：双传输（HTTP + gRPC）对等、38 个 RPC、事务性 outbox、SSE 实时推送、全文搜索、审核日志、管理角色、存储 GC、幂等键、断路器和限流、OpenTelemetry 追踪、Prometheus 指标。原设计文档和 ROADMAP 中列出的所有方向均已实现。

本文件列出的是 **代码中不存在、过往文档未覆盖、但对生产级产品至关重要的 5 个下一波方向**。每个方向均附有代码证据说明它当前为何缺失，以及为什么需要它。

---

## 方向 1：数据生命周期管理 — 保留策略与归档

### 它是什么

建立一个按实体类型（消息、通知、评论）的 TTL 保留策略，超过期限的数据自动转入低成本归档存储（或彻底删除），并配合用户可见的配额/清理机制。

### 为什么当前缺失

代码库中唯一处理数据淘汰的组件是 `internal/storage/gc/sweeper.go`，它仅作用于 **软删除后的文件**（硬删除超过 retention 窗口的行）。对于逻辑删除前的数据，没有全局 TTL：

| 实体 | 当前行为 | 问题 |
|------|----------|------|
| 消息 (`messages`) | 永久保留，只有发送者/接收者可软删除 | 高流量用户的消息表无限增长 |
| 通知 (`notifications`) | 永久保留，只有 `MarkAllRead` 可标记已读 | 没有批量清理或自动淘汰 |
| 评论 (`comments`) | 永久保留，仅 soft-delete | 大量旧评论随产品增长堆积 |
| 文件 (`files`) | 仅软删除后可被 GC 回收 | 从未标记删除的文件永远保留 |

`internal/domain/message.go` 和 `internal/domain/notification.go` 中不包含 `ExpiresAt` 或 `TTL` 字段。迁移文件（`migrations/*.sql`）中没有 `expires_at` 列。`internal/config/config.go` 中没有 `retention` 配置块。

### 为什么需要它

1. **合规与用户期望**：GDPR、CCPA 等隐私法规要求用户能够清除数据。当前用户只能逐条软删除消息/文件，没有批量清理或自动遗忘机制。
2. **存储成本**：无限制的通知和消息累积推高 Postgres 存储，且备份时间更长。产品团队应该在用户设置中提供"自动删除 X 天前的消息"。
3. **性能退化**：通知列表查询 `WHERE recipient_uid = ? ORDER BY created_at DESC`（`internal/notification/repository.go` 中的 `ListByRecipient`）随未清理的历史通知增多，扫描深度线性增加，即使有索引。

### 边界情况

- 消息由两个参与者共享：接收者设置的 TTL 不应影响发送者视角的保留期。
- 已读 vs 未读：未读通知/消息不应在用户查看之前被硬删除；可先软删除（`status = deleted` / `read_at = nil`）再在独立 GC 中清除。
- 引用完整性：移除一条消息需要级联清理其关联的通知、评论和 outbox 事件。

### 架构建议

在 `domain` 层增加 `ExpiresAt *time.Time` 字段。在 `config` 层增加每个实体的默认 TTL。一个后台周期性任务（类似 `storage/gc/sweeper` 的设计模式）扫描过期的逻辑行，将其转入 `soft_delete` 状态；另一个波次硬删除。通过 outbox 通知用户数据被清理。

---

## 方向 2：乐观并发控制 — ETag / 版本化写入

### 它是什么

为所有可变更资源（评论编辑、消息编辑、文件元数据更新、通知标记已读）添加版本号（`version` 列）或基于时间的乐观锁，配合 HTTP `If-Match` / `If-Unmodified-Since` 头，防止并发写入丢失更新。

### 为什么当前缺失

当前代码库全面采用 last-writer-wins：

- `internal/comment/service.go` 中的 `Edit` 方法执行 `WHERE id = ?` 的简单 `UPDATE`，不检查 `updated_at` 是否匹配客户端已知的值。
- `internal/message/service.go` 中的 `Edit` 方法同样直接更新内容，无版本校验。
- `internal/file/service_crud.go` 中的 `Update`（PATCH 文件元数据）执行无条件更新。
- `internal/pkg/errors/errors.go` 中没有 `Conflict` 错误码（409）的使用，也没有 `PreconditionFailed`（412）的使用，表明当前设计未考虑冲突检测。
- `internal/handler/http/file.go` 和 `internal/handler/http/comment.go` 中没有读取 `If-Match` 或 `If-Unmodified-Since` 头的逻辑。
- `internal/handler/grpc/comment.go` 和 `internal/handler/grpc/message.go` 的 gRPC handler 同样未传递版本信息。

在所有 domain 实体中，仅有 `Comment`、`Message` 和 `File` 具备 `UpdatedAt time.Time` 字段，但服务层从未将其作为并发守卫使用。

### 为什么需要它

1. **数据正确性**：两个窗口/客户端同时编辑同一条评论或消息，最后一个提交者静默覆盖前一个提交者的内容。在协作场景（如两个客服同时回复同一工单消息）中这会导致数据丢失且不可感知。
2. **用户体验**：无冲突提示意味着用户永远不会发现他们的编辑被覆盖，直到手动检查。
3. **API 成熟度**：REST API 的最佳实践（RFC 7232）要求条件请求。没有版本化，客户端的缓存也无法基于资源版本进行增量同步。

### 边界情况

- 首次创建时版本为 1；每次更新原子性地递增版本。
- 乐观锁失败应返回 HTTP 409（gRPC `Aborted`），让客户端重新获取最新版本后重试。
- 批量操作（如 `MarkAllRead`）不受此约束，可以跳过版本校验。
- 审核日志（`internal/audit/repository.go`）应记录冲突事件以帮助调查。

### 架构建议

在 `domain` 的实体接口中定义 `GetVersion() int` / `SetVersion(int)`。在 GORM repository 中，更新时添加 `WHERE version = ?` 条件，并检查 `RowsAffected`。如果返回 0，服务层返回 `Conflict` 错误。HTTP handler 将客户端传递的 `If-Match` 头的 ETag 解析为版本号，gRPC handler 从 request message 中提取。

---

## 方向 3：分布式速率限制与资源配额

### 它是什么

将当前基于单机内存的令牌桶速率限制替换为共享状态（Redis）后端，并为每个用户/账户增加累计资源配额（每日消息发送量、最大文件存储量、每月 API 调用量）。

### 为什么当前缺失

- `internal/pkg/ratelimit/ratelimit.go`：一个纯内存的令牌桶实现。`Limiter` 结构体持有 `sync.Map`，键值对永不过期（除非 IdleTTL 触发的清理）。这在单实例部署中没问题，但在 N 个副本的水平扩展场景下：
  - 用户的速率预算被放大 N 倍：每个实例都有自己的桶，用户可以在实例 A 上消费全部额度，在实例 B 上同样消费全部额度。
  - 限速的效果随副本数稀释，违背了限速的初衷。
- `internal/middleware/ratelimit.go` 中的 `RateLimit` 中间件完全依赖于这个内存 `Limiter`。
- `internal/config/config.go` 中的 `RateLimitConfig` 没有 `backend` 或 `redis` 字段。
- 没有任何 **累计配额**（例如"免费用户每天最多发送 100 条消息"）的实现。唯一的约束是速率（RPS），而不是体积（每天/每月的总量）。

### 为什么需要它

1. **多副本生产部署**：Kubernetes 部署通常有 3+ 个 aero-id 副本。内存速率限制器在副本数 > 1 时语义错误。
2. **滥用防护**：速率限制防止瞬时突发，但无法阻止攻击者在一天内以恒定的低速率发送大量请求直到耗尽服务资源。累计配额是防御的必需层。
3. **产品商业化**：层状定价（免费 / 专业 / 企业）基于累积使用配额而非瞬时速率。当前的限制模型无法支撑此模式。

### 边界情况

- Redis 不可用时的降级行为：窗口期内故障切换到宽松的内存限制（fail open limited），避免单点故障。
- 配额计算的一致性：使用 Lua 脚本或 `INCR` 的原子性；配额接近或超过时的响应包含 `X-RateLimit-*` 头。
- 账单 / 计费集成：配额事件应写入出箱（outbox），以便计费系统消费。

### 架构建议

引入 `internal/pkg/ratelimit/redis.go`（可选实现），通过接口模式替换当前的内存实现。在 `Limiter` 接口上抽象 `Allow(key string, tokens int) bool`。增加 `internal/resourcequota/` 包处理累计配额，支持可配置的窗口和上限。配额用完时返回 HTTP 429 或 gRPC `ResourceExhausted`。

---

## 方向 4：移动端推送通知 — FCM/APNs 集成

### 它是什么

在当前的 SSE 实时推送（`internal/hub/hub.go` + `internal/handler/http/events.go`）之外，增加对 Apple Push Notification service (APNs) 和 Firebase Cloud Messaging (FCM) 的支持，使用户在未保持 SSE 连接时（移动端后台、WebSocket 断开）仍能收到消息、评论回复和提及的通知。

### 为什么当前缺失

- 通知管道在 `internal/notification/dispatcher.go` 中设计得很好：事件从 outbox 流出，经过块检查（block check）、偏好检查（preference check）、频率限制（rate limiter）和折叠（collapse），最后写入 `notifications` 表并通过 `hub.Pusher` 推送到 SSE。
- 但 `hub.Pusher` 接口（`internal/hub/hub.go` 中的 `Publish(uid, eventType, data string)`）**仅面向内存中的 SSE 订阅者**：如果用户的设备未连接 SSE，推送被静默丢弃。
- 没有设备注册表：没有 `device_tokens` 表或 `push_subscriptions` 表用于存储 APNs/FCM 令牌。
- 没有推送通知的偏好控制：`domain.NotificationPreference` 中已有 `quiet_hours_start`、`quiet_hours_end` 和 `timezone` 字段，但当前只服务于通知创建逻辑，从未用于决定是否触发设备推送。
- 没有轮询回退机制：对于不支持 SSE 的客户端，没有为通知提供长轮询替代方案。

### 为什么需要它

1. **移动端体验**：这是用户对现代通讯/文件分享应用的基本期望。没有推送通知，移动用户只有在主动打开应用时才能看到新消息。
2. **参与度**：推送通知直接驱动用户回访。对于消息场景，`message.received` 事件的新消息通知是核心功能，不推送给用户意味着产品价值严重受损。
3. **已存在的基础设施**：通知模型（`domain.Notification`）、调度器（`dispatcher.go`）、折叠、偏好和静默时段都已实现。唯一的缺口是从服务到设备的最后一步。

### 边界情况

- 设备令牌过期：用户卸载应用时 APNs/FCM 返回错误，需从注册表中清理失效令牌。
- 静默时段：`NotificationPreference.QuietHoursStart/End` 应阻止这段时间内的推送（但通知行仍应写入，以便用户下次打开应用时看到）。
- 通知折叠：同一用户的多条通知（如多条新消息）应合并为一条推送（"你收到了 5 条来自 Alice 的消息"），而不是产生 5 条推送。
- 授权撤销：用户撤销推送权限后停止发送。

### 架构建议

在 `internal/hub/hub.go` 中扩展 `Pusher` 接口，或增加一个独立的 `PushService`：

```go
type DeviceToken struct {
    UID    string
    Token  string
    Platform string // "ios" | "android" | "web"
    CreatedAt time.Time
}
```

在 `internal/notification/dispatcher.go` 的 `Handle` 方法中，经过现有检查后，除了写入通知行和 SSE 推送外，还调用 `PushService.Send(recipientUID, notification)`。该服务查询 `device_tokens` 表并发送到 APNs/FCM。提供 `web_push` 作为最终的轮询降级方案。

---

## 方向 5：跨实体引用一致性与级联生命周期

### 它是什么

建立跨实体的引用完整性规则——当源实体被删除、隐藏或使引用失效时，其依赖实体（评论、通知、文件共享）自动级联处理，而不是让虚假引用累积。

### 为什么当前缺失

当前删除行为是实体本地的，没有级联：

| 主实体 | 删除时 | 依赖实体 | 当前行为 |
|--------|--------|----------|----------|
| 消息 (`messages`) | 发送者软删除 | 附着的评论 | 评论保留，`target_id` 指向已删除的消息 |
| 消息 | 发送者软删除 | 关联的通知（`message.received`） | 通知保留，`subject_id` 悬空 |
| 文件 (`files`) | 软删除 | 文件共享 (`file_shares`) | 共享记录保留（`file_service.go` 未级联删除） |
| 文件 | 软删除 | 附着的评论 | 评论保留，`target_id` 指向已删除的文件 |
| 用户 (`uid`) | SSO 端删除 | 所有实体（消息、文件、评论、通知、块、偏好、设备令牌） | 全部保留，成为孤儿 |

`internal/domain/comment.go` 中的 `Comment` 结构有 `TargetID` 字段，但此 ID 在父实体删除后从不验证或清理。`internal/repository/file_share.go` 中的共享记录在文件软删除后未被移除。`internal/repository/notification.go` 中的通知在引用实体删除后未被标记为"不可用"。

### 为什么需要它

1. **用户界面一致性**：用户看到一个"你有一条新消息"的通知，点击后消息不存在（已被发送者删除）。这造成困惑和信任损失。
2. **引用完整性退化**：没有级联，数据库被悬空引用填充。`LEFT JOIN` 或后续的 `Get` 操作返回 `404`，服务层必须对每一个引用检查（`internal/service/message.go` 中的 `Get` 会检查文件、用户等），返回模糊的错误码或空白状态。
3. **存储效率**：无价值的悬空行占用数据库空间并拖慢索引扫描。

### 边界情况

- 软删除 vs 硬删除：级联应尊重软删除语义——如果一条消息被软删除，其通知应保留但标记为 `source_deleted`，而不是硬删，以便用户仍可查看通知历史。
- 双向删除：如果发送者删除了一条消息，接收者应仍能看到它（只是标记为"消息已撤回"）。这不符合简单的级联规则，需要仲裁策略。
- 批量级联：用户账户删除（SSO 触发）可能需要后台扫描的批量级联清理，而不是在请求路径同步删除。

### 架构建议

将级联语义建模为 outbox 事件：

1. 实体删除时，写入 `entity.deleted` outbox 事件，载荷中包含实体类型和 ID。
2. outbox dispatcher 中的新 handler 消费此事件，执行相关级联操作：
   - 消息删除 → 标记关联通知为 `source_deleted`，可选清除附着的评论。
   - 文件删除 → 撤销共享记录，标记关联通知为 `source_deleted`。
   - 用户账户删除（通过 SSO webhook/事件）→ 启动异步清除。
3. 将 `source_deleted` 标志添加到 `notifications` 表，让查询层在列表视图中有条件地隐藏这些条目，但详情页仍可显示。

---

## 总结：优先级与影响矩阵

| 方向 | 产品影响 | 运营价值 | 实现成本 | 依赖方 |
|------|----------|----------|----------|--------|
| 1. 数据生命周期管理 | 中（隐私合规、存储成本） | 高（DB 性能、运营自动化） | 中 | 配置层、后台任务（参考 GC sweeper） |
| 2. 乐观并发控制 | 中（数据正确性） | 中（冲突可观测、API 成熟度） | 低-中 | domain、service、handler（HTTP+gRPC） |
| 3. 分布式限流与配额 | 低（限速语义） | **高**（多副本安全、滥用防护、商业化） | 中-高 | 新 Redis 后端、配置、计费接口 |
| 4. 移动端推送通知 | **高**（用户参与度） | 低 | **高**（第三方依赖） | 设备注册表、FCM/APNs 集成、推送调度 |
| 5. 跨实体生命周期一致性 | 中（用户体验） | 中（引用完整性） | 中 | outbox 事件类型扩展、dispatcher 新 handler |

### 建议路线

- **近期（2-3 周）**：方向 2（乐观锁）——成本最低，收益明确，覆盖评论/消息编辑场景中的数据丢失风险。
- **中期（3-5 周）**：方向 3（分布式限流）——在生产多副本部署前是阻塞项，且 Redis 集成可复用现有的 `internal/pkg/ratelimit` 接口抽象。
- **中期（4-6 周）**：方向 5（级联生命周期）——在方向 1（数据清理）之前是前提：没有引用的话，盲目清理会制造悬空 UI。
- **长期（6-10 周）**：方向 1（数据 TTL）——复用方向 5 的级联基础设施和 GC sweep 模式。
- **长期（8-12 周）**：方向 4（移动推送）——工作量大且依赖外部基础设施，但产品差异化最高。

---

*本文件基于 `git log --oneline main..HEAD` 之前完成的所有功能的全面代码扫描编写。没有为生成本文创建或修改任何代码。*
