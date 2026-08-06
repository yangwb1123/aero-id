# aero-id — 剩余高价值扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 + 产品经理
> **方法**: 全局代码扫描后，逐一比对 `docs/requirements/` 下 5 份及 `docs/REVIEW_*` 下 8 份既有审查文档的主题索引，定位**零覆盖或仅有单句提及**的方向
> **前置条件**: 代码库处于 `feat/roadmap` 完全合并后的稳定状态

---

## 文档定位

aero-id 已拥有 5 份 `docs/requirements/` 方向文档，覆盖了 20+ 个扩展方向：

| 已有文档 | 覆盖方向 |
|----------|---------|
| `architecture-extensions.md` | 群聊/频道、统一活动流、内容生命周期策略、协作审核工作台、文件预览加工管线 |
| `edge-cases-and-performance.md` | 分布式限流、多地域部署、Webhook 事件平台、Schema 版本化 |
| `product-directions.md` | 文件版本管理、定时动作引擎、自适应限流、数据平台/嵌入式分析 |
| `untapped-directions.md` | 移动推送网关、SDK/开发者平台、OAuth 2.0、数据导入迁移、多区域部署 |
| `expansion-directions.md` | 群组对话、用户 Webhook、多副本扩展、WebSocket、存储配额 |

本文档定位为**补缺分析** — 提出在所有前述文档中零覆盖或仅有单句提及的 5 个方向。

---

## 方向一：会话线程与消息组织（Conversation Threading & Message Organization）

### 概念

在当前 1:1 和未来群聊的对话中，引入**线程化回复**（Threaded Replies）和**消息组织**能力，使对话不再是平铺的线性列表，而是可折叠的、可导航的结构化信息流。

### 为什么当前文档未覆盖

群聊（Group Chat / Channels）关注的是**参与者维度**的扩展（1:1 → N:N）；而线程化关注的是**消息维度**的扩展（平面 → 层次化）。两者正交但互补。当前所有 5 份文档均未涉及消息组织层面。

### 用户场景

1. **Threaded Replies**：在对话中，用户 A 发了一个问题，用户 B 和 C 想针对这个问题展开讨论而不干扰主对话流。B 对 A 的消息发起一个 Thread，C 在 Thread 中回复。主对话流只显示"3 条回复"，点击展开查看完整 Thread。

2. **Pinned Messages**：对话管理员（或参与者）可以置顶重要消息（公告、决议、文档链接），置顶消息始终显示在对话顶部。

3. **Saved / Bookmarked Messages**：用户可以将消息加入"收藏"（Save），在独立的 `Saved Messages` 页面统一查看，而不需要在各个对话中翻找。

4. **Message Labels / Tags**：用户可以为消息打标签（如 `#todo`、`#important`、`#reference`），支持按标签过滤搜索。

### 现有基础设施

| 组件 | 可复用度 | 说明 |
|------|---------|------|
| `messages` 表 | 高 | `messages.meta`（JSONB）字段可以存储 `parent_id`（指向被回复的消息）、`is_pinned`、`is_saved` 等标志 |
| `search` 系统 | 高 | 全文搜索已支持，加入标签/收藏过滤只需扩展 `tsvector` 或 `meta` 查询 |
| `notification` 系统 | 高 | Thread 中新回复触发的通知与现有 `comment.replied` 事件类似 |
| `hub.SSE` | 中 | 新 Thread 回复可通过 SSE 推送给 Thread 参与者 |
| `outbox` | 中 | 消息 pin/unpin、save/unsave 可作为审计事件写入 outbox |

### 架构影响

- **Schema**：`messages` 加 `parent_id UUID REFERENCES messages(id)`（可为 NULL，NULL 表示顶级消息）、`messages` 加 `is_pinned BOOLEAN DEFAULT FALSE`；新建 `saved_messages` 表（`uid, message_id, saved_at, labels TEXT[]`）
- **Service**：新增 `ThreadReply`、`PinMessage`、`UnpinMessage`、`SaveMessage`、`UnsaveMessage`、`ListSaved`、`GetThread` 方法
- **Repository**：`ListMessages` 增加 `parent_id IS NULL` 过滤（顶级消息）；新增 `GetThread` 查询（`WHERE parent_id = ? ORDER BY created_at`）；新增 `saved_messages` repo
- **Notification**：Thread 回复发出 `message.thread_reply` 事件→推送给 Thread 中所有参与者
- **Search**：搜索范围增加 `saved_messages` 和标签过滤
- **Handler**：新增 HTTP + gRPC 端点（约 8 个 RPC）

### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| Thread 消息被软删除 | Thread 中显示"[消息已删除]"，Thread 仍然存在 |
| 对话中的消息被 pin 后删除 | Pin 自动解除，或显示"[已删除的消息] — Pinned" |
| 用户 save 了某人的消息，那人之后删除消息 | Saved 记录保留但标记为"消息已不可见" |
| Thread 深度限制 | Thread 限制为 1 层（与评论一致），对 Thread 的回复还是顶级消息下的回复 |
| 大量 Pin（> 100） | 限制同时 pin 的数量（建议：每个对话最多 20 条 pinned） |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户价值 | 🔥🔥🔥🔥（消息用户每天用，线程化是基本 UX） |
| 架构杠杆 | 中（少量 Schema 变更，现有通知/Search 可复用） |
| 实现成本 | 低-中（无外部依赖，纯业务层增量） |
| 依赖前置 | 无（群聊非必需，但先做线程化可在群聊中直接受益） |

**建议**: P2 — 可在群聊之后或与群聊并行开发。

---

## 方向二：公开分享链接与 CDN 分发（Public Share Links & CDN Distribution）

### 概念

当前文件分享是**身份感知**的——文件拥有者将文件的读权限授予特定的 uid。这是"团队成员"级别的共享。缺失的是**匿名公开分享**——生成一个公开链接，任何知道链接的人（无需登录）都可以下载或预览文件。

### 为什么当前文档未覆盖

文件共享（`file_shares` 表）仅支持"授权给特定 uid"的模式。搜索全部 13 份现有文档，没有任何一份将**公开链接分享**或**CDN 分发**视为独立方向。`architecture-extensions.md` 中的"文件预览管线"关注的是**格式转换**而非**分发**。

### 用户场景

1. **分享文件给外部用户**：用户 A 想将一个大文件分享给 B（没有 aero-id 账号）。当前做不到——B 必须先有 SSO 账号才能下载。

2. **嵌入到第三方站点**：用户 A 在论坛/博客中嵌入文件（如图片、PDF）。需要一个公开的、永久的 URL。

3. **链接保护**：分享链接时可设置密码保护、过期时间、下载次数上限。

4. **CDN 集成**：频繁被访问的公开文件应通过 CDN（如 CloudFront、Cloudflare）分发，减少源站压力，提升全球下载速度。

5. **下载分析**：分享链接的创建者可以看到下载次数、独立 IP、时间分布等统计。

### 现有基础设施

| 组件 | 可复用度 | 说明 |
|------|---------|------|
| `storage` 层（Local/S3） | 高 | S3 后端可以直接生成 presigned URL（当前仅用于上传）；Local 后端可延用 HMAC 签名 |
| `FileStorage.GetDownloadURL` | 高 | 现有方法只需扩展 `public: true` 参数 |
| `files` 表的 `visibility` 列 | 高 | 已有 `public/private` 枚举，但目前仅用于 owner 管理的语义区分，未在下游使用 |
| `file_shares` 表 | 中 | 结构可复用（加 `share_token`、`password_hash` 字段），或新建 `public_links` 表 |
| `idempotency` 系统 | 低 | 创建分享链接的幂等性（可选） |

### 架构影响

- **Schema**：新建 `public_links` 表：

  ```sql
  CREATE TABLE public_links (
      id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
      file_id         UUID NOT NULL REFERENCES files(id),
      owner_uid       TEXT NOT NULL,
      token           TEXT NOT NULL UNIQUE,       -- 分享链接令牌
      password_hash   TEXT,                        -- BCrypt（可选）
      max_downloads   INT,                         -- NULL = 不限
      download_count  INT NOT NULL DEFAULT 0,
      expires_at      TIMESTAMPTZ,
      created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
      deleted_at      TIMESTAMPTZ
  );
  CREATE INDEX idx_public_links_token ON public_links(token) WHERE deleted_at IS NULL;
  CREATE INDEX idx_public_links_owner ON public_links(owner_uid) WHERE deleted_at IS NULL;
  ```

