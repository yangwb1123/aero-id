现在我已经对代码库有了全面的了解。以下是我全面的架构分析。

---

# 架构分析：aero-id

## 1. 架构评估

### 优势

**1. 严格的单向依赖关系。** 层级划分（handler → service → repository/storage/sso）得到严格执行。Handler 进行传输 I/O，service 是唯一的业务逻辑层。这种分离是过去许多生产后端崩溃的根源——它在 aero-id 中做得很好。

**2. 采用 Outbox 模式。** `internal/outbox/` 包是一个成熟的、生产就绪的事务性发件箱，具有退避重试、死信和可插拔的 Handler 链。通知以事件形式持久化到 DB，保证交付——不会在进程崩溃时丢失事件。这是许多系统遇到的问题，而 aero-id 做对了。

**3. 干净的存储抽象。** `FileStorage` 接口（`Put`/`Get`/`Delete`/`PresignGet`/`PresignPut`/`NewKey`）加上可选的 `Lister` 接口，保持后端实现独立。`Local` 和 `S3` 都是完整的实现，包括 GC 的 List 接口。对称的 `PresignGet` 和 `PresignPut` 支持直接上传——这是经过深思熟虑的。

**4. 错误处理纪律。** 从 `pkg/errors.AppError` 向上传播原始 repo 错误，由 service 包装，并由 handler 渲染——没有泄露的 SQL 错误，一致的 HTTP/gRPC 映射。

**5. 幂等性具有正确的失败开放语义。** 幂等性中间件明确设计为在存储错误时失败开放，因此数据库抖动永远不会导致写入丢失。这是一个老练的设计决策。

**6. 双传输同构。** HTTP 和 gRPC handler 共享完全相同的 service 层。这意味着认证、授权和业务逻辑在两个协议之间行为一致——这是一个重要的正确性属性。

### 局限性

**1. 缺少端点级控制下的全局速率限制。** 每个实例的令牌桶是合理的默认设置，但单一全局 `Limiter` 意味着 `POST /v1/files`（50MB 上传）和 `GET /v1/healthz` 之间的速率相同。没有路由维度意味着恶意行为者可以通过快速调用廉价的端点来排空昂贵的端点的令牌。鉴于速率限制是可选的并且被此问题削弱，它不太可能在生产中被启用。

**2. 存储故障切换不存在。** `buildStorage()` 是一个 `switch`——只选择一个后端。如果 S3 宕机，没有降级到 local。存储的冷热分层不存在。在具有 HA 要求的部署中，这是单点故障。

**3. SSE 重连溢出是静默的。** `replayMissed` 在 `limit=50` 处硬截断，没有溢出标记。如果用户在断开连接期间累积了 50 多条通知，则最旧的通知被成功重放，但**最新的通知静默丢失**——而最新的通知是最可能被用户注意到的。排序是 `created_at ASC`，这加剧了这一点：溢出会丢弃最新的条目。

**4. 发件箱轮询有 5 秒的延迟下限。** 基于轮询（`poll_interval: 5s`）意味着即使没有退避，通知也至少延迟 5 秒。对于聊天级实时性（一对一的“正在输入”或即时消息传递），这不够。但鉴于出站范围（通知，可能是 webhook），这可能是可以接受的权衡。

**5. 没有缓存层。** 没有分布式缓存。SSO 有内存缓存，但存储元数据（文件列表、消息列表）每次都命中共识。没有 `Last-Modified` / `ETag` 支持读缓存。对于具有大型文件列表或对话历史的用户来说，这会增加延迟。

**6. 自定义指标而不是 Prometheus 客户端。** `internal/pkg/metrics` 是手写的，而不是使用 `prometheus/client_golang`。这意味着丢失了标准 RED 指标（速率、错误、持续时间）的直方图/摘要，并且必须从头开始构建任何仪表板——与标准 Prometheus 导出器不兼容。

**7. 没有搜索索引。** 搜索是用于全文匹配的 SQL `LIKE` 查询。它不会大规模扩展，并且目前没有明确的架构方向来将其提取到单独的搜索服务中。

