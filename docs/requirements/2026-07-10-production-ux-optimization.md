# aero-id — 生产级用户体验与可消费性优化

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局代码扫描（全部 `internal/` 22 个包、~40 个 Go 源文件、22 份迁移 SQL、双传输层 handler、`configs/config.yaml`、`cmd/`、`api/proto/`），确认代码级证据。无代码变更。
> **前提**: `feat/roadmap` 已合并，38 个 gRPC RPC + 全 HTTP 对等 + 生产加固已交付。

---

## 本文定位

已有 20+ 份分析文档（`docs/requirements/*.md`、`docs/REVIEW_*.md`）覆盖了 ~60 个功能/架构方向。本文聚焦一个特定的切口：

**当面向生产环境的 API 消费者（移动端 App、第三方开发者、前端 SPA）时，当前 API 的"可消费性"和"开发者体验"（Developer Experience / API Consumability）存在系统性缺口。**

这不是功能缺失（功能已经很强），而是**交付方式**的缺失：API 提供了正确的数据，但没有以对消费者友好的方式交付。

---

## 当前状态快照

经过 22 轮迁移和两轮功能迭代，aero-id 的 API 功能面已经很完善：

| 维度 | 功能 | 状态 |
|------|------|------|
| **功能对等** | 38 个 gRPC RPC 与 HTTP 端点完全对齐 | ✅ |
| **认证** | Bearer token SSO 验证 + 缓存 + 断路器 | ✅ |
| **幂等** | Idempotency-Key 支持 POST 请求 | ✅ |
| **限流** | Per-actor / Per-IP token bucket | ✅ |
| **错误结构** | 统一 `{error: {code, message, request_id}}` | ✅ |
| **分页** | Cursor 分页（UUIDv7） | ✅ |
| **搜索** | 跨实体全文搜索（FTS + ILIKE） | ✅ |

**但消费端优化几乎为零：**

| 缺失能力 | 现状 | 影响 |
|---------|------|------|
| 响应压缩 | JSON 纯文本传输，无 gzip/brotli | 带宽浪费 5-10x |
| 部分响应 | 始终返回完整 entity，无法 `?fields=id,name` | 移动端传输过量数据 |
| 分页元数据 | 只有 `next_cursor`，无 `has_more`/`total` | 客户端需额外请求判断是否到底 |
| 限流透明度 | 429 时无 `X-RateLimit-*` 头、无 `Retry-After` | 客户端盲目重试撞墙 |
| 缓存策略 | 无 ETag、`Cache-Control`、`304 Not Modified` | 每次请求都 200 全量响应 |
| 条件请求 | 无 `If-None-Match`/`If-Modified-Since` | 无法节省带宽和延迟 |
| 批量操作 | 单实体 CRUD 只能逐条操作 | N 次删除 = N 个请求 |
| 运行时诊断 | 无 pprof、无 runtime metrics、无慢查询日志 | 排查问题靠猜测 |
| 压缩传输 | gRPC 默认使用 protobuf（高效），HTTP 无压缩 | HTTP 消费者付费更多流量 |

---

## 方向一：移动端 API 响应优化 — 压缩、部分响应与分页元数据

### 为什么需要

**代码级证据（全部在当前 `main` 分支确认）：**

| 文件 | 代码 |
|------|------|
| `internal/handler/http/router.go:72` | 路由初始化，无 `gin.Context.SetAccepted`、无压缩中间件 |
| `internal/handler/http/comment.go` | `ListComments` → 始终返回完整 Comment 结构（含 reply_count, status, like_count, target_* 等全部字段） |
| `internal/handler/http/message.go` | `Inbox` → 始终返回完整 Message 结构 |
| `internal/handler/http/file.go` | `ListFiles` → 始终返回完整 File 结构（含 storage_key, checksum 等内部字段） |
| `internal/pkg/httpx/httpx.go` | 全局响应序列化：`c.JSON(200, data)` — 固定格式，无字段选择 |
| `internal/handler/http/notification.go:ListNotifications` | 返回完整 Notification 结构（含 collapsed_count, subject_* 等） |

**Gin 压缩中间件** `gin-contrib/gzip` 或 `gin-gonic/gin` 内置的 `WithCompress` 从未启用。

