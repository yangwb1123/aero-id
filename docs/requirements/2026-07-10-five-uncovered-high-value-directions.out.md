您提供了一份非常全面、结构清晰的分析文档。我已经完整阅读。

几个观察：

1. **交叉验证方法论很扎实** —— 从 13+8 份既有文档中提取 ~60 个方向进行去重比对，再回扫 internal/ 代码库确认缺口，逻辑链条完整。

2. **代码级证据充分** —— 每个方向都列出了具体的文件位置和代码行为，比如方向一的 `hub.go` 只有 `Publish` 无 `Subscribe`、方向四的 `maxBytes` 是全局共享 int64 字段。这比纯概念性分析更有说服力。

3. **架构设计务实** —— 每个方向都有核心模型、新表 DDL、流程描述、Edge Cases 表。特别好评的是 Edge Cases 里考虑了 block 关系对 Presence/Typing/Bot 的影响（A typing → B blocked A → 不投递）。

4. **综合路线图合理** —— Phase 1 的 P1 三个方向（计量配额 + 设备管理 + 在线状态）确实是基础设施层的优先项，Phase 2 的 Recall/Schedule 可以利用 outbox relay 模式降低实现成本。

要不要我把这份文档保存到 `docs/requirements/` 目录下？目前该目录下有 13 份文档，这份作为第 14 份编号可以延续命名风格。

另外，如果你打算按路线图的 Phase 1 推进，需要我帮你做以下任意一项吗？

- 对某个方向做更深的 **schema 设计评审**（比如方向四的 `user_resource_usage` 表在高并发下的竞争条件处理）
- 对某个方向写 **ADR (Architecture Decision Record)**
- **任务分解** 成可追踪的 GitHub Issues
- 基于某个方向的代码级证据，做一次 **代码健康检查** 检查是否有违反 AGENTS.md 约束的文件接近触发器阈值

请指示方向。
