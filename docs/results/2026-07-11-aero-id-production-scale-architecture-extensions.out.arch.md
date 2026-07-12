现在我已经对代码库有了透彻的了解，以下是我的架构分析。

---

# aero-id 架构分析

## 1. 架构评估

### 当前架构的优势

**分层依赖规则**（handler → service → repository/storage/sso）是该项目的核心工程优势。这是一个教科书式的整洁架构，其约束在 AGENTS.md 中得到严格执行，并在 `app.go` 的组合根中得以体现。具体的优势包括：

- **事务性发件箱模式**（`internal/outbox/`）是一个明智的设计选择。域事件在导致它们的数据的*同一*数据库事务中写入，然后由 `Relay` 异步排空。这避免了最终一致性系统中的经典两阶段提交问题，并且 `MarkFailed`/`backoff`/`DeadLetter` 逻辑意味着下游失败不会丢失事件。`NotificationDispatcher` 和可选的 `WebhookHandler` 展示了这种模式的正确使用。

- **域实体的 SSO 所有权分离**——没有本地账户表，没有用户的外键，只有 `uid` 字符串——是 aero-id 与身份提供者之间边界的一个干净定义。`sso.Client` 中的 `TokenCache` + `UserCache` + `singleflight` + 断路器层构建良好，具有 `StaleTTL` 用于在 SSO 故障时优雅降级。

- **`IdempotencyStore` 已经跨副本工作**（基于 GORM，共享数据库），因此幂等键在无需修复的情况下就能正确扩展。这是一个值得记录的对比。

- **存储接口**（`FileStorage`）是后端无关的，具有用于 GC 的 `Lister` 可选接口。`PresignGet`/`PresignPut` 的添加避免了在以 S3 为后端时流量通过应用进程，这是大型文件操作的正确架构。

- **GORM 事务模式**：`messageRepo.Create`、`commentRepo.CreateWithReplyBump` 和 `MarkReadWithEvent` 都正确地使用 `Transaction(func(tx *gorm.DB) error { ... })` 来原子地写入域行+发件箱事件+物化计数器。这在应用层保证了发件箱模式的可靠性。

### 架构债务和约束违规

**1. 单实例状态——根本性架构差距**

两个关键子系统（`internal/pkg/ratelimit` 和 `internal/hub`）保持纯进程内状态，没有跨副本协调。对于单副本部署，这没问题。在副本数 > 1 时，这会产生*静默错误行为*：

- **限流器**：`ratelimit.go` 使用 `map[string]*bucket` + `sync.Mutex`。没有任何东西阻止攻击者将 50 个 gRPC 请求发送到实例 A 和 50 个 HTTP 请求发送到实例 B——每个本地桶都允许。更糟糕的是，`Allow()` 的 `fail-open` 路径（`if l == nil || l.rate <= 0 ... return true`）意味着配置漂移（例如，滚动重启期间）可能会静默地使某些副本无限制。

- **SSE Hub**：`hub.go` 的 `Publish()` 调用 `pub(uid, ...)`，如果 `h.subs.Load(uid)` 在其他实例上返回 false，则什么也不做。订阅者仅在他们连接的实例上接收事件。`replayMissed` 路径在重连时通过数据库回填提供了部分缓解，但初始实时交付在有多个实例时存在缺陷。

**2. 搜索连接池耗尽**

`searchService.Search` 对于每种类型最多迭代 3 个种类，每个种类有 5 秒超时（最坏情况下共 15 秒）。由于 `MaxOpenConns` 通常为 25-50（来自 `config.yaml`），50 个并发搜索请求会耗尽连接池：50 个请求 × 每个请求最多 3 个 DB 连接 = 150 个并发查询，远远超过 50 个最大值。这会影响*所有*用户，而不仅仅是发起搜索的用户。

**3. 不完整的数据生命周期**

`StorageGCConfig` 和 `storage/gc/sweeper.go` 存在，但仅覆盖文件行和 blob。没有架构来清理：
- 软删除的消息（最终会无限期积累）
- 孤立的通知（用户删除后）
- 审计日志（TTL 后）
- file_shares 记录（引用被删除的文件）

