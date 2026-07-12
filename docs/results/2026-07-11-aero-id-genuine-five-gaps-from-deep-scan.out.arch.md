现在我已对代码库有了全面的了解。以下是我的架构分析。

---

# 架构分析：aero-id 服务

## 1. 架构评估

### 1.1 当前架构的优势

审查的代码库展示了一个经过深思熟虑、大部分实现良好、遵循务实工程原则的代码库。核心优势：

| 优势 | 证据 |
|---|---|
| **明确的分层** | 严格单向依赖：`handler → service → repository → Postgres`，以及 `service → {storage, sso}`。这是教科书中分层架构的范例。 |
| **传输层独立** | HTTP 和 gRPC 共享同一套 `service` 接口。没有业务逻辑泄露到 handler 中。 |
| **强错误边界** | 仓库返回原始错误 → 服务层翻译为 `AppError` → handler 渲染。没有原始 DB 错误泄露到 API 响应中。 |
| **务实的 YAGNI** | 设计文档明确列出了 out-of-scope 项（点赞 v2、metrics、i18n）。代码库没有膨胀。 |
| **可测试的架构** | 使用 testcontainers 进行真正的 Postgres 集成测试，service 层使用 mock。拒绝 sqlmock。 |
| **正确的身份边界** | SSO 持有身份。没有本地的 accounts 表。用户由 `uid` 字符串引用，不做 FK——这正确地防止了与外部 SSO 的数据不一致。 |
| **限制性的工程约束** | 文件大小 ≤500 行，函数长度 ≤50 行，圈复杂度 ≤10，if 嵌套 ≤3 层。这些是强有力的质量门禁。 |

### 1.2 架构债务（按严重性排序）

#### 债务 #1（P0）：双栈死代码 —— 审查已验证

这是代码库中最严重的架构问题。代码库包含**两个完整的实现栈**，它们互不知晓：

```
活跃栈 (internal/service/ + internal/repository/)：
  internal/app/app.go
    → service.NewFileService(repository.NewFileRepository(gdb), ...)  ← 已布线
    → service.NewCommentService(repository.NewCommentRepository(gdb, ...), ...)  ← 已布线
    → handler/http.NewFileHandler(fileSvc, ...)  ← 已布线

死栈 (internal/{file,comment,message,block,notification}/)：
  internal/file/service_crud.go           ← 从未调用
  internal/file/repository.go             ← 从未调用
  internal/comment/service.go             ← 从未调用
  internal/comment/repository.go          ← 从未调用
  internal/message/service.go             ← 从未调用
  internal/message/repository.go          ← 从未调用
  internal/block/service.go               ← 从未调用（仅测试）
  internal/notification/service.go        ← 从未调用（仅测试）
  internal/{file,comment,...}/handler_{http,grpc}.go  ← 未在 app.go 中布线
```

**影响**：~2500–3500 行死代码，包括：
- 完整重复的领域模型（`internal/comment/comment.go` 重新导出声明的 `domain.Comment`，但独立的 service 实现定义了自包含的数据流）
- 两组完整的迁移级别 GORM 仓库（`internal/comment/repository.go` vs `internal/repository/comment.go`）
- 两套 service 测试（测试从**不**执行的代码 → 虚假的测试通过）
- 双倍的心智负担——开发者阅读时无法确定哪个实现是权威的

**严重性**：P0。这是工程基础问题。每个新特性都必须跨两个栈实现或理解哪个栈是活的，大大增加了认知负担。搜索引擎包 `internal/search/search.go` 从死栈中导入 `file.Repository` 和 `comment.Repository` 接口——这使得移除死栈需要改变搜索包。

**根本原因**：看起来死栈来自一次更具雄心的"模块化按领域"尝试（每个领域一个包，每个包含 handler/service/repository），之后重构为更扁平的分层布局（`internal/{handler,service,repository}/`—每个职责一层）。原有的文件被留下，未被移除。

#### 债务 #2（P1）：软删除没有恢复路径

所有业务表都使用 `gorm.DeletedAt`，但没有为用户提供恢复 API。GC sweeper（`internal/storage/gc/sweeper.go`）在保留期（可配置）后进行硬删除。用户意外删除后无法恢复。

