我已详细阅读这份《aero-id 扩展方向分析报告》。以下是针对报告中各方向的技术评审与补充意见：

---

## 总体评价

这份扫描非常全面，准确捕捉了 aero-id 当前架构中**最关键的三个瓶颈**（SSE Hub 本地状态、Outbox relay 多实例冲突、内存限流不可共享），以及**最高价值的产品空白**（群聊、文件后处理、版本控制、滥用防护）。以下逐项给出补充意见：

---

## 方向一：水平扩展与分布式状态

报告正确地识别了这也是**生产部署的第一道闸门**。补充几点：

### SSE Hub 跨实例推送

除了 Redis Pub/Sub，还可以考虑 **PG LISTEN/NOTIFY** 作为轻量替代——不需要额外引入 Redis，利用现有 PostgreSQL 连接即可。对于通知推送这种低吞吐场景（~每秒几十次），PG notify 完全够用，并且能避免 Redis 引入带来的运维复杂度。

```sql
NOTIFY aero_sse_channel, '{"uid":"...","event":"..."}';
```

缺点是：
- 单 channel 消息大小有限（~8KB）
- 没有消息持久化（但 outbox 已有持久化，PG notify 仅作实时广播）
- 长连接场景下需要 LISTEN loop

建议**分两步走**：
1. 短期：PG LISTEN/NOTIFY + 本地 SSE Hub
2. 长期：如果 Redis 已经是组织基础设施，再迁移到 Redis Pub/Sub

### Outbox Leader Election

最佳方案是使用 **PostgreSQL 咨询锁（`pg_advisory_lock`）**，无需新增表或外部依赖：

```go
// 在 outbox relay 启动时尝试获取锁
SELECT pg_try_advisory_lock(42); -- 42 = outbox relay 的常量锁 ID
// 失败则 sleep-retry，成功则开始轮询
// 优雅关停时释放：SELECT pg_advisory_unlock(42);
```

优点是零额外基础设施，与 PG 事务生命周期绑定，连接断开自动释放。

### 分布式限流

报告建议 sliding-window + Redis。补充：可以先实现**可插拔 `LimiterBackend` 接口**，当前内存实现作为默认，Redis 实现作为可选。接口设计：

```go
type LimiterBackend interface {
    Allow(ctx context.Context, key string, burst int, interval time.Duration) (bool, error)
    // 降级：返回 error 时调用方 fallback 到本地 token bucket
}
```

降级路径必须考虑——Redis 不可用时退回本地模式（宽松但不 fail close）。

---

## 方向二：文件后处理管线

这是复用现有 outbox 基础设施的**自然延伸**，但要注意几个架构设计点：

### Task 定义与编排

不赞成为每种 MIME 类型创建独立接口。建议采用**管线（pipeline）模式**——每个文件上传触发一个 outbox 事件，然后按顺序经过多个 stage：

```
Upload → [Virus Scan] → [Thumbnail] → [Content Extraction] → [NSFW Detection]
```

每个 stage 是独立的 processor，stage 失败不影响上游 stage 结果。设计为：

```go
type Stage interface {
    Name() string
    Process(ctx context.Context, f *domain.File, blob io.Reader) (*StageResult, error)
}
```

### 资源控制

对大文件处理，关键约束是：
- 每个 processor 实例有**并发限制**（semaphore）
- blobs 下载到临时文件而非内存（`os.CreateTemp`）
- 超时控制（`context.WithTimeout` per stage）
- 如果缩略图 stage 对 >100MB 图片超时，文件仍然可以正常访问

### 使用现有 outbox 事件

文件上传后写入的 outbox 事件（如 `file.uploaded`）已经存在，可以直接订阅。但需要确保：
- outbox relay 在多实例下只有一个 active（依赖方向一的 leader election）
- processor 失败事件进入死信队列，由人工或重试机制处理

### 全文内容索引

报告提到 `files.extracted_text` 列，但当前 `files` 表没有这个列。需要新增迁移。不过，已有 FTS 索引确实是复用的（搜索当前基于消息/评论/文件的 `filename` + `meta`）。

---

## 方向三：多人群组会话

这是**产品价值最高**但实现难度也最大的方向。报告中的数据模型设计（`conversations` + `conversation_members` + `conversation_messages`）是正确的。

### 关键设计决策

当前 `messages` 表的设计（`from_uid`, `to_uid`）限制了扩展性。建议**不要复用 messages 表**，新建 `conversation_messages` 表，独立于 1:1 消息。原因：

1. 当前的 `messages` 表有很多既有的查询路径（对话视图、mark-read、notifications）都和 `from_uid`/`to_uid` 耦合
2. 混合两种模型会显著增加查询复杂度
3. 1:1 消息和群聊消息的权限模型完全不同

### Block 在群聊中的语义

报告已正确指出关键边界情况。补充实现方式：
- 群聊消息查询时，对于 A 屏蔽 B 的情况，A 看到的群聊消息中 B 的消息表现为 `{deleted: true, sender: null}`（软删除
- 写路径：A 发送消息到群聊时，检查 A 是否屏蔽了群内任何人——不需要，屏蔽只影响读路径
- 通知路径：A 发送群聊消息时，不通知被 A 屏蔽的成员；也不通知屏蔽了 A 的成员

