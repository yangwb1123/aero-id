Now I have a complete picture of the codebase. Here is the comprehensive architecture analysis.

---

# 架构分析报告：aero-id

## 1. 架构评估

### 1.1 当前架构的优势

**横向分层（Layered）结构清晰。** 代码遵循严格单向依赖：handler → service → repository/storage/sso。这个约定在实际代码中得到良好执行——`app.go` 中的构造代码明确展示了依赖注入的层次。服务层是所有业务逻辑的唯一入口，HTTP 和 gRPC 共享同一套 service 实现，这是设计文档 D2 决策的正确落地。

**选项模式的配置策略成熟。** `service/file.go` 中的 `FileOption`、`NotificationDispatcherOption`、`ServerOption` 等函数选项模式，使得扩展性良好，避免了巨型构造参数。每个选项的职责单一，且可以安全地 nil/零值传递。

**领域驱动与贫血模型的平衡。** `domain` 包只承载实体和纯函数（`CanReadFile`、`ValidateNewComment`），不注入基础设施依赖。核心的不变量（评论仅允许两层嵌套）同时在前置声明和文档中被明确提及，为未来迁移到 DDD Aggregates 保留了空间。

**幂等性设计在 HTTP 层已完善。** `router.go` 的中间件链（Auth → RateLimit → Idempotency）顺序正确：先鉴别身份，再按 uid 限流，最后对写操作做幂等。中间件通过 `idempotency.Store` 接口与具体存储解耦——这是 gRPC 层可以复用的模式。

### 1.2 当前架构的局限性

**1. 包级别职责重复——最严重的技术债**

代码库中存在两套完整的分层体系：

| 层次 | 生产路径（`internal/service/` + `internal/handler/`） | 幽灵路径（`internal/file/`、`internal/message/` 等） |
|------|------|------|
| Service 接口+实现 | `service/file.go`: `fileService` + `FileService` 接口 | `file/file.go`: `fileService` + `Service` 接口 |
| Repository 接口 | `repository/file.go`: `FileRepository` | `file/repository.go`: `Repository` |
| ShareRepository | `repository/file_share.go`: `FileShareRepository` | `file/repository_share.go`: `ShareRepository` |
| HTTP Handler | `handler/http/file.go` (344 行) | `file/handler_http.go` (315 行) |
| gRPC Handler | `handler/grpc/file.go` (201 行) | `file/handler_grpc.go` (204 行) |
| 选项集 | `WithStorageRefs`、`WithFileAuditLogger` 等 | `WithFileAuditLogger`、无 `WithStorageRefs` |

**幽灵路径的总代码量：约 3,169 行（`internal/file/` 全部 12 文件）**，其中约 70% 是与生产代码重复的逻辑。`internal/message/` 和 `internal/comment/` 各自的幽灵层约 250 行/包（仅 handler + repo 接口重复，service 层无重复）。

这种双重结构带来的问题：
- 当在 `service/file.go` 中增加 `WithStorageRefs` 选项时，`internal/file/file.go` 中不会同步更新——选项漂移已经发生（已验证）
- 认知负载：新开发者需要理解哪套代码真正在生产中运行
- 测试覆盖率虚高：`internal/file/service_test.go`（875 行）测试的是不接入生产的路径
- 维护成本线性增长：每次更新需要修改两套代码

**2. `internal/search/search.go` 的异常依赖路径**

搜索服务同时依赖两个路径的 Repository 接口：
- 生产路径：`service/search.go` 用 `repository.FileRepository`
- 幽灵路径：`internal/search/search.go` 用 `file.Repository`

这意味着幽灵路径的 `file.Repository` 接口承担了生产职责，但实际上幽灵路径的 `file.Repository` 没有任何实现被注入——`file/repository.go` 中的 `Repository` 只是接口声明，其实现是 `repository.FileRepository`（通过 GORM 实现）。二者之间没有继承或组合关系，只是巧合地接口签名兼容。

