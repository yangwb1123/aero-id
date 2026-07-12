以下是对 `2026-07-11-aero-id-code-scan-five-unsystematically-covered-high-value-extensions.md` 的完整架构分析报告。

---

# 架构分析报告: aero-id 五个未系统性覆盖的扩展方向

## 1. 架构评估

### 1.1 当前架构的优势

文档揭示了一个已走向成熟的服务。从架构师视角，aero-id 当前架构的核心优势值得明确承认：

- **面向约束的接口隔离**：`service` → `repository` → `storage` → `sso` 的分层是干净的。中间件（auth、rate-limit）作为横向切面插入 gin 路由组，没有污染业务逻辑。
- **查询范式统一**：所有列表端点采用 `WHERE id < cursor ORDER BY id DESC LIMIT n+1` 模式，这是一个被精心设计的决策——避免了 `OFFSET` 的深分页问题，且与 UUIDv7 的单调递增特性天然契合。
- **可观测性投资到位**：OTel 追踪、RED 指标、per-dependency `/readyz`——这是很多 MVP 阶段不会做的投入，意味着团队对生产可运维性有认知。
- **事务性 Outbox 模式**：这是分布式一致性中的高阶模式，能在不引入消息中间件的情况下保证最终一致性，设计决策合理。

### 1.2 当前架构的局限性

文档中指出的 5 个方向揭示了**同一类架构债务**——**隐式契约缺乏显式声明和防御机制**。

| 域 | 隐式假设 | 风险 |
|----|---------|------|
| Cursor 分页 | "cursor 是用户提供的 ID，不需要验证" | 数据泄漏、静默终止 |
| API 版本化 | "所有消费者可以同步升级" | 无法做破坏性变更 |
| 搜索作用域 | "全局搜索就够用" | 产品体验鸿沟 |
| 速率限制 | "一个桶就能管住所有端点" | 端点间互相污染 |
| 数据库超时 | "查询永远不会超过 context 超时" | 级联故障 |

这些问题的共性根源是：**系统在"假设正确操作路径"下运行良好，但在"错误、恶意、或退化"路径下缺乏防御性编程**。

### 1.3 关键设计决策合理性评估

| 决策 | 当前状态 | 评估 |
|------|---------|------|
| Cursor = 纯 UUID | ✅ 简洁，❌ 无验证 | 在 MVP 阶段合理，生产需升级 |
| 全局单桶限流 | ❌ 无法差异化 | MVP 阶段通常单桶起步，但不应停留在生产 |
| 共享连接池 | ✅ 简化运维 | 合理，但当查询超时失控时是单点故障 |
| Context 不设 deadline | ❌ 依赖 goroutine 协作 | 这是文档中最严重的架构债 |
| 共享 Service 层 | ✅ 架构正确 | 双传输共享业务逻辑是正确的 |

### 1.4 需要修正的设计缺陷（架构债务）

1. **`Page.Cursor` 是无类型字符串**：`domain.Page` 是跨所有列表端点的核心数据结构，它的非对称性（输入是脆弱字符串，输出也是脆弱字符串）使得任何增强都需要在该类型层面做修改。

2. **Context deadline 未显式传播**：这是最值得警惕的问题。Go 的 `context.WithTimeout` 是防御性超时的标准机制，但当前只有搜索路径使用。GORM 的 `db.WithContext(ctx)` 会将 `ctx.Done()` 传播到 SQL driver，但如果 `ctx` 没有 deadline，这个机制毫无意义。

3. **连接池健康指标缺失**：`sql.DB.Stats()` 暴露了所有连接池状态数据（`InUse`、`Idle`、`WaitCount`、`WaitDuration`），但未被采集到 metrics 系统中。这是"免费"的可观测性改进——一个 Prometheus gauge 就能暴露出来。

4. **废弃代码无标记**：`internal/{file,comment,message,block,notification}/service.go` 中的独立 service 接口是死代码，应有 `// Deprecated` 注释和 `go vet` 规则来防止误用。

---

## 2. 扩展方向（深度分析）

基于文档内容，我在此进一步深化每个方向，提供架构层面的设计方案和更宏观的视角。

