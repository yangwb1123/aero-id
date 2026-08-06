# aero-id — 全量代码体系扫描：五个被系统性遗漏的生产级扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 逐文件通读 `internal/` 全部 20+ 子包（~180 Go 源文件）、`migrations/` 全部 22 份 SQL、`api/proto/aero_id/v1/aero_id.proto`、`cmd/`、`configs/`、`Makefile`。  
> **交叉验证**: 与 `docs/requirements/` 下 **120+ 份既有分析文档**、`docs/ARCHITECTURE.md`、`docs/REVIEW_*`、`docs/specs/aero-id-design.md`、`ROADMAP.md` 逐方向、逐关键词交叉比对。  
> **核心承诺**: 以下 5 个方向在所有既有材料中 **从未被作为独立方向进行完整的代码证据链论证**。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和双视角（架构师 + PM）论证。

---

## 代码库成熟度摘要

aero-id 已完成的功能远超典型 MVP：

| 域 | 已交付能力 |
|----|-----------|
| **传输** | HTTP (Gin) + gRPC 38+ RPCs 完全对等 · SSE 实时推送（含 last_event_id 重连回放）· gRPC health + 可选 reflection · OTel 追踪 + RED 指标 |
| **文件** | 流式上传 · S3 presigned 直传 · SHA-256 内容去重 · MIME/Size 校验 · 显式分享/撤销 · 存储引用计数 + GC 扫表 + 孤儿清理 · 元数据 PATCH |
| **消息** | 1:1 私信 · Reactions · 编辑/软删 · 已读回执 + 计数 · Inbox/Conv 视图 · 系统消息 · 全文搜索 |
| **评论** | 两层嵌套（楼中楼）· Like/Unlike + 原子计数 · 举报→审核→隐藏/显示 · 编辑 · FTS 搜索 |
| **通知** | Outbox 驱动 · SSE 实时推送（last_event_id 重连回放）· 折叠去重 · 偏好设置（启用/静默时段）· Block 抑制 · Webhook HMAC 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引）· CJK ILIKE 回退 · 多 kind 联合搜索 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 · 幂等键（DB 持久化 + 过期清理）· 双传输限流 · SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning · 冷启动 warmup · 优雅关停 + 最后 drain |
| **治理** | 方向性 Block · 审计日志（append-only）· Admin 角色（4 权限）· permission 中间件 · 评论举报处理 |

在此坚实基础上，以下 5 个方向瞄准的是 **将 aero-id 从功能完备的后端服务推向可跨服务协作、可治理、可运维、可长期演进的平台级基础设施**。

---

## 方向一：Outbox 事件契约版本化与 Schema Registry

### 问题陈述

aero-id 的 outbox 系统支持 3 种内部通知事件（`message.sent`、`message.read`、`comment.replied`）和可选的 Webhook 外发。所有事件的 **payload 是纯 JSON，无版本号、无 schema、无向前兼容合约**。这是一个在单体中不明显的、但在多团队/微服务生态中会爆炸的架构债务。

### 代码级证据

**证据 1：事件 payload 是纯 Go struct，无 version 字段**

```go
// internal/service/notification.go
type messageSentPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
}

type messageReadPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
}

type commentRepliedPayload struct {
    CommentID       string `json:"comment_id"`
    ParentAuthorUID string `json:"parent_author_uid"`
    ReplierUID      string `json:"replier_uid"`
}
```

没有任何 struct 包含 `version` 或 `schema` 字段。如果未来需要添加 `message_sent_payload_v2`（比如增加 `file_id` 或 `kind` 字段），当前 schema 无法区分——所有 webhook 消费者同时收到新旧格式。

**证据 2：Webhook 消费者无法自描述事件格式**

```go
// internal/outbox/webhook.go
type webhookPayload struct {
    ID            string          `json:"id"`
    EventType     string          `json:"event_type"`
    AggregateType string          `json:"aggregate_type"`
    AggregateID   string          `json:"aggregate_id"`
    Payload       json.RawMessage `json:"payload"`
    CreatedAt     time.Time       `json:"created_at"`
}
```

