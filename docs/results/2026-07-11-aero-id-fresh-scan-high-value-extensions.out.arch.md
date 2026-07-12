现在我对代码库有了深入理解。以下是我的架构分析。

---

# 架构评审：aero-id 服务

## 1. 架构评估

### 当前架构的优势

分层结构（Handler → Service → Repository → DB 和 Storage）是可靠的。以下方面运作良好：

- **传输无关性（Transport Agnosticism）**：`ServiceRegistrar`/`RouteRegistrar` 模式允许 HTTP 和 gRPC 注册同一组用例，而无需`app.go`（组合根）了解具体处理程序类型。这是一个经过实战检验的解耦方式。
- **事外出站（Transactional Outbox）**：`internal/outbox/` 是已构建的最可靠机制——领域事件与领域行在同一数据库事务中写入，然后异步排干。这解决了任何多资源系统中微妙的最终一致性难题。
- **SSE Hub**：每个 uid 的通道实现具有每个 uid 的锁分段——为 Alice 发布永远不需要锁住 Bob。通道缓冲为 16，通过丢事件来应对慢消费者而非阻塞——对于实时推送来说，这是一个务实的退路。
- **存储 GC（Storage GC）**：基于引用计数的清理（`storage_refs` 结合 `ListSoftDeletedBefore`）对于软删除的 blob 生命周期来说是正确且必要的。
- **速率限制器（Rate Limiter）**：基于令牌桶的实现，带有每 uid 锁分段清理器——无需分布式状态，在单个实例上性能足够优良。

### 架构债务：双实现（方向一）

这是最深层次的问题。目前存在 **两条完全独立的路由** 到达业务逻辑：

| 层面 | 旧路径 | 新路径 |
|------|---------|---------|
| 传输层 | `internal/handler/{http,grpc}/*.go` | `internal/{file,comment,message,...}/handler_{http,grpc}.go` |
| 业务逻辑 | `internal/service/*.go` | `internal/{file,comment,message,...}/service*.go` |
| 持久化 | `internal/repository/*.go` | `internal/{file,comment,message,...}/repository*.go` |

目前，**只有旧的程序路径被连线**（见 `internal/app/app.go`）。新的领域包已存在但**未被使用**。这产生了 **约 7,000 行死代码**（4,593 行非测试 + 2,406 行测试），这些代码编译通过，消耗 CI 资源，需要在维护旧代码时保持测试绿色。

新包的完成状态各有不同：
- `internal/file/` — 完整（service、repository、handlers、share）
- `internal/comment/` — 完整（service、repository、handlers）
- `internal/message/` — 完整（service、repository、handlers）
- `internal/notification/` — 完整（service、repository、handlers、dispatcher）
- `internal/block/` — 不完整（仅有 handler、repository，没有 service）
- `internal/search/` — 不完整（仅有 handler，没有 service 或 repository）
- `internal/audit/` — 工具类（repository 仅有）

需要解决的依赖链：`internal/search/` → `internal/{comment,message,file}/`，因此删除必须按拓扑顺序进行。

### 事件驱动架构：强大的基础，但并非处处集成

事外出站 + SSE hub 为异步处理提供了坚实的基础。然而：

- **通知分发器被包裹在 `outbox.NewChain` 中**，以支持本地通知 + webhook 分发。这会创建一个有意思的依赖关系：通知行从出站事件中创建，意味着通知在领域行提交后**最终**到达。对于需要立即返回结果的读取路径来说，这没问题，但对于希望即时获得反馈的 UI 来说，可能需要在 SSE 通道上添加客户端重试逻辑。
- **跨行级授权检查**（例如评论 → 文件 → 分享查找）目前以同步方式遍历多个存储库，每个调用都采用拦截器式 `domain.MustActor`。这在读取路径中引入了几次往返，但保持了严格的一致性。

### 领域模型设计合理但未充分使用

`internal/domain/` 包包含实体类型（File、Comment、Message）、枚举（Visibility、TargetType、SearchKind）以及像 `CanReadFile` 这样的纯函数。然而：

