# aero-id — 架构师/产品经理视角：扩展方向分析

> 日期：2026-07-11
> 目标：基于当前代码库全局扫描，提出 3-5 个高价值、尚未实现的扩展方向。
> 原则：每个方向必须具有代码证据（当前缺什么），且必须对产品/架构有明显提升。

---

## 当前系统能力摘要（基于全局扫描）

| 能力域 | 状态 |
|-------|------|
| 文件 CRUD + 上传（流式/预签名）+ 校验（大小/MIME）+ 去重（checksum）+ S3/local + GC 回收 | ✅ 完备 |
| 消息 CRUD + 类型（文本/图片/文件/系统）+ 已读回执 + 编辑 + 软删 + reactions | ✅ 完备 |
| 评论 CRUD + 两层嵌套 + 点赞 + 举报/审核 + 编辑 | ✅ 完备 |
| 双传输（HTTP + gRPC）全对等 | ✅ 完备 |
| SSO 集成（缓存/断路器/stale-stalefallback/重试/mTLS/keepalive/遥测） | ✅ 完备 |
| 可观测性（OTel 追踪 + Prometheus RED 指标 + SSO/outbox 指标 + 分依赖 readiness） | ✅ 完备 |
| 写路径正确性（GORM 事务 + 幂等键 + 事务性 outbox） | ✅ 完备 |
| 通知（outbox → dispatcher → notification rows + block感知 + 偏好感知 + 速率限制 + collapse + SSE 推送） | ✅ 完备 |
| 全文搜索（PG tsvector + GIN 索引 + HTTP/gRPC 端点） | ✅ 完备 |
| 文件分享（显式按用户授权） | ✅ 完备 |
| 用户屏蔽（双向 + 通知抑制） | ✅ 完备 |
| 通知偏好（启用/静默时段/时区） | ✅ 完备 |
| Outbox webhook（HMAC 签名 + 事件过滤） | ✅ 完备 |
| 存储引用计数（原子 blob 删除） | ✅ 完备 |
| 管理员角色/权限（moderator/admin + 审计日志） | ✅ 完备 |
| SSE 实时推送（多 subscriber/uid + last_event_id 断线恢复） | ✅ 完备 |
| 数据库迁移（22 个可逆迁移 + CLI + 自动/手动模式） | ✅ 完备 |
| 频率限制（每 actor/IP 令牌桶 + 空闲桶回收） | ✅ 完备 |
| 测试（单元 + testcontainers 集成测试） | ✅ 完备 |
| CI/CD（GitHub Actions + Docker 多阶段构建 + docker-compose） | ✅ 完备 |

---

## 方向一：消息会话/话题模型（Conversation / Thread）

**代码证据：** 当前 `messages` 表仅以 `from_uid`/`to_uid` 存储单条私信，没有任何"会话"或"群组"概念。`handler_http/message.go` 的收件箱/发件箱列表按 `to_uid`/`from_uid` + `created_at DESC` 平铺，无法按话题聚合。`messages.kind`（1-4）和 `meta` JSONB 字段已预埋，但没有会话元数据（主题、参与者列表、置顶、归档状态）。

**为什么需要：**
1. **产品价值极高**——私信产品的核心抽象是"会话"。没有话题的用户体验相当于把所有邮件平铺在一个收件箱里，没有线程、没有群组、没有置顶。这在 MVP 阶段可以接受，但进入生产后会是最大的 UX 缺口。
2. **现有基础设施可直接复用**——SSO UserCache 已支持批量用户查询（`GetUsers`），可用于会话参与者头像/昵称补全；`block` 系统可在会话级别拒绝屏蔽者的消息；`notification` dispatcher 已支持 `message.received` / `message.read_receipt` 通知，可扩展为 `conversation.new_message`。
3. **自然的多设备基础**——会话模型是群聊、消息搜索、消息归档、leave-conversation 等功能的先决条件。

**建议实现思路：**
- 新增 `conversations` 表（id, type/dm/group, topic, created_at, updated_at, deleted_at）
- 新增 `conversation_participants` 表（conversation_id, uid, last_read_at, joined_at, left_at）
- 在 `messages` 表中增加 `conversation_id` 列（可为 NULL 向后兼容现有消息）
- Service 层提供：创建设会话、添加/移除参与者、列出我的会话、与会话内消息列表
- 会话级的通知偏好继承 + 参与者退出后不再收到新消息

---

## 方向二：文件版本历史与变更追踪

**代码证据：** `files` 表有 `checksum`（SHA256）、`storage_key`（指向实际 blob），`storage_refs` 表已经实现了引用计数（`GetCount` / `Incr` / `Decr`）。但 PATCH 更新文件元数据时不保留之前的版本信息；如果用新的 blob 替换（通过新的上传操作关联同一 file row），旧 blob 在引用计数归零后会被 GC 回收——**没有任何版本记录**。`files.meta` JSONB 虽然可以存额外元数据，但无结构化的版本差异日志。

