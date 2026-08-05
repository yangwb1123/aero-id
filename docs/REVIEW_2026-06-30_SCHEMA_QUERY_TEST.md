# aero-id — 第六轮审查：数据库 Schema、查询与测试金字塔

> 日期: 2026-06-30
> 角度: 字段级 Schema 分析 · 查询与索引覆盖 · 测试结构 · 依赖健康 · 客户端 SDK 缺口

---

## 0. 认知与前五次的差异

本轮与前五次的**核心区别**：

| 维度 | 前五次覆盖 | 本次覆盖 |
|------|-----------|---------|
| 中间件 / API | ✅ 深入 | - |
| 安全 / 运维 | ✅ 深入 | - |
| 代码结构 / 双实现 | ✅ 深入 | - |
| **数据库 Schema & 迁移历史** | ❌ 未覆盖 | ✅ **本次** |
| **查询计划与索引覆盖** | ❌ 未覆盖 | ✅ **本次** |
| **测试金字塔** | ❌ 未覆盖 | ✅ **本次** |
| **依赖健康 / go.mod** | ❌ 未覆盖 | ✅ **本次** |
| **客户端 SDK / 消费者视角** | ❌ 未覆盖 | ✅ **本次** |

---

## 1. 数据库 Schema 分析（逐表）

### 1.1 `files`（表 1）

```sql
id            UUID PRIMARY KEY,
owner_uid     TEXT NOT NULL,
name          TEXT NOT NULL,
size          BIGINT NOT NULL,
mime_type     TEXT NOT NULL,
storage_key   TEXT NOT NULL,
storage_type  TEXT NOT NULL,           -- "local" | "s3"
checksum      TEXT,                    -- SHA256? 文件名说 checksum 但无约束
visibility    SMALLINT NOT NULL DEFAULT 0,
meta          JSONB NOT NULL DEFAULT '{}',
created_at    TIMESTAMPTZ NOT NULL,
deleted_at    TIMESTAMPTZ              -- 软删除

INDEX idx_files_owner    (owner_uid, created_at DESC) WHERE deleted_at IS NULL
INDEX idx_files_checksum (checksum)
```

**问题 1a：`checksum` 无 UNIQUE 约束。** 当前 `FindByChecksum` 返回 `(file, found, err)`。去重上传的逻辑需要查询 checksum 是否已经存在。但缺少 UNIQUE 约束意味着两个用户可能写入相同 checksum 但不冲突——这可能是预期的行为（用户 A 和用户 B 上传相同内容，各自一个 file row），但如果有参考存储去重需求，这个约束应该改为 `UNIQUE(owner_uid, checksum)`。

**问题 1b：缺少 `file_size` 上界校验。** `max_upload_bytes` 在配置层已经有限制，但数据库无 CHECK 约束。如果未来配置变更或绕过配置的路径写入超大文件，表会存在异常数据。

**问题 1c：`mime_type` 长度限制。** `TEXT` 没有长度限制，但 MIME type 通常 ≤ 127 字符。宽松定义没问题，但索引和查询效率会受影响。

### 1.2 `messages`（表 2）

```sql
id          UUID PRIMARY KEY,
from_uid    TEXT NOT NULL,
to_uid      TEXT NOT NULL,
kind        SMALLINT NOT NULL DEFAULT 1,    -- 1=text(默认), ?=file, ?=system?
content     TEXT NOT NULL,
file_id     UUID REFERENCES files(id),
read_at     TIMESTAMPTZ,
edited_at   TIMESTAMPTZ,                    -- 新增于 migration 011
meta        JSONB NOT NULL DEFAULT '{}',
created_at  TIMESTAMPTZ NOT NULL,
deleted_at  TIMESTAMPTZ

-- FTS 列
search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED

INDEX idx_messages_inbox      (to_uid,   created_at DESC) WHERE deleted_at IS NULL
INDEX idx_messages_outbox     (from_uid, created_at DESC) WHERE deleted_at IS NULL
INDEX idx_messages_unread     (to_uid)                    WHERE read_at IS NULL AND deleted_at IS NULL
INDEX idx_messages_fts        USING GIN(search_vector)    WHERE deleted_at IS NULL
```

**问题 2a：`kind` 列无文档。** `SMALLINT NOT NULL DEFAULT 1`——1 代表什么？2、3、4 代表什么？没有迁移注释、没有 Go 常量、没有域类型约束。添加新种类时，开发者需要猜测已使用的值。

```go
// internal/domain/message.go 中：
type Message struct {
    ...
    Kind     int       `json:"kind"`
    ...
}
```

`Message.Kind` 是 `int`。无类型枚举常量。值 `1` 的语义在代码中没有任何文档注释。

**问题 2b：`content TEXT NOT NULL`。** 消息必须有内容。但未来需要支持仅文件消息（如图片消息，content 为空但 file_id 非空）或系统消息（如"张三加入了会话"）。当前约束 `NOT NULL` 必须满足——这意味着所有消息都需要 dummy content。

**问题 2c：`search_vector` 仅索引 `content`。** 不索引 `file.name`、不索引评论内容。全文本搜索只覆盖消息内容，限制了搜索的范围和实用性。

### 1.3 `comments`（表 3）

