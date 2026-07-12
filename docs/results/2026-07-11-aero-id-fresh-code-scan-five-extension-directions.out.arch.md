# 架构分析报告：aero-id

---

## 1. 架构评估

### 1.1 当前架构优势

**分层清晰，依赖单向。** `handler → service → repository` 的严格单向依赖是正确且可持续的架构。这使得：

- **业务逻辑可集中测试**——引入 `mocks` 后，service 层的纯逻辑测试不依赖数据库，测试速度很快。这是微服务架构中非常宝贵的属性，因为 aero-id 已经是多传输层（HTTP + gRPC），如果业务逻辑散落，跨传输层的表现不一致将是灾难。
- **替换存储后端成本可控**——`FileStorage` 接口的存在使存储迁移（local ↔ s3）只需修改 `app.go` 中的构造调用。当前是"可替换"而非"已替换"，但接口抽象到位。
- **新入口点成本低**——未来如需加入 WebSocket stream 或 Kafka consumer，仅需增加 handler 层，复用完整 service 逻辑。

**GORM 与裸 SQL 的平衡。** 项目使用 GORM 管理模型生命周期（迁移、软删除、预加载），但列表查询使用 `WHERE id < ? ORDER BY id DESC` 的裸 SQL 风格。这不是混用，而是成熟判断——ORM 擅长单行 CRUD，分页查询需要精确控制执行计划。这个做法应保持。

**UUIDv7 作为排序键 + 游标。** UUIDv7 的可排序性（时间前缀）使游标分页不需要额外的 `created_at` 索引，减少了索引维护开销。这是设计亮点。当前问题——cursor 不含 expiry——是工程遗漏而非设计缺陷。

### 1.2 当前架构局限性

**中间件链缺乏分组感知。** 当前限流中间件是全局的，路由是 `router.go:88` 中静态构建的。这意味着：

- 无法按 HTTP verb 或资源类型差异化限流
- 无法精细控制认证强度（某些路由需强力认证，某些可免 token）
- 路由定义的"所有权"不清晰（是 handler 的职责还是 app.go 的职责？）

这实际上是 `app.go` 组合根层的一个职责边界模糊。路由构建和中间件编排本应是两个独立的关注点，但当前混在 router 组装中。

**死代码带来的编译依赖成本。** 文档指出了 1030 行死代码在编译路径中。这不仅仅是维护成本——它制造了**虚假的循环依赖风险**。`internal/search` 导入了 `internal/{comment,message,file}`，如果这些包之间本不需要互相感知，那么日后的重构就会被这些存量 import 拖累。`internal/search` 的存在使开发者不敢轻易改变 `comment`/`message` 的公共接口，因为"可能会破坏搜索"——尽管搜索代码从未被调用。

**返回值型错误体系的代价。** 项目使用 `pkg/errors.AppError` 包裹错误并在 handler 层统一渲染。这是当今的主流做法（类似 Go 1.24 的 error wrapping + gRPC status codes 的桥接），但有一个容易被忽略的成本：**service 层返回 `AppError` 意味着 service 层需要感知 gRPC 状态码和 HTTP 状态码**。`AppError` 内部携带了 `gRPC Code` + `HTTP Status` 两个字段。这违反了 service 层不应感知传输协议的理想原则。

**权衡**：如果完全解耦，需要在 service 层定义纯业务错误码，再到 handler 层做两次映射（业务码 → HTTP + gRPC）。这增加了映射代码量，但降低了 service 层对传输协议的感知。考虑到当前仅有两个传输层，当前的耦合是可接受的。如果将来加入第三个传输（例如 WebSocket/gRPC-stream），则应解耦。

### 1.3 关键设计决策评估

