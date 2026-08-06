# aero-id — Post-Roadmap 全量代码扫描：五个高价值扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 + 产品经理
> **方法**: 对 `internal/` 全部 45+ Go 包、`migrations/` 22 份 SQL、双传输层 handler、`cmd/`、`configs/` 进行全局扫描，交叉比对 `docs/requirements/` 下 30+ 份既有分析文档的方向索引，确保以下方向未被系统性覆盖。
> **约束**: 不编写任何代码，仅分析。

---

## 代码库现状摘要

当前 aero-id 已达到很高的工程成熟度：完整 CRUD 覆盖文件/消息/评论三个实体，双传输层（HTTP + gRPC）完全对等（38 个 RPC），22 个可逆迁移，OOB 观察体系（RED 指标、OTel 追踪、SSO 健康仪表盘、Outbox 仪表盘），写路径正确性保障（事务性 outbox、幂等键、原子计数器），SSO 韧性（断路器、stale-cache、singleflight、TLS/mTLS），内容安全（文件大小限制、MIME 白名单、checksum 去重、存储 GC 回收），用户安全（拦截、举报审核队列、通知偏好静默时段），实时推送（SSE Hub），全文搜索（PostgreSQL tsvector），管理后台（审计日志 + 角色权限）。

以下 5 个方向基于 **具体代码证据**，代表从 MVP 到生产级可扩展平台的必经之路。

---

## 方向一：文件内容安全管线（Content Safety Pipeline）

### 代码证据

文件上传管线在安全方面存在根本性空白。以下代码路径确认 **零自动内容检查**：

| 位置 | 安全缺口 |
|------|---------|
| `internal/file/service_upload.go` | `UploadFile` → `storage.Put` 之间仅校验 `MimeType` 白名单和 `MaxUploadBytes`，**无病毒扫描、无 NSFW 检测、无文档文本提取** |
| `internal/file/service_upload.go` → `CompleteUpload` | 预签名直传 S3 完成后，`Stat` 对象大小后直接写行，**不经任何安全检测线** |
| `internal/storage/local.go` / `s3.go` | `Put` 和 `PresignUpload` 透传 reader 到磁盘或 S3，**零检查** |
| `internal/storage/storage.go` | `FileStorage` 接口定义 `Put/Get/Delete/PresignUpload/Stat`，**无安全相关的返回或回调** |
| `internal/outbox/relay.go` | Outbox relay 仅处理 `message.sent`/`message.read`/`comment.replied` 三类事件 → 通知，**无文件处理类事件** |

产品层面，评论举报（`internal/comment/handler_http.go`, migration `000007_create_comment_reports`）是 **事后人工** 驱动的审核。对于文件，连事后举报的 API 也没有——文件的 `visibility` 只有公开/私有二元状态，没有"举报此文件"的产品入口。

这意味着任何用户上传的文件——包括恶意软件、盗版内容、色情图像——都会不经检查地永久存储在存储后端，直到有人工发现并手动删除。

### 为什么需要

1. **法律合规**: 多数司法管辖区要求平台对用户上传内容进行合理检查（DMCA、GDPR、儿童保护）。缺乏任何自动化管线构成合规风险。
2. **平台安全**: 无病毒扫描意味着 aero-id 本身可能成为恶意软件的分发渠道（即使文件只对上传者可见，一个被盗的 token 就能下载）。
3. **信任模型缺陷**: 产品承诺"data owned by accounts"，但没有任何机制确保这些数据不会被滥用来托管非法内容。

### 建议方向

利用现有 outbox 基础设施，添加文件处理事件类型（`file.uploaded`）。新增 `internal/content/` 包定义扫描接口：

```
outbox relay → 文件安全管线
  ├─ ClamAV 病毒扫描（检查已签名的可执行文件/宏/OLE 对象）
  ├─ 图像 NSFW 检测（基于 TensorFlow/VGG16 或第三方 API）
  ├─ 文档文本提取 + 敏感内容匹配（DLP：信用卡、API key 正则）
  └─ 结果回写（标记文件为 clean/rejected/quarantined）
```

| 组件 | 建议 | 已有基础 |
|------|------|---------|
| 事件触发 | outbox 新增 `file.uploaded` 事件类型 | ✅ outbox 表+ relay 已就绪 |
| 处理管线 | 异步 worker（与 relay 解耦，不阻塞通知分发） | ❌ 需新建 |
| 结果存储 | `files` 表新增 `scan_status` / `scan_result` 列 | ❌ 需迁移 |
| 隔离机制 | quarantine 桶（被拒文件移入隔离区而非直接删除，供人工复核） | ❌ 需设计 |
| 重试/死信 | 利用 outbox 现有 retry + dead-letter 机制 | ✅ 迁移 000018 |