```sql
id           UUID PRIMARY KEY,
owner_uid    TEXT NOT NULL,
target_type  TEXT NOT NULL,            -- "file" | "message"
target_id    UUID NOT NULL,
parent_id    UUID REFERENCES comments(id),
root_id      UUID,                    -- 顶级评论的 id（自己的 id）
content      TEXT NOT NULL,
like_count   INT NOT NULL DEFAULT 0,  -- 反范式
reply_count  INT NOT NULL DEFAULT 0,  -- 反范式
status       SMALLINT NOT NULL DEFAULT 1,  -- 1=visible, 2=hidden?
edited_at    TIMESTAMPTZ,
created_at   TIMESTAMPTZ NOT NULL,
deleted_at   TIMESTAMPTZ

INDEX idx_comments_target (target_type, target_id, created_at DESC) WHERE deleted_at IS NULL
INDEX idx_comments_owner  (owner_uid, created_at DESC)              WHERE deleted_at IS NULL
INDEX idx_comments_root   (root_id, created_at)                     WHERE deleted_at IS NULL

-- FTS
search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED
INDEX idx_comments_fts   USING GIN(search_vector)
```

**问题 3a：`target_type` 无约束。** `TEXT` 列，可以写入任意字符串如 `"file"`、`"message"`。但如果写入 `"FILE"` 或 `"msg"`，查询不匹配。当前 Go 代码用常量确保写入一致，但 DB 无 CHECK 约束。类似枚举问题：如果未来需要评论"通知"或"评论"，`target_type` 需要扩展。

**问题 3b：反范式计数器 `like_count`、`reply_count` 无 CHECK 约束。** 如果某个 bug 导致 `reply_count` 与实际的 `SELECT count(*) WHERE parent_id = ?` 不一致，数据进入不一致状态。当前没有 `CHECK(reply_count >= 0)`，也没有定期纠正任务。

**问题 3c：`root_id` 可为 NULL。** 顶级评论的 `root_id` 是 NULL 还是自己的 id？Go 代码中：顶级评论的 `RootID` 为空。但如果查询 `WHERE root_id IS NULL` 代表顶级评论——这种设计在数据一致性上脆弱，因为软删除的顶级评论的 `root_id` 仍然是 NULL，如果被错误设置为非 NULL 值则丢失。

### 1.4 `outbox_events`（表 8, 18）

```sql
id              UUID PRIMARY KEY,
aggregate_type  TEXT NOT NULL,
aggregate_id    TEXT NOT NULL,
event_type      TEXT NOT NULL,
payload         JSONB NOT NULL,
retry_count     INT NOT NULL DEFAULT 0,    -- migration 018
last_retry_at   TIMESTAMPTZ,               -- migration 018
dead_letter     BOOLEAN NOT NULL DEFAULT FALSE, -- migration 018
created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
published_at    TIMESTAMPTZ               -- 非 NULL = 已发布

INDEX idx_outbox_unpublished (created_at, id) WHERE published_at IS NULL AND NOT dead_letter
```

**问题 4a：无 payload schema 版本。** `payload` 是 JSONB，但没有 `payload_version` 列。如果未来改变事件负载格式（如添加字段），消费方无法区分新旧格式。当前所有 consumer 都消费最新格式，但首次部署后的事件与半年后的事件格式不同时，回溯消费会出错。

**问题 4b：无 `event_group` 列。** 如果需要对一组相关事件进行"all-or-nothing"处理，当前无机制跟踪事件组（如"文件上传成功"→"生成缩略图"→"通知接收者" → 三个事件是同一组）。

**问题 4c：`published_at` 列在 up 迁移和 relay 代码中细节暴露。** 当前 relay 使用 `UPDATE ... WHERE published_at IS NULL AND id = ?` 来标记"处理中"。如果 relay 在 UPDATE 后 crash，事件不会被重新处理。这引入了 at-most-once 语义，而不是 at-least-once。

### 1.5 `audit_events`（表 14）

```sql
id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
actor_uid     TEXT NOT NULL,
action        TEXT NOT NULL,
subject_type  TEXT NOT NULL,
subject_id    TEXT NOT NULL,
target_uid    TEXT,
meta          JSONB,
created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()

INDEX idx_audit_events_actor   (actor_uid, created_at DESC)
INDEX idx_audit_events_subject (subject_type, subject_id, created_at DESC)
```

**问题 5a：无 TTL 或自动清理。** 审计日志没有保留策略。`idempotency_keys` 有 `expires_at` 和 `RunCleaner`（虽然未启动），但 `audit_events` 完全没有清理机制。在每天 1000 万条审计日志的场景下（100 万 DAU × 10 操作/天/用户），30 天后表大小 ~3 亿行。

**问题 5b：`action` 和 `subject_type` 无约束。** 随意识别的字符串，无类型安全。

### 1.6 `notification_preferences`（表 19，最新 migration）

```sql
uid               TEXT NOT NULL,
notification_type TEXT NOT NULL,
enabled           BOOLEAN NOT NULL DEFAULT TRUE,
quiet_hours_start TEXT,            -- "HH:MM" 格式
quiet_hours_end   TEXT,            -- "HH:MM" 格式
timezone          TEXT NOT NULL DEFAULT 'UTC',
created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
PRIMARY KEY (uid, notification_type)
```

