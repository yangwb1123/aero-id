Now I have a comprehensive understanding of the architecture. Here is my analysis:

---

# aero-id 架构分析报告

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 在当前阶段采用了**务实的分层架构**，有以下几个突出的设计亮点：

**优势一：事务性 Outbox 模式（Transactional Outbox）**

这是当前代码库中架构质量最高的设计。所有异步副作用（通知创建、webhook 投递）都通过 `outbox.Events` 表在**同一数据库事务**中写入，再由 `Relay` 轮询分发。这保证了：
- 领域数据与副作用之间的**原子性**——comment_replied 事件不会在评论行写入后因进程崩溃而丢失
- 通知 dispatcher 与 block/preference checker 的链式组合——`outbox.Chain` 模式允许按顺序串联多个 handler
- 重试和死信机制——`RetryCount`/`DeadLetter`/`LastRetryAt` 提供了背压和故障隔离

这是当前架构中最经得起推敲的部分。

**优势二：轻量级 metrics 自实现**

`internal/pkg/metrics` 精巧地避免了 `prometheus/client_golang` 的依赖，仅用 `sync.Mutex` + `map` 实现了 RED 指标。对于当前规模，这是正确的选择——不引入外部依赖、不需要 Pushgateway、测试零配置。API 设计良好（`ObserveHTTP`/`ObserveGRPC`/`ObserveSSOCall`），扩展业务指标只需追加新的 `observe` 方法。

**优势三：清晰的一刀切接口层（Service Interface）**

`internal/service/service.go` 中定义的七个服务接口（`FileService`/`MessageService`/`CommentService`/`NotificationService`/`BlockService`/`AdminService`/`CommentService`）是**整个系统的中枢神经**。HTTP 和 gRPC handler 都依赖接口而非具体实现，service 层负责所有业务逻辑和授权检查。这意味着：
- 新增传输协议（如 WebSocket/SSE）只需实现 handler，复用 service
- 单测可以用 mock 替代 repo/storage/sso
- 错误边界清晰——repo 返回 raw error，service 翻译为 `AppError`

**优势四：SSO 集成设计**

Token 缓存（60s TTL）和用户缓存（300s TTL）都有后台清理 goroutine，没有依赖 Redis 等外部组件。`breaker.go` 为 SSO gRPC 调用提供了熔断保护。`UserCache.GetMany` 的批量合并逻辑（缺失 uid 攒批调用 SSO）是合理的优化。

### 1.2 当前架构的局限性

**局限性一：领域模型分散与膨胀**

设计文档（D2）明确选择了"横向分层而非纵向分模块"——但实际代码已经演进为按领域分包（`internal/file/`、`internal/comment/`、`internal/message/`、`internal/block/`、`internal/search/`、`internal/notification/`）。这实际上是向**领域驱动（DDD）结构**的自然演进，但存在一个关键问题：

```
# 新旧结构对比（service 层）
旧（设计文档）：internal/service/{file.go, message.go, comment.go}
新（实际代码）：internal/file/{file.go, service_crud.go, service_share.go, service_upload.go}
              internal/comment/{comment.go, service.go}
              internal/message/{message.go, service.go}
```

好处是每个领域自包含，坏处是**模块间交叉引用**增加：`comment/service.go` 依赖 `file` 和 `message` repo（因为 `assertTargetReadable` 需要校验 target 存在）。当三个模块需要互相调用时，横向分层反而更清晰。当前结构导致了包级别的循环依赖风险，需要靠接口而非具体类型解耦。

**局限性二：Soft Delete 级联的缺失是架构级别的问题**

这不仅仅是「少写了几个 SQL」——它是领域事件利用不足的体现。当前已经在用 outbox 机制做级联效果（通知创建），但**没有用同样的模式来处理数据级联**。一个更好的做法是：`file.Delete` 触发 `file.deleted` 事件，由事件 handler 清理 `file_shares`/`comment_reports` 等关联数据。但当前 delete 路径直接从 service 调用 repo 做软删，绕过了 outbox。

**局限性三：GDP 分解（God Package Problem）风险**

`internal/app/app.go` 已经接近 500 行（软限制），承担了所有依赖装配（wiring）的责任。随着业务扩展，它会变成 **God Object**——了解所有类型的创建顺序。依赖注入框架（wire/google-wire）或手动拆分为多个 `app/*_wiring.go` 文件是缓解方案。

### 1.3 架构债务与技术债

