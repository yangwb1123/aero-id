我已经完整读取了 `2026-07-10-fifth-wave-platform-extensions.md`（以下称"本文"），这是一份质量极高的分析文档。以下我以资深架构师视角，对本文的五方向及其设计建议进行系统性评审。

---

# 架构评审：aero-id 平台化五方向扩展

> 评审对象：`docs/requirements/2026-07-10-fifth-wave-platform-extensions.md`
> 
> 评审角色：资深架构师
> 
> 评审日期：2026-07-12

---

## 1. 架构评估

### 1.1 当前架构的优势

本文揭示的 aero-id 架构成熟度极高，值得肯定：

- **严格单向依赖**：`handler → service → repository` 的清晰分层，让本文的每个扩展方向都能在不破坏现有架构的前提下，以"新增包"或"修改单一包"的方式落地。这是架构质量的硬指标。
- **Outbox 模式作为事件骨干**：事务性 outbox + relay + 指数退避重试 + 死信队列的完整管道，为方向一（Webhook 平台化）提供了坚实底座——不需要重造轮子，只需要在现有管道的末端扩展。
- **`File.Meta JSONB` 的设计前瞻性**：虽然当前 API 对其零查询支持，但数据模型层面已经预留了扩展点。这是"模型先于 API"的正确做法。
- **存储 GC (`storage/gc/sweeper.go`) 的可复用模式**：go routine + 调度 + 分批处理的模式，可以直接复制到方向三（统一生命周期治理）的 sweeper 架构中——这是架构复用性的体现。

### 1.2 关键架构局限

本文没有明确指出的结构性局限：

| 局限 | 分析 | 影响方向 |
|------|------|---------|
| **传输层与业务层耦合过密** | 当前 `handler/{http,grpc}` 都在同一个 `internal/` 下，且 HTTP 和 gRPC handler 各有自己的请求/响应结构体。当引入新方向（如集合资源）时，需要在两个传输层各实现一套 handler，且保持行为一致——这是"显式成本"而非"自动获得的收益"。 | 方向④集合模型、方向②元数据查询 |
| **无统一的查询抽象层** | 当前 repository 层的 List 方法签名是硬编码的（`ListByOwner(ctx, owner, page)`），不支持动态 WHERE 条件组合。方向二的元数据查询是第一个打破这种硬编码边界的方向——但这需要 repository 层支持可组合的查询条件，这会改变 repository 接口的契约。 | 方向②结构化元数据查询 |
| **实体包之间的职责边界定义不一致** | `internal/file/`、`internal/message/`、`internal/comment/` 等包的职责边界清晰，但它们的"外围关系"（评论引用消息、消息引用文件、通知引用各类实体）没有在架构层面统一管理。方向三的 `EntityExecutor` 接口试图统一定义这个边界，但这是"事后治理"而非"设计时契约"。 | 方向③数据生命周期治理 |
| **没有统一的配置管理抽象** | 当前配置是全局 `config.Config` 结构体，所有包直接读取。方向一的 Webhook 多订阅者需要每个订阅者有独立的 `timeout`、`max_retries`、`retry_backoff`——这些不是全局配置，而是实体级配置。当前配置模式不支持这种"每实体配置"模式。 | 方向①Webhook 平台化 |
| **测试策略不覆盖集成点测试** | 现有单元测试覆盖 service 层（通过 mock），但缺少跨包的集成测试。方向三（级联删除）和方向四（集合成员引用外部实体）的正确性高度依赖多包协作的原子性——这个测试空白是风险。 | 方向③、方向④ |

### 1.3 架构债务评估

| 债务项 | 严重程度 | 由本文揭示/引发 |
|--------|---------|----------------|
| `outbox_events` 表无过期清理策略 | **高** — 会无限增长 | 方向三 |
| 通知类型字符串散落在 switch 语句中 | **中** — 维护负担 | 方向三（匿名化时需要全局替换 uid 引用） |
| 无 JSONB 索引 | **中** — 随 meta 数据量增长性能会退化 | 方向二 |
| Webhook 单 endpoint 设计 | **中** — 无法满足多外部消费者 | 方向一 |
| 软删实体无级联约束 | **高** — 孤儿数据持续累积 | 方向三、方向四（集合成员指向已删除资源） |

