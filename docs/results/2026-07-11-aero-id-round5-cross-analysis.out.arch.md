以下是我基于 `2026-07-11-aero-id-round5-cross-analysis.md` 文档，结合当前代码库特征和工程约束的完整架构分析。

---

# aero-id 架构分析报告：五个未被覆盖的高价值扩展方向

> **分析日期**: 2026-07-12
> **分析范围**: `docs/requirements/2026-07-11-aero-id-round5-cross-analysis.md` 识别的 5 个方向
> **参考基线**: `main` (2026-07-11)，247 个 Go 文件，38 个 gRPC RPC，全 HTTP 对等端点

---

## 一、架构评估

### 1.1 当前架构的核心优势

aero-id 当前的分层架构（handler → service → repository/storage/sso）经过多轮迭代后已相当成熟，主要体现在以下几个维度：

| 维度 | 优势 | 与 5 个方向的关系 |
|------|------|------------------|
| **传输对等** | HTTP + gRPC 共享同一套 service，接口扩展时只需改动 service + 两个 handler | 5 个方向的新端点均可直接套用此模式 |
| **事务性 Outbox** | 事件与数据变更在同一事务中提交，异步 relay 分发 | 草稿同步、提及通知、批量操作的审计事件均可复用 |
| **幂等性中间件** | 防重复提交的基础设施已在中间件层就绪 | 批量操作的部分失败语义需重新定义幂等范围 |
| **SSE Hub** | 多订阅者实时推送，支持 `last_event_id` 断线重放 | 草稿跨设备同步、提及通知推送天然适配 |
| **GC Sweeper** | `storage/gc/sweeper.go` 的定时清理模式 | 草稿过期清理可直接复用 |
| **UserDirectory** | `sso.Client` 已批量缓存用户查询 | @提及的用户解析直接调用，无需新 SSO 投入 |

### 1.2 架构债务与技术债

虽然 5 个方向本身是"新增功能"而非"修复缺陷"，但它们在代码级暴露了以下**结构性的架构债务**：

**债务 1：`Meta` JSONB 字段的语义漂移**

`domain.Message.Meta` 和 `domain.File.Meta` 是 `map[string]any` 类型，当前已用于存储前端元数据。若继续将引用/转发信息也塞入 `Meta`（如交叉分析文档建议的），将导致：

- `Meta` 的 schema 没有版本管理——前后端需隐式约定 key 名称和类型
- 无法通过 SQL 查询引用关系（JSONB 内的字段不能直接建立外键或索引）
- 审计日志中难以区分"用户写入的 meta"和"系统写入的 meta"

> **建议**: 在扩展方向三（引用/转发）实施前，先为 `Meta` 建立类型安全的子结构体，或新建独立字段/关系表。参见下方 §2.3 的分析。

**债务 2：`canReadFile` 的权限检查不完整**

交叉分析文档已指出 `canReadFile` 不检查 `file_shares` 表——这是方向五（关系遍历）的前置依赖，也是一个独立的正确性缺陷。当前 `canReadFile` 只检查 `owner_uid` 和 `visibility='public'`，但通过 `file_shares` 获得授权的用户也会被拒绝。

> **影响**: 方向五的 `GET /v1/files/:id/related` 在返回 `shares` 列表时，如果调用 `canReadFile` 判断当前用户是否有权查看文件详情，会漏过被分享用户。需要修复或增加一个独立的 `hasAccess` 检查。

**债务 3：repository 层缺少批量操作模式**

当前所有 repository 方法都是单实体操作（`Get`、`Create`、`Delete`）。唯一的"批量"方法是 `MarkConversationRead`——但这本质上是基于会话的"范围操作"，而非"任意 ID 集合操作"。

> **影响**: 方向四（批量操作 API）需要在 repository 层新增 `DeleteMany(ctx, ids, uid)`、`ShareMany(ctx, fileIDs, targetUID)` 等方法。这些方法应使用 `WHERE id IN (...)` 实现，但需要注意 IN-clause 的极限（PostgreSQL 默认 65K parameters，实际业务约束应远低于此）。

**债务 4：outbox event 不支持 trace 传播**

已在 `2026-07-12` 的文档中独立指出。这影响了方向二（@提及通知）和方向四（批量操作审计）的端到端可观测性——异步路径中的通知生成和审计写入将成为孤儿 span。

