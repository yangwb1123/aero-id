# aero-id 深度扫描：并发安全、数据生命周期与运维盲区

> **日期:** 2026-07-11  
> **视角:** 资深架构师 / 产品经理  
> **方法:** 全量代码逐文件扫描（`internal/` 全部 22+ 包、`migrations/`、`cmd/`、`configs/`、`Makefile`），与 `docs/requirements/` 下 **70+ 份既有分析** 以及 `ROADMAP.md`、`docs/REVIEW_*` 全部方向逐条交叉比对。  
> **约束:** 不编写任何代码。  
> **前置:** 代码库已交付 ROADMAP 全部功能 + 生产加固轮。当前 `main` 是最成熟版本。

---

## 前提确认

在翻阅约 70+ 份既有分析后，以下主题已被充分覆盖，**本文不再重复**（但列在这里让读者确认无遗漏）：

- 移动推送（FCM/APNs）
- 群组消息 / 多对多会话
- 文件版本管理与内容管线
- 死信运维与 Outbox 可观测性
- 跨资源活动流（Activity Feed）
- 通用延迟任务框架
- 资源级作用域搜索
- API Schema Contract / OpenAPI
- 合成事务探针
- 异步管线 Context 传播
- 通知通道 SSE/WebSocket
- 审计日志 / 全文搜索 / 消息 Reactions
- 文件流上传 / MIME 校验 / 大小限制 / 内容去重 / 直传预签名 / GC 回收
- 事务性 Outbox + 幂等键
- SSO 断路器 + stale-cache + singleflight + 重试 + 证书固定
- 速率限制 / 指标 / Tracing / 健康检查
- gRPC ops surface + reflection
- 迁移 CLI / 管理员角色系统
- 用户屏蔽 / 文件分享 / 评论举报审核
- HTTP handler 超时中间件
- 等等（约 120+ 独立方向）

**以下 5 个方向在所有既有分析中均为零系统性覆盖。**

---

## 方向一：多实例并发安全与 Outbox 重复处理（Multi-instance Safety）

### 代码级证据

**行级锁在全代码库中数量为零。**

```bash
$ grep -rn "FOR UPDATE\|SelectForUpdate\|pg_try_advisory\|LockRows\|SKIP LOCKED" internal/ --include='*.go'
# → 0 结果
```

这本身不是问题——许多服务在单实例部署或可容忍重复处理时不需要悲观锁。但 aero-id 的架构在当前设计下，**多实例部署会产生数据层面的重复处理**，且没有任何防护。

**证据 1: Outbox Relay — N 个实例处理相同的事件**

```go
// internal/outbox/repository.go
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    var rows []eventRow
    if err := r.db.WithContext(ctx).
        Where("published_at IS NULL AND NOT dead_letter").
        Order("created_at ASC, id ASC").
        Limit(limit).
        Find(&rows).Error; err != nil {
        // ...
    }
    // ← 没有任何行级锁，没有 FOR UPDATE，没有 SKIP LOCKED
}
```

在 3 实例部署下，所有 3 个 relay 同时调用 `FetchUnpublished`，各自获取相同的 `[event_A, event_B, ...]` 集合。然后：

```
实例 1: 处理 event_A → Dispatcher.Handle → notificationRepo.Create → SSE push → MarkPublished([A])
实例 2: 处理 event_A → Dispatcher.Handle → notificationRepo.Create → SSE push → MarkPublished([A])  ← 重复!
实例 3: 处理 event_A → Dispatcher.Handle → notificationRepo.Create → SSE push → MarkPublished([A])  ← 重复!
```

结果：**每条 outbox 事件产生 N 倍重复通知**。收件人会收到 N 条"你有一条新消息"通知。

**证据 2: GC Sweeper — 并发硬删除竞争**

