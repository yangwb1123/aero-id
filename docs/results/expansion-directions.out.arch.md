# 架构分析报告：aero-id 高阶扩展方向

## 1. 架构评估

### 1.1 当前架构的优势

当前 aero-id 的架构设计展现了极高的工程成熟度，几个关键亮点值得肯定：

**清晰的一元分层**
`handler → service → repository → Postgres` 的严格单向依赖，配合 `sso` 和 `storage` 作为基础设施抽象，形成了经典的 Port-Adapter 结构。这一设计使得 38 个 RPC 在 HTTP 和 gRPC 之间保持行为完全一致——这是一个被许多项目轻视、但实际维护中极其宝贵的属性。

**outbox 模式作为异步骨架**
outbox 不仅承担了一致性保障（事务写入 → 可靠投递），还意外成为了整个系统的"事件脊柱"。文档中列出的每个扩展方向（群组、文件处理、Webhook）都围绕 outbox 组织事件流——这意味着 outbox 已经从"一个基础设施组件"进化成了**架构级聚合点**。设计之初没有预料到这一点，但代码组织方式恰好允许了这种演变。

**限界上下文划分干净**
`domain` 包中实体定义精简（`Message`, `File`, `Comment`, `Block`），每个实体职责单一，没有出现"万能模型"。这给新增实体（如 `Group`、`WebhookSubscription`）留出了清晰的位置。

### 1.2 当前架构的局限性

以下局限并非设计缺陷，而是 MVP 阶段合理取舍后自然暴露的瓶颈：

**无跨实例协调层**
这是最紧迫的架构债务。当前架构本质上假设**单副本部署**——即使代码支持多实例，关键路径（限流、幂等、缓存）全部是本地的。文档中引用的 `config.go:119` 注释明确承认了这一点。这不是 bug，但一旦启动水平扩容，这就是一个**安全与正确性风险**。

**异步处理缺少任务抽象**
outbox + webhook handler 虽然是可用的，但缺乏"任务管线"的抽象。当前只有"发出一条事件 → handler 消费"的模型，无法表达"上传完成 → 生成缩略图 → 更新 meta → 通知用户"这样的多步工作流。如果引入方向 3（文件后处理），这种场景会大量出现，需要在 outbox 之上构建一层 workflow 抽象。

**消息模型严格绑定 1:1**
`Message` 表的 `from_uid` / `to_uid` 是需求阶段的合理决定，但现在成了一个架构约束——任何群组消息扩展都必须引入 `group_id` 可空列与权限交叉检查。如果预测群组会是核心场景，这一点应该在领域模型层面提前规划。

**管理面与运行时面未分离**
`admin` 角色权限嵌入在运行时服务中。随着 Webhook 订阅管理（方向 5）和群组管理（方向 1）的出现，管理 API 和运行时 API 的混合会带来权限模型的膨胀压力。考虑将管理面逐步独立为一个单独的 Bounded Context。

### 1.3 架构债务与技术债（已存在但可接受）

| 债务项 | 严重程度 | 说明 |
|--------|----------|------|
| 配置中硬编码 webhook 目标 | 低 | 方向 5 会自然解决 |
| per-instance 限流/幂等 | **高** | 方向 2 的驱动力 |
| 无 Pipeline/worker 抽象 | 中 | 方向 3 会暴露出 |
| 对话列表靠前端聚合 | 低 | 方向 4 解决 |
| outbox 并行轮询冲突 | 中 | 方向 2 Phase D 解决 |

---

## 2. 扩展方向分析

### 方向 A（优先级重排）：Redis 共享状态层

**业务价值**：直接解锁水平扩容的安全运行能力。无此方向，任何多实例部署都存在数据竞争和正确性风险。

**技术价值**：统一了分散在各组件的本地状态管理（限流、幂等、缓存），引入了一个可观测的共享状态层。

**核心挑战**：

1. **降级策略设计**：Redis 不可用时，系统应优雅降级到本地模式（性能下降而非不可用）。这意味着每个使用 Redis 的组件都需要保留本地 fallback，且 fallback 行为可配置。
2. **序列化契约**：不同组件对 Redis 的使用模式不同（限流是计数器+滑动窗口、幂等是 SET NX、缓存是 KV+TTL），需要统一但不过度抽象的接入层。
3. **配置管理**：Redis 连接池大小、超时、TLS、认证、Sentinel/Cluster 模式——这些配置需要在 `config.go` 中以一等公民身份出现，而非临时追加。