### 方向 A：Cursor 分页 Token 化（文档方向一的深化）

#### 为什么这是架构级改造而非简单加固

Cursor 分页的根本问题是**协议层语义缺失**。当前的 cursor 是资源标识符（UUIDv7）被复用为分页标记，但分页标记应该是一个 **不透明 token**，其语义由服务端完全控制。将其提升为 first-class 类型可以同时解决以下问题：

- 验证（格式校验、过期检查）
- 绑定（查询上下文签名、实体类型约束）
- 可观测性（日志记录 cursor 创建/消费的时间、频率、分布）

#### 核心设计决策

**选项 A：自包含 Token（推荐）**

将 cursor 从纯 UUID 升级为带元数据的、加密签名的 token：

```
base64url(
  version: 1 byte,
  entity_type: 1 byte enum,
  query_hash: 4 byte CRC32,
  expires_at: 4 byte unix timestamp,
  uuid: 16 byte UUIDv7,
  hmac: 32 byte HMAC-SHA256(secret, version||entity_type||query_hash||expires_at||uuid)
)
```

总计 ~58 bytes base64url 后约 78 字符（vs 当前 36 字符 UUID）。

- **优点**：无状态，无需额外存储或缓存；自验证，支持跨实例水平扩展
- **缺点**：payload 膨胀（约 2x）；需要全局 signing key 管理

**选项 B：服务端 Session 存储（不推荐）**

Cursor 创建时写入 Redis，返回随机 ID；消费时查询 Redis 验证。

- **优点**：cursor 长度最短、可动态调整策略
- **缺点**：引入 Redis 依赖；增加每次分页查询的 RTT；Redis 故障会导致所有分页中断

**选项 C：混合 Token（折中）**

在选项 A 的基础上，缓存已解密 token 的 LRU 映射（服务端内存），避免每次解密/校验 HMAC 的开销。

#### 接口契约变更

```go
// domain 层
type CursorToken struct {
    EntityType EntityType      // 枚举：Message, File, Notification, Comment
    QueryHash  uint32           // CRC32 of query parameters that affect filtering
    ExpiresAt  time.Time        // default: +5 min from issuance
    ID         uuid.UUID        // the actual cursor UUID
    Signature  [32]byte
}

type Page struct {
    Limit  int
    Cursor string       // ↓ new
    Token  *CursorToken // populated by HTTP layer after decode+verify
}
```

#### 对前端的影响

前端需要理解 `410 Gone` 响应（cursor 过期）的含义，并在收到后自动回退到第一页请求。

---

### 方向 B：API 版本化策略与废弃管理（文档方向二）

#### 版本化策略选择

**选项 1：URL 路径前缀（`/v1/` → `/v2/`）——简单但粗糙**

```
GET /v1/messages → GET /v2/messages
```

- **优点**：路由清晰，客户端容易区分
- **缺点**：粗粒度——无法在单端点级别做版本演进；URL 污染

**选项 2：Content-Type 协商（推荐）**

```http
Accept: application/vnd.aero.v1+json
Content-Type: application/vnd.aero.v1+json
```

- **优点**：细粒度（可以逐端点变更，非二进制全有/全无）；URL 保持干净
- **缺点**：gin 原生不支持 Accept 头版本路由，需要自定义中间件

**选项 3：查询参数**

```
GET /v1/messages?api_version=2
```

- **优点**：调试方便
- **缺点**：URL `?` 之后的参数容易被代理/缓存忽略；不符合 REST 最佳实践

**推荐策略**：URL 路径前缀（`/v1/` 前缀不变） + `Accept` 头版本协商的组合。新端点直接使用新版 Accept 头；旧端点通过中间件渐进式废弃。

#### 废弃标记基础设施

需要实现的是一套**声明式废弃元数据**系统，而非在每个 handler 中硬编码响应头：

```go
type RouteMeta struct {
    Deprecation  *DeprecationInfo  // nil = 未废弃
}

type DeprecationInfo struct {
    Sunset time.Time               // 移除日期
    MigrationURL string            // 文档链接
    Alternate     string           // e.g. "/v1/messages/v2"
}
```

