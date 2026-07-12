现在我有足够的上下文。以下是基于项目的完整架构分析。

---

# aero-id 架构深度分析报告

> **角色**：资深架构师  
> **日期**：2026-07-12  
> **范围**：基于项目代码库、设计文档 (`docs/superpowers/specs/2026-05-22-aero-id-design.md`)、8 份架构审查 (`docs/REVIEW_*`)、及 80+ 份需求分析文档的综合审视  
> **方法**：代码结构验证 + 分层依赖分析 + 并发/数据流追踪 + 跨文档交叉比对

---

## 一、架构评估

### 1.1 当前架构优势

**分层契约清晰度（A级）**

```
handler → service → repository → GORM/PostgreSQL
                 ├→ storage (local/s3)
                 ├→ sso (gRPC + cache)
                 └→ outbox (relay → notif/webhook/log)
```

严格单向依赖，无跨层回跳。HTTP 和 gRPC handler 共享同一套 `service` 层——这是"双传输单逻辑"模式的最佳实践。`service` 层是所有业务逻辑的唯一聚合点，避免了逻辑在多个入口间复制。

**领域完整性**

| 域 | 交付质量 |
|---|---|
| 文件 | 流式 + S3 presigned 双上传、checksum 去重 + 原子引用计数、显式共享/撤销、GC 清理 |
| 消息 | 1:1 私信、Reactions、已读回执、编辑/软删、Block 防护 |
| 评论 | 两层嵌套（含 domain 层校验）、Like/Unlike 原子计数、举报→审核→隐藏 |
| 通知 | Outbox 驱动、SSE 实时推送 + `last_event_id` 重连回放、折叠去重、偏好设置、Webhook HMAC 签名 |
| 韧性 | SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning、幂等键、优雅关停 |

**可观测性基础设施完善**：RED 指标（每路由 HTTP + gRPC）、OpenTelemetry 追踪（跨 SSO 调用）、分依赖 readiness 探测。

### 1.2 核心架构债务（按严重度排序）

#### 债务 1：双套实现 —— 严重程度 **Critical**

`internal/app/app.go` 和 `internal/app/new.go` 导入并使用 **套件 A**（`internal/repository/` + `internal/service/` + `internal/handler/{http,grpc}/` 下的平铺文件）。但代码库同时存在 **套件 B**（`internal/file/`、`internal/comment/`、`internal/message/`、`internal/block/` 等按领域组织的独立包。包含完整的事务逻辑定义并重复定义了 GORM model 结构体。

- 套件 B **完全未被任何执行路径引用**，是 dead code
- 两套代码有各自的单元测试，但只有套件 A 被集成测试覆盖
- 任何新功能必须在套件 A 中实现，但套件 B 的存在会误导读入者
- `make check-file-sizes` 不检测死代码，但度量（如代码覆盖率）失真

> **判定**：这看起来是一次领域驱动重构的中途停止。当前状态（孤儿代码可编译但零执行路径）在 Go 项目中尤其危险——Go 允许包级未引用代码存在，编译不报错，但运行时完全不执行。

**行动方案（三选一）：**

| 方案 | 工作量 | 风险 | 建议 |
|---|---|---|---|
| **A：删除套件 B** | 低（~2h） | 🟢 零 | 如果团队确认无意完成重构，立即删除 |
| **B：完成重构** | 中（~2-3d） | 🟡 中等 | 重写 `app.go` 的装配逻辑，从套件 A 切换到套件 B，然后删除套件 A |
| **C：保持现状 + 记录** | 零 | 🔴 高 | 持续累积维护负担和技术债 |

**推荐方案 A**（短期）+ **方案 B**（中期路线图）。如果重构意图是真实的，方案 B 的收益（按领域组织的包结构在大型项目中显著提升可维护性）值得 2-3 天的投入。

#### 债务 2：SSE Hub 无容量管理 + 静默丢事件 —— 严重程度 **High**

