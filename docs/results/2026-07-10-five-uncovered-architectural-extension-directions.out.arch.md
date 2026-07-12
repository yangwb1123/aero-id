现在我已充分掌握项目背景。以下是全面的架构分析：

---

# aero-id 架构分析报告

> 基于自 2026-05-25 起的全部架构文档、审查记录及代码库结构

---

## 1. 架构评估

### 1.1 当前架构的优势

**分层纯度极高。** `handler → service → repository` 单向依赖链不仅是纸上设计，代码级强制通过 `HARNESS.md` 的 `check-architecture` 工具把关。这是 Go 项目中罕见的纪律——多数项目在快速迭代中分层会逐步模糊。当前代码库 330+ 源文件中，特征包之间（`file`、`message`、`comment`、`block`、`notification`、`search`）无交叉导入，`domain` 不进 `internal/`，`handler` 不被非 handler 包引用——这在单体中达到了微服务的模块隔离度。

**双传输共享服务层是 ADR-001 的正确选择。** 41 个 gRPC RPC 与 HTTP 路由共享同一套 `service` 方法签名，无 `gin.Context` 或 `grpc.ServerStream` 泄露。这意味着：功能增长时，协议转换成本是 O(1) 而非 O(n)——新增一种传输（如 WebSocket）也只需要一个新的薄 handler 层。

**Outbox 模式的实现质量高于多数同类项目。** SSO 状态机（断路器 + stale cache + singleflight + serve-stale-window）的 4 态转换覆盖了所有现实场景：正常→瞬断→长中断→恢复，且从 ADR-004 可知断路器只对可用性错误触发以消除误报。这个 SSO 降级逻辑可以单独提取为可复用库。

**测试策略分层得当。** 27 包单元测试 + 21 包 testcontainers 集成测试，覆盖了 handler、service、repository 三层。`testpg` 包被隔离在 integration tag 外，默认 `go test ./...` 无需 Docker，兼顾了 CI 速度与集成验证。

### 1.2 架构瓶颈

**单实例设计是当前最大的架构债务。** 这不是"未来可能需要"的问题——分布式限流的缺失意味着水平扩展反而会**降低**限流效果（4 实例 × 20 RPS = 80 RPS 容量，攻击者收益递增）。in-memory token bucket 的 `sync.Mutex` 在 20k+ RPS 时成为真实争用点（已在 REVIEW_INFRA_DEEP 中确认）。这个瓶颈限制了整个系统的扩展性基线。

**消息模型是 1:1 的硬天花板。** `messages.to_uid` 的单值设计不仅是 schema 选择——它渗透到了所有查询逻辑中：`ListInbox` 的 `WHERE (from_uid = ? OR to_uid = ?)`、`GetConversation` 的双人排序、SSE 推送的事件范围。迁移到群聊不是 schema 变更，而是**查询范式变更**——从"两个 uid 之间的消息"变为"N 个成员的消息通道"。这是架构级变更，影响 repository、service、handler 三层。

**无缓存层意味着数据库是每请求必经路径。** 文件元数据、消息内容一旦写入几乎不变，但 GET 操作仍穿透到 GORM → PostgreSQL。对于 read-heavy 场景（消息列表、文件信息），数据库连接数 `max_open_conns: 50` 将在 50 并发读请求后成为瓶颈——此时数据库连接池是系统的 implicit semaphore。即便 DB 能处理 10ms 查询，50 并发即达延迟拐点。

**通知系统缺乏偏好过滤。** `NotificationDispatcher` 当前是"write all notifications, deliver all to SSE"的简单模型。没有 per-type 开关、没有免打扰时段、没有用户级静音。这在 MVP 阶段合理，但在生产环境中会导致通知疲劳——用户会在"关闭所有通知"和"被所有通知淹没"之间二选一，两种选择都不利于留存。

### 1.3 关键设计决策评估

