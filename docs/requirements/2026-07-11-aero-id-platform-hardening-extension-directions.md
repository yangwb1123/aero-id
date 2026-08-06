# aero-id — 资深架构师/产品经理视角：五项平台硬化扩展方向

> **日期**: 2026-07-11
> **视角**: 资深架构师 + 产品经理
> **方法**: 全量代码逐文件扫描 `internal/` 全部 ~180 Go 源文件（23 个子包）、`migrations/` 23 份 SQL、`api/proto/`、`configs/`、`cmd/`、`deployments/`、`Makefile`。基于当前 `main` 分支的实际代码状态。
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角分析。

---

## 前提：项目当前能力定位

aero-id 已是一个**功能完备**的数据服务平台。以下是经实际代码扫描确认的已交付能力：

| 域 | 已交付能力 |
|---|---|
| **传输层** | HTTP REST + gRPC 全对等（38+ RPCs）+ SSE 实时推送 + Webhook 事件转发 |
| **文件管线** | 流上传、SHA-256 内容去重、MIME/大小校验、S3 预签名直传、引用计数 GC、显式分享/撤销、审计日志 |
| **消息系统** | 1:1 私信、Reactions、编辑/软删、已读回执 + 计数、Block 拦截、事务性 Outbox 事件 |
| **评论系统** | 两层嵌套（楼中楼）、Like/Unlike（原子计数器）、举报→审核队列、Hide/Unhide、编辑 |
| **通知系统** | Outbox 驱动的异步创建 + 折叠去重、SSE 实时推送（last_event_id 回放）、偏好设置（启用/静默时段）、Block 抑制、Webhook 签名投递 |
| **全文搜索** | 跨实体 FTS（PG tsvector + GIN 索引）、CJK ILIKE 回退 |
| **写路径可靠性** | 事务性 Outbox + 幂等键 + 死信 + 重试退避 |
| **可观测性** | OTel 链路追踪（含 SSO hop）、RED 指标（HTTP + gRPC + SSO + Outbox）、per-dependency `/readyz` |
| **安全** | 方向性 Block、审计日志、Admin 角色 + 权限体系（moderator/admin）、SSO 断路器 + stale-cache + 证书固定 |
| **数据模型** | 23 次可逆迁移（含性能索引、FTS 索引、通知折叠、Outbox 重试、Storage Ref 引用计数） |

**当前定位**: 功能层面已越过 MVP 标准，但**从"功能完备"到"平台级"存在系统性硬化缺口**——这些缺口不在地表功能上，而在 API 消费者体验、安全基线、运行时自适应能力和运维契约上。

---

## 方向一：API Consumer Observability Transparency（API 消费者可观测性透明化）

### 问题陈述

aero-id 的内部可观测性已经相当成熟（OTel 追踪、RED 指标、per-dependency 健康检查），但这些能力**完全不对 API 消费者开放**。调用方无法获取自身请求的处理追踪信息、无法感知速率限制状态、无法将错误与上游请求关联。这导致：

- 客户端 SDK 无法实现智能重试（不知道 Rate Limit 何时重置）
- 开发者调试需要手动关联日志，没有 `request_id` 回传给技术支持
- 前端无法向用户展示友好的限流状态（"请 30 秒后再试"）

### 代码级证据

**证据 1：Rate Limiter 不输出任何协议可见的状态**

```go
// internal/pkg/ratelimit/ratelimit.go — Allow 方法
func (l *Limiter) Allow(key string) bool {
    // 返回一个 bool，没有任何剩余额度、重置时间等信息
}
```

```go
// internal/middleware/ratelimit.go — HTTP 中间件
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetString(middleware.ActorKey)
        if key == "" {
            key = c.ClientIP()
        }
        if !l.Allow(key) {
            // 只返回 429 状态码
            httpx.WriteError(c, apperr.TooManyRequests(
                "rate_limit.exceeded",
                "rate limit exceeded",
            ))
            c.Abort()
            return
        }
        c.Next()
    }
}
```

没有设置 `X-RateLimit-Limit`、`X-RateLimit-Remaining`、`X-RateLimit-Reset` 或 `Retry-After` 头。客户端完全不知道自己被限流的状态、剩余额度、或何时可以重试。

**证据 2：Request ID 在日志中存在但不在响应中返回**

