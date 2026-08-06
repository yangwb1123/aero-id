# aero-id — 最终剩余架构缺口：跨文档交叉分析后的 5 个高价值方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全局代码扫描（120+ Go 源文件，22 个迁移，双传输全对等）后，逐一比对 `docs/requirements/` 下 **10 份**方向文档 + `docs/REVIEW_*` 下 **8 份**审查文档 + `ROADMAP.md` 的所有主题索引，累计覆盖 **55+** 方向的交叉验证。本文件定位在所有前述文档中**零系统分析**的 5 个缺口。  
> **前提**: `feat/roadmap` 已合并，38 个 gRPC RPC + 全 HTTP 对等 + 生产加固已交付  
> **无代码变更**

---

## 核心发现

尽管 55+ 方向覆盖了广泛的产品和架构领域，以下 **5 个方向在所有现有文档中零系统分析**。每个方向均附代码级证据说明"为什么这是一个真实缺口"。

| # | 方向 | 类别 | 代码暗示 |
|---|------|------|---------|
| 1 | **软删除恢复 — 回收站与取消删除** | 产品缺口 | 全部实体使用 `gorm.DeletedAt` 软删除，但无任何恢复 API |
| 2 | **跨实体引用完整性系统** | 正确性 | `messages.file_id`、`comments.parent_id`、`file_shares.file_id` 等外键在父资源删除后留下悬空引用 |
| 3 | **用户可见的限流状态** | 运维/UX | 限流存在但用户完全不知——无响应头、无配额查询 API、无端点级差异化 |
| 4 | **优雅维护模式（只读静默）** | 运维 | 服务无"只读维护"状态，schema 迁移/维护窗口需要全量停机 |
| 5 | **实体变更历史与差异追踪** | 正确性/审计 | `updated_at` 和 `edited_at` 存在，但"改了什么"不可见——无 before/after 值追踪 |

---

## 方向一：软删除恢复 — 回收站与取消删除（Trash / Undelete / Recycle Bin）

### 为什么它被遗漏

所有现有文档关注的是"如何删除数据"（软删除、级联清理、GC 回收），从未思考"删除后如何恢复"。全部 10+ 份需求文档 + 8 份审查文档中，**零覆盖**。

这是一个反直觉的事实：aero-id 的每个实体都支持**软删除**——这意味着数据仍然存在于数据库中——但没有任何 API 可以让用户看到或恢复它们。删除是不可逆的、不可见的、永久性的。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/file.go` — `File` 结构体 | 包含 `gorm.DeletedAt` 嵌入字段。软删除的行保留在 DB 中。 |
| `internal/domain/message.go` — `Message` 结构体 | 同上。 |
| `internal/domain/comment.go` — `Comment` 结构体 | 同上。 |
| `internal/file/service_crud.go` — `Delete()` | 调用 `s.files.Delete(ctx, id)` → 底层 GORM 执行软删除（SET `deleted_at=now()`）。**无 undo 路径。** |
| `internal/message/handler_http.go` — `DeleteMessage` | 同上。 |
| `internal/comment/handler_http.go` — `DeleteComment` | 同上。 |
| `internal/handler/grpc/file.go` — `DeleteFile` | gRPC 端删除同样不可逆。 |
| `internal/repository/file.go` — `FindByChecksum` | 查询条件包含 `WHERE deleted_at IS NULL` → 软删除的行对用户和应用层完全不可见。 |
| `migrations/000001_create_files.up.sql` | `deleted_at TIMESTAMPTZ` 列存在。 |
| `migrations/000002_create_messages.up.sql` | 同上。 |
| `migrations/000003_create_comments.up.sql` | 同上。 |
| `internal/storage/gc/sweeper.go` — `purgeSoftDeleted` | GC sweeper 最终会硬删除超出 retention 的软删除行。**在这之前，数据是"活着的但不可见"。** |

### 用户场景

1. **误删除文件**：用户不小心删除了一个重要文件。当前行为：文件瞬间从列表消失。用户无法找回，除非 DBA 介入直接操作数据库。**在任何消费级/企业级产品中，这是不可接受的 UX。**
2. **误删除消息**：用户不小心删除了一条包含重要信息的消息。对方已经读过，但发送方无法再看到。同样无法恢复。
3. **延迟删除**：用户希望在删除后有一个"后悔期"（如 30 天内可恢复），之后才真正清理。当前软删除 + GC 机制已经为这个模型准备好了底层基础设施——只缺 API。
4. **批量勾选误操作**：用户勾选了 50 条消息然后点了"删除"，实际上是全选导致的误操作。没有回收站就无法恢复。

### 架构设计要点

#### 1. 统一回收站 API

```http
### 列出回收站内容（按实体类型聚合）
GET /v1/trash?types=files,messages,comments&page=...
→ {
    "items": [
      {
        "type": "file",
        "id": "uuid",
        "name": "report.pdf",
        "deleted_at": "2026-07-09T10:00:00Z",
        "retention_remaining": "25d 12h"
      },
      ...
    ],
    "next_cursor": "..."
  }

