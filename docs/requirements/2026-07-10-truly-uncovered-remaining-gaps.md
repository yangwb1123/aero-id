# aero-id — 八轮交叉分析后的最后剩余盲区

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全量代码扫描（`internal/` 全部 22+ 包、22 份迁移、`api/proto/`、`cmd/`、`configs/`），逐文件通读约 120+ 源文件后，与 `docs/requirements/` 下 **43 份既有分析文档**、`docs/REVIEW_*` 下 **8 份审查文档**、`ROADMAP.md` 的 **10 个方向** 做逐条标题级交叉比对，累计确认 **~70 个独立方向后**，定位以下 5 个在既有文档中零系统性覆盖的方向。
> **约束**: 不编写任何代码。每个方向附代码级证据。

---

## 交叉验证方法

```
输入:
  - 43 份 docs/requirements/*.md  → ~60 个方向
  -  8 份 docs/REVIEW_*.md        → ~20 个方向
  -  1 份 ROADMAP.md              →  5 个方向
  -  1 份 docs/specs/design.md    →  若干 out-of-scope 项
  --------------------------------------------------------
  合并去重                         → ~70 个独立方向
  逐方向与当前代码比对覆盖度         → 确认零覆盖的 5 个方向
```

每个方向已确认：在既有 70+ 个方向中，既无标题匹配，也无系统性正文分析。

---

## 五项最终剩余方向概览

| # | 方向 | 类别 | 优先级 | 代码证据维度 |
|---|------|------|--------|------------|
| 1 | **Block 抽象穿透——文件分享面未执行阻止检查**（Block Bypass via File Sharing） | 安全 / 隐私完整性 | **P1** | `file/{service_share,service_crud}.go` 零 block 引用；`domain/access.go` 不检查 block；`message/service.go` 和 `comment/service.go` 则有检查 |
| 2 | **机器可读的错误分类编目缺失**（Machine-Readable Error Taxonomy Contract） | API 契约 / 开发者体验 | **P2** | `pkg/errors/errors.go` 只有 6 个构造函数，错误码是自由字符串；无 `error.yaml`、无按端点编目、无 `retryable`/`rate_limit` 元数据 |
| 3 | **会话（Conversation）是隐式派生而非一等实体**（Conversation as Implicit Entity Limits Extensibility） | 架构瓶颈 / 产品可扩展性 | **P2** | 无 `conversations` 表；`ListConversation` 是全表 JOIN；群聊、会话级权限、会话级通知偏好均需完整建表才能支持 |
| 4 | **`repository/db.go` 遗留重复行类型未被清理**（Legacy Duplicate Row Types） | 技术债 / 维护风险 | **P3** | 9 个 `Row` 类型中 6 个在新 feature package 中有完全重复定义；`msgRowToDomain`/`commentRowToDomain`/`notificationRowToDomain` 函数在生产中零调用 |
| 5 | **通知限流器无后台过期清理——键表永久增长**（Notification Rate Limiter Stale Entry Accumulation） | 性能 / 资源管理 | **P2** | `service/notification.go` 的 `notifRateLimiter.keys` map 仅在 `allow()` 命中时裁剪过期条目；从未被命中的键残留永久 |

---

## 方向一：Block 抽象穿透——文件分享面未执行阻止检查

### 问题陈述

aero-id 的 block 功能（`internal/block/`）旨在提供用户级阻止机制——用户 A 阻止用户 B 后，B 不应能打扰 A。当前实现中：

- **消息（message）**：`Send()` 调用 `checkSendBlock` → 被阻塞时返回 Forbidden ✅
- **通知（notification）**：`NotificationDispatcher` 在创建通知前调用 `blocks.IsBlocked` → 被阻塞时静默跳过 ✅
- **评论（comment）**：评论创建本身不直接检查 block（由消息/文件 ACL 间接保护） ⚠️
- **文件分享（file share）**：**完全不检查 block** ❌

这意味着：