`internal/hub/hub.go` 是实时通知的核心，但存在三个结构性问题：

1. **无 per-uid subscriber 上限**：一个恶意客户端可打开数百个 SSE 连接耗尽服务端内存
2. **channel buffer 硬编码固定 16**：`make(chan SSEEvent, 16)`——不可配置
3. **非阻塞 send 静默丢弃**：`default` 分支无计数器、无日志、无告警

这使得 SSE 推送的可靠性 **为零**。通知的可靠性完全依赖数据库行，但实时推送通道的交付完全无保证。

> **影响**：用户在线时收到 16 个推送后，第 17 个通知**永不抵达客户端**。用户刷新页面后能看到它（从 DB 加载），但如果长期不刷新，他永远不会知道有通知被丢弃。

**修复优先级**：P1。可在不改变接口的情况下添加：
- `maxSubscribersPerUID` 配置（默认 10）
- `droppedEventsTotal` 指标（Counter，按 uid 聚合或仅全局计数）
- Hub 暴露 `SubscriberCount()` 给 metrics

#### 债务 3：通知行无 TTL/无级联清理 —— 严重程度 **Medium**

`notifications` 表**永久累积**。无 `expires_at`、无 `deleted_at`、无 `PurgeBefore` 方法。同时，当消息或评论被删除时，指向它们的通知行变成孤儿——用户点击通知后跳到 404。

- 表行数线性增长，最终影响 `ListByRecipient` 查询性能
- 已读通知占表的大比例（典型系统 >95%）
- 不符合数据最小化原则

#### 债务 4：Idempotency Cleaner 未启动 —— 严重程度 **Medium**

`internal/idempotency/store.go` 的 `RunCleaner` 和 `purgeExpired` 完整实现，但 `app.go` 从未调用 `idemStore.RunCleaner()`。`idempotency_keys` 表的 `expires_at` 列形同虚设——过期行永不删除，在生产中长期运行时持续增长，影响 `INSERT ... ON CONFLICT` 性能。

**修复**：单行代码——在 `app.go` 中启动 cleaner + 注册 shutdown。

#### 债务 5：gRPC 端缺少 `RequestID` 拦截器 —— 严重程度 **Low**

HTTP 端有 `RequestID` 中间件，gRPC 端无对应拦截器。`access_log` 依赖 `request_id` 关联日志行——gRPC 请求的日志中无 `request_id`。跨 HTTP/gRPC 的请求追踪在日志层面断裂。

### 1.3 架构级风险（非债务，但需关注）

| 风险 | 触发条件 | 影响 |
|---|---|---|
| **Outbox relay 串行处理** | 单个慢 handler（如 webhook 超时 30s）阻塞同批次所有事件 | 延迟通知投递 |
| **通知重复** | `CreateNotification` 成功但 `MarkPublished` 失败 → 重放后重复行 | 用户收到重复通知 |
| **多实例限流失效** | 部署两个实例 → per-instance 内存限流器，总量扩容为 2x，无协调 | 限流效果减半 |
| **No `FOR UPDATE SKIP LOCKED`** | 多实例 outbox relay 同时拉取 → 重复处理 | 业务副作用 |
| **SSO 缓存无跨实例共享** | 多实例各持有独立内存缓存，TokenCache 60s TTL → 每个实例各自验证 | 增加 SSO 负载 |

---

## 二、扩展方向

基于项目当前状态和上述架构债务，识别出以下 5 个高价值扩展方向（**与现有 80+ 需求文档做交叉比对，确保不重复**）：

### 方向一：多实例一致性基础设施（P0）

**为什么需要**：当前架构中限流器（per-instance 内存令牌桶）、UserCache/TokenCache（per-instance 内存）、Outbox relay（无 `FOR UPDATE SKIP LOCKED`）三项关键基础设施在设计时假设了单实例。一旦部署多副本（无论是 K8s 水平扩展还是 HA 双活），这三项会同时出问题：限流效果减半、SSO 验证压力倍增、outbox 事件重复处理。

