现在我已经对代码库有了全面的了解。让我提供架构分析。

---

# aero-id 架构分析报告

> **分析基准：** 截至 migration 000023 的完整代码库（包含：通知偏好、折叠、存储引用计数、管理角色、未读计数器实体化、outbox 退避重试）。输入文档中的五个方向是基于此现有基座提出的后续演进。

---

## 1. 架构评估

### 1.1 当前架构优势

**分层严格性是该系统最大的结构性优势。** `handler → service → repository + storage + sso` 的单向依赖已被完全遵循：没有任何 handler 直达 repository 的短路，没有 service 直接操作数据库，没有业务逻辑泄漏到传输层。这种纪律在当前的 Go 代码库中非常罕见。

**配置化的依赖治理能力令人印象深刻。** 所有横切关注点（限流、幂等、追踪、GC、outbox、webhook）均设计为可选组件（opt-in），在 `app.go` 的组装根中通过零值检查来保证。这意味着单个二进制文件可以在"轻量开发模式"（关闭所有非必要功能）和"全面生产模式"之间切换，无需重新编译。

**Outbox 模式作为核心集成模式的选择是正确的。** 事务性 outbox（行+事件同事务写入）消除了"双写"这一分布式系统中公认的最棘手问题。与此模式保持一致的新功能（AI 管线、webhook 交付、审核队列）将自然融入现有的 relay/dispatcher 架构，无需重新设计。

**五个方向之间的现有依赖链已经建立——这不是一个假设性问题。** 回收站依赖于在文件、消息和评论中已经就绪的软删除基础设施。审核控制台可以构建在已经存在的管理角色系统（migration 000022）之上。AI 管线有 outbox 作为事件源。多组织和版本历史有清晰的实体模型作为起点。

### 1.2 关键局限

**当前的领域隔离模式存在不一致性。** 对照浏览 `internal/` 目录可以发现，有些领域（`file/`、`message/`、`comment/`、`notification/`、`block/`、`audit/`、`search/`）在 `internal/` 根目录下有自己的包，而其他领域则嵌入在 `internal/service/` 中的组合服务结构体里。这种不一致如果任其发展，会随着新领域的增加导致高扇入的 `service.go` 变得越来越臃肿。已经在 `service.go` 中定义的 `Leave`/`Join`/`Notify`/`Hide` 方法会进一步加重这一问题。

**配置验证被推迟到运行时，而非在启动时捕获。** 尽管 `cfg.validate()` 存在，但缺少配置项之间的交叉字段验证（例如，`storage.gc.enabled = true` 但 `storage.type = "local"` 时没有 retention 配置，或者 rate_limit 已启用但限流器未配置）。一个专门的配置模式（如 `cfg.PostProcess()`）+ 结构体标签验证可以更早地捕获这些问题。

**迁移编号已达到 000023，但没有明确的领域分组方案。** 当迁移数量达到 50+ 时，定位特定领域的迁移或了解哪些领域在哪些迁移中发生了变化将变得困难。一个带语义前缀的命名约定（如 `023_<domain>_<change>.up.sql`）在达到 040+ 之前应该建立起来。

### 1.3 架构债务（非阻塞，但值得关注）

| 项目 | 严重程度 | 说明 |
|---|---|---|
| `service.go` 中的方法重复 | 低 | `Create`、`Get`、`List`、`Delete` 等常见签名在 `AdminService`、`BlockService`、`CommentService` 等之间重复；CRUD 的泛化接口值得考虑，但采用 YAGNI 原则 |
| 手动接口注册 | 低 | `app.go` 中的每个新 handler 都需要在 `NewRouter` 和 `NewServerWithOptions` 中手动注册；反射式注册可能较脆弱，但一个自动化的 `HandlerRegistrar` 模式可以减少模板代码 |
| `httpapi.RouteRegistrar` 隐式耦合 | 中 | 虽然注册器模式对于解耦 handler 注册很好，但所有 handler 共享 `routes.Auth` 和 `routes.Public` 组；随着路由数量的增长（超过 50 条），路由冲突和意外重叠的风险会增加 |
| 缺少 gRPC 网关 | 中 | HTTP 和 gRPC 共享 service 层，但产生了两个独立的路由集；一个 gRPC 网关可以消除 HTTP handler，但会引入 protobuf 生成和工具链依赖的复杂度（当前设计有意避免） |

---

## 2. 扩展方向

