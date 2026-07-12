Now I have a thorough understanding of the architecture. Let me produce the analysis.

---

# 架构分析报告：aero-id 五个扩展方向

## 1. 架构评估

### 1.1 现有架构优势

aero-id 的横向分层架构（handler → service → repository/storage/sso）整体设计质量很高，有以下几个突出的设计优点：

**优势一：严格的单向依赖和清晰的职责边界**

```text
handler/{http,grpc}  →  service  →  repository  →  Postgres
                                     ├──→  storage (local | s3)
                                     └──→  sso (gRPC client)
```

- Handler 层仅做**传输层 I/O**：解码请求、调用 service、渲染响应——零业务逻辑。这在 HTTP handler（`internal/handler/http/file.go`）和 gRPC handler（`internal/handler/grpc/comment.go`）中体现一致。
- Service 层是所有业务逻辑的**唯一聚合点**。`service.go` 中定义的 `FileService`、`MessageService`、`CommentService` 等接口是两个传输层共享的契约，确保了行为一致性。
- Repository 层返回原始 GORM 错误，service 层统一翻译为 `*AppError`——这一约定消除了跨层错误泄漏。

**优势二：领域模型是贫血的但恰如其分**

`domain/` 下的实体（`File`, `Message`, `Comment`, `Notification`）是纯数据容器 + 少量构造工厂函数，没有 ORM 标签侵入。`db.go` 中的 `*Row` 类型独立映射，`rowToDomain` / `domainToRow` 转换函数隔离了数据库 schema 与领域模型。这层间接成本虽小但价值极大——schema 变更不影响领域逻辑测试，领域逻辑变更不影响数据库映射。

**优势三：事务性 outbox 模式实现优雅**

`outbox.Relay` 周期轮询未发布事件，通过 `Handler` 接口（`Chain` + `NotificationDispatcher` + 可选的 `WebhookHandler`）扇出。事件在业务事务内写入（repository 层与 outbox 同写一个 `gdb` 连接），保证了**至少一次语义**。这是异步化扩展的正确基础。

**优势四：缓存策略务实**

SSO 客户端采用两级缓存——`TokenCache`（TTL 60s）和 `UserCache`（TTL 300s）+ 后台清扫 + `singleflight` 合并并发请求。`StaleTTL` 机制允许 SSO 不可用时提供最后已知身份，提高了可用性。断路器（`breaker`）进一步防止 cascading failure。

### 1.2 关键设计决策合理性评估

| 决策 | 合理性 | 评价 |
|------|--------|------|
| 账户不存本地 | ✅ | 与 AGENTS.md 一致，SSO 是 identity source of truth |
| 单 DB 连接池共享 | ⚠️ 现有架构合理，但有扩展风险 | 单个连接池被 repo/outbox/idempotency/GC/health check 共享。在流量增长时，outbox 轮询和 GC 扫描可能消耗连接池资源，影响用户请求延迟 |
| 无批量 API | ⚠️ 当前阶段合理，长期需要 | 38 个 RPC 全单体操作。对于消息/通知的"标记全部已读"已有 `MarkConversationRead` 和 `MarkAllRead`，但缺少如批量删除文件/消息、批量修改可见性等操作 |
| GENERATED ALWAYS AS STORED FTS | ✅ | 数据库自动维护 tsvector，消除了应用层不一致窗口 |
| 评论仅两层嵌套 | ✅ | 平衡了业务需求与查询复杂度（楼中楼模式） |
| 软删全表 | ⚠️ 有合理理由，但需注意 | 软删加 `WHERE deleted_at IS NULL` 的索引设计对查询性能有影响 |
| 无 hot-reload / 无运行时可调整 | ✅ YAGNI 合理 | 但运维友好性值得评估——日志级别、限流参数在生产中常需动态调整 |

### 1.3 已识别架构债务

**债务一：连接池未隔离——优先级 P1**