`EventType` 是 `"message.sent"` 这样的字符串，但没有对应的 schema URL、version、或 content-type 的扩展字段。外部消费者必须**硬编码** payload 解析逻辑，无法通过 schema registry 动态发现事件结构。

**证据 3：服务层无事件 schema 断言**

```go
// internal/outbox/outbox.go
func NewEvent(aggregateType, aggregateID, eventType string, payload any) *Event {
    data, err := json.Marshal(payload)
    // ...
}
```

没有 schema version 参数，没有 `MarshalJSON` 时自动注入版本号。事件生产者（`service/message.go:Send`）直接传递 Go struct，隐式承诺向后兼容。

**证据 4：Webhook event type filter 无验证**

```go
// internal/config/config.go
type WebhookConfig struct {
    EventTypes []string `mapstructure:"event_types"`
    // ...
}
```

配置文件中可以写 `event_types: ["message.sharted"]`（拼写错误），系统启动时不报错——只是静默不投递任何事件。没有 `EventType` 的中心注册表或 validate 函数。

**证据 5：Outbox 事件类型集中分散**

事件类型字符串散落在多个文件中：
- `"message.sent"` — `service/message.go`
- `"message.read"` — `service/message.go`
- `"comment.replied"` — `service/comment.go`

没有中心化的 `event_type` 常量定义，没有编译时校验。

### 架构师视角

这是一个**契约成熟度**问题。当前 webhook 的消费者假设内外部一致，但现实中：

1. 新增字段 → 所有外部消费者**立即可见**但可能解析失败（如果使用了 strict JSON unmarshal）
2. 重命名字段 → 旧消费者静默丢失字段
3. 废弃字段 → 无法优雅弃用（无 deprecation 标记）

建议方向：
- 为每个事件类型定义一个 `EventSchema` 注册表（go map），包含 version、payload type、deprecated 标记
- 事件 payload 中嵌入 `"event_version": 1` 字段，生产时自动注入
- 配置中验证 `event_types` 是否在注册表中
- 为 webhook 消费者提供 `X-Event-Schema-Version` 响应头或 `$schema` payload 字段

### 产品经理视角

虽然外部团队今天可能还没接入 webhook，但 **一旦接入，schema 变更就是一个 breaking change**。没有版本化就意味着每次部署都需要协调所有消费者同步更新。这是一个**平台级风险**——它限制了 aero-id 自身独立迭代的能力。

### 已知受影响模块

`internal/outbox/` · `internal/service/notification.go` · `internal/service/message.go` · `internal/service/comment.go` · `internal/config/config.go`

---

## 方向二：搜索的 Cursor 分页与范围限定

### 问题陈述

aero-id 的所有列表接口（inbox、conversation、comments、notifications、files、blocked users）都支持一致的 cursor 分页。**搜索是唯一的例外**——`Search` 使用简单的 `LIMIT ?` + 无分页、无 cursor。当搜索结果超过 limit 时，用户无法"翻页"。

同时，搜索只能全局执行（"搜我所有能看到的"），无法限定范围（"只在这个 conversation 里搜"、"只搜我拥有或分享给我的文件"）。

### 代码级证据

**证据 1：Search 输入无分页字段**

```go
// api/proto/aero_id/v1/aero_id.proto
message SearchRequest {
    string query = 1;
    repeated string kinds = 2; // "messages", "comments", "files"; empty = all
    int32 limit = 3;
    // ← 无 cursor/page 字段
}
```

**证据 2：SearchService 接口无分页**

```go
// internal/service/search.go
type SearchService interface {
    Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error)
    //                                                                       ↑ 只有 top-N limit
}
```

相比其他列表接口的 `page domain.Page` 参数——后者携带 `Cursor` 和 `Limit`——搜索只有裸 `limit`。

**证据 3：Repository search 同样无 cursor**

```go
// internal/message/repository.go
SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error)
//                                                  ↑ 无 cursor
```