- **Service**：新增 `CreatePublicLink`、`RevokePublicLink`、`ListPublicLinks`、`GetPublicLinkDownload`
- **Handler**：新增 HTTP 端点（gRPC 不需要——公开下载不是 gRPC 场景）：
  - `POST /v1/files/:id/links` — 创建公开分享链接
  - `DELETE /v1/files/:id/links/:linkId` — 撤销
  - `GET /v1/files/dl/:token` — 公开下载（无需认证！当前 `/files/dl` 路径需要 HMAC 签名，公开分享需要一个新的 `GET /s/:token` 端点）
- **Rate Limiting**：公开下载端点需单独限速（per IP，而非 per uid）
- **CDN**：S3 后端返回 CloudFront presigned URL（TTL 设为链接过期时间）；Local 后端通过反向代理（如 nginx X-Accel-Redirect）提供
- **Analytics**：`download_count` 原子递增；未来可加 `download_logs` 表记录时间戳 + IP + User-Agent

### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| 文件 owner 删除文件后链接仍在 | 软删除时级联 `deleted_at` 设置；下载时检查 `files.deleted_at` |
| 公开链接泄漏后被恶意传播 | owner 可随时撤销链接（Revoke）；可选 password 保护 |
| 大文件 + 大量并发下载 | S3 presigned URL 天然支持高并发；Local 后端需限流 |
| 文件更新后公开链接指向旧内容 | 当前文件不可变（上传即固定）；如果后续引入文件版本，需指定版本 |
| 限免/盗链检测 | 增加 Referer 白名单选项，仅允许特定域名嵌入 |

### 价值判断

公开分享链接是文件服务的**基础功能缺口**，几乎所有消费级和企业级文件服务（Google Drive、Dropbox、Box、S3）都原生支持。对于一个将自己定位为"data storage"的服务，缺少公开分享是一个明显的产品空缺。

**建议**: P1 — 实现成本低，用户价值高，且对 CDN 集成有长远架构意义。

---

## 方向三：AI/ML 内容智能平台（AI-Powered Content Intelligence Platform）

### 概念

建立一个统一的 AI/ML 基础设施层，为 aero-id 的所有内容域（文件、消息、评论）提供**智能分析**能力：自动分类、语义搜索、内容审核辅助、智能通知排序。

### 为什么当前文档未覆盖

原始设计规范（`docs/superpowers/specs/2026-05-22-aero-id-design.md`）在 11.2 节预埋了 `extracted_text`、`tags`、`embedding(VECTOR)` 列，但明确标注"不进 MVP"。此后所有扩展方向文档均未将 AI/ML 作为独立方向提出。架构扩展文档中的"协作审核工作台"讨论的是**人工审核流程**而非**AI 辅助**。

### 用户场景

1. **语义搜索（Semantic Search）**：当前搜索基于 PostgreSQL tsvector（词干匹配）。搜索"周末聚会照片"找不到"picnic 2026-07-12"。引入文本嵌入（embedding）向量搜索，用户可以用自然语言查询找到相关内容。

2. **自动标签与分类（Auto-Tagging）**：上传文件/发送消息时，AI 自动生成标签（如 `#工作、#个人、#财务、#旅行`），存入 `messages.meta.tags` 或 `files.meta.tags`，方便后续分类浏览。

3. **智能通知排序（Smart Notification Ranking）**：当前通知按时间排序。AI 可以学习用户行为：哪些通知用户总是立即阅读（消息 in 活动对话），哪些通知用户总是不读（批量通知、低优先级评论）。高评分通知排在最前或触发更频繁的推送。

4. **自动内容审核辅助（AI-Assisted Moderation）**：评论提交时，AI 模型根据文本内容给出"违规概率"（0-1）。超过阈值（如 > 0.9）自动隐藏；超过警戒线（如 > 0.7）入审核队列并在管理界面标记为"AI 标记"。

5. **文件内容理解（File Content Understanding）**：图片自动生成描述（image captioning）、PDF 提取文字并索引、音频转文字。所有提取内容存入 `files.meta` 或专门的 `file_content` 表。

### 现有基础设施

