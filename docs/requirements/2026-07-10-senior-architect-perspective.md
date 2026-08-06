# aero-id — 资深架构师视角：高价值扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局代码扫描（`internal/` 全部 22 个包、120+ 源文件、22 个迁移、`cmd/`、`configs/`、`api/proto/`）
> **关注点**: 基于**当前代码实际状态**的可操作方向，重点放在工程正确性（correctness）和架构完整性（completeness）上，而非纯功能堆砌
> **约束**: 无代码变更

---

## 理解现状

aero-id 是一个**高度成熟的双传输数据服务**。已实现：

| 领域 | 覆盖程度 |
|------|---------|
| **文件管线** | 上传/流式/去重/预签名直传/MIME 校验/大小限制/分享/元数据更新/GC 清理/内容 checksum |
| **消息** | 1:1 私信/系统消息/已读回执/消息反应/编辑/软删/全会话标记已读/未读计数 |
| **评论** | 2 级嵌套/原子 reply_count/点赞/举报/审核（隐藏/恢复）/编辑/编辑时间戳 |
| **通知** | Outbox 驱动/SSE 实时推送/未读计数/偏好设置（静音时段/时区）/折叠/速率限制/block 过滤 |
| **用户阻断** | 方向性阻断/审计日志/收件箱和通知双向过滤 |
| **搜索** | PostgreSQL tsvector 全文搜索/CJK ILIKE 回退/跨实体搜索 |
| **基础设施** | 幂等性/事务 outbox/per-actor 限流/circuit breaker/singleflight/OpenTelemetry 全链路追踪/TLS mTLS 证书固定/存储 GC/审计日志/管理角色/健康探针/指标 |

但成熟度不意味着没有缺口。以下 5 个方向触及**当前架构中最核心的工程正确性和完整性盲区**——它们不是"锦上添花的新功能"，而是"生产环境中必然会遇到的问题"。

---

## 方向一：异步管线最终一致性保障 —— Outbox 去重与幂等投递

### 当前状态

Outbox relay 遵循 **at-least-once** 语义：

```go
// internal/outbox/relay.go
for _, e := range events {
    if err := r.handler.Handle(ctx, e); err != nil {
        // mark failed, retry later
        continue
    }
    published = append(published, e.ID)
}
// ...
if err := r.repo.MarkPublished(ctx, published); err != nil {
    return 0  // events handled SUCCESSFULLY but NOT marked published — WILL be retried
}
```

崩溃窗口存在：**Handler.Handle 成功 → MarkPublished 失败/崩溃** → 重启后同一事件被再次投递。

当前 `NotificationDispatcher.Handle` 对重复投递**不做去重**：

```go
// internal/service/notification.go
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // ... block check, preference check, rate limit ...
    if err := d.notifs.Create(ctx, n); err != nil {
        return err  // 如果已经存在类似通知，不报错
    }
    // ... pusher ...
    return nil
}
```

这意味着：宕机恢复后，用户可能收到**重复的通知**（两条 "你收到一条新消息"），且 `notification` 表没有唯一约束来阻止重复行。

`internal/repository/notification.go` 中的 `Create` 方法是简单的 INSERT，无 UPSERT，无冲突检测。

### 为什么需要修复

这不是理论问题——在以下真实场景中必然触发：
- **滚动部署**：新版本部署时旧实例会被 SIGTERM，正在处理的 outbox 事件被中断
- **Outbox relay 重启**：每 5 秒轮询，崩溃恢复间隔可能让大量事件处于"已处理但未标记为已发布"状态
- **长时间 GC pause**：Go GC 停顿可能导致 MarkPublished 超时

### 建议方案

**Notification 行幂等键**：在 `notifications` 表上增加 `(recipient_uid, type, subject_type, subject_id)` 唯一约束，使用 `ON CONFLICT DO NOTHING` 实现幂等插入。或采用 UPSERT 模式。

**Outbox Event 去重令牌**：每个 outbox event 携带一个去重令牌（可基于 event id），下游 handler 检查幂等。`NotificationDispatcher.Handle` 在创建通知前先检查是否已存在相同事件的处理记录。

### 影响范围

- **低风险**：约束添加不改变现有行为
- **中等工作量**：更改 `repository/notification.go` 的 Create 方法
- **高收益**：消除一整类生产事故

---

## 方向二：游标分页的并发一致性 —— 增删交错下的分页不变量

### 当前状态

所有列表端点使用同一分页模式：