```go
// internal/middleware/request_id.go
func RequestID() gin.HandlerFunc {
    return func(c *gin.Context) {
        id := c.GetHeader("X-Request-Id")
        if id == "" {
            id = uuid.New().String()
        }
        c.Set(middleware.RequestIDKey, id)
        c.Next()
        // 注意：没有将 request_id 写入响应头
        // 没有 c.Header("X-Request-Id", id)
    }
}
```

`request_id` 在整个请求生命周期中是可用的（日志、追踪），但没有写回响应头。消费者无法将错误响应与日志中的追踪信息关联。

**证据 3：错误响应不包含可关联的追踪标识**

```go
// internal/pkg/httpx/httpx.go — WriteError 方法
func WriteError(c *gin.Context, err error) {
    // 序列化 AppError 为 JSON，写入 HTTP 响应
    // 输出包含 error 和 error_description
    // *不包含* request_id、trace_id、或 retry_after 字段
}
```

当消费者收到 `429 rate_limit.exceeded` 时，没有任何信息可以告诉它：
- 当前消耗了多少额度（Remaining）
- 桶何时重置（Reset）
- 应该等待多久重试（Retry-After）

### 架构师视角

当前的可观测性架构是**内向的**：指标、追踪和日志都是为运维人员设计的，完全忽略了 API 消费者这个同样重要的受众。对于一个平台级服务来说，这是架构上的"最后一公里"缺失。

具体而言，当前缺少三个协议级别的透明度契约：

1. **速率限制头**（`X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`）—— 让客户端 SDK 实现智能退避和优雅降级
2. **请求追踪头**（`X-Request-Id` 在响应中回传）—— 让消费者能够将错误与运维侧的日志/追踪关联
3. **错误响应中的相关性字段**（`request_id`, `retry_after` 在错误体中）—— 让自动化工具和人类开发者都能获取调试上下文

这些能力的共同特征是：**代码中的信息已存在，只是没有跨出进程边界**。`request_id` 已经有了但没写回；`ratelimit.Limiter` 内部有 `tokens` 计数但没暴露；错误处理已经结构化但没有扩展字段。

### 产品经理视角

从产品角度看，API 消费者体验是平台粘性的核心组成部分。当前状态意味着：

- **开发者调试困难**：当集成方收到 429 或 500 时，无法将错误与任何标识关联，支持工单无法追踪
- **前端体验粗糙**：文件上传被限流时，前端只能显示"服务器错误"而非"请在 30 秒后重试"
- **生态建设受阻**：计划中的第三方集成（SDK、Webhook 消费者、CLI 工具）都无法获得良好的错误处理体验

这是一个**低成本、高影响**的方向——主要是 header/field 的添加，不涉及架构变革，但对开发者和用户体验有立竿见影的提升。

### 边界情况

- 分布式部署下，`X-RateLimit-Reset` 需要基于当前实例的桶状态计算，多实例间可能存在偏差（但在同一实例上始终准确）
- `X-Request-Id` 应优先透传客户端传入的值（已在 `RequestID()` 中实现），确保端到端追踪链一致
- Rate limit header 的精度需要与桶的刷新粒度匹配（秒级 vs 毫秒级）

---

## 方向二：Structured Error Contract & API Developer Experience（结构化错误契约与开发者体验）

### 问题陈述

aero-id 的错误模型基于 `AppError` 结构体，包含错误码（`error_code`）和描述（`error_description`），但错误码是**无中心注册的散落字符串**，没有类型安全、没有文档、没有版本控制、没有消费者指南。对于集成开发者而言，错误处理的唯一方式是"字符串匹配"，这在脆弱性和可维护性上都是反模式。

### 代码级证据

**证据 1：错误码是散落在 50+ 个文件中的硬编码字符串**

```go
// internal/service/comment.go
return nil, apperr.BadRequest("comment.invalid", "invalid request body")

// internal/service/file.go
return nil, apperr.BadRequest("file.too_large", "file exceeds the 500MB limit")

// internal/service/message.go
return nil, apperr.BadRequest("message.invalid", "invalid kind")

// internal/service/notification.go — 无错误码引用（GET 类操作）

// internal/middleware/auth.go
return nil, apperr.Unauthorized("auth.missing_token", "missing bearer token")

// internal/middleware/ratelimit.go
httpx.WriteError(c, apperr.TooManyRequests("rate_limit.exceeded", "rate limit exceeded"))

// internal/handler/grpc/comment.go — gRPC 端映射同样的错误码
```

