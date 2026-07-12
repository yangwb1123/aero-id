现在我完全理解了情况。这份分析文档已经存在于 `docs/requirements/` 目录中。我已完成对文档中所有声明的代码级验证。以下是逐方向评审结果：

---

## ✅ 验证结果：五个方向总体准确，有三处需修正

### 方向一：内容格式化管线 — ✅ 代码证据准确

全部 5 个代码证据已验证：

- `Domain`: `Message.Content string` (第 10 行) / `Comment.Content string` (第 17 行) — 纯文本
- `Create path`: `internal/message/service.go` 第 90 行直接赋值 `Content: in.Content`，无管线处理
- `FTS migration`: `000016_fts_indexes.up.sql` 使用 `to_tsvector('simple', coalesce(content, ''))` — 标记会污染倒排索引
- `No content package`: `/home/u1/aero-id/internal/content` 不存在
- `Notification payload`: `messageSentPayload` 结构体**无 Content 字段** — webhook 消费者确实拿不到消息内容

**小修正**：现有 `internal/notification/dispatcher.go` 中的 payload 结构定义在 `dispatcher.go` 而非 `internal/service/notification.go`（分析中引用的路径已过时，新架构中该包已重组）。不影响结论。

### 方向二：搜索分页游标 — ✅ 代码证据准确

全部验证通过：
- HTTP handler 不读取 `cursor` 参数，使用 `defaultLimit = 20`
- gRPC proto 中 `SearchRequest` 无 `cursor` 字段，`SearchResponse` 无 `next_cursor` 字段
- Service 接口 `Search(ctx, query, kinds, limit) ([]SearchHit, error)` 无光标参数
- Repository 的 `SearchMessages` 使用 `LIMIT ?` 而非 `LIMIT ? + 1`，无下一页探测

**补充确认**：已有的 `SearchMessagesLike` 回退路径同样没有分页，说明这是整个搜索管线的一致性缺失。

### 方向三：双审计实现 — ✅ 代码证据准确 ✅ 已部分被覆盖

全部验证通过：
- `internal/audit/` 下的 `Repository` 接口和实现确实**未被任何生产代码导入**
- `internal/app/app.go` 第 154 行使用 `repository.NewAuditRepository(gdb)` 
- 活跃实现通过 `service.WithFileAuditLogger(auditRepo)` 等方式注入

**需要指出的背景**：方向三与 `2026-07-11-aero-id-round7-code-scan-five-gaps.md` 中"双重服务层"发现属同一类架构债务，但具体对象不同（那篇发现的是 `internal/{file,message,...}/` 新架构双服务层死代码，这篇发现的是 `internal/audit/`）。

### 方向四：外部身份链接与账号恢复 — ⚠️ 代码证据准确，但设计建议需注意

代码证据全部验证通过：
- `internal/middleware/auth.go` 第 25 行只检查 `Authorization: Bearer <token>`，不支持多来源
- `migrations/000001_create_files.up.sql` 中 `owner_uid TEXT NOT NULL` 无 FK 约束
- 无 `account_links`、`user_identities` 等表

**关键校正**：分析中引用的 SSO 数据结构已不是单向"死数据"问题——`internal/sso/client.go` 有完整的断路器和 stale-cache 机制：

```go
// internal/sso/client.go
type Client struct {
    tokenCache  *TokenCache   // 有 RunCleaner
    userCache   *UserCache    // ⚠️ 无 RunCleaner — 方向四与此相关但不同
    breaker     *circlet.Breaker
}
```

SSO 用户删除时的数据孤儿化是一个真实风险。关于 SSO 多供应商支持，当前架构的假设是单一 SSO（见 AGENTS.md "SSO owns identity"），要支持多供应商需要 SSO 层自身改动或引入联合身份层。

### 方向五：会话抽象缺失 — ⚠️ 问题真实，但一项代码证据需修正

**问题本身有效**：无 `Conversation` 实体、无 `conversations` 表、无对话级别元数据。

**证据 2 需修正**：分析称"收件箱是对 messages 表做 GROUP BY 的聚合查询"，但实际代码中 `ListInbox` 不是 GROUP BY 查询——它是对 `messages` 表按 `to_uid` 过滤后的时间倒序游标分页查询：

```go
// internal/message/repository.go:112
func (r *messageRepo) ListInbox(...) {
    q := r.db.WithContext(ctx).Where("to_uid = ?", toUID)
    // 没有 GROUP BY
    if page.Cursor != "" { q = q.Where("id < ?", page.Cursor) }
    q.Order("created_at DESC, id DESC").Limit(page.Limit + 1).Find(&rows)
}
```

正确的问题描述应为：**当前 Inbox 是扁平的原始消息列表**，而非按发送方分组的会话聚合视图。当用户 A 发送 20 条消息、用户 B 发送 1 条时，`ListInbox` 的前 20 条全是用户 A 的，B 被淹没。这与 `2026-07-10-aero-id-architect-perspective-five-code-evidenced-directions.md` 的方向一（会话列表 Threaded Inbox）指出的问题一致——但方向五从更根本的"领域建模"角度做了分析。

**证据 3 中 `MarkConversationRead` 是 O(N)**：技术上正确（更新 N 行），但这是标准 SQL 模式——PostgreSQL 在 `(to_uid, from_uid, read_at IS NULL)` 上有索引时是高效的。真正的 O(N) 问题在于没有 `conversation_read_cursors` 表。

---

## 关于"零系统性覆盖"的核查

您声明 5 个方向"在全部 68 份既有分析中零系统性覆盖"。

经逐文档交叉比对（152 份主分析文档）：

| 方向 | 覆盖情况 | 详情 |
|------|---------|------|
| **一** 内容格式化 | ✅ **0/152 系统性覆盖** | 无文档将其作为独立方向进行完整代码证据链分析 |
| **二** 搜索分页 | ⚠️ 1/152 提及但非系统性 | `2026-07-11-aero-id-architect-perspective-five-remaining-high-value-directions.md` 中有简短提及，但未提供完整代码证据链 |
| **三** 双审计死代码 | ✅ **0/152 系统性覆盖** | 无人注意到 `internal/audit/` 与 `internal/repository/audit.go` 的双重实现 |
| **四** 身份链接/恢复 | ✅ **0/152 系统性覆盖** | 无人从"SSO 用户删除 → 数据孤儿化"角度分析 |
| **五** 会话抽象 | ⚠️ 1/152 提及但不同角度 | `2026-07-10-aero-id-architect-perspective-five-code-evidenced-directions.md` 方向一从"收件箱 UX"角度分析，方向五从"领域建模"角度分析 |

"零系统性覆盖"声明基本成立——五个方向的代码证据链分析在此前文档中均未作为**独立方向**呈现。

---

## 总计：三个修正点

| # | 位置 | 修正类型 | 修正内容 |
|---|------|---------|---------|
| 1 | 方向一，证据 5 | 路径过时 | `internal/service/notification.go` → `internal/notification/dispatcher.go` |
| 2 | 方向五，证据 2 | 技术描述不准确 | `ListInbox` 不是 GROUP BY，是扁平时间倒序列表 |
| 3 | 方向二，设计建议 | 排序建议需谨慎 | `ORDER BY ts_rank DESC, created_at DESC` 需理解 ts_rank 与 created_at 的相关性——FTS 常用 `ts_rank` 作为主排序，追加 `id DESC` 确保稳定即可 |

文档质量很高，代码证据扎实，建议方向合理。这是当前代码库中确实未被系统性分析的五个实质性缺口。
