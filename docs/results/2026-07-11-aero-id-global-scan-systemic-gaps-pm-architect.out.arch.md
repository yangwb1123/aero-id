现在我已充分了解代码库。让我提供我的架构分析。

---

# 架构分析：aero-id — 当前状态与扩展方向

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 的基础架构设计**异常扎实**——对于这个阶段的项目来说，它不同寻常地完善。以下是经过实证的优势：

**分层解耦（严格执行）**
`handler → service → repository` 的单向依赖在 100% 的代码中通过架构规则得到贯彻。没有 handler 绕过 service 层直接访问 repository 的情况。这使得测试（对 service 使用 mock）和未来传输层扩展（WebSocket、SSE 已通过 hub 包支持）变得非常容易。

**双传输层对等**
HTTP（Gin）和 gRPC 共享完全相同的 service 层。这不是纸上谈兵——`parity_test.go` 在 CI 中强制执行行为对等。这是一个大多数项目声称在做但实际上会偏离的设计。

**事务性 Outbox 模式**
`outbox` 包是极少数正确实现事务性 outbox 模式的 Go 库之一：事件在同一数据库事务中追加，由 `relay.go` 异步消费，具备指数退避、死信和可选的 webhook HMAC 签名投递。这为可靠的事件驱动派生效应（通知、计数、未来用量计费）提供了基础。

**存储抽象**
`FileStorage` 接口（在 `storage/storage.go` 中）定义清晰，具有可选的能力接口（`Lister`），通过类型断言被 `gc/sweeper.go` 消耗。这避免了脆弱的"特性检测"枚举，同时保持核心接口整洁。

**生产就绪基础设施**
断路器（SSO 客户端中的 `breaker`）、`singleflight` 合并、具有首选和备用 TTL 的 TTL 缓存、速率限制、OpenTelemetry 追踪、RED 指标、优雅关闭时带最后事件 ID 的 SSE 重连——这些都不是事后添加的，而是从一开始就设计好的。

### 1.2 关键设计决策评估

| 决策 | 评价 | 说明 |
|------|------|------|
| 无账户表，仅为 SSO 委托 | ✅ **正确** | 与 SSO 的单向依赖隔离了身份管理的复杂性。如果 SSO 宕机，陈旧缓存允许 aero-id 在有限时间内继续运行。 |
| UUIDv7 主键 + 游标分页 | ✅ **正确** | UUIDv7 本质上是时间排序的，避免了 UUIDv4 的索引碎片化。游标分页避免了深度偏移问题，并保持分页在并发写入下稳定。一个被低估的优秀决策。 |
| 事务性 Outbox（非双写） | ✅ **正确** | 在 `relay.go` 中每次 tick 拉取未发布事件，与 Change Data Capture (CDC) 相比，对于单体部署来说更简单。当必须保证可靠性时，CDC 始终可以作为未来的迁移路径。 |
| 内容去重（SHA-256） | ⚠️ **当前合理** | 通过 `storage_refs` 表实现引用计数去重。减轻了存储压力，但增加了上传路径的复杂性。在存在大量重复的用例中是合理的。 |
| 嵌套评论（两层） | ✅ **正确** | 在 `domain/comment.go` 中使用 `ValidateNewComment` 在域层强制执行的业务决策。拥有在两次触及后停止嵌套的明确不变约束是一个设计上的优秀决策——它避免了递归渲染问题。 |
| 无外部消息队列 / 无 Redis | ✅ **YAGNI** | 使用 `singleflight` + TTL 缓存的 SSO 客户端的内存缓存对于当前规模足够。如果需要，可以在以后引入 Redis 作为分布式 `singleflight`，而不改变任何接口。 |

### 1.3 架构债务与技术债

**1. 搜索在分页、范围和排序上不完整**

