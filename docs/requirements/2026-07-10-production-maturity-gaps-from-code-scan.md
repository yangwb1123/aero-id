# aero-id — 三次全量代码扫描：5 个面向生产成熟度的架构缺口

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 对 `internal/` 全部 22+ 个包、22 份迁移 SQL、`cmd/`、`configs/`、`api/proto/` 做三次逐行代码扫描。  
>   与 `docs/requirements/` 下 **64 份**既有分析文档的逐条交叉比对，**确保零重复**。  
> **约束**: 不编写任何代码。每个方向附带精确代码位置和可操作的下一步。  
> **前置条件**: `feat/roadmap` 已合并；所有 ROADMAP 方向、生产加固全部交付。

---

## 交叉验证声明

本文定位的 5 个方向均满足：在所有 64 份 `docs/requirements/*.md` + `docs/REVIEW_*` + `ROADMAP.md` 的逐条比对中，
**既无标题级匹配，亦无系统性正文分析**。每个方向都是在代码中真实存在、但被所有此前分析遗漏的结构性问题。

---

## 方向一：新栈（领域包）与旧栈（分层包）之间存在功能缺陷漂移

### 性质

**数据正确性 · 维护风险（高）**

### 问题描述

代码库存在两套独立且完整的 handler→service→repository 堆栈：

| 堆栈 | 包路径 | 被 `app.go` 引用？ | 代码量 |
|------|--------|-------------------|--------|
| **旧栈**（分层） | `internal/service/`, `internal/repository/` | **是**（生产路径） | 约 5,500 行 |
| **新栈**（领域域） | `internal/comment/`, `internal/file/`, `internal/message/`, `internal/block/`, `internal/notification/`, `internal/search/`, `internal/audit/` | **否**（除 search 外） | 约 11,798 行 |

前一轮分析（`2026-07-10-last-unexplored-architectural-gaps.md`）已识别了双栈问题，将其归类为"死代码"。
但本文发现了一个更严重的问题：**新栈缺少旧栈中存在的功能，存在功能缺陷漂移**。

### 代码级证据

**证据：旧栈有 CJK 搜索回退，新栈完全没有**

旧栈的 repository 层有 `LIKE` 回退搜索方法：

```go
// internal/repository/comment.go:50-52
SearchComments(ctx context.Context, ownerUID, query string, limit int) ([]*domain.Comment, error)
SearchCommentsLike(ctx context.Context, ownerUID, query string, limit int) ([]*domain.Comment, error)

// internal/repository/file.go:43-47
SearchFiles(ctx context.Context, ownerUID, query string, limit int) ([]*domain.File, error)
SearchFilesLike(ctx context.Context, ownerUID, query string, limit int) ([]*domain.File, error)
```

新栈只暴露了 FTS 版本：

```go
// internal/comment/repository.go:390
func (r *repo) SearchComments(ctx context.Context, ownerUID, query string, limit int) ([]*Comment, error)
// 缺少：SearchCommentsLike

// internal/file/repository.go:223
func (r *fileRepo) SearchFiles(ctx context.Context, ownerUID, query string, limit int) ([]*File, error)
// 缺少：SearchFilesLike
```

**影响**：当搜索中文/日文/韩文时，`plainto_tsquery('simple', ?)` 不会正确切分 CJK 字符。旧栈的 `SearchXxxLike` 方法使用 `WHERE content ILIKE '%query%'` 作为回退，能返回部分结果。新栈用户搜索 CJK 内容将始终得到空结果——这是一个**生产可用性 bug**。

**证据 2：新栈的 `Repository` 接口缺少 `ListSoftDeletedBefore` 和 `HardDelete`**

```go
// internal/repository/file.go — 旧栈
ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*domain.File, error)
HardDelete(ctx context.Context, ids []domain.ID) error

// internal/file/repository.go — 新栈
// 完全缺少这两个方法！
```

