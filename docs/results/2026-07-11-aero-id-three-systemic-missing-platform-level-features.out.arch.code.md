现在我完全理解了上下文。这是 pi-batch 流水线系统的一部分。我看到了：

1. **当前阶段**：流水线已到达 `code-implementation` 阶段（最近一次提交是 `590a63a [pi-batch] Stage: technical-design`）
2. **21 个需求文档**都已完成需求分析和技术设计，生成了对应的 `.arch.md` 和 `.impl-plan.md`
3. **大量新代码文件**（`internal/audit/`、`internal/block/`、`internal/comment/`、`internal/message/`、`internal/file/`、`internal/notification/`、`internal/search/`、新的迁移等）都处于 untracked 状态

但我需要你明确告诉我：**你希望我为哪个/哪些需求文档实现代码？**

以下是你可选的文档（每个都已生成 `.arch.md` + `.impl-plan.md`）：

最新（7月12日）：
- `2026-07-12-aero-id-high-level-extension-directions`
- `2026-07-12-aero-id-extension-analysis-pm-architect`

7月11日：
- `2026-07-11-aero-id-architecture-scan-five-high-value-expansion-directions`
- `2026-07-11-aero-id-five-high-value-extension-directions`
- `2026-07-11-aero-id-global-code-scan-four-high-value-extensions`
- `2026-07-11-aero-id-production-scale-architecture-gaps`
- `2026-07-11-aero-id-production-scan-three-arch-extension-directions`
- `2026-07-11-aero-id-round5-cross-analysis`
- `2026-07-11-aero-id-round7-code-scan-five-gaps`
- `2026-07-11-aero-id-three-systemic-missing-platform-level-features`
- `2026-07-11-aero-id-truly-uncovered-architecture-expansion-directions`
- `2026-07-11-post-mvp-directions`
- `2026-07-11-systematic-expansion-direction-scan`
- `architecture-scan-2026-07-11`
- 及其他多个

7月10日文档（5个）

每个文档包含多个功能方向的详细任务分解（如群组消息、Redis 共享状态、文件后处理管线、对话聚合、Webhook 平台、大文件校验等）。

**请指定一个或多个文档名称**，我将严格按照其 `.arch.md` 架构设计和 `.impl-plan.md` 实现计划来编写代码。
