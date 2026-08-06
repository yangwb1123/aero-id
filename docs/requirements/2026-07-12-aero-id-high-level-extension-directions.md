# aero-id 高阶扩展方向

> 基于全局代码扫描（2026-07-11），以资深架构师/产品经理视角分析。

---

## 当前状态概要

```text
Files:       CRUD + presigned upload/download + sharing + metadata PATCH +
             content-addressed dedup (ref count) + size/MIME validation +
             GC reconciliation sweep + storage backends (local/S3)

Messages:    1:1 send/inbox/conversation + read receipts + reactions +
             edit/soft-delete + system messages + block-aware send +
             transactional outbox events + audit logging

Comments:    create/list/replies + 2-level nesting + like/unlike +
             report/moderation (open→resolved/dismissed) + hide/unhide +
             edit + audit logging

Search:      PostgreSQL FTS (tsvector + GIN) across messages/comments/files +
             CJK ILIKE fallback + per-kind timeouts

Blocks:      directional + suppress inbound messages + suppress notifications

SSO:         circuit breaker + serve-stale + retry/backoff + singleflight +
             token/user cache + TLS/mTLS + keepalive + cert-pinning

Write-path:  transactions + idempotency keys + transactional outbox

Infra:       rate limiting (per-actor/per-IP) + OTel tracing + RED metrics +
             per-dependency readiness (DB+SSO) + graceful shutdown +
             migration CLI + HTTP↔gRPC parity (38 RPCs / 5 services) +
             SSE real-time notifications + notification preferences
             (per-type enable/disable + quiet hours + timezone + collapse)

Admin:       role-based permissions (content.moderate/audit.read/user.ban) +
             grant/revoke roles + cross-user audit log query
```

**结论：MVP + Phase-0/-1 功能已全部交付，代码成熟度高，生产就绪性强。** 以下方向是在此基础上的高阶扩展，每个方向均引用现有基础设施和代码中的明确约束/空白。

---

## 扩展方向一：群组/多⽅消息 (Group Messaging)

### 为什么需要

当前的 `Message` 模型是严格的 1:1（`from_uid` → `to_uid`），没有群组、频道或房间的概念。这意味着：

- 团队协作场景（项目讨论、技术频道）需要外部工具
- 通知系统已经支持多类型事件分发，但消息层面缺少"发送给 N 个人"的能力
- `messages.kind` 字段已有 `MessageKindSystem` 枚举，但缺少"群组消息"这个预期中的 kind
- 7 个已实现的 outbox 事件类型（`message.sent`/`message.read`/`comment.replied`）中，如果存在群组，还需要 `group.member_added`、`group.renamed` 等

### 现有基础设施可直接复用

| 组件 | 接入方式 |
|------|---------|
| `outbox` | 群组变更（成员加入/离开/改名）直接写 outbox 事件 |
| `NotificationDispatcher` | 新增 `group.invite`、`group.message` 等通知类型 |
| `hub.SSE` | 群组成员实时收到新消息推送 |
| `CommentService` | 群聊支持回复/评论群组消息（复用 `target_type = "message"`） |
| `FileShareService` | 群组可作为一个 shared-with 主体 |
| `BlockService` | 群组成员可以 block 其他人的消息 |
| `idgen` / cursor 分页 | 群组消息列表复用现有分页模式 |
| `SearchService` | 搜索群组消息自动加入 FTS 索引 |

### 需要新增的概念

- `group` entity（ID, name, owner_uid, created_at, updated_at, soft_deleted_at）
- `group_members`（group_id, member_uid, role, joined_at）
- `Message.GroupID` 可空字段 → 非空时表示群组消息
- 群组层面 ACL：read 需要成员身份，write 需要成员身份（或特定角色）

### 边界情况

- 群主离职/账号删除 → 所有权转移（参考 GitHub org owner transfer）
- 群组包含被 block 的用户 → 消息发送时过滤（现有 `BlockChecker` 可复用）
- 非成员尝试拉取群组消息列表 → 404 而非 403（信息泄露防护）
- 群组消息的已读/未读：目前仅支持 1:1 `mark_read`，群组需要 `last_read_message_id` 跟踪每个成员的阅读位置
- 大量成员（1000+）的推送可靠性：outbox relay 按需批量 dispatch 通知

---

## 扩展方向二：分布式共享状态（Redis 后端）

### 为什么需要

当前代码中多处明确指出了 **per-instance 限制**：

| 组件 | 文件 | 问题 |
|------|------|------|
| Rate limiter | `config.go:119` | `"LIMITING IS PER-INSTANCE; distributed limiting would need shared state (e.g. Redis)"` |
| Idempotency store | `idempotency/store.go` | GORM 表不能跨实例去重 |
| Notification collapse | `service/notification.go:WithDispatcherCollapse` | `FindRecentByTypeAndActor` 在多个实例上各自查找，无法跨实例合并 |
| SSO caches | `sso/token_cache.go` / `user_cache.go` | 每个实例有自己的缓存，部署滚动后会全部冷启动 |
| Outbox relay | `outbox/relay.go` | 多实例并行轮询相同表 → 需要用 `SELECT ... FOR UPDATE SKIP LOCKED` 或租约机制，否则重复处理 |