当前 `app.go:65-69` 一个 `gdb`（`*gorm.DB`）传递给所有组件。当 outbox relay 在 5s 轮询间隔内遇到大量待消费事件时，可能耗尽连接池，导致用户请求等待数据库连接。同样，Storage GC sweep 的批量查询也可能干扰正常流量。

**建议方案**：按**优先级分池**——用户请求使用 `max_open_conns` 的 70% 的连接池，outbox/GC/定时任务使用独立的低优先级池（剩余 30% + 可选单独的最小连接数）。PostgreSQL 侧的 `pgbouncer` 事务池也可以缓解，但增加运维复杂度。

**债务二：FTS 索引重建能力缺失——优先级 P1**

文档已验证：`GENERATED ALWAYS AS STORED` 的列在索引表达式变更后**不自动重算历史数据**。当前无任何重建 API 或管理命令。场景：
- migration 扩展 `search_vector` 表达式（如 files 表从 `name` 扩展到 `name || meta->>'description'`）
- 批量数据修复后需要重新索引
- 管理员希望重算所有旧行以匹配新配置

现有方案 `ALTER TABLE ... ALTER COLUMN ... TYPE tsvector USING search_vector::tsvector` 会锁全表。生产环境需要**在线重建**——分批 UPDATE  + 排他锁窗口极小化。

**债务三：配置无热加载——优先级 P2**

`config.Load()` 一次性读取后 unmarshal，无 `WatchConfig`。以下参数在生产中常需无重启调整：
- 日志级别（debug → info → error）
- 速率限制（RPS/Burst）
- SSO 端点/超时（迁移期间）
- 存储 GC 间隔（压测时临时关掉）

重构方向：引入**配置注册表**——每个可热加载的参数注册到 `config.Registry`，goroutine 监听信号（SIGHUP）或定时重读，参数更新推送给订阅者（logger、rate limiter 等）。

**债务四：搜索架构缺少跨实体聚合——优先级 P2**

现有 `SearchService.Search()` 按 kind 顺序查询三个仓库（messages → comments → files），每个有独立超时（5s），最大总延迟 15s。没有并行化，没有评分/排序/去重。当数据量增长后，ILINE fallback（CJK 场景）的 `LIKE '%query%'` 扫描将不可接受。

## 2. 高价值扩展方向

### 方向一：批量操作 API（Batch Operations）

**业务价值**
私信、文件管理、通知等场景中用户频繁执行重复操作（删除多封邮件、批量标记已读、批量私信特定群组）。逐个调用 REST/gRPC 导致：
1. N 次 RTT 的网络开销
2. N 次 token 校验
3. N 次事务开销
4. 客户端实现复杂（必须自己处理部分失败）

现有 `MarkConversationRead` 和 `MarkAllRead` 是单向度批量的雏形，但不够系统化。

**核心挑战**
1. **部分失败语义**——批量操作中单条失败不应回滚全部。需要 `BatchResult` 模式：返回每个子操作的 status/code/error。
2. **原子性与性能权衡**——全成功快的场景用单事务；大批量（如 1000+）需分片事务。
3. **请求体大小限制**——HTTP 和 gRPC 都有默认限制（gRPC 4MB, HTTP 受 `read_timeout` 限制）。

**架构变更**
- Service 层新增 `BatchResultItem` DTO 类型，携带 `index` + `status` + `error`（可选）+ `data`（可选）
- 每个 Service 接口扩展 `Batch*` 方法（如 `FileService.BatchDelete(ids []ID) []BatchResultItem`）
- Repository 层按需提供批量 SQL（`WHERE id IN (?)` 或 `UPDATE ... WHERE id = ANY(?)`）
- Outbox 事件：批量操作产生**多条**事件而非一条"批量事件"，保持下游消费者无知

**影响范围**
- 核心文件：`service/service.go`（新增类型）、`service/file.go`（新增方法）、`handler/http/file.go`（新增路由）、`handler/grpc/file.go`（新增 RPC）
- Proto：新增 `BatchDeleteFilesRequest/Response` 等消息

