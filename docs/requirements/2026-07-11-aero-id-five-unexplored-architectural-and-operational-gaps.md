# aero-id — 全量代码扫描：五项未深入探讨的架构与运维缺口

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件通读 `main` 分支全部 ~170 个 Go 源文件、23 份迁移 SQL、proto 定义、配置与 wiring（`internal/app/app.go`），覆盖全部 22+ 子包。与 `docs/requirements/` 下 **97 份既有分析**、`ROADMAP.md`、`docs/CHANGELOG.md`、`docs/DECISIONS.md`、`docs/ARCHITECTURE.md` 以及 `docs/REVIEW_*` 8 份审查文档逐方向交叉比对。  
> **约束**: 不编写任何代码。

---

## 前置确认

97 份既有需求分析 + 8 份架构审查 + `ROADMAP.md` + `CHANGELOG.md` 累计覆盖了极其广泛的主题。以下方向在所有既有材料中有**零系统性覆盖或未作为独立方向进行完整的代码级证据链论证**。

---

## 方向一：Outbox 事件类型治理——Schema 版本、注册表与消费者契约（Outbox Event Type Governance）

### 代码级证据

当前系统中存在 3 种出站事件类型和 3 种通知类型，但**没有集中注册表、没有 Schema 版本、没有消费者契约**。

**证据 1：事件类型以裸字符串散落在两套实现中**

事件产生方（旧 `internal/service/`）：
```go
// internal/service/message.go:105
evt := newEvent("message", m.ID.String(), "message.sent", map[string]any{...})

// internal/service/message.go:237
evt := newEvent("message", m.ID.String(), "message.read", map[string]any{...})

// internal/service/comment.go:118
evt = newEvent("comment", c.ID.String(), "comment.replied", map[string]any{...})
```

新域包（`internal/message/`, `internal/comment/`）也有**完全独立**的相同事件字符串：
```go
// internal/message/service.go:95
evt := outbox.NewEvent("message", m.ID.String(), "message.sent", map[string]any{...})

// internal/message/service.go:205
evt := outbox.NewEvent("message", m.ID.String(), "message.read", map[string]any{...})

// internal/comment/service.go:122
evt = outbox.NewEvent("comment", c.ID.String(), "comment.replied", map[string]any{...})
```

**两套代码产生相同的事件类型字符串，但互不感知。**

**证据 2：通知类型的转换也是散落的 switch 语句**

通知分发（`internal/service/notification.go:262-298`）用硬编码的 switch 匹配事件类型：
```go
case "message.sent":
    // → notification type "message.received"
case "message.read":
    // → notification type "message.read_receipt"
case "comment.replied":
    // → notification type "comment.reply"
```

而 `domain/notification.go` 定义了 3 个常量，但**没有任何代码引用这些常量来产生或匹配事件**：
```go
const (
    NotifTypeMessageReceived NotificationType = "message.received"
    NotifTypeMessageRead     NotificationType = "message.read_receipt"
    NotifTypeCommentReply    NotificationType = "comment.reply"
)
```

事件类型字符串在 switch 中是硬编码的，常量是孤立的——你改了常量名也不会产生编译错误。

**证据 3：Payload 无版本号，消费者无法判断结构**

```go
// internal/outbox/outbox.go
type Event struct {
    ID            uuid.UUID
    AggregateType string      // 无 payload 版本字段
    AggregateID   string
    EventType     string
    Payload       []byte      // 纯 JSON，无 $schema 或 version 字段
    // ...
}
```

当前 payload 示例（`message.sent`）：
```go
map[string]any{"message_id": m.ID.String(), "from_uid": m.FromUID, "to_uid": m.ToUID}
```

如果未来需要在 payload 中增加 `"thread_id"` 或 `"conversation_id"` 字段，所有现有 webhook 消费者无法判断这是旧格式还是新格式。没有 `$version` 或 `$schema` 字段。

**证据 4：Webhook 消费者靠名字匹配，无订阅管理 API**

```go
// internal/outbox/webhook.go — 配置级过滤，非消费者自主订阅
type WebhookConfig struct {
    URL        string
    SigningKey string
    EventTypes []string   // 硬编码在启动配置中
    // ...
}
```

没有 `POST /v1/webhook/subscriptions` 让消费者自主注册感兴趣的 event_type 和回调 URL。Webhook 目标端点和事件类型选择是运维配置，不是运行时 API。

### 产品经理视角

