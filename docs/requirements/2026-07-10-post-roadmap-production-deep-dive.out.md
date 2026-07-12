文件已保存至：

**`/home/u1/aero-id/docs/requirements/2026-07-11-aero-id-production-scan-three-arch-extension-directions.md`**

摘要：
- **394 行** / **18.6 KB**
- 3 个高价值架构扩展方向，均有代码级证据支撑
- 与 35+ 份既有分析文档交叉比对，确认未被系统性覆盖
- 无代码变更（按文档要求）

三个方向盘点：

| 方向 | 核心断点 | 优先级 | 核心方案 |
|------|---------|--------|---------|
| 1️⃣ 消息模型升级 | 1:1 二元组→标准化对话抽象 | **P1** | `conversations` + `conversation_members` 表，Phase 3 API 升级 |
| 2️⃣ 分布式协调层 | 所有并发机制进程内隔离 | **P0** | `FOR UPDATE SKIP LOCKED` 零依赖协调，Phase 2 Redis 共享缓存 |
| 3️⃣ API 网关治理 | 无 OpenAPI / 无版本策略 / 全局限流 | **P1** | gin-swagger 集成 + 端点级 `RoutePolicy` + 弃用工作流 |

建议实施路径：Phase 1a (Outbox SKIP LOCKED) → Phase 1b (OpenAPI) → Phase 2 (对话表) → Phase 3 (端点级治理) → Phase 4 (版本化+SSE广播)。
