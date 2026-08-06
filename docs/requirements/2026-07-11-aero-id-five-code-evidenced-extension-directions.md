# aero-id — 五个代码证据驱动的高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件全量代码扫描，与 `docs/requirements/` 下 100+ 份既有分析交叉比对，确认以下 5 个方向**在任何既有文档中均未被作为独立方向进行完整的代码证据链分析**。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据（文件路径、行号级引用）和双视角（架构师 + PM）论证。

---

## 方向一：gRPC 拦截器层单元测试缺失

### 问题陈述

aero-id 在 HTTP 和 gRPC 双传输层共享同一套业务逻辑。HTTP 侧的中间件（auth、ratelimit、metrics、tracing、recovery、timeout 等）在 `internal/middleware/` 中已实施并有一定覆盖率的单元测试。但 **gRPC 侧的拦截器（interceptor）层完全没有独立的单元测试**——其行为仅被高层集成测试（`server_test.go`、`parity_test.go`）隐含覆盖，认证拒绝、限流行为、panic 恢复、trace 传播等关键路径在 gRPC 端未作为独立测试用例验证。

这是剩余的技术债中最显著的盲区，因为:

1. gRPC 和 HTTP 的认证/限流中间件虽然是逻辑对等的，但实现完全不同——HTTP 用 Gin middleware，gRPC 用 `grpc.UnaryServerInterceptor`。错误码映射、上下文传播、响应格式均不同。
2. 现有 `parity_test.go` 仅验证"HTTP 返回 200 时 gRPC 也返回 OK"的对等性，不验证 401/429/500 等异常路径的对等性。
3. 限流、认证、trace 传播的正确性对生产安全至关重要。

### 代码级证据

**证据 1：gRPC server 注册了 5 个拦截器，零个有独立测试**

```go
// internal/handler/grpc/server.go (约第 64-77 行)
func NewServerWithOptions(
    ssoClient *sso.Client,
    log *zap.Logger,
    metricsReg *metrics.Registry,
    opts []ServerOption,
    // ... service servers
) *grpc.Server {
    unary := []grpc.UnaryServerInterceptor{
        middleware.GRPCUnaryRecovery(log),      // ← 无独立测试
        middleware.GRPCAuth(ssoClient),          // ← 无独立测试
        middleware.GRPCMetrics(metricsReg),       // ← 无独立测试
        middleware.GRPCTracing(),                // ← 无独立测试
        middleware.GRPCRateLimit(limiter),        // ← 无独立测试
    }
    // ...
}
```

**证据 2：gRPC 拦截器实现在 middleware 包中但无 Test 文件**

```
internal/middleware/
├── access_log.go
├── auth.go               // ⋯ HTTP Auth + gRPC Auth 同文件
├── cors.go
├── middleware_test.go     // 只测试 HTTP 中间件
├── middleware_extra_test.go
├── metrics.go             // 含 GRPCMetrics
├── ratelimit.go           // 含 GRPCRateLimit
├── recovery.go            // 含 GRPCUnaryRecovery
├── tracing.go             // 含 GRPCTracing
├── tracing_test.go        // 只测试 HTTP Tracing
```

`auth.go` 中定义了 `GRPCAuth`，`metrics.go` 中定义了 `GRPCMetrics`，`ratelimit.go` 中定义了 `GRPCRateLimit`——但没有任何文件测试这些函数在 gRPC 上下文中的行为。

**证据 3：现有 parity_test.go 只覆盖成功路径**

```go
// internal/handler/grpc/parity_test.go (约第 30-60 行)
// 测试结构：HTTP 请求 → 200/OK → gRPC 请求 → 检查相同响应。
// 不测试: token 过期/缺失 → gRPC Unauthenticated
// 不测试: 限流触发 → gRPC ResourceExhausted
// 不测试: handler panic → gRPC Internal
```

**证据 4：gRPC 拦截器有自己的错误码映射路径，未测试**

