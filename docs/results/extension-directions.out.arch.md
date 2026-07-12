# 架构分析报告：aero-id 下一阶段扩展

> **角色**：资深架构师  
> **基线**：基于 `docs/requirements/2026-07-12-aero-id-extension-analysis-pm-architect.md` 的 5 个方向分析  
> **范围**：纯架构层面设计建议，不包含实现代码

---

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 当前架构体现了经过深思熟虑的设计决策，值得肯定的方面包括：

| 维度 | 优势 |
|------|------|
| **分层清晰** | `handler → service → repository → DB` 的单向依赖严格执行。每个层的职责单一，没有"service 直接操作 DB"或"handler 包含业务逻辑"的违规。这是项目中最重要的架构纪律。 |
| **S3 presigned 上传** | 选择 presigned URL 直传而非服务端代理上传，是在**吞吐量**与**控制力**之间做出的良好权衡。上传路径 0 服务端带宽消耗，直接利用 S3 的基础设施扩展性。 |
| **事务性 Outbox** | 这是一个经常被低估的模式。在消息/通知/评论事务中使用 outbox 保证了**事件可靠性**而不引入 2PC 或分布式事务。当前实现已经是行业标准模式。 |
| **双传输对称** | HTTP 和 gRPC 共享 `service` 层确保行为一致，消除了"HTTP 和 gRPC 行为不同"这类难以调试的 bug 来源。 |
| **断路器 + 服务降级** | SSO 调用有断路器 + stale cache 降级，这比大多数初创项目更早引入了弹性模式。 |

### 1.2 核心设计决策的合理性评估

我对几个关键决策的评估：

**决策 1：纯 1:1 消息模型（无 Conversation 实体）**

短期合理，长期不可持续。当前 `(from_uid, to_uid)` 的消息模型在代码层面非常简洁，`ListConversation` 的 OR 查询在数据量小 (< 100 万条消息) 时性能可接受。但一旦引入群聊，这个模型要么需要全表重写，要么需要用 `conversation_id` 做桥接转换。**这个决策已经产生了约 3-6 个月的技术债窗口**——如果再不做，数据迁移成本线性增长。

**决策 2：In-memory 速率限制**

对于单实例部署来说是完全合理的。但项目的代码和配置已经暗示了多副本部署（断路器、健康检查探针、OTel），而速率限制却停留在单实例级别，这是一个**架构不匹配**。在生产级部署中，这个决策使得速率限制形同虚设。

**决策 3：消息附件采用 `file_id` 直接引用而非快照**

这是一个有争议的决策。在文件服务中，资源所有权属于上传者，这是正确的。但在消息场景中，接收方应当拥有"看到这条消息时附件是什么样的"的确定性——即使发件人后续删除了文件。当前设计将文件生命周期和消息生命周期耦合，违反了**业务边界分离**原则。

### 1.3 架构债务与技术债务

按严重程度排序：

| 类型 | 债务项 | 严重程度 | 修复窗口 |
|------|--------|----------|----------|
| **架构债务** | 消息模型缺乏 Conversation 抽象 | 高 | 6-12 个月 | 
| **架构债务** | 速率限制单实例 vs 多副本部署 | 高 | 当前 | 
| **架构债务** | 附件生命周期无快照语义 | 中 | 3-6 个月 |
| **技术债务** | Presigned 上传无校验 | 高 | 当前 |
| **技术债务** | 用户设备/会话无注册机制 | 中 | 3-6 个月 |
| **技术债务** | 无分布式限流的告警/可观测性 | 低-中 | 6 个月+ |

---

## 2. 扩展方向

基于输入文档的 5 个方向，我从架构层面提出更广泛的评估，并额外补充 2 个我认为在架构层面需要提前考虑的方向。

### 2.1 🚨 方向 A：文件完整性 + 端到端校验链

**业务价值**：文件服务最核心的数据正确性保证。Presigned 上传是目前唯一没有服务端校验的路径。对于一个存储用户数据的系统，这是一个合规风险（如果用户上传的合同/凭证被静默损坏，责任归属问题）。

