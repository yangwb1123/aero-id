现在我对当前代码库有了全面的了解。下面是我的架构分析。

---

# 架构分析：aero-id

## 1. 架构评估

### 当前架构的优势

代码库经过了**远超原始设计文档的巨大演变**。最初 2026-05-22 的设计文档概述了一个 3 实体 CRUD 服务，声明“超出范围”的是 outbox、metrics、tracing、后台 worker、Redis、OpenTelemetry。代码库现在拥有所有这些，还有更多：

| 组件 | 实施状态 | 架构质量 |
|-----------|--------------|-------------------|
 | 事务性发件箱 (`internal/outbox/`) | 成熟——重试、退避、死信、webhook 传递 | 高。独立的包，清晰的接口，优雅的 `MetricsSink` 抽象 |
 | 实时 SSE Hub (`internal/hub/`) | 现有，具备每 uid 分片锁、非阻塞发布、断开连接 | 中等。进程内，不适用于多实例扩展 |
 | 通知 (`internal/notification/`) | 完整生命周期——写入发件箱的事件 → 分派器创建行 → Hub 推送 → 偏好/阻止感知 | 高。正确的架构解耦 |
 | 速率限制 (`internal/pkg/ratelimit/`) | 带有空闲驱逐的进程内令牌桶 | 中等。每实例限制（后续扩展方向 #3） |
 | 追踪 / 指标 | OpenTelemetry + Prometheus RED 指标 | 好。选择加入，非侵入式 |
 | 存储 GC | 带保留窗口的已删除文件/孤立 blob 清理 | 高。引用计数感知 |
 | 管理/封禁系统 | RBAC 角色、用户封禁（带过期）、审计跟踪 | 完整且正确（反驳了被分析文档中主张的“不存在”） |
 | 评论审核 | 报告/解决/驳回工作流，每个实体 | 功能完整，但**没有全局的管理队列** |
 | 幂等性 | 用于安全重试的 GORM 支持的存储 | 好。正确的 `Idempotency-Key` 实现 |

**架构模式的成熟度：** 事务性发件箱结构（将事件写入域 tx → 中继异步排空）代表了事件驱动正确性的最佳实践。它与通知/阻止/推送基础设施的结合，使这个代码库在技术债务曲线上的位置出乎意料地靠前——比原始设计文档所暗示的要成熟得多。

### 架构债务

**1. 跨代码库的重复包结构**

这是最紧迫的债务。代码库有两个平行的组织方案：

*旧版*：`internal/{comment,file,message}/` — 每个模块都有自己的 `handler_http.go`、`handler_grpc.go`、`repository.go`、`service.go`
*新版*：`internal/service/{comment,file,message}.go` + `internal/handler/{http,grpc}/` — 集中式服务层

旧版包似乎会委托给新版，但它们在混淆 `internal/app/new.go` 中的依赖关系图。在横向阅读代码库时，不清楚一个给定函数实际包含业务逻辑的位置。例如：`internal/comment/service.go:15449` 将业务逻辑委托给 `internal/service/comment.go:17969`，但调用者可以通过任一入口到达。

**决定：** 要么完成迁移（删除旧版包），要么回退并统一命名。目前的状态使得“业务逻辑单一来源”的维护信条难以验证。

**2. 配置扩散**

`config.Config` 包含约 50 个可配置字段，分布在 12 个子结构中（App、Server、Database、SSO、Storage、Tracing、RateLimit、Outbox、Log、DataRetention 等）。许多功能是选择加入的，默认禁用。对于新操作者来说，理解系统功能需要阅读整个配置结构。

风险不在于技术正确性，而在于**可操作性**：当一个生产问题出现时，运行中的配置可能激活了也可能未激活 GC、速率限制、自动迁移、追踪、退化处理和审计清理——所有这些都以非显而易见的方式相互作用。

**3. 通知管道复杂性**

