现在我已完全掌握代码库的当前状态。以下是我的架构分析：

---

# 架构分析：aero-id 领域导向重构与扩张路径

## 1. 架构评估

### 优势

当前代码库的技术质量令人印象深刻。值得保留和保护的优点包括：

- **严格的单向依赖**：`handler → service → repository/storage/sso` 链清晰且未违反。没有来自 service 层的循环导入。
- **Outbox 模式**：已使用可重试、退避、死信和链式处理器（通知分发器 + 可选 Webhook）正确实现。事务日志记录可靠。
- **双传输奇偶校验**：HTTP 和 gRPC 处理程序包装同一 `service` 包，没有 I/O 逻辑渗透到 domain 中。
- **可废弃的领域驱动包**：`internal/domain/` 拥有分离良好的实体、不变量和枚举，没有外部依赖。
- **工程约束强制执行**：文件大小限制、圈复杂度上限和函数长度约束（在 AGENTS.md 中）——全部是可测量且不可协商的。

### 重大发现：架构分叉（P0 架构债务）

这是对反馈文档中"事实纠正"的**重大修正**。该文档声称：

> 当前代码库已经从**单体 service 包**演进为**按领域划分的独立包**：
> 文件 → `internal/file/` (service_crud.go, handler_http.go, ...)

**这不是运行时使用的路径**。`app.go`（组合根）从 `internal/service/` 导入所有内容，**从** `internal/file/`、`internal/message/`、`internal/comment/`、`internal/notification/`、`internal/block/` 或 `internal/search/` **没有导入任何内容**。

实际的依赖图是：

```
app.go → service.* (internal/service/)
           → repository.* (internal/repository/)
           → storage.*, sso.*, domain.*
```

而领域包（`internal/file/`、`internal/message/` 等）是**完全独立的平行实现**。它们定义自己的 `Repository` 接口、自己的 `Service` 接口和 struct、自己的 handler 结构体——但**没有任何东西将它们连线到运行时**。这是孤立的死代码。

| 包 | 状态 |
|---|---|
| `internal/service/` | **运行时激活**——`app.go` 连线到这里 |
| `internal/file/` 的 `Service` + `Repository` + handler | **孤立**——未导入 |
| `internal/message/` 的 `Service` + `Repository` + handler | **孤立**——未导入 |
| `internal/comment/` 的 `Service` + `Repository` + handler | **孤立**——未导入 |
| `internal/notification/` 的 `Service` + `Repository` + handler | **孤立**——未导入 |
| `internal/block/` 的 `block.go` + `Repository` + handler | **孤立**——未导入 |
| `internal/search/` 的 `search.go` + handler | **孤立**——未导入 |
| `internal/audit/` 的 `repository.go` | **孤立**——类型由 `service.*` 通过 `repository.*` 使用 |

这是最紧急的架构债务：团队在重构时创建了第二个服务层，但从未将组合根切换到新结构。现在有两个代码体需要维护，而其中一个（领域包）静默腐烂，因为没有流量经过它。

### 次要发现：`mustActor` 重复

`domain/actor.go` 和 `service/service.go` 都定义了 `MustActor`、`ActorFrom` 和 `WithActor`。`domain` 版本现在是**规范的**（被领域包使用），但运行时使用 `service` 版本。这增加了连接不匹配的风险。

---

## 2. 扩展方向

### D0（新增）：解决架构分叉 — 完成领域导向重构

**为什么需要**：在重新构建之前支付技术债务。领域包结构是正确的目标，但组合根尚未切换。继续构建新功能同时在两个平行的服务层之间分裂会成倍增加摩擦。

**核心挑战**：

1. `internal/service/` 的 `Repository` 类型导入 `internal/repository/`（例如 `repository.FileRepository`）。领域包定义自己的 `Repository` 接口。两者大致等效但**不完全相同**——迁移需要映射所有方法。
2. 传输层（`internal/handler/{http,grpc}`）引用 `service.XxxService` 接口。领域包的 `Service` 接口具有相同的方法签名但位于不同的包中。处理程序需要更新它们的导入。
3. `hub/adapter.go` 当前导入 `internal/service` 以实现 `service.NotificationPusher`。如果迁移到领域包，hub 需要导入一个不同的接口定义。

**预期的架构变更**：