| 决策 | 评价 | 风险 |
|------|------|------|
| SSO 外置身份 | ✅ 正确。账户体系适合独立演进 | SSO 的 gRPC 延迟成为所有请求的前置延迟；TokenCache/UserCache 必须准确 |
| 多传输层共享 service | ✅ 正确。确保行为一致性 | 需要额外测试覆盖 gRPC 和 HTTP 的等价性 |
| GORM + 软删除 | ✅ 合理。小型项目 ORM 的收益 > 成本 | 大量行占用空间；需要在 `README` 声明清理策略 |
| 死代码未清除 | ❌ 不应当。技术债应记录并排期 | 当前是搜索包，门槛低；长期不清会导致"不敢删"文化 |
| 单全局 token bucket | ⚠️ 可接受短期。投入前需评估流量特征 | 50qps 以上需差异化限流；当前也许够用 |

---

## 2. 扩展方向

### 方向 A：查询层抽象 — 从裸露 GORM 到 Repository 接口 + 查询对象

**为什么需要：** 当前 repository 层直接暴露 GORM 方法签名，service 层通过 `repo.Where(...).Find(...)` 等价调用。这让 repository 层失去了约束能力——你无法在 repo 层做全局的 `statement_timeout` 注入、查询日志、或者慢查询监控，因为这些配置需要从 `*gorm.DB` 的 chain 方法入手。

更根本的问题：**如果将来需要迁出一个实体到独立的数据库服务，当前架构需要改动所有引用该 repository 的 service。**

**核心挑战：** GORM 的 chain API 设计本身就是为了"方便"而非"可替换"。如果用标准 `Repository[T]` 接口替代，会损失 GORM 的动态查询能力（预加载、count+find 组合等）。

**架构变更：**

```
当前: service → repository(struct with *gorm.DB) → GORM → Postgres
未来: service → Repository[T] interface → GORMRepository[T] → GORM → Postgres
                                      └→ RPCRepository[T] → gRPC → remote service
```

**对现有系统影响：** 影响中层。repository 接口需要定义全部 CRUD + 列表 + 关联操作。work estimate：每个领域 0.5 天 × 4 个域（file/comment/message/block）= 2 天。可与方向五（statement_timeout）合并实施，因为新的 repository 基类可以统一注入超时。

**风险点：** 过度抽象。如果项目规模在 6 个月内不会超过 10 张表，"先用 GORM + 显式 repository" 是务实做法。这个方向的优先级应低于方向五和方向三。

---

### 方向 B：请求生命周期上下文 — Context Middleware 统一化

**为什么需要：** 当前代码中，service 层需要从前端（HTTP/gRPC handler）接收解析后的 `uid` 和 `actor` 等认证上下文。这导致两个问题：

1. **gRPC 和 HTTP handler 都需要解析 token → 重复代码**。当前逻辑分别在 `internal/handler/http/middleware/auth.go` 和 `internal/handler/grpc/interceptor.go`。
2. **Service 层方法签名中存在隐式的上下文参数**——每个 service 方法接收 `ctx context.Context` 但不声明它期望 `x-actor-uid` 存在于其中。调用方是否注入没有编译期检查。

**核心挑战：** Go 的 `context.Context` 是"无类型、隐式、运行时"的。无法强制要求 context 中包含某个值。有两种解决路径：

- **路径 B-1：结构体参数**。将 `ctx context.Context` + 认证信息封装为一个显式类型 `type QueryContext struct { Ctx context.Context; ActorUID string; TraceID string }`，每个 service 方法接收 `QueryContext` 而非 `context.Context`。代价是所有调用方必须构造 `QueryContext`，且 gRPC 拦截器和 HTTP 中间件的解析结果有了类型保障。

- **路径 B-2：Context 键定义标准化**。定义全局 context key 并写死提取逻辑，用单元测试覆盖中间件的注入一致性。这是 Go 社区的主流做法（如 gRPC 的 `metadata`、net/http 的 `context.WithValue`），缺点是缺少编译期检查。

**建议：** 采用路径 B-2 而非 B-1。原因：B-1 会使所有 caller 签名变化，破坏第三方客户端嵌入（如其他 Go 微服务调用 gRPC 时需要构造 `QueryContext`），而 B-2 通过 linter 规则（禁止从 context 读取未注入的 key）可以实现等价效果。

