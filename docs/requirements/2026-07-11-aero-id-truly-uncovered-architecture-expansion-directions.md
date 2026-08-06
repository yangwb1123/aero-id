# aero-id — 真正未被覆盖的架构扩展方向

> **作者视角**：资深架构师 / 产品经理  
> **扫描基准**：`main` 分支全部 Go 源文件 + 22 个 migration + 双传输 proto + 配置 + 设计文档  
> **前置条件**：代码库已完整实现 ROADMAP 所有方向 + 生产加固 + 后续 65+ 份需求分析文档覆盖的方向  
> **方法**：逐文件扫描所有领域模型、API 端点、proto 定义、repository 接口、migration schema，寻找**每个现有文档中零系统分析的**高价值方向  
> **无代码变更**

---

## 前提：当前能力全景（基于代码扫描）

| 模块 | 已实现 |
|------|--------|
| **文件** | CRUD + SHA256 去重 + 流式上传 + MIME 白名单 + 大小上限 + 预签名直传 S3 + 显式共享（Grant/Revoke）+ 元数据 PATCH + 软删/硬删 + 存储 GC（保留/孤儿清扫）+ 引用计数 |
| **消息** | CRUD + 1:1 收件箱/会话 + 编辑 + 软删 + 已读回执 + 整会话标已读 + 未读数 + Reactions + 系统消息 + 文件附件 |
| **评论** | CRUD + 两层嵌套 + 编辑 + Like/Unlike + 举报 + 审核（Hide/Unhide/Resolve）+ 原子回复计数 |
| **通知** | Outbox 驱动 + SSE 实时推送 + 折叠（Collapse）+ 偏好（开关+时区感知静音时段）+ 发送速率限制 + Block 感知 + 未读 Feed |
| **搜索** | PostgreSQL tsvector/GIN 全文搜索（消息/评论/文件）+ 种类过滤 + 调用者隔离 |
| **权限** | 方向性用户阻止（Block）+ 管理角色/权限（moderator/admin）+ 审计日志 + 权限中间件 |
| **写路径** | 幂等键 + 事务性 Outbox + 原子计数 + 引用计数 StorageRefs |
| **可靠性** | SSO 断路器 + singleflight + 陈旧缓存容错 + 重试退避 + TLS/mTLS + 证书固定 + gRPC keepalive |
| **可观测** | RED 指标 + OpenTelemetry 追踪 + 按依赖就绪检查 + SSO 健康仪表 |
| **流控** | 每 actor/IP 令牌桶限流 + 空闲桶回收 + 通知发送速率限制 |
| **双协议** | 41 个 gRPC RPC + 20+ HTTP routes，完全功能对等 |
| **迁移** | 22 个可逆迁移，支持自动迁移或 CLI |
| **持久化** | SSE 实时推送 + Last-Event-ID 断线重连 + 错过事件回放 |
| **其他** | 文件上传去重 + Blob 引用计数 + 硬删除级联清理 + 寻回孤儿 Blob |

**简而言之**：代码库已经成熟到几乎所有"明显的"方向都已实现。以下 5 个方向源自对**当前领域模型边界的严格审视**——它们是当前架构的能力边界之外、但以现有基础设施为跳板可以达到的高价值领域。

---

## 方向一：多人群聊与协作空间（Multi-party Conversations & Collaboration Spaces）

### 为什么需要它

当前消息系统的领域模型被严格限制在 **1:1** 私信：

```go
// internal/domain/message.go
type Message struct {
    FromUID   string      // 发送者
    ToUID     string      // 接收者（单个用户）
    Kind      MessageKind
    ...
}
```

这个模型从根本上不支持以下场景：

