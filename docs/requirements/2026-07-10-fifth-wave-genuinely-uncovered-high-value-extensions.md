# aero-id — 第五轮全量代码扫描：五个未被既有分析覆盖的高价值扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码扫描（`internal/` 全部 22+ 包、38+ gRPC RPC、22 份迁移、双传输层 handler），与 `docs/requirements/` 下 **55+ 份既有分析文档** 做逐方向交叉比对，定位以下 5 个在既有分析中**零系统性覆盖**的高价值方向。  
> **约束**: 不编写任何代码。每个方向附代码级证据。

---

## 现状速览

aero-id 已完成的功能清单（于 `main` + `feat/roadmap` 上交付）：

- 22 份可逆迁移，覆盖文件/消息/评论/通知/outbox/审计/角色/存储引用/全文搜索等
- 双传输对等：38+ gRPC RPC 与 HTTP 路由完全一致
- 事务性 outbox + 幂等键 + 存储 GC 回收 + FTS 全文搜索
- SSO 韧性：断路器、stale-cache、singleflight、重试退避、TLS/mTLS、SPKI 证书固定
- 可观测性：RED 指标、OTel 链路追踪、SSO 健康仪表
- 产品面：消息 reactions、文件分享、评论审核、通知（推送 + 偏好 + 折叠）、用户阻止
- SSE 实时推送、角色权限系统（定义 + DB + 部分 API）
- 限流（全局 per-actor/IP token-bucket，HTTP + gRPC）

> 以下 5 个方向均**未在任何既有分析文档中作为主要主题出现**，均基于当前代码中的具体缺陷。

---

## 方向一：Idempotency Key 存储耗尽攻击面（Storage Exhaustion Attack Vector via Idempotency Keys）

**优先级：P1（安全/运维）**

### 问题陈述

`idempotency_keys` 表用于保证 POST 请求的幂等性：相同 `(uid, endpoint, idem_key)` 的首次请求创建一行记录，后续请求直接返回已缓存响应。该表有以下特征：

| 特征 | 值 | 来源 |
|------|-----|------|
| 保留行 TTL（status=0，reservation 状态） | **2 分钟** | `internal/idempotency/store.go:22` — `reservationTTL = 2 * time.Minute` |
| 已完成行 TTL（status>0，含响应体） | **24 小时** | `configs/config.yaml` — `idempotency_ttl: 24h` |
| 后台清理间隔 | **1 小时** | `internal/app/app.go:290` — `idemStore.RunCleaner(time.Hour)` |
| 每用户键数上限 | **无** | `internal/idempotency/store.go` — `Reserve()` 方法无用户级计数检查 |
| 索引 | `(uid, endpoint, idem_key)` 唯一约束 | `migrations/000004_create_idempotency_keys.up.sql` |

### 攻击场景

```
攻击者控制一个合法用户 token (uid = "attacker")
  ↓
循环 POST /v1/messages 带随机 Idempotency-Key（高基数的 UUID 或 64 字符随机串）
  ↓  
每次 POST 请求在 idempotency_keys 表中插入一行：
  - 首次: 一行 (uid="attacker", endpoint="/v1/messages", idem_key="random1", status=0, expires_at=now+2min)
  - 响应 202 → 攻击者不完成 Complete，而是发起更多 reservation
  - 每 2 分钟可创建约 ~1000 行（取决于 RPS 限流：默认 20 RPS × 120s = 2400 行/窗口）
  ↓
即使有 RPS 限制（20 req/s），攻击者每分钟仍可插入 1200 行
  ↓
24 小时后，即使后台清理每小时运行一次，已完成的响应行（含完整 JSON 响应体）
积累量可达到：1200 × 60 × 24 ≈ **170 万行/用户**
```

### 代码级证据

| 位置 | 行号 | 证据 |
|------|------|------|
| `internal/idempotency/store.go` | 37-52 | `Reserve()` 使用 `OnConflict{DoNothing}` 插入行——不检查用户已有键数，无计数器 |
| `internal/idempotency/store.go` | 20-21 | `reservationTTL = 2 * time.Minute` — 未完成行仅 2 分钟过期 |
| `internal/app/app.go` | 290 | `idemStore.RunCleaner(time.Hour)` — 清理间隔 1 小时 |
| `internal/idempotency/store.go` | 145-155 | `purgeExpired()` 使用 `WHERE expires_at < now()`— 不区分用户 |
| `internal/middleware/idempotency.go` | - | 中间件对每个带 `Idempotency-Key` 的 POST 调用 `Store.Reserve()`— 无节流 |