### 影响范围

- **水平扩容** 时，两个实例各自限流 → 有效容量翻倍但限流阈值不变（安全风险）
- **蓝绿部署** 期间，idempotency key 在旧实例上生效，新实例不认 → 重复写入
- **通知折叠** 在多个实例上各自创建折叠窗口 → 用户收到重复通知
- **SSO 缓存** 每个实例冷启动 → 部署后前 N 秒延迟飙升

### 推荐方案（渐进式）

1. **Phase A**: Idempotency store 迁移到 Redis（`SET NX EX TTL`），保留 GORM 作为 fallback
2. **Phase B**: Rate limiter 支持 `RedisBackend`（滑动窗口日志或 GCRA），保留内存模式作为降级
3. **Phase C**: SSO 缓存改为 Redis，内存缓存作为 L1（多级缓存）
4. **Phase D**: Outbox relay 引入 leader election / `SELECT ... FOR UPDATE SKIP LOCKED`

### 架构影响

```text
┌──────────────────────────────────────────────────┐
│                  aero-id instance                 │
│  ┌─────────┐  ┌──────────┐  ┌─────────────────┐  │
│  │ Limiter │  │ Idempot  │  │  SSO Cache L1   │  │
│  │ (memory)│  │ (memory) │  │  (memory, small) │  │
│  └────┬────┘  └────┬─────┘  └────────┬─────────┘  │
│       │            │                 │            │
├───────┴────────────┴─────────────────┴────────────┤
│                  Redis (L2 / source of truth)      │
└──────────────────────────────────────────────────┘
```

---

## 扩展方向三：文件后处理管线（Thumbnails / Preview / Transcoding）

### 为什么需要

文件系统已经非常完善（流式上传、MIME 校验、checksum 去重、GC 回收、引用计数、共享授权），但存在三个空白：

1. **用户无法预览**：上传一张照片后没有任何缩略图，下载全尺寸原图对移动端不友好
2. **无"处理中"状态**：`File` 模型没有 `status` 字段来表示"正在处理"或"处理失败"
3. **管线缺失**：没有异步任务系统来驱动图片缩略图生成、文档转 PDF 预览、视频初步帧提取等

### 现有基础设施可直接复用

| 组件 | 接入方式 |
|------|---------|
| `outbox` | 文件上传完成后写 `file.uploaded` 事件 → 管线 worker 消费 |
| `storage.FileStorage` | worker 下载原图、上传处理后产物 |
| `domain.File.Meta` | `meta.preview_path`、`meta.thumbnail_url` 等字段存储产物引用 |
| `storage_refs` | 缩略图 blob 的引用计数（与原文件共享或独立） |
| `FileService.UpdateMetadata` | 管线完成后回写 File 记录的 meta |
| `S3 storage` | 支持通过 `List` 遍历 + 清理过期临时产物（配合 GC） |

### 推荐管线事件流

```text
Upload Complete
      │
      ▼
outbox: file.uploaded  ──►  pipeline worker
                                │
                         ┌──────┴──────┐
                         ▼              ▼
                   image/*          application/*
                         │              │
                    ┌────┴───┐      ┌───┴──┐
                    ▼        ▼      ▼      ▼
               thumbnail  webp   pdf    txt-extract
                   │        │     │       │
                   ▼        ▼     ▼       ▼
              upload to storage, ref-counted
                   │
                   ▼
        outbox: file.preview_ready
                   │
                   ▼
        SSE push to owner + file shares
```

### 边界情况

- **大文件 / 慢处理**：设置 `max_processing_time`，超时后写入 `meta.processing_error` 并发出告警
- **无法处理的格式**：`meta.preview_status = "unsupported"`，前端展示兼容图标
- **图片旋转/EXIF 清理**：thumbnail 生成时剥离 EXIF 以避免隐私泄露（GPS 坐标等）
- **管线失败重试**：outbox relay 的 max_retries + dead-letter 机制天然支持
- **存储成本**：缩略图应有独立的 TTL 或生命周期策略，GC 阶段可选择清理低频访问的预览产物

---

## 扩展方向四：对话聚合列表 / 消息摘要（Conversation List）

### 为什么需要

当前 inbox 端点返回的是**所有消息的平面时间线**，缺乏现代即时通讯应用的标准功能：

- **无聚合视图**：用户无法看到"最近联系人的列表"——每条消息在 inbox 中独立出现，没有按联系人聚合
- **无摘要**：列表不显示每条对话的**最后一条消息预览**
- **无未读排序**：有未读消息的对话应置顶（已被 `domain.Message.ReadAt` 列支持，但无聚合查询）
- **无静音/置顶**：没有 `conversation_settings` 表

### 现有基础设施可直接复用

