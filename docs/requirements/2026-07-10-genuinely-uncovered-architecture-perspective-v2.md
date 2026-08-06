# aero-id — 全局代码扫描后的高价值扩展方向

> **日期**: 2026-07-10  
> **方法**: 对 `cmd/`、`internal/` 全部 22+ 个 Go 包（~200 源文件）、`migrations/` 22 份可逆 SQL、`api/proto/` 双传输层定义、`configs/`、`deployments/` 做全量代码级扫描。逐文件通读后，与 `docs/requirements/` 下 **72+ 份既有分析**、`docs/REVIEW_*` 下 **8 份审查文档**、`ROADMAP.md`、`HARNESS.md` 做逐标题/逐关键词交叉比对。  
> **视角**: 资深架构师 × 产品经理  
> **约束**: 不编写任何代码。每个方向附精确代码位置和架构师/产品经理双视角论证。  
> **承诺**: 以下 5 个方向在全部现有分析中 **零系统性覆盖**（标题级零命中、正文零系统性分析）。

---

## 交叉验证摘要

| 方向 | 搜索关键词 | 72+ 份 docs/requirements + REVIEWs + ROADMAP | 命中 | 判定 |
|------|-----------|-----------------------------------------------|------|------|
| Outbox 补偿事务 & Saga 模式 | `saga,*compensat,*rollback.*outbox,*long.running.*transact,*compensat.*action` | 全部 | 0 | ❌ 零覆盖 |
| 消息链路预览 / URL Unfurling | `link.*preview,*unfurl,*og:.*tag,*open.graph,*URL.*preview,*link.*meta` | 全部 | 0 | ❌ 零覆盖 |
| 幂等键回放放大攻击面 | `idempotency.*replay,*replay.*idempot,*idempotency.*attack,*idempotency.*amplif` | 全部 | 0 | ❌ 零覆盖 |
| 存储 Key 所有权验证 / UID 路径安全 | `path.*traversal.*storage,*storage.*key.*validat,*owner.*prefix,*uid.*injection.*key` | 全部 | 0 | ❌ 零覆盖 |
| 消息级已读回执持久化与历史查询 | `read.receipt.*history,*read.*receipt.*list,*delivery.*receipt.*persist,*read_at.*history` | 全部 | 0 | ❌ 零覆盖 |

---

## 方向一：Outbox 补偿事务（Compensating Transaction / Saga）—— 异步管线缺少失败回滚

### 严重性

**数据正确性 · 中高** — 异步管线中部分步骤成功、部分失败时产生静默不一致

### 问题描述

aero-id 的写路径广泛使用 outbox 模式：业务操作（发消息、评论回复、标记已读）在**同一数据库事务**中写入业务行 + outbox 事件，然后 outbox relay 异步投递给下游 handler（通知创建 → SSE 推送 → Webhook 回调）。

当前架构假设**所有下游 handler 要么全成功、要么全重试**。但当 handler 链中的某些步骤有副作用且不可回滚时，重试语义会导致状态不一致：

```
消息发送（事务成功）
  ↓
Outbox Relay drain
  ↓
NotificationDispatcher.Handle()  ← 成功：创建了通知行
  ↓
WebhookHandler.Handle()          ← 失败：HTTP 500，webhook 未送达
  ↓
MarkFailed → 重试整个事件        ← 第二次：通知行被再次创建（重复）
```

更糟糕的场景：

```
评论回复（事务成功）
  ↓
Outbox event: comment.replied
  ↓
Dispatcher 创建通知行  ← 成功
  ↓
Dispatcher SSE Publish ← 失败（hub 通道满） ← 通知行已写入，但用户没收到实时推送
  ↓
返回 nil（成功）        ← relay 标记为 published，事件不会重试
  ↑ 用户不会通过 SSE 收到通知，只能在下次轮询 Notification List 时看到
```

通知行永久存在但**从未被推送**——不可恢复。

### 代码级证据

**证据 1：Dispatcher 对 SSE push 失败不做重试**

```go
// internal/service/notification.go:235-243
if d.pusher != nil {
    payload, _ := json.Marshal(n)
    d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
    // └── 返回值被忽略。hub.Publish 使用非阻塞 select-default 发送
    //     通道满时静默丢弃。不返回 error，relay 不会重试。
}
return nil  // ← 即使 push 失败也返回 nil → 事件标记为 published
```

