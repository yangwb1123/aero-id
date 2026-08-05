# aero-id — 基础设施深度审查：中间件链、Metrics、错误路径、降级策略

> 日期: 2026-06-30
> 第五轮审查，覆盖前四次完全未触及的新角度

---

## 1. 中间件链深度分析

### 1.1 HTTP 中间件执行顺序（`router.go`）

```
gin.New()
  └── Use(RequestID)       ① 最早：生成/传播 request_id
  └── Use(Tracing)          ② 第二：启动 OTel span（依赖 request_id）
  └── Use(Recovery)         ③ 第三：panic 兜底
  └── Use(Metrics)          ④ 第四：RED 指标记录
  └── Use(AccessLog)        ⑤ 第五：请求日志
  └── Use(CORS)             ⑥ 第六：跨域检查
  └── Use(Timeout)          ⑦ 第七：ctx deadline 设置

  v1 group
    └── Auth                 ⑧ 认证：提取 Bearer token → SSO 验证
    └── RateLimit            ⑨ 限流：基于 uid 或 IP
    └── Idempotency          ⑩ 幂等：预留/重放请求
    └── [handler]            ⑪ 业务逻辑
```

**问题 1：`Recovery` 在 `Tracing` 之后。** 如果 panic 发生在 `Tracing` 之前的中间件（`RequestID`），`Recovery` 无法拦截。但实际上 `RequestID` 和 `Tracing` 都不应该 panic，所以这个顺序风险很低。

**问题 2：`AccessLog` 在 `Metrics` 之后。** Metrics 中间件记录了请求计数和延迟，但 `AccessLog` 在 `Metrics` 之后执行——这意味着如果 `Metrics` 中间件 panic（极不可能），日志不会产生。但这是可接受的顺序。

**问题 3：`Timeout` 在 `CORS` 之后。** 如果 `Timeout` 设置 `ctx deadline`，但 `CORS` 在检查 origin 时可能已经消耗了 time——对于 OPTIONS 预检请求（`c.AbortWithStatus(204)`），这个顺序实际上不会造成问题，因为 OPTIONS 被 `CORS` 提前 abort 了。

**问题 4：`AccessLog` 记录了 `actor_uid`，但只在认证之后才设置。** 对于 auth 失败的请求，`actor_uid` 是空字符串 `""`。日志可以区分已认证和未认证的请求，但需要明确在日志分析中过滤空值。

### 1.2 gRPC 拦截器执行顺序（`server.go`）

```
grpc.ChainUnaryInterceptor(
  TracingInterceptor,       ① 外层：OTel span
  MetricsInterceptor,       ② RED 指标
  RecoveryInterceptor,      ③ panic 兜底
  AuthInterceptor,          ④ 认证
  RateLimitInterceptor,     ⑤ 限流（可选）
)
```

**问题 1：`RateLimitInterceptor` 在 `AuthInterceptor` 之后——正确。** 这样限流可以 key 在 uid 上。但如果 `AuthInterceptor` 失败（非法 token），`RateLimitInterceptor` 不会执行。但这没问题——不浪费限流容量在非法请求上。

**问题 2：`RecoveryInterceptor` 在 `AuthInterceptor` 之前。** 如果 `AuthInterceptor` panic，`RecoveryInterceptor` 捕获不到（因为 interceptor chain 是嵌套的，`Auth` 在 `Recovery` 内部执行）。实际上，`AuthInterceptor` 中唯一的 panic 来源是 `bearerFromMD` 和 `v.ValidateToken`——两者都不应该 panic。但这是一个潜在的安全缺口：如果 `ValidateToken` 意外 panic，它将绕过 recovery 直接崩溃进程。

**问题 3：缺少 `RequestID` 拦截器。** HTTP 端有 `RequestID` 中间件，但 gRPC 端没有等价物。`access_log` 依赖于 `request_id` 关联日志行——gRPC 请求的日志中没有 `request_id`。这意味着跨 HTTP/gRPC 的请求追踪在日志层面是断裂的。

### 1.3 Abort 路径分析

