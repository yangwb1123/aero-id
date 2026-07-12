现在我对当前代码状态有了清晰的了解。以下是作为资深架构师的分析回应。

---

# aero-id 架构深度分析与决策指南

## 0. 当前代码状态验证

在响应之前，我快速验证了分析文档发布 2 天后的代码库状态（`HEAD: 37e177c`）：

| 发现 | 状态 | 说明 |
|------|------|------|
| 双栈未解决 | ✅ 确认 | `internal/app/app.go` **零引用**新栈包（`internal/file`, `internal/comment`, `internal/message`, `internal/notification`, `internal/block`, `internal/search`） |
| 新栈代码存在 | ✅ 确认 | 六个包各有 `handler_http.go` + `handler_grpc.go` + `service.go` + `repository.go` |
| 新栈 handler 可插拔 | ✅ 确认 | 均实现 `RegisterRoutes(r Routes)` 接口，与旧路由器兼容 |
| 旧栈仍在演进 | ✅ 确认 | `internal/service/` ~5,419 行, `internal/repository/` ~3,892 行 |
| 附录 D-1 (actor 重复) | ⚠️ 未验证 | 需进一步 grep 确认 |

**结论：分析文档中的所有核心发现仍然准确有效。**

---

## 1. 架构评估

### 1.1 当前架构优势

- **清晰的单向依赖**：`handler → service → repository → Postgres` —— 这是正确的分层，不应被破坏。
- **组合根集中化**：`internal/app/app.go` 是所有依赖注入的唯一入口，没有 `init()` 或全局状态服务定位器模式。
- **传输层对称**：HTTP 和 gRPC 共享同一 `service` 层，业务逻辑无重复，符合已确立的设计契约。
- **Outbox 模式成熟**：事务性 outbox → relay → dispatcher → SSE/webhook 链已在生产路径上运行，弥补了缺失的异步处理基础设施。
- **接口驱动而非具体类型驱动**：路由器的 `RouteRegistrar` 接口和存储层的 `FileStorage` 接口为变更提供了良好的自由度。

### 1.2 关键架构债务

| # | 债务 | 严重性 | 本质原因 |
|---|------|--------|---------|
| **Dual-Stack** | 两套平行 handler→service→repository 栈。一套在用 (~15.5k 行)，一套离线 (~11.8k 行)。 | **严重** | 领域驱动重构中途搁置。新栈的 `internal/{domain}/service.go` 与旧栈 `internal/service/{domain}.go` 之间存在服务接口重复。迁移到新栈需要重写 `app.go` 的依赖注入部分，但业务逻辑重写已于前期完成。 |
| **Actor 上下文重复** | `internal/service/service.go` 和 `internal/domain/actor.go` 各有一套 `WithActor`/`ActorFrom`，使用不同的 `ctxKey` 类型。 | **中** | 重构过程中的残留物。在一个 context 上混用两者会导致静默读取失败。目前之所以未触发，是因为两套栈不在同一请求路径上同时使用。 |
| **权限模型过程化** | 每个 service 方法手写权限检查。`SearchFiles` 遗漏 `file_shares` 权限（已于文档中发现），且未来每新增一个端点都需要手动确保权限检查正确。 | **中** | 缺乏统一的授权抽象层。 |
| **跨域关注点硬编码** | 审计日志、阻塞检查、限流、通知在 service 层以不同方式调用。新增跨域关注点（如操作频率异常检测）需要修改所有 service 方法。 | **中** | 缺少类似装饰器或中间件的织入点。 |
| **事件类型非契约化** | 事件类型字符串散布在多处，payload schema 由隐式的 `json.Unmarshal` 决定。 | **中低** | 事件驱动架构尚未完工——有 outbox 基础设施，但无事件类型治理。 |

### 1.3 关键设计决策评估

| 原始决策 | 评价 | 是否应重新审视 |
|---------|------|-------------|
| 领域驱动重构（将 handler/service/repository 放入 `internal/{domain}/`） | **方向正确**，但执行到一半中断 | 是——需要完成或明确关闭 |
| SSO 外部化（无 accounts 表，无 token 签发） | **正确**。与身份解耦是核心架构优势 | 否，应当保留 |
| GORM 作为 ORM | **合理选择**。Go 生态中成熟的 ORM，与 Postgres 配合良好 | 可保留，但应限制使用（复杂查询用原生 SQL） |
| UUIDv7 主键 + 游标分页 | **正确**。无需全表扫描即可高效分页，UUIDv7 天然按时间排序 | 否，应当保留 |
| 软删除（gorm.DeletedAt） | **合理**。需要审计追踪的系统软删除是标准做法 | 否，但需要明确的 GC 策略 |
| 两套 handler→service→repository 栈并行存在 | **不可接受的临时状态**。已持续超过开发周期，代码漂移风险实际存在 | 是——**必须在任何新功能开发前解决** |