**架构影响**：

```
Before:  [Limiter(mem)] [Idempot(mem)] [Cache(mem)]  (各自独立)
After:   [Limiter(mem→redis)] [Idempot(redis)] [Cache(L1:mem→L2:redis)]
         ↑ 共享连接池     ↑ SET NX             ↑ 多级缓存
```

关键决策：**是引入一个 `RedisClient` 单例还是每个组件各自持有？** 建议方案是 `internal/pkg/redis` 包提供一个 Client 管理器（连接池 + 健康检查 + 指标暴露），各组件通过接口注入。

**对现有系统的影响**：
- `config.go`：新增 `redis.endpoints`, `redis.pool_size`, `redis.timeout` 等配置
- 引入 `go-redis/redis` 或 `rueidis` 作为依赖
- 现有 `sso.TokenCache` 和 `sso.UserCache` 接口不变，实现替换
- `idempotency/store.go`：新增一个 `RedisStore` 实现
- `middleware/ratelimit.go`：新增 `RedisBackend` 实现

### 方向 B（优先级提升）：消息领域模型扩展（群组 + 对话聚合）

**为什么合并分析**：群组消息和对话聚合列表共享同一个领域核心——`Message` 模型的角色扩展。群组是"发送给 N 个人"，对话聚合是"按人/群组归类的最后一条消息"。两者在数据库层面高度耦合。

**业务价值**：这是最直接的用户可见功能提升。群组解锁协作场景，对话列表解决"消息一多就乱"的体验问题。

**核心挑战**：

1. **权限模型的交叉复杂度**：
   - 1:1 消息：基于 `from_uid` / `to_uid` 的简单二元关系
   - 群组消息：成员角色（owner/admin/member）+ Block 过滤 + 群组可见性
   - 这两种模式在同一个 `messages` 表中共存时，查询逻辑会产生大量条件分支

2. **已读跟踪的新模式**：
   - 1:1：`messages.read_at` 就够了（对话的双方各自标记）
   - 群组：需要 `group_members.last_read_message_id`，且计算"未读数"时需要跨用户聚合
   - 两种模式在同一 API 中暴露，前端需要区分处理

3. **对话列表的聚合性能**：
   - 文档中给出的 `DISTINCT ON (least, greatest)` 查询在百万级消息量下可能成为性能瓶颈，需要提前做 EXPLAIN ANALYZE
   - 考虑物化视图或定期缓存的折中方案

**架构影响**：

```
新增实体：
  group              → domain/group.go
  group_member       → domain/group_member.go
  Message.GroupID    → 现有 domain/message.go (可空)

新增接口：
  GroupService       → service/group.go
  ConversationService → service/conversation.go (新服务)

修改接口：
  MessageService     → Send 支持 GroupID
  SearchService      → 索引群组消息
```

**关键设计决策**：群组消息是否与 1:1 消息共用 `messages` 表？

| 选项 | 优势 | 劣势 |
|------|------|------|
| 同表（`group_id` 可空） | 搜索/通知/outbox 逻辑无需两套；cursor 分页统一 | 查询条件复杂度上升；索引管理变复杂；部分列语义模糊 |
| 分表（`group_messages` 独立） | 表结构清晰；各自优化索引 | 搜索/通知/outbox 需要分支处理；跨表查询困难 |

**推荐**：群组消息体量与 1:1 消息不在一个数量级时，选**同表 + 部分列共享**（content, created_at, kind 等通用列；`from_uid` 保留为发送者；`to_uid` 可为空或存群组 ID 的冗余）。如果群组消息很快会成为主导流量，分表更干净。中期展望，`messages` 表加 `group_id` 可空列是最小侵入方案。

### 方向 C：文件后处理管线

**业务价值**：用户上传图片/文档后，立即能看到缩略图或预览——这是文件存储产品的基本体验线。

**技术价值**：引入异步工作管线（Pipeline）的架构模式，这套模式未来可复用于视频转码、文档 OCR、病毒扫描等其他后处理场景。

**核心挑战**：