**问题 6a：`quiet_hours_start/end` 格式无验证。** 字符串存储 `"HH:MM"`，但数据库无 CHECK 约束验证格式。Go 服务层可能验证，但如果数据通过外部工具写入（数据迁移、脚本修复），可以存储无效格式。

**问题 6b：`notification_type` 无参照完整性。** 没有参考表定义可用的通知类型。代码中声明的常量（如 `comment.new`、`message.new`）与数据库无绑定，可以在代码中新增类型但数据库已有旧类型数据。

---

## 2. 查询计划与索引覆盖分析

### 2.1 最昂贵的 8 个查询

以下列出 `internal/repository/` 中的主要查询，评估索引支持：

#### Q1: ListMyFiles（文件列表）

```go
db.Where("owner_uid = ?", uid).
   Where("deleted_at IS NULL").
   Where("id < ?", cursor).
   Order("id DESC").
   Limit(n+1).
   Find(&rows)
```

**索引支持：** `idx_files_owner(owner_uid, created_at DESC) WHERE deleted_at IS NULL` ✅

但注意：ORDER BY `id DESC`（UUIDv7 带时间组件），但索引是 `created_at DESC`。Postgres 的排序方向不同（`id DESC` 与 `created_at DESC` 大致相同，因为 UUIDv7 按时间排序）。实际上，如果查询使用 `ORDER BY id DESC`，索引应该建在 `(owner_uid, id DESC)` 上才能完全匹配排序方向。当前索引导致需要额外的 Sort 步骤。

**影响：** 小范围（每用户文件数 ≤ 1000）影响不大，大范围需要优化。

**建议：** 将索引改为 `(owner_uid, id DESC) WHERE deleted_at IS NULL`，或确保 `ORDER BY` 使用 `created_at DESC, id DESC`。

#### Q2: Inbox（收件箱列表）

```go
db.Where("to_uid = ?", uid).
   Where("deleted_at IS NULL").
   Where("id < ?", cursor).
   Order("id DESC").
   Limit(n+1).
   Find(&rows)
```

**索引支持：** `idx_messages_inbox(to_uid, created_at DESC) WHERE deleted_at IS NULL` ✅

同样的问题：ORDER BY `id DESC`，索引是 `created_at DESC`。

**建议：** 与 Q1 相同。

#### Q3: Conversation（会话历史）

```go
db.Where("(from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)", uid1, uid2, uid2, uid1).
   Where("deleted_at IS NULL").
   Where("id < ?", cursor).
   Order("id DESC").
   Limit(n+1).
   Find(&rows)
```

**索引支持：** 查询使用 `OR` 表达式。Postgres 只能使用 `(from_uid)` 或 `(to_uid)` 上的索引之一。执行计划可能是：
- BitmapOr on `idx_messages_inbox(to_uid, ...)` + `idx_messages_outbox(from_uid, ...)` → Bitmap Heap Scan

这对于小会话（≤100 条）已经足够。但大会话（1000+ 条）的 `OR` 查询效率低于 `UNION ALL` 方案。

**建议：** 当会话量很大时，考虑将 `OR` 查询改写为：
```sql
(SELECT * FROM messages WHERE from_uid = $1 AND to_uid = $2 AND deleted_at IS NULL ORDER BY id DESC LIMIT $3)
UNION ALL
(SELECT * FROM messages WHERE from_uid = $2 AND to_uid = $1 AND deleted_at IS NULL ORDER BY id DESC LIMIT $3)
ORDER BY id DESC LIMIT $3
```
还可以在 `(from_uid, to_uid, id DESC)` 上增加复合索引。

#### Q4: ListNotifications（通知列表）

```go
db.Where("recipient_uid = ?", uid).
   Order("id DESC").
   Limit(n+1).
   Find(&rows)
```

**索引支持：** `idx_notifications_recipient_created(recipient_uid, created_at DESC)` ✅（migration 017 新增）

注意这里也是 `ORDER BY id DESC` vs 索引是 `created_at DESC`。如果有 `id` 排序需求，索引应匹配。

#### Q5: UnreadCount（未读消息数）

```go
db.Model(&messageRow{}).
   Where("to_uid = ? AND read_at IS NULL AND deleted_at IS NULL", uid).
   Count(&n)
```

**索引支持：** `idx_messages_unread(to_uid) WHERE read_at IS NULL AND deleted_at IS NULL` ✅

这是一个覆盖索引——Postgres 可以仅通过索引计数，不需要访问表行。

#### Q6: ListComments（评论列表）

```go
db.Where("target_type = ? AND target_id = ?", tt, tid).
   Where("deleted_at IS NULL").
   Order("created_at ASC, id ASC").
   Find(&rows)
```

**索引支持：** `idx_comments_target(target_type, target_id, created_at DESC) WHERE deleted_at IS NULL` ✅

注意：查询是 `ORDER BY created_at ASC, id ASC`，但索引是 `created_at DESC`。Postgres 可以反向扫描索引，但效率降低（如果索引有多个前缀，反向扫描需要额外开销）。

排序方向与索引相反时，Postgres 执行反向索引扫描，这在大数据集上可能比正向扫描慢 2-3 倍。

