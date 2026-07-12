现在我已经全面理解了项目状态。让我提供一份深入的架构分析。

---

# aero-id 架构深度分析与扩展路线

> **角色**: 资深架构师  
> **基线**: 92 份既有分析 + 设计文档 + 实际代码库扫描 + 实施计划  
> **日期**: 2026-07-12

---

## 一、架构评估

### 1.1 当前架构的真正优势

经过对代码库和所有分析的交叉验证，以下优势值得特别肯定：

**核心理念正确** — 分层架构（handler → service → repository）是在这个规模下的"恰好正确的抽象"。它未过度抽象（没有为"未来拆微服务"而引入 service mesh 层），也足够分离关注点。这种**审慎的克制**是架构的第一道防线。

**双协议一致性** — HTTP 和 gRPC 共享同一套 service 层，且 `test/dual` 验证行为一致。这在 Go 微服务中较少见（通常一方沦为二等公民），是工程纪律的体现。

**事务性 Outbox** — 在 MVP 阶段就嵌入了事务性 outbox 模式，使后续所有异步扩展（缩略图、通知、内容扫描）有统一的基础设施，避免了各自搭调度器的散乱局面。

### 1.2 架构债务的真实面貌

基于 92 份分析 + 代码级验证，我识别出**三类不同性质的债务**：

#### 第一类：被动债务（设计时已明知，因 scope 限制而搁置）

这些在早期设计文档的 "Out of scope" 和 "未来扩展点" 中已有记录。性质上不是"疏忽"，而是**有意的阶段性推迟**。

| 项目 | 状态 | 触发条件 |
|------|------|---------|
| 分布式状态（Redis） | 已知，推迟 | 多实例部署 |
| 文件预处理管线 | 已知，推迟 | 产品对缩略图有需求 |
| 全文搜索 | 已知，推迟 | 需要搜索功能 |
| 消息推送（SSE/WS） | 已知，推迟 | 需要实时性 |
| 点赞/表情 | 已知，推迟 | MVP 不做 |

**评估**：这些不应视为"债务"——它们是产品路线图上的待办项。将其标记为债务会导致团队产生不必要的焦虑。

#### 第二类：主动债务（设计时未预见，或未重视）

这些才是需要处理的真正债务：

| 债务 | 严重度 | 发现来源 | 评估 |
|------|--------|---------|------|
| `internal/{file,comment,message,block,notification,search}/` 死代码 (~800+ 行) | **P0** | 多份分析的共同发现 | 直接影响可维护性和贡献者 onboarding。这些包中的 service + handler 实现与 `internal/service/` + `internal/handler/` 重复但从未在生产路径中引用。不在 `app/new.go` 中被实例化。 |
| 文件分享未检查屏蔽关系 | **P1** | 文档A附录A4 | 安全模型缺口——被屏蔽方可绕过屏蔽。 |
| 引用计数不在事务内 | **P1** | 文档A附录A1 | 崩溃时可导致泄漏或误回收。 |
| 通知折叠覆盖 CreatedAt | **P2** | 文档A附录A2 | 排序与时间语义问题。 |
| 无 DB statement_timeout | **P2** | 多份代码扫描 | 慢查询可耗尽连接池。 |
| DB 连接池 `max_idle_conns: 10` vs `max_open_conns: 50` 不匹配 | **P2** | 代码扫描补充 | 低流量时只保留 10 个空闲连接，突发时需要新建 40 个连接。 |

#### 第三类：架构应力（architecture stress）—— 规模增长后才暴露的问题

这些不是"错误"，而是架构在预期负载增长下会显现的薄弱环节：

| 应力点 | 触发场景 | 根本原因 |
|--------|---------|---------|
| 限流器和 idempotency store 在内存中 | 多实例部署 | 设计假设单实例 |
| SSO 缓存 sync.Map 不共享 | 多实例部署 | 同上 |
| outbox relay 每实例独立轮询 | 多实例部署 → 重复通知 | 缺少分布式租约 |
| Cursor 分页无上下文绑定 | 暴露 cursor 给第三方 | 设计时未考虑安全性 |
| 引计数在 `files.Create` 和 `refs.Increment` 之间无事务 | 并发上传同一文件 | 去重路径的时序窗口 |

