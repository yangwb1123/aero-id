# 后 MVP 阶段高价值扩展方向

> **作者**: Architecture / PM Review  
> **日期**: 2026-07-11  
> **基线**: `main` 分支全量代码扫描（P0-P2 全部交付后）  
> **范围**: 不写代码，仅识别扩展方向、边界情况与性能优化机会

---

## 当前状态摘要

aero-id 已经是一个非常成熟的 Go 服务。回顾 22 个迁移文件和 200+ Go 源文件的实现情况：

| 领域 | 已实现 |
|------|--------|
| **文件** | CRUD、流式上传/下载、presigned 直传 S3、checksum 去重、尺寸/类型校验、显式共享（per-user grant）、可见性控制、storage GC（软删清理 + 孤儿 blob 扫描）、引用计数、元数据更新、审计日志 |
| **消息** | 发送/收件箱/会话、已读回执、编辑/软删、Reactions、全部标记已读、未读数统计、Block 防护、系统消息、Outbox 事件 |
| **评论** | 两层嵌套、Like/Unlike、举报/屏蔽/取消屏蔽、编辑、软删、审核队列（查看/处理）、Outbox 事件（atomic reply_count bump） |
| **通知** | Outbox 驱动推送、Block 感知、偏好开关（含静默时段）、速率限制、合并折叠、SSE 实时推送（含 last-event-id 断线重放）、未读数、全标已读 |
| **搜索** | PostgreSQL FTS 全文搜索（消息/评论/文件） |
| **管理** | 角色/权限体系（moderator/admin）、审计日志查询 |
| **可观测性** | RED 指标（每路由 HTTP + gRPC）、OpenTelemetry 追踪（跨 SSO）、READY 探测（DB + SSO） |
| **韧性** | SSO 熔断器、serve-stale 降级、单飞合并、限次退避重试、TLS/mTLS + 证书锁定、gRPC keepalive |
| **可靠性** | 事务性 Outbox、幂等键、原子计数器、引用计数 |
| **限流** | 每 actor / 每 IP 令牌桶（HTTP 429 + gRPC ResourceExhausted） |
| **实时** | SSE Hub（多设备支持、非阻塞推送、心跳保活） |
| **迁移** | 内建 migrate CLI、可选启动时自动迁移 |

**结论**：基础架构非常扎实。下文提出的方向不是修漏洞，而是**跨入下一阶段的能力增强**。

---

## 方向一：回收站 / 数据恢复（Trash Bin）

### 为什么需要

所有实体（文件、消息、评论）均使用 GORM 软删除——`deleted_at` 列存在、索引存在、数据未丢——但**用户无法恢复**。这是最直观的用户体验缺口：

- 用户误删文件 → 目前只能联系运维查库恢复
- `storage.GC` 定时清理软删行，一旦跑过就永远丢失
- 消息软删后发件人看不到（repo 默认 scope 隐藏），收件人也失去上下文

### 边界情况

| 场景 | 处理策略 |
|------|----------|
| 恢复后与现有实体的关联 | 文件恢复后，被共享者是否自动恢复读权限？消息恢复后会话顺序如何？建议恢复时还原 `deleted_at = NULL`，权限由当前 live 关联决定 |
| GC 竞争 | 回收站中的文件应在 GC 清理名单中排除（或延长保留窗口）。`storage.GC` 已使用 `Max(deleted_at, recovered_at)` 判断 |
| 保留期限自动清空 | 回收站行超过 TTL（如 30 天）后自动永久删除；可配置，默认关闭 |
| 空间配额计数 | 回收站中文件应占用用户配额（否则可绕过存储限制） |

### 实现要点

- 三条新 API：`GET /v1/trash`（列出已删资源）、`POST /v1/trash/:type/:id/restore`（恢复）、`DELETE /v1/trash/:type/:id`（永久删除）
- 修改 `storage.GC`：跳过未过保留期的软删行，或增加 `is_in_trash` 列
- 消息和评论的恢复需要校验目标实体仍存在（如恢复对已删文件的评论）
- 跨实体恢复：恢复文件时，其共享记录和引用该文件的消息不应自动恢复

### 预估收益

用户体验大幅提升——误删有救。软删架构已就位，增量成本低。

---

## 方向二：全局审核控制台（Global Moderation Console）

### 为什么需要

当前审核模型是**作者自管理**：评论作者可以看到举报列表并自行处理（隐藏或驳回）。这对一个开放平台远远不够：

- `admin` 和 `moderator` 角色已建表（roles + user_roles），权限常量已定义（`content.moderate`、`user.ban`），但**没有 API** 来使用它们
- 没有全局举报队列（跨所有用户，按时间/严重性排序）
- 无法禁止用户、无法全局隐藏/删除内容、无法广播系统通知

### 边界情况