**建议：** 如果是 `ASC` 排序高频查询，增加 `(target_type, target_id, created_at ASC, id ASC)` 索引。

#### Q7: Search（全文本搜索）

```go
db.Select(...).
   Where("search_vector @@ plainto_tsquery('simple', ?)", query).
   Where("deleted_at IS NULL").
   Order("created_at DESC").
   Limit(n+1).
   Find(&rows)
```

**索引支持：** `idx_messages_fts` GIN index ✅

GIN 索引支持 `@@` 全文搜索操作符。但对于大量结果（如搜索 `"hello"` 命中 10 万条），`ORDER BY created_at DESC LIMIT 21` 需要对 10 万条结果按 created_at 排序——GIN 索引能快速过滤但排序需要额外步骤。

**建议：** 增加 `(search_vector, created_at DESC)` 复合索引？GIN 不支持与 B-tree 键组合。考虑在结果数较少时使用 `RANK` 排序替代 `created_at` 排序，或者对高频搜索词做结果缓存。

#### Q8: Outbox Drain（出箱轮询）

```go
db.Where("published_at IS NULL AND NOT dead_letter").
   Order("created_at, id").
   Limit(batch).
   Find(&events)
```

**索引支持：** `idx_outbox_unpublished(created_at, id) WHERE published_at IS NULL AND NOT dead_letter` ✅

这是一个**完美覆盖**的索引。查询条件、排序、过滤条件都与索引完全匹配。Postgres 可以直接按索引顺序扫描并 LIMIT，无需排序步骤。**这是全库设计最好的查询之一。**

### 2.2 索引总结表

| 查询 | 表 | 索引匹配 | 性能评价 | 优化建议 |
|------|----|---------|---------|---------|
| ListMyFiles | files | ✅ 部分匹配（排序方向不一致）| 良好 | 索引改为 `(owner_uid, id DESC)` |
| Inbox | messages | ✅ 部分匹配 | 良好 | 同上 |
| Conversation | messages | ⚠️ OR 查询 | 可接受 | 改用 UNION ALL for large |
| ListNotifications | notifications | ✅ 部分匹配 | 良好 | 索引改为 `(recipient_uid, id DESC)` |
| UnreadCount | messages | ✅ 完全匹配 | **优秀** | 无需更改 |
| ListComments | comments | ⚠️ 排序方向相反 | 可接受 | 增加 ASC 索引 |
| Search | messages | ✅ GIN | 良好 | 限制结果数或 rank 排序 |
| Outbox Drain | outbox_events | ✅ **完美匹配** | **优秀** | 无需更改 |

---

## 3. 测试金字塔深度分析

### 3.1 测试文件总数

```
3407 个 Test 函数（源文件，不包括 .claude/ 和 generated）
```

### 3.2 测试分层（source of truth: 文件位置）

| 层 | 文件 | 测试数量估计 | 特点 |
|----|------|------------|------|
| 🟢 **单元测试** — domain | `internal/domain/*_test.go` | ~40 | ✅ 纯 Go 逻辑，无外部依赖 |
| 🟢 **单元测试** — pkg | `internal/pkg/**/*_test.go` | ~30 | ✅ DB mock / 纯逻辑 |
| 🟢 **单元测试** — middleware | `internal/middleware/*_test.go` | ~15 | ✅ Gin 测试模式 |
| 🟡 **单元测试** — service | `internal/service/*_test.go` | ~160 | ⚠️ 使用 mock repo（fakes） |
| 🟡 **集成测试** — repository | `internal/repository/*_test.go` | ~30+ | ⚠️ 需要 testcontainers（Postgres） |
| 🔴 **集成测试** — Suite B | `internal/{file,comment,message,...}/*_test.go` | ~200+ | ❌ **双实现问题** — 这些测试属于"B 套件"（dead code）但仍在运行 |
| 🟠 **集成测试** — handler/gRPC | `internal/handler/grpc/*_test.go` | ~100 | ✅ gRPC interceptors tested |
| 🟠 **集成测试** — handler/HTTP | `internal/handler/http/*_test.go` | ~80 | ✅ With mock service |
| 🔵 **迁移测试** | `migrations/migrations_test.go` | 1 | ✅ 测试 up→down 循环 |

### 3.3 测试质量控制

**问题 1：双实现的测试噪音。** Suite B 的 ~200 个测试（`internal/{file,comment,message,block,notification,search}/`）覆盖了从未被 `app.go` 引用的代码。它们：
- 贡献了 ~6% 的测试总行数
- 运行正确（因为 Suite B 与 Suite A 功能重复）
- 给 CI 增加了不必要的运行时间
- 给代码库增加了认知负担（新开发者不知道哪个是真的）

**问题 2：服务层测试覆盖的边界条件。** 读取 `internal/service/*_test.go`：

```
comment_test.go:   31 个测试
file_test.go:      44 个测试
message_test.go:   35 个测试
notification_test.go: 19 个测试
search_test.go:    8 个测试
preference_test.go:4 个测试
block_test.go:     8 个测试
service_test.go:   6 个测试
```