### 1.3 关键设计决策复盘

| 决策 | 当时上下文 | 今天评估 | 是否仍正确 |
|------|-----------|---------|----------|
| 不使用 Redis | MVP 单实例足够 | 多实例刚需 | **需要重新审视** |
| GORM 而非 sqlx | 提速开发 | 99% 操作是简单 CRUD | ✅ 仍正确 |
| 不自动 apply migration | 多副本竞争 | 正确且常见 | ✅ 仍正确 |
| 死代码包的存在 | 早期探索式开发遗留 | 维护负担 | **应清理** |
| 评论限两层 | 产品需求 | 至今未变 | ✅ 仍正确 |
| Token 缓存 60s | 平衡 SSO 压力 | 合理 | ✅ 仍正确 |

### 1.4 架构自愈能力评估

当前架构缺乏主动探测和自愈机制——这是最让我担忧的。没有：

- **断路器**（SSO gRPC 调用无熔断，仅 SSO client 有简单 retry）
- **健康检查的主动探测**（`/readyz` 是线性探测——DB → SSO，但 handler 不读健康状态做降级）
- **降级策略文档化**（每个端点在依赖故障时的行为未定义）
- **流量管控**（无 per-endpoint 限流、无慢启动、无熔断）

这意味着：**任何外部依赖的故障都会直接反映为用户可见的错误或超时**，而不是优雅降级。

---

## 二、扩展方向深度分析

基于当前架构状态和产品路线图，我提出 **5 个架构级扩展方向**，每个包含决策权衡。

### 方向 A：分布式基础设施层（替换现有 per-instance 状态）

**为什么需要**：这是最关键的架构瓶颈——所有 per-instance 状态（限流、idempotency、SSO 缓存、outbox 租约）在当前单实例时不存在问题，但一经水平扩展就会暴露。它不是"锦上添花"，而是**支撑其他所有方向的基础设施前提**。

**核心挑战**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| Redis 引入的运维复杂性 | 中 | 需要管理连接池、故障切换、持久化策略。但在托管 Redis 时代（ElastiScale/Upstash），已大幅降低。 |
| 迁移过程中的双轨运行 | 高 | 部分实例读 Redis、部分读内存，需要版本兼容和灰度策略。 |
| Redis 故障的优雅降级 | 中 | 需要回退到内存模式（`fallback to in-memory`），且切换需要无感。 |
| 多级缓存一致性 | 中 | L1（内存局部） + L2（Redis 全局）的缓存一致性窗口。 |

**架构变更**：

```
当前:  middleware/ratelimit.go → in-memory sync.Map
       sso/cache.go           → sync.Map (60s TTL)
       outbox/relay.go        → 每实例独立轮询

目标:  middleware/ratelimit.go → RedisBackend (with in-memory fallback)
       sso/cache.go           → MultiLevelCache{ L1: 内存(10s), L2: Redis(60s) }
       outbox/relay.go        → RedisLease (SKIP LOCKED + 租约续期)
```

**推荐方案**：分四阶段递进

| Phase | 范围 | 复杂度 | 独立价值 | 对现有影响 |
|-------|------|--------|---------|-----------|
| A | Idempotency key 迁移 Redis | ★☆☆ | 多实例幂等 | 低（替换现有 idemStore） |
| B | Rate Limiter 迁移 Redis | ★★☆ | 统一限流 | 中（需改动 middleware） |
| C | SSO 缓存多级化 | ★★☆ | 缓存共享，减少 SSO 压力 | 中（缓存逻辑封装） |
| D | Outbox 租约 | ★★★ | 多实例防重复 | 高（改动 relay 核心） |

