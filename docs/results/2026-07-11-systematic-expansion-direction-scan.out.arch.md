# 架构分析报告：aero-id 基础设施能力扩展

## 1. 架构评估

### 当前架构的优势

aero-id 的当前架构有几个值得肯定的设计决策：

**严格单向依赖（handler → service → repository | storage | sso）**。这是项目中最具价值的结构性约束。它保证了业务逻辑集中在 `service` 层，不会泄漏到传输层或持久化层。这种分层在微服务生态中经过验证——当需要新增一个 GraphQL 入口时，只需写一个新的 handler，重用同一套 service 逻辑。

**Outbox 模式的选择**。在需要保证"数据库写入 → 后续副作用（通知/webhook）"原子性的场景中，Outbox 是比双写（写入 DB + 发送消息）更稳健的方案。当前项目选择了这一模式，说明设计者对数据一致性有正确的优先级排序。

**软删除 + UUIDv7 标识体系**。UUIDv7 天然具备时间排序能力，配合 `WHERE id < cursor ORDER BY id DESC LIMIT n+1` 的分页方案，避免了 OFFSET 分页的常见陷阱（插入导致的数据偏移、全表扫描）。软删除则给未来的数据恢复、审计追溯留了余地。

### 关键设计决策的合理性验证

| 决策 | 合理性 | 风险 |
|------|--------|------|
| SSO 外部化，aero-id 无 accounts 表 | ✅ 边界清晰，规避了账户安全的核心复杂性 | SSO 的可用性成为 aero-id 的前置依赖——需要熔断和降级策略 |
| GORM 作为 ORM | ⚠️ 中等合理 | GORM 的复杂查询能力较弱，scope 过滤（方向二）可能需要纯 SQL escape hatch |
| 配置全量 startup 加载 | ❌ 如反馈中所指，缺少运行时热重载 | 变更限速阈值、日志级别等操作性参数需要重启，有业务中断成本 |
| 双传输（HTTP + gRPC）共享同构 service | ✅ 架构正确，但缺少契约约束工具 | 如反馈中指出的 P1 风险——HTTP 和 gRPC 的接口语义可能在迭代中漂移 |

### 架构债务识别

**1. 异步链路缺少可观测性**

这是当前最严重的架构债务。Outbox 流程中，事件生产者（service）通过事务写入 `events` 表，但随后 relay 的消费行为是在 `context.Background()` 中执行的。这意味着：

```
HTTP请求进入 → service写入DB(含outbox event) → 响应200给客户端
                                                    ↓
                                            outbox relay在后台轮询
                                            (context.Background())
                                                    ↓
                                            发送通知/webhook
                                            (trace链断裂点)
```

当某一个通知发送失败时，运维人员无法通过 trace 从原始请求跳转到失败的通知发送——孤立的 traces 需要靠日志时间戳手动关联。这在生产排障中是一个显著的效率损耗。

**2. 配置系统不支持运行时变更**

当前配置被设计为"启动时加载，全生命周期不变"。这在静态基础设施中没问题，但在以下场景中构成运维负担：

- SSO 超时阈值需要动态调优
- 限速/限流参数需要自适应
- 日志级别需要在不停机的情况下切换

**3. 缺少 API 契约规范**

HTTP 和 gRPC 双入口共享同一个 service，但没有任何形式化的契约文件（OpenAPI、protobuf 作为唯一真实来源）。当 gRPC 接口和 HTTP 接口的语义不同步时，唯一能依赖的是人的审查——而人工审查在高迭代速度下不可靠。

---

## 2. 扩展方向

### 方向一：延迟任务框架（P1）

**为什么需要**。当前的 Outbox 模式处理的是"事务后立即执行"的场景。但存在一类需求无法被 Outbox 覆盖：

- 内容上传后 24 小时未审核 → 自动撤回
- 用户删除账号后 30 天缓冲期 → 彻底清理
- 定时数据汇总/报表生成

这些场景需要"在未来的某个时间点执行一个任务"，而非"事务提交后尽快执行"。

**核心挑战**：

- **与 Outbox 的职责边界**（如反馈文档的表格所示）。如果不明确边界，团队会倾向于用 Outbox 做延迟任务（比如给 `events` 表加一个 `scheduled_at` 字段），或者在 `scheduled_jobs` 中滥用事务一致性语义。需要在设计文档中给出**决策树**。

