我注意到您已经提供了 **Code Implementer 角色提示词**，但没有附上实现所需的输入文件。为了进行代码实现，我需要您提供以下文件：

1. **架构设计文档**（.arch.md） — 描述系统架构、模块划分和关键设计
2. **实现计划文档**（.impl-plan.md） — 具体实现步骤和时间线
3. **原始需求文档** — 功能需求描述

根据当前工作目录 `/home/u1/aero-id`，这是一个已有的 Go 项目（aero-id），结构如下：

```
handler/{http,grpc}  →  service  →  repository  →  Postgres (GORM)
                             ├──→  storage  (local | s3)
                             └──→  sso      (gRPC client + caches)
```

请提供上述实现文档，或者如果您希望我直接基于现有的项目结构和 AGENTS.md 中的约束来实现某个具体功能，请明确说明需要实现的**功能需求**。例如：

- 实现某个新的 handler/endpoint
- 添加某个 service 方法
- 实现新的 repository 查询
- 添加存储后端功能
- 其他具体需求

我需要知道 **要实现什么** 才能开始编码。