旧栈的文件仓库接口包含 GC sweeper 需要的两个方法。新栈接口不包含它们——如果未来将 GC sweeper 切换到新栈，会编译失败。

**证据 3：新栈的 `SearchFiles` 查询不使用 `search_vector` 索引**

旧栈：
```sql
-- internal/repository/file.go:224
AND search_vector @@ plainto_tsquery('simple', ?)
```

新栈也使用相同的 SQL，但没有 `SearchFilesLike` 回退（如上所述）。

### 影响分析

| 风险 | 等级 | 说明 |
|------|------|------|
| **CJK 搜索不可用** | 🔴 高 | 中文、日文、韩文用户的全文搜索始终返回空结果 |
| **GC 兼容性断点** | 🟡 中 | 如果未来迁移 GC sweeper 到新栈，需要补充缺失的方法 |
| **维护者认知负荷** | 🟡 中 | 修复旧栈的 bug 后需要人工检查新栈是否存在相同的 bug |

### 建议下一步

**Phase 1 — 功能缺口审计（2 天）**

逐方法比对两套堆栈的所有接口和实现，建立功能覆盖矩阵：

```
internal/service/comment.go  vs  internal/comment/service.go
internal/service/message.go  vs  internal/message/service.go
internal/service/file.go     vs  internal/file/service_crud.go
internal/repository/comment.go vs  internal/comment/repository.go
... 等全部 7 个域
```

需要特别关注：搜索回退方法、事务边界、幂等性集成、权限检查。

**Phase 2 — 确定目标架构并迁移（1 周）**

两种策略二选一：

| 策略 | 描述 | 工作量 |
|------|------|--------|
| **A: 旧栈 → 新栈** | 保留新栈（领域域），补齐功能缺口，一次性切换 `app.go` 引用 | 1 周 + 功能补齐 |
| **B: 旧栈 → 旧栈清理** | 删除新栈，功能集中在旧栈，未来按需以新包形式重写 | 2 天删除 + 调整 import |

推荐策略 B 作为短期止血，策略 A 作为中长期演进——前提是缺口全部补齐。

**Phase 3 — 添加 CI 门禁（0.5 天）**

增加 `make dead-code-check`：扫描 `internal/` 下是否存在未被 `app.go` 引用的顶层包。当出现未被引用的包时 CI 告警。

---

## 方向二：Admin 运营面只有骨架——权限中间件未接线，角色管理无端点

### 性质

**运营能力缺失 · 安全（中）**

### 问题描述

aero-id 有一个完整的角色/权限模型设计：

1. `roles` + `user_roles` 表（migration 000022）
2. `HasPermission` repository 方法（`internal/repository/admin.go:52`）
3. `RequirePermission` 中间件（`internal/middleware/permission.go:20`）
4. 4 种内置权限常量（`internal/domain/admin.go`）

但**该模型几乎完全没有接入到实际的 API 面**：

| 组件 | 状态 | 代码证据 |
|------|------|---------|
| `RequirePermission` 中间件 | **从未在任何路由上注册** | `internal/handler/http/router.go` 搜索 `RequirePermission` → 0 引用 |
| `GrantRole` endpoint | **不存在** | `internal/handler/http/admin.go` → 只有 `GET /v1/admin/audit-log` |
| `RevokeRole` endpoint | **不存在** | 同上 |
| gRPC admin service | **完全不存在** | `internal/handler/grpc/ops.go` → 只有 health + reflection |
| `POST /v1/admin/users/:uid/ban` | **不存在** | 所有 handler 中搜索 `ban` → 0 |
| 权限检查方式 | **不一致** | 唯一的管理端点在 handler 内手动调用 `adminRepo.HasPermission`，而非通过中间件 |
| 管理端点权限粒度 | **单人单权** | 没有分层管理（super-admin 可管理 moderator，moderator 只能管内容） |

### 代码级证据

**证据 1：`RequirePermission` 中间件完备但零注册**