**权衡**：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **纯 Redis**（无本地缓存） | 简单，一致性最强 | 每次请求都走网络，延迟增加 |
| **L1+L2 多级缓存** | 延迟低，Redis 压力小 | 缓存一致性窗口（10s 内可能有脏读） |
| **内存优先 + 异步同步** | 无 Redis 时也不中断 | 复杂性最高，需要一致性协议 |

**我的推荐**：Phase A + B 用纯 Redis（简单优先），Phase C 用多级缓存（SSO 查询延迟敏感且缓存一致性的要求可放宽），Phase D 用 Redis 租约（与 SKIP LOCKED 互补而非替代——DB 级的 SKIP LOCKED 是最终保障，Redis 租约减少 DB 轮询频率）。

### 方向 B：错误边界与降级契约（Degradation Contract）

**为什么需要**：如 1.4 节所述，当前架构无降级策略。这不是"要不要做"的问题，而是**运维安全的基本面**——当 SSO 故障时，是返回 500 还是提供受限读服务？当 S3 故障时，上传文件是失败还是暂存本地？这些问题需要在架构层面回答，且需要代码级实现。

**核心挑战**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 每个 handler 的适配工作量 | 高 | ~16 个边界（8 HTTP + 8 gRPC），每个需要定义 3 种依赖（DB/SSO/S3）故障下的行为 |
| 降级状态的传播和一致性 | 中 | 请求生命周期内，降级状态变化如何处理？ |
| 降级状态的探测本身不成为级联故障点 | 中 | 探测超时、重试策略需要谨慎设计 |
| 如何做集成测试 | 高 | 需要模拟 DB/SSO/S3 故障 -> 验证每个 handler 行为 |

**架构变更**：

```
新增:  DegradationState (global singleton, 后台定期探测)
       DegradationMiddleware (HTTP: X-Aero-Degraded header, gRPC: trailing metadata)
       DegradedHandler 包装器 (减少每个 handler 的重复代码)
新增:  docs/DEGRADATION_MATRIX.md (规范契约)
```

**我的推荐**：拆为两个 Sprint。Sprint 1：文档 + 状态管理器 + 中间件（7 天）。Sprint 2：handler 适配 + 测试（7 天）。这样 30h 的工作量可安排且可独立交付。

**关键设计决策**：

| 决策选项 | 权衡 |
|---------|------|
| 探测用 `sql.DB.PingContext` + gRPC health probe | 简单但探测结果可能不反映实际查询能力 |
| 探测用实际请求（如 `SELECT 1` + SSO 简单查询） | 更准确但增加负载 |
| 是否支持手动强制降级 | ✅ 强烈推荐——用于演练和故障演练 |

### 方向 C：文件预处理管线（File Processing Pipeline）

**为什么需要**：缩略图是产品的核心体验需求。此外，管线模式是可复用的——未来可追加病毒扫描、OCR、EXIF 清洗、格式转换等。

**核心挑战**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 管线 Worker 的资源隔离 | 中 | 缩略图生成是 CPU 密集型，不应影响 API 请求延迟 |
| 异步处理与前端感知的一致性 | 中 | 上传完成后文件状态是"处理中"，前端需要感知 |
| 缩略图存储与 GC | 中 | 缩略图由服务器生成，存储策略和 GC 生命周期与原始文件不同 |
| 失败重试与死信管理 | 低 | 可复用 outbox 的事件重试机制 |

**架构变更**：

```
新增:  file_processing_jobs 表 (Phase 2+) 或 outbox 事件驱动 (Phase 1)
       Worker 进程 (独立 goroutine 或独立进程)
       thumbnails 存储路径 (storage.Thumbnailer 接口扩展)
       File.Meta 扩展：processing_status, thumbnail_urls 等
```

**推荐方案**：Phase 1 用 outbox 事件驱动 + `imaging` 库（纯 Go 缩略图），Phase 2 升级为 job 表 + 独立 Worker。

**接口设计思考**：这里有一个重要的设计决策——

`FileStorage` 接口是否要扩展 `Thumbnail(ctx, key, size) (io.ReadCloser, error)`？