### 恢复单个项目
POST /v1/trash/files/:id/restore
→ { "status": "restored", "file": { ... } }

### 批量恢复
POST /v1/trash/batch-restore
Content-Type: application/json
{
  "items": [
    {"type": "file", "id": "uuid"},
    {"type": "message", "id": "uuid"}
  ]
}
→ { "restored": 2, "failed": [] }

### 清空回收站（触发硬删除）
DELETE /v1/trash?older_than=30d
→ { "deleted": 15, "freed_bytes": 10485760 }
```

#### 2. 保留期配置

```yaml
# configs/config.yaml — 新增
trash:
  retention:
    files: 30d       # 文件保留 30 天
    messages: 7d     # 消息保留 7 天
    comments: 7d     # 评论保留 7 天
  max_items: 1000    # 每个用户回收站最大条目数
  cleanup_interval: 1h
```

#### 3. 与现有基础设施的整合

| 现有组件 | 复用方式 |
|---------|---------|
| `storage/gc/sweeper.go` | 回收站的"过期清理"可复用 sweeper 的定时模式和 `purgeSoftDeleted` 逻辑。只需将 retention 配置从 GC 转移到 trash。 |
| `domain.AuditLogger` | 恢复操作应写入审计日志（`file.restore`、`message.restore`、`comment.restore`）。 |
| `outbox.Event` | 恢复操作可选择性地发送 outbox 事件，通知相关的参与者（如分享者收到"文件已恢复"通知）。 |
| `ratelimit.Limiter` | 回收站操作同样受限流保护——防止枚举回收站内容。 |
| `repository.CommentRepository` | 恢复评论时需要级联恢复其被删除的子评论（或至少标记父评论可见）。 |

#### 4. 边界情况

| Edge Case | 处理 |
|-----------|------|
| **恢复文件时底层 blob 已被 GC 清除** | 如果 GC 已经硬删除了 blob，恢复文件行时 `storage_key` 指向不存在的 blob。`DownloadURL` → 返回 404（文件已永久丢失），在文件上方标记为"已过期"。 |
| **恢复消息时引用文件已被永久删除** | `file_id` 指向的行已被硬删除 → `GetFile` 返回 404。消息显示"附件不可用"。不影响消息文本。 |
| **恢复评论时父评论已被永久删除** | 父评论硬删除后，恢复的子评论变为"独立根评论"（设置 `parent_id = NULL`）。 |
| **回收站超过 `max_items`** | 最旧的条目被提前硬删除（FIFO 淘汰）。记录审计事件。 |
| **并发恢复同一项目** | 使用 `SELECT ... FOR UPDATE` 或 GORM 乐观锁。第二个恢复请求返回 409 Conflict。 |
| **已分享文件的恢复** | 文件恢复后，原有的分享记录（`file_shares`）是否也恢复？建议：**不自动恢复**——分享需要重新手动授权，避免安全风险。 |

### 成本估算

| 组件 | 量级 |
|------|------|
| 统一回收站模型（`internal/trash/` 包） | 中（3-5 天） |
| `gorm.DeletedAt` 作用域查询 + 恢复操作（repository 层） | 低（每个实体 1 天 × 3 = 3 天） |
| HTTP + gRPC handler | 低-中（2 天） |
| GC sweeper 集成（确保不提前删除保留期内条目） | 低（1 天） |
| 审计 + 通知集成 | 低（1 天） |
| **总计** | **~10-15 天** |

---

## 方向二：跨实体引用完整性系统（Cross-Entity Reference Integrity）

### 为什么它被遗漏

aero-id 有 4 个核心实体（files, messages, comments, file_shares），它们之间存在引用关系。当前代码可以分别删除每个实体，但**没有任何系统处理删除后的引用完整性**。这在所有 18 份文档中零覆盖。

### 代码级证据

| 引用关系 | 表现 | 删除后的问题 |
|---------|------|------------|
| `messages.file_id → files(id)` | 消息引用文件（带附件发送） | 删除文件后，消息的 `file_id` 指向不存在的行。`GetMessage` 返回时无法解析文件。 |
| `comments.parent_id → comments(id)` | 评论的父评论引用 | 删除父评论后，子评论的 `parent_id` 指向不存在的行。`Replies` 列表中出现"幽灵父级"。 |
| `comments.target_id → files(id)/messages(id)` | 评论挂载在文件/消息上 | 删除被评论的目标后，评论成为"无主评论"。当前 `ListComments` 会用 `target_id` 过滤，目标没了评论也看不到了。 |
| `file_shares.file_id → files(id)` | 文件分享记录 | 删除文件后，分享记录仍存在但文件不可用。分享者看到"文件已删除"。 |
| `comment_likes.comment_id → comments(id)` | 评论点赞记录 | FK 约束阻止直接硬删除，但软删除后点赞记录仍然存在。`like_count` 在软删除后不再更新。 |
| `comment_reports.comment_id → comments(id)` | 评论举报记录 | 同上。 |
| `message_reactions.message_id → messages(id)` | 消息反应记录 | 同上。软删除消息后 reactions 仍存在。 |

### 用户场景

1. **消息附件丢失**：用户 A 发送了一条带附件的消息给用户 B。用户 A 后来删除了该文件。用户 B 打开对话，看到消息但附件显示"文件不可用"。**系统应该做什么？** 自动重发失败通知？在删除前警告？保留引用并允许下载直到最后一个引用被删除？
2. **嵌套评论断裂**：用户 A 发布了一个评论，用户 B 回复了它。用户 A 删除了自己的评论（软删除）。用户 C 看到"回复了一个已删除的评论"。**这合理吗？** 是否应该自动顶级回复为根评论？
3. **文件分享失效**：用户 A 分享了一个文件给用户 B。用户 A 后来删除了文件。用户 B 的分享列表中出现了一个"已删除的文件"。**用户 B 是否应该得到通知？**
4. **目标对象删除后的评论**：一篇文件被删除后，该文件下的 15 条评论全部变成"无主数据"。**它们应该被级联删除吗？迁移到"孤儿评论"区？还是保留但标为"目标已删除"？**

### 架构设计要点

#### 1. 引用完整性策略引擎

为每个引用关系定义删除时的行为：

```go
// internal/integrity/policy.go