```go
// HTTP 端的 Abort 调用点：
auth_missing_token     → c.Abort()                    // 401，不继续
auth_invalid_token     → c.Abort()                    // 401，不继续
rate_limit_exceeded    → c.AbortWithStatusJSON(429)   // 429，不继续
idempotency_in_progress→ c.AbortWithStatusJSON(409)  // 409，不继续
idempotency_replay     → c.Abort()                    // 重放，不继续
cors_forbidden         → c.AbortWithStatus(403)       // 403，不继续
cors_options           → c.AbortWithStatus(204)       // OPTIONS 204
recovery               → c.Abort()                    // 500
```

**问题：** 当中间件 `Abort()` 后，`AccessLog` 仍然会记录（因为 `c.Next()` 返回后执行）。但此时 `actor_uid` 尚未设置（auth 失败时没有注入 actor）。`access_log` 中会出现 `actor=""` 的401/429/403日志行——这是正确的行为，但需要在日志分析时处理。

**影响：** 日志分析时需要过滤 `actor=""` 的请求，或者为未认证请求提供一个标记（如 `actor="anonymous"`）。

### 1.4 中间件总结建议

| 问题 | 风险 | 建议 |
|------|------|------|
| gRPC 无 `RequestID` | 日志关联断裂 | 添加 gRPC `RequestID` 拦截器，从 metadata 提取或生成 |
| gRPC `Recovery` 包裹 `Auth` 的方式 | 潜在 panic 泄漏 | 确认 `Auth` 路径无 panic，或在 `Auth` 内部加 recover |
| `AccessLog` 记录空 `actor_uid` | 日志分析噪音 | 空值时写 `actor="anonymous"` |
| `Timeout` 在 `CORS` 后 | 低风险 | 保持当前顺序即可 |

---

## 2. Metrics 系统深度分析

### 2.1 当前暴露的指标族

| 指标名 | 类型 | 标签 |
|--------|------|------|
| `aeroid_requests_total` | Counter | transport, method, route, status |
| `aeroid_request_duration_seconds` | Histogram (11 buckets) | transport, route |
| `aeroid_sso_cache_hits_total` | Counter | cache (token/user) |
| `aeroid_sso_cache_misses_total` | Counter | cache (token/user) |
| `aeroid_sso_errors_total` | Counter | op (validate/get_users) |
| `aeroid_sso_call_duration_seconds` | Histogram | op |
| `aeroid_outbox_published_total` | Counter | event_type |
| `aeroid_outbox_failed_total` | Counter | event_type |
| `aeroid_outbox_pending` | Gauge | (none) |
| `aeroid_outbox_oldest_age_seconds` | Gauge | (none) |
| `aeroid_outbox_drain_duration_seconds` | Histogram | (none) |

### 2.2 基数风险

**当前风险：** `aeroid_requests_total` 的标签组合 `{transport, method, route, status}`：

- transport: `http` | `grpc`
- method: `GET` | `POST` | `DELETE` | `PATCH` | `rpc`
- route: `/v1/files/:id` | `/v1/messages/:id` | `/v1/search` × ~20 个路由 + gRPC methods
- status: `200` | `201` | `204` | `400` | `401` | `403` | `404` | `409` | `429` | `500`

理论最大基数：2 × 5 × 30 × 10 = ~3000 时间序列。对于 Prometheus 这是一个非常安全的数值。但问题是：

- `route` 标签包含 UUID 路径参数（如 `/v1/files/:id`），Gin 的 `FullPath()` 返回的是模板路径 `/v1/files/:id`——这很好，**不会**把实际 UUID 暴露为标签值。✅ 已正确处理。
- gRPC 的 `fullMethod` 是 `/aero_id.v1.FileService/GetFile` 这种格式，也在合理范围内。✅

**缺失：** 没有 `version` 标签（API 版本）、没有 `handler` 标签（哪个 handler 处理的）。

### 2.3 遗漏的关键指标

