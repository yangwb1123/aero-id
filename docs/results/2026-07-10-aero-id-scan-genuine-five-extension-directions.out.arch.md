以下是从架构和技术设计角度对验证文档所揭示的五个方向的深入分析。

---

# aero-id 架构分析报告

## 1. 架构评估

### 1.1 现有架构的优势

**正确的模块边界**。`handler → service → repository/storage/sso` 的单向依赖是这套系统最坚固的设计决策。两个传输层（HTTP + gRPC）共享同一套 service，意味着：

- 业务逻辑的验证、授权、错误转换只写一次
- 新增传输协议（例如未来的 WebSocket RPC）只需新增 handler 适配
- `AppError` 同时携带 HTTP 状态码和 gRPC code，双协议的错误语义天然对齐

**接口隔离的务实程度**。`FileStorage`, `AuditLogger`, `BlockChecker`, `UserDirectory` 等接口粒度合理——每个接口只暴露调用方真正需要的方法，没有大而全的"上帝接口"。尤其值得肯定的是：

- `FileStorage` 接口涵盖了 `Put/Get/Delete/PresignGet/Stat`，恰好覆盖文件生命周期的所有操作，不多不少
- `AuditLogger` 在 service 层是可选的（nil 安全），这是正确的设计——审计应该是横切关注点，不应该硬耦合到业务路径
- `storage/gc/sweeper.go` 进一步提取了 `FileRepo` 和 `RefCounter` 子集接口，使 GC 可以独立单元测试

**领域事件的 outbox 模式**。关键变更（消息发送、评论创建、标记已读）通过 `outbox.Event` 与数据行在同一事务中持久化，然后由 relay 异步派发。这是正确的做法——它保证了"至少一次交付"语义，避免了业务操作成功而通知失败的数据不一致。

### 1.2 架构债务与技术债

验证文档准确识别了五个问题，但我想从**架构层面**补充它们的根因分类：

**类别 A：半成品功能（架构债务）**

| # | 问题 | 根因 |
|---|------|------|
| 1 | 用户封禁 | 常量+种子数据已完成，但 enforcement point 和管理面缺失。这是**典型的分阶段开发遗留**——权限基础架好了，但业务逻辑没有跟上 |
| 5 | Ref-count 原子性 | `Increment` 位于事务之外。**调用顺序的时序耦合**——Create→Increment 的顺序在单线程下正确，但并发环境下脆弱 |

**类别 B：设计层面的竞态条件（设计债务）**

| # | 问题 | 根因 |
|---|------|------|
| 2 | 内容去重竞态 | `FindByChecksum → Create` 是经典的 **TOCTOU (Time-of-check to time-of-use)** 模式。这个模式在单线程下安全，在并发上传同一文件时产生重复 |
| 5 | Ref-count 竞态 | `Create` 事务提交后、`Increment` 执行前，另一个并发的 `Delete` 会看到 `ref_count=0` 并删除底层 blob |

**类别 C：基础设施缺陷（架构脆弱性）**

| # | 问题 | 根因 |
|---|------|------|
| 3 | Webhook 级联失败 | `NewChain(dispatcher, webhookHandler)` 是**串行责任链**，且 relay 是单线程 `drain`。一个慢 webhook 阻塞所有 outbox 事件的交付，包括本地通知写入和 SSE 推送 |

**类别 D：产品能力缺口（非技术债务，但影响信任模型）**

| # | 问题 | 根因 |
|---|------|------|
| 4 | 消息撤回 | 从产品层面没有撤回能力。发送者一旦发出消息，即使在对方阅读前也无法撤回——这是一个**对称性缺失**：有"删除"（对自己隐藏）但没有"撤回"（对双方隐去） |

### 1.3 关键设计决策评估

