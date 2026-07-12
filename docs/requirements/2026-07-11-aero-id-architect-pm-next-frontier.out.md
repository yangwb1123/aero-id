This 文档已存在于 `docs/requirements/2026-07-11-aero-id-architect-pm-next-frontier.md`（772 行）。我已完成全量代码库交叉验证，以下是核查结果：

---

## ✅ 验证确认

### 方向一：级联生命周期 — ✅ 核心论据成立

- **File.Delete**（`internal/service/file.go:224`）— 确认：仅软删 + ref decrement，**无 file_shares 清理、无 notifications 处理**
- **Message.Delete**（`internal/service/message.go:285`）— 确认：仅软删 + 审计日志，**无 comments/notifications 级联**
- **Comment.Delete**（`internal/service/comment.go:214`）— 确认：仅软删，**无 comment_likes/comment_reports 清理**
- `migrations/` 中唯一含 `ON DELETE CASCADE` 的表是 `message_reactions`（`000013`）

### 方向二：幂等结果查询 — ✅ 核心论据成立，但 API 描述过时

当前 `internal/idempotency/store.go` 的接口是 `Reserve` / `Complete` / `Release`，**不是**文档引用的 `Get` / `Set`。但结论一样——没有任何查询端点暴露给客户端。

### 方向三：自适应限流 — ✅ 核心论据成立

`internal/pkg/ratelimit/ratelimit.go` 确认：纯静态 token bucket，`Allow(key) bool`，无优先级、无负载感知、无全局信号收集。

### 方向四：通知 Timeline 语义 — ✅ 核心论据成立

`internal/domain/notification.go` 确认：仅 `ReadAt *time.Time`、无 `ArchivedAt` / `SubjectPreview` / `SubjectStatus` / `ActorNickname`。

---

## ⚠️ 发现 3 处事实偏差

| # | 文档原文 | 代码实际 | 影响 |
|---|---------|---------|------|
| 1 | "`idempotency/store.go` 含 `Get`、`Set`、`RunCleaner`" | 实际为 `Reserve`、`Complete`、`Release`、`RunCleaner`（中间件也不调 `s.Get`，而是 `s.Reserve`） | 低 — 不影响核心论点，但证据 1/2 的代码截图需更新 |
| 2 | "`repository/file_share.go` 中 `DeleteAllByFile` 方法存在但无人调用" | 该方法 **不存在**。`fileShareRepo` 只有 `Grant`/`Revoke`/`ListGrantees`/`ListFilesSharedWith`/`IsSharedWith` | 低 — 缺失级联清理的结论不变，只是待补方法的命名需确认 |
| 3 | "`messages` 表的 `file_id FK` 是 `NO ACTION`" | 读 `migrations/000002_create_messages.up.sql` — 确认为 `NO ACTION`（PG 默认） | ✅ 无误 |

---

## 📊 与既有文档的交叉比对

文档声称 4 个方向均"零系统性覆盖"，但核查发现部分论题已有触及：

| 方向 | 已有文档 | 覆盖程度 |
|------|---------|---------|
| 级联生命周期 | `genuine-four-systemic-architectural-gaps.md`（方向一）、`five-genuine-code-evidenced-architectural-gaps.md`（点 4） | **部分覆盖** — 前者聚焦"硬删除后孤儿实体"，文档的切入点是"软删除后引用碎片"，确有不同 |
| 幂等结果查询 | `architect-pm-next-frontier.md 方向二` — **就是本文档自身** | N/A |
| 自适应限流 | `architect-pm-specific-code-evidenced-gaps.md`（点 5）、`round5-cross-analysis.md` | 附带提及，未系统性分析 |
| 通知 Timeline | `senior-architect-pm-five-unsystematically-covered-high-value-extensions.md`（点 3） | 附带提及归档能力，未系统性分析消费语义 |

"零系统性覆盖"的声明基本成立——已有文档是零散触及而非系统性分析。建议在文档中加一句区分，说明方向二在该文件中是首次完整展开。

---

## 🛠 下一步

文档质量高、建议策略合理。需要我做什么？

1. **修正并保存** — 修正 `idempotency/store.go` API 描述 + `DeleteAllByFile` 不存在的问题，确认是否覆盖或另存为新文件？
2. **开始实施方向一 Phase 1** — 在 file/message/comment 的 `Delete` 方法中增加级联清理逻辑
3. **开始实施方向二 Phase 1** — 新增 `GET /v1/_/idempotency/:key` 端点和 gRPC `GetIdempotencyResult` RPC
4. **分析/扩展任一方向** — 深入具体实现路径
5. **不做任何事** — 只是确认文档准确性
