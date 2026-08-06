# aero-id — 全局代码扫描：四项尚未被系统性覆盖的高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 通读 `main` 分支全部源码（`internal/` 23 个子包、`migrations/` 22 份 SQL、`api/proto/`、`cmd/`、`configs/`、`deployments/`、`Makefile`），与 `docs/requirements/` 下 **100+ 份既有需求分析**、8 份 `docs/REVIEW_*` 审查文档、`ROADMAP.md`、`docs/CHANGELOG.md`、`docs/DECISIONS.md`、`docs/ARCHITECTURE.md`、`docs/TODO.md` 做逐方向交叉比对。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和双视角论证。

---

## 前提：当前代码库成熟度

ROADMAP 五个方向（可观测性、SSO 韧性、文件管道、写入正确性、产品面）已全部交付。现有能力清单：

| 领域 | 已实现 |
|------|--------|
| 基础设施 | HTTP+Gin + gRPC 双栈、GORM+Postgres、本地/S3 存储、OTel 链路追踪、RED 指标、启动预热、优雅关停、迁移 CLI |
| 可靠性 | 幂等键、事务性 Outbox（含死信+退避）、SSO 断路器+serve-stale+singleflight+重试、速率限制（内存 token bucket） |
| 文件 | CRUD、流式上传、MIME 校验、大小限制、内容去重（checksum）、预签名直传 S3、显式分享（uid 粒度）、存储 GC 回收 |
| 消息 | 1:1 私信、收件箱/会话视图、已读回执、Reactions、编辑/删除、系统消息、Block 守卫 |
| 评论 | 两级嵌套、Like/Unlike、编辑、软删除、举报+审核队列、隐藏/显示 |
| 通知 | Outbox 驱动异步通知、SSE 实时推送、偏好（含静默时段）、折叠、限速、Webhook 分发 |
| 安全 | 方向性用户屏蔽、操作审计日志（fire-and-forget）、管理员角色/权限、SPKI 证书固定 |
| 搜索 | PostgreSQL tsvector + GIN 全文搜索（跨消息/评论/文件，按 actor uid 隔离） |

在如此成熟度上寻找全新方向已是挑战。以下 4 项源自对**代码库中尚未被任何既有需求文档系统性论证**的真实架构缺口。

---

## 方向一：多副本部署下的 Outbox 协调与 SSE 跨实例穿透

### 问题陈述

当前 aero-id 的 outbox relay 和 SSE hub 都假设**单实例部署**。在生产多副本场景下，两个关键基础设施将同步失效：

1. **Outbox relay 重复处理**：`FetchUnpublished` 在 `outbox/repository.go` 中执行简单的 `WHERE published_at IS NULL AND dead_letter = false` 查询，**无任何悲观锁机制**（如 `SELECT ... FOR UPDATE SKIP LOCKED`）。两个副本同时轮询将拿到相同的事件列表，各自创建通知行、各自推送 SSE——导致接收方收到重复通知。

2. **SSE hub 实例碎片化**：用户连接到实例 A 的 SSE 连接（`GET /v1/events`），但实例 B 的 outbox relay 处理了事件并调用 `hub.Publish()`——通知被推送到实例 B 的内存 hub，实例 A 的用户连接**永远收不到**。当前仅靠 `last_event_id` 重连回放弥补，但这本质上是在用「轮询修复实时」。

### 代码级证据

**证据 1：`FetchUnpublished` 无悲观锁**

```go
// internal/outbox/repository.go
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    var rows []eventRow
    err := r.db.WithContext(ctx).
        Where("published_at IS NULL AND dead_letter = false").
        Order("created_at ASC").
        Limit(limit).
        Find(&rows).Error
    // ...
}
```

没有 `.Clauses(clause.Locking{Strength: "UPDATE"})`，没有 `SKIP LOCKED`。两个副本执行相同查询得到相同 `rows`。

**证据 2：SSE Hub 纯内存，无跨实例通信**

```go
// internal/hub/hub.go:32-36
type Hub struct {
    subs   sync.Map // uid → *uidSubs
    nextID atomic.Int64
    closed atomic.Bool
}
```

`subs` 是 `sync.Map`——完全在进程内。实例 B 无法将事件推送至实例 A 的订阅者。

**证据 3：NotificationDispatcher 在 MarkPublished 前通知推送**