**对现有系统影响**：低——新 API 不影响现有单体 API 语义。但需要统一"批量操作与 outbox 交互"的约定（每条子操作发独立事件 vs 发聚合事件）。

**实现方案选项**

| 方案 | 优点 | 缺点 |
|------|------|------|
| A: 单事务全成功或全失败 | 语义简单 | 大数量锁冲突、部分失败无法处理 |
| B: 每条子操作独立事务 | 支持部分失败 | 单个请求产生 N 个事务，性能损耗 |
| C: 分批事务（每 100 条一个事务） | 平衡性能与部分失败 | 边界情况（跨批原子性）需文档化 |

**推荐**：方案 B 作为默认，Server-Side Streaming gRPC 逐条返回结果；HTTP 返回完整 `BatchResult` 数组。关键的操作（如批量删除文件）应考虑操作顺序——先检查所有权限，再执行删除，将权限错误与执行错误区分。

### 方向二：搜索索引在线重建 + 索引扩展（Search Index Rebuild）

**业务价值**
当前 FTS 使用 `GENERATED ALWAYS AS ... STORED` 依赖数据库自动维护，缺乏运维手段。随着业务数据增长，以下需求频繁出现：
1. 索引表达式变更后重建历史数据（如文件搜索从只搜名称扩展到搜 meta 描述）
2. FTS 配置调整后的批量重索引（如自定义词典、词干分析器配置）
3. 脏数据修复后的重新索引
4. 管理员手动触发以解决索引碎片问题

**核心挑战**
1. **在线 vs 离线**——`ALTER COLUMN ... TYPE tsvector USING` 会锁表，生产环境不可接受
2. **大表分批**——百万行级表的重算需要分页处理，每批 UPDATE 后暂停以避免冲击
3. **与现有 FTS 查询的互斥**——重算期间的查询一致性（读旧数据还是新旧混合）

**架构变更**
- Repository 层新增 `RebuildFTSIndex(ctx, table string, batchSize int) (total, updated int64, err error)` 方法
- AdminService 新增 `RebuildSearchIndex(ctx, target string) error` RPC（需要 admin 权限）
- 后台异步任务模式——重索引作为单次 job 运行，通过 outbox 或独立 goroutine 执行

**注意**：`GENERATED ALWAYS` 列无法直接 `UPDATE` 更新——需要：1) 临时将列改为非 GENERATED，2) 手动更新，3) 恢复 GENERATED 定义。更好的做法是使用**触发器**而非 GENERATED 列：在 `BEFORE INSERT OR UPDATE` 上设置 `NEW.search_vector = to_tsvector(...)`，这样管理员可以直接 `UPDATE` 行触发重新计算。

**预计变更**
- `migrations/`：新增 migration 将 GENERATED 列改为触发器维护
- `internal/repository/db.go`：新增 `rebuildFTSIndex(ctx, table)` 方法
- `internal/service/admin.go`：新增 `RebuildFTSIndex` 方法
- `internal/handler/{http,grpc}/admin.go`：新终端点

### 方向三：读写连接池隔离 + 查询路由（Connection Pool Isolation）

**业务价值**
当前单连接池被多个子系统共享。随着流量增长，以下问题会显现：
1. `outbox.Relay` 轮询（默认 5s 间隔，每次 `FetchUnpublished` 查询）在事件积压时可能占用多连接
2. Storage GC 的批量查询和更新争抢连接
3. `idempotency.RunCleaner` 每小时的清扫
4. 高流量下的慢查询（如复杂 FTS 查询 + ILIKE fallback）阻塞连接池

当 max_open_conns=20 时，outbox + GC + idem cleanup 可能占用 3-4 个连接，剩下 16 个给用户请求。在突发流量下这个比例会恶化。

**核心挑战**
1. **GORM 不支持多连接池切换**——一个 `*gorm.DB` 实例绑定一个 `*sql.DB`。需要多个 GORM 实例。
2. **事务一致性**——outbox 事件须与业务行在同一个连接写入（保证 at-least-once）。如果 outbox 用独立池，事务无法跨池传播。
3. **运维复杂度**——多连接池需要配置暴露和监控（每个池的活跃/空闲连接数）。