**影响**：基本的数据安全措施缺失。对于存储用户文件的文件存储服务来说，这是一个产品层面令人担忧的问题。

**级联复杂性**：恢复一个文件需要同时恢复其 `file_shares` 记录。恢复一个评论可能需要恢复其子资源。在实施之前，这需要周到的建模。

#### 债务 #3（P2）：没有批量操作 API

所有变更端点都是单资源操作。使用 `repeated string grantee_uids` 的 `ShareFile` 是唯一的批量参数。批量的缺失增加了网络往返次数并最终限制了客户端效率——对于批量删除消息、批量取消分享文件或批量标记已读等操作。

**影响**：增量式但恒定的摩擦。每一个批量操作都需要客户端循环。

#### 债务 #4（P2）：没有运行时配置 API

`PermSystemConfig` 权限已定义且可用于角色管理（授予/撤销端点），但不存在服务范围的运行时配置 API。配置更改需要重新部署。对于特性开关、速率限制调整或维护模式，没有运行时注册表。

#### 债务 #5（P3）：被动降级，无主动维护模式

`internal/handler/http/health.go` 根据依赖健康度返回 `"ready"`/`"degraded"`/`"unavailable"`，但没有操作员触发的维护模式端点、每个特性的开关或连接排空 API。

### 1.3 关键设计决策评估

| 决策 | 合理性 | 评价 |
|---|---|---|
| **分层而非模块化**（D2） | 评论、文件、消息之间有强耦合；同一层间交叉调用比跨模块接口更顺畅 | ✅ 始于简单是正确的。但死代码——极端的模块化尝试——显示出对于这种规模的代码库，分层确实更好，只是重构做得不彻底 |
| **GORM 而非 sqlx**（D3） | 稳定 schema，CRUD 性质 > 复杂查询 | ✅ 合理。GORM 的 `DeletedAt` 软删除与审计要求完美契合 |
| **Token 缓存 60s**（D4） | 平衡 SSO 载荷与撤销延迟 | ✅ 合理权衡。电路断路器 + 重试使其稳健 |
| **评论嵌套两层**（D5） | 防止复杂的 UI 树和深查询 | ✅ 在 domain 和 service 层双重强制执行——正确的防御性设计 |
| **不在启动时自动迁移**（D10） | 多副本竞争中的安全 | ✅ 正确的运维实践 |
| **无 metrics / tracing 内置**（D8） | YAGNI | ⚠️ 在某个数量级后合理，但审查显示已添加 tracing/metrics（`internal/pkg/metrics`、`internal/pkg/tracing`）——设计文档已过时 |

---

## 2. 扩展方向

### 方向 A（P0）：消除双栈死代码

**为什么需要**：消除技术债务。死代码不仅仅是混乱——它会误导新人、产生虚假的测试覆盖率、并增加了维护者的认知负荷。

**技术难点**：
1. `internal/search/search.go` 导入死栈的仓库接口（`file.Repository`、`comment.Repository`、`message.MessageRepository`）。搜索服务必须改为从活跃栈（`internal/repository/`）导入接口，或者活跃栈必须暴露搜索包可以契约使用的仓库接口。
2. 死栈的 `handler_{http,grpc}.go` 是死 handler，但搜索的 `handler_grpc.go` 导入转换函数（`comment.CommentToPB`、`file.FileToPB`、`message.MessageToPB`）。这些转换函数也存在于活跃的 `internal/handler/grpc/` 包中。
3. 死栈的单元测试很全面——在删除死实现时，必须验证活跃栈上是否缺少任何测试覆盖的边界情况。

**预期变更**：
1. 在活跃的 `internal/repository/` 包中暴露仓库接口（如果尚未存在），以便 `internal/search/` 可以依赖它们。
2. 将死栈中的任何唯一转换函数（`*ToPB` 等）迁移到活跃的 `internal/handler/grpc/` 包中。
3. 删除 `internal/{file,comment,message,block,notification}/` 包——只保留接口定义（如果有任何外部消费者）。
4. 更新 `internal/search/` 以导入活跃的仓库接口。
5. 验证 `make test && make lint && make check-file-sizes` 通过。

**对现有系统的影响**：无。死代码本来就没有被调用。

