代码证据全部准确。该分析文档质量很高。现在我从架构师角色进行深入分析。

---

# 架构分析：aero-id 五个高价值扩展方向

## 1. 架构评估 — 当前系统的优势与局限

### 1.1 当前架构的成熟度

aero-id 的架构选择整体是**合理的**，已经走过了"从 0 到 1"阶段，处于"从 1 到 10"的临界点。

**核心优势：**

| 维度 | 现状 | 评价 |
|---|---|---|
| **分层架构** | handler → service → repository/storage/sso —— 严格单向依赖 | **优秀**。这是可测试性和可扩展性的基础。与业界最佳实践一致 |
| **传输对等** | HTTP (Gin) + gRPC 41+ RPCs 完全对等暴露 | **优秀**。内部服务用 gRPC，外部用 HTTP/JSON，但业务逻辑共享同一层 |
| **可靠性工程** | Outbox 模式 + 指数退避 + Dead Letter + 幂等键 + 断路器 + stale-cache + singleflight | **高于 MVP 预期**。这些通常是第 2-3 轮架构迭代才添加的。 |
| **错误处理** | 三层错误映射（repo raw → service AppError → handler render） | **优秀**。杜绝了 SQL 错误泄漏到客户端的常见事故 |
| **ID 设计** | UUIDv7 作为主键 + 分页游标 | **正确**。UUIDv7 天然时间有序，兼容分布式生成，兼顾 DB 索引局部性 |
| **OTel 集成** | 全链路追踪 + RED 指标 | **生产就绪**级的可观测性 |

**可借鉴的细节决策：**

- `pkg/idgen` 封装 UUIDv7 生成，与 DB 层解耦，未来迁移到雪花 ID 只需改一个包
- 幂等键过期清理（`IdempotencyKeys` TTL 机制），避免存储无限膨胀
- SSO 客户端的懒加载（lazy dial）和断路器，防止冷启动时级联失败

### 1.2 架构局限 — 制约增长的四个结构性瓶颈

**瓶颈一：领域模型扁平化**

当前 `internal/domain/` 下的模型是扁平的数据结构（`Message`、`Comment`、`File`、`Notification`），它们有自己的字段，但**缺乏关系之间的显式连接**。例如：

- `Message` 和 `Conversation` 之间无关联实体
- `File` 和 `Label`/`Folder` 之间无关联实体
- `Comment` 和 `ModerationAction` 之间无关联实体

这在 MVP 阶段是可接受的，但对于平台级系统来说，领域模型的扁平化意味着**无法表达业务关系**——关系只能靠 WHERE 子句拼凑。

**瓶颈二：系统边界模糊**

aero-id 当前在 `internal/` 下约 180+ 个 Go 文件按传输层映射组织（`message/`、`file/`、`comment/`、`outbox/`、`search/`）。这种组织方式与业务能力映射良好——但对于跨领域的"横切关注点"（moderation、audit、analytics）没有独立的位置。

具体来说，`moderation` 的逻辑目前散落在 `comment/service.go`（举报处理）、`middleware/ratelimit.go`（全局节流）、`outbox/`（通知）中，没有归入一个清晰的内聚包。

**瓶颈三：缺少策略引擎层**

当前所有业务规则都是硬编码的 if-else 散落在 service 方法中：

- "新用户多久可以发布内容" → 硬编码
- "举报多少次自动隐藏" → 不存在
- "event_types 是否合法" → 甚至没有校验
- "最大 nesting 深度 = 2" → 硬编码在 domain 验证中

随着规则复杂度增加（策略引擎/规则引擎），这种硬编码模式会变得不可维护。

**瓶颈四：控制面/数据面未分离**

当前的 Outbox relay 同时承担了：
1. 数据面职责——投递事件
2. 控制面职责——管理投递配置、监控投递状态

死信事件不可观测、重试策略不可配置，根因在于控制面和数据面没有分离。

### 1.3 架构债务

真正的架构债务（Architecture Debt，区别于代码层面的 Technical Debt）：