### 架构债务

| 债务 | 位置 | 风险 |
|---|---|---|
| `app.go` 文件长度 ≈ 300 行，呈增长趋势 | `internal/app/app.go` | 即将达到 500 行限制意味着即将需要重构 |
| `events.go` Handler 在 handler 层混合 SSE 渲染 + 重放业务逻辑 | `internal/handler/http/events.go` | 重放逻辑应该属于 service 层，但为了简单的流式传输方便而放在这里 |
| 通知收敛在 dispatcher 中逻辑耦合（5 分钟窗口 + 类型 + 行为者） | `internal/notification/dispatcher.go` | 收敛策略与交付逻辑交织；添加新策略需要修改 dispatcher |
| SSO 客户端配置爆炸：构造函数接收 12 个标量参数 | `internal/app/app.go:134-147` | 在添加更多选项之前应该使用配置对象 |

---

## 2. 扩展方向

### 方向 A：速率限制的路由感知分层（P1 — 高价值，低风险）

**为什么需要：** 目前，一个全局速率限制器在所有端点上等额分配一个共享的每秒请求数预算。一个写重负载行为者（发送 50MB 文件上传）与轻量级读操作（列出通知）竞争相同的令牌桶。这削弱了速率限制作为服务质量机制的有效性。在生产中，这意味着：
- 恶意行为者可以对 `/healthz`（成本 0.001 成本的请求）进行垃圾请求，以消耗昂贵的 `POST /v1/files` 端点的预算
- 管理员无法说“此端点每秒允许 5 个请求，另一个端点每秒允许 100 个”——他们只有一个全局节流阀

**核心挑战：**
1.  **键空间设计**：`(actor, endpoint_group)` 与 `(actor, method+path)`。端点粒度造成配置爆炸（30+ 个端点 × 每个配置）。端点分组（例如，“写入”、“读取”、“管理”）是正确的权衡。
2.  **配置复杂性**：如何表示分层覆盖？每个端点的 YAML 键？基于标签的系统？必须保持可管理。
3.  **gRPC 奇偶性**：HTTP 和 gRPC 必须使用相同的分层限制器实现。gRPC 拦截器方法信息路径与 HTTP URL 不同。

**预期的架构变更：**
- 引入 `ratelimit.Router` 分发器，将 `(actor, endpoint_group)` → 一个特定的 `Limiter` 实例
- 创建一个从端点路径到组的映射（共享的配置驱动映射，HTTP 和 gRPC 使用）
- 在配置中保留全局默认值，添加 `rate_limit.overrides: [{group: "file_upload", rps: 5, burst: 10}]`
- 更新两个端点（`middleware/rate_limit.go`、`grpcsrv.WithRateLimit`）以使用 Router

**对现有系统的影响：**
- `Limiter` 本身不变——只有客户端（中间件/拦截器）改变
- 配置模式增加，但向后兼容：省略 `overrides` = 保持全局行为
- 仅配置变更；零数据库变更

**前置条件：** 将 `app.go` 中的限流器构造提取到它自己的函数中（减少 `app.go` 的膨胀）。

### 方向 B：存储切换的多后端抽象（P0 — 高价值，高影响）

**为什么需要：** 所有存储请求都通过单个 `FileStorage` 实例。如果该后端宕机，文件服务就停机了。具有 HA 要求的部署需要：
- 故障切换：如果 S3 不可用，降级到本地磁盘
- 迁移：在不中断服务的情况下，在存储后端之间迁移数据
- 冷热分层：热数据在本地 SSD 上（低延迟），冷数据在 S3 上（低成本）

**核心挑战：**
1.  **一致性语义**：如果 S3 降级，故障切换到 local，然后 S3 恢复——你怎么知道哪些对象在哪个后端上？你需要一个写时路由策略或一个记录对象位置的元数据存储。
2.  **读穿与回写**：缓存在 local 上（SSD 快）但写入 S3（持久）的模式需要小心处理部分故障。
3.  **GC 交互**：`storage/gc/sweeper.go` 需要一个 `Lister`——如果对象分散在多个后端上，GC 必须联合列出它们。

