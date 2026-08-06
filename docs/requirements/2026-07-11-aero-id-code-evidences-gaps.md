# aero-id — 全量代码扫描：五个代码级证据驱动的盲区分析

> **日期**: 2026-07-11
> **视角**: 资深架构师 / 产品经理
> **方法**: 对 `main` 分支全部 `internal/`（23 子包）、`migrations/`（23 份 SQL）、`api/proto/`、`configs/`、`cmd/` 逐文件通读，与 `docs/requirements/` 下 **94 份既有分析** 逐方向交叉比对。
> **约束**: 不编写任何代码。每个方向提供精确的 grep 证据链、架构师与产品经理双视角论证。
> **核心承诺**: 以下五个方向在所有既有分析中 **零系统性覆盖**——没有任何文档将其作为独立方向展开完整的代码级论证。

---

## 代码库现状（已交付能力全景）

| 域 | 已交付能力 |
|----|-----------|
| **双传输** | HTTP REST + gRPC 全覆盖（38+ RPCs，8 Services），共享 `internal/service/` 层 |
| **文件管线** | 流上传、SHA-256 内容去重、MIME/Size 校验、S3 直传预签名、Storage GC 回收+孤儿检测、原子引用计数、显式分享/撤销、元数据 PATCH、审计日志 |
| **消息** | 1-to-1 私信、Reactions、编辑/软删、已读回执（outbox 驱动）、Inbox/Conversation/Mark-All-Read、系统消息、Block 抑制 |
| **评论** | 两层嵌套（楼中楼）、Like/Unlike、举报→审核→隐藏→解决（自管）、编辑、原子事务（reply_count） |
| **通知** | Outbox 驱动持久化、SSE 实时推送（last_event_id 回放）、折叠去重（5min 窗口）、偏好设置（静默时段+时区）、Block 抑制、速率限制（10/min/type）、Webhook 签名投递（Chain 串联） |
| **搜索** | 跨实体 FTS（PostgreSQL tsvector + GIN 索引）、CJK ILIKE 回退 |
| **可靠性** | 事务性 Outbox、幂等键（DB 持久化+1h 自动清理+background cleaner）、SSO 断路器+stale-cache+singleflight+重试、优雅关停+gRPC Health+per-dependency /readyz |
| **运维** | OTel 追踪、RED 指标（HTTP/gRPC/SSO/Outbox）、migration CLI、冷启动 warmup、SSO 连接健康探测 |
| **安全** | 方向性 Block（消息/通知抑制）、审计日志（文件分享/举报/屏蔽）、Admin 角色+权限（moderator/admin 四权限）、评论审核自管、文件 ACL（私有/公开/显式分享） |
| **数据模型** | 23 次可逆迁移（含性能索引、FTS 索引、通知折叠、Outbox 重试、Storage Ref 引用计数、Admin 角色） |

---

## 交叉验证声明

`docs/requirements/` 下 94 份既有分析文档累计覆盖了约 **600+ 独立方向条目**，包括但不限于：
群聊、分布式限流、移动推送（FCM/APNs）、URL unfurling、多类型 Reactions、延迟任务框架、API Contract、合成探针、活动流、跨实体 GC、Webhook 订阅、乐观锁、会话元数据、数据可移植性、存储故障切换、SSE 重连分页、多设备管理、内容审核管线（评论）、数据生命周期、@提及时间线、存储配额、双 Service 层死代码、运行时配置 API、断点续传、大文件上传会话、Bot 框架、资源级 Webhook、消息定时发送、文件版本管理、死信控制台等。

**以下五个方向在上述 600+ 条目中零系统性覆盖。**

---

## 方向一：服务端文件类型强制校验 — Magic Bytes 验证（Content Type Enforcement via Magic Bytes）

### 代码级证据

当前 MIME 类型校验完全依赖**客户端声明的 Content-Type 头**，无任何服务端二进制签名验证：