**技术难点**：
- **Multipart upload 的 ETag 不可靠**：S3 的 multipart ETag 不是文件内容的纯 MD5，而是一个复合值（各部分 MD5 拼接后再 MD5）。不能直接用 ETag 做文件校验。
- **异步校验的时序问题**：如果使用 worker 异步校验，`CompleteUpload` 立即返回 200，但几秒后校验失败——这时用户已经认为上传成功，需要额外机制处理这种"延迟失败"。
- **校验 + 去重的竞态**：两个用户同时上传同一文件，异步 worker 可能同时开始校验同一 blob，导致重复计算。

**架构变更**：

```
当前：Client → PUT to S3 → CompleteUpload (无校验)
建议：Client → PUT to S3 → CompleteUpload (记录 pending_checksum) → 
      Checksum Worker → 匹配 → 更新文件状态为 verified + 去重
                          → 不匹配 → 标记文件为 corrupted + 通知用户
```

**选项分析**：

| 选项 | 方案 | 复杂度 | 用户体验 | 推荐 |
|------|------|--------|----------|------|
| Option A | 同步验证：Client PUT 后服务端 GET 全部内容算 checksum | 低（实现简单） | 上传后等待，大文件不可接受 | ❌ |
| Option B | 异步验证：Worker 从 S3 下载后校验 | 中（需 worker 队列） | 立即返回，延迟验证结果 | ✅ 大型 |
| Option C | 混合验证：小文件同步校验，大文件异步 | 高 | 最优但实现复杂 | ⏳ 第二阶段 |

---

### 2.2 📱 方向 B：跨设备状态同步 + 设备注册

**业务价值**：这是从"可用"到"好用"的门槛能力。推送通知（Push Notification）是移动端产品的**必备基础设施**——没有设备注册就意味着无法推送。同时，已读状态跨设备同步直接影响用户对产品"同步感"的感知。

**技术难点**：
- **Push Notification 第三方依赖**：FCM/APNs 需要外部服务依赖，增加了系统的外部耦合点。且 APNs 仅在中国大陆外的 Apple 设备可用，中国大陆需要特殊处理（比如通过厂商推送通道）。
- **SSE + Push 的双通道一致性**：SSE 在线时通过 SSE 推送，离线后用 Push 推送。但如何避免用户同时收到在线推送和 Push 通知导致重复？需要在 outbox 中标记 delivery_method。
- **Push Token 刷新**：APNs/FCM token 会定期刷新（尤其是 APNs 的静默刷新），需要设备保活机制。

**架构变更**：

```
新增基础设施：
- user_devices 表（设备注册）
- push 模块（FCM/APNs 客户端）
- outbox 新增 pushHandler worker

现有复用地：
- SSE Hub（跨设备已读同步事件的即时推送）
- Outbox 的 at-least-once 保证（Push 通知的可靠性）
```

**关键决策**：

对于 Push，我建议**初期不接受"在中国大陆使用 APNs/FCM 推送"的场景**，保持在**海外 Google Play/iOS 网络正常环境**下工作。中国大陆的厂商通道推送（华为/小米/OPPO/Vivo）需要大量适配工作，应作为 P2+ 项。

---

### 2.3 ⛓️ 方向 C：附件生命周期+缩略图管道

**业务价值**：这是文件服务与消息/评论服务之间的**契约完整性**问题。当前的悬挂引用问题本质上是因为 `file_id` 跨服务引用没有引用计数或快照机制。此外，缩略图是消息/评论场景中不可跳过的体验需求。

**技术难点**：
- **快照一致性**：发送消息时复制文件元数据到 `message_files`，但如果在复制过程中文件被删除，事务会失败（Racing condition）。需要事务隔离或重试。
- **缩略图存储策略**：缩略图应存储在 S3 同区域、同 bucket（不同前缀）还是不同 bucket？同 bucket + 前缀分离最简单，但需要确保 storage GC 不独立删除缩略图。
- **格式覆盖**：缩略图生成需要对所有允许的图片格式（JPEG/PNG/WebP/GIF）有统一的处理库。GIF 的缩略图需要额外决定是取第一帧还是生成静态图。

**架构变更**：

```
新增：
- message_files 表（快照存储）
- thumbnail 模块（图片处理）
- 可选的 async thumbnail worker

修改：
- MessageService.SendMessage：在事务中创建消息记录 + 快照记录
- FileService.Delete：检查文件是否被消息引用，决定是否允许删除
- Storage GC：感知缩略图与源文件的关联关系
```