**预期的架构变更：**
- 引入 `RouterStorage` 实现 `FileStorage`，持有多个后端和一个选择策略
- 策略选项：`failover`（主 + 备用）、`tiered`（尝试 local，回退到 S3）、`metadata`（从 DB 读取后端）
- `StorageConfig` 获得 `backends: [{name: "hot", type: "local", ...}, {name: "cold", type: "s3", ...}]`
- `buildStorage` 被重构以构建 `RouterStorage`（可以降低 `app.go` 的规模）
- `Local` 后端需要为嵌入/分布式部署添加可选的 S3 复制

**对现有系统的影响：**
- 向后兼容：单个后端配置保持为 `RouterStorage` 使用单个后端
- 如果使用故障切换，需要迁移来填充本地缓存

### 方向 C：SSE 重连溢出检测与恢复（P1 — 低风险，中价值）

**为什么需要：** 用户在离线期间超过 50 条通知会静默丢失最新条目。考虑到 `created_at ASC` 排序，丢失的是**最新**通知——最可能被用户看到的。这是一个数据丢失场景，目前静默发生。

**核心挑战：**
1.  **溢出信号**：`ListSince` 返回 `(notifications, nextCursor, hasMore)` 而不是在 50 条处静默截断
2.  **客户端拉取**：SSE 协议是单向的。客户端没有办法说“等一下，我还有更多”。需要混合拉-推机制。
3.  **排序问题**：`ORDER BY created_at ASC` 意味着第 50 个事件是最新的——但如果你只发送 50 条，客户端看到的是最旧的 50 条，认为它是最新的，然后忽略了第 51 条。倒序（DESC）会发送最新的 50 条，但会乱序——如果客户端在没有处理旧消息的情况下跳转到新对话，这是一个产品问题。

**预期的架构变更：**
- 将 `ListSince` 改为 `(ctx, uid, since, limit) → (notifs, nextCursor, hasMore, error)`
- 当 `hasMore` 为 true 时，在 SSE 流中包含一个特殊标记：`event: overflow\n data: {"cursor": "..."}`
- 客户端 JS 在收到 `overflow` 事件后可以调用 `GET /v1/notifications?cursor=...` 来拉取分页的历史记录
- 更简单的方法：在 SSE 流中发送 `event: missed_count\n data: 73`，这样客户端就知道它丢失了一些并可以拉取历史记录

**对现有系统的影响：**
- `MissedNotifFetcher` 接口改变——影响 mock，可以逐步更新
- 存储库查询保持相同，只有包装器签名改变
- 需要新的 HTTP 端点用于分页历史记录拉取，或复用现有的通知列表端点

### 方向 D：幂等性二级缓存 & 降级指标（P2 — 低风险，中价值）

**为什么需要：** 虽然幂等性中间件正确地在故障时失败开放，但它没有降级通知。操作员不知道幂等性存储何时损坏。此外，当 Postgres 可用时，对 Postgres 的每一次幂等性检查都会增加一次往返。内存中的二级缓存可以加快常见路径的速度（重放同一个键）并为 DB 故障提供备用。

**核心挑战：**
1.  **缓存一致性**：如果 Postgres 是真相来源，内存缓存多久可以过时？TTL 过期策略。
2.  **内存压力**：幂等性键有 24 小时 TTL。对于高吞吐量的端点，这可能会积累成为显著的内存开销。
3.  **恢复**：当 Postgres 恢复时，缓存如何过渡回正常路径？

**预期的架构变更：**
- 引入 `CachingStore` 包装器实现 `idempotency.Store`，将 `GormStore` 与 `sync.Map` 或 `freecache` 内存缓存组合
- 添加指标：`idempotency.store_errors`、`idempotency.cache_hits`、`idempotency.cache_misses`
- `GormStore` 获得可选的 `InMemoryStore`（小 map，仅用于重放已完成的记录）
- 配置：`idempotency.cache.enabled`、`idempotency.cache.ttl`