### 为什么这是高价值

1. **存储 DoS 向量**: 170 万行/用户 × 1000 恶意用户（使用不同 token）→ 17 亿行。PostgreSQL 表膨胀导致查询退化、VACUUM 压力、WAL 日志暴涨。
2. **成本放大**: 每行包含 `response` 字段（字节数组，可能很大，如大 JSON 响应或文件元数据），攻击者可用极低带宽放大磁盘/内存消耗。
3. **幂等性被破坏**: 当表膨胀到索引失效时，`Reserve`/`Complete` 的 `ON CONFLICT` 查询性能退化，合法用户遭遇 500。
4. **清理效率低下**: `purgeExpired` 每小时全表扫描一次 DELETE 过期行——表越大，DELETE 越慢，同时 WAL 写入越多。

### 建议方案（不写代码，仅设计）

1. **每用户键数上限**：在 `Reserve` 入口增加 `SELECT COUNT(*) WHERE uid=? AND expires_at > now()` 检查，超过阈值（如 1000）返回 429/TooManyRequests。
2. **reservation-only 键的快速回收**：status=0 且 expires_at < now() 的行在每次 Reserve 时同步清理（而非依赖后台定期任务），减少短 TTL 行的堆积窗口。
3. **分区或时间分片**：按月或按用户 ID 哈希对 `idempotency_keys` 分区，使 `DELETE` 不阻塞全表。
4. **无响应体的 reservation**：对于 status=0 的行，`response` 列设为 NULL 而非空字节数组——减少磁盘占用。

**估算**: 低（~50 行代码，不改架构）

---

## 方向二：通知折叠竞态条件导致"幽灵通知"（Notification Collapsing Race -> Ghost Notifications）

**优先级：P1（数据正确性/UX）**

### 问题陈述

通知折叠（Notification Collapsing）是 aero-id 的一个优化：当同一 `(recipient, type, actor)` 的多个通知在 5 分钟窗口内到达时，dispatcher 将合并到已有行而非创建新行。但当前的实现存在一个**先插入后查找**的竞态条件。

### 时序问题

```
时间 T1: 通知 A 到达（发送者 = userX，类型 = message.received，接收者 = userY）
         通知 A dispatcher 执行:
         1. Create(notification A)          ← 先创建新行 A
         2. FindRecentByTypeAndActor(...)     ← 找可折叠的现有行 → 未找到
         3. → 结束，A 保持为独立行

时间 T1+1ms: 通知 B 到达（同一发送者、类型、接收者）
            通知 B dispatcher 执行:
            1. Create(notification B)          ← 先创建新行 B
            2. FindRecentByTypeAndActor(...)     ← 查找 → 找到行 A（未读，刚创建）
            3. IncrementCollapsedCount(A)       ← 折叠到 A
            4. → B 行成为"幽灵"通知：存在但从未被引用，用户只看得到 A

结果：
  - notification B 在 DB 中作为独立行存在（未读，read_at IS NULL）
  - CountUnread() 将其计入未读数
  - 用户看到未读数比实际多 1，但点开通知列表时看不到该行对应的展示
    （因为折叠后该行未被标记已读，只能通过 MarkAllRead 整体清除）
```

更严重的是并发场景：

```
时间 T1: 通知 A dispatcher — Create(A) ✓
时间 T1+1μs: 通知 B dispatcher — Create(B) ✓
时间 T1+2μs: A dispatcher — FindRecent(...) → 未找到（B 刚创建但未读且在同一窗口内？取决于 created_at 比较）
时间 T1+3μs: B dispatcher — FindRecent(...) → 未找到（A 也在同一窗口内，但它的 created_at 与 B 相同）
            → **A 和 B 各自独立，都无法折叠到对方**
            → **两个"幽灵"通知**
```

### 代码级证据