没有整体数据导出端点和没有 SSO 用户删除回调，使得 GDPR 式的删除权（right to erasure）在架构上无法实现。这是一个合规风险，而不仅仅是技术债务。

**4. 依赖图中的一个细微违反**

`hub/adapter.go` 导入 `service` 以访问 `service.NotificationPusher` 接口。然而 AGENTS.md 说 "hub 可以导入 service；service 禁止导入 hub"。这违反了 AGENTS.md 定义的严格单向依赖规则吗？实际上*没有*——`hub` → `service` 是反向的：`service` 引入 `hub.Adapter` 作为 `NotificationPusher`，而 `hub` 适配器仅导入接口类型。依赖箭头仍然是 `service` ⊃ `hub`，适配器包不创建循环。这个设计模式（适配器在实现包中，而不是在接口包中）是正确的。

---

## 2. 扩展方向

### 方向 1：分布式限流（P0）

**为什么需要：** 当前每个实例的桶使得限流在副本数 > 1 时语义无效。攻击者可以简单地路由请求到不同的实例，每个都允许其本地突发。由于 `ratelimit.go` 在 `app.go:184-210` 中被注入到 HTTP 和 gRPC 传输中，跨两个协议分段请求会使差距加倍。在部署了 3 个副本且 `burst=40` 的情况下，一个攻击者可以在桶清空前发送 3 × 40 = 120 个请求。*这不是对配置错误的保护；这是对配置的含义的根本性破坏。*

**核心挑战：**
1. **精确性 vs. 性能**：分布式计数器需要原子操作（Redis `INCR` + 基于调用时钟的滑动窗口，或对于严格桶的 `lua` 脚本），这些操作会为每个请求增加一次 RTT。本地桶是零开销的。
2. **故障模式**：Redis 宕机——回退到每个实例的桶（降级保护）还是拒绝所有请求（安全但有害）？
3. **密钥基数**：对于每个经过身份验证的 UID（可能数十万）加上公共路由的客户端 IP，Redis 内存使用需要跟踪。Redis 的 `EXPIRE` 可以通过 TTL 自动处理淘汰，但由于 `MaxMemory` + `allkeys-lru` 驱逐策略，在重负载下存在驱逐非限流密钥的风险。

**选项 A：基于 Redis 的滑动窗口（推荐）**
- 每个请求的键：`rl:{uid}:{window}`（窗口 = 时间戳 // 窗口大小）
- 使用 `GET` + `INCR` + `EXPIRE`（原子性前缀，使用 Lua 脚本用于复合操作）
- 优点：精确，TLL 自动清理
- 权衡：每个请求额外 1-2ms 延迟；需要 Redis 作为强制依赖项

**选项 B：混合式（每个实例 + Redis 协调）**
- 每个实例本地桶 *N/B*（其中 N = 总速率，B = 实例数）
- 定期从 Redis 同步实际计数，通过滚动窗口调整本地桶
- 优点：低延迟，在 Redis 中断时优雅降级
- 权衡：在同步间隔内窗口边界存在轻微不精确

**选项 C：Postgres `pg_advisory_lock` + 计数器行**
- 表 `rate_limit_counters(uid, window_start, count)`，唯一约束为 `(uid, window_start)`
- `INSERT ... ON CONFLICT DO UPDATE SET count = rate_limit_counters.count + 1 WHERE count < burst RETURNING count`
- 优点：零新依赖
- 权衡：写入吞吐量受 Postgres 行锁争用的限制；比 Redis 慢约 10 倍

**对现有系统的影响：** `ratelimit.Limiter` 接口需要从 `Allow(key string) bool` 演变为 `Allow(ctx context.Context, key string) bool`。现有的每个实例实现可以保留为纯本地配置的后备。GORM 的 `idempotency.Store` 已经展示了如何在不破坏现有测试的情况下，在数据库支持的共享服务和每个实例的后备之间切换。

### 方向 2：跨副本 SSE（P0）

