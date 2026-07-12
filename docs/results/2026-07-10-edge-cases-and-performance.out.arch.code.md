我已经详细审查了项目当前状态。以下是我了解到的完整情况：

---

## 当前状态概览

### 已完成的工作
- **项目已交付所有 ROADMAP 功能**：P0-P2 全部完成（SSE实时推送、全文搜索、审计日志、消息Reactions、SSO证书固定等）
- **最近 commit**: `[pi-batch] Stage: technical-design` — 包含大量架构设计和实现计划文档
- **工作树中有 49 个文件的未提交变更**（8655行新增/2264行删除）— 这部分代码已经编译通过且所有测试均通过

### 可用的设计文档

最相关的需求/设计文档对是：

| 文档 | 文件 |
|------|------|
| **需求文档** | `docs/requirements/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.out.md` |
| **架构设计** | `docs/results/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.out.arch.md` |
| **实现计划** | `docs/results/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.out.impl-plan.md` |

该文档覆盖 **5 个方向**，分解为 **34 个任务**：

| 方向 | 描述 | 任务 |
|------|------|------|
| 方向一 | 顶级评论不通知 | TASK-001 ~ TASK-006 |
| 方向二 | Reactions/Likes 不通知 | TASK-007 ~ TASK-013 |
| 方向三 | 消息软删除不通知 | TASK-014 ~ TASK-018 |
| 方向四 | 文件无类型分类 | TASK-019 ~ TASK-025 |
| 方向五 | 通知偏好颗粒度粗 | TASK-026 ~ TASK-034 |

---

**作为 Code Implementer，我需要您明确以下信息才能开始实现：**

1. **选择方向**：从哪一组任务开始？例如方向一（顶级评论通知）、方向四（文件类型分类）？
2. **是否基于现有工作树变更继续**：当前工作树有通知系统的基础设施改进（偏好检查、速率限制、通知折叠等），这些是否应该保留或参考？
3. **优先实现策略**：按架构师推荐的 `A → [B, C, D] 并行 → E → F → G` 顺序，还是按其他顺序？

请告诉我您要具体实现哪个方向的哪些任务，我会按照 AGENTS.md 的约束（文件 ≤500行、函数 ≤50行等）编写代码，并生成完整的代码实现报告。
