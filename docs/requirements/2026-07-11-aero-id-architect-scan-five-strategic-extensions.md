# aero-id — 全局代码扫描：五个战略性扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码逐文件扫描（`internal/` 全部 23+ 子包、`migrations/` 23 份 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`、`.github/workflows/ci.yml`），结合 `internal/app/app.go` composition root 的实际 wiring 验证。与 `docs/requirements/` 下已有 120+ 分析文档交叉参照，确保方向基于当前代码实际状态。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据。  
> **声明**: 本文件不追求"零覆盖"——经过 120+ 轮分析后已无此可能。本文聚焦于代码中真实存在、高商业价值、跨域影响的战略性方向，而非功能点修补。

---

## 代码库成熟度快照

| 层 | 已交付能力 |
|----|-----------|
| **传输** | HTTP (Gin) + gRPC 全对等 · SSE 实时推送 · 健康/就绪探测 |
| **服务** | 文件（CRUD/分享/预签名/元数据）· 消息（1:1/收件箱/对话/已读回执/反应）· 评论（两层嵌套/点赞/举报/审核）· 通知（outbox 驱动/折叠/偏好/静默时段）· 搜索（FTS + CJK ILIKE）· 阻止 · 管理（审计日志/角色） |
| **基础设施** | 事务性 Outbox + 重试/死信 · 幂等键 · SSO 断路器 + stale 缓存 + singleflight + mTLS + cert-pinning · OpenTelemetry 追踪 · RED 指标 · Token Bucket 限流 · 冷启动 warmup |
| **存储** | Local / S3（MinIO 兼容）· 内容去重 · 引用计数 · GC 扫表 + 孤儿 blob 清除 · MIME/大小校验 · 预签名直传 |
| **数据** | 22 次可逆迁移 · UUIDv7 主键 · 软删除 · 分页（cursor-based）· GORM |
| **运维** | 可配置迁移策略（auto/CLI）· 优雅关停 · 23 项配置覆盖所有 feature flag |

---

## 方向一：多参与方消息（群聊 / 会话抽象层）

### 概述

当前消息系统是严格的 1:1 私信模型。缺失群聊、会话（Conversation）实体、成员管理、对话级别的操作。这是 aero-id 产品模型中最大的单一缺口。

### 代码级证据

**证据 1：Message 模型只有 `FromUID` / `ToUID`，没有会话 ID**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string          // ← 单发送者
    ToUID     string          // ← 单接收者
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
}
```

没有 `ConversationID`、`RoomID`、`ChannelID` 或 `GroupID` 字段。这种设计使消息无法归属于一个多参与方上下文。

**证据 2：Inbox 查询返回扁平消息列表，而非对话摘要**

```go
// internal/repository/message.go — ListInbox
// 返回的是 to_uid 为接收者的所有消息，按 created_at DESC 排序
// 没有 "按 last_message_at 分组为对话、显示最后一条消息+未读数" 的逻辑
```

用户看到的是一长串单条消息，而非类似微信/Telegram 的对话列表。

**证据 3：没有群成员表或会话元数据表**

迁移文件清单：

```
migrations/000001_create_files.up.sql
migrations/000002_create_messages.up.sql
...
migrations/000022_admin_roles.up.sql
```

没有任何迁移文件定义 `conversations`、`conversation_members`、或 `message_recipients` 表。

**证据 4：已读回执仅针对单条消息**

`MarkConversationRead` 批量标记某个发件人的所有消息为已读，但它基于 `(to_uid, from_uid)` 的二元组合——当引入群聊后，这个概念需要按 `(conversation_id, member_uid)` 重定义。

### 为什么需要

**产品经理视角：**
- 群聊是 IM 产品的基线能力，而非增值功能。没有群聊意味着 aero-id 无法与任何现代通讯工具竞争。
- 场景：一个团队共享文件和评论，但无法建立一个"项目 Alpha"群组一次性沟通。成员只能两两私信，信息支离破碎。
- 对话级别功能（置顶、禁言、归档、退出）是用户预期的体验基线。

**架构师视角：**
- 当前 `(from_uid, to_uid)` 模型无法扩展为多对多消息模式。引入 `conversations` 表是架构级变化，而非局部修补。
- 数据结构变化波及：消息模型、收件箱查询、通知路由、块拦截（对话级禁言 vs 用户级阻止）、存储布局（共享文件属于对话而非个人）。
- 群聊引入新的一致性问题：成员退出后是否可查看历史？如何计算群聊的未读数？这些需要在设计阶段明确。

### 边界情况

- 群成员退出后：不可见新消息，但历史消息的可见性需产品决策（单向删除 vs 全员删除）
- 消息引用：1:1 对话中的文件分享到群聊时的 ACL 传播
- 通知路由：群聊中`@all` vs `@user` 的通知语义差异
- 群聊创建者的特殊权限：增减成员、解散群组、转让所有权

---

## 方向二：完整的管理后台与内容审核平台

### 概述

`internal/domain/admin.go` 定义了四项管理权限（`content.moderate`、`audit.read`、`user.ban`、`system.config`），`internal/repository/admin.go` 实现了角色授予/撤销和审计日志查询。但 HTTP/gRPC 层仅暴露了审计日志列表——所有其他管理功能要么未实现、要么未接入传输层。`RequirePermission` 中间件已完整实现但从未被任何路由引用。

### 代码级证据

**证据 1：`RequirePermission` 中间件已实现但零引用**

```go
// internal/middleware/permission.go
func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
    return func(c *gin.Context) {
        // ... 完整实现：提取 actor、查询 HasPermission、403 拒绝 ...
    }
}
```

全局 grep 确认：
```
$ grep -rn "RequirePermission" internal/ --include="*.go"
internal/middleware/permission.go:20:func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
// ↑ 只有定义，没有任何路由调用它
```

**证据 2：AdminHandler 仅暴露审计日志，内部直接调用 HasPermission 而非通过中间件**

```go
// internal/handler/http/admin.go
type AdminHandler struct {
    adminRepo repository.AdminRepository
}

