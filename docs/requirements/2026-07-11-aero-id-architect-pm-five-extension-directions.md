# aero-id — 架构师/产品经理视角：五项高价值扩展方向

> **日期:** 2026-07-11
> **方法:** 全量代码库逐文件扫描 — `internal/`（全部 23+ 子包，~180 Go 源文件）、`migrations/`（全部 22 份 SQL）、`api/proto/`、`configs/`、`cmd/`、`Makefile`。与 `ROADMAP.md`、`docs/REVIEW_*`、`docs/specs/aero-id-design.md` 及 `docs/requirements/` 下已有分析做交叉比对。
> **约束:** 不编写任何代码。每个方向提供代码级证据与架构师/产品经理双视角论证。
> **视角:** 身份数据生命周期 · 存储运营成本 · 可观测性盲区 · 多租户基础 · 内容消费体验

---

## 代码库成熟度概述

aero-id 当前状态已经远超典型 MVP：事务性 Outbox、SSE 实时推送、SSO 断路器 + 服务降级、存储 GC 回收、FTS 全文搜索、完整 HTTP↔gRPC 双栈等价、幂等键、速率限制、操作审计、管理员角色体系——这些都是一流生产级服务的标志。

在此基础之上，以下 **5 个方向** 在已交付功能的外围或盲区中存在系统性空白。它们不是"再加一个 endpoint"的局部修补，而是需要**架构层新增**的平台级能力。每个方向都基于代码中的具体证据，且在当前 `docs/requirements/` 数据库中 **没有以独立方向形式被系统性论证**。

---

## 方向一：SSO 身份生命周期集成（Identity Lifecycle Webhook）

### 为什么需要

aero-id 将身份委托给外部 SSO，这是正确的架构决策。但当前集成是**纯请求时验证**（request-time validation）：SSO 只在每次 API 调用的 `Auth` 中间件中被查询。一旦 SSO 侧发生**身份变更事件**（用户删除、账号封禁、权限吊销），aero-id 完全没有感知能力，导致：

1. **安全漏洞**：用户在 SSO 被删除/封禁后，其在 aero-id 中已上传的文件、已发送的消息、已发表的评论**永久保留**且仍可被其他用户访问（公共文件、对话历史）。这是一个典型的数据残留风险。
2. **合规风险**：GDPR 删除权（Right to Erasure）要求服务商在收到用户删除请求后级联清除该用户在所有系统中的数据。当前 aero-id 无法响应此类事件。
3. **产品体验**：给已删除用户的 `uid` 发送消息→消息成功写入但接收方永远看不到；查看评论列表→`actor_uid` 解析为空（SSO 返回空 map），显示"未知用户"。

### 代码级证据

**证据 1：`Auth` 中间件只做 request-time 验证，不订阅变更**

```go
// internal/middleware/auth.go
func Auth(v TokenValidator) gin.HandlerFunc {
    return func(c *gin.Context) {
        token := bearerToken(c.GetHeader("Authorization"))
        // ...
        uid, err := v.ValidateToken(c.Request.Context(), token)
        // 验证通过就放行，没有缓存 uid 的元数据（如 is_banned、is_deleted）
        // 也没有在后台监听 SSO 的生命周期事件
        c.Set("actor_uid", uid)
        c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), uid))
        c.Next()
    }
}
```

**证据 2：`UserCache` 永不过期删除——cache 只有 TTL 弹射，没有失效推送**

```go
// internal/sso/user_cache.go — UserCache
type userCacheEntry struct {
    user  User
    expiresAt time.Time
}

func (c *UserCache) Get(uid string) (User, bool) { /* TTL 检查 */ }
func (c *UserCache) Set(uid string, u User)       { /* 设置 TTL */ }
// 没有 Invalidate(uid) / InvalidateMany(uids) 方法
// 没有 webhook 端点接收 SSO 的失效通知
```

**证据 3：系统不存在"用户状态"概念——`domain.actor.go` 只有 uid 字符串**