```go
// 标准模式: WHERE id < cursor ORDER BY id DESC LIMIT n+1
// 示例: internal/repository/comment.go
q.Where("id < ?", page.Cursor).Order("id DESC").Limit(limit + 1)
```

分页 cursor 是 UUIDv7 字符串，天然按时间排序。但存在**并发写入下的分页不变量破坏**：

### 问题分析

**场景 1：翻页时新数据插入**

```
第 1 页请求: LIMIT 2 → 返回 [id=300, id=299], cursor = "299"
用户翻页 → 第 2 页请求: WHERE id < "299" LIMIT 2
同时: 一个新评论 id=298 插入
第 2 页结果: [id=298, id=297]  ← id=298 在 [id=297] 之后插入但被返回
用户看到: [300, 299, 298, 297]  ← 连续，但 298 本应在第 1 页
后果: 如果用户持续翻页，插入速度足够快时，新数据会"推着"旧数据往后续页面走，
     用户永远翻不到"最后一页"（无限滚动中的"永远加载" bug）
```

**场景 2：软删除导致空洞**

```
用户有: [id=105, 104, 103, 102, 101]
第 1 页 (LIMIT 3): 查询 WHERE id < "999" → [105, 104, 103], cursor="103"
期间: id=104 被软删除
第 2 页: WHERE id < "103" → [102, 101]
用户看到: [105, 104(deleted→hide), 103, 102, 101]
中间出现空洞（隐藏条目），但 count 显示无变化
```

**场景 3：UUIDv7 的时间分辨率有限**

UUIDv7 的毫秒精度意味着同一毫秒内创建的条目具有不可比较的排序。当两个条目在同一毫秒创建时，`id < cursor` 的排序依赖于 UUID 随机部分的字典序，可能与 `ORDER BY id DESC` 的不一致。

```go
// internal/pkg/idgen/idgen.go
// idgen.NewID() 使用 UUIDv7: 毫秒时间戳 + 随机位
// 同一毫秒内: 排序完全取决于随机位
```

### 代码级证据

| 位置 | 问题 |
|------|------|
| `internal/repository/comment.go` — `ListByTarget` | 标准 cursor 分页 |
| `internal/repository/message.go` — `ListInbox` | 标准 cursor 分页 |
| `internal/repository/file.go` — `ListByOwner` | 标准 cursor 分页 |
| `internal/repository/notification.go` — `ListByRecipient` | 标准 cursor 分页 |
| `internal/repository/block.go` — `ListBlocked` | 标准 cursor 分页 |
| `internal/repository/audit.go` — `List` | 标准 cursor 分页 |
| `internal/domain/shared.go` — `Page` | cursor 是 string，无并发安全提示 |

### 建议方案

1. **稳定排序键**：使用 `(created_at, id)` 复合排序而非仅 `id`，这样即使 `id` 的随机部分产生冲突，`created_at` 也能保证稳定的全局顺序
2. **Keyset Pagination 正确实现**：
   ```sql
   WHERE (created_at, id) < ($1, $2)
   ORDER BY created_at DESC, id DESC
   LIMIT $3
   ```
3. **文档化已知限制**：即使使用复合排序，在 `created_at` 完全相同的极端情况下仍可能出现微小漂移——应在 API 文档中说明分页是 "best-effort 一致" 而非 "严格一致"

### 影响范围

- **中等工作量**：修改所有 list repository 方法的排序和 WHERE 子句
- **高风险**：cursor 格式变更会破坏正在使用中的分页令牌（老旧 cursor 会失效）
- **缓解方案**：向后兼容——旧 cursor（纯 id）仍可工作（等价于 `(created_at=NULL, id=cursor)`）

---

## 方向三：SSO 账户生命周期集成 —— 跨系统数据完整性

### 当前状态

aero-id 不拥有身份——每个 `owner_uid`/`from_uid`/`to_uid` 都是 SSO 中定义的外部 uid。这是架构的有意设计：

```
AGENTS.md: "SSO owns identity. No accounts table, no FK to accounts;
reference users only by uid string."
```

但这也意味着当 SSO 中的账户被**删除**或**停用**时，aero-id **完全没有感知机制**。

### 代码级证据

```go
// internal/domain/access.go
func CanReadFile(actor string, f *File) bool {
    return f.Visibility == VisibilityPublic || f.OwnerUID == actor
}
// 如果 actor 指向一个已被 SSO 删除的 uid，这个函数仍然返回 true
// 没有 "orphan" 或 "deleted_account" 状态
```

