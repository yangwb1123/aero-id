# aero-id — 全局扫描：五个真正高价值的架构扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局代码扫描（全 `internal/` 30+ 包、双传输层、22 份迁移、全部配置项），与 `docs/requirements/` 下 55+ 份既有分析交叉比对，定位以下 5 个在既有分析中**零系统性覆盖或仅最浅层触及**的高价值方向。
> **约束**: 不编写任何代码。每个方向附代码级证据。

---

## 当前能力基线

经全量代码扫描确认，aero-id 已具备以下能力：

| 领域 | 具体能力 |
|------|---------|
| **核心实体** | 文件（双后端、流式上传、预签名、去重、GC）、消息（1-to-1、Reactions、已读回执）、评论（嵌套两层、Like/Unlike、举报审核） |
| **写入正确性** | 事务性 outbox、幂等键（reservation+commit）、重试退避+死信 |
| **可观测性** | RED 指标、OTel 链路追踪、per-dependency `/readyz`、SSO 健康仪表 |
| **SSO 韧性** | 断路器、stale-cache 兜底、singleflight、mTLS、证书固定 |
| **通知系统** | Outbox 驱动通知、SSE 实时推送、偏好+静默时段、合并去重 |
| **安全** | 用户阻塞、角色权限模型、全局 per-actor 限流 |
| **管理面** | 审计日志查询、`cmd/migrate` CLI、存储 GC |
| **API 传输** | 41 个 gRPC RPC 与 HTTP 路由完全对等 |

---

## 方向一：Outbox 通知派发器绕过特性 Service 层（Notification Dispatcher Bypasses Feature Service）

**优先级：P2（架构/可维护性）**
**既有分析覆盖：0/55+ 份文档**

### 问题陈述

当前通知系统的写入路径存在**两条独立的代码路径**，它们共享同一个 Repository 接口但**完全不同的业务逻辑**。一条路径经由 outbox relay 驱动，另一条路径留空待未来使用。这种架构分裂意味着业务规则容易出现分歧。

### 代码级证据

**路径 A — Outbox 派发器**（实际被 Product Path 使用）:

```
outbox.Relay (5s ticker)
  → FetchUnpublished events
  → Handler.Handle(event)
    → service/notification.go: NotificationDispatcher.Handle()
      → 直接调用 notifs.Create(ctx, n)       ← 跳过 internal/notification/ 的 Service 层
      → pusher.Publish(...)                    ← 同步 SSE 推送
```

**路径 B — Feature Service**（定义完整但除 List/MarkRead 外从未被写入路径调用）:

```
internal/notification/service.go: notificationService
  → List()        ← 被 HTTP/gRPC handler 使用
  → MarkRead()    ← 被 HTTP/gRPC handler 使用
  → MarkAllRead() ← 被 HTTP/gRPC handler 使用
  → UnreadCount() ← 被 HTTP/gRPC handler 使用
  → [Create 方法不存在于该接口]
```

**关键证据位置:**

- `internal/service/notification.go:163` — `NewNotificationDispatcher()` 接受 `repository.NotificationRepository` 作为直接依赖。派发器不引用 feature package 的 Service。
- `internal/service/notification.go:216-238` — `Handle()` 方法执行 `d.notifs.Create(ctx, n)` 直接写入 repository，没有任何业务逻辑层介入。
- `internal/notification/service.go` — Feature Service 的 4 个方法都含有 `domain.MustActor(ctx)` 和错误包装，但写入路径完全不需要这些检查（actor 已由 outbox 确认）。
- `internal/handler/http/events.go:93` — SSE 断线重放的 `replayMissed()` 方法调用 `h.fetcher.ListSince()` 直接访问 repository 的 `ListSince` 方法，同样跳过了 Service 层。

### 为什么这是高价值的

1. **业务逻辑分歧风险**：如果未来在 `internal/notification/service.go` 中添加了通知创建的业务规则（例如：静默时段检查必须在通知创建时而非只读查询时执行；部分类型通知需要记录更多审计信息），这些规则对 outbox 派发路径完全不生效。两个路径的创建逻辑必须手动保持同步。

2. **统一入口缺失**：通知创建没有单一入口。任何新的通知生成者（例如未来实现的消息反应通知 "message.reacted"、文件分享通知 "file.shared"）必须知道要直接调用 repository 而非 Service，这增加了新开发者的认知负担。