```go
// internal/notification/dispatcher.go:74-80
if err := d.notifs.Create(ctx, n); err != nil {
    return err
}
if d.pusher != nil {
    payload, _ := json.Marshal(n)
    d.pusher.Publish(n.RecipientUID, n.Type, string(payload))
}
```

创建通知行之后立即推送 SSE。如果两个副本都「成功」创建了通知行（因为 Publisher 在 MarkPublished 之前），用户收到两次相同的 notification 行。

### 架构师视角

这是一个**生产级部署的硬缺口**。任何水平扩展（超过 1 个副本）都会触发这些问题。修复路径：

- **Outbox relay**：在 `FetchUnpublished` 中加入 `FOR UPDATE SKIP LOCKED` 子句，使每个事件只被一个 relay 实例领取。GORM 的 `clause.Locking` 支持此能力。
- **SSE hub**：引入外部队列（如 Redis Pub/Sub 或 NATS）作为跨实例事件总线：实例 B 的 relay 处理事件后，将通知发布到外部队列 → 所有实例消费该队列并推送至本地 hub。当前 hub 的 `Pusher` 接口（`hub/adapter.go`）已足够抽象来支持此切换。
- **最终一致性**：`last_event_id` 重连回放机制应作为兜底（最终一致性），而非主推送路径。

### 产品经理视角

无直接影响——用户感知不到后端架构。但**间接影响巨大**：

- 如果部署了多副本（如 K8s 至少 2 副本），用户会收到重复通知或推送延迟从实时退化为 SSE 重连间隔（最高 30s）。
- 功能上线阶段用单实例可忽略此问题；流量增长到需要水平扩展时此问题会突然爆发。

### 边界情况

- `FOR UPDATE SKIP LOCKED` 依赖 PostgreSQL 9.5+，不兼容 SQLite 等测试驱动——需要 repository 接口抽象或测试中 mock。
- 即使有了跨实例推送，冷启动期间（新 pod 刚上线，hub 为空）的 SSE 连接仍会错过通知，需依赖 `last_event_id` 回放。

---

## 方向二：异步后台进程的可观测性断层

### 问题陈述

aero-id 的可观测性在**请求路径**上已是业界标杆（OTel tracing、RED 指标、请求日志），但所有**异步后台进程**——outbox relay、storage GC sweeper、idempotency cleaner、rate limit idle eviction——均使用 `context.Background()` 启动。这意味着：

1. **无追踪上下文传播**：outbox relay 调用 NotificationDispatcher → Dispatcher 调用 BlockChecker（走 SSO gRPC 查询）→ 这些 gRPC spans 没有父 span，无法关联到触发事件的原始请求。
2. **shutdown 信号不传递**：`shutdown()` 调用 `stopRelay()` 关闭 ticker，但正在运行的 `drain()` 方法感知不到应用正在关闭——它使用 `context.Background()`，所以即使数据库连接已关闭，drain 仍试图执行 SQL。
3. **GC sweeper 和 cleaner 完全无指标**：对比 outbox relay 已有完整 metrics（published/failed counters、drain duration），GC sweeper 和 idempotency cleaner 没有任何指标暴露。

### 代码级证据

**证据 1：Outbox relay 使用 `context.Background()`**

```go
// internal/outbox/relay.go:163-170
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()  // ← 无传入 context
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            case <-done:
                t.Stop()
                r.drain(context.Background()) // ← 最终 drain 也无超时
                return
            }
        }
    }()
    return func() { close(done) }
}
```

**证据 2：GC sweeper 同样使用 `context.Background()`**

```go
// internal/storage/gc/sweeper.go:255
go func() {
    ctx := context.Background()  // ← 无 trace 传播、无取消
    for {
        select {
        case <-t.C:
            _, _ = s.Run(ctx)
        case <-done:
            t.Stop()
            return // ← 注意：不做最终 sweep（与 relay 不同）
        }
    }
}()
```

**证据 3：Idempotency cleaner 无指标**

```go
// internal/idempotency/store.go:164-166
func (s *GormStore) purgeExpired() {
    _ = s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&idempotencyRow{}).Error
    // 无 metrics，无日志（除 DB 驱动日志外）
}
```