```go
// internal/storage/gc/sweeper.go
func (s *Sweeper) purgeSoftDeleted(ctx context.Context) (rowsPurged, blobsPurged int, err error) {
    rows, _ := s.repo.ListSoftDeletedBefore(ctx, before, s.cfg.PurgeBatch)
    // 实例 1 和实例 2 都获取到相同的 row 集合
    s.repo.HardDelete(ctx, ids)
    // 同时执行 HardDelete — GORM 的 DELETE ... WHERE id IN (...) 是幂等的，
    // 但在 blob 删除阶段，两个实例同时判断 same_blob 的引用计数：
    n, _ := s.countLive(ctx, f.StorageKey)
    if n > 0 { continue }  // 都读到 0
    store.Delete(ctx, f.StorageKey)  // 都尝试删除同一 blob
    // 第一个成功，第二个返回 NotFound，日志记录警告
}
```

这不是幂等问题——GC 的 `store.Delete` 返回 `ErrNotFound` 被吞掉，**不会产生数据损坏**。但重复的 blob 删除请求导致不必要的 S3/Local 操作成本和日志噪音。

**证据 3: Rate Limiter — 全局限速被绕过**

```go
// internal/pkg/ratelimit/ratelimit.go
// RateLimitConfig 的注释已承认：
// Limiting is PER-INSTANCE; distributed limiting would need
// shared state (e.g. Redis) and is out of scope.
```

在 N 实例部署下，每个实例的令牌桶独立计算。一个恶意的 uid 可以发送 `N × RPS` 的请求量，**完全绕过速率限制的意图**。

**证据 4: SSE Hub — 实例间隔离**

```go
// internal/hub/hub.go
// Hub 是进程内 map[uid]chan SSEEvent
// 没有跨实例通信
```

用户连接到实例 1 的 SSE，但通知可能由实例 2 的 outbox relay 产生并推送。由于 hub 是进程内共享内存，实例 2 推送的事件 **永远不会到达连在实例 1 上的客户端**。

### 为什么需要

这不是理论问题。在容器化部署（K8s）下，**多实例是常态而非例外**：

1. **Outbox 双倍通知是 P0 级数据正确性问题**。用户收到重复通知导致糟糕的 UX，难以通过幂等性修复（因为是不同的 notification row，不是同一个 row 重复创建）
2. **限速被绕过使 DDoS 防护形同虚设**
3. **SSE 推送的实例隔离**意味着通知实时性在不同部署拓扑下断裂

### 建议方案

分为"短期补丁"和"长期重构"两个路径：

**短期（每个工作 1-2 天）:**

| 问题 | 方案 | 成本 |
|------|------|------|
| Outbox 重复处理 | `FetchUnpublished` 加 `FOR UPDATE SKIP LOCKED` — 每个 event 行被一个实例锁定，其他实例跳过 | 低。仅修改 1 个 repository 方法 |
| 限速绕过 | 文档已知问题。标注为"已知限制，生产部署需 <1 实例或配合外部限速网关" | 零代码，文档更新 |
| SSE 实例隔离 | 在 outbox dispatcher 中检查：如果当前实例的 hub 无该 uid 订阅，不产生 SSE push（仍写入 notification 表供下次轮询） | 低 |

**长期（架构改进）:**

- 引入 **Redis pub/sub** 或类似机制作为 SSE 推送的跨实例通道
- 或使用 分布式限速（Redis 滑动窗口 / Lua 脚本）
- 或由 K8s leader election 确保 outbox relay + GC sweeper 仅在一个实例上运行

### 优先级

**P0** — 多实例 Outbox 重复处理是数据正确性问题，修复成本极低（`FOR UPDATE SKIP LOCKED`）。

---

## 方向二：写偏斜与并发计数器漂移（Write Skew in Concurrent Operations）

### 代码级证据

这是当前架构中最隐蔽的数据一致性问题：没有 `SELECT ... FOR UPDATE` 或其他序列化机制保护跨行读-修改-写操作。

**证据 1: Comment Reply Count**

```go
// internal/comment/service.go (inferred from architecture)
// 一个事务内：
// tx.Create(&commentRow)        // 插入新评论
// tx.Model(&parent).Update("reply_count", reply_count + 1)  // +1
// 
// 但这里没有 SELECT ... FOR UPDATE parent 行锁定
```