| 决策 | 评价 | 建议 |
|------|------|------|
| GORM 作为 ORM | ✅ 对大部分 CRUD 是合理选择；但对 `FOR UPDATE` 锁定、`ON CONFLICT DO UPDATE` 等场景，`gorm.Expr` 的使用暴露了 ORM 在复杂 SQL 下的局限性 | 考虑在 repository 层引入 **raw SQL + sqlx 风格**的辅助，关键路径（ref-count 增减、去重查询）使用显式 SQL 而非 ORM 链式调用 |
| 串行 Chain Handler | ⚠️ 当前的设计假设 handler 是"无副作用的过滤器链"，但 `webhookHandler` 有外部网络 I/O，将其串在 `dispatcher` 后面改变了性质的假设 | **Chain 应该支持并发+超时模式**，或者将 webhook 拆到独立 relay |
| 单线程 Relay | ✅ 简化了实现，对于 MVP 阶段合理；但对于生产环境，`drain` 中的慢事件会阻塞 batch 中其他所有事件的交付 | 引入**每事件隔离**——一个事件的 handler 失败不应阻止同一 batch 中其他事件的标记发布 |
| 可选的接口（nil 安全） | ✅ 这对开发本地运行、测试配置非常友好。但**检查的时机是构造时**，不是运行时，所以配置错误可以早期发现 | 建议在 `app.go` 中对关键接口（`refs`, `shares`, `audit`）添加 `warn-on-nil` 日志，帮助运维发现未预期的配置遗漏 |
| `storage/gc` 的 OPT-IN 设计 | ✅ 正确的保守策略 | 无 |

---

## 2. 扩展方向

### 方向 A：事务内原子去重与引用计数

**为什么需要**。当前 `FindByChecksum → Put → Create → Increment` 的四步序列存在两个竞态窗口。随着用户增长和上传量增加，这两个窗口从"极小概率"变成"必然发生"。

**核心挑战**。

1. **去重与写入的顺序反转**：理想的顺序是 "先占坑，再上传"——在存储 blob 之前先在 database 中创建行，但这样无法知道 checksum
2. **Increment 的事务归属**：`Increment` 需要在 `Create` 的同一事务中执行，但当前的 `refs.Increment` 是独立的 repository 调用，无法自然地加入 GORM 事务
3. **回滚协调**：如果 `Increment` 成功了但后续操作失败（理论上这个场景不会发生，因为它是最后一步），ref-count 会出现正向泄漏

**预期的架构变更**。

```
当前：
  Put(stream) → FindByChecksum → [可能 Delete(blob)] → Create(tx) → Increment(外部队列)

建议：
  Put(stream) → Create(tx包含Increment) → [可能 Delete(blob) + Decrement]
         ↓
  在 transaction 中执行：
    - 行插入
    - ref-count Increment (INSERT ... ON CONFLICT DO UPDATE)
    - outbox 事件插入
  作为一个原子提交
```

- `StorageRefRepository.Increment` 需要接受一个 `*gorm.DB` 参数（或整个 repository 层提供 `WithTx` 方法将事务句柄传递下去）
- 或者：将 ref-count 合并到 `FileRepository.Create` 的内部实现中，上层 service 不感知

**对现有系统的影响**。

- 接口变化：`FileRepository` 需要支持事务回调或接受 `TxProvider`
- 对 service 的影响：`fileService.Create` 的后半部分需要重构
- 测试影响：mock 需要更新

### 方向 B：Outbox 并行扇出与故障隔离

**为什么需要**。当前单线程串行 Chain 是重大的生产稳定性风险。`drain` 中任何一个事件卡住（慢 webhook、网络分区、webhook endpoint 挂起连接不关闭）会阻塞：

1. 同一 batch 中**所有其他事件的交付**（包括本地通知写入）
2. 之后的 drain 轮次（因为 relay 单线程）
3. SSE 推送（因为 dispatcher 在 webhook 之前执行，但 chain 串行意味着 dispatcher 完成通知写入后要等 webhook 返回才能继续到下一个事件）

**核心挑战**。

1. **保持至少一次语义**：并发交付需要在 `MarkPublished` 之前确认所有并发分支完成；部分失败需要正确的错误处理和重试语义
2. **事件顺序**：某些业务场景可能依赖事件顺序。需要区分"必须有序"的事件类型和"可乱序"的
3. **背压与限流**：并发度不能无限制，需要可配置的 worker pool

**预期的架构变更**。