```go
// internal/domain/actor.go
func WithActor(ctx context.Context, uid string) context.Context {
    return context.WithValue(ctx, actorKey, uid)
}
// uid 是纯字符串，不附带 is_active、is_banned、deleted_at 等状态
```

所有业务表（`files`、`messages`、`comments`、`notifications`、`file_shares`）的 `owner_uid`/`from_uid`/`to_uid`/`recipient_uid` 列均为**无 FK 约束的字符串**。SSO 删除用户后，这些数据变成无主孤儿。

**证据 4：发消息给不存在的用户仍成功写入**

```go
// internal/message/service.go — Send
func (s *messageService) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
    actor, err := mustActor(ctx)
    // ...
    // 不验证 toUID 是否在 SSO 中存在
    // 消息直接写入 messages 表
}
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **SSO → aero-id webhook** | 新增 `POST /_sso/webhook/user-lifecycle` 端点（HMAC 签名验证），接收 `user.deleted` / `user.banned` / `user.reactivated` 事件 |
| **UserStatusCache** | 在 `internal/sso/` 增加 `user_status_cache`，缓存 `(uid → is_active)` 状态，webhook 到达时立即失效 |
| **写入时验证** | `SendMessage` 等创建涉及其他用户的操作加一个轻量 `IsActive(uid) bool` 检查（命中 cache，不穿透 SSO），非活跃用户拒收 |
| **后台级联清理** | Outbox 消费 `user.deleted` 事件后，异步清洗该用户的文件（转匿名/隐藏）、评论（标记 `Deleted`）、消息（保留但标记"用户已注销"） |
| **GDPR 导出端点** | 新增 `GET /v1/my-data` 导出当前用户所有数据（文件元数据、消息、评论），JSON 格式压缩包 |

### 边界情况

- **雪崩级联**：如果 SSO 批量发出 10K 用户删除事件，后台清理必须限速（batched + rate-limited outbox 消费）。
- **自删除 vs 管理员删除**：自删除（用户"注销账号"）应清理数据；管理封禁（ban）应隐藏内容但保留审计证据。
- **重新激活**：`user.reactivated` 事件应恢复评论可见性但不恢复已物理删除的数据。
- **SSO webhook 重放**：幂等键防止同一事件重复处理。

---

## 方向二：存储分层与生命周期管理（Storage Tiering & Lifecycle Policies）

### 为什么需要

当前所有文件存在单一存储后端，且永久保留直到被显式删除（软删除 + GC 回收）。这对一个文件服务来说，在**成本**和**合规**两个维度存在显著缺口：

1. **成本失控风险**：不存在"冷数据自动沉降"机制。用户上传的文件不论多老、多不活跃，都与热数据享受相同存储成本。在 S3 场景下，标准存储 vs Glacier 有 ~5x 价差，对存储密集场景影响巨大。
2. **合规缺失**：不存在文件级 TTL（自动过期）、保留策略（必须保留 N 天）、法律封存（legal hold）。这在与合规敏感行业（医疗、金融）的集成中是硬门槛。
3. **产品缺失**：用户无法设置文件自动过期（"分享链接 7 天后失效"），无法查看存储空间使用明细。

### 代码级证据

**证据 1：`File` 模型无生命周期字段**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
// 无 ExpiresAt、无 StorageClass、无 RetentionPolicy、无 LegalHold
```

22 份迁移中无任何 `expires_at`、`storage_class`、`retention_policy` 相关列。

**证据 2：`FileStorage` 接口无分层操作**

```go
// internal/storage/storage.go
type FileStorage interface {
    Put(ctx context.Context, key string, r io.Reader) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
}
// 无 CopyToClass(source, destClass) — 无法在存储类之间迁移
// 无 SetStorageClass(key, class) — 无法更改现有对象的存储类
```

**证据 3：StorageGC 只负责清理已删除数据，不负责沉降冷数据**