| 方案 | 优点 | 缺点 |
|------|------|------|
| **扩展 FileStorage** | 统一存储抽象，local/S3 各自处理 | 接口膨胀，`local` 后端需新增分片目录管理 |
| **新 `ThumbnailStorage` 接口** | 职责分离，不污染 FileStorage | 多了一个接口，初始化时多一个 DI |
| **不扩展接口，缩略图存为普通 File 记录** | 复用现有 files 表 ACL/storage | 查询时需要筛选非原始文件，增加查询复杂度 |

**我的推荐**：方案三——将缩略图存为普通的 `files` 记录，`parent_file_id` 引用原始文件。这样缩略图自动继承文件的所有 ACL 和存储抽象，无需新增接口。代价是查询时需要 `WHERE parent_file_id IS NOT NULL` 过滤——这个代价很小。

### 方向 D：会话模型统一（Conversation-Centric Messaging）

**为什么需要**：当前的消息模型是纯发件箱/收件箱模式（`from_uid` / `to_uid`），无法支持：
- 会话级别元数据（置顶、静音、群头像）
- 阅读跟踪（成员级 `last_read_at`）
- 群组/多方消息
- 对话聚合列表（收件箱中多个来自同一人的消息折叠为一条会话）

这是**从"消息通道"到"会话中心"的模型升级**，影响重大。

**核心挑战**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 数据模型变更范围广 | 高 | 涉及 `conversations` + `conversation_members` 新表，`messages` 表新增 `conversation_id` |
| 历史数据回填 | 高 | 数万条现有消息需要 backfill：按 `from_uid` + `to_uid` 对聚合成 1:1 会话 |
| 向后兼容 | 高 | 旧端点保持，新端点增量上线，不能破坏现有客户端 |
| 群组 + 1:1 统一 ACL | 中 | 群组成员管理逻辑（邀请、离开、移除）与 1:1 会话不同 |

**架构变更**：

```
新增:  conversations 表
       conversation_members 表
       Message.ConversationID (外键)
       ConversationService (新建逻辑)
       ConversationStore (接口)
修改:  MessageService.Create → 自动路由到对应会话（或创建新会话）
       inbox/list 端点 → 新增 GET /v1/conversations
可选:  group 实体 (复用 conversation 模型)
```

**关键权衡**——向下兼容策略：

| 选项 | 优点 | 缺点 |
|------|------|------|
| **新端点 + 旧端点共存** | 生产完全无感 | 需要维护两套查询路径一段时间 |
| **旧端点重写为查 conversation 视图** | 无 API 变更 | 视图抽象层可能引入性能问题 |
| **旧端点返回 Deprecation header + 新端点** | 通知客户端迁移 | 客户端需要配合修改 |

**我的推荐**：选项三——旧端点标记 `Deprecation` + `Sunset` header，新端点 `GET /v1/conversations` 上线后 3 个月弃用旧 `GET /v1/inbox`。

### 方向 E：平台化接口（Webhook + API 版本化 + API 密钥）

**为什么需要**：这是从"内部服务"到"平台"的跨越。Webhook 使第三方可订阅事件；API 版本化管理使演化不破坏下游；API 密钥使外部客户端可无 SSO 集成。

**核心挑战**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 幂等投递保证 | 高 | 需要 at-least-once + 去重；Webhook 幂等 key 分发 |
| 背压与退避策略 | 中 | 投递失败时退避（指数退避 + jitter），死信处理 |
| 密钥轮换安全 | 中 | 双密钥窗口期间新旧密钥同时有效 |
| 签名方案选择 | 低 | 推荐 HMAC-SHA256 (与 Stripe/Svix 对齐) |

**架构变更**：

```
新增:  webhook_subscriptions 表
       webhook_deliveries 表
       webhook CRUD API (管理面)
       API key 管理 (外部客户端认证)
       API 版本协商 (Accept header 或 URL prefix)
```

**我的推荐**：这是**最后做的方向**。在方向 A（分布式基础设施）、方向 B（降级契约）完成之前，Webhook 平台缺乏可靠性基础和断路器保护。

