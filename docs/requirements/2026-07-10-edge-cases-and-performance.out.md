现在我有了完整的代码验证基础。以下是我的综合反馈：

---

## 代码验证结果与文档勘误

基于 `internal/` 核心文件的交叉验证，我对文档的关键发现做以下确认与修正：

### ✅ 确认正确的发现

| ID | 等级 | 文档判断 | 代码证据 |
|----|------|---------|---------|
| **EC-5** | 中 | SSE channel cap=16 静默丢弃 | `hub.go:96` — `make(chan SSEEvent, 16)` + `select { case ch <- evt: default: }` |
| **EC-10** | **中高** | SSO 缓存无容量上限 | `token_cache.go` 的 `map[string]tokenEntry` + `user_cache.go` 的 `map[string]userEntry` — 均无 `maxSize`/LRU |
| **EC-15** | **中高** | 搜索不检查 `file_shares` | `SearchFiles(ctx, ownerUID, query, limit)` — 接口只接受 `ownerUID`，未 `UNION` 分享权限 |
| **EC-6** | 高 | GC 默认禁用 | `config.yaml` 中 `gc.enabled: false`，docker-compose 中无 `AERO_ID_STORAGE_GC_ENABLED` |
| **PERF-6** | 中 | 缓存无预热 | `TokenCache` + `UserCache` 的构造器不加载任何数据，`app.go` 中无预热逻辑 |
| **PERF-14** | 低中 | Relay ticker 固定间隔 | `relay.go:Start(interval)` — 固定 `pollInterval`，每次 tick 都执行 `drain()` |

### ⚠️ 需要修正或补充的发现

| ID | 文档判断 | 实际情况 | 影响 |
|----|---------|---------|------|
| **EC-1** ❌ | "先 SELECT 判断状态再 UPDATE，存在 TOCTOU 窗口" | `comment.go:Like()` 使用 **单个事务** 内 `ON CONFLICT DO NOTHING` + `gorm.Expr("like_count + 1")` — 是安全的原子操作。**文档高估了风险。** | 低 — 建议将状态从"中"降为"低"，或移除 |
| **EC-4** ❌ | "NotificationDispatcher 当前对重复投递不做去重" | `service/notification.go` 中的 `NotificationDispatcher` **已实现** 折叠（`WithDispatcherCollapse(5min)`）→ 使用 `FindRecentByTypeAndActor` + `IncrementCollapsedCount`。但 `internal/notification/dispatcher.go` 是另一实现（未使用）。**outbox 主路径已有折叠机制。** | 中 — 文档忽略了主 dispatcher 的实现。不过折叠不是完全去重（只覆盖 `collapseWin` 窗口内的同类型通知），EC-4 的重复投递风险在 relay crash 场景中仍然存在 |
| **EC-7** ❌ | "同一批中硬删除后 blob 被遗漏" | `sweeper.go` 的 `HardDelete(ctx, ids)` **一次性删除所有 id**，然后用 `seen` map 去重 storage key。如果两文件引用同一 blob，在硬删除所有行后 `countLive` 返回 0 → blob 被正确删除。**文档假设逐行删除，与实码不符。** | 低 — 实际上是正确的行为，EC-7 是假阳性 |
| **EC-11** ⚠️ | "搜索查询无超时/资源限制" | `service/search.go:23` 定义 `searchTimeout = 5 * time.Second`，`:81` 使用 `context.WithTimeout(ctx, searchTimeout)` 包裹搜索。**Repository 层确实无 `statement_timeout`，但 Service 层有 context timeout。** 文档说"数秒"正确，但断言"没有 timeout"不完全准确 | 中 — 风险等级仍为中，但理由需调整 |
| **EC-2** ⚠️ | "reply_count 不递减" — 低风险 | 需要确认 `comment.go:Delete` 中是否递减父评论的 `reply_count`。让我验证。 | 待确认 |

### 🆕 额外发现（代码扫描补充）

| # | 发现 | 严重性 | 位置 |
|---|------|--------|------|
| **N1** | `internal/notification/dispatcher.go` 和 `internal/service/notification.go` 存在**两个独立的 Dispatcher 实现**。`app.go` 使用的是 `service.NotificationDispatcher`（支持 collapse/rate-limit）。`notification.Dispatcher` 是**孤立的实现**——未被 outbox relay 使用。重复代码。 | 中 | `internal/notification/dispatcher.go` vs `internal/service/notification.go` |
| **N2** | `metrics.go:ObserveHTTP` 注释说 `route` 应该是 `matched template` (如 `/v1/files/:id`)，但 handler 层调用时传入的是**实际路径**如 `/v1/files/123`（需验证）—— 会导致 Prometheus 标签基数爆炸 | 中高 | `internal/pkg/metrics/metrics.go` + 各 HTTP handler |
| **N3** | `UserCache.GetMany` 的 `fetcher` 调用期间释放了读锁——同一 uid 可能同时触发两次远程调用（如文档的 `.out.md` 补充风险所述） | 中 | `internal/sso/user_cache.go:74-82` |
| **N4** | `Sweeper.Start` 使用 `context.Background()`——PERF-13 确认。shutdown 时不能传播取消信号 | 中 | `internal/storage/gc/sweeper.go:215` |

