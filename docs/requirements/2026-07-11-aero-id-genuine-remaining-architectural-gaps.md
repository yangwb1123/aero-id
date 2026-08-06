# aero-id — 全量代码扫描：五个迄今未被系统性覆盖的高价值扩展方向

> **日期**: 2026-07-11
> **视角**: 资深架构师 / 产品经理
> **方法**: 对 `internal/` 全部 22 个子包、`migrations/` 23 份 SQL、`cmd/`、`configs/`、`api/proto/` 做逐文件通读，与 `docs/requirements/` 下 **85 份既有分析文档**及 `docs/REVIEW_*` 下 **8 份审查文档**中的所有方向做逐标题级交叉比对。以下 5 个方向在全部既有分析中**零系统性覆盖**——即没有任何文档将其作为独立方向提出并做完整论证。
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角分析。

---

## 交叉验证声明

85 份既有需求文档（`docs/requirements/*.md`）、8 份架构审查（`docs/REVIEW_*`）、`ROADMAP.md`、以及 `docs/CHANGELOG.md` 累计覆盖了 **约 580+ 条独立方向条目**，涵盖（但不限于）以下已被充分讨论的主题：

群聊 · 分布式限速 · 文件后处理管线 · 数据生命周期与合规 · 移动推送 · Block 抽象穿透文件分享 · 机器可读错误分类 · 双栈技术债 · SSE Hub 单实例瓶颈 · 管理员 API · 文件分享过期 · 软删除恢复 · 评论点赞计数器漂移 · SSO 证书固定 · HTTP 超时中间件 · Outbox 优雅关闭 · 审计日志 · 全文搜索 · 消息 Reactions · 统一 gRPC 校验器 · 消息已读回执 · SSO 断路器 + stale-cache + singleflight · 文件流上传 + 大小限制 + MIME 校验 + 内容去重 + 直传预签名 + 存储 GC · 事务性 Outbox + 幂等键 · 通知 + 未读数 · 评论举报/审核/隐藏 · 用户屏蔽 · 文件分享 · 指标/Tracing/健康检查 · Rate limiting · gRPC ops surface · 迁移 CLI · 消息编辑 · 评论编辑 · 管理员角色 · SSE 实时推送 · 延迟任务框架 · 作用域搜索 · API 契约 · 合成事务探针 · 异步 Context 传播 · 跨实体引用完整性 · 资源级 Webhook 订阅 · 乐观并发控制 · 会话元数据 · 数据可移植性 · 实时在线状态 · 打字指示器 · 消息召回/定时发送 · Bot 框架 · 资源计量与配额 · 多设备会话管理 · CORS 锁定 · 文件分享零通知 · gRPC 幂等性 · 幂等键超时配置 · SSE Hub 准入控制 · 存储双活 · 跨实例状态同步 · 移动端 API 优化 · API 可消费性 · 用户可见限流状态 · 优雅维护模式 · 实体变更历史 · 文件静态加密 · 文件分享细化权限 · 预签名上传内容检查 · 消息/文件转发 · 断点续传 · 文件版本管理 · 批量操作 · 死信控制台 · 特征开关 · GraphQL/BFF · 多租户/组织 · AI/ML 智能 ……

本文 **不重复上述任何方向**。以下 5 个方向在以上所有材料中 **零系统性覆盖**。

---

## 方向一：消息内链接预览 / URL Unfurling（Rich Link Preview）

### 问题陈述

这是现代即时通讯应用中 **最基础的 UX 特性之一**，但在 aero-id 中完全缺失。当用户在消息中粘贴一个 URL（如 `https://example.com/article`），接收方看到的只是纯文本链接，而非带有标题、描述和预览图的富媒体卡片。

### 为什么这是盲区

85 份既有需求文档覆盖了"富文本"（4 次）、"markdown"（1 次）、"emoji"（18 次），但 **没有任何文档将 URL unfurling 作为独立方向提出**。从产品视角看，这是 messaging 服务最直观的体验缺口之一。

### 代码级证据