func (h *AdminHandler) RegisterRoutes(r Routes) {
    r.Auth.GET("/admin/audit-log", h.ListAudit) // ← 唯一的 admin 端点
}

func (h *AdminHandler) ListAudit(c *gin.Context) {
    // 直接在 handler body 里检查权限，没有使用 RequirePermission 中间件
    allowed, err := h.adminRepo.HasPermission(c.Request.Context(), actor, "audit.read")
}
```

**证据 3：`PermUserBan` 定义存在，但没有任何 ban 功能**

`internal/domain/admin.go` 定义了 `PermUserBan = "user.ban"`，但代码库中没有：
- 用户封禁/解封的表或迁移
- 封禁状态的 service/repository 方法
- HTTP/gRPC 端点
- 在任何请求路径上检查用户是否被封禁（消息发送、评论创建、文件上传）

**证据 4：`PermContentModerate` 未接入任何道路由**

`content.moderate` 权限预期允许管理员全局隐藏评论或覆盖审核决策。当前评论隐藏/取消隐藏只能由评论作者执行（`internal/service/comment.go` 中的 `setStatusAsAuthor` 强制 `c.OwnerUID != actor` 检查），没有管理员覆盖路径。

```go
func (s *commentService) setStatusAsAuthor(ctx context.Context, id domain.ID, status domain.CommentStatus) error {
    // ...
    if c.OwnerUID != actor {
        return apperr.Forbidden("comment.forbidden", "only the author can change this comment's visibility")
    }
    // ↑ 管理员无法越过此检查来隐藏违规评论
}
```

**证据 5：角色管理（GrantRole/RevokeRole）已实现但无传输层接入**

`internal/repository/admin.go` 实现了 `GrantRole` 和 `RevokeRole`，但没有任何 handler 调用它们。角色管理只能通过直接数据库操作进行。

### 为什么需要

**产品经理视角：**
- 用户举报机制已完整（评论举报 → 作者查看 → 解决/驳回），但缺少管理员介入路径。当一个评论被多个用户举报，而评论作者自己就是骚扰者时，当前系统没有任何办法让平台介入。
- 封禁能力是社交平台的基础安全设施。一个恶意用户在 aero-id 上发送骚扰消息，目前最多只能被对方 block——但 block 只是单向的，对方仍然可以骚扰其他用户。
- `content.moderate` 权限使平台可以雇佣审核员，这是走向"平台级产品"的关键一步。

**架构师视角：**
- 已经有完整的基础设施（角色系统、权限检查、审计日志、`RequirePermission` 中间件），但上层暴露不足 20%。
- 当前的 admin handler 直接持有 `AdminRepository`（非接口注入），与其他 handler 层架构不一致。其他 handler 都经过 `service` 层，但 admin handler 直接调用 repository，跳过了 service 层的错误转换和审计。

### 边界情况

- 封禁与数据可见性：被封禁用户的数据是否对其他用户可见？(内容隐藏 vs 删除)
- 封禁范围：封禁是否影响已发送但未读的消息？(撤回 vs 保留)
- 分级封禁：暂时封禁 vs 永久封禁 vs 仅限制发消息
- 审核操作本身的审计：谁在什么时候封禁了谁——当前审计日志只记录事件，但没有标准化的"管理操作"事件类型
- 申诉流程：用户被误封后有怎样的申诉渠道

---

## 方向三：乐观并发控制与数据一致性保障

### 概述

所有实体（File、Message、Comment）均无版本号或乐观锁机制。在并发场景下，最后写入者获胜（last-write-wins），可能导致隐式数据丢失。当前仅在数据库约束层面（唯一索引、外键）保证基本完整性，但业务层面的并发冲突缺乏保护。

### 代码级证据

**证据 1：无任何实体包含版本字段**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
    // 没有 Version 或 LockVersion 字段
}

// internal/domain/message.go
type Message struct {
    // ... 没有 Version 字段
}

// internal/domain/comment.go
type Comment struct {
    // ... 没有 Version 字段
}
```