**为什么需要：** 在 N > 1 个副本时，订阅者仅在他们连接的实例上接收实时事件。`hub.go` 的 `pub()` 是一个静默无操作，用于在其他实例上的订阅者。`replayMissed` 路径重连时通过数据库查询提供了不一致的补救措施，但对于需要亚秒级交付的实时功能（消息送达回执、打字指示器），这会产生明显的 UX 差异。

**核心挑战：**
1. **发布开销**：事件必须从写入器实例广播到*所有*实例，每个实例再扇出到其本地订阅者。对于每个订阅者有 16 个缓冲槽的 `default` 丢弃容量，如果事件在实例之间延迟，则存在通道溢出丢失的额外风险。
2. **排序和去重**：如果实例 A 和 B 都从 Postgres 的相同 LISTEN 通道接收事件，它们各自*都会*尝试 `pub()` 到其本地订阅者。订阅者在多个设备上打开连接，可能连接到不同的实例——每个设备应获得一次事件。对于每个订阅者组的去重需要在事件上有一个序列 ID，以允许幂等消费。
3. **NOTIFY 的 8000 字节限制**（方向 5 对齐）：较大的通知负载必须由实例懒加载，而不是内联发送。

**预期架构变更：**

```
当前：
  服务 → 发件箱 → 调度器 → Hub.Publish → 每个实例 map[uid][]chan SSEEvent

             实例 A                   实例 B
          map[A][]chan              map[A][]chan
          （接收到事件）          （错过事件）

目标：
  服务 → 发件箱 → 调度器 → Hub.Publish → [PG LISTEN/NOTIFY] → 每个实例
                  ↓
             通知表                  Hub 本地订阅者
              （真相来源）
```

PG LISTEN/NOTIFY 是自然的选择，原因如下：
- 零新依赖（PostgreSQL 已经是强制性的）
- 订阅自动管理（LISTEN 和 UNLISTEN 在连接级别）
- 无需服务发现（PostgreSQL 是共享的）
- 通知是异步且轻量的

然而，NOTIFY 频道是集群范围的——每个监听数据库连接的实例都会收到每个通知。去重策略必须是：
1. **在通知负载中包含 `event_id`**（UUIDv7，单调递增）
2. **每个实例跟踪 `last_seen_event_id`**，跳过重复项
3. **使用幂等重放**：如果订阅者由于通道溢出而错过事件，他们重连时的 `replayMissed` 路径会通过数据库查询进行回填

**后备选项：原生 pgx LISTEN/LASYNC。** 当前代码库通过 `gorm.io/gorm` 使用 `pgx`，后者本身支持 `pgconn` 上的 LISTEN/NOTIFY。然而，GORM 的连接池管理与 LISTEN 的持久连接需求不兼容——`db.SqlDB()` 句柄属于一个受 `SetMaxOpenConns`/`SetMaxIdleConns` 管理的池。LISTEN 连接必须是一个专用的、长期存在的 `pgx.Conn`，*不与* GORM 池共享。正确的方法：

```go
// 在 app.go 中的 app.New() 内部：
pgConn, err := pgx.Connect(ctx, dsn)  // 专用连接，不在池中
_, err = pgConn.Exec(ctx, "LISTEN aero_events")
go func() {
    for {
        n, err := pgConn.WaitForNotification(ctx)
        // 扇出到本地 hub
    }
}()
```

### 方向 3：存储配额（P1）

**为什么需要：** 没有配额，单个用户可以写入文件直到物理存储耗尽。在 S3 中，这会产生云成本失控的暴露（拒绝钱包攻击）。`file.go` 中的 `maxBytes` 检查仅按文件限制，按跨所有文件的*总*使用量限制是缺失的。

**现有基础设施：** `storage_refs` 表（来自迁移 `000021`）和 `storageRefRepo = repository.NewStorageRefRepository(gdb)` 已经存在，跟踪每个存储键的引用计数。一个 `user_storage_usage(owner_uid TEXT PRIMARY KEY, total_bytes BIGINT)` 表可以用相同的发件箱事件保持同步——`file.created` 增量 + `file.deleted` 减量。`NotificationDispatcher` 展示了这种基于事件的聚合模式。

