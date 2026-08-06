# aero-id — 真正未被覆盖的五个高价值平台扩展方向

> 审查日期: 2026-07-10  
> 视角: 资深架构师 + 产品经理  
> 方法: 全局代码扫描（全 `internal/`、`migrations/`、`configs/`、`docs/`）  
> 前提: 项目已完成 22 次数据库迁移、38+ gRPC RPC、完整双传输对等、事务性 outbox、全文搜索、通知系统、审核系统、SSO 弹性、可观测性。  
> 目标: 在已有的 50+ 份分析文档之外，找到 **五个真正未被覆盖、高杠杆率、可工程落地的方向**。

---

## 目录

1. [方向一：文件流水线深化 —— 预览 + 转码 + 病毒扫描](#方向一文件流水线深化--预览--转码--病毒扫描)
2. [方向二：国际化（i18n）与多语言支持](#方向二国际化i18n与多语言支持)
3. [方向三：批量操作与资源生命周期管理](#方向三批量操作与资源生命周期管理)
4. [方向四：内容聚合 Feed 与发现层](#方向四内容聚合-feed-与发现层)
5. [方向五：事件源（Event Sourcing）深化 —— 事件版本化、回放、Schema 管理](#方向五事件源event-sourcing-深化--事件版本化回放schema-管理)

---

## 方向一：文件流水线深化 —— 预览 + 转码 + 病毒扫描

### 现状

当前的 `file` 包是一个非常成熟的**存储流水线**：

| 能力 | 状态 |
|------|------|
| 上传（流式、multipart、presigned S3） | ✅ |
| 大小限制 + MIME 校验 | ✅ |
| 内容去重（checksum dedup） | ✅ |
| 可见性（私有/公开） + 按用户分享 | ✅ |
| 软删除 + GC 回收（行清理 + 孤儿 blob） | ✅ |
| 预签名下载 URL | ✅ |
| 元数据更新（PATCH） | ✅ |

**但是：文件一旦存储，服务对它的认知就终止了。** 文件以原始字节形式存在，aero-id 不知道它存储的是什么内容、是否安全、是否可以被浏览器预览。

### 缺口

#### 1.1 无文件预览管道

当前唯一的下载方式是返回原始文件或生成预签名 URL。对于以下场景完全没有支持：

- **图片** → 没有缩略图生成。用户上传 12MP 的 JPEG（~6MB），每次列表浏览都要下载全尺寸。没有 `<img>` 友好的小尺寸变体。
- **视频** → 没有转码或关键帧截取。用户上传 4K 视频，接收方必须下载完整文件才能知道内容。
- **文档** → 没有 PDF/TXT/Markdown 渲染。服务可以安全地渲染 HTML 预览，但没有这层。
- **音频** → 没有格式降级，没有波形/封面提取。

**影响规模：** 对于任何以文件为核心的服务，预览能力直接决定产品体验。没有预览，用户必须下载每个文件才能查看内容——带宽增加 10-50 倍，移动端体验极差。

#### 1.2 无病毒/恶意软件扫描

用户上传的文件直接写入存储。没有任何安全检测：

```
User Upload → [Size/MIME Check] → [Dedup] → Store
                                              ↑ no AV scan
```

**攻击面：**
- 恶意用户上传包含病毒的可执行文件
- 上传带有 XSS payload 的 SVG 文件（可绕过 MIME 检查）
- 上传图片隐写（steganography）作为 C2 信道
- 上传非法内容（需合规审查）

**值得注意的是：** aero-id 有 `AllowedMimeTypes` 配置，但这只能阻止明显的滥用。一个合规的 `application/pdf` 可以包含病毒。

#### 1.3 无存储类 / 分层

文件不分冷热。所有文件存储在同一个后端：

- 不常访问的归档文件占用热存储成本
- 没有大小阈值（小文件合并 vs 大文件单独存储）
- 没有自动降级策略（90 天未访问 → 归档到廉价存储）

### 建议方向

#### A. 预览管道（异步 Job 队列）

构建一个异步的**媒体处理管道**，但**不需要引入外部消息队列系统**——复用现有的 `outbox` 基础设施 + Job 模式：

```
Upload Complete
    → outbox event: "file.created"
    → Handler: dispatch preview job
    → Preview Worker (goroutine pool):
        ├── Image → resize to thumbnails (256px, 512px) + store as sibling keys
        ├── Video → ffmpeg frame capture at 5s + store thumbnail
        ├── PDF/Doc → extract first page as PNG (libreoffice headless / pdftoppm)
        └── None of above → fallback to mimetype icon
    → Store preview metadata in preview_refs table
    → SSE push: "preview.ready"
```

**关键设计决策：**
- **预览不是阻塞的。** `CompleteUpload` 立即返回文件 ID。客户端轮询或等待 SSE `preview.ready` 事件。
- **预览变体存储在同一存储后端**，但使用命名约定：`{user_id}/{file_id}/thumb_256.jpg`。
- **预览失败不阻塞文件访问**。原始文件始终可下载。
- **Worker 是进程内的**（goroutine pool），默认使用 `runtime.NumCPU()` 个 worker。未来可扩展为独立进程，但现在保持简单。

**新表：**

```sql
CREATE TABLE file_previews (
    file_id         UUID REFERENCES files(id) ON DELETE CASCADE,
    variant         VARCHAR(32),     -- 'thumb_256', 'thumb_512', 'preview_png', 'waveform'
    storage_key     VARCHAR(1024) NOT NULL,
    width           INT,             -- nullable, for images
    height          INT,             -- nullable, for images
    size_bytes      BIGINT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (file_id, variant)
);
```

**新端点：**

```
GET /v1/files/:id/preview/:variant → 重定向到预签名 URL 或 404
```

**新增配置：**

```yaml
preview:
  enabled: true
  max_concurrency: 4
  image_sizes: [256, 512, 1024]
  video_snapshot_seconds: 5
  timeout: 30s
```

#### B. 病毒扫描

在 `CompleteUpload` 路径中插入一个**可跳过的扫描步骤**：

```
CompleteUpload
    → Check MIME allow-list (existing)
    → Stat file size (existing)
    → [NEW] AV scan: call external scanner via Pipe
        ├── Clean → proceed to Create
        └── Infected → Delete blob + return 422 (Unprocessable Entity)
```

**集成方式：**
- **非阻塞模式（推荐 MVP）：** outbox event `file.uploaded` → 异步扫描 → 发现病毒后标记文件 `status = 'quarantined'` + 通知管理员 + 通知上传者。
- **同步模式：** 在 `CompleteUpload` 中同步调用 `clamd /var/spool/scan` 或 `ClamAV REST API`。小文件（<10MB）延迟可接受。

**新列：**

```sql
ALTER TABLE files ADD COLUMN scan_status VARCHAR(16) DEFAULT 'pending';
-- pending | clean | infected | error
ALTER TABLE files ADD COLUMN scanned_at TIMESTAMPTZ;
```

#### C. 存储分层

基于 `files.last_accessed_at`（当前不存在的列）实现自动归档：

```sql
ALTER TABLE files ADD COLUMN last_accessed_at TIMESTAMPTZ DEFAULT NOW();
CREATE INDEX idx_files_last_accessed ON files(last_accessed_at) WHERE deleted_at IS NULL;
```

后台 Job（复用 GC sweeper 模式的 `Start/Stop`）：

- 每 24h 运行一次
- 找到 `last_accessed_at < now() - 90d AND storage_type = 'hot'` 的文件
- 迁移到 `storage_type = 'archive'`（例如 S3 Glacier 或本地冷存储目录）
- 更新 `storage_key` 和 `storage_type`

#### D. 文件处理与现有架构的集成

预览管道和病毒扫描共享同一 Job 框架。**建议不要引入 Kafka/RabbitMQ**——aero-id 当前的 outbox + goroutine pool 模式足以支持 MVP。未来如需伸缩，可将 Job 表从 outbox 分离为独立的 `processing_jobs` 表。

### Why This Direction

1. **文件是 aero-id 的三驾马车之一。** 如果没有预览和扫描，文件能力是不完整的——是"存储"而非"服务"。
2. **安全责任。** 托管用户上传的文件而不扫描，在法律合规上是站不住脚的（欧盟 DSA、美国 Section 230）。
3. **现有基础设施可以直接复用。** Outbox 模式、goroutine pool、存储后端的接口——预览管道是它们的自然扩展。
4. **产品差异化。** 大部分文件存储服务不提供免费预览。内联预览是 Slack/Google Drive/Dropbox 等产品的标配。

### 估算

| 组件 | 工作量 | 依赖 |
|------|--------|------|
| 图片缩略图管道 | 中（3-5 天） | `imaging` lib (Go) 或 `sharp` (node.js sidecar) |
| 视频快照 | 中（2-3 天） | `ffmpeg` binary on PATH |
| 文档预览 | 中-高（5-7 天） | `libreoffice --headless` 或 `pdftoppm` |
| ClamAV 集成 | 低（1-2 天） | `clamd` socket / REST |
| 存储分层 | 中（3-5 天） | S3 lifecycle config + 新列 |

---

## 方向二：国际化（i18n）与多语言支持

### 现状

aero-id 是一个**完全不自知语言的服务**：

- 内容全部作为原始字节处理（消息、评论、文件名），不做任何语言感知处理
- 错误消息硬编码为中文（`"文件未找到"`、`"权限不足"`）
- 全文搜索使用 PostgreSQL `simple` 字典——这对中文、日文、韩文（CJK）无效，因为它们不按空格分词
- 通知静默时段使用 `HH:MM` 格式，时区由用户在偏好中手动设置
- 排序一律按 `created_at`，不考虑语言特定的排序规则（collation）

### 缺口

#### 2.1 全文搜索的中文分词缺失

这是**最明显的产品可用性问题**，在之前的分析中也有提及，但从未深入分析影响。

当前搜索实现：

```sql
-- migration 000016: FTS indexes
-- 使用 simple 字典创建 tsvector
-- simple 字典不做词干分析、不做分词
```

```go
// internal/repository/message.go — SearchMessages
// WHERE search_vector @@ plainto_tsquery('simple', ?)
```

**问题：** `plainto_tsquery('simple', '你好世界')` 被当作一个 token `'你好世界'` 处理。因为没有空格，PostgreSQL 不认为这是一个多词查询。搜索"你好"不会匹配包含"你好世界"的消息。

**影响：** 中文用户无法进行有效的全文搜索。这是产品层面的严重缺陷——不是性能问题，而是**完全不可用**。

**解决方案：**
- PostgreSQL 扩展 `zhparser`（基于 SCWS 分词）或 `jieba`（pg_jieba）用于中文
- 或使用 ICU 分词器（`pg_icu`）
- 或迁移到外部搜索引擎（Meilisearch / Typesense），这些引擎原生支持多语言分词

**注意：** 这不仅是添加扩展的问题——如果迁移分词器，现有索引需要重建。在百万行级表上需要计划停机窗口。

#### 2.2 错误消息与服务端提示的多语言

当前所有错误代码是英文键（`"file.not_found"`），但错误消息（`"file not found"` 或 `"文件未找到"`）是特定语言。客户端收到的是 `AppError.Message`，没有可编程的国际化方式。

**建议：** 将所有面向用户的消息识别符化（message identifier），客户端负责翻译：

```go
// 当前
apperr.NotFound("file.not_found", "文件未找到")

// 建议
apperr.NotFound("file.not_found", "file.not_found") // code + message key 相同
// 或者
apperr.NotFound("file.not_found", nil) // 无消息，客户端使用错误代码查找翻译
```

**但这在客户端不可用时怎么办？** 更好的方案：服务端保留默认语言，但接受 `Accept-Language` 头部：

```go
// 带语言的消息存储
var errorMessages = map[string]map[string]string{
    "file.not_found": {
        "zh-CN": "文件未找到",
        "en-US": "File not found",
        "ja-JP": "ファイルが見つかりません",
    },
}
```

**工作量评价：** 低。只影响 `pkg/errors` 包 + 消息字典 + HTTP middleware（提取 `Accept-Language`）。

#### 2.3 时区与本地化格式

当前通知偏好的静默时段是 `HH:MM` + `timezone` 字符串：

```go
type NotificationPreference struct {
    QuietHoursStart string // "22:00"
    QuietHoursEnd   string // "08:00"
    Timezone        string // "Asia/Shanghai"
}
```

**缺口：**
- 时区 IANA 名称没有验证（用户可以设置 `"foo/bar"`）
- `Dispatcher` 检查静默时段时，没有调整到用户时区
- 没有 DST（夏令时）感知的比较——`"02:30"` 在 DST 切换日可能有问题
- 创建时间 `created_at` 以 UTC 存储——这是正确的，但消费 API 没有返回时区偏移

**建议：**
- `time.LoadLocation()` 验证时区
- 在 Dispatcher 中添加时区感知的安静时段检查
- API 响应中的时间戳增加 `ISO 8601` 格式（已做到），客户端自行转换

#### 2.4 排序规则（Collation）

当前 `ORDER BY created_at DESC` 是纯时间排序，没有问题。但如果有按名字排序的功能（文件列表按名称排序、用户搜索等），则需要语言感知的排序：

```sql
ORDER BY name COLLATE "zh-CN-x-icu"
```

**当前状态：** 无名字排序，所以没有直接影响。但如果在方向一中添加了"按文件名搜索"功能，就需要考虑。

### 建议方向

| 优先级 | 项目 | 工作量 | 影响 |
|--------|------|--------|------|
| P0 | 全文搜索中文分词 | 中（3-5 天 + 测试） | 中文用户完全不可用 → 产品级 Bug |
| P1 | 错误消息国际化框架 | 低（1-2 天） | SDK/客户端开发者体验 |
| P2 | 时区验证与 DST 感知 | 低（1 天） | 静默时段准确性 |
| P2 | Accept-Language 响应 | 低（1 天） | 多语言体验 |

### Why This Direction

1. **搜索中文分词不是"增强"——是"修复"。** 如果目标市场包含中文用户（代码和注释有大量中文，这似乎是明确的），当前搜索对他们是不可用的。
2. **国际化是平台全球化的前提。** 没有 i18n 框架，扩展到日本、韩国、欧洲等市场意味着重新架构。
3. **工作量小、影响大。** 错误消息国际化可以在 2 天内完成，影响所有 API 消费者。

---

## 方向三：批量操作与资源生命周期管理

### 现状

当前所有资源操作都是 **单资源粒度**：

| 操作 | 一次处理 | 批量删除 N 个 |
|------|---------|---------------|
| 删除消息 | `DELETE /v1/messages/:id` | N 个 API 调用 |
| 标记已读 | `POST /v1/messages/:id/read` | N 个 API 调用 |
| 软删除文件 | `DELETE /v1/files/:id` | N 个 API 调用 |
| 归档会话 | 不存在 | N/A |
| 设置静默时段 | `PUT /v1/notification-preferences` | 一次全部设置 |
| 批量操作 | 无 | 无 |

**有意义的例外：**
- `POST /v1/conversations/:peer/read`（一个调用标记整个会话为已读）——正确
- `PUT /v1/notification-preferences`（支持批量设置）——正确

**缺口模式：** 客户端需要执行多资源操作（"清空收件箱"、"删除与 Bob 的所有消息"、"归档 30 天前的文件"）时，只能发送 N 个串行请求。这不仅慢，而且在有请求超时的情况下不可靠——如果第 47 个请求失败，前 46 个已经执行了，没有回滚。

### 场景分析

#### 3.1 批量删除

```
用户场景: "清空与 Bob 的整个会话"
当前:   GET /v1/conversations?with=bob → 获取 200 条消息 ID → 200 次 DELETE
                                                                    ↑ 超时风险，不稳定
期望:   POST /v1/conversations/bob/delete 或 POST /v1/batch/messages/delete
```

**更多批量删除场景：**
- 批量删除自己上传的文件（`POST /v1/batch/files/delete`）
- 批量删除通知（`POST /v1/batch/notifications/delete`）
- 账户注销时的全量数据清除（`POST /v1/account/purge`——需要级联所有关联数据）

#### 3.2 批量操作

```
用户场景: "将 Bob、Alice、Charlie 都添加到这个文件的分享列表"
当前:   3 次 POST /v1/files/:id/shares
期望:   PUT /v1/files/:id/shares { "grantees": ["bob", "alice", "charlie"] }
       （幂等：添加不存在的，忽略已存在的）
```

**更多批量操作场景：**
- 批量标记已读（`POST /v1/batch/messages/read`）
- 批量静音会话（`POST /v1/batch/conversations/mute`）
- 批量隐藏评论（`POST /v1/batch/comments/hide`——审核员操作）

#### 3.3 资源生命周期管理（TTL / 自动过期）

当前所有资源永久保留（除了 GC 清理软删除的行）。没有资源级别的 TTL：

```
用户场景: "我想发送一条 24 小时后自动销毁的消息"
当前:    不可能。消息保存到被显式删除。
期望:    POST /v1/messages { "content": "...", "ttl": "24h" }
         → 24h 后消息自动软删除
```

**生命周期功能矩阵：**

| 资源 | 当前 | 期望 |
|------|------|------|
| 消息 | 永久 | 可选的 `ttl` 字段 + 后台到期处理 |
| 文件 | 永久 | 可选的 `expires_at` 字段 |
| 会话 | 永久 | 静默自动归档（N 天无活动 → 自动归档） |
| 文件分享 | 永久 | 可选的 `expires_at` + 自动撤销 |
| 导出包 | 不存在 | 7 天后自动删除 |
| 通知 | 永久 | N 天后自动清理已读通知 |

### 建议方向

#### A. 批量 API 端点

**设计原则：**
- 批量操作应**原子化**：要么全部成功，要么全部失败（在 DB 事务中执行）
- 批量操作应**幂等**：重试不应导致副作用
- 批量大小应**受限**（防止 DoS）：默认 100，最大 500

```http
POST /v1/batch/messages/delete
Content-Type: application/json
Idempotency-Key: batch-del-abc123

{
    "ids": ["msg-001", "msg-002", ..., "msg-100"]
}
```

```json
// 200 OK
{
    "deleted": 100,
    "failed": [],
    "next_batch": null
}
```

**出错时行为：**

```json
// 200 OK（部分成功——取决于设计决策）
{
    "deleted": 95,
    "failed": [
        {"id": "msg-042", "reason": "not_found"},
        {"id": "msg-088", "reason": "forbidden"}
    ],
    "next_batch": null
}
```

#### B. 资源 TTL 系统

**实现方式（最小化改动）：**

1. 在需要 TTL 的资源表上增加 `expires_at TIMESTAMPTZ` 列
2. 后台 Job（类似 GC sweeper）周期性扫描 `expires_at < NOW() AND deleted_at IS NULL`
3. 对过期的资源执行软删除（复用现有的 `SoftDelete`）
4. 通知相关方：如果消息过期，通知接收方（可选）

```sql
ALTER TABLE messages ADD COLUMN expires_at TIMESTAMPTZ;
CREATE INDEX idx_messages_expires ON messages(expires_at) WHERE deleted_at IS NULL AND expires_at IS NOT NULL;
```

**后台 Job 集成：** 复用 `internal/storage/gc/sweeper.go` 的 `Start/Stop` 模式：

```go
type ExpirySweeper struct {
    messages MessageRepo
    files    FileRepo
    now      func() time.Time
}

func (s *ExpirySweeper) Run(ctx context.Context) (int, error) {
    // 1. 找到过期的消息
    expired, err := s.messages.ListExpired(ctx, s.now(), 500)
    // 2. 软删除
    for _, m := range expired {
        s.messages.SoftDelete(ctx, m.ID)
    }
    // 3. 文件的类似逻辑
}
```

#### C. 保留策略（管理员配置）

```yaml
retention:
  messages:
    default_ttl: 0          # 0 = 不自动过期
    max_ttl: 90d             # 单条消息最长 TTL
  notifications:
    read_retention: 30d      # 已读通知 30 天后自动清理
    unread_retention: 365d   # 未读通知 1 年后清理
  files:
    ephemeral_ttl: 0
```

### Why This Direction

1. **用户体验的直接提升。** 批量操作是最频繁的用户请求之一（"清空收件箱"的 N+1 问题）。
2. **隐私合规趋势。** 瑞士/法国的消息应用法规要求">消息自动过期"功能。GDPR 的数据最小化原则鼓励数据保留期限。
3. **现有模式直接复用。** `GC sweeper` 的 `Start/Stop` 模式是 TTL 后台 Job 的完美模板。ExpirySweeper 和 BlobSweeper 可以共享同一调度器。
4. **商业上的"阅后即焚"预期。** 用户越来越期望消息应用有 ephemeral 选项。这是与 Slack/Telegram 等产品对标的必备功能。

### 估算

| 组件 | 工作量 |
|------|--------|
| 批量删除 API（消息 + 文件 + 通知） | 中（3-5 天） |
| 批量操作的事务原子性 | 中（2-3 天） |
| 资源 TTL + ExpirySweeper | 中（3-4 天） |
| 保留策略配置 | 低（1 天） |

---

## 方向四：内容聚合 Feed 与发现层

### 现状

aero-id 的数据模型是**完全面向归属的**：

```
用户 A 的 文件           ← 只有 A 能看到（或 A 显式分享的人）
用户 A → 用户 B 的消息  ← 只有 A 和 B 能看到
文件 F 上的评论          ← 与 F 的生命周期绑定
```

**完全没有"发现"机制：**
- 没有"最近公开文件"的列表
- 没有"热门评论"或"活跃讨论"
- 没有"你可能感兴趣的人"
- 没有"所有我参与的讨论"的统一视图（统一 inbox）

当前 `visibility = 'public'` 的列存在，但**没有端点可以列出公开文件**。公开文件只能通过直接知道文件 ID 来访问。

### 挖掘更深：产品缺口

#### 4.1 缺少全局"公开内容"Feed

`files.visibility` 列支持 `public/private` 二元值。`domain.CanReadFile` 也处理了公共可见性：

```go
func CanReadFile(actor string, f *File) bool {
    return f.Visibility == VisibilityPublic || f.OwnerUID == actor
}
```

**但没有任何 API 列出公开文件。** `ListByOwner` 只能列出所有者自己的文件。没有 `ListPublic`、`Discover` 或 `Feed` 端点到。所以 `visibility = 'public'` 是一个死功能——文件可以是公开的，但没有人能找到它们。

#### 4.2 缺少跨实体收件箱

当前用户有：
- 消息收件箱（`GET /v1/messages/inbox`）
- 通知列表（`GET /v1/notifications`）
- 文件列表（`GET /v1/files`）
- 评论（通过文件/消息关联获取）

**但没有统一的"我的所有活动"视图：**
- "有人评论了我的文件"
- "我的文件被分享了"
- "Bob 回复了我对 Alice 消息的评论"

这些信息分散在通知列表和直接查询中，且各有不同的分页。

#### 4.3 缺少社交/协作功能

- **没有 `@提及` 系统**：用户在评论/消息中不能提及其他人
- **没有关注系统**：不能关注用户以接收其公开内容的更新
- **没有趋势/热度**：没有基于互动量的内容排名

### 建议方向

#### A. 公开发现端点

最便宜的 MVP 是添加一个列出公开文件的端点：

```
GET /v1/discover/public-files?cursor=xxx&limit=20
```

```sql
SELECT * FROM files
WHERE visibility = 'public' AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 21;
```

在此基础上增加：
- 公开文件的总点赞/评论计数排序
- 按文件类型过滤（`mime_type LIKE 'image/%'`）
- 按日期范围过滤

**后续扩展：**

```
GET /v1/discover/trending-files      → ORDER BY (like_count + comment_count) DESC
GET /v1/discover/recent-files        → ORDER BY created_at DESC
GET /v1/discover/public-comments     → 公开文件上的公开评论
```

#### B. 统一活动流

构建一个**活动流**（activity feed），将消息、通知、文件活动汇总到一个时间线中：

```
GET /v1/feed?cursor=xxx
```

返回：
```json
[
    {
        "type": "message.received",
        "from": {"uid": "bob", "name": "Bob"},
        "preview": "嗨，文件收到了吗？",
        "unread": true,
        "timestamp": "..."
    },
    {
        "type": "comment.reply",
        "file": {"id": "file-123", "name": "设计稿.png"},
        "from": {"uid": "alice", "name": "Alice"},
        "preview": "我觉得第二版更好",
        "timestamp": "..."
    },
    {
        "type": "file.shared",
        "file": {"id": "file-456", "name": "报告.pdf"},
        "from": {"uid": "carol", "name": "Carol"},
        "timestamp": "..."
    }
]
```

**架构方案：** Feed 是**只读查询，不是新事件源**。它查询已有的 `messages`、`notifications`、`file_shares` 表，按时间合并排序。支持按类型过滤和日期范围限定。

#### C. @提及与通知

添加 `@username` 解析到评论和消息中：

1. 在 `comment.Create` / `message.Send` 中解析 `@([a-zA-Z0-9_]+)` 模式
2. 通过 SSO 查找被提及的 uids
3. 生成 `mention` 类型的 outbox 事件
4. Dispatcher 创建通知给被提及的用户

**注意：** 这需要 SSO 支持用户名查找（当前通过 uid 查找，不支持用户名搜索）。或者 aero-id 维护一个轻量级的用户名缓存。

### Why This Direction

1. **`visibility = 'public'` 是一个死功能。** 没有发现层的公开可见性是无用的。添加公开发现端点（3 天工作量）可以激活这个已存在但从未使用的列。
2. **Feed 是产品的"正面"。** 对大多数用户来说，主界面要么是收件箱、要么是 feed。当前 aero-id 只有收件箱（消息），没有 feed。
3. **协作功能是用户粘性的来源。** @提及、关注、公开内容——这些是将"存储工具"转变为"协作平台"的功能。
4. **架构增量很小。** Feed 是现有表的统一查询；@提及可以复现 outbox + notification 模式。没有新的基础设施。

### 估算

| 组件 | 工作量 |
|------|--------|
| 公开文件发现端点 | 低（1-2 天） |
| 统一活动 Feed | 中（3-5 天） |
| @提及解析 + 通知 | 中（3-4 天，依赖 SSO 用户名查找） |
| 趋势/热门排序 | 低（1-2 天） |

---

## 方向五：事件源（Event Sourcing）深化 —— 事件版本化、回放、Schema 管理

### 现状

aero-id 有一个运行良好的事务性 outbox：

```
Write Path:  Create Message → INSERT message + outbox event (same TX)
Relay:       FetchUnpublished → Handle(dispatcher) → MarkPublished
Dispatcher:  event type → notification row → SSE push
Chain:       [Dispatcher | Webhook] (composable via Chain)
```

**当前能力：**

| 能力 | 状态 |
|------|------|
| 事件持久化到 `outbox_events` 表 | ✅ |
| 事件按类型路由到 Handler | ✅ |
| 重试 + 死信（超过 `max_retries`） | ✅ |
| 指数退避（retry_count + last_retry_at） | ✅ |
| 多个 Handler 链式调用（dispatcher + webhook） | ✅ |
| 发布追踪（published_at） | ✅ |
| Outbox 观测指标 | ✅ |

### 缺口

#### 5.1 事件 Schema 没有版本控制

当前 `outbox_events.payload` 是 `JSONB`，由 `json.Marshal(payload)` 创建。当事件处理代码发生变化时（例如 `messageSentPayload` 结构体增加 `timestamp` 字段），旧事件仍然以旧格式存在，新代码可能无法解析。

```go
type messageSentPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
    // 如果未来增加 Timestamp string 字段
    // 旧事件不会有这个字段
}
```

**当前行为：** `json.Unmarshal` 对缺失字段是宽松的（零值），但对新加的必填字段会静默失败。如果 handler 依赖新字段，旧事件可能被不正确地处理或静默忽略。

**建议：** 增加 `event_version` 列到 `outbox_events` 表，Handler 根据版本进行不同的反序列化：

```sql
ALTER TABLE outbox_events ADD COLUMN event_version INT NOT NULL DEFAULT 1;
```

```go
type messageSentPayloadV1 struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
}

type messageSentPayloadV2 struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
    Timestamp string `json:"timestamp,omitempty"`
}
```

#### 5.2 无事件回放能力

当前事件被消费（`published_at` 设置为非 NULL）后就不可撤销了。没有：

- **回放特定时间范围的事件**：`POST /v1/admin/replay-events?from=2026-06-01&to=2026-06-30&type=message.sent`
- **回放单个事件**：通过 ID 重新投递
- **回放所有死信**：批量重新投递 `dead_letter = true` 的事件

**为什么需要这个能力：**
1. **灾难恢复**：通知系统因 bug 丢失了一批事件，可以回放修复
2. **测试**：在 staging 环境模拟生产事件流水
3. **审计**：检查"当事件 X 发生时，系统做了什么"
4. **外部消费者同步**：新的 webhook 消费者加入时，需要消费历史事件

**建议：** 在 admin API 中增加事件回放端点。回放不是复制事件——而是使用原始事件的 payload 创建**新**的 outbox 事件，进入正常的 relay 流水：

```http
POST /v1/admin/replay-events
Authorization: Bearer <admin-token>
Content-Type: application/json

{
    "event_type": "message.sent",
    "from": "2026-06-01T00:00:00Z",
    "to": "2026-06-30T23:59:59Z",
    "max_events": 10000
}
```

#### 5.3 outbox_events 表无限增长

`outbox_events` 是一个只追加的表——事件从不删除，只有 `published_at` 被设置。在长期运行的生产实例中：

- 如果每秒 1000 个事件，每天 = 86.4M 行
- 即使索引良好，`FetchUnpublished` 查询需要扫描 `published_at IS NULL` 的行
- 表膨胀导致 autovacuum 压力

**当前状态的保护：** `max_retries` 超过后标记为 `DeadLetter`，但行仍然存在。

**建议：** 增加 outbox 事件保留策略：

```yaml
outbox:
  retention:
    published: 7d    # 已发布事件保留 7 天后可清理
    dead_letter: 30d  # 死信保留 30 天供审计
```

后台 Job（复用 GC sweeper 模式）定期删除过期的 outbox 行：

```go
func (s *OutboxSweeper) Run(ctx context.Context) {
    cutoff := s.now().Add(-s.cfg.PublishedRetention)
    s.repo.DeletePublishedBefore(ctx, cutoff)
    
    dlCutoff := s.now().Add(-s.cfg.DeadLetterRetention)
    s.repo.DeleteDeadLetterBefore(ctx, dlCutoff)
}
```

#### 5.4 事件驱动的 Webhook 订阅缺少生命周期管理

当前 webhook 是**静态配置的**：

```yaml
outbox:
  webhook:
    url: "https://hooks.example.com/events"
    signing_key: "${AERO_ID_WEBHOOK_SIGNING_KEY}"
    event_types: ["message.sent", "message.read"]
```

**没有：**
- 用户/管理员可以注册/管理 webhook 的 API
- webhook 的健康监测（endpoint 响应慢/失败 → 暂停发送）
- 每个 webhook 的独立签名密钥
- webhook 日志（已交付、失败、延迟）

**建议：** 构建 `webhook_subscriptions` 表 + CRUD API，但使用**复用而非新建**的原则——复用 `outbox` 的 `Handler` 接口和 `Chain` 机制：

```sql
CREATE TABLE webhook_subscriptions (
    id              UUID PRIMARY KEY,
    owner_uid       VARCHAR(64) NOT NULL,
    url             VARCHAR(2048) NOT NULL,
    event_types     TEXT[] NOT NULL DEFAULT '{}',
    signing_key     VARCHAR(64) NOT NULL,
    headers         JSONB DEFAULT '{}',
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    failure_count   INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

```http
POST /v1/webhooks                          → Create subscription
GET  /v1/webhooks                          → List my subscriptions
PATCH /v1/webhooks/:id                     → Update URL / events
DELETE /v1/webhooks/:id                    → Deactivate
POST /v1/webhooks/:id/test                 → Send test event
```

**Outbox 集成：** `Relay` 需要能够分派事件给**多个** webhook handler（每订阅一个）。而不是当前的单链模式，改为 fan-out：

```go
// 在 app.go 中，不是构建一个 WebhookHandler，
// 而是构建一个 MultiWebhookHandler，它查询所有活跃订阅
handler := outbox.NewMultiWebhookHandler(webhookRepo, log)
relayHandler = outbox.NewChain(dispatcher, handler)
```

### 建议方向总结

| 项目 | 工作量 | 优先级 | 原因 |
|------|--------|--------|------|
| 事件版本控制 | 低（1-2 天） | P1 | 防止未来 schema 变更时事件丢失 |
| 事件回放 API | 中（2-3 天） | P2 | DR + 审计 + 测试 |
| Outbox 清理 Job | 低（1-2 天） | P1 | 防止表无限膨胀 |
| Webhook 订阅管理 | 高（5-7 天） | P1 | 从"运维配置"到"开发者平台"的跨越 |

### Why This Direction

1. **现有投资的最大化复利。** aero-id 已经构建了事务性 outbox、relay、chain、dispatcher——这是最难的 70%。剩下的 30%（版本控制、回放、webhook 管理）将这个基础设施从"内部管道"转变为"平台产品"。
2. **可观测性与审计的边界。** 没有事件回放和版本控制，outbox 事件一旦发布就不可审计——如果系统行为异常，无法"重放"来诊断。
3. **运维成熟度需求。** `outbox_events` 表无限增长是一个实际的运维问题。在 22 个迁移之后，第 23 个应该是关于数据生命周期管理的。
4. **Webhook 是生态系统的入口。** 开发者通过 webhook 构建集成。没有自服务 webhook 管理，每个集成需要运维配置变更——这不是平台级产品。

### 估算

| 组件 | 工作量 |
|------|--------|
| 事件版本列 + 版本感知反序列化 | 低（1-2 天） |
| 事件回放 admin API | 中（2-3 天） |
| Outbox 事件清理 Job | 低（1-2 天） |
| Webhook 订阅表 + CRUD API | 中（3-5 天） |
| MultiWebhookHandler fan-out 集成 | 中（2-3 天） |

---

## 整体路线图思考

```
          现在                                        3个月后
          │                                            │
          ▼                                            ▼
    ┌─────────────────────────────────────────────────────────┐
    │  技术债修复周期                                         │
    │  ├─ 中文搜索分词 (P0)     ← 产品级 Bug                  │
    │  ├─ 事件版本化 (P1)       ← 架构防御性                    │
    │  ├─ Outbox 清理 (P1)      ← 运维必然性                   │
    │  └─ 公开文件端点 (P2)     ← 解锁死功能                   │
    ├─────────────────────────────────────────────────────────┤
    │  高价值功能周期                                           │
    │  ├─ 图片缩略图管道 (P1)   ← 文件产品体验核心              │
    │  ├─ 批量操作 API (P1)     ← N+1 问题的解决方案            │
    │  ├─ 资源 TTL (P1)         ← 隐私/合规需求                 │
    │  └─ Webhook 订阅管理 (P1) ← 平台化基础                    │
    ├─────────────────────────────────────────────────────────┤
    │  平台扩展周期                                             │
    │  ├─ 统一活动 Feed (P2)    ← 产品黏性来源                  │
    │  ├─ @提及 (P2)            ← 协作基础                      │
    │  ├─ 病毒扫描 (P2)         ← 安全责任                      │
    │  └─ 事件回放 (P2)         ← DR 能力                      │
    └─────────────────────────────────────────────────────────┘
```

### 选择原则

1. **先用先得：中文搜索分词**。这是当前产品中唯一的"完全不可用"功能，优先级最高。
2. **基础设施先于产品**：事件版本化和 outbox 清理在任何依赖 outbox 事件的新功能之前完成。
3. **复用已有模式**：GC sweeper → ExpirySweeper + OutboxSweeper。Dispatcher → Webhook 订阅。outbox event → 回放。
4. **不引入新基础设施**：这五个方向全部可以使用现有的 Postgres + Go runtime + 存储后端实现。不需要 Kafka、Redis（除非分布式限流）、Elasticsearch。

---

## 附录：与现有架构的一致性检查

| 方向 | 新表 | 新端点 | 新配置 | 现有模式复用 |
|------|------|--------|--------|-------------|
| 文件预览/转码 | `file_previews` | `GET /v1/files/:id/preview/:variant` | `preview.*` | outbox event → goroutine worker |
| 国际化 | 无（消息字典） | 无 | `i18n.default_language` | middleware + pkg/errors |
| 批量操作/TTL | `expires_at` 列 | `POST /v1/batch/*` | `retention.*` | GC sweeper `Start/Stop` |
| Feed/发现 | 无（查询已有表） | `GET /v1/discover/*`, `GET /v1/feed` | 无 | 现有 repository 只读查询 |
| 事件源深化 | `webhook_subscriptions` | `CRUD /v1/webhooks`, `POST /v1/admin/replay-events` | `outbox.retention.*` | outbox + sweeper + chain |

所有五个方向都遵循现有的**handler → service → repository** 分层，不破坏任何 ADR 决策，不引入外部依赖（除病毒扫描可能需要 `clamd` 套接字连接外）。