域代码通过事务性发件箱写入事件 → 中继轮询 → 通知分派器创建行 → SSE Hub 推送给客户端。这是架构上正确但复杂的。该管道中需要链式处理的每个阶段都会增加全生命周期延迟和操作面。

对于 MVP，这是合理的设计。如果系统要扩展到更高的通知吞吐量（每秒数百个事件），分派器从单个数据库事务中创建每个通知行将会成为瓶颈。

**4. 没有会话（群聊）概念**

消息完全是 1 对 1 的。没有 `conversations` 表，没有 `conversation_participants`，没有多用户聊天。原始架构决策“YAGNI 群聊”使系统在面对多用户消息处理请求时处于劣势。

### 关键架构决策：评估

| 决策（原始设计文档） | 实际 | 评估 |
|----------------|--------|----------|
| D2：横向分层 vs 纵向按模块拆分 | 同时存在（旧版 + 新版） | 债务。选择一个方向 |
| D3：GORM vs sqlx | GORM | 对于复杂的查询（搜索、聚合、通知），ORM 抽象开始出现裂痕 |
| D8：不内置指标/追踪 | 已添加 Prometheus + OTel | 正确的决定。原始 YAGNI 过于严格 |
| D9：testcontainers-go | 已实施 | 正确。实际 PostgreSQL 优于模拟 |

**架构健康状况总结：** 代码库比文档所述更加成熟，但带来了可见的组织债务。它在架构模式（发件箱、分片锁、引用计数 GC）方面表现出色，但在包组织方面存在不足。

---

## 2. 扩展方向

以下是 5 个高价值架构扩展方向，根据对代码库实际现状的审查确定。它们解决了真正存在的缺口，而不是批评文档中想象的那些。

### 方向 1：会话（群聊）基础设施

**为什么需要：** 代码库当前支持 1 对 1 消息。任何多用户消息传递场景（群聊、频道、项目讨论）都需要新的基础设施。这不仅仅是添加一个表——它需要重新思考消息寻址、在线状态和读后确认。

**核心挑战：**
- 消息寻址：从 `{from_uid, to_uid}` 到 `{from_uid, conversation_id}`，参与者列表是外部的
- 读后确认：在 N 方会话中，每条消息有 N-1 个读回执
- 加入/离开事件：参与者变更必须生成系统消息并更新活跃集
- 推送通知：会话中的消息必须传递给 N-1 个收件人，而不是 1 个
- 现有向后兼容性：`internal/message/` 中已建立的 `MessageService` 接口需要扩展现有方法的重载或新方法

**期望的架构变更：**
```
internal/
  conversation/              # 新域包
    conversation.go          # 实体：Conversation, Participant
    repository.go            # conversations, conversation_participants 表
    service.go               # 创建、加入、离开、列出成员
    handler_http.go
    handler_grpc.go
```

`messages` 需要一个 `conversation_id` 列（可为空，对于现有 1 对 1 消息为 NULL，以保持向后兼容）。

**对当前系统的影响：** 中等。现有 1 对 1 消息继续工作。新表被添加。分派器需要更新，以处理每个会话的通知扇出。

**优先级：** P1（如果多用户消息传递在产品路线图上）

---

### 方向 2：全局审核控制台

**为什么需要：** 评论审核目前是每个实体的——评论作者可以查看和解决针对他们自己评论的报告。管理员不能查看整个系统的报告、批量操作或自动执行内容过滤。这是一种“个人审核”模型，不适合需要中央信任与安全团队的平台。

**核心挑战：**
- 报告聚合：`comments` 表中的 `comment_reports` 需要可被管理员发现。目前没有 `SELECT * FROM comment_reports ORDER BY created_at` 的全局端点
- 内容安全管线：批评文档正确地识别出一个真正存在的缺口——自动过滤（敏感词 → Spam 评分 → NSFW）。这是一个全新的基础设施，而不是关于审核面板的 API 增量
- 批量操作：审核员需要能够隐藏/删除/封禁多个实体
- 审计集成：审核操作必须写入审计日志（已存在 `internal/audit/`，所以这主要是连接的问题）
- 通知：被审核影响的用户需要被告知（现有通知基础设施可以处理）