---

## 2. 扩展方向评审

### 2.1 方向一：Webhook 交付平台化

**设计评价：** 优秀。从"功能"到"平台"的跃迁设计合理。

**关键建议：**

1. **避免在 outbox Event 上直接耦合 delivery 字段**。本文建议在 `outbox_events` 表上增加 `delivery_attempts`、`last_error` 等字段。但 outbox 是"事件日志"，webhook 是"事件消费者"的一种。更好的做法是：

   **选项 A（本文方案）：** 在 `outbox_events` 表加 delivery 字段
   - 优点：查询递送历史只需要单表查询
   - 缺点：outbox 模型变得臃肿；引入新的 webhook consumer（如 Kafka）时，这些字段派不上用场

   **选项 B（建议）：** 新表 `webhook_delivery_logs(event_id UUID, subscriber_id UUID, attempt INT, status TEXT, error TEXT, latency_ms INT, created_at TIMESTAMPTZ)`
   - 优点：outbox 模型保持纯净；递送日志独立扩展；可以对递送日志做独立的分区/归档
   - 缺点：查询递送历史需要 JOIN 或两次查询
   - **建议采用选项 B**——关注点分离原则在此适用。outbox 应该只关心"事件是否已被至少一个消费者确认"，而不关心"具体哪个消费者递送了几次"。

2. **密钥轮换的 72h 窗口设计合理**，但需要补充：
   - 紧急轮换路径（`?immediate=true`）：当已知密钥泄露时，不应等待 72h 过渡期
   - 密钥版本元数据在事件头中：`X-Event-Signature-Version: v2`，方便消费者快速定位使用哪组密钥验证

3. **依赖关系冲突**：方向一（Webhook 平台化）需要方向五（变更安全）中的 API 兼容性检查来保证 `POST /v1/webhooks` 新端点不会破坏已有客户端。实施顺序上，方向五应先于方向一。

**复杂度评估：** 本文估算 2 周，合理。但其中 3-4 天应分配给契约建立（proto 定义 + API 文档 + 幂等协议）。

### 2.2 方向二：结构化文件元数据查询

**设计评价：** 4 天工作量的投入产出比极高，设计清晰。

**关键建议：**

1. **操作符约定需要扩展到 gRPC API**。本文的 `__gt`、`__contains` 等后缀约定对 HTTP query string 很自然，但 gRPC 请求是 protobuf 结构体。gRPC 侧的 API 设计有两条路径：

   **选项 A（map 结构）：**
   ```protobuf
   message MetaCondition {
     string key = 1;
     enum Operator { EQ, GT, GTE, LT, LTE, CONTAINS, EXISTS, IN }
     string value = 2;  // JSON 编码的标量值或数组
   }
   message ListFilesRequest {
     // ...existing fields...
     repeated MetaCondition meta_conditions = 10;
   }
   ```

   **选项 B（flat 字段）：**
   ```protobuf
   message ListFilesRequest {
     // ...existing fields...
     map<string, string> meta_eq = 10;      // 精确匹配
     map<string, string> meta_gt = 11;      // 大于
     map<string, string> meta_contains = 12; // ILIKE
     repeated string meta_exists = 13;       // 键存在
   }
   ```
   
   **建议采用选项 A**。选项 B 的 flat 字段设计在操作符扩展时（如新增 `__ncontains`、`__regex`）需要新增字段，而选项 A 只需要在枚举中增加值。Proto 兼容性更好。

2. **需要定义查询条件的逻辑组合**。当前设计只支持 AND 组合（所有条件同时满足）。未来是否需要 OR？建议在设计时预留：
   - 第一版只支持 AND（显式文档说明）
   - proto 中 `MetaCondition` 的 repeated 天然是 AND
   - 为将来预留 `meta_conditions_operator` 枚举（AND/OR），但第一版不实现 OR