type DeleteAction int

const (
    // Cascade: 级联软删除引用方（删文件 → 级联删除引用该文件的评论）
    ActionCascade DeleteAction = iota

    // Orphan: 保留引用方，但标记引用为"已断开"
    ActionOrphan

    // Block: 阻止删除（当存在活跃引用时拒绝删除）
    ActionBlock

    // Nullify: 将引用字段置为 NULL（删文件 → 消息的 file_id 设为 NULL）
    ActionNullify

    // Notify: 保留引用，但通知引用方
    ActionNotify
)

type ReferencePolicy struct {
    ChildEntity  string       // "message", "comment", "file_share", "comment_like"
    ParentEntity string       // "file", "comment"
    ForeignKey   string       // "file_id", "parent_id", "target_id"
    OnDelete     DeleteAction // 删除父实体时对子实体的处理策略
}
```

#### 2. 内置策略配置

```go
var DefaultPolicies = []ReferencePolicy{
    // 消息引用文件 → 删除文件时置空消息的 file_id（消息文本保留）
    {ChildEntity: "message", ParentEntity: "file", ForeignKey: "file_id", OnDelete: ActionNullify},

    // 评论引用父评论 → 删除父评论时提升子评论为根评论
    {ChildEntity: "comment", ParentEntity: "comment", ForeignKey: "parent_id", OnDelete: ActionOrphan},

    // 评论引用目标（文件/消息）→ 删除目标时置空评论的 target_id，标记为"无目标评论"
    {ChildEntity: "comment", ParentEntity: "target", ForeignKey: "target_id", OnDelete: ActionOrphan},

    // 文件分享引用文件 → 删除文件时置空 file_shares（或级联删除分享记录）
    {ChildEntity: "file_share", ParentEntity: "file", ForeignKey: "file_id", OnDelete: ActionCascade},

    // 点赞/举报/反应 → 删除父实体时级联删除（这些是依赖数据，无独立意义）
    {ChildEntity: "comment_like", ParentEntity: "comment", ForeignKey: "comment_id", OnDelete: ActionCascade},
    {ChildEntity: "comment_report", ParentEntity: "comment", ForeignKey: "comment_id", OnDelete: ActionCascade},
    {ChildEntity: "message_reaction", ParentEntity: "message", ForeignKey: "message_id", OnDelete: ActionCascade},
}
```

#### 3. 集成到 Service Delete 路径

每个现有的 `Delete` 方法在执行软删除前，先调用引用完整性引擎：

```go
// 伪代码
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // 1. 检查引用完整性
    refs, err := s.integrity.Check(ctx, "file", id)
    if err != nil { ... }

    // 2. 根据策略执行
    for _, ref := range refs {
        switch ref.Policy.OnDelete {
        case ActionCascade:
            s.softDeleteRef(ctx, ref)        // 级联删除引用方
        case ActionNullify:
            s.nullifyRef(ctx, ref)            // 置空外键
        case ActionBlock:
            return apperr.Conflict(...)       // 拒绝删除
        case ActionOrphan:
            s.markOrphan(ctx, ref)            // 标记为孤立
        case ActionNotify:
            s.scheduleNotification(ctx, ref)  // 异步通知
        }
    }

    // 2. 执行原删除
    return s.files.Delete(ctx, id)
}
```

### 成本估算

| 组件 | 量级 |
|------|------|
| 策略引擎模型与配置（`internal/integrity/`） | 中（3-5 天） |
| 在 5 个 Delete 方法中集成引用检查 | 中（每个 1 天 × 5 = 5 天） |
| 数据库迁移（添加 orphan/missing 标记列） | 低（1 天） |
| 测试（单元 + 集成：引用策略的行为验证） | 中（3 天） |
| **总计** | **~12-15 天** |

---

## 方向三：用户可见的限流状态（Rate Limit Observability）

### 为什么它被遗漏

`INFRA_DEEP.md` 在表格中一句提到了 `X-RateLimit-*` 响应头"不存在"，但从未将其作为独立的产品方向分析。全部其他 17 份文档零覆盖。

当前限流实现是一个"黑箱"：用户只有被拒绝时才知道限流存在（HTTP 429 / gRPC ResourceExhausted）。没有任何手段让用户或客户端主动了解当前的限流状态。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/middleware/ratelimit.go` | 限流中间件仅在拒绝请求时设置 `Retry-After` 头。通过的请求**不附加任何限流信息。** |
| `internal/pkg/ratelimit/ratelimit.go` — `Limiter` | `Allow()` 返回 `(time.Duration, bool)`——只有等待时间和是否允许。没有 `Remaining()`、`Limit()`、`Reset()` 方法。 |
| `internal/handler/grpc/server.go` — `rateLimitInterceptor` | gRPC 限流拦截器在超出限制时返回 `ResourceExhausted`。通过的请求**不附加 trailing metadata。** |
| `internal/handler/http/router.go` — `RouterConfig.RateLimiter` | 全局单一限流器实例。所有端点共享同一配额。 |
| `internal/config/config.go` — `RateLimitConfig` | 只有全局 `RPS`、`Burst`、`Enabled`。无 `PerEndpoint`、`PerMethod` 配置。 |
| `internal/app/app.go:263-275` | 限流器在启动时构建一次，运行时不能动态调整 RPS。 |