与 `ListInbox` 对比：
```go
ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
//                                                                                    ↑ cursor
```

**证据 4：FTS 查询使用 `LIMIT ?` 固定截断**

```sql
-- migrations/000016_fts_indexes.up.sql (generated column)
-- internal/repository/message.go:SearchMessages
SELECT ... WHERE search_vector @@ plainto_tsquery('simple', ?)
ORDER BY ts_rank(...) DESC
LIMIT ?  -- ← 无 cursor，无 offset
```

`ts_rank` 排序天然难以 cursor 分页（同一文档的 rank 值可能变化），但加上 `(rank, id)` 复合 cursor 可以实现稳定分页。

**证据 5：搜索不能限定范围**

```go
// internal/search/search.go
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    // 只接受 actor uid 和 query
    // 没有 scope 参数（如 "conversation_id"、"file_id"、"only_owned"）
}
```

### 架构师视角

搜索的"无分页"不是设计疏忽——因为 `ts_rank` 排序使得 cursor 分页实现比普通时间排序复杂。但这对用户来说意味着 **搜索结果永不完备**：大量匹配的结果被截断且用户无法翻页检索。

技术上可行的方案：
- 使用 `(ts_rank, id)` 复合 cursor：`WHERE (ts_rank < last_rank) OR (ts_rank = last_rank AND id > last_id)`
- 或转换为 offset 分页（对搜索可接受，因为用户通常只翻前几页）
- 搜索范围：为 messages 引入 `conversation_id` 过滤，为 files 引入 `visibility`/`owner_uid` 过滤

### 产品经理视角

当前搜索 UX 模式：用户输入 "report" 得到 20 条结果，但系统中有 200 条匹配。用户无法翻页看到更多——这是**搜索功能的核心缺失**。另一个场景：用户想找三个月前与 Alice 聊过的的某个文件——现在只能全局搜索"文件名"然后手动翻，无法限定搜索范围到「与 Alice 的对话」。

### 已知受影响模块

`api/proto/aero_id/v1/aero_id.proto` · `internal/search/` · `internal/service/search.go` · `internal/repository/{message,comment,file}.go` · `internal/handler/grpc/search.go` · `internal/handler/http/search.go`

---

## 方向三：gRPC 服务间鉴权（Service-to-Service Authentication）

### 问题陈述

aero-id 暴露 gRPC 端口（:9090）用于内部服务调用。当前 gRPC 的鉴权模型与 HTTP 完全相同：**校验 Bearer Token → 解析 uid → 注入 ctx**。这意味着任何能获取到用户 token 的进程都可以全量调用 gRPC API。在多服务架构中，非用户触发的内部调用（后台 worker、批处理作业、管理工具）需要一个**不同于用户 token 的、声明服务身份**的鉴权方式。

### 代码级证据

**证据 1：gRPC AuthInterceptor 只校验用户 token**

```go
// internal/handler/grpc/server.go（约第 50–90 行）
func AuthInterceptor(ssoClient *sso.Client) grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
        token, err := bearerToken(ctx)
        if err != nil {
            return nil, status.Error(codes.Unauthenticated, "missing bearer token")
        }
        uid, err := ssoClient.ValidateToken(ctx, token)
        // ... uid 注入 ctx ...
    }
}
```

Interceptor **不区分**请求是来自 end-user 还是来自另一个内部服务。两者都必须提供 SSO 签发的用户 token。

**证据 2：gRPC server 没有 service-identity 中间件**

```go
// internal/app/app.go
grpcServer := grpcsrv.NewServerWithOptions(
    ssoClient, log, metricsReg,
    []grpcsrv.ServerOption{grpcsrv.WithRateLimit(limiter)},
    // ... handler 列表 ...
)
```

没有 dual-auth middleware（先从 mTLS 客户端证书中提取 service identity，失败则回退到 Bearer token 用户认证）。

**证据 3：mTLS 配置只为 SSO 客户端配置，不为 gRPC 服务端配置**