### 1.3 关键设计决策评估

| 决策 | 现状 | 对 5 个方向的影响 |
|------|------|------------------|
| UUIDv7 作为主键 + 游标分页 | ✅ 正确 | 草稿表、提及表、批量操作的结果列表中均可复用 |
| 软删除 (`gorm.DeletedAt`) | ✅ 正确 | 引用/转发中需要区分"引用已删除的消息" vs "引用不存在的消息" —— 软删除的 deleted_at 非空即可判断 |
| 无 Redis / 仅内存缓存 | ⚠️ 够用但受限 | 草稿表的过期清理若高频触发，内存扫描可能成为瓶颈（当前 user cache 60s TTL 可接受，但草稿过期检查是表级扫描） |
| 无消息队列 (仅 outbox) | ✅ 正确 | outbox 足够承载提及通知和草稿同步事件的异步分发；批量操作的结果通知也可以通过 outbox 发送 |
| Service 层单文件 (< 500 行) | ✅ 符合约束 | 但方向二（@提及）和方向四（批量操作）可能会使 service 文件接近 400 行的 refactor 阈值 |

---

## 二、扩展方向评估与深化

本节基于交叉分析文档的 5 个方向，从架构层面深化分析，补充原文档未充分讨论的决策点和权衡。

### 2.1 方向一：草稿与自动保存 (P1)

**原文档结论正确，但有以下补充：**

#### 存储模型的选择权衡

原文档建议 `drafts` 统一表 + JSONB `content`。我补充三个可选方案：

| 方案 | 存储 | 优点 | 缺点 | 推荐度 |
|------|------|------|------|--------|
| **A) 统一草稿表** | `drafts(content JSONB)` | 简单，单一查询端点，跨实体类型查询容易 | JSONB 无 schema 约束，前端兼容成本高 | ⭐⭐⭐ 推荐 MVP 阶段 |
| **B) 各实体独立草稿表** | `message_drafts`、`comment_drafts` 等 | 类型安全，可按实体类型独立分库分表 | N 个实体 = N 套 CRUD | ⭐⭐ 不推荐 |
| **C) 复用现有表 + draft flag** | `messages` 加 `status='draft'` | 无新表，消息模型变化最小 | 软删除 + 草稿状态组合复杂度高；游标分页需排除草稿 | ⭐ 不推荐 |

**推荐方案 A + 以下优化**：

```sql
-- 增加 created_by 区分"前端自动保存"和"用户手动保存"
-- 增加 ttl 字段控制过期时间（替代 fixed 7 days）
CREATE TABLE drafts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid   TEXT NOT NULL,
    draft_type  TEXT NOT NULL,        -- 'message' | 'comment' | 'file_metadata'
    subject_id  UUID,                 -- 编辑已有资源时关联的资源 ID
    content     JSONB NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    saved_by    TEXT NOT NULL DEFAULT 'manual',  -- 'auto' | 'manual'
    ttl         INTERVAL NOT NULL DEFAULT '7 days',
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(owner_uid, draft_type, COALESCE(subject_id, '00000000-0000-0000-0000-000000000000'))
);
```

#### 过期清理策略

原文档提到"周期性清理"，需明确清理策略的两种选择：

- **懒清理（推荐）**：读取草稿时检查 `expires_at`，过期则忽略并标记待清理。GC sweeper 每 30 分钟扫描清理一次。优点：不引入额外定时任务，复用 `storage/gc/sweeper.go` 的模式。
- **主动清理**：独立的 cron ticker 每 5 分钟 `DELETE FROM drafts WHERE expires_at < now()`。优点：及时释放存储。缺点：需要新 ticker 协程。

#### 与现有基础设施的整合深度

原文档列出了可复用的组件，但缺少**版本冲突处理**的讨论：

> **并发编辑冲突**：用户 A 在桌面端和手机端同时打开同一个草稿。桌面端保存 v1 → 手机端保存 v1（期望 v2）→ 冲突。处理方式有两种：
> - 乐观锁（推荐）：`UPDATE drafts SET content=$1, version=version+1 WHERE id=$2 AND version=3`，affected=0 时返回 409 Conflict，客户端需重新加载最新版本
> - 最后写入胜出：不检查版本，静默覆盖。简单但数据丢失风险高