### 方向 B（P1）：软删除恢复 API + 回收站

**为什么需要**：用户期待基本的数据安全——意外删除应该可撤销。没有回收站，一个错误的删除就是永久丢失。

**核心挑战**：
1. **级联恢复**：一个 `file` 可能有 `file_shares` 记录。一个 `comment` 可能有 `comment_reports` 和子评论。恢复必须选择性地级联或警告用户有关依赖资源的信息。
2. **保留期边界**：GC sweeper（`internal/storage/gc/sweeper.go:138-145`）在 `retention` 后执行硬删除。恢复 API 必须在硬删除之前恢复 `deleted_at` 为 NULL。GC 和恢复之间需要协调。
3. **共享场景**：其他用户对已删除文件的引用怎么办？`messages.file_id` 持有对文件的引用——如果文件被恢复，消息应该自动链接回它，还是应该保留快照？

**预期变更**：
- 每个域的新仓库方法：`Undelete(ctx, id)`（将 `deleted_at` 设置为 NULL）
- 新的 service 方法：`RecoverFile(ctx, id)`，`RecoverComment(ctx, id)`，等等。
- 仓库级 GC 的协调：将 `deleted_at` 更新为 NULL，但需要确保保留期不会使新恢复的行立即被删除。一个简单的方案：`RecoverFile` 设置 `deleted_at = NULL`，GC 只扫描 `deleted_at < now - retention`。
- 新的 HTTP/gRPC 端点：`POST /v1/files/{id}/restore`，`POST /v1/comments/{id}/restore`
- 回收站在 API 层列出可恢复的资源：`GET /v1/files/deleted`，`GET /v1/comments/deleted`

### 方向 C（P2）：批量操作 API

**为什么需要**：UI 提升——批量选择消息并删除、批量取消分享文件、批量标记已读。减少 N+1 问题。

**核心挑战**：
1. **事务语义**：批量操作必须是原子的（全部成功或全部失败）还是尽最大努力的？对于 UI 用例，尽最大努力通常可以接受，但必须明确记录。
2. **授权**：批量删除必须检查每一条消息的 actor 是否是发送者。这可以通过一次查询来完成（`WHERE from_uid = ? AND id IN (?)`），而不是每条消息一个查询。
3. **软删除幂等性**：已经软删除的记录再次软删除应被视为成功。

**预期变更**：
- 新的仓库方法：`SoftDeleteMany(ctx, ids []domain.ID)`——单次 `UPDATE ... SET deleted_at = now() WHERE id IN ?` 查询
- 新的 service 方法：`BulkDeleteMessages(ctx, ids []domain.ID)`，`BulkUnshareFile(ctx, id, granteeUIDs []string)`
- 新的 HTTP/gRPC 端点：`POST /v1/messages/bulk-delete`（请求体：`{ ids: [...] }`）

### 方向 D（P2）：运行时配置 API + 特性注册表

**为什么需要**：操作敏捷性。在生产中启用/禁用特性、更改速率限制、设置维护模式或调整保留策略，无需部署。

**核心挑战**：
1. **配置源层次**：运行时配置应覆盖文件/env 配置，但不应在重启后持久化（除非写入 DB 或外部配置存储）。典型的模式：**应用启动 → 配置文件/环境 → 内存中的运行时覆盖**。
2. **在运行中的请求中使用特性开关**：特性开关必须在请求路径中被检查——如果 `"comments.enabled"` 为 false，则 `/v1/comments` 路由应返回 503。这需要一个从配置变更广播到活跃路由/中间件的机制。
3. **一致性**：在多副本环境中，运行时配置变更不会即时传播。声明这个限制以避免混淆。

**预期变更**：
- `internal/service/` 中的新包：`runtimecfg` 或 `feature`
- 将配置值存储在 `sync.Map` 中或通过 `atomic.Value` 进行无锁读取
- 管理端点：`GET /v1/admin/config`，`PUT /v1/admin/config`（带写入/重建验证）
- 每个特性的中间件检查或 service 层检查
- 在 `internal/config/config.go` 中验证：运行时值不得突破编译时安全边界

### 方向 E（P3）：主动维护模式 + 优雅连接排空