注册路由时附加元数据，中间件自动注入 `Deprecation` / `Sunset` / `Link` 响应头。

#### 死代码清理路径

`internal/{file,comment,message,block,notification}/service.go` 中的死接口应该：

1. 加 `// Deprecated: use internal/service.Xxx` 注释
2. 在 `Makefile` 中加 `grep -r "// Deprecated" internal/ --include="*.go"` 作为 lint 警告
3. 计划在下个里程碑移除（需要确认是否有外部消费者依赖这些接口符号）

---

### 方向 C：查询作用域系统化（文档方向三 + 架构扩展）

文档方向三只谈了"对话内搜索"。但从架构师视角，这应该被抽象为**查询作用域（Query Scope）**——一种跨实体类型的通用过滤机制。

#### 查询作用域模型

```go
// domain 层
type QueryScope struct {
    ActorUID     string          // 当前操作者（必填，由 auth 中间件注入）
    Conversation *ConversationScope // 可选：限定到特定对话
    TargetKind   TargetKind      // 可选：限定实体类型
    TargetID     uuid.UUID       // 可选：限定特定实体
    TimeRange    *TimeRange      // 可选：时间范围
}
```

这个模型应该被以下所有查询重用：
- 搜索（当前仅接受 query + kind）
- 消息列表（当前按 inbox/conversation 过滤）
- 文件列表（当前按 owner/share 过滤）
- 审计日志（当前按 actor/target 过滤）
- 通知列表（当前按 recipient 过滤）

#### 架构影响

- **Repository 层**：每个查询方法增加 `scope *domain.QueryScope` 参数，内部根据作用域拼接 WHERE 子句
- **HTTP/gRPC handler**：从请求参数中解析作用域，验证合法性
- **Service 层**：将 auth 上下文中的 actorUID 注入 scope，确保用户不能越权限定到不属于自己的对话

#### 为什么不只为搜索加 `peerUID` 参数？

如果仅加 `peerUID`：解决了一个特定场景，但未来每出现一种新作用域（如"文件类型限定"、"消息按时间段搜索"），就需要在搜索 API 上加一个参数。将作用域抽象为 first-class 类型使得：
1. 新作用域可以透明添加，不破坏现有 API 契约
2. 服务端可以验证作用域组合的合法性（如 `conversation + kind=files` 的组合是否有意义）
3. 审计日志可以记录作用域信息，便于调试

---

### 方向 D：多级速率限制架构（文档方向四的深化）

#### 架构设计

```yaml
rate_limiting:
  enabled: true
  strategy: multi_bucket         # 多桶策略
  global:
    rps: 100                     # 全局 per-actor 上限
    burst: 200
  groups:
    - name: write_operations
      rps: 20
      burst: 40
      routes: ["POST /v1/messages", "POST /v1/comments", "POST /v1/files", "POST /v1/reports"]
    - name: read_operations
      rps: 60
      burst: 120
      routes: ["GET /v1/*"]
    - name: search
      rps: 10
      burst: 20
      routes: ["GET /v1/search"]
    - name: upload
      rps: 5
      burst: 10
      routes: ["POST /v1/files/upload"]
```

#### 中间件链设计

```
Request → Auth MW → RateLimit (global) → RateLimit (group) → Handler
                         ↓                    ↓
                   429 if exceeded        429 if exceeded
```

- 全局限流器使用 **per-actor**（`uid`）的 token bucket
- 组限流器在全局通过后检查端点到哪个组
- 限流响应头：`X-RateLimit-Global-Limit`、`X-RateLimit-Global-Remaining`、`X-RateLimit-Group: write_operations` 等

#### Token 桶实现选择

| 维度 | 选项 A：内存 Token Bucket | 选项 B：Redis LUA Script | 选项 C：客户端分布式节流 |
|------|--------------------------|------------------------|------------------------|
| 延迟 | ~5μs | ~1ms（网络 RTT） | 0（客户端计算） |
| 一致性 | 单机 ≈，多机 ≠ | 多机一致 | 近似 |
| 复杂度 | 低 | 中（需要 Redis） | 低（算法简单） |
| 推荐场景 | 单实例部署 | 多实例部署 | 不推荐（无法服务端强制） |