**核心挑战**：
- 外部队列 vs DB 轮询：outbox relay 是否迁移到 Redis Streams / PGQ / NATS？
- 共享缓存 vs 缩减 TTL：是否引入 Redis 做分布式缓存，还是简单缩减 TTL 让每个实例各自验证？
- 分布式限流：是否引入 Redis + sliding window，还是接受 per-instance 近似值？

**预期架构变更**：

```
当前：                  多实例目标：
┌─Instance1──┐         ┌─Instance1──┐ ┌─Instance2──┐
│ 限流(内存)  │         │ 限流(Redis) │ │ 限流(Redis) │
│ Cache(内存) │    →    │ Cache(Redis)│ │ Cache(Redis)│
│ Relay(DB轮询)│        │ Relay(PG SKIP LOCKED)  │
└─────────────┘         └────────────┘ └────────────┘
```

**对现有系统的影响**：
- `GormStore`（幂等键）本身是 DB 持久化，不需要改，多实例天然安全
- `TokenCache` 和 `UserCache` 需加上可选的 Redis 后端（或保持内存 + 缩减 TTL 至 15s）
- `RateLimiter` 需加 Redis 后端（`sliding window` + `INCR` + `EXPIRE`）
- Outbox relay 的 `FetchUnpublished` 需加 `FOR UPDATE SKIP LOCKED`（PG 9.5+ 支持）

**选项权衡**：

| 维度 | 仅改 DB 查询（最小改动） | 引入 Redis（合理投入） | 引入 Redis + Streams（全面重写） |
|---|---|---|---|
| 工作量 | ~2h | ~1d | ~3d |
| 可扩展性 | 有限（DB 轮询压力） | 好 | 最好 |
| 运维复杂度 | 不变 | +Redis 集群 | +Redis 集群 |
| 推荐 | 过渡方案 | ✅ **推荐** | 过度设计 |

### 方向二：死信队列运维面（P0）

**为什么需要**：当前 outbox relay 的 `dead_letter` 标记存在、`PurgeDeadLetters` 存在、重试逻辑存在——但没有任何 API 或 UI 可以**查看**死信、**重放**死信、或**丢弃**死信。运维人员完全不知道系统存在卡住的事件。

**核心挑战**：
- 权限模型：死信操作是否与 admin 角色系统统一？建议复用现有 `PermOutboxAdmin`（或在 `domain/outbox.go` 新增）
- 重放安全：重放一个已处理一半的死信事件是否幂等？

**预期架构变更**：
- 新增 `outbox.Repository.FetchDeadLetters(page)` 和 `outbox.Repository.ReplayEvent(id)`（或 `ResetToPending(id)`）
- 新增 HTTP `GET /v1/admin/dead-letters` + `POST /v1/admin/dead-letters/:id/replay` + `DELETE /v1/admin/dead-letters/:id`
- 新增 gRPC `AdminService` 的对等方法
- 新增 `ObserveOutboxDeadLetter` 指标（按 event_type 分类的 Counter）

**对现有系统的影响**：**极低**。全部是新增文件或方法，现有逻辑零更改。`PurgeDeadLetters` 已存在，只需补充查询和重放能力。

### 方向三：乐观并发控制（P1）

**为什么需要**：当前所有 `Update` 操作是无条件 `db.Updates()`——即使两个请求同时更新同一条记录，后一个覆盖前一个，无任何冲突检测。这在文件元数据更新、评论编辑等场景可能造成静默数据丢失。

**核心挑战**：
- 如何引入版本号而不破坏现有 API？建议用 `ETag` / `If-Match` header 做可选的乐观锁
- 是否需要迁移现有数据填充 `version`？Go 的 GORM 可以 `ALTER TABLE ... ADD COLUMN version BIGINT DEFAULT 1`，零停机