---

## 2. 扩展方向

基于现有债务和代码库结构，我识别出以下高价值方向：

### 方向 1：双栈归一化（暂称 Phase 0）

**为什么需要**：11,798 行无主代码存在误导性，且代码漂移风险随每个提交递增。旧栈仍在增长（5,419 行 service + 3,892 行 repository），新旧栈之间越来越难以同步。

**核心挑战**：新栈的 `service.go` 接口定义与旧栈不同。新栈将 `Service` 接口定义在每个包内（如 `internal/file/service.go` 中的 `type Service interface`），而旧栈将具体实现集中在 `internal/service/{file,message,...}.go`。因此，切换不是简单的 import 替换，需要：

1. 决定**哪个 Service 接口定义**作为权威——新栈的细粒度接口，还是旧栈的集中式实现
2. 确保 `app.go` 的依赖注入匹配目标栈的构造函数签名
3. 验证两个栈的 repository 使用相同的表名映射（GORM 表名约定）

**架构变更**：
- **选项 A（迁移到新栈）**：将 `app.go` 中的注入点从 `internal/handler/http`/`internal/service` 逐个域切换到 `internal/{domain}`。每个域切换后验证。完成后删除旧栈代码。
- **选项 B（废弃新栈）**：在新栈的每个包根目录加 `// Deprecated` 注释。更新 AGENTS.md 记录决策。**不删除代码**（保留作为参考，但标记为不可修改）。
- **选项 C（混合）**：选择 1-2 个低风险域（如 block、preference）先迁移到新栈作为验证，其余废弃。

**对现有系统的影响**：
- 选项 A：对生产服务有短期风险（每次切换都需要集成测试验证），但长期消除所有双栈问题。
- 选项 B：零风险，但失去新栈的整洁结构。
- 选项 C：折中方案，验证迁移可行性后再决策。

### 方向 2：事件契约注册表 + 消费者隔离

**为什么需要**：当前有 9 种事件类型，paylods 通过 `json.RawMessage` 隐式定义。新增事件类型时，开发者需要 grep 所有消费者确认兼容性。且消费者链（`outbox.Chain`）一个阻塞会拖慢所有消费者。

**核心挑战**：
- 需要一个类型化的 `EventRegistry`，将事件类型字符串映射到 Go 结构体类型
- 消费者需要从顺序执行改为独立的 goroutine + channel + 超时
- 现有消费者（dispatcher、webhook）需要符合新接口

**架构变更**：
- 新建 `internal/events` 包，包含 `EventRegistry` 和 payload 类型定义
- 将 `outbox.Chain` 改造为 fan-out 模式（每个消费者独立 goroutine）
- 事件版本化机制（向后兼容的 payload 演进）

**对现有系统的影响**：低。outbox relay 的内部实现变更，但对外接口（`outbox.Handler`）不变。现有消费者只需注册到 registry。

### 方向 3：声明式权限模型

**为什么需要**：当前权限检查分散在约 20 个 service 方法中。`SearchFiles` 遗漏 `file_shares` 的权限检查是一个具体 bug，但背后是系统性漏洞——没有统一的地方可以审计"用户 X 对资源 Y 有什么权限"。

**核心挑战**：
- 设计一个足够表达现有权限规则的 DSL（或 Go 代码规则表）
- 所有资源类型（file、message、comment、block）的访问规则需要统一描述
- 性能：每个请求都需要评估权限，规则链不能太慢

**架构变更**：
- 新建 `internal/auth` 包，包含 `Policy`（规则表）和 `Check(ctx, actor, resource, action) error`
- 在 service 层添加统一的 `AuthGuard` 调用点（推荐作为跨域能力链的组成部分）
- 为每个资源类型定义 `Resource` 结构体（`{Type, ID, Owner, Meta}`）

**对现有系统的影响**：中。需要逐个 service 方法将内联的权限检查迁移到统一调用。迁移期间可以双轨运行（新老检查同时执行，日志记录差异但不禁用请求）。