**核心挑战：**
1. **竞争条件**：使用 `SUM(size)` 在请求时检查配额在并发上传中存在 TOCTOU 问题。物化计数（通过发件箱事件保持同步）是准确的，但如果聚合落后，可能会允许短暂的超额。
2. **配额执行点**：在服务层的 `Create()` 中的配额检查意味着在检查之前流式传输文件并计算校验和——这是浪费的。检查应该在路由之前（对于 `PresignUpload`）或在 S3 上传回调中（对于直接上传）进行。
3. **超额策略**：当用户超过配额时——拒绝新的上传（硬限制），还是允许但标记软限制？

**建议：** 实现一个 `UsageRepository` 接口，由 `service.FileService` 在 `Create()` 和 `PresignUpload()` 中进行检查。发件箱事件 `file.created`/`file.deleted` 使物化计数器保持同步。对于 `PresignUpload`，检查可以在分配键之前进行——无争议。

### 方向 4：DDoS/滥用保护（P1）

**为什么需要：** 搜索的每个种类 5 秒超时 + 每个请求最多 3 个种类 = 每个搜索请求最多 15 秒的 DB 时间。通过连接池耗尽（`MaxOpenConns=50`，50 个并发搜索请求），这会演变为所有用户的拒绝服务。此外，SSE 没有每个 UID 的连接限制——一个用户打开 10,000 个 WebSocket 等效连接，每个连接消耗一个 goroutine 和一个 16 个槽位的通道缓冲区。

**核心挑战：**
1. **搜索需要隔离**：搜索查询应该使用一个*单独的连接池*或一个显式的 `SET statement_timeout = '5s'` 连接设置来防止池污染。为搜索操作设置单独的 `MaxOpenConns=5` 可以防止一个用户的行为使所有其他用户无法进行简单的数据库查询。
2. **SSE 连接预算**：每个实例需要一个每个 UID 的连接上限。该限制可以是全局的（所有实例上的 `max_connections_per_uid=10`），通过 Redis 实现，或每个实例的（`max_connections_per_uid_per_instance=5`），不需要协调。鉴于 SSE 的预期用例（消息通知），每个用户的每个实例 5 个连接可能就足够了。
3. **对非认证路由进行 IP 级限流**：`/files/dl`（HMAC 签名的公共路由）当前在无认证的 `Public` 组中路由。此路由上的限流退化到每个 IP 的桶，但 ILIKE 搜索路径是认证的，按 UID 限流。慢速/昂贵的路由（搜索、批量操作）应该有一个*单独的、更严格*的限流器桶，与常规请求桶分开。

**建议：**
1. 为搜索添加一个 `LimitedPool` 包装器（`MaxOpenConns=5`，`statement_timeout='5s'`），这样搜索查询不会从主池中获取连接。
2. 向 `hub.Hub` 添加每个 UID 的连接预算：`Subscribe()` 检查 `uid → count` 映射（对于跨副本协调是可选的 Redis）。
3. 添加路由分类：`POST /v1/search` 获得一个专用的限流器，速率低于常规 API 路由。

### 方向 5：数据生命周期和合规性（P2）

**为什么需要：** 没有用于消息/评论/通知的整体 GC 策略，软删除的行无限期增长。表最终会膨胀，使索引扫描变慢并增加存储成本。对于拥有数百万用户的平台，这不仅仅是维护问题——这是一个合规风险（GDPR 删除权）。

**现有基础设施：** `storage/gc/sweeper.go` 展示了一种适用于所有实体的模式：
1. `ListSoftDeletedBefore(ctx, cutoff, batch)`
2. `HardDelete(ctx, ids)`
3. 删除级联的实体（评论、共享、通知）

**核心挑战：**
1. **级联删除**：删除一个文件应该（最终）删除其共享记录、链接的评论和引用它的通知。软删除域行，然后通过发件箱事件异步清理级联记录，是正确的方式——但发件箱没有为所有实体类型注册 `delete` 事件处理程序。
2. **SSO 同步**：当 SSO 删除一个用户时，aero-id 需要回调以触发级联删除。当前 `sso/client.go` 没有 `DeleteUser` RPC。添加一个 SSO → aero-id gRPC 回调端点（或在 SSO 中使用发件箱模式）是必需的。
3. **数据导出**：需要导出的端点（`GET /v1/export`），返回用户的数据消息、文件和作为结构化 archive 的评论。这需要一个后台作业（受发件箱模式影响）来积累数据，而不是同步地收集它（对于大型用户来说可能太大而无法在单个 HTTP 响应中处理）。