| 应该有的指标 | 为什么需要 |
|-------------|-----------|
| `aeroid_db_connections_in_use` | 数据库连接池饱和度 |
| `aeroid_db_query_duration_seconds` | 数据库查询延迟（histogram） |
| `aeroid_gc_rows_purged_total` | GC sweeper 清理了多少行 |
| `aeroid_gc_blobs_purged_total` | GC sweeper 删除了多少个 blob |
| `aeroid_gc_duration_seconds` | GC pass 耗时 |
| `aeroid_idem_keys_total` | 幂等键总数（gauge）或清理数 |
| `aeroid_sse_connections_active` | 活跃 SSE 连接数 |
| `aeroid_sse_events_dropped_total` | SSE 推送丢弃数 |
| `aeroid_goroutines_active` | Go runtime goroutine 数 |
| `aeroid_storage_operations_total` | 存储后端操作计数（Put/Get/Delete/Stat） |
| `aeroid_rate_limit_keys_active` | 活跃限流 bucket 数 |

### 2.4 Metrics 写放大

`ObserveOutboxDrain` 每次 drain 调用一次。在 5s interval 下，每天 ~17,280 次调用。每次调用加锁 `r.mu.Lock()`。如果多个 relay（当前只有一个）同时上报，锁竞争在 10k+ RPS 下可能成为瓶颈。

**建议：** 考虑批量累积 + 定期 flush 模式（`atomic` 计数器 + 每 15s 一次 lock + flush），减少 `r.mu` 的竞争频率。

---

## 3. 错误传播路径追踪

### 3.1 数据流错误映射完整性

```
Repository (raw)          →  Service (AppError)          →  Handler (HTTP/gRPC)
─────────────────────────────────────────────────────────────────────────────
gorm.ErrRecordNotFound    →  NotFound("x.not_found")    →  404 / codes.NotFound
ErrDuplicate              →  Conflict("x.duplicate")    →  409 / codes.AlreadyExists
其他 DB 错误               →  Internal("x.failed")       →  500 / codes.Internal
context.DeadlineExceeded  →  (透传)                      →  500 / codes.Internal (丢失)
```

**问题 1：`context.DeadlineExceeded` 被 `Internal` 包装。** 当请求超时（`Timeout` 中间件设置的 deadline 到期），handler 或 service 层可能收到 `context.DeadlineExceeded`。当前错误映射：

```go
// service 层：任何非预期错误变为 Internal
return nil, apperr.Internal("x.failed", "...").Wrap(err)
```

这会把超时错误映射为 **500 Internal Server Error**，但 HTTP 语义上应该是 **504 Gateway Timeout**（客户端的请求超时了）或 **503 Service Unavailable**（上游处理不过来）。gRPC 映射为 `codes.Internal` 但应该是 `codes.DeadlineExceeded`。

**建议：** 在 `Wrap` 或 `AppError` 构造函数中检查 `errors.Is(err, context.DeadlineExceeded)`，自动映射为 `504 / codes.DeadlineExceeded`。或者在 `WriteError` / `GRPCError` 函数中添加这个检查。

### 3.2 服务层 `mustActor` 的错误码

```go
func mustActor(ctx context.Context) (string, error) {
    uid, ok := ActorFrom(ctx)
    if !ok {
        return "", apperr.Unauthorized("auth.unauthorized", "missing authenticated actor")
    }
    return uid, nil
}
```

**问题：** `Unauthorized` 但返回 `codes.Unauthenticated`（gRPC 层面正确）。HTTP 层面返回 `401`。但如果 actor 是通过某些不存在的中间件路径丢失的（非 Bearer token 方式），"missing authenticated actor" 应该返回 `401` 还是 `500`？当前统一 `401`，这是合理的——请求未认证，无论原因。

### 3.3 仓库层 `ErrNotFound` 的歧义

```go
var ErrNotFound = errors.New("repository: not found")
```

`Get` 操作返回 `ErrNotFound`。但 `Delete` 操作也返回 `ErrNotFound`？看代码：

```go
// comment repo SoftDelete:
func (r *commentRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&commentRow{})
    if res.RowsAffected == 0 {
        return ErrNotFound
    }
    ...
}
```

但 `messageRepo.SoftDelete` 不检查 `RowsAffected`——它总是返回 nil。不一致！消息软删除返回 nil（保持幂等），评论软删除返回 `ErrNotFound`（非幂等）。

