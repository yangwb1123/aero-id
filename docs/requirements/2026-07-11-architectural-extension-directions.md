# aero-id — 架构扩展方向分析

> **作者**: 资深架构师 / PM  
> **日期**: 2026-07-11  
> **方法**: 全代码库扫描（Go 源码 ~140 个文件，22 个 migration，全量配置，架构文档）  
> **目标**: 识别当前已交付功能之上、尚未覆盖的高价值扩展方向  

## 当前状态摘要

aero-id 已完成双传输（HTTP REST + gRPC）下 5 个核心域（文件、消息、评论、通知、阻塞）的全部 CRUD + 业务逻辑，以及以下生产级基础设施：

| 领域 | 已交付 |
|---|---|
| 可观测性 | RED metrics、OTel 追踪（穿透 SSO）、per-dependency /readyz、SSO 健康仪表盘 |
| SSO 韧性 | 断路器、serve-stale 降级、singleflight 合并、指数退避重试、TLS/mTLS 证书固定 |
| 文件管线 | 流式上传（非全量内存）、大小/MIME 校验、内容 checksum 去重、预签名直传 S3、GC 回收 |
| 写路径正确性 | 事务性 `reply_count`、idempotency-key、transactional outbox |
| 产品面 | 评论点赞/举报/审核、显式文件共享、消息 Reactions、系统消息、通知推送、SSE 实时事件流 |
| 基础设施 | 速率限制、gRPC health/reflection、outbox webhook 交付、审计日志、全文搜索 (tsvector)、管理角色 |
| 内容生命周期 | 编辑（评论/消息）、软删、屏蔽、静默时段偏好、通知折叠 |

Schema–API gap: **无残留**。所有已设计的列均有对应端点。

---

## 方向一：多设备连接管理、推送网关抽象与交付保证

### 现状

SSE 实时推送（`internal/hub`）是一个单进程、内存注册表式实现：

```go
// hub.go — 核心结构
type Hub struct {
    subs   sync.Map // uid → *uidSubs (每个 uid 一个 map[int64]chan SSEEvent)
    nextID atomic.Int64
    closed atomic.Bool
}
```

- **单进程单体**: `sync.Map` 是进程内存，多副本时每个实例只能看到自己的订阅者。一条通知只会落到一个实例，用户在另一个实例上没有连接 → "消息丢失"现象。
- **无持久化**: 订阅者断开后其 channel 被关闭。`last_event_id` 重放依赖通知表的 `created_at` 时间戳，但重放逻辑在 `EventsHandler` 中、不在 hub 本身，且容量仅 50 条。
- **无背压**: channel buffer = 16，满则静默丢弃。发送方（dispatcher）无反馈——它不知道自己发出的通知被丢弃了。
- **无设备级寻址**: 同一个 uid 的连接全被视为等价。无法区分"我正在看的浏览器 tab"和"我口袋里的手机"，也无法向指定设备类型推送（SSE vs FCM/APNS vs WebSocket）。
- **无交付回执**: SSE 协议没有应用层 ACK。hub 调用了 `pub()` 就认为"已推送"，从未确认客户端实际收到并处理后端事件。

### 为什么需要

1. **多副本部署必破**。只要运行 >1 个 aero-id 实例，SSE 实时性就不可靠。这是从 "dev 单体" 到 "production 集群" 的第一步也是最痛的一步。
2. **移动端缺失**。真实产品需要推送通知（iOS/Android）而非 SSE 长连接。推网关层缺失导致移动端只能退回到轮询 `/v1/notifications`，损害 UX 和服务端成本。
3. **无交付契约**。静默丢弃意味着业务方（如金融/医疗场景）无法依赖通知到达。需要 at-least-once + 回执。
4. **通知重复 / 乱序风险**。last_event_id 重放没有去重 ID，断线重连可能在重放和实时流之间产生 1–2 条重复。

### 建议方案轮廓

- **抽象 Push Gateway 接口**: `push.Gateway` 接口将"发送给 uid"和"发送给 device token"统一——SSE 是其中一种实现；FCM/APNS 是另外两种——每个实现独立注册。
- **设备注册表**: `devices` 表 (uid, device_id, platform, push_token, created_at)；客户端在连接时注册（或通过 API），服务端据此向正确的通道推送。
- **跨实例事件总线**: hub 保留本地订阅，外加一个轻量级 pub/sub（如 Redis Pub/Sub 或 pg notify/listen）将事件广播给所有实例的本地 hub。
- **交付回执 + 超时重试**: SSE 事件 ID 带上应用层 ACK 端点（`POST /v1/events/:id/ack`）；未 ACK 的事件在超时后回退到通知表的 `read_at` 标记逻辑。