基于现有基座（软删除存在、管理角色就绪、outbox 活跃、引用计数已部署），以下五个方向代表了从"精心设计的单实例服务"到"团队协作平台"的自然演进。每个方向都附带输入文档的补充观察以及我的架构分析。

### 🔥 方向 1：回收站（P0 — 独立，无上下游依赖）

#### 为什么需要它

**这是用户可感知的"撤销删除"的最直接路径。** 当前，`DELETE`（软删除）是永久性的——一旦标记，数据就不可见，只能由 GC 恢复。没有用户可见的恢复机制。回收站弥合了软删除语义和用户期望之间的差距。

此外，**回收站是文件 GC 安全性的关键。** 当前，存储引用计数（migration 000021）防止 blob 在还有文件行引用它时被 GC 回收，但一旦文件行被永久清除（超 retention 期），引用计数归零，blob 被物理删除。回收站提供了一个优雅的中止机制：允许用户在永久清除之前反删除文件。

#### 核心挑战

**统一路由与按资源隔离之间的权衡。** 输入文档提出了一个统一的 `trash_items` 表 `(id, resource_type, resource_id, owner_uid, deleted_at, expires_at)`，而不是在每个实体表上加 `is_in_trash` 布尔列。这是一项关键的设计决策：

- **选项 A：统一表** — 一个 `trash_items` 表，GC 逻辑集中在一处，`GET /v1/trash` 的游标分页只需查询一张表。缺点：需要资源类型的多态关联（没有外键约束），并且在反删除时需要类型特定的恢复逻辑（恢复文件需要恢复存储引用，恢复消息需要恢复消息收件箱索引，恢复评论需要恢复父引用）。
- **选项 B：每个实体的 `trashed_at` 列** — 在 `files`、`messages`、`comments` 上加 `trashed_at TIMESTAMPTZ`。反删除只是一个 UPDATE，与软删除共存（单独的时间戳）。缺点：GC 逻辑需要联合查询（`WHERE trashed_at < now() - interval '30d'`）或者三个独立的清理查询。

**我的建议：选项 A（统一表），但有明确的理由。** 统一表的设计与 `soft_delete + trash` 的双重生命周期兼容：实体的 `deleted_at` 标志实际删除，而 `trash_items.expires_at` 控制垃圾回收。统一 GC 逻辑比三个独立的清理循环更容易正确实现，而多态关联可以通过一个类型安全的枚举（而不是字符串）来管理，并在 service 层通过基于 `resource_type` 的恢复调度器来管理。

#### 预期的架构变更

```
新增：
  internal/trash/
    repository.go    — 与 repository/ 对话（或直接与 GORM 对话）
    service.go       — ListTrash / Restore / EmptyTrash / PurgeExpired
  migrations/000024_trash_items.up.sql
  handler/http/trash.go
  handler/grpc/trash_server.go

修改：
  internal/service/file.go — Delete() 写入 trash_items 行
  internal/service/message.go — Delete() 同上
  internal/service/comment.go — Delete() 同上
  internal/storage/gc/sweeper.go — 优先清理已过期回收站条目，再清理软删除
  internal/app/app.go — 注册新的 handler
```

#### 对现有系统的影响

**最小。** 回收站是纯附加的：现有的软删除保持不变。唯一的修改点是将 `DELETE` 操作重定向以同时写入 `trash_items` 表。存储层 GC sweeper 需要更新，以优先清除回收站条目，然后再清除软删除的行——但这是一个逻辑变更，而非架构变更。

---

### 🔥 方向 2：审核控制台（P1 — 与 AI 管线形成上下游依赖链）

#### 为什么需要它

**这是角色系统（migration 000022）的自然延伸。** 没有审核控制台，管理角色只是元数据——它们有权限，但没有工作台来行使权限。审核控制台将权限转化为工作流。

输入文档正确地观察到，审核控制台和 AI 管线形成一个上下游链条：控制台接收举报→将内容传递给 AI 进行审核→将结果返回给控制台供人工确认。这种链条意味着审核控制台应该先于 AI 管线构建（或至少与其并行构建），因为控制台定义了 AI 集成点的接口。

#### 核心挑战

**审核队列设计。** 当前的审核模型是作者自管的——作者在自己的评论上查看和解决举报。全局审核控制台需要将所有实体（评论、消息、文件）的举报聚合到一个统一的队列中。队列设计必须解决：