3. **SQL 注入防护**：`meta->>'key' ILIKE '%val%'` 中的 `key` 和 `val` 都来自用户输入。即使 PostgreSQL 的 JSONB 操作符有类型安全，也必须在 repository 层使用参数化绑定，而非字符串拼接。本文没有明确提及这个安全边界——**这是必须细化的实现约束**。

4. **索引与性能**：
   - GIN 索引（`idx_files_meta_gin`）覆盖 `@>` 和 `?` 操作符，但不覆盖范围查询（`__gt`/`__lt`）
   - 对范围查询，`btree` 索引是必要的，但需要用户指定哪几个 meta 键高频使用
   - 建议 GIN 索引作为默认，btree 索引作为可配置选项（在 `configs/config.yaml` 中声明高频查询字段）

### 2.3 方向三：数据生命周期统一治理

**设计评价：** 需求正当，但这是五个方向中**架构影响最大**的，本文的估算（3 周）偏乐观。

**关键建议：**

1. **`EntityExecutor` 接口的设计需要更谨慎**。本文建议：
   ```go
   type EntityExecutor interface {
       PurgeExpired(ctx context.Context, policy RetentionPolicy, limit int) (int, error)
       CascadeDelete(ctx context.Context, uid string) error
       AnonymizeUID(ctx context.Context, oldUID, newUID string) error
   }
   ```

   这个接口将三种不同的生命周期操作（过期清理、级联删除、匿名化）耦合在一起。建议分离——因为它们的变化频率不同、错误处理策略不同、事务边界不同：
   ```go
   // 只负责"按策略硬删除过期数据"
   type PurgeExecutor interface {
       Purge(ctx context.Context, policy RetentionPolicy, limit int) (int, error)
   }
   
   // 只负责"按 uid 级联删除"
   type CascadeDeleter interface {
       CascadeDelete(ctx context.Context, uid string, opts DeleteOptions) error
   }
   
   // 只负责"匿名化 uid 引用"
   type Anonymizer interface {
       Anonymize(ctx context.Context, oldUID string) error
   }
   ```

   接口分离原则：如果某个实体（如 `outbox_events`）需要过期清理但不需要级联删除，它就不需要实现 `CascadeDelete`。

2. **级联删除的事务边界**。跨实体级联删除（如"删除用户 → 删除评论 → 删除评论的点赞"）不能在单个数据库事务中完成（可能涉及数十万行）。需要引入 saga 模式或 outbox 驱动的最终一致性：

   ```
   主请求
    ├→ outbox 事件: "delete_user.{uid}"
    ├→ file-executor 消费 → 硬删文件
    ├→ message-executor 消费 → 硬删消息 + reactions
    ├→ comment-executor 消费 → 硬删评论 + likes + reports
    └→ notification-executor 消费 → 清理通知
   ```

   每个 executor 的操作都是原子的（小事务），但整体是最终一致的。需要有：
   - 补偿操作（如果文件删除失败，已删除的消息需要回滚？或者只需要记录失败继续执行？）
   - 进度追踪表 `lifecycle_operations(id, uid, status, progress, error)`

3. **Legal Hold 的设计需要数据级而非仅策略级**。当前设计在 `retention_policies` 表中有 `legal_hold boolean`。但 legal hold 通常是针对特定实体（"这批文件正在诉讼中"）而非针对整个实体类型。建议：
   ```sql
   CREATE TABLE legal_holds (
       id UUID PRIMARY KEY,
       target_uid TEXT,        -- 某个用户
       target_file_id UUID,    -- 或某个具体文件
       reason TEXT NOT NULL,
       placed_by TEXT NOT NULL, -- 管理员 uid
       expires_at TIMESTAMPTZ, -- 到期自动解除
       created_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   ```
   检查逻辑：删除前先检查 `legal_holds` 表中是否有匹配的条目。

