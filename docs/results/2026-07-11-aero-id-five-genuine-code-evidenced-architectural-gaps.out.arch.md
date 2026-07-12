Now I have a thorough understanding of both the document and the current codebase state. Here is my architectural analysis.

---

# 架构分析报告：aero-id 五处体系性架构缺口

## 1. 架构评估

### 当前架构的核心优势

aero-id 采用经典的**分层架构**（handler → service → repository），配合**事务性 Outbox 模式**，在数据一致性和异步处理之间取得了很好的平衡。这是正确的选择——通知、搜索索引、Webhook 等副作用如果与主操作在同一事务中创建，就不会出现"通知丢了"这类数据一致性问题。

从代码中可以看到几个经过深思熟虑的设计决策：

- **Outbox + 指数退避 + 死信队列**：基于数据库的 at-least-once 投递，是分布式系统中最务实的可靠性模式。相比引入 Kafka/RabbitMQ，它在保持运维简单的同事提供了足够强的保证。
- **UUIDv7 作为主键和光标**：时间排序 + 全局唯一，避免了 `OFFSET` 的分页陷阱，是 DDD 聚合根的合理选择。
- **SSO 客户端的断路器 + 多级缓存 + singleflight**：对外部依赖的容错处理是生产级系统的标志。`stale-cache` 策略（故障时使用过期缓存）比硬性熔断更优雅。
- **双传输（HTTP + gRPC）共享 service 层**：这是 AGENTS.md 中严格施行的架构纪律。gRPC 作为内部服务间通信协议、HTTP 作为外部客户端协议的分工合理。

### 架构债务与局限性

尽管整体质量很高，以下五个方面构成了可衡量的架构债务：

**债务 1：分层边界被绕过（已修复）。** 文档指出的 `AdminHandler` 直接注入 `repository.AdminRepository`（绕过 service 层）是违反架构规则的典型例子。当前代码已修复——`AdminHandler` 现在通过 `service.AdminService` 访问，并且 `RequirePermission` 中间件已正确集成。这是文档分析价值的直接证明：识别出的问题在被记录后的短期内就得到了修复。

**债务 2：SSE Hub 是一个无监管的"自由市场"。** 没有连接上限、没有丢事件监控、没有背压机制。这是当前架构中最薄弱的环节。对比 SSO 客户端的断路器 + 多级缓存 + 指标暴露，SSE Hub 作为一个同样重要的实时通道，其健壮性投入严重不足。

**债务 3：通知行是"只进不出"的数据沼泽。** `notifications` 表没有 TTL、没有清理策略、没有批量删除 API。在运行一年后，这将成为性能问题。尤其值得关注的是，碰撞折叠机制每次折叠都留下一行不可见的孤儿行——虽然单行无害，但乘以用户数 × 时间，就是可预测的表膨胀来源。

**债务 4：Webhook 是"二等公民"。** 相比之下，SSO 客户端的断路器、重试、超时有完整的配置项和指标覆盖。Webhook 只有裸 HTTP 调用 + 共用 relay 的重试。这种实现不对称意味着：团队知道如何构建可靠的集成（SSO 就是证据），但 Webhook 没有得到同等的架构关注。

**债务 5：新旧服务层并存带来迁移风险。** `internal/file/service_crud.go`（新架构）和 `internal/service/file.go`（旧服务层）并存，且当前 wired 的是旧层。双实现本身就意味着：
- 维护成本翻倍（两个文件实现相似的逻辑）
- 行为差异（新层的 `Delete` 没有 `StorageRefRepository.Decrement`，存在 TOCTOU 问题）
- 迁移风险：当新层最终被 wired 时，竞态条件会静默导致数据丢失

---

## 2. 扩展方向

### 方向 A（P0）：SSE 实时通道可靠性工程化

**为什么需要：** SSE 是通知系统的"最后一公里"。如果这公里不可靠，前面 Outbox 的 at-least-once 保证就失去了意义。用户的直接体验是"通知不实时"、甚至"通知丢了"。

