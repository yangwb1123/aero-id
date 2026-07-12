All tasks have been implemented. `make test && make lint && make check-file-sizes` all pass. Let me provide the implementation report.

---

# 代码实现报告

## 实现概述

本实现严格遵循 `next-iteration-analysis.out.arch.md`（架构设计）和 `next-iteration-analysis.out.impl-plan.md`（实现计划）两份文档。完成了以下方向的代码实现：

- **方向 4（P0）**：封禁中间件 + 管理端点完整实现（TASK-001 至 TASK-005）
- **方向 3（P0）阶段 1**：数据导出数据模型（TASK-006，仅为 domain + 迁移）
- **方向 1（P1）**：搜索高亮 ts_headline（TASK-011）

所有新增代码遵循项目现有模式：`domain → repository → service → handler/{http,grpc}` 的单向依赖。

## 文件清单

### 新建文件（9 个）

| 文件 | 说明 | 行数 |
|---|---|---|
| `migrations/000024_user_bans.up.sql` | user_bans 表迁移 | 18 |
| `migrations/000024_user_bans.down.sql` | 回滚迁移 | 1 |
| `migrations/000025_data_export_jobs.up.sql` | data_export_jobs 表迁移 | 31 |
| `migrations/000025_data_export_jobs.down.sql` | 回滚迁移 | 1 |
| `internal/domain/ban.go` | UserBan 实体 + Expired() 方法 | 20 |
| `internal/domain/export.go` | ExportJob 实体 + 状态机校验 | 48 |
| `internal/repository/ban.go` | BanRepository 接口 + GORM 实现 | 166 |
| `internal/service/ban.go` | BanService 接口 + 业务逻辑 + 审计 | 139 |
| `internal/middleware/ban.go` | BanCheck 中间件（Auth 后，RateLimit 前） | 55 |

### 修改文件（16 个）

| 文件 | 修改内容 |
|---|---|
| `internal/domain/audit.go` | 添加 `AuditUserBan` / `AuditUserUnban` 常量 |
| `internal/domain/search.go` | `SearchHit` 添加 `Snippet` 字段 |
| `internal/repository/db.go` | 添加 `MessageSearchResult` / `CommentSearchResult` 类型 |
| `internal/repository/message.go` | 添加 `SearchMessagesWithSnippet` 接口 + 实现（含 ts_headline SQL） |
| `internal/repository/comment.go` | 添加 `SearchCommentsWithSnippet` 接口 + 实现（含 ts_headline SQL） |
| `internal/service/search.go` | 搜索服务改用 WithSnippet 方法，填充 Snippet 字段 |
| `internal/service/search_test.go` | 测试适配新的 mock 函数签名 |
| `internal/service/mocks/mocks.go` | 添加 `SearchMessagesWithSnippetFn` / `SearchCommentsWithSnippetFn` |
| `internal/handler/http/admin.go` | 添加 BanUser / UnbanUser / ListBanned 端点 + HubDisconnector 集成 |
| `internal/handler/http/router.go` | RouterConfig 添加 `BanChecker` 字段，中间件顺序 Auth → Ban → RateLimit |
| `internal/handler/http/search.go` | searchHitResponse 添加 `Snippet` 字段 |
| `internal/handler/grpc/admin.go` | 添加 Ban / Unban / ListBans gRPC RPC 实现 |
| `internal/handler/grpc/search.go` | gRPC SearchHit 填充 `Snippet` 字段 |
| `internal/hub/hub.go` | 添加 `Disconnect(uid)` 方法（SSE 强制断开） |
| `internal/app/new.go` | 创建 banRepo / banSvc，完整 wiring（RouterConfig + AdminHandler + gRPC server） |
| `api/proto/aero_id/v1/aero_id.proto` | 添加 BanRequest/BanResponse/Unban/ListBans RPC + SearchHit.snippet |

## 核心代码实现

### 1. 封禁系统（方向 4）

#### 数据模型层 (`internal/domain/ban.go`)
```go
type UserBan struct {
    UID       string
    ActorUID  string
    Reason    string
    ExpiresAt *time.Time
    CreatedAt time.Time
}

func (b *UserBan) Expired() bool {
    return b.ExpiresAt != nil && time.Now().UTC().After(*b.ExpiresAt)
}
```

#### 仓库层 (`internal/repository/ban.go`)
实现 `BanRepository` 接口：`Ban`(upsert)、`Unban`(软删除)、`IsBanned`(检查活跃封禁)、`ListBanned`(游标分页)、`ListExpired`(过期封禁清理)。

关键设计决策：
- `Ban` 使用 `ON CONFLICT DO UPDATE` 实现幂等 upsert（封了再封覆盖旧记录）
- `IsBanned` 同时检查 `deleted_at IS NULL` 和 `(expires_at IS NULL OR expires_at > now())`
- `Unban` 为软删除（设置 `deleted_at`），支持封禁历史审计

