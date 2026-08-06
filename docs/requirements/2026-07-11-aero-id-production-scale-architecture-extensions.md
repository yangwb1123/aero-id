# aero-id — 生产级水平扩展架构：五项面向多副本部署的扩展方向

> **日期:** 2026-07-11  
> **视角:** 资深架构师 / 产品经理  
> **方法:** 逐文件扫描 `internal/` 全部 23 个子包（~180 Go 源文件）、`migrations/` 全部 22 份 SQL、`api/proto/`、`configs/`、`cmd/`、`deployments/`、`Makefile`。与 `docs/requirements/` 下 **116+ 份既有分析文档** 做逐关键词交叉比对，确保以下方向在既有材料中 **未被作为独立方向做完整的代码证据链论证**。  
> **约束:** 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角论证。

---

## 当前代码库水平扩展现状

aero-id 的架构在单个实例上是高度成熟的——双传输、事务性 Outbox、断路器、幂等键、限流、SSE 实时推送等。然而，全面扫描显示 **大量核心基础设施是单实例（in-process）的，在水平扩展到 2+ 副本时，要么功能静默失效、要么安全模型坍塌**。

以下 5 个方向瞄准同一个核心缺口：**将 aero-id 从单实例服务提升为多副本集群就绪的平台**。

---

## 方向一：分布式速率限制（Distributed/Coordinated Rate Limiting）

### 为什么需要

aero-id 当前的速率限制器（`internal/pkg/ratelimit`）是一个纯粹的**单进程内存令牌桶**。在多副本部署下，这个设计存在一个**安全模型裂缝**：如果配置为 100 RPS、部署 5 个副本，每个副本独立维护自己的令牌桶——一个恶意用户实际上可以获得 500 RPS 的有效吞吐量。

这不是理论风险——它是竞态条件下的 **确定性放大**。限流的目的是保护下游（数据库、SSO、S3），而单实例限流在多副本下完全失效。

### 代码级证据

