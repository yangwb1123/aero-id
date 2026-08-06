# aero-id — 深度扩展方向分析

> **作者**: 资深架构师 / PM 视角  
> **日期**: 2026-07-10  
> **范围**: 基于 `main` 分支全局代码扫描（截止当前）  
> **方法**: 零代码变更，纯分析；每项均附代码依据  
> **声明**: 本文件与 `docs/requirements/expansion-directions.md` 互补。该文件覆盖了群组对话、Webhook 订阅 API、多副本扩展、WebSocket、存储配额 5 个方向；本文聚焦另外 5 个未被系统分析的产品领域。

---

## 目录

1. [多表情反应体系](#1-多表情反应体系)
2. [会话模型统一与收件箱重构](#2-会话模型统一与收件箱重构)
3. [自动化内容安全与滥用防御](#3-自动化内容安全与滥用防御)
4. [文件预处理与缩略图管线](#4-文件预处理与缩略图管线)
5. [审计告警与异常行为检测](#5-审计告警与异常行为检测)

---

## 1. 多表情反应体系

### 代码依据

当前反应系统有两个独立实现，**均不支持多表情**：

| 实体 | 表 | 反应列 | 语义 | 文件 |
|---|---|---|---|---|
| 评论 | `comment_likes` | `(comment_id, uid)` | 仅有"赞"（布尔值），`like_count` 是 `int` | `000005_create_comment_likes.up.sql` |
| 消息 | `message_reactions` | `(message_id, uid)` | 仅有"存在"（有/无反应），无反应类别 | `000013_message_reactions.up.sql` |

在 `internal/domain/comment.go` 中：

```go
type Comment struct {
    ...
    LikeCount  int    // 只有一维计数
    ...
}
```

在 `internal/service/message.go` 中，`ReactToMessage` / `UnreactMessage` 只接受 `actorUID`，没有 reaction_type 参数。

**结论**：当前产品层面，用户对任何内容只能表达"赞"或"无"——没有 😂 ❤️ 😢 😡 👍 等多元化情绪表达。

### 为什么需要

1. **产品竞争力**：2026 年的社交/协作产品中，多表情反应是标配（Slack、Discord、Telegram、GitHub）。缺少这一能力，aero-id 在用户体验上会明显落后于竞品。
2. **互动深度**：单一"赞"无法区分"同意""感动""好笑""愤怒"等情绪，限制了用户表达方式，也缩小了内容互动的数据维度。
3. **与通知系统整合**：不同表情可以触发不同级别的通知（❤️ 可能比 👍 更值得推送）。

### 设计要点

```sql
-- 统一 emoji_reactions 表（替代 comment_likes 和 message_reactions）
CREATE TABLE emoji_reactions (
    resource_type   TEXT NOT NULL,  -- 'comment' | 'message' | 'file'
    resource_id     UUID NOT NULL,
    uid             TEXT NOT NULL,
    reaction_type   TEXT NOT NULL,  -- 'thumbsup', 'heart', 'laugh', 'cry', 'angry', ...
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id, uid, reaction_type)
);
```

1. **统一模型**：一张表覆盖所有资源类型（comments/messages/files），按 `resource_type` 多态查询。
2. **去重策略**：同一用户对同一资源可添加多种表情，但每种表情只有一个（`PRIMARY KEY` 覆盖四列）。
3. **计数优化**：`comment.like_count` 保持为单一 `int`，但允许按 `reaction_type` 查询细分计数。可在服务层提供 `ReactionSummary` DTO：

   ```go
   type ReactionSummary struct {
       Total int                       // 总反应数
       Items map[string]ReactionCount  // reaction_type → {count, user_reacted}
   }
   ```

4. **迁移路径**：
   - 新表建好后，`comment_likes` 和 `message_reactions` 保持（向后兼容），新反应写入 `emoji_reactions`
   - 旧数据通过后台 Job 迁移（可选）
   - 服务层新增 `GetReactionSummary` 方法，在 `List` / `Get` 时注入
5. **通知整合**：
   - 当有人对用户的内容添加特定表情时，生成通知（与当前 `comment.replied` 等事件类似）
   - 可设置"高价值表情"（❤️ → 推送通知；👍 → 静默更新计数）

### 边界情况

| Edge Case | 处理方式 |
|---|---|
| 用户频繁切换表情 | 每次切换是独立的 upsert 操作，幂等 |
| 10k+ 人同一条评论 | `emoji_reactions` 按 `(resource_type, resource_id)` 有复合索引，排除软删 |
| 应删除的表情类型 | TOML/YAML 配置允许列表，服务启动时加载；未知类型返回 BadRequest |
| 通知风暴（一篇文章被万人点赞） | 复用现有的 `NotificationDispatcher` 速率限制 + 折叠（collapse）机制 |
| 软删资源上的反应 | 反应是资源的一部分：资源软删时反应保留（但列表不展示）；硬删时级联删除 |

---

## 2. 会话模型统一与收件箱重构

### 代码依据

当前消息系统的核心模型是**独立的 1:1 消息行**：

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    ...
}
```

收件箱查询是**按人**的：

```go
// internal/service/message.go
func (s *messageService) Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error) {
    // ListInbox 按收件人查询，返回的消息不分组
    msgs, next, err := s.messages.ListInbox(ctx, actor, page, unreadOnly)
    ...
}
```

**没有**以下概念：
- `conversations` 表（会话头信息）
- `conversation_members` 表（成员/角色/加入时间）
- `conversation.last_message_id` / `last_message_at` （最近一条消息的快照）
- `conversation.unread_count` 反范式加速

这意味着：
- 前端必须自行从原始消息中**重构**会话列表（DISTINCT on from_uid + to_uid）
- 无法支持群组对话（已有 `expansion-directions.md` 指出这一点）
- 无法支持会话级别的操作（置顶、静音、离开、设置头像/名称）
- 每条消息的已读跟踪独立，没有会话级别的 "Mark All Read"

### 为什么需要

1. **前端复杂度转移到了客户端**：每个客户端（Web、iOS、Android）都需要实现相同的"从原始消息重建会话列表"逻辑，容易出现不一致。
2. **会话级功能的基础**：没有会话实体，就无法实现置顶会话、静音会话、查看会话成员历史、多设备已读同步等 UX 功能。
3. **与群组对话紧密耦合**：群组功能的引入必然需要一个会话实体。当前 `expansion-directions.md` 中"群组对话"方向已提出群组模型，但 1:1 会话同样需要这个实体来提供一致的产品体验。
4. **性能优化**：当前 `Inbox` 查询返回所有消息，前端需要去重。服务端提供预计算的会话列表可大幅降低数据量（20 条消息 → 1 条会话摘要）。

### 设计要点

```sql
CREATE TABLE conversations (
    id              UUID PRIMARY KEY,
    kind            TEXT NOT NULL DEFAULT 'direct',  -- 'direct' | 'group' | 'channel'
    name            TEXT NOT NULL DEFAULT '',
    avatar_key      TEXT,        -- 指向 files.storage_key 或外部 URL
    owner_uid       TEXT,        -- group/channel 的创建者；direct 可为空
    last_message_id UUID REFERENCES messages(id),
    last_message_at TIMESTAMPTZ,
    last_content    TEXT,        -- 最近一条消息的摘要（<= 100 字符）
    last_from_uid   TEXT,        -- 最近消息的发送者
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    deleted_at      TIMESTAMPTZ
);

CREATE TABLE conversation_members (
    conversation_id UUID NOT NULL REFERENCES conversations(id),
    member_uid      TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'member',  -- 'admin' | 'member'
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_read_at    TIMESTAMPTZ,
    muted_until     TIMESTAMPTZ,   -- NULL = not muted; future = muted until
    pinned          BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (conversation_id, member_uid)
);
```

1. **1:1 会话自动创建**：当 A 首次向 B 发送消息时，自动创建一个 `direct` 类型的 `conversation` 并添加双方成员。`last_message_*` 列随每次发送原子更新。
2. **群组会话手动创建**：先创建 `conversation` + 初始成员列表，然后开始发消息。
3. **收件箱重构**：`Inbox` 端点改为返回 `[]*ConversationSummary`（包含 `last_message` 快照 + `unread_count`），而非返回原始消息。新端点 `GET /v1/conversations`。
4. **消息列表端点**：`GET /v1/conversations/:id/messages` —— 按会话查看消息历史（替代当前的 `Conversation` 端点）。
5. **已读跟踪**：`conversation_members.last_read_at` 配合消息查询的 `WHERE created_at <= last_read_at` 实现会话级已读。
6. **与现有端点向后兼容**：保持 `GET /v1/conversations/:uid` 别名（自动路由到自动创建的 direct 会话）。

### 边界情况

| Edge Case | 处理方式 |
|---|---|
| A 屏蔽 B 后，双方会话 | 会话存在但消息不可达（block 检查在 Send/List 层）；会话列表中不展示来自被屏蔽方的未读 |
| 用户删除某条消息后会话更新 | `last_message_id` 在删除时需检查是否是最新消息，若是则回退到前一条 |
| 用户离开群组后重新被邀请加入 | 保留原 `conversation_members.deleted_at`，恢复时复用旧行 |
| 大量会话导致 LIST 查询慢 | `last_message_at DESC` 索引 + 用户级分页（cursor = last_message_at） |
| migration 回填旧数据的 1:1 会话 | 后台 Job 扫描 `messages` 表，按 `(LEAST(from_uid,to_uid), GREATEST(from_uid,to_uid))` 分组创建 conversation + members |

---

## 3. 自动化内容安全与滥用防御

### 代码依据

当前 aero-id 的安全模型完全依赖**事后处置**和**入口限流**：

**已存在的防御手段**：
| 机制 | 位置 | 局限性 |
|---|---|---|
| 速率限制 | `middleware/ratelimit.go` | 只控制请求频率，不检查内容质量 |
| Block 系统 | `internal/block/` | 用户主动屏蔽他人，被动防御 |
| 评论举报 | `domain/comment.go:CommentReport` | 需要其他用户举报，延迟高 |
| 审核队列 | `service/comment.go:ResolveReport` | 仅评论作者可处理，无全局审核能力 |
| 管理角色 + 审计日志 | `000022_admin_roles.up.sql`, `repository/admin.go` | 只有 `ListAudit` 端点，**没有** 内容制裁端点（如封号、批量删除、暂停用户） |
| 文件 MIME 白名单 | `service/file.go:mimeAllowed` | 只检查 Content-Type 头，不检查实际文件内容 |
| 文件大小上限 | `service/file.go:cappedReader` | 限制体积，不检查内容是否恶意 |

**缺失的关键能力**：
- **无内容扫描**：上传的文件不经过病毒/恶意软件扫描，不检查是否包含敏感信息
- **无垃圾消息过滤**：`messages.content` 和 `comments.content` 无 Spam 检测
- **无用户信用体系**：没有对反复违规用户的自动制裁（临时/永久封禁、内容审核锁）
- **无全局审核端点**：管理员端点 `admin.go` 只有 `ListAudit`，无法执行 Hide/Delete/Suspend
- **无预防性风控**：短时间大量举报同一用户/文件 → 自动触发临时限制

### 为什么需要

1. **法律合规风险**：大量用户上传的文件/消息/评论中若包含非法内容（儿童虐待、版权侵权、仇恨言论），平台需要有能力**及时发现和处置**，否则面临法律责任。
2. **存量安全风险**：没有文件内容扫描，恶意用户可以通过简单的 MIME 伪装上传可执行文件、脚本、钓鱼页面。
3. **用户体验风险**：一个垃圾消息发送者可以瞬间向所有用户发送成千上万条垃圾消息——当前的速率限制按 uid 计算，但无法区分合法用户和滥用者。
4. **声誉风险**：一旦平台成为垃圾/恶意内容的温床，核心用户会流失。

### 设计要点

```sql
-- 用户制裁记录
CREATE TABLE user_sanctions (
    id              UUID PRIMARY KEY,
    uid             TEXT NOT NULL,
    sanction_type   TEXT NOT NULL,  -- 'suspend' | 'ban' | 'content_lock' | 'rate_limit_lowered'
    reason          TEXT NOT NULL,
    expires_at      TIMESTAMPTZ,    -- NULL = 永久
    applied_by      TEXT NOT NULL,  -- admin uid 或 'system'（自动触发）
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_sanctions_active ON user_sanctions(uid) WHERE expires_at IS NULL OR expires_at > now();
```

1. **多层次防御管线**（按成本和侵入性排序）：

   ```
   [请求到达] → [入口限流]（已有）
            → [Spam 评分]（新）→ 低分 → 延长审核/静默限流
            → [内容匹配]（新）→ 命中黑名单 → 拒绝
            → [文件扫描]（新）→ 可疑 → 隔离 + 运营告警
            → [业务逻辑]（已有）
            → [事后审计]（已有）
   ```

2. **Spam 检测策略**（无外部依赖，纯规则）：
   - 消息频率：同一用户每分钟发送消息数超过阈值 → Spam 分 +1
   - 重复内容：同一内容（sha256）在短时间内被不同用户发送 → Spam 分 +5
   - 链接密度：消息中 URL 占比过高 → Spam 分 +2
   - 举报聚集：短时间内同一用户被 N 个不同用户举报 → 自动触发临时内容锁

3. **文件扫描集成**：
   - 在 `file.Create` 和 `file.CompleteUpload` 中，写入后异步触发扫描（ClamAV / 第三方 API）
   - 扫描结果写入 `file.meta` 字段（`scanned_at`, `scan_status`, `scan_result`）
   - 扫描完成前文件标记为 `processing`，前端提示"正在扫描…"
   - 扫描发现恶意内容 → 自动软删 + 通知文件 owner + 写入审计

4. **管理端点**：
   | 端点 | 权限 | 功能 |
   |---|---|---|
   | `POST /v1/admin/users/:uid/sanction` | `user.ban` | 创建制裁记录 |
   | `DELETE /v1/admin/users/:uid/sanction/:id` | `user.ban` | 解除制裁 |
   | `GET /v1/admin/users/:uid/sanctions` | `audit.read` | 查看历史制裁 |
   | `POST /v1/admin/comments/:id/hide` | `content.moderate` | 全局隐藏评论 |
   | `POST /v1/admin/messages/:id/delete` | `content.moderate` | 全局删除消息 |
   | `GET /v1/admin/flagged-content` | `content.moderate` | 待审核内容队列 |

5. **与 Rate Limiter 整合**：
   - 受制裁用户的 uid 被注入一个**更严格的 token bucket**（例如从 10rps 降至 1rpm）
   - `middleware/ratelimit.go` 在 `rateLimitKey` 中查询 active sanctions

### 边界情况

| Edge Case | 处理方式 |
|---|---|
| 误判：合法用户被自动制裁 | 设置的制裁是"临时"（如 1h），用户可以通过联系管理员申诉；审计日志记录触发原因 |
| 文件扫描延迟导致用户困惑 | 前端在 `file.meta` 中查询 `scan_status`；未完成时显示"正在处理" |
| 攻击者通过大量举报搞垮竞争对手 | 举报也计入 Spam 评分：短时间内举报大量不同用户 → 举报者 Spam 分上升 |
| 制裁过期后自动恢复 | `user_sanctions.expires_at` 到期后，`middleware/permission.go` 自动跳过制裁检查 |
| 被 ban 的用户创建新账号 | 如果需要防止，则需要 SSO 层集成（aero-id 无账号表，无法强制） |

---

## 4. 文件预处理与缩略图管线

### 代码依据

当前文件上传管线是**纯存储**导向的——文件存进去，就完了：

```go
// file/service_crud.go — Create 方法
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    ...
    key, size, err := s.store.Put(ctx, actor, in.Name, reader)
    checksum := hex.EncodeToString(hasher.Sum(nil))  // SHA-256 只在存储时计算一次
    ...
    f := &domain.File{
        ...
        Checksum: checksum,  // 仅用于存储去重，不用于内容识别
    }
    ...
}
```

文件模型中的 `Meta` 字段（`map[string]any`）预留了扩展空间，但没有被任何预处理填充：

```go
// domain/file.go
type File struct {
    ...
    Meta   map[string]any  // 未被任何系统写入（除了 owner 通过 UpdateMetadata 写入的字段）
    ...
}
```

**缺失的能力**：
- 上传图片 → 无缩略图（需前端下载原图）
- 上传视频 → 无首帧预览
- 上传文档（PDF/DOCX）→ 无可提取的文本/页码
- 任何文件 → 无病毒扫描（已在方向 3 中讨论）
- 图片 → 无 EXIF 元数据提取（GPS、相机、时间戳）
- 文件 → 无内容类型二次确认（仅信赖客户端的 Content-Type）

### 为什么需要

1. **核心体验缺陷**：文件上传是 aero-id 的核心功能之一，但用户上传图片后，在任何列表（`ListByOwner`、`ListSharedWithMe`）中都看不到缩略图。消息中的图片附件也无法内联预览。这是产品体验上最明显的缺口之一。
2. **带宽与加载性能**：没有缩略图意味着列表页需要下载完整文件才能展示内容——对于照片/截图动辄几 MB 的情况，这极度浪费带宽且加载缓慢。
3. **元数据搜索潜力**：如果文件上传后自动提取文字（OCR/PDF 文本），FTS 搜索可以覆盖文件内容而不仅仅是文件名（当前搜索只索引文件名 `to_tsvector('simple', coalesce(name, ''))`）。
4. **安全合规**：EXIF 数据可能包含 GPS 坐标等敏感个人信息；用户上传照片后平台应有能力检测并提示/清除这些元数据。

### 设计要点

```sql
-- 文件预处理任务队列（与 outbox 模式一致）
CREATE TABLE file_processing_jobs (
    id          UUID PRIMARY KEY,
    file_id     UUID NOT NULL REFERENCES files(id),
    job_type    TEXT NOT NULL,  -- 'thumbnail' | 'scan' | 'metadata' | 'ocr' | 'transcode'
    status      TEXT NOT NULL DEFAULT 'pending', -- 'pending' | 'processing' | 'done' | 'failed'
    result      JSONB,          -- 处理结果（缩略图 storage_key、扫描结论等）
    retry_count INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

1. **异步处理架构**：
   ```
   [文件上传完成] → [写入 file_processing_jobs 行]
                → [独立 Worker 轮询 job 表]
                → 每个 job_type 由专用 handler 处理
                → 结果写回 files.meta（或单独的 thumbnails 表）
   ```

2. **缩略图生成**：
   - 使用 `imaging` 或 `vips` 库为图片生成多种尺寸（`sm: 256x256`, `md: 512x512`, `lg: 1024x1024`）
   - 缩略图存入同一存储后端，key 为 `{owner}/thumb_{size}_{file_storage_key}`
   - 视频提取首帧（使用 `ffmpeg`）
   - 生成后的缩略图 key 写入 `file.meta.thumbnails`

3. **元数据提取**：
   ```json
   // file.meta 在处理后填充
   {
     "original": {"name": "photo.jpg", "type": "image/jpeg"},
     "image": {
       "width": 4032, "height": 3024,
       "orientation": 1,
       "exif": {
         "make": "Apple", "model": "iPhone 15",
         "gps_lat": 37.7749, "gps_lng": -122.4194,
         "datetime_original": "2026-07-10T12:00:00Z"
       }
     },
     "thumbnails": {
       "sm": "u1/thumb_sm_abc123.jpg",
       "md": "u1/thumb_md_abc123.jpg",
       "lg": "u1/thumb_lg_abc123.jpg"
     },
     "scan": {
       "status": "clean",
       "scanned_at": "2026-07-10T12:00:05Z"
     }
   }
   ```

4. **API 端点**：
   - `GET /v1/files/:id/thumbnail?size=sm` —— 返回缩略图（presigned URL 或直接流式返回）
   - `GET /v1/files/:id/preview` —— 返回文件预览元数据（文档页数、视频时长、图片分辨率等）

5. **与搜索整合**：
   - OCR handler 提取图片中的文字，写入 `files.search_vector`（需要将 `search_vector` 定义为 `regconfig` 重新生成）
   - PDF/DOCX 文本提取后追加到搜索索引
   - 当前 `000016_fts_indexes.up.sql` 只索引文件名，预处理后可以覆盖文件内容

### 边界情况

| Edge Case | 处理方式 |
|---|---|
| 超大图片（全景图 100MB+） | 先 resize 到安全尺寸再生成缩略图；原图保持不动 |
| 处理 job 失败重试耗尽 | 标记为 `failed`，不阻塞文件读取；运营可触发重试 |
| 文件在缩略图生成前被删除 | job 轮询时检查 `files.deleted_at`，若已删除则跳过 |
| 非图片/视频文件 | 不生成缩略图，但执行其他处理（文档 → 文本提取；其他 → 仅扫描） |
| 同一文件多次上传（checksum 去重） | 去重后共享预处理结果；`file_processing_jobs` 按 checksum 去重 |
| 用户隐私：EXIF GPS 信息暴露 | 默认在返回给客户端的元数据中**移除** GPS 字段（仅在 Meta 中保留给 owner 查看） |

---

## 5. 审计告警与异常行为检测

### 代码依据

审计日志系统已经相当完整：

```sql
-- 000014_create_audit_log.up.sql
CREATE TABLE audit_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_uid    TEXT NOT NULL,
    action       TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    target_uid   TEXT,
    meta         JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

且已有 7 种审计事件类型被写入：

```go
// domain/audit.go
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"
)
```

但**审计日志的使用止步于被动查询**：

```go
// handler/http/admin.go
func (h *AdminHandler) ListAudit(c *gin.Context) {
    // 只有查询端点，没有告警/分析
    events, next, err := h.adminRepo.ListAudit(c.Request.Context(), action, page)
    ...
}
```

**缺失的能力**：
- 无规则引擎：不能在审计事件流上定义"如果 X 事件在 Y 时间内出现 N 次，则触发告警"
- 无告警通道：即使检测到异常，也无通知机制（告警应发往运营团队）
- 无异常评分：无法给用户/操作打"风险分"来辅助决策
- 无聚合视图：没有按 action / actor / target 聚合的审计面板

### 为什么需要

1. **安全运营闭环**：审计日志的终极价值不在于"能查"，而在于"能及时发现异常"。被动查询意味着安全事件只能事后追溯，无法实时响应。
2. **内部威胁检测**：恶意或误操作的管理员可能批量删除大量内容或批量导出文件——当前没有任何机制在破坏发生时发出警报。
3. **合规要求**：SOC2 / ISO27001 等合规标准要求组织具备"监控和告警"能力，仅有日志存储是不够的。
4. **自动化的成本控制**：方向 5（存储配额）实施后，需要检测异常的文件上传/分享模式来防止配额滥用。

### 设计要点

```sql
-- 告警规则
CREATE TABLE alert_rules (
    id              UUID PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT,
    action          TEXT NOT NULL,              -- 监控哪个 audit action
    threshold       INT NOT NULL,               -- 阈值（次数）
    window_seconds  INT NOT NULL,               -- 时间窗口（秒）
    scope           TEXT NOT NULL DEFAULT 'actor', -- 'actor' | 'global' | 'target'
    severity        TEXT NOT NULL DEFAULT 'warning', -- 'info' | 'warning' | 'critical'
    enabled         BOOLEAN NOT NULL DEFAULT true,
    notify_uids     TEXT[] NOT NULL DEFAULT '{}', -- 告警通知给谁
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 告警事件
CREATE TABLE alert_events (
    id              UUID PRIMARY KEY,
    rule_id         UUID NOT NULL REFERENCES alert_rules(id),
    action          TEXT NOT NULL,
    actor_uid       TEXT,          -- 触发告警的操作者（scope=actor 时）
    count           INT NOT NULL,  -- 窗口内实际次数
    threshold       INT NOT NULL,  -- 触发阈值
    window_seconds  INT NOT NULL,
    detail          JSONB,         -- 触发的具体事件 ID 列表
    acknowledged_at TIMESTAMPTZ,   -- 运营确认时间
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

1. **规则引擎设计**：

   ```
   [审计日志写入] → [规则匹配器（异步）] → [窗口计数器]
       ↓                                       ↓
   [告警事件表] ← [阈值触发]                  [TTL 过期自动清除]
       ↓
   [通知渠道] → dispatcher → 运营通知 / Webhook / 内部告警
   ```

2. **内置规则模板**（启动时 SEED，可编辑）：

   | 规则 | Action | 窗口 | 阈值 | 严重度 |
   |---|---|---|---|---|
   | 批量文件删除 | `message.delete` / 按 `actor_uid` | 5min | 50 | critical |
   | 批量分享 | `file.share.grant` / 按 `actor_uid` | 5min | 30 | warning |
   | 多次违规举报 | `comment.report.resolve` / 按 `target_uid` | 1h | 10 | critical |
   | 频繁屏蔽/解封 | `user.block` + `user.unblock` / 按 `actor_uid` | 10min | 20 | info |
   | 审计日志空窗期 | global | 24h | 0 | warning |

3. **与现有通知系统整合**：
   - 告警事件触发后，通过已有的 `NotificationDispatcher` 发送给 `alert_rules.notify_uids` 中配置的管理员
   - 也可以通过 `Outbox` Webhook 发给外部 SIEM（安全信息和事件管理系统）

4. **运营控制面**（管理员端点）：
   | 端点 | 权限 | 功能 |
   |---|---|---|
   | `GET /v1/admin/alerts/rules` | `system.config` | 列出告警规则 |
   | `POST /v1/admin/alerts/rules` | `system.config` | 创建规则 |
   | `PATCH /v1/admin/alerts/rules/:id` | `system.config` | 更新规则 |
   | `DELETE /v1/admin/alerts/rules/:id` | `system.config` | 删除规则 |
   | `GET /v1/admin/alerts/events` | `audit.read` | 告警事件列表 |
   | `POST /v1/admin/alerts/events/:id/acknowledge` | `audit.read` | 确认告警 |

5. **审计分析面板**（轻量，无需外部 BI 工具）：
   - `GET /v1/admin/audit/stats` —— 按 action 聚合的事件计数
   - `GET /v1/admin/audit/stats/users` —— 按 actor_uid 聚合的操作排行
   - `GET /v1/admin/audit/heatmap` —— 按小时的操作热力图

### 边界情况

| Edge Case | 处理方式 |
|---|---|
| 告警风暴（规则触发后短时间内又触发多次） | 同一规则在 `alert_events.created_at + Cooldown` 内不重复告警（默认 1h cooldown） |
| 规则匹配延迟 | 窗口计数器使用异步+TTL，允许至多 1 分钟延迟；关键规则可配置同步匹配 |
| 大量规则全量扫描性能 | 规则按 action 索引，每个 audit 事件只匹配 `alert_rules WHERE action = ?`（少量规则） |
| 运营人员调整规则后不生效 | 规则缓存 TTL 5 分钟；支持手动刷新 |
| 误告警导致运营疲劳 | 阈值+严重度分级：critical 告警才发紧急通知，info/warning 归入日报 |

---

## 综合优先级矩阵

| 方向 | 用户价值 | 运营价值 | 合规价值 | 技术复杂度 | 耦合度 | 推荐优先级 |
|---|---|---|---|---|---|---|
| **1. 多表情反应** | 🔥🔥🔥🔥🔥 | — | — | 低 | 低（独立新表） | **P1** — 快速迭代 |
| **2. 会话模型统一** | 🔥🔥🔥🔥🔥 | 🔥🔥 | — | 高 | 高（核心数据模型变更） | **P2** — 与群组对话同步 |
| **3. 内容安全/滥用防御** | 🔥🔥 | 🔥🔥🔥🔥🔥 | 🔥🔥🔥🔥🔥 | 中 | 中（跨多层） | **P1** — 生产安全底线 |
| **4. 文件预处理管线** | 🔥🔥🔥🔥🔥 | 🔥🔥🔥 | 🔥🔥 | 高 | 低（独立 Worker） | **P2** — 缩略图是体验刚需 |
| **5. 审计告警/异常检测** | — | 🔥🔥🔥🔥🔥 | 🔥🔥🔥🔥🔥 | 中 | 低（审计之上增量） | **P1** — 安全运营闭环 |

### 执行建议

**Sprint 1-2（立即启动）**：
- **方向 3**（内容安全）：先落地 Spam 评分 + 管理端点（无需外部依赖），文件扫描可延后
- **方向 5**（审计告警）：先实现规则引擎内核 + 内置规则模板，告警通知复用现有 dispatcher

**Sprint 3-4**：
- **方向 1**（多表情反应）：核心代码量小（新表 + 服务层 + handler），适合快速交付
- **方向 4**（文件预处理）：启动 Worker 框架 + 图片缩略图，其余 job_type 迭代追加

**Sprint 5+**：
- **方向 2**（会话模型统一）：数据量大，需要先完成旧数据的 migration backfill；建议与群组功能（`expansion-directions.md` 方向一）合并为一个发布

---

## 附录 A：代码扫描中发现的额外边界情况（已有功能的处理建议）

这些是当前已有功能中尚未被覆盖或未充分处理的边界情况：

| # | 边界情况 | 涉及功能 | 代码位置 | 建议 |
|---|---|---|---|---|
| A1 | 文件去重竞争：两个用户同时上传同一 checksum 文件 | 文件去重 + Storage Ref | `file/service_crud.go:85-95` | 当前去重路径可能的竞态：A 上传→创建 row→ref++，B 上传→checksum 匹配→复用 key→ref++。但若 A 在 `files.Create` 后、`refs.Increment` 前崩溃 → ref 漏加。建议在 `files.Create` 事务中同时执行 `refs.Increment`。 |
| A2 | 通知折叠（Collapse）时的 `created_at` 覆盖 | 通知折叠 | `service/notification.go:212` | 折叠后 `existing.CreatedAt` 被赋值为 `time.Now().UTC()`，改变了原始通知的创建时间，可能导致排序异常。建议保留最早的 `created_at`。 |
| A3 | SSE 重连时 `replayMissed` 的并发安全 | SSE 推送 | `handler/http/events.go:92-114` | 当多个设备同时以同一 `last_event_id` 重连时，`fetcher.ListSince` 返回相同数据，且 `c.Writer.Flush()` 在写入后未检查错误。建议加 `Flush` 错误处理。 |
| A4 | `Conversation`（当前 `withUID` 参数）分页 + 并发写入 | 消息列表 | `repository/message.go:ListConversation` | 当前按 `created_at DESC` 分页。若在分页查询期间有新消息写入，旧的分页 cursor 可能跳过这些新消息（时序偏移）。建议使用 `id` 作为 cursor 进行严格排序。 |
| A5 | Block + 群组消息的交互 | Block 系统 | `internal/block/` | 当前 Block 只影响 1:1 消息和通知。若引入群组对话，被屏蔽用户在群组中的消息需要对屏蔽方不可见。当前代码在 `service/message.go` 的 `Send` 中检查 `IsBlocked(toUID, actor)`，群组场景需要额外检查每个成员的 block 状态。 |
| A6 | Search timeout 上下文泄漏 | 搜索 | `service/search.go` | 每个 search kind 创建一个新的 `context.WithTimeout`，循环中用 `defer cancel()` 但在循环外用 `cancel()`。若一个 kind 超时，其上下文被取消但循环继续；后续 kind 的 ctx 可能已被取消的父 context 影响。建议每个迭代独立创建新的根上下文。 |
| A7 | 批量 operate 缺少事务 | 批量操作（如消息批量标记已读） | `repository/message.go:MarkConversationRead` | 当前 `MarkConversationRead` 使用 `UPDATE ... WHERE ... AND read_at IS NULL` 一条语句，这是原子的。但未来若有更复杂的批量操作（如批量删除、批量分享），需要注意事务边界。 |
| A8 | Outbox Event payload JSON 中丢失 `replier_uid` | 通知 | `service/notification.go:commentRepliedPayload` | `commentRepliedPayload` 结构体定义了 `ReplierUID` 字段，但在 `notificationFor` 方法的 `case "comment.replied"` 中，检查 `p.ReplierUID` 时发现其可能为空——因为服务层 `comment.go` 的 `Create` 方法在构建 outbox event 时**没有填充** `replier_uid`。这会导致通知中 `ActorUID` 为空，影响推送展示。 |
| A9 | 预签名上传 (Presigned Upload) 没有 checksum 去重 | 文件直传 | `service/file_presign.go:CompleteUpload` | 代码注释明确写了"Content-checksum dedup is intentionally SKIPPED for direct uploads"。这意味着直传路径会浪费存储（同一文件上传两次 → 两个 blob），且 GC 需额外处理。建议：Stor 后端支持 ETag 时，可在 CompleteUpload 时下载头几个字节计算弱 checksum 做初步去重。 |

---

## 附录 B：性能优化机会（已有功能的增量改进）

| # | 优化点 | 当前实现 | 优化建议 | 预期收益 |
|---|---|---|---|---|
| B1 | 通知列表复合索引 | `idx_notifications_recipient_created` 是 `(recipient_uid, created_at DESC)` | 考虑包含 `read_at` 作为 INCLUDE 列，避免回表查询 | 减少未读过滤的 IO（覆盖索引） |
| B2 | Outbox relay 批量标记 | `MarkPublished` 逐条 UPDATE | 使用 `UPDATE ... WHERE id = ANY($1)` 批量标记 | 降低 relay 每次 drain 的 DB 往返次数 |
| B3 | SSO UserCache 预热 | 冷启动时第一批请求全部穿透到 SSO | `app.warmup()` 中可尝试获取当前 SSO session token 的关联用户来预热 | 减少部署后前几分钟的 SSO 延迟 |
| B4 | 文件列表 + 用户信息的 N+1 | `ListByOwner` 返回 `[]*domain.File`，每个文件不包含 owner 显示名 | 服务层目前直接在 `List` 后额外调用 `enrichUsers`（批量的），已经避免了 N+1。但上游如果有 10 个共享文件则 10 个 owner 信息一起查询。当前做法正确。 | ✅ 已优化 |
| B5 | Cursor 分页使用 UUID v7 有序性 | 当前 `WHERE id < cursor ORDER BY id DESC` | UUIDv7 天然时间有序，但 `idgen.NewID()` 生成的 UUID 直接作为主键。PostgreSQL 对 UUID 的排序不会利用其时间前缀。建议：创建 `id DESC` 索引并确保查询使用该索引（pg_hint_plan 或验证 query plan）。 | 大表分页性能稳定 |

---

*本文件基于 `main` 分支 2026-07-10 快照完成。建议团队阅读后与 `expansion-directions.md` 合并讨论，形成统一的 Sprint 规划。*