**期望的架构变更：**
```
internal/
  moderation/                # 新包（或扩展 internal/service/admin.go）
    filter/
      chain.go               # 可配置的过滤器管线
      sensitive.go           # 敏感词匹配
      spam.go                # Spam 评分
      nsfw.go                # NSFW 图像检测
    service.go               # 全局队列、解析、自动操作
    handler_http.go
    handler_grpc.go
```

现有 `CommentService.Report` / `ResolveReport` 保持不变。`AdminService` 获得一个 `ListAllReports` 方法。过滤器管线被注册为评论创建/编辑路径中的中间件。

**对当前系统的影响：** 低到中等。`admin.go`（HTTP）和 `admin.go`（gRPC）已经有 `RequirePermission(PermUserBan)` 模式，可以扩展到 `RequirePermission(PermModerate)`。新的 `moderation/` 包是增量的。

**优先级：** P1（对任何拥有多个作者/用户的平台都是高价值）

---

### 方向 3：用于水平扩展的分布式共享状态

**为什么需要：** 系统有三个进程内组件在多实例部署中会失效：
1. **SSE Hub** — 连接到实例 A 的客户端不会收到实例 B 发布的事件
2. **速率限制器** — 每个实例独立跟踪令牌；在 5 个实例上，有效速率是配置值的 5 倍
3. **幂等性存储** — 当前是 GORM 支持的（数据库事务性），所以对于幂等性实际上没问题，但速率限制和 Hub 是纯内存的

**核心挑战：**
- Hub：需要一个共享的 pub/sub 层（Redis Pub/Sub、NATS 或数据库 LISTEN/NOTIFY）
- 速率限制：需要一个共享计数器（Redis 或数据库行，具有原子递增）
- 延迟：Redis pub/sub 引入网络跃点；对于 SSE，这增加了 ~1ms 的尾部延迟
- 故障模式：Redis 宕机 → 速率限制应“故障开放”（当前行为），Hub 应降级为仅本地传递

**期望的架构变更：**
```
internal/
  hub/
    hub.go                   # 保持不变（本地接口）
    remote.go                # 新：Redis pub/sub 适配器（可选包装）
  pkg/ratelimit/
    ratelimit.go             # 保持不变（本地）
    distributed.go           # 新：Redis 支持的实现（可选）
```

键设计原则：**本地实现是默认和回退**。如果 Redis 未配置，Hub 和速率限制器独立运行。如果配置了 Redis，则将其用于共享状态，但每个实例在 Redis 宕机时降级为本地。

**对当前系统的影响：** 低。Redis 连接是选择加入的。`Hub` 和 `Limiter` 保持其现有接口；添加了共享的后端。

**优先级：** P1（如果部署扩展到 >1 个副本）

---

### 方向 4：SSO 弹性与离线模式

**为什么需要：** SSO 是一个外部 gRPC 依赖。当前，`stale_ttl` 允许在 SSO 宕机时提供过期的用户缓存数据，但**所有** `/v1/*` 请求需要 SSO 验证令牌。如果 SSO 宕机超过缓存 TTL（60s），则每个经过身份验证的请求都会失败。

**核心挑战：**
- 令牌验证：aero-id 不签署令牌。没有 SSO，就没有身份验证方法
- 用户解析：如果 SSO 宕机，用户缓存最终会过期，并且列举实体（评论、消息）会无法解析用户显示名称
- 降级策略：只读操作可能通过过期的缓存允许；写入操作应明确失败
- SSO webhook 用于级联删除：批评文档正确识别出“SSO 不通知 aero-id‘用户已删除’”是一个缺口——当前，已删除的 SSO 用户的文件/评论/消息会保留，变成孤立的

