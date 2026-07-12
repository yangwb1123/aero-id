下面是我作为架构师的完整分析报告。

---

# aero-id 架构分析报告

> 日期：2026-07-12
> 范围：aero-id Go 服务代码仓库，基于 `docs/requirements/` 下的分析文档、`AGENTS.md` 工程约束、以及全量代码扫描

---

## 1. 架构评估

### 1.1 现状评估

aero-id 的代码质量成熟度很高。单向依赖约束（handler → service → repository → Postgres）严格执行，误差处理体系（`AppError` → HTTP/gRPC 双映射）完整，transactional outbox、幂等键、限流、可观测性（RED 指标 + OTel Tracing + SSO 健康仪表盘）等基础设施全部就位。

**整体架构风格**：水平分层（Layered Architecture），而非垂直切片（Vertical Slices）。每个横向层（handler、service、repository）承载所有领域职责。

### 1.2 优势

| 维度 | 评价 |
|------|------|
| **依赖方向** | 严格的单向依赖，没有循环导入。这是 Go 项目中罕见的纪律性。 |
| **误差处理** | `AppError` 统一了 HTTP 和 gRPC 的错误表示，service 层翻译原始错误，handler 只渲染。没有原始 DB 错误泄漏到传输层。 |
| **基础设施完整度** | Outbox 模式、断路器、Stale-cache fallback、Singleflight、幂等键——这些生产级模式全部就位，很多项目上线后半年才开始补。 |
| **可测试性** | Service 层通过接口依赖 repository/storage/SSO，可以独立 mock 测试。集成测试用 testcontainers 启动真实 Postgres 和 MinIO。 |
| **双传输对齐** | HTTP 和 gRPC 共享同一套 service 层，38 个 RPC 完全对齐。这是正确的一步。 |

### 1.3 局限性

**局限性一：水平分层 vs. 垂直切片的不一致性**

当前代码中存在两套平行的架构风格：

| 风格 | 接入路径 | 文件组织 |
|------|---------|---------|
| **Layered（旧）** | `internal/handler/{http,grpc}/*` → `internal/service/*` → `internal/repository/*` | 按层分目录 |
| **Vertical（新）** | `internal/{file,message,comment,...}/{handler_http,handler_grpc,service,repository}.go` | 按领域分目录 |

`app.go` 只导入了旧风格的新层。这意味着 `internal/message/service.go`、`internal/file/service_crud.go` 等文件**对运行时无影响**——它们是被编译了（在同一 module 内）但从未被调用。这是一个「重构中状态」：代码已迁移但接线未完成。

风险在于：如果有人同时维护两套代码，它们将产生分歧。当前 `internal/message/service.go` 中的 `Send` 和 `internal/service/message.go` 中的 `Send` 在逻辑上等价，但如果 bug 修复只修改了其中一套，行为就会出现偏差。

**局限性二：Admin 运营面有骨架但缺肌肉**

代码审查发现，`internal/handler/http/admin.go` 已经比分析文档描述的更完整——它已经使用了 `RequirePermission` 中间件（`PermAuditRead` 和 `PermSystemConfig`），并且已经实现了角色授予/撤销/列表端点。但以下关键缺口仍然存在：

- `PermUserBan`（`domain/admin.go` 定义）——**零使用**。没有 API 端点，没有 `user_bans` 迁移，没有封禁/解封逻辑。
- `PermSystemConfig` —— 只有角色管理路由使用它，但没有系统配置查询/更新 API，没有系统广播 API。

这意味着角色权限系统是个"半成品"：可以授予角色和查询审计日志，但权限框架预先规划的两个核心运营操作（封禁用户、管理系统配置）没有实现。

**局限性三：UserCache 无界增长（已证明）**

`UserCache` 没有 `RunCleaner` 等价物，而 `TokenCache` 有。这是一个生产环境 OOM 隐患。修复成本 ~30 行，风险完全不可接受。

**局限性四：死信队列不可见**

`outbox_events` 表的 `dead_letter` 列已存在，事件在达到 `maxRetries` 后确实被标记为死信。但没有任何指标暴露死信数量，没有任何端点可以查询/重新投递/清除死信事件。这是一个无预警的静默数据丢失路径。