1. **群聊**：一个团队讨论项目，N 个参与者共享一个会话
2. **频道/房间**：主题性的持续讨论空间（类似 Slack channel、Discord room）
3. **广播消息**：管理员向所有用户或一个用户组发公告
4. **项目空间**：文件 + 消息 + 评论聚合在同一个协作上下文中

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/message.go` | `ToUID` 是 `string` — 只能容纳单个 uid；`FromUID` + `ToUID` 组合没有"群组"概念 |
| `internal/message/repository.go` | `ListConversation(ctx, uidA, uidB)` — 严格双人会话查询；`ListInbox(ctx, toUID)` — 只按接收者查询 |
| `internal/message/service.go` `Send()` | `checkSendBlock(ctx, in.ToUID, actor)` — 单个接收者的 Block 检查 |
| `api/proto/aero_id/v1/aero_id.proto` | `MessageService` 无 `CreateGroup` / `AddMember` / `ListGroups` RPC |
| `migrations/000002_create_messages.up.sql` — schema | `to_uid TEXT NOT NULL` — 单值列，无关联表支持多接收者 |
| `internal/handler/http/message.go` | `sendMessageRequest.ToUID string` — handler 层只接受单个接收者 |

架构上不存在 `conversation` / `room` / `group` 实体。消息直接以 `(from_uid, to_uid)` 对路由，没有中间抽象层。

### 建议的架构演进

引入 `conversations` 和 `conversation_members` 表：

```
conversations: id, type (direct|group), title, created_by, created_at, ...
conversation_members: conversation_id, member_uid, role, joined_at, ...
```

消息不再直接引用 `to_uid`，而是引用 `conversation_id`。1:1 私信是 `type='direct'` 的 conversation 特例——两个成员的群组。
现有 `to_uid` / `from_uid` 查询可以平滑迁移：Direct conversation = "双人 conversation"。

**迁移路径**：
1. 新增 `conversations` / `conversation_members` 表
2. 为已有消息创建隐式 direct conversation（每对 (sender, recipient) 建一个）
3. 消息表加 `conversation_id` 列（可为 NULL，迁移后逐步废弃 `to_uid` 查询）
4. 新增 API: 创建群组、添加/移除成员、列出我的会话
5. 将通知扩展：`user.added_to_group`、`group.message` 等

### 为什么现有分析未覆盖

65+ 份现有需求文档主要关注"如何改进已有功能的质量和可靠性"。群聊是一个**新实体类别**——它不在当前领域模型中，所以没有任何文档分析它。所有消息相关的分析都默认了 1:1 私信模型。

### 影响/工作量

影响：**极高**。这是消息产品最核心的功能缺口，直接决定了 aero-id 能否从"私信工具"升级为"协作平台"。
工作量：**高**。涉及 schema 变更、新领域实体、新 API（HTTP + gRPC）、通知扩展、权限模型。

---

## 方向二：数据生命周期管理（Data Lifecycle Management）

### 为什么需要它

当前系统有一个根本性的不对称：**文件有 GC 机制，消息和评论没有**。

```go
// internal/storage/gc/sweeper.go — 只处理文件
func (s *Sweeper) purgeSoftDeleted(ctx context.Context) (rowsPurged, blobsPurged int, err error) {
    rows, lerr := s.repo.ListSoftDeletedBefore(ctx, before, s.cfg.PurgeBatch)
    // ... hard-delete file rows and orphan blobs
}
```

消息和评论的软删除行**永久保留**：

| 位置 | 发现 |
|------|------|
| `internal/message/repository.go` `SoftDelete()` | 设置 `deleted_at` — **没有对应的 hard-delete 或 GC** |
| `internal/comment/repository.go` `Delete()` | 同上 |
| `internal/repository/message.go` | 无 `ListSoftDeletedBefore` / `HardDelete` 方法 |
| `internal/repository/comment.go` | 同上 |
| `migrations/000002_create_messages.up.sql` | `deleted_at TIMESTAMPTZ` 列存在，但从未被硬删除 |
| `migrations/000003_create_comments.up.sql` | 同上 |

除了软删除问题，更大的缺口是：

1. **没有基于时间的保留策略** — 不能配置"消息保留 90 天"或"评论保留 1 年"
2. **没有归档机制** — 旧数据不能移到冷存储（无论是文件 blob 还是消息行）
3. **没有合规性删除** — 当 SSO 通知账户注销时，无法级联删除该用户的所有资源
4. **没有数据导出** — 用户在 GDPR 要求下无法导出自己的所有数据

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/message.go` — `Message` | 有 `gorm.DeletedAt`，但 service 层 `Delete()` 只做软删；无法硬删或按策略删除 |
| `internal/domain/comment.go` — `Comment` | 同上 |
| `internal/message/handler_http.go` — `Delete` | 调用 `svc.Delete()` → `messages.SoftDelete()` — 不可逆 |
| `internal/comment/handler_http.go` | 同上 |
| `internal/repository/file.go` | 文件有 `ListSoftDeletedBefore` + `HardDelete` — 消息和评论没有等价方法 |
| `internal/service/notification.go` | `NotificationDispatcher` 只处理创建，无清理陈旧通知的逻辑 |
| `internal/app/app.go` | `storagegc.New(...)` 只注入 fileRepo，没有消息/评论的 sweeper |
| `migrations/` | 没有任何 migration 定义过期/归档策略 |
| 所有 HTTP/gRPC handler | **没有导出 API** — 用户无法下载自己的数据 |