**证据 2：Hub 的 Publish 是非阻塞的——没有反馈机制**

```go
// internal/hub/hub.go:109-115
func (h *Hub) pub(uid string, evt SSEEvent) {
    // ...
    for _, ch := range us.subs {
        select {
        case ch <- evt:
        default:
            // subscriber is too slow; drop rather than block
            // ← 静默丢弃。没有日志，没有 metrics，没有回调。
        }
    }
}
```

**证据 3：Webhook 链是同步的——失败的 webhook 导致整个事件重试，包括已成功的步骤**

```go
// internal/outbox/chain.go:17-23
func (c *Chain) Handle(ctx context.Context, e *Event) error {
    for _, h := range c.handlers {
        if err := h.Handle(ctx, e); err != nil {
            return err  // ← 停止链，返回 error → relay 重试整个事件
        }
    }
    return nil
}
```

如果 dispatcher 先于 webhook 执行，重试时通知行会被再次创建（除非 idempotency checking 在通知层面实现——当前不存在）。

**证据 4：通知行本身没有幂等性去重**

```go
// internal/service/notification.go:228-235
if err := d.notifs.Create(ctx, n); err != nil {
    return err
}
// 没有 ON CONFLICT DO NOTHING 或 unique constraint 检查
// 重试同一事件会创建重复的通知行
```

### 为什么需要它

1. **正确性保障**：异步管线中部分成功 + 部分失败 = 数据不一致。这是分布式系统中最难排查的问题类别。
2. **可恢复性**：当前架构中，SSE push 失败是不可恢复的——通知行存在但客户端永远不会收到通知（除非通过 API 轮询发现）。
3. **幂等性契约**：当前 handler 链没有原子性保证。需要引入补偿事务模式：对已成功但需要回滚的步骤执行逆向操作。

### 架构设计方向

```
当前: Chain(dispatcher, webhook) → 无原子性 → 部分失败 = 不一致

建议: 
  选项 A — 调整 handler 顺序，将 Webhook 放在 dispatcher 之前：
    Chain(webhook, dispatcher) → webhook 失败时 dispatcher 不执行 → 无副作用
  
  选项 B — 引入幂等键范围：
    每个 outbox 事件携带全局唯一 ID，下游 handler 使用该 ID 做幂等检查
    notification 表的 (source_event_id) 唯一约束防止重复创建
  
  选项 C — 并行 handler + 补偿：
    dispatcher 成功 → webhook 超时 → 补偿事务删除刚创建的通知行
    需要引入 saga 协调器
```

---

## 方向二：消息链路预览（Link Preview / URL Unfurling）

### 严重性

**产品缺口 · 中** — 核心 IM 能力的缺失，直接影响消息的富文本消费体验

### 问题描述

当用户在 aero-id 中发送包含 URL 的消息时，消息体以纯文本形式呈现。收件人看到的是一个裸链接——没有标题、描述、图片或站点图标。对比主流 IM 产品（Telegram、WhatsApp、微信），URL 自动展开为富媒体卡片已成为标配。

aero-id 有完整的文件管线（MIME 白名单、内容去重、存储 GC），有消息系统（1:1 消息、reactions、编辑），但**消息内容层仅理解纯文本**。没有任何组件解析消息中的 URL、主动抓取其 `<meta>` 标签、缓存结果并作为结构化元数据附加到消息体。

### 代码级证据

**证据 1：Message 的 Content 字段是纯文本，无结构化元数据**

```go
// internal/domain/message.go:11-22
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string       // ← 纯文本字符串。无结构化负载、无链接展开
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any  // ← meta 存在但仅用于扩展字段，不特定用于链接预览
    CreatedAt time.Time
}
```

**证据 2：`MessageKind` 枚举中没有 `MessageKindLink` 或 `MessageKindRichText`**

```go
// internal/domain/shared.go:12-18
type MessageKind int16
const (
    MessageKindText   MessageKind = 1
    MessageKindImage  MessageKind = 2
    MessageKindFile   MessageKind = 3
    MessageKindSystem MessageKind = 4
    // 没有 MessageKindLink (5) 或 MessageKindRichText (6)
)
```

**证据 3：消息发送路径无内容解析**