**对现有系统的影响：**
- 零 API 变更，零模式变更
- 纯添加——`Store` 接口不变
- 新配置键，新指标

### 方向 E：通知偏好与频道选择（P2 — 中价值，中风险）

**为什么需要：** 目前，所有通知类型都通过 SSE 推送给所有用户。用户无法选择退出“标记已读回执”或“评论回复”通知。从产品角度来看，这是通知系统的一个基本缺失功能。

**核心挑战：**
1.  **频道抽象**：通知目前只有 SSE。添加电子邮件/推送/短信（可能超出 aero-id 的范围——SSO 可能处理外部通知）。aero-id 应该存储偏好并选择交付频道。
2.  **偏好存储的迁移**：新表，同步。
3.  **跨实体交互**：块检查已经在 dispatcher 中。偏好检查也应该在 dispatcher 中。dispatcher 目前是扁平的——添加更多检查会使其更胖。

**预期的架构变更：**
- `preference` 包作为通知偏好的共享类型（之前已在 `app.go` 中引用）
- Dispatcher 获得 `preferenceChecker` 参数（类似于块检查器）
- 新表 `notification_preferences`（`uid TEXT, notif_type TEXT, channel TEXT, enabled BOOL`）
- API：`GET /v1/notifications/preferences`、`PUT /v1/notifications/preferences`

**对现有系统的影响：**
- 新表，新端点
- Dispatcher 获得一个额外的检查——但模式（`if blocked { return }` → `if !preferred { return }`）是相同的
- 向后兼容：空偏好 = 全部接收

---

## 3. 接口设计建议

### 关键原则

1.  **保持 `FileStorage` 接口纯净。** 目前的接口设计非常好：最小方法集、清晰的语义（`Put` 消耗一次 reader，`PresignGet` 返回 URL）、可选的 `Lister` 用于 GC。不得为路由添加扩展方法。相反，引入 `StorageRouter` 作为 `FileStorage` 的实现。

2.  **`ratelimit.Limiter` 保持纯净。** 不要用路由感知来污染它。引入一个单独的 `RateLimitRouter` 类型，持有端点组 → `Limiter` 的映射。中间件/拦截器将路由简化为组并选择限制器。关注点分离。

3.  **`Store` 接口的幂等性保持稳定。** 不要改变 `Reserve`/`Complete`/`Release`。通过包装器组合添加缓存：`CachingStore{inner: GormStore, cache: InMemoryStore}`。这与现有的 Go 组合模式很好地匹配。

4.  **发件箱 Handler 链已经正确。** `outbox.Chain` 模式（当前用于链接 dispatcher 和 webhook）应该被保留并用于任何未来的通知目标。

### 全新的抽象层

**`storage/router.go`** — `FileStorage` 实现，持有多个后端和一个选择策略：

```
type Router struct {
    backends []BackendGroup  // name + FileStorage + selector
    strategy Strategy         // "failover" | "tiered" | "metadata"
}

type BackendGroup struct {
    Name  string
    Store FileStorage
    Lister Lister        // optional; for GC
}
```

这允许 `buildStorage` 返回单个 `FileStorage`（`Router`），保持所有现有代码不变，同时支持故障切换和分层。

**`ratelimit/router.go`** — 端点感知的分发：

```
type Router struct {
    global   *Limiter           // fallback
    overrides map[string]*Limiter // endpoint group → limiter
    resolver EndpointResolver    // path → group name
}
```

### 向后兼容性

| 变更 | 向后兼容策略 |
|---|---|
| 存储路由 | 单后端配置（无 router config）→ `Router` 委托给单个后端，零行为变化 |
| 速率限制分层 | 无 `overrides` 的全局配置 → 回退到当前行为 |
| SSE 溢出 | 新的响应字段；旧客户端忽略意外的 SSE 事件类型 |
| 幂等性缓存 | 被包装器完全遮盖；`Store` 接口不变 |
| 通知偏好 | 新端点；旧客户端仍然接收所有通知 |