| 组件 | 可复用度 | 说明 |
|------|---------|------|
| `outbox` | 高 | `file.uploaded`、`comment.created` 事件可触发 AI 任务，解耦同步路径 |
| `search.tsvector` 列 | 中 | embedding 向量搜索需要 `pgvector` 扩展，与现有 GIN 索引互补 |
| `files.meta` JSONB | 高 | 标签、分类、提取的文字均可存入 meta，无需改 Schema |
| `comments.content` / `messages.content` | 高 | 文本内容可直接作为 ML 管道输入 |
| `audit_events` | 低 | AI 判决结果可以记入审计 |

### 架构影响

- **新包**：`internal/ai/` — 定义核心接口：

  ```go
  type Embedder interface {
      Embed(ctx context.Context, text string) ([]float32, error)
  }
  type Classifier interface {
      Classify(ctx context.Context, text string, labels []string) (map[string]float64, error)
  }
  type Moderator interface {
      Moderate(ctx context.Context, text string) (*ModerationResult, error)
  }
  type Summarizer interface {
      Summarize(ctx context.Context, text string) (string, error)
  }
  ```

- **实现选择**：
  - 轻量方案：调用外部 LLM API（OpenAI、Claude）或自建模型（通过 gRPC/TCP）
  - 中度方案：本地 `pgvector` 扩展 + OpenAI Embeddings API
  - 重度方案：引入向量数据库（Milvus / Qdrant / Weaviate）

- **Schema**（可选，增量）：
  - `files` 加 `embedding vector(1536)`（或新建 `file_embeddings` 表）
  - `comments` 加 `ai_moderation_score FLOAT, ai_moderation_label TEXT`
  - `messages` 加 `embedding vector(1536)`

- **Outbox 消费者**：新增 `internal/outbox/ai.go`，订阅 `file.uploaded`、`comment.created` 事件，触发 AI 处理链：提取→分类→嵌入→存储结果

- **搜索增强**：`SearchService` 支持混合搜索（BM25 + 向量相似度），按 `weight` 参数调节文本匹配和语义匹配的比例

- **配置**：新增 `ai.embedder.provider`、`ai.moderator.enabled`、`ai.moderator.threshold`、`ai.auto_tag.enabled` 等

### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| AI 服务不可用 | 降级：跳过 AI 处理，不影响主业务流程；`meta.ai_status: "skipped"` |
| AI 误判（False Positive） | 审核员可以覆盖 AI 判决；`ai_moderation_score` 保留用于模型改进 |
| 用户举报 AI 判决结果 | 提供"人工复审"请求，结果反馈给模型 |
| 多语言内容 embedding | 选择支持多语言的 embedding 模型（如 `text-embedding-3-small`） |
| AI 处理延迟 | 处理是异步的（通过 outbox），不影响上传/发送延迟 |
| 隐私与数据泄露 | AI 调用若使用外部 API，需配置数据合规协议（如 Azure OpenAI 的"无训练"承诺） |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户价值 | 🔥🔥🔥🔥（语义搜索是搜索体验的质变） |
| 架构杠杆 | 高（统一 AI 层可被所有领域复用） |
| 实现成本 | 中-高（需要外部 AI 服务 + pgvector 扩展） |
| 依赖前置 | 无，但建议在文件预览管线之后（方向 5 中已有部分基础） |

**建议**: P2 — 高价值但需外部依赖；可以 P1 先做 **AI 辅助审核**（最小可行，Moderator 接口即可），后续扩展为完整平台。

---

## 方向四：反滥用信任与安全平台（Anti-Abuse Trust & Safety Platform）

### 概念

构建超越单纯速率限制的系统性反滥用体系，包含：用户信任评分、行为异常检测、设备指纹、自动/半自动事件响应、以及完整的案件管理生命周期。

### 为什么当前文档未覆盖

现有 13 份文档中：
- `FAILURE_ABUSE_API.md` 简要提及了"垃圾消息检测管道"（Spam Detection Pipeline）作为方向 C，但未展开为完整平台
- `edge-cases-and-performance.md` 提到"自适应限流"（Adaptive Rate Limiting）但聚焦于流量控制而非滥用检测
- 其他文档未涉及

**无任何文档系统性地讨论信任评分、行为分析、设备指纹、或事件响应自动化。**

### 用户场景