### 1.4 架构债务

| 债务类型 | 位置 | 严重程度 | 修复成本 |
|---------|------|---------|---------|
| 双重服务层（未完成的架构迁移） | `internal/{file,message,...}/` vs `internal/service/` | 中 | 中（一次性的接线工作） |
| 死权限代码 | `PermUserBan` 和 `PermSystemConfig` 定义了但未使用 | 低-中 | 低（实现或删除） |
| UserCache 缺少淘汰 | `internal/sso/user_cache.go` | 高（OOM 风险） | 极低（<30 行） |
| 死信队列无监控 | `internal/outbox/relay.go` | 中 | 低 |

---

## 2. 扩展方向

### 方向 A：完成垂直切片迁移（架构债消除）

**为什么需要**：
当前的双重服务层是"重构中状态"——两套平行的实现不接线。这带来三个具体风险：
1. 两套代码可能逐渐偏离（bug fix 只修了一套）
2. 新人需要耗费认知负担理解哪个是"活的"
3. 测试套件有两套但只运行其中一套

**核心挑战**：
- `internal/service/` 中的 service 依赖于 `internal/repository/` 中的 GORM 实现
- 新风格包（`internal/message/` 等）自己定义了独立的 repository 接口和 handler
- 接线不是简单的 import 替换——需要决定哪种架构胜出

**预期架构变更**：

选项 A（推荐）：**选新弃旧**

将 `app.go` 的 wiring 从旧风格切换到新风格包。这意味着：
1. 删除 `internal/service/` 下的所有文件（代码已迁移到 `internal/{domain}/service.go`）
2. 删除 `internal/handler/{http,grpc}/` 下的所有 handler（代码已迁移到 `internal/{domain}/handler_*.go`）
3. 将 `app.go` 的 import 改为指向 `internal/{file,message,comment,...}` 的 New 函数
4. 删除 `internal/repository/` 中与 `internal/{domain}/repository.go` 重复的部分

选项 B（保守）：**弃新留旧**

删除 `internal/{file,message,...}/service.go` 等文件（作为未完成的重构回滚），保持现有架构。但这样做损失了新风格带来的领域内聚性（每个领域的 handler + service + repository 在一个目录中）。

**对现有系统的影响**：选项 A 是一次性的中等工作量（1-2 天），选项 B 更快（半天）但错过了架构改进。

### 方向 B：会话列表（Threaded Inbox）——产品层级缺失

**为什么需要**：
当前 `ListInbox` 返回的是一个扁平的消息列表（`WHERE to_uid = ? ORDER BY created_at DESC`）。如果用户 A 发了 20 条消息给当前用户，前 20 条结果全来自 A，B/C/D 的消息被完全淹没。任何即时通讯应用的第一屏是会话列表——这个缺失直接影响产品竞争力。

**核心挑战**：
- GROUP BY 查询需要确认 `idx_messages_inbox(to_uid, created_at DESC) WHERE deleted_at IS NULL` 索引是否覆盖
- 分页：基于 `from_uid` 的 cursor 分页需要仔细设计（不能用 `id` 作为 cursor）
- 未读数聚合：需要 `COUNT(*) ... WHERE to_uid = ? AND read_at IS NULL GROUP BY from_uid`

**预期架构变更**：
- 新增 repository 方法 `ListConversations(ctx, uid, page)`，SQL 使用 `GROUP BY from_uid`
- 新增 service 方法，添加上下文注入 + 用户信息 enrichment
- 新增 HTTP `GET /v1/conversations` + gRPC RPC
- 可选：新增物化视图 `conversation_summary` 表，支持大规模生产环境

**对现有系统的影响**：纯新增，不修改现有 API。低风险。

### 方向 C：死信队列管理平台（可靠性提升）

**为什么需要**：
当前死信事件一旦标记就不可见、不可查询、不可恢复。运营人员无法回答基本问题："过去一小时有多少事件被死信？""哪些事件类型最容易失败？"

**核心挑战**：
- 死信数据在 `outbox_events` 表中，数量可能很大（不应该一次性加载全部）
- 重新投递（replay）需要清除 `dead_letter` 标志并重置 `retry_count`
- 防止无限重投：replay 操作应该有速率限制和手动确认