4. **SSO de-provisioning 的延迟一致性**。SSO 调用 `HandleUserDeleted` RPC 后，aero-id 不应当同步完成所有删除操作（可能耗时数分钟）。设计应当是：
   - SSO 调用 → aero-id 返回 202 Accepted + operation_id
   - aero-id outbox 触发异步级联删除
   - aero-id 提供 `GET /v1/admin/deletion-operations/:id` 查询进度
   - 完成时可选通知 SSO（通过回调 webhook）

5. **估算调整**：3 周可能低估。仅 `EntityExecutor` 在每个实体包的实现、单元测试、集成测试、以及级联删除的进度追踪+saga 设计，保守估计 4-5 周。

### 2.4 方向四：集合式资源模型

**设计评价：** 用户体验价值最高，但技术风险也最高。

**关键建议：**

1. **多态引用的正确性保证是整个方向的最大风险**。`collection_members.entity_id` 指向 `files/messages/comments` 的行，但数据库层面**没有外键约束**（因为是多态的）。这意味着：
   - 当资源删除时，集合成员变成悬挂指针
   - 没有级联删除保护
   
   解决策略：
   **选项 A（触发器维护）：** 每种实体表的 `AFTER DELETE` 触发器清理 `collection_members` 中的引用。但 GORM 软删除不触发 SQL trigger（`UPDATE deleted_at` 而非 `DELETE`）。
   **选项 B（应用层维护）：** 在资源删除的 service 方法中，增加对 `collection_members` 的清理。但需要在所有实体（file/message/comment）的 service 中同步修改——维护脆弱。
   **选项 C（集合成员添加 entity_deleted_at 字段 + 后台清理）：** 不主动清理，而是在查询集合成员时过滤 `entity_deleted_at IS NOT NULL` 的行；后台 sweeper 定期清理标记行。
   
   **建议采用选项 C + 方向三的 LifecycleSweeper 联动**。选项 C 不需要修改现有实体包的删除逻辑，新增的 `entity_deleted_at` 字段不会阻塞集合的正常使用。结合方向三的 `PurgeExecutor` 清理过期的孤儿成员。

2. **集合的分享模型需要明确边界**。本文提到"共享集合"但未深入设计。集合分享比文件分享复杂——因为它包含的动态资源列表需要独立于集合本身的 ACL。
   - 建议：集合分享复用 `file_shares` 的多态模式（改名为 `resource_shares`），但集合的"查看"权限不等于集合成员的"查看"权限。
   - 例如：Alice 共享了集合"项目文档"给 Bob，Bob 可以看到集合结构和文件列表，但 Bob 访问集合中的某个文件时仍需文件级的权限检查。

3. **与方向二（元数据查询）的关系**。方向四和方向二可以产生强大的组合效应：
   - `GET /v1/collections/:id/files?meta.type=contract`
   - 这要求方向四的 API 在查询集合成员时，复用方向二的 meta 条件解析能力
   - 实施顺序上，方向二应先于方向四

4. **估算调整**：2 周可能低估。仅权限模型设计（集合分享 + 成员权限独立检查）就需要额外的 2-3 天设计 + 实现。建议调整为 3 周。

### 2.5 方向五：变更安全管理与预发布治理

**设计评价：** 预防性投入的 ROI 最高，但部分建议的实施方案需要更深入设计。

**关键建议：**

1. **Shadow Migration 的 PostgreSQL DDL 事务性限制需要更明确的处理策略**。PostgreSQL 中，以下 DDL **不能在事务内回滚**：
   - `CREATE INDEX CONCURRENTLY`（不在事务内运行）
   - `DROP INDEX CONCURRENTLY`
   - `CREATE TABLESPACE`
   - `ALTER TABLESPACE`
   
   解决方案：
   - 对于 `CREATE INDEX`：先验证语法（EXPLAIN），再在 shadow database 上使用非并发的 `CREATE INDEX`（在事务内），但在生产使用 `CREATE INDEX CONCURRENTLY`（不在事务内）
   - Shadow run 应分为"语法安全检查"和"性能影响评估"两个阶段