---

## 方向二：多实例分布式协调（速率限制、SSE、Outbox、缓存）

### 现状

当前所有跨请求协调组件都是**进程内单例**：

| 组件 | 实现 | 多副本问题 |
|---|---|---|
| 速率限制 | `internal/pkg/ratelimit` — 内存 token bucket | 每个实例独立计数，用户可双倍消耗配额 |
| SSE Hub | `internal/hub` — `sync.Map` | 事件只投递到发送方所在实例 |
| Outbox Relay | `internal/outbox` — 每实例轮询 + `MarkPublished` | 多实例同时轮询产生重复处理，虽然后续 `MarkPublished` 有幂等性保护，但重复的 handler 执行会增加 DB 压力 |
| TokenCache | `internal/sso/token_cache.go` — `sync.Map` + 后台清扫 | 冷启动时每个实例都穿透到 SSO |
| UserCache | `internal/sso/user_cache.go` — `sync.Map` + 批量合并 | 同上 |

### 为什么需要

1. **速率限制的语义正确性**。per-instance 的 token bucket 在 N 副本下实际阈值为 `N × RPS`。这是安全/anti-abuse 的漏洞。攻击者只需轮询所有实例即可规避限流。
2. **Outbox 冲突与浪费**。N 个实例同时 `FetchUnpublished` 会拿到同一批事件、分 N 条 `notification` 行。当前幂等设计（`PublishedAt != NULL`）防止了重复投递，但多实例做了无用功（写冲突、回滚、重试），在事件密集型场景下浪费显著。
3. **缓存放大**。每实例独立的 TTL 缓存意味着冷启动 + 滚动更新时 SSO 流量放大 N 倍。这可能在部署高峰期压垮 SSO。
4. **运维复杂度**。每实例独立指标（速率限制命中率、缓存命中率）导致监控仪表盘聚合困难，无法区分"整体过载"还是"单实例倾斜"。

### 建议方案轮廓

- **分布式速率限制**: 引入 Redis 作为中心 token bucket（滑动窗口或 GCRA 算法），保留内存 bucket 作为第一级缓存。Redis 宕机时降级到允许所有请求（fail-open）。
- **Outbox leader 选举**: 只有 leader 实例执行 outbox drain；其他实例仅在 leader 失活时接管。可用 pg advisory lock 或 Redis `SET NX EX` 实现轻量级选主，避免引入 etcd/ZK。
- **共享缓存**: TokenCache 和 UserCache 增加 Redis 后备层（local LRU → Redis → SSO），或利用分布式 cache-aside 模式。SSO 冷启动穿透不再随副本数线性增长。
- **实例注册 / 优雅摘除**: 健康检查增加 `draining` 状态（目前 gRPC health 已可标记 NOT_SERVING，但 HTTP 侧无等价机制——实例收到 SIGTERM 后应等待 in-flight SSE 推送完成再退出）。

---

## 方向三：多域内容审核管线——文件与消息的审核 + 自动化扫描接入点

### 现状

**评论** 拥有完整的审核链路（`comment_reports` → `status` 生命周期 → 隐藏/取消隐藏 → `ResolveReport`），而 **文件** 和 **消息** 完全没有审核能力：

| 能力 | 评论 | 消息 | 文件 |
|---|---|---|---|
| 举报 | ✅ (`comment_reports`) | ❌ | ❌ |
| 隐藏/封禁 | ✅ (status = 3) | ❌ | ❌ (visibility 只有 public/private) |
| 审核队列 | ✅ (ListReports) | ❌ | ❌ |
| 自动扫描钩子 | ❌ 无 | ❌ 无 | ❌ 无 |
| 管理员批量操作 | 部分 (ResolveReport) | ❌ | ❌ |

此外，所有域都缺少**自动化扫描的接入点**——没有 place 来配置或调用外部恶意内容检测服务（NSFW 图片检测、文本垃圾/冒犯性分类器、重复内容检测、恶意软件扫描）。

### 为什么需要

1. **法律合规风险**。没有消息/文件的举报通道，平台无法及时获知不法内容（仇恨言论、骚扰、非法文件共享）。在大多数法律体系下，这构成"notice-and-action"缺陷。
2. **杠杆效应低**。审核基础设施在评论域已经跑通（报告表、状态字段、服务层 `ResolveReport`、权限检查 `content.moderate`），扩展到消息和文件是同类工作，但产生的合规覆盖面是 3 倍。
3. **无自动化防线**。没有钩子意味着只能人工审核。接入一个简单的哈希值阻止列表（PhotoDNA / 感知哈希）或文本分类器就能显著减小审核延迟和人工成本。当前代码没有设计接入点。
4. **跨域一致性**。审核员需要在一个界面看到用户的所有待审内容（消息+评论+文件），当前三个域各自独立，增加审核操作复杂度。