```go
// internal/middleware/auth.go (GRPCAuth 函数)
// 当 SSO 验证失败时返回 status.Error(codes.Unauthenticated, ...)
// 当 uid 为空时返回 status.Error(codes.PermissionDenied, ...)
// 这些 gRPC 特有的错误码映射从未作为独立测试用例验证。
```

### 架构师视角

- **风险分类**: 中～高。认证和限流是安全关键路径，HTTP 侧有测试覆盖但 gRPC 侧没有。部署时如果 gRPC 拦截器因重构而静默失效（如错误地移除 `GRPCAuth`），`make test && make test-integration` 不会捕获。
- **建议**: 为每个 gRPC 拦截器创建独立的单元测试文件（`auth_test.go`、`ratelimit_test.go`、`tracing_test.go`、`recovery_test.go`、`metrics_test.go`），使用 `bufconn` 建立内存 gRPC 连接，注入特定的测试 service 来验证拦截器行为。
- **优先级**: 高。这是生产加固的最后一块拼图——HTTP 侧已有，gRPC 侧不应成为盲区。

### 产品经理视角

- 对用户无直接影响——这是正确性保障而非新功能。
- 但认证/鉴权的静默失效会导致：所有 gRPC 调用（内部服务间）失败 → 依赖 aero-id gRPC 的服务（如 SSO 批量查用户）中断 → 上游服务降级。
- **ROI**: 低投入（每个拦截器约 50-100 行测试），高回报（安全关键路径的持续正确性）。

---

## 方向二：文件访问审计跟踪（Download Audit Trail）

### 问题陈述

文件是 aero-id 的核心资产，但**文件的实际访问（下载、流式读取、预签名 URL 消费）完全没有审计记录**。目前审计日志只记录"谁授予了谁文件访问权限"（`file.share.grant`），却不记录"谁在何时实际访问了文件"。

这意味着:
1. **安全盲区**: 无法发现异常访问模式（如某个共享文件被大量下载）。
2. **合规缺口**: 敏感文件的分发无法追溯。
3. **产品限制**: 文件所有者无法看到自己的文件被谁、何时、多少次访问过。
4. **缺乏对预签名 URL 的追溯**——签发后无法知道 URL 是否被使用过。

### 代码级证据

**证据 1：审计日志仅记录共享事件，不记录访问事件**

```go
// internal/domain/audit.go (约第 20-25 行)
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"
)
// 注意：没有 AuditFileDownload，没有 AuditFileStream
```

**证据 2：文件 Get/DownloadURL 路径不产生审计事件**

```go
// internal/service/file.go — Get 方法（约第 103-118 行）
func (s *fileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
    actor, err := mustActor(ctx)
    // ... 加载文件、检查权限 ...
    // 返回文件对象，但没有任何 audit.Log 调用
    return f, nil
}

// internal/service/file.go — DownloadURL 方法（约第 210-230 行）
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
    // 生成预签名 URL，不记录谁在何时请求了下载 URL
    return s.store.PresignGet(ctx, f.StorageKey, ttl)
}
```

**证据 3：本地预签名 URL 验证入口无记录**

```go
// internal/handler/http/file.go — downloadLocal 或 handleDownload
// 当用户通过 /files/dl?key=...&sig=... 下载时，handler 验证签名后直接流式返回内容
// 不记录这次成功的下载
```

**证据 4：S3 预签名 URL 无从追溯**

```go
// internal/storage/s3.go — PresignGet（约第 139-151 行）
func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
    presigner := s3.NewPresignClient(s.client)
    req, err := presigner.PresignGetObject(..., s3.WithPresignExpires(ttl))
    // URL 签发后谁使用它、是否被使用——完全不可见
}
```

