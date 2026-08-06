# aero-id — 扩展方向分析

> 基于 `main` (2026-07-11) 全局扫描。不包含代码修改，仅分析。

---

## 项目当前能力摘要

在提出扩展方向之前，先明确已实现的边界，避免重复推荐。

| 域 | 已实现能力 |
|---|-----------|
| **文件** | CRUD、流式上传、S3 presigned 直传、内容哈希去重、MIME 类型白名单、大小上限、显式共享(粒度到 uid)、可见性(public/private)、元数据更新(PATCH)、storage GC 回收、软删除 + 硬清理、HMAC 签名下载 |
| **消息** | 发送/收件箱/会话、已读回执、编辑、软删除、Reactions (点赞)、系统消息、Block 守卫(阻止被屏蔽者发消息)、按会话标记已读、未读数统计 |
| **评论** | 创建(含两级嵌套校验)、列表/回复列表、点赞/取消点赞、举报 + 处理队列(resolve/dismiss)、编辑、隐藏/取消隐藏、软删除、target ACL 校验 |
| **通知** | Outbox 驱动的事件→通知映射(message.sent/read、comment.replied)、首选项(启用/关闭/免打扰时段)、折叠(5min窗口合并)、每类型速率限制、Block 抑制、SSE 实时推送 + last_event_id 断线重放 |
| **搜索** | PostgreSQL tsvector 全文搜索 + CJK ILIKE 回退、跨消息/文件/评论联合搜索 |
| **管理** | RBAC 角色(permission-based)、审计日志查询(action 过滤)、角色授予/撤销 |
| **基础设施** | 双传输(HTTP + gRPC 38 RPCs)、in-memory 速率限制、OTel 追踪、幂等性键、事务性 outbox、断路器、TLS/mTLS、SSO 服务降级(stale cache)、健康检查(/healthz + /readyz 多依赖探针)、Prometheus RED 指标、storage GC 定时回收 |

---

## 扩展方向

---

### 1. 🚨 大文件端到端完整性验证（Presigned Upload Checksum Gap）

**当前状态**

`PresignUpload` + `CompleteUpload` 流程已经是生产级设计：客户端直接 PUT 到 S3，应用层仅做元数据持久化。但 `CompleteUpload` 显式注释了：

> Dedup is skipped (no checksum without downloading).

这意味着：
- 对于通过 presigned URL 上传的大文件，**完全没有内容完整性验证**——客户端可以 PUT 损坏数据或完全不同的内容，服务端无法感知。
- 内容去重在 presigned 路径上完全失效，同一文件被上传 N 次产生 N 个独立 blob（storage GC 也无法合并，因为每个 blob 都有独立的 storage_key）。
- 如果客户端声明 `Content-MD5` 但实际上传不同内容，S3 会拒绝 —— 但只对单个分片有效，且 aero-id 不验证该头部。

**为什么需要**

这是一个文件服务中最基础的数据正确性保证。Presigned 上传路径目前是"信任但无法验证"的。对于用户上传的重要文件（凭证、合同、图片），服务端应当有能力：
1. 验证最终持久化的内容与客户端意图一致
2. 对相同内容的文件做去重以节省存储成本
3. 在审计日志中记录内容指纹

**建议方案**

- 要求客户端在上传前计算 `SHA-256`，在 `CompleteUpload` 请求中携带 `expected_checksum`
- 服务端通过 **S3 Select / 范围读取** 或通过 **异步 Lambda/Worker** 计算实际 checksum 后对比
- 或者更轻量的方案：客户端 PUT 后立即 `HEAD` 对象获取 `ETag`（S3 的 ETag 通常是 MD5），与客户端声明值交叉验证
- checksum 匹配后，再利用 storage ref 引用计数实现跨用户去重（基础设施已就绪：`WithStorageRefs` + `storage_refs` 表 + `reuse` 逻辑）

**边界情况**

| Edge Case | 处理 |
|-----------|------|
| 超大文件(>5GB) 使用 multipart upload | ETag 不再是整个文件的 MD5，需客户端额外计算 SHA-256，通过 `x-amz-tagging` 或自定义 metadata 传递 |
| 网络中断导致部分上传 | S3 上的 "部分对象" 由 storage GC 的 `MinOrphanAge` 守卫清理（已实现） |
| 客户端声明 checksum 但未通过验证 | `CompleteUpload` 返回 400，storage GC 异步清理孤 blob |
| 去重命中后用户删除文件 | `Delete` -> refcount 减 1；仅当 count==0 时物理删除，不误伤共享同 blob 的其他文件 |

---