**推荐**：选项 A（内存）作为初始实现，迁移到选项 B（Redis）当需要多实例共享限流状态时。

---

### 方向 E：数据库查询治理战略（文档方向五 + 架构扩展）

这是五个方向中最紧急的（文档将其列为 P0），但我认为它的架构影响远比"加个 statement_timeout"要深。它应该被提升为一个**查询治理（Query Governance）**子域。

#### 分层超时模型

```
Gin HTTP server timeout (30s)
    ↓
Service context deadline (15s for writes, 5s for reads)
    ↓
GORM query statement_timeout (10s for writes, 3s for reads)
    ↓
PostgreSQL statement_timeout (same as GORM via SET LOCAL)
```

每一层是上一层的安全网。核心原则是：**超时应该在距离调用者最近的位置第一道门槛被触发**。

#### 连接池分离（读/写分离）

长期来看，读/写混合工作负载共享同一个连接池的风险过高。推荐的演进路径：

1. **阶段 1（当前）**：单连接池，统一 `statement_timeout`
2. **阶段 2（下一步）**：通过 PG `pg_hba.conf` 或 pgbouncer 做连接级读写分离，应用层只改 DSN
3. **阶段 3（长期）**：使用 read-replica，配置 `database.reader_dsn`

阶段 1 的 GORM 配置示例（不写具体代码，给出原则）：

```
- 默认 statement_timeout = 5s
- 写事务（在 service 层标记）设置 statement_timeout = 10s
- 搜索查询设置 statement_timeout = 8s（FTS 允许更长时间）
```

#### Metrics 采集维度

当前缺失的数据库指标（通过 `sql.DB.Stats()` 周期性采集）：

| 指标 | 标签 | 说明 |
|------|------|------|
| `db_connections_max` | 无 | 连接池最大连接数 |
| `db_connections_in_use` | 无 | 当前活跃连接数 |
| `db_connections_idle` | 无 | 当前空闲连接数 |
| `db_connections_wait_count` | 无 | 等待连接的总次数（累积） |
| `db_connections_wait_duration_seconds` | 无 | 等待连接的总时长（累积） |
| `db_query_duration_seconds` | `query_type`, `table` | 按查询类型和表聚合的查询延迟 |

其中 `db_connections_in_use` 是最关键的 SLO 信号——当它接近 `max_open_conns` 时，是连接池即将耗尽的 early warning。

#### 长期：查询分类与资源隔离

当系统规模进一步增长时，单连接池 + 单数据库实例会成为瓶颈。此时应该引入：

```
                    ┌──────────────┐
                    │   Primary   │
                    │  (writes)   │
                    └──────┬───────┘
                           │
        ┌──────────────────┼──────────────────┐
        │                  │                   │
  ┌─────▼──────┐   ┌──────▼──────┐   ┌───────▼──────┐
  │ Replica 1  │   │  Replica 2  │   │   Replica 3  │
  │ (读流量)   │   │ (搜索专用)  │   │ (消息推送)   │
  └────────────┘   └─────────────┘   └──────────────┘
```

但这需要接入层做 L7 路由（根据查询类型路由到不同的 replica），架构复杂度大幅上升。**目前不需要**——阶段 1 的 statement_timeout 已经能覆盖 90% 的场景。

---

## 3. 接口设计建议

### 3.1 实体类型枚举（跨模块共享）

当前多个模块各自定义自己的类型枚举（`domain.SearchKind`、消息类型、通知类型等）。需要引入一个共享的 **entity type registry**：

```go
// internal/domain/entity.go
type EntityType uint8

const (
    EntityUnknown   EntityType = 0
    EntityMessage   EntityType = 1
    EntityFile      EntityType = 2
    EntityComment   EntityType = 3
    EntityNotification EntityType = 4
    EntityReport    EntityType = 5
    // 未来的：EntityConversation, EntityChannel, EntityChannelMessage ...
)
```

这个枚举被以下所有场景复用：
- Cursor token 中的 `entity_type`（验证 cursor 只能用于对应实体列表）
- 搜索中的 `kind` 参数类型
- 审计日志中的 `target_type`
- 通知中的 `resource_type`