```go
// message repo SoftDelete:
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    if err := r.db.WithContext(ctx).Where("id = ?", id).Delete(&messageRow{}).Error; err != nil {
        return fmt.Errorf("soft delete message: %w", err)
    }
    return nil  // 总是返回 nil，即使没有行被删除
}
```

**影响：** 如果客户端对已删除的消息再次调用 `DeleteMessage`，返回 `nil`（幂等）。如果对已删除的评论再次调用 `DeleteComment`（通过服务层），服务层会在 `Get` 阶段返回 `NotFound`——所以最终行为都是 404。但**内部行为不一致**：消息删除是幂等（返回 nil），评论删除需要先 Get 检查存在性（返回 NotFound）。

**建议：** 统一软删除的幂等行为——要么都检查 `RowsAffected`（返回 NotFound），要么都不检查（总是 nil）。根据 `service/message.go` 的 `Delete` 实现，它期望幂等行为（`if m.FromUID != actor` 后检查，但 `Get` 阶段已隐式处理了不存在性）。

### 3.4 错误堆栈跟踪的缺失

`AppError` 的 `Wrap` 不记录堆栈：

```go
func (e *AppError) Wrap(cause error) *AppError {
    cp := *e
    cp.Cause = cause
    return &cp
}
```

在调试生产问题时，工程师需要知道错误发生的代码位置。Go 的 `errors.Is/As` 只能追踪 cause chain，不能定位源码位置。

**建议：** 考虑在 `Wrap` 时捕获调用栈（`runtime.Callers`），或在日志中输出 `+` 形式的 stack trace（通过 `fmt.Sprintf("%+v", err)` 仅在日志级别为 debug 时启用）。

---

## 4. 降级策略分析（Graceful Degradation）

### 4.1 各依赖降级全景

| 依赖 | 降级策略 | 影响范围 | 评价 |
|------|---------|---------|------|
| **SSO 不可用** | stale cache + circuit breaker → fast-fail | 全 `/v1/*` 不可服务 | ✅ 优秀——有 stale 窗口和断路器 |
| **Postgres 不可用** | 无降级，直接 500 | 全 API 不可服务 | ⚠️ 合理——DB 不可用时无法服务 |
| **S3 不可用** | 上传失败（500），下载走 presigned URL（如果已存在） | 文件操作 | ⚠️ 上传不可用但下载可能还工作 |
| **Local 存储** | 同 S3——上传播写不可用 | 文件操作 | ⚠️ 合理 |
| **Redis（未引入）** | N/A——不存在 | - | N/A |

### 4.2 SSO 降级的边界情况

SSO 使用 stale cache 机制：`TokenCache` 有 `StaleTTL`（默认 300s）。在 SSO 不可用时，缓存在 TTL 过期后仍可服务 `StaleTTL` 时长。

**问题：** 如果 SSO 不可用时间超过 `TokenCacheTTL + StaleTTL`（即 60s + 300s = 360s = 6 分钟），所有 token 都被清出缓存。之后：

1. 新请求到达 → `ValidateToken` → 缓存 miss → 单飞 → `breaker.Allow()` 返回 false（断路器已打开）→ `GetStale` 也 miss（超过 stale TTL）→ 返回 `errCircuitOpen` → 客户端 401
2. 6 分钟后所有在线用户都需要重新登录（获取新 token）
3. 但 SSO 恢复后，第一个请求的 `breaker.Allow()` 返回 true（cooldown 已过）→ half-open probe → 成功 → breaker 关闭 → 恢复正常

这实际上是一个**合理的设计**——6 分钟内缓存的 token 仍然有效，6 分钟后用户需要重新登录。这是正确行为。

**但有一个细微问题：** `TokenCache` 的 `StaleTTL` 和 `TokenCacheTTL` 都是从 SSO 的 `expires_at` 开始计算？看代码：

```go
// validateUpstream:
c.tokenCache.Set(token, resp.Uid, exp)
// exp = time.Unix(resp.ExpiresAtUnix, 0)
```