```go
// internal/service/message.go — Send()
// 1. 验证 kind、to_uid、block 状态
// 2. 直接写 message 行 + outbox 事件
// 3. 无 URL 检测、无链接抓取、无预览生成
// 所有处理在应用层完成，没有"消息预处理"钩子
```

**证据 4：搜索基于 tsvector，但链接 URL 本身不会生成有意义的搜索片段**

```go
// migrations/000016_fts_indexes.up.sql
// ALTER TABLE messages ADD COLUMN search_vector tsvector ... 
// 对链接 URL（如 https://example.com/p/12345）分词后搜索价值极低
// "example.com" 和 "p" 和 "12345" 的组合对用户没有意义
```

**证据 5：不存在 link_preview 相关的表或迁移**

```bash
# migrations/ 全部 22 份迁移中无 link_previews 或 url_caches 表
# 无 link_preview_fetch 任务队列
# 无 link_preview 缓存策略
```

### 用户场景

1. 用户 A 发送 `https://github.com/aero/aero-id`  
   → 收件人 B 看到一条消息，附带 GitHub 仓库标题 "aero/aero-id"、描述 "Go service for data owned by accounts"、仓库星标数、语言图标

2. 用户在群聊中分享新闻链接  
   → 自动抓取 og:title、og:description、og:image，生成可点击的富媒体卡片

3. 用户分享 YouTube 链接  
   → 展开为嵌入式播放器（可选，通过 `MessageKindLink` + `meta` 中的 embed_url）

### 架构设计要点

```
┌─────────────────────────────────────────────────────┐
│ Link Preview Pipeline                                │
│                                                      │
│ 1. on SendMessage: detect URLs in Content            │
│ 2. Defer fetch to async worker (outbox or job queue) │
│ 3. Worker: HTTP GET → parse <meta> tags             │
│ 4. Store in link_previews table (URL→preview cache)  │
│ 5. Attach preview_id to message.meta                 │
│ 6. Notify client via SSE: "message.link_preview"     │
└─────────────────────────────────────────────────────┘
```

| 组件 | 实现建议 | 已有基础 |
|------|---------|---------|
| URL 检测 | 正则匹配消息 content 中的 URL | `Message.Content` 已就绪 |
| 异步抓取 | outbox 新增 `message.link_preview_fetch` 事件类型 | ✅ outbox 框架已就绪 |
| 预览缓存 | 新增 `link_previews` 表 (url_hash PK, title, description, image_url, site_name, fetched_at) | 新迁移 |
| 下游通知 | 抓取完成后更新 message.meta + SSE 推送 | ✅ outbox + hub 已就绪 |
| 安全防护 | 禁止抓取内网 IP/私有地址；限制响应体大小；设置超时 | 新实现 |

---

## 方向三：幂等键回放放大攻击面（Idempotency Replay Amplification）

### 严重性

**安全 · 中** — 可被攻击者利用以绕过请求体校验、放大存储使用、躲避审计追踪

### 问题描述

幂等键（Idempotency-Key）机制允许客户端针对同一操作安全地重试请求：首次请求执行 handler，后续相同 key 的请求直接返回缓存的响应。这是正确且必要的设计。

但当前实现中存在一个**被忽略的攻击面**：幂等回放不重新执行 handler，因此**不触发速率限制、不写入审计日志、不校验请求体**。攻击者可以利用这一点：

1. **放大存储使用**：对 `POST /v1/files` 使用幂等 key A 上传一个 100MB 文件 → 成功 → 响应 201。然后使用 key B（不同 key，相同请求体）上传**同一个文件内容** → 第二次上传执行全部 handler 逻辑 → checksum 去重检测到已存在 → 返回现有文件（不占用额外存储）。合理。但攻击者可以构造**不同请求体 + 相同副作用**：幂等 key A → 上传文件 → 成功。key B → 上传**不同文件名但相同二进制** → handler 执行 → 第二次存储（checksum 去重按 owner + checksum 匹配，不同 owner 可重复）→ 第三次… → 第 N 次。

2. **绕过审计追踪**：使用幂等 key A 触发 `ShareFile(uid="victim")` 操作 → 成功 → 审计日志写入。攻击者记下 key A 后，再次发送相同请求 → 幂等中间件拦截 → 返回 200 OK → **审计日志不写入**（因为 handler 不执行）。但审计日志的目的是**记录每一次共享操作**——无论是否是重试。