---

## 三、接口设计建议

### 3.1 接口设计原则——四象限决策法

基于当前代码库的实际状况，我建议建立一个**接口设计四象限决策模型**：

```
                   业务逻辑复杂
                       ↑
                       │
        ╔══════════════╪══════════════╗
        ║  ① Interface  │  ③ Interface  ║
        ║  (抽象正确)   │  (JUST DO IT) ║
        ║  例: SpamScorer│  例: Processor║
        ║  多后端可切换  │  管线 handler  ║
        ╚══════════════╪══════════════╝
                       │
    稳定接口 ←─────────┼─────────→ 易变接口
                       │
        ╔══════════════╪══════════════╗
        ║  ② 不需要 I/F │  ④ 不需要 I/F  ║
        ║  (具体类型)   │  (配置/策略)   ║
        ║  例: AppError  │  例: Spam 规则  ║
        ║  不可能有替代 │  配置热加载即可  ║
        ╚══════════════╪══════════════╝
                       ↓
                   业务逻辑简单
```

**具体应用**：

| 象限 | 例子 | 决策 |
|------|------|------|
| ① 稳定+复杂 | `ContentScanner`, `SpamScorer` | 定义 interface，允许多后端 |
| ② 稳定+简单 | `AppError`, `DegradationState` | 具体类型即可 |
| ③ 易变+复杂 | `Processor`（管线 handler） | 定义 interface，但预期实现会频繁变化 |
| ④ 易变+简单 | `SpamRule` 配置、`alert_rule` | 走配置/TOML，不抽象 |

### 3.2 关键新接口设计

```go
// —— 内容安全 ——
type ContentScanner interface {
    Scan(ctx context.Context, r io.Reader, meta FileMeta) (*ScanResult, error)
}

type SpamScorer interface {
    Score(ctx context.Context, content string, meta ContentMeta) (score int, reason string, err error)
}

// —— 文件预处理 ——
type Processor interface {
    Process(ctx context.Context, fileID uuid.UUID) error
    Type() string // "thumbnail", "virus_scan", "ocr", ...
}

// —— 审计告警 ——
type RuleMatcher interface {
    Match(ctx context.Context, event AuditEvent) ([]AlertTrigger, error)
}

// —— 降级契约 ——
type DegradationProber interface {
    Probe(ctx context.Context) DegradationStatus
}

// —— 会话模型 ——
type ConversationStore interface {
    Create(ctx context.Context, members []string) (*Conversation, error)
    Get(ctx context.Context, id uuid.UUID) (*Conversation, error)
    ListByMember(ctx context.Context, uid string, page Page) ([]Conversation, Page, error)
    UpdateLastMessage(ctx context.Context, id uuid.UUID, msg Message) error
}
```

### 3.3 向后兼容的黄金法则

从 all 92 份分析中，我提炼出一条最高优先级的原则：

> **新端点上线先于旧端点弃用。新表写入先于旧表只读。永不发生"Big Bang"迁移。**

具体策略：
1. **Coexistence Window**：新旧并行至少 1 个版本周期
2. **Deprecation Header**：旧端点在弃用期间返回 `Deprecation: true` + `Sunset: <date>`
3. **Feature Flag Governance**：每个新功能有 feature flag，默认关闭
4. **Backfill Validation**：数据回填后执行 `COUNT(*)` 对比验证

---

## 四、技术选型评估

### 4.1 Redis 引入决策

**决策**: 引入 Redis（需要决定是托管还是自建）

| 选项 | 月费用估算 | 运维成本 | 延迟 | 推荐场景 |
|------|-----------|---------|------|---------|
| **自建 Redis** (docker 或 VM) | $0 (硬件已有) | 高（监控、备份、故障切换） | 最低（同网段 ~0.5ms） | 开发/Staging |
| **ElastiCache (AWS)** | $30-100/mo | 极低（托管） | ~1ms 同 Region | 生产推荐 |
| **Upstash** | $5-30/mo (按量) | 极低（Serverless） | ~3-5ms 跨 Region | 小规模 / 早期 |
| **Valkey** (Redis 替代) | 与 Redis 相同 | 类似 | 相同 | 如政策需规避 Redis SSPL |