**核心挑战：**
- Hub 当前的 `pub()` 方法是非阻塞的，没有反压机制告诉上游"我撑不住了"
- Hub 和 Dispatcher 之间没有契约——Dispatcher 不知道事件是否被丢弃
- 没有连接指标，操作员无法诊断"用户为什么收不到实时推送"

**预期的架构变更：**
1. 在 Hub 中引入**容量管控**：`max_subscribers_per_uid`（int）、`channel_buffer`（int）配置化
2. 增加**丢事件计数器**（Prometheus Counter），按 `uid` 或 `event_type` 打标
3. 实现**慢消费者检测与断开**：如果 channel 在 `pub()` 时持续 full，可考虑断开最老的 subscriber 或记录日志
4. 可选的**背压通道**：在 `Pusher.Publish()` 返回一个 `dropped bool`，Dispatcher 据此做降级处理（例如：只写数据库但不推 SSE，标记该用户需要下一次 `replayMissed` 补充）

**对现有系统的影响：** 低。`Hub` 是独立的包，修改不涉及其他层。`Pusher` 接口的返回值变更（void → `(dropped bool)`）需要修改所有实现（当前只有 `Hub` 自己）。

### 方向 B（P0）：通知行生命周期治理

**为什么需要：** 这是一个"现在不修、以后很难修"的问题。通知行是时间序列数据，价值随时间快速衰减。没有 TTL 和清理机制意味着：
- 表行数线性增长（L 级用户 × N 条通知/天 × 365 天 → 亿级行）
- 已读通知占比 >95%，但占用全表扫描和索引空间
- 指向已删除资源的通知成为死链接

**核心挑战：**
- 级联清理需要跨表操作（删除消息时标记相关通知），增加了事务复杂性
- TTL 策略需要权衡：用户可能希望保留历史通知作为记录
- 批量删除需要避免长时间的锁或事务

**预期的架构变更：**
1. **新增 `notifications.expires_at` 列**（可空的 TIMESTAMPTZ），通过后台定时任务设定过期时间
2. **实现 `NotificationGC` 后台扫表器**（复用 `internal/storage/gc/sweeper.go` 模式），定期清理过期已读通知
3. **资源软删除时通知级联**：在 `service/message.Delete` 和 `service/comment.Delete` 中，额外调用 `notificationRepo.MarkSubjectDeleted(subjectType, subjectID)`——设置一个 `subject_deleted` 标志，前端据此显示"该内容已被删除"
4. **新增用户端清理 API**：`DELETE /v1/notifications`（可选参数：`older_than`、`read_only`）

**对现有系统的影响：** 中。需要新增迁移（加列）、后台任务、额外的 service 调用。无侵入性，不影响现有 API。

### 方向 C（P1）：Webhook 断路器与独立可靠性

**为什么需要：** 当前 Webhook 与 NotificationDispatcher 在同一个 outbox Chain 中顺序执行。如果 Webhook 端点挂起（无响应），dispatcher 的通知投递也被延迟。更严重的是——没有断路器意味着一个缓慢的 webhook 端点会影响所有 outbox 事件的投递速度（因为 relay 是串行处理 batch 的）。

**核心挑战：**
- 断路器阈值需要合理配置：网络抖动不应该触发熔断，但持续 5xx 应该
- 可重试错误与不可重试错误的区分：400/401/410 应该立即死信，5xx/429/网络错误应该重试
- 指标驱动：没有监控就无法诊断 webhook 端点的健康状态

**预期的架构变更：**
1. **拆分为独立的 outbox handler 链**：dispatcher 和 webhook 使用不同的 handler 链——即使 webhook 失败，dispatcher 仍然成功。事件的 `published_at` 只由主链决定
2. **在 WebhookHandler 中实现断路器**（复用 SSO 的 `breaker.go` 模式）
3. **区分 HTTP 状态码**：4xx → 立即死信（标记 `dead_letter=true` 并记录原因），5xx/429 → 递增 retry_count
4. **新增 webhook 专属指标**：`webhook_delivery_total`（status_label）、`webhook_latency_seconds`（直方图）、`webhook_errors_total`（error_label）