```go
// internal/middleware/permission.go:20-43
func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
    return func(c *gin.Context) {
        // 完整的：检查 actor_uid → 调用 HasPermission → 403 或放行
    }
}
```

但 `internal/handler/http/router.go` 中的路由注册完全未使用此中间件：

```go
func NewRouter(..., adminHandler *AdminHandler, ...) *gin.Engine {
    // ...
    r.Auth.GET("/v1/admin/audit-log", adminHandler.ListAudit)  // ← 无 RequirePermission 包装
    // 所有其他路由：无 RequirePermission
}
```

**证据 2：`GrantRole`/`RevokeRole` 完全实现但无传输层暴露**

```go
// internal/repository/admin.go:68-90
func (r *adminRepo) GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error { ... }
func (r *adminRepo) RevokeRole(ctx context.Context, uid, roleCode string) error { ... }
```

两个方法实现完整、测试覆盖。但 `internal/handler/http/admin.go` 和 `internal/handler/grpc/ops.go` 均未注册任何角色管理路由。

**证据 3：gRPC 的 admin 面完全空白**

`internal/handler/grpc/ops.go` 只有 health + reflection 注册。没有：
- `GrantRole` RPC
- `RevokeRole` RPC  
- `ListRoles` RPC
- `ListUserRoles` RPC
- `BanUser` RPC
- `SystemConfig` RPC

这意味着管理员用户无法通过任何编程方式（HTTP 或 gRPC）管理系统角色。

### 影响分析

| 场景 | 当前限制 |
|------|---------|
| **新员工入职** | 管理员无法通过 API 授予角色，必须直接操作数据库 |
| **权限审计** | 无法通过 API 查询某个用户拥有哪些角色 |
| **自动化运营** | 无法编写脚本批量管理权限（如季度权限复审） |
| **内容制裁** | 没有 API 封禁用户或批量隐藏违规内容 |
| **gRPC 管理** | 内部微服务无法以编程方式执行管理操作 |

### 建议下一步

**Phase 1 — 补充角色管理端点（2 天）**

HTTP + gRPC 双端同步实现：

| 端点 | 权限要求 | 说明 |
|------|---------|------|
| `POST /v1/admin/roles/grant` | `system.config` | 为用户授予角色 |
| `POST /v1/admin/roles/revoke` | `system.config` | 撤销用户角色 |
| `GET /v1/admin/roles` | `audit.read` | 列出所有角色定义 |
| `GET /v1/admin/users/:uid/roles` | `audit.read` | 查看用户的角色 |
| `POST /v1/admin/users/:uid/ban` | `user.ban` | 封禁用户 |
| `POST /v1/admin/users/:uid/unban` | `user.ban` | 解封用户 |

**Phase 2 — 将 `RequirePermission` 中间件接入路由注册（1 天）**

```go
// 当前
r.Auth.GET("/v1/admin/audit-log", adminHandler.ListAudit)

// 改为
admin := r.Auth.Group("/v1/admin",
    middleware.RequirePermission(domain.PermAuditRead, adminRepo))
admin.GET("/audit-log", adminHandler.ListAudit)
```

**Phase 3 — 实现 gRPC admin service（2 天）**

在 `api/proto/aero_id/v1/aero_id.proto` 中新增 `AdminService`，包含 GrantRole、RevokeRole、ListRoles、BanUser 等 RPC。

---

## 方向三：文件分享缺少生命周期管理——永久授权、无级联、无通知

### 性质

**安全 · 产品完整性（中）**

### 问题描述

当前文件分享（`file_shares` 表 + `ShareFile`/`UnshareFile` API）是最简实现：

- 分享是 **永久** 的（无 `expires_at` 列）
- 权限是 **二值** 的（授予/收回，无只读/读写之分）
- 无分享 **通知**（grantees 不知晓文件已被分享给他们）
- 文件 **软删除后分享记录成僵尸**（grantees 列表显示一个不存在的文件）
- 无分享 **限额**（一个文件可以分享给无限用户）
- 无分享 **审计**（谁在何时访问了分享的文件）