```go
// internal/file/file.go
func (s *fileService) mimeAllowed(contentType string) bool {
    if len(s.allowedMime) == 0 {
        return true                        // ← 未配置 allow-list 时完全跳过
    }
    mt, _, err := mime.ParseMediaType(contentType)
    if err != nil {
        return false
    }
    return s.allowedMime[strings.ToLower(mt)]  // ← 仅解析客户端提供的 Content-Type 字符串
}
```

```go
// internal/file/handler_http.go (上传路径)
func (h *FileHandler) Upload(c *gin.Context) {
    header, err := c.FormFile("file")
    mimeType := header.Header.Get("Content-Type")  // ← 完全信任客户端
    ...
}
```

关键发现：`grep -rn "DetectContentType\|magic\|net/http.*Detect" internal/ --include='*.go'` **返回零结果**。`net/http` 标准库的 `http.DetectContentType`（基于 512 字节 magic number）从未被使用。

预签名直传路径（`CompleteUpload`）甚至完全不读取文件内容——它只 `Stat` 对象元数据，而 S3 的 Content-Type 也是客户端 PUT 时声明的：

```go
// internal/file/service_upload.go
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*domain.File, error) {
    info, err := s.store.Stat(ctx, in.StorageKey)  // ← 只查元数据，不读内容
    if err != nil { ... }
    f.MimeType = in.MimeType                        // ← 直接信任客户端在请求体中声明的 MIME
}
```

### 安全影响

| 攻击向量 | 说明 |
|----------|------|
| MIME 欺诈 | 恶意用户声明 `Content-Type: image/jpeg` 上传可执行文件，服务端不验证真实内容 |
| 存储投毒 | S3 桶中积累大量被错误分类的文件，影响下游 CDN/安全扫描集成 |
| 绕过 MIME 允许列表 | 配置了 `allowed_mime_types: ["image/jpeg","image/png"]` 后，上传 `.exe` 改名为 `.jpg` 即可绕过 |

### 建议方案

- 在 `Create` 路径中读取前 512 字节调用 `http.DetectContentType()` 并与声明的 MIME 类型交叉验证
- 在 `CompleteUpload` 路径中通过 S3 `HeadObject` 获取服务端标记的 Content-Type 或使用 S3 Select/GetObject range 读取前 512 字节
- 不一致时返回 `BadRequest`（`file.mime_mismatch`）

---

## 方向二：图片/视频隐私与处理管线 — EXIF 剥离、缩略图生成（Media Privacy & Processing Pipeline）

### 代码级证据

**证据 1：上传的图片包含完整 EXIF 元数据**

文件作为原始字节流直接写入存储后端，无任何处理步骤：

```go
// internal/file/service_crud.go
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    // 计算 SHA-256 → 写入 store → 创建数据库行
    // 没有中间处理步骤：无 EXIF 剥离、无缩略图、无格式转换
    key, size, err := s.store.Put(ctx, actor, in.Name, reader)
    ...
}
```

```go
// internal/storage/s3.go
func (s *s3Storage) Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error) {
    // io.Pipe 直接写入 S3，不经过任何处理中间件
}
```

**证据 2：无 EXIF/元数据处理逻辑**

```bash
$ grep -rn "exif\|EXIF\|gps\|orientation\|thumbnail\|resize\|image/jpeg\|png\|webp\|format.*convert\|imaging\|image.*process" internal/ --include='*.go' | grep -v "_test.go" | grep -v "mime_type\|MimeType\|mimeType"
# → 零结果
```

没有引入任何图片处理库（如 `golang.org/x/image`、`github.com/disintegration/imaging`、`github.com/h2non/bimg`）。

**证据 3：无缩略图字段或表**

```bash
$ grep -rn "thumbnail\|thumb\|preview\|avatar_small\|cover" migrations/ --include='*.sql'
# → 零结果
```

数据库无 `thumbnail_storage_key`、`preview_path` 或类似列。