每个服务、每个 handler 都自由地定义新的错误码字符串。目前全库约有 40-50 个不同的错误码，**没有任何注册表、枚举类型或文档**。

**证据 2：AppError 结构体缺少为消费者设计的辅助字段**

```go
// internal/pkg/errors/errors.go
type AppError struct {
    Code       string // 错误码，如 "file.too_large"
    Message    string // 人类可读描述
    HTTPStatus int    // HTTP status code
    GRPCCode   codes.Code
    // 没有:
    //   - Retryable bool         // 是否可安全重试
    //   - RetryAfter time.Duration // 建议等待时间
    //   - FieldErrors []FieldError  // 字段级验证错误（适用于 REST）
    //   - InternalID string       // 内部追踪 ID（目前靠 request_id，但未关联）
    //   - DocURL string           // 指向文档的链接
}
```

**证据 3：没有跨传输的错误码映射保证**

```go
// internal/handler/http/file.go — HTTP handler
httpx.WriteError(c, apperr.BadRequest("file.invalid", "missing 'file' form field"))

// internal/handler/grpc/file.go — gRPC handler（假设同样给 400 类错误）
// gRPC 端也使用同一个 AppError 类型，但错误码一致性的唯一保障是开发人员记得用相同的字符串
```

虽然 HTTP 和 gRPC 共享 `AppError` 类型，但错误码的**语义一致性没有自动化验证**。没有测试确保相同的业务错误在 HTTP 和 gRPC 上返回相同的错误码。

**证据 4：Repository 层的错误翻译没有为消费者提供结构化上下文**

```go
// internal/repository/comment.go
if err := r.db.WithContext(ctx).Create(...).Error; err != nil {
    return fmt.Errorf("create comment: %w", err)
}

// internal/service/comment.go 中翻译为：
return nil, apperr.Internal("comment.create_failed", "failed to create comment").Wrap(err)
```

错误从 DB 到 service 的翻译过程中丢失了所有 DB 级别的上下文（唯一约束违反、外键错误等）。消费者永远看不到"这是重复请求" vs "引用的文件不存在"的区别，它们都被映射为同一个 `comment.create_failed`。

### 架构师视角

错误契约是 API 的**一等公民**，但当前被当作文本字符串处理。这带来的架构问题：

- **无可发现性**：开发者无法通过编程方式获取所有可能的错误码列表
- **无演化能力**：错误码的添加/废弃/重命名无法版本化管理
- **无结构化**：消费者只能通过字符串解析来区分错误类型（例如检测 `file.too_large` 来提示用户压缩文件）
- **无国际化入口**：错误信息是英文硬编码的，没有 i18n 的接入点

需要的是一个**3 层错误架构**：

```
Layer 1: 错误码注册中心（Error Registry）
  - 每个错误码唯一注册，含 HTTP/gRPC 映射、可重试性、文档链接
  - 可用 Go 枚举或代码生成确保类型安全

Layer 2: 结构化错误体（Structured Error Body）
  - field_errors: 字段级验证错误列表
  - retryable: 布尔值
  - retry_after: 持续时间（秒）
  - request_id: 关联的追踪 ID
  - doc_url: 指向该错误码文档的链接

Layer 3: 消费者文档（Consumer Documentation）
  - 自动生成的错误码文档（从注册中心导出）
  - SDK 中的错误类型映射
```

### 产品经理视角

对于 API 产品的长期健康：

- **集成成本**：当前每个集成方都需要手动匹配错误码字符串，且无法依赖任何版本化保证
- **支持成本**：错误信息不包含 `request_id`，用户报告问题后运维人员无法关联到具体日志
- **竞争差异**：优秀的开发者体验（Stripe、Twilio 风格的结构化错误）是 API 产品的重要竞争壁垒
- **合规需求**：GDPR/CCPA 相关的错误（数据不存在、权限不足）需要明确的可重试性分类

第一批次建议是建立一个**错误码注册中心**（一个集中常量定义 + 自动生成文档），将目前 40-50 个散落字符串收拢。后续再迭代结构化错误体。

### 边界情况

- 新旧错误码的兼容性：新增错误码不应破坏现有消费者（永远是 ADDITIVE）
- 安全错误（`auth.unauthorized`）应避免返回过多细节，防止信息泄露
- 内部错误（`internal`）不应暴露实现细节给消费者

---

## 方向三：Adaptive Self-Regulating Rate Limiting（自适应速率限制）

### 问题陈述