### 代码级证据

**证据 1：`file_shares` 表缺少过期列**

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE file_shares (
    file_id    UUID NOT NULL REFERENCES files(id),
    owner_uid  TEXT NOT NULL,
    grantee_uid TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (file_id, grantee_uid)
    -- 缺少: expires_at, permission, accessed_at, max_downloads
);
```

对比成熟的分享系统（Google Drive, Dropbox）：分享链接可设过期时间、密码保护、下载次数限制、仅查看/可评论/可编辑三级权限。

**证据 2：文件删除后分享记录成为僵尸**

```go
// internal/service/file.go — Delete()
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // ...
    if err := s.files.Delete(ctx, id); err != nil { ... }
    // 软删除文件行，但不级联删除 file_shares
    // 也不通知 grantees "您对文件 X 的访问权限已被撤销"
    // 更不标记已失效
}
```

当文件被软删除后：
- 原 owner 看不到文件（正常 ✅）
- **grantees 仍然可以在 `ListSharedWithMe` 中看到该文件的记录**（因为 join 到 `files` 表时 `deleted_at IS NULL` 隐含过滤，导致 grantees 看到记录但点击后返回 404）
- 没有优雅的"文件已被 owner 删除"提示

**证据 3：分享操作无通知**

```go
// internal/service/file_share.go — ShareFile()
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ...
    if err := s.shares.Grant(ctx, id, actor, granteeUIDs); err != nil { ... }
    // 没有发送 outbox event "file.shared"
    // 没有通知 grantees
    // 没有写入通知行
}
```

grantees 需要有某种方式知晓文件已被分享。当前用户唯一的发现方式是主动调用 `ListSharedWithMe`。

**证据 4：分享权限不可撤销粒度粗**

`UnshareFile` 是唯一撤销方式——只能完全收回。无法：
- 将 grantee 从"可编辑"降级为"只读"
- 暂时挂起分享
- 设置分享过期时间

### 影响分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 分享机密文件给外部顾问 | 顾问永久可访问直到被手动收回 | 设 30 天过期，过期后自动失效 |
| 分享给同事编辑权限 | 无法——所有分享都是只读 | 可选择只读/可评论/可编辑 |
| 删除自己分享的文件 | grantees 看到 404 但列表仍有记录 | 级联撤销分享并通知 grantees |
| 批量分享给 100 人 | 成功——无上限检查 | 设上限（如每次最多 50 人） |
| 查看谁访问了分享的文件 | 无法——无访问审计日志 | 记录每次访问的时间、IP、用户代理 |

### 建议下一步

**Phase 1 — 数据库扩展（1 天）**

新增 migration `000023_file_shares_lifecycle`：

```sql
ALTER TABLE file_shares ADD COLUMN expires_at TIMESTAMPTZ;
ALTER TABLE file_shares ADD COLUMN permission SMALLINT NOT NULL DEFAULT 1;
--  1 = read, 2 = comment, 3 = edit
ALTER TABLE file_shares ADD COLUMN max_downloads INT;      -- NULL = unlimited
ALTER TABLE file_shares ADD COLUMN downloaded_count INT NOT NULL DEFAULT 0;
ALTER TABLE file_shares ADD COLUMN last_accessed_at TIMESTAMPTZ;
ALTER TABLE file_shares ADD COLUMN revoked_at TIMESTAMPTZ; -- soft-revoke
```

**Phase 2 — 级联删除与通知（2 天）**

- 文件软删除时：标记所有关联 `file_shares.revoked_at = now()`
- `ListSharedWithMe` 查询增加 `AND revoked_at IS NULL`
- 文件删除时 emit `file.deleted` outbox event → dispatcher 通知所有 grantees
- 分享创建时 emit `file.shared` outbox event → dispatcher 通知所有 grantees

**Phase 3 — 分享 API 扩展（2 天）**

```go
// 扩展 ShareFile 输入
type ShareFileInput struct {
    FileID     domain.ID
    GranteeUIDs []string
    Permission SharePermission  // read | comment | edit
    ExpiresAt  *time.Time       // optional
    MaxDownloads *int           // optional
}
```

- `PATCH /v1/files/:id/shares/:grantee`：修改权限或过期时间
- `GET /v1/files/:id/shares`：查看分享记录
- `GET /v1/files/:id/shares/:grantee/access-log`：查看访问日志

---

## 方向四：请求链路缺乏缓存层——同一请求内重复查询无法消除

### 性质

**性能 · 延迟（中）**

### 问题描述

在一个请求的全生命周期中，同一个实体可能被多次查询。当前代码库**没有请求级缓存**，每条查询都穿透到数据库。

### 代码级证据

**证据 1：消息发送时文件存在性检查 + 后续引用**

```go
// internal/message/service.go — checkSendFile()
func (s *service) checkSendFile(ctx context.Context, fileID *domain.ID, actor string) error {
    f, err := s.files.Get(ctx, *fileID)  // ← 第一次查询
    // ...
    if f.OwnerUID != actor { ... }
    return nil
}