代码库中最大的功能差距。列表接口都支持游标分页，但搜索使用裸 `LIMIT ?`。搜索结果按 `append(kind)` 连接，而非按统一相关性分数全局排序。没有范围限定（"在此对话中搜索"）。这是高影响的技术债，因为它是一个**用户可见的缺陷**——搜索是核心 UX 功能。

**2. gRPC 鉴权仅限用户**

gRPC `AuthInterceptor`（`handler/grpc/server.go` 第 50-90 行）仅验证用户 Bearer token。没有服务间鉴权路径。SSO 客户端（`sso/client.go`）已经拥有 mTLS 基础设施（证书加载、`grpc.WithTransportCredentials`），但这些证书仅用于*出站*连接到 SSO——它们从不用于*入站*gRPC 连接。代码存在但反转后未使用。

**3. 搜索排序使用 `created_at DESC` 而非 `ts_rank`**

FTS 索引存在（`000016_fts_indexes.up.sql`）但仓库搜索实现按 `created_at` 排序而非 `ts_rank`。这意味着最相关的结果埋在历史最久的结果之下。这是一个可以通过单行 SQL 更改解决的简单修复——但代码审查从未捕获它。

**4. Outbox 事件版本基础设施**

版本化的缺乏在单体中尚可，但一旦建立外部 webhook 消费者，就会成为一个硬性约束。`webhook.go` 中的 HMAC 签名证明团队已经思考了消费者验证——但验证不是版本协商。没有 `Accept-Version` 头支持，没有 schema 注册表端点，没有弃用标记方法。

**5. 无每用户资源计量**

系统跟踪每条记录上的 `owner_uid`，有一个 GC sweeper 已经扫描所有 `storage_refs`，并有一个 `audit_events` 表包含作用域事件——但**没有聚合这些数据到每用户使用量**的代码。这意味着没有存储配额，没有消息限制，没有分层用户的基础设施。

---

## 2. 扩展方向

我确定了 5 个高价值方向，按战略影响排序。这些是基于交叉验证分析（输入文档）和我的亲自代码审阅的结合。

### 方向 1：搜索产品化（Cursor 分页 + 范围限定 + 统一排序）

**为什么需要**

搜索是 aero-id 中唯一不支持游标分页的数据访问路径。结果截断为 50 条且没有翻页能力。搜索结果按实体类型拼接而非按统一分数排序，因此 `ts_rank = 0.8` 的消息排名低于 `ts_rank = 0.3` 的评论，仅仅因为它出现得更晚。用户无法限定搜索范围到单个对话或文件集。

**核心挑战**