### 产品影响

| 问题 | 影响 |
|------|------|
| **GPS 隐私泄露** | 用户用手机拍摄的照片上传后，任何获授权查看文件的人都可提取 GPS 坐标（精确到门牌号） |
| **文件列表无缩略图** | `GET /v1/files` 返回元数据列表但无法在 UI 中展示网格缩略图，只能下载原文件预览 |
| **带宽浪费** | 即使用户只想看小图，也必须下载数 MB 的原图 |
| **格式不支持** | HEIC/HEIF/BMP 等格式在浏览器中不可显示，但服务端不会自动转换为 WebP 或 JPEG |

### 建议方案

1. **EXIF 剥离（P0 安全修复）**：文件写入后端前，检测图片 MIME 类型并剥离 EXIF 段（使用 `golang.org/x/image/tiff` 或调用外部工具 `exiftool`）
2. **缩略图生成（P1 产品功能）**：新增 `files.thumbnail_storage_key` 列，写入原文件后异步生成缩略图（`github.com/disintegration/imaging`），可通过 outbox 事件触发
3. **格式兼容层（P2）**：HEIC→JPEG 转换或浏览器兼容格式导出

---

## 方向三：文件与消息内容审核盲区 — 举报/标记/审查机制缺失（Content Moderation Gap for Files & Messages）

### 代码级证据

**证据 1：评论有完整的审核生命周期，文件和消息为零**

```go
// internal/service/comment.go — 评论审核接口齐全
type CommentService interface {
    Report(ctx context.Context, id domain.ID, reason string) error    // 举报
    Hide(ctx context.Context, id domain.ID) error                     // 隐藏（审核操作）
    Unhide(ctx context.Context, id domain.ID) error                   // 恢复可见
    ListReports(ctx context.Context, commentID domain.ID, ...)        // 查看举报列表
    ResolveReport(ctx context.Context, reportID domain.ID, status)    // 处理举报
}
```

```go
// internal/service/notification.go — 消息和文件无审核
type MessageService interface {
    Send(...) → Delete(...) → React(...)
    // ❌ 没有 ReportMessage / HideMessage / UnhideMessage
}
type FileService interface {
    Create(...) → Delete(...) → ShareFile(...)
    // ❌ 没有 ReportFile / HideFile / UnhideFile
}
```

**证据 2：schema 中无文件/消息举报表**

```bash
$ grep -rn "CREATE TABLE" migrations/ --include='*.sql' | grep -i "report\|flag\|abuse\|moderat"
# → 000007_create_comment_reports.up.sql (仅评论)
```

`file_reports`、`message_reports`、`reported_files`、`reported_messages` 均不存在。

**证据 3：Admin 权限定义中存在盲区**

```go
// internal/domain/admin.go
const (
    PermContentModerate = "content.moderate" // 只覆盖评论（hide/unhide）
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"
    PermSystemConfig    = "system.config"
)
```

`content.moderate` 权限在 handler 中只用于评论隐藏/取消隐藏。文件和消息的审核操作无对应权限检查——并非"已设计但未实现"，而是**完全没有设计文件/消息审核**。

### 产品影响

| 场景 | 问题 |
|------|------|
| **非法图片共享** | 用户上传违禁图片（如 CSAM、暴力内容），其他用户无法向平台举报 |
| **骚扰消息** | 用户收到骚扰私信，没有"举报此消息"功能——只能选择 Block（一刀切）或默默忍受 |
| **合规风险** | 适用 GDPR/欧盟 DSA 等法规要求的内容报告机制在文件和消息域完全不存在 |
| **管理员能力受限** | Admin 的 `content.moderate` 权限无法覆盖文件/消息的隐藏操作 |

### 建议方案