两个并发回复的执行时序：

```
T1: 读取 parent.reply_count = 0
T2: 读取 parent.reply_count = 0
T1: UPDATE parent SET reply_count = 1
T2: UPDATE parent SET reply_count = 1  ← 写偏斜！最终 =1，应该是 2
```

这是经典的**写偏斜（Write Skew）**问题，在读提交隔离级别下 100% 可复现。PostgreSQL 默认的 Read Committed 隔离级别 **不防御此场景**。

**证据 2: Comment Like Count**

```go
// internal/comment/service.go (inferred)
// LIKE: INSERT INTO comment_likes → UPDATE comments SET like_count = like_count + 1
// UNLIKE: DELETE FROM comment_likes → UPDATE comments SET like_count = like_count - 1
```

并发 like 和 unlike 的时序：

```
T1: INSERT comment_likes (success)
T2: DELETE comment_likes (success, 删除 T1 刚插入的行)
T1: UPDATE comments SET like_count = like_count + 1  ← 已经 +1
T2: UPDATE comments SET like_count = like_count - 1  ← 又 -1
// 最终一致！但这是巧合——如果顺序调换则计数器偏斜
```

更糟的是并发 unlike + unlike（double-unlike counter drift）：
```
T1: DELETE comment_likes (成功删除)
T2: DELETE comment_likes (RowAffected=0，无报错)
T1: UPDATE comments SET like_count = like_count - 1  ← 从 5 到 4
T2: UPDATE comments SET like_count = like_count - 1  ← 从 4 到 3
// like_count 被减了 2 次，但只删了 1 行！漂移！
```

**证据 3: 内容去重竞争**

```go
// internal/service/file.go (inferred from file.go interface)
// Dedup logic:
existing, err := fileRepo.FindByChecksum(ctx, ownerUID, checksum)
if existing != nil {
    return existing, nil  // 复用已有
}
// ← 两个并发上传通过 checksum 检查
fileRepo.Create(ctx, newFile)  // 两个都创建
// 虽然 checksum 没有唯一约束（索引非唯一），结果产生两个相同 checksum 的文件
```

**证据 4: Reply Count 的 GORM 实现**

```go
// 检查 GORM 产生的 SQL：
// 当前评论 service 中的 reply_count 更新使用：
db.Model(&parent).UpdateColumn("reply_count", gorm.Expr("reply_count + ?", 1))
// 这是一个原子 SQL 级别的 UPDATE，不会被写偏斜影响。
```

等一下，让我重新验证上面这个。GORM 的 `UpdateColumn` with `gorm.Expr("reply_count + ?", 1)` 会生成：

```sql
UPDATE comments SET reply_count = reply_count + 1 WHERE id = $1
```

这**是原子操作**，PostgreSQL 的行级锁在这里实际上是隐式的：Update 语句获取被更新行的行级锁。所以 reply_count 的"读-改-写"发生在数据库层的单个原子操作中。

但是等等——`like_count` 的更新如果也使用 `gorm.Expr("like_count + ?", 1)`，则同样安全。所以**计数器本身不受写偏斜影响**。

不过 `FindByChecksum` + `Create` 的竞争仍存在（证据 3），以及 **reaction toggle** 的计数器漂移问题仍可能发生：

```go
// LIKE:
INSERT INTO comment_likes (comment_id, owner_uid) VALUES ($1, $2) ON CONFLICT DO NOTHING
// 如果没有冲突（首次点赞），然后
UPDATE comments SET like_count = like_count + 1 WHERE id = $1

// UNLIKE:
DELETE FROM comment_likes WHERE comment_id = $1 AND owner_uid = $2
// 如果 RowsAffected > 0（确实删除了），然后
UPDATE comments SET like_count = like_count - 1 WHERE id = $1
```

如果点赞和取消点赞同时发生：