| 位置 | 行号 | 证据 |
|------|------|------|
| `internal/service/notification.go` | 183-191 | `Handle()` 先调用 `d.notifs.Create(ctx, n)`，随后检查 `collapseWin > 0` 然后 `FindRecentByTypeAndActor` |
| `internal/repository/notification.go` | 102-117 | `FindRecentByTypeAndActor` 查询 `WHERE read_at IS NULL AND created_at >= ?` — 查找窗口内未读行 |
| `internal/repository/notification.go` | 120-134 | `IncrementCollapsedCount` 仅递增 `collapsed_count` — 不会标记新创建行为已读或删除 |
| `internal/comment/service.go` | - | 评论回复触发 outbox 事件 → dispatcher 以同样 `(recipient, type, actor)` 批量处理 |
| 并发来源 | `internal/outbox/relay.go` | `drain()` 在单个 tick 中处理 **一整批** 事件（`FetchUnpublished` 最多 100 条），全部顺序通过同一个 dispatcher |

### 为什么这是高价值

1. **数据完整性问题**：用户看到的未读数与实际可读通知数不一致，破坏信任。
2. **不可恢复**：幽灵通知只能通过 `MarkAllRead` 批量清除，用户无法逐条标记已读（因为它们不在通知列表中）。
3. **影响面广**：任何频繁发送同类型事件的场景都会触发（消息轰炸、评论回复风暴）。
4. **难以调试**：运维无法区分哪些通知是"真的"、哪些是折叠残留。

### 建议方案

1. **先查找后插入**：交换 `Create` 和 `FindRecentByTypeAndActor` 的顺序。先查找窗口可折叠行，如存在则仅递增 `collapsed_count`+重置 `created_at`；不存在时才创建新行。
2. **使用 UPSERT 语义**：在 `FindRecentByTypeAndActor` 和 `IncrementCollapsedCount` 上加写锁或 `SELECT FOR UPDATE`，保证并发安全。
3. **清理 ghost 通知**：后台定期清理 `read_at IS NULL` 且 `collapsed_count > 1` 且没有对应主题源（母行已被删除）的孤儿行。
4. **原子折叠**：将折叠逻辑移到 DB 层（PL/pgSQL 函数或 GORM 钩子），减少应用层竞态窗口。

**估算**: 低-中（~80 行代码 + 一条迁移用于清理存量幽灵通知）

---

## 方向三：Outbox Dead Letter 队列运维盲区（Dead Letter Queue Operational Blind Spot）

**优先级：P1（运维/可靠性）**

### 问题陈述

outbox relay 的核心职责是可靠地将领域事件分发为通知。当事件处理失败达到 `maxRetries`（默认 20）后，事件被标记为 `dead_letter=true` 并从 relay 的 `FetchUnpublished` 查询中排除。**但没有任何 API、仪表盘或告警让运维人员看到、检查、重新入队或清理死信事件。**

这是事务性 outbox 架构中最重要的运维缺口之一——事件的可靠传递是整个通知系统的基石。

### 代码级证据

| 位置 | 行号 | 证据 |
|------|------|------|
| `internal/outbox/relay.go` | 78-93 | `drain()` 调用 `MarkFailed` 时传递 `maxRetries`；超出阈值后在 repository 层设 `dead_letter=true` |
| `internal/outbox/outbox.go` | 14-25 | `Event` 结构体包含 `DeadLetter bool` 字段 |
| `internal/outbox/repository.go` | - | `FetchUnpublished` 的 SQL 包含 `WHERE dead_letter = false` |
| `internal/outbox/repository.go` | - | `MarkFailed` 方法在 `retryCount >= maxRetries` 时设置 `dead_letter = true` |
| `internal/handler/http/admin.go` | 20-50 | `AdminHandler` 只暴露了 `GET /v1/admin/audit-log` — 无 dead letter 端点 |
| `internal/handler/grpc/ops.go` | - | gRPC Ops 服务仅包含 Health 和 Reflection — 无 dead letter 操作 |
| `internal/app/app.go` | 113-118 | `AdminHandler` 构造时只传入 `adminRepo`，没有 outbox repo 或 dead letter 查询能力 |

### 影响分析