**3. 通知折叠的设计权衡未受监控**

幽灵通知行（在 collapse 窗口中重复的通知类型/actor）被明确标记为"audit trail"设计选择，代码注释确认这是故意的。但存在量化风险：
- 高频率场景（如批量消息通知）下，`collapsed_count` 持续增长，冗余行数 = 5min 窗口内的同类型通知数
- `WHERE read_at IS NULL AND collapsed_count = 0` 的查询会误报未读
- 没有清理策略（TTL 或异步合并）

**4. gRPC 与 HTTP 的横切关注点不对称**

| 关注点 | HTTP | gRPC |
|--------|------|------|
| 幂等键 | ✅ Idempotency 中间件 | ❌ 无 |
| 限流 | ✅ 路由级，Auth 后 | ✅ 拦截器级，Auth 后 |
| 请求追踪 | ✅ RequestID 中间件 | 仅 OTel tracing 隐式 |
| 权限（RBAC） | 部分路由 | ✅ PermissionInterceptor |

缺失 gRPC 幂等键是关键差异：对于支持 `POST /v1/ files` `messages` 等写操作的双协议服务，gRPC 客户端没有安全的自动重试机制。

### 1.3 架构债务分类

| 债务类型 | 严重程度 | 修复成本 | 影响范围 |
|----------|----------|----------|----------|
| 幽灵路径重复代码 | **高** | 中（删除 + 依赖修正） | 维护、新成员 onboarding |
| gRPC 无幂等键 | **中** | 低（复用 `idempotency.Store`） | 双协议一致性 |
| Outbox 死信不可见 | **低** | 低（加管理端点） | 运维可观测性 |
| SSE Hub 单实例限制 | **中** | 高（需 Redis Pub/Sub 迁移） | 水平扩展 |
| 通知幽灵行膨胀 | **低** | 低（加 TTL 清理或最终一致性合并） | 生产数据质量 |

---

## 2. 扩展方向

### 方向一：幽灵路径治理——从双重结构到单一真相源（P0）

**为什么需要：** 这不是新功能扩展，而是阻止技术债进一步积累的基础设施清理。3,169 行重复代码增加维护成本、测试误报、新开发者认知负载。不治理的话，未来每个新功能（如 AI embedding 写入、MCP server 暴露）都需要在两条路径上实现。

**核心挑战：**
- `internal/search/search.go` 对 `file.Repository` 的依赖需要迁移到 `repository.FileRepository`
- `internal/message/message.go` 和 `internal/comment/comment.go` 各自定义了 `FileRepository` 微接口（仅有 `Get` 方法），但这是合法的接口隔离，不应删除——它们属于"按需裁剪的接口"，不是重复的完整接口
- 搜索包本身是唯一"反向依赖"的消费者，其他幽灵包没有被任何生产代码导入

**预期架构变更：**
1. 删除 `internal/file/handler_http.go` `handler_grpc.go` `service_crud.go` `service_share.go` `service_upload.go`（五个文件，不含测试和接口）
2. 保留 `internal/file/file.go` 中的 `Repository` 和 `ShareRepository` 接口（如果搜索包依赖），或者将搜索包迁移到 `repository.FileRepository`
3. 测试文件对应删除或重定向到生产路径的测试

**对现有系统的影响：** 低。幽灵路径不接入生产路由，删除后唯一受影响的是 `internal/search/search.go` 的 import 路径。可以分步实施：先消除依赖，再删除代码。

### 方向二：跨实例实时推送——从进程内 sync.Map 到分布式 Hub（P1）

**为什么需要：** 当前 SSE Hub 使用进程内 `sync.Map` + per-uid 锁分段 + 16 个 buffer channel，设计精巧但仅限单实例。当 aero-id 水平扩展到 2+ 实例时：
- 用户连接到实例 A，消息分配到实例 B，SSE 推送没有到达
- 需要跨实例协调通知推送
- sticky session 可以缓解但会打破负载均衡的均匀分布