| 债务类别 | 严重程度 | 说明 |
|---|---|---|
| **级联缺失** | P0 | 软删 orphan 数据：message_reactions, comment_likes, file_shares 形成死指针 |
| **单机限流** | P1 | `rate_limit.enabled: false` 默认关闭——团队可能不知道分布式部署后的隐患 |
| **SSE 推送不可靠** | P1 | buffer=16 + silent drop，连接断开后 Last-Event-ID 回放仅 50 条 |
| **metrics 无业务指标** | P2 | 无法回答"今天上传了多少文件"这类问题，运维只能通过日志 grep |
| **存储配额缺失** | P2 | 用户可以无限制上传，直到磁盘满 |
| **app.go 过度膨胀** | P2 | 接近 500 行，所有 wiring 集中在单一文件 |
| **跨设备推送缺失** | P3 | 同用户多设备：MarkRead 后不通知其他设备同步 |

---

## 2. 扩展方向

### 方向一：级联生命周期管理（数据完整性）

**为什么需要**：当前软删产生了大量孤儿数据。`message_reactions` 的 `message_id` 引用已软删的消息——如果未来做消息恢复（undelete），这些关联无法复原。最严重的是 `file_shares`——删除文件后，分享记录保留，下次查询会尝试 Join 不存在的文件。这个问题随着数据量线性恶化。

**核心挑战**：
1. 避免级联操作在请求路径上同步执行——删除一条消息不应同步扫描所有 reaction 行
2. 需要区分"级联清理"和"级联通知"——`comment.deleted` 事件既要删除 likes，也要通知参与者
3. 批处理/异步级联需要幂等性——同一事件可能被处理多次

**预期架构变更**：

```
当前:
  service.Delete → repo.SoftDelete (仅当前行)

改进:
  service.Delete → repo.SoftDelete + outbox.Append("file.deleted"|"message.deleted"|"comment.deleted")
                                         ↓
                                   Consumer: 级联清理 + 级联通知
```

核心变更：
- 在 `domain` 中新增事件类型常量（`AuditFileDeleted`/`AuditMessageDeleted`/`AuditCommentDeleted`）
- 在 `service.Delete` 路径中加入 `outboxRepo.Append(ctx, tx, &event)`——需要与软删在**同一事务**中
- 引入新的 outbox handler：`cleanup.Handler` 接收 `{entity_type, entity_id}`，执行 `DELETE FROM message_reactions WHERE message_id = ?` 等清理
- `cleanup.Handler` 应链在现有 `dispatcher` 之后（或之前）——通过 `outbox.Chain` 组合

**影响范围**：
- 需要 GORM 事务外部化：当前 service 层没有显式开启事务，需要将 `*gorm.DB` 透传给 service（或让 repo 实现 `Begin/Commit/Rollback` 方法）
- `message`/`comment`/`file` repo 需要新增批量删除方法（如 `HardDeleteReactions(ctx, messageID)`、`HardDeleteLikes(ctx, commentID)`）
- 迁移文件：为 `message_reactions`/`comment_likes`/`file_shares` 添加 `deleted_at` 列（使用软删而非硬删，保留审计痕迹）

### 方向二：分布式限流与多集群支持

**为什么需要**：部署超过 1 个副本时，单机限流形同虚设。在 `rate_limit.enabled: true` 时，一个用户可以同时发送 `N*burst` 请求（N=副本数）。而且没有全局的共享状态协调。

**核心挑战**：
1. 引入 Redis 增加了部署依赖和运维复杂度——需要确保 Redis 与 aero-id 同时可用
2. 分布式限流的精度和性能权衡——每个请求都要一次 Redis GET 操作（约 0.1-0.5ms）
3. 降级策略——Redis 不可用时，回退到单机限流 vs 完全放开

**两种选项**：

| 选项 | 方案 | 优点 | 缺点 |
|---|---|---|---|
| **A** (推荐) | 引入 Redis + 滑动窗口(pi sliding window) | 精确、业界成熟、支撑其他能力（分布式锁、计数） | 新增依赖 | 
| **B** | PostgreSQL advisory lock + 计数器行 | 零新依赖、事务性 | 性能差（每个请求一个 SQL）、对 PG 连接数有冲击 |

**选项 A 实现建议**：
- 在 `internal/pkg/ratelimit` 包下新增 `RedisLimiter` 实现，接口与现有 `Limiter` 兼容
- 使用 Redis Sorted Set（ZADD + ZCOUNT）实现滑动窗口——key = `ratelimit:{uid}`，member = request_id + timestamp
- 配置式切换：`rate_limit.driver = "local" | "redis"`，local 为兜底
- 引入 `github.com/redis/go-redis/v9` 依赖，或者封装更简单直接的 redsync

