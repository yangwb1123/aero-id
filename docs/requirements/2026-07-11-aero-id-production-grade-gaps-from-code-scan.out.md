好的，文档已在磁盘上（`docs/requirements/2026-07-11-aero-id-production-grade-gaps-from-code-scan.md`，639行，28514字节）。让我对全量代码证据进行交叉验证。

---

## 交叉验证报告

我逐一检查了分析文档中每个代码证据引用与实际源代码的对应关系。以下为验证结果：

### ✅ 方向一：操作成本感知速率限制 — **完全验证通过**

| 证据 | 文件:行号 | 验证状态 |
|------|-----------|---------|
| `Allow()` 固定消耗 1 token | `ratelimit.go:68-74` | ✅ `b.tokens >= 1` + `b.tokens--` |
| HTTP 中间件平坦调用 | `middleware/ratelimit.go:46` `l.Allow(key)` | ✅ 无 cost 参数 |
| gRPC 拦截器同理 | `handler/grpc/server.go:130` `l.Allow(rateLimitKey(ctx))` | ✅ 无 cost 参数 |
| 无 `AllowWeight` | 全包 grep 零命中 | ✅ |

**补充发现**：`ratelimit.go:45` 中 `New()` 的 `burst` 参数是 `int` 类型，如果要做加权消耗（如 cost=10、burst=5），burst 会迅速被消耗完。这意味着加权方案需要同时将 burst 从 `int` 升级为 `float64`（或按 cost 等比例放大 burst）。

---

### ✅ 方向二：内存数据结构无界增长 — **完全验证通过**

| 证据 | 文件:行号 | 验证状态 |
|------|-----------|---------|
| Hub.Subscribe 无上限 | `hub.go:42-45` | ✅ 无 maxConnections / maxPerUID |
| uidSubs 删除逻辑正确 | `hub.go:85-89` | ✅ len==0 时 Delete |
| Rate Limiter buckets 无硬上限 | `ratelimit.go:29-37` | ✅ 仅有 idleTTL 保护 |
| App shutdown 不调用 Hub.Close | `app.go:401-440` (`shutdown()`) | ✅ **确认缺失** — HTTP Shutdown、gRPC GracefulStop 都存在但 `sseHub.Close()` 未被调用 |

**补充发现**：
- `notification.go` 中的 `notifRateLimiter.keys map[string][]time.Time` 虽然在每次 `allow()` 中内联清理过期条目（行 `~196-203`），但**没有 map 大小上限**。在极端情况下（大量不同 `recipientUID:notifType` 组合同时涌入），清理论只会清理过期条目，不会限制**活跃条目数**。如果系统有 10 万用户 x 3 种通知类型，每 30 秒触发一次，map 持有约 5000 个活跃键，每个 `[]time.Time` 常有 5-10 个 entry → 约 50K 个 time.Time 对象，内存尚可接受但未考虑攻击场景（伪造 recipientUID）。

---

### ✅ 方向三：消息内容验证空白 — **完全验证通过**

| 证据 | 文件:行号 | 验证状态 |
|------|-----------|---------|
| message.Send 只有空值检查 | `message/service.go:46-48` | ✅ `in.Content == "" && in.FileID == nil` — 无长度上限 |
| message.Edit 同理 | `message/service.go:221-224` | ✅ `content == ""` — 无长度上限 |
| comment.Create 同理 | `comment/service.go:25-30` | ✅ `in.Content == ""` — 无长度上限 |
| comment.Edit 同理 | `comment/service.go:80` | ✅ `newContent == ""` — 无长度上限 |
| domain.ValidateComment 仅检查嵌套 | `domain/comment.go:37-42` | ✅ 只检查 `parent.ParentID != nil` |
| 迁移无 CHECK 约束 | `000002_create_messages.up.sql` / `000003_create_comments.up.sql` | ✅ `TEXT NOT NULL` 无额外约束 |

**补充发现**：`message.SendSystem()`（`service.go:121-142`）同样只有 `content == ""` 的检查。系统消息可能用于管理通知或系统广播，理论上更需要长度保护（但可能是内部调用）。

---

### ✅ 方向四：SSE 连接生命周期管理 — **完全验证通过**

| 证据 | 文件:行号 | 验证状态 |
|------|-----------|---------|
| Hub 有 Close 方法但 App 不调用 | `hub.go:101-113` / `app.go:401-440` | ✅ 方法存在，但 shutdown() 中无调用 |
| 唯一退出路径是 context.Done | `events.go:63-64` | ✅ `case <-c.Request.Context().Done(): return` |
| 无全局连接上限 | `hub.go:42-45` | ✅ |
| 无 per-UID 上限 | `hub.go:42-45` | ✅ |
| 背压静默丢弃 | `hub.go:118-126` line 125 `default:` | ✅ 无日志、无指标、无通知 |

**补充发现**：`events.go:58-61` 的 replay 区块可能阻塞较长时间（DB 查询），在此期间 SSE 连接 context 可能已经取消——但没有检查 `ctx.Done()`。如果客户端在 replay 期间断开，replay 仍会走完所有 50 条通知的循环再开始主 loop。

---

### ✅ 方向五（附加）：通知投递效果分析 — **完全验证通过**

| 证据 | 文件:行号 | 验证状态 |
|------|-----------|---------|
| Notification 仅有 ReadAt | `domain/notification.go:27-28` | ✅ 只有 `ReadAt *time.Time` |
| 无 DeliveredAt / DeliveryChannel / ClickedAt | 同上 | ✅ 确认缺失 |
| 无通知相关 metrics | `metrics.go` | ✅ 有 HTTP RED、gRPC RED、SSO 指标、Outbox 指标，但无 `notifications_*` |

**补充发现**：`metrics.go:28-38` 的 Registry 结构体有 `sync.Mutex` 保护，如果要添加新的通知指标系列，需要在这个已有 200+ 行的文件中新增代码——可能触碰到 AGENTS.md 的 500 行文件大小限制。建议方向五的实现考虑拆分 metrics 文件（比如 `metrics_notification.go`）。

---

## 总体评估

| # | 方向 | 验证 | 补充的细微修正 |
|---|------|------|---------------|
| 1 | 操作成本感知限流 | ✅ | Burst 需从 int→float64 升级才能支持加权消耗 |
| 2 | 内存无界增长 | ✅ | 通知频率限制器的 map 也缺少硬上限 |
| 3 | 消息内容验证 | ✅ | **额外风险**: SendSystem 也未做限制 |
| 4 | SSE 生命周期管理 | ✅ | replay 阶段缺少 context 取消检测 |
| 5 | 通知投递效果 | ✅ | 新增指标系列可能需要拆分 metrics.go |

**结论**: 文档中的 35 处代码引用**全部验证通过**，5 个方向提出的系统性缺口在代码库中真实存在。以上补充的 5 处细节修正不改变原始分析的核心论证和优先级建议。

需要我进一步做什么？如果需要，我可以将这些补充修正写入文档的对应章节。