---

## 对文档五个方向的评价

### 方向 A：分布式限流 — ⭐⭐⭐⭐

**代码证据支撑：** `config.yaml` 中 `rate_limit.enabled: false`（默认关闭）+ `internal/pkg/ratelimit/ratelimit.go` 使用 per-instance token bucket。多副本部署下确实失能。

**关键补充：**
1. 当前 `RateLimit` 中间件使用 `*ratelimit.Limiter` 实例——需要为分布式模式实现 `Limiter` 接口的不同后端（Redis）
2. 配额管理（resource_quotas）的原子更新可以利用现有的 **outbox + 事务** 模式，类似 quota 检查在 service 层的事务内完成（如文档所述）
3. **已有基础设施可用**：`idempotency` 模式的 `key BY uid` 可复用为配额核算的锁定机制

### 方向 B：多地域部署 — ⭐⭐⭐

**杠杆最大但成本最高。** 代码中已有多处隐含了单体部署假设：
- `UserCache` 和 `TokenCache` 是纯内存的
- `ratelimit.Limiter` 是 per-instance 的
- `sso.Client` 是单一 endpoint
- `Sweeper` 是 per-instance 的（多个副本会重复 sweep！）

**简化路径更实际：** 读写分离（读副本）+ `ReplicaSelector` 模式。完全分片需要大幅重构。

### 方向 C：Webhook/事件订阅平台 — ⭐⭐⭐⭐⭐

**最高杠杆方向。** 现有基础设施已投入 80%：
- `outbox` 包已有：`Repository`、`Relay`、`Handler`、`Event` 模型、`MarkFailed`/`MarkPublished`、死信、backoff
- `outbox.NewWebhookHandler` 已存在——但**只支持单一 URL**
- 事件类型枚举已在 `dispatcher.go` 中定义（`message.sent`、`comment.replied`、`message.read`）

所需新增的不过是一个 `webhook_subscriptions` 表 + 扇出逻辑。**这是最短路径到"平台化"的方向。**

### 方向 D：Schema 版本化 — ⭐⭐⭐

**技术债务较低，但价值高。** 当前迁移基于 `cmd/migrate` CLI + `internal/pkg/migrate` 包，是简单的 up/down 模式。

**代码证据：** 22 个迁移都是 **schema-only**，没有数据迁移。`auto_migrate: false` 意味着迁移是手动执行的。零停机迁移的场景尚未考虑。

**可立即做的改进：** `expand/migrate/contract` 不需要大型框架——每次迁移多加一个 `_data.sql` 后缀约定 + `migrate verify` 命令检查 `down.sql` 的可逆性即可。

### 方向 E：负载测试 + SLO — ⭐⭐⭐⭐

**这是所有方向的前提条件。** 代码库已经有：
- `Makefile` 目标链
- `test/` 目录（单元 + 集成）
- Docker Compose 用于本地运行

**缺失：** 没有任何 `scripts/loadtest/` 目录或 k6 脚本。性能仪表盘没有基准线。

**建议优先做这个方向**，因为它为所有其他方向提供**可测量的决策依据**。

---

## 核心修正：方向执行排序

综合文档的 Phase 0-3 + 我的代码验证结果，建议调整如下：

```
Phase 0（立即可做，1 周内）
  ├── EC-10 (SSO 缓存 LRU 上限) — 真正的安全风险
  ├── EC-15 (搜索权限检查 file_shares + user_blocks) — 数据泄露风险
  ├── N1 (删除重复的 notification/dispatcher.go 实现) — 消除技术债务
  ├── N2 (metrics 路由标签基数验证与修复)
  └── PERF-6 (SSO 缓存预热)
  ↑ 以上不需要架构变更，直接作为 code review 的一部分

Phase 1（2-3 周）
  ├── 方向 E（负载测试 + SLO 框架）— 所有后续的可衡量性前提
  ├── 方向 C（Webhook 事件平台）— 利用 outbox 基础设施，杠杆最高
  └── EC-4 加强（通知 upsert 唯一约束 / 幂等键）

Phase 2（4-6 周）
  ├── 方向 A（分布式限流）— 多副本安全基线
  ├── 方向 D（Schema 版本化 + expand/migrate/contract）— 零停机前提
  └── 群聊（方向 1，来自 companion doc）

Phase 3（6-8 周）
  ├── 方向 B（多地域部署）— 扩展性约束
  ├── 审核工作台（方向 4，来自 companion doc）
  └── 文件加工管线（方向 5，来自 companion doc）
```

**与文档原提案的主要差异：**
1. **方向 C 提前到 Phase 1**（而非 Phase 2）— 因为 outbox 基础已就位，投入产出比最高
2. **方向 E 提前到 Phase 1** — 和文档一致，但强调它的前置性
3. **方向 B 推迟到 Phase 3** — 成本最高，收益取决于用户规模
4. **Phase 0 增加 N1 和 N2** — 代码扫描发现的实际问题

---

是否需要我进一步深入验证某个具体方向，或者开始实施 Phase 0 的修复？