**预期架构变更**：
- 所有需要并发控制的实体增加 `version` 字段（`BIGINT NOT NULL DEFAULT 1`）
- Repository 层 `Update` 改为 `UPDATE ... SET ..., version = version + 1 WHERE id = ? AND version = ?`
- 服务层：`RowsAffected == 0` → `AppError.Conflict`
- HTTP handler 可选支持 `If-Match` / `If-Unmodified-Since` header

**对现有系统的影响**：**中等**。每个实体的 `Update` 方法需要改 WHERE 子句。但这是纯架构增强——所有现有客户端无需改动即可继续工作（不传版本号 = 不启用乐观锁）。

### 方向四：文件预处理管线（P2）

**为什么需要**：目前文件上传是"一把梭"——无论用户上传 4K 照片还是 100MB 视频，系统只存储原文件。无缩略图、无 EXIF 清理、无病毒扫描、无格式转换。对于社交类产品，缩略图是刚需。

**核心挑战**：
- 长时间操作（缩略图 5-500ms，视频处理数秒到数分钟）不能阻塞 outbox relay
- 需要独立的 worker 进程或 goroutine 池
- GC 交互风险：管线处理中如果未建立 `storage_ref`，GC 可能误删正在处理的 blob

**预期架构变更**：

```
Upload → outbox event (`file.uploaded`) → worker 进程
                                            ├→ 缩略图生成 → 存储缩略图 → 建立 ref
                                            ├→ EXIF 清理  → 重写 blob
                                            ├→ 病毒扫描  → 标记安全/危险
                                            └→ 更新 File.Meta 中的处理状态
```

**选项权衡**：

| 方案 | 复杂度 | 可靠性 | 推荐阶段 |
|---|---|---|---|
| 同一进程中 goroutine 池处理 | 低 | 低（进程重启丢失排队任务） | Phase 1（MVP） |
| 独立 worker 进程订阅 outbox | 中 | 高 | Phase 2（生产） |
| 引入 Redis Streams + 独立 worker | 高 | 最高 | Phase 3（大规模） |

**对现有系统的影响**：需要新增 `file_versions` / `file_thumbnails` 表 + `file_processing_jobs` 表。`storage.GC` 需跳过正在处理的 blob。

### 方向五：活动流 / 动态（Activity Feed）（P2）

**为什么需要**：现有通知系统是"谁对你做了什么"（你收到了回复），但缺少"你关注的人做了什么"（张三上传了新文件、李四评论了某文章）。这是社交产品的核心差异化能力。

**核心挑战**：
- **写放大与读性能的三角 tradeoff**：fan-out 写（每个关注者一份）vs 读时聚合（实时查询所有关注的 actor 的活动）vs 缓存策略
- 与现有通知系统的关系：活动流是通知的上游 / 通知是活动流的子集？

**预期架构变更**：
- 新增 `activity_entries` 表（actor_uid, verb, object_type, object_id, created_at）
- 新增 `/v1/feed` 端点返回关注者的活动聚合
- Follow 系统：新增 `follows` 表 + `POST /v1/follows` / `DELETE /v1/follows/:uid`

**方案选择**：

| 方案 | 写放大 | 读延迟 | 复杂度 | 推荐 |
|---|---|---|---|---|
| A：fan-out 写 | 关注者数倍写入 | O(1) | 高 | ❌ 不适合初期 |
| B：读时聚合 | 1x 写入 | O(N) N=关注数 | 低 | ✅ **推荐初期** |
| C：混合（B + 缓存） | 1x 写入 | O(cached) | 中 | ✅ 推荐中期 |

**对现有系统的影响**：**中低**。新表 + 新端点 + 新服务。通知系统的 outbox 事件可作为活动流的输入源（复用已有事件模型）。

---

## 三、接口设计建议

### 3.1 分层接口原则

当前架构的接口设计整体良好，但有三个可改进点：