**架构变更：** 在 `internal/middleware` 或 `internal/pkg/ctxkeys` 中定义标准化的 context key 和提取函数，然后在双方 handler/transport 中统一使用。Service 层不做改变——它已经从 `ctx context.Context` 中读取值，只是需要确保所有读取路径是单一的。

**对现有系统影响：** 低。涉及文件数约 5-8 个（auth.go, interceptor.go, service 中各方法的上下文提取点）。是纯重构，不改变行为。

---

### 方向 C：可观测性基座 — 结构化日志 + 请求级追踪 + DB 指标

**为什么需要：** 当前代码有 `pkg/logger` 但没有在根 context 中注入 trace ID。当多个 service 方法调用时，日志行无法关联到同一个请求。方向五建议的 DB 连接池指标也需要 `/metrics` 端点。实际上，**可观测性是一个横切关注点，应该在基础设施层面做好，而不是在每个 service 方法里手工注入**。

**具体而言：**

- 每个请求应获得唯一的 traceID（UUIDv7 即可，无需分布式追踪系统）
- `context.Context` 应携带 traceID，在中间件层注入
- Logger 应自动提取 traceID，无需手动传递
- `/metrics` 应暴露 go runtime 指标 + DB 连接池状态 + HTTP handler 延迟分位数
- 所有 handler 应在返回时记录 `method path status latency`

**核心挑战：** 这不是一个"方向"，而是一个基础设施决策。如果当前团队习惯打印日志时忘记填 traceID，那么引入自动注入后是降低负担。但如果日志库不够灵活（当前 `pkg/logger` 基于什么库），可能需要换库。

**建议的改动层级：** 根框架层。在 `app.go` 的中间件链中加入 `traceID` 注入 + `logging` 中间件。repository 层的 `*gorm.DB` 应当带有 `InstanceSet("logger", ...)` 注入 traceID 到 SQL 日志。

**对现有系统的影响：** 中等。logger 签名可能改变，所有日志调用点需要检查。但每个日志调用仅涉及文件内 1-2 行变动，影响面可控。建议在方向五实施后做（引入 statement_timeout 后，更需要日志关联到慢查询）。

---

### 方向 D：复合事件的异步处理 — 从同步 RPC 到异步消息

**为什么需要：** 当前 aero-id 中，创建 comment 时通知消息接收方是同步的（通过 service 中调用 `sso.ResolveUsers` 和插入消息记录）。如果 SSO 响应慢或消息接收方发现 DB 连接池压力大，文件/评论创建的延迟会升高。

但更重要的是**功能角度**：当前文档没有提及任何"上传完成后执行工作流"的场景，比如文件上传后自动生成缩略图、审核标记后的过期清理。这些都是异步事件。

**核心挑战：** 引入消息队列是架构的跳跃。从无 MQ 到有 MQ，需要解决：
- 可靠性（至少一次投递 / 至多一次）
- 重试与死信
- 生产者和消费者的部署拓扑
- 事务性问题（"comment 入库"与"发送通知"不同步）

**建议的架构变更：** 引入一个**轻量级事件总线**而不是完整的消息队列。可以在同一个进程内的 goroutine 中：

```
service.CreateComment → domain.Event{CommentCreated, ...} → eventbus.Publish(ctx, event)
                                                            └→ goroutine pool 异步处理
```

这个阶段不需要 Kafka/RabbitMQ。Go 的 `chan` 加上 ttl-capped retry 就足够应对"生成通知"这类延迟不敏感场景。

只有当需要**跨实例、跨服务**的事件（如文件上传后通知图像处理服务）时，才考虑引入队列。

**对现有系统影响：** 中等。需要新增 `internal/eventbus` 包，修改 comment/message service 在写入后发布事件。但注意：这个方向不应该在 P0/P1 之前做。它应该排在方向三、方向五之后。

---

### 方向 E：数据平面与控制平面分离

