根据文件结构分析，我看到了几个关键信息：

1. **流水线阶段**：已完成 `requirement-analysis` 和 `technical-design`，当前是 `code-implementation` 阶段
2. **已有文档对**：每个需求文档都有对应的 `.arch.md`（架构设计）和 `.impl-plan.md`（实现计划）
3. **已有部分代码输出**：一些文档已经有 `.code.md` 文件（之前代码实现阶段的产物）
4. **工作目录中有大量未跟踪代码**：`internal/audit/`, `internal/block/`, `internal/comment/`, `internal/file/` 等新包

---

请您确认具体要实现的**输入文档对**，例如：

```
文档对示例：
  📄 架构设计: docs/results/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.out.arch.md
  📄 实现计划: docs/results/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.out.impl-plan.md
```

或者您可以直接把 **架构设计文档** 和 **实现计划** 的内容贴给我，我会按照 `AGENTS.md` 中的约定和代码风格，编写可编译、可测试的高质量 Go 代码。