- **调度精度 vs 资源消耗的平衡**。如果要求秒级精度（如：撤销操作在指定时间精确执行），需要类似 `pg_timetable` 或 `pg_cron` 的数据库级调度器，或者每秒钟扫描 `scheduled_jobs` 表。如果分钟级精度可接受（大多数业务场景），可以每 10-60 秒扫描一次。建议架构提供配置项，允许不同任务类型声明各自的精度需求。

- **任务幂等性**。延迟任务可能在执行前多次被调度（leader 切换、relay 重启）。需要每个任务有唯一的 `idempotency_key`。

**预期架构变更**：

```
┌─────────────────────────────────────────────────┐
│                  service 层                      │
│  ┌─────────────┐    ┌───────────────────┐       │
│  │ 业务逻辑     │───→│ Outbox (事件)     │       │
│  │             │    └───────────────────┘       │
│  │ 业务逻辑     │───→│ ScheduledJob (任务) │       │
│  └─────────────┘    └───────────────────┘       │
└─────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────┐
│                repository 层                     │
│  ┌──────────────────┐  ┌──────────────────────┐ │
│  │ events 表        │  │ scheduled_jobs 表     │ │
│  │ (立即/近实时消费)  │  │ (按 scheduled_at 消费) │ │
│  └──────────────────┘  └──────────────────────┘ │
└─────────────────────────────────────────────────┘
         │                        │
         ▼                        ▼
┌──────────────────┐  ┌──────────────────────────┐
│ Event Relayer    │  │ ScheduledJob Runner      │
│ (轮询 events)     │  │ (轮询 scheduled_jobs)    │
└──────────────────┘  └──────────────────────────┘
```

推荐的实现策略：复用 Outbox 的 Dispatcher 模式和 Retry 机制。核心差异：

- `scheduled_jobs` 的 relay 按 `scheduled_at ≤ NOW()` 轮询，而非 `created_at > last_seen_id`
- 新增一个 `CronJob` 类型，通过 cron 表达式周期性地创建 `ScheduledJob`

**对现有系统的影响**：

- 低影响。`scheduled_jobs` 作为新的独立表和独立的 relay runner 存在，不修改 Outbox 现有逻辑。
- 需要注意：新增的 runner 需要被 health check 覆盖，避免"false health"。

---

### 方向二：作用域搜索（P1）

**为什么需要**。当前 aero-id 的资源（files、messages、comments）是按 owner 隔离的。但在以下场景中需要跨用户/跨资源类型的搜索：

- "查找我收到的所有关于项目 X 的评论"
- "搜索所有被标记为违规的附件"
- "找出用户 A 和用户 B 之间的所有消息"

当前的 `WHERE owner_uid = ?` 模式无法满足这些需求。

**核心挑战**：

- **scope 的组合爆炸**。搜索条件可能是 `(owner_uid = ? AND resource_type = 'file') OR (to_uid = ? AND resource_type = 'comment')`。SQL 层的复合索引规划必须提前对最常见的 scope 组合做覆盖分析。如果索引设计不当，全表扫描的风险很高。

- **分层决策：SQL 层 vs 搜索服务层**。这是一个关键架构决策：

  | 方案 | 优势 | 劣势 |
  |------|------|------|
  | SQL 层 AND 拼接 | 性能好，无需中间层 | 索引提前规划，scope 组合有限 |
  | 搜索服务层后过滤 | 灵活，可支持复杂组合 | 内存/CPU 开销大，不适用于海量数据 |

  建议采用**混合策略**：对高频 scope 组合（<=5 种）预先创建覆盖索引，SQL 层直接过滤；对低频/临时组合使用搜索服务层过滤，并在监控中记录慢查询，逐步优化为 SQL 层。

- **分页的一致性**。当前的 UUIDv7 光标分页假设单 owner 的内部时间序。跨 owner/跨资源类型后，cursor 的语义需要重新定义——可能需要引入"搜索快照"或"滚动窗口"。

**对现有系统的影响**：

- 中到高影响。搜索会触及 repository 层的最底层 SQL 逻辑。
- 需要引入一个 `Scope` 类型来表达过滤条件组合。
- 索引变更需要在 migration 中回滚（`.down.sql`）。

---

### 方向三：API 契约规范（P1，从 P2 升级）

**为什么需要**。双传输同构是 aero-id 的架构核心承诺——但没有任何工具确保 HTTP 和 gRPC 的接口语义保持同构。对于一个即将引入外部消费者的服务，这是一个**运营风险**而非技术债。

