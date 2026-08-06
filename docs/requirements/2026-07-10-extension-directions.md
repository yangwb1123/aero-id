# aero-id 高价值扩展方向

> **作者**: AI 架构师 · 日期: 2026-07-10  
> **方法**: 全量扫描当前代码库（internal/ 85 个源文件 + 22 个迁移 + 双协议层），  
>           基于现有架构评估 3–5 个最高商业/工程价值的扩展方向。  
> **不编写代码**，仅分析、描述、估算。

---

## 当前状态（摘要）

| 维度 | 成熟度 |
|---|---|
| 三大核心实体（文件/消息/评论）CRUD | ✅ 完整，含软删、编辑、嵌套（评论2层） |
| 双协议（HTTP REST + gRPC） | ✅ 38 RPC，完全对等 |
| 写路径可靠性（事务→outbox→幂等） | ✅ 完整，含死信重试 |
| SSO 韧性（缓存/熔断/重试/stale-fallback） | ✅ 完整，含 mTLS/cert-pinning |
| 可观测性（RED metrics / OTel / 健康检查） | ✅ 完整 |
| 限流（per-actor token bucket） | ✅ 完整，双协议 |
| 实时推送（SSE） | ✅ 含断线重播 |
| 通知（偏好/静默时段/折叠/block感知） | ✅ 完整 |
| 审核（评论举报/作者自隐/审计日志） | ⚠️ 基础（缺少全局审核队列与封禁 API） |
| 角色权限 | ✅ 完整（moderator/admin seed + 中间件） |
| 文件 pipeline（限大小/限MIME/去重/流式/预签名/GC） | ✅ 完整 |
| 文件分享（显式授权/撤销/列出） | ✅ 完整 |
| 全文搜索（FTS 跨三实体） | ✅ PG tsvector + GIN |
| 消息反应 / 已读回执 | ✅ 完整 |
| 管理 API | ⚠️ 存在但最小（仅 audit-log 查询） |
| 分布式/多实例 | ❌ 均为单机实现 |
| 22 个迁移全部 up/down 可逆 | ✅ |

---

## 方向一：群组/多人会话（Group & Multi-party Conversations）

### 为什么当前不够

消息模型是严格 1对1 的 `from_uid → to_uid`。没有"群组"或"频道"概念。  
现代消息产品（Slack、Discord、微信）的核心都是群组会话；缺少这个意味着 aero-id 只能做点对点通信，产品天花板明显。

### 需要什么

**新实体与关系**（约 3-4 个新表）：

| 实体 | 描述 |
|---|---|
| `conversations` | 群组本身：标题、头像、类型（群组/频道）、创建者、创建时间 |
| `conversation_members` | 成员表：(conversation_id, uid, role, joined_at, last_read_at) |
| `messages.conversation_id` | 现有 messages 表加一个 `conversation_id`（可为 NULL → 保持向后兼容 1对1） |
| `conversation_invitations`（可选） | 邀请/审批流 |

**行为变更**：
- `Send` 可以指定 `conversation_id` 替代 `to_uid`
- `ListInbox` 改为按 conversation 聚合
- 通知策略：群组消息通知需要更细粒度（@mention、@all、静音设置）
- 权限：群管理员可以增删成员、修改群信息、转让群主

### 影响评估

| 项目 | 评估 |
|---|---|
| 工程影响 | **高** — 消息是核心实体，改动涉及 schema、service、handler、outbox、notification 全链路 |
| 迁移兼容 | 现有 1对1 数据无需迁移；新 `conversation_id` 可为 NULL |
| 用户价值 | **极高** — 直接决定产品能做团队协作还是只能点对点 |
| 估算 | 3-4 周（含测试 + gRPC 对等） |

---

## 方向二：全局审核工作台（Admin Moderation Console）

### 为什么当前不够

代码库已经具备了审核的**基础设施层**：
- 角色权限系统（`roles` + `user_roles`，含 `content.moderate` / `user.ban` 权限）
- 评论举报机制（`comment_reports` 表 + 作者自处理 Resolve）
- 审计日志（`audit_events` 表 + 写入钩子）
- 全局查询 endpoint （`GET /v1/admin/audit-log`）

**但缺少运营方（moderator/admin）实际操作的 API：**