**为什么需要：**
1. **协作与审计**——文件是核心资产。用户误覆盖后无法回滚；管理员审计时无法查看"这个文件在何年何月被替换过"。GDPR 场景下需要知道文件在某一时间点的确切内容。
2. **代价低**——存储引用计数已经到位，去重（checksum）已实现，`storage GC` 能正确处理多版本引用。新增一个 `file_versions` 表（`file_id`, `version_number`, `storage_key`, `checksum`, `size`, `mime_type`, `created_at`, `meta_diff`）只需数十行 SQL + 相应的 service/repo 逻辑。每次成功的 blob 写入（包括首次上传）都追加一条版本记录。
3. **S3 对象锁定**——如果未来使用 S3 Object Lock 做 Write-Once-Read-Many，版本表可以作为应用级快照索引。

**建议实现思路：**
- 新增 `file_versions` 表（file_id FK, version INT, storage_key, checksum, size, mime_type, meta JSONB, created_at）
- 在 `service_upload.go` 中，每次完成上传（包括首次和替换）后 insert 一条版本记录
- 暴露 HTTP `GET /v1/files/:id/versions` + `GET /v1/files/:id/versions/:version?mode=stream|presign`
- `PATCH /v1/files/:id` 更新元数据时，也创建一个版本快照（记录变化前后的 meta diff）
- GC sweeper 调整：只有 file 全部版本都被软删/清除后，才释放 blob 引用计数

---

## 方向三：多设备推送通知（FCM/APNs）与设备注册

**代码证据：** 通知推送目前唯一的实时通道是 SSE（`internal/hub/hub.go` + `internal/handler/http/events.go`）。`notification/dispatcher.go` 的 `Pusher` 接口（`Publish(uid, eventType, data string, eventID int64)`）只被 `hub.Adapter` 实现。`notification_preferences` 表已有启用/禁用、静默时段、时区等设置——这些在移动推送场景下完全适用。但没有设备注册表、没有 FCM/APNs token 存储、没有手机推送适配器。

**为什么需要：**
1. **移动端首要需求**——SSE 要求浏览器保持长连接（或客户端 WebSocket），移动端在后台时无法接收 SSE。没有 FCM/APNs 就意味着消息和通知的"实时性"只在桌面 Web 打开时存在。这是任何现代通信产品的基础能力。
2. **现有 dispatcher 架构可复用**——`NotificationDispatcher` 已经是一个 `outbox.Handler`，可以在 chain 中追加 `FcmPushHandler`（实现 `outbox.Handler`），从 dispatcher 接收事件并将其路由到 FCM。不需要修改通知创建逻辑。
3. **偏好设置天然匹配**——静默时段、类型级别的启用/禁用直接映射到移动推送的 delivery 决策逻辑。不需要另建一套配置。

**建议实现思路：**
- 新增 `device_registrations` 表（uid, device_id, platform("ios"|"android"|"web"), push_token, created_at, updated_at）
- 新增 HTTP `POST /v1/devices`（注册/更新 device token）+ `DELETE /v1/devices/:id`（注销设备）
- 新增 `internal/push/` 包，定义 `PushSender` 接口，实现 FCM (HTTP v1 API) + APNs (HTTP/2) 适配器
- 在 `outbox/handler` chain 中（app.go wiring 处），追加一个 push handler：从 dispatcher 的输出中获取 `(recipientUID, notificationType, payload)`，查询该 recipient 的所有活跃设备，逐个发送推送
- 推送失败（token 过期）→ 删除对应的 device registration 记录

---

## 方向四：分布式频率限制（Redis 后端）

**代码证据：** `internal/pkg/ratelimit/ratelimit.go` 实现了基于内存令牌桶的限流器，`Allow(key string) bool` 接口清晰，`RunCleaner` 回收空闲桶。当前是**每个进程独立**的——`internal/app/app.go` 的 wiring 中创建了一个限流器实例，HTTP 中间件和 gRPC 拦截器共享它。

在多副本部署中，如果一个攻击者以每秒 20 次（`rate_limit.rps`）的速率请求实例 A，同时以同样的速率请求实例 B，集群整体承受 40 RPS。这不是理论问题——`config.yaml` 中 `rate_limit.enabled: false` 默认关闭也反映了设计者对此的清醒认知。

**为什么需要：**
1. **多副本安全的唯一手段**——单实例限流在水平扩展时是"每个入口都有自己的门卫"：攻击者可以简单地多开连接打到不同实例。要真正做到防滥用，频率限制必须是集群范围的。
2. **系统已为可插拔后端做好准备**——`ratelimit.Limiter` 的接口足够简洁（`Allow` + `RetryAfter`），可以提取一个后端抽象（`Backend` 接口：`Allow(ctx, key, rate, burst) (bool, time.Duration)`），当前内存实现作为一个后端，新增 Redis 版（sliding window / token bucket via Lua 脚本）作为第二个后端。连接池可以用已经引入的 `github.com/testcontainers/testcontainers-go` 中隐含的 Redis 模块风格。
3. **成本低，影响可控**——Redis 限流器 fallback 策略：如果 Redis 不可达，降级到本地内存限流（fail-open/closed 可配置）。不会因为限流层的可用性问题导致整体不可用。