**对现有系统的影响：** 中高。outbox Chain 的拆分改变了 relay 的处理语义。断路器是一个新增组件，需要充分测试。

### 方向 D（P1）：文件服务的架构迁移治理

**为什么需要：** 新旧两套文件服务层并存是目前最大的技术债。文档识别的 TOCTOU 问题（新层回退到裸 `CountByStorageKey`，没有原子引用计数）是迁移风险的典型例子。这个问题不是特例——双实现之间的任何行为差异都可能成为生产事故。

**核心挑战：**
- 新层设计可能有意简化（也许认为 `StorageRefRepository` 是过设计？），但这个决策需要被显式论证，而不是遗忘
- 迁移需要同时更新 `app.go` 的 wiring 和所有调用者
- 测试覆盖需要验证行为等价

**预期的架构变更：**
1. **在新层中补上 `StorageRefRepository.Decrement`**，或者**显式决策**不在新层中使用 ref-counting（如果是前者，需要文档明确理由）
2. **制定迁移计划**：选择一个 release 窗口，将 wiring 从旧层切换到新层，验证所有测试通过
3. **迁移完成后移除旧层代码**，消除维护双份代码的成本

**对现有系统的影响：** 当前风险有限（旧层是活跃代码），但迁移切换是一次性高风险操作。

### 方向 E（P2）：Admin 操作表面的协议完整性与细化

**为什么需要：** 虽然文档指出的问题（GrantRole/RevokeRole 零调用者）已在当前代码中修复，但 Admin API 仍然值得进一步深化。具体来说，当前的 Admin API 缺少：
- **角色定义管理**：创建/更新/删除角色、修改角色的权限集
- **批量操作**：批量赋予/撤销角色
- **操作审计的查询细化**：按时间段、按操作者、按目标用户筛选

**核心挑战：**
- 角色定义管理强依赖业务需求——哪些角色是可配置的，哪些是系统内置的？
- 批量操作需要事务性保证

**预期的架构变更：**
1. **扩展 `AdminService` 接口**：`DefineRole`、`UpdateRole`、`DeleteRole`
2. **在 gRPC proto 中补充对应 RPC**
3. **前端管理控制台的 API 支撑**

**对现有系统的影响：** 低。当前 Admin 层已有完整的模式和基础设施。

---

## 3. 接口设计建议

### 关键模块的接口设计原则

从当前代码来看，接口设计整体质量很高，但存在几个可优化的原则：

**原则 1：所有"出口"都应该报告结果。** 当前 `Pusher.Publish()` 返回 void，导致调用者无从知道事件是否到达 subscriber。这违反了**信息隐藏为调用者服务**的原则——调用者有权知道操作的结果。

建议改为：
```go
// 当前
Publish(uid, eventType, data string)

// 建议
Publish(uid, eventType, data string) (dropped int)
// 或者更丰富的：
type PublishResult struct {
    Dropped   int
    Delivered int
    SlowSubs  int
}
Publish(uid, eventType, data string) PublishResult
```

**原则 2：后台任务应该统一生命周期管理。** 当前多个系统各自实现自己的 `Start(stop func())` 模式（`outbox.Relay.Start`、`storage/gc.Sweeper.Start`、`idemStore.RunCleaner`、`rateLimit.RunCleaner`），每个都返回一个 `stop` 函数。虽然灵活，但缺乏统一管理。

建议引入一个 `TaskRunner` 抽象：
```go
type Task interface {
    Name() string
    Start(ctx context.Context) error  // blocks until error or ctx cancelled
}

type TaskRunner struct {
    tasks []Task
}

func (r *TaskRunner) Add(t Task)
func (r *TaskRunner) Run(ctx context.Context) error  // runs all, fails first
```

这样 `app.go` 的关闭逻辑可以从手动收集 stop 函数简化为一组 Task。

**原则 3：Outbox Event Handler 应该显式表达语义。** 当前 `Handler.Handle()` 返回 `error` 或 `nil`，语义是"成功 vs 重试"。但有些场景需要更丰富的语义：