**为什么需要：** 当前 `/v1/*` 路由混合了数据请求（获取文件、搜索对话）和管理操作（可能不会有，但如果报告功能需要审核则会有审核）。审计日志、速率限制配置、删除策略等运维功能如果嵌入到同一个 API 中，会导致：

- 权限模型膨胀（用户既需要数据权限，又需要管理权限）
- 配置变更需要重启服务
- 难以给第三方开发者提供"管理 API"

**核心挑战：** aero-id 当前没有管理 API 的需求。但如果按照 AGENTS.md 的定位——"Go service for data owned by accounts"——那么在将来接入更多客户端（移动端、web app、第三方开发者）时，管理面的需求是必然会出现的。

**建议：** 这不是当前的优先级。但需要在架构中留出空间——例如在路由层面预留 `/admin/*` 或 `/v2/admin/*` 的子路由组，确保不跟 `/v1/*` 的数据路由冲突。控制面板的功能可以通过环境变量或启动参数禁用。

**实施形态：** 轻量开始。一个单独的管理路由组 `/v1/admin/*`，使用不同的中间件（admin 认证，而非普通 token 认证）。初始功能只需要一个端点：`GET /v1/admin/stats` 返回 DB 连接池状态和请求计数。这可以与方向五的 metrics 端点合并设计。

---

## 3. 接口设计建议

### 3.1 关键抽象原则

**Repository 接口当前的隐式约束**应当被显式化。具体来说：每个 repository 方法如果接收 `page` 参数，应当对传入的 cursor 做输入校验（格式、长度），然后在 repo 层将其转为 SQL 条件。当前 `PageFromQuery` 在 handler 层做解析，cursor 是一个裸字符串被传入 repo——这意味着：

- repo 无法验证 cursor 格式（是否是有效的 UUIDv7 哈希？）
- repo 无法给 cursor 注入过期检查
- handler 层的 cookie/query 参数解析与 repo 层的 SQL 构造之间有隐式契约

**建议**：将 `Page` 的定义泛化，使其包含 **Repository 层需要的全部翻页参数**，而不是一个裸 `cursor string`。增加：

```go
type Page struct {
    Cursor    string // opaque token, repo will decode
    Limit     int
    // New fields for direction 1:
    CreatedAt time.Time // decoded from cursor or zero
}
```

或者更进一层：将 cursor 设计为**可序列化结构体**而非字符串，在 handler 层序列化（Base64/hex），在 repository 层反序列化。这样 cursor 可以携带过期时间和上一个查询的过滤条件摘要。

### 3.2 是否需要新的抽象层

**当前不需要。** 项目的分层（handler/service/repository/storage/sso）已经覆盖了所有关注点。但有一个值得注意的弱点：

**`internal/app` 正在变成一个"上帝包"**。`app.go` 目前做以下所有事情：
- 初始化所有基础设施（DB、SSO client、storage backend）
- 创建所有 repository
- 创建所有 service
- 构建所有 HTTP 路由
- 注册所有 gRPC handlers
- 启动限流器
- 配置所有中间件
- 启动 metrics 端点

如果 app.go 超过 300 行（当前可能在 200-250 之间），考虑将**路由构建**和**中间件配置**提取到单独的工厂函数中。

### 3.3 向后兼容性策略

对于方向三（对话内搜索），接口变更方案只有一种合理的：**在现有 `SearchRequest` 中增加可选参数 `conversation_with`**。老的 SDK/客户端如果不传此参数，行为保持不变（全局搜索）。这是完全的向后兼容。

对于方向四（per-endpoint 限流），不应改变任何接口签名。限流是基础设施行为，是中间件配置。但**需要在新引入的限流中间件中记录 `X-RateLimit-*` headers**，使得客户端可以选择适配限流行为。

对于方向二（API 生命周期），清理死代码时**不应改动公共接口**。死代码指无任何引用的代码，删除它们不影响任何实际调用路径。应使用 grep 的 `-n` 模式确认每个函数的调用方。

---

## 4. 技术选型