---

## 方向二：多实例部署架构（Multi-Instance Readiness）

### 代码证据

所有关键运行时状态均为 **单进程内内存**，多副本部署时出现一系列结构性断裂。代码中无任何分布式协调或跨实例通信：

| 状态类型 | 位置 | 问题 |
|---------|------|------|
| SSE 连接注册表 | `internal/hub/hub.go:31` — `sync.Map` keyed by uid | 用户连到实例 A 后，实例 B 产生的通知通过 `Pusher.Publish` 仅推送给本 Hub ← 用户丢失实时推送 |
| 速率限制令牌桶 | `internal/pkg/ratelimit/ratelimit.go` — 进程内 `sync.Map` | 每个副本独立计数。假设 `rps=20`，三副本部署实际全局限量 = 60；用户轮询不同副本可绕过限额 |
| SSO Token 缓存 | `internal/sso/token_cache.go` — `sync.Map` | 滚动部署期间新副本冷启动，第一批请求全部穿透到 SSO；5 副本同时重启，SSO 瞬时负载暴增 5 倍 |
| 幂等键存储 | `internal/idempotency/store.go` — GORM store（DB 层） | ✅ 天然跨副本正确 |
| Outbox relay | `internal/outbox/relay.go` — 每个副本独立轮询 | ⚠️ 所有副本竞争同一 outbox 表，relay 通过 `MarkPublished` 防重复处理 → 功能正确但造成 N 倍无意义 DB 轮询 |
| GC sweeper | `internal/storage/gc/sweeper.go` — 每个副本独立运行 | ⚠️ 竞争条件：两个副本同时扫描到同一孤 blob 并发删除，一个成功一个失败（`ErrNotFound`安全）→ 功能无害但浪费 |

`internal/app/app.go:147-148` 的 SSO 冷启动热身是好意，但跨 5 副本滚动部署时，每个新副本独立热身，每个都穿透到 SSO：

```go
// SSO cold-start warmup: probe the SSO connection before serving real requests
probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
if err := a.sso.HealthCheck(probeCtx); err != nil {
    a.log.Warn("sso warmup probe failed ...")
}
```

而 SSE Hub 的问题最严重——它不是一个"性能优化"，而是一个**功能正确性**问题：用户连接断开后，重新连接可能落到不同实例，之前实例的 Hub 已经关闭，该用户没有任何方式收到离线期间的通知（除非通知投递有异步兜底，但当前 SSE 是唯一的主动推送路径）。

### 为什么需要

1. **可用性**: 多实例部署是生产高可用的基线要求。当前 SSE 问题意味着任何水平扩展都会破坏通知的实时性——这是面向用户产品的核心体验故障。
2. **成本**: 速率限制的 "N 倍放大" 使得生产限流形同虚设，运营者无法准确控制平台流量。
3. **可扩展性**: 缓存冷启动风暴限制了滚动部署的安全速度，迫使运营者在"快速部署"和"不压垮 SSO"之间做选择。

### 建议方向

| 优先级 | 组件 | 方案 | 复杂度 |
|--------|------|------|--------|
| P0 | SSE Hub | 粘滞路由（Sticky Session）+ 启动时广播反弹通知（re-connect → 回放 missed） | 中 |
| P1 | 速率限制 | 可选 Redis 集中令牌桶（`internal/pkg/ratelimit` 扩展 `Backend` 接口） | 中 |
| P1 | SSO 缓存 | 可选 Redis 集中缓存 + 本地 L1 缓存（两级缓存策略） | 中 |
| P2 | GC sweeper | Leader 选举（基于 Postgres 行锁或 advisory lock）确保仅一个副本运行 | 低 |

需要注意的是：不是所有部署都需要分布式协调。对于单实例/双实例的小规模部署，当前架构完全够用。但方向一的价值在于**识别当前为单实例设计的架构在哪里会出现功能性断裂**，让运营者知道何时需要介入。

---

## 方向三：Webhook 平台成熟度（Webhook Platform Maturity）

### 代码证据

`internal/outbox/webhook.go` 已经实现了基础的 outbox → HTTP webhook 传递路径：

```go
// internal/outbox/webhook.go:74
func (h *WebhookHandler) Handle(ctx context.Context, e *outbox.Event) error {
    // POST JSON body to cfg.URL with HMAC-SHA256 signature
    // Response status <200 || >=300 → error → relay retries
}
```