**核心挑战：**
- `hub.go` 的 per-uid 锁分段（`uidSubs.mu`）是进程内互斥，迁移后分布式锁的复杂度骤增
- 非阻塞推送（`select default: drop`）在分布式环境下更难保证至少一次送达
- buffer channel（16 个槽）的背压语义需要重新设计——分布式队列的消息过期机制
- 需要 `Pusher` 接口的适配层：当前是 `hub.Publish()` → sync.Map，未来需要 `hub.RedisPublish()` → Redis Pub/Sub 或 Redis Streams

**预期的架构变更：**

```go
// 新增分布式实现
type RedisHub struct {
    redis   *redis.Client
    local   *Hub  // 降级回退
}
func (h *RedisHub) Publish(uid, eventType, data string) {
    // 1. 发送到 Redis Pub/Sub
    h.redis.Publish(ctx, "sse:"+uid, payload)
    // 2. 同时保持本地推送，抑制双重推送
}
```

**对现有系统的影响：** 中高。`Pusher` 接口不受影响，但 `hub.New()` 需要变成可配置的工厂函数。`notification.go:233` 的同步发布逻辑距离分布式发布语义有一定距离——需要引入至少一次送达保证，这意味着需要对 outbox 事件和 SSE 推送做去重。

### 方向三：gRPC 幂等键——补齐双协议一致性缺口（P1）

**为什么需要：** 当前 HTTP 有完整的 Idempotency 中间件链（`idempotency.Store` + `middleware.Idempotency` + TTL 清理），但 gRPC 完全没有。对于生产环境通过 gRPC 调用 aero-id 的内部服务，`CreateFile` `SendMessage` `CreateComment` 等写操作没有安全的自动重试语义。这在实际运维中迟早会遇到——尤其是当 SSO 认证短暂超时或网络抖动时。

**核心挑战：**
- gRPC 的拦截器模式下，幂等键可以通过 UnaryServerInterceptor 统一注入，比 HTTP 更简洁
- 需要客户端传递幂等键：通过 gRPC metadata 的 `Idempotency-Key` header
- 现有的 `idempotency.Store` 可以直接复用，无需存储层改造
- 需要确认：幂等键应在认证拦截器**之后**（key 才与 uid 绑定），但与其他拦截器的顺序需要明确：`Auth → RateLimit → Idempotency → Handler`

**预期的架构变更：**

```go
func IdempotencyInterceptor(store idempotency.Store, ttl time.Duration) grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
        key := metadata.Value("idempotency-key") // 来自 metadata
        // 读/写 store 决定是否执行
    }
}
```

写入 `interceptors` 切片时，放置在 AuthInterceptor 之后、handler 之前：

```go
interceptors := []grpc.UnaryServerInterceptor{
    RecoveryInterceptor(log),
    AuthInterceptor(validator),
    IdempotencyInterceptor(idemStore, idemTTL),  // NEW
    // RateLimit, Permission ...
}
```

**对现有系统的影响：** 低。只添加拦截器，不改变已有 handler。已有 HTTP 的 `idempotency.Store` 接口可以直接复用。gRPC metadata 的传递方式与 HTTP header 对应性良好。

### 方向四：Outbox 管理面——死信可见性与手动干预（P2）

**为什么需要：** 当前 outbox 的死信行（`dead_letter = true`）仅能通过 SQL 直接查看。当通知推送因 SSO 超时、webhook 目标不可达、或数据不一致等原因反复失败时，运维人员无法：
1. 查询哪些事件正在积压或已死信
2. 手动重试某个死信事件
3. 批量清理已处理的死信
4. 配置死信告警阈值

这是一个运维可观测性的缺口，但不是紧急任务——大部分 outbox 事件的处理是瞬时的，死信数量在正常运行时接近于零。但一旦生产环境中出现 SSO 故障或 webhook 目标变更，运维团队就需要这个接口。