2. **Schema diff 的工具选型需要讨论**。本文建议使用 GORM 的 `AutoMigrate` 来生成目标 schema。但这有局限性：
   - GORM `AutoMigrate` 默认只添加列和索引，不删除列——因此 diff 会漏报"模型已删除但数据库仍有的列"
   - GORM 的 `AutoMigrate` 不处理复杂约束（复合主键、部分索引、排除约束）
   
   **替代方案**：使用 `pg_dump --schema-only` 导出两个数据库的 schema，然后用 `apgdiff` 或 `pgquarrel` 做精确的 schema diff。
   
   建议：`check-schema-diff` 工具采用"双路径"策略：
   - 路径 A：GORM `AutoMigrate` 对比（快速、覆盖 90% 的变更）
   - 路径 B：`pg_dump` + `apgdiff` 对比（完整、覆盖全部变更，但运行较慢）
   - CI 中默认运行路径 A，每周运行一次路径 B

3. **Proto 兼容性检查的工具链**。本文提到 `buf breaking` 或 `protolock`。需要明确：
   - `buf breaking --against .git` 是最低侵入方案——直接对比当前分支与目标分支的 proto 文件
   - 需要决定检查模式：`WIRE_JSON`（只检查 wire 格式兼容性）还是 `FILE`（检查文件级变化）
   - 建议使用 `WIRE_JSON` 模式，因为它只检测实际会影响通信的变更

4. **配置漂移检测的安全设计**。`GET /debug/config` 端点的暴露面控制：
   - 只在 `127.0.0.1:PORT` 或管理端口监听，不从主服务端口暴露
   - 密钥以不可逆哈希形式返回，但不应该暴露密钥的前缀或部分（防止暴力破解）
   - 配置变更应该通过 audit log 记录

5. **估算调整**：5 天合理，但需要明确"MVP 版"与"增强版"的范围：
   - MVP（3 天）：Schema diff（路径 A）+ Proto compat（`buf breaking`）+ CI 集成
   - 增强版（额外 3 天）：Shadow migration + 配置漂移检测

---

## 3. 接口设计建议

### 3.1 关键模块接口原则

基于本文的五方向，我提出以下接口设计原则：

| 原则 | 说明 | 适用方向 |
|------|------|---------|
| **关注点分离** | 每个接口承担一种职责。如本文的 `EntityExecutor` 应拆分为三个独立接口 | 方向③ |
| **查询可组合** | Repository 层的 LIST 方法应支持可选条件组合，而非硬编码 WHERE 子句。引入 `QueryOption` 模式 | 方向②、方向④ |
| **操作幂等性** | 所有写操作（尤其是跨实体操作）应支持幂等键 | 方向①、方向③ |
| **渐进式暴露** | 新增 API 端点先使用 `POST /v1/experimental/xxx` 或特性标志，稳定后再升格为正式 API | 方向④（集合分享权限模型复杂） |
| **协议对称** | HTTP 和 gRPC 的 API 语义必须完全一致。条件查询操作符（`__gt`、`__exists`）在 gRPC 中应使用枚举而非字符串 | 方向② |

### 3.2 是否需要新的抽象层

**必要的新抽象**：

1. **`QueryOption` / `Criteria` 模式**（所有方向共享）
   - 当前 `repository.ListByOwner(ctx, owner, page)` 签名是紧耦合的
   - 需要引入可扩展的查询条件模式：
   ```go
   type ListOption func(*queryBuilder)
   func WithOwner(uid string) ListOption
   func WithMetaCondition(key string, op MetaOp, val any) ListOption
   func WithCursor(cursor string) ListOption
   func WithLimit(n int) ListOption
   ```
   - 这会让现有 repository 层的变更不影响已存在调用方（通过默认参数行为保持向后兼容）

