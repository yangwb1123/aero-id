# aero-id — 第五波扩展：平台化基础设施与深层架构缺口

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局扫描 `internal/` 全量源码（22 个包，180+ 源文件）、`migrations/` 22 份 SQL、`api/proto/`、`cmd/`、`configs/`，并与 `docs/requirements/` 下 38 份既有分析文档的全部方向标题和核心发现做**逐条比对**，确认以下 5 个方向在现有文档中**未被系统性覆盖**。
> **约束**: 不编写任何代码。每个方向附带代码级证据和设计建议。

---

## 前置：本文的独特定位

aero-id 已经是一个工程成熟度极高的服务——22 个可逆迁移、38+ gRPC RPC 与 HTTP 完全对等、事务性 outbox、幂等键、全文搜索、SSE 实时推送、Webhook、通知偏好（含静默时段）、通知折叠、用户阻止、管理角色与权限、审计日志、SSO 韧性（断路器 + stale-cache + singleflight + 证书固定）、RED 指标与 OTel 追踪、文件管线（MIME 白名单、大小限制、checksum 去重、流式上传、预签名直传 S3、引用计数 GC）、限流、后台 Goroutine 治理等。

已有 38 份分析文档累计覆盖了 **70+ 个扩展方向**，包括：

| 类别 | 代表方向 |
|------|---------|
| 产品功能 | 群聊、文件版本、@提及、表情反应、活动流、消息线程、内容集合、暗色模式 |
| 架构演进 | 多副本缓存粘滞路由、分布式限流、GraphQL/BFF、多区域部署、事件溯源、Schema 版本化 |
| 运维成熟度 | SLO 框架、混沌测试、配置漂移检测、供应链安全、零停机迁移、异地灾备 |
| 正确性/Edge Case | 并发计数竞争、FTS 写放大、Outbox 顺序保证、SSE 断线重连、通知表无限增长 |
| 安全合规 | E2EE、静态加密、PDF 证书固定、审计告警、内容安全检测 |
| 开发者体验 | OpenAPI 规范、SDK 生成、开发者门户、集成测试套件 |
| 平台能力 | 资源配额、媒体加工管线、定时任务引擎、Webhook 平台、多租户、数据导出 |

本文聚焦的 5 个方向在上述所有分析中**未被系统性覆盖**。它们是"平台化"视角下的深层缺口——不是单一功能缺失，而是整个平台缺少的基础设施层。

---

## 方向一：Webhook 交付平台化（Webhook Delivery Platform）

### 为什么需要

当前 webhook 是**最简单可用实现**——`outbox.WebhookHandler` 对每个事件发起一次 HTTP POST，非 2xx 则在下次 relay tick 重试，超过 `MaxRetries` 进入死信（静默丢弃）。这在 MVP 阶段够了，但作为一个被外部系统依赖的集成通道，缺失以下能力：

| 缺失能力 | 对消费者的影响 |
|---------|-------------|
| 交付历史查询 | 无法排查"某个事件是否送达了" |
| 重试策略差异化 | 所有事件类型用同一重试策略（次数 + 间隔），无法为关键事件（如消息事件）配置更激进的重试，而忽略次要事件 |
| 递送延迟与成功率统计 | 无法监控 webhook 健康度，无法定义 SLO |
| 密钥轮换 | HMAC 签名密钥从环境变量读取，启动后不可变；泄露后需重启整个服务 |
| 事件重放 | 消费者丢失事件后只能等待实时推送，无法从历史重放 |
| 消费者幂等指引 | 事件无 `idempotency_key` 字段，消费者需要自己实现去重 |

### 代码级证据

```go
// internal/outbox/webhook.go — 当前 WebhookHandler 的结构
type WebhookHandler struct {
    cfg    WebhookConfig    // 单 URL, 单签名密钥, 单超时
    client *http.Client
    log    *zap.Logger
}

// WebhookConfig 定义：
type WebhookConfig struct {
    URL        string            // 单个 endpoint
    SigningKey string            // 单个密钥，启动后不可变
    Timeout    string
    EventTypes []string          // 过滤器
    Headers    map[string]string // 固定的附加头
}
```