**架构选项**：

| 决策点 | 选项 A | 选项 B |
|--------|--------|--------|
| 缩略图生成时机 | 同步：CompleteUpload 时生成 | 异步：Worker 生成 |
| 缩略图存储 | 同 bucket，`thumbnails/{storage_key}` | 独立 `thumbnails` bucket |
| 文件删除行为 | 不允许（返回 409） | 允许但标记 message 为 "附件已删除" |

我的建议是：**Option B (异步) + 同 bucket 前缀隔离 + 允许删除但优雅降级**。同步生成会阻塞上传路径，同 bucket 简化 GC 逻辑，优雅降级给用户更好的体验。

---

### 2.4 🛡️ 方向 D：分布式速率限制 + 滥用防护

**业务价值**：这是**生产级部署的门槛能力**。当前的 in-memory 速率限制在多副本环境下有严重的绕行问题。同时，社交功能（举报/like/消息发送）在缺乏滥用防护时，会成为 spam 和攻击的目标。

**技术难点**：
- **Redis 作为新依赖**：项目目前 0 外部依赖（除 Postgres + S3/MinIO），引入 Redis 意味着运维复杂度增加 + 多了一个故障点。
- **算法选择**：滑动窗口 vs. GCRA vs. Token Bucket——各有利弊。GCRA (Generic Cell Rate Algorithm) 在大规模分布式限流中表现最好（内存效率高、平滑性好），但实现复杂度高。Token Bucket 最直观，但突发流量处理不如 GCRA 平滑。
- **Per-endpoint 配置管理**：随着 endpoint 数量增长，配置会变得庞大，需要良好的组织方式。

**架构变更**：

```
新增基础设施：
- redis 包（Redis 客户端抽象）
- redisLimiter 实现 ratelimit.Limiter 接口

配置变更：
type: memory | redis
redis:
  addr: localhost:6379
  fallback_to_memory: true
endpoints:
  "POST /v1/comments/*/report":
    rps: 2
    burst: 5

保持现有接口不变量：
ratelimit.Limiter 接口不变（新增实现类）
```

**关于 Redis 依赖的权衡**：

| 考量 | 评价 |
|------|------|
| **运维成本** | Redis 是成熟中间件，几乎标配于此类系统 |
| **替代方案** | 可以用 Postgres 的 `advisory lock` + 内存状态做分布式限流，但性能远不如 Redis |
| **单点故障** | 通过 `fallback_to_memory` 降级策略缓解 |
| **现有熟悉度** | 团队已用 Postgres + MinIO + SSO gRPC，Redis 是合理的新增依赖 |

**结论**：引入 Redis 是值得的——不仅解决当前限流问题，还为后续功能（Push Notification 消息队列、分布式锁、缓存、实时排行榜）提供了基础。评估后推荐引入。

---

### 2.5 🧵 方向 E：Conversation 抽象 + 群组消息

**业务价值**：这是**产品演进的分水岭**。1:1 消息模型支撑不了群聊。群聊是消息产品最常见的需求之一，也是用户粘性的重要来源。但实现群聊意味着消息模型需要从"记录级"变为"会话级"。

**技术难点**：
- **数据迁移**：现有百万级消息需要从 `(from_uid, to_uid)` 模型迁移到 `conversation_id` 模型。迁移过程中需要保持现有 API 可用（旧 `ListConversation` 需要兼容）。
- **写扩散 vs. 读扩散**：群聊消息的 fan-out 策略是核心架构决策：
  - **读扩散**（每条消息写一次，每个成员的已读位置不同）。适合大群，但 `ListMessages` 需要 `conversation_id + read_cursor` 过滤。
  - **写扩散**（给每个成员写一条消息记录）。适合小群 (< 50)，存储膨胀但查询语义简洁。
  - 当前代码 `Message` 有 `from_uid`/`to_uid`，写扩散需要 N 条记录，读扩散需要重构查询逻辑。
- **向后兼容**：现有 HTTP 和 gRPC 的 `ListConversation`/`Conversation` RPC 已有用户使用，需要标记为 deprecated 但保持可用。

