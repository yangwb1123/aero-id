已保存文件至 `docs/requirements/2026-07-10-aero-id-architect-pm-deep-expansion-analysis.md`。

相比已存在的 `expansion-directions.md`（覆盖群组消息、Redis 共享状态、文件后处理管线、对话聚合列表、Webhook 平台），本文覆盖的 **5 个未被系统分析的领域**：

| # | 方向 | 文件 | 推荐优先级 |
|---|---|---|---|
| 1 | 多表情反应体系 | 新建 `emoji_reactions` 统一表替代 `comment_likes` + `message_reactions` | **P1** — 快速迭代 |
| 2 | 会话模型统一与收件箱重构 | 新建 `conversations` + `conversation_members` 表，收件箱改为聚合视图 | **P2** — 与群组对话同步 |
| 3 | 自动化内容安全与滥用防御 | Spam 评分 + `user_sanctions` 表 + 管理端点 + 文件扫描集成 | **P1** — 生产安全底线 |
| 4 | 文件预处理与缩略图管线 | `file_processing_jobs` 异步 Worker（缩略图/元数据/OCR/扫描） | **P2** — 体验刚需 |
| 5 | 审计告警与异常行为检测 | `alert_rules` + `alert_events` 规则引擎 + 通知整合 + 运营面板 | **P1** — 安全运营闭环 |

此外，附录 A 发现了 9 个已有功能中的边界情况（如文件去重竞态、通知折叠 `created_at` 覆盖、SSE 重连并发安全、Outbox 中丢失 `replier_uid` 等），附录 B 列出了 5 个性能优化机会。

如需进一步操作——如将某个方向拆解为技术设计文档（`.md`）、生成对应的 migration SQL、或开始实现其中某个方向——请告知。