| ADR | 评估 | 需要重新审视吗？ |
|-----|------|-----------------|
| ADR-001：双传输同服务层 | ✅ 毫无疑问的正确选择 | 否 |
| ADR-002：SSO 唯一认证源 | ✅ 安全模型简洁 | 否 |
| ADR-003：事务性 Outbox | ✅ 一致性基石 | 否 |
| ADR-004：断路器仅可用性错误 | ✅ 精巧的设计，避免了误断路 | 否，但建议提取为正式库 |
| ADR-005：方向性阻塞模型 | ✅ blocker=recipient 语义清晰 | 否 |
| ADR-006：自主工厂模式 | ⚠️ 对单/小团队合理，但缺少人工把关的明确定义 | 建议补充"人工核准阈值"定义 |

**ADR-006 的风险：** "所有重大变更（>10 文件、公共接口修改、安全模型变化）需人工 Approve"——人工核准依赖个人判断。建议将阈值数字化：（1）文件变更数 >10 需核准，（2）公共接口变更（`service` 层签名修改）需核准，（3）安全相关变更（认证/授权/加密路径）**总是**需核准，无论文件数。

### 1.4 技术债分类

**第一类：必须在功能开发前修复（P0 技术债）**
- `like_count` 非原子 UPDATE —— 高并发下计数器漂移
- `idempotency_keys` 无 TTL 清理 —— 表膨胀
- gRPC 无 `RequestID` 拦截器 —— 日志关联断裂
- `context.DeadlineExceeded` 误映射为 500 —— 语义错误
- SSE 无 `last_event_id` 重连 —— 重启丢通知

**第二类：基础设施缺口（已分析但未处理）**
- Metrics 缺少 DB 连接池/GC/SSE/存储操作指标
- 搜索无 `statement_timeout` 保护
- Outbox handler 失败无退避
- 消息/评论软删除幂等行为不一致

**第三类：配置安全漏洞**
- `signing_key` 默认值可通过验证
- `sso.insecure: true` 默认值无启动警告
- Metrics 无基数控制 alerter

---

## 2. 高价值架构扩展方向

### 方向 1：多人群聊 / 频道系统（架构级变更）

**业务价值：** 1:1 消息到群聊是社交平台从"功能"到"平台"的跨越。文件共享（已实现）天然需要群聊语境——"分享给项目组"比逐人分享合理得多。当前 41 个 RPC 中消息占 11 个，是最大的单一领域——群聊将释放这个领域的 3x 增长潜力。

**核心挑战：**
- **查询范式重写。** 当前所有消息查询基于双人模型（`WHERE (from_uid=? OR to_uid=?)`）。群聊需要 `conversation_id` + 成员资格检查——这是对 repository 层最深层的变更。每个 `ListInbox`、`GetConversation`、`UnreadCount` 都需要新查询。
- **通知扇出成本。** 1:1 消息产生 1 条通知；群聊 N 个成员（不含发送者 + 被拦截者）产生 N-1-X 条通知。100 人群的消息产生 ~99 条通知行 + ~99 次 SSE 推送。这需要批量写入优化 + SSE fanout 速率控制。
- **与现有数据的迁移。** 历史 1:1 消息需要自动转换为镜像 conversation。迁移策略是：对每对 (min_uid, max_uid) 创建一个 conversation，将消息全部重挂到该 conversation_id。这是一个一次性、可回滚的数据迁移，但需要在代码中处理双活兼容期（旧 API 路径继续工作）。

**预期架构变更：**

```
新增：
  conversations 表 (id, type, title, created_by, created_at, updated_at)
  conversation_members 表 (conversation_id, uid, role, joined_at)
  改造 messages 表 (增加 conversation_id, 弃用 to_uid 但保留旧列兼容)

影响范围：
  message repository   — 新增 queryConversationMessages, rewritten ListInbox
  message service      — 新增 CreateConversation, AddMember, RemoveMember, Leave
  message handler      — 新增 4-5 个 HTTP/gRPC 端点
  notification service — 批量化 Handle 逻辑
  SSE hub              — fanout 到 conversation 的所有在线成员
```