当前速率限制器使用**静态配置**——RPS 和 Burst 在启动时从 YAML 读入，在整个运行生命周期中保持不变。它不感知下游依赖（SSO、Postgres、S3）的健康状态，不感知系统负载（CPU、内存、连接池使用率），也不区分操作的成本（搜索 vs 简单读取）。这意味着：

- 当下游 SSO 开始变慢时，限流器不会自动降低准入速率来保护系统
- 数据库连接池接近耗尽时，限流器不会提前节流
- 昂贵操作（搜索、文件上传）和廉价操作（列表查询）被同一限流桶对待

### 代码级证据

**证据 1：Limiter 是纯静态的，没有反馈输入**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu      sync.Mutex
    buckets map[string]*bucket
    rps     float64
    burst   int
    // 没有:
    //   - errorRate     float64 // 最近错误率
    //   - latencyP99    time.Duration // 最近延迟
    //   - saturation    float64 // 系统饱和度
    //   - dynamicRPS    float64 // 动态调整后的 RPS
}
```

`Allow()` 方法只在令牌桶上做检查，不查询任何外部状态：

```go
func (l *Limiter) Allow(key string) bool {
    l.mu.Lock()
    defer l.mu.Unlock()
    b, ok := l.buckets[key]
    if !ok {
        b = &bucket{tokens: float64(l.burst), last: time.Now()}
        l.buckets[key] = b
    }
    b.refill(l.rps) // 使用固定的 rps 补充令牌
    if b.tokens >= 1 {
        b.tokens--
        return true
    }
    return false
}
```

**证据 2：配置层只有静态参数**

```go
// internal/config/config.go
type RateLimitConfig struct {
    Enabled      bool          `mapstructure:"enabled"`
    RPS          float64       `mapstructure:"rps"`
    Burst        int           `mapstructure:"burst"`
    IdleTTL      time.Duration `mapstructure:"idle_ttl"`
    CleanupEvery time.Duration `mapstructure:"cleanup_every"`
    // 没有:
    //   - DynamicAdjust    bool    // 是否启用动态调整
    //   - MinRPS           float64 // 动态调整的下界
    //   - MaxRPS           float64 // 动态调整的上界
    //   - SaturationProbe  string  // 饱和度探测端点
}
```

**证据 3：Rate limit 不区分操作成本**

search、file upload、message list 等不同成本的操作，被同一个中间件、同一个 `Limiter` 实例限流：

```go
// internal/handler/http/router.go
// 所有 v1 路由共享同一个 rate limiter
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

搜索可能耗 5 秒 DB 时间，而 notification list 只需 1ms，但它们消耗相同的令牌权重。

**证据 4：Rate limit 被 bypass 时没有信号传递给限流器**

当 SSO、DB 或 S3 出现健康问题时（`/readyz` 已经检测到），rate limiter 仍然以满速率放行请求。没有将下游健康状态反馈到限流决策中的机制。

### 架构师视角

静态限流器在负载稳定、依赖健康的情况下工作良好，但在以下场景中会成为系统的薄弱环节：

- **雪崩放大**：SSO 开始变慢 → 请求等待时间增加 → 连接池线程被占满 → 正常请求也开始排队 → 系统进入线程 starvation
- **不公平的资源分配**：搜索这类昂贵操作消耗了大量 DB 资源，但受到与轻量查询相同的限流约束
- **缺乏弹性**：运维人员需要在负载模式变化时手动调整 RPS 配置并重启服务

需要的是一种**2 层限流架构**：

```
Layer 1: 静态基线（当前的 token bucket）
  - 为每个 actor 提供基础保护
  - 在正常操作期间生效

Layer 2: 动态调节器
  - 定期检查下游健康状态（DB P99 延迟、SSO 错误率、连接池饱和度）
  - 当检测到降级时，自动降低全局准入速率（如 RPS x 0.5）
  - 当系统恢复时，逐渐恢复到基线
```

同时需要引入**操作成本加权**——搜索消耗 5 个令牌、列表消耗 1 个令牌等，让限流器更精确地保护系统资源。

### 产品经理视角

- **用户公平性**：一个用户用搜索 API 不应该耗尽所有共享限流配额，影响其他用户的消息发送
- **可靠性 SLO**：自适应限流是多实例部署下防止级联故障的关键能力
- **运维简化**：消除"流量增加→人工修改配置→重启"的运维循环

### 边界情况