```
当前：
  relay.drain → batch → for each event: dispatcher → [webhook] → MarkPublished

方案一：每事件独立 goroutine（轻量）
  relay.drain → batch → for each event: 
    go handle(event): dispatcher → [webhook]  // 超时控制
    wait group / errgroup 聚合结果
  → MarkPublished(successful events)

方案二：独立 webhook relay（重量，更隔离）
  outbox.NewRelay(outboxRepo, dispatcher, ...)   // 负责本地通知
  outbox.NewRelay(outboxRepo, webhookHandler, ...)  // 独立 relay，不同 poll interval
  两个 relay 各有一个 drain loop，互不阻塞

方案三：Chain 内部并发 + 独立超时
  Chain 改为 fan-out：同时调用 dispatcher 和 webhookHandler
  每个 handler 有自己的 context.WithTimeout
  任一失败则返回错误（或根据配置容忍 webhook 失败）
```

**推荐**：方案一最轻量、改动最小。方案二适合未来 webhook 负载高、需要独立监控的场景。建议先上方案一，预留方案二的配置开关。

**对现有系统的影响**。

- `outbox.Relay.drain` 需要重构为并发处理
- `Chain` 可能退化为特定链的显式代码（不再通用）
- 配置层需要增加并发度/超时参数
- 已有的 webhook 超时是 handler 级别的（`http.Client.Timeout`），事件级别的超时需要 relay 层支持

### 方向 C：用户封禁的完整生命周期

**为什么需要**。这是一个**安全缺口**——常量 `PermUserBan` 和种子数据已经就绪，但没有任何 enforce。这意味着：

- 被 ban 的用户可以继续上传文件、发送消息、发表评论
- 没有任何管理员可以 ban/unban 用户
- 没有自动解封机制（临时封禁无过期）

**核心挑战**。

1. **封禁的存储模型**：需要 `banned_users` 表或扩展 `user_roles`（标记 `banned` 角色？）。但 `user_ban` 是一个权限，不是角色——持有 `user.ban` 的管理员可以 ban 别人
2. **封禁的 enforcement 点**：需要在所有"写入"操作前检查——文件上传、消息发送、评论创建、评论编辑、文件共享等。这与 `BlockChecker` 类似但语义不同（ban 是平台级禁令，block 是用户级黑名单）
3. **临时封禁**：需要支持 `banned_until` 时间戳，可选的。relay 或定时任务需要定期扫描到期解封

**预期的架构变更**。

```
新增：
  - migrations/000024_user_bans.up.sql
    CREATE TABLE user_bans (
      uid          TEXT PRIMARY KEY,
      reason       TEXT NOT NULL DEFAULT '',
      banned_by    TEXT NOT NULL,           -- admin uid
      banned_until TIMESTAMPTZ,            -- NULL = 永久
      created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
      updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
    );
  
  - internal/domain/ban.go （新的 domain 实体）
  - internal/repository/ban.go
  - internal/service/ban_service.go（或扩展 adminService）
  - internal/handler/http/admin.go + grpc: BanUser/UnbanUser 端点

修改变更：
  - adminService 新增 Ban(ctx, uid, reason, duration) / Unban(ctx, uid) / IsBanned(ctx, uid)
  - 在 fileService.Create, messageService.Send, commentService.Create 等入口处
    添加 ban check（通过 BanChecker 接口，nil-safe）
  - middleware 或 service 层统一注入 BanChecker
  - 需要添加用户角色管理端点以配合：GrantRole/RevokeRole 已有但未暴露在 HTTP 路由中
    （当前 `POST /roles/grant` 和 `/roles/revoke` 已注册但需要 `system.config` 权限）
```

**对现有系统的影响**。

- 新增文件：`domain/banned_user.go`, `repository/ban.go`, 可能 `service/ban_checker.go`
- 修改 `service/file.go`, `service/message.go`, `service/comment.go` 添加 ban 检查
- `AdminService` 接口扩展
- `admin.go` handler 新增路由
- 需要设计 `BanChecker` 接口（类似 `BlockChecker`）

### 方向 D：消息撤回协议

**为什么需要**。当前只有 sender-only 的软删除。这意味着：