**预期架构变更**：
- `MetricsSink` 增加 `ObserveOutboxDeadLettered(eventType string)`
- 新增 `GET /admin/outbox/dead-letters`（分页查询）
- 新增 `POST /admin/outbox/dead-letters/:id/replay`（重新投递）
- 新增 `POST /admin/outbox/dead-letters/:id/dismiss`（清除）
- 配置 Prometheus 告警规则：`sum(rate(aero_id_outbox_dead_letter_total[5m])) > 0`

**对现有系统的影响**：低风险。不影响 outbox 核心路径，纯新增端点。`MetricsSink` 的 `ObserveOutboxDeadLettered` 是向后兼容的接口扩展（现有实现只需新增方法体，或者通过适配器模式容忍缺少的方法）。

### 方向 D：用户封禁系统（安全缺口填补）

**为什么需要**：
`PermUserBan` 权限已定义、`admin` 角色已预置此权限、表结构已设计——但没有任何 API 使用它。社交平台必须有能力封禁滥用用户，目前只能直接操作数据库。

这不仅是功能缺失，也是**安全合规问题**：没有 API 意味着谁有数据库访问权限就可以绕过任何审计。

**核心挑战**：
- 封禁范围的定义：封禁后的影响面（消息、评论、文件、还是全部？）
- 封禁的时效性：临时封禁 vs. 永久封禁，到期自动解封？
- 历史数据处理：封禁后是否隐藏历史内容？
- 通知：被封禁用户是否需要收到通知？

**预期架构变更**：
- 新增 migration `user_bans` 表（`uid`, `reason`, `banned_by`, `expires_at`, `created_at`）
- 新增 repository 方法：`IsBanned(ctx, uid)`, `BanUser(ctx, uid, reason, ...)`, `UnbanUser(ctx, uid)`
- service 层增加 ban check 守卫点：在 `Send`、`CreateComment`、`CreateFile` 等写入操作前检查
- HTTP `POST /v1/admin/users/:uid/ban`, `POST /v1/admin/users/:uid/unban`
- 在 `RequirePermission(PermUserBan)` 中间件下保护

**对现有系统的影响**：
- 中风险。ban check 需要在多个 service 方法中插入（Send、CreateComment、CreateFile 等），需要仔细设计守卫点的插入位置
- 需要考虑性能：ban check 是一个额外的 DB 查询或缓存查询
- 建议在 middleware 层做 ban check（影响所有写操作），或在 service 层通过装饰器模式实现
- 由于 SSO 持有用户身份，需确认 ban 的逻辑是否要与 SSO 同步

### 方向 E：用户画像运营分析面板（高业务价值）

**为什么需要**：
当前的服务对单个用户的操作没有便捷的聚合视图。运营人员无法查询"某个用户发送了多少消息""上传了多少文件""被举报了多少次"等基础运营 KPI。

**核心挑战**：
- 跨表聚合查询（messages、files、comments、reports、audit_log）
- 大表扫描的性能（COUNT 在亿级表上很慢）
- 数据时效性：实时 vs. 准实时 vs. T+1

**预期架构变更**：
- 新增 `user_stats` 物化表或聚合查询 API
- `GET /v1/admin/users/:uid/stats?period=7d` 返回：消息数、文件数、评论数、举报次数、存储空间等
- 后台 cron 或物化视图刷新机制（可选）
- 在 `RequirePermission(PermAuditRead)` 下保护

**对现有系统的影响**：低风险，纯新增。但需要注意大表 COUNT 的性能影响，初期可以用 `EXPLAIN` 分析，后期引入物化视图。

---

## 3. 接口设计建议

### 3.1 关键设计原则

**原则一：Service 接口是契约，不是实现细节**

当前 `internal/service/service.go` 中定义的服务接口已经是一个好的契约——它同时被 HTTP 和 gRPC handler 使用。如果完成垂直切片迁移，新风格包（`internal/message/message.go`）中的 `Service` 接口应当保持与现有接口一致的签名，避免下游 handler 的修改。