**证据 2：GORM 更新使用无条件覆盖**

```go
// internal/repository/file.go — UpdateMetadata
// 使用 map[string]any 直接 update，无 WHERE version = ? 子句
func (r *fileRepo) UpdateMetadata(ctx context.Context, id domain.ID, fields map[string]any) error {
    return r.db.WithContext(ctx).Model(&FileRow{}).
        Where("id = ?", id).
        Updates(fields).Error
}

// internal/repository/comment.go — UpdateContent
func (r *commentRepo) UpdateContent(ctx context.Context, id domain.ID, content string, editedAt time.Time) error {
    return r.db.WithContext(ctx).Model(&CommentRow{}).
        Where("id = ? AND status = ?", id, domain.CommentStatusNormal).
        Updates(map[string]any{"content": content, "updated_at": editedAt, "edited_at": editedAt}).Error
}
```

没有任何更新操作检查数据版本或使用 `UPDATE ... WHERE version = ?`。

**证据 3：消息编辑存在竞态条件**

```go
// internal/service/message.go — Edit
// 1. Get 读取当前消息
m, err := s.messages.Get(ctx, id)
// 2. 业务逻辑检查（消息存在、发送者匹配、非 system 消息）
// 3. UpdateContent 写入新内容
if err := s.messages.UpdateContent(ctx, id, content, now); err != nil {
```

在步骤 1 和步骤 3 之间，另一个请求可能已经编辑了同一条消息。编辑 1 的内容会被编辑 2 无条件覆盖——用户最后看到的内容取决于哪个请求最后完成了 DB 写入，而非用户的意图。

**证据 4：文件可见性更新存在类似问题**

```go
// internal/service/file.go — fileService.UpdateMetadata
// 读取 → 业务逻辑 → 写入，三步之间无锁保护
f, err := s.ownedFile(ctx, id, actor)
// ... 构造 fields map ...
if err := s.files.UpdateMetadata(ctx, id, fields); err != nil {
```

如果一个用户将文件设为 public 以分享，同时另一个会话将其设回 private，结果非确定。

### 为什么需要

**产品经理视角：**
- "编辑冲突"是用户信任问题。当用户编辑一条消息后看到的是另一版本（因为另一个请求抢先），这会破坏用户对系统的信任。
- 社交产品中，不当内容审核与编辑恢复之间的竞态是一个实际的攻击面：恶意用户可以在被隐藏的瞬间重新编辑，利用竞态窗口绕过审核。

**架构师视角：**
- 当前 codebase 在写路径上没有防丢失保护。幂等键解决了重复提交问题（同一请求被发送两次），但不解决并发编辑问题（两个不同的请求同时操作同一资源）。
- 随着系统增大（多实例部署），并发冲突的概率线性增加。单实例场景下窗口小，多实例 + 连接池 + keepalive 下窗口显著扩大。
- 修复成本较低：GORM 支持 `version` 列乐观锁（`gorm:"version"` tag），只需在关键写路径（消息编辑、评论编辑、文件元数据更新）添加即可，不影响读取性能。

### 边界情况

- 重试语义：当 `UPDATE ... WHERE version = ?` 影响 0 行时，是返回 409 Conflict 还是透明重试一次？
- 批量操作：`MarkConversationRead` 和 `MarkAllRead` 涉及多行更新，乐观锁无法逐行应用——需要悲观锁或整个会话级别的版本号。
- 计数器字段：`LikeCount`、`ReplyCount` 使用原子更新 `SET like_count = like_count + 1`，不受版本号保护——要么保留原子更新，要么切换为版本号 + 读取后进行业务计算。

---

## 方向四：资源配额系统与使用量追踪

### 概述