**影响范围**：
- `middleware/ratelimit.go` 需改为注入接口而非具体 limiter 实现
- 现有 `rateLimitKey(uid, ip)` 函数不变
- 配置新增 `rate_limit.driver`、`rate_limit.redis_addr` 等
- 健康检查 `/readyz` 需要探测 Redis 可达性

### 方向三：推送基础设施升级

**为什么需要**：当前 SSE 推送有三大薄弱环节——① buffer=16 静默丢弃；② 无跨设备同步；③ 无 APNs/FCM 等离线推送 fallback。对于需要即时通知的 IM/评论场景，这构成了功能完整性缺陷。

**核心挑战**：
1. 水平扩展——多副本部署时，用户 B 的 SSE 连接可能在副本 1，但 outbox relay 只在副本 2 运行，`hub.Publish` 无法跨节点投递
2. 离线推送需要外部服务（APNs/FCM），增加运维复杂度
3. 移动端离线时，SSE 不可用，需要替代的轮询机制

**预期架构变更**：

```
当前:
  outbox relay → dispatcher → SSE Hub (单机内存)

改进:
  outbox relay → dispatcher → Redis Pub/Sub → 各副本的 SSE Hub
                                         ↓  (跨节点广播)
                                    持久化通知表 ← 客户端轮询 fallback
```

具体建议：
1. **Step 1（短期）**：增加 Hub 的 channel buffer 大小（16→128），并增加丢弃计数器指标（`hub.dropped_total`），让运维可见丢事件
2. **Step 2（中期）**：引入 Redis Pub/Sub 作为跨节点广播通道——outbox relay 将事件 publish 到 Redis channel，各副本的 subscriber 接收后推送到本地 SSE 连接。这自然解决了水平扩展问题
3. **Step 3（长期）**：新增 `PushProvider` 接口（FCM/APNs），并提供配置化开关。service 层做推送决策时，先检查用户 SSE 连接在线状态，若不在线则 fallback 到离线推送

**影响范围**：
- `hub.Pusher` 接口保持不变，增加 Redis-backed 实现
- `dispatcher` 中 push 失败（channel full）不再静默丢，而应记录到 metrics
- SSE 的 Last-Event-ID 回放需要支持更大的 limit（目前 50 条在激烈场景下不够）
- 健康检查和停机逻辑需要考虑 Redis Pub/Sub 连接的优雅关闭

### 方向四：存储配额与用量计量

**为什么需要**：`max_upload_bytes: 52428800` 仅限制单文件大小。用户可无限上传直到磁盘满或 S3 账单失控。在 SSO 的多租户场景下，配额是基本的计量单位——没有配额就无法定价、滥用控制、容量规划。

**核心挑战**：
1. 配额检查性能——每次上传都需要 `SUM(size) WHERE owner_uid = ?`，是否需要物化视图？
2. 软删与配额释放——文件软删后是否立即释放配额？还是等待 GC 清理后？
3. 配额阈值的多级设置——硬限制 vs 软限制（警告），以及超额行为的判定（拒绝上传 vs 允许但标记）

**预期架构变更**：

1. **新增 `storage_quotas` 表**：
   ```sql
   CREATE TABLE storage_quotas (
     owner_uid TEXT PRIMARY KEY,          -- 用户维度
     max_bytes BIGINT NOT NULL DEFAULT 0, -- 0 = unlimited
     used_bytes BIGINT NOT NULL DEFAULT 0,
     warning_bytes BIGINT,                -- >= 时触发告警
     updated_at TIMESTAMPTZ NOT NULL
   );
   ```

2. **配额检查点**：
   - `service/file.go` 中 `UploadFile` 和 `CompleteUpload` 路径——在写入 rows 前检查 `used_bytes + size ≤ max_bytes`
   - `PresignUpload` 路径——在分配 storage key 时预留配额（状态为 pending）
   - 软删/硬删时更新 `used_bytes`

3. **并发控制**：使用 `SELECT ... FOR UPDATE` 锁定配额行，防止并发上传竞态。或者使用 PostgreSQL 的 `pg_advisory_xact_lock()` 在应用层做串行化