```go
// internal/config/config.go
type SSOConfig struct {
    TLSCAFile     string   // 仅用于 SSO 客户端 → 外部 SSO 服务
    TLSCertFile   string   // 仅用于 SSO 客户端
    TLSKeyFile    string   // 仅用于 SSO 客户端
    TLSCertPins   []string // 仅用于 SSO 客户端
}

// 没有 server.grpc.tls_* 配置
type GRPCConfig struct {
    Host string
    Port int
    ReflectionEnabled bool
    // ← 无 TLS/mTLS 配置
}
```

**证据 4：内部调用无法跳过 SSO**

如果另一个微服务需要在没有用户上下文的场景下调用 aero-id（例如：事件处理 worker 查询某个文件的信息），当前唯一的方案是伪造一个 token 或走未鉴权的 HTTP 路由（`/files/dl` 类）。两者都不是合理的工程方案。

### 架构师视角

这是一个**边界安全**问题。今天的单体部署中不明显，但在以下场景会立刻暴露：
1. aero-id 被拆出多个 worker 部署（例如独立 outbox relay 进程做 webhook 投递）
2. 外部微服务需要查询文件元信息或发送系统消息
3. 管理面板需要执行审计日志查询而不经过 end-user token

推荐方案：
- gRPC 端口增加 mTLS 支持（`server.grpc.tls_*` 配置）
- 引入 `InternalAuthInterceptor`：如果客户端提供了有效的 service certificate（mTLS），则从证书 CN/组织名提取 service identity，注入 ctx（与 user uid 分开的 key）
- 在 service 层区分 `ActorUID`（用户）和 `CallerIdentity`（服务），允许 internal callers 使用有限的服务级权限

### 产品经理视角

这虽然是一个偏基础设施的方向，但它直接影响**平台集成能力**。如果 SSO 团队想从他们的后台发一条"你的账户已升级"的系统消息到某个用户，他们需要写一个完整的前端流程来获取用户的 token——而不是简单的内部 RPC 调用。没有 service-to-service auth，aero-id 在微服务生态中就是一个"只能由用户直接调用的孤立服务"。

### 已知受影响模块

`internal/handler/grpc/server.go` · `internal/app/app.go` · `internal/config/config.go` · `internal/middleware/auth.go`

---

## 方向四：部署元信息暴露与版本探针

### 问题陈述

aero-id 在 `config.App.Version` 和 `config.App.Env` 中保有版本和环境信息，但从未在任何 HTTP 或 gRPC 端点上暴露它们。也没有 build 时间、commit SHA、或健康检查中附带版本信息——导致运维人员无法确认正在运行的实例是哪个版本、哪个环境、哪个编译构建。

### 代码级证据

**证据 1：/healthz 和 /readyz 不返回版本信息**

```go
// internal/handler/http/health.go
func (h *HealthHandler) Live(c *gin.Context) {
    c.JSON(http.StatusOK, gin.H{"status": "ok"})
    // ← 没有 "version", "build", "commit" 字段
}

func (h *HealthHandler) Ready(c *gin.Context) {
    // 返回 status + per-dependency check 结果
    // ← 没有版本/环境信息
}
```

**证据 2：gRPC health 也不包含版本**

```go
// internal/handler/grpc/server.go
// grpc.health.v1 标准接口仅返回 SERVING/NOT_SERVING
```

**证据 3：没有 /version 或 /info 端点**

```go
// internal/handler/http/router.go
// 注册的路由：
//   GET /healthz, /readyz, /metrics  ← 没有 /version
//   /v1/*  ← 业务端点
```

**证据 4：配置中存在版本但未被使用**

```go
// internal/config/config.go
type AppConfig struct {
    Name    string `mapstructure:"name"`
    Env     string `mapstructure:"env"`
    Version string `mapstructure:"version"`
    // ↑ 这些值在 app.go 里只传给 tracing.Init（作为 ServiceName/ServiceVersion/Environment）
    // 但没有被暴露到任何业务或运维端点
}
```

**证据 5：没有 build-time 注入机制**