| 债务项 | 位置 | 影响 | 建议处理时机 |
|---|---|---|---|
| `meta` JSONB 无约束使用 | `File.Meta` | 多客户端同步时，不同客户端用不同 key 写入，后端无法索引或校验任何语义 | 在引入 Labels/Folders 时一并设计标准化 meta key 列表 |
| 未验证的 `event_types` 配置 | `config.WebhookConfig` | 管理员手误拼写不投递，无人察觉 | P1，低风险，仅加一个 validate 函数 |
| 全局 rate limit 不分操作类型 | `middleware/ratelimit.go` | 无法为举报、评论、消息设置不同限速阈值 | P2，在滥用预防管道中一起解决 |
| 消息没有举报机制 | `internal/message/` | 私信骚扰/垃圾无反馈通道 | P1-P2，与滥用预防管道同步 |

---

## 2. 扩展方向 — 对文档中 5 个方向的架构级分析与补充

我在这里**不重复文档内容**，而是提供文档未覆盖的架构深入分析。

### 方向一：Conversation 模型升级——群组聊天与多参与者消息

#### 文档已覆盖：✅ 数据模型缺口、群组需求、迁移策略

#### 未覆盖的架构深度分析：

**2.1.1 数据模型设计的三种选项**

| 方案 | 描述 | 优势 | 劣势 |
|---|---|---|---|
| **A: 隐式 Conversation** | 不新增 `conversations` 表，在 Message 上新增 `conversation_id` 列，首次消息触发生成；群组=多个 to_uid 存为 JSONB 数组 | 迁移成本最低；当前代码改动最小 | 无法做会话级元数据；群组权限管理脆弱；查询性能差 |
| **B: 显式 Conversation 实体** | 新增 `conversations` + `conversation_members` 表；消息通过 conversation_id 关联 | 架构清晰；支持群组/频道/私聊多种类型；可扩展强 | 迁移成本高；需要处理现有 1:1 消息的向后兼容 |
| **C: 混合模型** | 新增 `conversations` 和 `conversation_members`；但 `Message` 同时保留 `from_uid`/`to_uid`（用于 1:1 廉価查询）和 `conversation_id`（用于群组查询）；首次加载旧 1:1 消息时，自动创建隐式会话 | **推荐**。取得迁移兼容性和架构清晰性的平衡 | 模型有些冗余；需要在两个路径间维护一致性 |

**我的推荐：方案 C**

原因是 aero-id **已有大量生产数据**（消息表中有现存 1:1 消息对），且这些数据需要保持可访问。方案 C 的具体实现策略：

1. **数据层**：新增 `conversations` 表和 `conversation_members` 表；`messages` 表新增 `conversation_id` 可空列（旧消息为 NULL）
2. **服务层**：在 `GetConversation`/`ListConversations` 方法中，如果检测到 `conversation_id` 为 NULL 的旧 1:1 消息对，在**读取时**自动创建隐式会话（创建操作幂等）
3. **写路径**：所有新建消息强制写入 `conversation_id`；旧的 1:1 Send 逻辑在 SQL 级别保证隐式会话的惰性创建
4. **回填作业**：一个后台迁移 job（可配置启动时机），逐步给所有现有消息回填 `conversation_id`

**2.1.2 与通知系统的交互复杂度**

这是文档没有提到的关键架构挑战。群组消息的通知触发逻辑远比 1:1 复杂：

- 1:1 消息：发送消息 → 通知收件人（单向）
- 群组消息：发送消息 → 需要为**除发送者外的每个成员**生成通知
- 如果某成员开启了**静默/勿扰**模式，不能产生通知
- 如果某成员设置了**按会话的独立通知偏好**，需要检查成员的 `notification_pref_override`
- **已读回执**：1:1 是一对一；群组是 N:N——每个成员有独立的 `last_read_at`

这意味着 `internal/outbox/` 中现有的 notification dispatcher 需要支持**广播到多个接收者**（目前是单收件人模型）。这不是一个点改动，而是涉及 `Notification` 领域模型的扩展——可能需要 `notification_recipients` 连接表。

**2.1.3 关键架构决策：共享已读 vs 独立已读**

群组的已读状态有两种设计：

- **共享已读**：消息一发出去，所有成员看到相同的已读计数（"张三、李四已读"）——类似 Slack
- **独立已读**：每个成员的 `last_read_at` 独立——类似 WhatsApp

aero-id 的架构应**默认选独立已读**，原因：
- 当前 1:1 已读回执已经是独立模型（每个 `Message` 有 `ReadAt`）
- 独立已读与 `conversation_members.last_read_at` 字段直接对应
- 共享已读可以通过独立已读聚合得到（select count(*) where read_at > created_at）