### 3.2 Query Scope 接口

```
// 搜索
GET /v1/search?q=keyword&scope=conversation:uuid&scope=time:2026-06-01..2026-07-01

// 消息列表（对话限定）
GET /v1/messages?with_uid=peer-uuid

// 文件列表（限定类型）
GET /v1/files?scope=kind:mime:image/*
```

`scope` 参数使用 URI 模板风格：`scope=<type>:<value>[,<value>]`，如 `scope=conversation:a1b2c3d4,scope=kind:application/pdf`。同一个 scope 类型出现多次则取并集。

### 3.3 速率限制响应头标准化

参考 IETF draft `draft-ietf-httpapi-ratelimit-headers` 标准：

| 头 | 值 | 说明 |
|----|-----|------|
| `RateLimit-Resource` | `upload` | 限流资源标识 |
| `RateLimit-Limit` | `5` | 配额上限 |
| `RateLimit-Remaining` | `2` | 剩余配额 |
| `RateLimit-Reset` | `1728662400` | 配额重置时间（Unix timestamp） |
| `Retry-After` | `300` | 建议重试等待秒数（仅 429 时返回） |

### 3.4 版本协商接口

```http
# 请求时指定偏好的版本（如果不指定，默认当前最新）
Accept: application/vnd.aero.v1+json

# 响应中声明版本
Content-Type: application/vnd.aero.v1+json
X-API-Version: 1.0

# 废弃时附加
Deprecation: true
Sunset: Sat, 31 Dec 2026 23:59:59 GMT
Link: </docs/migration-v2>; rel="deprecation"; type="text/html"
```

---

## 4. 技术选型

### 4.1 是否引入新技术栈？

| 技术 | 方向 | 推荐 | 理由 |
|------|------|------|------|
| Redis | 方向一（服务端 cursor session）、方向四（分布式限流） | **不引入** | 当前用内存 Token Bucket 和自包含 cursor token 完全足够。Redis 引入会新增运维复杂度（高可用、持久化、网络延迟），现阶段性价比低 |
| pgbouncer / Pgpool | 方向五（连接池分离） | **不引入** | 阶段 1 的 statement_timeout + 连接池监控就够。引入连接池中间件在架构上是合理的，但当系统规模尚未达到时，过度引入 |
| Prometheus + Grafana | 方向五（DB metrics） | **已引入** | OTel metrics 已就绪，只需 `sql.DB.Stats()` 加入 registry |
| protoc-gen-validate | 方向二（proto 版本化） | **可评估** | 用于 proto 字段废弃标记和迁移文档生成 |
| ACME/自定义 CA | 方向四（限流签名） | **不需要** | HMAC 签名使用现有 signing key 即可 |

**核心原则**：不要为单一方向引入新的基础设施依赖。所有五个方向都可以在当前技术栈内实现。

### 4.2 第三方依赖评估标准

当考虑引入新依赖时，遵循以下标准：

| 标准 | 门槛 | 说明 |
|------|------|------|
| 许可证合规 | MIT / Apache 2.0 | 避免 AGPL、SSPL 等传染性许可证 |
| 维护活跃度 | 最近 6 个月有提交 | 在 `go.mod` 中观察版本更新频率 |
| Go 版本兼容 | 支持 Go 1.26 | 与项目 pin 一致 |
| 测试覆盖率 | ≥ 70% | 检查上游仓库的 CI 报告 |
| 二进制大小影响 | ≤ 5MB | 避免 fat 依赖导致构建膨胀 |
| 替代方案 | 优先用标准库 | 如 `crypto/hmac`、`crypto/rand` 等 |

### 4.3 自建 vs 采购决策矩阵

五个方向均**不需要外部采购**。论证如下：

| 方向 | 为什么自建 | 为什么不需要采购 |
|------|-----------|----------------|
| Cursor token | 业务定制语义（entity_type、query_hash），通用库无法匹配 | cursor 分页不是独有机制，但其绑定规则是业务特定的 |
| API 版本化 | Gin middleware 本身就够 | 已有 `gin.RouterGroup` 可以挂载中间件；无专门 API 网关的成本效益 |
| 对话搜索 | 复用已有 FTS + GIN 索引 | 不需要 ElasticSearch / Meilisearch 等外部引擎 |
| 多级限流 | 内存 Token Bucket 实现简单 | API 网关（如 Kong、TyK）当前部署中是额外基础设施 |
| 查询超时 | GORM 的 statement_timeout 支持 | 不需要 SolarWinds DPA 等商业工具 |