- 实体是纯数据容器（拥有公共字段的 Go 结构体）。领域逻辑（可见性检查、嵌套深度验证）位于服务层，而非实体自身的方法中。
- 聚合边界隐式存在（"一个文件及其分享记录"），但并未编码到结构体中——这是一个将不变量封装到聚合根中的机会。

---

## 2. 扩展方向

### 方向 A：跨用户内容去重 → 多租户共享存储（P0—P1 价值）

**当前状态：** `FindByChecksum` 限定 `WHERE owner_uid = ? AND checksum = ?`；测试显式断言跨用户查找不应命中。`storage_refs` 基础设施已就绪，但仅为单用户场景服务。

**战略价值：** 跨用户去重可将存储成本降低 40—70%（在常见场景中，多个用户上传相同的文件）。对于像 MinIO 或本地磁盘这样的对象存储后端，物理 blob 的减少直接转化为节约；对于 S3，还能降低 API 调用费用。

**挑战：**
- 哈希命名空间：SHA-256 足以避免碰撞，但必须验证所有权——Alice 上传一个恶意文件，恰好与 Bob 合法文件的哈希匹配，不应给 Bob 的 blob 引用计数增加负担。
- 引用计数正确性：`Increment`/`Decrement` 已在 `Create`/`Delete` 路径中被调用，但**单用户去重已使其处于活跃状态**。将范围扩展到跨用户后，引用计数将变得至关重要——对应 `storage_refs` 表上的停机或死锁。
- 硬删除风暴：如果 Alice 删除了她的文件，而 Bob 是最后引用者，系统必须避免物理删除 blob 直到所有引用消失。当前的 `CountByStorageKey` 后备方案（在 ref 计数未激活时使用）对于跨用户场景来说过于脆弱。
- 存储 GC 交互：GC 清扫器从软删除行开始工作。在跨用户引用中，清扫器必须读取（或忽略）所有活跃引用，这需要对 `storage_refs` 添加 `Unscoped()` 查询。

**架构变更：**
- `FindByChecksum`：移除 `owner_uid` 过滤条件 → `WHERE checksum = ?`
- `storage_refs`：当前每个调用就地进行 `Increment`/`Decrement`。需要提升为事务性操作（在 `Create` 的事务内执行 `Increment`）。
- GC：修改 `storage/gc/sweeper.go` 以检查 `storage_refs`，而非 `files` 表。
- 测试：更改 `internal/repository/file_test.go:177-178` 中的断言，使跨用户查找预期成功。

### 方向 B：搜索范围扩展（P2→P1 价值）

**当前状态：** 搜索限定于 `owner_uid = actor` 或 `(from_uid = ? OR to_uid = ?)`。分享给用户的文件不会被搜索到。GIN 索引已存在（迁移 000016），但查询范围从未 JOIN `file_shares`。

**战略价值：** 用户在搜索时期望找到他们能访问的所有内容——包括分享给他们的内容。搜索结果中缺少已分享文件是一个 UX 缺口。

**挑战：**
- 权限越界：服层已有 `CheckFileReadAccess`，但搜索查询必须应用相同的 ACL，且不能泄漏不应暴露的行。这需要在 SQL 层进行 JOIN 或子查询。
- GIN 索引交互：对 `files.name` 的 JOIN + FTS 性能在分享表较大的情况下可能会下降。带索引的 `EXISTS` 子查询可能优于 JOIN。
- 消息搜索：消息搜索使用 `(from_uid = ? OR to_uid = ?)`。对于群聊或转发场景，这种双参与者模型可能不足——未来方向可能需要 JOIN 到收件人表或 Conversation 表。

**架构变更：**
- 存储库层：添加 `SearchFilesSharedWith(actor, query, limit)` 或修改 `SearchFiles` 以接收一个表示附加过滤条件的可选 `uid string`。
- 服务层：不修改——它已经根据 `actor` 来返回结果。
- 迁移：无需新的迁移——GIN 索引已经存在。

### 方向 C：批量操作入口（P2 价值）

**当前状态：** 分享（授权）在 HTTP 和 gRPC 上都支持批量（`[]string` / `repeated grantee_uids`）。取消分享、删除、标记已读和更新元数据都是单实体的。消息 MarkConversationRead（按人范围）已是批量，但并非按选择。

