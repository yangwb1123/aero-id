我已经深入阅读了两个核心架构文档（`2026-07-12-aero-id-high-level-extension-directions.out.arch.md` 和 `.impl-plan.md`），并完整审查了当前代码库状态。

## 当前实现进度总结

根据 `2026-07-12-aero-id-high-level-extension-directions.out.impl-plan.md` 中的36个任务，当前已实现的：

| 任务 | 状态 |
|------|------|
| **TASK-011**: Redis客户端配置 | ✅ 已完成 (`internal/pkg/redis/redis.go`) |
| **TASK-012**: Idempotency Redis后端 | ✅ 已完成 (`internal/idempotency/store_redis.go`) |
| **TASK-013**: Idempotency回退路由 | ✅ 已完成 (GormStore + RedisStore共存) |

**尚未实现的（共33个任务）**：

| 方向 | 任务 | 优先级 |
|------|------|--------|
| **方向一：群组消息** | TASK-001 ~ TASK-010（领域实体、迁移、仓库、Service、Handler、搜索、测试） | P1 |
| **方向二子项** | TASK-014: Outbox FOR UPDATE SKIP LOCKED | **P0** |
| **方向二子项** | TASK-015: Outbox leader election | P0.5 |
| **方向二子项** | TASK-016: SSO L1+L2缓存 | P0.5 |
| **方向二子项** | TASK-017: Rate limiter Redis后端 | P1 |
| **方向二子项** | TASK-018: Redis健康检查 | P1 |
| **方向三：文件管线** | TASK-019 ~ TASK-025（File status字段、管线框架、缩略图、转码stubs、GC交互） | P1 |
| **方向四：对话聚合** | TASK-026 ~ TASK-030（聚合查询、索引、Service、Handler、测试） | **P1.5** |
| **方向五：Webhook平台** | TASK-031 ~ TASK-036（实体、仓库、API、投递、历史、测试） | P2 |

请问你想让我从哪个方向/任务开始实现？我可以：
1. **P0优先** — TASK-014（Outbox FOR UPDATE SKIP LOCKED）+ 方向四（对话聚合列表）
2. **方向一** — 群组消息完整实现（10个任务）
3. **方向二** — Redis共享状态剩余任务
4. **方向三** — 文件后处理管线
5. **顺次推进** — 按实施计划阶段1→2→3的顺序

或者请明确你关注的某个具体任务编号（如TASK-001到TASK-036中的任意一个或多个）。