| 当前设计 | 问题 | 改进建议 |
|---|---|---|
| `service` 层方法签名直接暴露 `context.Context` 和域模型 | 测试时需要构造完整的域模型 | 引入 `Request`/`Response` 对象模式（如 `SendMessageInput`），分离 handler 层与域模型的耦合 |
| `repository` 层接口定义在 `internal/repository/` 而非调用侧 | 变更一个域的影响范围扩散 | 按**接口隔离原则**（ISP），将接口定义移至消费方（service 包中定义 `MessageRepository` 接口，由 `repository` 包实现） |
| `FileStorage` 接口在 `internal/storage/`，实现有 2 个（local + s3） | 当前模式良好，但 `PresignGet` 在 local 实现中使用 `SIGNING_KEY` | 确认 `SIGNING_KEY` 不是 JWT 密钥，避免安全审计混淆 |

### 3.2 是否需要新的抽象层

**建议新增**：一个可选的 **并发控制抽象**（`ConcurrencyManager` 接口）：

```go
type ConcurrencyManager interface {
    // 乐观锁：实体更新时自动附加版本检测
    OptimisticLock(ctx context.Context, table string, id domain.ID, version int64) error
    // 分布式锁：用于跨实例的互斥操作
    Mutex(ctx context.Context, key string, ttl time.Duration) (UnlockFunc, error)
}
```

这对方向三（乐观并发控制）和方向一（多实例一致性）提供了统一入口。实现可按需切换：单实例用内存锁，多实例用 Redis。

**不建议引入**：**Command/Query 分离（CQRS）**或**事件溯源（Event Sourcing）**。项目当前规模不需要这些模式的复杂度。当前 `service` 层 + outbox 已经提供了足够的命令/事件分离。

### 3.3 向后兼容性策略

| 变更类型 | 策略 |
|---|---|
| 新增端点 | 无兼容性问题 |
| 新增字段到响应体 | 安全——JSON 序列化自动包含新字段，客户端忽略不识别的字段 |
| 新增字段到请求体 | 安全——`ShouldBindJSON` 忽略未提供的字段（零值） |
| 修改现有字段 | **禁止**——应弃用旧字段、引入新字段 |
| 新增 gRPC 方法 | 安全——gRPC 的向前兼容保证 |
| 修改 proto 字段号 | **禁止**——造成二进制不兼容 |

**特别风险**：当前 API 的 `PATCH /v1/files/:id` 使用的是无条件更新。如果引入乐观锁（方向三），必须保持现有行为不变——仅在客户端提供 `If-Match` header 时才启用冲突检测。

---

## 四、技术选型

### 4.1 是否需要引入新技术栈

| 方向 | 需要引入 | 选项 | 推荐 |
|---|---|---|---|
| 多实例一致性（方向一） | **需要** 分布式缓存 | Redis / KeyDB / Dragonfly | **Redis 7.x**（生态最成熟，运维工具丰富） |
| 文件预处理管线（方向四） | **需要** 工作队列 | Redis Streams / NATS / RabbitMQ / PGQ | **Redis Streams**（与 Redis 复用，无需额外依赖） |
| 推送通知（notif channel） | **需要** FCM/APNs 客户端 | `firebase.google.com/go`（admin SDK）/ 自建 HTTP v1 | **firebase-admin-go**（Google 官方维护，支持 FCM + APNs） |
| 活动流（方向五） | **无需** 新基础设施 | 全部基于 PG + 应用层缓存 | — |

**核心原则**：**每引入一个外部依赖，必须回答"为什么 GORM/PG 不够用？"**

- 在当前项目规模（单 Postgres 实例）下，`FOR UPDATE SKIP LOCKED` + 内存缓存足够支撑到每日数百万事件级别
- 引入 Redis 的最低阈值：**多实例部署 + 每日事件量 > 50 万** 或 **需要跨实例共享状态**
- 引入消息队列的最低阈值：**每日事件量 > 200 万** 或 **需要异步 worker 处理长时间任务**

### 4.2 第三方依赖评估标准