```
用户 A 阻止用户 B
  ↓
A 可以通过 ShareFile() 将私有文件分享给 B  （✅ 通过——无 block 检查）
  ↓
B 看到文件出现在 ListSharedWithMe() 列表中 （✅ 通过）
  ↓
B 可以在文件上创建评论                    （✅ 通过——comment 服务检查 canReadFile，后者通过 IsSharedWith 返回 true）
```

**Block 的语义完整性被破坏。** 用户 A 阻止了 B，但 A 仍可以主动分享文件给 B，而 B 可以借此与 A 的私有内容交互。

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/file/service_share.go:1-86` | `ShareFile`、`UnshareFile`、`ListSharedWithMe` — **三个方法中零 block 引用**。`grep -n "block\|Block" internal/file/service_share.go` 返回空 |
| `internal/file/service_crud.go:57-62` | `canRead` 方法只检查 `visibility` 和 `shares.IsSharedWith`，**不检查调用者与文件所有者之间的 block 关系** |
| `internal/file/service_crud.go:130-145` | `Delete` 方法只检查所有权，不检查阻止——被阻塞者无法删除文件（因为不是 owner），但可以访问已分享给他们的文件 |
| `internal/file/file.go:88-89` | `Service` 接口定义中无 `WithBlockChecker` 或类似选项。文件服务根本没有集成 block 能力的钩子 |
| `internal/message/service.go:56-62` | 对比：消息服务的 `checkSendBlock` 在 `Send` 中被调用 ✅ |
| `internal/service/notification.go:210-220` | 对比：通知 dispatcher 在 `Handle` 中调用 `blocks.IsBlocked` ✅ |
| `internal/domain/access.go:4-8` | `CanReadFile` 只检查 `visibility == public OR owner == actor` — 这是纯 ACL 函数，domain 层无 block 概念 ✅（设计如此，但 service 层未补充） |

### 影响分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| A blocked B, A shares file with B | 共享成功，B 可访问 | 共享应被拒绝（403）或静默失败（假定阻塞者不应能主动接触被阻塞者） |
| A blocked B, B previously shared a file with A | B 的文件仍出现在 A 的 `ListSharedWithMe` | A 应该看不到被阻止者的文件（对阻止者不可见） |
| A blocked B, B comments on a file A shared publicly | B 可以评论（public 文件允许任何人评论） | 符合预期——block 不阻止对公共内容的访问，只阻止直接交互 |
| A blocked B, then makes a file public | B 可以评论该公共文件 | 符合预期 |

### 边界情况

1. **双向 block**：如果 A 和 B 互相 block，A 分享文件给 B，当前行为允许。期望行为取决于产品定义——通常单向阻止应阻止任何交互，双向同理。

2. **Block 后已有分享**：当 A 阻止 B 时，现有已生效的分享应如何处理？当前 block 操作（`block/block.go:Block()`）仅插入 `user_blocks` 行，不级联处理已有分享。

3. **Block 后新分享**：当 A 阻止 B 后创建新分享，应拒绝。当前未拒绝。

### 建议方案

1. **文件服务注入 `BlockChecker`**：参照 `message/service.go:WithBlockChecker` 的模式，为 `fileService` 增加 `WithBlockChecker` 选项。

2. **`ShareFile` 检查 actor→grantee 的 block 关系**：如果 grantee 阻止了 actor（`blocks.IsBlocked(ctx, grantee, actor)`），则拒绝共享——被阻塞者的意愿不应因阻塞者的行为被覆盖。

3. **`ListSharedWithMe` 过滤被阻止的分享者**：使用 `block.BlockChecker` 过滤掉来自被阻止者的文件。

4. **`canRead` 补充 block 检查**：当 actor 不是 owner 时，检查 actor 与 owner 之间的 block 关系——被阻止者不应通过分享机制读取对方的文件。

5. **`Block()` 操作可选级联**：block 创建时，可考虑软删除或暂停所有 blocker→blocked 的现有文件分享。

### 工作量估算

- 新增 `WithBlockChecker`：~10 行
- `ShareFile` 集成 block 检查：~20 行
- `ListSharedWithMe` 过滤：~15 行
- `canRead` 补充检查：~10 行
- 单元测试：~100 行
- **总计：约 1-2 天**

---

## 方向二：机器可读的错误分类编目缺失

### 问题陈述

当前 aero-id 的错误处理系统（`internal/pkg/errors/errors.go`）提供了 6 个错误类和统一的 AppError 结构，但错误码完全是自由文本字符串。对于 API 消费者（特别是 gRPC 客户端和第三方集成），这带来三个问题：

1. **不可编程处理**：客户端无法确定一个 `"file.not_found"` 错误是否可重试（retryable）。`"comment.nesting_too_deep"` 和 `"file.too_large"` 都是 400（BadRequest），但前者的语义是"永远不可重试"，后者是"减小文件后重试"。

2. **无错误文档契约**：每个端点的错误类型列表未编目。客户端不知道 `POST /v1/comments` 可能返回 `comment.nesting_too_deep`、`comment.target_not_found` 还是 `comment.invalid`。开发者只能通过试错学习。

3. **不可监控分类**：运维团队无法按错误类别设置告警。`comment.invalid`（用户输入错误）和 `file.store_failed`（系统故障）混在同一个 500 / 400 分类下，但前者的告警阈值应该很低，后者应立刻告警。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/pkg/errors/errors.go:38-60` | 6 个构造函数 (`BadRequest`, `Unauthorized`, `Forbidden`, `NotFound`, `Conflict`, `Internal`) 使用固定的 HTTP/gRPC 映射。元数据只有 `Code`、`Message`、`Cause`。无 `Retryable`、`RateLimited`、`RetryAfter`、`SafeToShowToUser` 等分类字段 |
| `internal/service/comment.go:30-35` | `comment.Create` 返回 `"comment.nesting_too_deep"`（BadRequest）——但重试 100 次也不会成功，客户端需要的是不同语义 |
| `internal/service/file.go:45-50` | `file.Create` 返回 `"file.too_large"`（BadRequest）——这个错误是**可重试的**（减小文件后），但 AppError 无法表达"减小后重试"的约束 |
| `internal/service/message.go:25-30` | `message.Send` 返回 `"message.forbidden"`（Forbidden）当收件人阻止了发送者——这个错误是**确定性的**，重试不会改变结果 |
| `internal/service/file.go:160` | `file.store_failed`（Internal）——对于客户端，这可能是"重试可能成功"（S3 临时不可用），也可能是"永远不会成功"（S3 配置错误）。客户端无从判断 |
| 全量搜索 | `grep -rn '"file\.\|"message\.\|"comment\.\|"notification\.\|"block\.\|"auth\.\|"search\.\|"rate\.\|"preference\.\|"admin\.' internal/` — 统计：~60+ 个唯一错误码，散落在 10+ 个 service 文件中，**无集中编目** |