### 建议的架构演进

1. **统一 GC 框架**：将 `storage/gc/sweeper.go` 的模式抽象，扩展为适用于所有实体的 `LifecycleManager`
2. **保留策略**：引入 `retention_policies` 表（或配置驱动），定义每个实体类型的保留窗
3. **消息/评论 GC**：实现 `ListSoftDeletedBefore` + `HardDelete` 方法
4. **数据导出**：添加 `POST /v1/export` 端点，异步生成包含用户所有数据的归档（JSON/CSV）
5. **账户注销回调**：SSO → aero-id webhook 端点，级联软删除或匿名化用户数据
6. **归档存储**：将超过 N 个月的消息自动移到归档表（或冷存储），保持主表活跃数据量可控

### 为什么现有分析未覆盖

现有文档覆盖了"如何让文件 GC 工作"，但没有系统性地问："消息和评论的软删除行怎么办？"。这是一个跨实体的、系统级的数据治理问题——超出任何单一功能的 scope。

### 影响/工作量

影响：**高**。合规性（GDPR 数据导出/删除）、存储成本、查询性能都直接受影响。
工作量：**中高**。主要是数据生命周期框架的抽象 + 消息/评论 GC 实现。

---

## 方向三：用户实时状态与活动感知（Presence & Activity Awareness）

### 为什么需要它

aero-id 是一个**实时协作平台**（消息、评论、文件共享），但完全没有"谁在线/谁离线/谁正在做什么"的信息：

1. **无在线状态**：无法知道对方是否在线、上次在线时间
2. **无输入指示器**：发送消息时不知道对方是否正在输入
3. **无已读状态追踪（跨会话）**：只知道单条消息已读，不知道对方整体"最近活跃"情况
4. **无活动 feed**：不知道自己关注的人最近做了什么（评论了某个文件、上传了新文件等）

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/hub/hub.go` | SSE Hub 支持实时推送，但**只推送通知事件**——没有"presence"事件类型 |
| `internal/handler/http/events.go` | SSE 端点只推送 `Notification` 事件——没有心跳/状态事件 |
| `api/proto/aero_id/v1/aero_id.proto` | 没有任何 PresenceService、ActivityService RPC |
| `internal/domain/notification.go` | 通知类型只有 `message.received`、`message.read_receipt`、`comment.reply` |
| `internal/sso/user_cache.go` | 用户缓存返回 uid/nickname/avatar——**没有在线状态字段** |
| `internal/domain/shared.go` | 领域模型只有 `SystemSenderUID = "system"`——没有 tracking 相关实体 |
| SSO proto (`api/proto/sso/v1/sso.proto`) | 可能也不支持在线状态查询（SSO 是身份服务而非状态服务） |

架构中没有任何追踪用户活动状态的组件。SSE Hub 有发布能力但没有被用于此目的。

### 建议的架构演进

1. **Presence 服务**：基于 SSE 连接的在线/离线追踪
   - 用户建立 SSE 连接 → 标记为"在线"
   - 断连 → 标记为"离线"（带宽限期避免闪烁）
   - 提供 `GET /v1/presence/:uid` 查询在线状态
   - 当关注的人状态变化时推送 `presence.online` / `presence.offline` 事件

2. **输入指示器**：WebSocket 通道（或 SSE 带 client→server 通道）传递 typing 事件
   - `POST /v1/typing` → 广播 `typing.start` / `typing.stop` 到会话参与者

3. **最近活跃追踪**：
   - 记录用户最后一次 API 请求时间（可在 `middleware/auth.go` 中无侵入追踪）
   - 提供 `GET /v1/users/:uid/activity`（仅对共同会话参与者可见）

4. **活动 feed**：
   - 使用 outbox 事件的子集生成跨实体活动流（例如：用户上传文件 → `feed.file.uploaded`）
   - `GET /v1/feed` 返回关注用户/实体的最近活动

### 为什么现有分析未覆盖

现有 65+ 份文档中，"用户在线状态"作为一个独立的方向从未被系统分析。文档聚焦于 SSE 的"通知推送"能力（已实现），但没有思考 SSE 连接作为"用户在场信号"的副产品价值。

### 影响/工作量

影响：**中高**。直接提升用户对"实时感"的体验——输入指示器和在线状态是即时通讯的标配功能。
工作量：**中**。Presence 本身是轻量级的（基于 SSE 连接生命周期）；Activity feed 需要新的事件类型和聚合查询。

---

## 方向四：基于策略的文件访问控制与分享增强（Policy-Based Access Control & Enhanced Sharing）

### 为什么需要它

当前文件分享机制是**粗粒度的、静态的、二元的**：

```go
// internal/file/service_share.go
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ... 单纯授权 granteeUIDs 读取该文件
}