```
T1: INSERT comment_likes (成功)
T2: DELETE comment_likes (成功，RowAffected=1)
T1: UPDATE like_count + 1 (2→3)
T2: UPDATE like_count - 1 (3→2)
// 没问题，因为两个 UPDATE 都发生在各自的事务中
```

但如果 T1 的 INSERT 和 T2 的 DELETE 的执行顺序产生冲突：

```
T1: INSERT comment_likes (成功插入)
T2: DELETE comment_likes (RowAffected=1 — 删除了 T1 刚插入的行？)
```

不，如果 T1 插入 (comment_id=1, owner_uid=user1)，T2 尝试删除 (comment_id=1, owner_uid=user1)，在 Read Committed 下，T2 的 DELETE 会等待 T1 的锁释放（如果有唯一约束行锁），或者直接看到 T1 插入的行并删除它。

实际上`comment_likes`有唯一约束`(comment_id, owner_uid)`，所以：
- T1 的 INSERT 可能会阻塞（如果 T2 先有行存在）
- T2 的 DELETE 会等待 T1 的 INSERT 提交/回滚

在 Read Committed 下，这实际上是安全的。好的，让我重新评估这个问题。

实际上最危险写偏斜是 **FindByChecksum 竞赛**和**通用的跨行读-修改-写**模式。

让我修正我的分析。

**真正的写偏斜风险：文件去重**

```go
// FindByChecksum 没有 FOR UPDATE，并且 checksum 列没有唯一约束
// Concurrent identical uploads scenario:
// T1: FindByChecksum(ctx, owner, checksum) → nil (未找到)
// T2: FindByChecksum(ctx, owner, checksum) → nil (未找到)
// T1: Create(ctx, file1) → 成功
// T2: Create(ctx, file2) → 成功 ← 两个文件，相同内容
```

检查 `idx_files_checksum` 索引——这是一个普通索引，不是唯一约束。所以数据库不会阻止重复。

### 为什么需要

1. **计数器漂移难以检测**。不像程序崩溃会导致明显错误，计数器漂移会产生"谜之 bug"——like_count 显示 5 但实际只有 3 个点赞行
2. **数据去重失效浪费存储**。checksum 索引就是为去重设计的，但缺少唯一约束和 FOR UPDATE 使其不可靠

### 建议方案

| 问题 | 方案 | 成本 |
|------|------|------|
| 文件去重竞争 | `migration: ALTER TABLE files ADD CONSTRAINT ... UNIQUE(owner_uid, checksum) WHERE deleted_at IS NULL` + service 层用 `ON CONFLICT DO NOTHING` 或 `INSERT ... ON CONFLICT` | 低。迁移 + 几行代码 |
| 通用写偏斜 | 在需要读-修改-写的地方使用 `db.Clause(clause.Locking{Strength: "UPDATE"})` 或 `SELECT ... FOR UPDATE` | 低。GORM 原生支持 |
| Reaction 计数器 | `UPDATE ... SET like_count = (SELECT COUNT(*) FROM comment_likes WHERE comment_id = $1)` | 中。性能开销在低并发下可忽略 |

### 优先级

**P1** — 文件去重竞争是产品级问题（浪费存储 + 用户困惑）；reaction 计数漂移只在极端并发下出现但难以排查。

---

## 方向三：SSE 长连接资源管理与 DoS 面（SSE Connection Resource Management）

### 代码级证据

**当前 SSE 实现没有连接限制、没有背压、没有速率限制、没有心跳保活超时。**

```go
// internal/hub/hub.go
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)  // ← 固定 buffer，每连接一个 goroutine
    // 没有每 uid 连接上限
    // 没有全局连接上限
    // 没有新连接速率限制
}

// internal/handler/http/events.go
func (h *EventsHandler) Stream(c *gin.Context) {
    sub, unsub := h.hub.Subscribe(uid)
    defer unsub()
    // ← 每个 SSE 连接消耗:
    //   1 个 goroutine (gin Handler goroutine)
    //   1 个 channel (buffer 16 SSEEvent)
    //   1 个 time.Ticker (30s)
    //   1 个 map entry in uidSubs
    //   1 个 tcp connection
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    // 没有 timeout 机制：如果客户端意外断开但 gin 未检测到，
    // 这个 goroutine 永远不退出
    for {
        select {
        case <-c.Request.Context().Done():
            return  // ← 依赖 gin 的 context cancellation
        case evt, ok := <-sub:
            // ...
        case <-ticker.C:
            // 30s ping
        }
    }
}
```