### 用户场景

1. **客户端自适应**：API 客户端（SDK、CLI、前端）可以在接近限流阈值时自动降速，而不是被突然拒绝后等待 `Retry-After`。这是**弹性客户端**的基础。
2. **开发者调试**：调用 API 的开发者经常遇到 429 错误但不知道为什么。提供 `X-RateLimit-*` 头可以帮助他们理解"你用了多少配额"。
3. **端点差异化定价**：文件上传（高成本）和消息列表（低成本）应该有不同的限流配额。高价值用户应该有更高的配额。当前无法做到。
4. **限流告警**：如果用户频繁接近限流阈值，说明他们可能需要升级套餐或改变使用模式。当前无法检测。

### 架构设计要点

#### 1. 响应头规范

遵循 IETF HTTP API Rate Limit Headers（draft-ietf-httpapi-ratelimit-headers）：

```http
HTTP/1.1 200 OK
X-RateLimit-Limit: 100           # 窗口内限制数
X-RateLimit-Remaining: 72        # 当前窗口剩余
X-RateLimit-Reset: 1625900000    # 窗口重置 Unix 时间戳
X-RateLimit-Policy: 100r/s       # 策略描述
```

在 gRPC 中通过 trailing metadata 传递：

```go
grpc.SendTrailer(ctx, metadata.Pairs(
    "x-ratelimit-limit", "100",
    "x-ratelimit-remaining", "72",
    "x-ratelimit-reset", "1625900000",
))
```