推荐乐观锁，这与 idempotency key 的语义互补——idempotency key 防**网络重试**的重复提交，乐观锁防**并发编辑**的覆盖。

---

### 2.2 方向二：@提及与用户标记 (P1)

**核心挑战：提及解析的时机和粒度**

原文档的提及解析方案是正则匹配 `@([^\s,.;:!?，。；：！？]+)`，这在纯文本内容上工作良好，但在以下场景有歧义：

1. **Markdown 或富文本中的 @**：`@` 可能出现在代码块、链接文本中。需要前端在保存时标注哪些 `@` 是真正的提及，而非服务端全量解析。
2. **提及自动补全**：原文档提到"依赖 SSO 新增 `SearchUsers` RPC"——这是方向二的一个**外部依赖项**。如果 SSO 不支持用户搜索，自动补全只能在前端做本地前缀匹配（仅匹配已缓存的联系人列表）。

**推荐的双轨策略**：

```
短期（无 SSO 搜索支持）：
  - 服务端在 Create/Edit 时解析 content 中的 @pattern
  - 验证被提及的 uid 是否存在（通过 UserDirectory.GetUsers）
  - 但自动补全仅限当前用户的"最近联系人列表"（从 messages 表中提取）
  - 通知类型扩展：新增 mention 通知类型

长期（SSO 支持 SearchUsers 后）：
  - 提及自动补全端点 GET /v1/users/suggest?q=prefix
  - 服务端透传到 SSO 的 SearchUsers RPC
  - 可缓存高频搜索结果
```

#### 提及通知与现有通知系统的叠加

原文档正确指出提及和回复通知是正交且可叠加的。但有一个**重复通知**的边界案例需要设计：

> 用户 A 评论 "回复 @用户B：你是对的"
> - 如果父评论是用户 B 的 → B 收到 `comment.reply` 通知（现有行为）
> - 同时 B 被 @提及 → B 也收到 `mention` 通知（新增行为）
> - **B 会收到两条通知**（一条是回复通知，一条是 @通知）

解决方案：
- **去重**（推荐）：在 `dispatcher` 中检查 `subject_type` + `subject_id` 是否已为同一被通知者创建过通知（基于 `notification_reply` 表已有逻辑扩展）——如果已收到 `comment.reply` 则不再创建 `mention`，反之亦然
- **保留两条**：简单但用户体验差
- **合并为一条**：通知内容同时包含"回复"和"@提及"两种上下文——复杂，且需要新通知类型

推荐去重方案：在下游 dispatcher 中，对同一 `(uid, subject_type, subject_id)` 组合仅发一条通知。如果同时匹配 `reply` 和 `mention` 规则，以 `reply` 为准（回复的上下文更优先）。

#### SSO 依赖的断路器策略

提及解析依赖 SSO 的 `GetUsers` 批量验证。如果 SSO 短暂故障：

- 严格模式（推荐）：SSO 不可用 → 拒绝消息/评论创建，返回 503。因为无法验证被提及用户是否存在，不能冒险将消息发给不存在的用户。
- 宽松模式：SSO 不可用 → 跳过提及验证，仅保存原始文本（@提及降级为纯文本）。消息创建成功，但不触发提及通知。

`UserDirectory.GetUsers` 已有缓存 + 断路器 + stale cache 回退，但**stale cache 只能回答"这个用户之前见过"**，无法回答"这个新用户是否存在"。因此严格模式更安全。

---

### 2.3 方向三：内容引用与转发 (P2)

**原文档的方案合理，但需深化以下两点：**

#### 引用模型的存储决策

原文档建议将引用信息存入 `messages.meta` JSONB。我对此方案提出**审慎反对**：

| 准则 | JSONB in meta | 独立关联表 |
|------|--------------|-----------|
| 查询"哪些消息引用了 msg-X" | 全表 JSONB 扫描，不可索引 | `SELECT * FROM message_quotes WHERE quoted_id = ?` 走 B-Tree |
| 软删除兼容性 | 被引用消息软删除后，JSONB 中的 id 仍存在——显示"原文已删除"需额外 JOIN | 同理，但可外键 + ON DELETE SET NULL |
| 数据完整性 | 无约束——引用了一个不存在的消息 ID 无法在 DB 层检测 | 外键可选，建议不加（软删除场景复杂） |
| 承载更多元数据 | 容易扩展 | 需要 migration |

**推荐：混合方案**