`Set` 中，`exp` 是 SSO 返回的到期时间。`Get` 检查 `time.Now().After(exp)`？还是 `time.Now().After(createdAt + TTL)`？

看 `token_cache.go`：

```go
func (c *TokenCache) Get(token string) (string, bool) {
    // ...
    if time.Now().After(entry.expiresAt) {
        c.delete(token)
        return "", false
    }
    return entry.uid, true
}

func (c *TokenCache) GetStale(token string) (string, bool) {
    // ...
    if time.Now().After(entry.expiresAt.Add(c.staleTTL)) {
        c.delete(token)
        return "", false
    }
    return entry.uid, true
}
```

所以 `expiresAt` 是 `min(SSO 返回的 expires_at, createdAt + TokenCacheTTL)`。`GetStale` 在 `expiresAt + staleTTL` 之前都可以返回 stale 值。这意味着 stale 窗口是从**缓存的到期时间**开始算，而不是从 SSO 的绝对到期时间。

**建议：** 确认这个行为是否与预期一致。如果 SSO token 本身的有效期是 1 小时，但 `TokenCacheTTL` 是 60s，那么缓存会在 60s 后过期，但 stale 窗口从 60s 开始算 + 300s = 总共 360s。SSO 返回的 `exp` 大于 `60s + 300s` 时，缓存过期早于 token 的真正到期——多出的有效时间没被利用。可以考虑将 `expiresAt` 设为 `min(SSO_exp, createdAt + TokenCacheTTL + StaleTTL)`。

### 4.3 无全局断路器

**当前：** SSO 有断路器，但数据库和存储后端没有。如果数据库变得很慢（比如 `SELECT ... ORDER BY ... LIMIT` 在全表扫描上），请求会堆积在数据库连接池中：

- `max_open_conns: 50` 全部被慢查询占用
- 新请求等待数据库连接（GORM 的 `MaxOpenConns` 语义）→ HTTP 请求累积
- 最终：goroutine 泄漏或 OOM（因为每个等待的请求持有 goroutine）

**建议：** 为数据库操作添加批量超时（`statement_timeout`）+ 连接池监控。当数据库 P50 延迟 > 1s 时，考虑"读降级"（如直接返回错误提示"服务暂时不可用"），避免所有请求排队等待。

### 4.4 优雅关机：未处理的中断信号