| 场景 | 当前状态 | 后果 |
|------|---------|------|
| 事件处理因临时错误（SSO 超时、DB 锁）失败 21 次 | 事件静默死信，无告警 | 发送方认为消息已发送，接收方从未收到通知 |
| 通知 dispatcher 因 payload 畸形持续失败 | 事件死信，不影响其他事件 | 运维完全不知道哪个事件、哪个用户、哪个类型的问题 |
| 应用更新后注释事件格式变更 | 旧格式事件全部死信 | 积压的死信行占用磁盘，无任何可见性 |
| 希望重试某个死信事件（如 SSO 已恢复） | 不可能：无 API 修改 `dead_letter` 标记 | 只能直接操作数据库——有风险、不可审计 |
| 希望对死信事件做根因分析 | 查看事件 payload、retry_count、last_retry_at 需要直接查 DB | 不可自服务，对非 DBA 不可用 |

### 建议方案

1. **Dead Letter 管理 API（HTTP + gRPC）**：
   - `GET /v1/admin/outbox/dead-letters` — 分页查询死信事件列表（含事件类型、payload、重试次数、最后错误时间）
   - `POST /v1/admin/outbox/dead-letters/:id/re-queue` — 将单条死信重置为待消费状态（`dead_letter=false`, `retry_count=0`）
   - `POST /v1/admin/outbox/dead-letters/re-queue-all` — 批量重新入队指定事件类型的所有死信
   - `DELETE /v1/admin/outbox/dead-letters/:id` — 手动清理单条死信
   - `POST /v1/admin/outbox/dead-letters/purge` — 清理全部死信（需确认）

2. **指标告警**：在 `/metrics` 中暴露 `outbox_dead_letter_total{event_type}` 计数器，通过 Prometheus 告警规则在死信数量 > 0 时告警。

3. **死信原因快照**：在 `MarkFailed` 时记录最后一次错误信息（`last_error TEXT` 列），帮助运维在不查日志的情况下了解失败原因。

**估算**: 中（~150 行 handler + ~50 行 repository + 1 条迁移）

---

## 方向四：Outbox 跨事件类型因果顺序违反（Cross-Type Event Causal Ordering Violation）

**优先级：P2（UX/数据一致性）**

### 问题陈述

outbox relay 按 `created_at ASC` 顺序处理事件。但当同一聚合（如同一消息、同一对话）产生**不同类型的事件**时，dispatcher 创建的 notification 行之间**没有任何顺序保证**。用户可能看到因果顺序颠倒的通知。

### 具体场景

```
时间线:
  T1: 用户 A 发送消息给用户 B → 产生 "message.sent" 事件
  T2: 用户 B 读取消息 → 产生 "message.read" 事件（通过 MarkReadWithEvent）
  
outbox 表:
  row 1: event_type="message.sent",  created_at=T1
  row 2: event_type="message.read",  created_at=T2 (T2 > T1)

Relay 处理（单线程，created_at ASC）:
  step 1: Handle("message.sent")   → 创建通知 "message.received" (recipient=B)
  step 2: Handle("message.read")   → 创建通知 "message.read_receipt" (recipient=A)

→ 通知创建顺序: 先 received, 后 read_receipt  ✅ （符合因果）
```

**问题出在并行批处理与通知创建的时间差异：**

```
Relay 在一批中取到 10 个事件（其中 message.sent 和 message.read 在同一批中）
  ↓
message.sent 处理 → Create(notification) → DB 写入
message.read 处理 → Create(notification) → DB 写入
  ↓
两个通知几乎同时写入 DB
  ↓
用户查询通知列表时：默认按 created_at DESC 排序
  ↓
如果 message.read_receipt 的 created_at 略晚于 message.received（通常如此），排序正确 ✅
  ↓
但如果两个通知在同一毫秒内创建（高负载下，UUIDv7 的时间精度足够让多个通知有相同微秒级时间戳）:
  ↓
通知列表显示:
  1. message.read_receipt  (created_at = T1+0ms)
  2. message.received      (created_at = T1+0ms)
  ↓
用户看到"用户 B 已读"通知早于"用户 B 收到了"通知
```

更严重的场景跨通知类型：

```
场景：用户评论被回复
  T1: 用户 B 回复用户 A 的评论 → event "comment.replied"
  T1+1ms: 用户 B 发送消息给用户 A → event "message.sent"
  
  dispatcher 生成:
  - 通知 A: "comment.reply"     (recipient=A, actor=B)
  - 通知 B: "message.received"  (recipient=A, actor=B)
  
  如果 created_at 相同或 B.created_at < A.created_at:
  用户 A 在通知列表中看到:
    1. "B 发了一条消息" (发生在回复之前？) ❌
    2. "B 回复了你的评论" (实际上发生在消息之前) ❌
```