### 方向 4：最终一致性治理与可观测 SLO

**为什么需要**：当前系统存在多个最终一致性灰色地带（搜索不检查分享、block 不追溯已有消息、消息编辑不触发通知、文件 GC 默认禁用）。团队对这些行为没有统一的文档化契约。

**核心挑战**：
- 为每个业务操作建立"即时/最终/尽力/不保证"清单
- 为每个最终一致保证定义 `MaxLag` SLO 并监控达标率
- 修复遗留的不一致问题（搜索 + 分享、block + 追溯、edit + 通知）

**架构变更**：
- 新建 `internal/consistency` 包，包含 `Checklist` 定义
- 在现有 outbox 监控的基础上，增加 `outbox_lag_seconds` 的告警规则
- 逐步填补一致性缺口（每个缺口对应一个独立的修复 PR）

**对现有系统的影响**：低。不影响现有请求路径。清单只是文档+监控，修复缺口是增量改进。

### 方向 5：全链路可观测性——跨 outbox 边界追踪

**为什么需要**：当前 OTel 追踪止步于 HTTP/gRPC handler 边界。当用户投诉"通知没收到"时，运维人员无法追踪到 outbox 事件是否发布、relay 是否消费、dispatcher 是否成功创建通知行。

**核心挑战**：
- 在 outbox relay 中创建 child span，继承自写入 outbox 事件的 trace
- 为 repository 层添加通用 tracing wrapper（~7 个 repo，每个 5-15 个方法）
- 确保 span 上下文在 outbox 的 goroutine 之间传递

**架构变更**：
- 在 `internal/pkg/tracing` 中提供 `TracedRepository` 包装器（使用 Prometheus 直方图）
- 修改 `internal/outbox/relay.go` 在 `handleWithTrace` 中创建 child span
- 新增结构化业务日志输出（audit 写入 `zap.Logger`）

**对现有系统的影响**：低。纯增量添加，不影响业务逻辑。

---

## 3. 接口设计建议

### 3.1 双栈归一化后的目标接口契约

无论选择迁移还是废弃，都需要建立一个清晰的接口治理规则：

```
  handler/http/*.go          handler/grpc/*.go
       │                           │
       ├── 依赖 service.{X}Service 接口 ──┤
       │                           │
       ▼                           ▼
       ┌───────────────────────────────┐
       │         service 层              │  ← 唯一业务逻辑层
       │   (internal/service/*.go)      │
       └───────────────┬───────────────┘
                       │ 依赖 repository.{X}Repository 接口
                       ▼
       ┌───────────────────────────────┐
       │       repository 层             │  ← 唯一持久化层
       │   (internal/repository/*.go)   │
       └───────────────────────────────┘
```

**建议**：以当前 `internal/service` 包中的 Go 接口（`FileService`, `MessageService`, etc.）作为权威接口定义。新栈的 `internal/{domain}/service.go` 中的接口如果与旧接口等效，应统一到同一位置。避免两处各有一个 `Service` 接口。

### 3.2 跨域能力层接口

建议引入一个轻量的跨域能力链，但不是作为中间件（会破坏 service 层的纯业务逻辑），而是作为 service 的一个可选组合：

```go
// internal/capability/capability.go
package capability

type Context struct {
    Actor    string
    Resource Resource
    Action   string
}

type Checker interface {
    Check(ctx context.Context, capCtx *Context) error
}

// 在 service 构造函数中注入
type Option struct {
    Checkers []Checker
}
```

**设计原则**：
- **自底向上织入**：Checker 在 service 构造函数中注入，而非在 handler 中间件中注入。
- **可组合而非可继承**：每个 Checker 只关心一个关注点（限流、权限、阻断、审计）。
- **fail-open vs fail-closed**：每个 Checker 应明确标注其失败模式。例如，阻断检查 fail-closed（检查失败 = 拒绝），审计日志 fail-open（记录失败不影响请求）。

### 3.3 事件契约接口

```go
// internal/events/registry.go
type EventType struct {
    Name        string
    Schema      reflect.Type      // 用于契约测试
    Consumers   []string          // 用于文档和监控
    Description string
    Sensitive   []string          // 字段名列表，用于脱敏
}

type Registry struct {
    mu     sync.RWMutex
    events map[string]EventType
}

func (r *Registry) Register(et EventType) { ... }
func (r *Registry) Lookup(name string) (EventType, bool) { ... }
```