**战略价值：** 批量操作对于移动客户端和批量工作流至关重要。支持选择多个文件并删除、标记已读或批量更改可见性的用户将获得响应式体验。

**挑战：**
- 原子性：跨实体的批量操作应在单个事务中执行（全有或全无）。这需要对存储库进行"批量"方法。
- 页面范围 vs. 显式选择：批量操作可以是 "cursor 后的所有内容"（页面范围）或 "这些 ID 列表"。页面范围更适合移动端（无限滚动），而 ID 列表更适合桌面端（复选框选择）。两者都需要支持。
- 幂等性：批量删除应该是幂等的（已删除实体被静默跳过）。

**架构变更：**
- 存储库层：添加 `DeleteBatch(ctx, ids []domain.ID)`、`MarkReadBatch(ctx, ids []domain.ID)`。
- 服务层：添加对应的 `BatchDelete`、`BatchMarkRead` 方法，参数接收 `[]domain.ID`。验证：所有 ID 必须属于同一所有者。
- 传输层：HTTP POST `/v1/files/batch-delete`，带 JSON body `{"ids": [...]}`。对于 gRPC：`repeated bytes ids`。
- 错误语义：部分失败（ID 子集失败）需要 `BatchResult` 结构体，结构为 `{Ok: [...], Failed: [...]}`。

### 方向 D：审计与可观测性系统工程（P1 价值）

**当前状态：** 审计日志已嵌入服务层（`file_share.go` 中的 `s.audit.Log`）。指标通过 `internal/pkg/metrics` 进行注册表收集。追踪通过 OpenTelemetry 实现。但存在以下缺口：

- 审计事件从过程内部点**附带**发出（带 `_ = s.audit.Log(...)`，表示失败被忽略）。没有结构化的审计管道——某些路径会审计，某些不会。
- 指标注册表是进程内的——实例间聚合需要 Prometheus 抓取或其他外部系统。
- 追踪是手动检测的：服务层中不存在自动的 `OpenTelemetry` 中间件来捕获跨度。

**战略价值：** 对于合规性（谁分享了什么，何时操作），必须有一条从创建到删除、对每个访问事件进行审计的确定路径。缺失的事件或静默失败的审计是不可接受的。

**架构变更：**
- 审计存储库：从 `internal/audit/repository.go` 中的 GORM 切换到专用的审计表（`audit_events`），带有独立的保留规则（90 天 => 冷存储，1 年 => 删除）。
- 审计服务：应在服务层使用 `defer` 模式或职责链，在每次成功操作后记录审计事件，而不是在特定位置内联记录。
- 结构化日志：结构化日志中的所有审计事件（`zap.Object("audit", ...)`）以便日志摄取者（如 Loki）可以查询它们——作为 DB 备份。

### 方向 E：域事件与集成事件（P1 价值）

**当前状态：** 事外出站提供了一种通用的、基于 DB 的事件机制。然而，事件模式编写松散（`Event.Payload` 是 `[]byte`）。领域事件严重依赖于出站事件的结构——没有显式的领域事件类型。

**战略价值：** 当出站事件流有明确的 Schema Registry（如 CloudEvents 规范）时，像 webhook 集成、数据分析管道和跨服务同步这样的横向特性将变得更加可靠。当前 webhook 处理程序使用 HMAC 签名（这很好），但模式没有版本控制。

**架构变更：**
- 引入 CloudEvents 模式作为规范：事件的每个 `EventType` 都应具有在文档或 proto 文件中定义的对应结构。
- 添加 Schema Registry 集成（例如，在出站表中添加 `schema_version` 列），以确保消费者不变地演进。
- 使用出站模式实现事件源读取模型（例如，物化视图存在 `message_unread_counts`，但从消息读取 → 使用物化出站事件将消除当前的同步触发器模式）。

---

## 3. 接口设计建议

### 3.1 领域接口

当前的接口定义在 `internal/file/file.go`、`internal/comment/comment.go` 等中，代表了一种**基于仓储的抽象**。它们严格遵循 CRUD 模式。向领域驱动设计的迁移（在双实现清理之后）应引入**基于聚合的接口**：