### 方向二：Webhook 外发投递可靠性

#### 文档已覆盖：✅ 幂等键缺失、最多一次语义、顺序保证缺失、死信不可观测、配置验证缺失

#### 未覆盖的架构深度分析：

**2.2.1 顺序保证的两种方案对比**

文档指出没有 `sequence_number`，但未深入分析**实现顺序保证的两种路径**：

| 方案 | 实现 | 优势 | 劣势 |
|---|---|---|---|
| **全局单调序列** | 每个 event 获取一个全局自增 seq（DB 序列或 Redis INCR） | 绝对顺序，消费者逻辑简单 | 单点瓶颈；分布式下无法跨区域；overhead 高 |
| **按 aggregate_id 的本地序列** | 每个 aggregate（如一个 conversation）内部维护一个 `version`，每次写入递增 | 无全局瓶颈；天然与数据版本一致；与领域模型对应 | 消费者需要在每个 aggregate 内部维护顺序状态；跨 aggregate 的事件无序 |

**推荐：方案 B（按 aggregate_id 的本地序列）**，理由：

1. aero-id 的领域模型天然按 `aggregate_type + aggregate_id` 组织（已有此设计）
2. 消费者关心的顺序往往**只在同一个 aggregate 内部有意义**——例如对一个 message 的 `updated` 事件在 `created` 之后
3. 与 OCC（乐观并发控制）共享相同的 version 字段

实现方式：在 `outbox_events` 表新增 `aggregate_version INT NOT NULL DEFAULT 1`，每次往同一个 aggregate 写入事件时，`INSERT ... SET aggregate_version = (SELECT COALESCE(MAX(aggregate_version), 0) + 1 FROM outbox_events WHERE aggregate_type = ? AND aggregate_id = ?)`

**2.2.2 死信可观测性的 API 设计模式**

文档提出需要管理 API。我建议采用 **"事件重放"模式**而非"死信重试"模式：

```
# 不推荐：专有的死信重试 API
POST /admin/outbox/retry/:id  ← 耦合于 outbox 的实现细节

# 推荐：通用事件重放 API
POST /admin/events/replay
{
  "event_ids": ["...", "..."],
  "target": "webhook" | "notification"  ← 选择投递通道
}
```

理由：
- "重试死信"本质上等同于"重新投递一个已存在的 event"
- 通用事件重放 API 不仅用于死信，还可以用于**事件迁移**（event schema 变更后重新投递）
- 与 Outbox 的事件模型保持一致——不引入额外的抽象

**2.2.3 Webhook 配置的版本管理**

当前 webhook 配置是静态的（在 config.yaml 中）。对于平台化场景，建议：

- 引入 `webhook_subscriptions` 表（类似 GitHub 的 webhook API）
- 每个 subscription 独立配置：URL、secret、event_types、max_retries、retry_interval
- 在投递时，将 `outbox_events` 的事件与 `webhook_subscriptions.event_types` 做匹配过滤

但这带来一个新的架构挑战——**订阅管理**。是否应该把这部分放到 aero-id 中，还是放到外部配置中心？

**我的建议**：先保留配置文件驱动（不做 DB 化的 full subscription manager），因为引入新表和新 API 的复杂度可能高于收益。先解决幂等性和可观测性这两个最痛的缺口。

### 方向三：内容编辑历史追踪

#### 文档已覆盖：✅ 直接覆写不保存历史、无 schema、无 proto 字段、审计不记录

#### 未覆盖的架构深度分析：

**2.3.1 存储设计的两种方案**

| 方案 | 实现 | 优势 | 劣势 |
|---|---|---|---|
| **新表存储全量版本** | `message_revisions(content, created_at, edited_by)` 每编辑一次 INSERT 一行 | 完整版本链；查询方便 | 存储线性增长；对于大量编辑的场景（如协作文档）可能膨胀快 |
| **JSONB diff 存储** | 只存储 `{old_content, new_content, diff_ops}` | 存储体积小；天然支持反查 | 查询旧版本需要重建；diff 算法需要一致；不支持审计场景的直接读取 |

**推荐：方案 A（全量版本）**，理由：
- 消息/评论的特点是 **编辑频率低**（正常用户不会一天编辑几十次消息）
- 合规审计场景需要"当时看见了什么"——全量版本最直接
- 为实现简单，可直接复用消息表的 schema 版本化