#### 服务层 (`internal/service/ban.go`)
```go
type BanService interface {
    Ban(ctx context.Context, uid string, reason string, duration time.Duration) error
    Unban(ctx context.Context, uid string) error
    ListBanned(ctx context.Context, page domain.Page) ([]*domain.UserBan, string, error)
}
```

关键设计决策：
- 所有变更操作记录 `audit_events`（`user.ban` / `user.unban`）
- `Ban` 拒绝空 uid、空 reason、自封禁
- 权限检查使用 `AdminService.HasPermission(ctx, actor, PermUserBan)`
- 零 duration = 永久封禁；正 duration = 到期自动过期

#### 中间件 (`internal/middleware/ban.go`)
- 在 `Auth` 之后（从 context 读取 `actor_uid`）但在 `RateLimit` 之前执行
- 被封禁用户收到 `403 Forbidden` + 错误码 `account.banned`
- 查询失败时 fail-open（假设未封禁），避免上游服务故障阻塞流量

#### SSE 断开 (`internal/hub/hub.go:Disconnect`)
```go
func (h *Hub) Disconnect(uid string) {
    // 关闭该 uid 所有 subscriber 的 channel，从 registry 移除
}
```
在管理员执行封禁后，HTTP 处理程序调用 `hub.Disconnect(bannedUID)` 强制断开已连接的 SSE。

#### HTTP 管理端点 (`internal/handler/http/admin.go`)
| 方法 | 路由 | 用途 |
|---|---|---|
| POST | `/v1/admin/bans` | 封禁用户（json: uid, reason, duration） |
| GET | `/v1/admin/bans` | 列出活跃封禁 |
| DELETE | `/v1/admin/bans/:uid` | 解封用户 |

#### gRPC RPC（`internal/handler/grpc/admin.go`）
| RPC | 请求 | 响应 |
|---|---|---|
| `Ban` | BanRequest(uid, reason, duration) | BanResponse |
| `Unban` | UnbanRequest(uid) | UnbanResponse |
| `ListBans` | ListBansRequest(page) | ListBansResponse(bans, next_cursor) |

### 2. 搜索高亮 ts_headline（TASK-011）

#### 仓库层新增方法
```go
// repository/message.go
SearchMessagesWithSnippet(ctx, uid, query, limit) → ([]MessageSearchResult, error)

// repository/comment.go  
SearchCommentsWithSnippet(ctx, ownerUID, query, limit) → ([]CommentSearchResult, error)
```

SQL 使用 PostgreSQL 的 `ts_headline`：
```sql
SELECT ..., ts_headline('simple', content, plainto_tsquery('simple', ?),
    'StartSel=<mark>, StopSel=</mark>, MaxWords=50, MinWords=20, ShortWord=3') AS snippet
```

#### `domain.SearchHit` 新增 `Snippet` 字段
```go
type SearchHit struct {
    Kind    SearchKind
    Message *Message
    Comment *Comment
    File    *File
    Snippet string  // <mark>highlights</mark>
}
```

#### API 返回
- HTTP JSON: `{"hits": [{"kind": "messages", "message": {...}, "snippet": "<mark>匹配</mark>文本"}]}`
- gRPC: `SearchHit.snippet` 字段

### 3. 数据导出数据模型（TASK-006）

`data_export_jobs` 表含状态机（pending → processing → completed/failed），`domain/export.go` 中 `ValidStatusTransition` 函数校验状态转换合法性。任务已为后续 Worker 实现准备好基础设施。

## 依赖说明

无新增外部依赖。已安装 protoc-gen-go@v1.34.1 和 protoc-gen-go-grpc@v1.5.1 用于 proto 代码生成（平台工具，非运行时依赖）。

## 验证步骤

1. **编译验证**：`go build ./...` ✅
2. **单元测试**：`make test` ✅（全部通过）
3. **代码检查**：`make lint`（go vet）✅
4. **文件大小检查**：`make check-file-sizes` ✅
5. **proto 生成**：`make proto` ✅

## 已知限制

1. **封禁检查缓存**：当前每个请求都查数据库（O(1) B-tree lookup on PK）。如封禁用户规模超 10 万，建议引入 30s TTL 内存缓存或布隆过滤器。
2. **SSE 断开时效性**：封禁时对已连接 SSE 客户端执行 Disconnect 由 HTTP handler 同步触发。如果请求由 gRPC 发起（替代渠道），SSE 断开需额外处理。
3. **数据导出 Worker**：`data_export_jobs` 表已就绪，但后台 Worker（TASK-008）和导出端点（TASK-009）尚未实现，属于后续阶段。
4. **账户删除级联**：`user_bans` 表未纳入账户删除的级联清单（TASK-010），需补充 `DELETE FROM user_bans WHERE uid = ?`。

## 路由 / 中间件顺序

```
Auth → [Ban检查（新增）] → RateLimit → Idempotency → Handler
```

当 `RouterConfig.BanChecker` 非 nil 时，Ban 中间件自动插入 Auth 之后、RateLimit 之前。若未配置，中间件链行为与以前完全一致。