- **优先级排序：** 是按报告数量排序（最多举报的置于顶部）、按时间排序（最旧的优先），还是按严重程度排序（带关键词的举报优先）？
- **工作分配：** 是将举报分配给最近的审核员，还是轮询分配？审核员能否声明某个举报？
- **操作原子性：** 审核操作（隐藏、警告、封禁）是否为原子的？审核日志是否捕获审核员身份和原因？

**审查中的竞态条件**（如 `REVIEW_2026-06-30_PRODUCT_GAPS.md` 所述）必须被解决。多个审核员针对同一内容的操作必须具有确定性语义——一种简单的方法是引入"操作日志"模式，其中每次审核操作都会被追加记录，而内容状态是所有操作中"最严格"的应用（例如，如果任何审核员隐藏了内容，即使后续操作是"驳回"，内容也保持隐藏状态）。

#### 预期的架构变更

```
新增：
  internal/moderation/
    queue.go       — 审核队列（全局举报列表 + 优先级排序）
    actions.go     — 审核操作（隐藏、警告、封禁、内容移除）
    service.go     — 审核工作流编排
  migrations/000025_moderation_up.sql — moderation_actions 表 + 扩展 comment_reports

修改：
  internal/service/comment.go — Report() 写入 moderation queue
  internal/handler/http/admin.go — 新增 /v1/admin/moderation/* 端点
  internal/app/app.go — 注册 moderation 服务
```

---

### 🔥 方向 3：AI 管线（P1 — 链接在审核控制台下游）

#### 为什么需要它

**输入文档正确地指出了这是 outbox 的典型场景。** AI 审核延迟不敏感但需要异步处理——这正是 outbox 模式的设计目标。内容创建后 → 事件排入 outbox → relay 分派给 AI Moderation handler → AI 审核结果回调写入 moderation queue。

**O2 观察（outbox LISTEN/NOTIFY 集成）是可靠的但需要权衡。** 将当前基于轮询的 relay 替换为 LISTEN/NOTIFY 可以将审核延迟从秒级（轮询间隔）降低到毫秒级（触发器→通知），但代价是增加了复杂度：

- **选项 A（基于轮询）：** 当前模式。每 5 秒轮询一次未发布的事件。保证最多 5 秒的延迟。简单，自包含，无需 PostgreSQL 扩展。
- **选项 B（轮询 + NOTIFY 加速器）：** `pg_notify` 在 outbox INSERT 时触发，relay 上的 goroutine 监听通知。当收到通知时，立即触发轮询（而不是等待下一个 5 秒间隔）。延迟变为亚秒级，同时保留了轮询作为故障安全机制。
- **选项 C（纯 NOTIFY 驱动）：** 移除轮询；仅依赖 LISTEN/NOTIFY。引入空通知丢失的风险（如果连接中断则可能丢失通知），并且需要在 relay 上进行消息循环。

**我的建议：选项 B** 作为增量步骤。`LISTEN` goroutine 在 `relay.Start()` 中是最多 20 行代码，对现有轮询基础设施零风险。当轮询在 NOTIFY 触发时被唤醒，延迟变为亚秒级；如果 NOTIFY 连接断开，轮询提供端到端的可靠性。

#### 核心挑战

**AI 抽象接口必须通用，而非针对审核定制。** 输入文档暗示了 AI 管线的两个潜在阶段：审核（立即）和摘要/嵌入（未来）。抽象必须服务于两者：

```go
type AIService interface {
    // Moderate returns a moderation verdict for the given content.
    Moderate(ctx context.Context, content *ModerationInput) (*ModerationResult, error)
    
    // Embed returns a vector embedding for the given content.
    Embed(ctx context.Context, content string) ([]float32, error)
    
    // Summerize returns a concise summary.
    Summerize(ctx context.Context, content string, maxLength int) (string, error)
}
```

这种抽象使后端（当前为审核）与底层提供商解耦——无论是内部模型、OpenAI 还是 Anthropic。它为 provider 路由（"审核使用内部模型，嵌入使用 OpenAI"）打开了可能性，而无需修改服务层。

#### 预期的架构变更