当前代码中以下位置都依赖 uid 的有效性，但没有任何活跃性检查：

| 位置 | 实体 | 风险 |
|------|------|------|
| `internal/service/message.go` — `Send()` | `to_uid` 参数 | 可以向已删除的账户发送消息 |
| `internal/service/file.go` — `ShareFile()` | `granteeUIDs` | 可以向已删除的账户分享文件 |
| `internal/service/block.go` — `Block()` | `uid` 参数 | 可以阻断一个不存在的用户 |
| `internal/repository/notification.go` | `recipient_uid` | 通知被写入但永远不会被读取 |
| `internal/repository/message.go` | `from_uid` / `to_uid` | 对话另一方已不存在 |

当 SSO 中的 uid 消失时：
- **数据残留**：用户的文件、消息、评论永久存在，但无归属
- **虚假交互**：新用户可以给已删除账户发消息（永远不会被阅读）
- **存储浪费**：已删除账户的未读通知、未处理报告持续堆积
- **合规风险**：GDPR 删除权——aero-id 无法响应 SSO 发起的账户删除

### 建议方案

**SSO → aero-id 生命周期回调端点**：

```
SSO 删除账户
    ↓
POST /aero-id/v1/internal/account-lifecycle
    { "action": "deleted", "uid": "user_123" }
    ↓
aero-id 执行级联操作:
  1. 软删除所有 owner_uid=user_123 的文件
  2. 软删除所有 from_uid=user_123 的消息
  3. 标记所有 to_uid=user_123 的消息为 "recipient_deleted"
  4. 软删除所有 owner_uid=user_123 的评论
  5. 清理所有 recipient_uid=user_123 的通知
  6. 审计日志记录
```

**需要的基础设施**：
- **内部端点**（受共享密钥保护，不在公共 /v1 下）：`POST /internal/account-lifecycle`
- **级联操作**：在每个 repository 中增加 `SoftDeleteByOwner(ctx, uid)` 方法
- **幂等保证**：相同的 uid 删除请求可以安全重放

### 影响范围

- **中等-高工作量**：需要在每个 repository 中增加批量删除方法
- **低风险**：新功能不改变现有行为
- **高收益**：合规 + 数据完整性 + 存储节约

---

## 方向四：管理员操作台与跨账户支持工作流

### 当前状态

已实现的管理能力：

```go
// internal/domain/admin.go
const (
    PermContentModerate = "content.moderate"
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"
    PermSystemConfig    = "system.config"
)
```

管理员可以：
- ✅ 查询审计日志（按 action 过滤）
- ✅ 授予/撤销角色
- ✅ 检查权限

但管理员**不可以**：
- ❌ 查看任何用户的私信
- ❌ 删除任何用户的文件
- ❌ 隐藏任何用户的评论（当前只有评论作者自己能隐藏）
- ❌ 以用户身份发送消息
- ❌ 查看用户的完整资源列表

### 代码级证据

```go
// internal/service/comment.go — setStatusAsAuthor
func (s *commentService) setStatusAsAuthor(ctx context.Context, id domain.ID, status domain.CommentStatus) error {
    // ...Get comment, check owner...
    if c.OwnerUID != actor {
        return apperr.Forbidden("comment.forbidden", "only the author can change this comment's visibility")
    }
    // ...
}

// internal/service/file.go — ownedFile
func (s *fileService) ownedFile(ctx context.Context, id domain.ID, actor string) (*domain.File, error) {
    // ...
    if f.OwnerUID != actor {
        return nil, apperr.Forbidden("file.forbidden", "only the owner can share this file")
    }
}
```

所有权限检查都是 **owner 比较**，没有"admin override"路径。对于有 `PermContentModerate` 的管理员，也没有相应的服务层方法。

**管理端 API 覆盖**：

| 端点 | 存在性 | 管理员可用 |
|------|--------|-----------|
| `GET /v1/admin/audit-log` | ✅ | ✅ |
| `POST /v1/admin/grant-role` | ❌（Repository 层支持，无 HTTP/gRPC 端点） | ❌ |
| `POST /v1/admin/revoke-role` | ❌ | ❌ |
| `DELETE /v1/admin/users/:uid/files` | ❌ | ❌ |
| `POST /v1/admin/comments/:id/hide` | ❌ | ❌ |
| `POST /v1/admin/users/:uid/messages` | ❌（查看消息而非发送） | ❌ |
| `GET /v1/admin/users/:uid/messages` | ❌ | ❌ |