- 发送者无法撤回已发送的消息（即使对方未读）
- 这是一个信任和隐私问题：发送者犯错（发错内容、发错人）没有补救手段
- 对于合规场景（错误发送敏感信息），撤回是刚需

**核心挑战**。

1. **撤回的时间窗口**：需要决定"多久内的消息可以撤回"。常见做法是 `time_window` 配置项（例如消息发送后 5 分钟内可撤回），超过则拒绝
2. **撤回的副作用**：撤回后，对方侧应该看到什么？常见选项：
   - 消息消失（删除行——但会破坏对话连续性）
   - 消息被替换为占位符（"[消息已被撤回]"）
   - 替换内容 + 留有撤回记录
3. **撤回通知**：如果撤回已经通过 outbox 通知了对方，对方的客户端需要收到撤回指令。这需要 outbox 支持 `message.recalled` 事件类型
4. **撤回系统通知**：系统通知是否可以撤回？理论上不应该

**预期的架构变更**。

- `MessageService` 新增 `Recall(ctx, id)` 方法
- 消息表可能需要新增 `recalled_at` 或 `recalled` 列
- outbox 新增 `message.recalled` 事件类型
- 撤回的权限校验：仅原发送者、未超出时间窗口、非系统消息
- 撤回后，对方端需要合理的 UI 提示

**对现有系统的影响**。

- 新增迁移 `000024_message_recall.up.sql`
- 修改 `repository/message.go`（新增 `Recall` 或 `MarkRecalled` 方法）
- 修改 `service/message.go`（新增 `Recall` 方法）
- 修改 handler 层新增 `POST /v1/messages/:id/recall` 端点
- 新增 `message.recalled` outbox 事件

### 方向 E：可观测性基础设施

**为什么需要**。设计文档明确指出 "metrics / tracing 不内置（YAGNI）"，但系统已经演进到包含：

- outbox relay（有 `MetricsSink` 接口，但实现是空的）
- SSO 客户端（有 `Metrics` 参数、`Breaker`）
- 边缘 rate limiter
- 存储 GC

这些组件已经在产生有价值的可观测信号，只是当前没有被采集。当 outbox 延迟成为问题（方向 B）、rate limiter 被触发但看不出谁在被限，或者 GC 运行但不清楚效果时，盲操作是危险的。

**核心挑战**。

1. **避免过度工程**：不需要分布式 tracing 框架 Prometheus + OpenTelemetry。**结构化日志 + 可导出的 metrics 端点** 就够了
2. **MetricsSink 的实现**：已经有了接口，只需要一个 Prometheus 或 expvar 风格的实现
3. **RED 指标**：全链路（请求速率、错误率、延迟）对每个 service 方法

**预期的架构变更**。

- 实现 `MetricsSink` 接口（Prometheus 计数器/直方图）
- 为 HTTP 和 gRPC handler 添加 RED 指标
- 添加 `GET /metrics` 端点（Gin handler 或单独的 expvar mux）
- outbox relay 的 `MetricsSink` 实际工作

**对现有系统的影响**。

- 新增 `internal/pkg/metrics/prometheus.go`（或使用现有 `metrics.Registry` 加 Prometheus 导出）
- 依赖新增 `prometheus/client_golang`
- 非破坏性变更——所有已有的 sink 接口保持不变

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

**原则 1：事务边界应该由 repository 层显式暴露**

当前的架构中，service 层无法将多个 repository 调用包装在同一数据库事务中。这导致了方向 #5 的 ref-count 原子性问题。建议引入：

```go
// 选项 A：事务上下文
type TxProvider interface {
    Tx(ctx context.Context, fn func(txCtx context.Context) error) error
}

// 选项 B：事务性 Repository 方法
type FileRepository interface {
    CreateWithRef(ctx context.Context, f *domain.File, refKey string) error
    // 内部原子地执行：INSERT file + UPSERT storage_ref
}
```

**推荐选项 B**，因为它在 repository 层封装了数据库细节，不向上泄露事务 API。

**原则 2：责任链应该支持"任意失败不阻塞其他"模式**