**证据 5：file_shares 表无访问统计**

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE file_shares (
    file_id     UUID NOT NULL REFERENCES files(id),
    grantee_uid TEXT NOT NULL,
    granted_by  TEXT NOT NULL,
    granted_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (file_id, grantee_uid)
);
-- 没有 access_count，没有 last_accessed_at
```

### 架构师视角

- **影响面**: 当前 `Get` 和 `DownloadURL` 是纯读取操作，不触及事务。增加审计日志不必改为同步写入——可异步写入 `audit_events` 表（`audit.Logger` 接口已支持，且文档注明不得阻塞主操作）。
- **预签名 URL 的特殊性**: 签发时记录预期使用者，但实际使用发生在 aero-id 进程之外（S3 直接发给客户端）。S3 侧可以启用访问日志（Server Access Logging），但 aero-id 无法实时感知。
- **实现路径**: 
  1. 在 `file.Get` 流式下载路径增加异步 `audit.Log(ctx, AuditFileDownload)`。
  2. 在 `file.DownloadURL`（预签名签发）路径记录签发事件。
  3. 可选：在本地存储的 `/files/dl` handler 记录实际消耗事件。

### 产品经理视角

- **用户价值**: 文件所有者可以在文件详情中看到"X 次访问、最后访问者、最后访问时间"。对协作场景是基础功能。
- **安全价值**: 运营团队可以检测异常访问模式（如同一个共享链接被短时间内大量 IP 消耗）。
- **实现成本**: 低。审计事件写入基础设施已存在（`audit_events` 表 + `repository.AuditRepository`）。只需在文件服务中增加 `scope="file.access"` 的审计日志调用。

---

## 方向三：搜索质量与搜索分析（Search Relevance, Typo Tolerance & Analytics）

### 问题陈述

aero-id 的全文搜索使用 PostgreSQL FTS，但搜索质量处于"能用"而非"好用"的水平。具体问题：

1. **无相关性排序**: 搜索结果按 `created_at DESC` 而非相关性（`ts_rank`）。
2. **无容错**: "helo" 不会匹配 "hello"；无前缀匹配；无拼音/模糊搜索。
3. **无搜索建议（autocomplete/typeahead）**: 用户在搜索框输入时无即时建议。
4. **无搜索分析**: 无法知道用户搜索什么、什么搜索词返回零结果、哪些实体类型搜索最频繁。
5. **文件名搜索退化**: 文件名使用 `ILIKE` 而非 FTS，大目录下性能差。

这对产品体验影响显著——搜索是用户发现已有内容的核心途径。

### 代码级证据

**证据 1：FTS 索引已建但未被用于排序**

```sql
-- migrations/000016_fts_indexes.up.sql
CREATE INDEX idx_messages_fts ON messages USING GIN(tsv_content);
CREATE INDEX idx_comments_fts ON comments USING GIN(tsv_content);