**2.3.2 编辑 window 与撤销窗口**

这是一个产品层面的架构决策，但对数据模型有影响：

- **无限制编辑**：用户可以随时编辑历史消息 → revision 表无限增长
- **有时间窗口的编辑**（如 Discord 的 5 分钟限制） → 过期后不能编辑，但可以查看历史
- **只有撤销能力的编辑**（如 LINE 的"取消发送"在 24 小时内） → 不保存版本链，只记录最近一次覆盖

**推荐**：实现**两种模式**：
1. 标准模式：消息编辑无时间窗口限制（增加版本链），但编辑后的标识（`edited: true`）永远显示
2. 严格模式：5 分钟后不允许编辑，只能软删

这应该在配置层可切换，而非硬编码。

**2.3.3 审计日志的关联设计**

编辑历史与审计日志的关系：

```
message_revisions
  ├── id: UUID
  ├── message_id: UUID (FK → messages.id)
  ├── content: TEXT     ← 旧内容
  ├── created_at: TIMESTAMPTZ  ← 编辑发生时间
  └── edited_by: TEXT   ← 编辑者 uid

audit_log
  ├── id: UUID
  ├── action: "message.edit"
  ├── subject_id: message_revisions.id  ← 关联到 revision
  ├── actor_uid: TEXT
  └── created_at: TIMESTAMPTZ
```

这样，审计日志不存储内容 diff，而是引用 revision 记录。审核员可以通过审计日志→revision→message 的路径追溯完整编辑链。

### 方向四：用户数据组织原语

#### 文档已覆盖：✅ 无 tags/folders/labels、无批量操作、meta JSONB 无约束

#### 未覆盖的架构深度分析：

**2.4.1 标签 vs 文件夹的语义差异（影响数据模型）**

| 特征 | 标签 (Label/Tag) | 文件夹 (Folder) |
|---|---|---|
| 层次结构 | 扁平（无父子关系） | 树形（父子嵌套） |
| 一个文件属于 | 多个标签 | 一个文件夹（通常） |
| 删除行为 | 删除标签不影响文件 | 删除文件夹→文件去哪？需要决策（取消关联/软删/回收站） |
| 共享语义 | 按标签共享文件 = 按标签筛选后共享 | 按文件夹共享 = 共享子树 |
| 查询复杂度 | `file_labels` JOIN 即可 | 递归 CTE 或物化路径 |

**我的建议**：先做**标签**（扁平），不做**文件夹**（树形）。

理由：
1. 树形文件夹的共享、移动、删除的语义复杂度远高于标签
2. 标签可以用简单的 `file_labels` 连接表实现，不涉及递归查询
3. 先上线标签，验证用户是否确实需要组织能力，再决定是否做文件夹
4. 标签可以**渐进地**支持层次化——如果未来需要，可以给 `labels` 表加 `parent_id` 实现标签层次

**2.4.2 "收藏/星标"比文件夹更轻量、更高频**

从产品角度，`starred` 字段（boolean）是最快回报的功能。

技术实现：`files.starred BOOLEAN NOT NULL DEFAULT FALSE`——一条 ALTER TABLE 语句，一个索引，后端无需新表。前端可以做"已加星标"筛选器。

**架构决策**：starred 作为 file 表的直接列（而非一个连接表）是合理的——"是否收藏"是文件本身的属性（元数据），而非关系。

**2.4.3 批量操作的幂等性设计**

批量操作需要特别注意幂等性，因为客户端可能重试：

```json
// POST /v1/files/batch/delete
{
  "ids": ["uuid1", "uuid2", "uuid3"],
  "idempotency_key": "batch-delete-abc123"
}
```

后端实现策略：
1. 使用数据库事务 + 逐行操作（而不是一条 SQL 批量 DELETE），可以在某条失败时回滚整个批次
2. 幂等键确保同一批次不会执行两次
3. 返回每条的执行状态：`{"results": [{"id": "uuid1", "status": "deleted"}, {"id": "uuid2", "status": "not_found"}]}`

**架构决策**：批量操作的错误处理应该是"最佳效果"（best-effort，尽可能处理能处理的条目）还是"全有或全无"（all-or-nothing）？

