感谢您提供详细的角色提示词。我已阅读了项目上下文和实现计划文档。

根据当前代码库的状态分析：

- **最近提交** (`961e896 [pi-batch] Stage: technical-design`) 已经包含了大量实现计划中的变更——包括配置加固、gRPC 传输协议加固、中间件采样器、降级状态管理器等。
- **未跟踪的文件**中包括 `cliff.toml`、`docs/DEGRADATION_MATRIX.md`、`docs/DATA_RETENTION.md` 等，表明部分任务已有进展。
- **实现计划** (`docs/imp plan/2026-07-12-implementation-plan.md`) 定义了 5 个工程方向、25 个任务。

---

**请问您希望我从实现计划中的哪个具体任务开始实现？** 例如：

**方向一（发布工程）：** TASK-001~005（Goreleaser、Docker CI、版本标签、CHANGELOG 自动化、SBOM）
**方向二（可观测性成本）：** TASK-006~009（AccessLog 采样、追踪自适应降采样、Relay 日志聚合、指标写放大优化）
**方向三（降级契约）：** TASK-010~014（降级矩阵文档、状态管理器、中间件、Handler 适配、集成测试）
**方向四（传输协议加固）：** TASK-015~021（gRPC keepalive、限流、消息大小限制、HTTP 加固、配置校验、单元测试）
**方向五（数据生命周期）：** TASK-022~025（audit/notification/outbox 清洁器、策略文档）

或者您有其他具体的实现需求？请提供具体任务或功能描述，我将严格遵循架构设计、实现计划和项目约定来完成。