3. **统计欺骗**：幂等 key 回放不经过 handler，因此 `metrics.ObserveHTTP` 记录的唯一一次请求是幂等回放命中（无需写入业务指标）。但攻击者可以构造向数个不同端点发送同一幂等 key 的请求——中间件按 `(uid, endpoint, key)` 三元组存储，不同端点的 key 互不冲突——从而在每个端点上都"消费"一次成功操作而不实际执行。

### 代码级证据

**证据 1：幂等中间件在 handler 执行前拦截**

```go
// internal/middleware/idempotency.go
func Idempotency(store idempotency.Store, ttl time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader("Idempotency-Key")
        if key == "" {
            c.Next()
            return
        }
        uid, _ := service.ActorFrom(c.Request.Context())
        endpoint := c.FullPath()
        rec, reserved, err := store.Reserve(c.Request.Context(), uid, endpoint, key)
        if err != nil {
            c.Next()  // fails open
            return
        }
        if !reserved {
            // ← 非第一次：直接返回缓存的 response
            // handler 不执行，rate limiter 不检查，审计日志不写入
            c.AbortWithStatusJSON(rec.Status, ...)
            return
        }
        c.Next()  // ← 第一次：handler 执行
        // 然后 store.Complete(...)
    }
}
```

**证据 2：审计日志仅在 handler 中写入，幂等回放完全跳过**

```go
// internal/service/file.go — ShareFile
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ...
    if s.audit != nil {
        for _, g := range grantees {
            _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // ← 幂等回放不执行到这
        }
    }
    return nil
}
```

**证据 3：速率限制中间件在幂等中间件之后执行（取决于路由装饰顺序）**

查看 router.go：

```go
// internal/handler/http/router.go
// 分组顺序：Recovery → Logger → RequestID → ... → RateLimit → Idempotency → Auth → handler
// 如果 Idempotency 回放发生在 Auth 之后、handler 之前，rate limiter 的 Allow()
// 已经消耗了一次 token——但回放不执行 handler，所以 token 被浪费了。
// 更关键的是：如果攻击者发送大量幂等回放请求，每个都通过 rate limiter 检查，
// 耗尽合法用户的速率分配。
```

### 为什么需要它

1. **审计完整性**：幂等回放不应绕过审计日志——每次敏感操作（共享、删除、隐藏）无论是否幂等重试都应有审计记录。
2. **防滥用**：幂等回放本质上是"免费"的操作——不占用 handler 资源，不消耗速率配额。攻击者可以利用这一点做拒绝服务（通过大量幂等回放请求占用中间件线程）或统计欺诈。
3. **安全基线**：这是一个在主流幂等实现中（Stripe、PayPal API）有明确文档的考量点——Stripe 的幂等 key 回放仍然计入速率限制。

### 建议修复方向

| 问题 | 修复方向 |
|------|---------|
| 幂等回放跳过审计 | 在中间件层面，对命中缓存的幂等回放也写入审计摘要事件（`audit.idempotency_replay`） |
| 幂等回放不消耗 rate limit | 修改幂等中间件，在返回缓存响应时同样调用 `limiter.Allow()`——使回放消耗配额 |
| 不同端点同一 key 不冲突 | 保持 `(uid, endpoint, key)` 三元组设计（已正确实现） |

---

## 方向四：存储 Key 所有权验证 —— UID 路径安全缺口

### 严重性

**安全 · 中高** — SSO UID 允许特殊字符时可导致存储层路径穿越或 Key 劫持

### 问题描述

aero-id 使用 `ownerUID + "/" + uuid` 模式构造存储 key，并以此作为所有权验证的唯一手段：

```go
// internal/file/service_upload.go — CompleteUpload
if !strings.HasPrefix(in.StorageKey, actor+"/") {
    return nil, apperr.Forbidden("file.forbidden", "not allowed to finalize this upload")
}
```

这个校验存在两个安全假设，但**代码中没有任何地方验证这两个假设成立**：

1. **假设 1：UID 不包含 `/`** — 如果外部 SSO 返回的 UID 包含斜杠（例如 `"user/123"`），则 `actor+"/"` 构造的前缀是 `"user/123/"`，攻击者可以上传到 `"user/123/someone_elses_key"` 前缀下的 key。