- 在 `messages` 表新增 `quoted_message_id UUID` 可空字段——用于最常见的"引用单条消息"场景（回复引用）
- `Meta` JSONB 中的 `forwarded_from` 结构保留——转发涉及更多元数据（原始对话 ID、转发者、转发时间），且查询频率低
- 不做独立 `message_quotes` 表——YAGNI，除非未来需要"引用墙"等社交功能

```go
// domain 扩展
type Message struct {
    ID              ID
    FromUID         string
    ToUID           string
    Kind            MessageKind
    Content         string
    FileID          *ID
    QuotedMessageID *ID         // 新增：引用的消息 ID（回复引用场景）
    ReadAt          *time.Time
    EditedAt        *time.Time
    Meta            map[string]any  // forwarded_from 继续放这里
    CreatedAt       time.Time
}
```

#### 引用权限的设计原则

引用权限是方向三最复杂的部分。核心原则：

1. **引用者必须有对被引用消息的读权限**。如果消息是 1:1 私信，只有对话双方可以引用该消息。
2. **引用的消息被删除后**：引用方显示"原文已被删除"（根据软删除的 `deleted_at` 字段判断）。不应级联删除引用方消息。
3. **转发可见性**：转发消息时，`forwarded_from.original_conversation_uid` 是否应该暴露给转发接收方？如果用户 A 转发用户 B 的消息给用户 C，C 应看到"转自 B"还是仅"转自某用户"？建议设计为：默认暴露原始发送者 uid，但允许 SSO 层通过 user preference 控制"是否允许他人转发我的消息时显示我的身份"。

---

### 2.4 方向四：通用批量操作 API (P2)

**架构中最具挑战的方向。原文档的分析准确，补充以下架构决策：**

#### 部分失败模型的设计空间

原文档推荐非事务性 + 详细结果。这是正确的。但需要明确**幂等性语义**：

**场景**：客户端发送 `POST /v1/files/batch-delete`，包含 10 个 ID。服务端成功删除 7 个，第 8 个处理时服务崩溃。客户端重试（使用相同的 Idempotency-Key）：

| 方案 | 行为 | 优缺点 |
|------|------|--------|
| **A) 已成功的不重复执行** | 幂等键映射到前 7 个已成功结果 + 后 3 个尝试完成。重试时取幂等缓存的前 7 个 + 重新执行后 3 个 | ✅ 用户体验好。但幂等键的缓存需支持增量更新——当前 `idempotency.Store` 只缓存整个响应，不适合部分更新 |
| **B) 整个请求重新执行** | 幂等键等于请求体 hash。重试时因为前 7 个已删除，`Delete` 对这些 ID 返回 `not_found`→ 最终结果: 7 succeeded + 3 not_found | ✅ 实现简单，当前幂等中间件可直接使用。但客户端需要理解 `not_found` ≠ 失败 |
| **C) 应用层幂等** | 不为批量操作提供 idempotency key 支持。客户端自行处理重试 | ❌ 否定已有基础设施的价值 |

**推荐方案 B**，因为：
- 零变更到现有幂等中间件
- 对于 DELETE 操作，重复删除是安全的
- 对于 UPDATE/SHARE 等操作，幂等键映射到请求体 hash，相同请求返回相同结果

#### 批量操作的限流权重要求变更

当前 `middleware.RateLimit` 按请求计数。对于批量端点，一个包含 100 个 ID 的请求应消耗 100 单位的限流配额。这需要在中间件层支持"自定义权重"：

```go
// 中间件扩展
type RateLimitOption func(*RateLimiter)
func WithCostPerRequest(cost int) RateLimitOption
```

或者更简洁的方式：批量端点显式声明放大系数，在 handler 中调用 `limiter.AllowN(ctx, len(ids))`。

#### Repository 层的批量方法设计

```go
// 在现有 repository 基础上新增
type FileRepository interface {
    // 已有
    Create(ctx, file) error
    Get(ctx, id) (*domain.File, error)
    Delete(ctx, id, uid) error
    
    // 新增批量
    DeleteMany(ctx, ids []uuid.UUID, uid string) ([]BatchResult, error)
    ShareMany(ctx, fileIDs []uuid.UUID, targetUID string) ([]BatchResult, error)
}

type BatchResult struct {
    ID     uuid.UUID
    Status BatchItemStatus  // succeeded | not_found | forbidden | failed
    Error  string            // 详细错误信息（空字符串 = 成功）
}
```