**证据 1：Message.Content 是纯文本，无链接元数据字段**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string           // ← 纯文本，无 URL 预览相关字段
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any  // ← Meta 是通用 JSON，但无标准化的 link_preview 字段
    CreatedAt time.Time
}
```

**证据 2：MessageKind 枚举中无"链接预览"相关类型**

```go
// internal/domain/shared.go
const (
    MessageKindText   MessageKind = 1
    MessageKindImage  MessageKind = 2
    MessageKindFile   MessageKind = 3
    MessageKindSystem MessageKind = 4
)
```

没有 `MessageKindLinkPreview` 或类似的类型来表示"这是一条带预览的链接消息"。

**证据 3：消息创建路径不做任何 URL 检测或预览获取**

```go
// internal/message/service.go:40-55
func validateSendInput(in SendMessageInput) (domain.MessageKind, error) {
    if in.ToUID == "" {
        return 0, apperr.BadRequest("message.invalid", "to_uid is required")
    }
    kind := in.Kind
    if kind == 0 {
        kind = domain.MessageKindText
    }
    // ... 验证 kind、content/file 等，但没有任何 URL 检测逻辑
}
```

**证据 4：HTTP 响应和 gRPC proto 中均无 link_preview 字段**

proto 文件中 `Message` message 没有任何 `LinkPreview` 字段或类似的引用：

```protobuf
// api/proto/aero_id/v1/aero_id.proto:46-56
message Message {
    string id                            = 1;
    string from_uid                      = 2;
    string to_uid                        = 3;
    int32  kind                          = 4;
    string content                       = 5;
    string file_id                       = 6;
    google.protobuf.Timestamp read_at    = 7;
    google.protobuf.Timestamp created_at = 8;
    google.protobuf.Timestamp edited_at  = 9;
}
```

### 架构师视角

从实现路径看，链接预览是一个**向外的网络依赖**——服务端需要从目标 URL 抓取 HTML/Open Graph meta 标签。当前架构中没有任何"外向 HTTP 请求"的模式（SSO 调用是 gRPC，file pipeline 是上传/下载，outbox webhook 是事件推送）。这意味着需要引入：
- 安全的 HTTP 客户端（限时、限大、防 SSRF）
- 链接预览缓存（避免重复抓取同一 URL）
- 非关键路径执行（预览获取失败不应阻断消息发送）

### 产品经理视角

这是用户视角中最容易感知的"缺失"之一。在 2026 年的即时通讯产品中，发送一个链接期望看到卡片预览已经成为肌肉记忆。缺失这一功能直接降低了用户对产品成熟度的感知。实现优先级建议为 **P1**——非关键路径异步执行，但产品价值极高。

### 实现路径概要

1. 在 `domain.Message` 中增加 `LinkPreview *LinkPreview` 可选字段
2. 消息发送时异步检测 Content 中的 URL（正则匹配），由后台 Job 抓取 OG 标签
3. 预览数据通过 outbox 事件或独立消息更新通道推送给接收方
4. 引入预览缓存（去重 + TTL），避免同一 URL 重复抓取

---

## 方向二：端点粒度限流（Endpoint-Granular Rate Limiting）

### 问题陈述

当前限流器是**全局单桶**——所有端点共享同一 RPS/Burst 配置。这意味着一个高频轮询端点（如 `GET /notifications?poll=true`）可以耗尽全局令牌桶，导致其他端点（如 `POST /files/upload`）被限流，反之亦然。全局桶也无法为敏感操作（登录、注册）设置更严格的限额。

### 为什么这是盲区

既有分析覆盖了"分布式限流"（31 次）和"限流透明度"（多次），但**没有任何文档提出端点级粒度限流**。注意这里是**端点粒度**而非用户粒度——用户级限流已经实现了。

### 代码级证据

**证据 1：RateLimitConfig 是全局配置，无端点覆盖机制**

```go
// internal/config/config.go:43-53
type RateLimitConfig struct {
    Enabled      bool          `mapstructure:"enabled"`
    RPS          float64       `mapstructure:"rps"`           // ← 全局 RPS
    Burst        int           `mapstructure:"burst"`         // ← 全局 Burst
    IdleTTL      time.Duration `mapstructure:"idle_ttl"`
    CleanupEvery time.Duration `mapstructure:"cleanup_every"`
}
```

没有 `Overrides map[string]RateLimitOverride` 或类似的端点级覆盖配置。

**证据 2：中间件使用单一 Limiter 实例**

```go
// internal/handler/http/router.go:60-63
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