| 能力 | 状态 |
|---|---|
| 查看全局待处理举报队列 | ❌ 无 |
| 全局隐藏/删除任意评论/消息 | ❌ 只能作者自操作 |
| 封禁/解封用户 | ❌ `user.ban` 权限已定义，无端点 |
| 角色管理（列出角色/分配/撤销给特定用户） | ❌ 只有 GrantRole/RevokeRole，无 ListRoles/ListUserRoles |
| 查询某用户的操作历史 | ❌ audit-log 只能按 action 过滤，不能按 actor/target 筛选 |

### 需要什么

**新/扩展 endpoint（均在 `/v1/admin/` 下）：**

| Endpoint | 用途 | 需要的权限 |
|---|---|---|
| `GET /v1/admin/reports` | 全局举报队列（状态、时间、分页） | `content.moderate` |
| `POST /v1/admin/reports/:id/resolve` | 运营 resolve 举报 | `content.moderate` |
| `POST /v1/admin/comments/:id/global-hide` | 强制隐藏（覆盖作者意愿） | `content.moderate` |
| `POST /v1/admin/messages/:id/global-delete` | 强制删除消息 | `content.moderate` |
| `POST /v1/admin/users/:uid/ban` | 封禁用户 | `user.ban` |
| `POST /v1/admin/users/:uid/unban` | 解禁用户 | `user.ban` |
| `GET /v1/admin/users/:uid/audit` | 查询特定用户的操作历史 | `audit.read` |
| `GET /v1/admin/roles` | 列出所有角色 | `system.config` |
| `GET /v1/admin/user-roles/:uid` | 查看某用户的角色 | `system.config` |

**封禁影响面**：封禁不是删除 SSO 账号（aero-id 没有账户表），而是本地标记。被封禁用户：
- 无法创建/编辑/删除文件、消息、评论
- 已有内容可以选择隐藏或保留
- 被封禁用户在 write 路径前检查，拒绝写入

### 影响评估

| 项目 | 评估 |
|---|---|
| 工程影响 | **中** — 主要是 handler/service 层，schema 只需新增 `bans` 表；现有基础设施（权限/审计）完整可用 |
| 用户价值 | **极高** — 没有审核 API 的生产服务无法安全运营 |
| 估算 | 2-3 周（含测试 + gRPC 对等） |

---

## 方向三：文件版本控制与内容更新（File Versioning & Content Update）

### 为什么当前不够

当前文件创建后内容**不可变**。`UpdateMetadata` 只能改 name/visibility/meta。  
当用户需要"更新一个文件"时（例如替换合同附件、更新产品图片），只能：
1. 上传新文件 → 获得新 ID
2. 在所有引用处更新 ID（消息附件、分享记录等）

这是糟糕的 UX，而且导致**引用断裂**：旧消息依然指向旧文件。

### 需要什么

**版本化模型**（新增 `file_versions` 表）：

```sql
CREATE TABLE file_versions (
    id          UUID PRIMARY KEY,
    file_id     UUID NOT NULL REFERENCES files(id),
    version     INT NOT NULL,              -- 自增版本号
    storage_key TEXT NOT NULL,
    size        BIGINT NOT NULL,
    checksum    TEXT,
    mime_type   TEXT NOT NULL,
    created_by  TEXT NOT NULL,             -- 谁上传了这个版本
    created_at  TIMESTAMPTZ NOT NULL,
    deleted_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX ON file_versions(file_id, version);
```

**行为变更**：

| 场景 | 当前行为 | 目标行为 |
|---|---|---|
| 文件内容更新 | 不支持 | `POST /v1/files/:id/versions` 上传新内容 → 自动版本号递增 |
| 读取文件 | 返回单个文件 | 默认返回最新版本；可选指定版本 |
| 下载 | 最新/唯一 key | 指定版本对应的 storage_key |
| 列表 | 文件列表 | 可展开版本历史 |
| 分享 | 分享文件 | 可分享指定版本 |
| 存储 GC | 按 file row 引用计数 | 也按 file_version row 计数 |

**版本保留策略**（可选）：
- 保留最近 N 个版本（configurable）
- 自动删除超过 M 天的版本

### 影响评估

| 项目 | 评估 |
|---|---|
| 工程影响 | **中-高** — 涉及 file service、storage GC、双协议 handler、share service 的多处修改 |
| 向后兼容 | 现有文件视为 version 1，无需迁移 |
| 用户价值 | **高** — "数据归账户所有"的产品核心承诺包含版本历史 |
| 估算 | 3-4 周（含引用计数适配 + GC 适配 + 测试） |