```go
// internal/storage/gc/sweeper.go — Run()
func (s *Sweeper) Run(ctx context.Context) (Stats, error) {
    purged, blobs, err := s.purgeSoftDeleted(ctx)  // 只清理软删除的数据
    // ...无冷数据迁移逻辑
    return stats, firstErr
}
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **文件级 TTL** | 新增 `expires_at` 列 + 后台 `FileExpirer` 组件（类似 sweeper），到期后自动软删除文件 |
| **存储分层策略** | 新增 `storage_class` 列（`hot` / `warm` / `cold`），`FileStorage` 扩展 `CopyToClass`, `ListByClass` |
| **冷数据迁移 job** | 后台定时任务扫描 `updated_at > 90d && storage_class = hot` 的文件，迁移到 `warm`（S3 Infrequent Access）或 `cold`（S3 Glacier Deep Archive） |
| **用户存储配额** | 新增 `storage_quotas` 表 + 上传时 `SELECT SUM(size) WHERE owner_uid = ?` 校验 |
| **分享链接过期** | `ShareFile` 增加可选的 `expires_at` 参数，挂后台 `ShareExpirer` 自动撤销过期分享 |

### 边界情况

- **访问触发回热**：读取冷数据的请求应触发异步回热（restore）并返回 `202 Accepted` + 预计等待时间。
- **分层与 GC 互斥**：迁移中的文件不能被 GC 回收。需要迁移事务：先 `CopyToClass` 成功，再更新 `storage_class` 列。
- **合规 vs 成本**：法律封存（legal hold）的文件应跳过自动迁移和过期，始终保留在热存储。
- **配额弹性**：配额检查应支持软限制（告警）和硬限制（拒绝写入）两种模式。

---

## 方向三：Outbox 死信可观测性与恢复面板（Dead Letter Observability & Recovery）

### 为什么需要

Outbox 是 aero-id 异步可靠性的核心基础设施。当事件处理持续失败超过 `maxRetries`（默认 20 次），事件被标记为 `dead_letter` 并**永久静默丢弃**。但当前系统存在一个严重的运维盲区：

1. **死信不可见**：没有 API 或 UI 列出死信事件、查看其 payload 和失败原因。
2. **死信不可恢复**：没有机制对死信进行手动重试、编辑 payload 后重试、或者直接丢弃。
3. **无告警**：没有主动告警机制（当死信数量超过阈值时通知运维）。
4. **无失败模式分类**：无法区分"临时性失败（可重试）"和"永久性失败（payload 损坏，重试无用）"。

对于依赖 outbox 做通知投递、webhook 分发、审计记录的核心系统，这是一个影响可靠性的盲区。

### 代码级证据

**证据 1：死信后仅标记字段，再无处理路径**

```go
// internal/outbox/relay.go — drain()
for _, e := range events {
    if err := r.handler.Handle(ctx, e); err != nil {
        // 记录 MarkFailed — 当 retryCount >= maxRetries 时设置 dead_letter=true
        if err := r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now); err != nil {
            r.log.Warn("outbox: mark failed failed", ...)
        }
        continue
    }
}
// 死信事件（dead_letter=true）被 FetchUnpublished 过滤掉 —— 再也不会被处理
```

**证据 2：`FetchUnpublished` 过滤死信**

```go
// internal/outbox/repository.go 中，GormRepository.FetchUnpublished
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*outbox.Event, error) {
    var rows []outboxEventRow
    err := r.db.WithContext(ctx).
        Where("published_at IS NULL AND dead_letter = false").
        //                    ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ 死信被硬过滤
        Order("created_at ASC").
        Limit(limit).
        Find(&rows).Error
        // ...
}
```

**证据 3：`outboxEventRow` 无 `last_error` 字段**

```go
// 内部类型，基于 outbox 表结构
type outboxEventRow struct {
    // ...
    RetryCount int
    LastRetryAt *time.Time
    DeadLetter  bool
    // 无 LastError string — 无法知道最后一次失败的原因
    // 无 Payload 的 human-readable 摘要字段
}
```

虽然可以通过 payload 反序列化推导部分信息，但运维人员无法直接看到："这条事件尝试了 20 次，最后几次的错误都是 'connection refused'——是下游服务挂了"。

**证据 4：Repository 接口无死信查询/操作**

```go
// internal/outbox/outbox.go
type Repository interface {
    Append(ctx context.Context, tx *gorm.DB, e *Event) error
    FetchUnpublished(ctx context.Context, limit int) ([]*Event, error)
    MarkPublished(ctx context.Context, ids []uuid.UUID) error
    MarkFailed(ctx context.Context, id uuid.UUID, retryCount, maxRetries int, at time.Time) error
    // 无 ListDeadLetters(ctx, page) — 无法查看死信
    // 无 RetryDeadLetter(ctx, id) — 无法手动重试
    // 无 DiscardDeadLetter(ctx, id) — 无法手动丢弃
}
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **`outbox_event_rows.last_error` 列** | 每次 MarkFailed 时记录错误信息摘要（≤ 512 字符），让运维知道失败原因 |
| **死信列表 API** | `GET /v1/admin/outbox/dead-letters?page` + `GET /v1/admin/outbox/dead-letters/:id` |
| **死信重试 API** | `POST /v1/admin/outbox/dead-letters/:id/retry` — 将事件重置为 `dead_letter=false, retry_count=0, published_at=null` |
| **死信丢弃 API** | `DELETE /v1/admin/outbox/dead-letters/:id` — 直接删除事件 |
| **死信告警** | Prometheus gauge `outbox_dead_letter_count` + 当 > 0 时发出告警（已有 `metrics.ObserveOutboxDrain` 扩展） |
| **死信自动通知** | 批量的死信可产生内部通知（`POST /_hooks/dead-letter-alert`），触发值班流程 |