3. **SSE 重放绕过全部层**：`ListSince` 直接调用 repository 意味着重放的通知不会经过任何业务过滤（例如：被阻塞用户的旧通知是否仍应重放？已撤回内容的通知是否应跳过？）。

### 建议

- 将 `Create` 方法添加到 `internal/notification` 的 Service 接口中，使所有业务规则集中。
- `NotificationDispatcher` 改为接受 Service 接口而非 Repository，使写入路径经过统一的业务逻辑层。
- SSE 重放路径同样改为使用 Service 层方法。

---

## 方向二：存储后端扩展性缺口 —— 无可插拔后端注册机制（Storage Backend No Pluggable Architecture）

**优先级：P3（平台/可扩展性）**
**既有分析覆盖：0/55+ 份文档**

### 问题陈述

`storage.FileStorage` 接口定义清晰，但后端的选择实现在 `app.go` 的一个硬编码 switch 中。添加新的存储后端（Google Cloud Storage、Azure Blob Storage、Backblaze B2、Alibaba OSS、Cloudflare R2）需要修改应用核心代码并重新编译。

### 代码级证据

`internal/app/app.go:93-115`:

```go
func buildStorage(ctx context.Context, cfg *config.Config) (storage.FileStorage, httpapi.SignedDownloader, error) {
    switch cfg.Storage.Type {
    case "local":
        local := storage.NewLocal(...)
        return local, local, nil
    case "s3":
        s3, err := storage.NewS3(...)
        return s3, nil, nil          // ← nil downloader for S3
    default:
        return nil, nil, fmt.Errorf("unknown storage type %q", cfg.Storage.Type)
    }
}
```

对比 `storage/storage.go` 中定义良好的接口：

```go
type FileStorage interface {
    Upload(ctx context.Context, key string, reader io.Reader) error
    Download(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
}
```

以及扩展接口 `Lister`（仅被 GC sweep 使用）：

```go
type Lister interface {
    List(ctx context.Context, fn func(ObjectInfo) (bool, error)) error
}
```

**关键缺口：**

1. **无注册机制** — 没有 `RegisterBackend(name string, factory func(Config) (FileStorage, error))` 或类似的 plugin 模式。
2. **SignedDownloader 与 FileStorage 紧密耦合** — `buildStorage` 同时返回两者，且 local 存储实现了这两个接口（作为 `httpapi.SignedDownloader`），而 S3 则返回 `nil`。新的后端如果也要支持本地签名下载，其集成方式不明确。
3. **配置耦合** — `config.StorageConfig` 的 `Local` 和 `S3` 字段是结构体成员，而非通用 map。新增后端需要往 `config.go` 添加新的配置结构体。
4. **S3 特有逻辑分散** — `internal/service/file_presign.go` 中的 `PresignUpload()` 直接硬编码 S3 调用（`s3Storage` 类型断言）；MinIO 兼容的 presigned URL 逻辑仅对 S3 后端生效。

### 为什么这是高价值的

1. **厂商锁定风险**：当前仅支持 Local（开发环境）和 S3（生产环境，兼容 MinIO）。切换到任何其他对象存储需要修改核心代码。
2. **平台扩展天花板**：如果 aero-id 要向多租户平台发展，不同租户可能使用不同的存储后端（A 用 S3、B 用 GCS），当前架构无法支持。
3. **测试基础设施耦合**：集成测试通过 testcontainers 启动 MinIO（S3 兼容）。如果团队想用 GCS 模拟器测试，需要修改测试基础设施的核心逻辑。

### 建议

- 引入注册表模式：`storage.Register("gcs", NewGCS)`，让 `buildStorage` 通过注册表查找后端工厂。
- 将后端的特有配置（presigned URL 支持、本地签名下载支持）抽象为 `Capabilities` 接口，而非通过类型断言和返回 `nil`。
- 将 `config.StorageConfig` 改为使用 `map[string]any` 或单独的通用配置段落，使新后端只需实现接口即可接入。

---

## 方向三：管理员角色管理系统仅完成一半 —— 有数据模型和权限校验，无管理 API（Half-Implemented Admin Role System）

**优先级：P1（安全/运维）**
**既有分析覆盖：1/55+ 份（仅提 audit-log 暴露不足，未分析角色 CRUD 缺口）**