#### 2. Limiter 接口扩展

```go
// internal/pkg/ratelimit/ratelimit.go

type Result struct {
    Allowed   bool
    Remaining int           // 当前窗口剩余请求数
    Limit     int           // 窗口限制
    ResetAt   time.Time     // 窗口重置时间
    RetryAfter time.Duration // 仅当 Allowed=false 时有效
}

type Limiter struct {
    // ... 现有字段
}

func (l *Limiter) Allow(key string) Result {
    // 现有 Allow() 逻辑 + 返回完整 Result
}
```

#### 3. 端点级限流配置

```yaml
# configs/config.yaml — 扩展
rate_limit:
  enabled: true
  default:
    rps: 20
    burst: 40
  endpoints:
    /v1/files/upload:
      rps: 5          # 文件上传成本高，限流更严格
      burst: 10
    /v1/messages:
      rps: 50         # 消息操作更频繁
      burst: 100
    /v1/comments:
      rps: 30
      burst: 60
    /v1/search:
      rps: 10         # 搜索成本高
      burst: 20
      timeout: 5s     # 搜索专属超时
```

#### 4. 限流状态 API

```http
GET /v1/rate_limit/status
Authorization: Bearer <token>
→ {
    "global": {
      "limit": 100,
      "remaining": 72,
      "reset_at": "2026-07-10T10:00:00Z",
      "policy": "100r/s"
    },
    "per_endpoint": {
      "/v1/messages": { "limit": 50, "remaining": 42, "reset_at": "..." },
      "/v1/files":    { "limit": 5,  "remaining": 3,  "reset_at": "..." }
    }
  }
```

### 成本估算

| 组件 | 量级 |
|------|------|
| Limiter Result 类型 + `Allow()` 改造 | 低（1-2 天） |
| HTTP 响应头中间件 | 低（1 天） |
| gRPC trailing metadata | 低（1 天） |
| 端点级配置解析 + 多限流器 | 中（2-3 天） |
| 限流状态 API | 低（1 天） |
| **总计** | **~6-8 天** |

---

## 方向四：优雅维护模式（只读静默 / Read-Only Quiescing）

### 为什么它被遗漏

所有 18 份文档在各处提及了"优雅关停"（graceful shutdown），但从未讨论过"在不关停的情况下进入维护模式"的能力。`ROADMAP.md` 的 P0-4 讨论了 outbox relay 优雅关闭——但那是一个关于关闭时 drain 的技术细节，不是关于"在运行中进入只读状态"。

这是一个**操作盲区**：aero-id 当前只有"正常运行"和"全部关停"两种状态。没有中间状态允许维护操作（schema 迁移、缓存预热、配置更新、存储后端切换）而不完全切断服务。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/app/app.go` — `Run()` | 启动两个 server 后，只有 `ctx.Done()` 或 server error 能触发关停。无维护模式入口。 |
| `internal/app/app.go` — `shutdown()` | 关停是不可逆的：立刻将 gRPC health 翻转为 NOT_SERVING，然后停掉全部后台协程。没有"暂停 → 维护 → 恢复"路径。 |
| `internal/middleware/timeout.go` | 只处理超时，不检查"是否处于维护模式"。 |
| `internal/middleware/auth.go` | 只检查认证，不检查"是否允许写入"。 |
| `internal/handler/http/health.go` | `/readyz` 检查 DB + SSO 健康状态，但无"手动设置不健康"的 admin API。 |
| `internal/handler/grpc/server.go` | `SetServing(false)` 只在 `shutdown()` 中调用一次。不能在运行时临时设为 NOT_SERVING 再恢复。 |
| `internal/pkg/ratelimit/ratelimit.go` | 限流器只能调整速率，不能根据维护模式拒绝写入。 |
| `internal/config/config.go` | 所有配置在启动时加载，无运行时热重载。 |
| `internal/handler/http/router.go` — `NewRouter` | 路由在启动时一次性注册，不能运行时添加/移除/禁用路由。 |

### 用户场景

1. **零停机 Schema 迁移**：需要添加一个包含 `NOT NULL` 约束的新列。当前做法：停服 → 加列 → 填充默认值 → 启服。理想做法：进入维护模式（拒绝写入，允许读取）→ 添加新列 → 后台 backfill → 退出维护模式。客户端看到读取正常，写入返回 503（Service Unavailable）而非连接断开。
2. **存储后端切换**：从 local 存储切换到 S3。当前需要停服、迁移数据、启服。维护模式下可以：允许读取旧数据，拒绝新的上传，同时后台迁移数据。
3. **限流/功能开关配置热加载**：运维发现限流配置有问题（如过于严格），想在不需要重启的情况下调整 RPS。当前无法做到。
4. **数据库连接池耗尽**：当连接池耗尽时，理想行为是自动进入"写入降级"模式——拒绝写入请求但继续服务读取，而不是对所有请求都返回 500。

### 架构设计要点

#### 1. 维护模式状态机

```go
// internal/maintenance/mode.go