```go
// internal/outbox/webhook.go — 递送逻辑：成功即忘，失败在 relay 层重试
func (h *WebhookHandler) Handle(ctx context.Context, e *Event) error {
    body, _ := json.Marshal(payload)
    req, _ := http.NewRequestWithContext(ctx, "POST", h.cfg.URL, bytes.NewReader(body))
    req.Header.Set("X-Event-Signature", sign(body, h.cfg.SigningKey))
    resp, err := h.client.Do(req)
    if err != nil || resp.StatusCode >= 300 {
        return fmt.Errorf("webhook delivery failed: ...") // → relay 重试
    }
    return nil // 成功，无日志、无历史、无分析
}
```

- `internal/outbox/relay.go` — relay 的 `MaxRetries` 是全局配置，无法按事件类型差异化
- `internal/outbox/repository.go` — `outbox_events` 表有 `published_at`，**无** delivery_attempts、last_error、consumer_ack 等字段
- **无** `POST /admin/webhook/secret/rotate` 端点
- **无** 递送分析指标（已超出 outbox 已有的 published/failed 计数）

### 建议方案

#### 数据模型扩展

```sql
-- outbox_events 表增加递送元数据
ALTER TABLE outbox_events ADD COLUMN IF NOT EXISTS
    delivery_attempts     INT NOT NULL DEFAULT 0,
    last_error           TEXT,
    last_delivered_at    TIMESTAMPTZ,
    consumer_ack_key     TEXT,         -- 消费者返回的幂等确认密钥
    acked_at             TIMESTAMPTZ;  -- 消费者确认时间

-- 新增：多 webhook 订阅者
CREATE TABLE webhook_subscribers (
    id              UUID PRIMARY KEY,
    owner_uid       TEXT NOT NULL,          -- SSO uid
    url             TEXT NOT NULL,
    signing_key     TEXT NOT NULL,          -- 每个订阅者可独立密钥
    event_types     TEXT[],                 -- 空 = 全部
    headers         JSONB,
    timeout         INTERVAL DEFAULT '10s',
    max_retries     INT DEFAULT 3,
    retry_backoff   INTERVAL DEFAULT '1s',  -- 指数退避基数
    enabled         BOOLEAN DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### API Surface

| 端点 | 说明 |
|------|------|
| `POST /v1/webhooks` | 注册 webhook 订阅者 |
| `GET /v1/webhooks` | 列出我的 webhook |
| `PUT /v1/webhooks/:id` | 更新（含密钥轮换） |
| `DELETE /v1/webhooks/:id` | 删除 |
| `POST /v1/webhooks/:id/rotate-key` | 独立密钥轮换（新密钥 `X-Event-New-Key` 同时发送一段时间） |
| `GET /v1/webhooks/:id/deliveries` | 递送历史（分页） |
| `POST /v1/webhooks/:id/replay?after=<ts>` | 重放在 after 之后产生的所有事件 |

#### 密钥轮换安全设计

1. `POST /rotate-key` 生成新密钥，旧密钥继续有效 72 小时
2. 事件同时携带 `X-Event-Signature`（旧密钥）和 `X-Event-New-Signature`（新密钥）
3. 72h 后旧密钥废弃，新密钥成为唯一签名密钥
4. 消费者有充足的时间窗口迁移

#### 递送分析指标

在 `internal/pkg/metrics` 中增加：
- `webhook.delivery_count{status=2xx|4xx|5xx|timeout}`
- `webhook.delivery_latency_seconds{p50|p95|p99}`
- `webhook.events_pending`（待递送事件数）
- `webhook.oldest_pending_age_seconds`

### 边界情况

- **消费者慢响应**：超时配置 + 独立 `http.Client` per subscriber，不阻塞其他订阅者
- **密钥泄露**：`POST /rotate-key` 即时生效，旧密钥在 72h 过渡期内仍可验证签名但标记为 `compromised`，在管理日志中告警
- **背压**：当递送持续失败（下游宕机）时，事件在 outbox 表中堆积。需增加 `max_pending_events` 保护，超限时自动暂停该订阅者并告警
- **事件顺序**：当前 relay 按 `created_at ASC` 轮询，单线程处理。多订阅者场景下需保证每个订阅者的事件顺序，但不同订阅者之间可并行

### 估算

- 新表 + migration：1 个文件
- 新 `internal/webhook/` 包：~500 行（repository + service + handler）
- 修改 `internal/outbox/relay.go`：支持多订阅者 + 递送日志
- 支持现有单 endpoint 向后兼容
- **总计约 2 周**

---

## 方向二：结构化文件元数据查询层（Structured File Metadata Query Layer）

### 为什么需要

`domain.File` 的 `Meta map[string]any` 字段在 PostgreSQL 中以 JSONB 存储，但 API 层面**完全不可查询**。这直接限制了 aero-id 作为"用户自有数据服务"的核心价值：

| 场景 | 当前状态 | 期望 |
|------|---------|------|
| 发票管理 | 上传发票时在 Meta 存 `{"invoice_no":"INV-001","amount":1500,"status":"paid"}` | `GET /v1/files?meta.invoice.status=paid&meta.invoice.amount>1000` |
| 媒体资产 | 上传照片时在 Meta 存 `{"camera":"iPhone15","gps":"31.23,121.47","taken_at":"2026-07-10T10:00:00Z"}` | `GET /v1/files?meta.taken_at>=2026-01-01&meta.camera=iPhone15` |
| 文档分类 | 上传合同时存 `{"type":"contract","party":"Acme Corp","expires_at":"2027-01-01"}` | `GET /v1/files?meta.type=contract&meta.party=Acme` |
| 自定义字段索引 | 无法为 Meta 中的自定义字段创建索引 | 可以为高频查询字段创建 GIN 索引或部分索引 |

### 代码级证据

```go
// internal/domain/file.go:16-28
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
    Meta        map[string]any  // ← JSONB, 但 API 无法过滤/排序/投影
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