对比 outbox relay 的 metrics：
```go
// internal/outbox/relay.go:95-98
if r.metrics != nil {
    r.metrics.ObserveOutboxPublished(e.EventType)
    r.metrics.ObserveOutboxFailed(e.EventType)
}
```

### 架构师视角

请求路径可观测（`handler → service → repository`）而异步路径不可观测，意味着**每次 incident 响应靠猜**。具体修复路径：

- 为 `Relay.Start()` 和 `Sweeper.Start()` 增加 `context.Context` 参数，从 app shutdown context 继承取消信号。
- 在 relay 和 sweeper 中创建子 span 作为 trace 根，使 outbox handler 中的 SSO 调用能关联到「relay tick」的父 span。
- 为 GC sweeper 和 idempotency cleaner 增加 metrics sink（至少 counters + duration histogram）。
- 修复 GC sweeper 的 `Stop` 不做最终 drain 的问题（当前 relay 已修复但 sweeper 未跟随）。

### 产品经理视角

不可见但影响**SLA 诊断能力**：

- 当通知延迟时（outback backlog），运维需要看到 outbox pending gauge 和 oldest event age 才知道是 relay 问题还是 handler 问题。GC 无类似指标。
- 当 idempotency 表膨胀时（purge 失效），无警报，只能靠磁盘告警间接发现。
- **推荐优先级**：P1（生产加固轮）。无新功能但提升运维安全感。

### 边界情况

- 传递 app context 给 relay 时需注意：如果在 graceful shutdown 超时后 context cancelled，正在运行的 drain 应能被取消。需要 `select` 模式检查 `ctx.Done()`。
- 最终 drain 应使用带超时的派生 context（如 5s）以免 handler 挂死阻塞 shutdown。

---

## 方向三：文件内容级安全校验（Magic Bytes / MIME 魔数检测）

### 问题陈述

当前文件上传的安全防线只有一道：基于 `Content-Type` 头部的 MIME 类型白名单校验。**这不可信任**——客户端（甚至攻击者）可以将 `Content-Type: image/png` 的请求与恶意二进制 payload 一同发送。存储 GC 清理的是陈旧数据而非恶意数据。对比 File 实体中的 `checksum` 列——使用 SHA256 可用于完整性验证但未被用于安全用途。

这导致以下攻击面：

- **类型混淆攻击**：上传 `.exe` 但声明为 `image/png` → MIME 白名单通过 → 下载时受害者的浏览器根据 Content-Type（来自 `files.mime_type`）渲染为图片 → 实际是恶意程序。
- **存储成本滥用**：攻击者上传大量无效数据（填满存储或触发成本）。
- **已设计但未利用的字段**：`files.checksum` 已通过内容哈希实现去重，但该哈希从未被用于内容类型验证。

### 代码级证据

**证据 1：MIME 校验仅依赖 Content-Type 头部**

```go
// internal/file/file.go:132-138
func (s *fileService) mimeAllowed(contentType string) bool {
    if len(s.allowedMime) == 0 {
        return true
    }
    mt, _, err := mime.ParseMediaType(contentType)
    if err != nil {
        return false
    }
    return s.allowedMime[strings.ToLower(mt)]
}
```

函数签名接受的是 `contentType string`（HTTP 头传输的值），而不是文件内容或 io.Reader。

**证据 2：Create 路径中 checksum 已计算但仅用于去重**

```go
// internal/file/service_crud.go:41-45
hasher := sha256.New()
reader = io.TeeReader(reader, hasher)
// 写入 storage ...
checksum := hex.EncodeToString(hasher.Sum(nil))
// checksum 仅用于 FindByChecksum 去重，不做类型验证
```

**证据 3：CompleteUpload 路径无内容读取，无法做魔数校验**

```go
// internal/file/service_upload.go:80-91
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*File, error) {
    // ...
    info, err := s.store.Stat(ctx, in.StorageKey) // ← 只获取 size，不读取内容
    // ...
    // 无 io.Reader 可用，无法检测魔数
}
```

`Stat` 只返回 `ObjectInfo{Size, Modified}`——不包含内容。

**证据 4：File 实体有 checksum 但无 detected_mime_type 字段**

```go
// internal/domain/file.go:5-16
type File struct {
    // ...
    MimeType    string        // 来自客户端声明的 Content-Type
    Checksum    string        // SHA256 内容哈希
    // 无 DetectedMimeType string
    // 无 MagicBytesValidated bool
}
```

