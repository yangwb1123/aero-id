好，我已经完整扫描了当前代码库的 `main` 分支。你的这份架构文档写得非常扎实——五条方向的识别准确、理由充分、依赖分析清晰。以下是我的反馈，分为 **事实校正**、**对每条方向的深度评估**、**发现的新机会** 和 **执行路径建议**。

---

## 一、事实校正：文档中过时的包路径

你在文档中引用的 `internal/service/mocks`、`internal/handler/{http,grpc}` 等路径已被重构。当前代码库已经从 **单体 service 包** 演进为 **按领域划分的独立包**：

| 领域 | 当前包路径 |
|------|-----------|
| 文件 | `internal/file/` (service_crud.go, service_upload.go, service_share.go, handler_http.go, handler_grpc.go, repository.go, repository_share.go) |
| 消息 | `internal/message/` (service.go, repository.go, handler_http.go, handler_grpc.go) |
| 评论 | `internal/comment/` (service.go, repository.go, handler_http.go, handler_grpc.go) |
| 通知 | `internal/notification/` (dispatcher.go, service.go, repository.go, handler_http.go, handler_grpc.go) |
| 屏蔽 | `internal/block/` (block.go, repository.go, handler_http.go, handler_grpc.go) |
| 搜索 | `internal/search/` (search.go, handler_http.go, handler_grpc.go) |
| 审计日志 | `internal/audit/` (repository.go) |

每个包里 service/handler/repository 各司其职，handler 层非常薄，service 层是业务逻辑的唯一来源——完全符合你描述的架构原则。你在文档"涉及改动规模"一节引用旧路径时需要注意映射。

---

## 二、对每条方向的深度分析

### D1. 配额管理 — ⭐ P0 正确

这是当前最明显的缺口。我额外发现的佐证：

**现有设施可复用：**
- **全局 `MaxUploadBytes`** (`config.StorageConfig.MaxUploadBytes`) 已有，但只在 `file/service_upload.go` 中做单文件检查，扩展为按用户配额检查的模式非常自然
- **Outbox + Notification** 管道已经就位——接近阈值时推送预警通知可以零额外基础设施成本实现
- **限流器** (`internal/pkg/ratelimit/`) 虽然进程内，但配额检查是同步的，不需要分布式协调

**一个需要关注的设计决策：** 你是打算在 **service 层每个 Create/Upload 操作前同步检查**，还是用 **outbox 事件做异步最终一致性计数**？同步检查更精确但会增加写路径延迟（每次写入多一次 DB 查询）；异步不会增加延迟但在高并发下可能短暂超限。我的建议：

```
同步检查（关键路径）：
  file.Create / message.Send / comment.Create
    → quotaRepo.Check(ctx, uid, resource_type, delta)
    → 超限返回 429 / 403（配置控制软/硬）
    
异步更新（计数写入不阻塞主路径）：
  same outbox events → QuotaUsageUpdater handler
    → quotaRepo.Increment(ctx, uid, resource_type, period, delta)
```

这样写路径只做读检查（快），计数更新走 outbox（可靠不阻塞）。PG 行锁竞争比在写路径上做 `INSERT ... ON CONFLICT DO UPDATE` 小得多。

### D2. 媒体处理流水线 — ⭐ P1，但建议缩小初始范围

文档描述的范围很全（缩略图+转码+扫描+文档预览），但初始实施建议**仅做病毒扫描**，理由：

1. **ClamAV 可以用 Unix socket 调用**，无需额外基础设施（FFmpeg 和文档预览需要更多依赖）
2. **扫描是安全基线**，MIME 白名单只能阻挡类型不匹配的文件，不能阻挡带有恶意 payload 的合法类型文件
3. **缩略图/转码可以异步**但需要存储派生文件——当前 `domain.File` 没有 `FileDerivatives` 概念，需要 migration + domain 扩展
4. **被标记为 quarantine 的文件需要阻止下载**——当前 `FileService.Get` / `DownloadURL` 没有"状态"概念，需要加 `File.Status` 字段

建议把 "processor pipeline" 设计成一个 **Handler 链**（参考 `outbox.NewChain` 模式）：

```go
type Processor interface {
    Process(ctx context.Context, f *domain.File) (*ProcessingResult, error)
}

type Pipeline struct {
    processors []Processor
    // on failure: mark quarantine, emit notification
}
```

`file.uploaded` outbox 事件 → Pipeline.Handle → 每个 processor 顺序执行 → 结果写回 `files.meta` 或 `file_derivatives` 表。

### D3. 分布式协调 — ⭐ 文档低估了紧急性

你说"流量达到单实例瓶颈时"再开始 Phase 1，但我认为 **当前代码已经有几个多实例数据丢失风险**：

1. **SSE Hub** (`internal/hub/hub.go`) — 完全进程内。如果用户连接到实例 A，但通知 outbox 事件被实例 B 的 relay 处理，`Publish` 只会推送给 B 的 subscriber，而 A 上的用户收不到。**这不是性能问题，是正确性问题。**
2. **Outbox relay** — 当前 `Relay` 没有分布式锁。双实例同时 `FetchUnpublished` + `MarkPublished` 存在重复通知的风险。`MarkPublished` 有 `published_at IS NULL` 保护，但竞态窗口仍然存在。
3. **TokenCache / UserCache** — 进程内 TTL 缓存在多实例下缓存命中率减半，SSO 压力翻倍，可能触发断路器误判。

**最紧急且侵入最小的 Phase 0**：给 SSE hub 加上 **Redis Pub/Sub 桥接**，让跨实例的 `Publish` 广播到所有实例。这只需要：