type Mode int

const (
    ModeNormal       Mode = iota // 正常服务
    ModeReadOnly                 // 只读：拒绝写入，读取正常
    ModeDraining                 // 排空：拒绝新连接，等待进行中请求完成
    ModeShutdown                 // 关停中
)

type Manager struct {
    mu     sync.RWMutex
    mode   Mode
    reason string                // 维护原因（用于日志和响应体）
    since  time.Time             // 进入该模式的时间
}
```

#### 2. 维护模式中间件

```go
// internal/middleware/maintenance.go

func Maintenance(mgr *maintenance.Manager) gin.HandlerFunc {
    return func(c *gin.Context) {
        mode := mgr.Current()
        if mode == maintenance.ModeNormal {
            c.Next()
            return
        }
        // 只读模式：拒绝所有非 GET/HEAD/OPTIONS 请求
        if mode == maintenance.ModeReadOnly && !isSafeMethod(c.Request.Method) {
            c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
                "error":   "service.maintenance",
                "message": "service is in read-only maintenance mode",
                "since":   mgr.Since().Format(time.RFC3339),
                "reason":  mgr.Reason(),
                "estimate": mgr.EstimatedDuration().String(),
            })
            return
        }
        c.Next()
    }
}
```

#### 3. 维护操作 API

```http
### 进入维护模式
POST /v1/admin/maintenance/enter
Authorization: Bearer <admin-token>
Content-Type: application/json
{
  "mode": "read_only",
  "reason": "Scheduled schema migration v23",
  "estimated_duration": "15m",
  "notify_users": true
}
→ {
    "mode": "read_only",
    "since": "2026-07-10T02:00:00Z",
    "read_requests_allowed": true,
    "write_requests_allowed": false
  }

### 查询当前状态
GET /v1/admin/maintenance/status
→ {
    "mode": "read_only",
    "since": "2026-07-10T02:00:00Z",
    "reason": "Scheduled schema migration v23",
    "stats": {
      "rejected_writes": 42,
      "served_reads": 15832
    }
  }