```
Phase 1: 统一接口
  - 将规范接口定义从 internal/service/ 移动到 internal/{file,message,comment,...}/ 各自的包
  - 使 internal/service/ 的 struct 包装领域包的 struct（适配器模式）

Phase 2: 切换组合根
  - app.go 从 internal/{file,message,...} 导入
  - internal/service/ 变薄为仅重新导出或完全删除

Phase 3: 删除旧代码
  - 一旦没有代码路径到达 internal/service/，在运行完整测试套件后删除它
```

**对现有系统的影响**：高风险。这影响每一层。从开始到完成需要一个专用的冲刺，并全面测试覆盖。

---

### D1. 配额管理 — ⭐ P0（与反馈一致）

**为什么需要**：没有配额管理层，任何单个用户可以耗尽共享存储或淹没其他用户的消息收件箱。这是一个计费/安全基础。

**核心挑战**：

1. **写路径延迟**：同步配额检查为每次 Create/Upload/Send 增加了一次 DB 往返。对于消息（高频、低延迟预期），这可能很重要。
2. **并发超限**：如果检查是异步的，高并发下可能发生短暂超限。如果检查是同步的，行锁竞争成为一个因素。
3. **资源配置**：`resource_type` 需要从当前的 3 个（文件、消息、评论）映射扩展到未来的实体（群聊、收藏等）。

**预期的架构变更**：

- 新的 `quota` 表：`(uid, resource_type, soft_limit, hard_limit, used_count, period_start)`
- `internal/quota/` 领域包，包含：
  - `domain/` 中的 `Quota` 实体
  - `Repository`（用于 upsert+check 的 GORM）
  - `Service.Check(ctx, uid, resourceType, delta) error`
- 在 service 层的 Create/Upload/Send 路径中插入同步检查
- 可选的异步 outbox 事件用于更新计数（反馈文档的建议）

**技术选型权衡**：同步与异步

| 标准 | 同步检查 | 异步（出站事件） |
|---|---|---|
| 精度 | 精确 | 最终一致 |
| 写延迟 | +1 次 DB 往返 | 无增加 |
| 实现复杂度 | 简单 | 中等 |
| 前期推荐 | ✅ 用于文件上传 | 当延迟敏感时用于消息/评论 |

**对现有系统的影响**：中等。为新表新增迁移。在每个领域包的 service 中插入检查调用。无需更改传输层。

---

### D2. 媒体处理流水线 — ⭐ P1（与反馈一致，但缩小范围）

**为什么需要**：MIME 白名单仅阻挡格式伪装，不阻挡合法类型中的恶意负载。病毒扫描是安全基线。

**核心挑战**：

1. **`domain.File` 缺少 `Status` 字段**：需要 `active` / `quarantined` / `deletion_pending` 枚举。这需要迁移 + 实体变更。
2. **Service 层文件读取需要状态感知**：`FileService.Get()`、`DownloadURL()` 必须拒绝隔离文件。新状态字段必须渗透 ACL 检查。
3. **流水线架构**：当前出站处理器是单体式的（`notification.Dispatcher` 处理所有事件）。需要通用的 `Processor` 接口。

**预期的架构变更**：

- 向 `domain.File` 添加 `Status string`（值：`"active"`、`"quarantined"`、`"pending_deletion"`）
- `internal/pipeline/` 包，包含：
  - `Processor` 接口
  - `Pipeline`（带优雅失败的有序处理器链）
  - `ClamAVProcessor`（初始，Unix socket 调用）
  - `ProcessorResult`（通过出站事件写回文件状态）
- 出站事件 `"file.uploaded"` → `pipeline.Handle` → 更新 `file.Status`

**对现有系统的影响**：中等。新增表迁移 + 新包。ACL 检查需要一处协同变更。

---

### D3. SSE 多实例正确性（出站模式 Phase 0）— ⭐ P0（优先级提升）

**为什么需要**：当前 `hub.Hub` 是完全进程内的。多实例用户收不到推送到其他实例的通知。**这不是性能问题，是正确性问题。**

**核心挑战**：

1. **Redis Pub/Sub 桥接**：`hub.Publish()` 当前发布到本地订阅者。需要也发布到 Redis 频道，同时后台 goroutine 订阅 Redis 并推送到本地订阅者。
2. **连接泄露**：如果 Redis 宕机，hub 必须优雅降级（仅本地传递），而不是崩溃。
3. **`Last-Event-ID` 重播已经实现**：`events.go` 已经实现了 `replayMissed` 方法。代码已经存在——它只需要配置驱动的 Redis 桥接来补充它。

**预期的架构变更**：