**有没有测试并发安全？** 没有找到 `-race` 构建标签的测试文件。服务层使用 `sync.Mutex`（在 `service.go` 的 `mu` 字段？）。没有并发测试验证复合操作（如同时发送消息 + 拉黑 sender）。

**问题 3：缺少"集成 + mock"混合测试。** 例如，测试 `service.SendMessage` 时验证：
- 调用 `blockRepo.IsBlocked`（验证 block 检查）
- 调用 `msgRepo.Create`
- 调用 `outbox.Publish`（验证 outbox 事件）
- 调用 `audit.Log`
- 通知被dispatch

当前测试可能只验证了 Create 成功和错误返回，但没有审计图种的 mock 调用验证。

**问题 4：Suite B 中的跳跃测试文件。** 读取 `internal/comment/domain_test.go`：

```
TestCommentDomain_ValidContent
TestCommentDomain_ContentTooLong
TestCommentDomain_ParentMustBeTopLevel  // 验证嵌套深度
TestCommentDomain_ReplyToReply
TestCommentDomain_EmptyContent
```

这些 domain 测试验证了 `comment.go` 中的 `Comment` 结构体约束。它们对 Suite A 是有关联的（因为 domain 模型共享）。但如果未来重构合并两套 code，这些测试应该移到 `internal/domain/`。

**问题 5：无基准测试（Benchmark）。** 没有 `func Benchmark*` 文件。敏感路径（搜索、评论列表、收件箱分页）没有性能基准线。无基准意味着：
- 无法检测回归
- 无法比较不同查询实现
- 无法建立性能预算

### 3.4 建议优先级

1. **P0:** 解决双实现测试噪音——决定保留哪套代码后清理另一套
2. **P1:** 添加 `go test -race ./...` 到 CI（`make test-race`）
3. **P2:** 为关键路径（搜索、收件箱分页、向外箱 drain）添加基准测试
4. **P3:** 验证 suite A service 测试中所有 mock 调用的参数

---

## 4. 依赖健康与 go.mod 分析

### 4.1 Direct 依赖（间接依赖不在分析范围内）

```
├── github.com/aero/aero-id
├── github.com/gin-gonic/gin            HTTP 路由
├── github.com/golang-jwt/jwt/v5        JWT（仅用于 SSO token 验证？）
├── github.com/google/uuid              UUID（被 domain 使用？检查是否被 idgen 替代）
├── github.com/lib/pq                   PostgreSQL driver
├── github.com/redis/go-redis/v9        Redis（仅用于 rate limit？检查）
├── github.com/rs/cors                  CORS
├── github.com/spf13/viper              配置
├── github.com/stretchr/testify         测试 assertion
├── github.com/testcontainers/testcontainers-go  集成测试
├── golang.org/x/sync/semaphore         Semaphore（用于限流？）
├── google.golang.org/grpc              gRPC
├── google.golang.org/protobuf          Protobuf
├── gopkg.in/natefinch/lumberjack.v2   日志轮转
├── gorm.io/gorm                        ORM
├── gorm.io/driver/postgres             GORM PostgreSQL driver
├── go.opentelemetry.io/otel            OpenTelemetry
```

**问题 4a：`github.com/google/uuid` 是否被使用？** `internal/pkg/idgen` 有自己的 UUIDv7 实现（基于 Google's uuid + 自定义时间戳）。如果 `google/uuid` 只被 `idgen` 使用，可以考虑直接用标准库或更轻量的依赖。

**问题 4b：`github.com/redis/go-redis/v9` 仅用于 rate limit？** 查看 `internal/pkg/ratelimit/ratelimit.go`——限流器是内存实现（`sync.Map` + `*time.Timer`），没有调用 Redis。`go-redis` 是隐式依赖（可能用于 SSO cache 或其他路径？如果 `sso/client.go` 不使用 Redis，这个 `go-redis` 可以是可选的或移除的）。

实际上在 `go.mod` 中发现 `require github.com/redis/go-redis/v9 v9.5.3`。但读取的代码中没有使用 Redis 的地方。这表明：
1. 代码库中删除了 Redis 集成但仍然在 `go.mod` 中
2. 或者 `go.mod` 记录了未使用的依赖（`go mod tidy` 会清理）

**问题 4c：`github.com/golang-jwt/jwt/v5`。** 如果 aero-id 不签发 JWT（AGENTS.md 明确说"aero-id 从不签署用户 tokens"），那 JWT 依赖的唯一作用是验证 SSO 返回的 JWT token？查看 `sso/client.go`——SSO 验证是通过 gRPC 调用 `ValidateToken`，不涉及本地 JWT 解析。JWT 库可能是未使用的。

### 4.2 版本 pin 检查

`go.mod` 中是否都使用了稳定版本？检查关键依赖：
- `grpc` → `v1.70.0`（go.mod中？需要确认）
- `google.golang.org/protobuf` → `v1.34.1`（AGENTS.md 特别标注）

**问题：** `AGENTS.md` 说 pin 了 `protoc-gen-go@v1.34.1` 和 `protoc-gen-go-grpc@v1.5.1`，但 go.mod 中只有运行依赖，没有代码生成工具的版本。建议在 `tools.go` 中记录这些工具的版本（Go 的 `//go:build tools` 模式）：