### 4.1 当前评估：所需的新组件

| 需求 | 推荐方案 | 备选 | 决策依据 |
|------|---------|------|---------|
| Per-endpoint 限流 | gin 原生 middleware + 路由分组 | Redis 滑动窗口 | 0 外部依赖；当前部署只是单实例，无需分布式限流 |
| Cursor 签名/过期 | HMAC-SHA256 + Base64 编码 | JWT | HMAC 无需新依赖，现有 `SIGNING_KEY` 可用；JWT 的重量在这里没必要 |
| Statement timeout | GORM callback hook | `sql.Conn` 的 `Exec("SET LOCAL")` | callback hook 不需要修改每个 repository 方法 |
| Trace ID | UUIDv7 写入 context | OpenTelemetry SDK | 需要零外部依赖；UUIDv7 生成器已存在 |
| 异步事件 | goroutine + channel pool | Redis PubSub / Kafka | 当前单实例场景不需要持久化事件 |

### 4.2 第三方依赖评估原则

当前 `go.mod` 已有核心依赖：GORM、protobuf、gin。在引入新依赖前，应遵循以下评估顺序：

1. **是否真正需要**——这个功能用当前依赖是否可实现？HMAC 用 Go 标准库 `crypto/sha256` 即可，无需加依赖。
2. **是否需要分布式能力**——当前是单实例，`channel` 就够。如果将来多实例，Redis PubSub 或 NATS 才值得引入。
3. **社区健康度**——stars、issue 响应速度、Go 版本兼容性。Go 社区偏爱"小而明"的库。
4. **升级风险**——依赖的版本约束是否会阻塞 Go 版本升级？例如 `protoc-gen-go` 需要保持跟 `google.golang.org/protobuf` 的版本对齐。

### 4.3 自建 vs 采购

当前所有扩展方向都不需要"采购"。需要决策的是"自建 vs 使用开源库"：

- **Per-endpoint 限流**：自建 1 天 vs 引入 `github.com/ulule/limiter/v3` 等库。建议**自建**，因为 aero-id 只需要一个简单的 token bucket 挂在 gin 中间件上，引入外部限流库会带来配置复杂度。
- **异步事件总线**：自建 vs 引入 `github.com/asaskevich/EventBus`。同样建议**自建**，因为需要的功能极少（类型安全的事件 + goroutine pool），外部库的泛型支持在 Go 1.26 下可能不够好。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0 (必须立即做) ── 方向五: Statement-level timeout
P1 (高价值，尽早做) ── 方向三: 对话内搜索
P1 (高价值，尽早做) ── 方向四: Per-endpoint 限流
P2 (正确性问题) ── 方向一: Cursor 分页改进 (含过期检查)
P2 (平台健康) ── 方向二: 死代码清理 + API 生命周期管理
P3 (基础设施) ── 可观测性基座 (traceID + metrics)
P4 (未来) ── 异步事件总线
```

### 5.2 阶段划分

**Phase 1：稳定性基座（1 周）**
- 方向五：GORM callback hook 注入 `SET LOCAL statement_timeout = '5s'`
- 方向四（Step 1）：将全局限流从 `app.go` 提取为可路由分组配置的中间件，但先保持单 bucket
- 为新限流配置的 YAML 字段预留（enabled=false 时不生效，不影响当前行为）

关键产出：`/metrics` 增加 DB 连接池指标（`db_conn_pool_open{state="in_use|idle|wait"}`）

**Phase 2：对话内搜索 + 死代码清理（1 周）**
- 方向三：Service 层 `Search` 接口增加可选参数，repository 复用 `ListConversation` 的 SQL
- 方向二：删除 `internal/search/` 整包，标记被引用但无实际调用点的 `service.go` 为 `// Deprecated`
- 方向一：修改 `Page` 结构体，cursor 嵌入 HMAC 签名 + 5 分钟过期时间戳

