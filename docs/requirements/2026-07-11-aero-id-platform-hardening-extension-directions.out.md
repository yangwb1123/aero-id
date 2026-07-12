现在我已经验证了所有关键代码点。这是一份全面评估：

---

## 整体评价：方法论扎实，但证据部分存在重大事实错误

这份文档的**方向判断和架构洞察是高质量的**——它能正确识别平台级别的缺口（自适应限流、媒体隐私管线、降级契约），这些问题确实是 aero-id 从功能完备走向平台级的关键交付。但 **"全量代码逐文件扫描"的断言在 Direction 1 上被证伪**——3 条核心证据中有 2 条与当前 `main` 分支的实际代码**完全矛盾**。

---

## 方向一：事实错误与纠正

### ❌ 不准确主张 1：「Rate Limiter 不输出任何协议可见的状态」

**实际代码证明**：`Limiter` 有 `RetryAfter()` 方法（`/internal/pkg/ratelimit/ratelimit.go:97-102`），且 middleware（`/internal/middleware/ratelimit.go:54-58`）正确设置 `Retry-After` 头：

```go
retry := l.RetryAfter()
if secs := int(retry.Seconds()); secs > 0 {
    c.Header("Retry-After", strconv.Itoa(secs))
} else {
    c.Header("Retry-After", "1")
}
```

更是返回了包含 `request_id` 的结构化 429 错误体。

**缺失的**：确实是 `X-RateLimit-Limit`、`X-RateLimit-Remaining`、`X-RateLimit-Reset` 头。但这些功能的一部分（`Retry-After`）已经实现。

### ❌ 不准确主张 2：「Request ID 在日志中存在但不在响应中返回」

**实际代码证明**：`/internal/middleware/request_id.go:22` 明确写回响应头：

```go
c.Writer.Header().Set(RequestIDHeader, rid)
```

这是项目启动后的第一次提交就实现的基础设施。

### ❌ 不准确主张 3：「错误响应不包含可关联的追踪标识」

**实际代码证明**：`/internal/pkg/httpx/httpx.go:39-40` 在错误体中包含 `request_id`：

```go
type errorDetail struct {
    Code      string `json:"code"`
    Message   string `json:"message"`
    RequestID string `json:"request_id,omitempty"`
}
```

Rate limit 429 响应也包含 `request_id`。

### ✅ 方向一中仍然正确的子问题

虽然上面的具体证据有误，但以下两个问题**仍然成立**，且被文档正确识别：

1. **缺少 `X-RateLimit-Limit/Remaining/Reset`**：消费者确实无法知道剩余配额和桶重置时间。`RetryAfter` 只告诉消费者"等多久"，不告诉"还能发多少个"。
2. **缺少响应级别的系统状态指示**：没有 `X-Aero-Status: healthy/degraded` 之类的头。这是方向五的接入点。

---

## 方向二：证据基本准确，且实际情况比文档声称的更严重

### ✅ 准确的：81 个不同错误码散落各处

文档声称 "40-50 个不同的错误码"，但实际打桩发现 **81 个唯一错误码**（`internal/service/` 内 81 个，`internal/handler/` 另加 25 个），分布在 **76 个 Go 源文件**中。问题规模比文档声称的更大。

### ✅ 准确的：AppError 结构化程度不足

确实缺少 `Retryable`、`FieldErrors`、`DocURL` 等消费者导向字段。

### 🔶 需要补充的观察

文档指出"Repository 层的错误翻译没有为消费者提供结构化上下文"，这是对的。但有一个重要细节被文档忽略了：**aero-id 已经有幂等键（idempotency key）机制**来处理重复请求——在 `internal/handler/http/router.go:59` 中可以看到 `Idempotency` middleware 的安装。这意味着"重复请求"的场景已经被部分覆盖，只是错误码层面没有区分化。

---

## 方向三：完全准确，且文档低估了一个关键子问题

### ✅ 准确的：Limiter 完全静态，无反馈输入

验证通过。

### ✅ 准确的：配置层只有静态参数

验证通过。

### ✅ 准确的：所有操作共享同一限流桶

验证通过。router 中所有 v1 路由共享同一个 `ratelimit.Limiter` 实例。

### 🔶 文档低估的：写操作的幂等键与限流的交互

aero-id 已经有 `Idempotency` middleware（`/internal/handler/http/router.go:59`）。当幂等键重复时，系统可以返回缓存的响应而**不消耗限流令牌**，但这没有实现——幂等检查在限流之后，实际上每个重复请求都消耗了令牌。这是一个额外的优化点。

---

## 方向四：完全准确，但已实现的功能比文档声称的少

### ✅ 准确的：文件管线无元数据处理

验证通过。`Create` 方法中 `store.Put(ctx, actor, in.Name, reader)` 直接写入原始字节。**零中间处理**。

### ✅ 准确的：File 模型和 Storage 接口无处理钩子

验证通过。