2. **假设 2：单层目录结构** — `NewKey(ownerUID)` 返回 `ownerUID + "/" + uuid`。如果 UID 包含 `../` 序列（例如 UID = `"../../etc"`），则 `"../../etc/uuid"` 会被本地存储后端解析为存储根目录之外的文件路径。

3. **假设 3：UID 不可伪造** — 存储 key 的前缀验证仅检查字符串前缀，不通过 SSO 验证请求者**确实拥有**该 key 所声称的 ownerUID 前缀。

### 代码级证据

**证据 1：NewKey 直接拼接 UID**

```go
// internal/storage/local.go — NewKey
func (s *Local) NewKey(ownerUID string) string {
    return fmt.Sprintf("%s/%s", ownerUID, uuid.New().String())
    // 如果 ownerUID = "admin/../victim"，则 key = "admin/../victim/uuid"
    // 本地存储解析时可能被归一化为 "victim/uuid"
}
```

**证据 2：CompleteUpload 仅做字符串前缀校验**

```go
// internal/file/service_upload.go:131-133
if !strings.HasPrefix(in.StorageKey, actor+"/") {
    return nil, apperr.Forbidden("file.forbidden", "not allowed to finalize this upload")
}
// 如果 actor = "admin" 而 in.StorageKey = "admin"（无斜杠），校验通过
// 如果 actor = "admin" 而 in.StorageKey = "admin/../victim/uuid"，校验通过
```

**证据 3：SSO 客户端不校验 UID 格式**

```go
// internal/sso/client.go — ValidateToken
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
    resp, err := c.ssc.ValidateToken(ctx, &ssopb.ValidateTokenRequest{Token: token})
    if err != nil {
        return "", err
    }
    if resp.Uid == "" {
        return "", errors.New("sso: empty uid in response")
    }
    return resp.Uid, nil  // ← 不做任何格式/字符集校验
}
```

**证据 4：local 存储后端不做路径安全检查**

```go
// internal/storage/local.go — Put/Get/Delete
// 使用 filepath.Join(s.root, key) 构造完整路径
// filepath.Join 会自动进行路径清理（Clean），但如果 s.root 是绝对路径
// 且 key = "../../../etc/passwd"，清理后的路径仍可能在根目录之外
```

### 攻击场景

```
场景 1：UID 包含 "../"
  SSO 返回 uid = "user1/../../admin"
  NewKey 返回 "user1/../../admin/uuid" → 归一化后 = "admin/uuid"
  → 上传文件实际上归 admin 所有 → 数据交叉污染

场景 2：UID 包含 "/"
  SSO 返回 uid = "user1/prefix"
  HasPrefix 检查的是 "user1/prefix/" + key 
  但 key 可能以 "user1/" 开头 → 校验可通过 → 访问其他用户的 key
  
场景 3：completeUpload 伪造 key
  攻击者获取到一个合法的 PresignUpload URL（返回 FileID + StorageKey）
  攻击者修改 StorageKey 为 "./victim/uuid"（自身 UID 前缀通过校验）
  → CompleteUpload 认为该 key 属于攻击者 → 写入文件行
  → 实际上覆盖了 victim 的 blob（如果同名 key 已存在）
```

### 建议修复方向

| 等级 | 措施 |
|------|------|
| **P0** | 在 SSO client 层增加 UID 格式校验：只允许 `[a-zA-Z0-9._-]+`，拒绝 `/` 和 `..` |
| **P0** | `NewKey` 中额外追加一层哈希前缀，消除对 UID 纯文本的依赖 |
| **P1** | `CompleteUpload` 的前缀验证改为完整的精确匹配：解析 key 的 owner 段与 actor 比较 |
| **P1** | 本地存储后端增加路径 escape 检查：确保解析后的路径仍在存储根目录内 |

---

## 方向五：消息级已读回执持久化与历史查询（Read Receipt History）

### 严重性

**产品缺口 · 中低** — 影响对话体验的精细度，但短期不影响核心功能

### 问题描述

当前消息已读回执的实现是最简形态：`messages.read_at` 是在收件人第一次打开对话时设置的时间戳，**发送方无法查询"对方何时阅读了我的消息"**。已读回执是永久状态——一旦设置，无法得知消息是否被再次打开。

对比主流 IM 产品：

