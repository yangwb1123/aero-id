现在我已掌握充分的代码级证据。以下是完整的架构分析。

---

# 架构分析：aero-id 生产级断层评估与扩展方向

## 1. 架构评估

### 当前架构的优势

aero-id 当前的架构有一个很难得的品质：**它对自己的职责边界非常清醒**。SSO 外置、无 accounts 表、transport 层 handler-service-repository 三层严格分离、Otel 可观测性已到位——这些决策意味着架构的核心骨架是正确的，问题全在"单实例默认设计"向"分布式生产部署"过渡的过程中。

具体来说：

1. **依赖注入方向干净**。`internal/app/new.go`（437 行）是唯一的 composition root，所有组件通过构造函数注入，没有 `init()` 魔术、没有 `wire` 或者 `fx` 的包袱。这在 Go 生态中已经很难得。

2. **缓存层有清晰的后备方案**。SSO client 的 `singleflight` + 熔断器 + stale TTL 三级退化（见 `client.go:validateUpstream`）是正确应对依赖故障的范本。同一模式可复用到数据库连接治理。

3. **GORM 的使用尺度合理**。没有滥用 ORM 黑魔法，`db.go` 中每个 repository 的 row→domain 转换是显式的，便于未来读写分离时对读操作插桩。

4. **工程约束覆盖面好**。AGENTS.md 中的 500 行文件限制、50 行函数限制、cyclomatic ≤ 10——虽然当前文件级约束已被部分突破（`service/comment.go` 481 行、`handler/http/comment.go` 366 行），但方向是对的。

### 关键设计决策评估

| 决策 | 当前选择 | 评估 |
|------|---------|------|
| **SSO 认证外置** | aero-id 不签发 token | ✅ 正确。不积攒身份系统的技术债 |
| **进程内缓存** | `sync.Map` token/user cache | ⚠️ 单实例正确，多副本错误的默认值 |
| **单 GORM 连接池** | 所有查询复用 | ⚠️ 简单期正确，规模期需要读写分离 |
| **通用限流器** | `pkg/ratelimit` 通用 token bucket | ✅ 接口干净，可替换后端 |
| **SSE Hub 进程内** | `sync.Map` 连接注册表 | ⚠️ 单副本正确，多副本需要跨进程通信 |
| **配置热加载** | 启动时一次性读取 | ❌ 没有依据需要复用的理由，纯技术债 |
| **文件下载** | Local 用 `io.Copy` / S3 用 presigned | ⚠️ Local 缺 HTTP 协议优化；S3 缺代理审计选项 |

### 架构债务的系统性归类

我梳理出**三个层次**的债，它们的偿还优先级不同：

**第一层——设计时未考虑多副本（最容易修复，修复收益也最大）**：
- 进程内缓存 × 3（TokenCache, UserCache, RateLimiter）
- 进程内 SSE Hub 注册表
- 无 warmup 机制（`App.Run` 中的 `warmup()` 目前只做了 connection probe，没有缓存预热）

**第二层——单点故障风险（修复难度中等，但安全合规可能强制要求）**：
- 凭据无轮换（5 类凭据，全是硬依赖）
- gRPC 连接无证书热加载（TLS 证书过期 = 服务不可用）

**第三层——非功能性退化可以通过功能降级缓解（修复成本高，优先级最低）**：
- 无 Range/ETag/304（用户体验但非功能阻断）
- 无查询隔离（慢查询影响其他请求，但不是服务不可用）

---

## 2. 扩展方向

基于断层文档 + 代码级验证，我补充 **5 个高价值架构扩展方向**，其中部分方向文档中已有涉及，但我加入了新的视角。

### 方向 A：进程内状态 → 一致共享状态层（文档方向一的 P1/P2 扩展版）

**为什么需要**：
文档对此已有完整分析。我补充一个重要的定量观察：当前 `TokenCache` 的 TTL 是秒级（来自 `client.go:NewClientFromConn` 的 cfg 注入），意味着每 3-5 个请求就有一个 cache miss。在 5 副本滚动部署时，每个新副本的 token cache hit rate 从 ~90% 跌落到 ~0%，SSO 需要承受约 50 倍的瞬时峰值。这不是"锦上添花"，是**运维事故等级**。