但当前实现处于 MVP 水平，缺失的平台级特征包括：

| 特征 | 代码状态 | 生产要求 |
|------|---------|---------|
| 按 endpoint 的投递指标 | ❌ 只有全局 outbox 仪表盘，无 per-endpoint 的成功/失败/延迟 | 无法监控特定 webhook 接收方是否健康 |
| 自动 suspend 故障 endpoint | ❌ 持续返回 5xx 的 endpoint 被无限重试直至 dead-letter | 浪费资源 + 延迟健康事件的处理 |
| 事件回放（Replay） | ❌ dead-letter 事件只能通过直接 DB 操作重试 | 运维痛点；接收方修复后无法回放丢失事件 |
| 接收方侧幂等（Idempotency-Key） | ❌ webhook payload 中不含幂等键或事件序列号 | 重试导致接收方重复处理（非幂等接收方会出数据问题） |
| endpoint 注册/管理 API | ❌ 仅通过静态配置，无 API 让接收方自行订阅/退订 | 运维摩擦；新增集成需重启服务 |
| Secret 轮换 | ❌ `cfg.SigningKey` 仅启动时读取，永不轮换 | 安全风险：签名 key 泄露后无法轮换 |
| 签名时间戳验证 | ❌ payload 中不含时间戳，接收方无法做重放攻击防护 | 安全风险：攻击者可重放旧事件 |

此外，webhook 配置的加载路径仅在 `internal/app/app.go:137-155`：

```go
if cfg.Outbox.Webhook.URL != "" {
    // 构建 WebhookHandler
    // 通过 outbox.Chain(dispatcher, webhookHandler) 串联
}
```

这是一个固定且唯一的 webhook 端点。不支持多端点、不支持每端点事件类型过滤、不支持每端点限速。

### 为什么需要

1. **平台扩展性**: Webhook 是 aero-id 作为"平台"而非"单服务"的基石——外部系统（审核管线、数据分析、CRM、AI 加工）通过 webhook 获得实时事件流。当前单端点架构限制了这一愿景。
2. **运营可靠性**: 没有 per-endpoint 的监控和自动保护，一个故障的接收方会拖慢整个 outbox relay（所有事件都要尝试推送给它），影响通知分发的实时性。
3. **安全合规**: 签名 key 无法轮换、缺少时间戳防护、没有事件序列号，这些问题在安全审计中会被标记为高风险。

### 建议方向

将单端点 webhook 升级为多端点的 Webhook 平台：

```
webhook 表（migrations/000023）:
  id, url, signing_key_hash, event_types[], 
  headers JSONB, enabled bool, suspend_count, last_suspend_at,
  rate_limit_rps, rate_limit_burst, created_at

每次投递:
  1. 查询匹配 event_type 的已启用 endpoint
  2. 每个 endpoint 独立限速 + 监控
  3. 连续失败 → 自动 suspend + 通知运营
  4. 成功 → 记录 delivery 记录 + 更新统计

管理 API:
  POST   /v1/admin/webhooks              — 注册 endpoint
  GET    /v1/admin/webhooks               — 列表 + 统计
  PUT    /v1/admin/webhooks/:id           — 更新配置
  DELETE /v1/admin/webhooks/:id           — 注销
  POST   /v1/admin/webhooks/:id/replay    — 手动回放 dead-letter 事件
```

---

## 方向四：文件生命周期策略引擎（File Lifecycle Policy Engine）

### 代码证据

当前文件生命周期管理只有两个操作——软删除和 GC 硬删除——缺乏面向用户和面向策略的完整管理能力：

| 生命周期阶段 | 当前实现 | 缺口 |
|-------------|---------|------|
| **创建** | ✅ 上传、去重、预签名直传 | — |
| **读取 / 分享** | ✅ 显式分享、公开/私有 | — |
| **更新** | ✅ 元数据 PATCH（重命名/可见性/meta） | ❌ 覆盖上传（同名文件）无版本关联 |
| **软删除** | ✅ `deleted_at` 时间戳 | ❌ 无用户可见回收站；软删不可逆（无 restore API）|
| **硬删除** | ✅ Storage GC sweeper（`internal/storage/gc/sweeper.go`） | ❌ 不可配置策略；所有文件统一 retention，不区分文件类型/大小/owner |
| **归档** | ❌ 完全缺失 | 大文件/旧文件只能原地保留或删除，无 'move to cold storage' |
| **过期** | ❌ 完全缺失 | 无"文件 30 天后自动删除"的 TTL 机制 |
| **版本** | ❌ 完全缺失 | 同文件名覆盖上传 → 新 UUID 行，无法追踪历史版本 |

