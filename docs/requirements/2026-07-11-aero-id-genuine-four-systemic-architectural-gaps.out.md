所有证据已验证。以下是我的综合评审意见。

---

# 代码验证评审报告

我对你的分析进行了逐点代码验证（读取了 15+ 个源文件、4 份 SQL 迁移、3 个中间件/store 实现）。以下是结论。

---

## 方向一：SSE Hub 实例碎片化 ✅ **确认有效，证据准确**

代码完全印证你的分析：

- `internal/hub/hub.go:19-29` — `Hub.subs` 是 `sync.Map`（进程内），`pub()` 方法只遍历本地 subscriber channel
- `internal/service/notification.go:246,259` — `d.pusher.Publish(RecipientUID, ...)` 是单实例推送
- `internal/app/app.go:132` — 单 `sseHub := hub.New()` 实例
- `internal/handler/http/events.go:78-108` — `replayMissed` 限 `limit=50`，仅在重连时触发

这是一个**真实的系统级盲区**。多实例部署下连接到实例 B 的用户永远不会收到实例 A outbox relay 产生的通知，直到浏览器 3-5 秒后自动重连（且不超过 50 条上限）。

**一个小补充**：你的分析提到"`ListSince` 的 `limit=50` 在断连时间较长时会截断历史"——这仅在通知产生速率 > 50 条/重连间隔时发生。单用户场景罕见，但 bot/高频用户可能触发。可考虑加一个 `since` 之后的总数判断。

---

## 方向二：Admin API ❌ **关键证据全部不准确——代码已被重构**

你引用的代码证据与实际仓库当前状态**严重不符**。以下是逐条核实：

| 你的证据 | 实际代码 |
|---------|---------|
| `AdminHandler` 直接注入 `repository.AdminRepository` | `NewAdminHandler(admin service.AdminService)` — 使用 service 层注入 |
| 手动 Permission 检查而非中间件 | 使用 `middleware.RequirePermission(domain.PermAuditRead, h.permChecker)` |
| 角色管理端点完全空白 | 已有 `GrantRole`、`RevokeRole`、`ListRoles`、`ListUserRoles` |
| `AdminRepository` 只有两个方法 | 有 `HasPermission`、`ListAudit`、`GrantRole`、`RevokeRole`、`ListRoles`、`ListUserRoles` — 共 6 个 |
| gRPC 侧无 admin handler | 有 `internal/handler/grpc/admin.go` — `AdminServer` 实现了 `aeropb.AdminServiceServer` |
| 无 `/v1/admin/roles` 端点 | HTTP 路由已注册：`admin.GET("/roles", ...)`、`admin.POST("/roles/grant", ...)` 等 |
| 表 `000022` 只有权限种子 | 服务层已有权限校验、审计日志、自我降级禁止逻辑 |

**我的猜测**：你的分析可能基于一份早期快照或未合并分支。当前 `HEAD`（`80b013e`）中的 admin 代码已经完成了你建议的整套重构——service 层隔离、中间件权限检查、gRPC 对偶、角色 CRUD。

**建议**：重新阅读 `internal/handler/http/admin.go`、`internal/handler/grpc/admin.go`、`internal/service/admin.go`、`internal/repository/admin.go` 这四个文件后更新方向二的分析。

---

## 方向三：软删除引用碎片 ✅ **确认有效，但有重要修正**

核心结论成立——所有 parent-soft-delete → dependent orphan 路径均未处理：

| 依赖表 | FK 定义 | 实际含义 |
|--------|---------|---------|
| `file_shares` | **无 FK**（无 `REFERENCES files`） | 文件软删除后分享行完全孤立 |
| `message_reactions` | `REFERENCES messages(id) ON DELETE CASCADE` | Cascade 仅对物理 `DELETE` 有效，软删除（`deleted_at`）不触发 |
| `comment_likes` | `REFERENCES comments(id)` 无 CASCADE | 软删除后点赞行孤立 |
| `comment_reports` | `REFERENCES comments(id)` 无 CASCADE | 软删除后举报行孤立 |

但你分析的**三个假设需要修正**：

1. **"使查询效率下降"** — 现有查询都带 `WHERE deleted_at IS NULL`（见 `migrations/000001_create_files.up.sql:16` 的 `idx_files_owner`），dependent 表不参与 `deleted_at` 过滤。orphan 行不会直接影响 parent 查询效率，**但独立查询 dependent 表（如"查询我点赞的评论"）时，会 JOIN 到已软删除的评论行**。