**建议：** 将 GC sweeper 模式扩展到 `MessageRepository`、`CommentRepository` 和 `AuditRepository`。为每个实体类型添加 `ListSoftDeletedBefore` + `HardDelete` + 级联清理，由新的 `DataLifecycleService` 管理，生命周期策略由配置驱动（每个实体类型的保留 TTL）。

---

## 3. 接口设计原则

### 当前的优势

在 `service/service.go` 中定义的服务接口（`FileService`、`MessageService`、`CommentService` 等）是文档化的、完整的和行为一致的。它们在 HTTP 和 gRPC 处理程序之间共享，确保行为统一。这是一个有意的设计决策，不应更改。

`repository` 接口也定义良好，但有一个细微的弱点：`FileRepository` 接口声明了 12 个方法，比单一职责原则建议的要多。从职责角度看：
- 6 个 CRUD 方法（Create、Get、ListByOwner、Delete、UpdateMetadata、FindByChecksum）
- 2 个搜索方法（SearchFiles、SearchFilesLike）
- 2 个存储键方法（CountByStorageKey、CountAnyByStorageKey）
- 2 个 GC 方法（ListSoftDeletedBefore、HardDelete）

这些代表着不同的关注点，应该被拆分：

```go
// 文件 CRUD
type FileCRUDRepository interface { Create, Get, ListByOwner, Delete, UpdateMetadata, FindByChecksum }

// 文件搜索
type FileSearchRepository interface { SearchFiles, SearchFilesLike }

// 文件存储键管理
type FileStorageKeyRepository interface { CountByStorageKey, CountAnyByStorageKey }

// 文件生命周期
type FileLifecycleRepository interface { ListSoftDeletedBefore, HardDelete }
```

这对功能行为没有直接影响，但它：
- 降低了任何具体实现的认知负荷
- 使完整文件存储库更易于测试（更小的 mock）
- 遵循 AGENTS.md 的每个文件单一职责限制

### 新的抽象层

1. **`storage.UsageQuota`**：这个接口可以跨服务层使用：

```go
type UsageQuota interface {
    Check(ctx context.Context, ownerUID string) (remaining int64, err error)
    Reserve(ctx context.Context, ownerUID string, delta int64) error
    Release(ctx context.Context, ownerUID string, delta int64) error
}
```

这抽象了基于 Postgres 或基于 Redis 的配额跟踪，允许在引入 Redis 作为依赖时进行无缝切换。

2. **`service.NotificationPusher` 已经存在**且设计良好。不需要更改。

3. **`idempotency.Store`** 已经正确抽象。不需要更改。

4. **`outbox.Handler`** 目前是一个单一方法接口。考虑演变为一个*处理程序链*是一个已经存在的模式（`outbox.NewChain` 包装 `NotificationDispatcher` 和 `WebhookHandler`）。这个链模式应该转化为所有发件箱消费者的一等支持。

### 向后兼容性

- 任何限流器接口更改（`Allow` → `Allow(ctx, key)`）应该通过添加新方法同时保留旧方法来引入，并带有弃用标记。
- 当前的每个实例限流器在添加 Redis 后应该通过配置保留作为后备。
- SSE 端点应该保持向后兼容：如果跨实例交付尚未部署，只连接到一个实例的客户端仍然可以正确运行（仅在一个分割中错过实时事件）。

---

## 4. 技术选型

### 新的依赖项评估

**Redis**（用于方向 1 和 4）

| 标准 | 评估 |
|---|---|
| 团队熟悉度 | 假设未知——需要操作培训 |
| 运维开销 | 中等（需要持久化配置、监控、备份） |
| 故障影响 | 高（限流失效）——需要降级到每个实例模式 |
| 替代方案 | Postgres（用于速率计数）、无（用于分布式桶） |
| 版本要求 | >= 6.2（支持 `EXPIRE` 和 Lua 脚本） |