func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
    // ... 撤销授权
}
```

分享的能力边界：

1. **只有 Read 权限** — 不能授予 write/share/admin 权限
2. **无过期时间** — 一旦授权，永久有效，直到手动撤销
3. **无分享链接** — 必须知道对方的 uid，不能生成公开或密码保护的链接
4. **无访问追踪** — 不知道谁在何时访问了被分享的文件
5. **无目录级分享** — 只能逐文件分享，不能分享"整个文件夹"
6. **无嵌套权限** — 不能将分享权限再分享给第三方

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/file/service_share.go` `Grant()` | 只接受 `fileID` + `granteeUIDs` — 无权限级别、无过期参数 |
| `internal/file/repository_share.go` | `file_shares` 表只有 `(file_id, uid)` — 无 `permission`、`expires_at` 列 |
| `migrations/000006_create_file_shares.up.sql` | 实际 schema: `file_id TEXT`, `uid TEXT`, `created_at` — 无额外列 |
| `internal/file/file.go` `ShareRepository` | 接口：`Grant(ctx, fileID, ownerUID, granteeUIDs)` — 无过期/权限参数 |
| `internal/handler/http/file.go` `Share()` | `POST /files/:id/shares` body: `{ "uids": [...] }` — 无额外字段 |
| `api/proto/aero_id/v1/aero_id.proto` | `ShareFileRequest` 只有 `file_id` + `grantee_uids` |
| `internal/domain/access.go` | `CanReadFile()` 检查 visibility + ownership + 分享——但只检查"是否可以读" |

整个权限模型是一个简单的二元判定（CanRead = yes/no），没有策略引擎。

### 建议的架构演进

1. **丰富 file_shares 模型**：加 `permission`（read/write/admin）、`expires_at`、`max_downloads` 列
2. **分享链接**：`POST /files/:id/share-links` → 生成一个共享令牌（UUID），可选密码保护
   - `GET /shared/:token` → 匿名或密码验证后访问文件
3. **访问审计**：`file_shares` 加 `last_accessed_at`、`access_count`；或专门建 `file_access_log` 表
4. **权限提升**：引入 `write` 权限（被分享者可以上传新版本或修改元数据）
5. **级联权限**：`CanShare` 权限允许被分享者再分享给其他人

### 为什么现有分析未覆盖

现有 65+ 文档中，"文件分享"被认为已经实现（Grant/Revoke API 存在）。但这些文档没有深入分析**分享模型的丰富度**——它们比较了"有分享 vs 无分享"而不是"分享能做多少事情"。

### 影响/工作量

影响：**中高**。对于需要精细控制文件访问的企业/团队场景，这是差异化竞争力。
工作量：**中**。Schema 扩展 + 新 API + 策略检查服务。

---

## 方向五：跨实体统一订阅与通知路由（Cross-Entity Subscription & Unified Notification Routing）

### 为什么需要它

当前通知系统的核心限制：**通知是代码定义的、与事件类型硬编码的、只通过 SSE 投递的**。

```go
// internal/notification/dispatcher.go
func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":     return d.notificationMessageSent(e)
    case "message.read":     return d.notificationMessageRead(e)
    case "comment.replied":  return d.notificationCommentReplied(e)
    default:                 return nil  // 未知事件被静默丢弃
    }
}
```

这意味着：