系统缺乏任何 per-user 资源配额（存储容量、文件数量、消息频率、总评论数）。当前的限制是全局性的（`MaxUploadBytes`、`AllowedMimeTypes`、全局 RPS 限流），没有面向用户或面向组织的分层策略。对于多租户 SaaS 部署场景，这是不可持续的。

### 代码级证据

**证据 1：唯一的资源限制是全局配置**

```go
// internal/config/config.go
type StorageConfig struct {
    Type             string   `mapstructure:"type"`
    MaxUploadBytes   int64    `mapstructure:"max_upload_bytes"`   // 0 = unlimited
    AllowedMimeTypes []string `mapstructure:"allowed_mime_types"` // empty = allow any
    // ...
}
```

没有任何 per-user 的配额配置——所有用户共享同样的上限。

**证据 2：没有使用量追踪基础设施**

```
internal/repository/ 下无 usage 或 quota 相关的 repository
internal/service/ 下无 usage 或 quota 相关的 service
migrations/ 下无 usage 或 quota 相关的表
```

没有表记录用户当前的文件总大小、当天发送的消息数量、或对话总数。

**证据 3：服务层没有配额检查点**

以文件创建为例，检测点完全基于全局策略：

```go
// internal/service/file.go
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    // ...
    if s.maxBytes > 0 && in.Size > s.maxBytes {
        return nil, apperr.BadRequest("file.too_large", ...)
    }
    // 没有 per-user 的总存储量检查
    // 没有 per-user 的文件数量检查
    // 没有 per-user 的当天上传计数检查
    // ...
}
```

消息发送也类似：

```go
// internal/service/message.go — Send
// 没有 per-user 的消息频率检查（除全局 RPS 限流外）
// 没有 per-user 的对话数量检查
```

**证据 4：全局 RPS 限流不能替代配额**

`internal/pkg/ratelimit/ratelimit.go` 的 token bucket 每秒/每分钟重置，是瞬时流量控制，不是长期资源分配策略。一个用户可以在 1 秒内发送 10 条消息（通过 RPS 限制），然后每小时发送 36000 条——没有"每日消息上限"的概念。

### 为什么需要

**产品经理视角：**
- 多租户 SaaS 的基线能力。没有配额意味着免费用户和付费用户在资源消耗上没有差异，无法构建 freemium 或分层定价模型。
- 防止滥用：恶意用户可以通过创建大量文件或发送海量消息耗尽共享存储和数据库资源，影响所有用户。
- 运营透明度：管理员需要查看"谁用了多少资源"的能力。当前没有任何使用量仪表盘的数据来源。

**架构师视角：**
- 配额检查需要正确的放置位置——在 service 层检查配额并在超出时返回 `429 Too Many Requests` 或 `403 Forbidden`。当前 service 层的检查点模式（`maxBytes`、`mimeAllowed`）已经为配额检查提供了模板。
- 使用量计数需要权衡一致性：是同步计数（每次操作写入 `usage` 表，有写入放大）还是异步计数（通过 outbox 事件投递到独立计数器，有短暂不一致）？
- 配额系统与存储 GC 存在交互：用户删除文件后，配额应尽快释放。当前 GC 的回收窗口（`Retention` 配置）可能让已删除文件继续占用配额。

### 边界情况

- 软删除与配额：用户删除文件后（软删除），配额是立即释放还是等待 GC 硬删除后？
- 超额处理：当用户的配额在操作中被调低（如从 1GB 降为 500MB），当前已消耗超出新配额的用户如何处理——强制阻止新操作 vs 允许维持但不可增长？
- 配额继承：如果引入"组织"层级（一个组织有多个用户），用户配额是其个人配额 + 组织配额的一部分，需要多层合计。
- 传输层一致性：HTTP 和 gRPC 的配额错误码需要统一（HTTP 429 对应 gRPC `ResourceExhausted`），而当前 `pkg/errors` 没有 `ResourceExhausted` 错误类型。

---

## 方向五：平台可集成性——API 契约文档与无代码/低代码场景支持

### 概述

aero-id 具有功能完善的后端，但缺乏面向外部集成者的 API 消费体验：没有 OpenAPI 规范、没有生成的客户端 SDK、没有 gRPC API 的开发者文档、没有 Webhook 事件类型文档、没有 API changelog 或版本化策略。这使得系统难以集成到外部生态（如 Zapier、n8n、企业 IT 系统）。

### 代码级证据

**证据 1：HTTP API 无 OpenAPI/Swagger 规范**

```go
// internal/handler/http/router.go 及所有 handler
// 没有任何 swaggo 注解、openapi 结构体标签、或手动维护的 spec 文件
// 路由定义是 Gin 函数式注册，无法自动生成文档
```

