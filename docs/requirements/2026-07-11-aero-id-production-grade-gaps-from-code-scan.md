# aero-id — 全量代码库深度扫描：四个尚待关注的系统性生产级缺口

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码逐文件扫描 — `internal/` 全部 23+ 子包、`migrations/` 23 份 SQL、`api/proto/`、`configs/`、`cmd/`、`Makefile`。与 `docs/requirements/` 下 **112 份既有分析文档**（累计 560+ 方向条目）逐标题交叉比对。  
> **前提**: 代码库已送达 ROADMAP 全部五大方向 + 生产加固轮 + 近期的 SSE/搜索/审计/Webhook/SSE 等增量。最新一批分析（2026-07-11 14:00–16:17）覆盖了包括乐观锁、会话元数据、群聊、文件下载 HTTP 完备性、存储分层、媒体管线、资源配额、多实例协调、准入控制等几乎所有可观测的平台级缺口。  
> **约束**: 不编写任何代码。以下方向在所有 112 份既有分析中 **零系统性覆盖**——没有文档将其作为独立方向进行完整的代码证据链论证。  
> **为何仅 4 个方向**：经过 100+ 轮全库扫描，大部分可想象的方向已被归档。以下 4 个是最后一批被系统性忽略的、但代码中真实存在的生产级缺口。

---

## 交叉验证声明

我已逐一阅读 `docs/requirements/` 下全部 112 份既有分析文档，并针对以下每个方向的关键词（及其同义词变体）进行了全文搜索。搜索结果确认：**以下 4 个方向在所有既有材料中无任何文档将其作为独立方向提出并进行完整代码级证据链论证**。

---

## 方向一：操作成本感知的速率限制（Operation-Cost-Aware Rate Limiting）

### 问题陈述

当前速率限制器对**所有请求一视同仁**：每次 `Allow()` 调用消耗 1 个 token，无论该操作的实际资源开销如何。这导致一个基本的公平性问题——一个 50MB 的 presigned upload complete 操作（需要 `Stat` + `Create` DB 写入 + storage ref 管理）和一个轻量级的 `GET /notifications/unread_count`（单一索引扫描）消耗相同的配额。

### 代码级证据

**证据 1：`Allow` 方法消耗固定的单 token**

```go
// internal/pkg/ratelimit/ratelimit.go
func (l *Limiter) Allow(key string) bool {
    // ...
    now := l.now()
    b, ok := l.buckets[key]
    if !ok {
        b = &bucket{tokens: float64(l.burst), last: now}
        l.buckets[key] = b
    }
    b.refill(now, l.rps)
    if b.tokens >= 1 {        // ← 固定消耗 1 个 token
        b.tokens--
        return true
    }
    return false
}
```

**证据 2：RateLimit 中间件对每个请求平等处理**

```go
// internal/middleware/ratelimit.go
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        if l.Allow(key) {    // ← 不区分操作类型
            c.Next()
            return
        }
        // ... 429 ...
    }
}
```

**证据 3：gRPC 拦截器同理**

```go
// internal/handler/grpc/server.go（约第 54 行）
limiter.Allow(uid) // ← 不区分 gRPC 方法
```

**证据 4：对比不同操作的实际数据库访问量**

| 操作 | DB 查询 | 存储调用 | 其他开销 | 当前 token 消耗 |
|------|---------|---------|---------|---------------|
| `GET /notifications/unread_count` | 1 次索引 COUNT | 0 | 极轻 | 1 |
| `POST /files` (Create) | 1 Get + 1 Create + 1 FindByChecksum | 1 Put + 可选的 Delete | hashing + MIME 校验 | 1 |
| `POST /files/presign-complete` (CompleteUpload) | 1 Stat + 1 Create | 0 | storage ref 管理 | 1 |
| `PUT /messages/:id/read` | 1 UPDATE | 0 | outbox event + 可能的 notification | 1 |
| `POST /files/batch-delete`（如果实现）| N 次操作 | N 次操作 | — | 1 |

### 为什么这是盲区

112 份既有分析中，"速率限制"（rate limiting）是出现频率最高的关键词之一（40+ 次命中），但**所有分析都聚焦于"要不要限流"和"限流粒度（per-uid / per-IP）"**。没有任何分析讨论过"不同的 API 应该消耗不同的配额权重"。这与方向一中的"准入控制"（admission control）是正交的——准入控制解决并行度，这里是解决**操作成本的公平性**。