**核心挑战：**
- 需要定义管理面 API 的鉴权级别：仅限 admin 角色（已有 `adminSvc` 和 `PermissionInterceptor`）
- 死信重试本质上需要回放事件——重新插入到 outbox 表（`retry_count = 0` `dead_letter = false`）或直接调用 handler
- 批量清理需要谨慎：删除死信意味着丢失审计记录

**预期的架构变更：**
- 在 `internal/outbox/` 中添加管理接口：`ListDeadLetters` `RetryDeadLetter` `PurgeDeadLetters`
- 通过 `internal/handler/grpc/admin.go` 或新增管理端点暴露
- 复用现有的 RBAC：`admin.ListDeadLetters` → `PermAuditRead`

**对现有系统的影响：** 低。outbox 表结构不变，relay 逻辑不变。新增管理端点和仓库方法只读/插入。

### 方向五：通知幽灵行治理——从"设计权衡"到"可控语义"（P2）

**为什么需要：** 当前幽灵通知行的存在被标记为"harmless"，但在以下场景中确实存在风险：
- 用户数 10 万+ 时，5min collapse 窗口 + 高频场景（如群消息）→ 每天数千冗余行
- `WHERE read_at IS NULL` 查询误报未读，用户可能看到"未读"数量与实际不一致
- `collapsed_count` 持续增长但合并行本身没有索引或 TTL

**核心挑战：**
- 删除旧合并行需要小心：它们承载着 `collapsed_count` 的状态——单纯的 `DELETE` 会丢失合并计数
- 最终一致性合并需要异步 worker 或 trigger，但当前没有 worker 基础设施
- outbox 的 relay 已经在事件循环中，可以考虑在 relay 中合并

**预期架构变更——三个可选方案：**

**方案 A（低成本）**：已有的 notification 查询中增加 `WHERE collapsed_count = 0 OR created_at > now() - interval '1h'` 过滤。不需要表结构变更，仅改查询逻辑。

**方案 B（中等成本）**：在 relay 的 `HandleEvent` 中，当发生折叠时，不是插入新行 `n` 后再更新 `existing`，而是改为更新 `existing` 的 `collapsed_count` 和 `created_at`，完全不插入新行。损失了审计追踪，可以另外加审计表。

**方案 C（高成本）**：在幽灵行插入后，运行异步 worker 定期清理超过保留期的幽灵行（`DELETE WHERE collapsed_count > 0 AND created_at < now() - interval '7d'`）。

**对现有系统的影响：** 低~中。方案 A 零风险，方案 B 改变语义，方案 C 需要 worker 框架。

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

**1. Repository 接口应当集中声明，不分散重复**

当前问题：`internal/file/repository.go` 声明了 `Repository` 接口（12 方法），`internal/service/file.go` 中依赖的是 `repository.FileRepository`（生产路径）。这意味着同样的契约在两处声明——一处是"幽灵"（`internal/file/repository.go`），一处是真实（`internal/repository/file.go`）。

**建议**：统一将 Repository 接口定义在 `internal/repository/` 包中。`internal/search/` 应当导入 `repository.FileRepository`，而非 `file.Repository`。这修复了幽灵路径的依赖问题。

**2. 微接口（Mini Interface）是好的，但应当明确标注用途**

`internal/message/message.go:54` 定义了 `FileRepository` 微接口（仅 `Get` 方法），这是合法合理的——它精确表达了 message 包对 file 层的依赖需求。但需要在注释中说明：该接口适配 `repository.FileRepository` 并通过 `NewMessageService` 的构造函数注入。

**建议**：为每个微接口添加 `//go:generate` 或显式断言，确保编译期检测到生产类型满足接口。

```go
// FileRepository is the subset of the file store needed for message file
// validation. Satisfied by *repository.FileRepository in production.
var _ FileRepository = (*repository.FileRepository)(nil)
```

**3. Service 接口应当保持职责单一，不混合层**

