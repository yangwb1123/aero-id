# 角色确认

我已了解 **Code Implementer（代码实现者）** 角色定义和核心要求，同时已阅读并理解 **aero-id** 项目的 `AGENTS.md` 上下文。

## 项目关键要点（我已明确）

| 要点 | 内容 |
|---|---|
| 语言/模块 | Go 1.26, module `github.com/aero/aero-id` |
| 架构 | handler → service → repository → Postgres (GORM) |
| 严格约束 | 文件 ≤500行, 函数 ≤50行, 复杂度 ≤10, if嵌套 ≤3层 |
| 测试 | `make test`（单元测试） / `make test-integration`（需要Docker） |
| 验证门禁 | `make test && make lint` |
| 软删除 | 所有删除为软删除（`gorm.DeletedAt`） |
| ID生成 | UUIDv7 (`pkg/idgen`) |
| SSO身份 | 无accounts表，仅通过uid引用用户 |
| 错误处理 | repo返回原始错误 → service包装为AppError → handler渲染 |

---

我准备好将架构设计文档（`.arch.md`）和实现计划文档（`.impl-plan.md`）转化为高质量的代码实现了。

不过，我注意到当前消息中**没有附带具体的 .arch.md 和 .impl-plan.md 文件**——请提供需要实现的功能设计文档，我将严格按照以下流程工作：

1. **阅读**设计文档和实现计划
2. **分析**现有代码结构（`internal/` 下各包）
3. **实现**代码，遵循所有工程约束
4. **验证**通过 `make test && make lint`
5. **输出**完整的代码实现报告

请提供需要实现的具体功能文档。
