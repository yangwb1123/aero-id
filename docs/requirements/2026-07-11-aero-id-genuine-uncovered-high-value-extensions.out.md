我已完整阅读了这份全局代码扫描文档。以下是对五个方向的理解确认：

## 已确认的理解

| 方向 | 核心理由 | 我理解的关键代码证据 |
|------|---------|-------------------|
| **#1 Admin gRPC + 封禁 + 死信** | 数据面 41 RPCs 全对等，控制面 HTTP-only 零对等 | `internal/handler/http/admin.go` 存在但无 `grpc/admin.go`；proto 无 `AdminService`；`PermUserBan` 定义了但无端点；`outbox.Repository` 缺死信查询/重放方法 |
| **#2 分布式限流** | 每实例 token bucket，N 实例 = N × rps 失效 | `internal/pkg/ratelimit/ratelimit.go` 文档明确写 "PER-INSTANCE"；配置无 Redis 项 |
| **#3 会话生命周期** | 1:1 私信隐式重建，无 mute/archive/pin/typing | `messages` 表无 `conversation_id`；`MessageService` 接口缺 Mute/Archive/Pin/Typing 方法 |
| **#4 内容生命周期** | 无 TTL、无 GDPR 导出、无批量清除 | `domain.File`/`domain.Message` 无 `expires_at`；`storage/gc/sweeper` 只扫软删除；无 `/v1/export` 端点 |
| **#5 通知覆盖缺口** | 只有 3 种通知类型，但产品场景 ≥ 10 | `dispatcher.go:73-80` switch default 丢弃所有其他事件；`file.shared` 已发布但被静默丢弃 |

每个方向的交叉比对确认：**docs/requirements/ 下既有 ~75 份文档中未系统性覆盖这五个方向**。

---

请问你希望我基于这份分析做什么？例如：

1. **为某个方向编写实施计划**（逐步实现方案、文件变更列表、测试策略）
2. **对某个方向做更深入的代码级 trace**（追踪所有相关文件和调用链）
3. **创建对应的 Issue/PR description** 模板
4. **在 docs/requirements/ 下建立新的分析文档**
5. **开始实现某个方向的代码**

请告诉我你的倾向或具体需求。