- `internal/hub/redis.go` 或 `internal/hub/hub.go` 中的可选 `RedisHub`：
  - 如果配置了 Redis 端点，`hub.New()` 接受可选的 `redis.Client`
  - `Publish` → 本地订阅者 + `redis.Publish("sse:{uid}", msg)`
  - 后台 goroutine → `redis.Subscribe` → `h.pub()`
- `config.Redis` 块（可选，nil = 仅进程内）
- **不改变通知分发器或出站事件**

**对现有系统的影响**：低。hub 更改是新增的。如果未配置 Redis，行为保持不变。

---

### D4. 协作与社交图谱 — ⭐ P2（与反馈一致）

**为什么需要**：会话/群聊是超越基本消息收发。但 aero-id 的产品定位问题（"个人数据存储 API"与"团队协作平台"对比）需要先于技术研究回答。

**核心挑战**：

1. **消息模型扩展**：当前 `Message` 有 `FromUID`/`ToUID`（1:1）。群聊需要 `ConversationID` 和 `conversation_participants` 表。
2. **文件协作**：`file_shares` 表有读权限但无写/管理概念。需要 `permission` 列。
3. **提及/通知**：`@mention` 解析需要将用户名映射到 UID（需要 SSO 集成）。

**预期的架构变更**：

- `conversations` 表 + `conversation_participants` 表
- 向 `Message` 添加 `ConversationID`
- 向 `file_shares` 添加 `Permission` 列
- `internal/conversation/` 领域包

**对现有系统的影响**：中等至高。涉及多个表迁移和消息实体的向后兼容扩展。

---

### D5. 合规接口 — ⭐ P1（与反馈一致，但添加缺失的 SSO 清理 RPC）

**为什么需要**：数据导出和删除对于法规合规是必要的。但**当 SSO 删除用户时，aero-id 留下孤立的引用**。

**核心挑战**：

1. **用户数据清理 RPC 缺失**：proto API 定义中有 38 个 RPC，但没有用于 SSO 发起的清理的 `DeleteAccountData`。
2. **硬删除与软删除**：所有实体当前使用 `gorm.DeletedAt`。合规性硬删除需要超出软删除的单独的 GC 通道。
3. **导出限制**：为单个用户导出所有文件/消息/评论可能是大量数据。需要异步作业存储（不是当前架构）。

**预期的架构变更**：

- 新 proto RPC `DeleteAccountData(uid)` + 双传输处理程序
- `internal/compliance/` 包，包含：
  - `ExportJob`（异步，存储到临时位置）
  - `DeleteAccountData`（同步，级联软删除，安排硬删除）
- SSO 集成：SSO 在删除用户时调用 `DeleteAccountData`
- 审核日志记录所有合规操作

**对现有系统的影响**：中等。新 RPC + 新包。需要 SSO 端协同变更来调用新的 RPC。

---

## 3. 接口设计建议

### 3.1 服务接口去重

**当前问题**：`internal/service/service.go` 定义了 `FileService`、`MessageService`、`CommentService` 等接口。`internal/file/file.go` 也定义了 `Service` 接口。这两个集合需要收敛。

**推荐**：规范接口定义应属于领域包（`internal/file/`、`internal/message/` 等）。`internal/service/` 应该包装它们并最终消失。

```
// 目标状态
internal/file/
  file.go           → Service interface (规范)
  service_crud.go   → fileService struct (唯一实现)
  service_upload.go → 上传方法
  service_share.go  → 分享方法

internal/handler/http/file.go → 导入 file.Service
internal/handler/grpc/file.go → 导入 file.Service
```

### 3.2 新抽象层的评估

**需要新的抽象层：**

| 层 | 理由 | 推荐 |
|---|---|---|
| `internal/quota/` | 配额管理是跨领域的横切关注点 | ✅ 独立领域包 |
| `internal/pipeline/` | 媒体处理是有独立生命周期管理的可组合工作流 | ✅ 独立包 |
| `internal/compliance/` | 合规 API 需要异步作业管理 | ✅ 独立领域包 |
| `internal/conversation/` | 群聊是消息的子域，但需要自己的数据类型 | ✅ 独立包 |
| 通用 `middleware/quota.go` | 配额可能作为中间件检查，但 service 层检查更精确 | ❌ 在 service 层检查 |

**不需要：**

| 提议 | 理由 |
|---|---|
| 共享分布式锁层 | 不是必需的——outbox `published_at IS NULL` 检查 + 幂等存储已经处理了常见的竞争 |
| 通用事件总线抽象 | 出站事件已经是这个抽象；另一个抽象会重叠 |