-- 但 service/search.go 和 repository 中未使用 ts_rank
```

**证据 2：Repository 搜索实现使用基本的 FTS 查询，无排名**

```go
// internal/repository/message.go — SearchMessages（约第 200-220 行）
func (r *messageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    // ... 使用 plainto_tsquery 但未用 ts_rank 排序
    // ORDER BY created_at DESC 而非 ORDER BY ts_rank DESC
}
```

**证据 3：文件名搜索使用 ILIKE——非 FTS，性能随数据增长退化**

```go
// internal/repository/file.go — SearchFiles（约第 180-200 行）
func (r *fileRepo) SearchFiles(ctx context.Context, ownerUID, query string, limit int) ([]*domain.File, error) {
    // SELECT * FROM files WHERE owner_uid = ? AND deleted_at IS NULL
    // AND (name ILIKE '%' || ? || '%' OR ...)
    // 没有使用 files.name 的 GIN trigram 索引
}
```

**证据 4：搜索服务不收集分析数据**

```go
// internal/search/search.go — Search（约第 30-60 行）
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    // ... 执行搜索，返回结果
    // 没有记录查询日志、零结果查询、搜索频率
    return hits, nil
}
```

### 架构师视角

- **低挂果实**: 将 `ORDER BY created_at DESC` 改为 `ORDER BY ts_rank(tsv_content, plainto_tsquery(?)) DESC` 即可显著提升结果质量，无需新增索引或外部依赖。
- **trigram 索引**: 文件名搜索可从 ILIKE 迁移到 `pg_trgm` GIN 索引（`CREATE INDEX ... ON files USING GIN(name gin_trgm_ops)`），支持模糊和容错匹配，无需外部搜索引擎。
- **搜索分析表**: 添加 `search_query_log(query_hash, user_hash, result_count, created_at)` 表即可收集聚合搜索趋势，且不存储原始搜索词（隐私保护）。
- **不建议过早引入 ElasticSearch**——PostgreSQL FTS + pg_trgm 在单机十亿级以内都够用。

### 产品经理视角

- **用户感知价值极高**: 搜索是用户在工作流中反复使用的功能。"搜不到"直接导致挫败感和低效。
- **Typo tolerance 是基本期望**: 用户打错字时给出"你是不是想找 XXX"的提示是现代搜索的最低门槛。
- **搜索分析驱动产品决策**: "什么搜索词返回零结果"直接告诉我们需要上传什么内容；"哪些实体被搜索最多"指导产品优先级。

---

## 方向四：Webhook 事件生命周期管理（Event Schema Contract & Webhook Management API）

### 问题陈述

aero-id 的 outbox webhook 已经实现了事件签名、类型过滤和重试，但是**缺乏对 webhook 可观测性、消费者契约和声明周期管理的支持**。目前:

1. **事件无版本号**: 事件 payload 是裸 JSON，消费者无法判断是否是新版格式。
2. **无交付历史可见性**: 无法查询某个事件是否已成功投递、失败原因、重试次数。
3. **无 API 管理 webhook**: webhook URL 是静态配置（`config.yaml`），运行时不可修改。
4. **无消费者契约测试**: 无法判断 schema 变更是否会破坏现有消费者。
5. **无密钥轮换 API**: signing key 变更需要重启进程。

随着 outbox 成为系统的异步事件骨干（驱动通知、可能的文件处理管线），webhook 的可靠性和管理能力成为生产瓶颈。

### 代码级证据

**证据 1：Event 结构体不携带版本信息**

```go
// internal/outbox/outbox.go — Event（约第 25-37 行）
type Event struct {
    ID            uuid.UUID
    AggregateType string
    AggregateID   string
    EventType     string
    Payload       []byte       // 裸 JSON，无版本标记
    CreatedAt     time.Time
    PublishedAt   *time.Time
    RetryCount    int
    LastRetryAt   *time.Time
    DeadLetter    bool
}
```

**证据 2：Webhook 配置是静态启动参数**

```go
// internal/outbox/webhook.go — WebhookConfig
type WebhookConfig struct {
    URL        string            // 静态配置
    SigningKey string            // 静态配置，轮换需要重启
    Timeout    time.Duration
    EventTypes []string
    Headers    map[string]string
}