- 误判降级：动态调节器需要足够的采样周期和置信度，避免对瞬态抖动作出过度反应
- 恢复试探：降低后的恢复应采用"慢启动"策略（类似 TCP congestion control），逐步增加而不是瞬间恢复
- 操作成本映射：不同版本间操作的成本可能变化，成本表需要定期校准

---

## 方向四：Media Privacy Pipeline — EXIF Stripping & Metadata Sanitization（媒体隐私管线）

### 问题陈述

用户通过 aero-id 上传的图片文件可能包含敏感的 EXIF 元数据（GPS 坐标、相机型号、拍摄时间、用户名字等）。当前的上传管线**不做任何元数据清理**，这些隐私数据会随文件存储和分享，构成隐私泄露风险和法律合规风险（GDPR 对位置数据的处理有明确要求）。

### 代码级证据

**证据 1：文件上传管线在存储前不做任何内容转换**

```go
// internal/file/service_crud.go — Create 方法核心逻辑
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    // ... 前置验证（大小、MIME）...

    hasher := sha256.New()
    reader = io.TeeReader(reader, hasher)

    key, size, err := s.store.Put(ctx, actor, in.Name, reader)
    // 直接将用户上传的原始字节流写入存储后端
    // 没有中间处理步骤（EXIF 剥离、图片重压缩、病毒扫描等）

    checksum := hex.EncodeToString(hasher.Sum(nil))
    // checksum 基于原始文件计算，包含 EXIF 数据
    // ...
}
```

全程只有以下处理：
1. `cappedReader` 限制读取字节数（大小验证）
2. `io.TeeReader` + `sha256` 计算校验和（去重用途）
3. `store.Put` 将原始字节写入存储后端

没有任何图片解码、EXIF 读取、元数据清理的步骤。

**证据 2：File 模型没有元数据清理相关的字段**

```go
// internal/domain/file.go
type File struct {
    // ... 标准字段 ...
    Checksum    string         // 原始文件的 SHA-256
    // 没有:
    //   - StripMetadata bool  // 是否需要清理元数据
    //   - MediaProcessedAt    // 媒体处理完成时间
    //   - DerivativeKeys      // 处理后衍生文件的存储键
}
```

**证据 3：Storage 接口没有提供"处理中"钩子**

```go
// internal/storage/storage.go
type FileStorage interface {
    Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
    NewKey(ownerUID string) string
    PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
    // 没有 MediaPipeline 相关方法
}
```

没有 `Process(ctx, key string, opts ProcessingOptions) (processedKey string, err error)` 类似的扩展点。

**证据 4：MIME 类型 validate 但不影响后续处理**

```go
// internal/file/service_crud.go
func (s *fileService) mimeAllowed(mime string) bool {
    if len(s.allowedMimes) == 0 {
        return true
    }
    for _, a := range s.allowedMimes {
        if strings.EqualFold(mime, a) {
            return true
        }
    }
    return false
}
```

MIME 类型验证通过后，原始字节流被无差别地存入存储。对于 `image/jpeg` 和 `application/pdf`，管线行为完全相同——没有任何格式感知的处理。

### 架构师视角

当前的文件管线是**完全透明的**——字节进去，字节出来。对于文件存储和检索来说这是正确的抽象，但对于一个有隐私合规需求的内容平台来说，这是一个架构缺口。

需要的是在 `Put` 之前或之后插入一个**可选的媒体处理步骤**：

```
Upload → [Validation] → [Media Pipeline] → [Checksum/Store]
                           │
                    ┌──────┴──────┐
                    │              │
               EXIF 剥离      图片重压缩
               （针对图片）    （针对图片）
                    │              │
               OCR 清理         缩略图生成
               （针对 PDF）    （异步）
```

关键设计要点：
- **默认启用**：所有图片类上传默认执行 EXIF 剥离
- **可配置保留**：提供配置项让用户/应用选择是否保留元数据（如摄影社区需要保留相机信息）
- **处理与存储分离**：原始文件 vs 清理后的文件可以分别存储，或者替换存储
- **不影响校验和**：如果 EXIF 剥离发生在 checksum 计算之后，则去重逻辑不受影响

### 产品经理视角

