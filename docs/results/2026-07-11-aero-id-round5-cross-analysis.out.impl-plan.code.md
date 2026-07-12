All tests pass, lint is clean, and all file size constraints are satisfied. Here's the implementation report:

---

# 代码实现报告

## 实现概述

基于 `2026-07-12-aero-id-high-level-extension-directions.out.arch.md` 和 `.impl-plan.md` 中定义的架构规范，实现了以下任务：

- **TASK-014**: Outbox `FOR UPDATE SKIP LOCKED` — 防止多实例部署中的重复事件处理
- **TASK-019**: File 实体添加 `Status` 字段 — 文件后处理管线的基础
- **TASK-026/027**: 对话聚合列表查询 + 索引迁移 — 用户可感知的收件箱聚合
- **TASK-028/029**: 对话聚合 Service + HTTP Handler
- **TASK-031/032**: Webhook 订阅实体 + 迁移 + 仓库

## 文件清单

### 新建文件
| 文件 | 说明 |
|------|------|
| `internal/domain/webhook.go` | `WebhookSubscription` 和 `WebhookDelivery` 领域实体 + `GenerateSigningKey()` |
| `internal/repository/webhook.go` | `WebhookRepository` 接口 + GORM 实现：订阅 CRUD、事件类型过滤、失败计数、投递历史 |
| `migrations/000028_add_file_status.up.sql` | 给 `files` 表添加 `status` 列（smallint），现有行回填为 Ready(3) |
| `migrations/000028_add_file_status.down.sql` | 回滚：删除 `status` 列和索引 |
| `migrations/000029_conversation_indexes.up.sql` | 对话聚合表达式索引 + 未读计数索引 |
| `migrations/000029_conversation_indexes.down.sql` | 回滚 |
| `migrations/000030_create_webhook_subscriptions.up.sql` | `webhook_subscriptions` + `webhook_deliveries` 表 |
| `migrations/000030_create_webhook_subscriptions.down.sql` | 回滚 |

### 修改文件
| 文件 | 变更说明 |
|------|---------|
| `internal/domain/file.go` | 添加 `FileStatus` 枚举（Pending/Processing/Ready/Failed）+ `File.Status` 字段 |
| `internal/repository/db.go` | `fileRow` 添加 `Status int16` 列映射；`fileRowToDomain`/`fileDomainToRow` 转换 `Status` |
| `internal/repository/file.go` | `SearchFiles` 原始 SQL SELECT 列表添加 `status` 列 |
| `internal/outbox/repository.go` | `FetchUnpublished` 添加 `Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})` |
| `internal/message/message.go` | 添加 `ConversationSummary`、`ConversationList` 类型；接口添加 `ListConversations`、`CountUnreadFrom` |
| `internal/message/repository.go` | 实现 `ListConversations`（DISTINCT ON 模式）+ `CountUnreadFrom` |
| `internal/message/service.go` | 实现 `ListConversations` service 方法（调用 repo + 批量解析用户） |
| `internal/message/handler_http.go` | 添加 `GET /v1/conversations` 路由 + `ListConversations` handler + `conversationResponse` DTO |
| `internal/message/mocks_test.go` | mock 添加 `ListConversationsFn`、`CountUnreadFromFn` |
| `internal/search/service_test.go` | mockMsgRepo 添加 `ListConversations`、`CountUnreadFrom` 方法 |
| `go.mod` / `go.sum` | `lib/pq` 添加（`pq.StringArray` 用于 webhook event_types） |

## 核心代码实现

### 1. TASK-014: Outbox FOR UPDATE SKIP LOCKED

```go
// internal/outbox/repository.go
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    var rows []eventRow
    if err := r.db.WithContext(ctx).
        Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
        Where("published_at IS NULL AND NOT dead_letter").
        Order("created_at ASC, id ASC").
        Limit(limit).
        Find(&rows).Error; err != nil {
        return nil, fmt.Errorf("fetch unpublished: %w", err)
    }
    ...
}
```