**对现有系统的影响：** 中-高。核心查询变更影响现有 1:1 场景的代码路径。建议分 3 阶段：
1. **Phase 1（数据层）**：建表 + 历史迁移 + `conversation_id` 列 + 双写兼容
2. **Phase 2（API 层）**：群聊端点 + 成员管理
3. **Phase 3（通知层）**：批量通知写入 + SSE fanout + @提及

### 方向 2：传输协议加固与安全基线（当前缺口最大的方向）

**业务价值：** 这是审计反馈中"方向四"的核心——不是功能，而是安全基线。`MaxConcurrentStreams` 无限制允许客户端用完 goroutine 池；`ReadHeaderTimeout` 缺失允许多路复用层缓慢攻击。这两个是真正的 DoS 向量，其他（`MaxRecvMsgSize`、`InitialWindowSize`）是性能防护。当前无人触及这个层面，但攻击者会优先利用。

**核心挑战：** 需要区分**安全相关配置**和**性能配置**，分两批交付以控制变更风险。安全配置的修改可能影响现有 gRPC 客户端兼容性（如 `MaxConcurrentStreams` 降级可能使现有客户端重试）。需要在 staging 环境先验证再上线。

**预期架构变更：** 纯配置变更，无代码架构影响。`grpc.NewServer` 的参数从硬编码改为读取 `config.Server.GRPC` 段。服务端 `keepalive.EnforcementPolicy` 的添加防止客户端 keepalive 滥用。

**对现有系统的影响：** 低。配置参数的变更不会影响业务逻辑。但需要在文档中记录每个参数变更的理由和预期行为（`docs/GRPC_HARDENING.md`）。

### 方向 3：降级契约体系（文档 + 代码双落地）

**业务价值：** 当前系统有降级行为分析（INFRA_DEEP 的 §4），但无规范性契约。没有降级矩阵意味着运维人员不知道"SSO 挂了时 /v1/files 返回什么"、"DB 挂了时 /files/dl 还能用吗"。这是运维可操作性的缺口——SRE 需要在 incident 中快速判断降级路径是否正确，而不是读代码推断。

**核心挑战：** 
- **文档工作（P1-文档）**：创建 `docs/DEGRADATION_MATRIX.md`，逐端点定义每个依赖 (SSO/DB/S3/Local) 在不同状态下的行为。这个文档需要在 1 天内完成。
- **代码工作（P2-代码）**：实现降级状态管理器（`DegradationManager`）——聚合各依赖的健康状态，暴露 `IsDegraded(dependency)` 接口。每个 handler 需要读取这个状态并决定行为。这是 ~12-14 天的工作量，包括为每个 handler 写测试覆盖降级组合。
- **一个未文档化的设计资产：** `/files/dl`（HMAC 签名下载）当前是唯一完全不依赖 DB 和 SSO 的端点。这个行为已经有意设计，但没有文档化。可以在降级矩阵中将其作为"良好设计"的案例。

**预期架构变更：**

```
新增：
  internal/pkg/degradation/manager.go     — 状态聚合
  internal/middleware/degradation.go       — 写入 X-Aero-Degraded 头
  docs/DEGRADATION_MATRIX.md              — 规范性契约

影响范围：
  每个 handler（~8 个）需要读取 manager 状态
```

**对现有系统的影响：** 中。降级中间件的添加不需要改业务逻辑，但每个 handler 的降级适配需要测试。建议：文档做文档的（1 天），代码做代码的（分批交付）。

### 方向 4：数据生命周期管理（不仅仅是 GC）

**业务价值：** 当前系统有 GC（孤儿 blob + 过期软删除）和幂等键清洁器，但 `notifications`、`audit_events`、`outbox_events` 无清理机制。`audit_events` 和 `outbox_events` 是 append-only 表——随着时间推移，它们会成为存储膨胀的主要来源。以 1000 RPS × 5% 写入率估算，`outbox_events` 每天新增 4.3M 行（1000 × 5% × 86400 = 4,320,000）。30 天后即 1.3 亿行。