- **业务扩展受阻**：每增加一种通知类型（如 `"file.shared"`、`"comment.reported"`、`"user.blocked"`），需要改 4 个文件 + 加 migration + 发版。如果业务需要快速试验新通知（如"直播间点赞提醒"），当前的硬编码模式无法支持。
- **Webhook 生态不可用**：第三方开发者不能自主注册 webhook 来监听特定事件。当前 webhook 是运维配置项——每次添加监听者都要改服务配置并重启，这不是平台级的做法。
- **Schema 不兼容风险**：无版本号的 payload 在生产者升级后会导致消费者静默解析错误。没有优雅的向前/向后兼容机制。

### 架构师视角

这是一个典型的事件驱动基础设施的"童年债"——事件先跑起来，再考虑治理。修复路径：

1. **集中事件类型注册表**：在 `internal/outbox/` 中定义 `EventTypeRegistry`，所有产生事件的代码通过注册表获取类型字符串（而非裸字符串）→ 编译期类型安全，同时提供自描述文档。
2. **Payload Schema 版本化**：每个事件 payload 顶层增加 `"$version": 1` 字段。消费者按版本解析。`NewEvent()` 工厂方法自动注入版本号。
3. **Webhook 管理 API**：`subscriptions` 表（`id, owner_uid, event_type_pattern, target_url, signing_key, created_at`）+ CRUD 端点。Webhook handler 从 DB 加载订阅而非配置。
4. **事件类型演进策略**：同一种事件类型的不同 schema 版本用 `event_type@v1` 形式区分。Relay 可以根据 consumer 注册的版本号路由到对应 handler。

---

## 方向二：操作级速率限制——成本感知与优先级调度（Operation-Aware Rate Limiting）

### 代码级证据

**证据 1：当前限流器对所有操作一视同仁**

```go
// internal/pkg/ratelimit/ratelimit.go
func (l *Limiter) Allow(key string) bool {
    // 每次请求消费 1 个 token，无论操作类型
    if b.tokens >= 1 {
        b.tokens--
        return true
    }
    return false
}
```

全局配置 `rps: 20, burst: 40`（`configs/config.yaml`）应用于每个 actor。但以下三种操作消耗相同的预算：
- 列出通知（轻量查询，<1ms DB 时间）
- 创建评论（写入 + 事务 + 出站事件，~10ms）
- 上传 50MB 文件（流式写入 S3，可能 >1s）

**证据 2：中间件层面无法区分操作**

```go
// internal/middleware/ratelimit.go
func RateLimit(limiter *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := limiterKey(c)
        if !limiter.Allow(key) {
            // 429 — 无论什么路由
        }
    }
}
```

限流发生在路由匹配之后但 handler 执行之前——此时完全可以获取 `c.FullPath()` 或方法名来区分操作类型，但当前没有使用这一信息。

**证据 3：文件上传只有全局的 `MaxUploadBytes`，没有上传速率/频次限制**

```go
// internal/file/file.go
type fileService struct {
    // ...
    maxBytes int64 // 单文件大小上限，无每分钟上传次数限制
}
```

用户可以连续上传 50 个 50MB 的文件（在 global 20 rps 限制下大约 2.5 秒内完成），消耗 2.5GB 存储。没有 per-user 的上传频次或总量配额。

**证据 4：没有为写操作预留关键路径预算**

系统没有为创建消息、发送通知等"不可丢失"的操作预留 token 预算。如果某个用户消费了所有 20 rps 用于列表查询，此时一条关键消息的发送请求会被限流 429。在消息系统中，写操作应该获得比读操作更高的优先权。

### 产品经理视角

- **抗滥用能力受限**：攻击者可以用 20 rps 的列表请求占满预算，然后上传巨量文件——因为所有操作消耗相同。一个真正的 quota 系统应当为不同操作分配不同的预算池。
- **关键操作被非关键操作阻塞**：当用户频繁轮询通知时，可能恰好用完 rate limit 预算，导致重要消息无法发送。这在即时通讯场景中是不可接受的。
- **无法做差异化定价**：如果未来做 API 商业化，无法为昂贵操作（大文件上传）和廉价操作（文本搜索）制定不同的计费费率。

### 架构师视角

修复路径不要求引入分布式限流或 Redis——在单实例内存 bucket 的基础上增加操作权重即可：