4. **超出处理**：
   - `domain/errors.go` 新增 `ErrQuotaExceeded`
   - `repository/file.go` 新增 `UpsertQuota(ctx, ownerUID, deltaBytes)` 方法（增量更新）
   - `service` 层转换为 `AppError{Code: "file.quota_exceeded", HTTP: 413}`

**实现复杂度**：约 1-2 天（含迁移 + 单测 + 集成测）

### 方向五：业务可观测性（BPI）

**为什么需要**：没有业务指标，运维团队只能回答"服务是不是还活着"（RED），但无法回答"今天文件上传量同比变化"、"用户消息发送量趋势"、"通知发送成功率"。这些问题直接影响产品决策和故障根因分析。

**核心挑战**：
1. 标签基数控制——按 `uid` 维度打标会导致 metrics 基数爆炸（唯一的 label 值太多）
2. 聚合时机——是 push 模型（应用中计数）还是 pull 模型（日志 → 离线聚合）？
3. 业务指标的定义需要跨团队对齐——"文件上传量"是否包括 dedup skip？"通知发送成功"是指写入通知表还是 SSE 推送成功？

**实施方案**：利用 `metrics.Registry` 已存在的扩展机制

```go
// 在 metrics.go 中新增方法
func (r *Registry) ObserveBusiness(metric string, delta uint64) { ... }

// 注册业务指标名称
const (
    MetricFilesUploaded    = "files_uploaded_total"
    MetricMessagesSent     = "messages_sent_total"
    MetricCommentsCreated  = "comments_created_total"
    MetricNotifsDelivered  = "notifications_delivered_total"
    MetricNotifsDropped    = "notifications_dropped_total"  // SSE channel full
    MetricQuotaWarnings    = "quota_warnings_total"
    MetricFilesDeduped     = "files_deduped_total"
)
```

埋点位置：
- `service/file.go` UploadFile 成功后：`metricsReg.ObserveBusiness(metrics.MetricFilesUploaded, 1)`
- `service/message.go` Send 成功后：`metricsReg.ObserveBusiness(metrics.MetricMessagesSent, 1)`
- `hub.go` default 丢弃分支：`metricsReg.ObserveBusiness(metrics.MetricNotifsDropped, 1)`

**实现开销**：约 4-6 小时——纯埋点工作，不影响任何业务逻辑。

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

**原则一：接口归属消费者**

当前 `service.FileRepository` 等接口定义在 `repository/` 包中——这是正确的（接口归消费者）。但需要注意 `block.BlockChecker` 接口的定义位置：

```
internal/block/block.go → 定义 BlockChecker 接口
internal/comment/service.go → 使用 repository.BlockRepository（依赖具体类型而非接口）
```

这里有一处不一致：`comment/service.go` 直接依赖 `repository.BlockRepository`（具体类型），而 `notification/dispatcher.go` 依赖 `block.BlockChecker`（接口）。需要统一为接口注入模式。

**原则二：最小接口粒度**

`FileStorage` 接口目前包含 8 个方法，其中 `NewKey` 和 `PresignPut` 仅用于 S3 的 presigned upload 流程。对于只使用 local storage 的部署，这些方法留空或返回 `ErrNotSupported`。建议拆分为：

```go
type FileStorage interface {
    Put(ctx, ownerUID, name, reader) (key, size, err)
    Get(ctx, key) (reader, err)
    Delete(ctx, key) err
    Stat(ctx, key) (*ObjectInfo, err)
}

type Presigner interface {
    PresignGet(ctx, key, ttl) (string, err)
    PresignPut(ctx, key, ttl) (string, err)
}

type KeyAllocator interface {
    NewKey(ownerUID) string
}
```

这样 `service` 可以按需 type-assert，而不是将不支持的语义留给运行时错误。

### 3.2 是否需要引入新的抽象层

**需要：事务管理器（TransactionManager）**

当前代码库使用 GORM 的事务模式——但 service 层无法开启/提交/回滚事务，因为 `*gorm.DB` 不暴露给 service。outbox 的 `Append` 方法依赖 caller 传入 `*gorm.DB`，但 service 层没有 `*gorm.DB`。

建议引入：

```go
// domain.TxManager 或 repository.TxManager
type TxManager interface {
    Begin(ctx) (Tx, err)
}

type Tx interface {
    Commit() error
    Rollback() error
    DB() *gorm.DB  // 仅 repository 包内部使用
}
```

这样 `service.Delete(ctx, id)` 的流程变为：