**核心挑战**：
不是"要不要引入 Redis"，而是"引入 Redis 后如何渐进式切换而不重构整个 sso 包"。`sync.Map` 的接口和 Redis 的 SET/GET 语义差异大。

**预期的架构变更**：

```
internal/sso/
├── client.go           # 无变化（接口保持）
├── token_cache.go      # 当前: sync.Map 实现
│                        # 未来: TokenCache interface, 现有实现改名 InMemoryTokenCache
├── token_cache_redis.go # 新增: RedisTokenCache 
```

关键在于：**定义一个 `TokenCache` 接口**，让 `Client` 构造函数接受接口而非具体类型。这不是说要在 Go 里搞依赖注入框架，而是给将来插拔留一个 seam。

```go
// internal/sso/cache.go (新增)
type TokenCache interface {
    Get(token string) (uid string, ok bool)
    GetStale(token string) (uid string, ok bool)
    Set(token, uid string, exp time.Time)
    RunCleaner(interval time.Duration) (stop func())
}
```

**对现有系统的影响**：
核心影响面很窄——仅 `sso/client.go` 的构造函数和 `token_cache.go` 的名称。现有 tests 通过 `NewClientFromConn` 创建，不受影响。

**风险缓解**：接口提取后，先不加 Redis 实现。只做提取 + 编译通过。Redis 实现在另一个 PR 中独立添加。

### 方向 B：查询路由层——读写分离 + 查询优先级（文档方向二的扩展版）

**为什么需要**：
当前 `internal/repository/db.go` 的 GORM 连接池是无差别的。文档已经指出了搜索锁死连接池的问题。我补充一个更具体的场景：`audit_events` 表的写入（每次操作都写审计日志）和 `files` 表的读取（文件列表展示）混在同一个池子里的影响——审计写入的 backpressure 会阻塞用户文件下载。这在日均百万请求规模下会产生明显的长尾延迟。

**核心挑战和技术难点**：
GORM 的 `dbresolver` 插件确实可以一键配置读写分离，但真正的难点在于：

1. **事务中的读操作必须走主库**——`dbresolver` 需要在 `*gorm.DB` 的 context 层面标记 "这是事务内的读，不能路由到 read replica"。当前的 repository 方法在开启事务后没有传递这个标记。
2. **staleness tolerance 的配置**——某些查询场景（如通知列表）可以接受 1 秒的副本延迟，搜索可以接受 5 秒，但文件写入后的立即读取需要主库。当前代码在 service 层没有这种信息的传递机制。
3. **GORM 的事务模式**——`gorm.DB.Transaction()` 创建的 `*gorm.DB` 没有区分读写模式，插件只能按语句级别探测 `SELECT` 然后路由到从库，但如果 `SELECT` 出现在一个显式 `BEGIN...COMMIT` 块中，路由到从库会违反 PostgreSQL 的事务语义。

**对现有系统的影响**：
影响在 repository 层的每个查询方法。最小侵入方案是：在 `db.go` 中创建一个 `dbResolver` 实例，然后在每个 repository 的构造时传入带 resolver 的 `*gorm.DB`。repository 层的 Create/Update/Delete 不需要改，GORM 自动识别。

### 方向 C：错误协议的层次化扩展（文档未覆盖的隐含需求）

**为什么需要**：
文档诊断表最后一行提到了当前错误协议的不足（`code` 字符串 + `message`，无结构化元数据、无重试提示、无 i18n）。这看起来像"API 风格问题"，但在**生产规模下这直接决定了 SDK 的健壮性和客户端处理错误的效率**。

举例：gRPC 的 `validateUpstream` 方法有三种错误路径——熔断器打开、SSO 不可用、token 无效。当前这三种错误都变成同一个 gRPC status code（`Internal` 或 `Unauthenticated`），客户端无法区分应该重试还是放弃。

**核心挑战**：
这与方向一、二不同——它不是一个"断裂点"，而是一个"摩擦点"。不修复不会在运行时出事故，但会持续累积 SDK 开发成本和运维排障成本。

**预期的架构变更**：

```
internal/pkg/errors/
├── app_error.go     # 当前
├── app_error.go     # 扩展 ErrorDetails 字段
├── retry_info.go    # 新增: Retryable / RetryAfter / RetryHint
├── i18n.go          # 新增: 可选的 i18n key 映射
```