---

## 方向四：Webhook 订阅管理与平台化事件出口（Webhook Subscription Management）

### 为什么当前不够

当前 outbox 的 webhook 推送是**硬编码**的：一个 URL、一个 signing key，都在 `config.yaml` 中配置。这意味着：
- 只能有一个外部消费者
- 修改目标 URL 需要重启服务
- 无法动态注册/撤销
- 消费者无法查看投递状态或重试失败事件

对于一个定位为"数据基础设施"的服务，事件出口应该是一个**平台能力**而非配置项。

### 需要什么

**新增实体 `webhook_subscriptions`**：

```sql
CREATE TABLE webhook_subscriptions (
    id           UUID PRIMARY KEY,
    owner_uid    TEXT NOT NULL,          -- 谁注册的
    url          TEXT NOT NULL,
    signing_key  TEXT NOT NULL,
    event_types  TEXT[] NOT NULL DEFAULT '{}',  -- 空 = 所有事件
    headers      JSONB NOT NULL DEFAULT '{}',
    paused       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    deleted_at   TIMESTAMPTZ
);
```

**新 API endpoints**（带 auth + 权限控制）：

| Endpoint | 用途 |
|---|---|
| `POST /v1/webhooks` | 注册新 webhook |
| `GET /v1/webhooks` | 列出我的 webhooks |
| `PUT /v1/webhooks/:id` | 更新 webhook（URL/keys/events） |
| `DELETE /v1/webhooks/:id` | 删除 webhook |
| `POST /v1/webhooks/:id/pause` / `resume` | 暂停/恢复投递 |
| `GET /v1/webhooks/:id/deliveries` | 投递历史（可扩展性） |

**架构变更**：
- outbox relay 改为查 `webhook_subscriptions` 表决定投递目标，而非单例 handler
- 添加投递结果记录表（可选，初期可仅日志）
- 事件签名为每个订阅独立签名（用各自的 signing_key）

### 影响评估

| 项目 | 评估 |
|---|---|
| 工程影响 | **中** — 主要新增订阅管理 CRUD + 改造 relay 投递逻辑；现有 outbox 基础设施无需大改 |
| 用户价值 | **中-高** — 对平台化战略至关重要，但终端用户不直接感知 |
| 估算 | 2-3 周（含测试 + 双协议） |

---

## 方向五：分布式限流与生产多实例支撑（Distributed Rate Limiting & Multi-instance Readiness）

### 为什么当前不够

当前所有限流/缓存/状态均为**进程内内存**：

| 组件 | 当前实现 | 多实例问题 |
|---|---|---|
| Rate limiter | goroutine map + token bucket | 每实例独立计数 → 限流阈值翻倍 |
| SSO token cache | sync.Map | 每实例独立缓存 → 缓存命中率低、SSO 压力加倍 |
| SSO user cache | sync.Map | 同上 |
| Idempotency store | GORM (Postgres) ✅ | 已跨实例 |
| SSE Hub | 进程内 channel | 多实例时用户连接分散 → 推送不到 |

### 需要什么

**分阶段演进**：

**P0 — 立即能做的（无需外部依赖）**：
- [x] Idempotency 已是 PG 持久化（已完成）
- [ ] Rate limiter 改为 **Postgres 行级锁** 或 **advisory lock** 实现（无需 Redis，牺牲少量性能换取一致性）
- [ ] SSE Hub 添加 **跨实例事件中继**（最简单的方案：利用 outbox + 一个内部 topic table，每实例轮询本实例不拥有的 uid 的事件并推送）

**P1 — 引入 Redis/valkey 作为共享层**：
- 分布式限流（sliding window / token bucket in Redis）
- 共享 SSO token/user cache（降低 SSO 压力）
- SSE 跨实例发布（Redis Pub/Sub 或 Redis Stream）

**P2 — 实例感知的优雅关停与零停机部署**：
- gRPC health NOT_SERVING 已实现
- 连接 drain 等待（已完成）
- 新增：**就绪检测包含缓存预热**（cache warm on startup）

### 影响评估