典型的事故链：
```
1. 团队在 gRPC handler 中新增了一个字段 `priority`
2. 忘记同步更新 HTTP handler
3. HTTP 客户端永远收不到 `priority`
4. 问题在集成测试中被发现（如果有）或生产环境（如果没有）
```

**核心挑战**：

- **Step 1 的选型**。OpenAPI 规范应该从哪个方向生成？
  - 选项 A：从 protobuf 生成 OpenAPI（推荐）。protobuf 是 gRPC 的 IDL，通过 `grpc-gateway` 或 `protoc-gen-openapiv2` 可以自动生成 RESTful 映射。这保证 HTTP 和 gRPC 共享同一个源。
  - 选项 B：手写 OpenAPI，从 OpenAPI 生成 handler 骨架。适合 OpenAPI-first 团队，但需要额外的 codegen 步骤。
  - 选项 C：从 Go 代码注解生成。侵入性强，不适合已有项目。

  对于 aero-id，推荐选项 A，因为它与现有的 protobuf 定义自然对齐。

- **向后兼容性**。一旦 OpenAPI 规范发布，每个字段的变更都需要考虑客户端兼容性——不能随意 rename 或 remove field。

**对现有系统的影响**：

- Step 1（OpenAPI 规范生成）：低影响。工具链配置，不修改代码。
- Step 2（契约测试）：中影响。引入新的测试套件，CI 中增加 `make check-contract`。
- Step 3（消费方驱动的契约测试）：高影响。需要外部消费者提供测试用例。

---

### 方向四：合成事务探针（P1）

**为什么需要**。当前的 health check 仅验证服务是否在监听端口（L4 检查），无法验证核心读写路径是否正常（L7 检查）。典型的"false health"场景：

- 数据库连接池耗尽，连接失败但端口仍然开放
- MinIO 服务正常但存储桶权限变更导致上传失败
- SSO gRPC 连接正常但返回持续的错误

**核心挑战**：

- **探针的侵入性设计**。合成探针必须在"足够模拟真实用户"和"不能产生业务数据残留"之间取得平衡。推荐策略：
  - 使用 `monitor_` 前缀 uid 写入一条记录
  - 读回验证
  - 使用同一次事务删除
  - 如果删除失败，由独立 sweeper 清理（sweeper 也失败则告警）

- **探针本身的故障隔离**。探针应该有独立的超时和断路器。如果探针组件本身出现问题（如 `monitor_` 前缀的 uid 被误删除），不应该导致误报。建议：探针的告警需要连续 N 次失败才能触发，避免单次瞬断引发告警风暴。

- **共享 vs 隔离的存储资源**。如果探针使用生产数据库的同一实例，那么一个真实的数据库故障会同时导致探针失败（真阳性）和探针超时（也可能真阳性）。但如果希望检测的故障只影响部分用户（如某 shard 不可用），那么探针需要部署在**所有 shard 上**。轻量方案：探针只检查主 shard；重量方案：每个 shard 部署探针。

**对现有系统的影响**：

- 中影响。新增一个 `internal/probe` 包，或者集成到现有的 `internal/repository` 中。
- 探针不应该对业务性能产生可感知的影响——建议每个探针周期只做一次完整读写，间隔 30-60 秒。
- 探针的结果应该暴露为 Prometheus metrics，而非日志字符串。

---

### 方向五：Async Context Propagation（优先级：Outbox P1.5，其他 P2）

**为什么需要**。当前 Outbox relay 使用 `context.Background()` 导致 trace 链断裂。在以下场景中这是一个问题：

- 某 webhook 发送失败，需要查明是哪一个原始请求触发的
- Outbox relay 批量处理事件，某个事件处理时间异常长，影响了同批次的其他事件
- 需要按请求维度分析 Outbox 消费延迟

**核心挑战**：

- **context 如何从事务产生者传递到异步消费者**。HTTP 请求的 context（包含 trace id、span id）在写入 outbox event 时是活跃的，但 event 被持久化到数据库后，context 丢失了。需要找到一种方式将 trace context 随 event 存储——通常实现在 event payload 中嵌入一个 `trace_id` 和 `span_id` 字段。

- **OpenTelemetry 的嵌入策略**。如果 event payload 中携带 `traceparent`（W3C Trace Context 格式），relay 在消费时可以从 event 中提取 trace context 并创建子 span。这样 trace 链就变成了：

  ```
  HTTP请求 → service(创建span A) → 写入event(携带span A的traceparent)
                                                                  ↓
                                  outbox relay(创建span B, parent=span A的traceparent)
                                  → 发送通知(span C, parent=span B)
  ```

  这样完整的链路可以追溯到原始请求。