### 问题陈述

`internal/domain/admin.go` 定义了完整的角色权限模型：`Roles`、`UserRoles`、`Permissions`（`content.moderate`、`audit.read`、`user.ban`、`system.config`）。`internal/repository/admin.go` 实现了完整的角色管理操作（`GrantRole`、`RevokeRole`、`HasPermission`）。迁移 `000022_admin_roles` 创建了对应的表结构。

**但所有角色管理能力（GrantRole、RevokeRole、角色列表查询）完全没有暴露为 API 端点。**

### 代码级证据

**已实现但未暴露的 Repository 方法：**

`internal/repository/admin.go:25-30`:
```go
type AdminRepository interface {
    HasPermission(ctx context.Context, uid, perm string) (bool, error)
    ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
    GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error    // ← 未暴露
    RevokeRole(ctx context.Context, uid, roleCode string) error               // ← 未暴露
}
```

`internal/domain/admin.go` 定义了权限常量：
```go
const (
    PermContentModerate = "content.moderate"  // 审核评论
    PermAuditRead       = "audit.read"        // 查询审计日志（唯一被暴露的）
    PermUserBan         = "user.ban"          // 封禁用户
    PermSystemConfig    = "system.config"     // 修改系统设置
)
```

**唯一暴露的管理端点：**

`internal/handler/http/admin.go:25-56`:
```go
func (h *AdminHandler) RegisterRoutes(r Routes) {
    r.Auth.GET("/admin/audit-log", h.ListAudit)  // 唯一的管理路由
}
```

gRPC proto (`api/proto/aero_id/v1/aero_id.proto`) 中没有任何 admin service 的 RPC 定义。

对比 GRANT/REVOKE 的操作：
- `GrantRole` — 无对应 API 端点，无 gRPC RPC，无 Service 层包装
- `RevokeRole` — 同上
- `ListRoles`（展示所有角色及其权限）— 无 Repository 方法、无 API、无 Service
- `ListUserRoles`（展示某用户拥有的角色）— 无

### 为什么这是高价值的

1. **安全运维死角**：要授予或撤销管理权限，操作员必须直接操作数据库。这在需要紧急 revoke 被攻陷账号的权限时是灾难性的——每一步都需要 DB 连接、手动构造 SQL、没有审计跟踪。
2. **权限模型无法管理**：定义了完整的 RBAC 模型（角色、权限、用户关联），但没有管理界面，角色的创建、修改、授权只能通过迁移文件或手动 SQL 完成。这意味着任何角色调整都需要一次数据库迁移或 DBA 工单。
3. **half-implemented 比未实现更危险**：管理员权限查询（`HasPermission`）在生产路径中生效，但授予/撤销的路径不存在。这意味着初始角色必须通过数据迁移设置，后续维护只能靠裸 SQL。

### 建议

- 暴露 `POST /v1/admin/roles/grant`、`POST /v1/admin/roles/revoke`、`GET /v1/admin/roles`、`GET /v1/admin/roles/:uid` 等 HTTP 端点
- 在 gRPC proto 中添加对应的 AdminService RPC
- 添加 Service 层包装（含审计日志、幂等性、权限自检——不能被降权的人自己降权）
- 添加 `system.config` 权限的相关端点（系统级配置的 CRUD）

---

## 方向四：幂等键 Reservation 与 Commit 的非原子性竞态条件（Idempotency Key Reservation-Commit Race）

**优先级：P1（数据正确性）**
**既有分析覆盖：1/55+ 份（提及存储耗尽攻击面，未分析竞态条件）**

### 问题陈述

当前幂等键实现使用两阶段协议：`Reserve` 创建占位记录 → 实际业务处理 → `Commit`（标记完成）或 `Abandon`（清理）。Reserve 和 Commit 在**不同的数据库事务**中执行，且数据库不保证 Serializable 隔离级别（使用默认 Read Committed）。这引入了竞态窗口。

### 代码级证据

`internal/idempotency/store.go` 中的核心流程：

**Reserve 阶段**（单独事务，idempotency store 自身隐式事务）：
```go
func (s *GormStore) Reserve(ctx context.Context, uid, endpoint, key string) (bool, error) {
    // 插入一行，status=0 (reserved)，expires_at=now+2min
    return s.db.WithContext(ctx).Clauses(clause.Insert{Modifier: "ON CONFLICT DO NOTHING"})...
}
```