```go
type Hub struct {
    subs    sync.Map  // local subscribers
    redis   *redis.Client
    pubsub  *redis.PubSub
}

func (h *Hub) Publish(uid, eventType, data string) {
    h.localPub(uid, eventType, data)     // push to local subscribers
    h.redis.Publish("sse:"+uid, msg)      // relay to other instances
}

// background goroutine: receive from Redis and push to local subscribers
```

这可以在 **不改变 notification dispatcher 一行代码** 的前提下解决跨实例推送问题。其他的 Phase 1/2 可以按你的节奏推进。

### D4. 协作与社交图谱 — ⭐ P2 但需要回答产品问题

这部分文档描述得很清楚。我想补充的是：

**从实现角度看，群聊并不依赖 D3**。群聊的核心是 `conversation_participants` 表和消息的 `conversation_id` 字段，通知可以使用已有的 outbox 为每个参与者创建 notification row。如果 SSE 推送跨实例有问题（D3），用户刷新页面或轮询时仍能拿到通知——只是实时性受影响，不是功能不可用。

所以 D4 可以**独立于 D3 并行推进**。真正的依赖是**产品方向决策而非技术依赖**。

**一个需要讨论的设计约束**：文件分享目前只有读权限（`file_shares` 表）。如果要支持群聊中的文件协作（共同编辑等），需要扩展 `file_shares` 加上 `permission` 列（read/write/admin），或者引入 `conversation.file_access` 概念。

### D5. 合规接口 — ⭐ P1，但文档遗漏了一个关键点

文档说 "无依赖"，实际上它依赖的是 **异步处理基础设施（outbox）**，而 outbox 已经有了。但我发现一个更紧迫的问题：

**当前 aero-id 没有 orphan 用户数据清理管道。** 当 SSO 删除用户后，aero-id 中的 `owner_uid` / `from_uid` / `to_uid` 变成悬挂引用。文档提到"定期或通过 webhook"清理，但没有说 **谁触发这个清理**。

建议的设计补充：SSO 应该发送一个 gRPC webhook（或 aero-id 轮询 SSO 的 deleted-users 端点）来触发数据清理：

```
SSO 删除用户 → SSO 调用 aero-id DeleteAccountData RPC（需新增）
  → aero-id 软删所有该 uid 的数据
  → 进入 GC 管道进行硬删
```

这个 RPC 在 38 个现有 RPC 中不存在，需要加到 proto + 双协议 handler。

---

## 三、文档中未覆盖的中等规模优化

除了附录中的点外，我还发现的几个值得关注的问题：

### 1. `domain.File` 缺少 `Status` 字段

当前文件模型没有状态字段。D2（病毒扫描）假设有 `quarantine` 状态，D5（合规删除）需要 `deletion_pending` 状态。建议在 schema 中预先加入：

```sql
ALTER TABLE files ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
-- 值: active, quarantined, deleted_pending
```

### 2. SSE hub `?last_event_id` 持久化缺失

你已经在附录中提到了，我再补充一个数据：当前 `events.go` handler 中，`Last-Event-ID` 头被解析但没有用于重新投递错过的通知。这意味着：

- 用户断开 SSE 连接超过 16 个事件（channel buffer）后重连 → 事件永远丢失
- 如果客户端实现重连逻辑（EventSource 默认行为），它发送 `Last-Event-ID` 但服务端忽略

修复方案（可在不引入 Redis 的情况下完成）：

```go
// events.go handler
func (h *EventsHandler) ServeHTTP(w, r) {
    lastID := r.Header.Get("Last-Event-ID") // SSE spec
    if lastID != "" {
        // replay missed notifications from notifRepo.ListSince(lastID)
        // send them as SSE events before entering the subscribe loop
    }
    // then subscribe for real-time
}
```

### 3. Dispatcher 的 rate limiter 已经硬编码

在 `app.go` 中：

```go
service.WithDispatcherRateLimiter(10), // max 10 per type per minute
```

这个值不是配置驱动的。建议移到 config（`outbox.dispatcher.rate_limit`）中，方便运维调整。

---

## 四、执行路径建议

如果我来排期，会这样安排：

```
Week 1-2:  D1 配额管理（核心数据结构 + service 层检查 + 端点）
                 + SSE Hub Last-Event-ID 重播修复（小改动，大收益）
Week 3-4:  D5 合规接口（export_requests + 批量删除）
                 + 用户数据清理 RPC
Week 5-6:  D3 Phase 0（SSE Redis 桥接，解决多实例推送正确性）
                 + D2 Phase 0（ClamAV 病毒扫描）
Week 7-8:  D4 群聊基础（conversations + participant 表 + 消息扩展）
Week 9+:   D2 缩略图/转码 / D3 Phase 1-2 / D4 收藏/提及
```

理由：D1 和 D5 无额外基础设施依赖，能尽快交付商业/合规价值。D3 Phase 0 解决的是**正确性问题**而非性能问题，应该比"等到单实例瓶颈"更早。

---

## 五、需要你确认的问题

1. **D1 配额检查**：同步检查 + 异步 outbox 更新，还是纯同步？关键路径延迟是可接受的吗？
2. **D4 产品方向**：aero-id 的定位是"个人数据存储 API"还是"团队协作平台"？这决定了 D4 的投入力度和优先级。
3. **D3 优先级**：你认同 SSE 跨实例推送是正确性问题而非性能问题吗？如果认同，我建议把 Phase 0 提到 P1。
4. **当前重构**：你已经从旧包路径迁移到新领域包结构了。文档里涉及的文件路径需要更新以匹配新架构，需要我帮忙逐个对齐吗？

准备好后可以告诉我从哪个方向开始实施。