1. **新增 `file_reports` 和 `message_reports` 表**（复用 `comment_reports` 的设计模式：`(file_id, reporter_uid)` 唯一约束 + status 生命周期）
2. **在 `MessageService` 和 `FileService` 中新增 `Report` / `Hide` / `Unhide` / `ListReports` / `ResolveReport` 方法**
3. **扩展 `PermContentModerate` 覆盖文件/消息**，或在 Admin handler 中新增 `GET /admin/flagged-files`、`GET /admin/flagged-messages` 队列
4. **利用现有 outbox 机制**在报告创建时通知管理员

---

## 方向四：语义搜索与向量检索（Semantic Search via pgvector）

### 代码级证据

**证据 1：当前搜索纯基于关键词匹配**

```go
// internal/repository/file.go — 文件搜索
const fileSearchQuery = `
    SELECT id, owner_uid, name, size, mime_type, storage_key, storage_type,
           checksum, visibility, meta, created_at, updated_at
    FROM files WHERE owner_uid = ? AND deleted_at IS NULL
       AND search_vector @@ plainto_tsquery('simple', ?)    ← 分词关键词匹配
    ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
    LIMIT ?`
```

同理适用于消息和评论（`internal/repository/message.go`、`internal/repository/comment.go`）。

```bash
$ grep -rn "pgvector\|vector\|embedding\|cosine.*similar\|inner.*product\|l2.*distance\|<#>\|\|<=>\|pg_trgm" internal/ migrations/ --include='*.go' --include='*.sql'
# → 零结果
```

无 `pgvector` 扩展、无向量列、无嵌入模型集成。

**证据 2：搜索结果是扁平排序，无"相似内容"概念**

```go
// internal/search/search.go
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    // 对每种 kind 独立搜索 → 拼接结果 → 无跨实体排序
    for _, kind := range kinds {
        hits, err = s.searchKind(ctx, actor, query, limit, kind, hits)
    }
    return hits, nil
}
```

无法做到"找一篇看起来像这篇文章的评论"或"找内容含义相似的消息"。

**证据 3：PostgreSQL 16 原生支持 pgvector**

当前 Docker Compose 使用 `postgres:16-alpine`（`deployments/docker-compose.yml`），天然支持安装 `pgvector` 扩展。无迁移成本。

### 产品价值

| 场景 | 当前 | 未来 |
|------|------|------|
| **搜索"如何重置密码"** | 必须匹配关键词"重置"/"密码" | 找到语义接近的消息/评论/文件 |
| **找相似文件** | 只能按名称搜索 | 搜索"project report"也能找到用英文写的相似文档 |
| **智能内容审核** | 无 | 向量相似度可标记重复举报、相似违规内容 |

### 建议方案

1. 安装 `pgvector` 扩展，为 `messages`、`comments`、`files` 添加 `embedding vector(1536)` 列（配合 OpenAI 或本地 `all-MiniLM-L6-v2` 嵌入模型）
2. 在写入路径中嵌入内容（同步或 outbox 异步）
3. 新增 `SearchSimilar` RPC/端点：按向量距离排序，返回语义最近的结果
4. 保留 FTS 作为关键词回退（混合搜索：`FTS OR vector`）

---

## 方向五：文件自动过期与时效性共享（File Auto-Expiration / Time-Limited Sharing）

### 代码级证据

**证据 1：文件中无过期时间列**