**期望的架构变更：**
```
internal/
  sso/
    client.go                # 保持现有客户端；添加降级感知
    degraded.go              # 新：降级模式下的绕过策略
  middleware/
    auth.go                  # 在 SSO 无法访问时添加受限制的访问
    degradation.go           # 已存在——扩展以处理 SSO 特定的降级
```

SSO webhook 端点：`POST /webhooks/sso/user.deleted`，接收用户删除事件并触发应用内级联（通知的 `dispatcher.go` 可以处理）。

**对当前系统的影响：** 低到中等。降级逻辑是选择加入的。SSO webhook 是一个新的 HTTP 端点，由外部调用，映射到现有清理逻辑。

**优先级：** P2（对生产部署高价值，对开发/阶段部署低）

---

### 方向 5：操作优先级感知速率限制

**为什么需要：** 当前速率限制器是一个单一的每用户令牌桶，对所有请求一视同仁。这意味着关键的`GET /v1/messages/unread-count` 流量与后台的 `POST /v1/comments/{id}/reports` 争夺相同的配额。批评文档正确地识别出这是一个真正存在的缺口——没有一个现有分析覆盖了操作优先级。

**核心挑战：**
- 分层桶：每个用户应该有多个桶（关键/正常/后台），具有不同的 RPS 配置
- 关键流量保证：读确认、未读计数和 SSE 重连应保证容量，即使后台批量操作被限制
- 配置：操作 → 优先级映射必须是声明式的（可能在 route 注册中或中央配置文件中）
- 向后兼容性：默认配置应将所有现有操作视为“正常”，这样现有的 `rate_limit.rps` 设置继续工作

**期望的架构变更：**
```
internal/
  pkg/ratelimit/
    ratelimit.go             # 添加多桶支持（每个 key 的桶映射）
    priority.go              # Priority 枚举和配置
  middleware/
    ratelimit.go             # 由路由选择的优先级感知 Key 派生
  handler/
    http/
      router.go              # 每个路由的优先级注解
    grpc/
      server.go              # gRPC 方法的优先级注解
```

每个路由的配置行示例：
```go
authMW := []gin.HandlerFunc{
    middleware.RateLimit(cfg.RateLimiter, ratelimit.PriorityCritical), // 用于未读计数
}
```

**对当前系统的影响：** 低。Limiter 获得了每个 key 的桶映射而不是单个桶。现有使用 `Allow(key)` 的调用者获取默认优先级（正常）。

**优先级：** P1（为生产级韧性提供高价值）

---

## 3. 接口设计建议

### 关键接口原则

**1. 完成从旧版到新版包结构的迁移**

`internal/{comment,file,message}/`（旧版）与 `internal/service/{comment,file,message}.go`（新版）之间的歧义必须解决。

**选项 A：删除旧版包**，将所有路由移入`internal/handler/{http,grpc}/`。这是更清晰的长期方案，但需要重构调用者。
**选项 B：从旧版包重定向到新版**，使用薄包装器。这解决了歧义，没有破坏调用者。
**选项 C：回退**，统一到旧版结构。这是减少破坏的最短路径，但从架构上看不太清晰。

**建议：** 选项 A，分两步进行：(1) 在单个 PR 中迁移所有路由，然后 (2) 删除旧版包。

**2. 通过组合而不是继承的方式来扩展**

对于每个提议的方向，新的服务类型应该组合现有的服务，而不是嵌入它们：

```go
// 正确的方法：
type ModerationService struct {
    adminService  service.AdminService  // 组合
    commentService service.CommentService
    filter        *FilterChain
}

// 错误的方法：
type ModerationService struct {
    *service.AdminService  // 继承——不要这样做
}
```

这保持了单向依赖关系图（`ModerationService` → `AdminService`，而不是相反）。

**3. 让基础设施组件可选择加入并可在本地回退**

Hub、速率限制器和其他进程内组件应保持其当前接口。远程/共享后端被添加为可选包装器，在依赖不可用时透明地降级：