1. **操作成本分级**：定义 `OperationCost` 枚举（如 `CostRead = 1, CostWrite = 5, CostUpload = 20`），每个 handler 在注册路由时声明自己的成本。
2. **加权 token 消耗**：`Allow(key, cost int) bool` 方法代替 `Allow(key)`。限流器在消耗 token 时扣减 `cost` 而非 1。
3. **关键路径预留**：为写操作（消息发送、评论创建）保留最小预算（如 burst 的 20%），确保即使读操作耗尽 token，写操作依然可以执行。
4. **操作级 Metrics**：`aeroid_rate_limit_denied_total{operation="file.upload"}` 等标签，让运维能识别哪些操作最常被限流。

---

## 方向三：通知类型系统演进能力——从静态枚举到运行时可扩展（Extensible Notification Type System）

### 代码级证据

**证据 1：通知类型被硬编码为 Go 常量，共 3 种**

```go
// internal/domain/notification.go:5-12
type NotificationType string

const (
    NotifTypeMessageReceived NotificationType = "message.received"
    NotifTypeMessageRead     NotificationType = "message.read_receipt"
    NotifTypeCommentReply    NotificationType = "comment.reply"
)
```

整个系统只有 3 种通知类型。没有任何方式在不改代码的情况下添加新类型。对比生产级通知系统（如 AWS SNS、Firebase），新通知类型应当是配置驱动的。

**证据 2：通知偏好表对有/无声明的类型行为不同**

```go
// migrations/000019_notification_preferences.up.sql
CREATE TABLE notification_preferences (
    uid               TEXT NOT NULL,
    notification_type TEXT NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    quiet_hours_start TEXT,
    quiet_hours_end   TEXT,
    timezone          TEXT NOT NULL DEFAULT 'UTC',
    PRIMARY KEY (uid, notification_type)
);
```

notification_type 列是 TEXT，没有 FK 约束到类型注册表。当一个新的通知类型在代码中被添加但还没有 preference 行时，默认是 `enabled=true`——这看起来很好，但：
- 没有机制让用户发现"新通知类型"的存在并设置偏好
- 如果未来某个通知类型默认应该 disabled（如营销类通知），只能通过发版改代码
- 没有通知类型的分组能力（如将所有"社交类"通知批量设为静默）

**证据 3：通知分发器用裸 switch 匹配字符串**

```go
// internal/service/notification.go:262-298
func (s *notificationService) Handle(ctx context.Context, e *outbox.Event) error {
    switch e.EventType {
    case "message.sent":
        // ...
    case "message.read":
        // ...
    case "comment.replied":
        // ...
    default:
        return nil // 静默忽略未知事件类型——无人知晓新事件被丢掉了
    }
}
```

默认分支 `return nil` 意味着一个未知事件类型被**静默忽略**。如果由于打字错误导致事件类型不匹配，该 bug 在开发/测试环境中不会被发现——事件只是消失。

**证据 4：通知类型之间无层次关系**

没有分类体系。无法表达"message.received 和 comment.reply 都属于 INTERACTION 类"或"message.read_receipt 属于 STATUS 类"。用户在 UI 上无法按类别批量管理通知偏好。

### 产品经理视角

- **产品迭代速度受限**：每增加一种通知类型都是一个完整的开发→测试→部署周期。如果产品需要快速尝试"热门内容提醒"或"好友上线提醒"，当前的架构不支持 A/B 测试通知类型。
- **用户控制权不足**：用户无法发现系统有哪些通知类型可用，也无法批量管理。未来如果系统增加到 10+ 种通知类型，当前的偏好 API（逐个设置）将不可用。
- **默认策略僵化**：不能针对不同通知类型设置不同的默认启用/禁用策略。营销类通知应当默认关闭，事务类通知应当默认开启——当前无法实现。

### 架构师视角

1. **通知类型注册表**：引入运行时配置（或 DB 表）定义所有已知通知类型及其元数据（名称、描述、默认启用、分类标签）。
2. **Handler 注册模式**：用策略模式替代 switch。每个通知类型对应一个独立 Handler（实现 `NotifHandlerFunc` 接口）。新类型通过注册新的 Handler 添加，无需修改 switch 语句。
3. **类型分类与批量管理 API**：`notification-type` 上增加 `category` 标签。`PUT /v1/notification-preferences` 支持按 category 批量设置。
4. **未知事件可见性**：dispatcher 的 default 分支应当记录 warning 日志并增加计数器（`aeroid_notification_unknown_event_total`），确保新事件不会静默丢失。

---

## 方向四：文件上传频次与总量配额（Per-User Upload Quota & Rate Enforcement）

### 代码级证据