### 3.4 向后兼容性策略

- **双栈归一化期间**：新旧栈 handler 不应同时注册到同一个 gin 引擎。切换是逐域替换，而非并行运行。
- **事件契约注册表引入后**：旧的事件类型字符串继续被识别（向后兼容）。新增事件类型必须注册。
- **权限模型迁移期间**：旧权限检查和新 `AuthGuard` 同时运行，结果差异日志记录，但请求以旧检查结果为准。过渡期后切换。

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

| 技术 | 推荐 | 理由 |
|------|------|------|
| OPA/Rego (策略引擎) | **不推荐** | 对于当前规模（~20 条规则），Go 代码规则表足够。OPA 增加了运行时依赖和调试复杂度。 |
| CNCF Notifications (事件注册表) | **不推荐** | aero-id 的事件系统是内部事件总线，不需要兼容 CNCF 的 CloudEvents 规范。 |
| Prometheus 直方图 (repository 追踪) | **推荐引入** | 已有 `metrics.Registry`，扩展为直方图只需现有基础设施增补。 |
| pg_stat_statements (慢查询) | **推荐启用** | 零代码变更，纯配置。Postgres 内置扩展，只需 `CREATE EXTENSION` + 配置参数。 |
| OpenTelemetry (跨 outbox 追踪) | **已有，扩展使用** | OTel 已在 tracing 初始化中使用。只需在 outbox relay 中创建 child span。 |

### 4.2 第三方依赖评估标准

对于任何新的第三方依赖，建议按以下标准评估：

1. **许可证兼容性**：必须与现有 Go 模块许可证（MIT/Apache-2.0）兼容
2. **依赖深度**：优先选择零传递依赖或浅依赖的库
3. **更新频率**：过去 12 个月有活跃提交
4. **Go 1.26 兼容性**：模块声明必须支持当前 Go 版本
5. **替代方案**：Go 标准库或现有依赖能否解决问题

### 4.3 自建 vs 采购

所有当前缺口建议自建。理由：
- 权限模型是高度业务特定的（file_shares、block、参与方访问等混合规则）
- 事件注册表是轻量结构和测试框架，不需要第三方事件治理平台
- 最终一致性清单本质上是文档 + 监控配置，不需要采购

---

## 5. 实施路线图

### Phase 0：双栈决策（1 周）

**这是所有后续工作的前置依赖。** 在决策明确之前，任何其他方向的实施都会受到双栈不一致的影响。

| 任务 | 工作量 | 产出 |
|------|--------|------|
| D-0.1：团队讨论确认新栈意图 | 1 天 | 决策记录 |
| D-0.2：执行决策（迁移 或 废弃） | 3-5 天 | 干净的单一架构 |
| D-0.3：更新 AGENTS.md 和架构文档 | 0.5 天 | 文档更新 |
| D-0.4：修复附录 D-1~D-5 的技术债 | 1 天 | 消除重复和冲突 |

**我的建议：选项 C（混合验证）**——先选择一个低风险域（如 `block`，`preference` 或 `notification`）迁移到新栈作为验证。这个域：
- 业务逻辑相对独立
- 依赖关系简单
- 影响面小

如果验证通过，继续迁移其他域。如果发现问题，回退到废弃策略。这种方式在 1 周内获得实际经验数据，而不是纯粹的理论决策。

### Phase 1：基础设施加固（2-3 周，P1）

| 方向 | 任务 | 工作量 | 前置依赖 |
|------|------|--------|---------|
| 事件契约注册表 | 创建 `internal/events` + 注册表 + 契约测试框架 | 1 周 | Phase 0 |
| 事件契约注册表 | 改造 outbox relay 为独立 goroutine fan-out | 1 周 | 注册表 |
| 最终一致性治理 | 创建 `internal/consistency` 清单（前 10 个操作） | 0.5 周 | 无 |
| 最终一致性治理 | 修复搜索 + 分享权限遗漏 | 0.5 周 | 无 |
| 全链路可观测性 | Repository 层 tracing wrapper + outbox trace 延续 | 1 周 | 无 |

**里程碑**：Phase 1 完成后，事件系统可观测、可测试、消费者隔离运行。

### Phase 2：架构中台化（3-4 周，P2）