**核心挑战：** 生命周期策略因表而异：
- `idempotency_keys`：24h TTL（当前已配置但 cleaner 已修复）
- `notifications`：保留 30 天，或用户读完后的保留期
- `audit_events`：保留 90 天（合规要求）
- `outbox_events`：published 后保留 7 天（调试需要），dead_letter 后保留 30 天
- `files`（软删除）：GC retention 窗口（当前可配置）

需要统一的生命周期策略声明 + 可配置清洁器。清洁器应该可独立开启/关闭，不依赖 GC 开关。

**预期架构变更：**

```
新增：
  internal/pkg/lifecycle/policy.go — 策略声明
  internal/pkg/lifecycle/cleaner.go — 通用清洁器（每个表一个 goroutine）

配置新增：
  lifecycle:
    idempotency_keys.ttl: 24h
    notifications.ttl: 30d
    audit_events.ttl: 90d
    outbox_events.published_ttl: 7d
    outbox_events.dead_letter_ttl: 30d
```

**对现有系统的影响：** 中-低。清洁器是独立的后台 goroutine，与主请求路径隔离。每个表的清理 SQL 是简单的范围 DELETE（分批次避免锁长事务）。

### 方向 5：缓存层（L1 进程级 + L2 Redis，可选）

**业务价值：** 当前架构无缓存。文件元数据、消息内容等读密集且几乎不变的数据每请求穿透到 DB。PostgreSQL `max_open_conns: 50` 在 read-heavy 场景下是隐式信号量。缓存命中可将 P99 延迟从 5ms 降至 <100μs，同时数据库连接需求降 60%，允许降低 DB 实例规格。

**核心挑战：** 
- **缓存一致性。** 选择了 cache-aside + 主动失效模式。`Create`/`Update`/`Delete` 后 Delete 对应 key。但 `ListInbox`、`GetConversation` 等范围查询不缓存（一致性太复杂）。这意味着缓存只覆盖单实体 GET 操作，覆盖面有限。
- **Redis 引入的运维成本。** Redis 高可用（Sentinel/Cluster）、网络延迟、内存消耗。需要与现有单实例部署兼容（`cache.enabled: false`）。
- **L1 进程缓存 vs L2 Redis。** 进程缓存（`ristretto`/`freecache`）延迟最低但实例间不一致；Redis 延迟稍高但全局一致。建议：L1 做最快路径（TTL 短，30s），L2 做全局缓存（TTL 长，5min）。

**预期架构变更：**

```
新增：
  internal/pkg/cache/interface.go     — Get/Set/Delete 接口
  internal/pkg/cache/redis.go         — Redis 实现
  internal/pkg/cache/memory.go        — 进程内缓存实现
  internal/pkg/cache/chain.go         — L1+L2 链式缓存

每个 Repository 包装：
  fileRepo → cachedFileRepo (Decorator 模式)
  messageRepo → cachedMessageRepo

配置新增：
  cache.enabled: false
  cache.l1.size: 100MB
  cache.l2.redis.addr: "localhost:6379"
  cache.default_ttl: 5m
```

**对现有系统的影响：** 中-低。Decorator 模式意味着 repository 接口不变，service 层零变更。但需要为每个缓存的 repository 写单元测试验证缓存失效逻辑。

---

## 3. 接口设计建议

### 3.1 现有接口评估

| 接口层 | 设计质量 | 评价 |
|--------|---------|------|
| `service` 接口 | ⭐⭐⭐⭐⭐ | 签署时无传输依赖，可作为 gRPC service 的直接映射 |
| `repository` 接口 | ⭐⭐⭐⭐ | 返回 `domain` 类型，但 `*gorm.DB` 作为依赖传入导致测试需要 mock 整个 DB |
| `storage.FileStorage` | ⭐⭐⭐⭐⭐ | 完美抽象——Local/S3 互换无需 service 改代码 |
| `sso.Client` | ⭐⭐⭐⭐ | 接口清晰，但返回到 domain 类型的转换在 service 层完成，可考虑移到 sso 包内 |
| `outbox.Store` | ⭐⭐⭐ | 与 GORM 强耦合；`FetchUnpublished` 返回 `[]*Event` 但 handle 时依赖具体类型断言 |