### 2. 📱 多设备同步与会话管理（Cross-Device State Sync）

**当前状态**

SSE Hub 已经支持同一 uid 下多订阅者（多设备/多标签页），但：
- **已读状态没有跨设备同步**：用户在手机端读了消息，桌面端的未读计数不会更新（`messages.read_at` 只在标记时设置，不通知其他设备）
- 通知已读/全部已读同理：`MarkAllRead` 只更新数据库列，SSE 订阅者不会收到"这些通知已被标记为已读"的推送
- **没有设备注册机制**：无法区分"同一用户的多个活跃连接"，也无法支持移动端 Push Notification（APNs/FCM）的 token 管理
- **没有会话管理 API**：用户无法查看/撤销自己的活跃会话，这是一个安全合规要求（GDPR "记住我" 设备列表）

**为什么需要**

产品本质上是一个消息+文件+评论系统——这类系统的用户天然在多设备上使用。没有跨设备同步：
- 用户在手机端读了消息后回到电脑端，未读红点依然存在，体验断裂
- 无法实现 Push Notification（移动端必须有 token 注册 + 服务端推送通道）
- 没有"下线其他设备"的安全能力

**建议方案**

- **设备注册表**：新增 `user_devices` 表 (`uid`, `device_id`, `device_name`, `push_token`, `push_provider`, `last_seen_at`, `created_at`)
  - `POST /v1/devices` 注册设备（包含 Push token）
  - `DELETE /v1/devices/{device_id}` 登出设备
  - `GET /v1/devices` 列出当前用户的活跃设备
- **已读状态同步事件**：在 `mark_read` 事务中通过 outbox 发出 `message.read_cross_device` 事件，relay 将其推送到该 uid 的 SSE 连接（已存在的 Hub/Pusher 架构可直接复用）
- **通知已读同步**：同样的模式——`notification.marked_read` 事件推送到同一用户的其他设备
- **Push Notification worker**：新的 outbox handler `pushHandler` 接收 `message.sent`/`notification.*` 事件，查询设备的 push token 并调用 FCM/APNs

**边界情况**

| Edge Case | 处理 |
|-----------|------|
| 同一用户同时在 10 个设备上在线 | SSE Hub 的每个订阅有独立 buffer，慢消费者丢事件不阻塞；Push Notification 作为保底 |
| 设备 token 过期/失效 | Push provider 返回非 200 时标记 device 为 inactive，不再推送 |
| 用户点击"退出所有设备" | 从 `user_devices` 中删除该用户所有记录，同时向所有活跃 SSE 连接发送 `session.revoked` 事件，客户端收到后主动断开 |
| 设备注册的幂等性 | 用 (uid, device_id) 做 upsert，`OnConflict DoNothing` |

---

### 3. ⛓️ 消息附件生命周期管理（File-Message Attachment Lifecycle）

**当前状态**

`Message` 有一个 `file_id` 可选字段，消息发送时可以附加文件。但是：
- **文件独立于消息存在**：发送消息后，文件的 owner 随时可以调用 `DELETE /v1/files/{id}` 独立删除该文件。删除后，消息的 `file_id` 成为悬挂引用，接收方看到消息时无法获取附件的任何信息（GET 返回 404）。
- **没有级联语义**：删除消息不会对文件的 owner 产生任何提示——文件仍然存在，但失去了与之关联的业务上下文。
- **没有引用计数**：多个消息引用同一个文件时，文件删除会破坏所有引用它的消息。
- **没有附件预览/缩略图**：对于图片/视频类型的附件，没有生成缩略图的能力。

**为什么需要**

这个问题属于数据完整性（data integrity）和用户体验交叉的领域。在消息场景中：
- 附件的内容完整性应当不依赖于发件人的后续行为——一旦发送，接收方应当始终能查看该附件
- 如果发件人删除了原始文件，引用该文件的消息应当优雅降级（显示"附件已不可用"而不是 500 或 "file not found"）
- 对于图片类附件，缩略图在移动端聊天列表中是刚需

**建议方案**

- **快照语义（Snapshot on Send）**：发送消息时，如果携带 `file_id`，在 `message_files` 表创建一条记录（`message_id`, `file_id`, `snapshot_name`, `snapshot_mime`, `snapshot_size`, `snapshot_storage_key`），复制文件的元数据。这样即使文件被删除，消息仍能展示附件名称和类型。
- 更轻量的替代方案：**冻结引用（Freeze Reference）**，在消息层做 `file_id` 检查时，如果文件已被删除，返回一个特殊的 `deleted_attachment` 占位信息，而不是直接 404。
- **缩略图生成**：对于 `image/*` 类型的附件，在 `CompleteUpload` 或通过异步 worker 生成缩略图（128×128 / 256×256），存储为同 storage key 的不同 variant。
- **附件引用计数**：在 `file_service.Delete` 中加入检查：如果文件被消息引用，返回 `409 Conflict`，或允许删除但标记为 `orphan_attachment` 状态。