**架构变更**：

```
新增表：
- conversations（会话实体）
- conversation_members（成员关系）
- conversation_read_cursors（已读指针）

修改表：
- messages：新增 conversation_id（FK），保留 from_uid，to_uid 变为 NULLABLE

新增 API：
- POST /v1/conversations（创建）
- GET /v1/conversations（列表摘要）
- PATCH /v1/conversations/{id}（更新）
- POST /v1/conversations/{id}/members（添加成员）
- DELETE /v1/conversations/{id}/members/{uid}（移除成员）

数据迁移策略：
1. 新建 conversation + 相关表
2. 为现有每一对 (min_uid, max_uid) 创建 1:1 conversation
3. 将现有消息的 conversation_id 更新为对应值
4. 逐步将流量切到新 API，保留旧 API 作为 shim
```

**读扩散 vs. 写扩散的架构决策**：

| 维度 | 读扩散 | 写扩散 |
|------|--------|--------|
| 存储量 | O(1) 每条消息 | O(N) 每条消息 (N=成员数) |
| 读取复杂度 | 需要 cursor 过滤 | 简单 WHERE uid = ? |
| 群大小上限 | 无理论上限 | 建议 ≤ 100 人 |
| 消息撤回 | 修改一条记录 | 修改 N 条记录 |
| 兼容现有 `Inbox` | 需重写查询 | 可沿用（按 uid 查） |

**我的建议**：**读扩散 + 写扩散混合**——对于 ≤ 50 人的群采用写扩散（兼容现有 Inbox 查询），对于大群采用读扩散。但目前项目尚未有"大群"的业务需求，建议初期全部使用**读扩散**（架构简洁，为未来扩展留空间），Inbox 查询新增 `conversation_id` 维度的过滤路径。

---

### 2.6 🆕 补充方向 F：事件驱动的异步架构标准化

这是一个我在评估中发现的**架构层面需要提前考虑的横向问题**。

**现状**：当前有多个"异步处理"的迹象但未形成模式：
- Storage GC 是独立 goroutine（没有使用统一的 worker 框架）
- Outbox 使用事务性 outbox 模式，但 event relay 使用的是简单的 goroutine polling（not channel/queue）
- 缩略图生成如果引入，将是第三个异步 worker

**问题**：未来会有更多异步场景（Push Notification、Checksum worker、缩略图生成、数据迁移、定时清理），如果没有统一的异步 worker 框架，每个 worker 各自实现，将导致：
- 可观测性不统一（日志格式、追踪上下文、健康检查）
- 错误处理不一致（重试策略、死信队列）
- 资源管理混乱（每个 worker 占用 goroutine，无法集中控制并发度）

**建议**：在引入任何新的异步 worker 之前，先定义并实现一个轻量级的 **Worker 抽象**：

```go
// 架构层面的抽象（非代码）
type Worker interface {
    Name() string              // for observability
    Process(ctx context.Context, job Job) error
    Concurrency() int          // max concurrent goroutines
    BackoffPolicy() Backoff    // retry strategy
}

// 统一注册
type WorkerRegistry struct {
    workers map[string]Worker
    pool    *semaphore.Weighted  // 全局并发控制
}
```

这不需要引入外部框架（如 RabbitMQ/Kafka）——在当前阶段，goroutine + semaphore + 统一日志即可。但模式要统一。

---

### 2.7 🆕 补充方向 G：数据层水平扩展准备

**现状**：当前所有查询基于 GORM + UUIDv7（天然时间有序），PostgreSQL 压力集中在单实例。目前阶段单实例 Postgres 完全足够，但需要**在架构层面为水平扩展预留插槽**。

**具体行动**（非立即执行，但需在设计时考虑）：

1. **读写分离插槽**：repository 层已经良好隔离，将来只需要替换 `*gorm.DB` 为读副本连接池（CQRS 的最简单形式）。当前不需要实现，但需要在 `internal/pkg/db` 中保留一个"读连接"的概念，允许未来直接注入。
2. **数据分片键决策**：如果未来需要分片（shard），应以 `uid` 或 `owner_uid` 为分片键——大多数查询以用户维度展开（用户的文件、消息、评论、通知）。`conversation_id` 不适合作为分片键。这个决策影响表设计。
3. **大表归档策略**：`messages`、`notifications`、`comments` 是增长最快的表。建议在设计阶段就在 `config` 中预留归档策略配置（`archive_after_days： 365`）。当前不需要实现归档，但 `Delete` 路径应当预留"硬删除+归档"的扩展点。

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