### 为什么需要

- **客服场景**：用户报告看到不当内容，管理员需要有能力立即隐藏它，而不需要找到原作者
- **信任与安全**：违反服务条款的用户，需要管理员能清理其所有内容
- **数据恢复**：用户说"我误删了一个文件"——管理员可以查看并恢复
- **配合 SSO 账户封禁**：当管理员在 SSO 中封禁用户时，还需要在 aero-id 中清理该用户的内容

### 建议方案

1. **引入 `ServiceContext` 携带管理权限**：
   ```go
   type ServiceContext struct {
       ActorUID      string
       AdminOverride bool  // true 时跳过 owner 检查
       Permissions   []string
   }
   ```

2. **在 CommentService/FileService/MessageService 中增加 admin 方法**：
   ```go
   type AdminCommentService interface {
       AdminHideComment(ctx context.Context, id domain.ID) error
       AdminDeleteComment(ctx context.Context, id domain.ID) error
       AdminListUserMessages(ctx context.Context, uid string, page domain.Page) (MessageList, error)
   }
   ```

3. **权限检查前置**：在每个 admin 方法入口检查 `PermContentModerate`，而不是跳过所有的 owner 检查

### 影响范围

- **中等工作量**：每个服务增加 2-3 个 admin 方法 + HTTP/gRPC 端点
- **低风险**：不影响现有用户操作路径
- **高收益**：解锁客服/审核工作流

---

## 方向五：用户级存储配额与用量追踪

### 当前状态

已有按文件的大小限制：

```yaml
# configs/config.yaml
storage:
  max_upload_bytes: 104857600   # 100MB per file
  allowed_mime_types: [...]     # per-file MIME 过滤
```

```go
// internal/service/file.go
if s.maxBytes > 0 && in.Size > s.maxBytes {
    return nil, apperr.BadRequest("file.too_large", "file exceeds the 100MB limit")
}
```

但**没有任何用户级的总量控制**。一个用户可以上传：
- 1000 个 100MB 的文件 → 100GB 总存储
- 每年如此 → 无限累积

### 代码级证据

```go
// internal/repository/file.go
// 没有 CountByOwner 或 SumSizeByOwner 方法
func (r *fileRepo) ListByOwner(ctx, ownerUID, page) ([]*domain.File, string, error) {
    // 只支持分页列表，不支持聚合统计
}

// internal/service/file.go — Create()
// 上传时只检查单文件大小限制，不检查用户总用量
```

**GC sweeper 默认未启用**：

```go
// internal/app/app.go
if cfg.Storage.GC.Enabled {
    // ... start sweeper ...
} else if cfg.Storage.Type == "s3" {
    log.Warn("storage gc is DISABLED with s3 backend — stale blobs accumulate")
}
```

生产配置中 GC 默认 disabled——存储无限增长。

### 为什么需要

- **成本归因**：S3 或本地存储有真实成本，需要按用户追踪
- **公平使用**：防止少数用户占用绝大多数存储资源
- **产品变现**：免费用户 1GB、付费用户 100GB——这是最常见的 SaaS 模式
- **运维安全**：缺乏配额的系统容易因为一个用户上传数 TB 文件导致存储爆满

### 建议方案

**在 FileRepository 中增加聚合查询**：

```go
// internal/repository/file.go
type StorageStats struct {
    TotalSize int64  `json:"total_size"`
    FileCount int    `json:"file_count"`
}

func (r *fileRepo) GetStorageStats(ctx, ownerUID string) (*StorageStats, error) {
    var stats StorageStats
    r.db.WithContext(ctx).
        Model(&fileRow{}).
        Select("COALESCE(SUM(size), 0) as total_size, COUNT(*) as file_count").
        Where("owner_uid = ? AND deleted_at IS NULL", ownerUID).
        Scan(&stats)
    return &stats, nil
}
```

**在 FileService 中实施配额**：

```go
// internal/service/file.go — Create()
func (s *fileService) Create(ctx, in CreateFileInput) (*File, error) {
    // ... existing checks ...
    
    // 配额检查
    if s.quota > 0 {
        stats, err := s.files.GetStorageStats(ctx, actor)
        if err != nil {
            return nil, apperr.Internal(...)
        }
        if stats.TotalSize + in.Size > s.quota {
            return nil, apperr.BadRequest("file.quota_exceeded", "storage quota exceeded")
        }
    }
    
    // ... continue with upload ...
}
```

