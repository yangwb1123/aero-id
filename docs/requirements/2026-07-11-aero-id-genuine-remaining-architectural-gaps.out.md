现在我拥有足够的数据来提供完整的验证。以下是逐方向分析结果：

---

## ✅ 全量代码级验证结果

我完成了以下交叉验证：
- **8 份 REVIEW 文档**（6月30日）逐行扫描
- **`docs/requirements/` 下 281 份文件**关键词搜索
- **核心代码文件**逐行确认每个代码级证据

---

### 方向一：消息内链接预览 / URL Unfurling

**"零系统性覆盖"声明：❌ 不准确**

`docs/REVIEW_2026-06-30_PRODUCT_GAPS.md` **第 548-565 行** 以独立方向 D 系统性覆盖了 URL 链接预览：

```markdown
### 🎯 方向 D：URL 链接预览（Link Unfurling / OG Metadata）
概念：当消息包含 URL 时，自动提取该 URL 的 Open Graph 元数据
...
新表 url_unfurls：...(url_hash TEXT PRIMARY KEY, og_title TEXT, ...)
outbox handler unfurlSink
messages 表增加 unfurls JSONB
API 变更：GET /v1/messages/:id 返回的 message 中已包含 unfurls
隐私：URL 预览 fetch 的 IP 可能泄露...建议使用代理
配置：unfurl.enabled、unfurl.timeout、unfurl.max_urls_per_message
```

同时 `docs/REVIEW_2026-06-30_FAILURE_ABUSE_API.md` 也有 10 次提及。此方向已被**完整地系统性覆盖**。

**代码证据准确度**：
- Evidence 1 (`Message.Content` 无链接元数据字段) — ✅ 准确
- Evidence 2 (`MessageKind` 无 LinkPreview 类型) — ✅ 准确
- Evidence 3 (消息创建路径不做 URL 检测) — ✅ 准确
- Evidence 4 (proto 中无 link_preview 字段) — ✅ 准确

---

### 方向二：端点粒度限流

**"零系统性覆盖"声明：✅ 基本准确**

在全部 8 份 REVIEW 文档中：
- `REVIEW_2026-06-30_PRODUCT_GAPS.md` 第 156 行讨论的是 **per-instance 限流 vs 分布式限流**（Redis-backed），不是端点级覆盖
- 确实没有任何文档将"端点粒度限流"作为独立方向

**代码证据准确度**：
- Evidence 1 (`RateLimitConfig` 无 Overrides) — ✅ 准确
- Evidence 2 (中间件使用单一 Limiter 实例) — ✅ 准确
- Evidence 3 (gRPC 同样全局) — ✅ 准确
- Evidence 4 (`ratelimit.Limiter` 按 actor uid 键控，无路由维度) — ✅ 准确

**额外发现**：`middleware/rate_limit.go` 中中间件确实接收单个 `*ratelimit.Limiter`，但 `gRPC` 端的限流注入是通过 `grpcsrv.WithRateLimit(limiter)` 传入。两端的**键空间都只有 actor uid，没有路由维度**。

---

### 方向三：幂等性存储降级模式

**"零系统性覆盖"声明：✅ 基本准确**（但现有降级已存在）

8 份 REVIEW 中确实没有任何文档系统性地分析过幂等性存储的降级模式。

**但代码证据有严重错误**：

| 证据 | 声明 | 实际代码 | 判定 |
|------|------|----------|------|
| Evidence 3 | "Store.Reserve 失败时直接返回 500" | `case err != nil: c.Next() // fail open` — 已在错误时透传请求 | ❌ **错误** |

幂等中间件已在 `internal/middleware/idempotency.go:43` 实现**有意的 fail-open 设计**：

```go
case err != nil:
    c.Next() // fail open
```

这是刻意设计（注释"Store errors fail open so the idempotency layer can never take writes down"）。所以"方向三"的核心论点——数据库抖动导致级联故障——**已经在中间件层被消除**。