**为什么需要**：零停机部署、计划的维护窗口、以及优雅处理故障。当前的健康检查是被动的——操作员在关闭前无法通过 API 将实例标记为维护中。

**核心挑战**：
1. **连接排空**：在关闭 gRPC listener 之前，告诉负载均衡器停止发送新请求。gRPC 健康检查协议（`grpc.health.v1.Health`）支持 `NOT_SERVING`，但 HTTP 需要一个等效机制（负载均衡器通常使用 `/healthz` 上的 503 状态码）。
2. **运行中的请求完成**：现有的 `shutdown_timeout`（10s）处理了这一点，但操作员需要一种方法来手动触发排空（例如，在蓝绿部署中）。
3. **与特性注册表的集成**：`"maintenance_mode"` 可以是一个特性开关，触发一个检查所有路由的中间件。

**预期变更**：
- 管理端点：`POST /v1/admin/maintenance`（带可选的 `reason` 和 `duration`）
- 中间件：在所有非健康、非管理的路由前检查 `isMaintenance` 标志
- gRPC：在维护期间将健康状态翻转为 `NOT_SERVING`
- 可选的 Webhook：在进出维护状态时通知下游系统

---

## 3. 接口设计建议

### 3.1 消除死代码的原则

死代码的根源是不完整的重构——从"每个领域一个包"转向"每个职责一层"而没有清理。为了避免将来重蹈覆辙：

1. **在重构前冻结**：不要添加新代码到死栈。所有新特性必须在活跃栈中实现。
2. **在同一个 PR 中删除死代码**：如果死栈的某个部分必须为搜索保留（接口文件），则只保留接口文件，删除实现。
3. **添加 linter 规则**：`go vet` 级别的检查，检测定义了 `Service` 接口但从未被 `app.go` 构造导入的包。

### 3.2 仓库接口设计

当前活跃仓库（`internal/repository/`）被实现为具体结构体，没有公开的接口。死栈的仓库有接口（例如 `internal/comment/repository.go` 声明了 `Repository` 接口）。应遵循的做法：

```
活仓库: internal/repository/comment.go
  定义：type CommentRepository struct{}  — 具体结构体
  被：internal/service/comment.go 导入

死仓库: internal/comment/repository.go
  定义：type Repository interface{...}  — 接口定义
  被：internal/search/search.go 导入（从死栈）
```

**建议**：将仓库接口提取到它们自己的文件（如 `internal/repository/interfaces.go`），由死栈导入搜索的仓库接口引用。然后可以删除死实现。

```
// internal/repository/interfaces.go
type CommentRepo interface {
    Create(ctx, c) error
    Get(ctx, id) (*domain.Comment, error)
    ListByTarget(ctx, t, target, page) ([]*domain.Comment, string, error)
    ...
}

// internal/repository/comment.go
func NewCommentRepository(db *gorm.DB) CommentRepo { ... }
```

### 3.3 添加新抽象层的标准

在新的抽象层之前，问：

1. **这个抽象有多个实现吗？** 如果没有，不要抽象它。
   - 错误：为单实例 SSO 客户端抽象 `UserDirectory`
   - 正确：`FileStorage` 接口有 `local` 和 `s3` 实现
2. **这个接口被多种传输方式使用吗？** 如果是，将其放入 `internal/service`（如 `FileService`、`CommentService`）。
3. **这个抽象降低了耦合还是增加了？** 如果对一个层的更改需要在三个地方更改接口，它就是过度抽象。

### 3.4 向后兼容性

- 所有新的 API 端点应采用 `POST /v1/*/bulk-*` 命名约定以区别于单资源端点
- 恢复端点应使用 `POST /v1/{resource}/{id}/restore`，这是 RESTful 且安全的（幂等、非 GET）
- 运行时配置应放在 `/v1/admin/config` 之后以避免与业务资源冲突

---

## 4. 技术选型

### 4.1 不需要新的技术栈

目前的选型（Gin + GORM + gRPC + Postgres + testcontainers）很好地适合该服务。新需求不需要根本性的堆栈变更。