- **批量处理的上下文管理**。如果 relay 一次拉取 N 个 events，每个 event 应该有独立的 trace context。relay 的 span 作为 batch 的父 span，每个 event 的消费作为子 span。

**对现有系统的影响**：

- Outbox 方向：中影响。需要在 event 的数据库模型中增加 `trace_context` 字段（JSON）。
- sweeper/cleaner 方向：低影响。这些异步任务可以延后改造。
- 注意：不要在冷路径（只读请求）中嵌入 trace context 存储，避免无谓的写放大。

---

## 3. 接口设计建议

### 关键模块接口原则

**1. Service 层保持纯同步语义**

无论异步链路如何扩展，service 对外暴露的接口应该是纯同步的。延迟任务在 service 层只是一个 `Schedule(ctx, job)` 调用（内部写入 `scheduled_jobs` 表），不暴露任何 worker 细节。这样可以保证：

- Handler 不感知异步的存在
- 测试时可以用 mock 替换 `Schedule` 调用
- 未来从 DB 迁移到消息队列时，service 接口不变

**2. Outbox 和 ScheduledJob 共享抽象**

```
type MessageProducer interface {
    Publish(ctx context.Context, topic string, payload []byte) error
    Schedule(ctx context.Context, topic string, payload []byte, at time.Time) error
}
```

`Schedule` 对应延迟任务，`Publish` 对应即时事件。底层实现可以不同（一张表 vs 两张表），但使用者接口一致。

**3. 探针的可插拔性**

```
type Probe interface {
    Name() string
    Check(ctx context.Context) (ProbeResult, error)
}
```

不同的探针（database、storage、sso）实现各自的 `Check` 方法，聚合到 health endpoint。

### 是否需要新的抽象层

**需要引入的抽象**：

- **Scope 过滤表达式**（方向二）。一个类型系统来表示过滤条件组合，避免 SQL 字符串拼接：

  ```go
  type Scope interface {
      Apply(query *gorm.DB) *gorm.DB
  }
  ```

  每种 scope 类型实现自己的 Apply 方法，支持 AND/OR 组合。

- **Trace context 编码/解码**（方向五）。标准的 W3C Trace Context 序列化/反序列化，嵌入 event payload。

**不需要引入的抽象**：

- 不需要新的消息队列基础设施（如 RabbitMQ、Kafka）——DB 表的语义已经满足要求。
- 不需要新的配置管理系统——热重载可以在现有 config 包上增量实现。

### 向后兼容性策略

1. **所有新功能必须是可选的/逐步上线的**：
   - 延迟任务：新增 `scheduled_jobs` 表，现有的 `events` 表不动。
   - 作用域搜索：新增服务方法 `ListScoped`，保留旧的 `ListByOwner`。
   - 合成探针：health endpoint 新增 `?probe=full` 参数，`/healthz` 保持原有检查。

2. **字段/属性的新增不能破坏现有消费者**：
   - Event payload 中新增 `trace_context` 字段（可选），不修改现有字段。
   - API response 中新增字段时，确保 old clients 忽略新字段。

---

## 4. 技术选型

### 是否需要引入新的技术栈

| 方向 | 新依赖 | 推荐 | 原因 |
|------|--------|------|------|
| 延迟任务 | 消息队列 | ❌ 不引入 | DB 表轮询的延迟（秒级到分钟级）已满足业务需求 |
| 作用域搜索 | 全文搜索引擎（Meilisearch/Sonic） | ❌ 暂时不引入 | 当前需求是过滤搜索而非全文搜索，SQL 复合索引满足 |
| API 契约 | protoc-gen-openapiv2 + OpenAPI validator | ✅ 工具链扩展 | 零运行时依赖，仅 CI 工具 |
| 合成探针 | 不引入新依赖 | ✅ 现有组件 | 用现有的 repository/storage/sso 实现 |
| Async Trace | OpenTelemetry | ✅ 已引入？检查现有依赖 | 如果未引入，建议引入；如果已引入，仅需扩展 event 模型 |
| 配置热重载 | fsnotify | ✅ 轻量依赖 | 监听文件变更 → 触发 config 重新加载 |

### 第三方依赖评估标准

按以下优先级评估：

1. **许可证兼容性**（必须 Apache 2.0 或 MIT）
2. **Go 模块版本稳定性**（> v1.0）
3. **维护活跃度**（最近 6 个月有发布）
4. **版本升级的破坏性**（major version 升级是否需要重写大量代码）