```go
// cmd/server/main.go
func main() {
    cfg, err := config.Load(*cfgPath)
    // cfg.App.Version 来自 configs/config.yaml 中的静态字符串
    // 而不是通过 -ldflags 注入的 git commit sha
}
```

没有 Makefile target 或 CI 步骤使用 `-ldflags="-X main.Version=$(git rev-parse HEAD)"`。

### 架构师视角

这是**可观测性基础设施**中一个很小但关键的缺失。当运营人员需要：
1. 确认金丝雀部署的实例是新版本
2. 排障时确认生产环境和 staging 实例的版本差异
3. 安全审计时追踪哪个版本的代码在处理某条数据
4. 在多版本滚动升级过程中确保负载均衡器正确路由

没有一个一键可得的版本信息端点，运维流程依赖日志前缀或编排系统的 labels——这些都是间接证据。

推荐方案：
- 通过 `-ldflags` 在 build 时注入 `version`、`commit`、`buildTime`
- 新增 `GET /version` 端点返回 `{"version": "...", "commit": "...", "build_time": "...", "env": "..."}`
- gRPC health response 的 metadata 中附带版本信息（如果 client 支持）
- 在 `/healthz` 和 `/readyz` 的响应中包含精简的版本 + 环境字段

### 产品经理视角

对于平台的消费者来说，一个版本端点也是基本要求——API 版本变更日志、SDK 版本兼容性矩阵都需要服务端暴露明确的版本信息。没有它，外部团队不知道他们对接的 API 是哪个版本，ab testing 也无法做。

### 已知受影响模块

`cmd/server/main.go` · `internal/handler/http/health.go` · `internal/handler/http/router.go` · `Makefile` · `internal/config/config.go`

---

## 方向五：每用户资源用量核算与容量管控

### 问题陈述

aero-id 完全没有**每用户资源使用量**的概念。没有 API 查询某个用户占用了多少存储空间、每天发送多少条消息、创建了多少评论。system admin 也无法获取全局资源使用分布（top N 存储消费者、消息量趋势、活跃用户排行）。这在 MVP 阶段合理，但在运营增长和成本控制阶段是盲区。

### 代码级证据

**证据 1：资源上限只有文件 level 限制**

```go
// internal/file/file.go
type Service interface {
    Create(ctx context.Context, in CreateFileInput) (*File, error)
    // ...
}
// 配置中有 MaxUploadBytes 和 AllowedMimeTypes，但无 per-user 存储配额
```

```go
// internal/config/config.go
type StorageConfig struct {
    MaxUploadBytes   int64    // 单文件上限
    AllowedMimeTypes []string // MIME 类型白名单
    // ← 无 MaxStoragePerUser
    // ← 无 MaxFilesPerUser
    // ← 无 DailyUploadLimit
}
```

**证据 2：消息和评论无 per-user 限流**

```go
// internal/config/config.go
type RateLimitConfig struct {
    RPS   float64 // 全局 RPS，按 actor uid 限速
    Burst int
    // ← 无消息发送配额（如 "每天最多发 500 条"）
    // ← 无评论创建配额
}
```

所有请求在速率限制器中消耗相同 token，所以一个恶意用户可以发送数百条短消息（每条成本 = 1 token）而不触发任何配额上限。

**证据 3：无存储用量统计查询路径**

```go
// internal/file/repository.go
type Repository interface {
    Get(ctx context.Context, id domain.ID) (*File, error)
    ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*File, string, error)
    // ...
    // ← 无 CountByOwner(ctx, ownerUID) (int64, error)
    // ← 无 SumSizeByOwner(ctx, ownerUID) (int64, error)
}
```

```go
// internal/message/repository.go
type MessageRepository interface {
    // ...
    // ← 无 CountBySender(ctx, fromUID) (int64, error)
}

// internal/comment/repository.go
type Repository interface {
    // ...
    // ← 无 CountByAuthor(ctx, ownerUID) (int64, error)
}
```

**证据 4：Admin 端无资源使用排行**