1. **事件类型与服务代码耦合**：要添加新通知类型必须修改 dispatcher 的 switch 语句
2. **用户不能订阅任意事件**：用户不能说"当某个文件被评论时通知我"
3. **只有 SSE 投递渠道**：没有 SMS、电子邮件、移动推送、Slack webhook 等备用渠道
4. **没有路由规则**：不能配置"紧急通知推 SSE + 邮件，一般通知只存 inbox"
5. **没有通知偏好继承**：每个用户需要手动配置每种通知类型的偏好——不能从某个角色/组继承

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/notification/dispatcher.go` `Handle()` | Switch 语句硬编码事件类型到通知类型的映射 |
| `internal/handler/http/events.go` | SSE 是唯一实时投递渠道 |
| `internal/service/preference.go` `Set()` | 用户偏好是每个类型手动的——没有"静音全部"或"只接收重要通知"的全局开关 |
| `internal/outbox/webhook.go` | Webhook 是**运维级别**的（所有事件→一个外部端点），不是用户级别的 |
| `internal/domain/notification.go` | `NotificationType` 只有 3 种硬编码常量 |
| `internal/repository/notification.go` | 通知查询：`ListByRecipient(ctx, recipientUID, page)` — 按接收者查询，不支持按主题（某个文件、某个会话）查询 |
| `internal/hub/hub.go` | SSE Hub `pub()` 分发到已连接客户端——没有持久化投递尝试或重试 |

### 建议的架构演进

引入 **订阅系统**，使用户能声明"我对什么实体上的什么事件感兴趣"：

1. **订阅表**：`user_subscriptions(uid, entity_type, entity_id, event_type, channel, config)`
   - 示例：订阅文件 F-123 的所有评论事件 → 推送 SSE
   - 示例：订阅用户 U-456 的所有文件上传事件 → 推送 SSE + 邮件
2. **电子邮件通知渠道**：用户配置邮箱 → dispatcher 调用 SMTP 发送摘要或即时邮件
3. **通知路由规则**：`notification_routing(uid, event_type_glob, channels[])`
   - 示例：即时消息 → SSE + 邮件；每日摘要 → 只存 inbox
4. **基于角色的通知继承**：团队管理员可以设置团队的默认通知偏好
5. **Webhook per-user**：用户可以提供自己的 webhook URL：接收 JSON 格式的通知回调

### 为什么现有分析未覆盖

通知系统在所有 65+ 份文档中被视为"已经完备"——SSE 推送、偏好、折叠、Block 感知都已实现。但**通知的可组合性和可扩展性**（用户能自定义"什么事件怎么通知我"）从未被分析。这是一个"已经工作但不够灵活"的典型架构边界问题。

### 影响/工作量

影响：**中高**。从"系统决定你怎么被通知"到"用户决定你怎么被通知"的范式转变。
工作量：**高**。新领域模型（订阅）、新路由引擎、电子邮件集成、用户端 webhook。

---

## 总结对比

| 方向 | 类型 | 影响 | 工作量 | 当前代码暗示 | 现有分析覆盖 |
|------|------|------|--------|-------------|-------------|
| 1. 多人群聊与协作空间 | 新实体/产品 | 极高 | 高 | `Message.ToUID` 单值，无 conversation 实体 | ❌ 零覆盖 |
| 2. 数据生命周期管理 | 系统/治理 | 高 | 中高 | 消息/评论无 GC，无导出/注销回调 | ❌ 零覆盖 |
| 3. 实时状态与活动感知 | 产品/体验 | 中高 | 中 | SSE Hub 未用于 presence，无活动 feed | ❌ 零覆盖 |
| 4. 策略化文件访问控制 | 产品/安全 | 中高 | 中 | Share 模型二元无过期，无链接分享 | ❌ 零覆盖 |
| 5. 跨实体订阅与通知路由 | 架构/产品 | 中高 | 高 | Dispatcher switch 硬编码，仅 SSE 渠道 | ❌ 零覆盖 |

---

## 建议的优先级排序

```
P0 (下一迭代):
  2. 数据生命周期管理 — 合规性驱动，消息/评论 GC 是数据完整性基本要求
  3. 实时状态与活动感知 — 体验提升最快，基于现有 SSE 基础设施

P1 (短期路线图):
  1. 多人群聊与协作空间 — 最大产品差异化，但工作量也最大
  4. 策略化文件访问控制 — 企业场景刚需

P2 (长期演进):
  5. 跨实体订阅与通知路由 — 极高的灵活性和用户价值，但需要完整的通知基础设施重构
```

**核心判断**：方向二（数据生命周期）和方向三（实时状态）是当前最优先的——它们利用现有基础设施解决遗留问题，且不需要引入新实体类型。方向一（群聊）是最大产品价值但也是最大工程量，应作为独立产品迭代阶段对待。