`DeleteMany` 的实现使用 `WHERE id IN (?) AND owner_uid = ?`，然后返回实际删除的行数。通过比对传入 ID 集合和实际删除的行集合，推断哪些 ID 是 `not_found` 或 `forbidden`。

---

### 2.5 方向五：跨域关系遍历 API (P1)

**原文档分析完整，但缺少一个关键架构决策：如何避免 N+1 查询风暴。**

关系遍历 API 天然是多表查询。一个 `GET /v1/files/:id/related` 可能涉及：

1. `messages WHERE file_id = ?`（引用消息）
2. `comments WHERE target_type='file' AND target_id = ?`（评论）
3. `file_shares WHERE file_id = ?`（分享授权）
4. `file_versions WHERE file_id = ?`（版本历史，未来）

**三种实现策略的权衡：**

| 策略 | 实现 | 延迟 | 一致性问题 | 推荐场景 |
|------|------|------|-----------|---------|
| **A) 并行查询（推荐）** | 使用 `errgroup` 同时查询 4 个域，最多 1 RTT 的等待时间 | 最优（平行 1 RTT） | 无——所有查询在同一事务快照中 | 默认策略 |
| **B) 串行查询** | 逐个查询，任一失败即返回 | 最差（4 RTT） | 无 | 数据库连接池小的时候保底 |
| **C) 预计算 + 缓存** | 写入时维护关系计数/摘要（如 `comments` 表已有 `ReplyCount`） | 最优（O(1)） | 最终一致性（写入到关系缓存有延迟） | 高频访问路径（如文件列表页显示评论数） |

**推荐组合：** 策略 A 用于实时 API（`/related` 端点），策略 C 用于列表页的摘要信息（文件列表中的评论数、引用数——这些应在文件写入时更新冗余字段）。

#### `include` 参数的设计

原文档提到 `?include=messages,comments` 参数，这是正确的。但需要考虑**错误隔离**：如果 `messages` 查询超时，是否应该返回 `comments`、`shares` 和 `versions` 部分成功？

建议：**不隔离**。任何一个子查询失败 → 整个 `/related` 返回 500。理由：
- 关系数据是聚合视图的一部分，部分返回会给客户端带来复杂的渲染状态
- 如果某个域超时，说明数据库或网络有问题，返回错误让客户端降级（例如隐藏"相关"面板）比返回不完整数据更安全
- 但如果是因为**权限不足导致的 404**（某个子查询返回空集而非错误），这属于正常行为，不应视为失败

#### `canReadFile` 修复前置条件

方向五的前置依赖是修复 `canReadFile` 不检查 `file_shares` 的问题。当前 `canReadFile` 的逻辑大致是：

```go
func canReadFile(file *domain.File, actorUID string) bool {
    if file.Visibility == domain.VisibilityPublic {
        return true
    }
    return file.OwnerUID == actorUID
}
```

需要改为：

```go
func canReadFile(ctx context.Context, file *domain.File, actorUID string) bool {
    if file.Visibility == domain.VisibilityPublic {
        return true
    }
    if file.OwnerUID == actorUID {
        return true
    }
    // 新增：检查 file_shares
    return fileShareRepo.HasAccess(ctx, file.ID, actorUID)
}
```

这个变更意味着 `canReadFile` 不再是一个纯函数——它需要 repository 依赖。影响范围：
- 所有现有调用点需要注入 `fileShareRepo`（或一个 `AccessChecker` 接口）
- service 层的 `canReadFile` 方法签名需要变更

---

## 三、接口设计原则

### 3.1 新增 API 的设计规范

基于已有的 HTTP + gRPC 对等模式，5 个方向的新 API 应遵循以下规范：

| 规范 | 说明 | 示例 |
|------|------|------|
| **批量操作返回值统一** | 所有批量端点返回同样的 result list 结构 | `{results: [{id, status, error}], total, succeeded, failed}` |
| **关系遍历的 include 参数** | 客户端选择需要的域，默认全返回 | `?include=messages,comments,shares` |
| **草稿在创建时同步** | 不要求幂等性（幂等由 idempotency-key 在客户端自行控制），但接口设计为 upsert 语义 | `PUT /v1/drafts` 而不是 `POST /v1/drafts` |
| **提及嵌入通知通道** | 不单独创建 `GET /v1/mentions` 端点，而是扩展通知系统的查询范围（`?type=mention`） | 复用 `GET /v1/notifications?type=mention` |