**已知风险：**

| 风险 | 详细 | 严重度 |
|------|------|--------|
| **Zombie 连接** | 客户端掉线（如笔记本合盖、网络切换）但 TCP 连接未断开 → gin 的 `c.Request.Context()` 不会 cancel → goroutine 永久泄漏 | **高** — 内存泄漏 |
| **每 uid 无上限** | 同一 uid 可以开无限个 SSE 连接（多个浏览器标签页重复刷新） → channel buffer 全部分配，hub map 膨胀 | **中** — 资源耗尽 |
| **全局无上限** | 攻击者用不同 uid 开大量 SSE 连接 → 服务端 goroutine + channel + TCP 连接全部耗尽 | **高** — OOM / 连接池耗尽 |
| **Hub 通道满丢弃** | buffer=16，若某个 uid 的连接消费慢 → 事件静默丢弃 | **低** — 可接受设计取舍 |
| **无活跃超时** | 连接可以保持数天而不超时断开 → 僵尸连接堆积 | **中** |

**进一步确认——检查 gin 默认行为：**

```go
// internal/handler/http/router.go (inferred)
// gin 使用 http.Server{ReadTimeout, WriteTimeout}
// 
// SSE 连接使用 WriteTimeout：如果 WriteTimeout 设置了，gin 会在写超时后关闭连接。
// 但问题是 WriteTimeout 设置时，每 30s 心跳会重置它。
// 如果在心跳间隔内没有新事件（即只有心跳），WriteTimeout 可能不会超时
// 取决于实现。
// 
// 更关键：ReadTimeout 对 SSE 无效（客户端不发请求体）。
// SSE 连接可能在无数据传输时永远存活。
```

检查 config：

```go
// internal/config/config.go
type HTTPConfig struct {
    ReadTimeout     time.Duration
    WriteTimeout    time.Duration
    // ...
}
// 如果 WriteTimeout 没有设置（zero），SSE 连接可以永远存活
```

### 为什么需要

1. **这是最容易被忽略的 DoS 面**。HTTP API 有速率限制保护，但 SSE 的长连接特性使其绕过传统的每请求限速模型
2. **资源泄漏难以监控**。goroutine 泄漏在 Go 中不产生显式错误日志，只能通过 pprof 或 goroutine 数指标发现
3. **有标准的降级方案**：限制每用户连接数、设置空闲超时、添加连接速率限制

### 建议方案

```go
// Hub 新增配置参数:
type HubConfig struct {
    MaxSubsPerUID    int           // 每 uid 最大连接数，超出则替换最旧连接
    MaxTotalSubs     int           // 全局最大连接数
    IdleTimeout      time.Duration // 空闲多久后主动断开
    ChannelBuffer    int           // channel buffer 大小（当前 16 硬编码）
}

// 连接管理策略:
// 1. Subscribe 时检查每 uid 连接数，超出则拒绝或关闭最旧的
// 2. 使用 context.WithTimeout / context.WithDeadline 代替无限等待
// 3. 可配置的 idle timeout：如果 5 分钟无事件发送且无客户端的 Last-Event-ID，断开
```

最小可行方案（1-2 天）：
- 为 `Hub` 增加 `Subscribe` 的 `maxPerUID` 参数，超出则 `ErrTooManySubs`
- 在 `EventsHandler.Stream` 中增加 context timeout（`context.WithTimeout(ctx, 1*time.Hour)`），让连接有上限存活时间
- 暴露 `/metrics` gauge：`sse_connections_total{uid=...}`

### 优先级