对于未来所有新增依赖，建议使用以下 checklist：

| 维度 | 通过条件 |
|---|---|
| **维护状态** | GitHub 最近一次提交 < 6 个月，stars > 500，issue 回复率 > 80% |
| **许可证** | Apache 2.0 / MIT / BSD |
| **Go 版本兼容** | `go.mod` 要求 Go ≤ 项目的 1.26 |
| **API 稳定性** | 已有 v1 稳定版 release，无 breaking change 预期 |
| **无 CGO** | 纯 Go 实现（CI 环境无需 cgo 工具链） |
| **无额外运行时** | 不要求 JVM / Python / Node 等运行时 |
| **测试覆盖率** | 项目自身有 CI 和 > 70% 测试覆盖率 |

### 4.3 自建 vs 采购决策依据

| 组件 | 自建理由 | 采购/引入理由 | 决策 |
|---|---|---|---|
| 缩略图生成 | — | `imaging` 库（Go 纯实现）或 `libvips`（CGO） | 引入 `github.com/disintegration/imaging`（纯 Go，无 CGO，支持 resize/crop/rotate） |
| 病毒扫描 | 在文件管线中集成 `clamav` | 无商业需求 | 自建：输出 `clamd` socket 扫描结果到 `File.Meta` 的 `scan_status` 字段 |
| 活动流 | 是自己产品差异化的核心能力 | 有 `Stream`、`GetStream.io` 等 PaaS | **自建**（数据主权 + 无额外成本） |
| 推送通知 | — | FCM/APNs 是事实标准 | 引入 firebase-admin SDK（Google 官方，免费） |

---

## 五、实施路线图

### 5.1 优先级矩阵

| # | 方向 | 优先级 | 前置依赖 | 工作量估计 | 风险 | 收益 |
|---|---|---|---|---|---|---|
| **D0** | 清理双套实现死代码 | **P0** | 无 | ~2-3h | 🟢 低 | 减少误导 + 编译时间缩短 |
| **D1** | 多实例一致性基础设施 | **P0** | D0 | ~2-3d | 🟡 中 | 使 HA 部署可行 |
| **D2** | 死信队列运维面 | **P0** | 无 | ~4-6h | 🟢 低 | 运维必备，快速修复 |
| **D3** | 乐观并发控制 | **P1** | D0 | ~2d | 🟡 中 | 防止静默数据丢失 |
| **D4** | 文件预处理管线 | **P2** | D1（Redis） | ~5-7d | 🟠 中高 | 用户体验提升 + 安全合规 |
| **D5** | 活动流 / 动态 | **P2** | D4（基础功能成熟度） | ~3-5d | 🟡 中 | 产品差异化能力 |

### 5.2 阶段划分

#### Phase 0（立即执行，1-2 天）：止血

```
Week 1
├── [D0] 删除套件 B 死代码（或标记废弃）
├── [D2] 死信查询/重放 API（HTTP + gRPC）
├── [D2] 死信指标 `ObserveOutboxDeadLetter`
├── [D1·微] Idempotency cleaner 启动（一行代码）
├── [D3·微] SSE Hub `droppedEventsTotal` + `maxSubscribersPerUID` 配置
└── [D1·微] gRPC `RequestID` 拦截器
```

**里程碑**：`make test && make lint` 全部通过。运维团队可通过 API 查看和重放死信。

#### Phase 1（2-3 周）：多实例就绪

```
Week 2-3
├── [D1] Outbox relay → `FOR UPDATE SKIP LOCKED`
├── [D1] RateLimiter → Redis 后端（可选，按部署拓扑）
├── [D1] TokenCache/UserCache → 缩减 TTL 或可选的 Redis 后端
├── [D3] 实体乐观锁：version 字段 + 条件 Update
├── [D3] HTTP `If-Match` / gRPC `version` 字段
├── [D3] `Conflict` AppError + handler 渲染
└── [D1] 集成测试：多实例并发场景
```