| 方向 | 任务 | 工作量 | 前置依赖 |
|------|------|--------|---------|
| 跨域能力层 | 定义 `capability.Context` + `Checker` 接口 | 0.5 周 | Phase 0 |
| 跨域能力层 | 将现有关注点（限流、权限、阻断、审计）提取为 Checker | 1.5 周 | Phase 0 |
| 声明式权限模型 | 设计 `internal/auth` 规则表 | 1 周 | 跨域能力层 |
| 声明式权限模型 | 迁移 7 个 service 的内联检查 | 1.5 周 | 规则表 |
| 最终一致性治理 | 修复 block 追溯、消息编辑通知缺口 | 1 周 | Phase 1 清单 |

**里程碑**：Phase 2 完成后，新增域（如未来的群聊、频道）自动继承所有跨域能力。

### Phase 3：运维成熟度（1-2 周，P2）

| 方向 | 任务 | 工作量 |
|------|------|--------|
| 全链路可观测性 | 基于一致性清单的 SLO 监控 + 告警 | 1 周 |
| 全链路可观测性 | 慢查询仪表盘（pg_stat_statements + Grafana） | 0.5 周 |
| 全链路可观测性 | 结构化审计日志输出 | 0.5 周 |

---

## 6. 风险与缓解

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 双栈迁移过程中引入回归 | 中 | 高 | 逐域切换，每个域切换后运行完整的 `make test && make test-integration`。先在 staging 验证。 |
| 新栈 Service 接口与旧栈不兼容 | 高 | 中 | Phase 0 选择低风险域做验证。接口差异在决策阶段暴露。 |
| 团队对迁移意愿不一 | 中 | 中 | 选项 C（混合验证）提供客观数据辅助决策，减少纯主观争论。 |
| 事件契约注册表导致 outbox 性能下降 | 低 | 低 | 注册表是纯内存查找（`map[string]EventType`），无性能影响。消费者隔离的 goroutine fan-out 会增加 goroutine 数，但每个域消费者不超过 3-5 个。 |
| 权限模型迁移期间漏检查 | 中 | 高 | 双轨运行期间记录日志但不阻断。过渡期后手动分析日志确认无差异。 |

---

## 7. 对你问题的直接回答

### Q1：是否将文档保存到特定路径？

**建议：保存到 `docs/architecture/`**（而非 `docs/requirements/`，因这是架构分析而非需求文档）。

但我注意到 `2026-07-10-architecture-migration-and-systemic-gaps.md` 已在 `docs/requirements/` 下。可以将你之前的 `.out.md` 响应（即你作为代理询问"接下来做什么"的那段）保存为 `docs/decisions/2026-07-12-phase0-decision-log.md`——这作为决策记录更合适。

### Q2：下一步行动建议

按以下顺序：

1. **今天——做出 Phase 0 决策**（见下方 Q3 选项）
2. **本周——执行 Phase 0**（双栈归一化）
3. **下周——启动 Phase 1**（事件契约注册表 + 最终一致性治理）
4. **并行——修复搜索权限 bug**（即使 Phase 0 未完成，这个 bug 是独立的，应在搜索 handler 中立即修复 `SearchFiles` 不检查 `file_shares` 的问题）

### Q3：Phase 0 关键决策——我的建议

**选项 C：混合验证（推荐）**

理由：
- **风险最低**：先迁移 `block` 或 `preference` 域（最小、最独立），1-2 天内完成，验证路径是否通畅
- **数据驱动决策**：验证结果直接回答"迁移是否可行"——如果 block 域顺利切换，其他域大概率也可行
- **可回退**：如果验证发现新栈与旧接口不兼容的问题超出预期，随时切换到废弃策略

验证标准：
- 迁移后 `make test && make test-integration` 全绿
- 新旧端到端行为一致（定义一组 E2E 测试用例，在迁移前后输入相同数据，比较输出）
- 性能无退化（比较迁移前后 p50/p95 延迟）

如果验证通过 → 继续迁移其他域（计划 2-3 周完成全部迁移）。
如果验证不通过 → 标记新栈为已废弃，明确告知团队不要修改。

---

**总结**：代码库的架构基础是健康的（分层、依赖方向、组合根集中、outbox 模式——这些都是正确的架构决策）。核心问题是**一个未完成的重构**导致了两套平行代码。先解决这个，后续的跨域能力层、权限模型、事件治理才能在一个干净的底座上构建。