| 场景 | 处理策略 |
|------|----------|
| 管理员与作者的权限冲突 | 作者隐藏后管理员可见；管理员强制删除后作者不可恢复。采用"最严格者优先"原则 |
| 封禁用户后的数据 | 封禁应阻止新内容写入，已有内容可选择保留或标记隐藏。outbox 通知应跳过被封禁者 |
| 操作审计 | 所有管理操作必须落 audit_events 表，且管理操作的审计日志不能被非 admin 查询 |
| 申诉流程 | 预留 `appeal_at` 字段位置，Phase 2 实现 |

### 实现要点

- `GET /v1/admin/reports` — 全局举报队列（支持 status、target_type、时间范围过滤）
- `POST /v1/admin/reports/:id/resolve` — 管理员跨作者审核
- `POST /v1/admin/users/:uid/ban` / `POST /v1/admin/users/:uid/unban` — 用户封禁（需要有 `ban_user` 表或标记）
- `POST /v1/admin/comments/:id/hide` — 管理员强制隐藏（跨作者）
- 中间件 `RequirePermission` 已存在，只需挂载到新路由

### 预估收益

运营团队不必登录数据库做审核操作。配合方向三的 AI 预处理，可形成"AI 过滤 → 人工确认"的完整审核链路。

---

## 方向三：异步 AI 内容审核管线（AI Moderation Pipeline）

### 为什么需要

现有 outbox 基础设施（事件 → relay → handler）为异步处理提供了完美基座。平台一旦开放 UGC（用户生成内容），有毒内容、垃圾信息、敏感信息就会成为首要风险：

- 举报目前是纯人工（用户举报 → 作者自行处理），无自动预筛
- 写入路径不检查内容安全性
- `files.meta`、`messages.meta`、`comments.content` 三个字段均可加入 AI 审核结果
- 服务名"aero-id"暗示可能对接 AI agent——应该预埋审核能力

### 边界情况

| 场景 | 处理策略 |
|------|----------|
| 高吞吐量 | 审核是异步的，先写入后审核；审核完成前内容对外可见（"先发布后审核"），或先隐藏待审（"先审后发"，需配置化） |
| 审核超时 / LLM 不可用 | 降级为放行（允许发布）或默认拒绝（看产品策略），通过 circuit breaker 隔离 LLM 依赖 |
| 多模态审核 | 文本（message/comment content）→ LLM；图片/文件（MIME type 匹配）→ 视觉模型 |
| 误判 | 用户可申诉审核结果；管理员可覆盖 AI 决策 |

### 实现要点

- `internal/moderator/` 新包：定义 `Moderator` 接口（`Check(ctx, content) -> Result`），先期实现一个 logging no-op 实现，后续接入具体 LLM
- outbox 新 handler：`ModeratorHandler` 插入 outbox 链（relay 中已有 chain 模式），监听 `message.sent`、`comment.replied`、`file.uploaded` 等事件
- `content_reviews` 表（或复用 `meta`）：记录审核结果、置信度、处理时间
- 审核结果可通过 outbox 反向通知发布者（"你的内容被标记"），也可自动调用 `Hide` 操作
- 可配置审核范围（仅文本/全量/关闭）、审核策略（放行/拦截/标记）

### 预估收益

从纯人工审核向 AI 辅助审核跃迁。outbox 基础设施无需改造，接入成本低。

---

## 方向四：多组织 / 数据空间（Multi-Organization Data Spaces）

### 为什么需要

当前数据模型是纯扁平按用户归属。当 aero-id 需要支撑团队协作场景（如项目文件共享、团队消息空间）时，缺少一个**组织级数据隔离层**：

- 文件共享目前是 per-file grant，没有"团队文件夹"概念
- 消息局限于点对点，没有群组/频道
- 评论依附于文件或消息，缺少"团队空间留言板"
- `visibility` 只有 private/public 二值，没有 "org-internal" 中间态

### 边界情况

| 场景 | 处理策略 |
|------|----------|
| 角色访问控制 | 组织内需要 owner / admin / member / guest 等角色，可复用现有 roles 表（扩展 scope 列） |
| 成员加入/离开 | 离开后已有共享内容应如何处理？继承 ACL 规则：保留读权限，移除写权限 |
| 嵌套组织 | 支持 parent_org_id 实现层级，但 Phase 1 仅支持平层 |
| 存储隔离 | 组织级文件应隔离到不同 storage prefix（S3 key prefix 或 local 子目录） |

### 实现要点

- `organizations` 表 + `org_members` 表
- 全领域模型增加可选 `org_id` 列（UUID，可为 NULL），查询时追加 `WHERE org_id IS NULL OR org_id = ?`
- 文件 storage key 调整为 `<org_id>/<owner_uid>/<yyyy>/<mm>/<uuid>`
- 消息扩展为 `room_id`（支持群聊），可以是 org 内的房间
- 评论扩展 `target_type = "org_file"` / `"org_message"`
- 权限检查：组织内成员自动继承 org 级别可见性
- 迁移策略：所有现有数据 `org_id = NULL` 兼容单用户模式

