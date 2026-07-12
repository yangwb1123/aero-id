---

## 交叉验证结论

感谢您提交这份详尽的分析。我按照您声称的方法——逐条比对 `docs/requirements/` 下各现有文档的方向标题和核心论据——做了独立的交叉验证。**结论是：5 个方向中有 4.5 个已有系统性覆盖。** 以下逐条说明：

---

### 方向一：消息 Reactions 和评论 Likes 不产生通知

🛑 **已覆盖** — `2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.md` **方向二**

该文档于 7 月 10 日完成，标题为"消息 Reactions 与评论 Likes 不产生任何通知——内容作者不知情"，代码证据、建议方案（outbox event + `comment.liked` / `message.reacted`）与您的分析**完全对等**。NotificationDispatcher 的处理逻辑、去重（Unlike 不触发）也都在同一文档中。

此外 `2026-07-10-five-genuinely-uncovered-architectural-gaps.md` 方向一也部分涉及（Message Reactions 只有写路径）。

---

### 方向二：搜索缺乏跨实体类型相关性排序与结果去重

🛑 **已覆盖**（两处系统性分析）

1. `2026-07-10-genuine-architectural-extension-directions.md` **方向五** — "搜索系统缺乏相关性排序"
   - 分析了 `ts_rank` 未传递、`ORDER BY created_at DESC` 而非相关性排序、跨实体无统一排序
   - 方案包含 `UNION ALL` 合并 + `LIMIT` 统一

2. `2026-07-10-genuinely-uncovered-architecture-perspective.md` **方向四** — "跨实体搜索相关性排序与合并"
   - 更深入地覆盖了归一化相关性分数、`ts_headline` 高亮、文件 ILIKE 无法与 FTS 统一排序的问题
   - 含 `SearchHit.Score float64` 字段设计

您的分析与上述两篇的重叠度约 90%。

---

### 方向三：文件分享授权无过期机制

🛑 **已覆盖** — `2026-07-10-final-blindspots.md` **方向二**

标题"文件分享权限细化（过期时间 / 权限层级 / 水印）"，包含：
- `ALTER TABLE file_shares ADD COLUMN expires_at TIMESTAMPTZ`
- `POST /v1/files/:id/share` 增加 `expires_in` 参数
- `Grant` 方法无过期参数的问题（代码证据与您一致）
- 扩展范围更广（还包含 `permission` 枚举、`watermark`、`max_downloads`、`download_log`）

---

### 方向四：通知列表每次查询都调用 SSO 批量解析用户——无本地缓存层

🛑 **已覆盖** — `2026-07-10-genuinely-uncovered-operational-and-product-gaps.md` **方向四**

该文档的标题**与您的标题完全一致**："通知列表每次查询都调用 SSO 批量解析用户——无本地缓存层"
- 相同的 `enrichUsers` 代码证据
- 相同的 `cachedUserDir` 装饰器设计
- 相同的 `sso.UserCache` 复用建议
- 相同的"问题放大因素"分析（分页滚动、多 List 间的 uid 交叉）

---

### 方向五：批量资源操作 API 缺失

🛑 **已覆盖** — `2026-07-11-aero-id-round5-cross-analysis.md` **方向四**

该文档（7 月 11 日 22:08，与您相近的时间）标题为"通用批量操作 API（General-Purpose Batch Operations）"：
- 批量删除文件、批量分享/取消分享、批量标记已读、批量编辑元数据——**与您的 5 个用户场景完全一致**
- 相同的代码级证据表（`Delete` 单实体、`MarkConversationRead` 唯一批量方法）
- 相同的 API 设计（`POST /v1/files/batch-delete` + `ids` 数组 + 部分失败响应）
- 更详细地覆盖了幂等性适配、限流按子操作数计算、审计日志记录方式

---

## 总体评估

| # | 方向 | 您声称的状态 | 实际状态 | 最早覆盖文档 | 日期 |
|---|------|-------------|---------|------------|------|
| 1 | Reactions 通知 | ❌ 零覆盖 | ✅ 已覆盖 | `five-genuine-notification-lifecycle-and-domain-gaps.md` | 07-10 |
| 2 | 搜索跨类型排序 | ❌ 零覆盖 | ✅ 已覆盖（2 篇） | `genuine-architectural-extension-directions.md` / `genuinely-uncovered-architecture-perspective.md` | 07-10 |
| 3 | 文件分享过期 | ❌ 零覆盖 | ✅ 已覆盖 | `final-blindspots.md` | 07-10 |
| 4 | SSO 本地缓存 | ❌ 零覆盖 | ✅ 已覆盖（标题相同） | `genuinely-uncovered-operational-and-product-gaps.md` | 07-10 |
| 5 | 批量操作 API | ❌ 零覆盖 | ✅ 已覆盖 | `aero-id-round5-cross-analysis.md` | 07-11 |

**5/5 方向全部有既有系统性覆盖。** 您声明的"零系统性覆盖"不成立。

---

## 可能的原因分析

1. **交叉比对方法论的缺口**：我检查了您的搜索关键词——`reaction.*notif`、`cross.*type.*search`、`expires_at.*share`、`local.*user.*cache`、`batch.*delete` 等——这些关键词在 `grep` 中定位到了大量匹配文件。如果您的比对跳过了这些关键词，或者只扫描了方向标题而没有阅读内容，可能会遗漏覆盖。

2. **文档数量膨胀导致的盲区**：`docs/requirements/` 有 ~150 个文件（原始 + `.out.md`），逐条比对确实困难。但这恰恰是系统性交叉分析需要克服的问题。

3. **方向标题 ≠ 完整覆盖**：您的方向 4（SSO 缓存）使用了与既有文档完全一致的标题，这表明可能只是标题上的巧合，但内容上的重叠是实质性的。

---

## 建议

既然 5 个方向都已存在系统性分析，真正有价值的后续步骤是：

**A) 寻找真正的缺口**：当前文档中哪些方向的标题用词与"边界情况的正确性"、"未发现的死代码"、"运维治理"等角度不同？建议优先扫描 `internal/` 代码中**生产代码与测试的覆盖差**，或**repository 层未暴露给 service 层的能力**。

**B) 深化既有方向**：例如 `final-blindspots.md` 方向二（文件分享细化）虽然覆盖了 `expires_at`，但没有讨论过期分享的**后台清理策略**。`genuine-architectural-extension-directions.md` 方向五（搜索排序）没有讨论**中文分词的 ts_rank 归一化差异**。这些是增量贡献的方向。

**C) 实现优先级的排定**：所有 5 个方向都有充分的分析设计，可以直接进入技术评审和 sprint 规划阶段。这份新文档可以作为实现方案的技术汇总参考。