**业务处理阶段**（应用级事务，由 handler 发起）：
```go
// 在 handler/file.go: POST /v1/files 中：
idemOK, err := s.idem.Reserve(ctx, ...)
// ... 然后开始应用级别的事务
tx := db.Begin()
// 执行业务逻辑
// Commit 或 Abandon
```

**Commit 阶段**（单独事务）：
```go
func (s *GormStore) Commit(ctx context.Context, uid, endpoint, key string, resp any) error {
    // 更新行：status=1，存储响应体，扩展 TTL
}
```

**竞态场景如下：**

```
时间线:

T0: 请求 A Reserve(key1) → 成功 (ON CONFLICT DO NOTHING, row inserted)
T1: 请求 A 开始业务处理（外部队列等待或上游 SSO 调用慢）
T2: 请求 B Reserve(key1) → ON CONFLICT DO NOTHING → 返回 false (已存在)
    但 B 的输出依赖于超时：如果 Reservation 的 expires_at 已过（2min后）
T3: expires_at 到期（Reserve 时设 2min TTL，但该 TTL 仅用于后台清理）
T4: 请求 A 的业务处理完成 → Commit(key1, resp) 成功
T5: 请求 C Reserve(key1) → 由于 Request A 已完成 (status=1)，不返回 true
    → 检查 status=0 且 expires_at < now 的行 → 清理后重新占用
```

**核心问题在于：**

`internal/idempotency/store.go:22` 中的 `reservationTTL = 2 * time.Minute` 是**软限制**：

1. 第一阶段的 Reserve 插入一行（status=0, expires_at=now+2min）
2. 如果 Reserve 的调用者（业务层 handler）在处理过程中耗时超过 2 分钟，Reservation 过期
3. 后台 cleaner 或下一次 Reserve 调用会清理过期占位
4. **但此时 Commit 仍可能成功执行**（status 不限制更新）
5. 结果：**同一个幂等键产生两条已 committed 的响应**，或者一次 committed + 一次新的 reservation，客户端可能导致双重写入

### 为什么这是高价值的

1. **数据完整性风险**：虽然幂等键的设计意图是防止重复写入，但当前的实现模式（两个分离事务）不能完全消除竞态窗口。尤其在大文件上传（presigned upload + complete）场景中，两步操作之间的间隔可能超过 2 分钟。
2. **与业务事务的分离**：Reserve 在业务事务之外，Abandon 也在业务事务之外。如果业务事务回滚，Reserve 的记录不会自动回滚（因为没有使用 `db.Transaction` 包裹整个流程），留下降级为 "未完成但无法重试" 的孤立记录。
3. **恢复窗口不存在**：当前没有 API 允许客户端查询幂等键的状态或重试一个失败但已保留的请求。

### 建议

- 将 Reserve/业务处理/Commit 包裹在**同一个数据库事务**中，确保原子性
- 或者将 Reservation 的过期检查与 Commit 的时间戳对比——拒绝在 expires_at 之后提交
- 添加幂等键状态查询端点（`GET /v1/idempotency/:key`）使客户端可以检查请求的状态
- 为长时间运行的操作（presigned upload）设计专门的幂等机制

---

## 方向五：软删除级联策略缺失 —— 孤立数据和僵尸关系（Missing Soft-Delete Cascade Policy）

**优先级：P2（数据一致性/产品体验）**
**既有分析覆盖：1/55+ 份（提及"社交闭环断裂"但未系统分析级联范围）**

### 问题陈述

aero-id 全面使用软删除（`gorm.DeletedAt`），但当一个父实体被删除时，其从属资源没有任何级联的可见性/生命周期策略。这产生两类问题：

1. **数据一致性问题**：删除消息后其 Reactions 成为孤立行；删除文件后 `file_shares` 授权记录成为僵尸授权
2. **用户可见的 UX 问题**：删除文件后，该文件上的评论仍然对所有人可见（但目标已无法访问）；删除消息后，其 Reactions 仍然在计数中

### 代码级证据

**问题 1：`message_reactions` 在消息软删除后未被清理**

`internal/message/service.go` — `Delete()` 方法：
```go
func (s *serviceImpl) Delete(ctx context.Context, id domain.ID) error {
    actor, err := domain.MustActor(ctx)
    ...
    return s.msgs.SoftDelete(ctx, id)
    // ← 没有级联删除 message_reactions！
}
```