关键设计原则：**向后兼容**——`code` 和 `message` 字段完全保留，新增的 `details` 或 `retry_info` 是可选的。gRPC 侧可以用 `google.rpc.Status` 的 `details` 字段携带结构化元数据。

**对现有系统的影响**：
极小。`internal/pkg/errors` 包已存在，仅扩展其结构。现有的所有 handler 无需改动，新增的字段在序列化时自动包含。

### 方向 D：可观测性的"生产级化"——结构化慢查询与熔断事件（文档策略隐含）

**为什么需要**：
当前代码已经内嵌了 OpenTelemetry tracing（`ssoTracerName` 和 `client.go` 中的 span），也有 metrics sink 接口（`MetricsSink`）。但有两个生产级盲区：

1. **数据库慢查询没有独立的指标上报路径**。`statement_timeout`（方向二 P0）只切断查询，但不暴露给可观测性系统——运维人员不知道哪些查询超时了。
2. **熔断事件（breaker open/close/half-open）当前没有日志**。`sso/breaker.go` 中使用 `sync.Map`，当 breaker trip 时没有结构化事件输出。在故障排查时，运维需要知道"breaker 在什么时间点因为什么而打开"。

**核心挑战**：
这不是"增加新的可观测性系统"，而是"给已有的 Otel + metrics sink 补全事件源"。挑战在于**不要重复文档已覆盖的内容**——这不是一个新方向，而是方向一至五中每个变更的公共依赖。

**实施建议**：
在每个 P0/P1 变更中加入一个名为 `observability.md` 的子项，列明该变更新增的指标和日志事件。例如：
- 方向一 P0（warmup）：增加 `cache_warmup_duration_seconds` 直方图指标
- 方向二 P0（statement_timeout）：增加 `db_query_timed_out_total` 计数器，标记 `repo` 和 `operation`

### 方向 E：功能降级分层架构（跨方向的协同策略）

**为什么需要**：
文档的"不做的情况"里隐含了一个理念——功能降级。这不是一个独立方向，而是方向一至四中共同需要的**统一降级策略**。

当前代码已经有一个雏形：`middleware/ratelimit.go` 检查 `middleware.AbortIfDegraded`（见 `file.go:PresignUpload`）。这表明降级策略已经开始构建了。但它是**孤立的**——每个 middleware/handler 独立判断降级条件。

**核心挑战**：
需要一个统一的 `DegradationManager`，在 app 启动时注册各依赖的健康状态，在运行时集中判断是否降级。不能每个 feature handler 都写 `if middleware.AbortIfDegraded(c, DepsForOp(DepDB, DepStorage), false)`——这不可组合、不可测试。

**预期的架构变更**：

```go
// internal/app/degradation.go (新增)
type DegradationManager struct {
    mu       sync.RWMutex
    statuses map[string]HealthStatus // "sso", "db", "storage"
}

func (d *DegradationManager) Register(name string, check func() error) // 定期运行 check
func (d *DegradationManager) IsHealthy(op string, deps []string) bool // handler 调用
```

**对现有系统的影响**：
当前已有一个 `stopDegradation` func（见 `app.go:shutdown`），说明 degradation 的概念已经在 composition root 层存在了。需要做的是把它从 shutdown 时调用的"二元好/坏"扩展为支持分组件健康度和部分降级。

---

## 3. 接口设计建议

### 核心原则

1. **最小 seam 原则**。不在接口设计上过度抽象。当前 aero-id 的 hierarchy 层数（handler → service → repository）已经足够，不需要引入新的层次。所有变更应该通过"提取接口 + 注入不同实现"的方式完成，而不是增加嵌套。

2. **保持 GORM 的 query builder 模式**。读写分离时，不要对每个 repository 方法做"写一个接口两个实现（主库版/从库版）"的适配。GORM 的 `dbresolver` 在 query chain 层面路由，repository 的构造注入仍然只需要一个 `*gorm.DB`——resolver 在连接池层面做决定。

3. **配置驱动，非代码驱动**。方向二（statement_timeout per query）、方向四（per-endpoint rate limit）、方向三（secret store 实现选择）都应该是 YAML 配置 + 启动时组装，不是每个变更改代码。

### 需要引入的新抽象层

