**代码级证据验证全部通过。** 以下是 5 个方向的 grep 验证汇总：

| 方向 | 搜索项 | 命中结果 | 结论 |
|------|--------|---------|------|
| **方向一：草稿** | `grep -rn "draft\|Draft" internal/` | **0 匹配** | ✅ 零覆盖确认 |
| **方向二：@提及** | `grep -rn "mention" internal/service/ internal/domain/ internal/handler/` | **0 匹配** | ✅ 零覆盖确认，`NotifType` 仅 3 种（`message.received`, `message.read_receipt`, `comment.reply`），无 `mention` |
| **方向三：引用/转发** | `grep -rn "quoted\|Quoted\|forwarded\|Forwarded" internal/` | **0 功能匹配**（仅测试文件中的"参数转发"） | ✅ 零覆盖确认 |
| **方向四：批量操作** | `grep -rn "batch\|Batch" internal/service/ internal/handler/` | **0 匹配** | ✅ 零覆盖确认 |
| **方向五：关系遍历** | `grep -n "ListGrants" internal/repository/` + `ListByOwner` + `CountByTarget` | **0 匹配** | ✅ 零覆盖确认；`CanReadFile` 不检查 `file_shares`，与文档一致 |

文件已保存至 **`docs/requirements/2026-07-11-aero-id-round5-cross-analysis.md`**。

---

### 核心洞察总结

这 5 个方向确实是在 50+ 已分析方向中均**零覆盖**的盲区。最让我注目的是它们的共同特征：

1. **草稿与自动保存** 💡 — 最有"显而易见却被遗忘"的感觉。所有 CRUD 都已完备，但无人思考"正在写但未提交"的数据。用户每天触碰最多的场景。

2. **@提及** 💡 — 代码已经暗示了此能力（SSO 的 `GetUsers` 批量解析已在服务层就绪），但从未被正式设计。可以将一条"内容中的 @ 模式匹配 → 通知分发"的链条贯通。

3. **内容引用/转发** — 工程成本最低（纯 `meta` JSONB 扩展），但在群聊未就绪之前价值受限。Phase 3 的定位合理。

4. **批量操作** — 最大的架构挑战是部分失败模型，但现有基础设施（幂等性 + outbox + 审计）已为实现它铺好了路。

5. **关系遍历** 💡 — 这是最让我眼前一亮的方向。活动流是时间轴的聚合，但用户真正需要的是**关系图遍历**："这条消息引用了哪个文件？谁评论了它？谁有权访问？" 一个 `GET /v1/files/:id/related` 端点就能把隔离的域（文件、消息、评论、分享）连接成一个有意义的网络。

**推荐立即行动项（Phase 1）：**
- **方向五（关系遍历）** 代码级风险最低，纯读操作，不需要新表，直接基于现有索引。可以先实现 `GET /v1/files/:id/related` 作为试点。
- **方向一（草稿）** 虽然是写操作，但逻辑独立（新表 + 新 service），不碰现有数据流。

两个方向可以在同一个 Sprint 中并行推进，为 Phase 2 的 @提及和批量操作奠定基础。