`internal/repository/message.go` 中 `messages` 的软删除：
```go
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    return r.db.WithContext(ctx).
        Model(&MessageRow{}).
        Where("id = ?", id).
        Update("deleted_at", time.Now().UTC()).Error
    // ← reactions 表 (message_reactions) 无对应清理
}
```

迁移 `000013_message_reactions.up.sql` 定义了 FK：
```sql
FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE
```
但由于 GORM 使用软删除（`UPDATE deleted_at` 而非 `DELETE`），`ON DELETE CASCADE` 永远不会触发。

**问题 2：`file_shares` 在文件软删除后成为僵尸授权**

`internal/file/service_crud.go` — `Delete()` 方法：
```go
func (s *serviceImpl) Delete(ctx context.Context, id domain.ID) error {
    ...
    return s.files.SoftDelete(ctx, id)
    // ← 没有级联清理 file_shares！
}
```

`internal/repository/file_share.go` — 文件删除后，`file_shares` 中的行仍然存在：
```go
// GetSharedWithMe 查询时不过滤文件是否已删除
func (r *fileShareRepo) ListForGrantee(ctx context.Context, granteeUID string, page domain.Page) ([]*domain.File, string, error) {
    // JOIN file_shares ON files.id = file_shares.file_id
    // WHERE file_shares.grantee_uid = ?
    // — 不会排除软删除的文件
}
```

**问题 3：`like_count` / `reply_count` 在评论删除后不递减**

`internal/comment/service.go` — `Delete()` 方法：
```go
func (s *serviceImpl) Delete(ctx context.Context, id domain.ID) error {
    ...
    return s.comments.SoftDelete(ctx, id)
    // ← like_count 和 reply_count 在父评论删除后不递减！
}
```

### 为什么这是高价值的

1. **授权泄密风险**：文件被软删除后，共享授权记录 (`file_shares`) 仍然存在。如果出于合规原因必须删除文件（数据保留策略结束），残余的授权记录向 DB 查询者错误地暗示该文件仍可共享。

2. **误导性计数**：`like_count` 和 `reply_count` 在父实体删除后不递减。这对用户界面来说是一种"幽灵计数"——用户看到"3 条回复"但其中 2 条已被删除，用户体验差。

3. **多级级联缺口**：如果一个文件被删除，它的评论仍在，评论的 reactions 还在，评论的回复还在（若未单独删除），reactions 出现在已删除评论的计数中。这种多级不一致在 audit 查询中尤为混乱。

### 建议

- 为每个 1:N 关系定义清晰的级联策略：
  - `message_reactions` → 消息软删除时同步软删除 reactions（或在查询时过滤 `deleted_at`）
  - `file_shares` → 文件软删除时同步级联清理 sharing 记录
  - `comment.like_count` / `reply_count` → 在评论软删除时递减相关计数
- 考虑在 `Get()` / `List()` 查询中统一添加对父实体状态的检查，避免展示孤立子实体
- 定义 outbox 事件（`file.deleted`, `message.deleted`, `comment.deleted`）以支持异步级联

---

## 总结与建议实施顺序

| 方向 | 优先级 | 风险类型 | 估算 | 前置依赖 |
|------|--------|----------|------|---------|
| 3: 管理员角色 API | P1 | 安全/运维 | 低 | 无（数据模型已就绪） |
| 4: 幂等键竞态修复 | P1 | 数据正确性 | 低 | 无 |
| 5: 软删除级联策略 | P2 | 数据一致性/UX | 中 | 方向 4 (事务性改进) |
| 1: 通知层架构修复 | P2 | 可维护性 | 中 | 无 |
| 2: 存储后端插件化 | P3 | 平台扩展性 | 高 | 无 |

**快速修复建议**（无需架构变更，可立即实施）：
- 在 `message/service.go` 的 `Delete()` 中清理 `message_reactions`（软删除或物理删除）
- 在 `file/service_crud.go` 的 `Delete()` 中级联清理 `file_shares` 行
- 暴露 `internal/repository/admin.go` 中的 `GrantRole`/`RevokeRole` 为 HTTP + gRPC 端点
- 在幂等键 `Commit` 时检查 `expires_at`，拒绝超时写入