**建议**：在完成垂直切片之前，先统一接口命名规范。旧风格：`MessageService`，新风格：`message.Service`。如果选择新风格，将旧包中的接口定义迁移到新包后，确保 `internal/handler/{http,grpc}/` 仍然满足接口。

**原则二：Admin API 应当通过 `RequirePermission` 中间件统一保护**

分析文档指出 `ListAudit` 硬编码检查 `audit.read`，当前代码已修复这个缺口（现在通过中间件保护）。**这是一个正向变化**。后续所有 admin 端点应严格遵循此模式：

```go
admin := r.Auth.Group("/admin")
admin.Use(middleware.RequirePermission(domain.PermWhatever, h.permChecker))
```

不要在 handler 函数体内重复权限检查——这会导致未来的端点忘记做检查。

**原则三：向后兼容性是默认要求**

所有新端点应当：
1. 使用新的 URL 路径或新的 gRPC RPC（不修改现有路径的语义）
2. 分页使用已有的 cursor 规范（`id < cursor ORDER BY id DESC LIMIT n+1`）
3. 错误使用已有的 `AppError` 体系（HTTP code + gRPC code 双映射）
4. 响应格式使用已有的 `httpx.OK` / `httpx.WriteError` 模式

### 3.2 是否需要新的抽象层

**当前不需要**。aero-id 的四层抽象（handler → service → repository/storage/sso）已经足够。

以下场景可能需要新的抽象层：

| 场景 | 推荐方案 | 理由 |
|------|---------|------|
| 用户封禁守卫点 | **中间件层装饰器** | Ban check 需要在多个 service 方法前执行，用装饰器模式（类似 middleware）比在每个 service 方法内重复检查更干净 |
| 跨领域聚合查询 | **CQRS 查询层** | 如果用户画像统计查询变得复杂，可以引入独立的 `internal/query/` 包，绕过 service 直接访问 repository 的读方法 |
| 分布式缓存 | **Repository 层缓存装饰器** | 如果单实例内存缓存不够（如需要共享的 UserCache），可以在 repository 接口上包装一个 Redis 层，对 service 透明 |

### 3.3 保持向后兼容性

| 变更类型 | 兼容策略 |
|---------|---------|
| 新增端点 | 完全兼容——新 URL，不影响现有 |
| 修改现有端点响应 | 字段只加不减（用 `json:",omitempty"`），不重命名现有字段 |
| 修改 gRPC proto | 字段编号不变，`reserved` 已删除字段。只加 optional 字段 |
| 迁移到垂直切片 | 先保留旧 handler 作为转发路由，确认新实现稳定后再删除旧代码 |

---

## 4. 技术选型

### 4.1 当前技术栈评估

| 层 | 当前选择 | 评估 | 建议 |
|----|---------|------|------|
| HTTP | Gin | 成熟、足够。Go 生态中事实标准。 | 无变化 |
| gRPC | google.golang.org/grpc | 标准选择 | 无变化 |
| ORM | GORM v2 | 功能覆盖 CRUD + transaction + migration | 如果未来需要复杂查询，考虑在 repository 中混用 raw SQL |
| 配置 | Viper | 标准选择 | 无变化 |
| 日志 | Zap | 高性能，结构日志 | 无变化 |
| 追踪 | OpenTelemetry | 行业标准 | 无变化 |
| 存储 | local / S3（MinIO 兼容） | 足够 | 无变化 |
| 缓存 | 进程内 map | 够用，但有 UserCache 无界增长问题 | 修复 UserCache 淘汰机制 |
| 限流 | 进程内 token bucket | 单实例够用 | 如果多实例部署，考虑 Redis 中心化方案 |

### 4.2 是否需要引入新技术

**短期（未来 1-2 个 sprint）**：不需要新第三方依赖。

五个扩展方向都可以在当前技术栈内实现：
- UserCache cleaner：纯 Go 代码，无外部依赖
- 死信队列管理：纯 Go + Postgres 查询
- 管理员 API：纯 Go + Postgres
- 会话列表：Postgres GROUP BY 查询
- 垂直切片迁移：代码重组，无新依赖

**中期（未来 1-3 个月）**：考虑引入：