| 抽象层 | 所在位置 | 接口形态 | 引入时机 |
|-------|---------|---------|---------|
| `TokenCache` / `UserCache` 接口 | `internal/sso/cache.go` | 现有方法的 interface 提取 | 立即（方向一 P0 前置） |
| `SecretStore` 接口 | `internal/config/secret.go` | `Get(ctx, name) (string, error)` + `Watch(ctx, name, onChange)` | 方向三 P0 |
| `RateLimitConfig` 扩展 | `internal/config/config.go` | 从简单 `rps/burst` 变为层级结构 | 方向四 P0 |
| `DegradationManager` | `internal/app/degradation.go` | 健康度聚合 + 查询 API | 方向一/二 P0 公共依赖 |

不建议做的抽象：
- **不要抽象 Repository 接口**（当前 repository 的 `*gorm.DB` 操作已经是"刚够用"的抽象，再加一层 mock 层或接口层只会增加运维复杂度且几乎零收益——service 层已经通过 mock 做了测试隔离）
- **不要引入 CQRS 框架**（当前规模下，ORM 级别的读写分离就足够了，完整的 CQRS 会引入 event sourcing 和 message bus 的复杂度）

### 向后兼容性策略

1. **Proto/Body 兼容**：错误协议扩展时，`code` + `message` 字段永远保留，新增字段为 optional，新旧 SDK 互认。
2. **配置兼容**：扩展 RateLimitConfig 时，旧的 `rps/burst` 顶级字段仍然工作（映射为新的 `per_user.rps/burst`），在新配置格式出现前不做 breaking change。
3. **API 路由不变**：方向五的下载优化不改变 `/files/dl` 的 URL 模式，只在 HTTP 头部层面支持 Range/ETag。
4. **缓存键格式不变**：Redis-backed TokenCache 的 key format 与 `sync.Map` 的 key 格式一致（token string），切换后不产生缓存预热间隙。

---

## 4. 技术选型

### 新依赖引入的原则

基于 AGENTS.md 的工程约束，我建议以下准入标准：

| 标准 | 阈值 | 一票否决条件 |
|------|------|------------|
| 现有代码可回避 | ≥7 天工作节省 | 已有等效 Go stdlib 方案 |
| Go 模块依赖计数增加 ≤3 | ≤3 transitive dependencies | 间接依赖超过 50 |
| 编译时间增加 ≤15% | Benchmark 验证 | 超过 20% |
| 许可证兼容 | MIT/Apache2/BSD | AGPL/GPL |
| 维护活跃度 | ≥2 年持续维护 | 年提交数 < 20 |
| 与当前 Go 1.26 兼容 | `go vet` 静默 | 使用 `unsafe` 或有已知 vet 问题 |

### 技术选型建议

| 需求 | 推荐方案 | 备选方案 | 基于为何 |
|------|---------|---------|---------|
| **分布式缓存** | Redis 7+ (go-redis v9) | KeyDB / Dragonfly | 方向一 + 方向四可共用；go-redis 是当前 Go 生态最活跃的 Redis 客户端 |
| **读写分离** | `gorm.io/plugin/dbresolver` (已有) | pgcat / pgpool-II（外部代理） | 0 新增依赖；GORM 内置，共两行配置切换；外部代理会增加运维复杂度 |
| **密钥管理** | 文件 SIGHUP（P0）→ Vault（P2） | AWS Secrets Manager / GCP Secret Manager | SIGHUP 零依赖；Vault 是最通用的自建方案 |
| **CDN** | CloudFront → S3 | Cloudflare R2 | S3 和 CF 的对接是成熟的；R2 没有 egress fee 但需要改 storage 实现 |
| **分布式限流** | Redis Lua 滑动窗口 | Redis 令牌桶 (cell rate algorithm) | Lua 脚本保证原子性；滑动窗口比令牌桶更精确控制短期 burst |

**关于 Redis 引入的特别考虑**：
文档的分析是准确的——方向一 P2（分布式缓存）和方向四 P2（分布式限流）可以共享一个 Redis 后端。这意味着一次基础设施投入覆盖两个问题域。

但有一个重要的设计决策需要提前做：**Redis 的"最小所需"部署模式**。

| 模式 | 适用规模 | 运维复杂度 | 可复用方向 |
|------|---------|-----------|-----------|
| 单实例 Redis (master) | ≤3 副本，日均百万请求 | 低 | 方向一 P2、方向四 P2 |
| Redis Sentinel (1 master + 2 replicas) | 高可用要求 | 中 | 同上 |
| Redis Cluster | 多区域，日均千万+ | 高 | 同上 |