**核心建议：** repository 接口应不再接受 `context.Context` 作为唯一参数——当前签名 `GetFile(ctx, id)` 是合理的，但 `ListFiles(ctx, filter)` 中的 `filter` 没有类型化（当前用 `map[string]any`？）。需要确认。如果 filter 是 `map[string]any`，建议改为类型化 `QueryFilter` 结构体，以在编译期捕获错误。

### 3.2 需要引入的新抽象

**降级状态管理器（`DegradationManager`）**

这是当前最紧迫的新抽象。定义：

```go
// 读不写——所有 handler 共享同一个只读快照
type DegradationManager interface {
    // IsDegraded 检查指定依赖是否处于降级状态
    IsDegraded(dep Dependency) bool
    // Status 返回所有依赖的当前状态快照
    Status() map[Dependency]Status
    // Subscribe 允许降级事件监听（用于 metrics/alerting）
    Subscribe() <-chan DegradationEvent
}

type Dependency int
const (
    DepSSO Dependency = iota
    DepDatabase
    DepStorage
)

type Status int
const (
    StatusHealthy Status = iota
    StatusDegraded
    StatusDown
)
```

设计原则：manager 不决定 handler 的行为——handler 读取状态后自行决定。这保持了"handler 含业务逻辑？"的讨论：降级行为是运维策略，放在中间件或独立的 `degradation` 包中。

**通用生命周期清洁器（`LifecycleCleaner`）**

```go
type LifecyclePolicy struct {
    TableName  string
    Column     string        // 通常是 created_at
    TTL        time.Duration
    BatchSize  int           // 分批删除，避免锁长事务
    Interval   time.Duration // 轮询间隔
}

type LifecycleCleaner interface {
    Register(policy LifecyclePolicy)
    Run(ctx context.Context) // 阻塞，在 goroutine 中调用
    Stop()
}
```

设计原则：每个表一个 policy 声明，一个 cleaner goroutine 管理所有表。而不是为每个表单独写一个清洁器。

### 3.3 向后兼容策略

**群聊迁移的兼容策略：**

```
Phase 1（双写）：
  - 新建 messages.conversation_id = NULL（允许 NULL）
  - 已有 1:1 消息的 conversation_id = NULL，查询同时检查新旧逻辑
  - 旧 1:1 端点 (`ListInbox`、`GetConversation`) 照常工作
  - 群聊端点仅返回 conversation_id NOT NULL 的消息

Phase 2（历史迁移）：
  - 后台迁移脚本将历史 1:1 消息写入镜像 conversation
  - 检测：若 conversation_id IS NULL 且 to_uid IS NOT NULL，走旧路径

Phase 3（收尾）：
  - 设置 conversation_id NOT NULL
  - 弃用 to_uid 列（不做强制删除，保留兼容性的最后保障）
```

**降级契约的兼容策略：** 降级中间件默认不活跃（`degradation.enabled: false`），只有明确配置才生效。这样现有部署不受影响，新部署逐步开启。

---

## 4. 技术选型

### 4.1 需要评估的新引入