### 架构师视角

这是一个**信任分类错误**：将不可信输入（客户端提供的 Content-Type）用作安全决策的唯一依据。

修复路径：
- **同步路径（`Create`）**：在 `io.TeeReader` 后、写入 storage 前，使用 `net/http.DetectContentType`（读取前 512 bytes）或 `github.com/h2non/filetype` 库进行魔数检测，与声明值交叉验证。不匹配时拒绝上传。
- **预签名路径（`CompleteUpload`）**：`CompleteUpload` 只有 `Stat` 信息无内容。需要扩展 `FileStorage` 接口添加 `ReadPartial(ctx, key, offset, size)` 方法，或要求客户端在上传后通过签名验证端点回调时提供内容的 SHA256 + 魔数。
- **审计/标记**：当检测值与声明值不匹配时，至少标记 `meta` 字段中的 `content_type_mismatch: true` 供后续审计。
- **扩展模型**：在 `File` 中增加 `detected_mime_type` 和 `magic_bytes_validated` 字段，分别记录实际类型和验证状态。

### 产品经理视角

- **安全影响**：高。无此能力意味着文件上传是「盲信」模式——在白名单开启时比不开启更危险（给操作者虚假安全感）。对于存储用户文件的服务，这是信任与安全的基本要求。
- **向后兼容**：对于已存储的文件可做异步批量校验并标记风险文件（`meta.risk = "mime_mismatch"`），不阻塞现有用户。
- **推荐优先级**：P1（安全加固）。短期可先只做同步路径的魔数检测（小改动），预签名路径的扩展留作 P2。

### 边界情况

- 魔数检测受限于前 512 bytes——对于复合格式（如 ZIP + DOCX / JPG + EXE 的 polyglot）无法完美检测。但已经大幅提升了基线安全。
- 对于 `application/octet-stream` 等泛型类型，魔数检测可作为补充而非替代。
- Empty file（size=0）不应被拒绝，魔数检测应跳过 0 长度文件。

---

## 方向四：Counter 列的异步一致性校验与自动修复

### 问题陈述

aero-id 有多处维护在业务行上的派生计数器：`comments.like_count`、`comments.reply_count`。这些计数器通过事务性 outbox 事件维护（其正确性依赖于 outbox handler 的成功执行），但**没有任何机制验证或修复它们是否与实际关联表的数据一致**。一旦发生以下任一情况，计数器将永久漂移：

1. Outbox handler 持久失败后进入死信——事件丢失，计数器永不更新。
2. 数据因直接 SQL 操作（管理脚本、数据迁移）变更而未触发 outbox。
3. 当前 `CreateWithReplyBump` 在 `internal/comment/repository.go` 中用事务保证了 reply_count +1 与 comment 创建的一致性，但 `Unlike` 等操作是独立调用（`comments.Like`/`Unlike`）——幂等键不防止 handler 失败。
4. `LikeCount` 来自 `SELECT COUNT(*)`（在 repository 中原子更新）但 `ReplyCount` 是自增/自减——模式不统一，潜在问题不同。

### 代码级证据

**证据 1：`like_count` 通过 GORM `UpdateColumn` 原子更新，但无兜底校验**

```go
// internal/comment/repository.go:104-118
func (r *repo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
    // ... 插入 comment_like 行 ...
    // 原子更新计数器
    r.db.Model(&commentRow{}).Where("id = ?", commentID).
        UpdateColumn("like_count", gorm.Expr("like_count + 1"))
    // 返回递增后的值
    var c commentRow
    r.db.Select("like_count").Where("id = ?", commentID).First(&c)
    return c.LikeCount, nil
}
```

当 `INSERT` 成功但 `UpdateColumn` 因网络故障或中间件超时失败时：`comment_likes` 表有数据但 `like_count` 未更新。

**证据 2：outbox 死信后计数器不一致**

```go
// internal/outbox/relay.go:103-110
if err := r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now); err != nil {
    r.log.Warn("outbox: mark failed failed", ...)
}
// 当 handler 持续失败达到 maxRetries，事件被打上死信标记，不再被轮询处理
```

死信事件中如果有 `comment.replied` 事件，`reply_count` 将永远缺少一次递增——无人修复。