对于分布式限流的 Redis 与 Postgres 的决策：

| 操作 | Redis（Redis 6.2+） | Postgres（15+） |
|---|---|---|
| 每个请求延迟 | ~0.5ms（本地网络） | ~2-5ms（包括 TCP + SSL） |
| 吞吐量 | ~100K ops/s（单线程） | ~5K ops/s（行锁争用） |
| 自动淘汰 | `EXPIRE` 或 `allkeys-lru` | `pg_cron` 或应用级清理 |
| 故障模式 | 回退到每个实例 | 回退到每个实例 |
| 运维负担 | 新的数据存储 | 重用现有 Postgres |

**结论：** 对于方向 1（限流），Redis 是正确的方式——因为速率检查在每个已认证的请求路径上，每个请求的延迟很重要，Postgres 的行级锁在工作负载下会成为瓶颈。对于方向 2（SSE 广播），Postgres LISTEN/NOTIFY 是正确的——零新依赖，并且基于数据库的交付与发件箱模式和谐地协同。

**PG LISTEN/NOTIFY 的 pgx 升级：** 当前代码库使用 `gorm.io/driver/postgres`，它构建在 `pgx v5` 之上。然而，GORM 的连接池管理不允许专用的 LISTEN 连接。所需的更改是添加一个独占的 `pgx.Conn` 用于 LISTEN，同时在所有其他方面继续使用 GORM。这个 `pgx.Conn` 应该在 `app.go` 中创建，与 `gdb` 分开。

### 自建与采购

对于方向 3（存储配额），自建显然是正确的方式——领域逻辑特定于 aero-id 的域模型，没有通用的现成解决方案能完全匹配。

对于方向 5（数据生命周期），自建，遵循 `storage/gc/sweeper.go` 的模式。

对于方向 4（DDoS），*架构模式*是自建的（特定于应用层的限制），但*后端*（用于协调限流的 Redis）可以是开源的，而不是需要 SaaS 的东西。

---

## 5. 实施路线图

### 优先级：P0（核心可靠性的阻塞项）

| 阶段 | 方向 | 工作内容 | 估计 |
|---|---|---|---|
| 阶段 1.1 | 方向 2——跨副本 SSE | 在 `app.go` 中添加专用的 `pgx.Conn`，注册为 LISTEN `aero_events`。在 `hub.go` 中实现去重（每个 UID 跟踪 `last_event_id`）。通过带有可重放负载的事件专用通道传输 Notifier。保留现有的 `Publish()` 作为回退。 | 3-5 天 |
| 阶段 1.2 | 方向 1——分布式限流 | 实现 `redisLimiter`（`Allow(ctx, key) bool` 带有滑动窗口）。创建 `Limiter` 接口，包装现有的每个实例实现和新的 Redis 实现。添加配置：`rate_limit.backend: "local" | "redis"`。为 Redis 中断添加降级（回退到每个实例）。 | 3-5 天 |
| 阶段 1.3 | 方向 4——搜索池隔离 | 添加 `LimitedDB` 包装器（`MaxOpenConns=5`，`statement_timeout='5s'`）。注入搜索仓库而不是主 GORM 实例。为 SSE 添加每个 UID 的连接预算。 | 2-3 天 |

**阶段 1 风险：** Redis 成为强制依赖项（如果选择方向 1 的 Redis 后端）。缓解措施：`rate_limit.backend: "local"` 是默认值，Redis 是显式选择加入的。团队需要 Redis 运维知识。

### 优先级：P1（可扩展性和成本控制）

| 阶段 | 方向 | 工作内容 | 估计 |
|---|---|---|---|
| 阶段 2.1 | 方向 3——存储配额 | 添加 `user_storage_usage` 表。实现 `UsageQuota` 接口。在 `fileService.Create()` 和 `PresignUpload()` 中连接。通过 `file.created`/`file.deleted` 发件箱事件保持同步。 | 4-6 天 |
| 阶段 2.2 | 方向 4——搜索速率路由 | 添加路由分类器（`search`、`bulk`、`regular`），每个都有独立的限流桶。为搜索添加更严格的默认值（`burst=5`，`rps=2`）。 | 2-3 天 |