但剩余的代码证据（Evidence 1: 唯一实现 GormStore、Evidence 2: 无 InMemoryStore、Evidence 4: 无配置）是准确的。有价值的部分是：
1. 没有 `InMemoryStore` 作为二级缓存
2. 没有 `DegradingStore` 包装器 + 自动恢复
3. 没有配置项控制降级行为
4. 没有 `idempotency.degraded` 指标

**所以方向三的骨架是对的，但核心论据被现有实现削弱了。**

---

### 方向四：多存储后端抽象与故障切换

**"零系统性覆盖"声明：✅ 基本准确**

- `REVIEW_2026-06-30_FAILURE_ABUSE_API.md` 第 74 行提到"Local 和 S3 存储切换需要重启（配置文件变更）"— 但只是作为已知限制的注释，**不是作为独立方向**的系统性分析
- 没有任何文档提出 `StorageRouter`、`FailoverStrategy`、冷热分层

**代码证据准确度**：
- Evidence 1 (`FileStorage` 接口支持多实现，但 wiring 只选一个) — ✅ 准确
- Evidence 2 (`StorageConfig` 无多后端或故障切换配置) — ✅ 准确
- Evidence 3 (FileService 持有单个 FileStorage 引用) — ✅ 准确
- Evidence 4 (S3 不可用时无降级到 local 的逻辑) — ✅ 准确

**额外观察**：`storage/FileStorage` 接口的设计是干净的。`buildStorage` 确实只返回一个实现。但 `storage/gc/` 目录中的 `sweeper.go` 需要与 `FileStorage` 交互——它目前只持有单个 `FileStorage` 引用，如果引入多后端也需要适配。

---

### 方向五：SSE 重连历史溢出分页

**"零系统性覆盖"声明：✅ 准确**

- `REVIEW_2026-06-30_PRODUCT_GAPS.md` 第 84 行讨论的是 **Hub channel buffer (16) 溢出**——这是实时推送通道的容量问题，**完全不同于** `replayMissed` 的 50 条上限溢出
- 没有任何文档分析 `replayMissed` 硬编码 limit=50 且无溢出检测的问题

**代码证据准确度**：
- Evidence 1 (`replayMissed` 使用硬编码 limit=50，无分页逻辑) — ✅ 准确
- Evidence 2 (`ListSince` 返回上限 50，无溢出标记) — ✅ 准确
- Evidence 3 (SSE 协议单向推送，无客户端拉取补丁机制) — ✅ 准确
- Evidence 4 (`ListSince` 无 next_cursor 返回值) — ✅ 准确

**额外发现**：`ListSince` 的查询是 `ORDER BY created_at ASC, id ASC LIMIT limit`——返回的是**最旧的通知优先**。这意味着在溢出场景中，丢失的是**最新的通知**（更可能被用户注意到）。如果改为 `ORDER BY created_at DESC`，丢失的是最旧的（用户可能还有模糊印象），这是值得讨论的产品权衡。

---

## 总结修正表

| # | 方向 | "零系统性覆盖"判定 | 代码证据准确性 | 核心价值 |
|---|------|-------------------|---------------|----------|
| 1 | URL Unfurling | ❌ **已在 PRODUCT_GAPS.md 方向 D 系统性覆盖** | ✅ 代码证据准确 | 已被识别 |
| 2 | 端点粒度限流 | ✅ 准确 | ✅ 全部准确 | 中-高 |
| 3 | 幂等性降级模式 | ⚠️ 方向未覆盖，但**核心论据被现有 fail-open 设计削弱** | ❌ Evidence 3 错误 | 低-中（已有 fail-open） |
| 4 | 多存储后端故障切换 | ✅ 准确 | ✅ 全部准确 | 高 |
| 5 | SSE 重连溢出分页 | ✅ 准确 | ✅ 全部准确 | 中 |

**经代码级验证后，真正"零系统性覆盖 + 代码证据全部准确"的方向是：方向二（端点粒度限流）、方向四（多存储后端故障切换）、方向五（SSE 重连溢出分页）。**

方向一已有完整覆盖。方向三的核心论点（数据库抖动导致级联故障）被现有 fail-open 设计大幅削弱，但其二级缓存和指标化部分仍有价值。