对于即将新增的模块，我提出以下接口设计原则：

**原则 1：接口 = 契约，而非实现细节**

当前代码中，`ratelimit.Limiter` 的接口设计是好的——它隐藏了"in-memory"还是"Redis"的实现细节。新增所有模块的对外接口都应遵循这一原则：

```
// ✅ 好的：抽象不暴露具体中间件
type FileValidator interface {
    VerifyChecksum(ctx context.Context, storageKey string, expected string) (bool, error)
}

// ❌ 差的：接口泄露实现细节
type S3ChecksumValidator struct { ... } // 命名暴露了 S3
```

**原则 2：跨服务调用通过 service 层，不直接调 repository**

这是当前架构的基石。消息附件快照的创建应当在 `MessageService` 中协调：
- `MessageService.SendMessage` 调用 `FileRepository` 读文件元数据
- `MessageService` 通过 `MessageFileRepository` 创建快照
- 不引入 `FileService.Snapshot()`（避免循环依赖）

**原则 3：配置驱动的功能开关**

对于分布式限流、缩略图生成、Push Notification 等功能，都应在配置层面有显式开关：
```yaml
features:
  distributed_rate_limiting: false  # opt-in
  thumbnail_generation: true
  push_notification:
    enabled: false
    provider: fcm
```

这允许在非生产环境中轻量运行（不需要 Redis、FCM 凭据），降低开发环境的复杂度。

### 3.2 是否需要引入新的抽象层

| 新增模块 | 是否需要新的抽象层 | 理由 |
|----------|--------------------|------|
| 分布式速率限制 | ❌ 不需要 | 现有 `ratelimit.Limiter` 接口可复用，只需新增 `redisLimiter` 实现 |
| 文件校验 | ✅ 需要 | `FileVerifier` 接口，包含同步校验和异步校验两种实现 |
| 设备注册 | ❌ 不需要 | 跟随现有 repository/service/handler 三层模式即可 |
| Push Notification | ✅ 需要 | `PushProvider` 接口，支持 FCM/APNs/将来厂商推送的切换 |
| 缩略图生成 | ⚠️ 可选 | 只有在需要支持多种格式/尺寸时才需要抽象；初期可直接在 `CompleteUpload` 路径中处理 |
| Worker 框架 | ✅ 强烈建议 | 第 2.6 节已详述，这是横切关注点 |

### 3.3 向后兼容性策略

对于可能破坏现有 API 的变更（尤其是 Direction E - Conversation 抽象）：

**HTTP API 兼容：**

```
阶段 1：API 共存
  旧 API（/v1/inbox, /v1/conversation）继续工作
  新 API（/v1/conversations, /v1/messages?conversation_id=）新增
  旧 API 内部转换：Inbox 查询变为 "获取所有用户的 conversation 的最新消息"

阶段 2：旧 API 降级
  旧 API 添加 X-Deprecated 响应头
  新 API 成为默认推荐

阶段 3：旧 API 下架（至少等待 6 个月）
  在 CHANGELOG 中明确标记 breaking change 窗口
```

**gRPC API 兼容：**

Protobuf 3 的字段级兼容性已经通过 `field = 1, 2, 3...` 的编号保证。关键策略：
- `Message.conversation_id` 新增为 `optional` 字段，前端根据版本选择发送旧或新格式
- `Conversation` RPC 新增在独立的 service 中，不影响现有 `MessageService`
- 现有 `ListConversation` RPC 保留 but deprecated (使用 `google.protobuf.StringValue` 标记)

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