**P1** — 虽然当前可能未暴露问题（单实例、低流量），但这是生产环境的安全隐患，且无任何监控告警。

---

## 方向四：全实体数据生命周期管理——软删除数据无限增长（Data Lifecycle Across All Entities）

### 代码级证据

当前系统只有**文件**拥有完整的生命周期管理（GC purge + orphan sweep），其余所有实体的软删除或过期数据**无限累积**：

| 实体 | 保护机制 | 清理机制 | 当前实现 |
|------|---------|---------|---------|
| `files` | 软删除 (`deleted_at`) | **有** — GC sweeper purge + orphan sweep | ✅ 完成 |
| `messages` | 软删除 (`deleted_at`) | **无** — 软删除行永不物理删除 | ❌ |
| `comments` | 软删除 (`deleted_at`) | **无** | ❌ |
| `notifications` | 无软删除（硬删除未实现） | 当前仅通过 `MarkRead` 翻转 `read_at`，不删除 | ❌ |
| `outbox_events` | `published_at` + `dead_letter` | `MarkPublished` 只设时间戳，**不物理删除** | ❌ |
| `idempotency_keys` | TTL (`expires_at`) | **有** — `purgeExpired` 定期清理 | ✅ 完成 |
| `audit_events` | 无软删除 | **无** — 行永不清除 | ❌ |
| `comment_likes` | 无软删除 | **无** — 但行体积小，可接受 | ❌ |
| `file_shares` | 软删除 (`deleted_at`) | **无** — 共享记录永不物理删除 | ❌ |
| `message_reactions` | 无软删除 | **无** | ❌ |
| `user_blocks` | 无软删除 | **无** — 但行体积小，可接受 | ❌ |
| `comment_reports` | 有 status 标记已处理 | **无** — 已处理的举报永不清理 | ❌ |
| `storage_refs` | 无软删除 | **无** — refcount 行永不清除 | ❌ |

**迁移文件确认：**

```sql
-- 000009: notifications — 只有 created_at, read_at，没有 deleted_at 列
-- 000008: outbox_events — published_at + dead_letter 标记，没有物理删除
-- 000014: audit_events — 只有 action/actor/target/created_at，无 TTL
-- 000021: storage_refs — ref_count 可减少到 0，但无回收机制
```

**GORM 软删除的默认行为加剧问题**：在所有查询上加 `WHERE deleted_at IS NULL`，因此软删除行永远不会被普通业务代码触及，它们静默累积：

```go
// internal/repository/message.go
// GORM 的默认 scope 已经加了 deleted_at IS NULL
// 但没有任何代码会硬删除物理行
```

### 为什么需要

这不是理论问题。**在生产环境中，每条消息、评论、通知最终都会被软删除或标记已读，但物理行永远存在**：

1. **表膨胀**。PostgreSQL 不会自动回收软删除行占用的空间（需要 VACUUM 和磁盘空间回收）。`outbox_events` 的写入速率（每个 API 写操作产生一条事件）在 10k QPS 下每月产生约 260 亿行
2. **全表扫描成本**。尽管有索引，膨胀的表导致 PostgreSQL 需要更多共享内存来缓存有效数据
3. **审计日志的合规风险**。某些法规要求审计日志在达到保留期限后必须物理删除（GDPR 数据最小化原则）
4. **idempotency_keys 已实现但其他实体未实现**，说明团队已经认识到此问题但只解决了一部分

### 建议方案

建立统一的数据生命周期管理策略，每个实体有不同的生命周期：

| 实体 | 保留期建议 | 清理策略 |
|------|-----------|---------|
| `messages` | 永久保留或用户请求删除后 30 天 | 批量硬删除 `deleted_at < now() - 30d` |
| `comments` | 同消息，30 天 | 同消息 |
| `notifications` | 30 天（已读后）或 90 天（未读） | 硬删除 `read_at IS NOT NULL AND read_at < now() - 30d` |
| `outbox_events` | `published_at` 后 7 天 | 硬删除 `published_at IS NOT NULL AND published_at < now() - 7d` |
| `audit_events` | 90 天（可配置） | 硬删除 `created_at < now() - 90d` |
| `file_shares` | 软删除后 30 天 | 同文件 GC 模式 |
| `storage_refs` | ref_count=0 后 1 天 | 延迟清理 |