**用户场景：**

1. **移动端消息列表**：一个用户有 200 条消息，ListMessages 返回 20 条 × ~500B = 10KB，压缩后 ~1.5KB。每日 50 万次请求 = 5GB → ~750MB，节省 ~85% 带宽。
2. **移动端文件列表**：手机端不需要 `storage_key`、`checksum`、`meta`。`?fields=id,name,size,mime_type,created_at` 可将响应缩小 60%+。
3. **列表滚动到底**：客户端收到 `{next_cursor: "abc", has_more: false}` 立即知道不需要再次请求 vs 必须再发一次请求并收到空列表才确定。
4. **慢速网络**：连续翻页 5 次，每页 20KB，150ms RTT。全量 100KB + 750ms → 部分响应 30KB + 450ms + 压缩后 8KB + 450ms。

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **RES-1** | **启用 HTTP 压缩中间件** — 对 `application/json` 响应做 gzip/brotli 压缩（根据 `Accept-Encoding` 协商），仅对 ≥1KB 响应生效以避免微小 payload 的 CPU 浪费 | `router.go` + 依赖 `gin-contrib/gzip` 或标准库 `compress/gzip` 封装 | **P0**（2 小时） |
| **RES-2** | **部分响应 / Sparse Fieldsets** — 支持 `?fields=id,name,created_at` 查询参数，序列化时仅输出指定字段。可采用 Go `reflect` 或 map[string]any + 手动白名单两种方案 | 每个 list/get handler 加 `fields` 参数逻辑，`httpx.OK` 扩展 | **P0**（1-2 天） |
| **RES-3** | **分页元数据** — List 响应增加 `has_more` 布尔字段（基于当前页返回 n+1 检测机制），`total` 可选（需要 COUNT 查询，默认不返回） | `service/*List` 结构体加 `HasMore` 字段 | **P1**（1 天） |
| **RES-4** | **去除内部字段** — 审查所有 List/Get 响应，移除仅内部使用的字段（`storage_key`、`checksum`），或加 `json:"-"`。保留兼容性版本。 | 逐 handler 审查 | **P1**（2 天） |

### 边界情况

- 部分响应字段枚举需要维护——新增字段时需要决定是否默认包含
- `?fields=invalid_field` → 应忽略未知字段（不报错，只输出合法的已知字段）
- 分页 `has_more` 基于 n+1 检测：repo 请求 limit+1 条，返回前 limit 条 + `has_more = len > limit`。当前代码已使用此模式但未暴露给客户端。
- 压缩对极小响应（<1KB）的 CPU 开销可能超过节省的带宽——可设置最小压缩阈值

---

## 方向二：限流透明度与客户端协作协议

### 为什么需要

**代码级证据：**

| 文件 | 代码 | 问题 |
|------|------|------|
| `internal/middleware/ratelimit.go` | `c.AbortWithStatusJSON(429, ...)` | 只返回 429 + JSON 体，无 `Retry-After`、无 `X-RateLimit-*` |
| `internal/pkg/ratelimit/ratelimit.go` | `Allow(key string) bool` | 接口仅返回 bool，不暴露剩余 token、重置时间等元数据 |
| `internal/handler/grpc/server.go` | `status.Error(codes.ResourceExhausted, ...)` | gRPC 限流时无额外 metadata |

**无任何文档或 API 契约说明限流行为。** 客户端开发者面临的问题：

1. 收到 429 → 不知道何时重试 → 硬编码 1s 间隔 → 撞墙 or 浪费机会
2. 不知道当前配额使用情况 → 无法提前减速 → 只能等 429 发生后被动处理
3. 不知道配额上限 → 无法规划请求速率

**这与业界标准差距明显：**