// 无运行时管理 API：
// - POST /v1/admin/webhook — 设置 webhook
// - GET  /v1/admin/webhook/deliveries — 查看投递历史
// - POST /v1/admin/webhook/rotate-key — 轮换签名密钥
```

**证据 3：Webhook handler 不记录投递历史**

```go
// internal/outbox/webhook.go — Handle（约第 55-80 行）
func (h *WebhookHandler) Handle(ctx context.Context, e *Event) error {
    // ... 发送 HTTP POST ...
    // 成功的投递仅仅记录在 outbox_events.published_at
    // 失败的投递仅仅递增 outbox_events.retry_count
    // 没有任何投递详情表（status_code、duration、response_body）
}
```

**证据 4：无事件模式注册表**

```go
// 所有 event_type 值散落在各处:
// internal/service/notification.go: "message.sent"
// internal/service/notification.go: "message.read"
// internal/service/notification.go: "comment.replied"
// 没有集中的模式注册表来定义每个 event_type 的 payload schema
// 消费者无法通过 API 获取当前事件 schema 定义
```

### 架构师视角

- **版本号策略**: 在 Event Payload 外层引入 `{"v": 1, "payload": ...}` 包装，使消费者可以按版本解析。未来 schema 变更时增加版本号，新旧版本并存，消费者按能力选择。
- **投递历史表**: 新增 `webhook_deliveries(event_id, url, status_code, duration_ms, response_snippet, attempted_at)` 表，提供 `GET /v1/admin/webhook/deliveries?event_type=` 端点供运营排查。
- **运行时可配置**: 将 `WebhookConfig` 迁移至 DB 表 `webhook_subscriptions`，提供 CRUD API。启动时从 DB 加载，变更通过 API 实时生效。
- **Schema 注册**: 新增 `event_schemas(event_type, version, json_schema, created_at)` 表 + `GET /v1/admin/events/schemas` 端点，消费者可查询。

### 产品经理视角

- 这不是面向终端用户的功能，而是面向集成者（其他团队/系统）的基础设施。
- 当其他团队开始消费 aero-id 的事件流时（如数据仓库、全文搜索引擎、CDN 缓存刷新），没有 schema 契约的 webhook 会频繁引发集成故障。
- **一句话价值**: "你的下游消费者不再需要问你这个事件 payload 里有什么字段。"

---

## 方向五：按链接分享文件并支持过期与访问控制（Share-by-Link with Expiration & Protection）

### 问题陈述

当前文件分享只能通过**显式授予特定用户**（`file_shares` 表 `grantee_uid`）完成。这个模型有几个根本限制:

1. **无法与无名用户分享**: 接收方必须有一个 SSO 账户且在 aero-id 中有 uid。
2. **无时间限制**: 分享是永久的，无法设置自动过期。
3. **无访问限制**: 无法设置访问次数上限、密码保护、或 IP 白名单。
4. **无链接管理**: 无法创建、查看、撤销分享链接；无法统计链接被访问的次数。
5. **产品场景缺失**: 临时协作、对外分享公开文件、限时资料分发等均无法实现。

这是一个产品能力和架构能力的双重缺口。几乎所有文件分享产品（Google Drive、Dropbox、Nextcloud）都将 share-by-link 作为核心功能。

### 代码级证据

**证据 1：file_shares 表仅支持 per-user 分享**

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE file_shares (
    file_id     UUID NOT NULL REFERENCES files(id),
    grantee_uid TEXT NOT NULL,          -- 仅支持指定用户
    granted_by  TEXT NOT NULL,
    granted_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (file_id, grantee_uid)
);
-- 缺少:
--   link_token    TEXT UNIQUE,    -- 链接标识
--   expires_at    TIMESTAMPTZ,    -- 过期时间
--   access_limit  INT,            -- 访问次数上限
--   password_hash TEXT,           -- 密码保护
--   access_count  INT DEFAULT 0   -- 已访问次数
```

**证据 2：文件服务无 share-by-link 相关方法**

```go
// internal/file/file.go — Service 接口
type Service interface {
    ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error
    UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error
    // 没有:
    // CreateShareLink(ctx, id, expiresAt, password) (string, error)
    // RevokeShareLink(ctx, linkToken) error
    // GetShareLinkInfo(ctx, linkToken) (*ShareLink, error)
    // AccessSharedFile(ctx, linkToken, password) (*File, error)
}
```

**证据 3：文件 Handler 无按链接分享端点**

```go
// internal/file/handler_http.go
// 注册的路由:
//   POST /v1/files/:id/share    → ShareFile
//   POST /v1/files/:id/unshare  → UnshareFile
//   GET  /v1/files/shared-with-me → ListSharedWithMe
// 没有:
//   POST /v1/files/:id/share-link  → 创建分享链接
//   DELETE /v1/share-links/:token  → 撤销分享链接
//   GET /v1/files/:id/share-links  → 查看文件的所有分享链接
```

**证据 4：canRead 接口不识别链接 Token**