### 3.2 是否需要新的抽象层

| 方向 | 建议抽象层 | 说明 |
|------|-----------|------|
| 方向一（草稿） | `internal/draft/` 新包 | 独立的 domain + repository + service。草稿是"临时数据"，生命周期和处理逻辑与正式实体不同 |
| 方向二（@提及） | `internal/mention/` 新包 | 提及的解析、验证、通知触发是完整子域。但可以简单地在 `internal/comment/` 和 `internal/message/` 中各加一个 `parseMentions` 方法——取决于代码行数是否触发 500 行约束 |
| 方向三（引用/转发） | 扩展现有 model，无需新包 | 引用是消息/评论的"额外属性"，不独立存在。在 `domain.Message` 和 `domain.Comment` 上加字段即可 |
| 方向四（批量操作） | 不新增包；在现有 repository 上扩展方法 | 批量是"重复调用"的优化版本，不是独立域 |
| 方向五（关系遍历） | `internal/graph/` 新包 | 关系遍历是整个系统的"查询视角"，跨越所有域。独立的包可以避免循环依赖 |

**关于新包的架构风险**：新增 `internal/draft/`、`internal/mention/`、`internal/graph/` 三个包是安全的——它们只依赖 `domain` 和 `repository`，不引入循环依赖。但需注意 `internal/graph/` 可能依赖多个 repository（file、message、comment、file_share）——这是合理的，关系遍历天然是多域聚合。

### 3.3 向后兼容性

5 个方向全部新增——没有现有端点需要修改，因此向后兼容性风险为零。唯一的例外是 `domain.Message` 和 `domain.Comment` 的 struct 扩展会改变 `encoding/json` 的输出（新增的零值字段会出现在 JSON 中），但这是非破坏性的——旧客户端会忽略未知字段，Go 的 `json:"omitempty"` 标签可以控制零值字段的序列化。

---

## 四、技术选型

### 4.1 不需要引入的新技术栈

5 个方向在当前技术栈内均可实现：

| 方向 | 现有依赖 | 新依赖 |
|------|---------|--------|
| 草稿 | Postgres (GORM), GC sweeper | 无 |
| @提及 | Postgres, SSO (UserDirectory), Outbox, SSE Hub | 无（SSO 搜索是可选的外部依赖，不是 aero-id 自身的技术选型） |
| 引用/转发 | Postgres | 无 |
| 批量操作 | Postgres, Idempotency Key, Rate Limit | 无 |
| 关系遍历 | Postgres (已有索引) | 无 |

### 4.2 需要升级的现有依赖

| 组件 | 需要升级 | 原因 |
|------|---------|------|
| `idempotency.Store` | 中等变更 | 方向四批量操作需要支持"同一幂等键映射到多个子操作组合结果"——当前实现是 key→response 的简单映射，需要扩展为 key→[response] 的映射或支持增量追加 |
| `middleware.RateLimit` | 小变更 | 方向四需要支持"自定义消耗权重"——当前固定每个请求消耗 1 单位限流配额，批量端点需要消耗 N 单位 |
| `domain.Meta` | 类型安全化 | 方向三引用/转发建议使用类型化的 struct 替代裸 `map[string]any` |

### 4.3 自建 vs 依赖外部

方向二（@提及）面临的唯一外部依赖是 SSO 的 `SearchUsers` RPC。这是一个典型的"自建 vs 依赖外部"决策：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **依赖 SSO SearchUsers** | SSO 拥有用户数据的权威来源，搜索准确 | SSO 团队需开发该 RPC；aero-id 上线等待 SSO |
| **自建用户索引表** | 独立演进，不阻塞 | 数据同步复杂度（需要事件驱动同步 SSO 用户变更） |
| **混合（推荐）** | SSO SearchUsers 存在时用 SSO，不存在时退回到从 `messages`/`comments` 表中提取"最近联系人"本地匹配 | 本地匹配覆盖不全（只能找到有过交互的用户） |

**推荐混合策略**：服务端提及验证走 `UserDirectory.GetUsers`（已存在），前端自动补全优先走本地已缓存的联系人列表。不阻塞 SSO 未提供 SearchUsers 的情况。