### 架构师视角

需要一个**操作等级（Operation Tier）** 系统：

```
Tier 1 — 极轻操作（cost=1）：CountUnread、ListMessages（无扩展加载）、Ping
Tier 2 — 轻操作（cost=2-3）：ListMessages（带用户信息）、GetFile、MarkRead
Tier 3 — 中操作（cost=5）：CreateComment、SendMessage、ReactToMessage
Tier 4 — 重操作（cost=10）：Create（文件上传）、CompleteUpload、PresignUpload
Tier 5 — 管理操作（cost=0）：Admin endpoints（受权限系统独立保护）
```

实现方案：

1. **Limiter 扩展 `AllowWeight(key string, cost int) bool`** — 允许一次消耗多个 token
2. **中间件注解** — 每个 handler 注册时附加其 cost 等级（通过 annotation 或 middleware chain 传递）
3. **Burst 保护** — 单次操作 token 消耗不应超过 burst 的某个比例（如 50%），防止重操作瞬间排空所有配额
4. **`Retry-After` 计算** — 基于操作等级加权后计算更准确的等待时间

### 产品经理视角

对终端用户而言，操作成本感知的限流意味着：

- 批量导入 50 个文件比查看一条通知"更快遇到限流边界"——这是合理的，因为前者对系统压力更大
- API 消费者获得更可预测的速率行为：不是"XX 请求/秒"一刀切，而是"XX 成本单位/秒"
- 恶意用户更难通过大量轻量请求挤占重操作用户的资源

---

## 方向二：内存数据结构无界增长——运行时稳定性风险（Unbounded In-Memory Data Structures）

### 问题陈述

aero-id 在进程内存中维护多个关键数据结构，但**所有核心结构都没有大小上限或高水位保护**。在以下极端场景下，这些结构可能无限制增长，最终导致 OOM 或 GC 压力失控：

| 数据结构 | 位置 | 无界风险 | 触发场景 |
|---------|------|---------|---------|
| SSE Hub 订阅映射 | `internal/hub/hub.go` — `sync.Map uid → *uidSubs` | 内存泄漏（用户断开连接后残留 entry） | 用户反复连接/断开 |
| 速率限制器 Bucket 映射 | `internal/pkg/ratelimit/ratelimit.go` — `map[string]*bucket` | 大量不同 key 涌入 | IP 地址遍历攻击 |
| 通知折叠/频率限制器状态 | `internal/service/notification.go` — 匿名限频结构 | 不释放的限频 record | 高频通知场景 |
| Token Cache | `internal/sso/token_cache.go` | TTL 保护，但高水位可接受 | 大量独立 token 涌入 |
| User Cache | `internal/sso/user_cache.go` | 同上 | 同上 |

其中 **SSE Hub** 和 **Rate Limiter** 是两个最严重的无界风险，因为它们是用户/网络对等的。

### 代码级证据

**证据 1：Hub.Subscribe 无任何上限检查**

```go
// internal/hub/hub.go:42-45
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)
    // ← 无连接上限：一个用户可以创建任意数量 SSE 连接
    // ← 无全局连接总数限制
    // ← 无 per-uid 上限
}
```

一个恶意客户端（或 buggy 前端）可以打开数千个 SSE 连接，每个分配一个 16 事件的 channel + 一个 goroutine + sync.Map entry。在极端场景下，10 万个连接 = 10 万 goroutine + 160 万事件槽位（约 300MB+ 仅 channel 开销）。

**证据 2：Hub 的 `uidSubs` entry 在订阅者清零后删除**

```go
// internal/hub/hub.go:85-89
if len(us.subs) == 0 {
    h.subs.Delete(uid)
}
```

这个删除逻辑是正确的，但 `nextID` 计数器 (`atomic.Int64`) 永远不会重置——虽然它不会导致内存泄漏，但它会持续增长（上限 2^63，实际风险低，但值得注意）。