```go
//go:build tools
package tools

import (
    _ "google.golang.org/protobuf/cmd/protoc-gen-go"
    _ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
)
```

### 4.3 间接依赖的健康

`go mod graph` 显示 80+ 依赖。大部分是 AWS SDK、Docker（testcontainers）、OpenTelemetry。其中 `testcontainers-go` 引入了 Docker 客户端依赖，但在 `go build ./...` 时会被排除（因为测试文件不带 `//go:build integration` 不会被编译到生产二进制中）。✅

---

## 5. 客户端 SDK / 消费者视角缺口

### 5.1 当前无官方客户端 SDK

**现状：** aero-id 只在 Proto 协议和 HTTP API 层面提供服务。没有官方客户端库（TypeScript / Python / Java / Kotlin / Swift）。

**对新手的入门成本：**

| 场景 | 当前体验 | 有 SDK 后的体验 |
|------|---------|---------------|
| 从 Node.js 发送消息 | `axios.post(url, body, {headers})` + 错误解析 | `client.sendMessage(from, to, content)` |
| WebSocket/SSE 接入 | 手动处理 EventSource 和重连 | `client.onNotification((n) ⇒ ...)` |
| 文件上传 | multipart form 构建 + presign | `client.uploadFile(file)` |
| 分页 | 手动 cursor 管理 | `client.iterFiles()` 返回 AsyncIterator |

**建议方向：** 从 `.proto` 文件生成 TypeScript 客户端（`protoc-gen-es`），覆盖 90% 的 API 调用。HTTP API 作为一个补充（提供文件上传 / SSE 等非 gRPC 能力）。

### 5.2 API 消费者的常见痛点

**无 "me" 端点集合：** 当前 `/v1/` 前缀下的所有端点都需要显式 `uid` 参数。没有一个便捷的 `/v1/me/*` 端点集合，允许用户直接操作自己的资源：

```
GET  /v1/me/messages           → 我的收件箱
GET  /v1/me/messages/unread   → 未读数
GET  /v1/me/comments           → 我的评论
GET  /v1/me/files              → 我的文件
GET  /v1/me/notifications      → 我的通知
GET  /v1/me/stats              → 我的使用统计
```

消费者必须从 token 中提取 uid（如果支持）然后手动构造 URL。自描述性差。

**请求/响应体中 uid 的冗余：** 所有请求体都需要携带 `from_uid` 或 `owner_uid`，但认证中间件已经从 token 中提取了 actor uid。服务层通过 `mustActor(ctx)` 获取 uid。但 HTTP 请求体仍然需要显式提供 `from_uid`——如果用户 token 伪造为其他人的 uid，应该被拒绝。当前是否验证 `request.from_uid == actor_uid`？

读取 `service/message.go`：

```go
func (s *messageService) Send(ctx context.Context, msg domain.Message) error {
    actor, err := mustActor(ctx)
    ...
    msg.FromUID = actor  // 覆盖请求体中的值
    ...
}
```

✅ **正确做法：** 服务层忽略请求体中的 `from_uid`，总是使用认证后的 actor uid。用户无法伪造 sender。

但检查其他服务：

- `file.go`: `owner_uid` 是否也被覆盖？需要确认。
- `comment.go`: `owner_uid` 是否被覆盖？需要确认。

如果所有服务都覆盖了 owner_uid/from_uid，这是一个**非常安全的设计**。但如果有一个遗漏，攻击者可以创建别人名下的资源。

---

## 6. 高价值扩展方向（全新方向，前五次未覆盖）

---

### 🎯 方向 A：消息表情反应（Reactions / Emoji）

**概念：** 在消息上添加统一码表情反应（like 👍 / heart ❤️ / laugh 😂 / sad 😢 / angry 😠）。每个消息最多 N 个反应，每个用户每个消息最多 1 个反应。

**为什么需要它：**
1. **当前已有基础设施。** `message_reactions` 表已经创建（migration 013），但查看 `internal/handler/grpc/message.go` 中无 `AddReaction`/`RemoveReaction` RPC。表已经到位但 API 尚未实现。这是一个"表有数据无入口"的缺口。
2. **产品粘性高。** 反应比回复门槛更低、参与率更高。1:1 消息中反应可以替代"收到"、"好"之类的简短回复。
3. **实现极低成本。** 一张表（已存在！），两个 API（Add/Remove），一个通知事件（`reaction.added`/`reaction.removed`），SSE 推送。后端代码量 < 200 行。
4. **与现有通知系统无缝集成。** 反应事件走 outbox → notification → SSE，不需要新基础设施。

**实现要点：**
- 启用 `internal/handler/grpc/message.go` 中的 `AddReaction` / `RemoveReaction` RPC（如果尚未实现）
- 在 `internal/service/message.go` 中增加 `React` / `Unreact` 方法
- 在 `internal/repository/message.go` 中利用已存在的 `reaction_audit_integration_test.go` 验证过的 repository
- 通知：`message.reaction_added` → 推送给消息 `to_uid`
- 注意：不支持对软删除的消息添加反应、每个用户每个消息只允许 1 个反应（UNIQUE 约束已经主键是 `(message_id, uid)` ✅）

---

### 🎯 方向 B：文件病毒扫描 / 内容安全