**建议实现思路：**
- 提取 `ratelimit.Backend` 接口：`Allow(ctx, key string, rate float64, burst int) (bool, time.Duration, error)`
- 现有 `Limiter` 重构为 `MemoryBackend`；`Limiter` 作为外观层持有 `Backend` + 可选兜底策略
- 新增 `RedisBackend`：使用 `github.com/redis/go-redis/v9` 的滑动窗口（ZREMRANGEBYSCORE + ZCOUNT）或令牌桶（`cl.Set` + `cl.Eval` Lua）
- 配置新增 `rate_limit.backend: "memory" | "redis"` + `rate_limit.redis.addr` / `rate_limit.redis.password`
- 在 `app.go` 的 wiring 中根据配置选择 backend，构建 `*ratelimit.Limiter`

---

## 方向五：数据生命周期管理与可配置保留策略

**代码证据：** `internal/storage/gc/sweeper.go` 为文件提供了可配置的保留期（`Retention`）和定时清理。但**消息**、**评论**和**通知**没有对应的保留策略——它们随 `deleted_at` 软删后永久累积。只有文件有 `HardDelete` 和 `ListSoftDeletedBefore` 方法。`migrations_test.go` 中验证了全部 22 个迁移，但没有任何清理/过期逻辑。

**为什么需要：**
1. **存储成本**——消息和评论是文本，但通知表（`notifications`）每产生一次通知就写入一行。在日活用户数千、每人每天数十条消息的场景下，通知表每月可能膨胀数百万行。没有自动清理，DBA 必须手动 truncate。
2. **合规/隐私**——GDPR 要求"存储不超过必要时间"。当前系统没有配置保留期的能力。用户删除账户（在 SSO 侧被禁用）后，aero-id 中的消息和评论仍被保留（仅软删）。未来如果 SSO 发出用户删除事件，aero-id 需要批量清理该用户的数据。
3. **现有 GC 架构可扩展**——`storage/gc/sweeper.go` 的模式（定时 + 可配置保留期 + batch 操作 + 指标记录）可以抽象为通用的 `retention.Sweeper`，应用于 files、messages、comments、notifications、idempotency_keys（当前 idempotency cleaner 使用 `time.Ticker` 直接 purge，没有 configurable retention）、outbox_events（dead-letter events 没有清理窗口）。

**建议实现思路：**
- 在 config 中新增 `retention` 配置节：
  ```yaml
  retention:
    message:     720d   # 消息保留 2 年
    comment:     720d
    notification: 90d   # 通知保留 90 天
    audit_log:    365d  # 审计日志保留 1 年
    outbox_dead_letter: 30d  # 死信事件保留 30 天后 hard-delete
  ```
- 新建 `internal/retention/sweeper.go`，封装定时清理逻辑（复用 storage GC 的模式）
- 为每个实体新增 `ListOlderThan(ctx, before time.Time, batch int)` + `HardDeleteOlderThan(ctx, before time.Time)` 方法
- Sweeper 在启动时根据配置注册对应的清理任务；0 值表示不清理（默认安全）
- 与 storage GC 的关系：storage GC 专注于 blob 生命周期；retention sweeper 专注于行级生命周期

---

## 小结

| # | 方向 | 产品价值 | 工程成本 | 架构契合度 | 与现有功能重叠 |
|---|------|---------|---------|-----------|--------------|
| 1 | 会话/话题模型 | ⭐⭐⭐⭐⭐ | 中 | 高（消息 meta 已预留） | 无 |
| 2 | 文件版本历史 | ⭐⭐⭐⭐ | 低-中 | 高（存储引用 + checksum 已就位） | 无 |
| 3 | 多设备推送通知 | ⭐⭐⭐⭐⭐ | 中 | 高（pusher 接口 + outbox chain 已就位） | 无 |
| 4 | 分布式频率限制 | ⭐⭐⭐ | 低-中 | 高（limiter 接口清晰） | 无（当前为单实例） |
| 5 | 数据生命周期管理 | ⭐⭐⭐⭐ | 中 | 高（GC 模式可复用） | 部分（文件 GC 已存在但可整合） |

这些方向的核心共同点是：**都建立在已有基础设施之上**，不需要推翻现有架构，而是在现有分层（handler → service → repository/outbox/storage）的开放接口处插入新模块。每个方向都解决了一个真实且当前未被覆盖的产品/运营缺口。