### 边界情况

- **payload 包含 PII**：死信 payload 可能包含用户消息内容，列表 API 需要基于 admin role 做权限控制（已有 `audit.read` 权限）。
- **重试风暴**：批量重试大量死信可能压垮下游。`retry` API 应加入速率控制（每分钟最多重试 N 条）。
- **幂等安全问题**：重试一个"消息发送"事件的死信会导致重复消息，因为 outbox 是**至少一次**语义。重试操作应默认只适用于**幂等事件**或提示操作风险。

---

## 方向四：多租户/工作区隔离基础（Multi-Tenancy / Workspace Isolation）

### 为什么需要

当前 aero-id 是一个扁平的、全用户共享命名空间的服务。不存在"组织"、"工作区"或"团队"的实体概念。这在以下场景中是严重的产品和架构限制：

1. **B2B 不可扩展**：如果业务需要支持"公司 A 的员工只能看到公司 A 的文件"，当前架构完全无法实现——所有用户在同一命名空间。
2. **数据泄漏风险**：文件搜索返回与查询匹配的所有**公开文件**（`WHERE visibility = public`），没有任何租户隔离。一个用户理论上可以搜索到另一个完全不相关组织的文件。
3. **跨租户管理盲区**：Admin 角色在当前是全局的——有 `audit.read` 权限的用户可以查看**所有用户的审计日志**，无法限制为"仅查看自己租户的审计日志"。
4. **计费/用量归属**：无法按组织聚合存储用量、API 调用次数、活跃用户数。

### 代码级证据

**证据 1：系统中不存在"租户"维度——全为 flat 模型**

```go
// internal/domain/file.go
type File struct {
    OwnerUID string  // 用户级别，无 TenantID/WorkspaceID
    // ...
}
internal/domain/message.go
type Message struct {
    FromUID string  // 仅有发送者和接收者
    ToUID   string
    // ...
}
// 全部 22 份迁移：无 tenants 表、无 workspace_members 表
```

**证据 2：文件搜索无租户过滤**

```go
// internal/repository/file.go — SearchFiles 签名
func (r *fileRepo) SearchFiles(ctx context.Context, actor, query string, limit int) ([]*domain.File, error) {
    // actor 用于权限过滤"只看自己的和公开的"
    // 但"公开"（visibility=public）是全系统公开，非同一租户内公开
}
```

**证据 3：全局 Admin / 无租户域限**