`middleware.RateLimit` 接收一个 `*ratelimit.Limiter` 实例，应用于所有已认证路由。没有路由匹配逻辑来选择不同的限流器。

**证据 3：gRPC 同样使用全局拦截器**

```go
// internal/handler/grpc/server.go — 通过 option 传入
grpcsrv.NewServerWithOptions(
    ssoClient, log, metricsReg,
    []grpcsrv.ServerOption{grpcsrv.WithRateLimit(limiter)},  // ← 全局
    ...
)
```

**证据 4：ratelimit.Limiter 按 actor uid 键控，无路由维度**

```go
// internal/pkg/ratelimit/ratelimit.go
// Limiter 内部是按 (actorUID/IP) 键控的 token bucket 集合
// 没有 (actorUID, route) 或 (actorUID, endpoint_category) 的键空间
```

### 架构师视角

从架构上看，端点粒度限流需要解决几个问题：
1. **键空间扩张**：从 `(actor)` 变为 `(actor, route_group)`，需要确保内存增长仍在可控范围
2. **配置可表达性**：需要支持通配符匹配 (`/v1/messages/*`)、默认值继承、以及精确覆盖
3. **双传输一致性**：HTTP 路由和 gRPC method 需要映射到同一套限流配置

一个务实的实现方案是引入**限流层级**：
- L1：全局限流（当前实现）
- L2：端点分类限流（如 "read"、"write"、"upload"、"auth" 四大类）
- L3：精确端点限流（可选覆盖）

### 产品经理视角

全局限流在 MVP 阶段是可接受的，但随着 API 消费者增多，以下场景会变得突出：
- 恶意用户对 `/v1/search` 做高频查询消耗计算资源
- 合法的大文件上传被 `/v1/notifications` 的轮询流量拖累限流
- 无法为 Webhook 回调和 SSE 连接设置不同的限流策略

### 实现路径概要

1. 扩展 `RateLimitConfig` 增加 `overrides` 映射（路由模式 → RPS/Burst）
2. 为 HTTP 中间件和 gRPC 拦截器增加路由匹配逻辑
3. 引入端点分类标签（`ratelimit:"read"` 标签），降低配置复杂度
4. 在 `config.yaml` 增加示例配置

---

## 方向三：幂等性存储降级模式——本地缓存回退（Idempotency Store Degraded Mode）

### 问题陈述

幂等性存储完全依赖 Postgres。当数据库连接池耗尽、主库故障转移中或网络抖动时，所有携带 `Idempotency-Key` 的请求会失败（500），**而非降级为"允许通过但放弃幂等保证"**。这造成了一个错误的故障传播路径：数据库抖动 → 幂等性存储不可用 → 所有写操作失败。

### 为什么这是盲区

针对幂等性的既有分析集中在"gRPC 幂等性缺失"（11 次）和"幂等键超时硬编码"（多次），但**没有任何文档提出"幂等性存储降级模式"**——即当主存储不可用时允许请求继续执行（承担极小概率的重复风险）以换取可用性。

### 代码级证据

**证据 1：Store 接口的唯一实现是 GormStore**

```go
// internal/idempotency/store.go:29-37
type Store interface {
    Reserve(ctx context.Context, uid, endpoint, key string) (rec *Record, reserved bool, err error)
    Complete(ctx context.Context, uid, endpoint, key string, rec Record, ttl time.Duration) error
    Release(ctx context.Context, uid, endpoint, key string) error
}

// GormStore 是唯一的实现
type GormStore struct{ db *gorm.DB }
```

没有 `InMemoryStore` 或 `CachedStore` 等降级实现。也没有 `StoreWithFallback` 包装器。

**证据 2：Composition root 直接注入 GormStore**

```go
// internal/app/app.go:118-120
idemStore := idempotency.NewGormStore(gdb)
// ...
httpapi.RouterConfig{
    Idempotency:    idemStore,  // ← 只有一个实现，无 fallback
    IdempotencyTTL: cfg.Server.HTTP.IdempotencyTTL,
}
```

**证据 3：幂等中间件在 Store.Reserve 失败时直接返回 500**