```
新增：
  internal/ai/
    interface.go   — AIClient / Moderator / Embedder / Summerizer 接口
    openai.go      — OpenAI 实现
    local.go       — 本地/自托管模型包装器
  internal/ai/provider/
    router.go      — 按任务类型的 provider 路由
  
修改：
  internal/outbox/relay.go — 选项 B NOTIFY 加速器（10 行变更）
  internal/moderation/service.go — AI moderation handler（outbox 事件消费者）
  internal/app/app.go — AI 客户端组装 + moderation handler 注册
  configs/config.yaml — AI provider 端点 + 超时 + 回退配置

注意：
  - 在内容送入外部 LLM 之前，consents 表（设计文档 11.2 部分）是必需的。
  - 用户应主动选择允许其数据被用于 AI 处理（opt-in，而非 opt-out）。
```

---

### 🔥 方向 4：多组织（P2 — 渐进式，经过共享可见性阶段）

#### 为什么需要它

**输入文档的观察 3 是务实且明智的。** 在引入 `org_id` 列之前，`visibility` 中的 `shared` 中间状态提供了一种低成本的方式来模拟团队空间语义，而无需架构变更。

当前的可见性模型是二元的（`private | public`）。`shared` 状态意味着"文件已与他人共享"——这是一个可见性类别，而不仅仅是一个通过 grant 实现的行为`shared` 状态使 UI 能够显示："此文件是共享的（与 5 人）"，在列表视图中不需要检查 grant 表。更重要的是，它提供了一个渐进式的迁移路径：

1. **阶段 0（现在）：** `shared` 可见性值 + 现有的 grant 表。文件可以标记为 `shared` 并在 UI 中显示为共享。
2. **阶段 1（未来）：** `org_id` 列。`shared` 可见性自然演变为 "组织范围内可见"（无需求助于特殊的 `org` 可见性值）。
3. **阶段 2：** 完整的团队空间，包含组织范围的 ACL 和成员管理。

#### 核心挑战

**`shared` 是一个可见性状态，而不是一个行为。** 当前，`visibility` 控制读访问：`private` = 仅所有者，`public` = 所有人。`shared` 从语义上讲是"仅限被授予者"——它不改变 ACL 逻辑（与 `private` 相同），但改变 UI 呈现方式。这引入了可见性与授权之间的语义交叉：

- `private` 文件：仅所有者可见。未共享或在共享前。
- `shared` 文件：仅所有者 + 被授权者可见。已共享。
- `public` 文件：所有人可见。在搜索结果中可见。

架构影响很小：`visibility` 枚举增加一个值，ACL 查询添加 `or files.visibility = 'shared' and ...（通过 grant 表授权）`。不改变表结构。

#### 预期的架构变更

```
立即（阶段 0）：
  修改：
    internal/domain/shared.go — 添加 VisibilityShared 常量
    internal/repository/file.go — 列表查询包含 grant 连接（OR 条件）
    internal/service/file.go — CreateFileInput 和 UpdateMetadataInput 接受 Shared
    internal/service/service.go — canReadFile 检查 Shared 的 grant 表
    
  无迁移——可见性是一个 Go 枚举，映射到 SMALLINT，当前未使用值 1 的 public。

未来（阶段 1+）：
  新增：
    migrations/000026_org_id.up.sql — 为 files/messages/comments 添加 org_id
    internal/org/ — 组织服务（CRUD + 成员管理）
```

---

### 🔥 方向 5：版本历史（P2 — 服务于团队协作愿景）

#### 为什么需要它

**文件版本控制是协作的基本要求。** 当前的文件模型是幂等的：上传一个具有相同名称的文件会替换之前上传的内容（通过内容去重）。没有历史记录，没有回滚，没有"恢复为之前版本"的操作。

版本控制也服务于消息编辑：当前，`messages` 有一个 `edited_at` 时间戳，但保存之前内容的版本历史意味着编辑不再是破坏性的。这增强了信任（接收方可以看到编辑了哪些内容）。

#### 核心挑战

**存储成本。** 每个版本存储文件的完整 blob 在存储上成本高昂。替代方案包括：

- **选项 A：全量复制。** 每个版本都是完整的 blob。简单，恢复速度快，但存储成本与服务中的版本数成正比。
- **选项 B：基于差异的（二进制 diff）。** 只存储从前一版本的差异。存储高效，但需要差异计算算法（bsdiff、xdelta）并为每个读取操作增加恢复延迟。
- **选项 C：混合——N 个全量副本 + 差异。** 保留最后 10 个版本为完整副本；较旧的版本存储为差异。为最近/最常用的操作提供快速恢复，同时保持存储可控。