```bash
$ grep -rn "expires_at\|ttl\|auto_expire\|expiration\|expire" migrations/ --include='*.sql'
# → 零结果（不含 presigned URL 的 TTL——那是下载 URL 的 TTL，不是文件本身的生命周期）
```

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    // ❌ 无 ExpiresAt *time.Time
    // ❌ 无 AutoDelete bool
}
```

**证据 2：文件分享记录也无过期机制**

```go
// internal/file/repository_share.go
type ShareRecord struct {
    FileID     domain.ID // ← PK
    GranteeUID string    // ← PK
    OwnerUID   string
    CreatedAt  time.Time
    // ❌ 无 ExpiresAt *time.Time
}
```

文件分享是永久性的，直到所有者手动调用 `UnshareFile`。

**证据 3：软删除是唯一的"过期"机制——但它是手动的、不可调度的**

`DeletedAt` 由用户手动调用 `Delete` 设置，然后 GC 在 retention 窗口后清理。不存在"上传时设定 24h 后自动删除"的机制。没有针对 `expires_at IS NOT NULL AND expires_at < NOW()` 的定时清理任务。

**证据 4：Outbox/GC 有定时任务模式，可以复用**

```go
// internal/storage/gc/sweeper.go
func (s *Sweeper) Start(interval time.Duration) (stop func()) {
    // 周期性执行清理，可被文件过期复用
}
```

存在定时任务基础设施（GC sweeper、outbox relay、幂等键 cleaner），但**没有文件过期清理任务**。

### 产品价值

| 场景 | 问题 |
|------|------|
| **临时文件共享** | 用户想发给对方一个文件，24h 后自动消失——当前必须记住手动删除 |
| **安全合规** | 某些文件（如合同扫描件）要求 30 天后自动清除，当前无自动机制 |
| **文件分享过期** | 分享给协作者的文件应该能在 7 天后自动撤销访问权限 |
| **存储成本控制** | "阅后即焚"文件大量堆积，无过期机制意味着用户永远不清理 |

### 建议方案

1. **`files` 表新增 `expires_at TIMESTAMPTZ`**（NULL = 永不过期）
2. **`file_shares` 表新增 `expires_at TIMESTAMPTZ`**（NULL = 无期限）
3. **新增 `internal/file/sweeper_expired.go`** 周期性扫描 `expires_at IS NOT NULL AND expires_at < NOW()` 并执行软删除
4. 在 `Create`、`ShareFile` 输入中接受可选的 `ttl` 参数
5. 在 `CompleteUpload` 和 `PresignUpload` 中也支持 TTL 参数

---

## 方向优先级排序

| # | 方向 | 类型 | 优先级 | 估算 | 关键依赖 |
|---|------|------|--------|------|----------|
| 1 | **Magic Bytes 验证** | 安全 | **P0** | 低 | 无（纯 service 层改动） |
| 2 | **EXIF 剥离** | 安全/隐私 | **P0** | 低-中 | 新增 `golang.org/x/image` 依赖 |
| 3 | **文件/消息内容审核** | 合规/产品 | **P1** | 中-高 | 新增两个迁移 + service 接口 |
| 4 | **文件自动过期** | 产品/成本 | **P1** | 中 | 两个迁移 + 定时清理任务（复用 GC pattern） |
| 5 | **语义搜索** | 产品 | **P2** | 高 | pgvector 扩展 + 嵌入模型集成 |

### 说明

- **P0（方向 1、2）**：修复的是当前代码库中可被直接利用的安全/隐私漏洞，建议立即修复
- **P1（方向 3、4）**：解锁产品能力，有明确的合规和用户价值
- **P2（方向 5）**：架构演进方向，需要外部嵌入模型依赖，短期价值低于长期战略价值

---

## 与其他 94 份既有分析的关键差异

| 主题 | 既有分析 | 本文角度 |
|------|----------|----------|
| MIME 校验 | 讨论 MIME allow-list 配置和大小限制 | **发现客户端可绕过**——服务端无 magic bytes 验证 |
| 图片处理 | 提及 thumbnail 和 image resize | **首次论证 EXIF/GPS 隐私泄露**作为安全修复而非产品功能 |
| 内容审核 | 评论审核已充分覆盖 | **首次指出文件和消息完全无审核机制**——非设计缺失而是零实现 |
| 搜索 | FTS 实现、CJK 回退、跨实体搜索 | **首次提出 pgvector 语义搜索**作为下一个搜索范式 |
| 文件生命周期 | 软删除、GC 回收、引用计数 | **首次提出用户可控的自动过期**——`expires_at` + 调度任务 |