### 影响分析

| 角色 | 当前体验 | 期望体验 |
|------|---------|---------|
| **前端开发者** | 错误处理靠阅读源码和试错 | 每个端点附带 OpenAPI 扩展 `x-error-codes: [...]`，SDK 生成强类型错误枚举 |
| **gRPC 客户端** | 收到 `status.Error(InvalidArgument, "comment.nesting_too_deep")`，需要解析 message 字符串 | 收到 `status.Error` 时附带 `ErrorDetails` 中的结构化元数据 |
| **SRE** | 无法区分"用户输入错误 4xx"和"系统故障 5xx"来设置告警 | `error_code` 和 `retryable` 作为 Prometheus label |
| **第三方集成开发者** | 写入 try/catch 时需要猜测哪些错误可重试 | 文档化的 `x-error-semantics`：`{retryable: true, rate_limit: false, idempotent: false}` |

### 建议方案

#### Short-term：错误编目 + 元数据字段

为 `AppError` 增加可选元数据：

```go
type AppError struct {
    Code    string
    Message string
    HTTP    int
    GRPC    codes.Code
    Cause   error
    // 新增
    Retryable bool   // true = 操作可安全重试（如超时、临时不可用）
    RateLimit bool   // true = 客户端应遵守 Retry-After
    SafeMsg   string // 可安全展示给终端用户的友好消息（可选）
}
```

#### Medium-term：`errors.yaml` 错误契约

