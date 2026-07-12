好，所有证据细节已确认。以下是我的深度反馈。

---

## 对分析文档的深度反馈

### 1. 证据链总体评价

五个方向的代码引用**全部准确**，没有虚假陈述。额外补充一个方向二未提及但值得注意的细节：

**`ListByTarget` 缺少目标实体活性检查** — 已有 GORM `Where("status = ?", CommentStatusNormal)` 过滤评论状态，但**没有 JOIN 检查 target 实体是否被删除**。一个已删除文件仍然会在评论列表中看到针对它的全部评论。这是方向二最重要的数据一致性缺口之一，文档提到但未突出。

### 2. 我建议调整的一个优先级判断

文档将**方向五（分布式基础设施）**评为 P0 并与方向一并行，给出的理由是"水平扩展前置条件"。但从实际部署需求看：

**当前单实例架构对方向一有直接影响。** 如果方向一（群组会话）先上线而方向五未完成，则：
- 群组通知仅通过 SSE 发送，SSE Hub 是内存单实例 → 多实例部署后群通知丢失
- Rate limiter 是内存单实例 → 多实例部署后速率限制失效（但这不是关键问题）
- SSO 缓存是内存单实例 → 多实例后每个实例各自从 SSO 拉取，放大 SSO 负载

**建议调整为：方向一的实现**主动预留分布式抽象接口**（如 Hub 实现一个 `Publisher` 接口，当前是 `Hub` 直接实现，改为 `Hub.Publish()` 调用 `Publisher` 接口）；方向五作为方向一的**第二阶段依赖**，在方向一合并后立即启动。这样方向一不阻塞，方向五也不被挤到 P2。

### 3. 一个被低估的风险点（方向一）

群组会话与现有 `messages` 表存在一个**外键约束缺失问题**：当前 `messages` 表没有 `conversation_id`，所有查询（Inbox、Conversation、Search）均基于 `(from_uid, to_uid)` 二元组。引入 `conversations` 表后：

- **数据迁移范围**：现有消息需要分配到 pair conversation。如果采用惰性创建（首次查询时检测），需要修改 `Inbox()` 和 `Conversation()` 的逻辑，这是对核心查询路径的修改。
- **Search 适配**：当前消息全文搜索没有 `conversation_id` 过滤，群组消息会混入个人搜索结果。

文档正确地识别了这些风险为"中-高"。

### 4. 一个缺失的方向（文件命名空间/租户隔离）

分析文档未覆盖一个方向：**多命名空间/项目隔离**。当前所有文件、消息、评论都平铺在所有用户之间（通过 `owner_uid` / `to_uid` 关联）。对于 aero-id 作为"数据 owned by accounts 的服务"而言，引入"空间"（Space / Project / Workspace）作为资源隔离边界是一个自然的演进方向：

- 文件属于 Space → 跨 Space 不可见
- 消息可以限定在 Space 内部
- 群组会话自然归属于 Space

代码证据：`files` 表没有 `space_id` / `namespace` 字段；`messages` 表也没有。但考虑到 aero-id 当前 SSO 层没有提供 "org/team/space" 概念，这个方向可能需要等 SSO 层先提供租户模型，所以不放在 P0/P1 是合理的。

### 5. 对推荐序列的微调

```
P0（立即启动，两队并行）
├── 方向一（群组会话）—— 架构层面预留 Publisher/CacheBackend 接口
└── 方向五（分布式原语）—— 先做：①定义 CacheBackend 接口 ②Redis 实现 ③SSO cache 迁移
　　　　　　　　　　　 方向一在消息路由层预留接口，方向五完成后适配

P1（方向一合并后）
├── 方向二（资源级联）—— 独立，可并行启动，风险最低
├── 方向三（可编程 Webhook）—— 依赖方向五的 Redis（但初期可以内存模式上线）

P2（持续）
└── 方向四（文件变换）—— 完全独立
```

### 6. 建议加入的下一阶段动作

基于当前代码库状态，方向一和方向五的**具体切入文件**：

**方向一切入点：**
| 顺序 | 文件 | 变更 |
|------|------|------|
| 1 | `internal/domain/message.go` | 新增 `ConversationID` 字段（`type ID`，可选） |
| 2 | `migrations/` | `000023_create_conversations.up.sql` + `000024_create_conversation_members.up.sql` |
| 3 | `internal/repository/message.go` | `ConversationRepository` 接口 + 实现 |
| 4 | `internal/service/message.go` | `SendMessageInput` 增加 `ConversationID`，路由逻辑分支 |
| 5 | `internal/service/service.go` | `ConversationService` 接口 |

**方向五切入文件：**
| 顺序 | 文件 | 变更 |
|------|------|------|
| 1 | `internal/pkg/cache/cache.go` | 定义 `CacheBackend` 接口（新包） |
| 2 | `internal/pkg/cache/memory.go` | 现有 `sync.Map` 实现迁入 |
| 3 | `internal/sso/token_cache.go` | 依赖 `CacheBackend` 接口而非 `map` |
| 4 | `internal/hub/hub.go` | `Publisher` 接口 + 内存实现 |
| 5 | `internal/config/config.go` | 新增 `[redis]` 配置段 |

---

**总体结论：** 这是一份优秀的分析文档。证据扎实，优先级判断合理，交叉覆盖检查诚实。如果做一件事能让分析的落地更顺畅，我建议**方向五先完成接口抽象和内存实现**——这一步骤可以立即做（不需要 Redis），且不影响方向一的并行开发，但为后续分布式化铺平了道路。