**概念：** 在上传的文件上自动执行病毒扫描（ClamAV）和内容安全检查（图片 NSFW 检测、文本敏感词过滤）。

**为什么需要它：**
1. **平台责任。** 一旦 aero-id 作为多用户平台运行，用户上传的文件可能包含恶意软件、不当内容。平台有法律责任删除和在特定情况下举报。
2. **不需要用户端扫描。** 移动端/Web 端无法可靠扫描文件。服务器端扫描是唯一可行的方案。
3. **可作为异步管道集成。** 扫描不阻塞上传路径——使用 outbox 事件的消费者模式。文件上传后 → 出事件 `file.uploaded` → 扫描 worker 消费 → 更新文件状态（`clean` / `quarantined`）。
4. **现有架构就绪。** outbox 事件已经支持延迟消费。`files` 表可以增加 `scan_status` 列（`pending`/`clean`/`infected`/`error`）。

**实现要点：**
- `files` 表增加列：`scan_status TEXT DEFAULT 'pending'`、`scan_at TIMESTAMPTZ`、`scan_result TEXT`
- 新建 `internal/scanner/` package：实现 ClamAV 客户端（通过 clamd socket 或 REST API）
- outbox 事件类型 `file.uploaded` → scanner worker 消费 → 更新 `scan_status`
- 扫描结果可选：通知文件拥有者、block 分享、quarantine 访问
- 延迟容忍：扫描可以在上传后 30s 内完成，不阻塞用户立即使用文件
- 资源预算：每个文件扫描平均 200ms-2s，需要配置 worker 池

---

### 🎯 方向 C：消息进度 / "已发送" → "已送达" → "已读" — 送达确认

**概念：** 当前 `read_at` 只跟踪已读状态。增加"已送达"状态（服务器已推送到目标设备/会话）。跟踪三级状态：已发送 → 已送达 → 已读。

**为什么需要它：**
1. **与已读回执历史互补（方向 E in 第五篇）。** 已读回执历史回答"谁读了"，送达确认回答"谁收到了"——两者组合提供完整的消息生命周期追踪。
2. **1:1 消息中是最基础的用户期望。** Telegram/WhatsApp 显示双勾（✓✓），一个勾表示"已发送"，两个勾表示"已送达"，两个蓝色勾表示"已读"。
3. **当前架构支持。** `messages` 表可以增加 `delivered_at` 列。SSE hub 在消息创建后可以尝试推送送达确认。如果用户离线，送达确认在用户连接 SSE 时发送。
4. **与客户端交互。** SSE 事件 `message.delivered` → 客户端更新 UI（灰勾 → 白勾）。

**实现要点：**
- `messages` 表增加 `delivered_at TIMESTAMPTZ`
- 在 `message.created` 事件被 outbox 处理 + notification 创建后，如果目标用户已连接 SSE，立即设置 `delivered_at = now()`
- 如果目标用户离线，`delivered_at` 保持 NULL，在用户重新连接 SSE 时批量设置（`UPDATE messages SET delivered_at = now() WHERE to_uid = ? AND delivered_at IS NULL AND deleted_at IS NULL`）
- API：`GET /v1/messages/:id/receipts` → `{id, sent_at, delivered_at, read_at}`
- SSE 事件类型：`message.delivered`、`message.read`
- 注意：不要在 `delivered_at` 上建立凸版更新索引（只会更新一次，且 WHERE 条件 `delivered_at IS NULL` 适合部分索引）

---

### 🎯 方向 D：简洁 API — 批量操作

**概念：** 添加批量 API 端点，允许用户在一个请求中操作多个资源：批量删除消息、批量标记已读、批量删除文件。

**为什么需要它：**
1. **当前所有操作都是单资源。** `DELETE /v1/messages/:id` 一次删一条。如果用户要清空与某人的会话（1000 条消息），需要 1000 个 HTTP 请求（或并发但每个请求都需要独立的认证 + 权限检查）。
2. **移动端/低带宽场景的关键优化。** 批量操作减少往返次数。一个 `POST /v1/messages/batch-delete` 替代 100 个独立 DELETE。
3. **不需要断页/分页。** 批量操作不受分页限制——可以删除会话中的所有消息，不需要迭代页面。
4. **实现简单。** 服务层重用现有业务逻辑（`Get` + `SoftDelete`），只是增加批处理循环。注意事务边界——批量操作应在单个 DB 事务中执行，或者每个操作独立（带有限制如"最多 500 条"）。

**实现要点：**
- API 设计（HTTP）：
  ```
  POST /v1/messages/batch-delete    { "ids": ["uuid1", "uuid2", ...] }
  POST /v1/messages/batch-read      { "ids": ["uuid1", "uuid2", ...] }
  POST /v1/comments/batch-delete    { "ids": ["uuid1", "uuid2", ...] }
  DELETE /v1/messages/conversation/:uid  清空与某人的会话
  ```
- gRPC 等价：`BatchDeleteMessages`、`BatchMarkRead`（现有 `MarkConversationRead` 类似）
- 批量大小限制：建议最多 500 条（防止内存 OOM 和连接池耗尽）
- 事务：所有删除在一个事务内（全有或全无），或逐条删除并返回失败列表
- 响应：
  ```json
  { "data": { "succeeded": ["id1", "id2"], "failed": [{"id": "id3", "error": "not_found"}] }}
  ```