创建 `docs/errors.yaml`（或嵌入 proto annotations），作为每个端点的错误契约：

```yaml
endpoints:
  POST /v1/comments:
    errors:
      - code: comment.nesting_too_deep
        http: 400
        retryable: false
        description: "回复嵌套超过两层"
      - code: comment.target_not_found
        http: 404
        retryable: false
        description: "评论目标文件/消息不存在"
      - code: comment.target_inaccessible
        http: 403
        retryable: false
        description: "无权访问评论目标"
  POST /v1/files:
    errors:
      - code: file.too_large
        http: 413
        retryable: true
        retry_after_hint: "减小文件大小后重试"
      - code: file.store_failed
        http: 503
        retryable: true
```

#### Long-term：OpenAPI / gRPC error details 集成

- HTTP：通过 `x-error-codes` 扩展暴露在 OpenAPI spec 中
- gRPC：利用 `google.golang.org/genproto/googleapis/rpc/errdetails` 的 `ErrorInfo`、`RetryInfo`、`QuotaFailure` 结构

### 工作量估算

- `AppError` 增加 `Retryable`/`SafeMsg` 字段：~10 行 + 更新所有构造函数
- 遍历 ~60 个错误码标记 `retryable`：~1-2 小时
- `errors.yaml` 编目：~半天
- **总计：约 2-3 天**

---

## 方向三：会话（Conversation）是隐式派生而非一等实体

### 问题陈述

当前的消息系统将"会话"定义为"两个 uid 之间的所有消息集合"，通过 `ListConversation(uidA, uidB)` 在 `messages` 表上执行 `(from_uid = A AND to_uid = B) OR (from_uid = B AND to_uid = A)` 查询来派生。这种设计对于 1:1 消息的 MVP 场景足够，但限制了整个产品方向的发展。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/message/repository.go:60-65` | `ListConversation` 的实现是 `WHERE (from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)` — 无 `conversations` 表可 JOIN |
| `internal/message/message.go:40-45` | `MessageService.Conversation` 签名只有 `(ctx, withUID, page)` — 无 conversation ID 参数 |
| `internal/handler/http/message.go:60-70` | HTTP 路由：`GET /v1/conversations/:uid` — ":uid" 实际上是 peer uid，不是 conversation ID |
| `internal/handler/grpc/message.go:40-50` | gRPC `ListConversation` RPC 同理 |
| `migrations/` 全部 22 个文件 | 没有任何 `conversations` 或 `conversation_members` 表 |
| `internal/domain/message.go:10-20` | `Message` 类型只有 `FromUID` + `ToUID`，无 `ConversationID` 字段 |

### 隐式模型的具体限制

| 功能需求 | 当前实现能否支持 | 不可行的根本原因 |
|---------|---------------|----------------|
| **群聊（3+ 人）** | ❌ | `from_uid` → `to_uid` 是 1:1 模型。群聊需要 N:N 的 `channel_members` + 路由表 |
| **会话归档/静音/置顶** | ⚠️ 勉强可行 | 可以在 `conversation_settings` 表中用 `(uid, peer_uid)` 模拟，但每个操作都需要 JOIN + UPSERT，且无法扩展 |
| **会话级通知偏好** | ⚠️ 勉强可行 | 同上 |
| **离开/删除会话** | ❌ | 无法区分"用户删除了所有消息"和"用户离开了对话" |
| **按会话列出收件箱** | ⚠️ 可行但低效 | 当前 `ListInbox` 返回单个消息而非按会话聚合。前端需要自己 Group By。大量用户有 1000+ 条消息时，聚合在应用层做代价高昂 |
| **会话级权限** | ❌ | 无法设置"只有会话创建者可添加/移除成员"等规则 |
| **跨会话搜索** | ⚠️ 可行 | `SearchMessages` 可以搜索，但结果无法按会话分组展示 |

### 影响分析

从产品路线图看，**群聊**（architecture-extensions.md、genuinely-new-architectural-extensions.md 等反复提及）是 aero-id 最高频的下一代功能需求。但当前的消息模型完全无法支持群聊，需要一次根本性的数据模型重构。

更关键的约束：即使不引入群聊，单靠隐式会话模型也无法支持以下基本 UX：
- **按未读会话数显示 Badge**（而不是按未读消息数）
- **置顶会话**（始终显示在列表顶部）
- **静音会话**（不触发通知但仍出现在列表中）
- **会话级已读状态**（区别于单条消息的已读状态）

### 建议方案

最小可行变更：引入 `conversations` 表作为一等实体，同时保持向后兼容。

```sql
CREATE TABLE conversations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        TEXT NOT NULL DEFAULT 'direct',  -- 'direct' | 'group' | 'channel'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 1:1 对话：只有两个成员
-- N:N 对话：N 个成员
CREATE TABLE conversation_members (
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    uid             TEXT NOT NULL,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at         TIMESTAMPTZ,  -- 离开对话时间；NULL = 仍在；非 NULL = 已离开
    PRIMARY KEY (conversation_id, uid)
);