2. **`internal/lifecycle/` 包**（方向三）
   - 独立的生命周期管理包，包含策略引擎、调度器、executor 注册表
   - 不应该放在 `internal/storage/gc/` 中——生命周期远超 blob 清理

3. **`internal/schemadiff/` 包**（方向五）
   - 需要独立包，不应该耦合到现有的 `internal/pkg/migrate/`

**不建议引入的抽象**：

- **不引入"元数据查询语言"**：方向二的使用 `__gt`/`__contains` 后缀约定已经足够。不要引入 GraphQL 或自定义查询 DSL——增加了学习和维护成本，回报有限。
- **不引入"集合引擎"**：方向四的集合模型保持简单（无嵌套集合），不要引入 DAG 或树结构引擎。

### 3.3 向后兼容性策略

| 变更类型 | 兼容策略 | 示例 |
|---------|---------|------|
| 新增 API 端点 | 无害，新客户端使用新端点，旧客户端不受影响 | `POST /v1/webhooks` |
| 扩展现有请求参数 | 忽略未知参数（HTTP）/ 使用 `optional` 字段（gRPC） | `ListFilesRequest` 增加 `meta_conditions` |
| 修改现有行为 | 使用特性标志 + 旧行为默认 | 方向③的级联删除默认不开启，需显式调用 |
| 废弃旧行为 | 标记为 `Deprecated` + 保留 2 个发布周期 | 旧 Webhook 单 endpoint 在被多订阅者替代后保留 2 个版本 |

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

| 方向 | 建议 | 理由 |
|------|------|------|
| 方向① Webhook 平台化 | **不需要**新技术栈 | 完全可以在现有 Go + PostgreSQL + outbox 模式上扩展。不需要引入消息队列（如 RabbitMQ/Kafka） |
| 方向② 元数据查询 | **不需要**新技术栈 | PostgreSQL JSONB + GIN 索引已经足够。不需要引入 Elasticsearch 或专门的元数据存储 |
| 方向③ 数据生命周期 | **不建议**引入新技术栈 | 但需要考虑：如果级联删除的数据量级很大（单用户 >100 万行），PostgreSQL 的批量 DELETE 可能产生锁竞争。此时可以引入 `pg_partman` 做表分区，但不是必须的 |
| 方向④ 集合模型 | **不需要**新技术栈 | 两张新表 + 标准 CRUD |
| 方向⑤ 变更安全 | **部分需要**新工具 | `buf` 工具链（已有）、`apgdiff`（可选）、自定义 Go 工具 |

**核心判断**：五个方向都不需要引入新的运行时技术栈（消息队列、搜索引擎、数据库等）。这是好事——降低了运维复杂度和学习曲线。

### 4.2 第三方依赖的评估标准

对于方向五中需要引入的新工具依赖，应采用以下评估标准：

| 标准 | 权重 | 说明 |
|------|------|------|
| Go 原生可选 | 高 | 优先选 Go 实现或可通过 `go install` 安装的工具 |
| CI 无需外部服务 | 高 | 工具应能在 CI 容器内离线运行，不依赖 SaaS API |
| 活跃维护 | 中 | 至少在过去 12 个月内有提交 |
| 许可证兼容 | 中 | Apache 2.0 / MIT / BSD 优先，AGPL 需审核 |

**具体推荐**：

| 场景 | 工具 | 评估 |
|------|------|------|
| Proto 兼容性检查 | `buf breaking` | ✅ 已广泛采用，Go 生态标准 | 
| Schema diff | `apgdiff` (Go) | ✅ Go 实现，CI 友好，成熟 |
| Schema diff（备选） | 自定义 `internal/pkg/schemadiff/` | 如果 apgdiff 不够精确，自建成本可控 |
| 配置漂移检查 | 自定义工具 | 不需要外部依赖，读取 config.yaml + 调用 `/debug/config` 即可 |

### 4.3 自建 vs 采购的决策

本文的五个方向均属于**核心业务逻辑**——Webhook 平台、元数据查询、数据生命周期、集合资源、变更安全——这些不是通用平台能力，而是 aero-id 产品的差异化特性。