1. **Worker 的生命周期管理**：worker 是作为 aero-id 的子进程运行，还是独立的二进制？独立更符合单一职责，但增加了部署复杂度。建议在初期作为内嵌 goroutine 池（`internal/pipeline`），后期拆分为独立部署。

2. **处理产物的引用计数**：缩略图 blob 是应该与原图共享引用计数，还是独立计数？
   - 共享：删原图时自动删除缩略图 ✓；但删除原图+保留缩略图场景 ✗
   - 独立：灵活性高 ✓；生命周期管理复杂 ✗
   - 建议：缩略图独立引用计数，但通过 `parent_storage_ref_id` 关联原图。GC 时：如果原图引用归零且缩略图也归零→清理；如果原图归零但缩略图仍被引用（比如分享缩略图链接）→保留。

3. **处理超时与降级**：
   - 设置 `max_processing_time`（建议 30s 为默认超时）
   - 超时后标记 `meta.preview_status = "timeout"`，触发告警而非阻塞用户
   - Video 帧提取等耗时操作应支持 TUS 式进度回调？初期可暂缓。

**架构影响**：

```
新增：
  internal/pipeline/       → 管线框架
    ├── manager.go         → 任务调度 + 并发控制
    ├── image.go           → 缩略图 / webp 生成
    ├── document.go        → PDF 预览 / 文本提取
    └── types.go           → Job 定义 + Status

修改：
  service/file.go          → Upload 完成后写 file.uploaded outbox 事件
  outbox/handler.go        → 注册 PipelineHandler 消费 file.uploaded
  domain/file.go           → File 新增 Status 枚举
```

**PR/评审指南**：确保 `pipeline` 包不直接依赖 `outbox`——它只消费 `Job`，事件驱动由外部编排。这保持了 outbox 作为"事件通道"而非"调度器"的角色。

### 方向 D：Webhook 集成平台

**业务价值**：如果 aero-id 要作为平台服务对外暴露事件（面向其他服务/第三方集成），webhook 管理面是必需的。

**核心挑战**：

1. **事件顺序保证**：同 aggregate 的多个事件（如消息的连续编辑）必须按顺序投递。解决方案是 `event_id + subscription_id` 的幂等键 + per-subscription 的顺序队列。这不难实现，但容易被忽略。

2. **密钥轮换的双窗口**：旧密钥过期前，新旧密钥同时有效一段时间。这要求存储两个密钥 + 各自的生效/失效时间。建议设计为：

   ```
   webhook_subscriptions:
     current_secret           → 用于签名新投递
     previous_secret          → 用于验证重试/旧签名
     previous_secret_expires  → previous 自动过期时间
   ```

3. **背压与退避的自动化**：
   - 连续 N 次 5xx → 指数退避 × 暂停 × 管理员告警
   - 暂停后自动恢复探测（每 5 分钟发一条探针请求）
   - 这些策略应在 `outbox` 层作为通用能力，而非 webhook handler 特有

**架构影响**：

```
新增：
  domain/webhook_subscription.go  → 实体
  service/webhook_service.go      → CRUD + 测试投递
  handler/http/webhook.go         → 管理 API
  repository/webhook_repo.go      → 持久化

修改：
  outbox/webhook.go               → 从配置驱动改为订阅表驱动
  admin/roles.go                  → 新增 webhook.manage 权限
  config.go                       → webhook 配置降级为默认/fallback
```

**关键决策**：Webhook 管理面是放在 aero-id 内部，还是独立为一个新的微服务？

| 选项 | 优势 | 劣势 |
|------|------|------|
| 放在内部 | 复用 outbox 层和 admin 权限模型；部署简单 | 管理面与运行时面耦合；权限膨胀 |
| 独立服务 | 职责分离清晰；独立扩缩容 | 需要跨服务调用；增加部署复杂度；与 outbox 的通信需要新协议 |

**推荐**：初期放在内部（与 admin 面同一层级），当订阅者超过 100 个或需要独立 SLA 时再拆分。注意代码组织上预留拆分的可能性——`service/webhook_service.go` 的接口定义应足够抽象，未来可移出。

---

## 3. 接口设计建议

### 3.1 核心设计原则

**接口按消费者分层，而非按实现分层**