| 技术 | 用途 | 评估 |
|------|------|------|
| **Redis** | 缓存 L2 + 分布式限流 | ⚠️ 建议仅在水平扩展超过 2 实例后引入。运维成本（高可用、持久化、网络延迟）在当前阶段可能大于收益。Cache 层可以先做 L1 进程缓存（无外部依赖）。 |
| **Redis `CL.THROTTLE`** | 分布式限流 | ⚠️ 同上。建议在 monolith 阶段先用 in-memory sharded ratelimit 优化（反馈中提到的 64-shard 优化）。Redis 不是近期的必须依赖。 |
| **Job Queue（Postgres-backed）** | 文件预览/转码 | ✅ 可以基于现有的 outbox 模式泛化。当前 outbox 是"写事件 + relay 处理"。泛化为通用 job queue 只需要增加 job type + scheduler。不需要引入独立的消息队列（RabbitMQ/Kafka）。 |
| **`ffmpeg`/`libvips`** | 缩略图生成 | ⚠️ 容器依赖，增加 Dockerfile 复杂度。建议：文件预览作为独立任务，不在主进程中执行——缩略图生成应该跑在独立 Worker 容器中，使用 job queue 触发。 |
| **OpenTelemetry Collector** | 追踪采样控制 | ⚠️ 当前 OTel 直接导出。生产环境建议通过 Collector 进行采样控制、批处理、多后端导出。但这不是 P0。 |

### 4.2 自建 vs 采购决策

| 场景 | 决策 | 理由 |
|------|------|------|
| 分布式限流 | **自建（基于 Redis Lua）** | `CL.THROTTLE` 是成熟模式，自制实现不到 100 行 Lua |
| 缓存 | **选型现有库（ristretto/freecache）** | 进程内缓存无外部依赖，自建错误处理不成熟 |
| Job Queue | **自建（泛化 outbox）** | 当前 outbox 模式已证明有效，自建可深度适配已有的事件类型 + handler 注册模式 |
| 文档预览 | **采购（Google Docs API / Microsoft Office Online）** | 文档格式转换（PDF、DOCX、XLSX）是成熟 SaaS 需求，自建质量难以达到 |
| 图片缩略图 | **自建（libvips）** | vips 社区成熟、性能优于 ImageMagick、Rust 绑定和 Go 绑定都可用 |
| 中文分词 | **采购型选型（zhparser PostgreSQL 扩展）** | 保持搜索在数据库层完成，不需要引入外部搜索引擎 |

### 4.3 第三方依赖的评估标准

当前 `go.mod` 有 14 个 direct 依赖——对于一个 41 RPC 的服务来说非常精简。建议设立以下采购门槛：

1. **必选型依赖**（如 `google.golang.org/grpc`、`github.com/gin-gonic/gin`）：只有标准库无法替代时才引入
2. **可选型依赖**（如缓存库 `ristretto`）：必须有 ≥1 年 GitHub 活跃 + ≥1000 stars + 无未修复安全漏洞
3. **工具型依赖**（如 `golang-migrate`）：必须 CLI 可独立使用，不强制增加部署依赖