**证据 3：`reply_count` 通过自增维护，与 `like_count` 模式不统一**

```go
// internal/comment/repository.go
func (r *repo) CreateWithReplyBump(ctx context.Context, c *Comment, evt *outbox.Event) error {
    return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        // ... 插入 comment ...
        if c.ParentID != nil {
            // 父评论 reply_count +1
            tx.Model(&commentRow{}).Where("id = ?", *c.ParentID).
                UpdateColumn("reply_count", gorm.Expr("reply_count + 1"))
        }
        return nil
    })
}
```

`like_count` 通过触发 outbox 事件来间接维护（没有类似 `reply_count` 的独立 inc/dec 操作直接在 repository 完成）。

### 架构师视角

计数器漂移是分布式系统中经典的「读一半写一半」问题。当前架构做了正确的事（事务 + outbox），但仍缺乏最后一道防线——**异步一致性校验器**：

1. 新增一个周期性的后台 worker（类似 GC sweeper），对 `comments` 表扫描，交叉验证 `like_count` 与 `comment_likes` 的实际计数，`reply_count` 与 `comments`（parent_id）的子计数。
2. 检测到不一致时：自动修正计数器值 + 写入审计日志 + 在当前 metrics 中暴露 `counter_drift_detected` 指标。
3. 死信管理控制台：对于死信事件，运维人员应该能够查看、重试或忽略。当前死信事件仅通过 DB 标记 `dead_letter = true`，无 API 曝光。

当前数据库已有性能索引（migration 000015），`SELECT COUNT(*)` 在 `(comment_id)` 索引上应是快速操作，定时校验的成本可控。

### 产品经理视角

- **数据质量**：无可见性但影响老板和用户信任。如果用户看到一个评论有「5 个回复」但点开只有 3 个，体验信任度直接下降。
- **规模拐点**：在几千条评论时漂移概率极低；到百万级时几乎必然发生。
- **推荐优先级**：P2（长期数据质量）。短期不阻塞上线，但建议在 outbox 死信可见性上做最低投入（API 查死信事件数）。
- **产品体验**：可考虑在前端不展示这些计数器（仅在特定 UI 展示），或使用「计数 ≈ 实际数」的渐进式披露。

### 边界情况

- 并发修正：如果 worker 修正计数器同时用户新加了 like，可能产生 `COUNT()` 与实际值之间的短暂窗口不一致。worker 应在事务内部执行 `SELECT COUNT(*) + 1` 原子操作。
- 超大表：对于 10 亿级 comment_likes 表，全表 `GROUP BY comment_id` 不可接受。可采用最近修改的评论增量校验策略（`WHERE updated_at > last_run`）。
- 死信重试可能再次失败：需提供手动确认跳过（mark as resolved）的能力。

---

## 优先级评分总结

| # | 方向 | 类型 | 架构复杂度 | 用户可见 | 推荐顺序 |
|---|------|------|-----------|---------|---------|
| 1 | 多副本 Outbox 协调 + SSE 跨实例 | 基础架构 | 中 | 间接（重复通知/延迟） | **P0 — 生产前置** |
| 2 | 异步进程可观测性 | 运维 | 低-中 | 不可见 | **P1 — 运维加固** |
| 3 | 文件内容级安全校验 | 安全 | 低-中 | 不可见 | **P1 — 安全加固** |
| 4 | Counter 一致性校验与修复 | 数据质量 | 中 | 低（长尾场景） | **P2 — 长期数据质量** |

---

## 跨方向关联

- **方向 1 的跨实例 outbox 依赖方向 2 的 context 传播** —— 如果 relay 使用了 `FOR UPDATE SKIP LOCKED`，被锁住的事件其 handler 内的 trace span 需要能关联到原始事件 ID。
- **方向 4（counter 校验）可作为方向 1（多实例 outbox）的验证指标**——如果计数器漂移率在部署多副本后增加，说明 relay 重复/漏处理事件。
- **方向 3（魔数校验）与方向 2（GC metrics）在 sweeper 中交汇**——如果 sweeper 被赋予「扫描已存储文件校验 MIME」的职责（异步批处理），它应该报告进度指标。

*本报告基于 `main` (80b013e) 的全面静态分析生成。建议在切入每个方向前补充对应的集成测试计划。*