| 方向 | 自建理由 | 如果考虑采购... |
|------|---------|----------------|
| Webhook 平台 | 需要与现有的 outbox 模型深度集成；数据模型和事件类型是 aero-id 特定的 | 如果外部消费者数量激增（>100 订阅者），可考虑 `Svix` 或 `Knock` 作为托管 webhook 平台，但会引入外部依赖和额外成本 |
| 元数据查询 | PostgreSQL JSONB 操作直接映射，4 天自建成本 | 无合适的托管方案——这是存储层查询能力 |
| 数据生命周期 | 与实体模型和 outbox 机制深度耦合 | 无合适方案——这是业务级治理，不是存储级治理 |
| 集合模型 | 核心产品功能 | 无合适方案 |
| 变更安全 | 需要与 aero-id 的 migration/CI 深度集成 | `buf`（proto compat）是开源工具，其他需要自建 |

**结论**：五个方向都应该自建。引入外部平台的风险（供应商锁定、数据出境、延迟增加）远大于自建成本。

---

## 5. 实施路线图（修订版）

### 5.1 优先级矩阵

基于本文的优先级建议，结合我的架构评审，调整如下：

| 方向 | 业务价值 | 技术风险 | 合规/安全 | 依赖关系 | 建议优先级 |
|------|---------|---------|----------|---------|-----------|
| ③ 数据生命周期治理 | ★★★ | ★★★★★（高）| ★★★★★ | 被依赖：⑤ | **P0** |
| ⑤ 变更安全 | ★★ | ★★★ | ★★★★★ | 依赖：无（可独立实施）| **P0** |
| ① Webhook 平台化 | ★★★★ | ★★★ | ★★★ | 被依赖：⑤（API 兼容性）| **P1** |
| ② 元数据查询 | ★★★★ | ★★ | ★ | 依赖：无 | **P1** |
| ④ 集合模型 | ★★★★★ | ★★★★ | ★ | 依赖：②（查询能力）+ ③（引用治理）| **P2** |

### 5.2 阶段划分