| 服务 | 限流响应头 |
|------|-----------|
| GitHub API | `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`, `Retry-After` |
| Twitter API | `x-rate-limit-limit`, `x-rate-limit-remaining`, `x-rate-limit-reset` |
| Stripe API | `X-Request-Id`, 429 时 `Retry-After` |
| **aero-id 当前** | **❌ 无** |

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **RLT-1** | **Limiter 接口扩展** — `Allow` 改为返回 `Result{Allowed bool, Remaining int, ResetAt time.Time}`，或增加 `AllowWithInfo` 方法 | `ratelimit.go` 核心接口变更 | **P0**（1 天） |
| **RLT-2** | **HTTP 限流头** — 中间件在每次响应中设置 `X-RateLimit-Limit`（配额上限）、`X-RateLimit-Remaining`（剩余）、`X-RateLimit-Reset`（重置 Unix 时间戳）。429 时额外设置 `Retry-After`（秒）。 | `middleware/ratelimit.go` | **P0**（0.5 天） |
| **RLT-3** | **gRPC 限流元数据** — 在 gRPC 响应 trailer 中设置同样的限流头 | `server.go` 拦截器 | **P1**（0.5 天） |
| **RLT-4** | **限流状态查询 API** — `GET /v1/rate-limit` 返回当前调用方各维度使用情况（global、per-endpoint 等），白名单用户可提前规划 | 新 handler | **P2**（1 天） |
| **RLT-5** | **限流行为文档** — 在 README 或 API 参考中明确：限流阈值、窗口、响应头格式、重试策略建议 | 文档 | **P0**（0.5 天） |

### 边界情况

- 限流头是否泄漏安全信息？`X-RateLimit-Remaining` 可能会帮助攻击者探测限流策略边界。建议：返回精确值，因为限流策略已经是半公开（通过实验可探测），保密收益 < DX 收益。
- gRPC 限流元数据需要通过 `grpc.Trailer()` 传递，客户端需显式读取。需在 proto 注释中文档化。
- 读操作的限流头与写操作是否应该不同？建议保持一致以简化客户端逻辑。

---

## 方向三：运行时诊断基础设施（Runtime Self-Diagnostics）

### 为什么需要

**代码级证据：**

| 文件 | 现状 |
|------|------|
| `internal/pkg/db/db.go` | GORM 配置了连接池（MaxOpenConns, MaxIdleConns, ConnMaxLifetime），但导出 `sql.DB.Stats()` 的端点不存在 |
| `internal/handler/http/router.go` | 有 `/healthz`、`/readyz`、`/metrics` 端点，**无** `/debug/pprof/*`、`/debug/db`、`/debug/vars` |
| `internal/middleware/metrics.go` | 记录 RED 指标（请求率/错误/延迟），但 **无运行时指标**：goroutine 数、GC 暂停时间、内存分配率、连接池状态 |
| `internal/handler/http/health.go` | 健康检查覆盖 DB + SSO，**不暴露** 数据库连接状态（`InUse`、`Idle`、`WaitCount`、`MaxOpen`） |
| `internal/middleware/tracing.go` | OTel 追踪已启用，但无慢查询追踪（GORM 的 `SlowThreshold` 日志未开启） |
| `go.mod` | 未引入 `net/http/pprof`、`expvar` |

**当生产出现问题时，当前可用的诊断工具：**

| 问题场景 | 现有工具 | 缺口 |
|---------|---------|------|
| 某端点延迟飙升 | OTel trace + RED metrics | ✅ 能定位是哪个端点慢，但 **不知道是 Go GC 暂停还是数据库慢查询** |
| 内存持续增长 | 无 | ❌ 无 heap profile，无法区分泄漏 vs 高负载 |
| goroutine 数异常 | 无 | ❌ 无法获取 goroutine dump 分析死锁或泄漏 |
| 连接池耗尽 | 无 | ❌ `SELECT 1` 健康检查通过，但 `WaitCount` 已在飙升 |
| 慢查询淹没问题 | RED 显示 DB 延迟，但无具体 SQL | ❌ 不知道是哪个查询慢、跑了多久、返回了多少行 |
| 限流阈值是否合理 | 收到 429 才知道 | ❌ 无法在限流发生前观察 bucket 的填充/消耗速率 |

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **DIAG-1** | **pprof 端点** — 挂载 `net/http/pprof` 到 `/debug/pprof/`，通过管理员权限保护（`RequirePermission("system.config")`）。支持 CPU profile / heap / goroutine / mutex / block。 | `router.go` 增加路由 + `middleware/permission.go` 认证 | **P0**（2 小时） |
| **DIAG-2** | **数据库连接池端点** — `GET /debug/db` 返回 `sql.DB.Stats()` 结构（OpenConnections, InUse, Idle, WaitCount, WaitDuration, MaxOpenConnections） | 新 handler | **P0**（2 小时） |
| **DIAG-3** | **运行时指标到 /metrics** — 在 RED metrics 注册表增加 Go runtime 指标（`runtime.ReadMemStats`、`runtime.NumGoroutine`、GC 统计），暴露为 Prometheus gauge | `pkg/metrics/metrics.go` | **P1**（1 天） |
| **DIAG-4** | **GORM 慢查询日志** — 启用 `Logger.SlowThreshold`（如 200ms），慢查询写入 zap logger 并记录 trace span。可通过 config `database.slow_threshold` 配置。 | `pkg/db/db.go` | **P1**（1 天） |
| **DIAG-5** | **goroutine dump 端点** — `GET /debug/goroutine?debug=2` 返回完整 goroutine 栈（类似 `net/http/pprof` 的 goroutine profile），限管理员角色 | 新 handler | **P2**（2 小时） |