### 3.3 向后兼容性

**写路径**：

- 新配额检查返回明确的 `429 Too Many Requests`——对于不知道这个错误码的老客户端来说，这是在标准 HTTP 限制内的行为（他们任何情况下都会得到 4xx）。
- 新文件状态字段默认为 `"active"`，因此现有客户端不受影响，直到他们尝试在隔离状态下下载文件。

**读路径**：

- 到领域包的服务接口迁移应该定义新接口作为旧接口的超集。当旧处理程序升级时，会逐步进行编译时检查。
- `internal/service/` 中的遗留接口应获得包装 `internal/file/Service` 实现的适配器方法。

**数据模式**：

- 所有新模式新增都应该是带默认值的新列（`ALTER TABLE ... ADD COLUMN ... DEFAULT`）。
- 软删除模式（`gorm.DeletedAt`）保持不变——合规性硬删除是新通道，不影响现有路径。

---

## 4. 技术选型

### 4.1 新框架/依赖评估

| 技术 | 用途 | 评估 |
|---|---|---|
| Go-Redis (go-redis/redis) | SSE Redis Pub/Sub 桥接、分布式缓存、分布式限流 | ✅ 推荐。轻量级、经过实战检验、无 CGo |
| ClamAV (clamav 库) | 病毒扫描 | ✅ 通过 Unix socket 集成——无需额外基础设施 |
| FFmpeg | 缩略图/转码 | ❌ P2 — 推迟。引入重型子进程依赖。当需要时，使用 `cmd/ffmpeg` 包装器 |
| RabbitMQ / Kafka | 异步处理 | ❌ 不必要的——出站模式 + Postgres 轮询处理 < 1000 个事件/秒 |
| Elasticsearch / Meilisearch | 全文搜索 | ❌ Postgres `pg_trgm` 对于 P1 的搜索需求已经足够 |
| Redis (分布式限流) | 跨实例速率限制 | ⏳ Phase 2（D3）——进程内限流对于单实例来说当前是可接受的 |

### 4.2 自建与采购决策

| 决策 | 推荐 | 理由 |
|---|---|---|
| 配额引擎 | 自建 | 业务逻辑简单（计数 + 限制），独立于任何外部供应商。10 个领域对象 |
| 病毒扫描 | 集成（ClamAV） | ClamAV 是 FOSS，通过 Unix socket 集成琐碎，提供真正的安全价值。供应商 API 会增加延迟 + 成本 |
| SSE Redis 桥接 | 自建（+redis 库） | 〜50 行核心逻辑。Redis Pub/Sub 模型直接映射到用例 |
| 合规数据导出 | 自建 | 异步作业模式使用现有的出站表。无供应商依赖 |
| 群聊 | 自建 | 领域核心，不是商品功能。25 个领域对象 |

### 4.3 需要避免的技术选择

1. **用于配额计数的 Redis**：`INSERT ... ON CONFLICT DO UPDATE` 在 Postgres 中对于这个工作负载更快且更简单。分布式锁增加复杂性而没有收益。
2. **外部消息代理**：出站模式 + Postgres 轮询可以处理数百万个事件而无需 Kafka。
3. **用于缩略图的 Lambda/FaaS**：异步处理可以在 aero-id 内部运行，没有 FaaS 延迟/成本。

---

## 5. 实施路线图

### 优先级排序

```
P0 — 发布阻塞：    分叉解析 (D0) + SSE 多实例 (D3 Ph0) + 配额 (D1)
P1 — 高价值：       合规 (D5) + 病毒扫描 (D2 Ph0)
P2 — 战略：         群聊 (D4) + 缩略图/转码 (D2 Ph1-2)
```

### 阶段划分和里程碑

**第 1-2 周：准备金矿（D0）+ 架构分叉解析**

| 任务 | 影响 |
|---|---|
| 审计 `internal/service/` 与 `internal/{file,message,comment}/` 之间的所有接口差异 | 无代码变更 |
| 将规范 `Service` 接口定义从 `internal/service/` 移动到 `internal/{domain}/` | 大范围但机械的更改——导入路径只影响处理程序 |
| 为 `internal/service/` 实现适配器包装器（委托给 `internal/{domain}/Service`） | 逐步切换 |
| 统一 `domain.MustActor` 作为唯一规范来源 | 删除 `service.MustActor`，更新调用者 |
| 将传输层（`handler/{http,grpc}`）切换到新规范接口 | 每处理程序 2-3 行导入更改 |