```go
// 而非：
type Repository interface {
    Get(ctx, id) (*File, error)
    Create(ctx, *File) error
    Delete(ctx, id) error
}
```

```go
// 应该：
type FileRepository interface {
    // 查询聚合
    FindByID(ctx, id) (*FileAggregate, error)
    // 持久化整个聚合（包含分享记录）
    Save(ctx, *FileAggregate) error
    // 移除聚合及其子记录
    Remove(ctx, id) error
}
```

目前，文件分享和文件本身是独立的存储库（`ShareRepository` 与 `Repository`）。将它们合并到一个 `FileAggregate` 接口中将使**主人删文件 → 他的分享记录也同步删除**的自包含事务成为可能，解决方向三（软删除级联）。

### 3.2 接口迁移兼容性

在过渡期间，新旧接口必须共存。具体策略：

- **标记旧接口为 `@deprecated`**，并添加内联文档说明替代方案。
- 使用**包装适配器**在组合根中进行桥接：`NewFileService`（旧）→ `file.Service`（新）。但长期来看，应删除旧接口（清理方向一）。
- 跨传输并行性：当前 `RouteRegistrar` 和 `ServiceRegistrar` 适用于两个路径。迁移后的包应继续保持这种注册模式。

### 3.3 向后兼容性

对于 HTTP API，路径模式**不应改变**（`/v1/files/:id` 必须保持有效）。向 v2 路径方案（例如 `/v2/files/batch`）的迁移可以通过在旧路由器上增加 `RouteRegistrar` 来实现，无需破坏现有客户端。

对于 gRPC，协议缓冲区的兼容性要求字段**永远不要被移除**——只能弃用。`repeated grantee_uids` 字段（在取消分享中目前为单一字段）应被弃用，并添加 `repeated grantee_uids` 用于批量取消分享。

### 3.4 跨包依赖

`internal/search/` 当前依赖 `internal/{comment,message,file}`。这是一个正确的依赖方向（搜索是消费者，而非提供者）。但为了保持紧凑：

- `internal/search/` 应仅导入目标包的**接口**，而非具体实现。
- 如果搜索需要目标包中的类型（例如 `domain.File`），这些类型必须留在 `internal/domain/` 中，而非具体包中。
- 当前代码正确遵循了这一模式（所有实体类型都在 `domain` 中）。

---

## 4. 技术选型

### 4.1 不需要按当前需求引入新技术栈

当前技术栈（Go 1.26 + PostgreSQL 16 + GORM + Gin + gRPC + MinIO）足以解决所有五个已识别方向中的业务需求。以下列出的任何技术都将是**按需引入**，仅在具体证据证明现有技术栈无法满足目标量级或延迟要求时考虑。

### 4.2 库替换建议

| 当前 | 改为何种 | 理由 |
|--------|---------|-------------|
| GORM (`gorm.io/gorm`) | 按项目保留 | 是标准的 Go ORM。其 `Unscoped()` 对软删除查询是必要的。迁移模式用得不错。**但**注意避免隐式 `N+1`——GORM 的 `Preload` 在处理大型结果集时可能会生成过多查询。如果性能成为瓶颈，考虑对读取路径使用 `sqlx` 或纯 SQL 构建器（`squirrel`），以便在 GORM 之上对热点查询进行精细优化。 |
| Gin (`github.com/gin-gonic/gin`) | 按项目保留 | 广泛使用，性能满足要求，路由器易于测试。其中间件链对认证、限速和幂等性支持很好。 |
| 协议缓冲区（protobuf-go） | 按项目保留 | 已经生成。protobuf 字段的兼容性规则是 gRPC API 演进的既定方案。 |
| OpenTelemetry | 按项目保留 | 现有追踪检测已内置于 `internal/pkg/tracing` 中。可以维持。 |
| Prometheus 指标 | 按项目保留 | `metrics.Registry` 是轻量级的，且与 Prometheus 抓取兼容。 |
| testcontainers-go | 按项目保留 | 用于集成测试（需 Docker）。Go 生态系统中无替代方案。 |
| golang-migrate | 按项目保留 | 生产级迁移工具。不需要更换。 |