```
Phase 0（2 周）—— 基础安全网 + 合规基线
├── 方向⑤ 变更安全 MVP
│   ├── Schema diff（GORM AutoMigrate 对比路径）
│   ├── Proto compat（buf breaking CI 集成）
│   └── CI 门禁
└── 方向③ 数据生命周期治理 Phase 1
    ├── retention_policies 表 + 迁移
    ├── PurgeExecutor 接口 + notification/message/comment 实现
    ├── 通知表过期清理 sweeper（解决已知的"通知无限增长"问题）
    └── 审计日志过期清理

Phase 1（3 周）—— 平台化核心能力
├── 方向① Webhook 平台化
│   ├── webhook_subscribers 表 + 迁移
│   ├── webhook_delivery_logs 表（解耦 outbox 与 delivery）
│   ├── multi-subscriber relay 改造
│   ├── 密钥轮换端点 + 72h 过渡期
│   └── 递送分析指标
├── 方向② 元数据查询
│   ├── MetaCondition proto 定义 + gRPC 枚举
│   ├── HTTP 查询参数解析器（__gt/__contains/__exists）
│   ├── Repository 层动态 WHERE 条件
│   ├── GIN 索引迁移
│   └── 安全边界（条件数量/长度限制 + 参数化绑定）
└── 方向③ Phase 2
    ├── AnonymizeExecutor 接口 + 全实体实现
    ├── SSO de-provisioning webhook handler
    └── CascadeDeleter 接口 + 全实体实现 + saga 进度追踪

Phase 2（3 周）—— 用户可见功能
├── 方向④ 集合资源模型
│   ├── collections + collection_members 表
│   ├── 集合 CRUD + 成员管理 API
│   ├── 多态引用治理（entity_deleted_at 标记）
│   ├── 集合分享（复用扩展的 share 模型）
│   └── 与方向② 集成的查询（集合内元数据过滤）
├── 方向③ Phase 3
│   ├── GDPR 导出端点 + 异步生成
│   ├── GDPR 删除端点 + 异步级联
│   └── Legal hold 表 + 检查逻辑
└── 方向⑤ 增强版
    ├── Shadow migration（DDL 事务性处理）
    ├── 配置漂移检测
    └── 增强版 schema diff（pg_dump + apgdiff）

Phase 3（2 周）—— 打磨 + 推广
├── 方向④ 补全
│   ├── 集合批操作（批量可见性/导出）
│   └── 搜索集成（搜索结果中标明集合归属）
├── 文档 + SDK 更新
├── 集成测试（跨包场景）
└── 性能测试（大规模集合、元数据查询）
```

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 方向③ 级联删除的数据量级超出预期 | 中 | 高——可能导致长事务、锁表 | 1. 每个 executor 分批处理（每批 100 条）2. 使用 `SELECT ... FOR UPDATE SKIP LOCKED` 避免锁竞争 3. 设置操作超时（30s per batch） |
| 方向② 元数据查询的参数化绑定实现遗漏，导致 SQL 注入 | 低 | 严重 | Code review 强制检查 + SQL 注入检测工具 + 单元测试覆盖"特殊字符"用例 |
| 方向① 多订阅者并行递送导致事件顺序不一致 | 中 | 中——某些消费者依赖事件顺序 | 1. 每个订阅者保证顺序（per-subscriber 串行递送）2. 不同订阅者之间可并行 3. 文档明确"不支持跨订阅者顺序保证" |
| 方向④ 多态引用无外键约束导致数据不一致 | 高 | 中——孤儿数据 | 1. 选项 C（entity_deleted_at 标记）2. 后台 sweeper 清理 3. 与方向③ 联动 |
| 方向⑤ Schema diff 在复杂约束场景误报/漏报 | 中 | 中——开发者信任度下降 | 1. Dual path 策略（GORM + pg_dump）2. 误报白名单机制 3. 人工审核流程覆盖自动检查 |

### 5.4 关键决策点清单

| 决策 | 选项 | 建议 | 决策时间点 |
|------|------|------|-----------|
| Webhook delivery log 是否与 outbox 事件表合并 | A: 合并 / B: 独立 | **B: 独立表** | Phase 1 开始前 |
| 元数据查询操作符传递方式（gRPC） | A: 枚举 + map / B: flat 字段 | **A: 枚举** | Phase 1 开始前 |
| cascading delete 事务模型 | A: 大事务 / B: saga 最终一致 | **B: saga** | Phase 0 方向③ 开始时 |
| 集合多态引用治理 | A: 触发器 / B: 应用层 / C: 标记+清理 | **C: 标记+清理** | Phase 2 方向④ 开始时 |
| Schema diff 工具链 | A: GORM 仅 / B: pg_dump + apgdiff | **双路径** | Phase 0 方向⑤ 开始时 |

---

## 总结

本文是 aero-id 项目迄今看到的最具深度的平台化扩展分析之一。五个方向的选择精准——它们不是随意的功能清单，而是从"缺少哪些基础设施层才能让 aero-id 从功能完备的服务进化成平台"这个视角出发的深度思考。

我的核心修订意见可以归结为三点：

1. **接口分离优先于接口合并**：`EntityExecutor` 拆分为三个接口、outbox 与 webhook delivery log 不合并、`MetaCondition` 使用枚举而非 flat 字段——保持关注点分离，降低每个模块的变化影响面。

2. **实施顺序由依赖关系驱动**：方向⑤（变更安全）和方向③ Phase 1（过期清理）应先行，因为它们为后续方向提供安全网和数据管理基础。方向④（集合模型）因其依赖方向②和方向③ 应排到最后。

3. **估算需要更现实**：方向③ 从 3 周调整为 4-5 周（saga 模式 + 进度追踪是新增成本），方向④ 从 2 周调整为 3 周（权限模型复杂度被低估）。方向①、②、⑤ 的估算基本合理。