当前 `service.FileService` 接口（`internal/service/file.go`）包含 11 个方法——涵盖了 CRUD、共享、预签名上传、完成上传等。这是合理的，因为它们都属于"文件"这个领域聚合。但 `internal/file/file.go` 中的幽灵 `Service` 接口与生产路径几乎完全一致。

**建议**：当治理幽灵路径时，删除幽灵的 Service 接口和实现。生产路径的 Service 接口是唯一的业务契约。

### 3.2 是否需要引入新的抽象层

**不需要**。当前架构的分层（handler → service → repository/storage/sso）是清晰的。问题不在于缺少抽象层，而在于存在**多余的重复层**（幽灵路径）。

唯一可能值得引入的抽象是：

**Observability Middleware Adapter**：当前 gRPC 和 HTTP 各自实现了自己的 metrics/tracing 截取逻辑。可以抽取出一个与传输无关的中间件接口，统一横切关注点的声明。但这是一个 P2 优化，不是当前要务。

### 3.3 向后兼容性策略

- **幽灵路径清理**：不涉及 API 变化，无兼容性问题
- **分布式 Hub**：`Pusher` 接口不变，运行时替换实现即可
- **gRPC 幂等键**：新增的拦截器需要在 proto 层面为请求添加 idempotency_key 字段（可选），不匹配已有客户端的不受影响
- **Outbox 管理 API**：新增端点，不影响现有行为

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

| 扩展方向 | 需要的新技术 | 评估 |
|----------|-------------|------|
| 幽灵路径治理 | 无 | 纯重构 |
| 分布式 Hub | **Redis**（Pub/Sub 或 Streams） | 需要，但需评估 Redis 运维成本 |
| gRPC 幂等键 | 无 | 复用 `idempotency.Store` |
| Outbox 死信管理 | 无 | 纯 API 扩展 |
| 通知幽灵行治理 | 无 | 纯修改 |

**Redis 引入决策**：目前 SSE Hub 单实例运行。如果未来上线多实例部署（实例数 ≥ 2），则需要 Redis 做跨实例事件分发。但在此之前，单实例 Hub 完全够用。建议在确实遇到水平扩展需求时再引入 Redis，不做提前投资。

### 4.2 第三方依赖评估标准

现有依赖列表是合理的。对于任何新增依赖应采用以下标准：

1. **必要性**：现有代码不能以合理复杂度实现吗？
2. **维护活跃度**：至少最近 6 个月有更新，有合理数量的维护者
3. **API 稳定性**：不依赖实验性 API 或快速迭代中的库
4. **License 兼容性**：MIT / Apache 2.0 优先
5. **测试覆盖**：依赖本身有足够的测试

### 4.3 自建 vs 采购决策依据

不存在"采购"场景。所有扩展方向（除 Redis 外）都是纯代码层面的。Redis 作为基础设施组件可自建（内部运维）或使用云服务商托管。

---

## 5. 实施路线图

### 优先级划分

| 优先级 | 方向 | 预估工作量 | 风险等级 |
|--------|------|-----------|---------|
| **P0** | 幽灵路径治理 | 2-3 人日 | 低 |
| **P1** | gRPC 幂等键 | 1 人日 | 低 |
| **P1** | 分布式 Hub（Redis） | 5-8 人日 | 中 |
| **P2** | Outbox 死信管理 | 2-3 人日 | 低 |
| **P2** | 通知幽灵行治理 | 1-2 人日 | 低 |

### 阶段划分

**Phase 1：基础设施清理（P0 → 1 周）**

**里程碑 1：幽灵路径消除**
- 任务 1.1：将 `internal/search/` 从 `file.Repository` 迁移到 `repository.FileRepository`
- 任务 1.2：逐一验证 `internal/message/` `internal/comment/` `internal/notification/` 的反向依赖（仅搜索包）
- 任务 1.3：删除所有幽灵的 handler、service、upload 文件
- 任务 1.4：删除对应的测试文件，或将其迁移到生产路径
- 任务 1.5：更新 `make check-file-sizes` 阈值（文件数减少，但现有文件大小可能超限）