// internal/message/service.go — createMessage()
func (s *service) createMessage(ctx context.Context, actor string, in SendMessageInput) (*domain.Message, error) {
    // 创建消息并存储 file_id
    // 注意：createMessage 由 Send 调用，Send 已先调用了 checkSendFile
    // 但消息持久化时不会再次检查文件——这没问题，但其他场景会重复查询
}
```

**证据 2：评论创建时目标实体检查 + 后续操作**

```go
// internal/comment/service.go — assertTargetReadable()
func (s *serviceImpl) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
    switch t {
    case domain.TargetTypeFile:
        f, err := s.files.Get(ctx, target)  // ← 查询文件
        // ...
    case domain.TargetTypeMessage:
        m, err := s.messages.Get(ctx, target)  // ← 查询消息
        // ...
    }
}
```

然后在后续的 `Create` 方法中：

```go
// internal/comment/service.go — Create()
func (s *serviceImpl) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
    actor, err := domain.MustActor(ctx)
    // ...
    if err := s.assertTargetReadable(ctx, actor, in.TargetType, in.TargetID); err != nil { ... }
    // assertTargetReadable 已经查询了 target 实体
    // 但如果后续操作需要 target 的完整信息（如消息发送方的 uid 用于通知路由）
    // 需要再查一次
}
```

**证据 3：每条消息的 inbox 查询都读取 SSO 用户信息**

```go
// internal/service/message.go — Inbox()
func (s *messageService) Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error) {
    msgs, next, err := s.messages.ListInbox(ctx, actor, page, unreadOnly)
    // ...
    users, err := enrichUsers(ctx, s.dir, senderUIDs(msgs))  // 对每个唯一 sender 调用 SSO GetUsers
}
```

- 如果 inbox 有 20 条消息来自同一个人，`senderUIDs` 去重后还是 1 次 SSO 调用——这很好
- 但如果 inbox 中 20 条消息来自 20 个不同的人，就是 1 次批量 SSO 调用——这 OK
- 但假设页面刷新：相同的 20 个 uid 会再次查询 SSO——**UserCache 可以减少但仍有 TTL 间隙**

更根本的问题：没有跨请求的任意实体缓存。

**证据 4：文件列表 + 搜索均无缓存**

```go
// internal/file/service_crud.go — List()
func (s *fileService) List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error) {
    // 直接从 DB 查询，无缓存
    files, next, err := s.files.ListByOwner(ctx, ownerUID, page)
}
```

- 热门文件（如公开文件）每次请求都从 DB 读取
- 同一用户连续两次刷新页面，相同的查询执行两次

### 影响分析

| 场景 | 数据库查询次数（当前） | 优化后 |
|------|----------------------|--------|
| 创建评论（文件目标） | 至少 2 次（文件查询 + 评论创建） | 1 次（如果第一次查询有缓存） |
| 创建评论（消息目标） | 至少 2 次（消息查询 + 评论创建） | 1 次 |
| 发送带文件的消息 | 至少 2 次（文件查询 + 消息创建） | 1 次 |
| 频繁访问的公开文件 | 每次请求 1 次 DB 查询 | 内存缓存命中，0 次 DB |

### 建议下一步

**Phase 1 — 请求级缓存（request-scoped cache）（2 天）**

```go
// 新增 internal/pkg/cache/request.go
type RequestCache struct {
    mu    sync.RWMutex
    items map[string]any
}