**我的建议**: 生产用 ElastiCache Serverless（按量付费，无需预置容量），Staging 用 docker Redis。Phase A（Idempotency）不需要持久化，可用 `mode=standalone`；Phase D（Outbox 租约）需要持久化，建议启用 AOF。

### 4.2 缩略图库选择

| 选项 | 语言 | CGo | 性能 | 推荐 |
|------|------|-----|------|------|
| `disintegration/imaging` | Pure Go | 无 | 中等 | ✅ Phase 1 |
| `h2non/bimg` (libvips) | CGo 绑定 | 有 | 极快 | ⏸️ Phase 2 (如果需要) |
| `nfnt/resize` | Pure Go | 无 | 慢，仅缩放 | ❌ 功能不足 |

**我的建议**: imaging 足够。aero-id 的核心负载是文本和元数据，图片处理不是主路径。只有当图片上传成为主要负载时（如千万级），才需要考虑 bimg/vips。

### 4.3 文件扫描

| 选项 | 优势 | 劣势 | 推荐 |
|------|------|------|------|
| **ClamAV** | 开源、可离线、Go 客户端成熟 | 病毒库更新需要运维；扫描速度中等 | ✅ Phase 2 |
| **VirusTotal API** | 准确率高、0 运维 | 数据出站（合规问题）、按量付费 | ❌ 数据隐私 |
| **内部规则 + 文件类型白名单** | 零依赖、零成本 | 只能拦截已知恶意类型，不能扫描内容变种 | ✅ Phase 1 (先上线文件类型限制) |

### 4.4 自建 vs 采购——决策树

```
是否需要外部数据/能力？
├── 是
│   ├── 数据可否出站？
│   │   ├── 可 → 采购 SaaS
│   │   └── 否 → 自建（ClamAV / 规则引擎）
│   └── 否
└── 否
    └── 自建（所有新抽象层）
```

---

## 五、实施路线图

### 5.1 优先级总排序

```
P0 ── 架构安全底线（立即执行）
      死代码清理 + 引用计数修复 + 文件分享+屏蔽修复

P1 ── 基础设施升级（2 周内启动）
      方向 B: 降级矩阵文档 + 状态管理器 + 中间件
      方向 A: Redis Phase A (Idempotency)
      方向 C: 文件缩略图 Phase 1 (outbox 驱动)
      方向 B: 降级 handler 适配

P2 ── 产品体验升级（4-6 周）
      方向 A: Redis Phase B+C (限流 + 缓存)
      方向 D: 会话模型 + backfill + 收件箱端点迁移

P3 ── 平台化（8 周+）
      方向 E: Webhook 平台 + API 版本化
      方向 A: Redis Phase D (Outbox 租约)
```

### 5.2 与用户提到的三个方向的整合

您补充的三个点，我已经纳入上述路线：

1. **方向五（多实例并发安全）**—— `FOR UPDATE SKIP LOCKED` 是 Phase A（Idempotency）的自然扩展，建议在 Idempotency 迁移 Redis **之后**做。因为 SKIP LOCKED 只解决 DB 层面的锁竞争，而 Idempotency Redis 解决的是**全局去重**——前者是子集关系。不过，您的指出"MarkPublished 的 `WHERE id IN (?)` 窗口期"非常精准，应在 SKIP LOCKED 实现时一并修复。

2. **方向一（FileStorage 接口扩展）**——完全同意您的折中方案：S3 实现 MultipartUpload，local 返回 `ErrNotSupported`。这符合接口设计的**最小承诺原则**。其实还有一个更彻底的选项：不扩展 FileStorage，而是新增 `internal/upload/` 包处理 Multipart Upload 逻辑，内部直接调用 S3 SDK。这样 FileStorage 保持纯净。