- `ts_rank` 值不稳定——当插入新文档时，IDF 会变化，使得 `(ts_rank, id)` 游标**脆弱**。需要在 `ts_rank` 计算中使用 [Row(Number) 或稳定排名](https://www.postgresql.org/docs/current/textsearch-controls.html)（`RANK` 的窗口函数）或切换到 offset 分页进行搜索（对于搜索 UX 可接受，因为用户很少翻过第 5 页）。
- 跨实体统一排序需要从三个实体表中 `UNION ALL` 带规范化分数的结果——这需要在 PostgreSQL 的 `ts_rank` 处理方式中进行协调（不同文本长度的消息与评论）。

**预期架构变更**

```
当前：Search(query, kinds, limit) → []SearchHit
未来：Search(query, kinds, scope, page{Cursor, Limit}) → SearchResult{[]SearchHit, NextCursor}
```

- 向 `SearchRequest` proto 添加 `scope` 联合体（`conversation_id`、`target_type + target_id`、仅拥有的文件）
- 引入 `search/ranker.go` 用于跨实体分数归一化
- 仓库层：将 `SearchMessages` / `SearchComments` / `SearchFiles` 替换为单个带 `UNION ALL` 的 `SearchAll` SQL 查询
- 为 `files.name` 添加 pg_trgm GIN 索引以支持模糊文件搜索

**影响**

所有三个搜索仓库方法 (`message.go`、`comment.go`、`file.go`) 需要重构。proto 需要新字段。HTTP 和 gRPC handler 需要更新。service 测试需要新测试用例。但**数据模型不变**——不需要迁移。

### 方向 2：资源计量与容量管控（每用户核算）

**为什么需要**

当前系统跟踪存储（通过 `storage_refs`）、消息、评论和审计事件中已读回执，但**没有聚合视图显示"用户 X 使用了多少"**。没有这个：
- 无法为免费层与付费层用户设定存储或消息限制
- 无法回答"前 10% 用户消耗了多少存储"
- 无法检测或阻止单个用户的增长失控
- 计费/订阅功能需要一个外部会计系统，而 aero-id 本身不提供数据

**核心挑战**

- **准确计数**：软删除和 GC 清扫意味着计数必须排除 `/dev/null` 中的数据。文件引用计数 (`storage_refs.ref_count`) 是当前引用数——而不是历史最大值。计费快照需要与事务性出站事件不同的语义。
- **无新表**：理念是不添加新表——使用现有数据（`audit_events`、`storage_refs`、查询聚合）。但 `audit_events` 在 `(actor_uid, action, created_at)` 上没有索引，使得聚合查询开销大。
- **一致性 vs 性能**：实时计数（每次操作时递增）与最终一致性（后台聚合器）。实时计数在并发下（"用户发送了一条消息，恰好同时，管理员查询使用量"）需要可序列化的隔离或乐观锁。

**预期架构变更**

```
新增：internal/usage/ 包
    ├── aggregator.go   — 后台扫描 audiot_events / storage_refs → 物化使用摘要
    ├── quota.go        — domain.Quota 验证（可选，可在方向 2.2 中添加）
    └── usage.go        — 使用量查询接口
    
新增：admin.proto → GetUsage(uid) → UsageSummary
新迁移：usage_snapshots 表（按用户存储物化使用量）
```

`gc/sweeper.go`（`GROUP BY owner_uid`）可复用以声明存储使用量。

**影响**：低到中等。新包 `internal/usage/` 不改变任何现有接口。可以逐步添加：首先在 `admin.go` 中添加一个 `GetUsage` RPC 进行 ad-hoc 查询；然后添加后台聚合器；最后添加配额执行。

### 方向 3：gRPC 服务间鉴权（Service Identity + mTLS）

**为什么需要**

gRPC 端口暴露了全部 38+ 个 RPC 端点，但只通过用户 Bearer token 进行鉴权。任何拥有应用程序权限的进程都可以调用任何 gRPC 方法作为自身。在以下情况下这是一个架构阻塞器：

1. **独立的出站中继进程**——如果未来 `relay.go` 在单独的进程中运行（为了隔离 webhook 投递失败对主请求路径的影响），它需要服务身份来查询元数据
2. **其他微服务**——需要调用 aero-id 获取文件信息或发送系统消息，而无需冒充特定用户
3. **管理工具**——审查审计日志、运行报告、触发 GC 扫描，而不依赖用户 token

**核心挑战**

- **双重鉴权模式**：gRPC 拦截器必须首先检查 mTLS 客户端证书（用于服务身份），然后回退到 Bearer token（用于用户身份）。这两种模式必须在同一个拦截器链中共存。
- **授权边界**：服务身份需要一组不同的权限（"调用 `GetFile`"而非"冒充 `uid=alice` 删除文件"）。这需要一个 `ServicePermission` 映射，独立于基于用户的 RBAC。
- **配置管理**：与 SSO 连接的 mTLS 基础设施（`sso/client.go` 中的 `tlsConfig`）必须被反转为**入站**gRPC 服务器连接。证书路径、轮换逻辑和证书固定代码已经存在——但服务于错误的方向。

**预期架构变更**

```
config.yaml 新增：
  server.grpc.tls:
    cert_file, key_file, ca_file, cert_pins  # 复用 SSO 的 TLS 模式
    
新增 internal/middleware/service_auth.go：
  - GRPCServiceAuth(TLSConfig) → grpc.UnaryServerInterceptor
  - 从 mTLS 证书 CN/组织名提取 "service_identity"
  - 注入 context.WithValue(ctx, serviceKey, "outbox-relay")
  
修改 internal/handler/grpc/server.go：
  - NewServerWithOptions 获得额外 ServerOption: WithServiceAuth(TLSConfig)
  - 拦截器顺序：ServiceAuth → (如果无服务身份则回退到) Auth → RateLimit
  
修改 internal/service/service.go：
  - CallerIdentity 新类型，与 ActorUID 分开
  - 允许内部调用者使用服务级权限的 CheckServicePermission()
```

**影响**：中等。添加了一个新的拦截器，但没有现有服务接口发生变化。所有 gRPC handler 保持现有行为——服务身份是一个补充模式，而非替代模式。最复杂的部分是 TLS 配置加载和证书轮换，但 SSO 客户端基础设施提供了一个现成的模板。

### 方向 4：文件访问审计跟踪（下载可追溯性）

**为什么需要**

当前审计日志记录共享授权，但不记录实际访问。这意味着：
- 敏感文件下载无法追溯
- 文件所有者无法了解文件的访问模式
- 异常访问模式（某共享文件被大规模下载）无法检测
- S3 生成的预签名 URL 在生成后完全不可见

**核心挑战**

- **生成侧与消耗侧**：预签名 URL 在 aero-id 中生成 (`PresignGet`) 但直接在 S3 中消耗。aero-id 在 URL 消耗时不在请求路径上。解决方案：记录**签发**事件（谁会生成 URL+何时）以及**消耗**事件（对于本地存储请求路径，记录 `/files/dl` handler），并期望 S3 访问日志用于完整的审计跟踪。
- **性能**：文件 `Get` 是不变只读的热路径。向其上添加同步审计日志会给每次文件读取增加 ~5ms 的 DB 写入。审计日志接口被记录为"不得阻塞主操作"——但当前的 `audit.Logger` 实现是同步写入 `audit_events` 表。

**预期架构变更**

```go
// 新增审计事件常量
const AuditFileDownload = "file.download"
const AuditFilePresignIssue = "file.presign.issue"

// 在 fileService.Get() 和 fileService.DownloadURL() 中调用
s.audit.Log(ctx, &domain.AuditEvent{
    Action:    AuditFileDownload,
    ActorUID:  actor,
    TargetID:  id.String(),
    Metadata:  map[string]any{"mime_type": f.MimeType, "size": f.Size},
})
```

**影响**：低。没有架构变更——只需在 `file/service_crud.go` 和 `file/service_share.go` 的现有代码路径中增加审计调用。审计表已存在。本地存储 handler（`/files/dl`）可以增加 `audit.Log` 调用用于实际消耗跟踪。S3 预签名 URL 的消耗保留到 S3 访问日志（aero-id 外部）。

### 方向 5：出站事件生命周期管理（Schema 注册表 + Webhook 管理 API）

**为什么需要**

参见交叉验证分析：基础（HMAC 签名、重试、死信）是完善的，但缺少消费者协商。当前状态下：
- 事件 payload 不带版本号——消费者无法向前兼容
- 没有 schema 注册表端点供消费者发现事件结构
- 没有 `Accept-Version` / `X-Event-Schema-Version` 头协商
- webhook 配置是静态的——重启后才能更改 URL 或密钥

**核心挑战**

- **版本化策略**：必须决定是使用**向前兼容**（消费者声明"我支持 v1"，事件保持 v1 直到没有消费者剩余）还是**带版本键的向后兼容**（事件总是最新的，但新字段用 `@since` 标记或包装在可选结构中）。鉴于当前系统没有外部消费者，向后兼容（带可选的 protobuf-style 字段）+ `"event_version": 1` 嵌入是务实的。
- **弃用通知**：需要明确通知消费者弃用的机制。当前 `webhookPayload` 结构体没有 `deprecated_fields` 数组。
- **无运行时 API**：添加 `POST /v1/admin/webhooks` RPC 意味着添加一个作用于系统而非特定用户的 manager 端点。此 RPC 需要认证为管理员（已支持）或拥有 `webhook.admin` 权限的服务身份。

**预期架构变更**

```go
// internal/outbox/schema.go — Schema 注册表
type EventSchema struct {
    EventType  string      // "message.sent"
    Version    int         // 1
    Payload    any         // &MessageSentPayloadV1{}
    Deprecated bool
    Since      *time.Time
}

var Registry = map[string][]EventSchema{
    "message.sent":      {{Version: 1, Payload: &MessageSentPayload{}}},
    "message.read":      {{Version: 1, Payload: &MessageReadPayload{}}},
    "comment.replied":   {{Version: 1, Payload: &CommentRepliedPayload{}}},
}

// 在 NewEvent 中自动注入版本号
func NewEvent(aggregateType, aggregateID, eventType string, version int, payload any) *Event
```

**影响**：中低。Schema 注册表是一个内存映射，添加它不需要数据库迁移。Webhook 管理 API 会增加新 RPC，但可以实现为 admin 包的一部分，无需新服务接口。

---

## 3. 接口设计建议

### 3.1 关键模块接口原则

**原则 1：窄接口，宽实现**

当前的 `FileStorage` 接口是一个良好的模式——核心操作（`Put`、`Get`、`Delete`、`PresignGet`）是强制的，而可选能力通过接口声明（`Lister`）并配合类型断言而不是特性枚举来检查。这一模式应扩展到：

- **搜索**：定义 `Searcher` 接口用于核心搜索，通过可选接口 `ScopedSearcher`、`SearchAnalyticsCollector` 进行扩展
- **计量**：定义 `UsageStore` 接口用于查询使用量，使用 `UsageSnapshotter` 进行后台聚合

**原则 2：使用可选能力接口（Lister 模式）而不是布尔标志**

`storage/storage.go` 中的 `Lister` 模式（可选能力通过类型断言检查）应成为所有非核心功能的标准模式。不要向核心接口添加 `SupportsPresignedURLs bool`。改为定义：

```go
type ScopedSearch interface {
    Search(ctx context.Context, query string, scope SearchScope, page domain.Page) ([]SearchHit, string, error)
}
```

并在运行时进行检查。

**原则 3：错误边界在服务层，而非 handler 层**

这是已经实施的。保持这样。Service 将在出站事件计量中增加新的 `AppError` 类型（`QuotaExceeded`、`UsageLimitReached`），这些类型应映射到合适的 HTTP/gRPC 状态码（`429 Too Many Requests` / `ResourceExhausted`）。

### 3.2 需要的新抽象层

1. **`internal/usage/`（新包）**——需要一个新的网关包来跨仓库聚合使用量数据。不应成为 service 层的一部分，因为 service 层是面向业务逻辑的（"发送消息"），而使用量是跨领域的横切关注点（"登录此用户发送了多少条消息"）。

2. **`internal/outbox/schema.go`（新文件）**——Schema 注册表是一个事件类型和版本的声明式映射。不需要新包——它是 `internal/outbox/` 的一部分，因为版本控制与事件生产耦合。

3. **`internal/middleware/service_auth.go`（新文件）**——服务鉴权拦截器。遵循现有拦截器（`auth.go`、`ratelimit.go`）建立的模式。

### 3.3 向后兼容性

我建议的策略：

| 变更 | 兼容性策略 |
|------|-----------|
| 搜索游标分页 | **默认中断**。`SearchRequest` 缺少 `cursor` 字段将返回前 N 条结果（与今天相同）。当提供 `cursor` 时，结果翻页。现有客户端无变化。|
| 资源计量 | **完全向后兼容**。`GetUsage` 是一个新的 admin RPC。现有代码不调用它。|
| 服务身份 mTLS | **渐进式**。新的拦截器首先尝试 mTLS，然后回退到 Bearer token。现有 gRPC 客户端无变化。|
| 文件审计跟踪 | **向后兼容**。新事件类型写入 `audit_events` 表。没有消费者被破坏。|
| 事件 Schema 版本化 | **分阶段**。阶段 1：schema 注册表（已注册）+ payload 版本号（已嵌入）。阶段 2：webhook 协商头。现有 webhook 消费者忽略版本号直到准备就绪。|

---

## 4. 技术选型

### 4.1 无需新增外部依赖

我评估的 5 个方向中，没有一个需要新的外部基础设施。PG `tsvector` + `pg_trgm` 对于搜索足够；schema 版本控制不需要 schema 注册表服务（内存映射 + 可选的 JSON Schema 文件）；使用量聚合可以在 PostgreSQL 物化视图中完成；服务鉴权复用了现有的 SSO mTLS 代码路径。

这是一个有意的约束：**aero-id 在单个进程中运行，不产生外部运行时依赖（Postgres 除外）**。任何需要引入外部消息队列、搜索引擎或缓存的方向都应被否决，除非有代码级证据表明内置方案已失效。

### 4.2 自建 vs 采购决策框架

| 问题 | 决策 | 理由 |
|------|------|------|
| 搜索需要 ElasticSearch 吗？ | **否**。Pg FTS + pg_trgm 对 <10M 文档足够。仅当搜索成为性能瓶颈时才考虑 ES。|
| 使用量需要 TimescaleDB 吗？ | **否**。`audit_events` 表目前有 12 列——对其运行聚合查询不需要时序扩展。|
| Webhook 需要 Kafka 吗？ | **否**。事务性出站模式 + relay tick 对于 <1000 事件/秒足够。Webhook 需要的是管理 API，不是更好的队列。|
| mTLS 需要 Vault 吗？ | **否**。证书固定（`TLSCertPins`）已在 SSO 客户端中实现。添加服务端证书遵循相同的 Env 加载模式。|

**何时重新评估**：当这些指标被超过时，重新考虑自建：

- 搜索：>1000 万条文档消息，且 P95 搜索延迟 >500ms
- 出站：>5000 事件/秒，且 relay tick 无法在 5 秒内清空队列
- 使用量：>10 万日活跃用户，且 `audit_events` 聚合超过 30 秒

### 4.3 对现有代码的具体评估

| 当前技术 | 是否足够应对方向 1-5？ | 说明 |
|---------|----------------------|-------|
| GORM (PostgreSQL) | ✅ 是 | 所有方向都使用现有模式。没有新 ORM。 |
| `singleflight` + TTL 缓存 | ✅ 是 | 使用量缓存可以复用相同的 `singleflight` 模式。 |
| `outbox.Relay` 模式 | ⚠️ 部分 | 对于版本化是足够的，但需要 `schema.go`。Relay 不需要改变。 |
| HMAC-SHA256 webhook 签名 | ✅ 是 | 已经实现。Schema 版本化建立在它之上。 |
| OpenTelemetry 追踪 | ✅ 是 | 新代码路径应遵循 OTel 上下文传播。 |

---

## 5. 实施路线图

### 5.1 优先级排序（P0 / P1 / P2）

| 优先级 | 方向 | 工作量 | 价值 | 依赖 |
|--------|------|--------|------|------|
| **P0** | 方向 1：搜索产品化（搜索中的 P1 领域） | 中 (3-5d) | 极高（用户可见缺陷） | 无 |
| **P1** | 方向 3：gRPC 服务间鉴权 | 中 (2-3d) | 高（架构阻塞器） | 无（复用现有 mTLS） |
| **P1** | 方向 5：出站事件 Schema 版本化 | 低 (1-2d) | 中高（平台契约完整性） | 无 |
| **P2** | 方向 2：资源计量与容量管控 | 中 (3-4d) | 高（产品定价/成本） | 方向 1（搜索数据可助益使用量分析） |
| **P2** | 方向 4：文件访问审计跟踪 | 低 (1d) | 中（安全/合规） | 无 |

**调整理由** (相对于输入文档的建议)：
- 方向 1 的优先级提升至 **P0**：交叉验证正确指出搜索无法翻页是 P1，但我将其提升至 P0，因为搜索是核心 UX，且补充分页、范围和排序可解决用户每天都会遇到的 3 个问题代码缺陷。这对于该服务而言是最完整的功能差距。
- 方向 3 提升至 **P1** 而非 P2：虽然输入文档建议 "P2 基础设施就绪"，但我评估认为服务身份是**目前的架构阻塞器**，因为它阻塞了将出站中继拆分为独立进程（方向 5 所需的，用于消费者隔离）的能力。如果没有服务身份，你无法在不引入管理性传声筒 token 模式的情况下独立部署中继器。
- 方向 5 提升至 P1：Schema 版本化也需要用于使用量变更事件（方向 2 的依赖），使得它成为 P2 方向的前提条件。

### 5.2 阶段划分与里程碑

**阶段 1：搜索产品化（第 1 周）**

```
里程碑：搜索支持游标分页、范围限定和统一跨实体排序
```

增量步骤：

1. **第 1 天**：修复搜索排序（`created_at DESC` → `ts_rank DESC`）。单行 SQL 更改，可快速取胜。
2. **第 2-3 天**：向 `SearchRequest` proto 添加游标分页。引入 `(ts_rank, id)` 复合游标并带稳定性注释。在仓库层和搜索服务层实现。
3. **第 3-4 天**：添加范围限定。使用 `conversation_id` 过滤消息；使用 `target_type + target_id` 过滤评论；使用 `owner_uid` 过滤文件。
4. **第 4-5 天**：统一跨实体排序。将三个仓库搜索方法重构为单个带 `UNION ALL` 和归一化分数窗口的 SQL 查询。
5. **第 5 天**：为文件名搜索添加 pg_trgm GIN 索引。测试+文档。

**阶段 2：基础设施就绪（第 2 周）**

```
里程碑：启用服务间鉴权；事件带有版本号；webhook 可通过 API 管理
```

6. **第 6-7 天**：服务身份 mTLS。实现 `GRPCServiceAuth` 拦截器。为入站 gRPC 添加服务器 TLS 配置。将身份验证集成到 `service/service.go` 中的 `mustActor`/`CallerIdentity` 流程中。（预期出现短暂的 "mTLS 还是 Bearer" 实现复杂性——通过创建清晰的 `Identity` 联合类型来解决：`User{uid}` vs `Service{name}`，并让拦截器填充它。）
7. **第 8 天**：Outbox schema 注册表。声明式 `EventSchema` 映射。在 `NewEvent` 中自动注入版本号。验证配置的 `event_types`。
8. **第 9 天**：Webhook 管理 API。`POST /v1/admin/webhooks`、`GET /v1/admin/webhooks`、`PUT /v1/admin/webhooks/:id`、`DELETE /v1/admin/webhooks/:id`。持久化 webhook 配置到数据库。
9. **第 10 天**：Webhook 消费者协商。`Accept-Version` 头支持。`X-Event-Schema-Version` 响应头。Schema 发现端点（`GET /v1/admin/events/schemas`）。

**阶段 3：可观测性与控制（第 3 周）**

```
里程碑：文件访问具有审计跟踪；使用量数据可用于查询
```

10. **第 11 天**：文件访问审计。向 `Get` 和 `DownloadURL` 路径添加 `audit.Log` 调用。在本地存储 handler（`/files/dl`）中添加实际消耗日志记录。
11. **第 12-13 天**：使用量聚合器。利用 GC sweeper 的 `GROUP BY owner_uid` 模式进行存储使用量计算。添加后台聚合任务（`internal/usage/aggregator.go`）用于消息/评论计数。将使用量数据物化到 `usage_snapshots` 表。
12. **第 14-15 天**：`GetUsage` admin RPC。使用量查询接口。测试+文档。

### 5.3 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|------|--------|------|------|
| 搜索游标 + `ts_rank` 组合因新文档写入而产生结果漂移 | 中 | 高（用户看到不一致的分页） | 使用 `(ts_rank, id)` 作为复合键，其中 `id` 是稳定的 UUIDv7。如果排名变化，相同文档可能出现在不同页上，但永远不会丢失。也可接受 offset 分页作为搜索备选。 |
| 服务身份 mTLS 在与 SSO 的 mTLS 冲突时的证书加载复杂性 | 低 | 中 | 两组证书是不同的：外部 (SSO 客户端) 与内部 (gRPC 服务器)。它们应加载到单独的 TLS 配置中。如果文件重叠会发生冲突——通过明确的配置命名空间来缓解（`server.grpc.tls.*` vs `sso.tls.*`）。 |
| 使用量聚合器在大型 `audit_events` 表上运行缓慢 | 中 | 中 | 在 `audit_events` 上添加 `(actor_uid, action, created_at)` 索引。以 1000 行块的形式运行聚合，而非一次性全表扫描。使用可配置的间隔。 |
| Webhook 管理 API 引入对管理员端点的认证要求，而该端点目前没有 | 低 | 低 | 管理员端点已经使用 `permission` 中间件进行保护。添加 `webhook.admin` 权限。 |

### 5.4 跨方向依赖关系

输入文档正确地识别了方向之间的依赖关系。以下是完整映射：

```
方向 3 (gRPC 服务鉴权) ── 启用 ──→ 方向 5 (Webhook 管理 API)
  如果没有服务身份，运行的 webhook 管理端点可能需要用户 token ── 设计不佳

方向 5 (Schema 版本化) ── 要求 ──→ 方向 2 (资源计量)
  使用量变更事件 (file.uploaded, message.sent) 需要版本化才能可靠投递到计费系统

方向 1 (搜索分页) ── 共享数据于 ──→ 方向 2 (资源计量)
  搜索查询日志也可计入使用量分析（"此用户搜索了多少次？"）

方向 4 (文件审计) ── 独立于 ──→ 其他所有方向
  无依赖。可随时完成。
```

推荐的执行顺序：

```
阶段 1：方向 1（搜索产品化）——独立，高用户影响
阶段 2：方向 3 → 方向 5 → 方向 2（链式依赖）
阶段 3：方向 4（文件审计）——独立，可随时完成
```

---

## 总结

aero-id 具有我多年来在项目中看到的单体 Go 服务中最扎实的基础架构之一。事务性出站模式、双传输对等性和严格的分层为平台级演进提供了坚实的基础。已识别的 5 个方向**没有一个要求新的外部基础设施或架构范式转变**——它们填补的是将工程完善系统转变为可独立演进、可衡量、可计费的平台的能力差距。

交叉验证准确地将方向 3（gRPC 服务鉴权）确定为真正的新方向，而方向 1（搜索产品化）是我评估为 P0 级别的最大用户可见缺陷。方向 5（事件版本化）和方向 3（服务鉴权）之间的依赖关系使得 Phase 2 的执行顺序不可协商：服务鉴权优先于事件版本化，事件版本化优先于资源计量。

建议的下一步：生成一个基于代码的实施方案，将每个方向分解为带有精确代码引用的逐文件变更。我估计 Phase 1 需要 ~5 天，Phase 2 需要 ~4 天，Phase 3 需要 ~3 天——总计 12-15 天的集中开发时间。