```go
// internal/service/file.go — canRead
func (s *fileService) canRead(ctx context.Context, actor string, f *domain.File) (bool, error) {
    if canReadFile(actor, f) {
        return true, nil
    }
    if s.shares == nil {
        return false, nil
    }
    shared, err := s.shares.IsSharedWith(ctx, f.ID, actor)
    // 无法识别非 uid 的链接 Token 持有者
    return shared, nil
}
```

### 架构师视角

- **模型设计**: 新增 `share_links` 表（`id, file_id, owner_uid, token, password_hash, expires_at, access_limit, access_count, created_at`），独立于 `file_shares`。链接 Token 用 `crypto/rand` 生成（足够安全），签名验证可复用现有的 HMAC 机制。
- **匿名访问路径**: 
  - `GET /files/dl/:token` 是匿名路由（不需要 Bearer token），handler 验证 token 有效性和过期/次数限制。
  - `canRead` 需要扩展以识别链接访问（通过在 context 中注入 link_token 信息）。
- **过期清理**: 可由现有的 storage GC sweep 扩展，定期清理 `expires_at < NOW()` 的 `share_links`。
- **密码保护**: 密码使用 bcrypt 哈希存储。匿名访问时要求提供密码（query 参数或 request body）。

### 产品经理视角

- **用户价值极高**: "临时分享一个文件给外部合作方"是最常见的文件使用场景之一。当前只能通过将文件设为 `visibility=public`（所有人都能看）或逐一添加 SSO 用户（仅内部用户），都不满足。
- **典型场景**:
  - 给客户发送一份限时访问的方案文档（24 小时后过期）。
  - 分享一张活动照片给非平台用户（可通过链接直接访问）。
  - 团队链接分享但设置密码保护以防链接泄露。
- **竞争差距**: 几乎所有对标产品都有这个功能。缺少它直接限制了 aero-id 在文件协作场景中的竞争力。

---

## 优先级与建议执行顺序

| 方向 | 类别 | 优先级 | 预估工作量 | 前置条件 | 理由 |
|------|------|--------|-----------|---------|------|
| 方向一：gRPC 拦截器测试 | 技术债/质量 | **P0** | 低（2-3天） | 无 | 生产加固最后的盲区，无前置依赖 |
| 方向三：搜索质量优化 | 产品/体验 | **P1** | 低-中（3-5天） | 无 | 低投入高感知，`ts_rank` 排序几乎是纯配置变更 |
| 方向二：文件访问审计 | 安全/合规 | **P1** | 低（2-3天） | 无 | 基础设施已就位，增加几行日志调用 |
| 方向五：按链接分享 | 产品 | **P2** | 高（2-3周） | 需要新的 DB 表 + handler + service 方法 | 新功能，但实现路径清晰 |
| 方向四：Webhook 生命周期 | 运维/集成 | **P2** | 中（1-2周） | 现有 webhook 基础设施 | 上游团队集成时才会紧迫 |

---

## 与既有分析的交叉验证

以上 5 个方向在 `docs/requirements/` 的 100+ 份既有分析中**未被作为独立方向进行代码证据链分析**。具体交叉验证结果：

| 方向 | 前序提及情况 | 差异说明 |
|------|-------------|---------|
| gRPC 拦截器测试 | 仅 1 处 REVIEW 文档简述"✅ tested"，未深入分析和提出改进 | 本分析提供完整的代码证据和具体的测试用例建议 |
| 文件访问审计 | 3 份文档各一句话提及"加 access_log"，无全面分析 | 本分析覆盖了从 domain model 到 handler 的全链路审计缺失 |
| 搜索质量 | 6 份文档提及，但分散且浅 | 本分析聚焦于可快速落地的改进（ts_rank、trigram、query log） |
| Webhook 生命周期 | 7 份文档提及"schema/version"，无完整论证 | 本分析将事件版本化 + 投递历史 + 管理 API 联系起来 |
| 按链接分享 | 9 份文档提及"share expiration"，未整合为完整方向 | 本分析涵盖共享全生命周期：创建/密码/过期/访问统计/撤销 |