```go
type Hub struct { /* 本地实现——保持不变 */ }
type RemoteHub struct {
    local *Hub     // 降级回退
    redis *redis.Client
}
func (h *RemoteHub) Publish(uid, typ, data string) {
    if err := h.redis.Publish(...); err != nil {
        h.local.Publish(uid, typ, data) // 降级
    }
}
```

---

## 4. 技术选型

### 是否需要新技术？

| 组件 | 当前 | 未来状态 | 需要的技术 |
|-----------|---------|--------------|----------------|
 | 实时 Pub/Sub | 进程内 Hub | 分布式 Hub | Redis Pub/Sub 或 NATS |
 | 分布式速率限制 | 进程内桶 | 共享桶 | Redis（带原子 INCR + EXPIRE） |
 | 内容审核管线 | N/A | 敏感词 + Spam + NSFW | 用于 NSFW 检测的机器学习模型（TensorFlow Lite / ONNX）或外部 API |
 | 全文搜索 | PostgreSQL FTS（`tsvector`） | 专用搜索索引 | 根据规模需求：pgvector 或 Elasticsearch/Meilisearch |
 | 文件转码 | N/A | 图像缩略图 / 病毒扫描 | 后台工作者（Go 工作者池或 Sidekiq） |

### 自建 vs 采购

| 决策 | 建议 | 理由 |
|--------|------------|---------|
| 审核 NSFW 检测 | 自建（初始阶段使用 TensorFlow Lite） | 对于早期使用模式，外部 API 的延迟/成本过高。TFLite 提供良好的离线分类 |
| Redis 共享状态 | 采购（`go-redis` 客户端） | 轮子不重新发明。Redis 是经过实战检验的操作级基础设施 |
| 消息队列（发件箱以外的用途） | 当前不需要 | 事务性发件箱模式对于当前吞吐量足够。当需要异步工作者时，重用现有中继 |

### 第三方依赖评估标准

对于每个新依赖，使用以下检查清单：
1. **许可证兼容性**：必须与 MIT/Go 标准兼容
2. **API 稳定性**：首选 `v1` 稳定版本
3. **影响范围**：该依赖是否引入传递性依赖？（选择没有 ORM 的 Redis 客户端，而不是包含整个框架的客户端）
4. **测试兼容性**：该依赖是否能在 testcontainers 环境中良好运行？（对于集成测试）

---

## 5. 实施路线图

### 优先级摘要

| 方向 | 优先级 | 工作量估计 | 价值时间线 | 依赖 |
|------------|----------|--------------|-----------------|----------------|
 | P1：全局审核控制台 | P1 | ~2 周 | 短期（即刻审核覆盖率） | 方向 5（内容管线） |
 | P2：操作优先级速率限制 | P1 | ~1 周 | 短期（运营韧性） | 无 |
 | P3：分布式共享状态 | P1 | ~2 周 | 中期（应扩展） | 需要 Redis 生产布置 |
 | P4：会话基础设施 | P2 | ~4 周 | 长期（产品路线图） | 消息模式迁移 |
 | P5：SSO 弹性 | P2 | ~2 周 | 中期（生产可靠性） | SSO 团队协作 |

### 第一阶段：运营韧性（第 1-2 周）

**里程碑：** 速率限制优先级 + 审核控制台

可交付物：
- `internal/pkg/ratelimit/priority.go`：多桶速率限制器
- `internal/middleware/ratelimit.go`：每个路由的优先级注解
- `internal/moderation/service.go`：全局报告队列（`GET /v1/admin/reports` + `POST /v1/admin/reports/:id/resolve`）
- `internal/moderation/filter/`：初始敏感词匹配管线（可配置的关键词列表）
- 向后兼容性验证：现有 `rate_limit.rps` 配置继续工作；现有报告端点保持不变

**风险：** 路线密度。如果这两个并行工作，代码审查瓶颈。
**缓解：** 第一阶段分两块，在集成测试边界处解耦。