对于 aero-id 这种基础设施服务，应**尽可能减少第三方依赖**。每个依赖都是潜在的 supply-chain 攻击面。

### 自建 vs 采购的决策依据

当前这五个方向都适合自建：

- 延迟任务：业务逻辑简单（向表插入一行 + 轮询），不值得引入外部消息队列带来的运维复杂度
- 作用域搜索：多资源类型的联合过滤是业务特定的，通用搜索产品（Elasticsearch）反而需要额外的索引定义和同步逻辑
- API 契约：工具链层面的配置（protoc plugin），不是运行时依赖
- 合成探针：高度定制化的健康检查，通用监控产品（如 Datadog Synthetic Monitoring）可以替代，但成本高且定制度不够
- Async Trace：OpenTelemetry SDK 是行业标准，符合"用规范不用框架"的原则

唯一的"采购"可能性是配置热重载——但这只是一个文件变更通知库（`fsnotify`），简单到不值得采购。

---

## 5. 实施路线图

### 优先级排序（最终版）

| 优先级 | 方向 | 理由 |
|--------|------|------|
| **P1** | 方向一：延迟任务框架 | 核心缺失，Outbox 无法覆盖的关键业务路径 |
| **P1** | 方向二：作用域搜索 | 多个已规划功能的前置依赖 |
| **P1** | 方向三 API 契约（Step 1） | 引入外部消费者前必须解决的运营风险 |
| **P1.5** | 方向五：Async Trace（Outbox 部分） | 核心可靠性的可观测性 gap，but not blocking |
| **P1** | 方向四：合成事务探针 | false health 是生产安全风险 |
| **P2** | 方向五：Async Trace（sweeper/cleaner 部分） | 次要异步路径，影响有限 |
| **P3** | 配置热重载 | 非阻塞，运维便利性 |

### 阶段划分

**Phase 1（迭代 1-2）**

实施 P1 方向中改动最小、价值最高的第一步：

1. 合成探针（方向四）——新增 `internal/probe` 包，集成到 health endpoint
2. API 契约 Step 1（方向三）——配置 `protoc-gen-openapiv2`，在 CI 中生成并验证 OpenAPI 规范

目标：在一个迭代内交付两个可独立验证的能力。

**Phase 2（迭代 3-4）**

1. 延迟任务框架（方向一）——新增 `scheduled_jobs` 表、`ScheduledJobRunner`、共享 Dispatcher 模式
2. Async Trace — Outbox 优先（方向五）——为 event 模型增加 `trace_context`

目标：建立异步链路的完整基础设施。

**Phase 3（迭代 5-6）**

1. 作用域搜索（方向二）——设计 scope 类型系统，规划复合索引，新增 `ListScoped` 方法
2. 配置热重载（P3）— 作为 Phase 3 的辅助能力

目标：完成搜索能力，完善基建。

### 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Outbox 与 ScheduledJob 的职责混淆导致设计反复 | 中 | 高 | 在 Phase 2 设计阶段创建决策树文档，明确边界 |
| Scope 搜索的索引优化不当导致性能退化 | 中 | 高 | 提前创建性能基准测试，在 staging 环境模拟 10M 行数据压力测试 |
| API 契约 Step 1 引入的工具链变更导致 protobuf 生成失败 | 低 | 中 | 先在分支上验证工具链；CI 中添加 `make check-contract` 但不 gate 整个 pipeline |
| 合成探针产生数据残留 | 低 | 低 | 探针使用独立事务+uid 前缀；sweeper 作为 safety net |
| Async Trace 增加 event 负载大小导致数据库膨胀 | 低 | 中 | trace_context 字段可压缩（JSON + 短 trace id）或单独存储 |

---

## 最终建议

这五个方向的选择是准确的，它们填补的是基础设施层的**能力空洞**而非功能增量。优先级调整建议：

1. **API 契约升级到 P1（整体，而非仅 Step 1）**。理由："双传输同构"是面向消费者的承诺而非内部实现细节——没有契约约束的能力无法形成承诺。

2. **将方向一和方向五的 Outbox 部分拉到同一个 Phase 执行**。理由是：延迟任务的 dispatcher 模式和 async trace 的 trace 嵌入可以共用一次 event 模型的扩展。如果分两期做，需要两次修改 event 表结构。

3. **在 ROADMAP 中明确"不做"的决策**。例如：不使用外部消息队列、不引入全文搜索引擎。这可以防止未来在评审中反复讨论同一个问题。