`internal/storage/gc/sweeper.go` 的 GC 配置从 `configs/config.yaml` 读取：

```go
type Config struct {
    Retention    time.Duration  // 软删后保留多久才硬删
    MinOrphanAge time.Duration  // 孤儿对象最小年龄
    PurgeBatch   int            // 每批处理数
    SweepOrphans bool           // 是否扫描孤儿对象
}
```

这是**全局统一**的配置。没有：

- 按文件类型（图片 vs PDF）区分 retention
- 按文件大小（小文件 vs 大文件）区分
- 按 owner（免费用户 vs 付费用户）区分
- 按 visibility（公开 vs 私有）区分
- 硬删除前的通知或审批

`internal/file/service_crud.go` 的 `Delete` 方法：

```go
func (s *FileService) Delete(ctx context.Context, id domain.ID) error {
    // 校验 owner
    // 软删除文件行
    // 递减 storage_refs.ref_count，到零时删 blob
}
```

删除即最终操作。无 restore，无回收站，无"先移至回收站、N 天后才可清除"的 UX。

### 为什么需要

1. **存储成本管理**: 无归档/过期策略意味着所有文件永久保留在同一存储层。对于平台运营者，存储成本随用户增长线性膨胀，且无法差异化管理。
2. **用户体验**: 无回收站意味着"误删即永久丢失"，这是数据存储服务的最低体验要求——Dropbox/Google Drive/Gmail 都有回收站不是巧合。
3. **合规需求**: GDPR 的"数据最小化"原则要求平台不应保留超过必要期限的用户数据。无过期/生命周期策略使得合规审计无法通过。

### 建议方向

引入策略驱动的文件生命周期引擎：

```
策略模型:
  id                  UUID
  name                TEXT            — "免费用户文件 90 天归档"
  scope_uid           TEXT            — NULL=全局, specific uid
  scope_file_type     TEXT[]          — "image/png", "application/pdf"
  scope_file_size_min BIGINT
  scope_file_size_max BIGINT
  scope_visibility    SMALLINT[]      — 0=私有, 1=公开
  action              TEXT            — "archive" / "delete" / "notify"
  after_days          INT             — 距 created_at 或 deleted_at 的天数
  enabled             BOOLEAN
  created_at          TIMESTAMPTZ

执行引擎:
  每天运行一次策略扫描
  匹配条件 → 执行动作
  归档: 更新 storage_key → cold-storage key, 标记 files.storage_tier = 'cold'
  过期通知: 发送 outbox 事件通知用户"您的 N 个文件将在 X 天后被删除"
  删除: 软删除 + 回收站（30 天后 GC 硬删）
```

回收站 UX：
```
GET  /v1/files/trash       — 列出我的软删文件
POST /v1/files/:id/restore — 从回收站恢复
DELETE /v1/files/trash     — 清空回收站
GC 策略: 回收站中的文件 30 天后自动硬删
```

---

## 方向五：结构化查询可观测性与性能基座（Query Observability & Tuning Foundation）

### 代码证据

当前所有数据库查询经过 GORM 的默认 logger，无逐查询的追踪、延迟分布、或执行计划分析：

```go
// internal/app/app.go:67-73
gdb, err := db.Open(db.Options{
    DSN:             cfg.Database.DSN(),
    MaxOpenConns:    cfg.Database.MaxOpenConns,
    MaxIdleConns:    cfg.Database.MaxIdleConns,
    ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
    LogLevel:        cfg.Database.LogLevel, // 仅控制日志级别，不区分查询类型
})
```

调用栈中至少存在三类显著不同的查询模式：

| 查询类型 | 代码位置 | 特征 |
|---------|---------|------|
| 点查询（Point Lookup） | `Get file by ID`, `Get message by ID` | 主键索引，微秒级 |
| 分页列表（Cursor Scan） | `ListInbox`, `ListNotifications`, `ListFiles` | 复合索引，毫秒级，行数可控 |
| 全文搜索（FTS tsquery） | `internal/repository/search_integration_test.go` ← 在 repo 层跑 tsquery | GIN 索引扫描，可能需要几十毫秒甚至秒级 |
| 聚合查询 | `CountUnread`, `UnreadCount` | 索引扫描 + 计数，毫秒级 |

