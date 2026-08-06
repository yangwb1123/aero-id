# aero-id — 基于深度代码扫描的五个高价值扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 对 `internal/` 全部 ~42 个 Go 包、`migrations/` 22 份迁移、`api/proto/`、`cmd/`、`configs/` 进行全局代码扫描，逐文件确认代码级证据  
> **约束**: 不编写任何代码，仅分析  
> **前提**: 已有 50+ 份分析文档覆盖了大量方向（可观测性、SSO 韧性、文件流水线、写入正确性、通知系统、搜索、管理后台、速率限制、存储 GC、内容生命周期等），本文聚焦于**代码中存在的、未被系统关注的五个真正的架构/产品断层**

---

## 核心发现

aero-id 的完成度极高——可观测性、SSO 韧性、事务性 outbox、通知系统、SSE 实时推送、全文搜索、速率限制、存储 GC、内容审核、身份管理、文件分享——几乎所有 ROADMAP 方向都已交付。但代码扫描揭露了 **5 个架构意图已设计但未完整实现、或存在路径上的正确性漏洞的缺口**。它们不是新功能请求，而是已有代码的自然延伸被截断或存在竞态条件。

| # | 扩展方向 | 代码证据 | 影响等级 | 估算 |
|---|---------|---------|---------|------|
| 1 | **用户封禁生命周期** — 权限基础设施已存在，但无存储、无端点、无任何 service 层检查 | `domain/admin.go:9` + 仓库无 ban 表 + handler 清单无 ban 端点 | 🔴 安全漏洞 | 低-中 |
| 2 | **内容去重竞态** — 并发上传相同时，FindByChecksum 与 Create 之间的窗口可导致重复 blob | `service/file.go:109-120` + `repository/file.go:20-22` | 🟡 正确性 | 低 |
| 3 | **Webhook 投递级联故障** — WebhookHandler 在 outbox relay 链中执行，一个慢端点阻塞所有事件类型 | `outbox/webhook.go` + `app/app.go` 的链式构造 | 🟡 韧性 | 中 |
| 4 | **消息撤回/撤销** — 消息一旦发送，发送方无法从收件箱中撤回 | 全仓 grep 无 recall/retract/unsend | 🟠 产品缺口 | 中 |
| 5 | **Ref-count 原子性缺口** — 文件创建完成后才递增 ref count，并发删除与 GC 可误删 blob | `service/file.go:148-150` 的 "best-effort" 注释 | 🟡 正确性 | 低-中 |

---

## 扩展方向一：用户封禁生命周期（User Ban Lifecycle）

### 代码证据

`internal/domain/admin.go` 第 9 行定义了 `PermUserBan` 常量：

```go
PermUserBan = "user.ban"   // ban/unban users
```

同文件定义了完整的权力模型（`Role`、`UserRole`、`PermContentModerate`、`PermAuditRead`、`PermSystemConfig`）。迁移 `000022_admin_roles` 创建了 `roles` 和 `user_roles` 表。`internal/repository/admin.go` 实现了 `GrantRole`、`RevokeRole`、`HasPermission`。`internal/middleware/permission.go` 实现了 `RequirePermission` 中间件。

但 **整个代码库没有一个地方真正使用 `PermUserBan`**：

- 没有 `banned_users` 表或迁移
- 没有 `BanUser` / `UnbanUser` 端点（HTTP 或 gRPC）
- `internal/service/message.go` 在发送消息时不检查接收方是否被封禁
- `internal/service/comment.go` 在创建评论时不检查评论人是否被封禁
- `internal/handler/http/admin.go` 只暴露了一个端点 `GET /admin/audit-log`
- `internal/handler/grpc/ops.go` 没有管理端点的 gRPC 对等体

### 为什么需要

这是 **一个设计意图已明确但实现完全缺失的完整功能**。从产品视角：

1. **安全合规**：没有封禁能力意味着无法阻止恶意用户继续使用服务。SSO 可能提供"禁用账号"能力，但即时、操作员可控的封禁应属于 aero-id 自己的范围（特别是内容审核场景）。
2. **幂等**：当前已有完整的角色/权限基础设施（roles 表、user_roles 表、`HasPermission`、`RequirePermission` 中间件），`PermUserBan` 是唯一定义但永不使用的权限常量。
3. **审核闭环**：当前 comment report 可以 resolve/dismiss，但没有对应的用户级制裁措施。审核流程到"报告被处理"就结束了，而运营人员需要的能力是"处理报告 + 封禁违规用户"。