```go
// internal/repository/admin.go — ListAudit
func (r *adminRepo) ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error) {
    // 无 tenantID 过滤条件——有 audit.read 权限的用户可以查看所有审计日志
    // 按 aero-id-design.md 的权限模型，admin 角色是全局的
}
```

**证据 4：Rate limiter 全局共享**

```go
// internal/app/app.go — New()
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
// 一个 rate limiter 实例服务所有用户——无法为不同租户设置不同速率限制
```

### 建议方案

> **架构原则**：多租户是侵入性变更。下面建议的是**增量演进路径**，而非一次性重写。

| 阶段 | 变更 | 影响 |
|------|------|------|
| **P0 — 租户身份锚点** | 在所有核心表加 `tenant_id VARCHAR(64)` 列（可为空=兼容旧数据）。SSO 集成在 token 验证时返回 `tenant_id`。 | 中 — 加列 + 迁移 |
| **P1 — 数据隔离** | `repository` 层所有查询加 `WHERE tenant_id = ?`（从 context 提取），默认值 "" 兼容旧数据 | 中 — repository 层改动 |
| **P2 — 管理作用域** | `admin_roles` 加 `tenant_id` 范围限制（`global` vs `tenant` 级别角色） | 低 |
| **P2 — 用量聚合** | 新增 `tenant_stats` 物化视图，汇总每个租户的文件数、存储量、消息数 | 中 |

### 边界情况

- **租户迁移**：不支持"将用户从租户 A 迁移到租户 B"——这是数据所有权变更，应通过应用层处理。
- **跨租户共享**：存在真实的跨租户场景（如合作方之间的文件共享），需要 grant-based 访问控制来绕过租户隔离。
- **SaaS vs 单租户**：建议通过配置开关控制多租户模式，单租户部署保持零性能开销。
- **租户数据删除**：租户注销时应触发该租户下所有数据的级联软删除。

---

## 方向五：内容处理管道——缩略图/预览与转码（Media Processing Pipeline）

### 为什么需要

aero-id 定位是一个文件（files）、消息（messages）、评论（comments）服务，**但当前文件上传后的处理是一条死胡同**：文件存入存储后端后，不做任何后续处理。这在产品维度和技术维度都是显著的缺失：

1. **用户体验断层**：上传一张 4000×3000 的 JPEG 后，列表视图仍需下载原图才能展示——传输开销大、渲染卡顿。市场同类服务（Google Drive、Dropbox、iCloud）均提供缩略图。
2. **带宽与存储浪费**：每条消息附带的图片以原始分辨率传输。在移动端场景下，4K 原图的传输耗时和流量消耗是产品留存杀手。
3. **缺乏预处理导致下游能力缺失**：没有缩略图就无法实现 masonry grid 布局、无法实现图片搜索（以图搜图）、无法生成视频海报帧。
4. **文档预览缺失**：无法查看 PDF/DOCX 文件的在线预览——当前只能下载查看。

### 代码级证据

**证据 1：Upload 路径终结于存储——无后续步骤**

```go
// internal/file/service_upload.go / service_crud.go
// file Upload/Create → 校验 → 存入 Store → 返回 File
// 无 post-processing hook，无 pipeline chain，无 thumbnail/job 创建
```

**证据 2：Domain 模型无 content-processing 状态**

```go
// internal/domain/file.go — File
type File struct {
    // ...
    Checksum string  // SHA-256，用于去重
    Meta     map[string]any  // 用户自定义元数据
    // 无 ProcessingStatus string  — 无处理状态（pending/processing/done/failed）
    // 无 ThumbnailKey string     — 无缩略图引用
    // 无 Previews map             — 无预览文件引用
}
```

**证据 3：没有异步处理基础设施——虽然 outbox 存在但未用于文件处理**

