这是一份极其详尽的架构扩展分析。我已经验证了附录中的关键发现并与实际代码进行了交叉核对。以下是我的反馈：

---

## 附录发现验证结果

我有 3 处勘误和 1 个补充风险：

| 发现 | 文档结论 | 实际代码结果 |
|------|---------|-------------|
| `idempotency.RunCleaner` 未启动 | ❌ Medium | ✅ **已启动**（`app.go:285`: `stopIdemClean := idemStore.RunCleaner(time.Hour)`）。代码审查时可能该函数尚未接入，但当前主线已包含。 |
| SSO 缓存无容量上限 | ✅ Medium 确认 | 确认两张 cache map 均无 `maxSize`/`maxEntries`。`RunCleaner` 只在期满后删除，但突发唯一 token 会导致无上限增长。 |
| 搜索无 `statement_timeout` | ✅ Medium 确认 | 确认 `SearchFiles`/`SearchComments`/`SearchMessages` 均直接使用传入 `ctx` 的默认超时，上游 handler 层未设置。 |
| 文件上传无审计 | ✅ Low-Med 确认 | `service_upload.go` 中确认无 `s.audit.Log(...)` 调用。Share 操作有审计（`service_share.go:44`），但上传/删除路径没有。 |
| `like_count` 无原子锁保护 | ❌ High **有误** | 实际使用 `gorm.Expr("like_count + 1")` + `ON CONFLICT DO NOTHING` **在事务内**，是安全的原子更新。文档高估了风险。 |
| SSE channel cap=16 | ✅ Low 确认 | `hub.go:42` 注释说明 cap=16，"slow consumers will silently drop events"。 |
| search 不检查 `file_shares` | ✅ Low 确认 | `SearchComments` 以 `owner_uid = ?` 限定范围，用户无法搜索他人文件上的评论。这是设计限制，文档判断准确。 |

**补充风险**：`UserCache` 的 `items map[string]userEntry` 在 `GetMany` 的 `missing` 列表中先占读锁，触发 `fetcher` 后释放并重新获取写锁——`fetcher` 调用期间锁被释放，这意味着**在批获取期间其他 goroutine 可能观察到不一致状态**（同一 uid 同时发起两次远程调用）。文档未提及此竞态。

---

## 对五个方向的逐项评价

### 方向 1：群聊与频道 — 评分 ⭐⭐⭐⭐☆

**优势**：
- `messages.(from_uid, to_uid)` 模型确实是 1:1 的天花板
- 索引设计清晰，`channel_members(channel_id, uid)` + `last_read_at` 是成熟方案
- 通知复用度极高——`NotificationDispatcher` 的 fan-out 逻辑只需将 `to_uid` 替换为 `channel_members` 聚合查询

**补充建议**：

1. **Schema 迁移策略**：文档提到 `messages.channel_id` 可为 NULL 兼容旧数据。建议**不要在同一张表上混用**——旧 1:1 消息应迁移为 `channel(type=direct, member_count=2)`。否则 inbox 查询需要 `WHERE (channel_id IS NOT NULL AND ...) OR (to_uid = ?)` 产生 OR 条件，与索引不亲和。一次性迁移脚本（`000022_migrate_messages_to_channels.up.sql`）可以解决。

2. **`last_read_at` 写放大**：500 人频道中每个人每次查看对话框都 UPDATE `channel_members.last_read_at`。建议用**逻辑时钟**（monotonic `channel.message_seq` 递增）替代时间戳——读取时只需在内存中记录 `last_seen_seq`，后台周期性持久化。

3. **离开频道后消息保留的 GDPR 考量**：`from_uid` 保留意味着即使成员退出，其发言仍可追溯。应该增加一个配置项 `anonymize_on_leave`（将 `from_uid` 替换为 `"deleted-user"`），满足 GDPR 删除权。

### 方向 2：统一活动流 — 评分 ⭐⭐⭐⭐⭐

**这是最推荐的"第一方向"**，理由充分（成本最低、无 schema 变更、为其他方向提供入口）。

**补充建议**：