| 组件 | 接入方式 |
|------|---------|
| `MessageService` | 新增 `ConversationList` 方法，返回聚合结果 |
| `MessageRepository` | 已有 `(from_uid, to_uid, created_at DESC)` 复合索引，新增聚合查询只需 `DISTINCT ON + window function` |
| `domain.Message.ReadAt` | 用于判断对话是否全部已读 |
| `sso.UserCache` | 聚合结果中的联系人信息由现有缓存提供 |
| `notification.SSE` | 新消息到达时自动更新对话列表（增量通知） |

### 推荐实现思路

```sql
-- 每个 uid 最近的 n 条消息作为"对话摘要"
SELECT DISTINCT ON (least(from_uid, to_uid), greatest(from_uid, to_uid))
    id, from_uid, to_uid, content, created_at, read_at
FROM messages
WHERE (from_uid = :actor OR to_uid = :actor)
  AND deleted_at IS NULL
ORDER BY
    least(from_uid, to_uid),
    greatest(from_uid, to_uid),
    created_at DESC;
```

### 边界情况

- **用户 block 了联系人**：对话列表中应隐藏（或特殊标记），现有 `BlockChecker` 可过滤
- **自消息**：`to_uid == from_uid` 应排除或特殊处理
- **大对话数量**：cursor 分页（现有 `Page` 类型），按 `max(created_at)` 降序
- **系统消息**：`from_uid = "system"` 是否作为"系统通知"显示在列表中？建议单独归类

---

## 扩展方向五：Webhook 事件平台 / 第三方集成网关

### 为什么需要

当前 outbox 已支持 webhook 投递（`internal/outbox/webhook.go`），但功能非常简单：

- 只有一个目标 URL，不支持多订阅者
- 无事件类型的订阅管理（按 event_type 过滤已实现，但写死在 config 里）
- 无重试策略的可见性（dead-letter 在 DB 里但无 API 查看）
- 无事件 schema 注册与版本管理
- 无签名密钥的轮换机制

如果 aero-id 要作为**平台服务**向其他系统暴露事件（例如："用户上传文件后自动同步到 CDN"、"收到消息后推送企业微信"、"评论被举报后触发审核流"），就需要一个完整的 webhook 管理面。

### 现有基础设施可直接复用

| 组件 | 接入方式 |
|------|---------|
| `outbox.Event` | 已定义事件 schema（aggregate_type, event_type, payload） |
| `outbox.WebhookHandler` | 已实现 HMAC 签名 + 超时 + 非 2xx 重试 |
| `outbox.Chain` | 已支持多 handler 链式调用 |
| `admin.Roles` | 订阅管理需要 `webhook.manage` 权限 |
| `repository.AuditRepository` | 订阅创建/删除应记录 audit event |
| `/metrics` | 每个订阅者的投递成功率、延迟、重试次数可暴露为 Prometheus gauge |

### 需要新增的概念

- `webhook_subscriptions` 表（id, owner_uid, url, secret, event_types[], enabled, retry_config, created_at）
- 订阅管理 API（CRUD + 测试投递 + secret 轮换）
- 投递历史查询（`webhook_deliveries`：subscription_id, event_id, status, http_status, duration_ms, error）
- 仪表板 metric（投递成功率、P99 延迟、dead-letter 计数）

### 边界情况

- **签名密钥轮换**：支持双密钥窗口（过渡期新旧密钥同时有效）
- **幂等投递**：每个 `event_id + subscription_id` 唯一，支持重试不重复
- **背压与退避**：单个订阅者持续 5xx 时自动减速（指数退避→暂停→告警）
- **事件顺序**：同一 aggregate（如一条消息的多次编辑）应保持顺序投递

---

## 总结矩阵

| # | 方向 | 价值 | 依赖基础设施 | 已有代码引用 | 建议相位 |
|---|------|------|-------------|-------------|---------|
| 1 | **群组消息** | 🌟🌟🌟🌟🌟 | outbox, notif, SSE, search, block | `MessageKind` 枚举已预留 | P1 |
| 2 | **Redis 共享状态** | 🌟🌟🌟🌟 | 无（新依赖） | config.go 明确标注 per-instance 限制 | P0 (扩容前) |
| 3 | **文件后处理管线** | 🌟🌟🌟🌟 | outbox, storage, Meta, UpdateMetadata | `File.Meta` 已设计为自由 schema | P1 |
| 4 | **对话聚合列表** | 🌟🌟🌟 | inbox, cursor pagination, ReadAt, UserCache | 当前 inbox API 已返回全量消息，前端聚合成本高 | P2 |
| 5 | **Webhook 集成平台** | 🌟🌟🌟 | outbox, WebhookHandler, admin roles, audit | `WebhookHandler` 已实现但无管理面 | P2 |

**优先级建议**：
1. 如果扩容在即 → **方向 2** 先行（Redis 共享状态）
2. 如果追求产品价值 → **方向 1**（群组消息）+ **方向 3**（文件预览）
3. 如果打磨消息体验 → **方向 4**（对话列表）
4. 如果定位为平台服务 → **方向 5**（Webhook 平台）