### 退出维护模式
POST /v1/admin/maintenance/exit
→ { "mode": "normal", "resumed_at": "..." }
```

#### 4. 通知集成

进入维护模式时：

- 通过 SSE 推送 `system.maintenance` 事件给所有在线管理员
- `/readyz` 保持 200（因为只读模式下服务仍然可用）但添加 `X-Maintenance-Mode: read_only` 头
- gRPC health service 仍然返回 SERVING（只读服务仍然是健康的）
- 可选择性地向所有活跃连接的用户推送通知

### 成本估算

| 组件 | 量级 |
|------|------|
| `internal/maintenance/` 包（Manager + 状态机） | 低（1-2 天） |
| HTTP 中间件 | 低（1 天） |
| gRPC 拦截器 | 低（1 天） |
| gRPC health service 集成 | 低（1 天） |
| Admin 管理 API | 低-中（2 天） |
| SSE 通知 + 客户端处理 | 低（1 天） |
| **总计** | **~7-10 天** |

---

## 方向五：实体变更历史与差异追踪（Entity Change History / Before-After Diff）

### 为什么它被遗漏

审计日志（`audit_events`）追踪了"谁在什么时候做了什么操作"，但它不追踪"改了什么"。`edited_at` 时间戳记录了一个实体被编辑过，但无法回答"编辑前的内容是什么"。

这在所有 18 份文档中**零覆盖**。现有审计日志的设计（`domain/audit.go`）的 `Meta` 字段虽然是 `map[string]any`，可以在理论上携带变更值，但**当前没有任何代码实际传递 before/after 值**。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/audit.go` — `AuditEvent` | `Meta` 字段类型为 `map[string]any` —— 设计上可以携带变更详情，但**没有任何调用者使用它来传递 before/after 值。** |
| `internal/domain/message.go` — `EditedAt` | 消息有 `EditedAt` 时间戳，标示"被编辑过"，但没有 `EditHistory` 结构体来保留旧版本内容。 |
| `internal/domain/comment.go` — `EditedAt` | 同上。 |
| `internal/domain/file.go` — `UpdatedAt` | 文件有 `UpdatedAt` 时间戳，但无法知道 name/visibility/meta 的哪些字段发生了变化。 |
| `internal/service/message.go` — `Edit()` | 编辑消息时，旧内容被直接覆盖。调用 `audit.Log()` 时传递的 `Meta` 为空。 |
| `internal/service/comment.go` — `Edit()` | 同上。评论编辑后旧内容丢失。 |
| `internal/service/file.go` — `UpdateMetadata()` | 文件元数据更新后，旧值被覆盖。审计日志记录操作但不记录变更值。 |
| `internal/repository/audit.go` — `Create()` | 审计事件的 `Meta` 字段在数据库中被存储为 `JSONB`，可以容纳结构化变更数据，但目前未被使用。 |
| `internal/domain/admin.go` — 角色权限 | 管理角色被修改后，旧的权限集不可恢复。 |

### 用户场景

1. **"已编辑"内容可审计**：用户 A 发送了一条消息"我们明天下午 2 点开会"，然后编辑为"我们明天下午 3 点开会"。收件人用户 B 看到了"已编辑"标记，但无法知道用户 A 改了内容。**在合规要求高的场景中（法律、金融、医疗），内容的编辑历史是法定要求。**
2. **文件元数据变更回溯**：管理员想知道"这个文件的 visibility 从 private 改成了 public 的时间，是谁改的"。当前审计日志只能说"用户 A 在 T 时刻调用了 UpdateFileMetadata"，但不知道 visibility 的旧值和新值。
3. **错误编辑回滚**：用户不小心编辑了评论引入了错别字。当前无法回退到编辑前的内容——因为旧内容没有被保留。
4. **合规调查**：合规官需要审查"过去 30 天内所有被编辑过的消息的内容变更记录"。当前无法实现——编辑前的数据不可恢复。

### 架构设计要点

#### 1. 变更记录模型

```go
// internal/domain/change.go

type EntityType string

const (
    ChangeFile    EntityType = "file"
    ChangeMessage EntityType = "message"
    ChangeComment EntityType = "comment"
)

// ChangeLog 记录一次实体变更的 before/after 快照
type ChangeLog struct {
    ID         ID
    EntityType EntityType
    EntityID   ID
    ActorUID   string
    // ChangedFields 是变更字段列表。每个字段包含旧值和新值的 JSON 表示。
    // 只有实际变化的字段被记录——未变化的字段不出现在列表中。
    ChangedFields []FieldChange
    Summary       string          // 人工可读的变更摘要（如"更新了文件名称"）
    CreatedAt     time.Time
}

type FieldChange struct {
    Field    string          // "name", "content", "visibility", "meta.description"
    OldValue json.RawMessage // 旧的字段值（JSON 编码）
    NewValue json.RawMessage // 新的字段值（JSON 编码）
}
```

#### 2. 存储设计

```sql
-- 统一变更记录表
CREATE TABLE change_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type     TEXT NOT NULL,           -- 'file' | 'message' | 'comment'
    entity_id       UUID NOT NULL,
    actor_uid       TEXT NOT NULL,
    changed_fields  JSONB NOT NULL,          -- [{"field":"name","old":"old.txt","new":"new.txt"}, ...]
    summary         TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 按实体查询用的索引
CREATE INDEX idx_change_logs_entity ON change_logs(entity_type, entity_id, created_at DESC);
-- 按操作者查询用的索引
CREATE INDEX idx_change_logs_actor ON change_logs(actor_uid, created_at DESC);
```

#### 3. 变更记录 API