**我的建议：选项 A（全量复制），有明确的上限（每个文件最多 50 个版本）。** 存储很便宜，而差异算法增加了复杂度和恢复延迟，而收益甚微。全量副本意味着恢复是一个简单的存储键切换，而不是差异重建步骤。版本上限可以通过 GC 在文件级强制执行（当超过 50 个版本时，删除最旧的版本）。

#### 预期的架构变更

```
新增：
  internal/file/version.go — 版本创建/恢复/列出
  migrations/000026_file_versions.up.sql — file_versions 表

修改：
  internal/service/file.go — UploadFile 基于先前版本创建版本行
  internal/storage/gc/sweeper.go — 按文件级强制执行版本上限
```

---

## 3. 接口设计建议

### 3.1 跨方向接口设计原则

**原则 1：Outbox 作为持久化事件总线。** 所有跨方向集成（AI 审核 → 审核控制台、文件创建 → 预览生成、内容编辑 → 版本创建）都应通过 outbox 事件进行。这样就可以：

- 无需点对点集成
- 事件重放用于数据恢复
- 审计跟踪跨方向的事件
- 背压通过积压指标自然实现

```
FileCreated → outbox → AI Moderation Handler → ModerationResult → outbox → Moderation Queue
  
FileUploaded → outbox → Thumbnail Handler → PreviewCreated → outbox → SSE push to client
```

**原则 2：策略注入而非继承。** `NotificationDispatcher` 的 `WithDispatcherBlockChecker`、`WithDispatcherPreferenceChecker`、`WithDispatcherRateLimiter` 模式是正确的，并且应该被新方向复用。审核控制台应该接收一个 `ReviewPolicy` 接口（决定什么值得审核），AI 管线应该接收一个 `ModerationPolicy` 接口（决定调用哪个 AI provider）。

**原则 3：传输层作为薄适配器。** HTTP 和 gRPC handler 应该保持现状。新方向应该以相同的方式添加：定义一个 service 接口，为两个传输协议编写 handler，在 `app.go` 中注册。这种方法已经过验证。

### 3.2 关键新抽象

**上下文提供者抽象。** 鉴于 `actor_uid` 通过上下文传递的模式，并且 org、版本、审核操作都需要访问当前上下文，一个显式的 `RequestContext` 或 `CallContext` 结构体可能会简化签名：

```go
type CallContext struct {
    ActorUID string
    // 未来扩展：
    // OrgID     string
    // RequestID string
    // UserRoles []string
}
```

这不是当前必需的，但当请求上下文扩展到超过 actor UID 时，值得考虑。

### 3.3 保持向后兼容性

**三个关键策略：**

1. **可选字段 + 零值作为"无变化"。** 如 `UpdateFileMetadataInput` 中使用的 `*string` 模式，应该扩展到所有可变数据结构。
2. **版本化 API 路径。** 当前路由在 `/v1/` 下。如果方向 2（多组织）所需的架构变更导致破坏性变更，`/v2/` 组应该在不迁移的情况下与 `/v1/` 共存。一个 `WithVersion("v1")` / `WithVersion("v2")` 的 `RouteRegistrar` 过滤器可以支持这一点。
3. **迁移兼容性。** 新列应该允许为 `NULL` 或具有默认值，以便现有的 SQL 查询继续工作直到显式更新。

---

## 4. 技术选型

### 4.1 需要引入的内容

| 技术 | 方向 | 理由 | 替代方案 |
|---|---|---|---|
| **PostgreSQL NOTIFY** (内置) | 方向 3（AI 管线） | 无需新依赖。`pg_notify` + `lib/pq` LISTEN 将 outbox 延迟从秒级降低到亚秒级。 | Redis pub/sub（引入 Redis 依赖）、轮询（保持秒级延迟） |
| **AI provider SDK** (OpenAI/Anthropic) | 方向 3（AI 管线） | LLM 审核需要外部提供商。在内部接口后抽象。 | 自托管模型（vLLM、Ollama）——成本更高但数据驻留合规 |
| **ImageMagick / libvips** | 不在范围但值得注意 | 缩略图生成。Dockerfile 依赖。 | 基于 Go 的缩略图（`govips`、`nfnt/resize`）、serverless（Lambda 图像处理） |

### 4.2 值得推迟的内容