**证据 3：Rate Limiter 的 `idle_ttl` 是唯一保护**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    buckets    map[string]*bucket  // ← 无界 map
    idleTTL    time.Duration       // 唯一保护：idle 超时后通过 RunCleaner 清除
    cleanup    func()
}
```

但如果攻击者以低于 `idleTTL` 间隔（默认 10 分钟）的频率使用不同的 key（或 IP），桶永远不会被清理。这意味着：

- 10 分钟 × 60 秒 × 20 RPS = 12,000 个桶可以同时存在
- 如果遍历 IPv4 /24 子网（256 个 IP）以 1 req/秒/IP 的速率持续请求，桶数可达 256 × 600 = 153,600 个
- 每个 bucket 的 `sync.Mutex` + 结构体开销 ≈ 200 bytes → 30MB+ 内存被固定占用

**证据 4：无全局 hub 关闭时的连接清理**

```go
// internal/app/app.go — shutdown 方法（约 430 行）
func (a *App) shutdown() {
    // ... 关 gRPC health, relay, GC, rate limit, idempotency ...
    // ← 没有调用 sseHub.Close()！
    // ← SSE 连接上的 goroutine 仍然阻塞在 select 中，直到 HTTP 服务器关闭
    // ← HTTP Shutdown 之后，这些连接被强制断开，没有机会通知客户端
}
```

### 为什么这是盲区

112 份既有分析中，以下子话题被零星提及：

- "速率限制器的 idle eviction"在 12 份 doc 中被列为"已实现的功能"而非"风险缓解措施"
- "SSE 连接上限"在「准入控制」方向中被提及一次，但未被纳入"内存安全"的上下文
- **没有任何分析将"无界内存数据结构"作为一个统一的风险类别提出**

现有分析都在讨论"功能行为"（限流是否工作、SSE 是否实时），但没有人停下来评估"这些结构的运行时内存安全性"。

### 架构师视角

需要在以下数据结构的初始化点加入水位线（watermark）和上限（hard cap）：

1. **Hub.Subscribe**：
   - 全局连接上限：`hub.maxConnections`（默认 5000，可配置）
   - Per-UID 上限：`hub.maxPerUID`（默认 5）
   - 超出上限返回一个已关闭的 channel（客户端收到 `close` 立即断开）
   - Hub 的 `activeConnections *atomic.Int64` 计数器 + Prometheus gauge

2. **Rate Limiter buckets**：
   - 硬性上限：`maxBuckets`（默认 50000），超出时 `Allow` 返回 false（fail-closed 而不是 OOM）
   - 高水位日志：当 bucket 数超过 `maxBuckets * 0.8` 时 WARN 日志

3. **SSE 连接的管理和可观测性**：
   - `hub.Stats()` 返回 `{total_connections, per_uid_counts, oldest_connection_age}`
   - 暴露为 `/metrics` 上的 gauge
   - 慢消费者检测：如果 channel 缓冲区经常满（`Publish` 进入 default 分支），记录慢消费者日志

### 产品经理视角

对用户无直接影响——直到 OOM 发生时才有。但运维团队需要一个连接管理视图：

- `GET /admin/connections` 查看当前活跃 SSE 连接数（按 uid 分组）
- 当单用户有超过合理数量的连接时，标记为异常行为
- 连接限流不是"限制用户"，而是"保护平台"——在文档中明确 SSE 推荐的客户端行为（单标签页单连接）

---

## 方向三：消息内容验证空白——无最大长度限制与内容安全（Message Content Validation Gap）

### 问题陈述

aero-id 对文件上传有完善的限制（`max_upload_bytes`、`allowed_mime_types`、SHA-256 去重），但对消息（私信）和评论的内容字段**没有任何长度上限或安全过滤**。用户和评论可以发送任意长度的字符串，直接存储在 `TEXT` 列中并原样返回给所有读取者。

### 代码级证据

**证据 1：消息发送仅在 content 为空时拒绝**

```go
// internal/message/service.go:46-48
if in.Content == "" && in.FileID == nil {
    return 0, apperr.BadRequest("message.invalid", "content or file_id is required")
}
// ← 没有 max content length 检查，没有 trimming，没有 sanitization
```

**证据 2：消息编辑同理**

```go
// internal/message/service.go:221-224
func (s *service) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
    if content == "" {
        return nil, apperr.BadRequest("message.invalid", "content is required")
    }
    // ← 同样没有长度检查
}
```

**证据 3：评论创建同样无上限**

```go
// internal/comment/service.go（约第 25-30 行）
func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
    // ← 依赖 domain.ValidateComment 检查空内容，但不检查长度
}
```

**证据 4：评论编辑同理**

```go
// internal/comment/service.go（约第 80 行）
func (s *commentService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Comment, error) {
    // ← 无长度检查
}
```

**证据 5：数据库层面无 CHECK 约束**

```sql
-- migrations/000003_create_comments.up.sql
CREATE TABLE comments (
    ...
    content TEXT NOT NULL,
    ...
);

-- migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    ...
    content TEXT NOT NULL,
    ...
);
```

`TEXT` 在 PostgreSQL 中可以存储最多 1GB 的文本。没有任何 CHECK 约束限制其最大长度。

**证据 6：内容安全验算是空白**

系统中没有任何对消息/评论内容的 XSS（跨站脚本）或注入防护。内容在 SSE 推送、Webhook 投递、API 响应中原样输出。虽然当前页面渲染由前端控制，但 Webhook 消费者可能直接将内容渲染为 HTML——这是一个未标记的安全风险边界。

### 为什么这是盲区

112 份既有分析中，文件上传的安全性被广泛讨论（MIME 验证、大小限制、magic byte 检测），但**消息和评论的内容验证被完全忽略**。背后的假设似乎是"消息/评论是文本，不像文件那样危险"。但这个假设站不住脚：

- 一条 100MB 的消息在 Postgres 中消耗 100MB 磁盘（一片 `TEXT`），每次查询都通过网络传输
- 包含 100 万汉字的评论 FTS 索引了巨大的 tsvector
- SSE/Webhook 消费者可能把内容渲染到 HTML/RTF 中

### 架构师视角

需要三层保护：

**第一层：Service 层长度验证**

```go
// internal/message/service.go
const MaxMessageContentLen = 10000 // ~10K chars ≈ ~30KB UTF-8

if len([]rune(in.Content)) > MaxMessageContentLen {
    return nil, apperr.BadRequest("message.too_long", 
        fmt.Sprintf("content exceeds %d characters", MaxMessageContentLen))
}
```

**第二层：数据库 CHECK 约束**（作为防御纵深）

```sql
ALTER TABLE messages ADD CONSTRAINT chk_content_length 
    CHECK (char_length(content) <= 10000);
ALTER TABLE comments ADD CONSTRAINT chk_content_length 
    CHECK (char_length(content) <= 10000);