---

## 五、实施路线图

### 5.1 优先级排序

基于"用户价值 × 技术风险 × 依赖性"三维评分：

| 方向 | 用户价值 (1-5) | 技术风险 (1-5) | 依赖项数量 | 综合优先级 |
|------|---------------|---------------|-----------|-----------|
| 方向一（草稿） | 5 | 1 | 0 | **P0** |
| 方向五（关系遍历） | 4 | 2 | 1（修复 canReadFile） | **P1** |
| 方向二（@提及） | 4 | 3 | 1（SSO 搜索可选） | **P1** |
| 方向四（批量操作） | 3 | 4 | 2（幂等扩展 + 限流扩展） | **P2** |
| 方向三（引用/转发） | 3 | 1 | 0 | **P2** |

### 5.2 阶段划分

```
Phase 0 —— 前置修复（1-2 天，独立于任何方向）
├── 修复 canReadFile 不检查 file_shares
├── 为 domain.Meta 引入类型安全子结构（quoted_message, forwarded_from 等）
├── 将 trace context 注入 outbox Event（解决异步路径追踪断裂）
└── 新增 domain/types.go 中 BatchResult 类型

Phase 1 —— 草稿 + 关系遍历（5-8 天）
├── 方向一（草稿）:
│   ├── 迁移: CREATE TABLE drafts
│   ├── 新增 internal/draft/ 包（domain, repository, service）
│   ├── HTTP: PUT /v1/drafts, GET /v1/drafts, DELETE /v1/drafts/:id
│   ├── gRPC: DraftService (SaveDraft, GetDraft, DeleteDraft, ListDrafts)
│   └── 自动清理: 复用 GC sweeper 模式清理过期草稿
│
└── 方向五（关系遍历）:
    ├── 新增 internal/graph/ 包
    ├── HTTP: GET /v1/files/:id/related, GET /v1/messages/:id/related
    ├── gRPC: GraphService (GetFileRelated, GetMessageRelated)
    ├── 使用 errgroup 并行查询实现
    └── 可选 include 参数过滤返回域

Phase 2 —— @提及 + 批量操作（6-10 天）
├── 方向二（@提及）:
│   ├── 迁移: CREATE TABLE mentions
│   ├── 新增 internal/mention/ 包
│   ├── 在 message.Create / comment.Create 中注入提及解析
│   ├── 通知类型扩展: 新增 mention 通知类型 + dispatcher 适配
│   ├── 去重: 与 reply 通知叠加时去重
│   └── 自动补全: GET /v1/users/suggest (本地联系人 + SSO SearchUsers 可选)
│
└── 方向四（批量操作）:
    ├── 扩展 idempotency.Store 以支持批量操作（幂等键→组合结果映射）
    ├── 扩展 middleware.RateLimit 以支持自定义权重
    ├── 新增 repository 批量方法 (DeleteMany, ShareMany 等)
    ├── HTTP: POST /v1/files/batch-delete, POST /v1/files/batch-share
    ├── HTTP: POST /v1/messages/batch-mark-read
    ├── gRPC: BulkService (BulkDeleteFiles, BulkShareFiles, BulkMarkRead)
    ├── 部分失败模型实现: 每个子操作独立提交
    └── 审计日志适配: 批量操作写单条审计事件（包含 IDs 列表）

Phase 3 —— 引用/转发（4-6 天，可与群聊并行）
├── 方向三（引用/转发）:
│   ├── domain 扩展: Message.QuotedMessageID, Comment.QuotedContent
│   ├── 迁移: messages.quoted_message_id UUID
│   ├── service 层: 创建时验证引用消息存在 + 可读权限
│   ├── forwarded_from 信息存入 Meta JSONB
│   ├── 已删除消息的引用显示 "原文已被删除"
│   └── HTTP/gRPC: SendMessage 扩展 quoted_message_id 参数
```

### 5.3 风险点和缓解策略