当前存在一个问题：`service` 层的接口同时服务于 HTTP handler 和 gRPC handler，这是正确的。但未来还需要服务于**管理 API**（Webhook CRUD）和**内部 worker**（pipeline）。建议按消费者角色分组，而非把所有方法放在一个大接口里：

```go
// 反模式：一个接口包含所有方法
type MessageService interface {
    Send(ctx, req) → ...       // 终端用户
    ListInbox(ctx, req) → ...  // 终端用户
    Delete(ctx, req) → ...     // 终端用户
    MarkRead(ctx, req) → ...   // 终端用户
    AdminList(ctx, req) → ...  // 管理员
    Stats(ctx, req) → ...     // 监控/内部
}
```

```go
// 推荐：按消费者角色拆分
type MessageUserService interface { Send, ListInbox, Delete, MarkRead }
type MessageAdminService interface { AdminList, AdminDelete }
// 共用的内部方法单独定义
```

这对当前代码影响很小，但在引入群组和 Webhook 后，管理面方法的集中度会快速上升。

### 3.2 新增抽象层的判断

| 抽象层 | 是否需要 | 理由 |
|--------|----------|------|
| `Pipeline` (任务管线) | **是，方向 C 必须** | 文件后处理是一个多步骤、可扩展的异步流程，不适合直接在 outbox handler 里硬编码 |
| `RedisClient` (共享状态管理器) | **是，方向 A 必须** | 多个组件需要 Redis 连接，需要一个统一管理连接池、健康检查、指标暴露的层 |
| `GroupACL` (群组权限) | **建议，方向 B** | 群组消息权限的复杂度（成员+角色+block）值得抽成一个独立的 checker |
| `WebhookSubscriptionManager` | **建议，方向 D** | 订阅的 CRUD + 密钥轮换 + 投递历史，管理面逻辑足够复杂 |

### 3.3 向后兼容性

每个扩展方向对现有 API 的兼容性影响：

| 方向 | 影响 | 兼容策略 |
|------|------|----------|
| Redis 共享状态 | 无外部 API 变更 | 纯内部替换 |
| 群组消息 | 新增 `POST /v1/groups` 等 API | `POST /v1/messages` 的 `to_uid` 保持不变；群组消息通过 `group_id` 字段区分 |
| 文件后处理 | `File` 模型新增 `status` 字段 | 前端默认不处理新字段；旧客户端忽略 `status` |
| 对话聚合 | 新增 `GET /v1/conversations` | 现有 `GET /v1/inbox` 保持不变 |
| Webhook 平台 | 新增 `GET /v1/admin/webhooks` | 现有 webhook 投递机制不变；旧配置通过迁移脚本转入订阅表 |

---

## 4. 技术选型

### 4.1 Redis 客户端库

| 选项 | 优势 | 劣势 | 推荐度 |
|------|------|------|--------|
| `go-redis/redis` (v9) | 社区最成熟；文档丰富；集群/Sentinel 支持 | 较重；API 风格偏旧 | ⭐⭐⭐⭐ |
| `rueidis` | 原生支持 RESP3；性能更高；DoCache 多级缓存模式 | 社区较小；API 风格不同 | ⭐⭐⭐ |

**建议**：选择 `go-redis/redis` v9，稳定成熟，团队上手成本低。如果未来确定方向 A Phase C（SSO 缓存改为多级缓存），`rueidis` 的 DoCache 模式更符合需求，可以评估迁移。

### 4.2 图片处理库

| 选项 | 优势 | 劣势 |
|------|------|------|
| `golang.org/x/image/draw` | 无外部依赖；轻量 | 格式支持有限（仅常见格式）；无 EXIF 处理 |
| `github.com/disintegration/imaging` | API 简洁；支持缩略图常用操作 | 需要 CGo 依赖？否，纯 Go |
| `github.com/nfnt/resize` | 简单 | 已归档不再维护 |
| 外部进程（`ffmpeg`/`ImageMagick`） | 功能最全；EXIF 剥离支持好 | 增加部署依赖；进程管理复杂度 |

**建议**：缩略图/缩放用 `imaging` + EXIF 清理用 `github.com/rwcarlsen/goexif` 或 `github.com/dsoprea/go-exif`。视频帧提取使用外部 `ffmpeg` 进程调用（通过 `exec`）。不要引入 ImageMagick——它的安全历史不够好。