三种问题：
1. **无查询超时隔离**: 搜索查询（慢查询）和点查询（快速查询）共享同一个 `sql.DB` 连接池。一个失控的 tsquery（例如 `query: "a"` 命中数万行）可以占满连接池、阻塞所有其他请求。
2. **无慢查询追踪**: GORM 日志只有 `LogLevel` 控制（`Warn`/`Info` 等），没有"超过 N 毫秒的查询自动记录执行计划"的能力。
3. **无串行化冲突重试**: PostgreSQL 在 `REPEATABLE READ` 或 `SERIALIZABLE` 隔离级别下会返回 `40001` 串行化冲突。当前代码（无显式事务隔离级别设置）使用默认的 `READ COMMITTED`，对于 outbox 写入等竞争操作存在竞态条件风险。

以 search 为例——`migrations/000016_fts_indexes.up.sql` 创建了 tsvector 列和 GIN 索引：

```sql
ALTER TABLE messages ADD COLUMN search_vector tsvector GENERATED ALWAYS AS (...);
CREATE INDEX idx_messages_search ON messages USING GIN(search_vector);
```

但查询代码（`internal/repository/message.go` → `SearchMessages`）未做：
- 查询超时上下文（`context.WithTimeout`）
- 最小查询长度限制（`query: "a"` 触发全表 GIN 扫描）
- 结果评分排序（`ts_rank`）

```go
// 推测的搜索实现（已在 service/search.go 中聚合，repo 层未展示）:
func (r *MessageRepository) SearchMessages(ctx context.Context, actor, query string, limit int) ([]*domain.Message, error) {
    // WHERE search_vector @@ plainto_tsquery('simple', ?)
    // 无排名、无超时、无限长
}
```

### 为什么需要

1. **生产可靠性**: 一个用户输入 `q=a` 的搜索请求可以拖慢整个数据库连接池，影响所有其他用户的正常操作——这是生产级服务的红线问题。
2. **性能可预测性**: 没有逐查询的 OTel span 或延迟记录，无法回答"搜索在第 99 百分位有多慢"、"哪个查询消耗了最多的数据库时间"等运维基本问题。
3. **扩展性**: 没有只读副本的感知能力，所有查询（包括搜索这类重查询）都打到写主库，浪费了 PG 的流复制能力。

### 建议方向

| 层 | 改进 | 优先级 |
|----|------|--------|
| GORM 插件 | 添加慢查询追踪：超过阈值的查询自动记录 + OTel span | P0 |
| 搜索包 | 添加 `context.WithTimeout` + 最小查询长度 + `ts_rank` 排序 | P0 |
| 只读副本 | DB 配置新增 `read_dsn`，读查询导向只读连接池 | P1 |
| 查询分析 | `/metrics` 增加 `aero_db_query_duration_seconds` 直方图，按 query_type label 区分 | P1 |

具体代码层面最小侵入改动：

```go
// 为数据库打开添加两个连接池（写 + 读），读池指向只读副本
type DBPools struct {
    Write *gorm.DB
    Read  *gorm.DB  // 可选的独立只读连接池
}

// 对所有列表/搜索类查询加入上下文超时
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()

// GORM 慢查询钩子
gdb.Callback().Query().Before("*").Register("observe:query_start", func(db *gorm.DB) {
    // 记录开始时间
})
gdb.Callback().Query().After("*").Register("observe:query_done", func(db *gorm.DB) {
    // if duration > threshold → log + record metric + optionally log EXPLAIN ANALYZE
})
```

---

## 总结

| # | 方向 | 类别 | 代码证据强度 | 为什么高价值 |
|---|------|------|-------------|-------------|
| 1 | 文件内容安全管线 | 安全 / 合规 | 强：上传管线零安全检测 | 法律合规基线；防止平台成为恶意软件分发渠道 |
| 2 | 多实例部署架构 | 基础设施 | 强：所有运行时状态为单进程内内存 | 多副本部署时 SSE 功能断裂、限流失效、缓存冷启动风暴 |
| 3 | Webhook 平台成熟度 | 平台化 | 强：当前是单端点 MVP | 外部系统集成的核心支柱；监控/回放/管理 API 缺位 |
| 4 | 文件生命周期策略引擎 | 产品成本 | 强：回收站/归档/过期/TTL 完全缺失 | 存储成本管理 + 用户误删保护 + GDPR 合规 |
| 5 | 结构化查询可观测性与性能基座 | 运维 | 强：查询无超时/无隔离/无区分 | 防止慢查询拖垮全库；为搜索和扩展做基础设施 |

这五个方向覆盖安全合规（1）、基础设施扩展（2）、平台化（3）、产品体验（4）、运维成熟度（5）——构成了从 MVP 到生产级可扩展平台的全景路线图。