```go
// internal/handler/http/admin.go
type AdminHandler struct {
    adminRepo repository.AdminRepository
}
// 当前只注册了 /admin/audit-log
// ← 无 /admin/usage/users（存储用量排行）
// ← 无 /admin/usage/files（全局文件统计）
// ← 无 /admin/usage/messages（消息量趋势）
```

**证据 5：审计日志可以用于用量统计但未被利用**

`audit_events` 表记录了关键操作（`file.share.grant`、`message.delete`、`user.block`），这些数据理论上可以聚合出部分用量指标，但没有被查询或暴露。

### 架构师视角

资源可核算性是任何多用户系统在增长中必须的：

- **成本归因**：如果 aero-id 后端存储使用按量付费的 S3，运营需要知道"谁用了多少"
- **公平使用**：没有配额，一个高频用户可能消耗所有 DB 连接池
- **异常检测**：突然的存储增长或消息激增可能是被盗号或滥用
- **容量规划**：没有全局使用趋势，无法预估何时需要扩容

建议方向：
- 新增 `UserUsageService` 聚合各 repository 的 count/sum 查询
- 存储配额：可选的 `MaxStorageBytes` 配置，在 `Create` 路径检查
- 消息日配额：可选的 `DailyMessageLimit`，配合日区间计数
- Admin 端点：`/admin/usage/{files,messages,comments,storage}` 支持分页排行和趋势
- 审计日志聚合：定时作业（或实时计数器）将关键事件聚合成每日/每小时使用量

### 产品经理视角

没有用量可见性，产品团队无法回答最基础的问题："用户平均存储多少文件？""Top 10% 的用户占用了多少存储？""系统每天处理多少条消息？"这些指标驱动着产品定价、免费额度设计、后端成本控制和基础设施投资决策。

此外，**配额是收费的前提**——如果未来需要提供按存储容量付费的增值服务、或对不同用户等级设置不同存储上限，当前架构完全不支持。

### 已知受影响模块

`internal/file/repository.go` · `internal/message/repository.go` · `internal/comment/repository.go` · `internal/handler/http/admin.go` · `internal/service/`（新增 `usage.go`） · `internal/config/config.go` · `internal/repository/`（新增 `usage.go`）

---

## 总结

| # | 方向 | 类型 | 优先级 | 涉及模块 | 既有覆盖度 |
|---|------|------|--------|---------|-----------|
| 1 | Outbox 事件契约版本化与 Schema Registry | 可靠性 / 平台 | P1 | `internal/outbox/`, `internal/service/`, `internal/config/` | 在 120+ 份文档中零系统性覆盖 |
| 2 | 搜索 Cursor 分页与范围限定 | 产品完整性 | P2 | `internal/search/`, `internal/service/search.go`, `api/proto/` | 搜索分页被部分文档点过名，但从无完整论证 |
| 3 | gRPC 服务间鉴权（Service-to-Service Auth） | 安全 / 架构 | P1 | `internal/handler/grpc/server.go`, `internal/app/app.go`, `internal/config/` | 零覆盖（全部文档讨论的都是"用户→服务"鉴权） |
| 4 | 部署元信息暴露与版本探针 | 运维 | P2 | `cmd/server/main.go`, `internal/handler/http/health.go`, `Makefile` | 零覆盖（build 注入、version endpoint 均未被论证） |
| 5 | 每用户资源用量核算与容量管控 | 运营 / 成本 | P1 | 全 repository 层 + Admin + Config | 存储配额被提及但"用量核算+容量管控"方向未论证 |

**

说明：P0 = 生产阻塞、P1 = 高价值、P2 = 值得做但不紧迫**

这些方向的共同特征：它们不是"再加一个 CRUD endpoint"或"再加一个存储后端"式的功能扩展，而是**当 aero-id 被当作平台而非单实例微服务运行时必然遇到的架构缺口**。每一个方向在当前代码库中都有精确的代码级证明——不是缺乏某个特性，而是系统级的设计尚未演进到那个阶段。