1. **性能优化**：4 表 UNION ALL + `ORDER BY created_at DESC LIMIT 21` 在单用户场景可行（每个用户的记录数有限），但**全局管理员时间线**（查看所有用户活动）会扫描大量数据。建议：
   - 对每个用户的关注/订阅关系建立物化路径
   - 或用 `pgroll` 做增量物化视图（`WITH NO DATA` + `REFRESH MATERIALIZED VIEW CONCURRENTLY`）

2. **折叠策略的具体实现**：文档提到"5 分钟内 3 个文件上传折叠为一条"。建议复用 `NotificationPreference` 的 `CollapsedCount` 模式，增加一个 `ActivityCollapseWindow` 配置（默认 5 分钟）。相同 `(actor, domain, action)` 在窗口内合并。

3. **SSE `activity.new` 事件**：现有 SSE 事件格式是 `{ID, Type, Data}`。需要在 `Data` JSON 中携带 `domain`、`object_id`、`summary_text`，使客户端无需额外 API 调用即可渲染。这与方向 0（文档未列但应处理）相关：**SSE 事件 payload 标准化**。

### 方向 3：内容生命周期策略引擎 — 评分 ⭐⭐⭐☆☆

**优势**：
- 生产级项目确需此功能
- `Sweeper` 的 `deleteAfter` 参数已预留接入点

**核心关切：配额实时更新**：

`used_bytes` 原子更新是正确方案。但需考虑**并发更新顺序问题**——如果上传事务提交后 outbox 事件触发前进程崩溃，`used_bytes` 不会回滚。建议用**DB 触发器**或**两阶段确认**：

```
BEGIN;
  INSERT INTO files (...);
  UPDATE storage_quota SET used_bytes = used_bytes + $size WHERE uid = $uid;
  -- 如果 UPDATE 导致 used_bytes > quota_max_bytes，ROLLBACK
COMMIT;
```

将配额检查和文件 INSERT 放在同一个事务中，彻底避免不一致。

**再补充一个风险**：`hard_delete` 动作会彻底删除 blob。如果 blob 在 S3 上但 DB 记录已删除（比如因 GC 清理时序），`hard_delete` 的 S3 DeleteObject 调用可能失败（网络分区）导致幽灵 blob——需要补偿机制（重试队列 ± dead letter）。

### 方向 4：协作审核工作台 — 评分 ⭐⭐⭐⭐☆

**这是最高用户价值的方向之一**，文档分析透彻。补充几点：

1. **AutoModerator 规则引擎的设计边界**：文档提到"不硬编码规则"。建议规则用 **CEL (Common Expression Language)** 或 **govaluate** 表达，存储为 JSONB，在 `AutoModerator` 中编译执行。这样热加载、无部署。

2. **审核员匿名化**：审核员查看用户详情时，不应暴露真实姓名。建议审核视图使用 pseudonym（`moderator-{hash}`），减少心理偏见。

3. **二阶段审核的 SLA 要求**：首次审核目标 < 15 分钟，申诉审核 < 4 小时。需要 `AuditSLA` 看板（现有 `metrics` 包可以扩展 Prometheus 直方图：`moderation_resolution_time_seconds`）。

### 方向 5：文件预览加工管线 — 评分 ⭐⭐⭐⭐☆

**现有设计的改进建议**：

1. **外部命令依赖的隔离**：`ffmpeg`、`pdftoppm`、`libreoffice` 都是重型依赖。它们应该在**独立容器或 sidecar 进程**中运行，而不是嵌入 aero-id 主进程。建议将 PipelineWorker 设计为单独的 `cmd/pipeline-worker/` 二进制文件，通过 gRPC 与主服务通信。

2. **缩略图尺寸标准化**：`small=200, medium=600, large=1200` 是合理默认值。但需要考虑**屏幕密度**（Retina/HiDPI），建议增加 `@2x` 变体（`thumb_medium@2x.jpg` = 1200px）。