| 技术 | 适用场景 | 决策信号 |
|------|---------|---------|
| Redis / ValKey | 多实例限流、分布式缓存 | 当实例数 > 2 时 |
| pgx 直接调用 | 复杂聚合查询比 GORM 更高效 | 当 GROUP BY 查询或用户画像查询出现性能瓶颈时 |
| Prometheus AlertManager 规则（YAML） | 死信告警 | 已经在用 Prometheus 指标，只需添加告警规则 |

### 4.3 自建 vs. 集成的决策

**所有当前需求都应自建实现，不需要采购外部服务**。理由：
1. aero-id 本身是基础设施服务，不涉及业务层审核/内容管理
2. 所需功能（会话列表、角色管理、死信查询）本质上是数据库查询 + API 暴露
3. 引入外部服务带来的附加复杂度（集成测试、断路、SLA 管理）远大于收益
4. 工程约束（文件 ≤500 行、函数 ≤50 行）确保了自建代码的可维护性

### 4.4 第三方依赖评估标准

评估新依赖时，建议按照以下标准过滤器：

```
1. GOAL: 这个依赖解决什么问题？是否可以用 100 行以内 Go 标准库实现？
   → 如果可以，标准库优先

2. COMPLEXITY: 这个依赖的 API 表面是否 >5 个方法？
   → 是的 → 说明抽象层次太高，考虑更轻量的替代

3. DEPTH: 这个依赖的传递依赖树有多深？
   → 引入 1 个依赖连带 10+ 个间接依赖 → 考虑替代方案

4. MAINTENANCE: 这个依赖的版本更新频率和社区响应速度？
   → 超过 6 个月未更新 → 风险

5. TESTING: 这个依赖是否可以在测试中 mock 或替换？
   → 不能直接 mock（如数据库驱动）→ 确保有接口抽象层
```

---

## 5. 实施路线图

### 5.1 优先级（基于当前代码状态重新评估）

基于今天的代码审查（2026-07-12），以下是对分析文档的增量修正后的优先级：

| 方向 | 优先级 | 工作量 | 风险 | 理由 |
|------|--------|--------|------|------|
| **UserCache 淘汰机制**（方向二） | **P0** | ~30 行 | 生产 OOM | 时间问题，不是概率问题 |
| **死信队列可见性**（方向五） | **P1** | 低 | 静默数据丢失 | 当前可以靠直接查询数据库运维，但不可持续 |
| **Admin 用户封禁**（方向三的子集） | **P1** | 中 | 运营安全缺口 | `PermUserBan` 定义了但 0 行使用代码，需要填补 |
| **会话列表**（方向一） | **P1** | 低-中 | 产品 UX 缺失 | 基础即时通讯能力 |
| **垂直切片迁移**（附） | **P2** | 中 | 代码分歧风险 | 当前无功能 bug，但随时间推移风险增大 |
| **系统广播 API**（方向四） | **P2** | 低 | 运营工具缺失 | 基础设施已就绪，只缺 API 暴露 |
| **Admin 系统配置 API**（方向三子集） | **P2** | 低 | 运营工具缺失 | 有 `PermSystemConfig` 权限，有端点框架，但无实际配置 API |

**关于方向三的特别说明**：

分析文档将方向三（Admin 运营面）从 P1 升级为 P0，理由是 `ListAudit` 绕过 `RequirePermission` 中间件。经过代码验证，这个问题**已被修复**——当前 `internal/handler/http/admin.go` 已经通过 `admin.Use(middleware.RequirePermission(domain.PermAuditRead, h.permChecker))` 统一保护审计日志端点。因此方向三回归到 P1。

但是 `PermUserBan` 完全未使用仍然是一个安全缺口，应该在 P1 周期处理。

### 5.2 阶段划分

**阶段 1（1-2 天）：P0 安全修复**

```
目标：消除生产环境 OOM 风险
- UserCache 增加 RunCleaner(30s) ← 30 行，复制 TokenCache 模式
- 添加 ObserveOutboxDeadLettered 到 MetricsSink
- 验证：make test && make lint
```

**阶段 2（1 周）：P1 核心功能 + 观察性**