### @mention

群聊场景中 @mention 是刚需。建议在消息体中使用类似 `@[uid:xxxxx]` 的格式，发送时解析出 mentioned 列表，在通知分发时标记 `mentioned_uids`，对这些用户推高优先级通知。

---

## 方向四：文件版本控制

### 数据模型建议

更倾向于**新建 `file_versions` 表**而不是在 `files` 上加版本字段，原因：

1. `files` 表当前的列（`owner_uid`, `storage_key`, `checksum`, `size`, `mime_type` 等）都是针对"当前版本"设计的
2. 版本历史可能有不同的元数据（如版本注释、提交者）
3. 避免 `files` 表行数膨胀（每个版本都是新行）

```sql
CREATE TABLE file_versions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id     UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    version     INTEGER NOT NULL DEFAULT 1,
    storage_key TEXT NOT NULL,
    checksum    BYTEA NOT NULL,
    size        BIGINT NOT NULL,
    mime_type   TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(file_id, version)
);
```

`files` 表保留当前版本的 `storage_key`/`checksum`/`size`，上传新版本时：
1. 插入 `file_versions` 行
2. 更新 `files` 表的存储字段
3. 旧版本的 storage key 由 GC 负责清理（引用计数需要扩展以覆盖版本引用）

### 共享与版本

报告说"共享作用于文件根"是正确的。但需要明确：如果用户通过分享链接下载，默认下载**最新版本**。可以加查询参数 `?version=3` 指定历史版本。权限检查作用于 `file_id` 级别，版本只影响 blob 定位。

### GC 策略

软删除 + 版本链 → GC 需要在清理文件时：
1. 检查 `file_versions` 表中是否有其他版本引用同一 blob（去重场景）
2. 只有所有版本 + 主记录都不引用时才能删除 blob
3. 版本保留策略（保留最近 N 个版本）可以作为定时任务

---

## 方向五：滥用防护与信誉系统

### 分层限流（Quick Win）

报告建议将其作为 quick win 单独拉出——我完全同意。这是**改动最小、收益最大**的方向一步。

具体实现建议：

```go
// config 中可以配置
ratelimit:
  default:
    rps: 10
    burst: 20
  per_endpoint:
    POST /v1/messages:       {rps: 5, burst: 10}
    POST /v1/comments:       {rps: 3, burst: 6}
    POST /v1/files:          {rps: 2, burst: 4}
  per_user_age:
    "< 1h":   {multiplier: 0.3}  // 新用户限流更严格
    "< 24h":  {multiplier: 0.5}
    "< 7d":   {multiplier: 0.7}
    default:  {multiplier: 1.0}
```

per-user-age 需要 SSO 返回 `created_at` 字段（或 aero-id 在首次用户交互时缓存注册时间）。

### 信誉系统

这是实现最复杂的方向。建议**分阶段**：
1. **Phase 1**：举报计数器（`users.report_count`, `users.resolved_report_count`）— 纯 SQL，无模型
2. **Phase 2**：静态信誉分（基于举报数/账号年龄/活跃度的加权公式，每 24h 批量计算）
3. **Phase 3**：动态信誉分（实时更新，影响限流阈值和举报权重）

### 管理员控制台

当前 `admin_roles` + `permissions` 表已经存在（如报告中提到），但缺少的是管理 API 端点。建议新增 `internal/handler/http/admin/` 包，挂载在 `/v1/admin/*` 路径下（需要 admin role 鉴权）。

---

## 优先级确认与补充建议

| 序号 | 方向 | 建议启动时机 | 前置依赖 |
|------|------|------------|---------|
| 0 | 分层限流（方向五步骤 1） | **立即** | 无 |
| 1 | 水平扩展 | 第 1 优先 | 无 |
| 2 | 文件后处理管线 | 第 2 优先 | 方向一完成（leader election） |
| 3 | 群聊 / 文件版本 | 并行（第 3 优先） | 方向一完成 |
| 4 | 信誉系统 Phase 2-3 | 持续迭代 | 方向五 Phase 1 完成 |

### 还有两个发现未在报告中提及

1. **幂等键没有整合到 outbox**——当前 `idempotency_keys` 表单独存在，但 outbox 事件写入时没有做幂等去重。如果 relay 在事件写入后、提交事务前崩溃，重试会创建重复事件。可以考虑将 idempotency key 整合进 outbox 写入路径。

2. **文件 GC 的孤儿 blob 扫描**——当前 GC 在清理已删除文件行之后扫描孤儿 blob。但如果文件有新版本（方向四），旧版本的 blob 被新的 `file_versions` 行引用，GC 需要区分"版本引用"和"原始引用"。

---

## 结论

这份报告准确地识别了 aero-id 从 MVP 到生产就绪系统所需的核心投资方向。架构评审工作到位，边界情况覆盖全面。建议按上述优先级顺序开始实施，先从**分层限流**（零架构改动）和**水平扩展**（关键架构瓶颈）两个方向入手。