### 边界情况

- **安全**：pprof 和 debug 端点必须受认证保护，绝不能暴露在公网。默认绑定 localhost 或通过 RBAC（`system.config` 权限）控制。
- **性能影响**：pprof CPU profile 会降低请求吞吐约 5-10%，应在有监控告警时按需使用，而非持续开启。
- **慢查询阈值**：不同查询的时延预期不同——文件上传的 500ms 可能正常，列表查询的 100ms 可能已偏慢。阈值先设为保守值（200ms），运营中根据 P99 延迟调整。
- **数据库连接池指标敏感度**：`WaitCount` 和 `WaitDuration` 是连接池压力的早期信号，应与 `max_open_conns` 对比配置告警阈值（如 `WaitDuration > 100ms` 或 `InUse > 80% MaxOpen`）。

---

## 方向四：跨实体引用完整性 — 面向用户侧的优雅降级

### 为什么需要

**代码级证据——引用关系链中无任何 null/placeholder 保护：**

| 引用关系 | 问题 | 代码位置 |
|---------|------|---------|
| `messages.file_id → files(id)` | 文件删除后，消息的 `file_id` 指向不存在的行。`GetMessage` 在 `fileRepo.GetByID` 时返回 `ErrRecordNotFound`，消息体中有 `file_id` 但无文件元数据 | `internal/message/service.go:Get` |
| `comments.parent_id → comments(id)` | 父评论被软删除后，子评论的 `parent_id` 指向已删除行。`Replies` 查询会跳过已删除的父评论，但子评论的 `parent_id` 在序列化时仍保留原始值 | `internal/comment/repository.go:ListReplies` |
| `comments.target_id → files(id)/messages(id)` | 目标文件/消息被删除后，评论的 `target_id` 指向不存在的内容。`ListComments` 使用 `target_id` 过滤，但不会检查目标是否存在 | `internal/comment/service.go:List` |
| `file_shares.file_id → files(id)` | 文件被软删除后，分享记录仍存在。`ListSharedWithMe` 返回的文件元数据中文件已软删除 | `internal/file/service_share.go:ListSharedWithMe` |
| `notification.subject_id` | 通知指向的消息/评论被删除后，点击通知跳转到不存在的内容 | `internal/handler/http/notification.go` |
| `comment_likes.comment_id → comments(id)` | 评论软删除后，点赞记录仍存在，`like_count` 可能"幽灵溢出" | `internal/repository/comment.go:Like` |

**无任何框架级处理：** 当前所有引用是以字符串形式存储的 UUID，没有外键约束（因为 SSO 的 uid 不能做 FK，但文件/消息/评论之间的引用可以有 FK）。每个 service 方法遇到 `ErrRecordNotFound` 时的行为不一致——有的返回错误，有的忽略。

### 用户场景