### 4.3 可能（但不是必须）添加的

- **事件模式注册表**（例如，简单的 `json.RawMessage` + 存储库中的 `schema_version` 列）：不需要新库。在 `Event.Payload` 旁边添加一个 `SchemaVersion int` 列，并通过 `EventType` → `SchemaVersion` 的顶层映射强制执行。
- **批量操作的悲观锁**：对于跨用户去重，`SELECT ... FOR UPDATE` 行锁（PostgreSQL 原生支持，属于 SQL）可能优于引入分布式锁管理器（如 etcd/Redis Redlock）。GORM 的 `Clause` 模块支持它。
- **聚合性能追踪**：如果搜索查询（FTS + JOIN `file_shares`）速度不够快，考虑引入 `pgvector` 进行语义搜索，但仅作为 P3 项目——GIN + `websearch_to_tsquery` 对于大多数搜索场景已经足够。

### 4.4 自建 vs. 采购

所有五个方向都更适合自建：

- **去重**：本质上是特定领域的（哈希 + 引用计数）。外部系统（如 MinIO 的对象锁定）不提供同样的每键引用计数语义。
- **搜索范围扩展**：纯 SQL 变更。无外部依赖。
- **批量操作**：存储库层的 Go 实现——无外部系统。
- **审计**：可能需要外部存储日志摄取器（如 Loki），但审计跟踪本身内置在代码中。
- **域事件**：出站 + webhook HMAC 已经是自建的。对于事件编排，如果出现 5 个以上的消费者，可以考虑引入 `NATS` 或 `RabbitMQ`，但当前设置对单一服务来说是正确的。

---

## 5. 实施路线图

### 可交付成果的组织

所有五个方向都可以并行推进（风险很低），但**方向一必须在所有其他方向之前完成**，因为其他方向中的存储库变更会影响两组实现。方向一之后，方向四和方向二是纯存储库变更（无新概念）。方向五涉及新的传输和存储库代码。方向三（级联）影响多个存储库。

```
里程碑 1：统一基础层（对方向一的清理）
里程碑 2：跨用户去重 + 搜索范围（对当前用户感知的修复）
里程碑 3：批量操作（面向移动端）
里程碑 4：级联语义（完成后台一致性的收尾）
```

### 阶段划分

#### 阶段 1：根基（2 周）— P0

**目标：** 在保留一个完整实现的同时删除死代码。此阶段的结果必须是**一个运行路径**（从传输到存储库），且编译通过，测试通过，lint 绿色。

1. 分析 DAG：`internal/search/` → `internal/{comment,message,file}/`。这是唯一的消费者驱动的依赖链。
2. 逆向删除：从消费者端开始（`internal/search/`、`internal/notification/dispatcher.go`）。
3. 删除被依赖方的未引用文件（`internal/{comment,message,file,block}/` 中仅被已删除消费者引用的文件）。
4. 将未被新包覆盖的测试迁移到旧的 `internal/service/` 测试套件。
5. 不可破坏的不变量：`make test && make lint && make check-file-sizes` 必须通过。

**风险：** 高——这是 ~7,000 行代码。如果两个实现中有任何行为分歧，删除后可能会出现回归。缓解措施：对每个删除的包进行逐步 `git diff`，并对照迁移前的集成测试进行确认。

#### 阶段 2：去重范围扩展（1 周）— P1

**目标：** 跨用户内容去重。

1. 修改 `FindByChecksum`：将 `WHERE owner_uid = ? AND checksum = ?` 改为 `WHERE checksum = ?`。
2. 更改测试断言：`file_test.go:177-178` 应期望跨用户查找成功。
3. 验证 `storage_refs.Increment/Decrement` 的线程安全性——这已被确认是正确的；不需要额外改动。
4. 测试引用计数之争：两个并发上传者上传相同内容 → 预期 `Increment` 被调用两次，`Decrement` 被安全调用一次。

**风险：** 低——单行 SQL 变更。引用计数的基础设施已经很坚固。

#### 阶段 3：搜索范围（1 周）— P2（如果对用户有用，可提升至 P1）

**目标：** 用户搜索覆盖所有可访问的文件，包括分享给他们的文件。