当前 `Chain` 的串行语义是"所有 handler 必须成功才等于成功"。建议引入 `FanOut` 变体，其语义是"所有 handler 独立执行，聚合失败":

```go
type FanOut struct {
    handlers []Handler
    timeout  time.Duration  // 每个 handler 独立超时
}

func (f *FanOut) Handle(ctx context.Context, e *Event) error {
    // 并发执行所有 handler，用 errgroup 收集
    // 每个 handler 使用派生 timeout context
    // 规则：任意 handler 失败 → 整体失败（重试整个事件）
    // 或者（可配置）：仅 webhook 失败 → 记日志不阻塞
}
```

**原则 3：可选接口应该同时提供运行时验证**

当前 nil-safe 的可选接口在配置遗漏时悄无声息。建议在 `app.go` 中添加启动时告警：

```go
if s.refs == nil {
    log.Warn("storage ref-counting is DISABLED — content dedup is vulnerable to concurrent deletion races")
}
```

### 3.2 是否需要新的抽象层

**不需要新的分层**，但需要改善现有层的边界清晰度：

- **不引入**"Application Service"层——当前的 service 已经就是这个角色
- **不引入**"Event Bus"抽象——outbox + relay 已经够用，现在需要的是并发模式改进，而非新基础设施
- **可以考虑**：`internal/service/ban.go` 作为独立文件，但不需要新的包。`BanChecker` 接口可以放入 `service/service.go` 与 `BlockChecker` 并列

### 3.3 向后兼容性策略

| 变更 | 兼容性 |
|------|--------|
| 方向 A：Increment 移入事务 | service 接口不变，行为隐性变化（从 best-effort 变成可靠），测试需要更新 |
| 方向 B：并发 fan-out | 行为变化——现在所有事件保证顺序交付，并发后不保证。这需要显式配置开关 |
| 方向 C：Ban 端点 | 新增端点，完全向后兼容 |
| 方向 D：Recall 端点 | 新增端点+新增字段，向后兼容 |
| 方向 E：Metrics | 新增端点+新增依赖，向后兼容 |

**关键决策**：方向 B 的并发 fan-out 应该**默认关闭**（现存行为不变），通过配置项 `outbox.fan_out = true` 启用。这样可以安全部署、逐步验证。

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

| 方向 | 技术需求 | 建议 | 理由 |
|------|---------|------|------|
| A | 事务内原子操作 | 不需要新库 | 现有 GORM 的 `Transaction` + 显式 SQL (`ON CONFLICT DO UPDATE`) 已足够 |
| B | 并发控制 | 不需要新库 | Go 标准库 `sync.WaitGroup` / `errgroup` 足够。不需要 goroutine 池库 |
| C | 封禁存储 | 不需要新库 | 现有的 GORM + PostgreSQL 表即可 |
| D | 撤回通知 | 不需要新库 | 通过已有的 outbox 事件机制实现 `message.recalled` 事件 |
| E | Metrics 导出 | **需要新增依赖** | `prometheus/client_golang` 是最成熟的选择 |

**关键判断**：五个方向中**只有一个**需要新的第三方依赖（Prometheus client）。其余的都可以用现有的技术栈解决。

### 4.2 自建 vs 采购的决策

这个问题在这里不相关——aero-id 是一个自建服务，不是第三方采购系统。

但有个相关的内部决策：**是否应该把 outbox 替换成外部消息队列（NATS / RabbitMQ / Redis Streams）？**

| 选项 | 优点 | 缺点 |
|------|------|------|
| 继续使用现有 outbox（数据库轮询） | 无新基础设施、事务一致性好、部署简单 | 数据库压力（但频率5s、batch 100 对 PG 几乎无影响）、缺少高级消费语义（重试分组、DLQ 管理） |
| 引入 NATS / RabbitMQ | 成熟的消息语义、fan-out 原生支持、管理界面 | 新基础设施、部署复杂度、运维负担、失去了"事务内写入"的原子性（需要二阶段提交或最终一致） |
| Redis Streams | 高性能、轻量 | 弱事务保证、数据丢失风险、增加运维复杂度 |