**架构变更**

```text
                     ┌──────────────────┐
                     │  Primary Pool     │ ← 80% max_open_conns
                     │  (user requests)  │   用于 HTTP/gRPC handler 调用的 repo 操作
                     └────────┬─────────┘
                              │
                     ┌────────▼─────────┐
                     │  Background Pool  │ ← 20% max_open_conns
                     │  (outbox/GC/idem) │   最低连接数保障不饿死
                     └────────┬─────────┘
                              │
                     ┌────────▼─────────┐
                     │  Admin Pool       │ ← 独立配置，用于迁移/重建索引等重操作
                     │  (migrations/     │
                     │   index rebuild)  │
                     └──────────────────┘
```

- `app.go`：创建两个 `*sql.DB` 连接池（primary/background），分别绑定到不同的 GORM 实例
- 关键点：outbox.Event 的写入必须在业务事务中用 **primary pool**，但 outbox.Relay 的 `FetchUnpublished`/`MarkPublished` 可以用 background pool（这些不需要与业务事务绑定）。这需要对 outbox 提供双 DB 句柄。
- Repository 的构造函数接收 primary `*gorm.DB`，Repository 接口可能增加 `WithBackgroundDB(db *gorm.DB)` 用于后台操作。

**对现有系统影响**：中等。需要确保 outbox 写入与业务行在同一事务。现有 `outbox.GormRepository` 和正常 repo 共享同一个 `gdb` 指针即可——两者都用 primary pool。outbox 的读取/清扫则可用 background pool。

### 方向四：Typing / Presence 实时状态（Real-time Presence）

**业务价值**
私信场景中，发送前的"对方正在输入"提示和在线状态是 IM 应用的标准期望。缺失此能力使得 aero-id 在用户感知上"不够实时"。

但是 **AGENTS.md 明确 SSO 拥有身份**——"aero-id stores no account records of its own"。这意味着 presence 状态不能与任何账户记录关联，必须是**瞬态的、进程内的**。

**核心挑战**
1. **无持久化**——presence 状态不能落库（因为没有 accounts 表）。只能是内存中的、可丢失的状态。
2. **水平扩展**——如果部署多个 aero-id 实例，presence 状态需要共享。内存方案无法跨节点。
3. **SSE 上行缺失**——当前 EventsHandler 仅支持下行 SSE。要实现 typing 指示，需要 WebSocket（双工）或 HTTP/2 Server Push 并配合上行通道。

**架构变更**
- 新增 `internal/presence/` 包：`Manager` 接口 + `local` 实现（`sync.Map[*uid]*lastSeen`）+ 可选 `redis` 实现（用于多实例共享）
- EventsHandler 增加 `POST /events/typing`（HTTP）或 WebSocket 端点，接收客户端的 typing 事件
- `MessageService.Send` 在发送前检查接收方是否在线（通过 `PresenceManager.IsOnline(uid)`）
- SSE 消息增强：增加 `typing` 事件类型，推送 `{from_uid, conversation_id}` 给接收方

**技术选型权衡**

| 方案 | 持久性 | 跨实例共享 | 复杂度 |
|------|--------|-----------|--------|
| A: 纯内存 | 进程重启/GC 丢失 | ❌ | 低 |
| B: Redis Pub/Sub | 依赖 Redis | ✅ | 中 |
| C: Redis + PERSIST | 崩溃后恢复 | ✅ | 高（需要 TTL） |

**推荐**：方案 A 作为起点（因为 aero-id 当前设计原则明确 YAGNI 掉分布式缓存），presence 标记为 **best-effort**——不保证准确，丢失后客户端通过 SSE 重连自动恢复。生产多实例时再切换到基于 Redis Pub/Sub 的方案 B。

