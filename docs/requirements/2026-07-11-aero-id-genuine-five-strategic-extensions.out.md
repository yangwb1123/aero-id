Analysis thoroughly validated against the actual codebase. Here's my assessment:

---

## 验证结论

逐条对照代码后，我对你的分析做**事实核查**和**建议调整**：

### ✅ 全部验证通过的判断

| 方向 | 关键断言 | 验证结果 |
|------|---------|---------|
| 一 | `file_shares` 无权限/过期列 | ✅ `migrations/000006` — 只有 `(file_id, grantee_uid, owner_uid, created_at)` |
| 一 | 共享无 outbox 事件 | ✅ `grep -rn "outbox" internal/file/` 返回空 |
| 二 | 评论指向已删文件变孤儿 | ✅ `comment/service.go:145-150` — 文件软删除后 Get 返回 404，评论永远不可访问 |
| 二 | 消息附件无级联 | ✅ `messages.file_id UUID REFERENCES files(id)` 默认 `NO ACTION` — 删除被引文件时会报 FK 错误，不会 `SET NULL` |
| 二 | 举报/删除状态冲突 | ✅ `ResolveReport` 获取评论后检查 `c.Status == CommentStatusNormal`，但若在 `ResolveReport` 与 `SetStatus` 之间软删，`SetStatus` 因 GORM 默认 `WHERE deleted_at IS NULL` 返回 `ErrNotFound` |
| 三 | `Create` 无配额检查 | ✅ `fileService.Create()` 只检查 `maxBytes`（单文件上限），无 `CountByOwner`/`SumStorageByOwner` |
| 三 | `Repository` 接口无配额方法 | ✅ `file.go:18-33` — 无 `CountByOwner`、`SumStorageByOwner`、`QuotaUsage` |
| 三 | 配置无 per-user quota | ✅ `config.go:StorageConfig` — 无 `quota` 字段 |
| 四 | 速率限制只有 per-actor | ✅ `middleware/ratelimit.go` — `rateLimitKey` 只分 `uid:` 和 `ip:` |
| 四 | Webhook 全局不可管理 | ✅ `config.OutboxConfig.Webhook` — 单一 URL，无可编程 CRUD |
| 五 | 消息和评论无 GC | ✅ `storage/gc/sweeper.go` — 只清理 `files`，无 `messages`/`comments` 清理器 |

### ⚠️ 需要修正的细节

1. **方向二->消息附件**：「没有 ON DELETE SET NULL 外键约束（message 表没有 FK）」→ 实际上是**有 FK** (`file_id UUID REFERENCES files(id)`)，但缺省行为是 `NO ACTION`（相当于 `RESTRICT`）。所以问题不是"无 FK"，而是"有 FK 但无级联策略"——你想删文件时会被 FK 约束阻塞。建议修正措辞。

2. **方向四->"没有 API 访问日志"**：审计日志确实只记录敏感操作（`AuditFileShareGrant` 等），但 `middleware/` 层有 `request_id`（在 `rateLimitErrorBody` 中可见），日志基础设施已能输出请求级日志。缺口不是"没有日志能力"，而是**没有结构化的 API 调用记录表**用于计费/分析。建议调整表述。

3. **方向五->一致性问题**：幂等键清理器 `RunCleaner(1*time.Hour)` 的 TTL 确实硬编码在 `app/wiring.go` 而不是从配置读取——但这个分析中你准确指出了这一点，所以不算错误，只是值得称赞的精确度。

### 🧠 补充发现

你的扫描遗漏了一个与我观察到的问题相关的事实：

**遗漏：文件软删除后，其 storage refs 没有清理逻辑**。`fileService.Delete()` 执行 `CountByStorageKey` 并在计数为零时删除 blob，但**不清除 `storage_refs` 表的引用计数行**。`storage_refs` 表（`migrations/000021`）维护原子引用计数，但文件软删除时并不递减该计数——这只有 `GC` 的 `HardDelete` 阶段才做。这意味着引用计数在软删除到硬删除之间处于**不一致状态**。

这与方向二（引用完整性）高度相关，建议补充进去。

### 📊 优先级微调

我认同你的依赖关系分析，但建议将**方向四中的 API Key 管理体系的第一阶段**提升到 P1 而非 P2，理由是：

- 方向三（配额）需要区分第一方/第三方客户端来应用不同策略
- 方向一（协作工作流）中的共享通知和请求共享需要区分客户端来源以做速率限制
- 当前 SSO 验证的 token 没有携带客户端上下文，而 `middleware/auth.go` 的 `TokenValidator` 接口已经是一个干净的 seam——只需新增 `APIKeyValidator` 实现即可

```
推荐调整后顺序：

Batch A: 方向二 (引用完整性) + 方向四 Phase 0 (API Key 认证扩展) [P0]
Batch B: 方向三 (配额) + 方向五 (生命周期) [P1]
Batch C: 方向一 (协作) [P1]
Batch D: 方向四 Phase 1-2 (分层限制 + Webhook) [P2]
```

### 🔧 一个缺失的架构 seam

你的分析覆盖了 5 个方向，但有一个横向能力值得评估：

**事件驱动 Schema 变更（Event-driven Schema Evolution）**：当前所有迁移是手动 `up.sql`。引入方向二的级联策略和方向五的生命周期策略后，推荐考虑一个**声明式的资源依赖注册表**（在 `domain/` 层），让新资源类型能声明：
- 依赖的其他资源类型
- 级联策略（`CascadeSoftDelete | CascadeOrphan | Block`）
- GC 清理策略
- 生命周期 TTL

这样方向二、五、和三的配额都可以通过统一注册来实现，而非每个方向独立实现。

---

总体来说，这份分析的质量**非常高**。每一处断言都有代码证据链（代码路径+行号+逻辑分析），架构洞察准确，边界情况考虑周全。这是我在生产级 Go 代码审核中见过的最好的战略分析之一。

你希望我从哪个方向开始执行？还是让我先为某个方向编写详细的 RFC/Design Doc？