### 实现建议（概述）

```sql
-- 新增迁移：banned_users 表
CREATE TABLE banned_users (
    uid          TEXT PRIMARY KEY,
    reason       TEXT NOT NULL DEFAULT '',
    banned_by    TEXT NOT NULL,
    banned_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ,   -- NULL = 永封, 否则到期自动解封
    UNIQUE (uid)
);
```

- 服务层：`BanUser(ctx, uid, reason, duration)` / `UnbanUser(ctx, uid)` / `IsBanned(ctx, uid) (bool, error)`
- 消息发送、评论创建、评论举报、文件上传前检查 `IsBanned` → `Forbidden`
- 自动解封：后台 goroutine 定期扫描 `expires_at < NOW()` 并删除行（或 outbox 事件驱动）
- 审计日志：每次 ban/unban 写入 `audit_events`

---

## 扩展方向二：内容去重竞态条件（Content-Dedup Race Under Concurrent Uploads）

### 代码证据

`internal/service/file.go` 第 109-120 行，用户上传流程中的去重逻辑：

```go
// Deduplicate: if this owner already has an identical file, reuse its blob and drop
// the copy we just wrote.
reused := false
if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
    if existing.StorageKey != key {
        _ = s.store.Delete(ctx, key)   // <--- 删除刚上传的 blob
        key = existing.StorageKey
    }
    reused = true
} else if !stderrors.Is(ferr, repository.ErrNotFound) {
    _ = s.store.Delete(ctx, key)
    return nil, ...
}
```

且第 148-150 行的 ref count 递增明确标记为 best-effort：

```go
// Increment the storage reference count so concurrent deletes and the GC
// know this blob is still live. Best-effort: a missed increment is no
// worse than the legacy CountByStorageKey heuristic.
if s.refs != nil {
    _, _ = s.refs.Increment(ctx, f.StorageKey)
}
```

**竞态场景**：用户 A 和用户 B 同时上传相同文件内容。

```
时间线:
t0: A 上传 → 存入存储 → 计算 checksum = X
t0: B 上传 → 存入存储 → 计算 checksum = X
t1: A 调用 FindByChecksum → 未找到 (B 还没创建行)
t1: B 调用 FindByChecksum → 未找到 (A 还没创建行)
t2: A 调用 Create → 创建行 (blob_key_A)
t2: B 调用 Create → 创建行 (blob_key_B)
t3: A 调用 refs.Increment(blob_key_A) → ref count = 1
t3: B 调用 refs.Increment(blob_key_B) → ref count = 1
```

最终：两个相同的 blob 都被保留，去重失败，存储浪费。

### 为什么需要

1. **存储成本**：内容去重的核心目的是节省存储。如果竞态条件下每次并发上传都会产生重复 blob，去重的价值大打折扣。
2. **引用计数不准确**：当 ref count 在 Create *之后*才 Increment，且是 "best-effort"，一旦 Increment 失败或跳过，GC sweeper 可能认为该 blob 无人引用并误删它（尽管行已创建）。
3. **频率**：在大规模上传场景（文件分享服务、批量导入）中，并发相同内容不是罕见情况。

### 实现方向

- 使用 PostgreSQL 的 `pg_try_advisory_xact_lock(hashed_checksum)` 或 `SELECT ... FOR UPDATE` 得 checksum 级别的悲观锁
- 或者：将 checksum → storage_key 映射移到独立表（`content_hashes`），在 `ON CONFLICT DO NOTHING` 中原子完成，使得并发插入者中只有一个成功
- 或者：ref count Increment 在 Create 的**同一个事务**中完成（当前 Increment 是 Create 后的单独调用）

---

## 扩展方向三：Webhook 投递级联故障风险（Webhook Delivery Cascading Failure）

### 代码证据

`internal/app/app.go` 中 outbox relay handler 的链式构造：

```go
var relayHandler outbox.Handler = dispatcher
if cfg.Outbox.Webhook.URL != "" {
    // ...
    webhookHandler := outbox.NewWebhookHandler(whCfg, log)
    relayHandler = outbox.NewChain(dispatcher, webhookHandler)  // dispatcher → webhook 串行链
}
relay := outbox.NewRelay(outboxRepo, relayHandler, cfg.Outbox.MaxRetries, log, metricsReg)
```

`internal/outbox/chain.go` 显示链是严格串行的：