1. 添加 `SearchFilesSharedWith(actor, query, limit)` 到存储库（在 `internal/repository/file.go` 中，或如果阶段 1 尚未合并，则在旧的契约中）。
2. 在搜索服务层中，合并 `SearchFiles`（所有者作用域）和 `SearchFilesSharedWith`（分享作用域）的结果。
3. 限制：每类 50 个总结果 → 在服务层中截断合并后的结果集不会增加复杂性。
4. 验证权限边界：搜索不得暴露文件给未经授权的用户。添加集成测试。

**风险：** 低——无新模式，仅有查询范围扩展。

#### 阶段 4：批量操作（2 周）— P2

**目标：** 批量删除、批量标记已读、批量修改元数据。

1. 存储库层：添加 `DeleteBatch(ctx, ids []domain.ID) error`、`MarkReadBatch(ctx, ids []domain.ID) error`。
2. 服务层：添加 `BatchDelete` 和 `BatchMarkRead`，带有验证（所有 ID 必须属于同一所有者，且为软删除安全）。
3. HTTP 传输：`POST /v1/files/batch-delete`（JSON body：`{"ids": [...]}`）、`POST /v1/messages/batch-read`。
4. gRPC 传输：定义 `BatchDeleteRequest` 和 `BatchMarkReadRequest` message。
5. 错误语义：对于批量 API，返回 `{ok: [...], failed: [...]}`，而非整体失败。

**风险：** 中等——批量事务的大小。在 `WHERE id IN (...)` 中，PostgreSQL 对超过 1000 个 ID 的列表有限制。将批量大小截断为 100（或通过页面光标使用 `LIMIT + OFFSET` 进行分页删除）。

#### 阶段 5：级联语义（1 周）— P1

**目标：** 删除评论会对其回复进行软删除；删除文件会移除其 `file_shares` 行。

1. 评论 `Delete`：将 `Delete(id)` 替换为 `DeleteWithReplies(ctx, id)`，该操作在事务中对 `comment_id = ?` 和 `parent_id = ?` 范围内的所有评论进行软删除。
2. 文件 `Delete`：添加 `s.shares.DeleteByFile(ctx, id)` 到事务中。
3. 消息 `Delete`：当前仅软删除消息行。如果消息有反应（`message_reactions`），由于软删除执行的是 `UPDATE` 而非 `DELETE`，因此不会触发 `ON DELETE CASCADE`。保持现状——不需要物理删除。
4. `comment_likes`：当前没有 `ON DELETE CASCADE`，但由于软删除执行的是 `UPDATE`，这是正确的——没有触发器的可能。

**风险：** 低——SQL 变更。仅对删除路径的局部影响。

---

### 关键架构风险总结

| 风险 | 领域 | 可能性 | 影响 | 缓解措施 |
|------|----------|------------|----------|--------------|
| 双实现回归（方向一） | 清理 | 高 | 高 | 验证每个删除的包与旧测试等价；逐合并 |
| 去重引用计数死锁（方向二） | 并发性 | 低 | 高 | 在 `Increment/Decrement` 上使用事务性 `SELECT FOR UPDATE` |
| 搜索中的权限越界（方向四） | 安全 | 低 | 高 | 集成测试：已分享文件的搜索必须仅暴露给目标用户 |
| 批量事务大小 | 数据库 | 中 | 中 | 分页/截断：每个事务最多 100 个 ID |
| 未来迁移期间模块化导入冲突 | 构建 | 中 | 中 | 确保所有 `internal/domain` 类型保持在公共包中——不要跨包引入循环 |

### 初始提议

如果我只能在以下三项上优先投入时间：

1. **方向一（P0）**—清理 7,000 行死代码。维护两条路径的成本已超过代码本身的价值。未来的每一次变更都会使两组保持同步的认知负荷增加。
2. **方向二（P1）**—跨用户去重。具有明确的投资回报率（存储成本节省 40—70%），且变更极小（一行 SQL + 测试断言）。
3. **方向三（P1）**—级联语义。数据完整性——软删除评论而不删除其回复孤立了区域行，且 UI 无法按预期渲染。这是一个正确的缺陷。