**推荐**：全有或全无。如果批次中有一条失败（如找不到、权限不足），回滚整个批次。这样客户端逻辑简单：要么全部成功，要么全部失败重试。如果逐条返回混合状态，客户端需要自己处理部分失败的部分。

### 方向五：滥用预防管道

#### 文档已覆盖：✅ 举报不自动触发、无内容过滤、无 reporter 限速、消息无举报入口

#### 未覆盖的架构深度分析：

**2.5.1 管道架构——而非散落的 if-else**

这是五个方向中最需要**架构级设计**的一个。滥用预防不是单一功能，而是一个**处理管道**（pipeline）：

```
┌─────────┐   ┌──────────────┐   ┌──────────────┐   ┌───────────┐   ┌───────────┐
│ 内容创建 │ → │ 预提交检查器  │ → │ 提交后扫描器  │ → │ 审核队列   │ → │ 自动处置   │
│ (提交前) │   │ - 敏感词     │   │ - 垃圾识别   │   │ (人工)    │   │ - 自动隐藏 │
│         │   │ - 黑白名单   │   │ - 重复举报   │   │          │   │ - 用户隔离 │
│         │   │ - 速率限制   │   │ - 用户信任分 │   │          │   │ - 通知审核 │
└─────────┘   └──────────────┘   └──────────────┘   └───────────┘   └───────────┘
```

这应该是一个**可插拔**的架构，而不是硬编码的检查链。建议：

```go
// internal/moderation/pipeline.go
type Stage interface {
    Name() string
    ShouldRun(ctx context.Context, in *StageInput) bool
    Run(ctx context.Context, in *StageInput) (*StageOutput, error)
}

type Pipeline struct {
    stages []Stage
}
```

**架构决策**：预提交检查器（同步，阻止提交）和提交后扫描器（异步，触发隐藏/审核）的分离是关键。

- **同步检查器**：必须快速（<50ms），不影响用户体验；失败则拒绝提交
- **异步扫描器**：可以在内容提交后进行（5s-30s 延迟），扫描结果触发后续处置

**2.5.2 举报阈值自动隐藏的设计模式**

文档提出"≥N 个不同用户的举报时自动隐藏"。这个设计比看起来复杂：

```
问题：N=3 意味着
- 如果 3 个好友串通举报同一评论 → 即使评论内容合规，也被隐藏
- 如果 1 个攻击者用 3 个马甲举报 → 自动隐藏被滥用
```

**改进设计**：
1. 阈值基于**不同真实用户**的举报数，而非总次数（即去重 reporter_uid）
2. 审核团队可以**提升或降低阈值**（配置化）
3. 自动隐藏不等于永久隐藏——审核员可以忽略阈值解除隐藏
4. 新增 `auto_hidden_at` 和 `auto_hidden_by` 字段（记录触发自动隐藏的阈值条件）

**2.5.3 信任分层（Trust Tier）**

这是一个文档未覆盖但非常关键的架构概念。滥用预防的基石是**新用户信任度低，老用户信任度高**：

```
TrustTier 0: 刚刚注册 → 不能评论、不能发消息、不能举报
TrustTier 1: 注册 >24h + 至少 1 次正常交互 → 可以评论（但不对外可见直到审核员批准）
TrustTier 2: 注册 >7d + 至少 5 次正常交互 → 正常权限，但受速率限制
TrustTier 3: 注册 >30d + 无违规记录 → 完全信任，限速放宽
```

这不是一个短期的功能，而是一个**框架性的架构设计**——信任分/信任等级应该内建于域模型中，所有内容提交都引用用户的信任等级。

**推荐**：首先新增 `user_trust` 表（或只在内存中维护，因为 SSO 侧有用户数据），与 SSO 的用户模型结合。当前阶段不需要复杂的机器学习评分，简单的**基于行为的规则**（注册时间、举报准确率、内容违规次数）已经覆盖 80% 的滥用场景。

---

## 3. 接口设计建议

### 3.1 关键设计原则

对于这 5 个方向的接口设计，我建议统一遵循以下原则：

**原则一：向后兼容优先（Additive-only changes）**

对于已发布的 API（HTTP v1 路由和 gRPC proto），所有变更必须是**增量**（additive-only）：

- 不允许修改现有 proto message 的 field number
- 不允许移除现有的 RPC 方法
- 不允许改变现有 HTTP 路由的 path 或 method
- 可选字段使用 `optional` proto3 语法
- 新增字段使用新的 field number，不使用 reserved 的