```go
func (c *Chain) Handle(ctx context.Context, e *Event) error {
    for _, h := range c.handlers {
        if err := h.Handle(ctx, e); err != nil {
            return err // 第一个 handler 出错就停止
        }
    }
    return nil
}
```

`internal/outbox/webhook.go` 中 WebhookHandler：

```go
func (h *WebhookHandler) Handle(ctx context.Context, e *Event) error {
    if !h.matches(e.EventType) {
        return nil
    }
    // ... HTTP POST 到外部端点，超时 = h.cfg.Timeout ...
    resp, err := h.client.Do(req)
    if err != nil {
        return fmt.Errorf("webhook post: %w", err) // 错误导致事件标记失败并重试
    }
    // ...
}
```

**级联故障场景**：假设配置了一个外部 webhook 端点（如 Slack、企业微信、自定义回调）。该端点出现延迟或故障：

1. Webhook POST 超时 → handler 返回 error
2. outbox relay 将此事件标记为失败（重试计数 + 1）
3. **同时**，此 outbox 事件对应的本地通知（dispatcher 已经成功处理）被**回滚**——因为 Chain 返回 error 后 relay 不会调用 MarkPublished
4. 结果：外部 webhook 慢，导致本地通知行重复插入（下一次 relay tick 重新执行整个 Chain），或导致某些事件类型被延迟

此外，relay 的单线程 `drain` 内逐个处理事件：如果当前 batch 中第 5 个事件的 webhook 慢，第 6-100 个事件全部等待。

### 为什么需要

1. **隔离性**：Webhook 是"增值投递"。本地通知（dispatcher）是"核心投递"。一个应该不影响另一个。
2. **SLA 保护**：外部 webhook 不在我们控制范围内。没有单端点慢影响全局投递的保护机制。
3. **背压管理**：当前 relay 没有队列深度监控或动态速率调整。大量事件入列时，webhook 延迟只会加剧积压。

### 实现方向

- **解耦 webhook 到独立 relay**：配置一个单独的 outbox relay（或 worker pool），专用于 webhook 投递。核心 relay 只负责 dispatcher → 本地通知。
- **Per-webhook 熔断**：为每个 webhook 端点添加电路断路器（类似 SSO breaker）。连续 N 次失败后跳过该端点（不再重试）直到冷却期结束。
- **异步非阻塞**：Chain 改为扇出（fan-out），核心 handler 错误仍阻止 MarkPublished，但副 handler（webhook）错误只记日志不阻止。
- **指标告警**：`outbox_webhook_latency_ms`、`outbox_webhook_error_rate`、`outbox_webhook_dead_letter` 等指标。

---

## 扩展方向四：消息撤回（Message Recall / Retract）

### 代码证据

全仓 grep 无 `recall`、`retract`、`unsend`、`withdraw` 相关代码。

当前消息的生命周期（`internal/domain/message.go` + `internal/service/message.go`）：

| 操作 | 效果 |
|------|------|
| `Send` | 创建消息行，对 sender 和 recipient 均可见 |
| `Delete` | 软删除（`deleted_at`），仅 sender 视角移除，recipient 仍能看到 |
| `Edit` | 改写消息内容 + `edited_at`，双方可见新内容 |
| `MarkRead` | 设置 `read_at`，不改变可见性 |

**产品缺口**：没有一种操作能让 sender 从 recipient 的 inbox 中移除消息。一旦发送，无法撤回。

### 为什么需要

这是任何严肃消息系统的基础功能：

1. **误发送**：发错人、发错内容、包含敏感信息
2. **消息撤回是行业标准**：微信、Telegram、Slack、Teams、WhatsApp 都支持消息撤回（有窗口期限制）
3. **合规要求**：某些场景（金融、医疗）需要能够撤销错误发送的受保护信息
4. **用户体验**：没有撤回能力的即时通讯系统在用户信任上存在缺陷

### 实现建议（概述）

- 表结构：在 `messages` 上增加 `recalled_at TIMESTAMPTZ` 列（迁移 000023）
- 服务层接口：`RecallMessage(ctx, id domain.ID) error`
  - 只有原始发送者可撤回
  - 可配置撤回窗口期（如消息发送后 5 分钟内）
  - 撤回后：recipient 看到 "消息已被撤回" 占位符而非原始内容
  - outbox 事件 `message.recalled` → 推送通知给 recipient
- 审计日志：记录撤回操作
- 幂等：已撤回的消息再次撤回是 no-op