**特别警告：** Redis 客户端选型（`go-redis/redis` v9 vs `redis/rueidis`）。`rueidis` 支持 `DoCache` 模式（客户端缓存），可以减少网络往返——但成熟度不如 `go-redis`。建议：默认用 `go-redis`，当延迟敏感时评估 `rueidis`。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0 — 安全底线 + 数据一致（当前缺口最紧急，0-2周）
P1 — 可操作性 + 合规（2-6周）
P2 — 架构演进（6-20周）
```

#### P0（立即——0~2 周）

| # | 项目 | 工作量 | 依赖 | 风险缓解 |
|---|------|--------|------|---------|
| P0-1 | gRPC 传输加固（`MaxConcurrentStreams`、`ReadHeaderTimeout`、`EnforcementPolicy`） | 1 天 | 无 | 先 stage 验证后 prod |
| P0-2 | `like_count` 原子化修复 | 0.5 天 | 无 | 低风险，已有 `ON CONFLICT` 保障 |
| P0-3 | `idempotency_keys` TTL 清洁器（+ outbox dead_letter 清洁） | 1 天 | 无 | RunCleaner 已有模式可复用 |
| P0-4 | `signing_key` 默认值拒绝 + `sso.insecure` 启动 WARN | 0.5 天 | 无 | 纯配置增强 |
| P0-5 | gRPC `RequestID` 拦截器 | 0.5 天 | 无 | 已有 HTTP 实现可移植 |
| P0-6 | `context.DeadlineExceeded` → 504/`DeadlineExceeded` 映射 | 0.5 天 | 无 | 低风险错误码修复 |
| P0-7 | SSE `last_event_id` 重连支持 | 1.5 天 | 无 | 向后兼容 |

**P0 总计：~5 天**

#### P1（短期——2~6 周）

| # | 项目 | 工作量 | 依赖 | 风险缓解 |
|---|------|--------|------|---------|
| P1-1 | **降级契约文档** `DEGRADATION_MATRIX.md` | 1 天 | 无 | 纯文档工作 |
| P1-2 | **数据生命周期策略** + 通用清洁器 | 3 天 | P0-3 | 先声明策略，后实现清洁器 |
| P1-3 | **通知偏好设置**（preferences 表 + API + Dispatcher 过滤） | 3 天 | 无 | 默认全开，向后兼容 |
| P1-4 | **管理员角色系统**（轻量，仅角色表 + 权限中间件 + 审核端点） | 5 天 | 无 | Phase 1 只做授权中间件 + 读端点 |
| P1-5 | Outbox handler exponential backoff | 2 天 | 无 | 兼容现有 `retry_count` |
| P1-6 | 搜索 `statement_timeout` + 查询长度限制 | 1 天 | 无 | 纯防护性修复 |
| P1-7 | Metrics 增强（DB 连接池、GC、SSE、存储操作） | 2 天 | 无 | 新增指标不破现有 |
| P1-8 | Docker 自动构建 CI（Goreleaser + 版本标签 + SBOM） | 2 天 | 无 | 与现有 CI 并行 |

**P1 总计：~19 天**

#### P2（中期——6~20 周）

| # | 项目 | 工作量 | 依赖 | 风险缓解 |
|---|------|------|---------|---------|
| P2-1 | **降级契约代码实现**（manager + 中间件 + 8 handler 适配 + 测试） | 12-14 天 | P1-1 | 降级中间件默认关闭，逐步开启 |
| P2-2 | **群聊 Phase 1**（conversations 表 + 历史迁移 + 双写兼容） | 10 天 | 无 | 数据迁移可回滚 |
| P2-3 | **群聊 Phase 2**（API 端点 + 成员管理） | 8 天 | P2-2 | 与旧 API 共存 |
| P2-4 | **群聊 Phase 3**（批量通知 + SSE fanout） | 5 天 | P2-3 | 扇出速率先限流 |
| P2-5 | **文件预览管道 Phase 1**（Job Queue 基础设施） | 5 天 | 无 | 基于 outbox 泛化 |
| P2-6 | **文件预览管道 Phase 2**（图片缩略图） | 5 天 | P2-5 | 缩略图失败不影响原始文件 |
| P2-7 | **缓存层 L1**（进程缓存，file + message GET） | 5 天 | 无 | 默认关闭，低风险 |
| P2-8 | **分布式限流**（Redis Lua + 可选） | 3 天 | 无 | 仅当多实例时启用 |
| P2-9 | 中文分词（zhparser 扩展） | 2 天 | 无 | 降级方案：`LIKE` 查询 |
| P2-10 | 冷启动预热策略 | 2 天 | 无 | 无副作用 |

**P2 总计：~57-61 天**

### 5.2 阶段划分与里程碑

```
M1（Week 1-2）：安全加固
  完成 P0-1 到 P0-7
  产出：gRPC 加固完成、数据一致修复完成、日志关联修复完成

M2（Week 3-6）：可操作性增强
  完成 P1-1 到 P1-8
  产出：降级契约文档、生命周期清洁器、通知偏好、角色系统 Phase 1
  评审点：决定群聊是否进入 Phase 1

M3（Week 7-14）：架构演进期
  完成 P2-1、P2-2、P2-3、P2-5、P2-7
  产出：降级代码落地、群聊基础 + API、Job Queue、进程缓存
  评审点：决定 Redis 是否需要引入