**边界情况**

| Edge Case | 处理 |
|-----------|------|
| 同一文件被多次作为附件发送 | 快照语义下每个消息有独立副本，不受原文件删除影响 |
| 发件人删除文件后撤销消息 | 消息已删除则无影响；若消息未删除，快照仍可展示 |
| 超大图片的缩略图生成 | 异步 worker 处理，不阻塞上传路径；失败时回退到原始文件 |
| 缩略图 storage GC | 缩略图应当与原文件同生命周期，或在单独的 `thumbnails` bucket 中由 GC 扫描原点判断 |

---

### 4. 🛡️ 分布式速率限制与滥用检测（Distributed Rate Limiting & Abuse Detection）

**当前状态**

当前速率限制：
- **in-memory token bucket**，`RateLimit.RPS/Burst` 可配置
- **per-instance**：多副本部署时每个实例有独立的 bucket，用户可以在不同实例间分散请求绕过限制（总 QPS = RPS × N）
- **key 粒度**：按 `actor_uid`（已认证）或 client IP（匿名）
- **全局启用/关闭**：不支持 per-endpoint 差异化限制

滥用防护缺失：
- 没有**评论频繁举报检测**：恶意用户可以每秒举报同一条评论多次（服务层有 `reporter_uid + comment_id` 唯一约束防御重复，但不防不同的评论）
- 没有**Like/Unlike 反转检测**：可以反复 like/unlike，制造非必要的数据库写入
- 没有**Spam 检测**：评论和消息内容没有垃圾文本识别
- 没有**全局 QPS 告警**：没有速率限制触发时的告警通知

**为什么需要**

项目的 rate limiter 设计文档标注了 **OPT-IN, per-instance**。在生产部署中（通常是多副本 + 负载均衡），单实例的速率限制几乎没有效果——用户只需轮换实例即可。这是 SSO 容错之后第二个单点失效风险。

同时，社交功能（评论、举报、Like）天然容易被滥用，缺乏滥用检测可能在产品早期就吸引 spam 和恶意行为。

**建议方案**

- **分布式速率限制**：使用 Redis 作为中心化 token bucket 后端（`sliding window` 或 `GCRA` 算法），替代当前 in-memory 实现
  - 保持原有接口 `ratelimit.Limiter`，新增 `redisLimiter` 实现
  - 配置化切换 `type: memory | redis`
  - 不要求 Redis 即可运行（memory 作为 fallback）
- **per-endpoint 速率配置**：在配置中允许覆盖，例如：
  ```yaml
  rate_limit:
    enabled: true
    default_rps: 20
    endpoints:
      "POST /v1/comments/*/report": { rps: 2, burst: 5 }
      "POST /v1/messages": { rps: 5, burst: 10 }
      "POST /v1/comments/*/like": { rps: 10, burst: 20 }
  ```
- **举报滥用检测**：reporter_uid 维度的速率限制（每分钟最多 N 条举报），超出后返回 `429 Too Many Reports`
- **内容 Spam 评分**：可插拔的评分接口（初期可用简单的关键词/正则黑名单，后续可集成 ML 模型），高于阈值自动标记为 `CommentStatusHidden` 并创建举报

**边界情况**

| Edge Case | 处理 |
|-----------|------|
| Redis 不可用 | 降级到 in-memory 模式（配置 `redis_fallback_to_memory: true`），记录告警日志 |
| 分布式环境时钟不同步 | 使用 Redis 的 `TIME` 命令获取统一时间戳，或接受毫秒级误差（滑动窗口算法天然容忍） |
| 新增 endpoint 忘记配置限制 | 使用 `default_rps` 兜底 |
| 突发合法流量（秒杀场景） | Burst 参数允许瞬时超过 RPS；配合 `idempotency_key` 保证重试安全 |
| 举报者被认定为滥用 | 系统自动临时屏蔽该用户的举报能力（类似 GitHub 的 abuse rate limit） |

---

### 5. 🧵 对话抽象与群组消息（Conversation Abstraction & Group Messaging）

**当前状态**