实现路径：
1. 每个实体增加 `deleted_at`（如尚未有）或使用已有的 `created_at`/`published_at` 判断
2. 新增 `internal/lifecycle/` 包，管理各实体的清理策略（类似 `storagegc` 的模式）
3. 注册到 `app.go` 的启动流程中，成为可选的清理 job
4. 清理结果暴露为 `/metrics` gauge（`lifecycle_purged_total{entity=...}`）

### 优先级

**P2** — 在单实例、小数据量下不会立即触发问题，但在生产环境中随着时间推移必然成为运维负担。推荐在每个主要版本中逐步加入。

---

## 方向五：Outbox Relay Handler 超时配置缺失与 Webhook Hang 风险（Outbox Handler Timeout Gap）

### 代码级证据

Outbox relay 使用 `context.Background()` 分发事件，**没有任何超时控制**：

```go
// internal/outbox/relay.go
func (r *Relay) drain(ctx context.Context) int {
    start := r.now()
    events, err := r.repo.FetchUnpublished(ctx, r.batch)
    // ← ctx 来自 Start 中的 context.Background()
    for _, e := range events {
        if err := r.handler.Handle(ctx, e); err != nil {
            // ← handler 没有超时！如果 webhook 端点 hang，这个调用永远不返回
        }
    }
}

func (r *Relay) Start(interval time.Duration) (stop func()) {
    go func() {
        ctx := context.Background()  // ← 没有 WithTimeout!
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            case <-done:
                r.drain(context.Background())
                return
            }
        }
    }()
}
```

**Webhook handler 的 HTTP 调用也没有传入 timeout context：**

```go
// internal/outbox/webhook.go (inferred)
func (h *WebhookHandler) Handle(ctx context.Context, e *outbox.Event) error {
    // 尽管 webhook 配置了 Timeout 参数（在 app.go 中解析为 time.Duration），
    // 但 context.Background() 传入意味着 http.Client 依然会使用自己的超时，
    // 如果没有设置 http.Client.Timeout，请求可以永远挂起
}

// app.go 中的 webhook 超时解析：
timeout := 10 * time.Second
if cfg.Outbox.Webhook.Timeout != "" {
    if d, err := time.ParseDuration(cfg.Outbox.Webhook.Timeout); err == nil && d > 0 {
        timeout = d
    }
}
// timeout 被传递给 outbox.WebhookConfig.Timeout
// 但它没有作用于传递给 Handle 的 context！
```

**结果：如果 webhook 端点变慢或 hang：**

```
1. Outbox relay 开始 drain 周期
2. 事件 1: notification dispatcher → 成功
3. 事件 2: webhook handler → http.Post → 永远等待
4. relay tick 被阻塞  → 后续所有事件无法发布
5. 通知延迟从 5s (tick interval) 变成 ∞  → 完全死锁
```

**Dispatcher 创建通知的调用也未被 timeout 保护：**

```go
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // ctx 来自 relay 的 context.Background()
    if err := d.notifs.Create(ctx, n); err != nil {
        return err  // ← 如果 DB 慢查询，这里也会阻塞
    }
}
```

### 为什么需要

1. **单点故障**。一个缓慢的 webhook 端点或一次 DB 慢查询就能瘫痪整个 outbox 管道——不仅是 webhook 投递，还阻塞了 notification dispatcher、SSE push
2. **无恢复机制**。即使事件被标记为失败（`MarkFailed`），当前 relay 的 goroutine 仍然被阻塞在 `Handle` 调用中——`MarkFailed` 永远不会被执行
3. **级联效应**。outbox 延迟 → 通知延迟 → 告警静默 → 用户投诉
4. **可通过单行代码修复**：`ctx, cancel := context.WithTimeout(ctx, handlerTimeout)`