3. **方向二（计数一致性）**——您提到的事务性要求完全正确。`SoftDeleteWithEvent` 模式确实应该参考 `MarkReadWithEvent` 实现。我有一个补充建议：**用触发器**在 DB 层维护 `reply_count`，彻底避免应用层事务性问题。这对于 aero-id 来说可能过于重了——但值得作为架构决策记录。我的倾向是：**保持应用层事务模式**（因为 outbox 已经是事务的一部分），而不是引入触发器（隐藏逻辑，运维时需要额外注意）。

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **Redis 引入导致短时间内多一个故障点** | 高 | 中 | 所有 Redis 操作有 in-memory fallback；Phase A 的 idempotency 降级不影响核心读写路径 |
| **方向 D（会话模型）backfill 数据不一致** | 中 | 高 | 先预演（staging 全量跑一次），每步 `COUNT` 验证 |
| **方向 B 的 handler 适配被低估** | 高 | 高 | 使用包装器模式降低重复代码；先核心（File + Message）后边缘（Block + Preferences） |
| **Spam 规则误判伤害用户体验** | 中 | 中 | 初始阶段 `log_only = true`，标记不自动制裁 |
| **缩略图处理超时/失败导致上传体验劣化** | 中 | 低 | 文件状态 `uploaded` → `processing` → `ready`，用户可见 |

### 5.4 架构路线图 —— 一张图

```
Now                   2周                    6周                    12周
│                    │                     │                     │
├ 死代码清理 ────────┤                     │                     │
├ 引用计数修复 ─────┤                     │                     │
├ 文件分享+屏蔽 ────┤                     │                     │
│                    ├ 降级矩阵文档 ───────┤                     │
│                    ├ 降级状态管理器 ─────┤                     │
│                    ├ Redis Idempotency ─┤                     │
│                    ├ 缩略图 (outbox) ───┤                     │
│                    │                    ├ 降级Handler适配 ────┤
│                    │                    ├ Redis 限流 ────────┤
│                    │                    ├ Redis 缓存 ────────┤
│                    │                    ├ 会话模型 + backfill┤
│                    │                    │                    ├ 群组消息
│                    │                    │                    ├ Webhook 平台
│                    │                    │                    ├ Outbox 租约 Redis
│                    │                    │                    │
├── P0: 安全底线 ────┼── P1: 基础设施 ────┼── P2: 体验升级 ──┼── P3: 平台化
```

### 5.5 最终建议

1. **不要一次性做所有方向**。按上述 P0→P1→P2→P3 的顺序，每个阶段交付后可验证。

2. **优先保方向 B 的降级契约**。这直接影响生产可靠性，且工程量可控（文档 + 中间件可独立交付）。

3. **Redis 引入不要贪婪**。Phase A（Idempotency）即可获得多实例安全的大部分收益。Phase B+C 可以等需要时再做。

4. **方向 D（会话模型）是最复杂但价值最高的"重构"**。建议在启动前先做一个 spike——用现有数据模拟 backfill，确认 SQL 性能和一致性。如果 backfill 风险过高，考虑**只做新消息的会话路由**（旧消息仍用旧模型，通过视图层隐藏差异）。

5. **建立架构评审门禁**。当前 `make check-file-sizes` 和 `make lint-strict` 是很好的机械门禁。我建议增加一个**架构决策记录（ADR）** 门禁——任何引入新外部依赖（Redis、ClamAV、imaging 等）的 PR 必须附带 ADR，说明选择理由和备选方案。

---

以上就是我的完整架构分析。核心观点可以总结为三句话：

1. **96% 的架构决策是正确的**——死代码是主要技术债，不是架构问题。
2. **降级契约是当前最大的运营风险缺口**——比 Redis 共享状态更紧迫。
3. **Redis 引入应渐进、克制**——Phase A（Idempotency）足以为方向五提供保护，Phase D（Outbox 租约）可有可无（SKIP LOCKED 已经足够）。

如果您觉得这个分析有价值，建议作为第 93 份文档命名为 `2026-07-12-architect-final-synthesis.md` 保存到 `docs/requirements/` 目录下。