### 4.3 自建 vs 购买/复用

| 组件 | 决策 | 理由 |
|------|------|------|
| 缩略图生成 | **自建** | 业务逻辑与存储/引用计数深度绑定，外部服务难以集成 |
| Webhook 投递 | **自建（已有）** | outbox 已经是基础设施，在此基础上加管理面比替换为 Svix/Standard Webhooks 更划算 |
| 对话聚合 | **SQL 实现** | PostgreSQL 的 `DISTINCT ON` 和窗口函数足够胜任，不引入搜索专用引擎 |
| 任务队列 | **自建（基于 outbox）** | 不要引入 RabbitMQ / Kafka——当前流量远未达到需要独立消息队列的级别。outbox + pipeline worker 足够了 |

### 4.4 什么时候应该考虑引入队列系统？

当同时满足以下条件时，考虑 RabbitMQ / Kafka / NATS：
1. 吞吐量超过 10,000 事件/秒
2. 需要精确的一次语义（Exactly-Once Semantics）而非 at-least-once
3. 事件消费者需要独立扩缩容和不同消费速度

当前场景（文件后处理、群组通知、webhook 投递）的合计量级远低于此阈值，outbox 轮询足够了。

---

## 5. 实施路线图

### 优先级重新排序

基于文档的优先级建议，结合架构影响和依赖关系，我给出的调整建议：

| 优先级 | 方向 | 依赖 | 建议在哪个版本切入 |
|--------|------|------|-------------------|
| **P0** | Redis 共享状态 (方向 2) | 无 | **立即启动** |
| **P1** | 群组消息 (方向 1) | 方向 2 需完成 Phase A（幂等迁移） | 方向 2 Phase A 之后 |
| **P1** | 文件后处理管线 (方向 3) | 方向 2 Phase A | 方向 2 Phase A 之后，可与方向 1 并行 |
| **P2** | 对话聚合列表 (方向 4) | 无直接依赖 | 任何时间 |
| **P2** | Webhook 平台 (方向 5) | 方向 2 Phase A（幂等）+ 方向 1（群组事件） | 方向 1 和 2 基础完成之后 |

**调整逻辑**：
- 方向 2（Redis）是所有方向中唯一"不做就无法安全扩容"的，所以 P0
- 方向 1（群组）和方向 3（文件后处理）都有直接的**用户体验价值**，并行开发互不阻塞
- 方向 4（对话列表）代码改动量最小，可作为"低风险交付"穿插在 P1 方向的间隙
- 方向 5（Webhook）价值依赖于平台化定位，且对 outbox 依赖深度最强，放在最后

### Phase 划分

**Phase 0 —— 基础设施加固（2-3 周）**

目标：消除水平扩容的安全风险。

```
Week 1:
  • 引入 go-redis，建立 internal/pkg/redis 连接管理器
  • 幂等存储迁移到 Redis（SET NX EX TTL），GORM 作为 fallback
  • 新增 redis 配置项 + 启动时健康检查

Week 2-3:
  • Rate limiter 增加 RedisBackend（滑动窗口 + GCRA）
  • SSO 缓存多级化（L1: small TTL memory → L2: Redis）
  • Outbox relay 引入 SELECT FOR UPDATE SKIP LOCKED
  • 集成测试覆盖 Redis 故障降级场景
```

**里程碑**：`envoy/load balancer` 可以安全地指向多个 aero-id 实例。

---

**Phase 1 —— 用户可见功能（4-6 周）**

两个并行轨道：

**Track A: 群组消息**
```
Week 1-2:
  • Group 实体 + GroupMember 实体 + 迁移脚本
  • GroupService: Create/List/AddMember/RemoveMember
  • Message.GroupID 可空字段 + 迁移

Week 3-4:
  • 群组消息发送 + 列表（cursor 分页）
  • 权限检查：成员 → 可见；非成员 → 404
  • Block 过滤集成

Week 5-6:
  • 群组消息已读跟踪（last_read_message_id）
  • 群组 outbox 事件 + SSE 推送
  • 所有权转移逻辑（owner 离职场景）
```