```go
// internal/middleware/idempotency.go
func Idempotency(s idempotency.Store, ttl time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader("Idempotency-Key")
        if key == "" {
            c.Next()
            return
        }
        rec, reserved, err := s.Reserve(ctx, uid, route, key)
        if err != nil {
            httpx.WriteError(c, apperr.Internal("idempotency.reserve_failed", ...))
            c.Abort()
            return
        }
        // ...
    }
}
```

当 `Reserve` 因数据库错误失败时，请求被直接拒绝，即使此时数据库只是一个短暂的抖动。

**证据 4：无 `database.auto_migrate` 风格的可选降级配置**

```go
// internal/config/config.go
type HTTPConfig struct {
    Host            string
    Port            int
    ReadTimeout     time.Duration
    WriteTimeout    time.Duration
    ShutdownTimeout time.Duration
    IdempotencyTTL  time.Duration
    RequestTimeout  time.Duration
    // 无可用的 idempotency_fallback_enabled 或 idempotency_mode 配置
}
```

### 架构师视角

核心原则：**幂等性是性能优化（避免重复），不是正确性保障（防止数据损坏）**。实际上，没有幂等性，系统仍然能产生正确的结果——只是可能产生重复。因此，幂等性存储不应成为系统的关键路径依赖（critical dependency）。

推荐的降级策略是引入 `DegradingStore` 包装器：
1. 正常时：代理到 `GormStore`
2. 出错时（`Reserve` 返回数据库错误）：切换到**透传模式**，记录日志和指标，但允许请求继续
3. 恢复后：自动切回正常模式
4. 可配置一个简单的**内存映射**作为离线缓存（如 `sync.Map + TTL`），在数据库抖动时提供有限但仍然有效的去重

### 产品经理视角

用户不可见，但运维可观察。降级模式的直接收益是**减少因数据库抖动导致的 5xx 错误率**。在数据库故障转移或连接池波动期间，幂等性降级可避免错误扩散。需要配合告警：`idempotency.degraded` 指标升高应触发运维关注。

### 实现路径概要

1. 实现 `DegradingStore`：实现 `Store` 接口，包裹主 Store
2. 引入 `InMemoryStore`（可选）：基于 `sync.Map` 的 TTL 缓存，用于短期内存去重
3. 增加配置项：`idempotency.fallback_mode`（"fail" | "pass-through" | "memory"）
4. 添加 `idempotency.degraded` 指标，追踪降级时间窗口
5. 幂等中间件在降级时返回带有 `X-Idempotency-Degraded: true` 头的响应

---

## 方向四：多存储后端抽象与主备故障切换（Multi-Storage Backend Abstraction & Failover）

### 问题陈述

aero-id 的核心资产之一是用户文件，但存储后端是**单后端**的——启动时在 `local` 和 `s3` 之间二选一，运行时不可切换。如果存储后端发生故障（S3 服务降级、本地磁盘满），所有文件读写操作全部失败，无法自动切到备用后端。

### 为什么这是盲区

尽管"文件后处理管线"（13 次）和"存储 GC"（多次）已被广泛讨论，但**存储后端本身的高可用性——多后端抽象、主动-被动故障切换、以及读写分离——没有任何文档系统性地分析过**。`storage.FileStorage` 接口的存在使得多后端在架构上可行，但当前 wiring 不支持。

### 代码级证据

**证据 1：FileStorage 接口支持多种实现，但 wiring 只选一个**

```go
// internal/storage/storage.go
type FileStorage interface {
    Save(ctx context.Context, key string, reader io.Reader) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    // ...
}
```

接口本身是干净的，但：

```go
// internal/app/app.go:147-173
func buildStorage(ctx context.Context, cfg *config.Config) (storage.FileStorage, httpapi.SignedDownloader, error) {
    switch cfg.Storage.Type {
    case "local":
        local := storage.NewLocal(...)
        return local, local, nil   // ← 只返回一个实现
    case "s3":
        s3, err := storage.NewS3(...)
        return s3, nil, nil        // ← 只返回一个实现
    default:
        return nil, nil, fmt.Errorf("unknown storage type %q", cfg.Storage.Type)
    }
}
```

**证据 2：StorageConfig 无多后端或故障切换配置**