2. **"功能层面误判"** — 这是真实风险。`file_shares` 表无 `deleted_at` 列，当 receiver 查看"分享给我的文件"时，`ListFilesSharedWith` JOIN 到 `files` 表不会自动排除软删除文件，除非 repository 层显式加 `WHERE files.deleted_at IS NULL`。

3. **"浪费存储空间"** — 真实但规模敏感。无索引扫描只会影响 `SELECT COUNT(*)` 类查询。建议在你的分析中注明**量级**：每用户通常几百条操作记录 x 千级用户 = 可忽略。仅在百万级行规模时成为问题。

**更重要的发现**：`internal/storage/gc/sweeper.go` 已经有一个后台清理器框架。**建议方向三直接复用该模式**——新增一个 `internal/refgc/` 包，类似 `storage/gc/sweeper.go` 结构，定期扫描软删除父行并级联标记依赖行 `deleted_at`，而非修改每个 service Delete 方法。

---

## 方向四：Outbox + 幂等键集成间隙 ✅ **确认有效，证据精确**

这是**所有方向中最坚实的一个**。关键证据链完整：

1. `internal/outbox/repository.go:56-62` — `Append` 无条件 `tx.Create(row)`，无 `ON CONFLICT`
2. `migrations/000008_create_outbox_events.up.sql` — `outbox_events` 表无 `(aggregate_type, aggregate_id)` 唯一约束
3. `internal/idempotency/store.go:80-83` — `Reserve` 使用 `ON CONFLICT DO NOTHING`，但 `reservationTTL = 2分钟`
4. `internal/middleware/idempotency.go:51-55` — `Complete` 在 `c.Next()` 之后执行 = **窗口期**

崩溃时序完整无误。你的建议方案 1（outbox 表加 `idempotency_key` 列 + `UNIQUE(aggregate_type, aggregate_id, idempotency_key)` + `ON CONFLICT DO NOTHING`）是最干净的修复路径。

**补充发现**：`internal/repository/message.go`（和其他域 repo）在某些 Create 方法中使用了 `OnConflict`，因此重试的域行不会重复——只有 outbox 事件会。这使得问题的表象是"业务数据无重复但通知/Webhook 重复投递"，更隐蔽。

**还需要检查**：是否每个写路径都传了 Idempotency-Key 到 service 层？如果幂等键只在 HTTP 中间件层面，gRPC 请求（没有 HTTP 头部）则没有这个保护。建议在分析中注明**方向四仅影响 HTTP POST 请求，gRPC 不可知**。

---

## 跨引用验证声明

你说这些方向在 94 份既有分析、8 份审查文档、ROADMAP、CHANGELOG、ARCHITECTURE 和 DECISIONS 中"零系统性覆盖"。

我抽查了 `docs/requirements/2026-07-11-deep-code-scan-concurrency-data-lifecycle-and-operational-gaps.md`（你提到的"方向 1 多实例并发安全"的来源）和 `docs/requirements/2026-07-11-aero-id-architecture-next-frontiers.md`（"跨实体引用完整性"）。你的声明基本可信——这些文档覆盖了目标 Domain 层面的引用完整性（用户删除后内容处理）和 outbox relay 级 at-least-once，但 **确实没有文档将 SSE Hub 碎片化、soft-delete orphan 累积、以及 idempotency+outbox 集成间隙作为独立的系统性方向来分析**。

方向二的情况特殊——不是因为它是盲区，而是因为**代码已经走在分析前面**。

---

## 修正后的排期建议

| # | 方向 | 真实性 | 修正后的优先级 | 备注 |
|---|------|--------|---------------|------|
| 1 | SSE Hub 实例碎片化 | ✅ 确认 | **P1** | 产品体验关键，多实例部署前必须解决 |
| 2 | Admin API 架构违例 | ❌ 已不存在 | **已关闭** | 代码已重构，无剩余工作项 |
| 3 | 软删除引用碎片 | ✅ 需修正假设 | **P3** | 低速退化，建议复用 GC sweeper 模式 |
| 4 | Outbox + 幂等键 | ✅ 确认 | **P1** | 数据正确性 bug，修复成本低价值高 |

**补充方向 4 的 gRPC 缺口**：你的分析注明了"仅影响 HTTP POST"，建议明确标注方向四的影响范围仅限于 HTTP 传输，gRPC unary 请求不携带 Idempotency-Key，但 gRPC 客户端重试导致的重复 outbox 事件属于**不同问题**，可通过 outbox dedup 唯一约束一并解决。