### 建议方案轮廓

- **统一举报模型**: `reports` 表（reporter_uid, subject_type="message"|"file"|"comment", subject_id, reason, status, resolved_by, resolved_at），替代目前只针对评论的 `comment_reports`。评论审核迁移到此表。
- **消息/文件隐藏**: 对消息增加 `hidden_at` 列（管理员隐藏后收/发双方仍可见但标记）；对文件增加 `admin_hidden` 状态，覆盖 `visibility`。
- **审核队列统一**: 扩展 `ListReports` 支持 `subject_type` 过滤，扩展 `GET /v1/admin/queue` 端点为跨域审核仪表盘。
- **Moderation Hook 接口**: `domain.ContentModerator` 接口（`Moderate(ctx, subject) -> (action: allow|flag|block, reason)`），在 service 层 create 后调用。具体实现可以是本地规则引擎 + 远程 gRPC 分类器 + 感知哈希库。
- **扩展权限**: 当前 `content.moderate` 权限已存在，代码可直接复用。

---

## 方向四：账户数据生命周期管理——导出、级联删除、保留策略

### 现状

aero-id **不存储账户行**（SSO 是身份权威），但存储账户名下的全部业务数据：文件、消息、评论、通知、举报、分享关系、审核日志。当前没有任何数据生命周期管理：

| 诉求 | 实现 |
|---|---|
| 用户导出自己所有数据 | ❌ 无端点和流程 |
| 用户要求删除所有数据（"right to erasure" / GDPR Art.17） | ❌ 无级联删除；soft delete 依赖 GC 异步清理 |
| 管理员设置不同数据类型的保留期限 | ❌ 无配置 |
| 合规审计：某个 uid 的所有数据踪迹 | ❌ 需手工查 7+ 张表 |
| 关闭账户后的数据隔离 | ❌ 行仍留在表中，仅 owner_uid 标识 |

GC sweeper (`internal/storage/gc`) **只清理孤儿 blob**，不处理业务行的完整删除。

### 为什么需要

1. **法律法规强制（GDPR / CCPA / 个人信息保护法）**。用户行使删除权时，必须级联删除该 uid 下的全部数据且不可恢复。"30 天后 GC 软删行"不满足合规时效要求。
2. **数据最小化 / 存储成本**。无保留策略意味着即使是"7 天后可删"的通知行也会永久留存。在规模下，notification 表会以每天数百万行的速度增长。
3. **信任与透明**。没有 `GET /v1/export` 端点，用户无法了解平台持有自己的哪些数据。这在面向消费者的产品中日益成为基本信任要求。
4. **审计准备不足**。当合规审计要求提供"用户 X 的所有数据痕迹"时，当前需手动 join 7+ 张表——容易遗漏。

### 建议方案轮廓

- **数据导出端点**: `GET /v1/account/export` → 后台异步任务（outbox 驱动）生成 JSON/CSV 归档，上传到临时存储，通过通知通知用户下载链接到期时间。
- **级联删除端点**: `DELETE /v1/account` 或管理员触发的 `POST /v1/admin/account/:uid/purge` → 在单事务内软删（或硬删）files, messages, comments, notifications, file_shares, comment_reports, user_blocks, message_reactions, notification_preferences 中所有 belonging 行。可配模式：SOFT（标记 + 阻止重新激活）vs HARD（不可逆）。
- **保留策略框架**: 在 `config.yaml` 中可配置每种数据类型的保留期限，GC sweeper 扩大职责范围来执行：retention.notifications=90d / retention.messages=365d / retention.audit_log=730d 等。
- **数据清单 API**: `GET /v1/account/data-summary` → 返回各表的数据量（总文件大小、消息数、评论数等），帮助用户/管理员了解数据范围。
- **SSO 钩子**: 监听 SSO 的"account deleted"事件（通过 outbox webhook 或 SSO gRPC 新增 rpc），自动触发 aero-id 的级联删除流程。

---

## 方向五：跨域活动图与关系模型（@提及、统一时间线、聚合视图）

### 现状

五个域（文件、消息、评论、通知、阻塞）在数据层面完全隔离，没有任何跨实体关联：