---

## 4. 技术选型

### 评估框架

对于 aero-id 中的任何新依赖，以下标准是硬性要求：

| 标准 | 最简门槛 |
|---|---|
| 依赖图大小 | ≤5 个传递依赖（避免 Kubernetes 式的依赖膨胀） |
| 许可证 | Apache 2.0 / MIT / BSD。没有 AGPL、没有 SSPL、没有 BUSL |
| API 稳定性 | 稳定（v1+），或广泛使用但未声明的 API |
| 与 Go 1.26+ 的兼容性 | 没有 CGo、没有汇编、没有 cgo 传递依赖 |
| 测试覆盖 | 核心路径的公开测试 |
| 大小 | 合理——没有像 `grpc-go` 的 `balancer/` 软件包那样迫使 vendor 膨胀 |

### 具体建议

| 提议的依赖 | 目的 | 替代方案 | 推荐 |
|---|---|---|---|
| `prometheus/client_golang` v1.x | 取代自定义指标 | 保留自定义 (已提交) 或使用 OpenTelemetry | **保留自定义**。现有的指标注册表工作正常，删除它会破坏许多代码。增量采用 Prometheus 格式。 |
| `alice/ratelimit` (或 fork) | 扩展速率限制 | 保留现有 | **保留现有的**。`internal/pkg/ratelimit` 是干净的，无依赖，并且工作正常。不要引入外部限制器。 |
| `hashicorp/golang-lru/v2` | 幂等性模式的内存缓存 | `sync.Map`，`freecache` | **使用 `sync.Map`**。幂等性缓存不需要 TTL 驱逐（我们已经通过 GormStore 的过期处理了）。总容量小（≤活跃用户 × 活跃键）。避免额外的依赖。 |
| `minio/madmin-go` | 用于 MinIO 的 S3 管理 API（桶健康检查、策略） | 保留现有的 | **不推荐**。S3 客户端已经可以做到这一点（`HeadBucket`、`ListObjectsV2`）。保持轻量。 |

### 自建 vs 采购的决策

| 能力 | 自建 | 采购 | 推荐理由 |
|---|---|---|---|
| 速率限制分层 | 简单：~150 行，无依赖 | 使用 envoy/gateway（需要基础设施） | **自建**。配置驱动的路由器是简单的。如果基础设施层做限流，Envoy 作为边缘网关是合理的，但作为后端服务，应用级限制器是合适的。 |
| 存储路由 | 简单：~200 行，无依赖 | 外部存储网关（MinIO 代理、SeaweedFS） | **自建**。路由器本身是轻量级的。SeaweedFS 可以处理这一点，但会引入大量的基础设施复杂性。 |
| 搜索索引 | 简单：~150 行，无依赖 | Elasticsearch、Meilisearch、Typesense | **自建**表示“LIKE 查询”并原地不动。如果搜索变得重要，计划迁移到专用搜索服务。 |
| 通知推送 | 简单：hub + SSE 目前有效 | Firebase、Pusher、WebPush | **通过发件箱处理**。对于外部推送（电子邮件、推送通知），推荐使用发件箱 + webhook。不要为推送通知引入外部依赖。 |

---

## 5. 实施路线图

### 优先级排序

| 优先级 | 方向 | 工作量 | 风险 | 依赖性 |
|---|---|---|---|---|
| **P0** | 存储故障切换（方向 B） | 1-2 周 | 中 | 需要 `Router` 抽象 |
| **P1** | 端点粒度限流（方向 A） | 3-5 天 | 低 | 无 |
| **P1** | SSE 重连溢出（方向 C） | 2-3 天 | 低 | 无 |
| **P2** | 幂等性二级缓存（方向 D） | 2-3 天 | 低 | 无 |
| **P2** | 通知偏好（方向 E） | 1 周 | 中 | 数据库迁移 |

### 阶段划分