### 预估收益

从个人数据服务升级为团队协作平台的基础设施。这是体系结构上最大的跃迁，建议作为长期战略方向。

---

## 方向五：文件版本历史（File Version History）

### 为什么需要

文件去重（checksum dedup）当前会让同名同内容的文件自动复用 blob。但当用户上传**同名但不同内容**的文件时，旧 blob 会被新 blob 覆盖（`storage.Key` 相同），版本丢失：

- 无版本回退能力
- 协作场景下"谁在什么时候改了啥"不可追溯
- 与方向四（组织协作）组合时，版本历史是刚需

### 边界情况

| 场景 | 处理策略 |
|------|----------|
| 去重与版本 | 同一 checksum 不上传新 blob（去重），但可记录一个新版本指向同一 blob（节省存储） |
| 版本数量爆炸 | 限制版保留数（如最近 50 版），超过时自动归档或删除最旧版本 |
| 存储成本 | 每个版本需独立 blob（除非 checksum 相同）；大文件多版本存储可能昂贵，需要配额 |
| 共享与版本 | 文件共享后，新版本自动对共享者可读？是的——共享是对文件实体，而非某一版本 |
| 删除 | 删除文件应同时删除所有版本（引用计数降至 0 时清除 blob） |

### 实现要点

- `file_versions` 表：`(file_id, version_number, storage_key, size, checksum, mime_type, created_at)`，加唯一索引 `(file_id, version_number DESC)`
- `files` 表增加 `current_version` 列（INT，默认 1）
- `Create` 逻辑：检测同名文件 → 若 content 变化则创建新版本，更新 `current_version`，**不覆盖**原 blob
- `Get` + `DownloadURL`：默认返回当前版本，支持 `?version=N` 参数
- 新增 API：`GET /v1/files/:id/versions`（列表）、`POST /v1/files/:id/versions/:version/restore`（回退）
- storage GC 感知版本引用计数，孤儿版本也可被清理

### 预估收益

最直接的用户数据安全与协作增强。文件平台无版本历史是一个公认的能力缺口。

---

## 性能优化机会（非独立方向，穿插在上述方向中）

以下优化点规模较小，可在实现上述方向时一并完成：

### O1：通知列表查询性能

**当前**：按 `created_at DESC` cursor 扫描，未读过滤无复合索引。百万行级别的通知表可能导致全表扫描。

**建议**：为 `notifications` 加复合索引 `(recipient_uid, read_at IS NULL, created_at DESC)`，使未读过滤走 Index Only Scan。这是迁移 `000015` 之后的又一瓶颈索引。

### O2：Outbox 轮询延迟与空转

**当前**：relay 使用固定间隔（默认 5s）轮询 `FetchUnpublished`。低负载下每 5s 一次空查询，高负载下可能有秒级延迟。

**建议**：实现指数退避空转——连续 N 次空结果后递增轮询间隔（5s → 10s → 30s），检测到新事件后立即恢复短间隔。同时配合 `LISTEN/NOTIFY` 实现事件驱动唤醒，消除轮询延迟。

### O3：SSE 连接反向压力

**当前**：SSE Hub 每个订阅者分配 16 个缓冲槽。慢消费连接事件被静默丢弃。

**建议**：增加可配置的背压策略（慢连接自动断开 or 降级为轮询建议），并在 Hub 层暴露连接数指标用于监控。

### O4：文件上传带宽管理

**当前**：`cappedReader` 阻止超尺寸上传，但对并发上传无速率限制。多路并发大文件上传可能耗尽出口带宽。

**建议**：可选并发上传额度（同一 actor 同时进行中的上传数上限），通过 service 层的 `inflight.Done()` / `inflight.Acquire()` 控制。

---

## 总结与推荐优先级

| 方向 | 类型 | 优先级 | 工作量 | 理由 |
|------|------|--------|--------|------|
| **回收站 / 数据恢复** | 功能 + 边界 | **P0** | 低 | 软删架构已就绪，用户呼声最高 |
| **全局审核控制台** | 功能 + 运营 | **P1** | 中 | 角色系统已完成，仅缺路由 + handler |
| **AI 审核管线** | 功能 + 架构 | **P1** | 中-高 | Outbox 基座完美，依赖少量新表 + handler |
| **多组织 / 数据空间** | 架构 | **P2** | 高 | 最大架构变更，建议充分设计后再开工 |
| **文件版本历史** | 功能 | **P2** | 中 | 文件方向的核心缺失能力 |

其中方向一（回收站）和方向二（审核控制台）可在不影响现有架构的前提下快速产出价值。方向三依赖方向二的 admin/mod 基础设施。方向四和五适合作为下一阶段的产品战略。