**Track B: 文件后处理管线**
```
Week 1-2:
  • Pipeline 框架：Job 定义、Worker 池、超时控制
  • File.Status 枚举（uploading/processing/ready/error）

Week 3-4:
  • Image worker：缩略图生成 + WebP 转换 + EXIF 剥离
  • 处理产物上传到 storage + 引用计数管理

Week 5-6:
  • 文档预览（PDF 缩略图 + 文本提取）
  • 管线失败重试 + dead-letter + 告警
  • meta.preview_status 暴露到 API
```

**里程碑**：用户可以创建群聊、在群内发送消息、上传图片后看到缩略图预览。

---

**Phase 2 —— 体验提升与平台能力（4 周）**

```
Week 1-2:
  • 对话聚合列表 API（GET /v1/conversations）
  • 未读置顶 + 最后消息预览
  • Block 过滤 + 自消息排除
  
Week 3-4:
  • Webhook 管理面 API（CRUD 订阅）
  • 密钥轮换双窗口
  • 投递历史查询 + 成功率指标暴露
  • 现有 webhook 配置迁移脚本
```

**里程碑**：消息体验达到现代 IM 应用标准；平台可向外暴露事件。

---

### 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Redis 成为新单点故障 | 中 | **高** | 哨兵/集群模式 + 本地降级 fallback + 跨 AZ 部署 |
| 群组消息的权限模型复杂度被低估 | **高** | 中 | 先实现最小可用版本（成员可读/可写），角色细粒度（admin/moderator）放在 Phase 2 |
| 文件后处理管线中的格式兼容性 | 中 | 中 | 不可处理的格式优雅降级（`preview_status=unsupported`），不阻塞用户上传 |
| 对话聚合 SQL 性能不达标 | 中 | 低 | 提前在 staging 用 100 万条数据压测；备选方案：物化视图 / 缓存层 |
| Webhook 平台开放后被滥用（DDoS 回调） | 低 | **高** | 订阅审核制（初期管理员手动审批）；单订阅者速率限制；自动暂停机制 |
| 方向并行开发导致团队上下文切换成本 | 中 | 中 | Track A/B 明确分配到不同开发者/小组；共用基础设施（Redis、Pipeline 框架）的接口提前定义 |

---

## 附录 A：关键架构决策记录（ADR）

### ADR-001：Redis 共享状态的抽象层次

- **状态**：提议（Phase 0 前确认）
- **决策**：`internal/pkg/redis` 包提供 Client 管理器，各组件通过接口注入 Redis 依赖，而非共享全局单例
- **理由**：组件级别的隔离便于独立降级、独立测试、独立配置 TTL/超时
- **代价**：每个组件需要额外几行配置代码

### ADR-002：群组消息与 1:1 消息同表存储

- **状态**：提议（Phase 1 前确认）
- **决策**：`messages` 表加 `group_id` 可空列 + `UNIQUE(group_id, created_at)` 部分索引
- **理由**：避免搜索/通知/outbox 中的双路分支；减少迁移工作量
- **代价**：查询条件复杂度增加；`to_uid` 在群组消息中语义变为"冗余存储的群组 ID"

### ADR-003：Pipeline Worker 初期内嵌，后期独立

- **状态**：提议（Phase 1 前确认）
- **决策**：Phase 1 中 pipeline worker 作为 aero-id 进程内的 goroutine 池运行
- **理由**：部署简单；避免 RPC 开销；流量不足以证明独立部署的必要性
- **触发独立条件**：CPU 使用率 > 40% 来自 pipeline，或需要独立扩缩容时

---

## 总结

当前 aero-id 代码库处于一个极好的状态：MVP 功能完整，基础设施组件（outbox、SSO 缓存、限流、存储抽象）的设计都预留了扩展点。五个扩展方向在架构上是**低摩擦的**——它们都围绕已有的核心抽象（outbox、`File.Meta`、`MessageKind`、cursor 分页）展开，而非推翻重来。

最大的风险是**群组消息的权限模型复杂度**和 **Redis 共享状态层的降级策略**，这两个方向都需要在 Phase 0/1 的早期进行充分的设计讨论和技术验证。

推荐接下来的讨论重点：**确认 ADR-001 和 ADR-002**，然后进入 Phase 0 的实现阶段。