```sql
-- migrations/000001_create_files.up.sql
-- Meta 列的定义（JSONB）
meta        JSONB NOT NULL DEFAULT '{}',

-- 索引情况：仅有一个 checksum 索引用于去重
CREATE UNIQUE INDEX IF NOT EXISTS idx_files_checksum ON files (checksum) WHERE checksum != '';
-- 无任何 JSONB 索引（GIN/btree-jsonb）
```

```go
// internal/file/handler_http.go — List 端点
// 当前只支持分页参数 (cursor, limit) 和 ownership 过滤
func (h *FileHandler) List(c *gin.Context) {
    list, err := h.svc.List(c.Request.Context(), httpx.PageFromQuery(c))
    // 不支持 meta.* 过滤参数
}
```

```go
// internal/file/service_crud.go — List 方法
func (s *fileService) List(ctx context.Context, page domain.Page) (*FileList, error) {
    actor, _ := domain.MustActor(ctx)
    files, next, err := s.repo.ListByOwner(ctx, actor, page)
    // └→ WHERE owner_uid = ? ORDER BY id DESC LIMIT n+1
    //   仅 owner 过滤，无 meta 条件
}
```

### 建议方案

#### 查询语法

采用 Postgres JSONB 原生操作符的轻量映射，不在 API 层引入完整查询语言：

```
GET /v1/files?meta.status=paid
→ WHERE meta @> '{"status": "paid"}'

GET /v1/files?meta.amount_min=1000&meta.amount_max=5000
→ WHERE (meta->>'amount')::numeric BETWEEN 1000 AND 5000

GET /v1/files?meta.type=contract&meta.party__contains=Acme
→ WHERE meta @> '{"type": "contract"}' AND meta->>'party' ILIKE '%Acme%'

GET /v1/files?meta.created__gt=2026-01-01
→ WHERE (meta->>'created')::date > '2026-01-01'
```

#### 操作符约定

| 后缀 | 含义 | SQL |
|------|------|-----|
| (无) | 精确匹配（=） | `meta @> '{"key": "value"}'` |
| `__gt` | 大于 | `(meta->>'key')::type > val` |
| `__gte` | 大于等于 | ... |
| `__lt` | 小于 | ... |
| `__lte` | 小于等于 | ... |
| `__contains` | ILIKE 包含 | `meta->>'key' ILIKE '%val%'` |
| `__exists` | 检查键存在 | `meta ? 'key'` |
| `__in` | IN 列表 | `meta->>'key' = ANY(...)` |

#### 索引策略