1. **用户信任评分（Trust Score）**：每个 uid 关联一个信任分（0-100），根据行为动态调整：
   - 加分：账号年龄、已验证邮箱、发送消息被回复的比例、举报他人被确认
   - 减分：被举报成立、发送大量相似消息、短时间内大量创建连接
   - 使用：信任分低于阈值时触发额外的验证码（CAPTCHA）、限制功能（不能发送链接）、或直接限流

2. **行为异常检测（Anomaly Detection）**：实时分析用户行为模式：
   - 短时间内发送相同内容给多个用户 → 垃圾消息特征
   - 短时间内上传大量文件 → 存储滥用特征
   - 登录后立即删除所有消息 → 被盗号清理痕迹特征
   - 多个账号从同一 IP 注册 → 僵尸网络特征

3. **事件响应自动化（Automated Response）**：异常事件触发自动动作：
   - 轻度可疑：发送验证码、消息进入审核队列（而非直接发送）
   - 中度可疑：临时限制发送频率、要求邮箱验证
   - 严重违规：自动冻结账号、通知管理员

4. **案件管理与人工审核工作台（Case Management）**：
   - 可疑行为生成"案件"（Case）进入管理队列
   - 审核员查看用户行为时间线、所有相关事件
   - 操作：标记/警告/临时冻结/永久封禁/解封
   - 案件可升级（escalate）至更高级管理员

5. **设备指纹与 IP 信誉（Device Fingerprinting & IP Reputation）**：
   - 收集客户端特征（User-Agent、Accept-Language、Screen Resolution、WebGL 指纹）生成设备指纹
   - IP 信誉查询（外部服务如 AbuseIPDB 或内部黑名单）
   - 检测多账号同设备/同 IP

### 现有基础设施

| 组件 | 可复用度 | 说明 |
|------|---------|------|
| `audit_events` | 高 | 用户所有操作已有审计日志，可直接作为行为分析的数据源 |
| `ratelimit` 包 | 中 | 当前令牌桶可作为执行层的一部分，但需要扩展为分布式（依赖方向中的分布式限流） |
| `block` 系统 | 中 | 用户屏蔽可作为信任分计算的输入（被大量屏蔽 = 负面信号） |
| `comment_reports` | 高 | 举报数据可作为信任分减分来源 |
| `admin_roles` / `moderation_queue` | 中 | 已有人工审核基础，可复用为案件工作台 |
| `hub.SSE` | 低 | 实时推送可疑活动给在线管理员 |
| `outbox` | 中 | 异常事件可以作为 outbox 事件下发 |

### 架构影响

- **新包**：`internal/trust/` — 核心信任引擎

  ```go
  type TrustEngine interface {
      Score(ctx context.Context, uid string) (int, error)
      RecordEvent(ctx context.Context, e *domain.TrustEvent) error
  }
  type AnomalyDetector interface {
      Evaluate(ctx context.Context, e *domain.TrustEvent) (*domain.AnomalyResult, error)
  }
  type ResponseExecutor interface {
      Execute(ctx context.Context, action domain.TrustAction, uid string) error
  }
  ```

- **Schema**：新增表：

  ```sql
  CREATE TABLE trust_scores (
      uid             TEXT PRIMARY KEY,
      score           INT NOT NULL DEFAULT 100,
      reason          TEXT,  -- 最后调整原因
      updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  CREATE TABLE trust_events (
      id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
      uid             TEXT NOT NULL,
      event_type      TEXT NOT NULL,  -- 'spam_like' | 'rapid_upload' | 'mass_delete' | ...
      severity        TEXT NOT NULL,  -- 'low' | 'medium' | 'high' | 'critical'
      meta            JSONB,
      created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  CREATE TABLE abase_cases (
      id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
      target_uid      TEXT NOT NULL,
      status          TEXT NOT NULL DEFAULT 'open',  -- 'open' | 'under_review' | 'resolved' | 'escalated'
      severity        TEXT NOT NULL DEFAULT 'low',
      assigned_to     TEXT,  -- admin uid
      resolution      TEXT,  -- 'warning' | 'temp_freeze' | 'permanent_ban' | 'dismissed'
      resolved_by     TEXT,
      resolved_at     TIMESTAMPTZ,
      created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  ```

- **集成点**：
  - 在所有写操作 handler 层注入 `TrustMiddleware`（检查信任分，低于阈值则拒绝或要求验证码）
  - 审计日志记录作为 `trust_events` 的隐式来源
  - `comment_reports` 被确认成立时触发信任分调整
  - 异常检测作为后台 worker（goroutine）实时处理流式审计事件