- **法律合规**：GDPR 将地理位置数据视为个人数据。不经用户明确同意就存储和分发 EXIF GPS 数据是不合规的。
- **用户信任**：支持图片分享但默认包含拍摄地点，对用户隐私是重大风险。
- **平台责任**：一旦用户的家庭地址因上传图片的 EXIF 数据泄露，平台可能面临法律责任。
- **差异化**：默认 EXIF 剥离是 Dropbox、Google Photos 等成熟平台已经实现的基础隐私保护，缺失此功能会显得产品不成熟。

建议最小可行范围：
1. 图片格式（JPEG、PNG、WebP、HEIC）默认剥离 EXIF 元数据
2. 提供一个配置选项 `media.strip_metadata: true/false`
3. Processing 钩子设计为可扩展的 pipeline（后续可添加缩略图、OCR、病毒扫描等 stage）

### 边界情况

- 剥离 EXIF 后文件需要重新计算 checksum（如果 checksum 用于去重），需要考虑去重是在清理前还是清理后
- 大图片的解码需要内存限制，防止恶意构造的畸形图片导致 OOM
- 对于 PNG，EXIF 数据在 tEXt/zTXt chunk 中，不能简单 strip——需要正确解析 PNG 结构
- 视频文件一般不包含 EXIF，但可能包含机型和位置信息在容器元数据中（MP4/MOV 的 `©too`、`©xyz` tag）
- 用户明确上传 RAW 格式（CR2、NEF）时，EXIF 数据是文件的有机组成部分，是否剥离需要业务决策

---

## 方向五：Graceful Degradation Contract & Consumer Transparency（优雅降级契约与消费者透明化）

### 问题陈述

aero-id 是一个依赖 SSO、Postgres、以及可选的 S3 存储的服务。虽然 `/readyz` 端点提供了内部健康检查，但**没有任何文档化的降级契约**说明当每个依赖失败时，哪些 API 可用、哪些不可用、以及 API 消费者应该期望什么行为。这使得集成方无法设计可靠的客户端逻辑，也使得运维人员无法做出优雅的降级决策。

### 代码级证据

**证据 1：服务层没有根据依赖健康状况改变行为的逻辑**

```go
// internal/service/service.go — FileService 接口定义
type FileService interface {
    Create(ctx context.Context, in CreateFileInput) (*domain.File, error)
    Get(ctx context.Context, id domain.ID) (*domain.File, error)
    List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error)
    Delete(ctx context.Context, id domain.ID) error
    DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error)
    // ...
}
```

每个方法在执行时假设所有依赖都可用。如果 SSO 宕机（`Get` 仍然需要调用 `GetUsers` 来验证权限），如果 DB 宕机（所有操作都失败），如果 S3 宕机（文件上传和下载都失败）。**没有部分降级逻辑**。

**证据 2：SSO 断路器只在客户端层面工作，不影响业务逻辑的可用性声明**

```go
// internal/sso/breaker.go
// circuit breaker 在 SSO 失败时快速失败 validate 请求
// 但当 SSO 不可用时：
// - 所有 /v1/* 请求失败（auth 中间件无法验证 token）
// - 服务实际上完全不可用
// - 没有声明"在 SSO 不可用时，公共/已认证但缓存有效的请求仍可服务"
```

虽然 `stale-cache` 允许缓存中已有有效 entry 的请求通过验证，但对于缓存中没有的 entry（新 token、首次请求），一旦 SSO 不可用就直接拒绝。

**证据 3：文件服务在 S3 不可用时没有任何后备**

```go
// internal/storage/s3.go — S3 后端
func (s *S3Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
    // 直接调用 S3 API，不做本地 fallback
    // 当 S3 不可用时报错，但不尝试本地副本
}
```

没有本地缓存、没有穿透式回退、没有任何降级行为。

**证据 4：没有消费者的降级通知**

当系统处于降级状态时（如 SSO 部分不可用、S3 高延迟），没有任何机制通知 API 消费者。响应头中没有 `X-Aero-Status: degraded` 或类似指示。

### 架构师视角

优雅降级不是一个可选的增强——它是**平台成熟度的核心标志**。当前的设计假设"所有依赖始终可用"——这在生产环境中永远不成立。

需要建立的是一个**降级契约文档**，系统化地定义：

