以下是关于这份文档在我逐行验证代码库后的**架构评审**。

---

## 综合验证结果

| 方向 | 文档准确性 | 代码级确认 |
|------|-----------|-----------|
| **方向一**：死代码 | ✅ **准确**（发现稍微低估了） | `internal/notification/` 完全未被外部引用 |
| **方向二**：通知内容上下文 | ✅ **准确** | `domain.Notification` 确实缺少预览/摘要字段 |
| **方向三**：通知折叠粒度 | ✅ **准确** | 折叠逻辑丢弃事件内容，仅保留计数 |
| **方向四**：屏蔽级联至文件分享 | ✅ **准确** | `fileService` 结构体中完全没有 `BlockChecker`；`canRead()` 未检查屏蔽关系 |
| **方向五**：搜索排序 | ⚠️ **部分不准确** | 主搜索路径**已经使用** `ts_rank`；ILIKE 仅作为 CJK 回退；但跨实体排序确实缺失 |

---

## 逐项详细分析

### 方向一：死代码 — 文档说的对，但还漏了一处

**确认**：`internal/notification/` 下 1516 行代码（477 行非测试代码 + 1039 行测试代码）完全死代码。

证据汇总：
- `app.go` 接入的是 `service.NewNotificationDispatcher`、`service.NewNotificationService`、`internal/handler/http/notification.go` 和 `internal/handler/grpc/notification.go` — 全部来自 `service/` + `handler/` + `repository/` 路径，**没有**来自 `internal/notification/`
- `grep` 显示 `internal/notification/` 有**零**个非测试引用
- 死代码中的 `dispatcher.go` 是一个早期的简化版，缺少 `PreferenceChecker`、`notifRateLimiter` 和 `collapseWin`

**额外发现**：`internal/search/`（195 行非测试代码 + 348 行测试代码 = 543 行）**也是死代码**。与 notification 完全相同的模式：

| 路径 | 行数 | 在生产中运行？ |
|------|------|:-----------:|
| `internal/search/search.go` | 100 | ❌ |
| `internal/search/handler_http.go` | 42 | ❌ |
| `internal/search/handler_grpc.go` | 48 | ❌ |
| 测试文件 | 348 | ❌ |
| **总数** | **~538** | **全部未运行** |

生产路径是 `service.NewSearchService()`（调用 `repository.*`），`internal/service/search.go`（一个更完善的版本，支持 `containsCJK()` 回退逻辑）。`internal/search/` 从未被导入——只有它自己的测试引用它。

**修正建议**：将方向一重写为"两处死代码包"，估计清理工作量翻倍（2 天，`internal/notification/` + `internal/search/` 都需要清理）。

---

### 方向二：通知内容上下文 — 准确

`domain.Notification` 确实不包含：
| 缺失字段 | 确认 |
|----------|:----:|
| `subject_preview` | ✅ 确认 |
| `summary_text` | ✅ 确认 |
| `subject_url` | ✅ 确认 |

Listing 端点在 `internal/service/notification.go` 的 `NotificationService.List()` 中返回 `(notifications, next_cursor, users)`，其中 users map 仅解决 actor 查找问题，subject 内容仍为黑箱。

**数据导向的修复建议**：`ALTER TABLE notifications ADD COLUMN subject_preview TEXT` 的估算合理。一个额外的优化：当 subject 已被删除时，将 `subject_preview` 设置为 `"[deleted]"`，这样客户端无需处理 404。

---

### 方向三：通知折叠 — 准确

`internal/service/notification.go` 第 234 行的 collapse 逻辑是：
```go
existing, err := d.notifs.FindRecentByTypeAndActor(ctx, n.RecipientUID, n.Type, n.ActorUID, d.collapseWin)
if err == nil && existing != nil && existing.ID != n.ID {
    _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
    return nil  // 新通知行已创建但被静默丢弃
}
```

文档中的信息丢失场景是真实的。然而，关于"已读折叠"的边界情况（T=3 时用户读取 N1，T=4 时 Alice 发送消息 → 新通知行 N2）对用户实际上表现良好——用户确实会看到新消息，只是永远不知道它之前的消息 "在吗？"。

实现方向（`collapsed_events JSONB`）是合理的。

---

### 方向四：屏蔽未级联至文件分享 — 确认，但有一个重要的边界情况

**确认**：`fileService` 结构体（在 `internal/service/file.go` 中）完全没有 `BlockChecker` 字段。`canRead()` 只检查：
1. `domain.CanReadFile(actor, f)` → 检查 `VisibilityPublic` 或 `OwnerUID == actor`
2. `s.shares.IsSharedWith(ctx, f.ID, actor)` → 检查显式分享

相比之下，`messageService` 有：
```go
blocks BlockChecker // nil => block guard is inactive
```