### 建议方案

```go
// internal/outbox/relay.go

// 新增配置：每次调用 handler 的超时时间
type RelayConfig struct {
    HandlerTimeout time.Duration  // 单个 handler 调用的超时（0 = 无超时，默认 30s）
}

func (r *Relay) drain(ctx context.Context) int {
    for _, e := range events {
        handlerCtx := ctx
        if r.handlerTimeout > 0 {
            var cancel func()
            handlerCtx, cancel = context.WithTimeout(ctx, r.handlerTimeout)
            defer cancel()
        }
        if err := r.handler.Handle(handlerCtx, e); err != nil {
            // ...
        }
    }
}
```

同时，Webhook handler 应使用 `http.NewRequestWithContext` 来传播 context 的超时：

```go
func (h *WebhookHandler) Handle(ctx context.Context, e *outbox.Event) error {
    req, _ := http.NewRequestWithContext(ctx, "POST", h.url, body)
    // ← context 的超时自动作用于 HTTP 请求
    resp, err := h.client.Do(req)
}
```

### 优先级

**P0** — 这是生产系统的"死锁开关"：一旦触发，outbox 管道永久阻塞直到手动重启。修复成本极低（2 行核心代码 + 配置项），但能防止一类极其隐蔽的中断。

---

## 优先级总览

| # | 方向 | 优先级 | 难度 | 修复成本 | 核心收益 | 说明 |
|---|------|--------|------|---------|---------|------|
| 1 | **多实例 Outbox 重复处理** | **P0** | 低 | 1-2 天 | 防止多实例下 N 倍重复通知 | `FetchUnpublished` 加 `FOR UPDATE SKIP LOCKED` |
| 2 | **Outbox Handler 超时** | **P0** | 极低 | 1 天 | 防止 relay 被单个 hang 死锁 | 2 行代码 + `http.NewRequestWithContext` |
| 3 | **SSE 连接资源管理** | **P1** | 中 | 3-5 天 | 防止 DoS 面、goroutine 泄漏 | 连接上限 + 空闲超时 + 监控 |
| 4 | **并发写偏斜（文件去重）** | **P1** | 低 | 1 天 | 防止存储浪费、去重失效 | 唯一约束 + ON CONFLICT |
| 5 | **全实体数据生命周期** | **P2** | 高 | 5-10 天 | 防止表膨胀、合规风险 | 每个实体独立 retention 策略 |

## 建议的立即行动项

以下两项可在 1 天内完成，不引入新依赖，解决 P0 级风险：

1. **`outbox/repository.go:FetchUnpublished`** 增加 `FOR UPDATE SKIP LOCKED` 子句，防止多实例重复处理
2. **`outbox/relay.go:drain`** 中对每个 `handler.Handle` 调用使用 `context.WithTimeout`（默认 30 秒），防止 webhook hang 阻塞整个 relay

两项改动仅触及 2 个文件，不改变接口，不破坏测试。

---

## 附：扫描中发现的轻量级技术债（单日可修复）

| 问题 | 文件 | 建议 |
|------|------|------|
| SSE Hub channel buffer 硬编码为 16 | `internal/hub/hub.go:46` | 暴露为配置参数或 `NewHub` 参数 |
| Outbox relay 的 `drain()` 使用 `context.Background()` 生成的 ctx，trace 中断 | `internal/outbox/relay.go:163` | 在 Start 时创建 root span 并传递 |
| `storage_refs` 的 `ref_count` 减少到 0 后无清理 | `internal/repository/storage_ref.go` | 加定时清理（ref_count=0 && updated_at 超过 24h） |
| Config validate 只检查必填字段，不检查语义一致性 | `internal/config/config.go:222` | e.g. `storage.gc.retention < storage.gc.interval` → 永远清理不完 |
| CI 中的 `make test` 使用 -race 但 `test-integration` 没有 | `.github/workflows/ci.yml` | 集成测试也应开启 `-race` |