**证据 1：Limiter 完全无共享状态**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu        sync.Mutex
    buckets   map[string]*tokenBucket  // 纯内存 map
    idleTTL   time.Duration
}
```

`buckets` 是每个实例独立的 `map[string]*tokenBucket`。没有任何共享后端（Redis、memcached 等）。配置中的注释明确声明了这一点：

```go
// internal/config/config.go (RateLimitConfig)
type RateLimitConfig struct {
    // ...
    // Limiting is PER-INSTANCE; distributed limiting would need
    // shared state (e.g. Redis) and is out of scope.
    RPS   float64
    Burst int
}
```

**证据 2：HTTP 和 gRPC 限流都使用同一个单实例 limiter**

```go
// internal/app/app.go
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
// ... 同一个 limiter 实例传给 HTTP 和 gRPC ...
router := httpapi.NewRouter(..., limiter, ...)
grpcServer := grpcsrv.NewServerWithOptions(..., grpcsrv.WithRateLimit(limiter), ...)
```

在单个进程内它是共享的——一旦进程数 >1，每个进程得到自己的桶。

**证据 3：没有限流指标暴露给运维判断是否失效**

`Allow` 方法只返回一个 `bool`。限流器不暴露：
- 每个 key 的当前桶水位
- 累计拒绝数
- 按 key 分布的拒绝率

运维人员无法回答"限流是否因为多副本而实际失效"。

### 产品经理视角

对客户来说，"限流"是一个 SLA 承诺。如果客户购买的是 100 QPS 的 API 计划，实际因为多副本获得 500 QPS，那么：
1. **限流形同虚设**——恶意用户可以轻松碾压后端
2. **SLA 不可信**——客户无法根据限流头信息判断自己的剩余配额
3. **公平性丧失**——一个高流量用户可以挤占其他用户的资源，因为没有全局上限

### 架构师视角

**设计考量：**
- **Redis 中心化方案：** 最直接。使用 Redis Sorted Set 或 Lua 脚本实现滑动窗口限流。但引入 Redis 增加运维复杂度和延迟（每次请求一次 round trip）。
- **本地 + 概率协调方案：** Token bucket 本地缓存 + 定期同步。降低延迟但允许短时突发越过配额（最终一致性限流）。
- **混合方案（推荐）：** 本地 Bucket 为主 + 异步 Heartbeat 在实例间协调。每个实例每 100ms 广播自己的消费计数；实例按收到的全局计数缩放自己的配额。牺牲少量精度换取零额外延迟。

**边界情况：**
- **实例冷启动：** 新实例加入时，本地配额按全局配置重算，不累计其他实例已消耗的配额
- **实例崩溃：** 剩余配额不会回吐，但崩溃实例的配额自动分配到其余实例
- **Redis 不可用：** 降级为纯本地限流（仍比没有好），记录告警

---

## 方向二：多副本 SSE 实时推送集群化（Multi-Replica SSE/Real-Time Clustering）

### 为什么需要

SSE（`GET /v1/events`）是 aero-id 的实时通知通道。当前实现使用纯内存的 `Hub`（`internal/hub/hub.go`），其 `Subscribe`/`Publish` 操作完全驻留在单个进程的 `sync.Map` 中。一旦部署 2 个实例：
- 实例 A 处理了消息发送 → Outbox relay → 通知创建 → `hub.Publish(recipientUID, ...)`
- 实例 B 持有 recipientUID 的 SSE 连接
- **通知永远无法到达客户端**

这是一个**静默的语义错误**——没有错误日志、没有错误码。SSE 连接正常维持，心跳正常发送，只是事件永远不会到达正确的实例。

### 代码级证据

**证据 1：Hub 是纯内存的**

```go
// internal/hub/hub.go
type Hub struct {
    subs   sync.Map  // uid → *uidSubs
    // 没有外部 pub/sub，没有数据库表，没有 gRPC 广播
}
```

`Publish` 方法只查找本地 `sync.Map`：
```go
func (h *Hub) pub(uid string, evt SSEEvent) {
    raw, ok := h.subs.Load(uid)
    if !ok {
        return  // 用户连接在另一个实例上 → 静默丢弃
    }
    // ...
}
```

**证据 2：Dispatcher 调用 Publish 不关心可见性范围**

```go
// internal/notification/dispatcher.go
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // ...
    if d.pusher != nil {
        payload, _ := json.Marshal(n)
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload))
        // 如果接收者在另一个实例，这行代码静默 no-op
    }
    // ...
}
```

**证据 3：没有回退机制**

如果 SSE 推送失败（无论是跨实例还是连接断开），客户端唯一的补救是依赖 `last_event_id` 重连回放：

```go
// internal/handler/http/events.go
// replayMissed 在客户端 Last-Event-ID 非空时才工作
// 但它的触发条件是客户端断开并重连——不是服务端感知到的推送失败
```

这意味着跨实例推送失败后，客户端直到自身断开连接并重新连接（可能是几分钟后）才收到缺失的通知。

**证据 4：没有跨实例事件的健康指标**

```go
// internal/hub/hub.go
// Hub 没有任何指标暴露：无连接数、无发布数、无丢弃数
```

运维无法监控"实时推送是否正常"。

### 产品经理视角

实时通知是 IM 和协作产品的**核心体验差异**。用户发送一条消息后期待立即看到对方"已读"。如果因为后端多副本部署导致实时推送静默失效：
- 用户抱怨"消息延迟"
- 技术排查时需要先排除负载均衡器、浏览器、网络——最终才发现是后端副本问题
- 最坏情况：用户认为系统不可靠，转向竞品

对于 SSO 管理员和高级用户，SSE 的跨实例不可靠是**不可接受的 SLA 缺陷**。

### 架构师视角

**推荐方案：PG LISTEN/NOTIFY + 本地 Hub 混合。**
- 每个实例的 `Dispatcher` 在发布前先执行 `SELECT pg_notify('sse_channel', payload)`
- 每个实例运行一个 Postgres LISTEN 协程，收到通知后投递到本地 Hub
- 这样利用了已在架构中的 PostgreSQL，零额外依赖
- 延迟增加 ~1ms（同 DB 实例）到 ~5ms（跨 AZ）

**备选方案：**
- **Redis Pub/Sub：** 标准方案，低延迟，但引入新的运维依赖
- **gRPC broadcast：** 实例间通过 gRPC 互相注册，事件广播到所有 peer。复杂度高，需要服务发现。

**边界情况：**
- **自我去重：** 使用 PostgreSQL NOTIFY 时，发起方也会收到自己的通知。Dispatcher 需要记录事件 ID 来去重（`sync.Map`  + 短 TTL）
- **实例崩溃：** 崩溃实例上的 SSE 连接全部断开。客户端重连时通过 `last_event_id` 回放恢复
- **连接洪泛：** 相同 uid 在多个实例上同时订阅（同一用户多设备），需要确保事件不重复投递到同一设备

---

## 方向三：存储配额强制执行与用量追踪（Storage Quota Enforcement & Usage Tracking）

### 为什么需要

aero-id 是一个**文件服务**，但当前没有**任何机制**限制单个用户可以消耗的总存储空间。`MaxUploadBytes`（`internal/service/file.go`）只限制单个文件的大小——一个用户可以上传 10 万个刚好低于上限的文件，稳步耗尽磁盘或 S3 预算。

对于多租户平台（aero-id 的 `owner_uid` 就是租户标识），缺少配额意味着：
1. **无成本控制：** 一个用户的存储消耗无法预测也无从限制
2. **无公平保证：** 一个用户消耗 90% 存储时，其他用户无法上传
3. **无计费数据：** 无法按用户计算存储成本

### 代码级证据

**证据 1：`MaxUploadBytes` 只限制单文件**

```go
// internal/service/file.go
type fileService struct {
    maxUploadBytes int64
    allowedMimeTypes []string
}
// 在 service_crud.go 中：
if s.maxUploadBytes > 0 && size > s.maxUploadBytes {
    return nil, apperr.BadRequest("file.too_large", "file exceeds maximum size")
}
// 仅验证单个文件大小，不检查用户总用量
```

**证据 2：没有用户维度的存储聚合查询**

```go
// internal/repository/file.go
// 支持的方法：
// - CountByStorageKey — 按 storage_key 计算引用
// - CountAnyByStorageKey — 同上含软删除
// - ListByOwner — 按 owner 分页
// 没有任何 [SUM(size) WHERE owner_uid = ?] 查询
```

`ListByOwner` 返回全量文件列表——要通过它计算总用量，必须遍历所有分页，在大用户场景下不可行。

**证据 3：上传路径不做空间检查**

```go
// internal/service/service_crud.go — Create 流程
func (s *fileService) Create(ctx context.Context, ownerUID, name string, size int64, r io.Reader) (*domain.File, error) {
    // ...
    if s.maxUploadBytes > 0 && size > s.maxUploadBytes {
        return nil, apperr.BadRequest(...)
    }
    // ← 这里没有检查 ownerUID 的当前总用量是否已达配额上限
    // ← 不检查总用量，直接写入
    file, err := s.repo.Create(ctx, ownerUID, name, size, storageKey, checksum, mime)
    // ...
}
```

**证据 4：配额配置不存在**

```go
// internal/config/config.go — StorageConfig
type StorageConfig struct {
    Type            string   `mapstructure:"type"`
    MaxUploadBytes  int64    `mapstructure:"max_upload_bytes"`
    AllowedMimeTypes []string `mapstructure:"allowed_mime_types"`
    // 没有 PerUserQuota 或 DefaultQuotaBytes
    Local LocalConfig
    S3    S3Config
    GC    GCConfig
}
```

### 产品经理视角

对于面向企业的服务，存储配额是**基本功能而非增值功能**：
- 免费层用户限制 100MB，付费层限制 10GB——这是 SaaS 的标准做法
- 没有配额意味着无法提供分级定价（tiered pricing）
- 突发场景（某用户上传整个数据库备份）会直接导致其他用户的服务降级

### 架构师视角

**实现要点：**
1. **异步总量缓存：** `SELECT COALESCE(SUM(size), 0) FROM files WHERE owner_uid = ? AND deleted_at IS NULL` 可以加在 `files.owner_uid` 索引上，但大表扫描仍昂贵。建议维护一个 `user_storage_stats` 物化汇总表，通过事务性 Outbox 事件更新
2. **一致性边界：** 配额检查是典型的 TOC/TOU 竞态——检查通过到实际写入之间可能有并发上传。软配额（超过警告但不拒绝）vs 硬配额（超限拒绝）需要产品决策
3. **GC 回吐：** 软删除后的配额应该何时回吐？立即？还是过了 retention 期？

**推荐实现路径（最小代价）：**
- 在 `service.Create` 中加入异步用量查询 + 硬配额拒绝
- 用 `files` 表的二级索引 `(owner_uid, deleted_at)` 加上 `SUM(size)` 实现 O(log n) 聚合
- 在 Redis 中缓存用户当前用量，TTL 30 秒，仅当实际写入后才失效

---

## 方向四：传输层与应用层 DDoS/滥用防护（Transport & Application Layer Abuse Protection）

### 为什么需要

aero-id 的速率限制（Rate Limit）只保护**每个请求的令牌消耗**，但完全不防御以下攻击向量：
1. **连接耗尽：** 攻击者可以建立数千个 SSE 长连接（`GET /v1/events`），每个连接消耗文件描述符、goroutine 和内存。没有每 IP 或每 uid 的连接数限制。
2. **慢速请求（Slowloris）：** HTTP 服务器有 `ReadTimeout`/`WriteTimeout`，但慢速请求仍然可以维持连接数分钟，占用连接池。
3. **请求放大：** 搜索端点接受复杂的 tsquery，虽然限制了 200 字符的查询长度，但 `ILIKE '%...%'` 回退路径在 CJK 场景下是**全表扫描**——攻击者可以用精心构造的查询锁住数据库连接。
4. **批量注册 Block/Reaction：** 速率限制（基于 uid 的令牌桶）无法防御针对不同 uid 但来自同一 IP 的批量注册操作。

### 代码级证据

**证据 1：SSE 端点无连接数限制**

```go
// internal/handler/http/events.go — Stream 方法
func (h *EventsHandler) Stream(c *gin.Context) {
    uid, ok := service.ActorFrom(c.Request.Context())
    // ... 立即进入 for-select 循环
    sub, unsub := h.hub.Subscribe(uid)
    defer unsub()
    // 没有检查该 uid 的已有连接数
    // 没有检查该 IP 的已有连接数
    for {
        select {
        case <-c.Request.Context().Done():
            return
        case evt, ok := <-sub:
            // ...
        case <-ticker.C:
            // ...
        }
    }
}
```

一个攻击者可以用同一个 token 建立 1000 个 SSE 连接，每个连接分配 16 个缓冲槽的 channel：

```go
// internal/hub/hub.go
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)  // 每个连接 16 个槽位
    // 没有限制同一个 uid 的总通道数
}
```

1000 个连接 × 16 槽 × ~256 bytes/槽 ≈ 4MB 缓冲 + 1000 goroutine。

**证据 2：CJK ILIKE 回退可以触发全表扫描**

```go
// internal/service/search.go
// 当 FTS 返回 0 结果且查询含 CJK 字符时：
msgs, err = s.messages.SearchMessagesLike(searchCtx, actor, query, limit)
// SearchMessagesLike 使用 ILIKE '%query%'，不走索引
```

任意用户可以构造一个 CJK 查询，强制 PostgreSQL 对 `messages`、`comments`、`files` 三张表做全表扫描。虽然每个 kind 有 5s 超时，但三个 kind 串联就是 15s 的数据库压力——攻击者可以开 100 个并发请求让数据库 CPU 打满。

**证据 3：没有 IP 级别的速率限制**

```go
// internal/pkg/ratelimit/ratelimit.go
func (l *Limiter) Allow(key string) bool {
    // key 是 uid（认证用户）或 IP（匿名）
    // 但对于认证用户，key 是 uid，不区分来源 IP
}
// 攻击者可以用 1 个 token + 100 个 IP → 绕过 IP 限流
```

**证据 4：没有 SSE slow consumer 保护**

```go
// internal/hub/hub.go
// 发送到 subscriber channel 时，full channel 走 default（drop）：
select {
case ch <- evt:
default:
    // subscriber is too slow; drop rather than block
}
// 但没有任何机制通知上游（Dispatcher）或关闭慢速消费者
```

一个 SSE 客户端连接慢到 channel 持续满，事件持续丢失——但连接不关闭，不记录日志，不告警。

### 产品经理视角

安全不是一个功能特性——它是一个**持续的成本**。DDoS/滥用防护直接关系到：
- **SLA 稳定性：** 一个恶意用户能通过搜索端点让数据库 CPU 100%，影响所有用户
- **成本控制：** 全表扫描导致数据库 I/O 激增，云厂商账单暴涨
- **合规：** SOC2、ISO 27001 要求服务具备基本的滥用检测和速率限制能力

### 架构师视角

**推荐防护矩阵：**

| 层 | 防护措施 | 技术方案 | 优先级 |
|----|---------|----------|--------|
| 网络 | 每 IP 连接数限制 | Gin middleware 计数器 | P1 |
| 传输 | SSE per-uid 连接上限 | Hub 注册时检查同 uid 连接数 | P1 |
| 应用 | 搜索端点成本标签 | FTS 复杂度评分 + 拒绝高开销查询 | P2 |
| 应用 | SSE slow consumer 断开 | 超过 N 次连续 drop 后主动关闭 channel | P1 |
| 应用 | 匿名操作全局限流 | IP 级 + 全局级两层限流 | P1 |

**SSE Slow Consumer 保护实现要点：**
- Hub 的 `Subscribe` 需要返回一个 `droppedCount` 通道
- 当连续 drop 超过阈值（如 5 次/分钟），Hub 主动关闭 channel
- `Watchdog` goroutine 监控 channel 的可用性

---

## 方向五：数据生命周期管理与级联清理（Data Lifecycle Management & Cascading Cleanup）

### 为什么需要

aero-id **不拥有用户身份**——用户存在于外部 SSO 中。当 SSO 删除一个用户时，aero-id 留下三组孤儿数据：
1. **文件：** 物理 blob 继续占用存储，直到 GC 根据 retention 清理
2. **消息：** 发送给已删除用户的消息永久存在，收件人字段指向一个不存在的 uid
3. **评论：** 类似地，评论的 `owner_uid` 指向一个不存在的 SSO 用户

此外，缺乏数据生命周期管理意味着：
- **无数据导出（Data Portability）：** GDPR 第 20 条要求提供机器可读格式的数据导出
- **无自动过期（Retention Policy）：** 没有为消息/通知/审计日志设置基于时间的自动清理
- **无级联删除：** 删除文件时，文件分享记录（`file_shares`）、评论、outbox 事件不会级联清除

### 代码级证据

**证据 1：删除文件时不会级联清理关联数据**

```go
// internal/service/file.go — Delete 方法
func (s *fileService) Delete(ctx context.Context, id domain.ID, actor string) error {
    // ...
    // 只删除了 file 行（软删除）
    // 没有清理：
    //   - file_shares（分享记录变成死引用）
    //   - 关联的 comments（target_id 指向不存在的文件）
    //   - 关联的 outbox events
    //   - 物理 blob（依赖 GC 异步清理）
}
```

迁移文件可见，分享记录（`file_shares`）是独立的：

```sql
-- 000006_create_file_shares.up.sql
CREATE TABLE file_shares (
    file_id      UUID NOT NULL,
    grantee_uid  TEXT NOT NULL,
    owner_uid    TEXT NOT NULL,
    -- 没有 FK 到 files(id) ON DELETE CASCADE
);
```

**证据 2：没有用户级数据导出**

```go
// api/proto/aero_id/v1/aero_id.proto
// 没有任何 RPC 用于数据导出
// 没有任何 RPC 用于批量获取某个 uid 的所有数据
```

**证据 3：没有基于时间的自动清理**

```go
// internal/config/config.go
type Config struct {
    // 没有 DataRetention 配置块
    // 没有 AuditLogRetention 配置项
    // 没有 NotificationRetention 配置项
}
```

审计日志、通知、已处理事件等数据**永久保留**，没有任何清理机制：

```sql
-- migrations/000014_create_audit_log.up.sql
CREATE TABLE audit_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_uid   TEXT NOT NULL,
    action      TEXT NOT NULL,
    -- 没有 expires_at 列，没有 retention 列
);
```

**证据 4：SSO 用户删除后无回调机制**

```go
// internal/sso/client.go
// SSO 客户端只有 ValidateToken 和 GetUsers（批量查询）
// 没有 "用户删除通知" 端点
// 没有 Webhook 接收 SSO 的用户删除事件
```

当 SSO 删除用户时，aero-id 完全不知情，数据永久留存。

### 产品经理视角

数据生命周期管理对三类利益相关者至关重要：

| 利益相关者 | 需求 | 当前状态 |
|-----------|------|---------|
| **终端用户** | 删除账号时所有数据也被删除 | ❌ 数据永久保留 |
| **终端用户** | 能导出自己的消息、文件、评论 | ❌ 无导出功能 |
| **合规官** | 审计日志保留 90 天后自动清理 | ❌ 永久保留 |
| **平台运营** | 通知和已读事件定期清理降低存储成本 | ❌ 无清理机制 |
| **安全团队** | 离职员工数据在 SSO 删除后自动清除 | ❌ 无级联删除 |

**典型用户故事：**
> "我三个月前离开了那家公司。SSO 账号已被删除，但我的文件还在 aero-id 上——而且被新员工看到。我需要确保我的数据要么被彻底删除，要么转交给接替我的人。"

### 架构师视角

**级联删除的三种策略：**

| 策略 | 描述 | 复杂度 | 一致性 |
|------|------|--------|--------|
| 同步级联 | 在主操作的事务中删除所有关联数据 | 低 | 强一致 |
| 异步事件驱动 | 主操作后发布 `entity.deleted` 事件，outbox 消费后清理 | 中 | 最终一致 |
| 后台 sweep | GC sweeper 定期扫描并清理孤儿数据 | 高 | 最终一致 |

**推荐方案：**
- 文件删除时同步清理 `file_shares`（因为它们有 FK 语义）
- 将 `entity.deleted` 事件写入 outbox，实现异步清理评论、通知、消息
- 增加数据导出端点 `GET /v1/account/export`，生成 JSON 归档文件（文件、消息、评论、通知）
- 在配置中增加 `data_retention` 段：

```go
// 建议的配置结构
type DataRetentionConfig struct {
    AuditLogDays      int  // 默认 90 天
    NotificationDays  int  // 默认 30 天
    SoftDeleteDays    int  // 超过此期限的软删除行被物理清除
    EnableAutoCleanup bool
}
```

**边界情况：**
- **大量数据导出：** 一个用户有 10 万条消息。导出需要分页 + 异步生成 + 临时存储链接通知。不应在请求路径中同步生成
- **导出中断：** 生成到一半时用户取消。需要有清理临时文件的机制
- **级联删除 vs 引用约束：** `file_shares` 目前没有 FK 约束，但业务逻辑依赖参照完整性。添加 FK `ON DELETE CASCADE` 需要在 `file_shares` 上先添加索引以避免锁表

---

## 总结：优先级矩阵

| # | 方向 | 类型 | 优先级 | 风险等级（如果不做） | 依赖 |
|---|------|------|--------|---------------------|------|
| 1 | **分布式速率限制** | 安全/架构 | **P0** | 多副本后限流完全失效 | 需引入 Redis 或协调协议 |
| 2 | **多副本 SSE 集群化** | 产品/架构 | **P0** | SSE 实时推送静默失效 | 推荐 PostgreSQL LISTEN/NOTIFY，零新依赖 |
| 3 | **DDoS/滥用防护** | 安全/运维 | **P1** | 搜索端点可触发全表扫描导致 DB 过载 | 无新依赖 |
| 4 | **存储配额与用量追踪** | 产品/成本 | **P1** | 单用户可能耗尽全部存储预算 | 需配置文件 + 聚合查询 |
| 5 | **数据生命周期管理** | 合规/运维 | **P1** | GDPR 不合规；数据无限增长 | 需配置 + Outbox 事件类型扩展 |

**核心发现：** 方向一和方向二是 **静默破坏者**——它们在多副本场景下不会报错、不会崩溃、不会记录告警，但功能**实际失效**或安全模型**实际崩塌**。它们在单实例开发/测试中永远不会被发现，只有在生产环境多副本部署时才会暴露。

将 aero-id 推向多副本生产部署前，方向一和方向二是**必须先解决的问题**。