**Phase 3：精细化限流（2 周）**
- 方向四（Step 2）：实现 YAML 配置驱动的路由分组限流
- 在每个限流中间件后设置 `X-RateLimit-Limit / Remaining / Reset` headers
- 添加 `RateLimit-Reset` 后，HTTP handler 层不需要感知——都是中间件层的工作

**Phase 4：可观测性 + 异步事件（2 周）**
- TraceID 注入中间件（复用 UUIDv7 生成器）
- Logger 改造为自动提取 traceID
- 轻量事件总线（goroutine pool, 容量 100, backpressure 策略：dropping oldest）

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Phase 1 中 `SET LOCAL` 在连接池复用下不生效 | 中 | 高（超时不生效） | 在 `db.go` 的 `ConnMaxLifetime` 回调中重置；或用 GORM 的 `AfterQuery` callback 设置 |
| Phase 2 删除 `internal/search` 漏删被导出的类型 | 中 | 中（编译失败） | 先 grep 确认无任何导入再删除；删除前先用 `git stash` / 独立分支测试编译 |
| Phase 3 限流配置的 YAML 版本管理 | 低 | 低（配置漂移） | 采用每次部署都完整指定配置的策略（而非增量修改）；用 `config.Load` 的验证钩子 |
| Phase 4 事件总线的 goroutine 泄漏 | 中 | 高（内存泄漏） | 注入 context cancellation 的清理逻辑；所有 goroutine pool 使用 `sync.WaitGroup` + context 协同退出 |
| 所有 Phase：多方向并行开发的代码冲突 | 中 | 中（合并成本） | 每个方向的变更应集中在 1-3 个包内，不要大范围跨包修改。如果两个方向都要改同一个文件，先做优先级高的那个 |

### 5.4 检查清单

Phase 1 完成标志：
- [ ] `make test` 和 `make lint` 通过
- [ ] DB 慢查询被 `statement_timeout` 中断（集成测试验证）
- [ ] `/metrics` 返回 `db_conn_pool_open` 指标

Phase 2 完成标志：
- [ ] HTTP `GET /v1/search?q=...&conversation_with=uid-xxx` 返回 scoped 结果
- [ ] gRPC `Search` 接口的新 `conversation_with` 字段被双向正确序列化
- [ ] `internal/search/` 目录删除，无编译错误
- [ ] 死代码行数减少 ≥ 600 行（从 ~1030 降到 < 400，剩余可能是合法弃用标记）

Phase 3 完成标志：
- [ ] `curl -v` 显示 `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` headers
- [ ] 不同路由组独立限流（读操作 50 rps 时，写操作仍可正常工作）
- [ ] `make test` 包含限流中间件的单元测试（mock time）

Phase 4 完成标志：
- [ ] 所有日志行包含 `trace_id` 字段
- [ ] `/metrics` 的 `/v1/*` 路由有延迟分位数（p50/p90/p99）
- [ ] 事件总线提交和消费的集成测试

---

## 总结

这份代码库的整体架构质量在同类 Go 微服务中属于良好——分层清晰、依赖单向、领域驱动。5 个方向的扫描验证得出了两个最重要的修正：

1. **Cursor 隐私风险被高估了**——现有 actor 过滤已经做了隔离，真正的风险是静默终止（P2 正确性问题），而非 P1 安全问题。
2. **死代码的范围比文档描述的广**——`internal/search/` 整包（148 行）是无引用的废弃子系统，比单文件更严重。

**优先级上最大的调整建议**：方向五（DB statement timeout）的权重应当超过所有其他方向，因为它直接决定了生产环境在 30 秒内是否会被一个慢查询击穿。这不是"可做可不做"的优化，而是上线前的基础设施要求。方向三（对话内搜索）紧随其后，因为它的 ROI 最高——产品价值大，技术成本低，无新依赖。

**架构层面最大的缺失**不是某个具体方向，而是**可观测性基座的缺少**。当前没有请求级别的追踪、没有结构化的日志关联、没有 metrics 端点来暴露运行时状态。这些在 5 个方向中都没有被充分覆盖。建议在 Phase 4 补充。