**阶段 2 风险：** 限流过严导致合法搜索偶尔出现误报。缓解措施：仔细调整默认值，添加断路器以在连续 N 次丢弃后恢复为总是允许。监控仪表板。

### 优先级：P2（合规性和运营）

| 阶段 | 方向 | 工作内容 | 估计 |
|---|---|---|---|
| 阶段 3.1 | 方向 5——不完整的数据生命周期 | 为消息、评论、通知和审计日志将 GC sweeper 模式扩展到 GC sweeper。添加级联删除影响（共享、通知、评论）。添加基于配置的保留策略（每个实体类型不同的 TTL）。 | 5-8 天 |
| 阶段 3.2 | 方向 5——SSO 删除回调 | 将 `DeleteUser` RPC 添加到 `sso/client.go`（或添加 SSO → aero-id gRPC 回调端点）。实现级联删除处理程序。 | 3-4 天 |
| 阶段 3.3 | 方向 5——数据导出 | 添加 `GET /v1/export` 端点。实现后台作业以积累用户的文件、消息和评论作为结构化 archive。 | 4-5 天 |

**阶段 3 风险：** 级联删除可能过于激进——硬删除用户数据可能会破坏其他用户看到的对话线程。缓解措施：对于用户的内容，使用匿名化（用 `[deleted]` 替换主体）而不是硬删除。

### 阶段 1 至 3 的跨阶段依赖性

```
阶段 1.1（PG LISTEN/NOTIFY）← 阶段 2.1（存储配额）在发件箱事件上无依赖
阶段 1.2（Redis 限流器）  ← 阶段 2.2（路由分类）无依赖，但共享限流器基础设施
阶段 1.3（搜索池隔离）    ← 阶段 2.2（搜索速率路由）共享搜索基础设施
阶段 3.1（GC sweeper 扩展） ← 阶段 1.1（PG LISTEN/NOTIFY）无依赖，但共享 db 基础设施

阶段 3.2（SSO 删除回调）  ← 阶段 3.1（GC sweeper）级联删除依赖于 GC 基础设施
阶段 3.3（数据导出）      ← 阶段 3.1（GC sweeper）导出定义在删除之前需要知识
```

关键路径是阶段 1.1 → 阶段 1.2 → 阶段 1.3，因为 P0 之间没有依赖关系，它们可以并行进行。

---

## 架构总结

aero-id 的架构具有坚实的基础——整洁的分层结构、深思熟虑的出站模式实现，以及清晰的关注点分离。五个已识别的方向代表了在单副本架构上构建时的自然演变，所有的*静默错误*模式（每个实例限流、SSE 交付丢失）在从副本数 1→N 过渡时才会显现。

我对代码库验证的审查确认了所有五个方向的有效性，并强调了两个关键盲点：

1. **方向 4 的搜索连接池耗尽**——比最初声称的更严重，因为它通过池饥饿产生全站点拒绝服务，而不是局部的表扫描。
2. **方向 1 的 fail-open 路径**——`Allow()` 对 nil 接收器的防护很安静，如果配置在滚动重启期间不同步，可能允许无限的吞吐量。在限流器上线之前，应添加一个启动时的 nil/配置有效性检查。

最令人印象深刻的架构资产是发件箱模式：它不仅提供了可靠的事件交付，还提供了大多数这些方向可以基于其构建的框架。方向 2（PG LISTEN/NOTIFY）是发件箱→数据库→跨实例交付管道的自然扩展。方向 3（配额）可以在发件箱事件上建模，方式与 `NotificationDispatcher` 在消息事件上建模的方式相同。方向 5（生命周期）可以基于发件箱的保证来协调级联删除。统一这些方向的原则是简单的：**共享的可变状态属于数据库**，而速率限制器是明确的例外，因为每个请求的延迟要求需要一个补充的数据存储。
