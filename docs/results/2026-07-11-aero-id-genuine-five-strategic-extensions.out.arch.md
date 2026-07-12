Now I have a thorough understanding of the codebase. Let me deliver the architectural analysis.

---

# 架构分析报告：aero-id

## 1. 架构评估

### 1.1 现有架构的优势

**强分层隔离做得非常到位。** 基于 `handler → service → repository` 的单向依赖链是 Go 服务端最成熟的模式之一，没有循环依赖，也没有跨层越权。以下三点尤其值得肯定：

| 优势 | 证据 |
|------|------|
| **Service 是唯一的业务逻辑层** | `notif/dispatcher.go` 的 `Handle()` 同时做权限过滤(block check × preference check × rate limit)、聚合、持久化 — 代码非常集中 |
| **Outbox 模式落地扎实** | `outbox/` 包含完整的 `Event → Repository → Handler → Relay → Chain` 链路，而且已经用 `Chain` 本地组合 dispatcher + webhook |
| **功能开关治理清晰** | optional features 全部通过 `With*` option 注入（share repo、ref repo、audit logger），零 ifdef / build tag 污染 |

**事件驱动不是空想而是跑在生产线上。** Dispatcher 已经处理四种事件类型，且 Webhook 已验证 HMAC 签名。这套机制已为异步扩展做好准备。

### 1.2 需要正视的架构债务

验证文档指出的五个方向构成一份精确的"债务清单"。按架构影响重新归纳：

#### 债务 1：引用完整性的"半吊子"状态（高优先级）

当前系统在**引用完整性**上处于一种尴尬的中间态：

- `messages.file_id` 有 FK 但 `NO ACTION` — 文件删除会因 FK 阻塞，而不是优雅处理
- `file_shares` 无 expire/revoke 机制 — 时间敏感的协作无法安全实现
- 软删除到硬删除之间 `storage_refs` 的引用计数处于不一致状态（服务层 `Delete()` 递减 ref count，但软删的行仍可能在 GC 前被读取）
- 评论指向已删文件产生"幽灵引用"（评论永远不可访问但不清理）

**根本问题**：系统缺乏关于"当资源 A 被删除时，引用它的资源 B 应该怎样"的统一决策框架。当前每个 domain 自己做，行为不一致。

#### 债务 2：配额和资源控制缺失（中等优先级）

`fileService.Create()` 仅检查单文件上限（`maxBytes`），没有：
- 每个用户的总存储配额
- 文件数量上限
- 接口调用配额

这意味着系统在 MVP 阶段之后无法应对"滥用者上传 1TB 小文件"的场景。最危险的是：**配额检查需要在 `Create()` 一开始就拒绝（fail fast），而不是在写入完成后清理**。当前缺少这个 gate。

#### 债务 3：Seam 被静态配置固化了（低级但阻碍扩展）

`middleware/auth.go` 的 `TokenValidator` 接口是一个非常干净的 seam，但系统只在两种场景中使用它：
- HTTP handler
- gRPC interceptor

这个 seam 没有暴露给外部配置者。部署时可以插入一个 `APIKeyValidator` 实现来支持第三方客户端，但需要侵入式修改 app wiring。

### 1.3 关键设计决策评估