**原则二：gRPC 和 HTTP 行为必须一致**

当前架构已有的优势——handler 层共享 service 层。新增功能时：

- 先定义 proto（作为 API 契约）
- 然后在 service 实现
- HTTP handler 和 gRPC handler 分别适配

如果 proto 与 HTTP JSON 不兼容的场景（如文件上传用 multipart），在 HTTP handler 特殊处理，但业务逻辑仍然在 service 层。

**原则三：新增的 API endpoint 前缀与现有模式一致**

```
/v1/conversations/         (新增群组)
/v1/files/batch/           (新增批量操作)
/v1/admin/events/          (新增事件管理)
/v1/moderation/            (新增审核管理)
```

### 3.2 是否需要引入新的抽象层？

**需要两个新抽象层：**

**① Moderation Pipeline（审核管道抽象层）**

当前 aero-id 的预检查逻辑分散在各处：
- `middleware/ratelimit.go` — 速率检查
- `comment/service.go` — 举报创建
- `message/service.go` — 消息发送（无任何预检查）

建议新增 `internal/moderation/` 包，提供 `Pipeline` 接口，所有内容提交通过该管道。这不意味着立即上敏感词检查，而是先建立抽象——即使只有"通过"一个 stage，也是可扩展的。

**② Policy Engine（策略引擎抽象层）**

当前所有业务规则是硬编码。建议新增 `internal/policy/` 包，提供可配置的策略检查。第一阶段可以很轻量：

```go
// internal/policy/policy.go
type Policy interface {
    Name() string
    Evaluate(ctx context.Context, input any) (*Result, error)
}

type ConfigurablePolicy struct {
    rules []Rule  // 从 config 解析的规则列表
}
```

例如，举报阈值、信任等级、编辑时间窗口都应该是策略配置，而非硬编码常数。

### 3.3 保持向后兼容性的具体措施

| 变更类型 | 兼容策略示例 |
|---|---|
| 新增 conversation_id 到 Message proto | 新字段可选（`optional`），旧客户端忽略 |
| 新增 event_types 校验 | 先 soft-warn（日志警告）一段时间后，再 hard-fail |
| 新增编辑历史 endpoint | 新路由，不影响现有编辑/消息获取逻辑 |
| 新增标签/文件夹到 File | 文件表加可空列，旧客户端不感知 |
| 新增 moderation pipeline | 默认所有 stage 返回"跳过"，不影响现有流程 |

---

## 4. 技术选型评估

### 4.1 各方向的技术栈需求

| 方向 | 是否需要新技术栈 | 评估 |
|---|---|---|
| Conversation 模型升级 | **不需要新栈**。Postgres + UUIDv7 已足够。群组通知广播可在现有 Outbox 机制上扩展 | 纯架构变更 |
| Webhook 投递可靠性 | **不需要新栈**。Postgres seq 实现 aggregate 序列号；新增管理 API 在现有 admin handler 上扩展 | 纯架构变更 |
| 内容编辑历史追踪 | **不需要新栈**。新表 + 审计日志 + proto 字段 | 纯架构变更 |
| 用户数据组织原语 | **不需要新栈**。标签表和连接表在 Postgres 内完成 | 纯架构变更 |
| 滥用预防管道 | **可能需要引入**。如果要上敏感词扫描或机器学习评分，可能需要引入外部服务（异步调用模式） | 有技术选型需要 |

**结论：4/5 的方向不需要引入任何新的技术栈或外部依赖。** 唯一需要技术选型的是 Abuse Prevention Pipeline 中的内容过滤部分。

### 4.2 内容过滤的技术选型

如果决定进入"自动内容过滤"阶段，有三种技术路径：

| 选项 | 描述 | 优势 | 劣势 |
|---|---|---|---|
| **A: 字符串匹配** | 内部实现敏感词/正则列表（嵌入配置或 DB 表） | 零外部依赖；延迟可控（<1ms）；离线可用 | 无法识别变体（"f@ke" vs "fake"）；无法处理语义垃圾；维护敏感词列表是持续的工作 |
| **B: 规则引擎集成** | 集成 Rego/Open Policy Agent 做内容策略评估 | 策略与代码分离；有完善的工具生态系统 | 引入新依赖和运行时；需要团队学习 Rego；用例可能不足以 justify 这个复杂度 |
| **C: 外部 API** | 调用 Google Perspective API / AWS Comprehend / 自建模型 | 语义理解；覆盖率广 | 延迟不确定（100ms-1s）；费用随用量增长；可用性依赖外部服务；数据隐私问题 |