M4（Week 15-20）：功能完成期
  完成 P2-4、P2-6、P2-8、P2-9、P2-10
  产出：群聊通知、缩略图、分布式限流、中文搜索、预热
  评审点：整体架构健康度重新评估
```

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 群聊 Phase 1 历史数据迁移损坏 | 低 | 高 | 先备份、后迁移、可回滚脚本 + testcontainers 测试迁移逻辑 |
| 降级中间件引入新 panic | 中 | 中 | 默认关闭 + 灰度开启 + 降级中间件本身被 Recovery 兜底 |
| SSE last_event_id 增加了状态复杂度 | 中 | 中 | 客户端不传 `Last-Event-ID` 则行为不变（向后兼容） |
| Redis 不可用导致缓存穿透返回 stale 数据 | 中 | 低 | 缓存默认关闭；开启时设短 TTL + 配置 circuit breaker 防止雪崩 |
| 通知偏好设置与 SSH 实时推送的时序冲突 | 低 | 中 | 写入阶段过滤 + 推送阶段再过滤（双重保障） |
| 管理员权限系统误配导致越权 | 低 | 高 | 权限检查在 service 层（非 handler 层），双层验证；审计日志记录每次鉴权结果 |

### 5.4 决策树：群聊实施时机的判断标准

```
当前状态：
  └── 1:1 消息 + outbox 通知 + SSE 推送

评估标准：
  1. 当前 1:1 消息的日均活跃会话数 > 100？ → 是 → 群聊有价值
  2. 用户是否主动要求群聊？（来自产品/客服反馈） → 是 → 前期验证
  3. 现有 1:1 消息的错误率 < 0.1%？ → 是 → 核心功能稳定，可架构升级
  4. 通知系统的 SSE 推送 P99 < 500ms？ → 是 → 实时性足够支撑群聊扇出

决策：
  4 项全中 → 立即启动群聊（M2 后进入 Phase 1）
  3 项中 → 启动群聊但缩小 Phase 1 的范围（只做数据层 + 迁移，不做 API）
  ≤2 项中 → 推迟群聊到 P3，优先完成其他方向
```

---

## 附录 A：与现有路线图的映射关系

| 本报告方向 | 对应 ROADMAP 方向 | 对应 ARCHITECT_REVIEW 方向 |
|-----------|-------------------|--------------------------|
| 方向 1（群聊） | 新方向（ROADMAP 未覆盖） | Direction 1（多人群聊/频道系统） |
| 方向 2（传输加固） | 新方向（ROADMAP 未覆盖） | 反馈修正后对应"方向四" |
| 方向 3（降级契约） | 新方向 | 反馈中"方向三"的文档部分 |
| 方向 4（数据生命周期） | 新方向 | Direction 5（数据生命周期策略） |
| 方向 5（缓存层） | 新方向 | Direction 5（分布式限流+缓存层） |

本报告方向与现有分析不冲突，而是填补了 5 个具体的空白点（gRPC 传输加固被所有既有分析完全忽略）。

## 附录 B：约束合规性检查

| 约束 | 当前状态 | 方向实施是否突破？ |
|------|---------|-----------------|
| 文件 ≤ 500 行 | 已通过（HARNESS.md 强制） | 方向 1（群聊）可能产生 >500 行的文件 → 需要拆分为多个 sibling 文件 |
| 函数 ≤ 50 行 | 已通过（golangci-lint funlen） | 方向 3（降级 manager）的设计应避免胖函数 |
| 复杂读 ≤ 10 | 已通过（golangci-lint cyclop） | 方向 1 的迁移逻辑需要特别注意——数据迁移脚本不应在主库代码中 |
| 包平均 ≤ 5.0 | 当前通过 | 方向 1 的 service 包复杂性可能增加；考虑拆分为 `service/message` 和 `service/conversation` |
| if 嵌套 ≤ 3 | 需遵从 | 方向 3 的降级判断逻辑需扁平化 |
| 单文件单职责 | 已通过 | 所有方向应遵守 |