| 决策 | 原文编号 | 评价 |
|------|---------|------|
| 横向分层非纵向切分 (D2) | D2 | ✅ **正确。** 三个业务模块高度耦合（评论挂在文件/消息上），纵向切分会导致大量的跨模块接口桥接，复杂度转移而非消除。当前 `service/` 包下的 `file.go`, `message.go`, `comment.go` 同包互调非常自然。 |
| 使用 GORM 而非 sqlx (D3) | D3 | ⚠️ **目前合理但需保持警觉。** GORM 在引用完整性（级联删除、事务性写 outbox event）场景下的隐式行为需要持续测试覆盖。当前 `comment/repository.go` 和 `message/repository.go` 在同一个 tx 里写入 outbox event — 这恰好是 GORM 的甜点。当引入复杂 JOIN 和窗口函数时，GORM 会成为阻碍而非动力。 |
| 评论限两层 (D5) | D5 | ✅ **合理决策。** 楼中楼（两层）是社交产品公认的最佳实践。更深层级对 UI 和查询复杂度的影响远大于业务价值。 |
| 不在应用启动时自动 migrate (D10) | D10 | ✅ **生产最佳实践。** 多副本部署时迁移竞争是最常见的生产事故根源。当前 `AutoMigrate` 作为 dev/CI 的便捷开关保留，但生产用显式步骤，这是一个健康的 trade-off。 |
| 缓存 60s (D4) | D4 | ✅ **合理的起步时间。** 但需要注意：当前的 token 缓存是 `sync.Map` 实现的，不按 uid 分片。随着活跃用户增长，`sync.Map` 在所有 uid 上的全量扫描清扫会成为瓶颈。预计在 5k+ 在线用户时需要考虑分片或 LRU cache。 |

---

## 2. 扩展方向

### 方向 A：声明式资源依赖框架（P0）

**为什么需要**

当前五个方向中债务 1（引用完整性）和债务 5（生命周期管理）本质上是同一个问题的两面：**系统缺少一套统一的规则来描述"当资源 A 发生 X 时，相关资源 B 该怎么办"**。方向 2（引用完整性）定义级联策略，方向 5（生命周期 TTL）定义老化策略 — 它们能用同一个框架表达。

**核心挑战**

1. **注册 vs 硬编码**：当前每个 domain 自己写级联逻辑（comment 检查 target 是否存在 → 不存在则 404）。如果改成注册表，需要一种方式让每个资源类型声明自己的依赖关系。
2. **DB 层面的执行 vs 应用层面的执行**：PostgreSQL 的 FK + ON DELETE CASCADE 是最简单的方式，但 aero-id 的软删除模型和跨资源类型（comments → files 的多态 FK）让 DB 级约束不够用。
3. **事务边界**：级联操作（删除文件 + 删评论 + 删 blob）需要在一个事务中完成，否则会有中间状态暴露。

**建议方案：Domain Event Registry**

```
internal/
  lifecycle/
    registry.go        // ResourceLifecycle 注册表（声明依赖）
    strategy.go        // CascadeSoftDelete | CascadeOrphan | Block | SetNull
    ttl.go             // TTL 声明 + 清扫器
  domain/
    resource.go        // ResourceType 枚举 + 生命周期声明接口
```

每个资源类型在 `init()` 或 wiring 时注册：

```go
// 概念示意，非代码
lifecycle.Register(File, lifecycle.WithDependents(
    lifecycle.Dependent{Type: Comment, Strategy: CascadeSoftDelete, Via: "target_id"},
    lifecycle.Dependent{Type: Message, Strategy: SetNull, Via: "file_id"},
    lifecycle.Dependent{Type: FileShare, Strategy: CascadeSoftDelete},
))
```

**对现有系统的影响**

- **影响面中等**：需要为 `FileService.Delete()` 加一个统一的级联执行步骤。但业务逻辑结构不会变 — 只是把现有的散落在各 service 的"delete target"逻辑集中到 `lifecycle` 包。
- **迁移路径平滑**：可以逐步接入 — 第一个月只接入 File → Comment/Message/Share，后面的资源类型在扩展时再加入注册表。

### 方向 B：Pluggable Auth Seam — API Key 体系（P0）

**为什么需要**

验证文档的优先级调整判断准确：API Key 是多个方向的依赖项。具体来说：

| 依赖方 | 需要的 API Key 能力 |
|--------|-------------------|
| 配额管理（方向三） | 需要区分 first-party（内部服务）和 third-party（外部开发者）以应用不同配额 |
| 速率限制（方向四 Phase 0） | 需要基于 API Key 做 per-client 限制，而非仅 per-actor |
| 协作工作流（方向一） | 文件共享通知的推送需要区分事件的发起方来源 |

**核心挑战**