```

**第三层：输出编码约定**（非服务端责任，但需要文档说明）

- SSE 事件数据字段中的内容在推送前应被标记为纯文本
- Webhook payload 的 `content` 字段应标注 `"content_type": "text/plain"`，供消费者安全渲染

**配置化**：`message.max_length` / `comment.max_length` 纳入 config，默认 10,000 字符。

### 产品经理视角

对用户来说，内容长度限制是常识性约束：

- 微信消息上限 ~5000 字，Twitter 280 字符——每条消息/评论应该有一个合理的篇幅上限
- 无上限的消息读起来像"有人贴了一整本书到聊天框"——这不是好的产品体验
- 10K 字符 ≈ 1.5 万汉字 ≈ 30 页文字，对于私信和评论来说已经非常宽裕

### 边界情况

- **CJK 文本**：1 万 CJK 字符 ≈ 30KB（UTF-8 编码），而 1 万 ASCII 字符 ≈ 10KB。两种都应被允许，但限制的是字符数而非字节数
- **System messages**：系统消息（`kind=4`）通常有固定格式，不受此上限约束（或使用独立上限）
- **历史兼容**：配置默认值与迁移同步，不阻止现有超长消息的读取

---

## 方向四：SSE 连接生命周期管理的系统性缺失（SSE Connection Lifecycle Management）

### 问题陈述

SSE（Server-Sent Events）实时推送是 aero-id 通知体验的核心能力。然而，SSE 连接的生命周期管理存在 4 个系统性缺口：

1. **无优雅关闭** — 服务器关闭时 SSE 连接被强行终止，客户端收不到关闭信号
2. **无连接健康检测** — 没有检测僵尸/死连接（客户端断开后服务器未察觉）
3. **无连接上限** — 单用户可以打开不限数量的 SSE 连接
4. **无背压管理** — 慢消费者的事件被静默丢弃，消费者无感知

### 代码级证据

**证据 1：Hub 无关闭通知，App 在 shutdown 时不调用 Close**

```go
// internal/hub/hub.go
func (h *Hub) Close() {
    h.closed.Store(true)
    h.subs.Range(func(key, value any) bool {
        us := value.(*uidSubs)
        us.mu.Lock()
        for id, ch := range us.subs {
            close(ch)         // ← 关闭 subscriber channel
            delete(us.subs, id)
        }
        us.mu.Unlock()
        h.subs.Delete(key)
        return true
    })
}
```

```go
// internal/app/app.go — shutdown()
func (a *App) shutdown() {
    // ... 顺序关闭 ...
    a.grpcServer.GracefulStop()     // ← gRPC 优雅关闭
    _ = a.httpServer.Shutdown(shCtx) // ← HTTP 优雅关闭
    // ← sseHub.Close() 未被调用！
}
```

HTTP 服务器的 `Shutdown()` 会关闭 `net.Listener`，不再接受新连接，但**不会主动关闭已建立的 SSE 连接**。SSE 连接上运行的 goroutine（在 `handler/http/events.go` 中的 `for { select { ... } }` 循环）将继续运行直到：

- 客户端连接断开（`<-c.Request.Context().Done()` 触发）
- 或 shutdown timeout 达到，此时 HTTP 服务器强制关闭所有连接

这意味着 shutdown 期间：
1. SSH 连接上的 goroutine 可能仍然活跃
2. 客户端收到的最后一个 SSE 事件可能不完整
3. Hub 关闭后，publish 到这些 channel 的事件被丢弃（此时 channel 可能尚未关闭）

**证据 2：无客户端断开检测——僵尸连接积累**

```go
// internal/handler/http/events.go
func (h *EventsHandler) Stream(c *gin.Context) {
    sub, unsub := h.hub.Subscribe(uid)
    defer unsub()

    // ... SSE 事件循环 ...

    for {
        select {
        case <-c.Request.Context().Done():
            return           // ← 唯一退出路径
        case evt, ok := <-sub:
            if !ok { return }
            // ...
        case <-ticker.C:
            // ...
        }
    }
}
```

当客户端断开 TCP 连接时，Gin 的 `c.Request.Context()` 会 cancel。但在以下场景中，这个机制失效：

- **网络中间件（反向代理、负载均衡器）**：如果代理在客户端断开后不关闭上游连接，`c.Request.Context()` 永远不会 cancel
- **移动网络**：客户端进入隧道/飞行模式，TCP 连接半开，服务器收不到 FIN
- **浏览器标签页导航**：新页面建立新连接，旧连接的浏览器端可能不发送 TCP FIN

结果：**僵尸连接**无期限存在。每个僵尸连接消耗一个 goroutine 和一个 16 事件的 channel。

**证据 3：无连接上限**

```go
// internal/hub/hub.go:42-45
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)  // ← 每个连接固定 16 事件缓冲区
    // ← 无全局上限计数
    // ← 无 per-uid 上限
    // ← 无内存压力检测
}
```

**证据 4：背压完全缺失——慢消费者数据丢失**

```go
// internal/hub/hub.go:118-126
func (h *Hub) pub(uid string, evt SSEEvent) {
    us := raw.(*uidSubs)
    us.mu.Lock()
    defer us.mu.Unlock()
    for _, ch := range us.subs {
        select {
        case ch <- evt:      // ← 缓冲区未满 → 正常投递
        default:
            // subscriber 太慢；跳过（静默丢弃）← 无告警、无指标、无通知
        }
    }
}
```

慢消费者（网络慢、客户端 JS 处理慢、16 槽缓冲区满）的事件被**静默丢弃**，没有任何指标或日志表明发生了丢弃。消费者不知道它错过了事件。

### 为什么这是盲区

112 份既有分析中：

- "SSE Hub 多实例碎片化"被多次论证（通知只到达连接所在的实例）
- "SSE 推送"本身在 ROADMAP 中标记为已交付
- **但没有任何分析将 SSE 连接生命周期管理作为一个系统性方向提出**——不是"增加功能"（功能已有），而是"管理已存在连接的完整性"

这个方向与「方向一：准入控制」中的"连接级上限"有交集，但那部分分析仅关注"拒绝新连接"，不涉及优雅关闭、僵尸检测、背压管理。

### 架构师视角

四个子问题需要分别解决：

**1. 优雅关闭**

在 `app.shutdown()` 中调用 `sseHub.Close()`，并在 Hub.Close 前：

1. Hub 设置 `closed = true` → 阻止新订阅
2. 广播一个 SSE `event: shutdown` 事件 → 客户端收到后主动断开
3. 给予客户端 `shutdown_timeout` 的窗口完成当前事件的消费
4. 然后关闭所有 channel

```go
func (a *App) shutdown() {
    if sseHub != nil {
        sseHub.BeginShutdown()    // 广播 shutdown 事件
        time.Sleep(2 * time.Second) // 等待客户端处理
        sseHub.Close()
    }
    // ...
}
```

**2. 僵尸连接检测**

- 在 SSE handler 中增加 `ReadDeadline` 或监听 `c.Request.Context()` + 额外的心跳确认
- 定期检查活跃连接是否收到客户端的 TCP ACK（应用层心跳 `event: ping` 超时）
- 如果 60 秒内未从客户端收到消息（SSE 是单向的，但浏览器通常会响应 ping），关闭连接
- 可配置：`sse.idle_timeout` 默认 120 秒

**3. 连接上限**

见方向二的建议（全局 5000、Per-UID 5）。

**4. 背压管理**

替代当前的静默丢弃策略：

- **Level 1（轻度压力）**：记录 WARN 日志 + 递增 `dropped_events_total` 指标
- **Level 2（中度压力）**：发送背压信号到客户端（通过 SSE `event: flow_control`）提示减少接收速率
- **Level 3（重度压力）**：自动断开最慢的消费者（基于最近 N 分钟内的丢弃率）

### 产品经理视角

对用户而言，这 4 个问题中的 3 个是透明的（优雅关闭、僵尸检测、连接上限），但**第 4 个是直接可感知的**：

> "我收到了 5 条通知的通知角标，但点击后只看到 3 条"

静默丢弃事件意味着用户可以感知到"我错过了什么"，但不知道错过了什么，也无法恢复。这破坏了实时推送的信任感。

**优化优先级建议**：

| 优先级 | 措施 | 影响 |
|--------|------|------|
| P0 | 静默丢弃 → 加指标日志 | 至少让运维知道有多少事件被丢弃 |
| P1 | `app.shutdown()` 中调用 `sseHub.Close()` | 防止部署时的事件丢失 |
| P1 | Per-UID 连接上限（默认 5） | 防止用户级滥用 |
| P2 | 僵尸连接检测 + 自动清理 | 控制内存泄漏 |
| P3 | 背压通知给消费者 | 高级消费者可以优雅降级 |

---

## 方向五（附加）：通知投递效果分析与运营闭环（Notification Delivery Effectiveness Analytics）

### 问题陈述

aero-id 的通知系统功能完备（outbox 驱动、SSE 实时推送、Webhook 投递、折叠去重、偏好设置）。但系统**完全不知道"通知投递后发生了什么"**——没有投递成功率、没有阅读率、没有类型维度效果分析。这是一个运营视角的黑盒。

### 代码级证据

**证据 1：通知只记录 read/unread，无投递元数据**

```go
// internal/domain/notification.go
type Notification struct {
    ReadAt         *time.Time      // ← 唯一的状态跟踪
    // 无 DeliveredAt, 无 DeliveryChannel, 无 DeliveryAttempts
    // 无 SeenAt（用户在 UI 中看到的时间而非点击时间）
    // 无 ClickedAt（用户是否点击了通知）
}
```

**证据 2：无通知级别的投递确认机制**

SSE 推送是"fire-and-forget"的——Dispatcher 调用 `pusher.Publish` 后立即返回，不确认客户端是否收到。Webhook 投递虽然重试，但只记录"成功/失败"，不记录投递延迟。

**证据 3：无通知效果指标**

```go
// internal/pkg/metrics/metrics.go
// 当前指标：HTTP RED、gRPC RED、SSO 健康、Outbox 队列
// 无：notifications_created_total（按类型）
// 无：notifications_read_total（按类型）
// 无：notifications_read_rate（阅读率）
// 无：sse_events_dropped_total
// 无：notification_delivery_latency_seconds
```

### 为什么这是盲区

112 份既有分析中，"通知偏好"（折叠、静默时段、按类型启用/禁用）被多次分析，"通知 UX"（通知中心、已读/未读）也被覆盖。但**没有任何分析将"通知投递效果分析"作为一个独立能力提出**。这不是"加一个功能"——这是为运营团队建立一个数据驱动的通知优化闭环。

### 架构师/产品经理双视角

需要增加的指标和字段：

**数据层扩展：**

```sql
-- notifications 表（新增列）
ALTER TABLE notifications ADD COLUMN delivery_channels TEXT[];       -- 投递渠道: {'sse', 'webhook'}
ALTER TABLE notifications ADD COLUMN first_delivered_at TIMESTAMPTZ; -- 首次投递时间
ALTER TABLE notifications ADD COLUMN last_delivered_at TIMESTAMPTZ;  -- 最近投递时间
ALTER TABLE notifications ADD COLUMN delivery_attempts INT DEFAULT 0;
ALTER TABLE notifications ADD COLUMN clicked_at TIMESTAMPTZ;         -- 用户点击通知的时间
```

**指标层新增：**

| 指标名 | 类型 | 标签 | 说明 |
|--------|------|------|------|
| `notifications_created_total` | Counter | type, actor | 通知创建数 |
| `notifications_read_total` | Counter | type | 通知标记已读数 |
| `notifications_read_rate` | Gauge | type | 12 小时内创建的通知的阅读率 |
| `notifications_clicked_total` | Counter | type | 用户点击通知数 |
| `sse_events_dropped_total` | Counter | uid | SSE 背压下丢弃的事件数 |
| `notification_delivery_latency` | Histogram | type | 从创建到首次投递的时长 |

**运营价值：**

- 发现"message.read_receipt"类型的阅读率远低于"message.received" → 说明已读回执通知可能被用户忽略 → 考虑优化内容或合并到消息通知中
- 高丢弃率的 SSE 连接 → 客户端实现可能需要优化 → 主动联系开发团队
- 投递延迟异常增长 → 标识 Outbox relay 可能遇到瓶颈 → 扩缩容决策依据

---

## 总结

| # | 方向 | 类型 | 代码证据 | 为什么之前被忽略 |
|---|------|------|---------|----------------|
| 1 | **操作成本感知速率限制** | 公平性/基础设施 | 所有操作消耗 1 token；无 cost 参数 | 40+ 文档讨论限流，但都聚焦于"限流粒度"，从未讨论"限流权重" |
| 2 | **内存数据结构无界增长** | 运行时稳定性 | Hub 无连接上限、Rate Limiter 无 bucket 上限、shutdown 不关闭 Hub | 12+ 文档提到 idle eviction，但将其视为"已实现功能"而非"风险管理" |
| 3 | **消息内容验证空白** | 数据完整性/安全 | 消息/评论只有空值检查，无长度上限、无安全过滤 | 文件安全性被广泛讨论，但文本内容安全被完全忽视 |
| 4 | **SSE 连接生命周期管理** | 可靠性/可观测 | 优雅关闭缺失、僵尸连接、无上限、背压缺失 | SSE 作为"已交付功能"被标记完成，但其连接管理从未被审查 |
| 5（附加） | **通知投递效果分析** | 运营/数据驱动 | 无投递元数据、无可投递指标、无阅读率追踪 | 通知"功能"被充分讨论，但通知"效果"从未被分析 |

### 建议优先级

```
P0（安全/稳定性）: 方向三（消息内容长度上限）— 半天改动，防止数据库/网络放大攻击
P0（稳定性）:    方向二（Hub 连接上限 + Rate Limiter bucket 上限）— 1 天，防 OOM
P1（可靠性）:    方向四（SSE 优雅关闭 + 僵尸检测）— 2 天，部署期间的事件完整性
P1（公平性）:    方向一（操作成本感知限流）— 2-3 天，多消费者公平性
P2（可观测）:    方向五（通知投递效果分析）— 3 天，运营数据驱动决策
```

### 边界情况联动

- **风险掩盖**：方向一的"操作权重"可能掩盖方向三（消息内容验证）的症状——如果 1KB 消息和 100MB 消息消耗相同的 token，权重限流不会惩罚超长内容
- **部署顺序**：方向四（SSE 优雅关闭）依赖方向二（Hub 上限）的 `closed` 状态——建议在 Hub 改造中一并完成
- **运维联动**：方向五的投递指标依赖方向四的 `sse_events_dropped_total`——两者共享部分数据基础设施