| 项目 | 评估 |
|---|---|
| 工程影响 | P0 **低**（无需外部依赖），P1 **中**（引入 Redis），P2 **低** |
| 用户价值 | **中** — 单实例足够大部分场景；对多实例生产部署是硬性要求 |
| 估算 | P0 1 周 · P1 2 周 · P2 1 周 |

---

## 综合建议与优先级

| 方向 | 商业价值 | 工程投入 | 依赖 | 建议 |
|---|---|---|---|---|
| 1. 群组会话 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | 无 | **Q3 产品方向** — 定义产品形态后再启动 |
| 2. 全局审核 | ⭐⭐⭐⭐⭐ | ⭐⭐ | 已有基础 | **立即启动** — 安全运营底线 |
| 3. 文件版本化 | ⭐⭐⭐⭐ | ⭐⭐⭐ | 无 | **紧随审核之后** — 核心数据能力 |
| 4. Webhook 平台化 | ⭐⭐⭐ | ⭐⭐ | 方向 2 的部分权限体系 | **Q4 平台化战略** |
| 5. 分布式支撑 | ⭐⭐⭐ | P0 ⭐ · P1 ⭐⭐ | 无 / Redis | P0 **现在可做** · P1 按需启动 |

### 推荐的执行序列

```
Phase A（2026-07 ~ 08）→ 方向二（审核） + 方向五 P0（PG 限流 + SSE 中继）
Phase B（2026-08 ~ 09）→ 方向三（文件版本化）
Phase C（2026-09 ~ 10）→ 方向一（群组会话）
Phase D（2026-10 ~ 11）→ 方向四（webhook 平台） + 方向五 P1（Redis 共享层）
```

---

## 附录：边界情况与性能优化备忘

### 边界情况（从现有代码推导）

| 场景 | 当前处理 | 风险/改进点 |
|---|---|---|
| 评论嵌套三层 | ❌ 拒绝（domain 层检查） | ✅ 已有 `ValidateNewComment` |
| 自阻塞（block yourself） | ❌ 拒绝（service 层检查） | ✅ 已处理 |
| 并发点赞 | ⚠️ 唯一约束保证幂等 | ✅ 已处理 |
| outbox 快重启事件丢失 | ⚠️ relay.Start 的 stop func 做最终 drain | ✅ 已处理 |
| 大文件上传 timeout | ⚠️ `cappedReader` + 配置上限 | ✅ 已处理 |
| SSO 完全不可用 | ✅ stale cache fallback + circuit breaker | 已处理 |
| 自己给自己发消息 | ⚠️ 允许（但通知被 `to_uid == from_uid` 过滤） | 需确认是否 UI 层阻止 |
| 文件被引用时删除 | ⚠️ 软删后 GC 等待 retention 窗口 | 合理 |
| 群组场景下 block 语义 | ❌ 未定义 | 方向一需要定义 |
| 消息已读回执风暴 | ✅ 通知折叠 5min + rate limiter 10/type/min | 已处理 |
| 多实例限流不一致 | ❌ 各实例独立 | 方向五解决 |

### 性能优化机会（低投入高回报）

1. **Outbox 轮询索引**：`FetchUnpublished` 查询 `(published_at IS NULL, created_at ASC)` — 已列在 ROADMAP。确认有无部分索引 `idx_outbox_pending`。  
   → 检查迁移 `000015_perf_indexes` 和 `000018_outbox_retry` 已覆盖。

2. **通知未读计数查询**：`CountUnread` 扫描全表计数。百万级通知时慢。  
   → 方案：维护 `notification_unread_counts(recipient_uid, count)` 物化小表，在 MarkRead/MarkAllRead/Create 时原子更新。

3. **SSE 断线重放限制**：`replayMissed` 硬编码 `limit=50`，大流量用户断线再连可能丢事件。  
   → 方案：改为 `limit=50` 但配合 `Last-Event-ID` 递进，让客户端多次请求补全。

4. **并发内容去重的 StorageKey 竞争**：已用 `StorageRefRepository` 的原子 refcount 解决 ✅

5. **GORM 查询 N+1 问题**：ListInbox 后逐条检查 block 状态？  
   → 检查：当前 `message.service.go` 的 `checkSendBlock` 是单条查询。列表场景应批量检查。

6. **Admin audit log 分页**：当前用 `id < cursor` 做 cursor，但 `audit_events` 主键是 UUID（非时序）。  
   → 方案：加 `created_at DESC` + `id DESC` 复合排序，或用时序 ID。