- **配置**：
  ```yaml
  trust:
    enabled: false  # opt-in，默认关闭
    spam_threshold: 30     # 信任分低于此值触发 spam 检查
    freeze_threshold: 10   # 信任分低于此值触发自动冻结
    anomaly:
      rapid_upload_count: 50    # 5 分钟内上传 50 个文件触发
      rapid_upload_window: 5m
      mass_message_count: 30    # 1 小时内发送 30 条相同内容触发
      mass_message_window: 1h
    ip_reputation:
      enabled: false
      provider: "abuseipdb"
  ```

### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| 误伤（False Positive） | 审核员可手动调整信任分并附带原因；系统不自动回调 |
| 信任分恢复机制 | 每天信任分自动回升 1 分（上限 100）；正面行为加速恢复 |
| 大规模误报攻击 | 攻击者集体举报正常用户 → 系统检测"举报来源"的信任分（低信任用户的举报权重低） |
| 白名单 | 管理员可将特定 uid 加入白名单（信任分始终 100）|
| 数据隐私 | 信任事件默认保留 90 天；用户不可查询自己的信任分（防止游戏系统） |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户价值 | 🔥🔥🔥（用户感知不强，但对平台健康至关重要） |
| 架构杠杆 | 中（独立子系统，与现有设施松耦合） |
| 实现成本 | 高（需要数据统计基础 + 异常检测逻辑 + 管理界面） |
| 依赖前置 | 审计日志（已有） |

**建议**: P3 — 在用户规模达到 ~10 万 DAU 之前，人工审核 + 限流基本够用。但**信任评分的数据模型建议尽早预埋**（`trust_scores` 表 + `trust_events` 表 + `trust_score` 列在用户上下文中），空跑收集基线数据，日后直接启用。

---

## 方向五：记录管理与合规治理（Record Management & Compliance Governance）

### 概念

为 aero-id 增加面向企业和受监管行业的合规治理能力：法律保留（Legal Hold）、留存策略（Retention Policy）、电子证据开示（eDiscovery）导出、以及合规审计追踪。

### 为什么当前文档未覆盖

搜寻全部 13 份文档：
- GDPR 数据导出在 `FLOW_CONCURRENCY_CACHE.md` 和 `untapped-directions.md` 中提及，但仅聚焦于用户主动导出
- **Legal Hold、Retention Policy、eDiscovery 在企业合规语境下的完整治理框架——零覆盖**

### 用户场景

1. **法律保留（Legal Hold / Litigation Hold）**：
   - 法律部门要求保留特定用户/特定时间范围的所有数据（包括已被用户删除的）
   - 被 Legal Hold 约束的用户删除消息 → 消息实际保留但用户看不见（硬删除被拦截）
   - 当 Legal Hold 释放后（诉讼结束），标记为可清理

2. **数据留存策略（Retention Policy / Data Lifecycle）**：
   - 组织策略：消息保留 7 年，评论保留 3 年，文件保留 10 年
   - 超出留存期的数据自动转为归档存储（成本更低）或安全删除
   - 策略按数据类型 + 组织部门配置

3. **电子证据开示（eDiscovery）**：
   - 审核员根据法院命令或内部调查请求，搜索和导出特定用户的所有数据
   - 导出格式为结构化（JSON/PDF/HTML）且带哈希链保证证据完整性
   - 支持按时间范围、关键词、参与人过滤

4. **合规审计追踪（Compliance Audit Trail）**：
   - 所有合规操作（Legal Hold 设置/释放、Retention 策略变更、eDiscovery 导出）记录在不可篡改的审计日志中
   - 审计日志支持导出为 SIEM 兼容格式（CEF/JSON）
   - 审计日志自身不可删除（append-only）

5. **可配置的合规区域（Compliance Zones）**：
   - 支持按数据地域配置不同的合规策略（如 GDPR 区域 vs SOC2 区域）
   - 每个区域有独立的 Retention 策略和 Legal Hold 状态

### 现有基础设施