```sql
-- GIN 索引：覆盖 @> 和 ? 操作符（大多数场景）
CREATE INDEX IF NOT EXISTS idx_files_meta_gin ON files USING GIN (meta);

-- 按需：对高频查询字段创建 btree 索引（范围查询加速）
CREATE INDEX IF NOT EXISTS idx_files_meta_amount ON files ((CAST(meta->>'amount' AS numeric)));
```

#### API 变更

| 变更 | 位置 | 复杂度 |
|------|------|--------|
| `List` 端点新增 `meta_*` 查询参数 | `internal/file/handler_http.go` + `internal/file/handler_grpc.go` | 低 |
| Service 层将 meta 参数转为 SQL 条件 | `internal/file/service_crud.go` | 中 |
| Repository 层支持动态 WHERE 条件 | `internal/file/repository.go` — `ListByOwner` 增加可选条件 | 中 |
| proto 扩展 `ListFilesRequest` | `api/proto/aero_id/v1/aero_id.proto` | 低 |
| GIN 索引迁移 | `migrations/000023_meta_gin_index.up.sql` | 低 |

#### 安全边界

- 只有 owner 可以按 meta 查询（与现有 `List` 权限一致）
- Meta 查询参数的数量和总长度应有限制（如最多 10 个条件，总长 2KB）防止恶意构造复杂查询
- JSONB 操作不应对 `shared_files` 中的非 owner 暴露——分享的文件只能按基础字段过滤

### 估算

- Migration（GIN 索引）：1 个文件
- Repository 扩展：~80 行
- Service 解析 + 安全限制：~100 行
- HTTP handler 查询参数提取：~50 行
- gRPC handler + proto：~80 行
- **总计：约 4 天**

---

## 方向三：数据生命周期统一治理框架（Unified Data Lifecycle Governance）

### 为什么需要

当前所有核心实体——文件、消息、评论、通知、审计日志、outbox 事件——各自独立演进，没有任何统一的数据生命周期管理：

| 实体 | 行数增长方式 | 当前清理机制 | 风险等级 |
|------|------------|------------|---------|
| `files` | 上传即创建，软删后保留 | 存储 GC 可硬删，但 OPT-IN 且默认关闭 | 🟡 中 |
| `messages` | 每条消息一行，软删标记 | **无**任何硬删除或归档 | 🔴 高 |
| `comments` | 每条评论一行，软删标记 | **无**任何硬删除或归档 | 🔴 高 |
| `notifications` | 每条通知一行，SSE 推送后保留 | **无**清理（已有分析指出，尚未修复） | 🔴 高 |
| `audit_events` | 每次敏感操作追加 | **无**清理 | 🟡 中 |
| `outbox_events` | 每次写入操作追加，`published_at` 标记 | **无**清理（已发布的依然在表中） | 🟡 中 |
| `reply_count` / `like_count` | 计数器可能漂移 | **无**校验或修复 | 🟡 中 |

更关键的是，整个系统缺少两个跨实体的生命周期场景：

**场景 A：SSO 用户注销（De-provisioning）**
当 SSO 删除一个用户时，aero-id 没有任何钩子来处理该用户的数据——文件成为孤儿，消息发送方不可解析，评论成为"幽灵作者"。当前只能通过 SSO 的 `GetUsers` 缓存过期来逐渐淡化，但数据本身永远留在库里。

**场景 B：GDPR 被遗忘权（Right-to-Erasure）**
没有 API 可以级联删除某个 uid 的所有数据。用户若要删除自己的所有记录，需要逐个调用文件删除、消息删除、评论删除、评论举报删除等 API——流程脆弱且可能遗漏。

### 建议方案

#### 统一生命周期模型

```sql
-- 保留策略定义（系统级 + 可覆盖到用户/实体级别）
CREATE TABLE retention_policies (
    id              UUID PRIMARY KEY,
    entity_type     TEXT NOT NULL,         -- 'file' | 'message' | 'comment' | 'notification' | 'audit_event' | 'outbox_event'
    scope           TEXT NOT NULL DEFAULT 'system',  -- 'system' | 'user:<uid>' | 'entity:<id>'
    hard_delete_after INTERVAL,            -- 软删后多久硬删（NULL = 永不）
    archive_after   INTERVAL,              -- 多久后归档（NULL = 不归档）
    max_rows_per_owner INT,                -- 每用户最大行数（FIFO淘汰）
    legal_hold      BOOLEAN DEFAULT FALSE, -- 法律保留覆盖所有删除策略
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(entity_type, scope)
);
```