func (c *RequestCache) Get(key string) (any, bool)
func (c *RequestCache) Set(key string, value any)
```

- 在 `app.go` 的 middleware 链中为每个请求创建 `RequestCache`，注入 `context.Context`
- Repository 层可选接入：`Get(ctx, id)` 时先查请求缓存
- 请求结束后自动释放

**Phase 2 — 全局只读缓存（可选，2-3 天）**

对于高频读取、低频变更的实体（如公开文件、用户信息），增加进程内缓存：

```go
// 在 service 层封装缓存 wrapper
type cachedFileService struct {
    inner FileService
    cache *sync.Map // id → *File, with TTL
}
```

注意：缓存失效策略需要谨慎设计。只在只读查询上使用，写操作直接穿透。

---

## 方向五：存储后端无断路器保护——S3 降级时全链路阻塞

### 性质

**运维韧性 · 级联故障（中-高）**

### 问题描述

aero-id 的 SSO 客户端有完整的弹性模式（断路器 + 指数退避 + 单飞 + 脏读回退），但**文件存储后端完全没有类似的保护**。

当 S3（或 local 存储的磁盘）出现故障时：

1. 所有文件读取操作阻塞直到 S3 超时（默认 `aws-sdk-go-v2` 客户端超时约 30s-60s）
2. 所有文件上传/删除操作同样阻塞
3. 依赖文件读取的评论创建、消息发送（附带文件）、缩略图预览全部阻塞
4. **HTTP/gRPC 连接池被慢查询填满，引发级联故障**

### 代码级证据

**证据 1：SSO 有完整的弹性——存储为零**

```go
// internal/sso/client.go 包含：
// - CircuitBreaker (closed/open/half-open)
// - 指数退避重试
// - singleflight 缓存击穿防护
// - tokenCache 脏读回退