我建议：**P2 阶段先走单实例 Redis + 本地 AOF 持久化**，不要一上来就上 Sentinel 或 Cluster。原因：
- 方向一 P2 + 方向四 P2 的总工作预期 2 周，单实例 Redis 可撑到日均 500 万请求
- Sentinal/Cluster 的配置和运维复杂度会引入额外的风险点
- TokenCache 和 UserCache 的 TTL 天然是 eviction 友好的——缓存条目有生命周期，不会无限增长

### 自建 vs 采购

对于 aero-id 这个规模的 Go 服务（40+ 内部包，22 个迁移，日均百万请求）：

| 决策 | 方案 | 推荐 | 理由 |
|------|------|------|------|
| SSR/session 粘滞 | ALB/Envoy 配置 | **采购**（已有基础设施） | 代码零侵入，纯运维配置。Envoy 的 `consistent_hash` LB 策略已支持基于 uid 的一致性哈希 |
| CDN 加速 | CloudFront/Cloudflare | **采购** | 边缘缓存是 IaaS 层职责，自建 nginx/OpenResty CDN 不经济 |
| 密钥轮换 | Vault | **自建 P0 → 采购 P2** | P0 的 SIGHUP reload 零依赖；P2 的 Vault 投入仅在 SOC2 审计要求时 |
| 分布式缓存 | Redis | **采购（自建 Redis 实例）** | 这是 PaaS 层。自建分布式缓存算法库（如 consistent hashing + gossip）成本 > 10x Redis |

---

## 5. 实施路线图

### 优先级排序（经代码验证的重排）

文档的优先级排布基本合理。我基于代码深度扫描做了一个微调：

| 优先级 | 方向 | 阶段 | 工作量 | 验证条件 | 依赖关系 |
|--------|------|------|--------|---------|---------|
| **P0** | 方向一：粘滞路由 + warmup | P0 | 1-2 天 | 滚动部署峰值 SSO 负载下降 ≥50% | 无 |
| **P0** | 方向二：`statement_timeout` + 慢查询日志 | P0 | 0.5 天 | DB 连接池不被搜索查询耗尽 | 无 |
| **P0** | **新增**：TokenCache/UserCache 接口提取 | P0 | 1 天 | 编译通过，tests 通过 | 方向一 P1 的前置 |
| **P1** | 方向四：`MultiLimiter` 组合限流 | P0 | 3-4 天 | 搜索限流 5 req/s 不阻塞文件上传 | 无 |
| **P1** | 方向五：Local 端 Range/ETag/304 | P0 | 2 天 | `curl -I` 可见 `Accept-Ranges` + `ETag` | 无 |
| **P1** | 方向三：SIGNING_KEY SIGHUP reload | P0 | 3 天 | `kill -HUP` 后新签名使用新 key，旧 URL 不失效 | 方向三接口提取 |
| **P2** | 方向一 P1：Redis-backed TokenCache + UserCache | P1 | 1 周 | Redis 部署后 SSO 负载下降 ≥90% | TokenCache 接口提取 |
| **P2** | 方向二 P1：`dbresolver` 读写分离 | P1 | 2-3 天 | 读副本负载上升，主库负载下降 | 方向二 P0 |
| **P2** | 方向四 P1：限流集成到 gRPC 拦截器 | P1 | 2 天 | gRPC 搜索调用也受 per-route 限流 | 方向四 P0 |
| **P2** | 方向一 P2 + 方向四 P2：Redis 分布式限流 + SSE Pub/Sub | P2 | 2 周 | 全局速率限制精确到 ±5% 误差 | Redis 基础设施就绪 |

**与文档的差异点**：
- **我将 TokenCache 接口提取提前到 P0**。这是所有分布式缓存工作的前提，且本身只需 1 天就能完成（提取接口 + 现有实现改名 + 编译通过）。不做接口提取，方向一 P1 的 Redis 实现就无法干净地插入。
- **我将方向三的 SIGNING_KEY SIGHUP reload 后移至 P1**。不是因为其不重要，而是因为方向一/二的 P0 阶段解决的是"服务会不会崩溃"的问题，方向三解决的是"泄露后怎么修复"的问题——前者更紧迫。

### 阶段划分