---

## 5. 实施路线图

### 5.1 优先级排序合并

文档的 P0-P2 排序基本合理，但我建议对实施顺序做微调：

```
        紧急
          │
    P0    │   DB Statement-Level Timeout
    │     │   (1天，稳定性加固)
    │     │
    │     │   Cursor 分页完整性
    │     │   (2天，正确性修复)
    │     │
    P1    │   Per-Conversation 搜索
    │     │   (2天，产品价值)
    │     │
    │     │   Per-Endpoint 限流
    │     │   (4天，安全加固)
    │     │
    P2    │   API 生命周期管理
          │   (1天，平台成熟度)
          └──────────────────────→ 影响
```

### 5.2 阶段划分

#### 🔴 阶段 1（本周）：止损加固

**目标**：防止级联故障，终止静默错误

| 任务 | 工作量 | 依赖 |
|------|--------|------|
| 在所有 repository 查询中设置 GORM `statement_timeout`（读 3s，写 10s） | ~0.5 天 | 无 |
| 在 `service.List*` 方法中增加 context deadline（`5 * time.Second`） | ~0.5 天 | 无 |
| 添加 `sql.DB.Stats()` 采集到 Prometheus metrics | ~0.5 天 | `pkg/metrics` |
| 为 `Page` 类型增加 `Validate()` 方法（格式校验、TTL 检查） | ~0.5 天 | 无 |

**检出条件**：
- `make test && make lint` 通过
- 手动验证：用 `curl` 模拟慢查询（`pg_sleep(6)`），确认 5s 后返回 503
- `/metrics` 端点出现 `db_connections_in_use` gauge

#### 🟡 阶段 2（下2周）：功能增强

**目标**：交付产品可见功能，升级分页安全性

| 任务 | 工作量 | 依赖 |
|------|--------|------|
| 设计并实现 `CursorToken` 编码/解码（version + entity_type + query_hash + expires_at + HMAC） | ~1.5 天 | 阶段 1 的 `Page.Validate()` |
| 更新所有 HTTP handler 使用新 cursor 格式 | ~0.5 天 | CursorToken 实现 |
| 支持 410 Gone 响应（cursor 过期或无效） | ~0.5 天 | CursorToken 实现 |
| `SearchMessages` 增加 `peerUID` 参数；HTTP handler 暴露 `?with_uid=` | ~1 天 | 无 |
| 前端适配：搜索框 + 对话上下文传递 | 前端团队自行估算 | 后端 API 变更 |

**检出条件**：
- 用用户 A 的 cursor 请求用户 B 的资源 → 410 Gone
- 用文件列表的 cursor 请求消息列表 → 410 Gone
- 过期 cursor（> 5 min）→ 410 Gone
- `GET /v1/search?q=hello&with_uid=user-abc` 返回正确对话级结果

#### 🟢 阶段 3（下个月）：安全与平台升级

**目标**：安全模型升级，平台成熟度

| 任务 | 工作量 | 依赖 |
|------|--------|------|
| 读/写分离限流器实现（全局桶 + 端点组桶） | ~3 天 | 无，可并行 |
| 配置管理：`rate_limiting.groups[]` 在 `config.yaml` 中定义 | ~0.5 天 | 限流器实现 |
| 限流响应头标准化（RateLimit-Resource, X-RateLimit-Limit 等） | ~0.5 天 | - |
| 在所有响应中注入 `X-API-Version` 头 | ~0.5 天 | - |
| 死代码标记：`// Deprecated: use internal/service.Xxx` | ~0.5 天 | - |
| API 废弃基础设施（`Deprecation` / `Sunset` 头中间件） | ~1 天 | - |

**检出条件**：
- 文件上传 5 rps 独立限流，不影响同时的列表查询
- 所有响应包含 `X-API-Version: 1.0` 头
- 搜索路径超时时返回明确错误代码而非 500

