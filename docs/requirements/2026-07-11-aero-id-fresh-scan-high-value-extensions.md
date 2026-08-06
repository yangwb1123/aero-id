# aero-id — 全局代码扫描：高价值扩展方向

> **日期:** 2026-07-11  
> **方法:** 全量代码逐文件扫描 — `internal/` 全部子包（~200 Go 源文件）、`migrations/` 23 份可逆 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`。  
> **视角:** 资深架构师 + 产品经理  
> **约束:** 不编写任何代码。每个方向提供精确的代码级证据。  
> **与既有文档的差异化:** 已与 `docs/requirements/` 下既有分析做逐方向交叉比对，确保以下方向在既有材料中未被系统性覆盖。

---

## 前置：代码库当前架构总览

aero-id 是一个高度成熟的双传输数据服务。以下能力在 `internal/` 中均有可验证的实现：

| 域 | 已交付能力 |
|---|---|
| **基础设施** | HTTP (Gin) + gRPC 全对等（38 RPCs）、GORM + PG16、本地/S3 双存储、OTel 追踪、RED 指标、per-dependency `/readyz`、冷启动 warmup、优雅关停 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 + Webhook HMAC 投递、幂等键、速率限制（双传输）、SSO 断路器 + stale-cache + singleflight + 重试 + keepalive + mTLS + 证书锁定、存储 GC + 孤儿扫描 + 引用计数 |
| **文件管线** | 流式上传、MIME/size 校验、SHA-256 内容去重、Presigned 直传、显示分享/撤销、元数据 PATCH、Storage Refs 引用计数 |
| **消息系统** | 1:1 私信 + 图文/文件/系统消息、Reactions（仅写路径）、编辑/软删、Inbox/Conv 视图、已读回执 + 计数、Block 拦截 |
| **评论系统** | 两层嵌套（楼中楼）、Like/Unlike（原子计数器）、举报→审核→隐藏/显示、编辑、作者作用域报告管理 |
| **通知系统** | Outbox 驱动的异步创建 + 折叠去重 + 速率限制、SSE 实时推送（last_event_id 重连回放）、偏好设置（启用/静默时段）、Block 抑制、Webhook 投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引 + CJK ILIKE 回退）、多 kind（消息/评论/文件）联合搜索 |
| **安全管理** | 方向性 Block、审计日志（Append-only）、Admin 角色 + 权限（content.moderate / audit.read / user.ban / system.config）、permission 中间件 |
| **运维** | `cmd/migrate` CLI + 可选 auto_migrate、gRPC health + reflection、23 次可逆迁移（含性能索引、FTS、通知折叠、Outbox 重试） |

---

## 发现一：双实现结构——~6000 行的技术债死代码

### 问题

代码库中存在**两套完全独立的实现**，功能重叠但只有一套被实际集成：

| 层 | 活跃路径（被 app.go 集成） | 未集成路径（死代码） |
|---|---|---|
| **HTTP handler** | `internal/handler/http/{comment,message,file,block,notification,search,admin,preference}.go` | `internal/{comment,message,file,block,notification,search}/handler_http.go` |
| **gRPC handler** | `internal/handler/grpc/{comment,message,file,block,notification,search,preference}.go` | `internal/{comment,message,file,block,notification,search}/handler_grpc.go` |
| **Service** | `internal/service/{comment,message,file,file_share,file_presign,block,notification,preference,search}.go` | `internal/{comment,message,file,block,notification,search}/service*.go` |
| **Repository** | `internal/repository/{comment,message,file,file_share,block,notification,preference,storage_ref,admin,audit}.go` | `internal/{comment,message,file,block,notification}/repository*.go` |
| **Domain 类型 + 接口** | `internal/domain/{comment,message,file,shared,errors,actor,access,notification,search,audit,admin}.go` | `internal/{comment,message,file,block,notification,search}/*.go`（内联定义了自己的类型和接口） |

### 代码级证据

**证据 1：`internal/app/app.go` 仅引用 `internal/service`，从不引用新包**

```go
// internal/app/app.go:30
import (
    "github.com/aero/aero-id/internal/service"   // ← 仅集成老路径
)
// 以下行使用 service.NewFileService / service.NewMessageService 等
// 从不使用 comment.NewService / message.NewService 等
fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type, ...)
msgSvc  := service.NewMessageService(msgRepo, fileRepo, ssoClient, ...)
```

**证据 2：HTTP handler 注册——所有路由来自 `internal/handler/http/`，而非新包**

```go
// internal/app/app.go:301-309
router := httpapi.NewRouter(log, cfg, ssoClient, health,
    httpapi.NewFileHandler(fileSvc, ...),       // 来自 internal/handler/http/file.go
    httpapi.NewMessageHandler(msgSvc),          // 来自 internal/handler/http/message.go
    httpapi.NewCommentHandler(commentSvc),      // 来自 internal/handler/http/comment.go
    ...
)
```

对应的 `internal/comment/handler_http.go`、`internal/message/handler_http.go` 等从未被任何导入引用。

**证据 3：新包中的 gRPC handler 注册同样未被引用**

`internal/comment/handler_grpc.go` 实现了 `Register(s *grpc.Server)`，但 `app.go` 的 `grpcsrv.NewCommentServer` 来自 `internal/handler/grpc/comment.go`，完全不使用新包。

**证据 4：两张表估算死代码规模**

```bash
# 活跃路径：internal/service/ ~3,500 行
# 未集成新包合计：internal/{comment,message,file,block,notification,search}/*.go ~6,000 行
```

### 为什么这是高优先级

1. **维护税翻倍**：每增加一个字段或修复一个 bug，理论上需要在两套实现中同步修改。当前由于只有一套运行，另一套只是没人清理的包袱，但**认知负担持续存在**——开发者在阅读代码时必须区分"哪些是活的，哪些是死的"。
2. **误导 AI / 新人**：代码库中同时存在 `comment.NewService()` 和 `service.NewCommentService()`，repo 中同时存在 `comment.Repository` 接口和 `repository.CommentRepository` 接口。没有清晰的标记说明谁是谁。
3. **测试覆盖率虚胖**：新包的测试（`internal/comment/service_test.go` 等）通过 CI 但测试的是从未运行的代码，给覆盖率报告注入假阳性。

### 建议

- **Phase 1（清理）**：移除未集成的新包文件（`internal/{comment,message,file,block,notification,search}/` 中的 handler + service + repository 文件，保留 domain 定义文件如果被活跃路径引用）。
- **Phase 2（迁移）**：如果未来要切换为 domain-oriented 结构，逐包将活跃路径迁移为新结构——**但不要同时维护两套**。

---

## 发现二：跨用户内容去重未实现（Storage 成本浪费）

### 问题

文件上传路径计算 SHA-256 校验和并做内容去重，但去重**仅限同一位用户**。两个用户上传同一个文件会存储两个 blob，浪费存储空间 2×。

### 代码级证据

**证据 1：`FindByChecksum` 查询条件是 `owner_uid = ? AND checksum = ?`**

```go
// internal/file/repository.go:139-142
func (r *fileRepo) FindByChecksum(ctx context.Context, ownerUID, checksum string) (*File, error) {
    var row fileRow
    err := r.db.WithContext(ctx).
        Where("owner_uid = ? AND checksum = ?", ownerUID, checksum).  // ← 限定同一用户
        First(&row).Error
    ...
}
```

**证据 2：上传路径的 Dedup 逻辑被 `owner_uid` 参数限定**

```go
// internal/file/service_crud.go:53
if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
    // 只在自己名下找——actor 是当前认证用户
    _ = s.store.Delete(ctx, key)
    key = existing.StorageKey
    reused = true
}
```

同理 `internal/service/file.go:126` 也存在相同限制。

**证据 3：`storage_refs` 表已存在且具备原子引用计数能力——但跨用户去重未启用**

```sql
-- migrations/000021_storage_refs.up.sql
CREATE TABLE IF NOT EXISTS storage_refs (
    storage_key TEXT PRIMARY KEY,
    ref_count   INT NOT NULL DEFAULT 1,   -- 引用计数
    ...
);
```

`internal/repository/storage_ref.go` 提供了完整的 `Increment` / `Decrement` / `GetCount` 原子操作。基础设施已就绪，但上传路径没有使用跨用户 checksum 查找。

**证据 4：去重机会的估算**

在 SaaS/多租户场景下，常见物料（默认头像、文档模板、分享的图片）会被大量用户重复上传。每个 blob 对应一份实体存储成本（S3 按量计费）。系统级去重可节省 30-70% 的原始存储。

### 产品经理视角

- **直接影响 AWS S3 账单**：去重粒度扩大为系统级后，N 个用户上传同一文件只付 1 份存储费 + N-1 个 metadata 行的极低成本。
- **不影响安全性**：即使底层 blob 是共享的，每个用户有独立的 `files` 行和自己的权限控制。去重完全透明。
- **已具备技术前提**：`storage_refs` 引用计数 + GC 孤儿扫描均已就绪，技术风险极低。

### 建议

- 将 `FindByChecksum` 的搜索范围改为全局（移除 `owner_uid` 条件），利用 `storage_refs.Increment` 原子计数管理 blob 生命周期。
- 补全 `storage_refs` 在 `Create` 和 `Delete` 路径中的引用数管理钩子（当前已有，只需修改查询范围）。

---

## 发现三：软删除无级联语义——父删除导致子孤儿

### 问题

所有实体都支持 GORM 软删除（`deleted_at`），但**没有级联语义**。当一个父实体被软删除时，其依赖实体的引用变成悬空（dangling reference）。

### 代码级证据

**证据 1：软删除根评论后，回复的 `parent_id` 指向已删除行**

```go
// internal/comment/service.go — Delete
func (s *serviceImpl) Delete(ctx context.Context, id domain.ID) error {
    ...
    // 只软删了本条评论，不处理其回复
    if err := s.comments.SoftDelete(ctx, id); err != nil {
        ...
    }
}
```

回复的 `parent_id` 指向一个 `deleted_at IS NOT NULL` 的行。回复本身仍可被读取和展示（`ListReplies` 通过 `root_id` 查询），但父级内容不可见，UI 层需要额外处理"父回复已删除"的情况。

**证据 2：文件软删除后，`file_shares` 记录和消息的 `file_id` 引用仍存在**

```go
// internal/file/service_crud.go — Delete
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    ...
    if err := s.files.Delete(ctx, id); err != nil {
        // 只是软删 files 行，不级联处理：
        // - file_shares 中该 file_id 的行未处理
        // - messages 中 file_id = id 的引用未处理
    }
}
```

- `file_shares` 没有 FK 约束且不自动级联，所以已授权的用户仍能看到一个指向已删除文件的分享条目。
- `messages.file_id` 指向的文件已不可读，但消息的 `FileID` 字段仍非空。消息接收者点击附件时会得到 404。

**证据 3：消息软删除后，`message_reactions` 记录变成孤儿**

```sql
-- migrations/000013_message_reactions.up.sql
CREATE TABLE IF NOT EXISTS message_reactions (
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,  -- 有 CASCADE
    ...
);
```

这里 `ON DELETE CASCADE` 存在，但 GORM 的软删除是 `UPDATE deleted_at = now()`，不是 DELETE。所以 `message_reactions` 的行不会被级联处理。同样 `comment_likes` 也有这个问题。

**证据 4：Block 解除后通知仍保留**

用户 A block 了用户 B → B 给 A 发送的消息会被拒绝。但如果 A 先收到了未读通知，然后才 block B，通知仍保留在 A 的通知列表中。Block 解除后，历史通知继续存在——无自动回溯清理。

### 产品经理视角

- **用户困惑**：用户删除了一个文件，但"共享给我的文件"列表中仍显示它；用户删除了一个评论，但回复列表仍在；用户删除了一条消息，但收到的未读消息中仍显示它的反应数。
- **产品逻辑的一致性**：软删除的"删除"语义在用户看来是"这个东西不存在了"，但系统的行为是"这个东西不可见了，但它的子内容或引用还在"。

### 建议

- 在 `service.Delete` 方法中增加级联逻辑（软删除子实体），而非依赖 DB 外键（因为软删除本质上是 UPDATE）。
- 或采用"可见性传播"模型：父被删除后，子内容的 `status` 变为 `CommentStatusHidden` / `MessageStatusDeleted` 但保留行。
- 在查询路径中，对 `parent_id` / `file_id` 引用的目标增加 `deleted_at IS NULL` 检查并优雅处理。

---

## 发现四：搜索范围仅限于用户自有数据（Public/Shared 内容不可搜索）

### 问题

全文搜索（FTS）目前完全按**操作者作用域**限制——用户只能搜索自己的消息、自己的评论、自己的文件。系统级公共资源（公开文件）和已授权资源（分享给我的文件）均不可搜索。

### 代码级证据

**证据 1：SearchMessages 查询按 `(from_uid = actor OR to_uid = actor)` 过滤**

```go
// internal/repository/message.go — SearchMessages (通过 internal/service/search.go 调用)
// 在活跃路径 internal/service/search.go 中：
func (s *searchService) searchKind(ctx context.Context, actor, query string, limit int, kind domain.SearchKind, ...) {
    case domain.SearchKindMessages:
        msgs, err := s.messages.SearchMessages(ctx, actor, query, limit)  // actor 作为 uid 过滤参数
    case domain.SearchKindFiles:
        files, err := s.files.SearchFiles(ctx, actor, query, limit)       // actor 作为 owner_uid 过滤
    case domain.SearchKindComments:
        cmts, err := s.comments.SearchComments(ctx, actor, query, limit)  // actor 作为 owner_uid 过滤
}
```

每个搜索方法都接受 `ownerUID` 参数，并用于 `WHERE owner_uid = ?` 过滤。

**证据 2：FTS 索引支持 24/7 的全文搜索但只被 per-actor 查询使用**

```sql
-- migrations/000016_fts_indexes.up.sql
-- 三个表都有 tsvector 列 + GIN 索引
ALTER TABLE messages ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;
CREATE INDEX IF NOT EXISTS idx_messages_fts ON messages USING GIN(search_vector) WHERE deleted_at IS NULL;
```

索引已在，但查询边界是被"owner_uid"限制的——没有全量搜索的 API 入口。

**证据 3：`SearchKindFiles` 只搜索自己名下的文件，不包含共享文件**

```go
// internal/search/search.go — searchKind
case domain.SearchKindFiles:
    files, err := s.files.SearchFiles(ctx, actor, query, limit)
```

没有搜索 `file_shares` 关联表的逻辑，所以 Alice 分享给 Bob 的文件，Bob 在搜索中找不到。

### 产品经理视角

- **搜索是用户发现内容的入口**：在协作场景中，用户期望能搜索"被分享给我的文件"或"公开可见的评论"——这是文件共享功能（已有）和搜索功能（已有）之间的断裂。
- **竞争产品对标**：Google Drive、Dropbox、Notion 的搜索都覆盖"我的 + 分享给我的 + 公开"范围。
- **开发成本低**：FTS 引擎和索引已就绪，只需修改搜索查询的作用域。

### 建议

- 在 `SearchFiles`（及对应的 `SearchMessages / SearchComments`）中增加作用域参数（如 `scope: "own" | "shared" | "public"`）。
- `shared` 作用域 JOIN `file_shares` 表，`public` 作用域用 `visibility = public`。
- 注意搜索结果的权限过滤：搜索结果必须与已有的 `CanReadFile` / `CanReadMessage` 一致。

---

## 发现五：缺少批量操作（Batch Operations）——所有 API 均为单实体粒度

### 问题

当前全部 38 个 gRPC RPC 和对应 HTTP 端点都是**单实体操作**。对于一个管理大量消息、通知、文件的用户，缺少批量操作能力带来显著的 UX 和效率损失。

### 代码级证据

**证据 1：消息已读回执只支持单条**

```go
// internal/handler/http/message.go:24
r.Auth.POST("/messages/:id/read", h.MarkRead)
// 无批量：POST /messages/read?ids=... 不存在
```

`MarkConversationRead` 存在（标记与某人的整个对话已读），但没有**选择性批量**（选定多条消息标记已读）。

**证据 2：通知标记已读有"全部已读"但无"批量标记选中"**

```go
// internal/handler/http/notification.go:22
r.Auth.POST("/notifications/:id/read", h.MarkRead)
r.Auth.POST("/notifications/mark-all-read", h.MarkAllRead)
// 有"全部"和"单条"，无"批量选择"：POST /notifications/read (body: {ids: [...]})
```

同样的模式存在于：通知删除、文件删除、消息删除。

**证据 3：文件共享只支持逐个授权**

```go
// internal/handler/http/file.go:37
r.Auth.POST("/files/:id/share", h.Share)
r.Auth.DELETE("/files/:id/share/:uid", h.Unshare)
// 无批量授权/撤销端点
```

虽然 `Shares.Grant` 的 repository 接口接受 `granteeUIDs []string`（slice），但 HTTP/gRPC 传输层只暴露了单用户版本。

**证据 4：Repository 层已有批量接口但传输层未暴露**

```go
// internal/repository/notification.go
func (r *notifRepo) MarkRead(ctx context.Context, id domain.ID, readAt time.Time) error
// 底层能力：WHERE id = ?
// 理论上可以扩展为 WHERE id IN (?)，但传输层不需要传递 id slice
```

```go
// internal/repository/file.go — HardDelete 接受 slice
func (r *fileRepo) HardDelete(ctx context.Context, ids []domain.ID) error
// 但此接口仅用于存储 GC，不被 HTTP/gRPC 暴露
```

### 产品经理视角

1. **通知管理**：用户收到 50 条未读通知，逐条点击已读是严重的摩擦。"选择全部"+"标记已读"是移动端和 Web 的基本操作。
2. **批量文件操作**：选择 20 个文件→批量删除、批量修改可见性、批量分享给同一个协作者——这些都不可用。
3. **批量消息管理**：选择对话中的多条消息→批量删除（清理对话历史）。
4. **性能影响**：批量操作也可以降低 API 调用次数、减少认证和速率限制的穿透消耗。

### 建议

- 定义批量操作 URI 模式：`POST /batch/{entity}/{action}`，如 `POST /batch/notifications/read`、`POST /batch/files/delete`。
- 利用仓库层已有的 slice 参数能力，不需要底层重构。
- 批量操作的幂等性：利用已有的幂等键机制覆盖批量操作。
- 限制批量大小（如一次最多 100 条），防止单一请求过大。

---

## 总结：优先级与建议

| # | 方向 | 类型 | 优先级 | 估算影响 | 为什么现在做 |
|---|------|------|--------|----------|------------|
| 1 | 双实现清理 | 技术债 | **P0** | 消除 6000 行误导性死代码 | 每延期一天，认知负担 + 未来的迁移成本都在增加 |
| 2 | 跨用户内容去重 | 成本优化 | **P1** | 存储成本降低 30-70%（SaaS 场景） | 基础设施就绪，只需修改查询范围 |
| 3 | 软删除级联语义 | 正确性 | **P1** | 消除悬空引用和用户困惑 | 影响多个实体，越迟修复删除行为的逻辑越难 |
| 4 | 搜索范围扩展 | 特性 | **P2** | 补全 FTS 的覆盖面 | 索引已存在，改动局限于查询逻辑 |
| 5 | 批量操作 | 产品体验 | **P2** | 显著提升大规模内容管理体验 | 仓库层已有能力，传输层补齐即可 |

### 依赖关系

- **#1（清理）** 不依赖其他方向，可以立刻开始。
- **#2（去重）** 依赖 #1 清理确定活跃代码后执行。
- **#3（级联）** 不依赖其他方向，但建议在 #1 之后进行以避免在死代码中重复投入精力。
- **#4（搜索）** 和 **#5（批量）** 独立且互不依赖。