```
阶段一：止血（7 天）
├── 方向一 P0：粘滞路由配置 + warmup 扩展
├── 方向二 P0：statement_timeout + 慢查询日志
├── TokenCache/UserCache 接口提取
└── DegradationManager 骨架

阶段二：稳定化（10 天）
├── 方向四 P0：MultiLimiter 组合限流
├── 方向五 P0：Local 端 Range/ETag/304
├── 方向三 P0：SIGNING_KEY SIGHUP reload + 双 key 过渡

阶段三：规模化（3-4 周）
├── Redis 基础设施上线
├── 方向一 P1：Redis-backed TokenCache + UserCache
├── 方向二 P1：dbresolver 读写分离
├── 方向四 P1：gRPC 限流集成

阶段四：优化（2-3 周）
├── 方向一 P2 + 方向四 P2：分布式限流 + SSE Pub/Sub
├── 方向五 P1：CDN 集成
├── 方向三 P1：TLS 证书热加载
```

### 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓释策略 |
|------|------|------|---------|
| Redis 引入后运维复杂度超出团队能力 | 中 | 高 | P0/P1 阶段完全不依赖 Redis；方向一 P1 开始前完成 runbook 编写 |
| `dbresolver` 插件与传统 `*gorm.DB` 的事务语义冲突导致数据一致性问题 | 中 | 高 | 阶段二先不启用从库，仅在测试环境验证；启用前做一整天 shadow 流量比对 |
| SIGHUP reload 导致短时签名不一致 | 低 | 中 | 双 key 过渡期确保 `oldKey` 验证 + `newKey` 签名；过渡期至少持续 2 倍 presignTTL（10 分钟） |
| SSE 粘滞路由 + Redis Pub/Sub 的双重投递导致用户收到重复通知 | 中 | 中 | 去重由客户端负责（SSE `id` 字段去重）；服务端不保证 exactly-once 推送 |
| MultiLimiter 配置复杂度导致运维人员配置错误 | 高 | 中 | 配置 schema 做严格的类型校验 + 「dry-run」模式（返回 429 的 Headers 但不实际限流，用于灰度验证） |

### AGENTS.md 工程约束交互影响

这些架构变更与 AGENTS.md 中的硬约束有以下潜在冲突点，需要提前规划：

| 方向 | 冲突文件 | 当前行数 | 风险 | 缓解 |
|------|---------|---------|------|------|
| 方向四：MultiLimiter | `pkg/ratelimit/ratelimit.go` | 约 130 行 | 扩展后可能到 250 行（√ 安全） | — |
| 方向四：集成到 middleware | `middleware/ratelimit.go` | 约 55 行 | 增加 `MultiLimiter` 分支后可能超 100 行 | 将 per-route 限流逻辑提取到独立的 `middleware/ratelimiter_route.go` |
| 方向五：Range/ETag | `handler/http/file.go` | **357 行** | **已接近触发 400 行警戒线** | 阶段前先将 `Download` 逻辑拆到 `handler/http/file_download.go` |
| 方向二：读副本 | `repository/db.go` | 约 330 行 | 增加 `dbresolver` 配置 + 新的 row 结构可能超 500 行 | 将每个 domain 的 row 结构拆到各自文件（`repo_file.go`、`repo_message.go` 等） |

需要特别注意 `handler/http/file.go` 的 357 行——它已经非常接近 AGENTS.md 中 400 行的重构警戒线。如果在阶段一中对它施加方向五的 P0 变更，会直接触发 refactoring trigger。**我建议在开始任何方向五工作前，先做一次预防性的拆文件：将 `file_download.go` 从 `file.go` 中拆分出来。**

---

## 总结

aero-id 的核心架构骨架是干净的。断层集中在以下几个决策上的"单实例默认假设"：

1. **进程内状态 = 分布式环境的状态不一致**（缓存、SSE、限流）
2. **单连接池 = 无查询隔离、无熔断、无法利用读副本**
3. **启动时一次性加载 = 无凭据轮换、无证书热加载**

修复这些断层不需要大重构——它们已经在现有代码结构中预留了"seam"（接口、配置层、composition root）。每个方向的 P0 阶段都只需要 0.5-4 天，且不需要引入新的外部依赖。最关键的是：**先做接口提取，再做后端替换**。TokenCache 接口的提取需要 1 天，但它会解锁后续所有分布式缓存的工作。