#### 🔵 阶段 4（下个季度）：长期演进

**目标**：架构成熟，为 v2 做准备

| 任务 | 工作量 | 依赖 |
|------|--------|------|
| `QueryScope` 抽象设计并实现 | ~3 天 | 所有列表/搜索 handler |
| 迁移现有列表查询到 `QueryScope` 模式 | ~5 天 | QueryScope 实现 |
| 设计并注册 `/v2` 路由策略（Accept 头协商） | ~1 天 | - |
| 连接池分离：可选的 `database.reader_dsn` | ~3 天 | infra 需要提供 read-replica |
| 清理废弃的 `internal/{entity}/service.go` 死接口 | ~1 天 | 所有消费者迁移完成 |

**检出条件**：
- `/v2` 路由可并存，`Accept: application/vnd.aero.v1+json` 映射到 v1 handler
- 读查询连接到 read-replica（如果配置了 `reader_dsn`）
- `make lint` 不再报告废弃接口的使用

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| Cursor Token 格式变更导致现有缓存 cursor 全部失效 | 高 | 中（用户需要重新加载第一页） | 保持向后兼容：如果解码失败，降级为纯 UUID 解析 + 日志警告；发布前在 changelog 中明确标注 |
| Statement timeout 太短导致正常慢查询被错误中断 | 中 | 中（影响 P99 用户） | 可配置：`database.statement_timeout_read: 3s` 写在 config 中，允许运维按需调整；先在 staging 观察一周 |
| 多级限流配置复杂，运维难度上升 | 低 | 低 | 提供默认配置（读 60 rps / 写 20 rps / 搜索 10 rps / 上传 5 rps），开箱即用；运维只需在需要时修改 |
| Accept 头版本协商与现有客户端不兼容 | 低 | 高 | 默认行为不变（无 Accept 头时使用最新版本）；版本协商是 opt-in，现有客户端不受影响 |

### 5.4 测试策略

| 方向 | 测试类型 | 关键场景 |
|------|---------|---------|
| Cursor Token | 单元测试 | 编码/解码 round-trip、HMAC 校验失败、过期 token 检查、跨实体类型拒绝 |
| Cursor 分页 | 集成测试 | 分页完整覆盖（n+1）、删除最后一条后分页继续、过期 cursor 返回 410 |
| 对话搜索 | 集成测试 | 参与者可搜索、非参与者不可搜索、全局 vs 限定混合使用 |
| 多级限流 | 集成测试 | 单端点超配额被限、其他端点不受影响、限流头值正确 |
| DB 超时 | 集成测试 | 慢查询超时返回合理错误、不泄漏 DB 连接、metrics 正确记录 |

每个方向的关键测试场景应该作为 `make test-integration` 的一部分运行。

---

## 总结

这份代码扫描文档揭示了五个独立但相互关联的扩展方向。我的核心建议总结如下：

1. **不要为这些方向引入新技术栈**——所有五个方向在当前 Go + Gin + GORM + PostgreSQL 栈内都可以实现。文档中的代码证据表明，核心基础设施已经就位，缺口在于防御性编程和显式契约管理。

2. **以"查询治理"为主线串联五个方向**——仔细分析会发现，cursor 验证、context deadline、statement_timeout、限流差异化、搜索作用域——这些都在回答同一个问题：**"谁可以在什么条件下以什么速率查询什么数据"。** 这个视角的统一可以避免逐个方向单独实现的碎片化。

3. **实施顺序遵循"止损 → 产品 → 安全 → 平台"**——先修复真正可能引发生产事故的问题（DB 超时、cursor 数据不完整），再交付用户能感知的特性（对话搜索），最后做基础设施层的安全与平台升级。

4. **最紧迫的单行变更**：`db.Set("gorm:query_options", "SET LOCAL statement_timeout='5s'")`——一行配置，消除当前架构中最大的单点故障风险。

5. **最大的产品差距**：对话内搜索——这是用户期望的基本功能，FTS 基础设施已就绪，实现成本极低（~2 天），价值极高。阶段 1（本周末）优先交付。