- `ErrSkip`：跳过该事件（不重试、不死信）
- `ErrDeadLetter`：立即死信（不可重试的错误）
- `nil`：成功

可以用哨兵错误或错误类型区分。

### 是否需要引入新的抽象层

**需要：一个统一的后台任务生命周期管理器。** 当前 `app.shutdown()` 手动调用 5 个不同的 stop 函数，且必须按特定顺序（先停 Outbox Relay 防止新事件，再停 GC，最后停服务器）。这种编排是隐式的，容易被修改顺序破坏。

**不需要：在 Hub 之上再加抽象层。** Hub 本身就是足够好的抽象——它是 SSE subscriber registry + publisher。问题不在缺少抽象，而在已有的抽象缺少完整的契约。

### 向后兼容性

所有建议的变更都遵循**增量化**原则：
- SSE Hub 的容量配置有合理的默认值（当前的行为）
- 通知 TTL 默认为空（不自动过期），需要运维显式配置才启用
- Webhook 的断路器默认不启用（保持当前行为）
- 旧服务层在迁移完成前继续存在

---

## 4. 技术选型

### 是否需要引入新的技术栈？

**不需要引入消息队列。** 这是一个需要明确说"不"的方向——当看到 Outbox + SSE 的组合时，很容易想到"为什么不直接用 Kafka/NATS/Redis PubSub？"

理由：
1. **运维复杂度**：aero-id 当前零外部基础设施依赖（除 Postgres + S3/MinIO 外）。引入消息队列意味着增加 ZooKeeper 集群或类似的运维负担。
2. **延迟要求**：通知的实时性要求是"秒级"，Postgres 轮询（5s interval）完全满足。如果未来需要毫秒级，可以引入 Redis PubSub 作为补充，但不需要替代 Postgres Outbox。
3. **事务性保证**：Outbox 最核心的优势是和业务操作在同一个事务中。引入消息队列意味着要么放弃事务性写入（先写 DB 再发消息，失败会丢消息），要么实现"发消息前等待 DB 事务确认"的复杂模式。

**考虑引入 Redis / in-process ring buffer：** 作为 SSE Hub 的临时 buffer 层。当 subscriber 离线时，事件不丢弃而是暂存到 Redis List/Stream 或 in-process ring buffer，待 subscriber 重连时回放。但这会引入新的基础设施依赖，需要权衡。

**不需要新的第三方框架。** aero-id 的技术栈（Go 1.26 + Gin + gRPC + GORM + Prometheus + OTel）已经是经过验证的成熟组合。当前缺的不是框架，而是对已有抽象的正确使用。

### 第三方依赖评估标准

从当前代码可以提炼出清晰的依赖引入标准：

| 标准 | 问法 |
|------|------|
| 必要性 | 这个依赖解决的核心问题是什么？能否用标准库+少量代码实现？ |
| 替换成本 | 如果这个依赖不再维护，迁移到替代品的成本有多高？ |
| 抽象泄漏 | 依赖的实现细节是否会漏到我们的业务代码中？ |
| 测试影响 | 这个依赖是否让单元测试更难写？ |
| 版本锁定 | go.mod 是否 pin 了版本？CI 是否验证了兼容性？ |

aero-id 当前对 `protoc-gen-go@v1.34.1` 和 `protoc-gen-go-grpc@v1.5.1` 的 pinning 是好的实践。

### 自建 vs 采购的决策依据

aero-id 是一个自建基础设施服务，当前所有功能都是自建。在这个前提下：

- **SSO 集成**：aero-id 与外部 SSO 通过 gRPC 通信，这是正确的。aero-id 不应该存储用户的身份凭证。
- **文件存储**：支持 local 和 S3/MinIO 两种后端，适配不同部署环境。Local 提供了签名验证的下载，S3 通过 presigned URL 避免流量经过 aero-id。设计合理。
- **实时推送**：自建 SSE 而不是使用第三方推送服务（WebSocket + WebPush / Firebase Cloud Messaging）是合理的——aero-id 需要推送的受众是"当前在线的同一系统的用户"，不是"浏览器中的任意用户"。
- **Webhook**：自建是唯一选择——Webhook 是 aero-id 向外部系统推送事件的出口，没有 SaaS 产品能满足这个场景。