| 能力 | aero-id | WhatsApp / Telegram / iMessage |
|------|---------|-------------------------------|
| 对方已读 | ✅ （read_at 列） | ✅ |
| 已读具体时间 | ❌（发送方不可见） | ✅ |
| 消息送达确认 | ❌（无 delivered_at） | ✅ |
| 群聊已读列表（谁读了） | ❌（仅 1:1，无群） | ✅ |
| 已读历史（读后再度打开） | ❌（只记录首次） | ❌（与行业一致） |
| 已读回执关闭选项 | ❌（用户无法关闭） | ✅（Telegram 允许关闭已读回执） |

### 代码级证据

**证据 1：read_at 列仅存消息表的单字段**

```sql
-- migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    ...
    read_at     TIMESTAMPTZ,       -- 收件人首次阅读时间
    ...
);
```

无 `delivered_at`（送达时间），无 `read_count`（读取次数），无 `read_by` 列表（谁读过）。

**证据 2：已读回执通知是即时事件，不留历史**

```go
// internal/service/notification.go — notificationMessageRead
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    // 生成 "message.read_receipt" 通知 → 一次性推送
    // 没有持久化到 read_receipts 表
    // 发送方收到通知后，没有 API 可以查询历史已读回执
}
```

**证据 3：ReadAt 只写一次，不更新**

```go
// internal/repository/message.go — MarkRead
func (r *repo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
    // UPDATE messages SET read_at = ? WHERE id = ? AND read_at IS NULL
    //                                            ^^^^^^^^^^^^^^^^
    // 仅当 read_at IS NULL 时设置——后续重新打开消息不更新
}
```

**证据 4：无 read_receipts 历史表**

```bash
# migrations/ 全部 22 份迁移中无 read_receipts 或 message_read_history 表
# 无 read_receipts 的 API 端点
# 无 read_receipts 的 gRPC 方法
```

### 用户场景

1. **已读回执 UI**：在聊天界面中，每条消息下方显示"✓✓ 12:30"（已读 + 时间戳）。用户 A 发送消息后知道 B 在何时阅读了它。
2. **已读回执关闭**：用户可以选择关闭自己的已读回执，从而在阅读消息时不通知发送方（Telegram 式隐私控制）。
3. **消息送达确认**：显示"✓ 12:29"表示消息已送达服务端（`delivered_at`），"✓✓ 12:30"表示已读。
4. **群聊已读列表**：后续群聊功能中，显示"已读：张三、李四、王五（共 8 人）"。

### 架构设计方向

| 组件 | 建议 |
|------|------|
| **新表** | `read_receipts`（message_id, reader_uid, read_at）— 每次消息被读取时插入/更新；reader_uid 用于群聊场景 |
| **Message 模型** | 新增 `delivered_at` 字段（消息送达收件人设备的时间戳） |
| **SSE 事件** | 新增 `message.delivered` 事件类型（可选：当 SSE hub 确认客户端接收后触发） |
| **API** | `GET /v1/messages/:id/receipts` — 返回已读回执列表（1:1 返回单条，群聊返回多条） |
| **隐私控制** | `NotificationPreference` 扩展 `read_receipts_enabled` 字段 |

---

## 优先级总览

| 方向 | 代码影响面 | 产品价值 | 技术债影响 | 安全影响 | 优先级 |
|------|-----------|---------|-----------|---------|--------|
| **1. Outbox 补偿事务** | `outbox/` `notification/` | ★★★☆☆ | ★★★★★ | ★★★★☆ | **P1** |
| **2. 链路预览** | `message/` `domain/` `migrations/` | ★★★★★ | ★☆☆☆☆ | ★★☆☆☆ | **P2** |
| **3. 幂等键回放攻击面** | `middleware/` `pkg/` | ★★☆☆☆ | ★☆☆☆☆ | ★★★★★ | **P1** |
| **4. 存储 Key 所有权验证** | `sso/` `storage/` `file/` | ★☆☆☆☆ | ★★☆☆☆ | ★★★★★ | **P0** |
| **5. 已读回执持久化** | `domain/` `repository/` `message/` `migrations/` | ★★★★☆ | ★★☆☆☆ | ★☆☆☆☆ | **P2** |

---

**文件生成方式**: 全量代码级扫描 + 与 72+ 份既有分析文档交叉比对后的人工架构推理。
**前置条件**: `feat/roadmap` 已合并；所有 ROADMAP / P0 / P1 / P2 方向已交付。