### ✅ 准确的：MIME 验证不影响处理管线

验证通过。

---

## 方向五：基本准确，但遗漏了现有优雅降级基础设施

### 🔶 代码已存在但被文档忽略的降级能力

1. **SSO stale-cache**（`/internal/sso/breaker.go` + 配置 `StaleTTL`）：当 SSO 不可用时，缓存中已有有效 entry 的请求仍然可以通过验证。这不是完美的"已认证用户读操作继续服务"，但它是一个基础降级机制。
2. **断路器**（breaker）已实现（`/internal/sso/breaker.go`）：三状态断路器（closed/open/half-open），可配置跳闸阈值和冷却时间。
3. **/readyz 健康检查**已经实现 per-dependency 检测。

### ✅ 文档正确的核心问题

尽管有以上基础设施，**业务逻辑层完全不感知降级状态**。`service` 层没有根据 SSO/DB/S3 健康状态改变行为的逻辑。`/readyz` 的状态没有被传播到请求处理路径。

---

## 需要补充的观察（文档遗漏的重要问题）

### 1. 文档没有发现：gRPC 端的错误码不一致问题更严重

```go
// internal/handler/grpc/comment.go
// gRPC handler 只用 GRPCError 映射，丢失了 HTTP 端的详细错误码
```

虽然 HTTP 和 gRPC 共享 `AppError`，但 `GRPCError()` 函数（`/internal/pkg/errors/errors.go:72-79`）只输出 `ae.GRPC` 状态码和 `ae.Message`，**完全丢弃了 `ae.Code`（错误码）**。这意味着 gRPC 消费者永远看不到 `file.too_large` 或 `comment.nesting_too_deep` 这样的结构化错误码——只能看到 `InvalidArgument` 这样的泛化 gRPC 状态码。这个跨传输错误码映射问题比文档描述得更严重。

### 2. 文档没有发现：本地存储后端的 HMAC 签名下载路径可能受 `Content-Disposition` 影响

下载 URL 使用 HMAC 签名（`/internal/storage/local.go`），但响应中没有设置 `Content-Disposition: attachment` 或 `X-Content-Type-Options: nosniff`，这确实存在反射型 XSS 风险（如果用户上传一个包含 HTML 内容的文件并获取直接的下载 URL）。文档在"快速获胜项"中提到了这个，但没提供代码证据位置。

### 3. 文档没有评估：迁移系统的扩展性

23 次迁移中包含了性能索引和 FTS 索引，但文档没有分析迁移回滚策略、零停机迁移设计，或迁移与 GORM auto-migrate 的共存规则（config 中已有 `AutoMigrate` 配置字段，绕过 `make migrate-up` 的手动流程）。

---

## 优先级和顺序：我基本同意，但有调整

| 方向 | 文档建议 | 我的调整 | 理由 |
|---|---|---|---|
| 方向一 | P0 | **P0** — 但工作内容改为添加 `X-RateLimit-*` 头，而非已实现的 `Retry-After` 和 `X-Request-Id` | 已有基础设施很好，剩下的是补充缺失头 |
| 方向五 | P0 | **P0** — 且应该在 Sprint 1 中与方向一合并执行 | 都涉及响应头 → 一起改 |
| 方向二 | P1 | **P1** — 但应优先修复 gRPC 端错误码丢失问题（P0 级别bug） | gRPC 消费者完全看不到结构化错误码 |
| 方向四 | P1 | **P0** — GDPR 合规风险较高，建议升为 P0 | 地理位置数据不经用户同意存储即违规 |
| 方向三 | P1 | **P2** — 降级，因为影响面大但可以先依赖现有限流 | 当前限流器在正常负载下有效，自适应是优化非必要 |

---

## 结论

**文档的优点**：
- 架构视角正确，方向选择合理
- 能识别从"功能完备"到"平台级"的硬化缺口
- "快速获胜项"中的三项（除 `X-Request-Id` 已实现外）都是高影响低成本的合理建议

**文档的主要问题**：
1. **Direction 1 的证据在事实上错误**——3 条证据中有 2 条与当前代码矛盾，说明"全量代码逐文件扫描"的断言不成立。作者可能基于早期版本的代码或记忆中的设计文档撰写。
2. **遗漏了关键问题**——gRPC 端错误码丢失（这是一个比 HTTP 端缺少 header 更严重的问题），以及 SSO stale-cache 等已存在的降级基础设施。
3. **低估了错误码扩散的规模**——声称 40-50 个，实际 81 个。

**总体打分**：
- 架构方向判断：**A**（正确识别了 5 个重要缺口）
- 产品视角分析：**A-**（优先级排序合理，合规角度切入好）
- 代码级证据准确性：**C**（Direction 1 存在重大事实错误）

这份文档的最有价值部分不是它的证据，而是它的**架构视角和方向判断**。建议基于实际代码修正证据部分后，作为内部架构评审文档使用。