**对现有系统影响**：低。独立包，不影响现有 service 层。跨实例时需要引入 Redis 依赖——这是第一个外部基础设施依赖（当前 SSO 已有 gRPC 依赖）。

### 方向五：可插拔的配置热加载与观测体系（Config Hot-reload + Observability）

**业务价值**
生产运维中，"改个日志级别查问题就需重启"的体验很差。结合 OpenTelemetry tracing 的启用/禁用，可以实现"先开启跟踪捕获问题，再关闭避免开销"的运维模式。

**核心挑战**
1. **线程安全的状态迁移**——rate limiter 的 RPS 动态调整需要重置现有的 token bucket，不能导致并发 `Allow()` 读脏数据
2. **原子更新**——一组配置（如 rate_limit 的 rps + burst + idle_ttl 三个值）需要一次性应用
3. **Watch 粒度**——viper 的 `WatchConfig` 只支持文件变更（fsnotify），K8s ConfigMap 挂载更新触发 inotify 但可能有延迟

**架构变更**

```go
// 配置注册表模式
type ConfigSubscriber interface {
    OnConfigChange(cfg *Config)
    ConfigKey() string // "rate_limit", "log", "tracing"
}

type Registry struct {
    subscribers map[string][]ConfigSubscriber
}

func (r *Registry) Register(s ConfigSubscriber)
func (r *Registry) Reload(cfg *Config) // 逐个通知订阅者
```

- `config.Load()` 保留启动参数，新增 `config.StartWatcher(ctx, interval)` 定时或 fsnotify 触发重读
- 每个可热加载的参数实现 `ConfigSubscriber` 接口：
  - `ratelimit.Limiter`：新增 `SetRate(rps float64, burst int)` 方法，创建新桶（旧桶优雅老化）
  - `logger`：新增 `SetLevel(level string)` 方法，使用 `zap.AtomicLevel`
- SIGHUP 信号处理：`app.Run()` 中监听 syscall.SIGHUP，触发配置重载

**对现有系统影响**：中。需要修改 `ratelimit.Limiter` 的不可变设计（当前无 `SetRate` 方法），这是违反原设计原则的变化。需要仔细评估安全性——是否允许**临时**修改导致意外放开限流。

## 3. 接口设计建议

### 3.1 Service 接口设计原则

当前 `service.go` 中定义的接口（`FileService`, `MessageService`, `CommentService` 等）是直接映射到 use case 的。这些接口设计有两个原则值得坚持：

**原则一：接口细粒度与实现粗粒度**

当前每个 service 接口是一个组合体——包含 CRUD + 业务操作（如 `CommentService` 包含 Like/Unlike/Report/ResolveReport/Hide/Unhide）。接口方法数量在 10-15 个之间，处于合理范围。不应为了"纯净"将 `CommentService` 拆为 `ReadCommentService` / `WriteCommentService`——这会增加 wiring 复杂度而没有实际收益。

**原则二：返回领域对象而非 DTO**

Service 方法返回 `*domain.File`、`*domain.Message` 等**领域类型**，而非传输专用的 DTO。Handler 层负责转为 JSON/Proto 响应。这确保了 service 层的可测试性——测试可以直接断言领域对象字段，无需了解传输格式。

### 3.2 新的抽象层引入判断

**需要引入的抽象**：

1. **`BatchResult` 泛型类型**——`service.BatchResult[T any]`，包含 `Index int`, `Data *T`, `Error *AppError`。统一所有批量操作的返回模式，降低错误处理复杂度。

2. **`presence.Manager` 接口**——抽象实时状态管理。当前不需要抽象，但在第二实例部署时抽象立即产生价值。

3. **`index.Rebuilder` 接口**——统一不同实体的索引重建操作：
   ```go
   type IndexRebuilder interface {
       Rebuild(ctx context.Context, entity string, batchSize int) (int64, error)
   }
   ```