**里程碑**：服务可在多副本 K8s 部署下正确工作。限流、缓存、outbox 三者在多实例下行为正确。

#### Phase 2（3-6 周）：内容增强

```
Week 4-6
├── [D4] `file_thumbnails` / `file_versions` schema
├── [D4] 缩略图生成 pipeline（imaging 库）
├── [D4] 缩略图存储 + ref 管理（避开 GC 误删）
├── [D4] EXIF 清理 pipeline（exif 库）
├── [D4] ClamAV 病毒扫描集成（可选，按部署需求）
├── [D4] `GET /v1/files/:id/thumbnail` 端点
└── [D1] Redis 基础设施正式上线
```

**里程碑**：上传文件自动生成缩略图，EXIF 清除，毒病扫描标注。

#### Phase 3（4-8 周）：社交化

```
Week 7-10
├── [D5] `follows` 表 + Follow/Unfollow API
├── [D5] `activity_entries` 表
├── [D5] 写路径：文件上传、评论、消息发信时写入活动（复用 outbox）
├── [D5] 读路径：`GET /v1/feed` → 聚合关注者的活动
├── [D5] 活动缓存（60s TTL）
├── [D5] `DELETE /v1/notifications`（通知清理 API）
└── [D5] 通知 TTL GC（后台 goroutine，可配置 TTL）
```

**里程碑**：用户可关注他人、查看动态流、清理通知历史。

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| **延迟/性能衰减**：Redis 引入增加 P99 延迟 | 中 | 中 | 所有 Redis 调用必须有 fallback（内存兜底），P99 增量不应超过 2ms |
| **outbox 重复处理**：`FOR UPDATE SKIP LOCKED` 未正确实现 | 低 | 高 | 增加幂等键验证：outbox 事件重放应有去重 |
| **scope creep**：活动流变成"类 Twitter"产品 | 高 | 高 | **严格限定 MVP scope**：仅记录文件上传 + 评论 + 消息，不支持转发/引用/标注 |
| **CGO 依赖**：`libvips` 缩略图需要 CGO | 中 | 中 | 选用纯 Go 的 `imaging` 库，放弃 `libvips` 的高性能优势 |
| **Redis 运维复杂性**：团队无 Redis 运维经验 | 中 | 中 | 使用托管 Redis（如 ElastiCache / Upstash），或 Phase 0 先用 PG 的 `FOR UPDATE SKIP LOCKED` 不引入 Redis |

### 5.4 不做事项（明确 Out of Scope）

- **不引入 CQRS / Event Sourcing**：当前 outbox + service 层模式够用，ES 增加 3x 架构复杂度
- **不引入 GraphQL**：双传输（REST + gRPC）已满足所有调用方需求
- **不重构到微服务**：当前单进程足够支撑到日活 100 万级别
- **不引入 K8s Operator**：部署复杂度不应超过服务的业务复杂度
- **不做大版本 v2 分叉**：所有扩展应向后兼容，v1 API 持续工作

---

## 总结

aero-id 当前正处于 **从 MVP 成熟迈向生产级平台** 的转折点。架构基础非常扎实（分层干净、可观测性好、韧性模式完善），但存在一个**最高优先级的架构债务**（双套实现死代码）和几项**阻塞多实例部署的基础设施缺口**。

建议的路线图核心信号：

1. **第一周**：止血——清死代码 + 死信运维面 + 最低成本修补（idempotency cleaner, hub metrics）
2. **2-3 周**：多实例就绪——让系统可以在 K8s 下正确水平扩展
3. **之后**：按业务优先级逐步投入内容管线（缩略图）和社交化（活动流）

关键决策点：**是否引入 Redis**。这个决策决定了 Phase 1 到 Phase 4 的架构走向。建议**在首次需要多实例部署时正式引入 Redis**，在此之前用 `FOR UPDATE SKIP LOCKED` 和缩减 TTL 过渡。