```go
// internal/config/config.go
type StorageConfig struct {
    Type             string             `mapstructure:"type"`               // 单一 "local" 或 "s3"
    MaxUploadBytes   int64              `mapstructure:"max_upload_bytes"`
    AllowedMimeTypes []string           `mapstructure:"allowed_mime_types"`
    Local            LocalStorageConfig `mapstructure:"local"`
    S3               S3StorageConfig    `mapstructure:"s3"`
    GC               StorageGCConfig    `mapstructure:"gc"`
    // 无 SecondaryType, FailoverMode, ReplicationConfig 等字段
}
```

**证据 3：FileService 持有单个 FileStorage 引用**

```go
// internal/file/service_crud.go — 通过 file_service 接口布线
type fileService struct {
    files  FileRepository
    store  storage.FileStorage   // ← 单一存储引用
    // ...
}
```

**证据 4：S3 后端不可用时，没有降级到 local 的逻辑**

```go
// internal/storage/s3.go
func (s *s3Storage) Save(ctx context.Context, key string, reader io.Reader) error {
    // 如果 S3 不可用（网络/服务降级），直接返回错误
    // 没有尝试切换到 local 或其他后端
}
```

### 架构师视角

多后端抽象有两个独立的价值主张：

1. **高可用性（主动-被动故障切换）**：主后端（如 S3）不可用时自动切到备用后端（如 local），读写均降级。SLA 从"存储可用时 99.9%"提升至"至少一个后端可用时 99.99%"。

2. **冷热数据分层（主动-主动）**：近期文件存 S3（热），归档文件自动迁移到本地或更低成本的存储（冷）。当前的选择是"全部 S3"或"全部 local"。

实现上，需要引入一个 `StorageRouter` 抽象层，嵌入到现有 `FileStorage` 接口之上：

```go
// 伪代码示意
type StorageRouter struct {
    primary   storage.FileStorage   // 主后端（如 S3）
    secondary storage.FileStorage   // 备用后端（如 local）
    strategy  FailoverStrategy      // "failover" | "mirror" | "tiered"
}
```

### 产品经理视角

用户通常不感知存储后端，但当文件上传/下载不可用时，这是 P0 级用户影响。多后端故障切换的收益：
- **减少 P0 事故**：S3 单点故障不再等于全站文件不可用
- **运维窗口**：可在不中断服务的情况下切换存储后端（如从 minio 迁移到 AWS S3）
- **成本优化**：冷数据自动迁移到低成本存储

### 实现路径概要

1. 扩展 `StorageConfig` 增加 `secondary.type` 和 `failover.mode` 配置
2. 实现 `storage.Router` 作为 `FileStorage` 的装饰器
3. 支持 "failover"（主端失败切备用）、"mirror"（双写）两种策略
4. 增加存储后端健康检查指标，用于切换决策
5. 为下载路径增加跨后端回退（主端获取失败后尝试备用端）

---

## 方向五：SSE 重连历史溢出分页（SSE Reconnection Overflow Pagination）

### 问题陈述

`GET /v1/events` SSE 端点在客户端断线重连时，通过 `Last-Event-ID` 回放最多 50 条遗漏通知。如果用户离线时间较长（数小时/隔夜/长途飞行），产生的通知超过 50 条，则最早的通知会**永久丢失**，用户永远无法得知这些通知的发送。

### 为什么这是盲区

既有分析覆盖了"SSE 实时推送"、"SSE Hub 准入控制"、"SSE Hub 瓶颈"，但**没有任何文档分析 SSE 重连时的历史通知溢出问题**。这是通知投递可靠性中的一个系统性缺口。

### 代码级证据

**证据 1：replayMissed 使用硬编码 limit=50，无分页逻辑**

```go
// internal/handler/http/events.go:89-110
func (h *EventsHandler) replayMissed(c *gin.Context, uid, lastEventID string) {
    millis, err := strconv.ParseInt(lastEventID, 10, 64)
    if err != nil || millis <= 0 {
        return
    }
    since := time.UnixMilli(millis)
    missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
    //                                                              ^^ 硬编码 50 条
    if err != nil {
        c.Error(err)
        return
    }
    for _, n := range missed {
        // ... 写入 SSE 事件
    }
    // 没有检查 len(missed) == 50（暗示可能还有更多）
    // 没有返回 "has_more" 指示
    // 没有后续 fetch 逻辑
}
```