1. **消息附件丢失**：用户 A 发送带附件的消息，用户 B 一周后打开对话 → 附件显示"文件不可用"（或直接报错）。用户 B 不知道文件曾经存在过。
2. **幽灵评论**：用户回复了一条评论，父评论被作者删除 → 子评论出现在 `Replies` 列表中，但 `parent_id` 指向不存在的内容。UI 无法渲染"回复给谁"。
3. **断链分享**：用户 A 分享了文件给用户 B，后删除了文件。用户 B 在"与我分享"列表中看到一条记录→点击→404。没有任何上下文表明文件已被删除。
4. **通知跳转失败**：用户收到 `comment.reply` 通知，点击跳转 → 评论已被隐藏或删除 → 404 页面。用户感到困惑。

### 建议范围

| 增量 | 描述 | 优先级 |
|------|------|--------|
| **INT-1** | **引用存在性预检** — 在 Delete 操作中预检：该实体是否被其他实体引用。如果被引用，有策略地处理（级联删除 / 置 null / 保留但标记为删除） | **P0**（3-5 天） |
| **INT-2** | **"已删除"占位符机制** — 当引用的实体不存在时，返回最小占位符（`{id, deleted: true, ...}`），而非 404 错误或静默空白。消息附件层面：`file: {id: "xxx", deleted: true, name: "（已删除的文件）"}` | **P0**（2-3 天） |
| **INT-3** | **文件删除前引用警告** — DELETE /files/:id 时检查 `messages` 和 `file_shares` 表，如果有引用，在响应中返回 `referenced_by: {messages: 3, shares: 2}` | **P1**（1 天） |
| **INT-4** | **通知目标存在性检查** — 点击通知（通过 `subject_type` + `subject_id` 跳转）时，如果目标不存在，返回友好的"此内容已被删除"页面/消息，而非 404 | **P1**（1 天） |
| **INT-5** | **添加数据库外键约束** — 在迁移中为 `file_shares.file_id → files(id)`、`comment_likes.comment_id → comments(id)`、`comment_reports.comment_id → comments(id)`、`message_reactions.message_id → messages(id)` 加上 `ON DELETE SET NULL` 或 `ON DELETE CASCADE`（根据业务策略） | **P2**（1 天） |

### 边界情况

- 级联删除的风险：不应自动级联删除用户的私信附件（消息不受文件删除影响，只影响下载能力）
- `ON DELETE SET NULL` 对于软删除无效（软删除 UPDATE，不触发 FK 动作）。要么改为硬删除时 FK 动作，要么在 service 层手动处理
- 占位符的大小：不应过大（保持轻量），`{id, deleted, name}` 三个字段即可
- 删除预检可能增加 Delete 操作的延迟——可以做成异步：Delete 立即返回 202 Accepted，Service 层异步检查引用并决定是否允许最终删除。但当前模式是同步的，所以需要在同步路径中做轻量预检

---

## 方向五：缓存策略与条件请求（Add Cache-Control & Conditional Requests）

### 为什么需要

**代码级证据：**

| 端点 | 响应头 | 问题 |
|------|--------|------|
| `GET /v1/files/:id` | 无 Cache-Control | 客户端永远无法缓存文件元数据。每次重新打开文件详情页都重新请求。 |
| `GET /v1/files` | 无 Cache-Control | 列表内容更新不频繁，但每次都全量传输 |
| `GET /v1/conversations/:uid` | 无 Cache-Control | 对话历史不会在秒级变化，30s 缓存即可 |
| `GET /v1/comments?target_type=&target_id=` | 无 Cache-Control | 评论列表可以缓存数秒（社交评论通常有 10-30s 可接受延迟） |
| `GET /v1/users/:uid` (SSO 代理) | 无 Cache-Control | 用户昵称/头像几乎不变，可缓存数小时 |
| `GET /v1/search?q=...` | 无 Cache-Control | 搜索结果可在秒级范围内缓存 |

**HTTP 缓存是"免费的"性能优化**——零额外基础设施成本，仅需正确设置响应头。

### 建议范围