| 技术 | 方向 | 为什么推迟 |
|---|---|---|
| **Redis** | 分布式限流、缓存 | 方向 5（缓存层）在 P3。在需要之前，每实例限流和没有缓存是可以接受的。 |
| **RabbitMQ / NATS** | 事件驱动 | Outbox 模式消除了专用消息代理的需求。Postgres 作为队列是好的选择。 |
| **gRPC 网关** | HTTP ↔ gRPC 统一 | 增量重复 HTTP handler 用于新方向增加了前期工作，但保持了简单性。网关在路由数 > 100 时才有意义。 |
| **Kubernetes CRD** | 多组织 | 组织管理可以完全在应用程序 SQL 中处理。 |

### 4.3 自建与采购的决策依据

**方向 3（AI 管线）的 AI 集成：采购（提供商 SDK）**，内部抽象接口。原因是：

- AI 提供商市场变化迅速。OpenAI 今天可能不是明天的最佳选择。接口隔离防止 vendor lock-in。
- 审核模型需要特定的微调或提示工程——这不是可以购买然后遗忘的。
- 数据驻留要求（请参见 consents 表）因地缘政治边界而异——抽象层允许每个地区的提供商路由。

**方向 5（版本历史）的版本存储：自建。** 存储很便宜，差异算法很复杂。带有固定保留上限的全量复制消除了对专门版本控制软件（Git LFS、专用的 VCS）的需求。

---

## 5. 实施路线图

### 5.1 优先级与依赖关系

```
P0（立即——非阻塞技术债 + 回收站）
├── 回收站（统一 trash_items 表）
│   ├── 迁移：trash_items 表
│   ├── 核心：trash service（列出、恢复、清空、清除过期项）
│   ├── 集成：挂接到现有 DELETE handler
│   └── GC 更新：sweeper 优先清除回收站条目
│
├── 通知部分复合索引
│   ├── 如果尚未存在，则添加 (recipient_uid, read_at, created_at DESC)
│   │   WHERE read_at IS NULL
│   └── 确认现有的 000017 迁移

P1（短期——合规 + 基础设施）
├── 审核控制台
│   ├── 迁移：moderation_actions 表 + 队列视图
│   ├── 核心：审核队列（全局举报列表 + 优先级排序）
│   ├── 核心：审核操作（隐藏、警告、封禁）带确定性语义
│   ├── API：/v1/admin/moderation/* 端点
│   └── 集成：通过 outbox 事件链接 AI 管线
│
├── AI 管线
│   ├── 抽象：AI 接口（Moderator、Embedder、Summerizer）
│   ├── 集成：O2 NOTIFY 加速器（在 relay 上 +10 行代码）
│   ├── 提供商：OpenAI SDK（如果它存在则内部，否则自托管）
│   ├── 合规：consents 表（在设计文档中已预留）
│   └── 处理程序：AI 审核 outbox 消费者 → 回调到审核队列
│
└── 材料化未读计数器的后台协调器
    └── cmd/reconcile-unread（在 000023 中已预留，但在 TODO.md 中未标记为完成）

P2（中期——团队协作）
├── 共享可见性 + 多组织阶段 0
│   ├── 领域：shared 可见性枚举值
│   ├── ACL：为 visibility=shared 的文件更新 canReadFile 以检查 grant 表
│   └── 领域：org_id 列 + 组织/c 组织服务
│
├── 版本历史阶段 0（文件）
│   ├── 迁移：file_versions 表
│   ├── 核心：上传时创建版本，通过键切换恢复
│   └── GC：每文件级强制执行版本上限（50）
│
└── 审核工作流增强
    ├── AI 提醒（将内容发送给 AI，标记为"待审核"）
    ├── 分析仪表板（举报趋势、审核员工作效率）
    └── 升级路由（复杂的举报 → 高级审核员）
```

### 5.2 里程碑

| 里程碑 | 交付物 | 验收标准 |
|---|---|---|
| **M1：回收站就绪**（2 周） | 用户可以将文件/消息/评论移至回收站并在 30 天内恢复 | 手动测试通过：删除→列出回收站→恢复→确认内容可访问 |
| **M2：审核控制台上线**（3 周） | 管理员可以查看、分配和解决举报；审核审计日志 | 集成测试：管理员 → 解决举报 → 隐藏内容 → 确认 UI 反映状态 |
| **M3：AI 审核试点**（2 周） | 出站事件驱动 AI 审核；结果反馈到审核队列 | 集成测试：发起举报 → 出站事件 → AI 处理程序 → 审核队列更新 |
| **M4：团队空间 MVP**（4 周） | `shared` 可见性 + 组织 CRUD + 组织范围的 ACL | 集成测试：用户 A 与组织 O 共享文件 → 用户 B（组织 O 成员）读取文件 |
| **M5：文件版本控制**（3 周） | 文件上传创建版本 + 回滚能力 | 集成测试：上传 → 编辑 → 恢复 → 确认内容是编辑前的版本 |