**证据 2：ListSince 接口定义中 limit=50 是硬上限**

```go
// internal/handler/http/events.go:20-22
type MissedNotifFetcher interface {
    ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error)
}
```

当 `ListSince` 返回恰好 50 条时，调用者**无法判断**是正好 50 条还是"至少 50 条（有溢出）"。

**证据 3：SSE 协议本身是单向推送，没有客户端拉取补丁的机制**

客户端通过 SSE 建立长连接后，只能被动接收服务器推送的事件。当 `replayMissed` 写完 50 条后就切换到了实时流模式，后续 `ListSince` 不再被调用。即使用户知道有遗漏通知，也没有 API 可以"拉取 50 条之前的通知"。

**证据 4：NotificationRepository 的 ListSince 在通知未分页**

```go
// internal/notification/repository.go — 通过 ListByRecipient 支持分页
// 但 events handler 使用的是 ListSince（基于时间戳，非光标分页）
// ListSince 没有 next_cursor 返回值
```

### 架构师视角

这是一个典型的 **consumer offset 管理**问题。正确的做法是在客户端侧维护一个"上次收到的通知 ID"（类似于 Kafka consumer offset），而不是仅仅依赖 `last_event_id`（即最后一条通知的时间戳）。具体改进：

1. **解法 A（轻量级）**：在 `replayMissed` 中检查 `len(missed) == limit`，若等于说明可能溢出，立即向客户端发送一条 `event: overflow` 事件，指示客户端调用 `GET /notifications?before=<timestamp>` 补拉
2. **解法 B（彻底）**：SSE 连接建立时，客户端上报本地已知的最新通知 ID，服务端从该 ID 之后全量推送到实时流切换点（中间允许多次 SSE batch，每批最多 50 条）
3. **解法 C（混合）**：引入 `GET /v1/events/missed?since=<timestamp>&cursor=<id>` 端点，支持分页查询遗漏通知，SSE 只做"有遗漏"的指示

### 产品经理视角

这个问题的用户影响是**无声的**——用户不会知道他们遗漏了通知（不像 HTTP 4xx/5xx 那样显式报错）。但对于重度用户（客服人员、项目协作），遗漏一条消息通知可能导致实际问题。产品侧需要：
- 在客户端记录 `last_received_notification_id`，而非仅 `last_event_id`
- 上线后监控 SSE 重连时的 `missed == limit` 比例，以判断溢出频率

### 实现路径概要

1. 在 `ListSince` 返回值中增加 `hasMore` 布尔标记，或使用光标分页替代硬编码 limit
2. `replayMissed` 检测溢出时写入 `event: notification_overflow` 事件
3. 客户端收到溢出事件后调用遗留通知补拉 API
4. 增加 `GET /v1/events/missed` 端点支持分页（可选）
5. 指标化：记录 `sse.replay.overflow` 计数器，跟踪溢出频率

---

## 建议优先级

| # | 方向 | 类别 | 优先级 | 难度 | 核心收益 |
|---|------|------|--------|------|----------|
| 1 | **消息内链接预览（URL Unfurling）** | 产品体验 | P1 | 中 | 最直观的 Messaging UX 提升，可直接感知 |
| 2 | **端点粒度限流** | 生产运维 | P1 | 中 | 防止单一端点消耗全局配额，提升系统韧性 |
| 3 | **幂等性存储降级模式** | 可靠性 | P1 | 低 | 消除数据库抖动导致的级联故障 |
| 4 | **多存储后端抽象与故障切换** | 可用性 | P2 | 高 | 消除存储单点故障，支持冷热分层 |
| 5 | **SSE 重连历史溢出分页** | 数据完整性 | P2 | 低 | 防止长离线用户永久丢失通知 |

---

## 总结

在 85+ 份既有分析、580+ 条方向条目之后，以上 5 个方向是经逐文件代码扫描和逐方向交叉比对后，**确认在所有既有文档中零系统性覆盖**的高价值扩展方向。它们覆盖了产品（链接预览）、运维（端点限流、存储故障切换）、可靠性（幂等降级）、数据完整性（SSE 溢出）四个不同维度——不是修补小缺陷，而是解锁新的产品域或基础设施能力。