**里程碑 1**：`make test && make lint` 绿色通过，无导入周期，`app.go` 使用领域包。

**第 3 周：D3 Phase 0 — SSE Redis 桥接**

| 任务 | 文件 |
|---|---|
| 向 `hub.New()` 添加可选 `redis.Client` 参数 | `internal/hub/hub.go` |
| 在 `Publish()` 中实现 Redis Pub/Sub 广播 | `internal/hub/hub.go` |
| 后台 goroutine 用于 Redis → 本地订阅者转发 | `internal/hub/hub.go` |
| 优雅降级（Redis 故障 → 仅本地） | `internal/hub/hub.go` |
| 向 config.yaml 添加 `redis` 配置块 | `internal/config/config.go` |
| 在 `app.go` 中连线 | `internal/app/app.go` |

**里程碑 2**：多实例 SSE 正确，完成 `Last-Event-ID` 重播。

**第 4-5 周：D1 — 配额管理**

| 任务 | 影响 |
|---|---|
| `domain.Quota` 实体 + `quota_repository.go` | 新文件 |
| `internal/quota/service.go` — `Check(ctx, uid, type, delta) error` | 新文件 |
| 将同步 `quotaService.Check()` 调用插入 `file.Create`、`message.Send`、`comment.Create` | 3 次一行代码变更 |
| 出站事件用于异步配额计数更新（`quota.usage_updated`） | 新事件处理程序 |

**里程碑 3**：文件上传、消息发送和评论创建都受到配额限制，第一天为 0。

**第 6-7 周：D5 Phase 0 — 合规基础**

| 任务 | 影响 |
|---|---|
| proto 中新的 `DeleteAccountData` RPC | API 合约 |
| `internal/compliance/service.go` — `ExportAccountData` + `DeleteAccountData` | 新包 |
| SSO 集成：`DeleteAccountData` 的 gRPC 客户端或 webhook 接收器 | SSO + aero-id 双方 |
| `export_requests` 表 + 异步作业创建 | 新迁移 |

**里程碑 4**：SSO 可以从 aero-id 中删除用户数据（级联软删除）。导出请求排队等待处理。

**第 8-9 周：D2 Phase 0 — 病毒扫描**

| 任务 | 影响 |
|---|---|
| 向 `domain.File` 添加 `Status` 字段 + 迁移 | schema 变更 |
| `internal/pipeline/processor.go` — `Processor` 接口 + `Pipeline` | 新包 |
| `ClamAVProcessor`（Unix socket，流式扫描） | 新文件 |
| 注册 `"file.uploaded"` 出站事件处理程序 | app.go 变更 |
| 拒绝在 `FileService.Get`/`DownloadURL` 中下载隔离文件 | ACL 变更 |

**里程碑 5**：上传文件在 5 秒以内经过 ClamAV 扫描。检测到问题的文件被标记为隔离且不可下载。

**第 10 周+：P2（群聊、缩略图、转码）**

按反馈文档的建议推进，但仅在 P0-P1 完成后进行。

### 风险点和缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| 分叉迁移破坏现有功能 | 中 | 高 | 测试完全覆盖，蓝色/绿色部署 |
| Redis 不可用 → SSE 降级错误 | 低 | 中 | 如果 Redis 配置了但不可用，hub 优雅降级为仅本地传递 |
| 配额检查增加写路径延迟 | 中 | 低 | 在合并之前使用基准测试（`go test -bench`）。使用 `(uid, resource_type)` 上的行锁 |
| ClamAV 扫描飙升至请求超时以上 | 低 | 中 | 对流式上传使用单独的 `Config.Upload.MaxScanSize`；大文件标记为"待扫描" |
| 在 deadline 之前未完成分叉迁移 | 中 | 高 | 模块化方法：每个领域包可以逐步迁移。在完全迁移之前，`internal/service/` 可以作为适配器层保留 |

### 最终建议

1. **不相信"事实纠正"文档中关于领域包路径的声明**。运行 `go build ./...` 显示哪些实际上被导入。我刚刚运行的验证显示**没有领域包被连线**。
2. **在构建任何新功能之前优先解决分叉问题**。并行维护两个服务层是拖慢交付的乘法器。
3. **D3 Phase 0 应该是 P0，不是 P1**。丢失的 SSE 推送是一个功能正确性错误，而不是一个性能问题。
4. **不要为配额管理引入 Redis**。Postgres `INSERT ... ON CONFLICT DO UPDATE` 对于这个工作负载更快且更简单，没有序列化开销。
