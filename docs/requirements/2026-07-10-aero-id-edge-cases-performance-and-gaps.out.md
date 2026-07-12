验证完成。我已检查了文档中引用的所有关键代码文件。以下是逐方向验证结果：

---

## 验证摘要

### 方向一：Notification Dispatcher 单事件 DB 调用 ✅ **完全准确**

| 证据 | 代码行 | 状态 |
|------|--------|------|
| `Dispatcher.Handle` 逐事件调用 `notifs.Create()` | `dispatcher.go:70` | ✅ |
| `notificationFor` 只处理 3 种事件类型 | `dispatcher.go:80-88` | ✅ |
| Repository 无批量变体 | `notification/repository.go:37-52` | ✅ |
| Outbox relay 串行处理每个事件 | `relay.go:108-139` | ✅ |

### 方向二：文件分享缺通知 ✅ **完全准确**

| 证据 | 代码行 | 状态 |
|------|--------|------|
| `ShareFile` 无 outbox 事件发射 | `service_share.go:28-79` | ✅ |
| `UnshareFile` 同样无事件 | `service_share.go:81-109` | ✅ |
| Dispatcher 无 `file.shared` 处理 | `dispatcher.go:80-88` | ✅ |

### 方向三：软删除级联缺口 ⚠️ **部分准确 — 有两处小误差**

**✅ 核心主张均正确：**
- 文件删除无 shares 级联 (`repository/file.go:109-117`)
- 消息删除无 reactions 级联 (`message/repository.go:141-146`)
- 评论删除无子评论级联 (`repository/comment.go:115-122`)

**❌ 不准确的子索赔：**
1. **"SearchMessages 没有过滤 deleted_at IS NULL"** (证据 3) — 实际代码中 `message/repository.go:195` 的 SQL 查询**明确包含** `WHERE deleted_at IS NULL`。文档描述不准确。
2. **"ReactToMessage 没有检查目标消息是否已删除"** (证据 4) — 代码使用 GORM 的 `First()`，它自动添加软删除过滤；已删除消息会返回 `ErrNotFound`。文档承认了这一点，但将其作为一个独立问题提出略显过度陈述。

这两个误差不影响整体的方向分析，但应修正。

### 方向四：后台任务 context.Background() ✅ **完全准确**

| 证据 | 代码 | 状态 |
|------|------|------|
| Outbox relay 使用 `context.Background()` | `relay.go:169,174` | ✅ |
| GC sweeper 使用 `context.Background()` | `sweeper.go:156-165` | ✅ |
| Idempotency cleaner 无上下文管理 | `store.go:137-148` | ✅ |
| Token cache cleaner (内存操作，模式一致) | `token_cache.go:118-130` | ✅ |
| Rate limiter cleaner (内存操作) | `ratelimit.go:109-122` | ✅ |

### 方向五：搜索结果拼合而非统一排序 ✅ **完全准确**

| 证据 | 代码行 | 状态 |
|------|--------|------|
| `Search` 顺序循环三种类型 | `search/search.go:58-79` | ✅ |
| 每种类型独立使用完整 limit | `search/search.go:90-112` | ✅ |
| Files FTS 只索引 `name` | `migrations/000016_fts_indexes.up.sql:15` | ✅ |
| 响应无排序信息 | `search/handler_http.go:33-42` | ✅ |

---

## 结论

文档质量极高 — **五个方向中的四个完全准确**。方向三存在两处小误差，但不影响核心论点（软删除无级联语义是真实问题，只是证据 3 和 4 的具体表述不完全精确）。

### 建议修正

如果希望更新文档以修正这两处：

1. **证据 3**（SearchMessages）：改为 *"SearchMessages 的 SQL 虽然手动添加了 deleted_at IS NULL 过滤，但其他搜索路径（如 LIKE 回退或未来新增的查询）若遗漏该条件可能导致已删除内容出现"*
2. **证据 4**（ReactToMessage）：改为 *"ReactToMessage 在 repository 层通过 GORM First() 隐式过滤已删除消息，行为正确；但依赖 GORM 隐式过滤而非显式断言是一种防护性编程的薄弱点"*