| 模块 | 推荐技术选型 | 引入理由 | 风险评价 |
|------|-------------|----------|----------|
| 分布式限流 | **Redis** (go-redis/redigo) | 分布式计数器、滑动窗口 #1 选择 | 低风险，社区成熟 |
| Push Notification | **firebase-admin-go** (FCM) + 可选 **apns2** (APNs) | 官方 SDK，稳定可靠 | 低风险，外部依赖 |
| 缩略图生成 | **Go standard library** (`image/jpeg`, `image/png`, `golang.org/x/image`) | 0 第三方依赖，性能足够 | 极低风险 |
| 异步 Worker 框架 | **自研轻量** (goroutine + semaphore + 统一接口) | 不需要重型消息队列 | — |
| 文件校验 | **S3 SDK** (HEAD + range GET) | 已在 go.mod 中 | 0 新增依赖 |
| 数据迁移 | **Go 脚本 + GORM** | 无需外部迁移工具 | — |

**关键评估**：除 Redis 外，其他扩展方向不需要引入新的外部基础软件。Redis 本身也是行业标配，风险可控。

### 4.2 第三方依赖的评估标准

对于将来可能引入的依赖，建议采用以下评估框架：

```
1. 必要性  — 是否有纯 Go 标准库替代方案？如果有，标准库优先。
2. 活跃度  — GitHub stars > 1k，最近 3 个月有提交
3. Go 版本兼容 — module 声明的 go 版本 ≤ go.mod 中的 1.26
4. 许可证  — Apache 2.0 / MIT / BSD（避免 GPL 传染性）
5. API 稳定性 — 是否 v1+，没有频繁的 breaking change
6. 依赖树大小 — go.sum 的行数增长 < 100 行
```

### 4.3 自研 vs. 采购的决策依据

在 aero-id 当前阶段（中小规模，核心业务差异化），我推荐以下决策框架：

| 场景 | 自研 | 采购/集成 | 依据 |
|------|------|-----------|------|
| Push Notification | — | ✅ FCM SDK | Push 通道是标准化接口，自研划不来 |
| 缩略图生成 | ✅ 标准库即可 | — | 需求简单，标准库足够 |  
| 分布式限流 | ✅ 基于 Redis 的轻量实现 | — | 业务逻辑简单，自研 200 行即可 |
| 内容审核/Spam检测 | ❌ 不推荐 | ✅ 考虑集成第三方 API (如 Perspective API) | Spam 检测是 AI/ML 领域，非团队核心能力 |
| 对话/群聊 | ✅ 必须自研 | — | 核心业务逻辑，差异化所在 |

**特别说明**：Spam 检测（输入文档提及但未深挖）我建议初期不做，或仅做最简单的关键词/正则过滤。一旦需要 ML 级的垃圾识别，应该采购 API 而非自研——这是典型的非核心竞争力。

---

## 5. 实施路线图

### 5.1 优先级排序（P0/P1/P2）

基于 **"降低风险 → 提升体验 → 扩展功能"** 的主线，结合每个方向的**依赖关系**：

```
P0: 分布式速率限制 (#4)  +  文件完整性校验 (#1)
  │         │
  │         └──→ 都为"数据正确性+系统韧性"——生产环境不可妥协
  │
  ├── P1: 消息附件生命周期 (#3)  +  多设备同步 (#2)
  │         │
  │         └──→ 提升产品体验；可并行进行
  │
  └── P2: 对话抽象+群聊 (#5)
             │
             └──→ 纯新功能，依赖前面打好基础
```

### 5.2 阶段划分和里程碑

| 阶段 | 时间估算 | 包含方向 | 关键交付物 | 外部依赖 |
|------|---------|----------|-----------|----------|
| **Phase 1 🛡️** 系统加固 | 2-3 周 | P0: #4 分布式限流 + #1 文件校验 | 1. `redisLimiter` 实现 + 配置切换<br>2. `ChecksumWorker` 异步校验<br>3. per-endpoint 限流配置<br>4. 启动时 Redis 健康检查纳入 `/readyz` | Redis (dev: Docker compose; prod: 托管实例) |
| **Phase 2 📱** 体验提升 | 3-4 周 | P1: #3 附件生命周期 + #2 跨设备同步 | 1. `message_files` 快照表<br>2. 文件删除的引用检查<br>3. `user_devices` 注册 API<br>4. 已读状态跨设备同步（SSE）<br>5. Push worker（可选，取决于 Push 需求优先级） | FCM 开发者账号（可选） |
| **Phase 3 🧵** 功能扩展 | 4-6 周 | P2: #5 Conversation 抽象 | 1. 数据模型变更迁移<br>2. 新旧 API 双写 + 兼容层<br>3. 群聊 API<br>4. 旧 API 逐步淘汰 | 无 |
| **Phase 4 🔧** 架构标准化 | 持续 | #6 Worker 抽象 + #7 水平扩展预留 | 1. Worker 接口统一<br>2. 所有异步任务迁移到统一框架<br>3. 数据归档策略基础实现 | 无 |