1. **Key 的管理面**：当前 `middleware/auth.go` 的 `TokenValidator` 是一个纯验证接口。要支持 API Key，需要一个新的 `APIKeyValidator` 实现，且需要为 API Key 提供 CRUD 管理端 — 这带来一个新的管理模块。
2. **Key 的元数据**：API Key 需要关联 client_id、client_type（first-party/third-party）、权限范围（scope）、和各维度配额。这比简单的 uid 映射复杂。
3. **混合认证**：同一请求可能同时携带 Bearer token（用户身份）和 API Key（客户端身份） — 需要设计清晰的优先级规则。

**建议方案：双层认证上下文**

```
ActorContext {
    UserUID: string        // 来自 Bearer token (SSO 验证)
    ClientID: string       // 来自 API Key (本地验证)
    ClientType: string     // "first_party" | "third_party"
    Scope: []string        // 授权范围
}
```

当前 `service.WithActor(ctx, uid)` 只注入了 uid。需要扩展为 `service.WithContext(ctx, ActorContext)`。

**对现有系统的影响**

- **影响面可控**：`middleware/auth.go` 的接口定义是 `TokenValidator`，新增 `APIKeyValidator` 遵守同一接口即可。路由层需要决定是 Bearer-only、API-Key-only 还是 Bearer+API-Key。
- **主要成本在管理和存储**：需要 `api_keys` 表 + CRUD API + 密钥哈希存储。

### 方向 C：统一配额引擎（P1）

**为什么需要**

缺少配额是当前架构最容易被"滥用"的点。一个用户可以：
- 上传 1000 个 100MB 的文件（无数量限制）
- 无限创建评论和消息（无频率限制之外的约束）
- 将文件设为公开后让 CDN 产生无限流量（无流量配额）

**核心挑战**

1. **配额的维度**：存储（bytes）、数量（count）、带宽（bytes/served）、速率（req/s）不同维度需要不同的计量策略。
2. **配额检查的点**：`Create()` 必须在写入前拒绝（fail fast），`Get()`/`List()` 的 quota 检查开销需要极低。
3. **超额处理**：软限制（警告）vs 硬限制（拒绝）的策略选择。

**建议方案：Quota Gate 接口**

```go
type QuotaGate interface {
    Check(ctx context.Context, actor string, resource ResourceType) error           // 写前检查
    Consume(ctx context.Context, actor string, resource ResourceType, amount int64)  // 写后计量
}
```

`service/Create()` 的流程变为：

```
QuotaGate.Check(actor, StorageBytes, file.Size)  →  fail fast 如果超限
store.Put(...)
repo.Create(...)
QuotaGate.Consume(actor, StorageBytes, file.Size)  // 记录实际用量
```

**对现有系统的影响**

- **影响面较小**：主要是给 `fileService.Create()`、`messageService.Send()`、`commentService.Create()` 增加可选的 `QuotaGate` 参数。不影响现有测试。

### 方向 D：Partitioned Rate Limiter（P1）

**为什么需要**

当前 `middleware/ratelimit.go` 的粒度是 per-actor（`uid:` 或 `ip:`）。这对于全局限制够用，但对于需要区分操作类型的场景（"每分钟创建评论 10 条"vs"每分钟读取文件 100 次"）无法满足。

**核心挑战**

1. **粒度设计**：是按 route（`POST /comments`）还是按 domain action（`comment.create`）分桶？
2. **与 API Key 的关系**：per-actor 限制和 per-client 限制是取交集（两者都通过）还是取并集（任一通过）？
3. **分布式一致性**：如果水平扩展到多副本，当前的 in-memory token bucket 无法共享状态。必须决定是否需要 Redis/Dragonfly。

**建议方案：分层 Token Bucket**

```
Layer 1: Global (per-instance)  — 对所有请求的总速率
Layer 2: Per-actor             — 对当前认证用户的速率
Layer 3: Per-action            — 对特定操作（comment.create, file.upload）的速率
Layer 4: Per-client (API Key)  — 对特定第三方客户端的速率
```