---

## 5. 实施路线图

### 优先级排序

```
P0 ─── SSE 可靠性工程化
   │    通知生命周期治理
   │
P1 ─── Webhook 断路器
   │    文件服务迁移治理
   │
P2 ─── Admin API 细化
```

**P0 的判定依据：**
- **SSE 可靠性**：影响所有用户的实时体验。丢事件无需重启即可恢复，但用户感知是不可逆的。
- **通知生命周期**：今天不修，一年后就必须修。表膨胀是一个可预测的问题，运维成本随着数据量线性增长。越晚修，清理成本越高。

**P1 的判定依据：**
- **Webhook 断路器**：只在配置了 Webhook 的部署中生效。影响面有限，但一旦出问题（挂起端点导致 relay 积压），影响范围会扩展到所有 outbox 事件。
- **文件迁移治理**：当前风险有限（旧层是 active 的）。但迁移窗口的每延迟一天都增加新旧层之间行为差异的风险。

**P2 的判定依据：**
- Admin API 细化：当前 Grant/Revoke/ListRoles 已在最新代码中存在。细化功能（角色定义管理、批量操作）是锦上添花。

### 阶段划分

| 阶段 | 内容 | 预计工作量 | 风险 |
|------|------|-----------|------|
| **Phase 1** | SSE Hub 配置化 + 丢事件指标 + 连接上限 | ~2 周 | 低 — 完全向后兼容 |
| **Phase 2** | 通知 TTL + GC 后台任务 + 前端通知清理 API | ~3 周 | 中 — 需要关注数据一致性 |
| **Phase 3** | Webhook 断路器 + 状态码区分 + 独立 handler 链 | ~3 周 | 中 — handler 链拆分影响 relay 语义 |
| **Phase 4** | 文件服务迁移 + 旧层清理 | ~2 周 | 高 — 生产切换需要灰度 + 回滚方案 |
| **Phase 5** | Admin API 细化 + 角色管理 | ~1 周 | 低 |

### 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| SSE 配置变化导致现有连接中断 | 低 | 高 | 默认值与当前行为完全一致；通过配置开关逐步启用 |
| 通知 GC 误删用户正在查看的通知 | 中 | 中 | 只清理超过 TTL 且已读的通知；GC 默认 TTL 设为 90 天，足够保守 |
| Webhook 断路器熔断后不知如何恢复 | 中 | 中 | 断路器暴露 admin API (POST /admin/webhook/reset)；输出清晰日志指明熔断原因 |
| 文件服务迁移导致 blob 被误删 | 低 | 极高 | 迁移前补充新层的 TOCTOU 修复；迁移后先在 staging 运行一周；保留旧层代码直到下一 release |
| 多个方向并行开发导致合并冲突 | 中 | 低 | 每个方向独立在 feature branch 开发；文件改动不重叠（不同包） |

### 一个关键的技术决策

在 Phase 1（SSE 可靠性）中，有一个决策需要仔细权衡：

**选项 A：为 Hub 增加背压机制**（`Publish()` 返回丢事件计数，Dispatcher 据此降级）

- 优点：Dispatcher 可以获得实时反馈，做智能决策（例如：只写 DB 不推 SSE）
- 缺点：`Pusher` 接口签名变更，影响所有调用者；Dispatcher 需要处理非关键的背压信号
- 建议：Phase 1 不做背压，只做可观测性（丢事件计数）。如果生产数据显示丢事件严重，再在 Phase 1.5 增加背压。

**选项 B：使用更保守的默认值**（channel buffer 从 16 扩展到 64，per-uid 连接上限设为 10）

- 这是最简单的优化——不改变任何逻辑，只调整常数。可以且应该在 Phase 1 的最初几天就完成。
- 结合实际生产数据，可以进一步精细化调整。

决策建议：**先做 Option B 的可配置化，再做 Option A 的背压**。可观测性是优化的前提——没有丢事件计数器就没法知道 buffer 是否够用。