消息模型是纯点对点的：
- `Message` 只有 `from_uid` 和 `to_uid` 两个参与方
- "会话"是通过 `ListConversation(ctx, actor, withUID)` 查询 `(from_uid=X AND to_uid=Y) OR (from_uid=Y AND to_uid=X)` 实现的
- 没有独立的 `Conversation` 实体——会话元数据（标题、头像、置顶消息、静音状态）完全不存在
- 没有群组概念：无法向 N 个接收方发送同一条消息

**为什么需要**

当前的 1:1 消息模型在未来会成为一个架构瓶颈：
- **群聊**是最常见的消息产品需求，但当前的 schema 和 API 完全不支持
- **没有"会话列表"** 的概念——`Inbox` 返回的是消息级别的列表，不是会话级别的摘要（每条消息一行，而不是"与 Alice 的最新消息"一行）
- **无法静音/置顶**：不支持用户对某个对话设置「免打扰」「置顶」「归档」
- **消息重复存储**：群聊如果实现为向每个接收方插入一条 `Message` 记录，存储 O(N) 且无法实现"撤回群消息"

**建议方案**

- **新增 `conversations` 表**：
  ```sql
  CREATE TABLE conversations (
      id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
      type         smallint NOT NULL DEFAULT 1, -- 1=1:1, 2=group
      title        text,
      avatar       text,
      created_at   timestamptz NOT NULL DEFAULT now()
  );
  CREATE TABLE conversation_members (
      conversation_id uuid NOT NULL REFERENCES conversations(id),
      uid             text NOT NULL,
      joined_at       timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (conversation_id, uid)
  );
  ```
- **消息模型变更**：`Message.conversation_id` 替代 `from_uid`/`to_uid` 作为路由依据，保留 `from_uid` 作为发送者
- **会话级操作**：
  - `POST /v1/conversations` 创建会话（1:1 自动或指定成员列表）
  - `GET /v1/conversations` 列出用户的会话列表（含最后一条消息摘要 + 未读数）
  - `PATCH /v1/conversations/{id}` 静音/置顶/修改标题
  - `POST /v1/conversations/{id}/members` 添加成员（群聊）
  - `DELETE /v1/conversations/{id}/members/{uid}` 移除成员
- **向后兼容**：现有 1:1 消息在新系统上线时，通过数据迁移自动创建对应的 conversation

**边界情况**

| Edge Case | 处理 |
|-----------|------|
| 群聊消息的"已读"语义 | 群聊中已读是对整个会话的——新增 `conversation_read_cursors` 表记录每个成员在每条会话中已读到哪条消息 |
| 用户退出群聊后收到历史消息 | 退群后取消 `conversation_members` 记录，后台任务清理该成员与已退群会话的通知关联 |
| 群聊消息撤回 | 只有发送者在特定时间窗口内（如 5 分钟）可撤回；撤回后对所有成员显示"消息已撤回" |
| 既要群聊又要兼容现有 API | `Inbox` 继续工作但返回会话维度的摘要；`Conversation` RPC 新增，旧 `Conversation` RPC 标记为 deprecated |
| 大群（500+ 人）的消息 Fan-out | 采用"写扩散"（Write Fan-out）：每条消息写入一次，每个成员的 `conversation_read_cursors` 记录其读取位置，而非每个人一条消息行 |

---

## 推荐实施顺序

| 优先级 | 方向 | 理由 |
|--------|------|------|
| **P0** | 大文件端到端完整性验证（#1） | 文件服务的核心数据正确性保证；当前已知 gap；修复成本低但影响大 |
| **P0** | 分布式速率限制（#4） | 生产多副本部署的必要条件；当前单实例限制形同虚设；防止滥用是社交产品的基础 |
| **P1** | 消息附件生命周期（#3） | 影响用户体验和数据完整性；与文件服务耦合较深，建议在 P0 之后立即跟进 |
| **P1** | 多设备同步（#2） | 产品化的必经之路；依赖 #4 的 SSE 基础设施已就绪，新增工作量主要在设备注册和 Push |
| **P2** | 对话抽象与群组（#5） | 新增功能而非修复缺陷；架构变更较大，建议在前 4 项稳定后启动 |

---

## 与 ROADMAP 的关联

这份分析与 `ROADMAP.md` 的关系：
- **ROADMAP 的所有 5 个方向**（可观测性、SSO 弹性、文件管道、写入正确性、产品表面）及其 **production hardening 波**（速率限制、gRPC ops、编辑、审计等）**已全部交付**。
- 本文件推荐的 5 个方向是 **ROADMAP 完成后的下一阶段**，基于当前代码库的实际状态（而非历史计划），并排除了已实现的项目。
- 其中方向 #4（分布式速率限制）是对 ROADMAP 中 production hardening 中速率限制的自然增强——从 single-instance 到 distributed。