**建议**：对 MVP 阶段继续使用现有 outbox。只有当 webhook 事件量 > 1000/s 或需要精确的消费语义时才考虑外部队列。方向 B 的并发改进可以解决当前 90% 的问题，不需要引入新队列。

### 4.3 第三方依赖评估标准

对于方向 E 的 Prometheus 引入，评估标准：

- **成熟度**：`prometheus/client_golang` v1.x，广泛使用，社区成熟
- **API 稳定性**：Prometheus exposition format 已经是实际标准，Go client 有 Go 1 兼容承诺
- **对现有架构的影响**：metrics 采集是**纯附加**的，不改变任何业务路径。`metrics.Registry` 已存在接口
- **运维成本**：添加一个 `/metrics` 端点，Prometheus server 拉取；没有额外的存储或守护进程

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0 — 安全缺口。不修复则系统有明确的安全漏洞
  ├── 方向 C（用户封禁）：常量已定义，enforce 缺失 = 安全控制未闭合
  └── 方向 A（ref-count 原子性）：可能导致用户数据永久丢失（blob 被误删）

P1 — 数据完整性 + 生产稳定性
  ├── 方向 B（outbox 并发）：生产故障场景（webhook 慢导致全站通知延迟），
  │   影响用户体验但不会丢数据
  └── 方向 A（去重竞态）：虽然也是数据完整性，但两个副本的代价低于 blob 被删

P2 — 产品能力
  └── 方向 D（消息撤回）：信任模型改进，但不是安全漏洞

P3 — 可观测性
  └── 方向 E：运维质量改进，时间灵活
```

### 5.2 阶段划分

**阶段 1（P0 修复）——预计 2-3 天**

```
里程碑：安全缺口闭合，数据完整性加固

Week 1:
  - 方向 C：User Ban
    - migrations/000024_user_bans 表
    - domain.BannedUser 实体
    - repository.BanRepository（CRUD + IsBanned 查询）
    - BanChecker 接口（放入 service/service.go）
    - service 层 ban 检查：file.Create, message.Send, comment.Create
    - AdminService v2: BanUser/UnbanUser 方法
      - 注意：现有的 admin.go 只有 HasPermission/ListAudit 两类操作
      - 新增方式：扩展 adminService 结构体，添加 Ban/Unban/IsBanned
    - handler 层新增端点：
      - POST /v1/admin/users/:uid/ban {reason, duration}
      - POST /v1/admin/users/:uid/unban
      - GET /v1/admin/users/:uid/ban-status（当前封禁信息）
    - gRPC: 同步新增 AdminService 的 RPC（需要 proto 变更 + make proto）
    - 后台自动解封：在 relay 模式或单独的 goroutine 扫描到期封禁
      - 最简单的实现：在 BanRepository 中添加 CleanExpired 方法
      - 与 GC 类似的 Start/stop 模式
  
  - 方向 A（ref-count 原子性）：
    - 将 Increment 移入 FileRepository.Create 的事务内部
    - 或者：在新的 TxFileRepository 接口中暴露事务
    - 移除 "Best-effort" 注释——在这之后是可靠的了
    - 注意：需要确保 GC 的 RefCounter 与事务中的 Increment 协调
```

**阶段 2（P1 修复）——预计 3-4 天**

```
里程碑：生产级 outbox 稳定性

  - 方向 B（outbox 并发 fan-out）：
    - outbox.FanOut 类型实现（或改进 Chain）
    - 配置项 outbox.fan_out.enabled（默认 false）
    - 超时控制：每个事件 fan-out 总超时
    - 故障隔离：webhook handler 失败不阻止 dispatcher 成功事件被 MarkPublished
    - 更细粒度的 metrics 暴露
    
  - 方向 A（去重竞态）：
    - 评估：是否真的需要修复去重？"多一个副本"的损害低于"blob 被删除"
    - 如果修复：在 file_service.Create 中使用 INSERT ... ON CONFLICT DO NOTHING
      可能更简单——让数据库检查 checksum + owner_uid 的唯一性
    - 需要迁移添加唯一索引：CREATE UNIQUE INDEX idx_files_owner_checksum 
      ON files(owner_uid, checksum) WHERE deleted_at IS NULL;
    - 然后 service 层可以在捕获唯一冲突时复用已存在的行