**验证门禁：**
- `make test && make lint` 通过
- `go build ./...` 通过
- `internal/file/` 的 `Repository` 接口只被测试和搜索包引用

**Phase 2：双协议一致性增强（P1 → 3-5 天）**

**里程碑 2：gRPC 幂等键上线**
- 任务 2.1：实现 `IdempotencyInterceptor`，复用 `idempotency.Store`
- 任务 2.2：确认拦截器链顺序：`Recovery → Auth → Idempotency → RateLimit → Permission → Handler`
- 任务 2.3：更新 proto 文档，标记幂等键参数为 optional
- 任务 2.4：为内部 gRPC 客户端（SSO 调用已存在）添加幂等键支持

**里程碑 3：分布式 SSE Hub 准备（与 Phase 2 可并行）**
- 任务 3.1：提取 `Pusher` 接口的完整单元测试（模拟 Redis 故障场景）
- 任务 3.2：实现 `RedisPusher`，使用 Redis Pub/Sub（或 Streams 做持久化）
- 任务 3.3：添加配置项 `hub.type = local | redis`，在 `app.go` 中工厂创建
- 任务 3.4：验证单实例降级路径（Redis 不可用时回退到 local Hub）

**验证门禁：**
- gRPC 幂等键集成测试（重复请求返回缓存响应）
- SSE Hub 切换配置后通知推送语义不变

**Phase 3：运维能力提升（P2 → 2-3 天）**

**里程碑 4：Outbox 管理面**
- 任务 4.1：实现 `outbox.ListDeadLetters` `RetryDeadLetter` `PurgeDeadLetters`
- 任务 4.2：通过 gRPC AdminService 暴露（权限要求：`PermAuditRead`）
- 任务 4.3：如果时间允许，添加死信告警：relay 的 metrics 增加 `dead_letter_total` 计数器

**里程碑 5：通知幽灵行清理**
- 推荐采用方案 A（最低成本）：修改 notification 查询，加入 `collapsed_count` 过滤
- 在 notification 表上加 `idx_notifications_collapsed_created` 复合索引
- 添加 `make check-notification-ghost-rows` 脚本做定期诊断

### 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 幽灵路径删除后的隐式依赖 | 低 | 高 | 在删除前做全量搜索 + 编译确认；分步删除（先迁移导入，再删除文件） |
| Redis 引入后的运维复杂度 | 中 | 中 | 开始时 Retain local Hub 降级路径；Redis 仅作 SSE 消息分发，不存储状态 |
| gRPC 幂等键与已有客户端不兼容 | 低 | 低 | 幂等键作为可选的 metadata header，不设置不影响 |
| 通知幽灵行的"harmless"假设在超大规模下不成立 | 低 | 中 | Phase 2 优先实现方案 A 过滤，后续视生产数据量决定是否升级到方案 B 或 C |
| 分布式 Hub 的消息去重 | 中 | 中 | 在 SSE 事件中内嵌 `ID`（notification_id），客户端可以自行去重；服务端不做严格精确一次交付 |

### 总体路线图

```
Phase 1 (Week 1):     ██████████░░░░░░░░░░  幽灵路径治理
Phase 2 (Week 2-3):   ██████████████░░░░░░  gRPC 幂等键 + 分布式 Hub 就绪
Phase 3 (Week 4):     ████████░░░░░░░░░░░░  Outbox 管理面 + 通知幽灵行
                     └─────────────────────── 时间线：约 4 周
```

**总结**：aero-id 的架构核心设计是健康的，幽灵路径是最严重的当前技术债。治理幽灵路径应该优先于任何新功能开发——它修复了 3,169 行重复代码的维护负担，消除了测试覆盖率的统计数据虚高，并为未来的 AI embedding、MCP server 等扩展方向铺平了道路。gRPC 幂等键是第二优先级的补全——它不属于新功能，而是已有设计（HTTP Idempotency）在另一个传输协议上的缺失补全。