### 5.3 风险点和缓解策略

| 风险 | 影响 | 概率 | 缓解策略 |
|------|------|------|----------|
| **Redis 成为新的故障点** | 限流失效，全链路容错降低 | 中 | `fallback_to_memory` 降级；`/readyz` 监控 Redis 健康；告警阈值 |
| **数据迁移（#5）导致数据不一致** | 百万级消息迁移出错 | 中高 | 分批次迁移（每批 1000 条）；迁移前后 checksum 校验；灰度切流 |
| **Push Notification 投递失败** | 用户收不到推送，体验下降 | 高 | Outbox 保证 at-least-once；Push worker 重试 + dead letter；客户端轮询作为 fallback |
| **FCM 在中国大陆不可用** | 中国大陆用户无推送 | 中 | 设计时选择 Push 接口，使后续可替换为厂商通道；当前阶段不承诺中国大陆推送 |
| **Presigned 校验增加上传延迟** | 大文件校验等待时间长 | 低 | 采用异步校验（Phase 1），同步返回 success，校验失败后通知回滚 |
| **Conversation 迁移破坏现有 API** | 客户端/用户报错 | 中 | 严格的 API 兼容策略（3.3 节）；每个 API 变更提前 1 个 release 标注 deprecated |

### 5.4 依赖关系图

```
Phase 1 (P0)
  ├── #4 分布式限流 → 无外部依赖（除 Redis）
  └── #1 文件校验 → 依赖 #4 的 Worker 统一框架（可先用独立 goroutine，后迁移）

Phase 2 (P1)
  ├── #3 附件生命周期 → 依赖 #1 的文件校验基础设施（checksum 可作为快照的一部分）
  └── #2 多设备同步 → 依赖 #4 的 redis（Push token 可存入 Redis 作为缓存层）
                                  
Phase 3 (P2)
  └── #5 Conversation → 可以独立进行，但强烈建议在 Worker 框架（#6）就绪后开始，
                         确保群聊中的异步任务（已读同步、通知 fan-out）有统一处理方式
```

### 5.5 "不做"清单

与任何好的架构一样，明确不做什么同样重要：

1. **不引入消息队列**（Kafka/RabbitMQ/NATS）——当前阶段的异步量不支撑引入队列系统的运维成本。goroutine + outbox 足够。
2. **不做中国大陆 Push 通道适配**（P2+）——厂商通道适配（华为/小米/OPPO/Vivo）各需单独 SDK + 审核流程，团队目前不应承担这个复杂度。
3. **不实现大群（> 500 人）的群聊优化**——当前是功能验证阶段，不是性能优化阶段。大群优化等有真实业务数据后再做。
4. **不实现 ML Spam 检测**（P3+）——用关键词过滤 + 人工审核队列兜底即可。
5. **不引入 API gateway**（如 Kong/APISIX）——当前双 transport (HTTP + gRPC) 模式简洁，API gateway 增加运维复杂度但收益有限。除非需要 API 版本管理、配额管理、多环境路由等，才考虑引入。

---

## 总结

aero-id 的当前架构质量令人印象深刻——分层清晰、模式规范、工程纪律严格。在上述 5 个扩展方向中，**分布式速率限制和文件完整性校验是立即需要解决的生产级问题**，对话抽象是最具产品价值的架构演进，但也是最需谨慎的——它牵涉到数据模型变更和 API 兼容性，建议放在稳定期再进行。

最关键的建议是：**在开始任何功能扩展之前，先建立统一的 Worker 抽象**。这不是最紧急的，但如果不做，每个新方向都会自建一套异步处理机制，3-6 个月后就会出现技术债。一个好的抽象能统一日志、追踪、重试、并发控制，为后面的所有扩展提供一致的基础。