-- 可选：会话设置
CREATE TABLE conversation_settings (
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    uid             TEXT NOT NULL,
    muted_at        TIMESTAMPTZ,
    pinned_at       TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ,
    PRIMARY KEY (conversation_id, uid)
);

-- 在 messages 表中增加 conversation_id 外键
ALTER TABLE messages ADD COLUMN conversation_id UUID REFERENCES conversations(id);
```

迁移策略：
1. 新增迁移创建 `conversations`、`conversation_members`、`conversation_settings` 表
2. 后台 Job 扫描所有现有消息的 `(from_uid, to_uid)` 对，为每对唯一组合创建一条 `conversations` 记录 + 两条 `conversation_members` 记录
3. 逐步迁移 `ListConversation` → JOIN `conversations` 表；保留旧查询作为回退

### 工作量估算

- 数据模型设计 + 迁移：~2 天
- 回填 Job：~1 天
- Service 层适配：~1-2 天
- 端点适配（保持向后兼容）：~1 天
- 测试：~1-2 天
- **总计：约 1-1.5 周**

---

## 方向四：`repository/db.go` 遗留重复行类型未被清理

### 问题陈述

在"新栈"（feature package 模式，如 `internal/file/`、`internal/message/`、`internal/comment/` 等各自拥有独立的 `repository.go`）被引入后，旧的 `internal/repository/db.go` 文件中的行类型定义和转换函数未被清理。这些类型中有相当一部分已经不再被生产代码调用——它们与 feature package 中的对应定义**完全相同**，形成维护双倍负担。

### 代码级证据

| 旧类型（`repository/db.go`） | 新定义（feature package） | 旧类型是否被调用 |
|------------------------------|--------------------------|-----------------|
| `messageRow` (line 81) | `internal/message/repository.go:18` | `msgRowToDomain` → 零外部调用 |
| `commentRow` (line 128) | `internal/comment/repository.go:17` | `commentRowToDomain` → 零外部调用 |
| `commentLikeRow` (line 184) | `internal/comment/repository.go:72` | 仅在 `db.go` 内部无调用 |
| `fileShareRow` (line 193) | `internal/file/repository_share.go:15` | 仅在 `db.go` 内部无调用 |
| `commentReportRow` (line 203) | `internal/comment/repository.go:80` | `commentReportRowToDomain` → 零外部调用 |
| `messageReactionRow` (line 230) | `internal/message/repository.go:34` | 仅在 `db.go` 内部无调用 |
| `notificationRow` (line 279) | `internal/notification/repository.go:14` | `notificationRowToDomain` → 零外部调用 |

**仍被使用的旧类型：**
- `fileRow` (line 22) — `fileRowToDomain` 被 `internal/repository/file.go` 和 `internal/file/repository_share.go` 调用 ✅
- `auditEventRow` (line 239) — `auditEventRowToDomain` 被 `internal/repository/audit.go` 和 `internal/repository/admin.go` 调用 ✅

### 风险

| 风险 | 描述 | 可能性 |
|------|------|--------|
| Schema 变更遗漏 | 新迁移修改 `comments` 表结构，开发者在 `comment/repository.go` 中更新了 `commentRow`，但忘记更新 `repository/db.go` 中的陈旧版本。编译通过但运行时行为错误 | 高 |
| 新开发者困惑 | 新成员看到两套行类型定义，不确定哪套是真实的、哪套是遗留的，可能同时修改两套或使用错误的一套 | 高 |
| `grep` 误导 | 在 `repository/db.go` 中修改 `fileRow` 的字段，不熟悉迁移历史的开发者可能认为这是 source of truth，而实际 `file/repository.go` 中的定义才是活跃的 | 中 |
| 隐式反模式 | 旧类型未被删除传递了一个信号："双定义模式是可以接受的"——鼓励更多重复而非清理 | 低 |

### 建议方案

1. **迁移 `fileRow` 和 `auditEventRow` 到 feature package**：
   - `repository/file.go` 改为引用 `file/repository.go` 中的 `fileRow` 和 `fileRowToDomain`
   - `repository/audit.go` 和 `repository/admin.go` 改为引用 `audit/repository.go` 中的 `auditEventRow` 和 `auditEventRowToDomain`（或就地保留如果 `audit` 包尚未有独立的 repository）

2. **删除 `repository/db.go` 中所有不再被外部调用的行类型和转换函数**

3. **为 `repository/db.go` 添加明确的 deprecation 注释**，标明该文件中剩余类型的迁移状态

### 验证方法

```bash
# 确认 msgRowToDomain 不被外部调用
grep -rn "msgRowToDomain" internal/ --include='*.go' | grep -v "_test.go"
# 预期：只出现 internal/repository/db.go 自身的定义
```

### 工作量估算

- 迁移 `fileRow` 和 `auditEventRow` 引用：~1 小时
- 清理无用代码：~30 分钟
- 验证编译 + 测试：~30 分钟
- **总计：约 2-3 小时**

---

## 方向五：通知限流器无后台过期清理——键表永久增长

### 问题陈述

`internal/service/notification.go` 中的 `notifRateLimiter` 是 `NotificationDispatcher` 用于防止通知洪水的组件。它维护一个 `map[string][]time.Time` 来跟踪每个 `(recipient, type)` 键在过去一分钟内的通知次数。该 map 仅在 `allow()` 方法被调用时对目标键进行过期裁剪。如果一个键被访问一次后再无后续访问，该键及其空 slice 在 map 中**永远残留**。

### 代码级证据

```go
// internal/service/notification.go:260-300
type notifRateLimiter struct {
    mu   sync.Mutex
    rate int
    win  time.Duration
    keys map[string][]time.Time   // key=recipient:type → timestamps
    now  func() time.Time
}
```

| 问题 | 说明 |
|------|------|
| **无上限** | `keys` map 无最大容量限制。在高 uid 基数下（10 万+活跃用户），如果每个 uid 收到至少一条通知，map 中就有 10 万+条目 |
| **无后台清理** | 与 `ratelimit.Limiter`（`internal/pkg/ratelimit/ratelimit.go`）有 `RunCleaner` 定期清理空闲 bucket 不同，`notifRateLimiter` 没有等价的 `RunCleaner` 方法 |
| **内存无限增长** | 假设一个生产环境有 100 万用户，每个用户至少被分发过一种通知类型，map 中就有 100 万条目。每个条目是一个 string key + 空 time slice → 约 64+24 = 88 字节 → ~88MB 无实际作用的内存 |
| **对比 `ratelimit.Limiter`** | `internal/pkg/ratelimit/ratelimit.go:90-110` — `Limiter` 有 `sweepIdle` + `RunCleaner`，定期淘汰空闲超过 `idleTTL` 的 bucket ✅ |

### 影响分析

| 用户规模 | 预计 map 条目数 | 内存消耗（近似） | 影响 |
|---------|----------------|----------------|------|
| 1K | 1K | ~100KB | 可忽略 |
| 10K | 10K | ~1MB | 可忽略 |
| 100K | 100K | ~10MB | 尚可接受 |
| 1M | 1M | ~100MB | 显著开销——除通知限流器外无其他用途 |
| 10M | 10M | ~1GB | 内存超标——需要集群内存的显著比例 |

> **注意**：实际影响取决于通知类型基数。如果只有 3 种通知类型，键的基数是 `uid_count × 3`。如果每个 uid 只触发过 1 种类型，是 `uid_count × 1`。

### 边界条件

1. **服务重启重置**——map 是纯内存的，重启后清空。所以这不是无限累积，而是每次进程生命周期内的累积。
2. **限流器默认关闭**——`WithDispatcherRateLimiter` 需要显式开启，默认 dispatcher 没有此组件。默认部署不受影响。
3. **与 `ratelimit.Limiter` 的对比**——后者有明确的 `idle_ttl` 配置、`RunCleaner` goroutine、`sweepIdle` 方法。前者的缺失是设计上的不对称。

### 建议方案

为 `notifRateLimiter` 增加等价于 `ratelimit.Limiter.RunCleaner` 的清理机制：

```go
func (rl *notifRateLimiter) RunCleaner(interval time.Duration) func() {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        for {
            select {
            case <-t.C:
                rl.sweep()
            case <-done:
                t.Stop()
                return
            }
        }
    }()
    return func() { close(done) }
}