**不需要引入的抽象**：
- 不需要"异步 job 框架"——outbox 已经提供了事务性的事件驱动模式，单次任务（如索引重建）可以用 goroutine + 状态报告，不需要通用 worker 框架
- 不需要"多租户抽象"——当前无明确业务需求
- 不需要"事件总线"抽象——`hub.Hub` + `outbox` 已经覆盖实时推送和持久化事件两个维度

### 3.3 向后兼容性策略

| 变更类型 | 策略 | 示例 |
|----------|------|------|
| 新增 API | 直接新增，不影响现有 API | 新增 `BatchDelete` RPC |
| 新增请求字段 | proto3 所有字段 optional，新增 default-value 字段安全 | 搜索请求加 `SearchRequest.rebuild` |
| 修改响应 | 新字段不会破坏旧客户端（忽略未知字段） | 通知加 `DeliveredAt` 字段 |
| 废弃接口 | 保留但标记 deprecated + log warning | 在下一大版本移除 |
| 修改 FTS 列类型 | 需要双写 migration + 应用层兼容 | 见方向二讨论 |

## 4. 技术选型

### 4.1 需要引入的新依赖

| 依赖 | 必要性 | 优先级 | 替代方案 |
|------|--------|--------|---------|
| Redis | 跨实例 presence（方向四） | P2 | 纯内存 + best-effort |
| Redis | 分布式限流（跨实例共享状态） | P2 | 维持 per-instance 限流 + 靠负载均衡分散 |
| pgvector | 语义搜索 | P3 | 外部 embedding 服务 + ELK 做向量搜索 |
| ELK / Meilisearch | 专用搜索引擎替换 FTS | P3 | 维持 PostgreSQL FTS + ILIKE fallback |

**当前不推荐引入的新依赖**：Redis 只在多实例 presence 场景产生刚性需求。维持 per-instance 限流和内存 presence 对大多数部署来说足够。搜索方面，PostgreSQL FTS 加上 `GIN` 索引和 `GENERATED ALWAYS` 在千万级数据量内表现可接受。

### 4.2 自建 vs 采购/集成

| 能力 | 决策 | 理由 |
|------|------|------|
| 搜索索引重建 | **自建** | 专为 GENERATED ALWAYS 列设计，PostgreSQL 原生能力，无外部依赖 |
| 配置热加载 | **自建** | Go 标准库 + viper WatchConfig / fsnotify 已足够轻量 |
| 实时 Presence | **自建**（内存实现）+ 可选 Redis | 不需要专门的消息服务器（如 Socket.IO 的额外成本） |
| 批量操作 | **自建** | 业务定义的批量语义，无成熟库可以替代 |

### 4.3 批量操作——gRPC Server-Side Streaming vs HTTP Chunked

批量操作中，HTTP 和 gRPC 的返回策略不同：

| 传输 | 推荐策略 | 理由 |
|------|---------|------|
| gRPC | **Server-Side Streaming** | `rpc BatchDeleteFiles(BatchDeleteFilesRequest) returns (stream BatchDeleteResult)` — 逐条返回，客户端实时获知进度 |
| HTTP | **Chunked JSON Array** 或 **200 + `BatchResult` JSON** | HTTP/1.1 不支持流式 JSON（除非 Chunked transfer encoding）。推荐：返回完整 `BatchResult[]` 结构，size 有限的情况下更简单 |

## 5. 实施路线图

### 优先级定义

| 级别 | 定义 |
|------|------|
| P0 | 生产阻塞问题，当前架构债务已影响可用性 |
| P1 | 高频运维/开发痛点，解决后显著改善效率 |
| P2 | 可预见业务需求扩展，有 Q3/Q4 计划 |
| P3 | "Nice to have"，资源允许时进行 |

### 阶段规划

**Phase 1（Q3 2026）：核心架构加固**

| 项目 | 优先级 | 预计工作量 | 风险 |
|------|--------|-----------|------|
| 连接池隔离（方向三） | **P1** | 2-3 周 | 中——需要拆分 GORM 实例，调整 outbox 事务边界。测试覆盖需要仔细验证 |
| 搜索索引重建 API（方向二） | **P1** | 1-2 周 | 低——isolated 变更，不涉及 service 层接口变更。但需充分测试锁行为 |
| 批量 delete 文件/消息 | **P1** | 2-3 周 | 中——需要定义部分失败语义，与 outbox 事件集成 |