**推荐**：先做 A（字符串匹配），预留 B 或 C 的接口抽象：

```go
// internal/moderation/filter/filter.go
type ContentFilter interface {
    Filter(ctx context.Context, content string) (*FilterResult, error)
}

// 第一阶段：敏感词过滤器
type SensitiveWordFilter struct {
    words []string // 从配置/DB 加载
}

// 第二阶段可扩展：集成外部 API 作为另一个 Filter 实现
type ExternalAPIFilter struct {
    client SomeAPIClient
}
```

理由：
1. 第一阶段（P1-P2）只需要防住明显的垃圾和敏感内容，字符串匹配足够
2. 预留接口抽象，未来升级到方案 B 或 C 时不需要改动调用方
3. 外部 API 的费用和数据隐私议题需要产品决策，不是纯技术决策

### 4.3 自建 vs 采购决策

对于这 5 个方向，**没有需要采购的场景**。所有功能都应该自建（build）：

- 它们在 aero-id 的域边界内，与现有数据模型紧密耦合
- 外部方案（如 webhook 投递平台、内容审核 API）要么引入同质化依赖，要么数据隐私不合规
- aero-id 的差异化价值在于对这些领域的垂直整合

**唯一的例外**：如果路线图进入"多语言敏感词检测"或"图片内容审核"，建议评估第三方 API。这超出了当前 scope。

---

## 5. 实施路线图

### 5.1 优先级建议

基于以下三个维度交叉评估：
- **产品价值**：对终端用户的直接影响
- **架构价值**：对系统长期可维护性的贡献
- **实施风险**：代码变更量和回归风险

| 方向 | 产品价值 | 架构价值 | 实施风险 | 优先级 |
|---|---|---|---|---|
| Webhook 投递可靠性 | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 低（模块独立，无 schema 变更） | **P0** |
| Conversation 模型升级 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 高（涉及 20+ 文件变更 + 迁移） | **P1** |
| 滥用预防管道（第一阶段） | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 中（新包 + 配置 + API） | **P1** |
| 内容编辑历史追踪 | ⭐⭐⭐ | ⭐⭐⭐ | 低（新表 + service 逻辑扩展） | **P2** |
| 用户数据组织原语 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | 中（新表 + proto + 批量 API） | **P2** |

我调整了文档中的优先级分配——将 Webhook 投递可靠性提升为 **P0** 而非 P1，理由：
- 这是**平台信任的基础设施**——没有幂等性保证之前，外部集成方无法信任事件投递
- 实施风险最低（outbox 模块内部改进，不跨模块）
- 为未来支持事件重放和 schema 演进奠定基础

### 5.2 阶段划分

```
                    ┌─────────────────────────────────────┐
                    │   Phase 0 (Sprint 1)                │
                    │   Webhook 投递可靠性 (P0)            │
                    │   + 最小滥用预防：reporter 限速 (P1)  │
                    │   ≈ 1 sprint                         │
                    └─────────────────────────────────────┘
                                       │
                    ┌──────────────────┴──────────────────┐
                    │                                     │
                    ▼                                     ▼
    ┌─────────────────────────────┐     ┌─────────────────────────────┐
    │   Phase 1 (Sprint 2-3)     │     │   Phase 1 (Sprint 2-3)     │
    │   Conversation 模型升级(P1)  │     │   滥用预防管道(P1)          │
    │   - conversations 表        │     │   - 内容 pipeline 框架     │
    │   - 群组消息               │     │   - 自动隐藏阈值           │
    │   - 通知广播适配           │     │   - 信任等级框架           │
    │   ≈ 2 sprints              │     │   - 消息举报端点           │
    └─────────────────────────────┘     └─────────────────────────────┘
                                       │
                    ┌──────────────────┴──────────────────┐
                    │                                     │
                    ▼                                     ▼
    ┌─────────────────────────────┐     ┌─────────────────────────────┐
    │   Phase 2 (Sprint 4-5)     │     │   Phase 2 (Sprint 4)       │
    │   内容编辑历史追踪(P2)       │     │   用户数据组织原语(P2)      │
    │   - message_revisions 表    │     │   - labels + file_labels   │
    │   - comment_revisions 表    │     │   - starred boolean       │
    │   - 审计日志扩展            │     │   - 批量删除 API          │
    │   ≈ 1 sprint               │     │   ≈ 1 sprint              │
    └─────────────────────────────┘     └─────────────────────────────┘
```