**配置化配额策略**：

```yaml
storage:
  max_upload_bytes: 104857600
  quota:
    default: 1073741824       # 1GB per user
    premium: 107374182400     # 100GB for premium users
  quota_check_enabled: true   # 可以关闭以进行迁移
```

**使用追踪端点**：

```
GET /v1/storage/usage → { total_size: 12345, file_count: 42, quota: 1073741824 }
```

### 影响范围

- **中等工作量**：Repo 层增加聚合查询 + Service 层配额检查 + HTTP/gRPC 使用量端点
- **低风险**：配额检查是新增逻辑，不改变现有行为（quota=0 表示无限制）
- **高收益**：成本控制 + 产品变现 + 公平使用

---

## 边缘案例（Edge Cases）汇总

在与方向无关但值得注意的边缘案例中，以下 3 个最为紧急：

### EC-1：预签名上传完成前崩溃

```
客户端获取 PresignUpload URL → PUT 成功 → 客户端崩溃/网络断开 → CompleteUpload 未调用
结果: S3 上有 blob，DB 无对应行
缓解: GC sweeper 的 orphan sweep 会清理（但 GC 默认 disabled！）
建议: 默认启用 GC + 设置合理的 min_orphan_age
```

### EC-2：并发去重竞争

```
用户同时上传两个相同内容的文件:
  goroutine A: FindByChecksum → 未找到 → store.Put → key_A
  goroutine B: FindByChecksum → 未找到 → store.Put → key_B
  goroutine A: 发现 key_A ≠ key_B → 删除 key_B → 使用 key_A
  goroutine B: 发现 key_B ≠ key_A → 删除 key_A → 使用 key_B
结果: 两个 blob 都被删除，或者都保留但指向同一 storage_key
建议: 在 service 层增加分布式锁（基于 checksum），或使用 PostgreSQL 的 advisory lock
```

### EC-3：Notification MarkRead 无所有权检查

```go
// internal/service/notification.go — MarkRead
func (s *notificationService) MarkRead(ctx context.Context, id domain.ID) error {
    _, err := mustActor(ctx)  // 只检查有 actor，不检查 recipient 所有权
    // ...
    s.notifs.MarkRead(ctx, id, readAt)  // repository 层直接 UPDATE
    // 不检查该通知的 recipient_uid == actor
}
```

这意味着**用户 A 可以标记用户 B 的通知为已读**（如果知道通知的 ID）。repository 层没有 recipient 过滤。

```go
// internal/repository/notification.go — MarkRead
func (r *notifRepo) MarkRead(ctx, id ID, readAt time.Time) error {
    return r.db.Model(&notifRow{}).
        Where("id = ?", id).
        Update("read_at", readAt).Error
    // ❌ 没有 AND recipient_uid = ?
}
```

建议：在 MarkRead 中增加 `WHERE recipient_uid = ?` 条件，使用 service 层传入的 actor uid 进行过滤。

---

## 优先级建议

| # | 方向 | 类型 | 优先级 | 工作量 | 理由 |
|---|------|------|--------|--------|------|
| 1 | **游标分页并发一致性** | 正确性 | **P0** | 中 | 影响所有分页列表，在高写入场景下必然出 bug |
| 2 | **SSO 账户生命周期** | 数据完整性 | **P0** | 中-高 | 合规要求（GDPR 删除权）+ 数据完整性 |
| 3 | **Outbox 去重与幂等投递** | 可靠性 | **P1** | 低-中 | 导致重复通知，影响用户体验 |
| 4 | **管理员操作台** | 运维 | **P1** | 中 | 客服/审核流程必须 |
| 5 | **存储配额** | 成本/产品 | **P2** | 中 | 初期可手动监控，用户增长后必须 |

### 快速修复（1 天内可完成）

- **EC-3（Notification MarkRead 所有权检查）**：在 repository 层的 WHERE 中增加 `recipient_uid = ?`
- **EC-1（默认启用 GC）**：将 `deployments/docker-compose.yml` 中的 `storage.gc.enabled` 设为 true
- **分页改进**：在所有 `ORDER BY id DESC` 改为 `ORDER BY created_at DESC, id DESC`——cursor 格式不变（旧的纯 id cursor 仍兼容），但排序稳定性大幅提升

---

以上分析基于 `main` 分支当前代码状态（2026-07-10），每个方向均附有可验证的代码引用。建议从 P0 方向开始实施，优先解决并发正确性和数据完整性这两个基础问题。