#### SSO De-provisioning Webhook 钩子

aero-id 应提供一个 webhook 接收端点（或 gRPC RPC），SSO 在删除用户时调用：

```proto
// 新增 RPC 或 HTTP 端点
rpc HandleUserDeleted(DeleteUserRequest) returns (DeleteUserResponse);

message DeleteUserRequest {
    string uid = 1;
    // 可选：transfer_to_uid 将数据转移给另一个用户
    string transfer_to_uid = 2;
    // true = 硬删除，false = 匿名化（uid 置空但保留内容）
    bool hard_delete = 3;
}
```

处理策略：
1. **匿名化**（默认）：将所有 `owner_uid`/`from_uid`/`to_uid`/`actor_uid` 替换为 `deleted-user:{uid}`
2. **转移**：如果指定 `transfer_to_uid`，将所有权转移给另一个用户
3. **硬删除**：级联删除所有关联数据（严格遵守 `retention_policies` 中的 `legal_hold`）

#### GDPR 数据导出 + 删除端点

```
GET  /v1/account/export?types=all
     → 生成数据导出包（JSON Lines 格式，包含该用户所有实体）
     → 通过 outbox 事件通知用户下载链接

DELETE /v1/account/data
     → 异步级联删除该用户的所有数据
     → 返回操作 ID，可查询进度
     → 完全删除后发送确认通知
```

#### 背景 Sweeper 统一调度

扩展现有的 `storage/gc/sweeper.go` 模式，创建一个统一的生命周期 sweep：

```go
type LifecycleSweeper struct {
    policies  RetentionPolicyRepository
    executors map[string]EntityExecutor // entity_type → 执行器
}

type EntityExecutor interface {
    // 硬删除超过策略期限的行
    PurgeExpired(ctx context.Context, policy RetentionPolicy, limit int) (int, error)
    // 按 owner 级联删除所有行
    CascadeDelete(ctx context.Context, uid string) error
    // 匿名化所有引用该 uid 的行
    AnonymizeUID(ctx context.Context, oldUID, newUID string) error
}
```

每个实体包（`internal/file/`、`internal/message/`、`internal/comment/` 等）实现 `EntityExecutor` 接口，让生命周期逻辑与业务逻辑保持在一起。

### 边界情况

- **Legal Hold**：当 `legal_hold=true` 时，所有删除/匿名化操作被跳过，即使 SSO de-provision 也不执行硬删除。需要管理端 `POST /admin/legal-hold` 设置
- **级联删除顺序**：先删评论（依赖点赞表），再删消息（依赖 reactions），再删文件（依赖 shares + versions），最后删通知和审计日志
- **大范围删除的进度追踪**：使用 outbox 事件驱动异步删除，单个事件处理 N 条记录，支持暂停/恢复

### 估算

- `retention_policies` 表 + migration：1 个文件
- `EntityExecutor` 接口 + `LifecycleSweeper`：~200 行
- 每个实体实现执行器：~50-100 行 × 6 实体
- SSO de-provisioning webhook handler：~150 行
- GDPR export/delete 端点：~200 行
- **总计：约 3 周**

---

## 方向四：集合式资源模型（Collection Resource Model）

### 为什么需要

当前所有资源是扁平的——文件没有文件夹、消息没有会话标签、评论没有话题分类。用户无法用自己的方式组织内容：

| 场景 | 问题 |
|------|------|
| **文件管理** | 用户有 500 个文件，无法创建"合同""照片""报告"等文件夹来分类。`Meta` 可以存标签但无法按标签做集合操作 |
| **消息分拣** | 收件箱中所有消息混在一起（即使已有会话概念），无法创建"工作""家庭"分类 |
| **评论收藏** | 用户在某个论坛帖子下看到一条高质量评论，无法"收藏"它 |
| **资源批操作** | "把这个文件夹里所有文件设为公开"需要 N 个 API 调用 |

### 建议方案

#### 核心数据模型

采用**多态集合**设计——一个集合可以包含不同类型的资源：