3. **ClamAV 集成的资源消耗**：一次病毒扫描需要 ~100MB RAM。在内存受限环境中，应使用**流式扫描**（clamd `INSTREAM` 命令），而非先下载完整文件再扫描。aero-id 的 `storage` 接口已有 `Reader(ctx, key)` 方法，可以 stream 到 clamd TCP 连接。

4. **与 S3 Presigned 直传的冲突**：当前 `PresignUpload` + `CompleteUpload` 模型下，文件直接进入 S3，aero-id 从未持有文件内容。缩略图生成需要**重新从 S3 下载文件**——这在带宽和延迟上是损失。对于小文件（<10MB），可以在 CompleteUpload handler 中同步下载并处理，避免额外 S3 往返。

---

## 整体反馈

### 缺失的一个方向：Webhook / 出站事件通知

当前 SSE 只对**活跃连接**有效。对于**服务端集成**（如外部系统监听文件上传事件触发 CI/CD），需要 Webhook 能力。Outbox 已经捕获了所有事件，缺的只是**Webhook 路由层**：

```go
type WebhookSubscription struct {
    ID        domain.ID
    UID       string        // 订阅者
    URL       string        // 回调端点
    Events    []string      // 事件类型白名单，空=全部
    Secret    string        // HMAC 签名密钥
    RetryPolicy string      // "exponential" | "linear" | "none"
    Enabled   bool
}
```

Webhook 的方向与 5 个方向正交，且是**其余 5 个方向**的外部集成出口。方向 1（群聊）需要 webhook 才有 chatbot 生态，方向 4（审核）需要 webhook 才有第三方审核工具。

### 推荐执行顺序调整

我同意方向 2 最先做。但对于 Phase 2，我建议微调：

```
Phase 1（2 周）
  └── 方向 2（统一活动流）+ 附录发现修复
       （SSO cache 容量上限 + search statement_timeout + upload audit）

Phase 2（6 周）
  ├── 方向 1（群聊与频道）
  └── 方向 3 的子集：仅配额管理 + 默认保留策略，
       不含策略引擎 CAS/Rule 的完整通用框架

Phase 3（10 周）
  ├── 方向 4（协作审核）+ 方向 5（预览管线）并行
  │   （审核是独立模块，预览管线依赖 storage 和 outbox，
  │    两个方向互不阻塞，可并行开发）
  └── Webhook 出站事件（方向 6，与审核同时交付）
```

原因是 **方向 3 完整的策略引擎**（可配置规则 + 优先级 + 不同 action）是**产品特性**，但**配额**是**运营必需品**。先做最小可行配额（上传前检查 quota_max_bytes），策略引擎放到 Phase 3 与审核和预览并行。

---

## 代码库其他发现

从代码扫描中补充几个未在文档中列出的点：

| 发现 | 严重性 | 位置 |
|------|--------|------|
| `CircuitBreaker` 的 `halfOpenMax` 默认 3，但**无自动恢复探测的间隔配置**——半开后成功 1 次立即全关，可能抖动 | Low | `internal/sso/circuit_breaker.go` |
| `outbox.Consumer` 的 `ProcessAll` 在单个 goroutine 中顺序处理事件——如果一个事件 handler 阻塞，后续事件延迟 | Medium | `internal/outbox/consumer.go` |
| `metrics` 包注册的 `http_requests_total` label 包含 `path` —— 在高基数路径（如 presigned URL）下可能导致 Prometheus memory explosion | Medium | `internal/pkg/metrics/metrics.go` |
| `RateLimit` 的 stored `atomics` 用 `*uint64` 做的滑动窗口，但 `RunCleaner` 只清理过期 entry，不清理**已过期但从未碰撞的 actor**——actor 数量增长时 map 无限膨胀 | Low-Med | `internal/pkg/ratelimit/ratelimit.go` |

这些可以贴在方向 2 的实现中一起修复（修复成本都很低）。

---

**总结**：这份分析文档质量极高，方法论扎实（代码证据驱动），对每个方向的架构影响评估准确，估算合理。方向 2 最佳作为起点，方向 1 是最大的产品跃升，方向 3/4/5 各自解决不同的生产/运营瓶颈。Webhook 作为补充方向值得纳入路线图。