func (rl *notifRateLimiter) sweep() {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    cutoff := rl.now().Add(-rl.win)
    for k, entries := range rl.keys {
        // 找到第一个存活条目
        firstLive := len(entries)
        for i, t := range entries {
            if t.After(cutoff) {
                firstLive = i
                break
            }
        }
        if firstLive == 0 {
            continue  // 所有条目都存活；正常
        }
        if firstLive >= len(entries) {
            delete(rl.keys, k)  // 所有条目过期；删除键
            continue
        }
        rl.keys[k] = entries[firstLive:] // 部分过期；截断
    }
}
```

### 工作量估算

- 添加 `RunCleaner` + `sweep` 方法：~40 行
- 在 `NotificationDispatcher` 中集成：~5 行（调用 `rateLimiter.RunCleaner`）
- 在 `app.go` 中停止清理器（与 `stopRateLimit` 类似）：~10 行
- 单元测试：~60 行
- **总计：约 0.5-1 天**

---

## 综合优先级建议

| # | 方向 | 优先级 | 工作量 | 类型 | 如果现在不做 |
|---|------|--------|--------|------|-------------|
| 1 | Block 穿透——文件分享面未执行阻止检查 | **P1** | 1-2 天 | 安全 / 隐私完整性 | block 功能存在误导性安全承诺。用户以为 block 了某人就能完全阻止交互，但实际上可以通过文件分享绕过 |
| 2 | 机器可读错误分类编目 | **P2** | 2-3 天 | 开发者体验 / API 契约 | 第三方集成靠试错；无 OpenAPI spec；错误告警无法按类型分级 |
| 3 | 会话（Conversation）作为一等实体 | **P2** | 1-1.5 周 | 架构瓶颈 / 产品可扩展性 | 每个"群聊"或"会话管理"需求都要求先重构数据模型——越晚越贵 |
| 4 | 遗留重复行类型清理 | **P3** | 2-3 小时 | 技术债 / 维护风险 | 下次 schema 变更可能漏改旧定义，导致难以调试的生产问题 |
| 5 | 通知限流器无后台清理 | **P2** | 0.5-1 天 | 性能 / 资源管理 | 用户规模增长时内存线性增长；对比 `ratelimit.Limiter` 是设计不对称 |

### 执行顺序建议

1. **方向一（P1）**：立即修复——block 语义完整性是安全基础。这是唯一有直接影响用户的**正确性问题**的方向。
2. **方向五（P2）**：快速低成本修复——复制 `ratelimit.Limiter.RunCleaner` 的现有模式，约半天工作量。
3. **方向四（P3，随时可做）**：2-3 小时清理债务，消除此后 schema 变更的风险。
4. **方向二（P2）**：第一个外部集成者（third-party SDK/API 消费者）出现前完成。
5. **方向三（P2）**：启动群聊功能需求前的 prerequisite。建议在第一个"群聊"产品需求下达前规划。