**阶段 1（P0）：存储可靠性（第 1-2 周）**

里程碑：`storage.Router` 已实现，包含故障切换策略。配置支持多个后端。

```
第 1 周:  storage/router.go — 路由接口 + 故障切换策略
         buildStorage 重构以构建 Router
         GC sweeper 更新以联合列出多个后端
第 2 周:  migration: 配置模式从单后端变为多后端
         集成测试：通过停止 MinIO、验证本地回退来模拟故障
         需要时进行 local→S3 复制
```

风险：`Lister` 接口用于 GC——如果某个后端不支持 List，路由器必须退回到仅主后端 GC，或者跳过该后端的孤儿检测。缓解措施：向 `List` 添加一个 `context` 参数，以便它可以优雅地返回 "not supported"。

**阶段 2（P1）：服务质量（第 3-4 周）**

里程碑：速率限制器感知端点。SSE 重连在有大量消息时不会丢失。

```
第 3 周:  ratelimit/router.go — 端点组映射
         HTTP 中间件 + gRPC 拦截器更新
         配置模式更新
         单元测试覆盖率
第 4 周:  SSE 溢出：
         - ListSince 签名更改为包含 hasMore
         - events.go 发送溢出事件
         - 可选的：客户端拉取端点用于历史记录
```

风险：gRPC 和 HTTP 之间的端点组映射分歧。缓解措施：使用共享配置驱动映射（`map[string]string`——路径/方法 → 组名）。

**阶段 3（P2）：可观察性和弹性（第 5-6 周）**

里程碑：幂等性降级可观测。操作员在仪表板中看到降级指标。

```
第 5 周:  幂等性 CachingStore + 指标
         in-memory 缓存：已完成记录的 sync.Map
         GormStore 获取降级计数器
第 6 周:  指标清理：标准化 RED 指标（可选）
         文档：操作指南
```

**阶段 4（P2）：用户面对的通知（第 7-8 周）**

里程碑：用户可以控制他们收到的通知。

```
第 7 周:  notification_preferences 表 + 迁移
         偏好 repository 和 service
第 8 周:  API 端点（GET/PUT）
         Dispatcher 集成
         测试 + 文档
```

### 风险与缓解

| 风险 | 可能性 | 影响 | 缓解措施 |
|---|---|---|---|
| 存储路由器引入延迟开销 | 低 | 中 | 热路径的路由器是没有额外分配的快速调度。基准测试证明。 |
| 具有多个后端的 GC 孤儿检测过于激进 | 中 | 高 | GC 需要 `min_orphan_age` 保护。添加 `Router` 级别的“所有后端都已列出”与“仅主后端已验证”语义。 |
| 端点组限制配置复杂 | 中 | 低 | 提供合理的默认值（按有效载荷大小对端点分组：`light`、`medium`、`heavy`）。不要要求每个端点一组。 |
| SSE 溢出会导致旧客户端上的 JS 崩溃（未知的事件类型） | 低 | 中 | 浏览器 `EventSource` 忽略未监听的事件类型。仅当客户端监听 `overflow` 时才会处理。降级是优雅的。 |
| 幂等性内存缓存使过期条目的 500 键在一段时间内保持活跃 | 低 | 低 | 在 `CachingStore` 上设置 1 小时的硬 TTL。此外，GormStore `purgeExpired` 会处理数据库端。 |

### 应用程序自身的架构健康

当前的 `app.go`（~300 行）正在接近 500 行的硬限制。我建议在实施 P0 变更之前进行预防性重构：

1.  **将 `buildStorage` 提取到它自己的文件中** — `internal/app/storage.go`
2.  **将 SSO 客户端构建提取到它自己的文件中** — `internal/app/sso.go`（带有 ca. 20 个配置参数的大型构造函数）
3.  **将速率限制器构建提取到它自己的文件中** — `internal/app/ratelimit.go`

这将在开始功能工作之前将 `app.go` 减少到 ~150 行的编排逻辑，使代码库在工程约束方面保持健康。