### 5.3 风险与缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| **回收站存储增长超过预期** | 中 | 存储成本，GC 压力 | 固定保留期（30 天）+ 每用户的回收站大小上限（例如，回收站中最多 1GB），超过上限的项目在保留期之前被驱逐 |
| **AI 提供商不可用或返回无意义结果** | 高 | 审核延迟，审核员信心下降 | 具有可配置超时的断路器（遵循 SSO 客户端的模式）；`ModerationResult` 中的置信度评分；低置信度评分的内容回退到人工审核 |
| **NOTIFY 连接在长时间运行的生产实例上断开** | 中 | 未检测到的丢失通知，事件延迟到下一个轮询间隔 | 健康检查 pings（`LISTEN` 连接上的 `statement_timeout`）+ 计量器（`last_notify_timestamp`）；如果通知在 2 个轮询间隔后未到达，则记录 WARN |
| **多组织 SQL 查询重复** | 高 | N+1 查询，复杂连接，难以调试的 GORM 行为 | 在 repository 层使用中间件/钩子自动附加 `AND org_id = ?` 到查询中（如 GORM 的 `Where` 钩子，或原始 SQL 包装器）+ 针对组织粒度查询的严格集成测试 |
| **版本历史存储成本失控** | 中 | 意外的存储费用增长 | 每个文件的版本上限（50）+ 可配置的最大总存储（"您每个文件的版本已达到 50 个——请整理旧版本"）+ 后台清除旧版本的作业 |

### 5.4 不在上述路线图中的额外观察

**输入文档的 O3 观察（共享可见性作为多组织的渐进步骤）值得单独强调。** 它遵循一个非常强大的架构模式："通过扩展现有模型来实现新语义，然后再引入新模型。"共享可见性是"文件与他人共享"的标志——这个概念用户已经理解（通过 grant 表）。将其提升为可见性级别意味着列表查询现在可以通过 `WHERE visibility = 'shared' AND ...` 过滤"共享文件"，而无需子查询。这是一个与产品定义完美匹配的架构优化。

**输入文档的 O1 观察（通知索引）在技术上是正确的，但可能已经由 000017 迁移解决了。** 我需要检查 `000017_notification_indexes.up.sql`（上面已读取）来确认。它创建了 `idx_notifications_recipient_created (recipient_uid, created_at DESC)` 和 `idx_notifications_recipient_unread (recipient_uid) WHERE read_at IS NULL`。建议的索引是 `(recipient_uid, read_at, created_at DESC) WHERE read_at IS NULL`——这是一个复合部分索引，与 O1 观察到的完全相同。**已解决。**

**O2 观察（outbox LISTEN/NOTIFY）方向是正确的，但必须保留轮询作为故障安全机制。** NOTIFY 连接在所有 PostgreSQL 驱动程序中本质上都是脆弱的（网络中断、连接池耗尽、`LISTEN` 在透明重新连接后丢失）。轮询 + NOTIFY 加速器提供了两全其美的方案。

---

## 总结

aero-id 处于从"精心设计的单实例服务"到"平台"的过渡点。五个方向构成了通往产品就绪协作系统的连贯路径：

| 方向 | 架构本质 | 风险 | 价值 |
|---|---|---|---|
| 回收站 | 生命周期管理 | 低 | 高（用户信任） |
| 审核控制台 | 工作流引擎 | 中 | 高（合规门线） |
| AI 管线 | 事件驱动集成 | 中-高 | 高（差异化） |
| 多组织 | 数据隔离模型 | 高 | 高（可寻址市场扩展） |
| 版本历史 | 数据谱系 | 低-中 | 中（留存增强器） |

路线图的执行应遵循渐进原则：每个方向都应作为一个独立的模块边界构建，与系统的其他部分通过 outbox 事件和已建立的接口模式进行通信。当前的架构基础（分层、双向传输、事务性 outbox、松耦合组件）为这种增长提供了坚实的基础，同时将回归风险降至最低。