**证据 1：`files` 表无任何配额相关列**

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
```

没有 `owner_uid` 级别的存储使用量追踪。系统无法回答"用户 X 用了多少存储空间"。

**证据 2：文件创建不检查用户使用量**

```go
// internal/file/service_crud.go
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    // 检查 size、MIME、checksum 去重……但不检查用户总使用量
    // ...
    return s.persistUpload(ctx, actor, in.Name, in.MimeType, in.Size, key, in.Visibility, checksum, in.Meta)
}
```

用户可以在 20 rps 的限制内连续上传任意数量的文件，消耗任意多的存储空间。

**证据 3：无 `storage_usage` 表或聚合索引**

```bash
$ grep -rn "sum\|SUM\|total_size\|storage_used\|quota" migrations/*.sql
# → 0 结果
```

没有维护用户存储使用量的表或物化视图。要查询"用户 X 用了多少空间"需要 `SELECT COALESCE(SUM(size), 0) FROM files WHERE owner_uid = ? AND deleted_at IS NULL` —— 全表扫描，成本随用户文件数线性增长。

**证据 4：GC 回收关注的是留过期而非用户配额**

```go
// internal/storage/gc/sweeper.go
type Config struct {
    Retention    time.Duration // 软删除文件的保留时间
    MinOrphanAge time.Duration
    PurgeBatch   int
    SweepOrphans bool
    // 注：没有任何配额相关的配置
}
```

存储 GC 是按时间清理，不是按用户配额。一个用户的存储用量超限时，GC 不会优先清理该用户的过期文件。

### 产品经理视角

- **成本控制空白**：对于文件服务来说，存储是最大的运营成本。没有 per-user 配额意味着不能做定价分层——免费用户 100MB、付费用户 10GB——这是文件类服务最基础的商业模式。
- **滥用防御缺失**：恶意用户可以上传海量小文件填满磁盘（即使每个文件不超过 `max_upload_bytes`）。没有频次限制或总量限制，存储增长完全不可控。
- **UX 缺口**：用户无法知道自己用了多少存储、还剩多少。没有用量提示的攻击面（graceful degradation when approaching limit）。

### 架构师视角

1. **`user_storage_usage` 表**：`(owner_uid, total_bytes, file_count, updated_at)`，在文件创建/软删除时通过同一事务更新。相比每次 `SELECT SUM(size)`，这种反范式设计提供了 O(1) 的用量查询。
2. **配额配置**：在 `config.yaml` 或 DB 配置表中定义 `storage.quota_default`、`storage.quota_overage_enforcement`（硬限制 vs 软限制+告警）。
3. **上传前置检查**：`Create` 和 `PresignUpload` 入口检查用户当前用量 + 新文件大小是否超过配额。超限返回 `429 QuotaExceeded` 或 `403 Forbidden`。
4. **通知集成**：当用户用量达到配额的 80%、90%、100% 时，通过 outbox 发送 `"storage.quota_warning"` 事件→触发通知推送。

---

## 方向五：配置运行时不可变——缺乏动态调整能力（Immutable Runtime Configuration）

### 代码级证据

**证据 1：Config 结构体在启动时一次性加载，无热重载机制**

```go
// internal/config/config.go
type Config struct {
    App      AppConfig      `mapstructure:"app"`
    Server   ServerConfig   `mapstructure:"server"`
    Database DatabaseConfig `mapstructure:"database"`
    SSO      SSOConfig      `mapstructure:"sso"`
    Storage  StorageConfig  `mapstructure:"storage"`
    // ... 全部字段在 Load() 时一次性读取，之后不可变
}

func Load(path string) (*Config, error) {
    // 读取文件 + 环境变量覆盖一次→返回 struct
    // 之后没有任何机制重新读取
}
```

**证据 2：运算时参数也是启动时固定的**

速率限制器在创建时确定 rate/burst：
```go
// internal/app/app.go — wiring 时
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
```
此后 RPS 和 Burst 无法动态调整。如果要应对突发流量，必须修改配置并重启服务。

SSO 的断路器参数同理：
```go
// internal/app/app.go
ssoClient, err := sso.NewClient(ctx, cfg.SSO.Endpoint, sso.TLSConfig{...}, sso.ClientConfig{
    BreakerThreshold: cfg.SSO.BreakerThreshold, // 启动时固定
    BreakerCooldown:  cfg.SSO.BreakerCooldown,
    // ...
})
```

**证据 3：特性开关全部是编译期/启动时决定的**

```go
// 示例：速率限制、tracing、storage GC 的开/关
// 全部在 config.yaml 中设定，启动后无法动态切换
rate_limit.enabled: false   # 如果想临时开启→重启
tracing.enabled: false      # 故障排查时想临时打开→重启
storage.gc.enabled: false   # 想手动触发一次 GC→没有 API
```

**证据 4：没有 runtime configuration API**

```bash
$ grep -rn "PATCH.*config\|PUT.*config\|/v1/config\|/admin/config\|runtime.*config\|dynamic.*config" internal/handler/ --include='*.go'
# → 0 结果
```

没有 `GET /v1/admin/config`（查看当前运行时配置）或 `PATCH /v1/admin/config`（动态调整参数）端点。

**证据 5：文件类型白名单也是启动时加载的**

```go
// internal/app/app.go
service.WithAllowedMimeTypes(cfg.Storage.AllowedMimeTypes),
```
如果需要紧急封锁某个 MIME 类型（如安全事件驱动的 `application/x-java-applet`），必须发版、构建、部署——无法在运行时快速响应。

### 产品经理视角

- **运营灵活性不足**：当业务出现突发流量（如促销活动导致消息量暴增），运营团队无法动态调高 rate limit 或降低通知频率——需要等发版。
- **安全响应慢**：发现某个 MIME 类型被滥用后，无法立即在配置中禁止它。从发现到修复的窗口期受限于 CI/CD 周期。
- **故障排查受限**：追踪问题需要临时开启更详细的日志或追踪采样率——当前需要重启服务，可能丢失故障现场。

### 架构师视角

不需要全量热重载——那是分布式系统的圣杯，但对于单实例来说，以下几个可独立调整的"调节把手"就能解决大部分场景：

1. **配置分层**：启动时加载的静态配置（DB DSN、端口等）+ 运行时可调的动态配置（rate limit、cache TTL、采样率、MIME 白名单等），存储在 DB 的 `runtime_config` 表或 etcd/consul 中。
2. **定期重读**：`ConfigWatcher` goroutine 每分钟检查动态配置的版本号（或 watches etcd key）。变更时通过原子操作更新内存中的配置副本。
3. **管理 API**：`GET /v1/admin/runtime-config` 查看当前动态配置 + `PUT /v1/admin/runtime-config` 修改（需要 `system.config` 权限）。变更记录到审计日志。
4. **特性开关 API**：`POST /v1/admin/features/toggle` + `GET /v1/admin/features`。支持用户粒度的特性开关（如为特定 beta 用户提前开启新功能）。

---

## 优先级总结

| # | 方向 | 类别 | 优先级 | 核心价值 | 估算影响 |
|---|------|------|--------|----------|----------|
| 1 | Outbox 事件类型治理 | 基础设施 / 平台 | P1 | 解锁 Webhook 生态、防止 Schema 兼容性事故 | 高 |
| 2 | 操作级速率限制 | 基础设施 / 安全 | P1 | 更精准的滥用防御、关键操作保障 | 中 |
| 3 | 通知类型可扩展 | 产品架构 | P2 | 加速通知类产品迭代、改善用户体验控制 | 中 |
| 4 | 文件存储配额 | 产品 / 成本 | P2 | 成本控制、商业模式基础、滥用防御 | 中 |
| 5 | 运行时配置可调 | 运维 | P3 | 缩短安全响应、改善运营灵活性 | 低-中 |

---

## 已交叉验证的既有覆盖范围

本文提出的 5 个方向在以下 97+ 份分析文档中**零系统性覆盖**（即没有文档将其作为独立方向进行完整的代码级证据链论证）：

- 移动推送（FCM/APNs）、群聊、文件版本管理、延迟任务框架、API 契约、合成探针、活动流、跨实体 GC
- 多实例分布式协调、SSE 跨实例广播、多设备管理、URL unfurling、消息转发、死代码清理
- 文件预览管线、内容审核管线、数据生命周期、@提及系统、Bot 框架、消息定时发送
- 双 Service 层架构债务、审计日志、全文搜索、消息 Reactions、文件流上传 + 大小/MIME 校验
- 内容去重、预签名直传、存储 GC、事务性 Outbox + 幂等键、SSE 实时推送
- SSO 断路器 + stale-cache + singleflight + 重试 + 证书固定 + 证书固定生命周期
- 速率限制、指标、Tracing、健康检查、gRPC ops surface、迁移 CLI、管理员角色系统
- 用户屏蔽、文件分享、评论举报/审核、消息已读回执、通知偏好 + 静默时段 + Webhook
- 等等（累计约 800+ 独立方向条目）
