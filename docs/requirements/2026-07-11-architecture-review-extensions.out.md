现在我有了完整的图景。以下是我对这份架构审核文档本身的 **验证审核**。

---

## 审核验证结果

我已对照 `main` 分支上的实际源代码验证了文档中的所有事实主张。以下是发现。

### ✅ 已确认准确的声明

| 声明 | 来源 | 代码证据 |
|------|------|----------|
| GORM OTel 插件缺失 | 附录 #1 | `internal/pkg/db/db.go` — 只有基本 GORM 配置，无 `opentelemetry.Plugin` |
| GC `Start` 使用 `context.Background()` | 附录 #2 | `internal/storage/gc/sweeper.go:213` — `ctx := context.Background()` |
| SSE heartbeat 硬编码 30s | 附录 #5 | `internal/handler/http/events.go:71` — `ticker := time.NewTicker(30 * time.Second)` |
| 速率限制为 per-instance | 方向 #2 | `internal/pkg/ratelimit/ratelimit.go:16` — 包注释：*"PER-INSTANCE: each running aero-id process keeps its own buckets in memory"* |
| SSO token/user 缓存为内存型 | 方向 #2 | `internal/sso/token_cache.go` / `user_cache.go` — 均为 `map[string]...` + `sync.RWMutex` |
| `PermUserBan` 已定义但未实现 | 附录 #4 | `internal/domain/admin.go:9` — 常量存在，但 grep 确认在业务代码中从未使用 |
| 无 `message_reports` 表 | 方向 #4 | 在 23 个迁移中或 `internal/` 中均未找到 |
| Hub.Publish 签名包含 eventID | 附录 #3 | `internal/hub/adapter.go:10` — `func (h *Hub) Publish(uid, eventType, data string, eventID int64)` |

### ❌ 事实错误

**1. 附录 #4：「管理端点仅有 `/v1/admin/audit-log`」**

这是 **错误的**。我在 `internal/handler/http/admin.go` 中发现以下已实现的路由：

| 端点 | 所需权限 | 状态 |
|--------|-------------------|--------|
| `GET /admin/audit-log` | `PermAuditRead` | ✅ 已在文档中引用 |
| `GET /admin/roles` | `PermAuditRead` | ❌ 文档遗漏 |
| `GET /admin/roles/:uid` | `PermAuditRead` | ❌ 文档遗漏 |
| `POST /admin/roles/grant` | `PermSystemConfig` | ❌ 文档中声称不存在 |
| `POST /admin/roles/revoke` | `PermSystemConfig` | ❌ 文档中声称不存在 |

因此，文档所说的*「没有分配/撤销角色的端点」*与实际情况不符——这两个端点都已完全实现，包括审计日志（`GrantRole` 中的 `if s.audit != nil` 分支）。

文档中关于缺少*「列出所有用户的端点」*的说法也需要限定范围：`GET /admin/roles/:uid` 确实用于按 UID 查询角色，尽管不存在通用的 `GET /admin/users` 端点。

**2. 附录 #6：「notification_preferences 的 timezone 列目前仅在表中定义」**

这是 **不准确的**。`IsAllowed` 方法（`internal/service/preference.go:82-106`）**已经**通过 `time.LoadLocation(p.Timezone)` 使用 `Timezone` 字段来计算静默时段：

```go
loc, err := time.LoadLocation(p.Timezone)
if err != nil {
    loc = time.UTC
}
now := time.Now().In(loc)
```

时区配置在 `Set`（第 55 行）中被默认设为 `"UTC"`，并且静默时段计算也正确处理了跨越午夜的情况。此功能是完整的。

**3. 方向 #4：评论审核的描述不够完善**

文档将评论审核描述为纯手动/被动方式，但代码显示：
- 已存在 `comment_reports` 表（迁移 000007），包含 `(comment_id, reporter_uid)` 的唯一约束
- 迁移 000010 添加了完整的报告队列生命周期：`status`（1=open / 2=resolved / 3=dismissed）、`resolved_at`、`resolved_by`
- `internal/comment/service.go` 处理 `ResolveReport` / `DismissReport`

因此，审核队列机制已经存在——缺少的是 **自动扫描**（病毒/NSFW/垃圾内容），文档正确地将其确定为缺失部分。

### ⚠️ 在附录中值得注意但缺失的改进点

文档的附录做得很好。以下是我在审核过程中发现的、可以添加的补充内容：

1. **`PermUserBan` 已定义但完全未使用** — 已验证。该常量存在于 `domain/admin.go` 中，但没有任何代码引用它，也没有相关端点、仓库方法或表。这是一个已声明但未实现的功能的真实案例。

2. **`internal/service/file_presign.go` 存在但仅在本地存储时有效** — S3 设置中下载器为 nil（`app.go:265`）。此项可能已知，但值得在附录中提及。

3. **配置项 `database.auto_migrate` 在生产环境中关闭，但在启动时可用**（`app.go:83-104`）——这与 AGENTS.md 中*「迁移不是自动执行的」*的声明形成对比，但实际上当 `auto_migrate: true` 时是自动执行的。一个小小的文档矛盾。

### 关于方向优先级建议的总体评价

批次顺序大体合理，不过基于代码分析，我会进行以下调整：

**批次 A — 按原计划进行：**
- 方向 5（可观测性）—— ✅ 低投入高回报，完全不依赖其他方向
- 方向 4 的子集（用户封禁 API + `message_reports`）—— ✅ 但**注意**：用户封禁会触及比预期更多的代码（需要路径创建、文件上传、消息发送的写入拦截；而当前没有 `user_bans` 表）

**批次 B 需要考虑的工作量：**
- 文件版本管理（方向 3）的工作量被低估为「中等」——需要 2-3 个新表、5+ 个新端点、GC 适配、共享语义决策。**更接近中高工作量。**

**批次 C 的依赖关系说明：**
- 方向 1（工作区）与方向 4（审核）之间的依赖关系比文档所承认的更弱——工作区权限模式是独立的，不需要审核管道。可以安全地并行进行。

文档是一份扎实的分析报告，上述更正后即可付诸实施。