| 组件 | 可复用度 | 说明 |
|------|---------|------|
| `audit_events` | 高 | 可作为合规审计日志的基础（需扩展为 append-only） |
| `outbox` | 高 | 留存过期清理可作为 outbox 消费者定时触发 |
| `storagegc.Sweeper` | 高 | 当前的 GC 清理逻辑可扩展为按 Retention 策略清理 |
| `soft delete`（gorm.DeletedAt） | 高 | Legal Hold 的"可见删除但实际保留"可通过修改软删除逻辑实现 |
| `admin_roles` | 中 | 合规管理员角色可在此扩展 |
| `migrate` CLI | 低 | 合规策略变更可能会影响迁移回滚逻辑 |

### 架构影响

#### Schema

新增 3 张核心表：

```sql
-- 法律保留记录
CREATE TABLE legal_holds (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    target_uid          TEXT NOT NULL,           -- 被保留的用户
    case_reference      TEXT NOT NULL,            -- 案件编号/法院命令编号
    reason              TEXT NOT NULL,
    scope               TEXT NOT NULL DEFAULT 'all',  -- 'all' | 'messages' | 'files' | 'comments'
    start_date          TIMESTAMPTZ NOT NULL DEFAULT now(),
    end_date            TIMESTAMPTZ,                -- NULL = 无限期
    created_by          TEXT NOT NULL,              -- 管理员 uid
    revoked_by          TEXT,
    revoked_at          TIMESTAMPTZ,
    deleted_at          TIMESTAMPTZ                 -- 软删
);
CREATE INDEX idx_legal_holds_target ON legal_holds(target_uid) WHERE deleted_at IS NULL;

-- 留存策略
CREATE TABLE retention_policies (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL,
    entity_type         TEXT NOT NULL,           -- 'messages' | 'files' | 'comments'
    retention_days      INT NOT NULL,            -- 保留天数
    action              TEXT NOT NULL DEFAULT 'delete',  -- 'delete' | 'archive' | 'anonymize'
    priority            INT NOT NULL DEFAULT 0,  -- 值越大优先级越高
    created_by          TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ
);

-- 合规审计日志（append-only，不可修改）
CREATE TABLE compliance_audit_log (
    id                  BIGSERIAL PRIMARY KEY,
    event_type          TEXT NOT NULL,           -- 'legal_hold_created' | 'legal_hold_revoked' |
                                                 -- 'eDiscovery_export' | 'retention_policy_changed'
    actor_uid           TEXT NOT NULL,
    target_uid          TEXT,
    detail              JSONB NOT NULL,
    checksum            TEXT NOT NULL,            -- SHA256(prev_checksum + payload) 链式校验
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 此表永不 DELETE，永不 UPDATE
```

#### Service / Repository

- **Legal Hold Service**：
  - `CreateLegalHold` → 设置 Legal Hold → 写入 compliance audit log
  - `ReleaseLegalHold` → 释放 → 写入 log
  - `IsUnderLegalHold(uid)` → 被所有 Delete 操作调用（在 Delete 前检查，如果是则阻止物理删除但修改可见性）

- **Retention Policy Service**：
  - `SetRetentionPolicy` → 创建/更新策略
  - `ApplyRetentionPolicy` → 后台 worker，定期扫描过期数据
  - 扫描 + 删除/归档逻辑：复用 `storagegc` 的 goroutine 模式

- **eDiscovery Service**：
  - `CreateDiscoveryRequest` → 生成搜索任务
  - `ExecuteDiscovery` → 收集用户数据（消息、文件、评论）→ 打包为 ZIP（带目录索引和校验和）
  - `ListDiscoveryExports` → 历史导出记录

#### Handler

- 新增管理端点（仅合规管理员角色可访问）：
  - `POST /v1/admin/compliance/legal-holds` — 创建设置
  - `DELETE /v1/admin/compliance/legal-holds/:id` — 释放
  - `GET /v1/admin/compliance/legal-holds` — 列表
  - `POST /v1/admin/compliance/retention-policies` — 设置策略
  - `PUT /v1/admin/compliance/retention-policies/:id` — 更新
  - `POST /v1/admin/compliance/eDiscovery` — 发起导出
  - `GET /v1/admin/compliance/eDiscovery/:id/download` — 下载导出包
  - `GET /v1/admin/compliance/audit-log` — 查询合规审计日志

#### 核心改动：Delete 路径

当前 Delete 是简单的软删除。需要改为：