```
svc.Delete:
  tx := s.txmgr.Begin(ctx)
  defer tx.Rollback() on panic
  s.repo.SoftDelete(tx.DB(), id)
  s.outbox.Append(tx.DB(), event)
  tx.Commit()
```

这个抽象可以使级联操作和 outbox 事件在同一个事务中原子提交。

### 3.3 向后兼容性

所有 HTTP API 当前返回的响应结构没有版本前缀（只有 `/v1/` 路由前缀）。以下变更需要保持兼容：

- **配额超额**：新增 HTTP 413 状态码，现有客户端不会理解——需要确保 413 的 `error.code` 是新增的，不破坏现有 4xx 解析
- **级联删除**：现有客户端软删消息后不会收到 404——必须继续保持"已删 = 404"语义。级联清理不能改变当前 service 接口的行为
- **推送扩展**：SSE Last-Event-ID 格式不变，新增 field 只追加不修改

**规则**：所有新增功能通过新接口暴露（如 `GET /v1/storage/usage`），不修改现有 handler 的请求/响应体格式。

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

| 扩展方向 | 需要引入 | 推荐技术 | 评估 |
|---|---|---|---|
| 级联生命周期 | 否 | 复用现有 outbox 机制 | 不需要新依赖 |
| 分布式限流 | **是** | Redis (go-redis/v9) | 需要评估是否已有 Redis |
| 推送基础设施 | 条件性 | Redis Pub/Sub → FCM/APNs | Step 2 需要 Redis；Step 3 需要 firebase-admin-go |
| 存储配额 | 否 | PG 表 + SELECT FOR UPDATE | 不需要新依赖 |
| 业务指标 | 否 | 复用现有 metrics.Redistry | 不需要新依赖 |
| 依赖注入 | 推荐 | wire (google/wire) | 缓解 app.go 膨胀 |

**关键决策点：Redis**

Redis 引入应该在方向二和方向三之间做联合决策。两个方向可以共享同一个 Redis 连接：

```
Shared Redis 使用场景:
  1. 分布式限流: 滑动窗口计数器
  2. 跨节点 SSE 广播: Pub/Sub channel
  3. (未来) 分布式锁: GCS sweeper 的互斥
  4. (未来) 缓存: 替代内存 TokenCache
```

**如果 Redis 不引入**：
- 方向二只能做每个副本独立限流（承认不够精确）或者使用 PG advisory lock（性能差）
- 方向三的跨节点推送无法实现——多副本部署时 SSE 事件不会到达全部连接

**建议**：先做方向五和方向一（零新依赖），联合评估 Redis 需求后再决定方向二和方向三的优先级。

### 4.2 第三方依赖评估标准

当前依赖清单非常精简（Gin/GORM/Viper/Zap/uuid/testcontainers）。新增依赖应通过以下标准：

1. **许可证兼容**：必须是 BSD/MIT/Apache 2.0
2. **Go 模块版本稳定性**：优先 `v1` 或 `v2` stable，避免 `v0.x`
3. **测试兼容性**：不能破坏 `go test ./...`（无需 Docker 的单元测试）
4. **覆盖必要性**：是否已有自实现足够？如 `go-redis/v9` 是 Redis 客户端事实标准，比自实现 Redis 协议解析合理

### 4.3 自建 vs 采购

当前所有能力都是自建，因为业务场景垂直（文件/消息/评论）且规模可预测。如果未来出现以下信号，需要重新评估：

- **即时通讯成熟度需求**：消息需要端到端加密、已读回执多设备同步 → 应考虑集成专业 IM SDK（如融云、腾讯云 IM）而非自建
- **推送到达率要求**：SSE 连接到 99.9%+ 到达率要求 → 需要集成 FCM/APNs（通过 Firebase Cloud Messaging），这是外部服务，无法自建
- **CDN 文件分发**：大文件下载需要全球加速 → 集成 Cloudflare R2 / AWS CloudFront，aero-id 只负责 presign URL

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0（下一个迭代，1-2 周）
├── 方向五：业务指标           — 4-6h，零风险，零依赖
├── 方向一（Step 1）：级联清理   — 2-3 天，阻塞数据完整性风险
└── 方向一（Step 2）：级联通知   — 1-2 天，复用 outbox 事件

P1（1-2 月）
├── 方向四：存储配额            — 1-2 天，基本的多租户计量
├── 方向三（Step 1）：SSE 改进   — 1 天，buffer 调整 + 丢弃指标
└── 方向二（评估）：Redis 引入决策 — 调研 + PoC