`go.mod` 检查确认没有 `swaggo/swag`、`kin-openapi` 或任何 OpenAPI 相关依赖。

**证据 2：gRPC proto 文件存在但未转化为开发者文档**

```
api/proto/aero_id/v1/aero_id.proto  ← 定义但无配套的文档页面或 proto 描述注释
```

proto 中的注释是代码级说明（参数类型、枚举值），不是面向 API 消费者的使用文档（请求示例、错误场景、限流说明）。

**证据 3：Webhook 事件类型没有文档化**

`internal/service/notification.go` 中定义了 3 种通知事件类型（`message.received`、`message.read_receipt`、`comment.reply`），但：
- 没有集中式事件类型注册表（事件类型是字符串散落在 `notificationFor` 函数中）
- 没有每个事件的 JSON payload schema 文档
- 事件类型的演化没有版本化策略

**证据 4：没有客户端 SDK 或 API 集成示例**

```
# 代码库中不包含：
# - 任何语言的生成客户端（Go/TypeScript/Python/Java）
# - API 集成示例代码
# - Postman 集合文件
# - Docker Compose 中无法直接"试用"的开发者体验
```

虽然 `deployments/docker-compose.yml` 包含服务堆栈，但没有端到端的"新用户 5 分钟内发送第一条消息"的引导流程。

**证据 5：没有 API 版本化或弃用策略**

```go
// internal/handler/http/router.go
routes := Routes{
    Auth: e.Group("/v1", authMW...),  // ← 硬编码 /v1 前缀
}
```

proto 包名 `aero_id.v1` 暗示版本化，但没有：
- 版本间的兼容性保证
- 字段弃用注释（`deprecated = true`）
- 版本转换层（例如 `/v1` → `/v2` 迁移路径）
- API changelog

### 为什么需要

**产品经理视角：**
- API 的可消费性决定了集成速度。没有 OpenAPI 规范意味着每个新集成方都需要阅读 Go handler 代码来理解 API——这不是可行的扩展路径。
- 低代码/无代码平台（Zapier、Retool、n8n）的集成要求 OpenAPI 或类似的标准接口描述。缺少这个，aero-id 无法进入企业自动化生态。
- API 版本化是平台契约的基线。"我们将在 v2 中弃用 X"需要一个版本框架来承载。

**架构师视角：**
- gRPC proto 是天然的 API 契约，可以自动生成文档（protoc-gen-doc）、客户端代码（protoc-gen-grpc-gateway 生成 REST+Swagger）、和服务端桩代码。当前只生成了 Go 服务端代码，浪费了 proto 的杠杆作用。
- 修复路径清晰：`protoc-gen-doc` 从 proto 注释生成 Markdown/HTML 文档；`protoc-gen-openapiv2` 为 gRPC-Gateway 生成 OpenAPI 规范；`buf push` 将 schema 推送到 BSR 供外部消费。
- Webhook 事件类型需要一个类型安全的注册表（可以是 Go 常量枚举 + JSON schema 文件），以防止事件类型字符串漂移（新增事件类型忘记在文档中添加）。

### 边界情况

- API 版本化策略：URL 路径版本（`/v1/`）vs Header 版本（`Accept: application/vnd.aero.v1+json`）vs 查询参数版本
- 向后兼容定义：proto 的 `field 1` 重命名 vs 移除 vs 更改类型——哪些是断裂性变更？
- Webhook 事件 schema 版本：当事件 payload 增加字段时，已有订阅者是收到增强版本还是旧版？是否需要 webhook 订阅时声明 schema 版本？
- SDK 生成策略：在 CI 中自动生成并发布到包管理器 vs 手动维护 SDK 仓库

---

## 优先级建议

| 方向 | 商业影响 | 工程复杂度 | 建议时机 |
|------|---------|-----------|---------|
| 方向一：群聊/会话 | ★★★★★ | ★★★★ | 下一个产品迭代（P1） |
| 方向二：管理后台/审核 | ★★★★ | ★★ | 当前即可开始（P0）——基础设施已就绪 |
| 方向三：乐观并发 | ★★★ | ★ | 当前即可开始（P0）——低成本高价值 |
| 方向四：资源配额 | ★★★★ | ★★★ | 产品定价确定后（P1） |
| 方向五：开发者体验 | ★★★ | ★★ | 对外开放 API 前（P2） |

**为何方向二和方向三标记为 P0**：方向二的 `RequirePermission` 中间件和大多数 repository 方法已实现，只需在 handler 层打通和暴露路由。方向三的乐观锁可以利用 GORM 内置的 `gorm:"version"` 标签，改动量小但能解决一类真实的数据竞争。