---

## 扩展方向五：Ref-Count 原子性缺口（Storage Ref-Count Atomicity Gap）

### 代码证据

`internal/service/file.go` 第 148-150 行：

```go
if s.refs != nil {
    _, _ = s.refs.Increment(ctx, f.StorageKey)
}
```

这不是在 Create 的事务中执行的，而是 `s.files.Create(ctx, f)` 成功后的单独调用。

`internal/repository/storage_ref.go` 的 Increment 实现（如存在）：

```go
// Increment increases the reference count for a storage key.
// Returns the new count after incrementing.
```

同一文件 `Delete` 路径（`service/file.go`）：

```go
if s.refs != nil {
    n, decErr := s.refs.Decrement(ctx, f.StorageKey)
    if decErr == nil && n == 0 {
        shouldDelete = true
    }
}
```

**时序竞态**：

```
t0: Goroutine A: Create 事务已提交（行存在，ref count = 0）
t0: Goroutine B: Delete 事务已提交（行软删，调用 Decrement → ref count = 0 → 物理删除 blob）
t1: Goroutine A: refs.Increment → ref count = 1
--- 此时 blob 已不存在，行引用了一个不存在的 blob ---
```

或者：

```
t0: Goroutine A (上传文件1): Create → refs.Increment(key_X) → ref_count[key_X] = 1
t0: Goroutine B (上传相同文件2): FindByChecksum → 找到文件1 → 准备重用 key_X
t1: Goroutine A: Delete → refs.Decrement(key_X) → ref_count[key_X] = 0 → 物理删除 blob
t2: Goroutine B: 继续使用 key_X → blob 已被删除
```

### 为什么需要

1. **数据完整性**：当前 "best-effort" 的注释表明这是一个已知但未修复的妥协。在生产环境中，并发创建+删除操作的组合必然触发此竞态。
2. **存储 GC 交互**：sweeper 使用 `RefCounter.GetCount` 来判断 blob 是否可回收。如果 Increment 漏执行、或执行在 Create 事务外，sweeper 可能误删活跃 blob。
3. **去重价值被削弱**：内容去重依赖 ref count 来确保 blob 不被误删。如果 ref count 不精确，去重带来的效率提升伴随着数据丢失风险。

### 实现方向

1. **将 ref count Increment 移到 Create 的同一事务内**：要求 FileStorage.Put 返回 storage_key，然后在该事务中 `INSERT INTO storage_refs (storage_key, count) VALUES (key, 1) ON CONFLICT (storage_key) DO UPDATE SET count = storage_refs.count + 1`。这保证行存在时 ref count ≥ 1。

2. **使用 SELECT FOR UPDATE 保护 checksum 查找**：在 `FindByChecksum` 之前对 `(owner_uid, checksum)` 或 `storage_key` 加行级锁：
   ```sql
   SELECT id FROM files WHERE checksum = ? AND owner_uid = ? FOR UPDATE
   ```

3. **或者使用 advisory lock**：
   ```go
   // 在事务开头获取 advisory lock
   db.Exec("SELECT pg_advisory_xact_lock(hashed_checksum)")
   ```

---

## 总结

| 方向 | 类型 | 当前代码证据 | 影响 | 建议优先级 |
|------|------|------------|------|-----------|
| 1. 用户封禁生命周期 | 功能缺口 + 安全 | 权限常量存在但无存储/端点/检查 | 运营安全 | **P1**（安全合规） |
| 2. 内容去重竞态 | 正确性 | FindByChecksum ↔ Create 窗口 | 数据一致性 | **P2**（侵入小，先验证） |
| 3. Webhook级联故障 | 韧性 | 链式串行执行 + 无隔离 | 可运维性 | **P1**（生产部署前修复） |
| 4. 消息撤回 | 产品缺口 | 无任何撤回能力 | 产品竞争力 | **P2**（消息基础功能） |
| 5. Ref-Count 原子性 | 正确性 | Increment 在事务外 + best-effort | 数据一致性 | **P1**（并发删除安全） |

**建议执行顺序**：

1. **方向 1**（封禁）+ **方向 3**（webhook 解耦）+ **方向 5**（ref-count 原子化）—— 生产安全与可靠性的基础，建议在进入生产前完成
2. **方向 2**（去重竞态）—— 与方向 5 关联度大，可一同修复
3. **方向 4**（消息撤回）—— 纯产品功能，取决于产品路线图优先级