### 代码级证据

| 位置 | 行号 | 证据 |
|------|------|------|
| `internal/outbox/relay.go` | 57-60 | `FetchUnpublished` 按 `created_at ASC` 排序——事件间顺序正确 |
| `internal/service/notification.go` | 183-191 | `Handle()` 对每个事件独立调用 `Create()`——无父子事件关联逻辑 |
| `internal/service/notification.go` | 105-176 | `notificationFor()` 为不同事件生成独立的 `domain.Notification`——无因果标记 |
| `internal/repository/notification.go` | 36-39 | `ListByRecipient` 按 `created_at DESC, id DESC` 排序——仅时间排序，无因果元数据 |
| `internal/outbox/outbox.go` | 16-18 | `Event` 结构体无 `ParentEventID` 或 `CausationID` 字段 |
| `internal/domain/notification.go` | 15-28 | `Notification` 无 `CausationID` 或序列号字段 |

### 为什么这是高价值

1. **UX 逆直觉**：用户看到通知按时间倒序排列，但如果因果顺序颠倒，用户无法建立上下文。
2. **调试困难**：因果断裂在单个通知级别不可见——用户只会感到"奇怪但说不出为什么"。
3. **跨类型影响**：一旦 aero-id 扩展到更多事件类型（如 `file.shared`、`file.modified`、`comment.report_resolved`），跨类型因果顺序问题会指数级增加。

### 建议方案

1. **事件 CausationID**：为 outbox `Event` 新增 `CausationID *uuid.UUID` 字段，记录触发该事件的父事件。Notification 写入时继承 CausationID，消费端可据此重建因果序。
2. **事件序列号**：为每个 `(aggregate_type, aggregate_id)` 维护单调递增的序列号（使用 DB 序列或 GORM 行锁）。Consumer 可通过序列号检测无序到达并重新排序。
3. **客户端排序提示**：在通知响应中增加 `causation_id` 和 `sequence` 字段，方便客户端（如 SSE 推送的场景）在内存中按因果重排。
4. **聚合级别通知折叠**：类似方向二的 collapsing，但对同一聚合 ID 的所有事件通知做折叠，用 `last_event_type` 表示最终状态而非逐条展示。

**估算**: 中（~100 行 domain 变更 + ~50 行 repository + 1 条迁移）

---

## 方向五：Rate Limiter 粒度缺口——全局限流无法区分端点优先级（Global Rate Limit Lacks Per-Endpoint Differentiation）

**优先级：P2（运维/产品体验）**

### 问题陈述

当前 rate limiter 使用**全局配额**（所有经过鉴权的 `/v1/*` 端点共享同一个 per-actor token-bucket）。一个用户在 20 req/s 的配额下，无论请求的是轻量级 `GET /v1/search?query=hello` 还是重量级 `POST /v1/files`（multipart 文件上传），消耗的令牌数完全相同。

### 三个具体问题

#### 问题 A：轻重不分，轻量操作被重量操作饿死

```
用户向 aero-id 发起：
  10:00:00 - POST /v1/files（上传 50MB 文件）→ 消耗 1 个令牌，占用 5 秒传输时间
  10:00:01 - GET /v1/search?query=hello → 消耗 1 个令牌 → 但剩余配额为 0 → 429
  
  结果：用户在等待大文件上传时无法进行简单的搜索操作
```

#### 问题 B：重试风暴放大问题

幂等键的存在意味着一个写操作可以被安全重试。当用户收到 429 时，合法重试（带 `Idempotency-Key`）消耗额外令牌，加速耗尽配额。

```
用户尝试 POST /v1/comments → 429
用户带 Idempotency-Key 重试 → 再次消耗令牌 → 再次 429
重试 3 次后配额耗尽 → 即使是 GET /v1/messages 也返回 429
```

#### 问题 C：无写操作保护读操作优先

架构设计中将通知/消息的实时推送期望为低延迟。但没有机制确保轻量查询（搜索、通知列表）在流量高峰时不被大量写操作挤压。