- 幂等性：支持 `Idempotency-Key` header（如果批量操作中途失败，客户端可以用同一 key 重试；幂等中间件可以复用之前的 response）

---

### 🎯 方向 E：服务健康仪表盘 & 自诊断工具

**概念：** 构建一个内部的 `/debug/` 或 `/admin/` HTTP 路由树，提供自诊断页面、运行时状态和控制台，用于运维调试。

**为什么需要它：**
1. **当前运维诊断链路由 `healthz`/`readyz`/`metrics` 和日志组成，缺少中间层。** `healthz` 是 binary（live/dead），`readyz` 是 binary（ready/not），`metrics` 是 Prometheus 数值。运维人员无法获得"更软"的诊断信息（如"数据库连接池使用了 30/50 个"、"SSO 缓存命中率 87%"）。
2. **非侵入式排查问题。** 生产问题排查时，工程师需要：
   - 检查配置值（`GET /debug/config`）→ 但不暴露密码
   - 查看 goroutine 栈（`GET /debug/pprof/goroutine`）
   - 查看数据库连接池状态（`GET /debug/db`）
   - 查看 outbox 积压量（`GET /debug/outbox`）
   - 查看 SSO 缓存状态（`GET /debug/sso/cache`）
   - 查看最近 N 条 audit_event（`GET /debug/audit/recent`）
3. **Go 标准工具已有部分能力。** `net/http/pprof` 提供 debug 端点，`expvar` 提供运行时变量。只需挂载到 `/debug/` 下并添加应用层端点。
4. **安全：仅在 `server.admin.enabled: true` 时暴露，可绑定在独立端口（如 `:9091`）**，不暴露给外部用户。

**实现要点：**
- 配置：`server.admin.enabled`（默认 false）、`server.admin.bind`（默认 `":9091"`）
- `/debug/pprof/` — Go 标准 pprof
- `/debug/config` — 非敏感配置（屏蔽密码、签名密钥、S3 凭证）
- `/debug/db` — `db.Stats()`（OpenConns、InUse、Idle、WaitCount、MaxOpenConns）
- `/debug/outbox` — `SELECT count(*) FROM outbox_events WHERE published_at IS NULL` + 最旧事件年龄
- `/debug/goroutine` — `runtime.NumGoroutine()`
- `/debug/version` — 构建时间、git hash、Go 版本
- `/debug/health/detail` — 每个依赖的逐项健康检查结果（DB → SSO → S3 → relay）
- 注意：**不要在公网暴露 `/debug/`**——建议绑定在 internal IP 或 localhost

---

## 7. 前五次未覆盖的小型边界情况

| # | 问题 | 严重性 |
|---|------|--------|
| 1 | `files.checksum` 无 UNIQUE 约束，去重检测基于 `SELECT 1` 不可靠 | Low |
| 2 | `messages.content NOT NULL` 阻止仅文件消息（无文本内容） | Medium |
| 3 | `messages.kind` 无类型枚举常量/文档 | Low |
| 4 | `comments.target_type` 无 CHECK 约束 | Low |
| 5 | `comment_likes` 和 `message_reactions` 的 PK 组合允许用户自赞/自反应（业务层应禁止） | Medium |
| 6 | `audit_events` 无 TTL/清理机制 | High |
| 7 | `outbox_events.payload` 无版本号 | Medium |
| 8 | `go-redis` 依赖可能存在但未使用 | Low |
| 9 | `google/uuid` 依赖可能与 `idgen` 冲突 | Low |
| 10 | 无 `go test -race` CI 步骤 | Medium |

---

## 8. 六篇审查全景图

```
六篇分析合辑覆盖维度（✅ = 已覆盖，空白 = 下次）：

                   1  2  3  4  5  6
                  ─────────────────
架构/一致性        ✅    ✅
安全/CSRF/TLS              ✅
运维/Docker/优雅关机       ✅
API 设计一致性              ✅
数据库索引/查询                     ✅
数据库 Schema 设计                    ✅
测试金字塔                             ✅
依赖/模块健康                          ✅
客户端 SDK/消费者                       ✅
Metrics 系统                  ✅
错误/异常路径                 ✅     ✅
中间件链                      ✅
降级策略                      ✅
配置系统                      ✅
产品方向/扩展        ✅ ✅  ✅  ✅  ✅  ✅

高价值扩展方向覆盖（前五次 25 个 + 本次 5 个 = 30 个方向）:
  1: 群聊 · 文件预览 · 管理角色 · 通知偏好 · 分布式限流
  2: 多租户 · 会话归档/静音 · 数据导出/GDPR · Web 搜索增强 · Webhook 事件平台
  3: 文件版本控制 · 定时发送 · 消息转发 · 文件标签 · 跨区域部署
  4: 可插拔认证 · gRPC-Web · 数据生命周期 · SLO 框架 · OpenAPI 文档
  5: 在线状态 · 消息草稿 · API Keys · 分析管线 · 已读回执历史
  6: 表情反应 · 病毒扫描 · 送达确认 · 批量操作 · 诊断仪表盘
```