```
目标：补齐最小的运营能力和可恢复性

Week 1:
- 死信队列查询 + 重新投递 API（3 个端点）
- 死信 Prometheus 告警规则
- Admin 用户封禁：user_bans 迁移 + repository + service + handler（2 个端点）
- 所有 admin 端点通过 RequirePermission 中间件保护
- 验证：make test && make lint && make test-integration
```

**阶段 3（1 周）：P1 产品功能**

```
目标：补齐基础产品 UX 缺口

Week 2:
- 会话列表 ListConversations HTTP + gRPC 端点
- 基于 GROUP BY from_uid 的 SQL 查询
- 客户端可用的会话预览（最新消息 + 未读数 + 时间戳）
- 验证：make test && make lint && make test-integration
```

**阶段 4（1-2 周）：P2 架构清理**

```
目标：消除架构债务

Week 3-4:
- 选择"垂直切片"或"水平分层"作为唯一架构风格
- 如选垂直切片：迁移 app.go 接线，删除旧风格代码
- 系统广播 API：暴露 POST /v1/admin/broadcast
- 确认两套测试套件合并为一，无测试覆盖率下降
- 验证：make test && make lint && make test-integration
```

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| UserCache cleaner 在并发下死锁 | 低 | 高（服务中断） | 用 `sync.RWMutex` pattern（与现有 `GetMany` 一致），在测试中加 -race 检测 |
| 垂直切片迁移破坏现有 API | 中 | 高 | 先写集成测试锁定现有行为，再重构。确保两套 handler 实现对相同输入产生相同输出 |
| Admin 封禁影响面过大（过度封禁） | 中 | 中 | 封禁检查只在写入路径插入，不影响读取。可以加 feature flag 逐步放开 |
| 会话列表 GROUP BY 查询在亿级消息表上超时 | 低-中 | 中 | 先在 staging 用生产数据量压测。如果超时，添加覆盖索引 `(to_uid, from_uid, created_at DESC)` 或物化视图 |
| 系统广播 API 全量发送阻塞 | 中 | 中 | 全量发送作为后台异步任务执行，API 立即返回任务 ID，客户端轮询进度 |
| 双重服务层在迁移期间继续分化 | 高（随时间增加） | 中 | **建议立即冻结新风格包**的修改，直到做迁移决策。在此期间所有功能修改只能在旧风格包上进行 |

### 5.4 关键决策点

| 决策 | 到期时间 | 决策依据 |
|------|---------|---------|
| **垂直切片 vs. 水平分层** | 阶段 4 之前 | 当前代码库同时存在两种风格，需要团队明确选择。建议：选垂直切片（`internal/{domain}/{service,handler_http,handler_grpc,repository}.go`），因为它为每个领域提供了更好的封装性，且 `app.go` 的 wiring 更清晰 |
| **UserCache 淘汰策略** | 阶段 1 | 选项：① 定时全量扫描（与 TokenCache 一致，~30 行） ② LRU 淘汰 ③ 概率性清理。建议选项①——与现有模式一致，最轻量 |
| **死信保留策略** | 阶段 2 | 死信事件需要保留多久？建议：保留 7 天后自动删除（新 migration 添加 TTL 列 + 后台清理），或一直保留直到手动操作 |
| **封禁范围** | 阶段 2 | 封禁是否阻止读取、评论、消息、上传全部？建议：初始实现"硬封禁"——禁止所有写入操作（消息、评论、文件上传）。读取不受影响。作为 feature flag 可调 |

---

## 总结

aero-id 是一个工程成熟度很高的 Go 服务。当前代码库的五个关键缺口方向均经过代码级验证，其中两个（UserCache 无界增长、死信队列不可见）是生产可靠性问题，需要优先修复；Admin 运营操作面和会话列表是产品/运营缺口，在 P1 周期补全；双重服务层是架构债，在 P2 周期清理。

**最重要的建议**：在新增任何功能之前，先修复 UserCache 的 ~30 行淘汰机制。这是唯一一个在生产环境一定会触发且不可逆（内存只增不减）的问题。其他所有缺口都是功能缺失或工具缺失，可以按优先级逐步补齐。