### 代码级证据

| 位置 | 行号 | 证据 |
|------|------|------|
| `internal/middleware/ratelimit.go` | 38-52 | `RateLimit` 中间件对所有路由调用 `l.Allow(key)`——单一预算，无路由区分 |
| `internal/handler/grpc/server.go` | 101-105 | gRPC 的 `RateLimitInterceptor` 同样使用单一 `l.Allow(key)`——不区分方法 |
| `internal/pkg/ratelimit/ratelimit.go` | 41-48 | `Limiter.Allow(key)` 的 key 格式为 `"uid:<uid>"` 或 `"ip:<ip>"`——不包含路径或方法信息 |
| `internal/middleware/ratelimit.go` | 56-64 | `rateLimitKey` 函数返回 `"uid:xxx"` 或 `"ip:xxx"`——不携带任何路由信息 |
| `internal/handler/grpc/server.go` | 124-129 | `rateLimitKey` 同样只返回 `"uid:xxx"` 或 `"ip:xxx"` |
| `configs/config.yaml` | 35-40 | `rate_limit.rps: 20` — 单一全局 RPS 配置 |

### 为什么这是高价值

1. **UX 问题**：用户在"正常操作"（搜索、浏览）时因之前的"重操作"（上传）而被限流，感到不公平。
2. **无差异化 QoS**：无法为不同端点设置不同优先级——写操作（产生成本）和读操作（消耗成本）同样对待。
3. **难以调参**：单一 RPS 值试图同时保护 CPU（搜索）、I/O（上传）、DB（列表查询），最优值各不同。
4. **无法做分层**：没有"硬限制"（保障 DB 不过载）和"软限制"（降低非关键操作的速率）的区分。

### 建议方案

1. **端点分级配额**：将全局 RPS 拆分为写操作和读操作两个独立预算。例如：15 req/s 分配给读操作，5 req/s 分配给写操作，互不影响。
2. **端点成本因子**：为每个路由定义"成本"系数。大文件上传 cost=10，轻量搜索 cost=1。每个请求扣除对应的令牌数，而非始终扣除 1。
3. **path-aware key**：rate limit key 包含路由前缀，例如 `"uid:alice:POST:/v1/files"`，使每个端点（或端点族）有独立的 bucket。
4. **分层限流**：第一层为全局 per-actor（20 req/s），第二层为写操作 sub-limit（5 req/s），第三层为单个端点的紧急保护（2 req/s for POST /v1/files）。

```
Limiter 结构建议:
  ┌──────────────────────────────────────┐
  │  Global Per-Actor (20 req/s)          │  ← 第一道防线，所有请求通过
  ├──────────────────────────────────────┤
  │  Write Budget (5 req/s)              │  ← POST/PUT/DELETE/PATCH 共享
  ├──────────────────────────────────────┤
  │  Per-Endpoint Cap                    │  ← 单个端点的极限保护
  │    POST /v1/files:       2 req/s     │
  │    POST /v1/comments:    5 req/s     │
  │    POST /v1/messages:    5 req/s     │
  └──────────────────────────────────────┘
```

**估算**: 低-中（~80 行配置 + ~50 行中间件逻辑）

---

## 总结与建议实施顺序

| # | 方向 | 类别 | 优先级 | 估算 | 前置依赖 |
|---|------|------|--------|------|---------|
| 1 | Idempotency Key 存储耗尽攻击面 | 安全/运维 | **P1** | 低 | 无 |
| 2 | 通知折叠竞态 -> 幽灵通知 | 数据正确性 | **P1** | 低-中 | 无 |
| 3 | Outbox Dead Letter 队列运维盲区 | 运维/可靠性 | **P1** | 中 | 无 |
| 4 | 跨事件类型因果顺序违反 | UX/数据一致性 | **P2** | 中 | 方向 3（outbox 增强） |
| 5 | Rate Limiter 粒度缺口 | 运维/产品体验 | **P2** | 低-中 | 无 |

**建议实施顺序**：方向 1 + 2 并行（两个独立且低风险的错误修复）→ 方向 3（核心运维能力的补充）→ 方向 4 + 5（UX 和产品体验优化）。

---

*本文档基于 2026-07-10 代码库状态生成。所有代码引用指向 `internal/` 下的源文件。*