### 第二阶段：大规模扩展（第 3-4 周）

**里程碑：** 多实例准备就绪

可交付物：
- `internal/hub/remote.go`：用于跨实例 SSE 传递的 Redis Pub/Sub 适配器
- `internal/pkg/ratelimit/distributed.go`：用于全局速率限制的 Redis 支持的令牌桶
- `internal/storage/gc/`：确认引用计数 GC 在 Redis 并置下正确运行
- 文档：操作手册，涵盖 Redis 设置、监控配置和故障转移程序
- 集成测试：启动两个 aero-id 实例 + Redis，验证 SSE 消息在实例之间传递

**风险：** Redis 引入了一个新的故障点。如果 Redis 宕机，系统应降级为本地操作（Hub 仅本地传递，速率限制独立）。
**缓解：** 第一阶段的所有分布式组件都有本地回退。Verify failure modes in integration tests。

### 第三阶段：新领域（第 5-8 周）

**里程碑：** 群聊（如果确认） + SSO 弹性

可交付物（会话）：
- 数据库迁移：`conversations` + `conversation_participants` 表；`messages.conversation_id` 可为空
- `internal/conversation/` 包：CRUD + 成员管理
- `internal/service/message.go`：添加 `SendToConversation` 方法
- `internal/notification/dispatcher.go`：会话消息的扇出推送通知
- 向后兼容性：现有 1 对 1 消息 API 保持不变（`conversation_id` IS NULL）

可交付物（SSO 弹性）：
- `POST /webhooks/sso/user.deleted`：接收 SSO 用户删除事件
- `internal/service/sso_cleanup.go`：由 webhook 触发的级联软删除
- 降级扩展：当 SSO 无法访问时，允许通过过期的令牌缓存进行有限时间的读访问

**风险：** 会话显著增加了消息传递基础设施的复杂性。系统从未被设计用于多用户路径。
**缓解：** 会话实施是纯增量的——现有 1 对 1 API 不受影响，1 对 1 路径的性能不变。第二阶段中的扇出推送通知在分派器中处理，重用发件箱模式。

### 架构审查门禁

在每个阶段，应用以下门禁：
1. `make check-file-sizes` — 文件不得超过 500 行（强制执行 AGENTS.md）
2. `make lint` — 没有 vet 错误，复杂度在限制范围内
3. `make test-integration` — 新的数据库交互经过 testcontainers 验证
4. 向后兼容性 — 旧版 API 客户端的集成测试仍通过
5. 配置选择加入 — 新功能默认禁用（请参阅 `RateLimitConfig.Enabled` 和 `StorageGCConfig.Enabled` 模式）

---

## 总结

aero-id 代码库状态良好——比设计文档所承认的要好得多，并且明显比所审查的分析文档所描述的更完整。事务性发件箱模式、分片 SSE Hub 和引用计数存储 GC 都是高质量的架构决策，可以将系统置于比通常预期的更成熟的阶段。

然而，重复的包结构（旧版 `internal/{comment,file,message}/` 与新版 `internal/service/`）是一个需要解决的债务。配置已经扩散到大约 50 个字段，使得系统对于新操作者来说理解起来更加困难。

五个扩展方向解决了真正的缺口：
1. **审核控制台 + 内容管线** 填补了“没有全局审核”的缺口
2. **操作优先级速率限制** 为关键流量提供精细化韧性
3. **分布式共享状态** 实现水平扩展
4. **会话基础设施** 解锁多用户消息传递
5. **SSO 弹性** 减少外部依赖

第一阶段（2 周）交付审核 + 速率限制优先级。第二阶段（2 周）交付多实例就绪。第三阶段（3-4 周）交付会话 + SSO 弹性。

每个方向都遵循一个不变的模式：**选择加入默认禁用，通过组合而不是继承来本地回退，纯增量到现有路径**。这确保现有生产流量永远不会中断。