```
Delete 调用：
  1. IsUnderLegalHold(owner_uid, entity)？
     └─ Yes → 记录"可见删除"标记（set deleted_visibility = 'hidden'）
              但实际 `deleted_at` 不设置或设置一个特殊的 `legal_hold_deleted_at`
              实体的物理行保留，数据对用户不可见，但对合规管理员/导出可见
     └─ No  → 正常软删除（现有行为不变）
  2. Append compliance event (optional)
```

### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| Legal Hold 与 Retention Policy 冲突 | Legal Hold 始终优先（最高优先级）|
| eDiscovery 导出包含被 Legal Hold 隐藏的数据 | 导出包括"用户不可见但 Legal Hold 保留"的数据，并标记为"under legal hold" |
| 合规审计日志写入失败 | 阻断操作——合规操作必须是强一致性的（ACID）|
| 跨区域合规策略冲突 | 一个用户可以属于多个合规区域 → 取最严格策略 |
| 大量 Legal Hold 同时释放 | 后台 worker 批量清理，不阻塞主路径 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户价值 | 🔥🔥（对 C 端用户无感，但对 B2B/企业客户是关键决策因子）|
| 架构杠杆 | 低（独立治理框架，与业务层松散耦合） |
| 实现成本 | 高（表设计 + Delete 路径改造 + 导出管线 + 管理端点）|
| 依赖前置 | 管理角色系统（已完成）、审计日志（已完成） |

**建议**: P3 — 面向企业客户的差异化功能。在进入 B2B 市场前准备设计，但不急于实现。核心的可观测指标是"是否有企业客户在 RFI/RFP 中询问合规能力"。

---

## 综合优先级建议

| 方向 | 用户价值 | 架构杠杆 | 实现成本 | 建议优先级 | 最佳开始时机 |
|------|---------|---------|---------|-----------|------------|
| **1. 会话线程与消息组织** | 🔥🔥🔥🔥 | 中 | 低-中 | P2 | 群聊之后或并行 |
| **2. 公开分享链接与 CDN** | 🔥🔥🔥🔥🔥 | 中 | 低 | **P1** | **下一迭代** |
| **3. AI/ML 内容智能平台** | 🔥🔥🔥🔥 | 高 | 中-高 | P2 | 先做 AI 审核（最小可行）|
| **4. 反滥用信任与安全平台** | 🔥🔥🔥 | 中 | 高 | P3 | DAU > 10 万时启动 |
| **5. 记录管理与合规治理** | 🔥🔥 | 低 | 高 | P3 | 进入 B2B 市场前准备 |

### 立即行动建议

1. **公开分享链接（方向 2）**不应再等待。它是用户感知最强的功能缺口，实现成本最低，且对 CDN 集成有长远架构意义。

2. **AI 内容审核的 Moderator 接口（方向 3 的子集）**可提前作为 P1 实现。它不需要完整的 AI 平台——只需要一个 `Moderate(text) → score` 接口，初期用规则引擎（敏感词列表 + 正则）或调用远程 API。审计日志和评论举报流程已经就位。

3. **信任评分的数据预埋**（方向 4 的表结构）建议在下一轮 Schema 变更中顺手加入。数据收集空跑 3-6 个月后，启用时已有基线数据，避免冷启动。

4. **合规治理**目前无明确需求信号，建议保留设计文档，在第一次收到企业客户 RFI/RFP 中的合规问题时再行评估。

---

## 附录：与已有覆盖的对比验证

| 方向 | 出现于哪些已有文档 | 本文贡献 |
|------|------------------|---------|
| 会话线程与消息组织 | **零覆盖** | 全新方向 |
| 公开分享链接与 CDN | **零覆盖** | 全新方向 |
| AI/ML 内容智能平台 | 原始设计规范 11.2 节预埋但未展开 | 首次作为完整平台方向提出 |
| 反滥用信任与安全平台 | `FAILURE_ABUSE_API.md` 中简要提及"Spam Detection"方向 C | 首次作为系统性平台提出 |
| 记录管理与合规治理 | `untapped-directions.md` 覆盖数据导出/GDPR，但未涉及 Legal Hold / eDiscovery / Retention | 首次提出完整合规治理框架 |

---

*本文档基于对 `main` 分支 2026-07-10 状态的全局代码扫描，结合 13 份既有审查/需求文档的交叉索引分析。无代码被修改。*