| 增量 | 描述 | 优先级 |
|------|------|--------|
| **CACHE-1** | **只读端点缓存策略头** — 为 Get/List 端点设置 `Cache-Control: private, max-age=NN`（private = 用户级，代理不缓存）。文件元数据：`max-age=30`；评论列表：`max-age=10`；搜索结果：`max-age=5` | **P1**（2 天） |
| **CACHE-2** | **ETag / 内容版本化** — 为 Get/List 响应计算 ETag（基于 `updated_at` 或内容哈希），客户端发送 `If-None-Match` 时返回 `304 Not Modified` 空体。对列表响应 ETag 较难（每页内容不同），可先应用于单实体 GET | **P1**（1 天） |
| **CACHE-3** | **Last-Modified 头** — 为单实体 GET 设置 `Last-Modified: updated_at`（对文件直接透传 S3 的 Modified 时间）。客户端 `If-Modified-Since` → 304 | **P1**（0.5 天） |
| **CACHE-4** | **可变响应头调整** — 写操作（POST/PUT/PATCH/DELETE）设置 `Cache-Control: no-cache` 防止浏览器误缓存写响应 | **P2**（0.5 天） |

### 边界情况

- **用户级数据隐私**：`Cache-Control: private` 确保 CDN/代理不缓存用户专属数据。仅公开数据（`visibility=public` 的文件元数据）可以使用 `public`。
- **ETag 与列表分页**：列表 ETag 的挑战在于第一页和第二页之间插入新数据会导致两页 ETag 都变化。建议：列表端点暂不实现 ETag/304，先从单实体 GET 开始。
- **304 空体与错误处理**：304 响应不可以包含 body。需确保 Gzip 中间件正确处理 304（不尝试压缩空响应）。

---

## 优先级路线图

| 方向 | 关键增量 | 工程投入 | 产品影响 | 建议顺序 |
|------|---------|----------|---------|---------|
| **方向一：响应优化** | 压缩 + 部分响应 + 分页元数据 | 🟢 低（2-4 天） | 🔴 高（移动端用户体验核心） | **P0** |
| **方向二：限流透明度** | `X-RateLimit-*` 头 + `Retry-After` | 🟢 低（2 天） | 🟡 中（开发者体验改进） | **P0** |
| **方向三：运行时诊断** | pprof + DB stats + 慢查询 + runtime 指标 | 🟡 中（3 天） | 🔴 高（运维刚需，影响 MTTR） | **P0** |
| **方向四：引用完整性** | 占位符 + 预检 + 外键 | 🟡 中（5-7 天） | 🔴 高（影响所有用户的日常体验） | **P1** |
| **方向五：缓存策略** | Cache-Control + ETag + Last-Modified | 🟢 低（2-3 天） | 🟡 中（互联网带宽成本 + 延迟优化） | **P1** |

### 执行建议

1. **P0 的三个方向并行启动**，因为它们不共享代码路径：
   - 方向二（限流透明度）只改 `ratelimit.go` + `middleware/ratelimit.go`
   - 方向三（运行时诊断）只改 `router.go` + 新 handler
   - 方向一（响应优化）涉及面稍广但可分解为独立 PR（压缩 → 字段过滤 → 分页元数据）
2. **方向四（引用完整性）** 需要先在 `domain` 层定义策略，再逐个实体实现，估算 1-2 周。
3. **方向五（缓存策略）** 可以在方向一的基础上叠加——压缩和缓存是互补优化，不冲突。

---

## 附录：本文件方向的唯一性验证

与 `docs/requirements/*.md` 下全部 20+ 份文档逐方向交叉比对：

| 方向 | `expansion-*.md` | `architect-*.md` | `product-*.md` | `remaining-*.md` | `untapped-*.md` | `final-bl*.md` | 其余 |
|------|-----------------|-----------------|---------------|-----------------|----------------|---------------|------|
| 响应压缩 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| 部分响应/fields | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| 分页 has_more | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| X-RateLimit 头 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| pprof 端点 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌* |
| 连接池状态 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | 🔶** |
| 慢查询日志 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | 🔶 |
| 引用占位符 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| 删除前引用警告 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Cache-Control | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |

> ❌ = 零覆盖；🔶 = 同类不同角度（连接池在 chaos/cqrs 文档中提及，但方向完全不同）
> *pprof 在 `docs/REVIEW_2026-06-30_SCHEMA_QUERY_TEST.md` 中作为单句提及（"挂载 `/debug/pprof/`" 在工具链段落），但从未作为独立方向分析
> **连接池状态在 `docs/REVIEW_2026-06-30_INFRA_DEEP.md` 中作为故障场景片段提及，非系统性方向