**关键设计决策**：使用 GORM 内置的 `clause.Locking`，而非原始 SQL。当两个实例同时轮询 outbox 时，每个实例获取不重叠的事件集——实例 A 锁定事件 1-3，实例 B 跳过它们并获取事件 4-6。零新依赖。

### 2. TASK-019: File Status

```go
// internal/domain/file.go
type FileStatus int16

const (
    FileStatusPending   FileStatus = 1 // uploaded, awaiting pipeline processing
    FileStatusProcessing FileStatus = 2 // pipeline worker actively processing
    FileStatusReady     FileStatus = 3 // all pipeline stages completed
    FileStatusFailed    FileStatus = 4 // a pipeline stage failed
)
```

**关键设计决策**：`Status` 默认值为 `Pending(1)`，新上传的文件自动进入管线。现有文件通过迁移回填为 `Ready(3)`。`idx_files_status` 部分索引仅覆盖非终态状态，帮助管线 worker 高效查询待处理文件。

### 3. TASK-026/027: 对话聚合

```go
// internal/message/repository.go
// 使用 DISTINCT ON + LATERAL 模式实现高效对话聚合
func (r *messageRepo) ListConversations(ctx context.Context, uid string, page domain.Page) ([]*ConversationSummary, string, error) {
    // Phase 1: DISTINCT ON (conv_key) — 每个对话取最新一条消息
    // Phase 2: CountUnreadFrom — 每个对话的未读计数
    ...
}
```

**关键设计决策**：采用两阶段查询——先通过 `DISTINCT ON (LEAST/GREATEST)` 获取每个对话的最新消息，再逐对计算未读计数。迁移 000029 的表达式索引保证第一阶段的排序不走全表扫描。游标使用 `conv_key` 字符串偏移（而非 UUID），避免对话间的顺序偏移。

### 4. TASK-031/032: Webhook 订阅

```go
// internal/domain/webhook.go
type WebhookSubscription struct {
    ID                     ID
    OwnerUID               string
    URL                    string
    EventTypes             []string  // empty = all events
    SigningKey             string    // HMAC-SHA256 key
    Secret                 string    // optional custom auth
    Enabled                bool
    MaxConsecutiveFailures int       // auto-disable threshold
    CreatedAt              time.Time
    UpdatedAt              time.Time
}
```

**关键设计决策**：使用 `pq.StringArray` 映射 PostgreSQL `text[]` 类型存储 `event_types`，支持 `ANY()` 操作符的高效过滤查询。`ListEnabledByEventType` 查询使用 `array_length IS NULL OR ? = ANY(event_types)` 模式，一次查询即可返回所有匹配的订阅。投递记录最多截断到 4KB。

## 依赖说明

新增依赖：
- `github.com/lib/pq` — 用于 `pq.StringArray` 处理 PostgreSQL 数组类型（webhook event_types 列）

## 验证步骤

```bash
make build     # 编译通过
make test      # 全部测试通过（18 个包）
make lint      # go vet 通过
make check-file-sizes  # 所有文件 <= 500 行
```

## 已知限制

1. **gRPC 对话聚合端点**：`ListConversations` 的 gRPC handler 需要 proto 文件变更和 `make proto`，当前仅实现了 HTTP 端点。完整的 gRPC 支持需要先修改 `api/proto/aero_id/v1/aero_id.proto` 添加对应的 RPC 和 message 定义。

2. **对话聚合查询覆盖索引**：迁移 000029 创建的表达式索引需要 PostgreSQL 12+ 以确保 `DISTINCT ON` 查询能正确使用覆盖索引。对于早期版本，查询性能可能下降。

3. **Webhook 投递集成**：Webhook 仓库已实现，但投递逻辑（从 outbox relay 调用）和故障管理（`IncrementFailures`/`ResetFailures`）的集成需要在下一轮完成，涉及修改 `internal/outbox/webhook.go` 和 `internal/outbox/chain.go`。

4. **File Status 管线 worker**：`FileStatus` 字段已添加，但实际的管线 worker（缩略图生成、视频转码 stubs）尚未实现——这是一个纯基础层变更，上层管线可以在此基础上构建。