```go
// 当前搜索 hits 是 union type —— 但每个 hit 完全独立
type SearchHit struct {
    Kind    SearchKind  // "messages" | "comments" | "files"
    Message *Message    // 三者之一非空
    Comment *Comment
    File    *File
}
```

缺失能力：
- **@提及系统**: 消息/评论内容中包含 `@user` 没有解析、没有通知、没有超链接机制
- **统一对话视图**: 无法获取"我和用户 X 之间"的完整交互——消息、评论（在 shared 文件上）、通知——在一个时间线上
- **文件语境查看**: 无法从一个文件追溯"哪些评论和消息提到了它"
- **活动流**: 没有域内聚合的时间线（"X 分享了文件，Y 评论了，Z 回复了消息"）
- **搜索排序**: 当前搜索无相关性排序，只是按 kind 依次查三个域拼接

### 为什么需要

1. **产品体验断层**。用户在不同域之间跳转体验割裂。@提及是最基础的社交功能之一，缺失意味着用户无法"提及"他人。
2. **无法构建上层视图**。没有跨域视图，就无法构建"Home 时间线""最近的会话""热点内容"等产品功能。
3. **搜索实用性不足**。当前搜索只是三个独立查询的简单拼接。没有 TF-IDF 或 BM25 排序、没有高亮、没有实体过滤。对于已有全文索引（tsvector）的实现，丰富排名函数是低成本高回报。
4. **@mentions 是高杠杆**。解析逻辑是可复用的文本处理管道（service 层文本输入 → 提取 mention → 解析 uid → 生成内置通知），影响消息、评论两个域，且是通知类型的自然扩展。

### 建议方案轮廓

- **@提及管道**: 在 `service.SendMessage` / `service.CreateComment` 中增加文本后处理步骤——正则匹配 `@handle` → 调用 `ssoClient.GetUsers` 解析 handl → uid → 通过 outbox 发送 `mention.created` 事件 → 通知接收方。需 migration 增加 `mentions` 表（subject_type, subject_id, mentioned_uid, offset, length）保证幂等。
- **统一时间线端点**: `GET /v1/timeline?with_uid=X&limit=20` → 按 `created_at DESC` 合并来自 messages（inbox/outbox 中涉及 X 的）、comments（target owner 为 X 的）、file_shares（X 分享给我的）的数据流。返回 union 结果 + actor 富化。
- **文件引用追溯**: 在 `file_meta` 的预留 `jsonb` 字段中记录被 `messages.file_id` 引用的次数；`GET /v1/files/:id/refs` 枚举引用该文件的消息和评论。
- **搜索排序升级**: 利用已有 GIN tsvector 索引 + `ts_rank` 函数，结合 `LIMIT` + 跨域合并后排 Top N。支持 snippet 高亮（`ts_headline`）。
- **活动通知扩展**: 当前 `dispatcher` 只处理 `message.sent` / `comment.replied` / `message.read_receipt`。增加 `mention.created` / `file.shared_with_you` / `comment.on_your_file` 等事件类型。

---

## 优先级排序建议

| # | 方向 | 影响面 | 工作量 | 前置依赖 | 推荐时机 |
|---|---|---|---|---|---|
| 1 | **多实例协调** | P0 — 生产可用性 | 中 | 无（新建组件，不重构现有代码） | **立即**（上线前必须） |
| 2 | **数据生命周期管理** | P0 — 法律合规 | 中 | 无 | **立即**（上线前必须） |
| 3 | **多域审核管线** | P1 — 法律合规 + 产品 | 中 | 方向 1（多实例审核队列需要） | 方向 1 之后 |
| 4 | **推送网关 + 多设备** | P1 — 产品体验 | 高 | 方向 1（跨实例事件总线） | 方向 1 之后 |
| 5 | **跨域活动图** | P2 — 产品差异化 | 中-高 | 方向 1 + 4（通知管道需要） | 方向 1–4 之后 |

### 第一批（上线阻断）

**方向二（多实例协调）** 和 **方向四（数据生命周期）** 是生产上线前的 blocker。没有分布式限速和跨实例 SSE，多副本部署会产生功能缺陷；没有级联删除和导出，有合规风险。

### 第二批（高杠杆）

**方向三（多域审核）** 复用评论域已跑通的审核基础设施，扩展到消息和文件是增量成本最低的合规投资。**方向一（推送网关）** 是移动端体验的前置条件。

### 第三批（差异化）

**方向五（跨域活动图）** 是高价值但非紧急的产品特性，建议在其他方向落地后启动。届时 `@`提及、统一时间线和搜索排序将为 aero-id 从"数据存储"升级为"协作平台"奠定基础。