```sql
CREATE TABLE collections (
    id          UUID PRIMARY KEY,
    owner_uid   TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT,
    icon        TEXT,                        -- emoji 或图标 URL
    visibility  TEXT NOT NULL DEFAULT 'private',  -- 'private' | 'public' | 'shared'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ                  -- 软删
);

CREATE INDEX idx_collections_owner ON collections (owner_uid) WHERE deleted_at IS NULL;

-- 多态集合项
CREATE TYPE member_entity_type AS ENUM ('file', 'message', 'comment');

CREATE TABLE collection_members (
    id          UUID PRIMARY KEY,
    collection_id UUID NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    entity_type member_entity_type NOT NULL,
    entity_id   UUID NOT NULL,               -- files/messages/comments 的 id
    added_by    TEXT NOT NULL,
    note        TEXT,                         -- 用户添加时的备注
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(collection_id, entity_type, entity_id),
    UNIQUE(collection_id, sort_order)
);

CREATE INDEX idx_collection_members_collection ON collection_members (collection_id);
CREATE INDEX idx_collection_members_entity ON collection_members (entity_type, entity_id);
```

#### API Surface

```
# 集合 CRUD
POST   /v1/collections                          # 创建
GET    /v1/collections                          # 列出我的集合
GET    /v1/collections/:id                      # 详情（含成员预览）
PUT    /v1/collections/:id                      # 更新
DELETE /v1/collections/:id                      # 删除（级联删除成员，资源本身不受影响）

# 集合成员管理
POST   /v1/collections/:id/members              # 添加资源（批量）
DELETE /v1/collections/:id/members/:member_id   # 移除
PUT    /v1/collections/:id/members/reorder      # 重排序

# 集合上的批操作
POST   /v1/collections/:id/actions/export       # 导出集合中所有资源
POST   /v1/collections/:id/actions/change-visibility  # 批量修改可见性
```

#### 权限模型

| 操作 | 权限检查 |
|------|---------|
| 创建/编辑/删除集合 | `actor == collection.owner_uid` |
| 查看集合 | `actor == owner_uid` OR `visibility == 'public'` OR 被显式分享 |
| 向集合添加资源 | `actor == owner_uid` AND `actor` 拥有该资源（或资源是公开的） |
| 查看集合成员详情 | 与查看集合相同 |

#### 与已有功能的集成

- **分享**：集合可以作为一个整体被分享（重用/扩展 `file_shares` 多态支持）
- **通知**：当集合被分享或添加新成员时，outbox 事件 → 通知
- **搜索**：搜索结果中标明资源所属的集合（`collection_ids`）
- **文件版本**：切换文件版本时，集合成员仍指向文件 ID，版本不影响集合

#### 边界情况

- **重复添加**：`UNIQUE(collection_id, entity_type, entity_id)` 防止同一资源被重复加入同一集合
- **资源删除**：当集合中的某个文件/消息/评论被删除（软删）时，集合成员保留但标记为 `entity_deleted=true`，在列表中显示为"已删除"
- **集合嵌套**：不支持集合嵌套集合（保持模型简单）。如果需要层次结构，上层用文件夹名称的 `/` 分隔
- **跨用户分享中的集合**：Alice 分享给 Bob 的集合，Bob 只能查看成员、不能修改集合结构
- **批量操作的原子性**：向集合添加多个资源时，使用事务保证全部成功或全部失败

### 估算

- migration（2 张表 + 索引）：~60 行
- `internal/collection/` 包：~800 行（domain + repository + service + handler_http + handler_grpc）
- proto 扩展：~100 行
- 分享系统多态扩展：~100 行
- 搜索集成：~50 行
- **总计：约 2 周**

---

## 方向五：变更安全管理与预发布治理（Change Safety & Pre-Production Governance）

### 为什么需要

aero-id 是一个被 SSO 和其他服务依赖的关键基础设施。其变更安全当前完全仰赖开发者的个人纪律和 CI 流水线的基本检查（`go test` + `make lint`）。随着代码库增长（22 个包，180+ 源文件，22 个迁移），缺少以下系统级安全网：