各层检查结果取交集（最严格的那个通过）。

**对现有系统的影响**

- **影响面小**：当前 `middleware/ratelimit.go` 的接口可以保持兼容，新增一个 `ActionLimiter` 注入 point。
- **迁移路径**：Phase 0 保持现状（per-actor），Phase 1 加入 per-action 和 per-client。

### 方向 E：声明式资源事件注册（P2）

**为什么需要**

当前的 outbox/dispatcher 架构已经非常好，但有一个成长瓶颈：**每次新增一个通知类型，需要改三个文件**（`dispatcher.go` 的 `Handle()` + `notificationFor()` + 具体 payload struct）。随着通知类型增长到 10+，这种模式的维护成本线性增加。

**核心挑战**

1. **声明式 vs 命令式**：需要一种方式让新通知类型以声明方式注册（"当 `file.shared` 事件发布时，生成 `notification.share_info`"），而不是在 dispatcher 里写 switch-case。
2. **模板化通知内容**：不同类型的通知内容格式不同（纯文本、富文本、本地化），需要支持模板扩展。

**建议方案：通知类型注册表（在现有架构上叠加）**

```
notif/
  registry.go       // 类型注册 + 模板
  template/         // 通知渲染模板
  dispatcher.go     // 保持不变（调用 registry 获取 renderer）
```

每个通知类型注册时声明：
```
Register("file.shared",
    Template("${actor} shared a file with you: ${file_name}"),
    SubjectType("file"),
    Blockable(true),      // 可以被接收者屏蔽
    Collapsible(true),     // 5分钟内同类通知可合并
)
```

这样 dispatcher 的 switch-case 就变成 registry lookup。

**对现有系统的影响**

- **影响面小**：Dispatcher 的接口不变，内部实现从 switch 变成 registry lookup。现有通知类型向后兼容。

---

## 3. 接口设计建议

### 3.1 核心原则

| 原则 | 说明 |
|------|------|
| **Seam at boundaries** | 每个外部依赖（存储、SSO、配额、速率限制）都以接口暴露，内部通过注入组装。当前做法已符合此原则，继续沿袭。 |
| **Extend, don't modify** | 对于 optional 功能（配额、层叠删除、API Key），全部通过 `With*` option 注入，零核心代码修改。 |
| **Keep the service layer clean** | Service 方法的签名不应因新功能改变。用 `context.Context` 传递 actor + 配额上下文，用 optional dependency 传递能力。 |

### 3.2 关键抽象层分析

当前最值得引入的新抽象层是 **Lifecycle Manager**（方向 A）和 **Quota Engine**（方向 C）。两者都遵循同一模式：

```
功能接口（QuotaGate / LifecycleRegistry）
  ↓ 可选的
Service 方法（WithQuotaGate / WithLifecycleRegistry）
  ↓
在 wiring 时组合
```

这个模式已经在 `fileService` 中被验证（`WithFileShareRepo`, `WithStorageRefs`, `WithFileAuditLogger`），新抽象层只需复制同一设计。

### 3.3 向后兼容性策略

1. **所有新接口都是可选的** — 不注入 = 行为不变。迁移路径零侵入。
2. **接口扩展用 variadic option** — 新增的 optional 参数不影响现有 `New*` 调用的签名。
3. **禁用旧行为以配置驱动** — 当某个新功能上线后，旧行为可以通过配置关闭，但默认值保持向后兼容。

---

## 4. 技术选型

### 4.1 需要评估的新技术