| 依赖 | 故障场景 | 受影响 API | 降级行为 | 消费者信号 |
|---|---|---|---|---|
| SSO | 不可用 | 所有 `/v1/*` | 已有缓存 token 可继续；新 token 失败 | 401 (新 token) / 正常 (缓存) |
| SSO | 高延迟 | 所有 `/v1/*` | 断路器打开后快速失败新请求 | 429/503 |
| Postgres | 不可用 | 所有需要 DB 的操作 | 只读 API 可能通过只读副本继续；写入 API 全部失败 | 503 |
| Postgres | 连接池满 | 写入 API | 写入失败；读取仍然正常 | 503 (写入) / 正常 (读取) |
| S3 | 不可用 | 文件上传/下载 | 上传失败；下载通过 local 缓存 fallback | 503 (上传) / 正常 (缓存命中) |
| Storage GC | 不可用 | 不直接影响 API | 存储空间可能增长 | 无直接影响 |

这份契约应该：
1. 以文档形式维护（消费者文档的一部分）
2. 在代码中有对应的降级实现
3. 有自动化测试验证每个降级路径

### 产品经理视角

- **服务等级协议（SLA）的基础**：没有降级契约，就无法定义有意义的 SLA——"99.9% 可用性"在 SSO 宕机时是 0% 还是 50%？
- **消费者信任**：当 API 消费者能够在文档中查到"SSO 不可用不影响已认证用户的读操作"，他们对平台的信赖度会大幅提升
- **运维跑书（Runbook）的基础**：降级契约文档是每次故障响应的决策依据

最小可行实现是**创建降级契约文档**（不在代码中），然后逐步将降级行为在代码中实现。第一批代码级的降级可以是：
- 在 `/readyz` 不健康的依赖上，服务仍然处理请求但增加 `Warning` 响应头
- 在 `X-Aero-Status` 头中反映系统健康状态（`healthy` / `degraded` / `unavailable`）

### 边界情况

- **部分降级 vs 完全降级**：某些场景下（如 DB 故障），全部 API 不可用的决策可能比部分降级更安全（防止写入部分成功）
- **降级恢复**：从降级状态恢复需要谨慎——缓存可能已经过时，连接池可能已经饥饿，瞬间恢复可能导致第二次崩溃
- **消费者感知**：降级信号不应该过于频繁地变化，消费者需要稳定的行为预期

---

## 优先级与执行路径

| 方向 | 影响 | 工作量 | 依赖 | 建议优先级 |
|---|---|---|---|---|
| 一：API Consumer Observability | **高** — 影响所有消费者（外部 + 内部 SDK） | **低** — 主要是 header/field 添加，零架构变革 | 无 | **P0** |
| 二：Structured Error Contract | **中-高** — 间接影响所有 API 消费者体验 | **中** — 需要代码迁移 + 文档生成 | 方向一（request_id 回传） | **P1** |
| 三：Adaptive Rate Limiting | **高** — 生产可靠性核心 | **中-高** — 需要新的调节器组件 | 方向一（rate limit 透明化） | **P1** |
| 四：Media Privacy Pipeline | **中** — 合规基础（GDPR 地理位置数据） | **中** — 需要 Media Pipeline 框架 | 无 | **P1**（合规驱动） |
| 五：Graceful Degradation Contract | **高** — 生产就绪的标志 | **中** — 文档先行，代码逐步实现 | 方向一（status header） | **P0** |

### 快速获胜项（1-2 天内可完成）

1. **`X-Request-Id` 响应头回传**：在 `request_id.go` 中间件中添加一行 `c.Header("X-Request-Id", id)`。零风险，立即提升消费者调试体验。
2. **`Content-Disposition` 下载安全**：在 `file.go` 的 `Download` handler 中添加 `c.Header("Content-Disposition", "attachment; filename=\"download\"")` 和 `c.Header("X-Content-Type-Options", "nosniff")`。消除匿名下载路径的潜在 XSS 向量。
3. **`Retry-After` 头在 429 响应中设置**：在 rate limit 中间件中添加 `c.Header("Retry-After", strconv.Itoa(estimatedWaitSeconds))`。让客户端知道何时重试。

### 建议执行顺序

1. **Sprint 1**：方向一（consumer observability）+ 方向五（降级契约文档）
   - 添加 `X-Request-Id`、`X-RateLimit-*`、`X-Aero-Status` 头
   - 编写降级契约文档
2. **Sprint 2**：方向二（错误契约）
   - 建立错误码注册中心
   - 生成错误码文档
3. **Sprint 3-4**：方向四（媒体隐私管线）
   - 设计可扩展的 Media Pipeline 框架
   - 实现 EXIF 剥离 stage
4. **Sprint 4-5**：方向三（自适应限流）
   - 实现动态调节器
   - 引入操作成本加权