| 风险 | 影响的方向 | 概率 | 缓解措施 |
|------|-----------|------|---------|
| SSO SearchUsers 不可用 | 方向二 @提及 | 中 | 短期的本地联系人匹配 + 明确的接口契约文档——当 SearchUsers 返回 Unimplemented 时降级 |
| 批量操作的限流放大效应 | 方向四 批量操作 | 中 | 在 rate limit 层添加 `WithBurstMultiplier` 配置，限制批量操作的总 QPS 阈值低于单实体操作 |
| 乐观锁导致草稿保存频繁冲突 | 方向一 草稿 | 低 | 自动保存使用 `saved_by='auto'` 标志，自动保存的版本冲突仅日志记录不报错；手动保存才返回 409 |
| 关系遍历的深度分页性能 | 方向五 关系遍历 | 低-中 | 每个子查询独立分页，使用游标分页（UUIDv7 排序）。关系遍历的典型使用场景（文件详情页的"上下文"面板）通常只需要前 N 条，不需要翻页 |
| 提及解析在 Markdown 中误判 | 方向二 @提及 | 低 | 前端在提交时标注 `@` 的范围（通过文本选区位置），服务端仅验证标注提及。或者：服务端跳过代码块（```）内的 `@` 匹配 |
| `canReadFile` 修复影响现有权限 | 方向五 关系遍历 | 低 | 修复后，被分享用户将能看到文件详情（当前会被拒绝）。这是功能修复而非破坏——需要通知现有用户，但不属于 API break |

### 5.4 与 AGENTS.md 工程约束的对齐

每个方向实施时必须满足的约束检查：

| 约束 | 方向一 | 方向二 | 方向三 | 方向四 | 方向五 |
|------|--------|--------|--------|--------|--------|
| 文件 ≤ 500 行 | ⚠️ `internal/draft/service.go` 预计 300-400 行，安全 | ⚠️ `internal/mention/` 的多文件设计可控制在每文件 300 行 | ✅ 仅修改 3-4 个现有文件，每文件增量 < 50 行 | ⚠️ `BatchResult` 类型 + 批量 repo 方法可能接近 200 行/文件 | ⚠️ `internal/graph/service.go` 预计 400-500 行——需拆分为 `graph_file.go`、`graph_message.go`、`graph_user.go` |
| 函数 ≤ 50 行 | ✅ 纯 CRUD | ⚠️ 提及解析函数需谨慎——正则匹配 + SSO 调用 + 通知触发可能超过 50 行，应拆分为 `parseMentions` + `resolveMentions` + `dispatchNotifications` | ✅ | ⚠️ `BatchDelete` 需包含循环 + 错误收集，建议拆分为 `executeBatch` + `collectResults` | ⚠️ `GetFileRelated` 需调用多个 errgroup，建议拆分为 `fetchRelatedMessages`、`fetchRelatedComments` 等方法 |
| 圈复杂度 ≤ 10 | ✅ | ⚠️ `createMessage` 加入提及解析后需关注，建议将提及逻辑独立到 `appendMentions` 函数 | ✅ 纯数据扩展 | ⚠️ 批量操作的错误处理路径多——使用类型驱动的 `BatchResult` 而非 if-else 链 | ✅ 每个子查询是简单 SELECT |

---

## 六、总结

交叉分析文档识别出的 5 个方向，从架构层面验证了以下结论：

**技术可行性极高**：5 个方向全部在当前技术栈内可实现，不需要新存储后端、新中间件、新基础设施。每个方向的实现路径清晰的映射到现有组件（Outbox、SSE Hub、GC Sweeper、UserDirectory、Idempotency Store）。

**工程成本可控**：按 Phase 0-3 划分，总预估 16-26 人天。考虑这 5 个方向是跨越多域（draft、mention、graph、batch），需要 2-3 名开发者并行工作（Phase 2 的方向二和方向四可以并行）。

**用户价值显著**：草稿和 @提及直接影响所有用户的写操作体验；关系遍历直接影响前端 UI 的数据多样性（文件详情页的"上下文"面板、用户页的"活动"汇总）。批量操作针对高频用户群（管理大量文件/消息的用户）。

**架构债务被暴露**：实施方向的过程中，`canReadFile` 的权限缺陷、`Meta` 的语义漂移、幂等中间件的批量支持缺陷、限流中间件的权重固定等债务将被一并修复。这些修复本身就有独立价值。

**推荐实施顺序**：Phase 0（前置修复）→ Phase 1（草稿 + 关系遍历）→ Phase 2（@提及 + 批量操作）→ Phase 3（引用/转发）。此顺序最大化独立的用户体验提升，同时将技术风险高的方向（批量操作）拖延到低风险方向验证完成后再启动。