| 技术 | 适用场景 | 推荐决策 |
|------|---------|---------|
| **Redis/Dragonfly** | 分布式速率限制、API Key 缓存、配额计数器 | **不推荐当前引入。** 当前单实例 per-actor 限流足够；分布式限流只在 2+ 副本时必要。建议在首次生产部署后，根据实际扩展需求评估。 |
| **SQLite** | 本地测试、单机部署 | **不推荐。** aero-id 的 PG 特定能力（UUIDv7 索引、JSONB 查询、数组排序）在 SQLite 上没有完全对应。继续使用 testcontainers 测试 PG 即可。 |
| **protovalidate / buf** | gRPC 请求参数校验 | **推荐评估。** 当前 gRPC handler 的校验在 `validate.go` 中是手写的。`protovalidate` 可以让校验与 proto 定义一起维护，减少代码重复。已经是成熟技术。 |
| **golang-migrate CLI 替代品** | 数据库迁移 | **保持现状。** golang-migrate 在 aero-id 中运行稳定，已经是 Go 生态的主流选择。没有必要更换。 |
| **pgx 直接使用（代替 GORM）** | 复杂查询性能 | **评估而非替换。** GORM 在 90% 场景下足够。如果方向 A（声明式资源依赖）需要在单个事务中执行复杂的级联操作，可以考虑在 `lifecycle/` 包中使用 `pgx` 直接执行特定 SQL，其余部分继续用 GORM。 |

### 4.2 自建 vs 采购/引入的决策

所有方向都推荐**自建**，理由如下：

| 能力 | 为什么自建而非购买 |
|------|------------------|
| 配额引擎 | 业务逻辑非常特定（基于 uid 多维度配额、软/硬限制），没有通用的 SaaS 产品可以适配 |
| API Key 管理 | AWS/GCP 的 API Gateway 太重且太贵。自建只需 `api_keys` 表 + HMAC 签名验证，代码量 < 300 行 |
| 资源生命周期框架 | 高度耦合于 aero-id 的领域模型，不可能外部化 |

唯一的例外是**分布式速率限制**：如果未来需要跨越多个 aero-id 实例共享限流状态，引入 Redis/Dragonfly 是合理选择。但这是 P2，且在 3 副本以下不必要。

### 4.3 第三方依赖评估标准

引入新依赖的标准：
1. **API 稳定性** — major version 已 > 1.0，API 在过去 12 个月内无 breaking change
2. **Go 版本兼容** — 确认支持 Go 1.26
3. **许可证兼容** — MIT / Apache 2.0 / BSD
4. **活跃维护** — 在过去 3 个月内有 commit 或 release
5. **零 CGO 依赖** — aero-id 的 Docker 镜像是 distroless，CGO 会导致构建复杂化

---

## 5. 实施路线图

### 5.1 优先级调整后的阶段划分

```
Phase 0 (当前 MVP)       核心 CRUD + SSO + 双协议 + outbox + GC
                          └── 已完成
Phase 1 (P0 — 核心工程)   方向 B (API Key) + 方向 A (引用完整性)
Phase 2 (P1 — 治理能力)   方向 C (配额) + 方向 D Phase 0 (per-actor 加强)
Phase 3 (P1 — 协作能力)   方向一 (File Share 增强 + 共享通知 + GC 完善)
Phase 4 (P2 — 高级能力)   方向 D Phase 1-2 (分层限流 + 可管理 Webhook)
                          + 方向 E (声明式通知注册表)
```

### 5.2 阶段详情

#### Phase 1：核心工程（预计 3-4 周）

| 里程碑 | 交付物 | 风险 |
|--------|--------|------|
| M1.1 | `api_keys` 表 + CRUD API + HMAC 签名验证 | 密钥存储安全（必须哈希存储，不暴露原始 key） |
| M1.2 | `middleware/apikey.go` — API Key 验证 + `ActorContext` 扩展 | 混合认证（token + API Key）的优先级逻辑设计 |
| M1.3 | 文件引用完整性 — File→Comment/Message/Share 级联删除 | 事务边界（删除操作不能跨多个 transactions） |
| M1.4 | `storage_refs` 表中软删除期间的引用计数一致性问题修复 | GC 和业务删除之间的竞争条件 |

**风险缓解**：
- M1.1：密钥 hash 使用 `bcrypt`（与常见密码哈希一致），API 只返回 key 的前缀（`ak_xxxx...xxxx`）用于展示
- M1.3：所有级联操作在一个 GORM transaction 中执行，利用 GORM 的 `tx *gorm.DB` 传播