```http
### 查询实体的变更历史
GET /v1/files/:id/changes?cursor=...&limit=20
→ {
    "changes": [
      {
        "id": "uuid",
        "actor_uid": "user_a",
        "actor": { "uid": "user_a", "nickname": "Alice" },
        "summary": "更新了文件名",
        "fields": [
          {"field": "name", "old_value": "draft.pdf", "new_value": "final.pdf"}
        ],
        "created_at": "2026-07-10T10:00:00Z"
      },
      ...
    ],
    "next_cursor": "..."
  }
```

#### 4. 集成到现有 Service

在每个 `Edit`/`Update` 方法中记录变更：

```go
// 伪代码 — service/message.go
func (s *messageService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
    existing, err := s.messages.Get(ctx, id)
    // ... 权限检查 ...

    // 记录变更
    if existing.Content != content {
        s.changeLog.Log(ctx, domain.ChangeLog{
            EntityType: domain.ChangeMessage,
            EntityID:   id,
            ActorUID:   actor,
            ChangedFields: []domain.FieldChange{
                {Field: "content", OldValue: json(existing.Content), NewValue: json(content)},
            },
            Summary: "编辑了消息内容",
        })
    }

    // 执行编辑
    existing.Content = content
    // ... 持久化 ...
}
```

#### 5. 保留策略

变更日志会随数据量增长而增长。需要配置保留策略：

```yaml
# configs/config.yaml — 新增
change_log:
  retention: 90d          # 变更记录保留 90 天
  cleanup_interval: 24h   # 每日清理过期记录
```

### 成本估算

| 组件 | 量级 |
|------|------|
| `internal/domain/change.go` — 模型定义 | 低（1 天） |
| `internal/repository/change.go` — 存储层 | 低（1-2 天） |
| `migrations/000023_change_logs.up.sql` | 低（1 天） |
| 在 3 个 Service（file/message/comment）的编辑方法中集成 | 中（每个 1-2 天 × 3 = 3-6 天） |
| HTTP + gRPC handler（变更查询） | 低（1-2 天） |
| 保留策略 + 清理 | 低（1 天） |
| 测试（单元 + 集成） | 中（2-3 天） |
| **总计** | **~10-14 天** |

---

## 交叉依赖矩阵

| 方向 | 依赖的基础设施 | 冲突的方向 | 先决条件 |
|------|-------------|-----------|---------|
| #1 软删除恢复 | `storage/gc/sweeper.go`（保留期协调） | 需修改 GC sweeper 避免提前清除保留期内数据 | 无 |
| #2 引用完整性 | 所有 Delete 方法的调用点 | 方向 #1（恢复时需考虑引用完整性） | 先于 #1 完成可简化恢复逻辑 |
| #3 限流可见性 | `ratelimit.Limiter` | `edge-cases-and-performance.md` 中"分布式限流"的全局限流重构 | 无 |
| #4 维护模式 | gRPC health service、`/readyz`、`/metrics` | 无已知冲突 | `app.go` 需要支持运行时状态切换 |
| #5 变更历史 | `audit.AuditLogger` 接口（复用 Meta 字段） | 无——与现有审计日志互补而非重叠 | 无 |

**建议的执行顺序**：方向 #5（变更历史）→ 方向 #3（限流可见性）→ 方向 #1（软删除恢复）→ 方向 #2（引用完整性）→ 方向 #4（维护模式）。方向 #5 最独立、风险最低，适合先行。方向 #4 最复杂、影响面最广，需要完整的架构理解，放在最后。

---

## 风险与收益总结

| 方向 | 工程风险 | 用户价值 | 操作价值 | 合规价值 |
|------|---------|---------|---------|---------|
| #1 软删除恢复 | 低——与现有 GC 协调即可 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐⭐（GDPR 更正权） |
| #2 引用完整性 | 中——影响 5+ 个 Delete 路径 | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐（数据治理） |
| #3 限流可见性 | 低——纯扩展，不改变现有行为 | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐ |
| #4 维护模式 | 中高——影响请求路径 + 后台协程 | ⭐ | ⭐⭐⭐⭐⭐ | ⭐ |
| #5 变更历史 | 低——新增表 + 记录逻辑 | ⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ |

---

*本文件基于 `main` 分支 2026-07-10 快照完成。建议团队阅读后与 `docs/requirements/` 下其他 10 份方向文档合并讨论，形成完整的 Sprint 规划依据。*