| 潜在需求 | 建议 | 理由 |
|---|---|---|
| 缓存 | 现有 `sync.Map` 缓存（在 SSO 客户端中）加上对象级别简单的内存缓存 | 不需要 Redis——数据量很小，TTL 只有 60-300s。Redis 会增加运维复杂度。 |
| 配置注册表 | 使用 Postgres 表 + `sync.Map` 快照（服务端缓存） | 运行时配置很少更改。慢路径访问 DB，快路径访问 `sync.Map`。 |
| 特性开关 | 无需框架。一个包含 `map[string]atomic.Bool` 的 `feature.Registry` 加上管理端点 | 特性开关的复杂性通常与它们的价值成反比。保持简单。 |
| 外部化配置 | 继续使用 Viper。运行时覆盖存储在同一注册表中。 | Viper 已经处理了分层（默认值 → 文件 → 环境）。运行时配置仅作为第四层（内存中）添加。 |

### 4.2 第三方依赖评估标准

| 标准 | 问题 |
|---|---|
| 必要性 | "我们不能在没有额外依赖的情况下在原生 Go 中实现这个吗？" |
| 上游健康状况 | 定期维护、公开的 issue 跟踪器、合理的发布节奏、BSD/MIT/Apache 许可证 |
| 依赖大小 | 拉动多少传递依赖？是否引入 CGo？ |
| 替代方案 | 标准库是否足以胜任？我们过去是否在另一个项目中成功使用了这个库？ |

当前依赖集通过了这个测试。无 JWT 库、无 Redis 客户端、无消息队列——每个缺失的依赖都是有意为之。

### 4.3 自建 vs 采购决策

对于这个特定的服务（轻量级 CRUD + 文件存储），最安全的路径是**为运维功能自建**：

1. **特性注册表 & 运行时配置**：自建。领域知识简单（原子键/值 + 持久化）。小于 200 行。
2. **回收站机制**：自建。在现有的 `gorm.DeletedAt` 之上。添加 `POST /restore` 端点和 GC 协作。
3. **批量操作**：自建。在已存在的仓库之上。添加 `BulkDelete` 仓库方法 + `BulkDelete` service 方法。
4. **恢复端点**：自建。在现有的 `Get`/`Update` 权限检查之上。

不应采购任何东西——所需的都是对现有代码的增量式、直接扩展。

---

## 5. 实施路线图

### 5.1 优先级矩阵

| 方向 | 优先级 | 理由 | 工作量估计 | 风险 |
|---|---|---|---|---|
| A：消除死代码 | **P0** | 工程基础。在添加新代码之前必须清理旧代码。 | 1-2 天 | 低。死代码未使用。最坏情况：搜索引用需要重新布线。 |
| B：回收站 | **P1** | 数据安全。每个文件服务都应允许恢复。 | 3-5 天 | 中。级联恢复在边缘情况下较为复杂。GC 协调需要细致。 |
| C：批量操作 | **P2** | 增量效率提升。依赖于 A 的清理（在干净的仓库接口上更容易）。 | 1-2 天 | 低。批量删除简单且直接。 |
| D：运行时配置 | **P2** | 高运维价值。运行中配置可以节省部署。 | 2-3 天 | 中。在多副本之间的一致性是一个已知的限制。 |
| E：维护模式 | **P3** | 锦上添花。现有的被动式降级对于小规模已经够用。 | 1 天 | 低。基本的布尔检查 + 健康状态翻转为 NOT_SERVING。 |

### 5.2 阶段划分

#### 阶段 1（第 1 周）：清理 — 方向 A

**目标**：移除死代码，验证活跃栈，更新所有导入。

步骤：
1. 识别活跃 `internal/repository/` 中缺少的接口定义。为每个域创建 `internal/repository/interfaces.go`，定义搜索所需的接口。
2. 更新 `internal/search/search.go` 和 `internal/search/handler_grpc.go` 以导入活跃的仓库接口，而非死栈的接口。
3. 将死栈中任何缺少的 `*ToPB` 转换函数迁移到 `internal/handler/grpc/` 包中。
4. 验证所有测试仍通过。
5. 删除 `internal/{file,comment,message,block,notification}/` 包。

**门禁**：`make test && make lint && make check-file-sizes`

#### 阶段 2（第 2 周）：回收站 — 方向 B