| 场景 | 风险 | 当前防护 |
|------|------|---------|
| **数据库迁移错误** | 生产环境执行导致数据丢失或长时间锁表 | go test 只检查迁移文件名，不检查实际 SQL 正确性 |
| **Schema 隐式变更** | 修改 model struct 但忘记生成迁移 | **无** — Go struct 与 SQL schema 之间没有任何一致性检查 |
| **API 破坏性变更** | 添加/修改 proto 字段导致现有客户端异常 | **无** — proto 文件没有 breaking change 检测 |
| **配置漂移** | 生产配置与预期不同（密钥被轮换、限流被关闭） | **无** — 启动时只做必要字段验证 |
| **依赖升级** | go.mod 依赖升级引入行为变化 | **无** — CI 不检查依赖变更范围 |

### 建议方案

#### 1. 迁移预演（Migration Shadow Run）

在执行迁移前，先在只读副本或同 schema 沙箱数据库上"预演"一次：

```bash
# 在 CI 中
$ shadow-migrate --dsn "$READ_ONLY_REPLICA_DSN" --migrations ./migrations --check-only
→ 验证所有 SQL 语法正确
→ 验证每个 `ALTER TABLE` 不会长时间阻塞（pg_locks 分析）
→ 验证 `down.sql` 能成功回滚
→ 估算迁移执行时间
```

**实现**：
- 将当前 `internal/pkg/migrate` 中的 `m.Up()` 逻辑提取为 `m.DryRun()` 方法
- `DryRun` 在事务内执行所有 SQL 后 ROLLBACK（对于 DDL，大多数 PostgreSQL 操作支持事务回滚）
- 报告：每条迁移的执行时间、影响的预估行数、锁超时风险

#### 2. Schema 一致性检查（ORM ↔ Migration Diff）

当 `internal/domain/*.go` 中的 GORM 模型变更时，自动检测是否缺少对应的迁移：

```bash
$ check-schema-diff --dsn "$TEST_DB_DSN" --migrations ./migrations
→ 自动运行所有 migration
→ 将 GORM 模型 AutoMigrate 与 migration 结果对比
→ 报告"有列在模型中有但在 schema 中无"或反之
```

**实现思路**：
- 创建一个 `internal/pkg/schemadiff` 包
- 使用 GORM 的 `AutoMigrate` 将当前模型迁移到临时数据库
- 使用 `migrate.Up()` 将迁移文件应用到另一个临时数据库
- 对比两个数据库的 schema（表、列、索引、约束）
- 在 CI 中作为强制门禁

#### 3. API 兼容性检查（Proto Breaking Change Detection）

```bash
$ check-proto-compat --before api/proto/v1/aero_id.proto --after api/proto/v1/aero_id.proto.new
→ 检测：
  - 删除/重命名字段（wire 不兼容）
  - 更改字段类型（wire 不兼容）
  - 添加 required 字段（旧客户端不发送）
  - 删除 RPC 方法（客户端调用失败）
→ 允许的变更：添加可选字段、添加新的 RPC 方法、添加新的 service
```

**实现**：
- 使用 `buf breaking` 或 `protolock` 工具
- 将当前 proto 文件快照提交到 CI artifact
- PR 中检测 proto 变更与基线对比
- 发现 breaking change 时在 PR 上注释详细报告

#### 4. 配置漂移检测

```bash
$ check-config-drift --config "configs/config.yaml" --running "http://:8080/debug/config"
→ 比较本地配置文件与正在运行实例的配置
→ 报告不一致（密钥值仅比较哈希，不暴露明文）
```

**实现**：
- 新增 `GET /debug/config` 端点（只在非生产环境或管理端口暴露）
- 返回当前运行时配置的哈希树
- `check-config-drift` 工具解析本地 config.yaml，计算相同哈希树，对比差异

#### 5. CI 门禁集成

```yaml
# .github/workflows/ci.yml 扩展
jobs:
  pre-prod:
    steps:
      - run: make shadow-migrate        # 迁移预演
      - run: make check-schema-diff     # schema 一致性
      - run: make check-proto-compat    # API 兼容性
      - run: make check-import-rules    # 架构依赖规则
      - run: make check-file-sizes      # 已有文件大小检查
```

### 边界情况