```go
// 对比：notification 使用 outbox 做异步处理
// 文件上传后不 emit 任何 outbox 事件
// 如果能 emit "file.uploaded" → 下游 handler 可以：
//   1. 生成缩略图
//   2. 提取文档预览
//   3. 执行病毒扫描
//   4. 更新搜索引擎索引
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **`file.previews` 表** | 新增预览文件存储：`(file_id, kind[varchar]: thumbnail_sm/thumbnail_md/preview_pdf, storage_key, width, height, size)` |
| **处理工作流** | 基于 outbox 的异步管道：上传完成 → emit `file.uploaded` → `MediaProcessor.Handle` → 生成缩略图/预览 → 存储 → 更新 file row |
| **缩略图生成策略** | images: 生成 150px / 300px / 600px 三种尺寸，保持宽高比。videos: 提取中间帧做海报帧。documents: 提取第一页文本片段 |
| **响应式图片 API** | `GET /v1/files/:id/preview/:kind` 返回对应尺寸的预览，带 CDN-friendly `Cache-Control` |
| **文件类型检测强化** | 当前基于 MIME type 声明——应扩展为 `libmagic`/`http.DetectContentType` 双重验证，防止 MIME 伪造 |

### 边界情况

- **邪恶文件（病毒/恶意软件）**：处理管道天然提供了插入病毒扫描（ClamAV）的钩子点。扫描失败的文件应标记 `malware` 状态，阻止下载。
- **处理超时**：大视频文件的转码可能耗时数分钟。outbox handler 应设置处理超时，超时后重新入队（retry），有次数限制。
- **存储成本**：缩略图/预览本身占用额外存储。需要权衡质量与存储。可选择"按需生成"（cache-based）而非"上传即生成"（eager）。
- **处理失败不阻塞上传**：上传必须即时成功返回。处理是异步的、最终一致——用户立即获得 file_id，预览延迟数秒后可用。

---

## 优先级与依赖关系

| # | 方向 | 优先级 | 前置依赖 | 估算工作量 | 核心受益者 |
|---|------|--------|----------|-----------|-----------|
| 1 | SSO 身份生命周期集成 | **P0** | 无 | 中 | 安全/合规/平台 |
| 2 | 存储分层与生命周期管理 | **P1** | 无 | 中-高 | 平台/财务 |
| 3 | Outbox 死信可观测性与恢复 | **P1** | 无 | 低-中 | SRE/运维 |
| 4 | 多租户/工作区隔离基础 | **P2** | 方向 1（SSO 提供 tenant_id） | 高 | 产品/B2B |
| 5 | 内容处理管道 | **P2** | 方向 1（处理状态存储于 File 扩展） | 高 | 最终用户 |

**建议顺序**：方向 3 最轻量（仅需新增 ~3 个 endpoint + 1 个 DB 列），可作为立即可交付的运维改进。方向 1 是平台安全基础，方向 2 直接降低运营成本。方向 4 和 5 牵引产品方向，但投入最大。

---

## 各方向与既有能力的架构映射

```
方向一：SSO 身份生命周期
  ┌──────────┐     webhook      ┌──────────┐
  │   SSO    │ ──────────────→  │  aero-id │
  │(identity)│  user.deleted    │          │
  └──────────┘   user.banned    │ outbox → │ → 级联清理
                                │ cascade  │
                                └──────────┘

方向二：存储分层
  Upload → [Hot/Standard] ──→ [Warm/IA] ──→ [Cold/Glacier]
                  ↑                ↑               ↑
              ┌────┴────────────────┴───────────────┘
              │  FileLifecycleManager (cron/outbox)
              └─────────────────────────────────────

方向三：死信可观测性
  Outbox Relay → MarkFailed → dead_letter=true → 静默丢弃
                         ↓
              新增: last_error + 死信 API + 告警

方向四：多租户
  File / Message / Comment ──→ 加 tenant_id 列
  Repository ──→ 所有查询加 WHERE tenant_id = ctx.tenant_id
  AdminRole ──→ 加 tenant_scope (global | tenant)

方向五：媒体处理管道
  Upload complete ──→ emit "file.uploaded" ──→ MediaProcessor
                                                    ├── ThumbnailGenerator
                                                    ├── PDFPreviewExtractor
                                                    └── VirusScanner (ClamAV)
```