```

**阶段 3（P2 + P3）——预计 2-3 天**

```
里程碑：产品完备 + 运维可见

  - 方向 D（消息撤回）：
    - 规格决策：撤回窗口？撤回后的展示方式？
    - migrations/000025_message_recall：添加 recalled_at 列
    - repository.MessageRepository 新增 MarkRecalled 方法
    - service.MessageService 新增 Recall 方法
    - handler 新增端点
    - outbox 新增 message.recalled 事件类型（前端需要订阅）
  
  - 方向 E（可观测性）：
    - 实现 metrics.Registry 的 Prometheus 导出
    - HTTP handler 注册 /metrics 路由
    - 为 outbox relay 实现 MetricsSink（目前有接口但实际 sink 为空）
    - 为 SSO 客户端的 circuit breaker 添加 metrics 指标
    - RED 指标：Go 默认的 HTTP 指标（请求数、延迟、inflight）
```

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **方向 A（ref-count）**：Increment 移入事务后，如果事务回滚，ref-count 会回滚。但当前 Increment 是 UPSERT，回滚可能导致其他文件引用的 ref-count 被回退 | 中 | 高——数据不一致 | 在构造时预检查：不存在其他文件引用同一 storage_key，或者使用 SAVEPOINT 嵌套事务。最简单的方案：只确保 Increment 在 Create 事务提交**之前**执行，但不一定在同一事务中。使用**同一数据库连接但独立事务**也可以消除时序窗口 |
| **方向 B（并发 fan-out）**：事件顺序不再保证，某些消费者可能依赖顺序 | 低 | 中 | 初始实现默认关闭 fan-out；开启时在文档中声明"尽力顺序"；未来可以根据 event_type 分片保证同类型顺序 |
| **方向 C（封禁）**：误封的问题——admin 误操作需要审计追踪 | 中 | 高 | 所有封禁/解封操作写入 AuditLog，与 `PermUserBan` 权限绑定，不允许自封 |
| **方向 D（撤回）**：撤回后的消息在对方端需要客户端配合显示占位符 | 低 | 中 | 这是 API 变更，需要前端团队配合。后端 API 先提供能力，前端渐进接入 |
| **方向 E（metrics）**：`/metrics` 端点可能泄露运行信息 | 低 | 低 | `/metrics` 放在 HTTP 的独立端口或使用 Basic Auth；或者使用 `/admin/metrics` 路径（受鉴权保护） |

### 5.4 关键里程碑建议

```
M0（Day 0） : 确认修复顺序，分配 task
M1（Day 2-3）: 方向 C + 方向 A（ref-count）完成 → make test && make lint green
M2（Day 6-7）: 方向 B（outbox fan-out）完成 → staging 环境下模拟慢 webhook 验证
M3（Day 9-10）: 方向 D（recall）+ 方向 E（metrics）完成 → 全量回归测试
M4（Day 11）  : 部署计划 + 回滚方案 + 文档更新
```

---

## 总结

五个方向中，**#1（用户封禁）和 #5（ref-count 原子性）是 P0**——一个是安全缺口，一个是数据完整性风险。两者牵涉的代码改动量都较小（新增 1-2 个文件，修改 3-4 个现有文件），但它们关闭了重要的负向场景。

**#3（outbox 级联失败）是 P1**——修复它不需要引入新基础设施，只需要重构 `outbox.Chain` 的调用模式。并行 fan-out + 独立超时可以在现有架构内干净实现。

**#4（消息撤回）是 P2**——产品能力缺口，但它与现有的 outbox 事件机制天然契合，实现成本可控。**#2（去重竞态）** 可以合并到方向 A 中一起修复，或者接受"极小概率多一个副本"的风险而推迟。

从架构韧性角度看，最大的单一风险点是 **serial Chain + single-threaded drain**。它把三个不同的故障域（DB 通知写入、SSE 推送、外部 webhook HTTP 调用）串在一起，任何一个的不稳定性都会影响其他。方向 B 的修复应该获得最高的技术优先级，即使它的业务优先级是 P1 而不是 P0。