- **Shadow Migration 在事务不可回滚 DDL 时的处理**：PostgreSQL 的 `CREATE INDEX CONCURRENTLY` 不在事务内运行。Shadow run 需要跳过此类语句，或使用 `SET lock_timeout` 检测锁风险
- **Proto 兼容性对新旧客户端共存的支持**：允许添加 optional 字段（向后兼容），但禁止删除/重命名。旧客户端在新 server 上应正常工作
- **Schema diff 的误报处理**：GORM 模型中的 `json:"-"` 或 `gorm:"-"` 标签标记的字段应被忽略；GORM 自动创建的索引（如外键索引）不应被视为"缺失迁移"
- **配置漂移检测的安全**：`/debug/config` 应在独立的管理端口或仅允许内网 IP 访问；密钥以 SHA-256 哈希形式返回

### 估算

| 组件 | 估算 |
|------|------|
| `internal/pkg/schemadiff/`（migration shadow run + schema diff） | ~400 行 |
| proto compat 集成（调用 `buf breaking`） | ~50 行 shell |
| `GET /debug/config` + drift check tool | ~150 行 |
| CI 配置调整 | ~30 行 YAML |
| **总计：约 5 天** |

---

## 方向优先级建议

| 方向 | 业务影响 | 技术杠杆 | 风险降低 | 估算 | 建议优先级 |
|------|---------|---------|---------|------|-----------|
| ① Webhook 交付平台化 | ★★★★ | ★★★★★ | ★★★ | 2 周 | **P1**（平台化基础，外部依赖入口） |
| ② 结构化元数据查询 | ★★★★ | ★★★ | ★★ | 4 天 | **P1**（解锁企业内容管理场景） |
| ③ 数据生命周期治理 | ★★★ | ★★★ | ★★★★★ | 3 周 | **P0**（合规 + 存储安全） |
| ④ 集合式资源模型 | ★★★★★ | ★★★ | ★ | 2 周 | **P2**（高用户体验价值，但非基础设施） |
| ⑤ 变更安全管理 | ★★ | ★★★★ | ★★★★★ | 5 天 | **P0**（防止生产事故的安全网） |

**执行建议**：

- **第一梯队（P0 — 立即启动）**：
  - 方向③ 数据生命周期治理：SSO de-provisioning 和 GDPR 合规是时间敏感的需求。通知表无限增长是已知问题。
  - 方向⑤ 变更安全管理：迁移 shadow run + schema diff 是预防性投入，投入产出比极高。在下一个生产版本前部署。

- **第二梯队（P1 — 下一迭代）**：
  - 方向① Webhook 交付平台化：有多外部消费者依赖时价值加倍。当前单 endpoint 设计不影响功能，但不具备平台扩展性。
  - 方向② 结构化元数据查询：4 天的工作量就可以解锁企业级文件管理场景。结合方向④ 集合模型使用价值最高。

- **第三梯队（P2 — 长期规划）**：
  - 方向④ 集合式资源模型：用户体验价值最高但技术依赖最广。建议在方向②（元数据查询）和文件分享功能稳定后再投入。

---

## 与现有代码库的关系

本文件 5 个方向与当前代码库的集成点：

| 方向 | 重用的现有设施 | 需修改的包 |
|------|-------------|-----------|
| Webhook 平台 | `outbox.Event`、`outbox.Relay`、`outbox.WebhookHandler`、`pkg/metrics` | `internal/outbox`、新 `internal/webhook/` |
| 元数据查询 | `domain.File.Meta`、`repository.FileRepository.ListByOwner`、GIN 索引 | `internal/file`、`api/proto/` |
| 数据生命周期 | `storage/gc/sweeper.go` 模式、`audit.AuditLogger`、`outbox` | 跨所有实体包 + 新 `internal/lifecycle/` |
| 集合资源 | `file_shares` 多态经验、分页模式、权限检查模式 | 新 `internal/collection/`、`api/proto/` |
| 变更安全 | `pkg/migrate`、CI 流水线、`Makefile` | 新 `internal/pkg/schemadiff/`、CI 配置 |

---

*本文基于全局代码扫描 + 38 份既有分析文档的交叉比对。每个方向都满足：① 有明确代码级证据，② 在所有既有文档中零系统性覆盖，③ 从架构师和产品经理双视角论证产出价值。无代码变更。*