**Phase 1 关键里程碑**：
1. 连接池隔离完成 → 生产压测验证，比较 p99 延迟
2. 索引重建 API + migration（将 GENERATED ALWAYS 切换为 trigger 方案）
3. 批量删除 API 完成端到端测试

**Phase 2（Q4 2026）：运维体验提升**

| 项目 | 优先级 | 预计工作量 | 风险 |
|------|--------|-----------|------|
| 配置热加载（方向五） | **P2** | 2-3 周 | 高——修改 `ratelimit.Limiter` 的不可变设计，需确保并发安全。SIGHUP 信号处理在容器化环境中有平台差异 |
| Typing/Presence 基础（方向四） | **P2** | 2-3 周 | 中——SSE 下行已有，缺上行通道。WebSocket 引入需要修改架构模式 |
| Opentelemetry tracing 按需启用 | **P2** | 1 周 | 低——已有 tracing 基础设施，只需添加运行时开关 |
| 批量操作扩展到评论/通知 | **P2** | 2 周 | 低——复用 Phase 1 的 `BatchResult` 模式和 API 模式 |

**Phase 2 关键里程碑**：
1. 限流器动态调整 + 日志级别热加载（运维工具链基础）
2. Presence MVP 完成（仅内存，不跨实例）
3. Observability 热开关可用（trace enable/disable 无需重启）

**Phase 3（2027 H1）：扩展到 AI 和语义能力**

| 项目 | 优先级 | 预计工作量 | 风险 |
|------|--------|-----------|------|
| pgvector 语义搜索 | **P2-P3** | 4-6 周 | 高——需要 embedding 服务、向量索引维护、混合搜索排序算法 |
| 跨实例 presence（Redis） | **P2** | 2 周 | 中——引入 Redis 运维依赖 |
| 分布式限流（Redis） | **P3** | 1 周 | 低——per-instance 限流在大部分场景足够 |
| 全文搜索替换为 Meilisearch | **P3** | 6-8 周 | 高——需要数据同步管道，生产切换风险大 |

### 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 连接池隔离引入 outbox 事务不一致 | 中 | 高 | 充分测试 + outbox 写入保持 primary pool 路径 |
| 速率限制器动态调整导致竞态 | 中 | 中 | 旧桶优雅老化（refill 到新配置而非立即重置） |
| 批量 API 部分失败语义难做 | 高 | 中 | 定义清晰的约定文档 + gRPC streaming 示例 |
| WebSocket 扩展增加运维复杂度 | 中 | 中 | 初始使用 SSE + HTTP POST 模拟上行，WebSocket 作为 v2 |
| GENERATED 列改为 trigger 时数据不一致 | 低 | 高 | 旧数据在 migration 中添加 `UPDATE` 步骤确保一致 |
| 配置热加载的 SIGHUP 在 K8s 下行不通 | 中 | 低 | K8s 使用 ConfigMap + 挂载 + fsnotify；备用信号 SIGUSR2 |

## 结论

aero-id 的当前架构质量高、约定清晰、技术债务可控。五个扩展方向中：

- **连接池隔离（方向三）** 和 **搜索索引重建（方向二）** 是 P1 级架构债务——生产环境下将在流量增长和 schema 变更时暴露问题。
- **配置热加载（方向五）** 和 **Typing/Presence（方向四）** 是 P2 级运维和体验增强——有价值但不是立即可见的痛点。
- **批量 API（方向一）** 是 Phase 1 的产出品——它既是业务需求也简化客户端。

五个方向的共同特征是**不改变核心架构模式**（仍然是 handler → service → repository + storage + sso），不破坏现有的单向依赖约定。这使得它们可以独立开发、独立发布、独立回滚。