P2（3 月+）
├── 方向三（Step 2）：跨节点推送  — 需要 Redis，依赖 P1 决策
├── 方向三（Step 3）：离线推送    — 需要 FCM/APNs 集成
├── 方向二：分布式限流           — 如果 P1 决策引入 Redis
└── app.go 拆分（技术债清理）     — 低风险但提升可维护性
```

### 5.2 阶段划分和里程碑

**Phase 0（立即启动，1 周）——低风险快赢**：

| 周 | 工作 | 交付 |
|---|---|---|
| Day 1-2 | 方向五：业务指标埋点 | metrics.Registry 新增方法 + 所有业务路径埋点 |
| Day 2-3 | 方向一 Step 1：级联事务支持 | TxManager 抽象 + outbox 事件同一事务 |
| Day 4-5 | 方向一 Step 2：级联清理 Handler | `cleanup.Handler` 链入 outbox relay |

里程碑 1：数据完整性风险消除，业务可观测性就绪。交付物：`make test && make lint` 全绿。

**Phase 1（第 2-3 周）——核心能力补全**：

| 周 | 工作 | 交付 |
|---|---|---|
| Day 6-7 | 方向四：存储配额 | `storage_quotas` 表 + 配额检查 + 迁移 |
| Day 8-9 | 方向三 Step 1：SSE 推送改进 | buffer 调整 + 丢弃指标 + Last-Event-ID limit 增加 |
| Day 10 | 方向二评估：Redis 引入决策 | PoC 报告：是否引入、成本分析、运维影响 |

里程碑 2：多租户计量就绪，推送可靠性提升。交付物：配额测试报告 + 推送丢事件对比数据。

**Phase 2（第 4-6 周）——扩展**：

| 周 | 工作 | 交付 |
|---|---|---|
| Day 11-15 | 方向三 Step 2：Redis Pub/Sub（如果决定引入） | 跨节点 SSE 广播 |
| Day 16-18 | 方向二：分布式限流（+Redis） | 配置式切换 local/redis |
| Day 19-20 | 技术债清理：app.go 拆分 + 接口一致性审查 | app/*_wiring.go 拆分 |

里程碑 3：多副本部署就绪。交付物：完整集成测试覆盖。

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| **级联事务影响性能** | 中 | 每次 delete 增加 outbox 写入 | 选型时用 `INSERT ... RETURNING` 批处理，单次 delete 带 N 个级联事件用一条 SQL |
| **Redis 引入导致部署复杂度增加** | 高 | 小团队运维负担 | 使用托管 Redis（如 Upstash/ElastiCache Serverless），零运维；或采用 PG 替代（选项 B） |
| **SSE 丢事件无法 100% 解决** | 中 | 移动端断连频繁 | Last-Event-ID 回放 + 客户端定期轮询 `/v1/notifications?since=X` 做兜底 |
| **配额检查并发竞态** | 低 | 上传超量 | `SELECT ... FOR UPDATE` 行锁 + 重试机制（最多 3 次） |
| **outbox relay 成为瓶颈** | 低 | 大量 delete 事件积压 | relay 支持并发消费（goroutine pool），每个 handler 独立 |

**关键决策树**：

```
是否需要多副本部署（production）？
├── 是：→ 引入 Redis（方向二+三联合决策）
│   ├── 已有 Redis 运维能力？→ 全量推进 P1/P2
│   └── 无 Redis 运维能力？→ 使用托管 Redis 或暂缓方向三 Step 2
└── 否：→ 方向三 Step 2 可推迟，方向二接受单机局限
    └── 配额和级联优先实施（零新依赖）
```

---

## 总结

aero-id 的架构质量在当前阶段是**健壮的**——事务性 outbox、双协议共享 service 层、SSO 集成设计都是正确决策。文档中识别出的五个方向确实命中了最薄弱的环节。

优先级上，我强烈建议**从方向五和方向一开始**——它们不引入任何新依赖、风险最低、用户感知最强（业务指标让运维立即可见，级联清理消除 silent data corruption）。方向二和三的 Redis 决策可以并行评估，但不应阻塞 Phase 0 的交付。

架构演进的核心原则：**不要为了未来的不确定性过度抽象（YAGNI），但也不要让已知的问题积累到必须停机修复（Technical debt ≠ reckless debt）**。当前五个方向中，方向一（级联）和方向四（配额）属于"已知问题积累"范畴，应该优先解决。