步骤：
1. 为每个域添加仓库 `Undelete(ctx, id)` 方法。
2. 在仓库中实现 GC 协调：`RecoverFile` 设置 `deleted_at = NULL`；GC 跳过在保留期内被恢复的行（`deleted_at < now - retention AND deleted_at IS NOT NULL`）。
3. 为每个域添加 service `Recover` 方法，包含授权检查（仅所有者）。
4. 添加端点：`POST /v1/files/{id}/restore`、`POST /v1/comments/{id}/restore`、`POST /v1/messages/{id}/restore`。
5. 添加 `GET /v1/files/deleted`、`GET /v1/comments/deleted`、`GET /v1/messages/deleted` 端点以列出可恢复的资源。

#### 阶段 3（第 3 周）：批量操作 — 方向 C

步骤：
1. 添加仓库方法：`SoftDeleteMany(ctx, ids)`、`MarkReadMany(ctx, ids)`、`UnshareMany(ctx, id, granteeUIDs)`。
2. 为 service 层添加授权批处理。
3. 添加端点：`POST /v1/messages/bulk-delete`、`POST /v1/notifications/bulk-mark-read`、`POST /v1/files/{id}/bulk-unshare`。

#### 阶段 4（第 4 周）：运行时配置 — 方向 D

步骤：
1. 添加 `runtimecfg` 表（键/值 + 版本号）。
2. 实现 `internal/service/runtimecfg/registry.go`：启动时从 DB 加载，通过 `sync.Map` 提供键/值访问，持有整个配置的可选 `sync.RWMutex` 以便原子更新。
3. 添加管理端点：`GET /v1/admin/config`、`PUT /v1/admin/config`。
4. 将运行时配置与现有中间件（速率限制器、特性开关）集成。

#### 阶段 5（第 5 周及以后）：维护模式 — 方向 E

步骤：
1. 在运行时配置中添加 `maintenance_mode` 特性开关。
2. 在 health 端点之前添加中间件：如果 `maintenance_mode` 为 true 且路径不是 `/healthz` 或 `/readyz`，则返回 503。
3. 如果 `maintenance_mode` 为 true，将 gRPC 健康状态翻转为 `NOT_SERVING`。
4. 添加端点：`POST /v1/admin/maintenance`（带原因 + 可选时长）。

### 5.3 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| 双栈死代码的移除破坏了搜索 | 中（搜索同时导入两个栈） | 高（构建中断） | 在删除之前系统性地追踪所有死栈的导入。先用 `go vet` + 无导入检查扫描。 |
| 回收站恢复暴露了级联边缘情况 | 中 | 中（数据不一致） | 从仅恢复文件开始（最简单的模型）。推迟级联恢复，使用明确的"恢复文件及其共享？[是/否]"提示。 |
| 运行时配置与现有 Viper 配置冲突 | 低 | 中 | 运行时配置使用不同的命名空间（`runtime_` 前缀或单独的注册表）。记录优先级：运行时覆盖文件。 |
| 多副本运行时配置一致性问题 | 中 | 低（最终一致就足够） | 明确记录运行时配置是最终一致的。如果需要强一致性，则使用基于 DB 的轮询。 |
| 特性注册表导致应用膨胀 | 低 | 低 | 保持注册表扁平（`map[string]atomic.Value`）。没有框架，没有依赖注入。 |

---

## 总结

aero-id 的代码库在架构上大部分是健全的——严格的分层、干净的错误边界、务实的选择。但有以下主要问题：

1. **双栈死代码**（~2500-3500 行）是最高优先级的架构债务，在添加任何新功能之前必须清理。
2. 缺少回收站是一个产品层面的缺陷，与工程卓越性无关。
3. 批量操作、运行时配置和维护模式是增量式增强，可以按顺序解决，不会有重大重构风险。

审查文档正确地识别了这五个方向。方向二（运行时配置）和方向五（维护模式）有细微的事实错误（`PermSystemConfig` 用于角色管理、`health.go` 已有的退化检测），但这些并不使核心论点无效。

**立即采取的行动**：
1. 启动阶段 1：删除死代码。这是 P0 阻挡项。
2. 制定阶段 2：回收站路线图。用户数据应该可以恢复。
3. 将死栈的测试覆盖率映射到活跃栈，以确保删除后不丢失覆盖边界情况。