#### Phase 2：治理能力（预计 2-3 周）

| 里程碑 | 交付物 | 风险 |
|--------|--------|------|
| M2.1 | `QuotaGate` 接口定义 + 存储配额实现 | 配额检查的原子性（并发上传绕过配额检查） |
| M2.2 | `fileService.Create()` 接入 `QuotaGate` | 不影响现有 Create 签名的兼容设计 |
| M2.3 | 速率限制 per-action 扩展 | 配置的复杂度增长 |

**风险缓解**：
- M2.1：配额消耗用 DB 行级锁（`SELECT ... FOR UPDATE`）确保原子性，不依赖分布式锁
- M2.3：per-action 限制使用一致的命名空间 `action:<uid>:<action_name>`，不创建新数据结构

#### Phase 3：协作能力（预计 2-3 周）

| 里程碑 | 交付物 | 风险 |
|--------|--------|------|
| M3.1 | `file_shares` 增加 `expires_at` + `permissions` 列 | 现有共享数据迁移（需要设默认值） |
| M3.2 | outbox `file.shared` 事件 → dispatcher 处理 → 推送通知 | 通知去重（一个文件被共享给 10 人，需要 10 条通知） |

**风险缓解**：
- M3.2：Dispatcher 已经支持 per-recipient 通知，只需在 `dispatcher.go` 中注册新事件类型

#### Phase 4：高级能力（预计 3-4 周）

| 里程碑 | 交付物 | 风险 |
|--------|--------|------|
| M4.1 | 分层限流（per-action + per-client） | 性能开销（每个请求检查多层桶） |
| M4.2 | Webhook CRUD API + 事件订阅管理 | 认证安全（管理 Webhook 的 API 必须严格鉴权） |
| M4.3 | 声明式通知类型注册表 | 现有通知类型的兼容迁移 |

**风险缓解**：
- M4.1：Token bucket 检查是 O(1) 操作，4 层检查仍然是 O(1)，性能影响忽略不计
- M4.2：Webhook CRUD 仅限 admin，不走 public API，降低攻击面

### 5.3 风险全景图

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| 级联删除跨多表的事务死锁 | 中 | 高 | 使用固定的资源遍历顺序（先 File → 后 Comment → 后 Message），避免循环依赖死锁 |
| API Key 泄露 | 中 | 高 | 存入数据库前用 bcrypt 加盐哈希；支持密钥轮换 |
| 配额计数和实际消耗不一致 | 低 | 中 | 每周跑一次对账脚本，用 `SUM(size)` 对比 `quota_usage` 表 |
| 分布式部署时 per-actor 限流失效 | 中 | 低 | 在 Phase 2 明确告知算子：per-actor 限流是 per-instance 的，如果扩展到 >1 副本需要 Redis |

### 5.4 不推荐立即做的事情

以下事情在验证文档中被隐含提及，但我认为应该推迟：

1. **事件驱动的 Schema 变更（Event-driven Schema Evolution）** — 当前 23 个迁移文件，
   schema 变化频率已经很低。声明式注册表在 schema 稳定后价值不大。
2. **结构化 API 调用日志表** — 当前的 request_id + zap 日志对于调试和审计已经足够。只有在需要按 API 调用计费/分析时才需要引入。YAGNI。
3. **评论 GC（清理指向已删文件的评论）** — 可以合并在方向 A 中一并实现，不需要单独引入。

---

## 总结

aero-id 的现有架构质量很高。**五个方向验证文档是一个精确的路障清单**，而不是系统崩溃报告。当前系统最大的架构缺口是：

> **[单一缺口] 缺少一个统一的"当主资源被删除时，附属资源怎么办"的决策框架**

这个缺口横跨引用完整性、生命周期管理、和 GC 一致性三个方向。如果只做一件事情，那就是建立 `lifecycle/` 包来统一管理资源依赖。这个单一举措能同时解决方向二、方向五和 GC 一致性问题，是最高 ROI 的架构投资。