而 `notificationDispatcher` 有：
```go
blocks BlockChecker // nil => block suppression is inactive
```

**重要边界情况**：方向性屏蔽很重要。如果 A 屏蔽 B（A→B 已屏蔽），但 B **未**屏蔽 A，那么：
- 被分享给 B 的 A 的文件应被 B 拒绝访问 ✅（文档提到）
- 被分享给 A 的 B 的文件应**被 A 允许**访问 ❌（文档**没提到这个方向性**）
- 通过 `visibility == public` 而非分享访问的文件是否应屏蔽？可能应屏蔽，但这会产生兼容性影响

修复的时间估算（2-3 天）是合理的。`fileService` 需要：
1. 一个 `WithFileBlockChecker(blocks BlockChecker)` 选项（与 message service 模式一致）
2. 在 `canRead()` 中增加检查：在 actor ≠ owner 时，`blocks.IsBlocked(blocker=actor, blocked=owner)`（我正试图读取属于你的文件，你是否屏蔽了我？）

---

### 方向五：搜索排序 — 需要在文档中更正

这是唯一有重大事实错误的方向。

**文档的证据 1 称**："Repository 层使用 ILIKE 而非 ts_rank"。
**实际代码**：

```go
// internal/repository/message.go:255-275
func (r *messageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    err := r.db.WithContext(ctx).Raw(
        `SELECT ... FROM messages
         WHERE deleted_at IS NULL
           AND (from_uid = ? OR to_uid = ?)
           AND search_vector @@ plainto_tsquery('simple', ?)
         ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC  ← 已使用 ts_rank!
         LIMIT ?`,
        uid, uid, query, query, limit,
    ).Scan(&rows).Error
```

三个实体类型都**已经**在 `ORDER BY ts_rank(...) DESC`，而不是 `created_at DESC`。ILIKE 仅在 `SearchMessagesLike` 中使用，这是仅用于 CJK 回退的独立方法（路径由 `internal/service/search.go` 中的 `containsCJK()` 处理）。

**仍然正确的文档部分**：
- ❌ "Repository 层使用 ILIKE 而非 ts_rank" — 这是**错误的**；主路径使用 `ts_rank`
- ❌ 代码块中引用了 `Order("created_at DESC")` — 这是 `SearchMessagesLike` 回退的代码，不是主搜索路径
- ✅ "跨实体排序不存在" — 确实，搜索结果按种类分组且迭代进行
- ✅ "ILIKE 回退搜索性能差" — 正确，但这仅适用于回退（CJK）
- ✅ "无高亮片段（`ts_headline`）" — 正确，未使用

**修正建议**：重写方向五，关注实际存在的真正问题：
1. **跨实体相关性排序**：没有 `UNION ALL`；每种实体独立搜索 `limit` 条结果，没有总体排序
2. **`ts_headline`** 缺失：返回的搜索结果不包含可渲染的摘要
3. **CJK 回退性能**：`ILIKE '%query%'` 是全表扫描（确实如此），但只在明确限定的 CJK 场景

相关性排序的估算（3-5 天）是合理的，但如果只是添加 `UNION ALL` + `ts_rank` 跨实体排序，实际可能接近 2-3 天。

---

## 总结

| 方向 | 准确性 | 关键修正 |
|------|--------|---------|
| 1 | ✅ 正确但低估 | `internal/search/` 处还有 538 行额外的死代码 |
| 2 | ✅ 正确 | 完全按描述执行 |
| 3 | ✅ 正确 | 边界情况表现尚可，但原则正确 |
| 4 | ✅ 正确 | 方向性需要考虑，但原则正确 |
| 5 | ⚠️ 需修正 | 主搜索路径**已经**使用 `ts_rank`；重写方向和证据表述 |

**优先级**（经实际代码验证后调整）：

| # | 方向 | 优先级 | 调整后工作量 | 理由 |
|---|------|--------|------------|------|
| 1 | 死代码（两处） | **P0** | 2 天 | 清除 `internal/notification/` + `internal/search/` |
| 2 | 通知内容上下文 | **P1** | 1-2 天 | 确实的 UX/性能缺口 |
| 4 | 屏蔽 + 文件分享 | **P1** | 2-3 天 | 安全模型缺口 |
| 3 | 通知折叠 | **P2** | 2-3 天 | 信息丢失但无安全影响 |
| 5 | 搜索排序 | **P2** | 2-3 天（非 3-5） | 跨实体 `UNION ALL` + `ts_rank` + `ts_headline`；主搜索已排序 |

我还注意到 `internal/domain/access.go` 只包含两个简洁的函数（`CanReadFile`、`CanReadMessage`），总计 6 行——远低于文件限制。符合约束。