// internal/storage/s3.go 只有：
// - 基础的 aws-sdk-go-v2 客户端
// - 无超时配置（使用 SDK 默认）
// - 无重试策略
// - 无断路器
// - 无降级行为
// - 无健康检查
```

**证据 2：所有文件操作路径无超时保护**

```go
// internal/storage/storage.go
type FileStorage interface {
    Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
}
```

所有方法接受 `context.Context` 参数，但：
- 没有任何 caller 设置超时 context
- 调用链（handler → service → storage）没有 per-operation 超时传递
- 没有区分快速失败（如 stat）和慢操作（如大文件 get）的超时策略

**证据 3：依赖文件查询的消息发送和评论创建会因 S3 故障而阻塞**

```go
// internal/message/service.go — checkSendFile()
func (s *service) checkSendFile(ctx context.Context, fileID *domain.ID, actor string) error {
    f, err := s.files.Get(ctx, *fileID)  // ← 文件元数据查询（DB），不是 blob 读取
    // 文件元数据在 DB 中，不需访问 S3——但某些场景需要读取 blob
}
```

但 `GetDownloadURL` 和 `PresignUpload` 直接调用 S3：

```go
// internal/service/file_presign.go
func (s *fileService) PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error) {
    // ...
    url, err := s.store.PresignGet(ctx, key, ttl)  // ← 直接调用 S3，无超时保护
}
```

### 影响分析

| 场景 | 当前行为（S3 故障时） | 期望行为 |
|------|---------------------|---------|
| 用户列表文件 | 请求挂起约 30-60s，最终超时 | 500ms 内快速失败，返回 503 |
| 用户下载文件 | 同上 | 同上 |
| 评论/消息引用文件 | 同上——因为评论创建前需要验证目标文件存在（但文件存在性 check 是 DB 操作，不应受 S3 影响） | 区分 DB 查询（快速）和 S3 操作（需超时保护） |
| 存储 GC 扫描 | GC 无法列出/删除对象，卡住 | GC 应跳过 S3 故障，下次重试 |
| **HTTP 连接池** | 50 个慢请求填满连接池，健康请求也被阻塞 | 快速失败，释放连接池 |

### 建议下一步

**Phase 1 — 给 Storage 接口增加带超时的 context 传播（1 天）**

所有调用 `storage` 的路径增加 `context.WithTimeout`：

```go
// internal/service/file.go — GetDownloadURL()
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
    // 添加文件级超时
    storeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()
    url, err := s.store.PresignGet(storeCtx, key, ttl)
}
```

在 `app.go` 中为 storage 操作配置默认超时：

```go
type StorageConfig struct {
    // ...
    OperationTimeout time.Duration // default 10s
    PresignTimeout   time.Duration // default 5s
}
```

**Phase 2 — Storage 断路器（3 天）**

借鉴 `internal/sso/breaker.go` 的模式，为 `FileStorage` 接口增加断路器包装：

```go
// internal/storage/breaker.go
type BreakerStorage struct {
    inner   FileStorage
    breaker *CircuitBreaker
    timeout time.Duration
}

func (b *BreakerStorage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
    if !b.breaker.Allow() {
        return nil, ErrStorageDegraded
    }
    // ...
}
```

断路器状态暴露为 Prometheus 指标（`storage_breaker_state`），/readyz 增加存储健康检查。

**Phase 3 — 区分致命错误和可重试错误（1 天）**

```go
// 在 storage 层区分：
var (
    ErrNotFound      = errors.New("object not found")     // 业务错误，不触发断路器
    ErrStorageDown   = errors.New("storage backend down")  // 基础设施错误，触发断路器
    ErrRateLimited   = errors.New("storage rate limited")  // S3 限流，触发退避
    ErrTimeout       = errors.New("storage operation timed out")
)
```

不同的错误类型触发不同的重试/降级策略：
- `ErrStorageDown` → 断路器打开，快速失败
- `ErrRateLimited` → 指数退避重试
- `ErrNotFound` → 立即返回给 caller

---

## 附录：各方向估算工作量

| 方向 | 优先级 | 估算工作量 | 依赖 |
|------|--------|-----------|------|
| 一：双栈功能缺口审计 | P0（生产可用性 bug） | 2 天审计 + 1 周迁移 | — |
| 二：Admin 运营面 | P1（运营必须） | 2 天端点 + 1 天中间件 + 2 天 gRPC | 方向一（确定使用哪个栈） |
| 三：文件分享生命周期 | P1（产品完整） | 1 天 migration + 2 天级联 + 2 天 API | — |
| 四：请求级缓存 | P2（性能优化） | 2 天 RequestCache + 2 天全局缓存 | — |
| 五：存储断路器 | P1（生产韧性） | 1 天超时 + 3 天断路器 + 1 天错误分类 | — |

---

## 决策建议

1. **本周**: 方向一 Phase 1（功能缺口审计）—— 必须先知道双栈间的差异才能安全地继续开发
2. **并行**: 方向二 Phase 1（角色管理端点）+ 方向三 Phase 1（数据库扩展）
3. **下月**: 方向五（存储断路器）—— 目标是让 aero-id 在 S3 降级时优雅降级而非阻塞级联
4. **按需**: 方向四（缓存）—— 等延迟成为瓶颈再做