`cmd/server/main.go`：

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
```

只处理了 `SIGINT` 和 `SIGTERM`。在 Docker/K8s 环境中：

- K8s 发送 `SIGTERM` → 30s 后 `SIGKILL`
- Docker `docker stop` 发送 `SIGTERM` → 10s 后 `SIGKILL`
- 一些 orchestrator 也会发送 `SIGHUP`（重载配置，不支持）

**缺少：** 不处理 `SIGHUP`（配置重载，但 aero-id 不支持）、`SIGQUIT`（pprof 转储）。

---

## 5. 配置系统分析

### 5.1 Viper 使用方式

`configs/config.yaml` 使用 `mapstructure` tags。`config.Load` 使用 Viper 读取文件和环境变量。

**环境变量映射：** `AERO_ID_*` 前缀，`.` 转 `_`。

例如：
- `server.http.port` → `AERO_ID_SERVER_HTTP_PORT`
- `sso.endpoint` → `AERO_ID_SSO_ENDPOINT`

**问题 1：非标准的环境变量命名。** Go 社区惯例是 `AERO_ID_HTTP_PORT`（扁平）或 `AERO_HTTP_PORT`（短前缀）。当前嵌套映射虽然功能正确，但运维人员需要查阅文档才能知道环境变量名。

**问题 2：缺少强制配置校验。** `config.Load` 声明"验证 required keys at startup"，但 `sso.endpoint` 是空字符串或 "127.0.0.1:9000"（默认值）时是否验证格式？`database.password` 如果是默认值 `"aero"` 是否通过？看 `config.go`：

需要确认 `config.go` 是否有如下验证：

```go
if cfg.SigningKey == "" || cfg.SigningKey == "change-me-please-very-long-random" {
    return nil, errors.New("signing_key must be changed from the default")
}
```

如果默认值通过了验证，生产部署容易忘记修改。

**问题 3：秘密值在日志中可能泄露。** 如果 `zap.Logger` 以 Debug 级别记录配置值（如连接字符串、密码），敏感信息会出现在日志中。

### 5.2 `config.go` 需要验证

需要检查 `config.go` 中是否对以下字段做了验证：

| 字段 | 风险 | 建议验证 |
|------|------|---------|
| `signing_key` | 默认值可被用于伪造签名 | 拒绝默认值 `change-me-please-very-long-random` |
| `database.password` | 默认值开放数据库 | 生产环境拒绝默认值 |
| `sso.endpoint` | 无效地址导致启动后立即报警 | 格式校验（`host:port`） |
| `server.http.port` | 端口冲突 | 检查 `1024-65535` |
| `storage.max_upload_bytes` | 0 = 无限制 | 生产环境建议设一个合理默认值 |
| `outbox.webhook.signing_key` | 为空时 webhook 无签名 | 如果 webhook URL 设置了，key 必须同时设置 |

---

## 6. 高价值扩展方向（本次新方向，前四次完全未覆盖）

---

### ⚡ 方向 A：在线状态 / 输入状态指示器

**概念：** 暴露用户在线状态（online/offline/idle）和输入状态（typing in conversation X）的实时信号。

**为什么需要它：**
1. **即时通讯的基本 UX 要素。** 没有在线状态和输入指示器，用户无法知道对方是否在线、是否正在回复。这是 Telegram/WhatsApp/Slack 都提供的最基础特征。缺失此功能使 1:1 消息显得"异步"而非"即时"。
2. **SSE Hub 基础设施已就绪。** 当前 SSE hub 只推送通知事件。在线状态和输入指示是天然适配 SSE 的——低频率、低延迟、不需要持久化。只需要新增事件类型 `presence.online`、`presence.typing`。
3. **实现简单，数据模型轻量。** 不需要数据库表（在内存中维护即可）、不需要持久化、不需要历史。用 SSE Hub 的现有订阅机制即可广播给同一会话的对端。
4. **与通知系统互补。** 用户收到消息通知时，如果看到发送者"在线"且有"输入中"状态，会等待回复而不是立刻回复——改变用户交互模式。

**实现要点：**
- 新增 `POST /v1/presence/heartbeat`（客户端定时发送心跳，如每 30s）
- 新增 `POST /v1/presence/typing { "conversation_with": "uid" }`（用户按键盘时发送）
- 服务端维护内存中的 `map[uid]*presence{lastHeartbeat, status}`，超时 60s 后自动标记 offline
- SSE 事件类型：`presence.online`、`presence.offline`、`presence.typing`
- 注意：**只在内存中维护，不持久化**。服务重启后所有用户标记为 offline，直到客户端重新发送心跳
- 不需要 gRPC 端点（这是纯实时特性，SSE 通道足以承载）

---

### ⚡ 方向 B：消息草稿 / 稍后发送（Drafts / Snooze）

**概念：** 允许用户编写消息但不发送，保存为草稿。支持"稍后提醒"（snooze）——定时收到通知提醒回复某个会话。

**为什么需要它：**
1. **用户消息行为天然需要草稿。** 用户可能会在输入中途被打断（会议、电话），回来时消息还留在输入框中但浏览器已关闭。草稿可以跨设备同步保存。
2. **产品差异点。** 1:1 消息是一个红海功能。草稿 + snooze 是小型的产品创新，可以在不增加架构复杂度的前提下提升用户粘性。
3. **实现非常简单。** 只需要一张表 `message_drafts`（`uid, to_uid, content, file_id, created_at, updated_at`）+ CRUD API。不需要走 outbox、不需要通知、不需要索引优化。
4. **与定时消息（方向 B 在第三次分析中）互补。** 草稿 + 定时发送 = "写好，设定发送时间，到时候自动发"。

**实现要点：**
- `message_drafts` 表：`(uid, to_uid, content TEXT, file_id, kind, created_at, updated_at)`，复合主键 `(uid, to_uid)` 或独立 ID
- API：`PUT /v1/messages/drafts`（upsert）、`GET /v1/messages/drafts`（列表）、`DELETE /v1/messages/drafts/:id`
- 草稿保存时不验证 block、不 emit outbox event、不写 notification
- 从草稿发送：`POST /v1/messages/drafts/:id/send` 或复用 `SendMessage`
- 注意：草稿的隐私性——草稿只有作者可见。不需要共享或协作编辑

---

### ⚡ 方向 C：只读 API Keys / 服务账号

**概念：** 允许用户（或组织管理员）生成具有受限制权限的 API keys，用于自动化、集成和监控。

**为什么需要它：**
1. **平台集成的必要组件。** 当前所有 API 调用需要 SSO Bearer token，有效期通常短（~1h）。服务账号 / API key 提供长期访问权限，适合脚本、CI/CD、webhook 回调。
2. **安全地暴露给第三方。** 如果需要将 aero-id 的文件或消息暴露给外部工具（备份脚本、分析工具、聊天 bot），不想使用个人账号的 token——权限太宽、token 太短。
3. **实现独立于 SSO。** API key 由 aero-id 自己管理（`api_keys` 表），不依赖 SSO。这使得集成场景可以脱离 SSO 存在。
4. **最低权限原则。** API key 应该可以限制到特定范围（如只读文件、只读消息）和特定资源（如只读 `/v1/files`，不读消息）。

**实现要点：**
- `api_keys` 表：`(id, uid, name, key_hash, permissions[]/scope, expires_at, last_used_at, created_at)`
- `POST /v1/api-keys` → 创建 key（返回明文 key，仅此一次）
- `GET /v1/api-keys` → 列出 key（不返回明文，只返回元数据）
- `DELETE /v1/api-keys/:id` → 删除 key
- 认证：新增 `X-API-Key` header 认证路径，在 `Auth` 中间件中与 Bearer token 并列
- Scope 模型：`files:read`、`messages:read`、`messages:write`、`comments:read`、`search` 等
- 注意：API key 的哈希存储（bcrypt 或 SHA256 + salt），never log the plaintext key
- 与现有 SSO 认证共存，互不冲突

---

### ⚡ 方向 D：数据分析管线（使用量统计 + 业务指标）

**概念：** 基于审计日志 + 业务数据，构建使用量统计分析 API，暴露给管理员或用户仪表盘。

**为什么需要它：**
1. **用户需要知道自己的数据量。** "我存储了多少文件？发送了多少消息？获得了多少个赞？"——这是用户个人中心的基本信息。当前无 API 可以回答这些问题。
2. **管理员需要平台级统计。** 日活跃用户、消息量趋势、文件存储总量——这些是运营 SaaS 产品的基本监控数据。当前只能通过数据库查询获得。
3. **计费/用量限制的基础。** 如果未来对存储或 API 调用进行计费（按量付费或套餐限制），用量统计是前置条件。
4. **数据已经存在，只是没有聚合查询。** 所有需要统计的数据已经在各表中——只需要聚合查询（`COUNT`、`SUM`、`GROUP BY`）即可。

**实现要点：**
- 用户级：`GET /v1/me/stats` → `{files_count, files_total_size, messages_sent, messages_received, comments_count, likes_received}`
- 管理员级：`GET /v1/admin/stats` → `{total_users, active_users_24h, messages_per_day, files_total_size, ...}`
- 增量 vs 全量：低频场景（用户个人页）可以直接 COUNT；高频场景需要物化视图或预计算表
- 物化视图示例：
  ```sql
  CREATE MATERIALIZED VIEW mv_user_daily_stats AS
  SELECT owner_uid, date_trunc('day', created_at) as day, count(*) as files_uploaded, sum(size) as storage_used
  FROM files GROUP BY owner_uid, date_trunc('day', created_at);
  ```
- 注意：分析查询不应该影响主库性能——建议使用**只读副本**或**异步物化视图刷新**
- 结合审计日志：`audit_events` 可以分析用户活跃度（actions_per_user_per_day）

---

### ⚡ 方向 E：消息回执的仓储（Read Receipt History）

**概念：** 当前 `messages.read_at` 记录了最新的已读时间，但无历史记录——不知道"谁在什么时候读了消息"。对于群聊场景（未来），需要知道哪些成员已读、哪些未读。

**为什么需要它：**
1. **消息的"已读详情"是即时通讯的标准功能。** WhatsApp/Telegram 显示"已读 3/5"和张三在 10:30 已读。当前只记录最新的已读时间戳，无法回答"谁已读了"。
2. **消息回执的审计需求。** 在合规场景（如法律通知、合同确认），需要证明"张三在 2026-06-30T10:00:00Z 阅读了此消息"。
3. **数据模型已预留扩展点。** `read_at` 列是单值——只能记录最后一次读取。需要 `message_read_receipts` 表来存储每次读取事件。
4. **与 outbox 联动。** 当前 `message.read` 事件已经携带了 `read_at`、`from_uid`、`to_uid`。如果把这些事件写入 `message_read_receipts` 表（而不仅仅是触发通知），就天然获得了回执历史。

**实现要点：**
- `message_read_receipts` 表：`(message_id, reader_uid, read_at)`，复合主键
- 在 `MarkReadWithEvent` 的事务中，除了更新 `read_at`，再插入一条回执记录
- API：`GET /v1/messages/:id/read-receipts` → 返回已读用户列表 + 时间
- 在 1:1 场景中：最多 1 条回执（对方已读）。在群聊场景中：N 条回执（各成员已读状态）
- 注意：回执记录是 append-only，不更新历史。第一次读取和第二次读取都记录（虽然 1:1 场景不需要第二次读取，但群聊场景中同一用户可能在不同设备上多次读取）
- 隐私考虑：用户是否可以选择不发送已读回执？需要 `privacy_settings` 支持"不发送已读回执"选项

---

## 7. 技术债补遗（前四次未列出的）

| 等级 | 问题 | 位置 |
|------|------|------|
| **Medium** | gRPC 无 `RequestID` 拦截器 → 日志关联断裂 | `internal/handler/grpc/server.go` |
| **Medium** | `context.DeadlineExceeded` 映射为 500 而非 504 | `internal/pkg/errors/errors.go` + `internal/pkg/httpx/httpx.go` |
| **Medium** | 消息软删除与评论软删除的幂等行为不一致 | `internal/repository/message.go` vs `internal/repository/comment.go` |
| **Low** | Metrics 缺少 DB 连接池、GC、SSE、存储操作指标 | `internal/pkg/metrics/metrics.go` |
| **Low** | `AppError.Wrap` 不捕获堆栈 | `internal/pkg/errors/errors.go` |
| **Low** | `Metrics` 每次观察加全局锁，高并发下竞争 | `internal/pkg/metrics/metrics.go` |
| **Low** | `presignTTL` 在 `app.go` 中硬编码 | `internal/app/app.go:29` |
| **Low** | `sso.insecure` 默认 true（生产应警告） | `configs/config.yaml` |
| **Low** | `signing_key` 默认值可绕过 | `configs/config.yaml` |

---

## 8. 本次与前四次分析的关系

```
第五次分析聚焦层：

  ┌─────────────────────────────────────────────────┐
  │                 用户体验层                        │
  │    方向A: 在线状态  方向B: 草稿/定时发送          │
  ├─────────────────────────────────────────────────┤
  │                API / 集成层                      │
  │    方向C: API Keys  方向E: 已读回执历史           │
  ├─────────────────────────────────────────────────┤
  │              业务逻辑层                          │
  │    第3节: 错误传播路径                           │
  ├─────────────────────────────────────────────────┤
  │              基础设施层                          │
  │    第1节: 中间件链  第2节: Metrics  第4节: 降级   │
  │    第5节: 配置系统                               │
  └─────────────────────────────────────────────────┘

前四次覆盖层（不再重复）:
  第1次: 架构概览 + 产品方向
  第2次: 产品缺口 + 平台演进
  第3次: 双实现一致性 + 后台goroutine
  第4次: 安全 + 运维 + API 设计
```