### 5.3 风险点和缓解策略

| 风险 | 影响方向 | 等级 | 缓解措施 |
|---|---|---|---|
| Conversation 升级破坏现有 1:1 消息查询 | 方向一 | **高** | 方案 C（混合模型）确保旧查询路径不变；逐步迁移；灰度部署 |
| 群组通知广播导致 Outbox 写放大 | 方向一 | 中 | 单条消息 + 接收者列表（而非每人一条记录）；通知收件人表用异步 batch insert |
| 滥用预防管道误伤正常用户 | 方向五 | **高** | 所有自动处置**必须**有手动撤销路径；预提交阶段的拒绝需要清晰的错误信息；配置化阈值 |
| 敏感词过滤导致性能下降 | 方向五 | 中 | 只对 content 文本做过滤（不做图像 OCR）；使用 Aho-Corasick 算法（O(n) 多模式匹配）而非多次正则；加缓存 |
| 编辑历史存储膨胀 | 方向三 | 低 | 消息/评论的自然编辑频率低；可设最大版本数（如 20 条），超出后删除最早的版本 |
| 标签/文件夹使用率低，投入产出比差 | 方向四 | 低 | 先做标签（轻量）+ starred（超轻量），根据遥测数据决定是否做文件夹 |

### 5.4 特别风险：方向一 + 方向的交互耦合

Conversation 模型升级（方向一）和滥用预防管道（方向五）**存在交互耦合**，如果安排在同一个 sprint 开发，需要注意：

1. 群组消息的滥用预防更复杂——恶意用户可以在群组中@所有人发送垃圾
2. 群组举报需要区分"举报整个会话"和"举报单条消息"
3. 信任等级在群组场景中应该影响"是否可以在群组中发言"而不是"是否可以看到群组"

**建议**：在 Phase 1 的两个并行轨道之间建立清晰的接口协议——`moderation.Pipeline` 接口应该设计为可以独立于 conversation 逻辑运行。

---

## 6. 额外发现 — 文档未覆盖但值得关注的架构问题

在阅读代码库和这份分析文档后，我注意到两个文档未覆盖但值得关注的问题：

### 6.1 事件 Schema 演化（Event Schema Evolution）

Outbox 事件的 `Payload` 是 `json.RawMessage`，没有任何 Schema 注册表。这意味着：
- 如果某个事件的 payload 结构发生变化，旧的死信事件和新的事件格式不同，消费者需要同时兼容
- 无法做 Schema 版本校验

**建议**：在 Webhook 投递可靠性（P0）中一并解决，在 event payload 中添加 `schema_version` 字段。

### 6.2 冷启动回放问题

如果服务重启，SSE 客户端通过 `last_event_id` 重连，但 Outbox 事件只存储了 payload，没有存储还原当前状态所需的**状态快照**。这意味着客户端重连后只能看到新事件，无法重建状态。

**这不是当前的问题**，因为 SSE 主要用于推送新通知，但在未来做"服务重启后用户看到自己是否有新消息"的场景中，可能需要考虑状态快照的机制。

---

## 总结

这份分析文档识别的 5 个方向质量很高，代码证据链完整，分析逻辑严谨。我的架构分析补充了：

1. **每种方案的多种实现选项**，以及推荐理由
2. **未覆盖的架构复杂性**（如群组通知广播、顺序保证方案对比、信任分层）
3. **接口设计原则**，重点在后向兼容
4. **技术选型评估**——4/5 方向不需要新技术栈
5. **调整后的优先级**——Webhook 投递可靠性提升为 P0
6. **分阶段实施路线图**（5 sprints）
7. **风险点**——特别是方向一和方向五的交互耦合
8. **两个额外发现的架构问题**——event schema 演化和冷启动回放

下一步如果可以，我建议从 **Phase 0（Webhook 投递可靠性）** 开始做架构设计文档，然后由我来评审具体的技术方案。
