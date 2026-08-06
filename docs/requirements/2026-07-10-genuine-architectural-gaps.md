# aero-id — 资深架构师全局扫描：5 个高价值未覆盖扩展方向

> **日期**: 2026-07-10
> **方法**: 全量 Go 源码扫描（`internal/` 22 个包，120+ 源文件）、`migrations/` 22 份 SQL、双传输层 handler、composition root `app.go`、config，与 `docs/requirements/` 下既有 42 份分析文档（累计 ~80+ 方向标题）逐条比对，确保以下方向**未被系统性覆盖**。
> **每项提供**：代码级证据（精确到文件名+行号范围）、产品/架构价值、边界条件。

---

## 前置：当前成熟度

aero-id 已交付远超典型 MVP 的能力。`make test && make lint` 全绿。既有分析文档覆盖了群聊、文件版本控制、通知扩展、推送通道、搜索中文分词、配额管理、限流、角色系统、Webhook 平台化、多实例 SSE 协调、离线同步协议、媒体处理管线等领域。**本文刻意避开这些已有方向**，聚焦于当前架构下的 **结构性盲点**。

---

## 方向一：异步管道的追踪连续性断裂（Outbox Trace Context Propagation Gap）

### 为什么需要它

当前系统有两类请求路径：

1. **同步路径**（HTTP/gRPC handler → service → repository/SSO）：OpenTelemetry 已接入，`middleware/tracing.go` 为每个请求创建根 span，span 上下文通过 `context.Context` 传递到 SSO gRPC 调用（`sso/client.go` 中的 `c.tracer.Start(ctx, "sso.ValidateToken")`），形成端到端 Trace。

2. **异步路径**（outbox relay → notification dispatcher → SSE push / webhook）：**完全断裂**。当 relay 的 `drain()` 在后台 goroutine 中处理事件时，使用的是 `context.Background()`，没有任何 trace 上下文可以继承。

这意味着：

| 场景 | 追踪现状 |
|------|---------|
| 用户发起 `POST /v1/messages` → relay 创建通知 → SSE 推送 | HTTP 请求有完整 trace ✅，但通知创建 + SSE 推送发生在**无 trace 的 goroutine** ❌ |
| Webhook 调用失败 | 无法追溯到原始请求 → DevOps 无法诊断"是哪个请求导致了这个 webhook 失败" |
| 通知创建延迟过高的根因分析 | 只有 relay drain 耗时（`ObserveOutboxDrain` 指标），没有完整的因果链 |

### 代码级证据

**证据 1：relay drain 使用 `context.Background()`**

```go
// internal/outbox/relay.go:~130-140
func (r *Relay) Start(interval time.Duration) (stop func()) {
    // ...
    go func() {
        ctx := context.Background()  // ← 纯根上下文，无 trace 信息
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            // ...
```

每次 `drain()` 调用都丢失了与原始请求的关联。即便查看 handler 错误的日志，也无法确认哪个上游请求触发了该事件。

**证据 2：NotificationDispatcher 同样丢失上下文**

```go
// internal/service/notification.go (old dispatcher, ~line 150-170)
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // ctx 来自 relay 的 context.Background(), 不是来自原始请求
    // 创建通知行、SSE 推送、webhook 调用都发生在无 trace 的上下文中
}
```

**证据 3：SSE push 无 trace**

```go
// internal/hub/adapter.go:~12-14
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
    h.pub(uid, SSEEvent{...})  // ← 纯数据推送，无 trace 传播
}
```

### 产品/架构价值

- **可诊断性**：当通知延迟 > 5s 时，Ops 能回答"是哪个请求触发的？SSO 调用花了多久？是哪一步慢？"
- **SLO 可测量**：从"用户操作"到"通知送达"的端到端延迟可追踪，可以定义真实 SLO
- **容量规划**：Trace 能揭示异步管道的瓶颈——是 relay backoff 导致延迟，还是 dispatcher 调用 SSO 慢？

### 建议

在 outbox 事件中携带序列化的 trace 上下文（W3C Trace Context `traceparent`），relay 在反序列化事件后，将恢复的 span 作为异步 span 挂接到原 trace 上：

```
[HTTP POST /v1/messages]──┬──[sso.ValidateToken]
                          └──[outbox.Append (写入事件 + traceparent)]
                               ↓  relay tick
                              [deserialize traceparent]
                               └──[notification.Create] (子 span, 挂回原 trace)
                                   └──[sse.Publish] (子 span)
```

规模估算：中（修改 outbox event payload schema + relay 反序列化 + dispatcher 创建子 span）。约 3 个文件，80-120 行新代码。

---

## 方向二：跨实例最终一致性下的 Read-Your-Writes 缺口

### 为什么需要它

当前系统的数据一致性模型是隐式的——**假设单实例部署**。但生产环境必定是多副本：
```
Client A: POST /v1/messages (→ Instance 1, 写成功)
Client A: GET /v1/conversations/:uid (→ Instance 2, 读——可能看不到刚发的消息)
```

具体问题：

1. **Cursor 分页的不一致**：`LIST WHERE id < cursor ORDER BY id DESC LIMIT n+1`。新创建的消息 id 是 UUIDv7（单调递增），如果副本 2 的 Postgres 复制延迟高于预期，新消息可能出现在**前一页**（因为 id 更大，cursor 无法包含它），导致客户端"翻页返回时发现新消息出现在已读区域"。

2. **写后立即读**：用户 A 发送消息后立即查询 inbox 或 unread_count。如果请求被路由到未同步的副本，看不到自己刚发的消息，UI 会显示"发送成功"但列表为空——UX 断层。

3. **幂等键的副本问题**：`idempotency_keys` 表在 Postgres 中，依赖主库的一致性。但如果写请求打到主库、重试请求打到只读副本，重试不会被去重。

### 代码级证据

**证据 1：inbox 查询无一致性保证**

```go
// internal/repository/message.go (ListInbox)
// WHERE to_uid = ? ORDER BY created_at DESC, id DESC LIMIT n+1
// 无任何一致性提示（如 SELECT ... FOR UPDATE），依赖 Postgres 的默认读已提交
```

**证据 2：unread_count 无一致性保证**

```go
// internal/repository/message.go (CountUnread)
// SELECT count(*) FROM messages WHERE to_uid = ? AND deleted_at IS NULL AND read_at IS NULL
// 索引查询，无事务包裹，无一致性提示
```

**证据 3：文件上传验证后的立即读取**

```go
// internal/file/service_upload.go (~line 40-60)
// CompleteUpload 写入文件行后，返回 File 对象
// 调用方可能立即尝试 GET /v1/files/:id —— 在副本延迟下可能 404
```

### 边界条件

| 场景 | 当前行为 | 理想行为 |
|------|---------|---------|
| 发送消息后立即读 inbox | 可能看不到新消息 | 读主库或等待副本同步 |
| 评论后立即读列表 | 新评论可能不出现 | 同上 |
| 上传文件后立即读 metadata | 可能 404 | 同上 |
| 幂等键重复请求到不同副本 | 可能执行两次 | 必须读主库 |

### 建议

**短期**（低投入）：在需要写后立即读的请求头中加入 `X-Aero-Read-After-Write: <last_written_id>`，中间件将请求粘滞到主库连接。

**长期**（高投入）：引入请求级别的"一致性标记"——客户端在写操作响应中获得一个 `write_token`（写入时间戳），后续读请求携带此 token，数据库层确保返回 `updated_at >= write_token` 的数据，否则阻塞等待或路由到主库。

规模估算：短期方案低投入（中间件 + 读库路由），长期方案中高投入（一致性标记层）。

---

## 方向三：用户数据出口——结构化数据导出与级联删除（隐私合规）

### 为什么需要它

aero-id 是"管理用户数据"的服务——文件、消息、评论都是**用户产生的内容**。但当前系统不存在以下能力：

| 需求 | 当前状态 | 合规驱动 |
|------|---------|---------|
| **用户数据导出**（GDPR Art. 20） | 无：用户无法一次性导出所有文件 + 消息 + 评论 | GDPR 数据可移植权 |
| **账户删除级联清除** | 无：不支持"某个 SSO uid 被删除后，其所有内容自动清理" | GDPR 被遗忘权 |
| **数据分类与最小化** | 无：没有区分"必需数据"（协作消息）和"非必需数据"（点赞历史） | 隐私设计原则 |
| **匿名化替代删除** | 无：评论和消息删除后，`from_uid`/`owner_uid` 仍保留在数据库中 | 数据最小化 |

虽然 Aero-id 本身没有账户表（ACL 委托给 SSO），但**用户数据**（文件、消息、评论）是 aero-id 负责管理的。SSO 删除账户后，aero-id 需要清理该 uid 下全部数据——当前完全无机制。

### 代码级证据

**证据 1：所有实体按 owner_uid 存储，但无批量删除入口**

```go
// internal/repository/file.go — 无 DeleteByOwner 方法
// internal/repository/message.go — 无 DeleteByUID 方法
// internal/repository/comment.go — 无 DeleteByOwner 方法
```

**证据 2：软删除是逐条进行的**

```go
// internal/service/message.go — Delete 要求 id 参数
func (s *messageService) Delete(ctx context.Context, id domain.ID) error {
    // → 检查 actor 是发送者 → 软删除单条消息
    // 无 "批量删除该用户所有消息" 的方法
}
```

**证据 3：存储没有"用户所有 blob"的索引**

文件 blob 按 `owner_uid` + `storage_key` 存储。删除用户所有文件需要：
1. 查询该用户所有文件行
2. 对每个 storage_key 检查引用计数
3. 删除 blob（仅当引用计数到零）
4. 硬删除所有文件行

当前没有事务层面保证这个流程的原子性。

**证据 4：审计日志中记录了操作但无法跟随 uid 删除**

```go
// internal/audit/repository.go — Log 方法只能追加，无批量清除
```

### 边界条件

| 场景 | 复杂度 |
|------|-------|
| 同一 storage_key 被多个用户引用（内容去重） | 需要引用计数 → 只有引用归零才删除 blob |
| 消息涉及双方（from_uid 和 to_uid） | 一方删除账户后，另一方仍应看到历史消息，但 from_uid 应变为 "deleted_user" |
| 评论涉及回复树 | 删除根评论可能导致回复链断裂：应保留骨架（置空内容但保留结构） |
| 通知引用已删除的用户 | 通知中的 ActorUID 应显示为 "已注销用户" 而非直接删除行 |
| SSO 通知 vs 用户主动请求 | 需要区分"SSO 通知 uid 已删除"和"用户通过 API 主动请求导出/删除" |

### 建议

设计一个 `DataPortabilityService`：

1. **数据导出**：`POST /v1/data/export` → 异步生成包含用户所有文件（presigned URL）、消息（JSON）、评论（JSON）的压缩包 → 通知用户下载链接（24h 过期）
2. **账户删除**：`POST /v1/data/delete` → 级联处理（利用 outbox 事件驱动）：
   - 文件：引用计数减一 → 归零则删除 blob + 硬删除行
   - 消息：保留行但 `from_uid` 匿名化 → `content` 保留（对方仍需看到）→ 不删除
   - 评论：同上（保留骨架）
   - 通知：保留行但 `actor_uid` 匿名化
3. **SSO webhook 订阅**：SSO 侧通知 uid 被删除 → aero-id 自动触发级联清理

规模估算：中高。核心逻辑在 service 层，不依赖新基础设施——但匿名化策略涉及领域模型变更，需要 migration 新增 `deleted_uid` 标记或软删除策略的重构。

---

## 方向四：存储后端的混合架构与跨区域数据放置

### 为什么需要它

当前存储模型是**单后端**——启动时选择 `local` 或 `s3`，运行时不可变。此设计存在三个结构性限制：

1. **无法按策略放置数据**：所有文件使用同一个存储后端。无法做到 "小型文件（<1MB）存本地 SSD 以降低延迟，大型文件存 S3 以降低成本"。

2. **无跨区域读优化**：上海用户上传的文件，纽约用户访问时仍需从上海 S3 桶读取（如果 bucket 在上海），或从本地存储读取（如果部署在上海）。没有基于地理位置的路由或 CDN 集成。

3. **无存储迁移能力**：从 `local` 切换到 `s3`（或反之）需要停机维护和数据搬运。当前 GC sweeper 只做清理，不做迁移。

### 代码级证据

**证据 1：存储后端在启动时确定，运行时不可变**

```go
// internal/app/app.go:~350-370
func buildStorage(ctx context.Context, cfg *config.Config) (storage.FileStorage, httpapi.SignedDownloader, error) {
    switch cfg.Storage.Type {
    case "local":
        return local, local, nil
    case "s3":
        return s3, nil, nil
    default:
        return nil, nil, fmt.Errorf("unknown storage type %q", cfg.Storage.Type)
    }
    // ← 运行时无法切换、无法按文件选择
}
```

**证据 2：FileStorage 接口是单后端抽象的**

```go
// internal/storage/storage.go
type FileStorage interface {
    Put(ctx, ownerUID, name string, reader io.Reader) (key string, size int64, err error)
    Get(ctx, key string) (io.ReadCloser, error)
    Delete(ctx, key string) error
    // 无 "GetURL(ctx, key, opts)" 方法 —— 无法在运行时选择 CDN 端点
}
```

**证据 3：下载 URL 生成与存储后端绑定**

```go
// internal/service/file_presign.go
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
    // → 调用 s.store.PresignGet(ctx, key, ttl)
    // → local 后端返回 HMAC 签名的本地 URL
    // → s3 后端返回 presigned S3 URL
    // 无法根据客户端 IP 选择就近端点
}
```

**证据 4：File 领域模型只有一个 storage_type**

```go
// internal/domain/file.go
type File struct {
    StorageKey  string    // 存的是 blob 的相对路径
    StorageType string    // "local" 或 "s3" —— 运行时唯一判断后端的方式
    // 无 region、无 bucket、无 CDN 相关字段
}
```

### 边界条件

| 场景 | 当前问题 |
|------|---------|
| 同一文件在不同地域有副本 | 不支持：一个 storage_key 只对应一个物理位置 |
| 存储后端切换期间的读写 | 正在迁移的文件可能在新/旧后端都不可用 |
| 多后端引用计数 | GC sweeper 只能访问当前后端，无法清理迁移后的旧端孤儿 blob |
| 按文件大小做策略 | 需要上传感知，在 service 层判断：文件 > 10MB → S3，否则 local |

### 建议

**Phase 1 — 多后端路由器**（低投入）：引入 `storage.Router` 实现 `FileStorage` 接口，内部持有多个 `local`/`s3` 实例，根据文件大小/MIME 类型/用户属性路由到不同后端。对上层透明。

**Phase 2 — 存储迁移 API**（中投入）：`POST /v1/admin/storage/migrate` 触发后台迁移任务，将 `storage_type = "local"` 的 blob 逐步搬运到 S3，完成后更新 `storage_type` 和 `storage_key`，利用 outbox 事件触发增量迁移。

**Phase 3 — CDN 集成**（依赖 Phase 1）：`FileStorage` 新增 `ServeURL(ctx, key, opts)` 返回 CDN 端点 URL，客户端 IP 就近选择。

---

## 方向五：API 变更兼容性生命周期（Deprecation & Sunset Protocol）

### 为什么需要它

当前 proto 定义了 `aero_id.v1` 包，所有 HTTP 路由挂载在 `/v1` 下。但随着 proto 和 API 的演化，以下问题必然出现：

1. **字段废弃**：`File` message 的某个字段不再有意义。删除它会导致现有 gRPC 客户端反序列化失败（protobuf 严格模式）或静默丢失数据（宽松模式）。

2. **RPC 重命名/删除**：`FileService.ListFiles` 改为 `FileService.SearchFiles`。老客户端调用 ListFiles 直接返回 `Unimplemented`——无迁移窗口。

3. **HTTP 路由重命名**：`PUT /v1/files/:id/metadata` 改为 `PATCH /v1/files/:id`。老客户端得到 404，无指引。

4. **行为变更**：`ListComments` 默认排序从 `created_at DESC` 改为 `created_at ASC`。下游系统静默受影响。

### 代码级证据

**证据 1：无任何 Deprecation 元数据**

```go
// api/proto/aero_id/v1/aero_id.proto
// 无 google.api.field_behavior 注释
// 无 deprecated 选项
// 无版本过渡 RPC（如 ListCommentsV2）
message File {
    string id = 1;
    string owner_uid = 2;
    // ...
}
```

**证据 2：HTTP handler 无 Sunset 头**

```go
// internal/handler/http/file.go — 无 Deprecation/Sunset 响应头
func (h *FileHandler) List(c *gin.Context) {
    // ...
    httpx.OK(c, gin.H{"files": files, "next_cursor": next})
    // 不返回 Deprecation: true
    // 不返回 Sunset: Sat, 10 Jan 2027 00:00:00 GMT
}
```

**证据 3：proto 版本号硬编码在 URL 和 package 名中**

```
// proto package = aero_id.v1
// HTTP prefix = /v1/
// gRPC service = FileService (不是 FileServiceV1)
// ← 无法优雅地同时运行 v1 和 v2
```

**证据 4：无 gRPC 版本化 RPC**

```go
// internal/handler/grpc/server.go
// RegisterFileServer 直接将 service 注册为 FileService
// ← 不支持 FileServiceV1 + FileServiceV2 共存
```

### 边界条件

| 场景 | 当前结果 | 理想行为 |
|------|---------|---------|
| 废弃字段 `files.visibility` | 直接删除 proto 字段 → 旧客户端崩溃 | 先标记 `deprecated`，12 个月后删除 |
| 替换 `ListComments` 为新分页 | 无法同时部署新旧 RPC | 注册 `CommentService` + `CommentServiceV2` 共存 |
| 删除 HTTP 路由 | 客户端看到 404/LB 报错 | 返回 `410 Gone` + `Sunset` 头 + 迁移指引 |
| 修改请求参数语义 | 无感知变更 → 下游静默异常 | 新 endpoint + 旧 endpoint 返回迁移警告 |

### 建议

建立 API 生命周期管理规范：

1. **Proto 层面**：使用 `google.api.field_behavior` 标记废弃字段，`deprecated = true` 标记废弃 RPC。

2. **gRPC 层面**：新 endpoint 注册为 `FileServiceV2`，保留 `FileService` 作为兼容桥接（内部调用 v2 实现）。

3. **HTTP 层面**：新路由挂载在 `/v2/` 下。旧路由自动注入响应头：
   - `Deprecation: true`
   - `Sunset: Sat, 10 Jan 2027 00:00:00 GMT`
   - `Link: </v2/files>; rel="successor-version"`

4. **监控层面**：废弃 endpoint 的调用次数和调用方（uid/app）记录到日志/指标，当调用量归零时安全下线。

规模估算：中。主要是规范建立 + 中间件层面的 Deprecation 头注入 + proto 注释规范。不需要大的架构变更，但需要工程纪律。

---

## 总结：五个方向的权衡对比

| # | 方向 | 价值 | 投入 | 紧迫性 | 类型 |
|---|------|------|------|--------|------|
| 1 | 异步管道追踪连续性 | 诊断能力提升 | 中 | 上线前 | 可观测性 |
| 2 | Read-Your-Writes 一致性 | 避坑（多副本） | 低-中 | 多副本部署前 | 正确性 |
| 3 | 数据导出与级联删除 | 合规（GDPR） | 中-高 | 欧盟用户前 | 合规 |
| 4 | 多后端混合存储 | 成本优化 | 高 | 规模化后 | 成本/性能 |
| 5 | API 生命周期管理 | 兼容性保障 | 中 | 首个 breaking change 前 | 演进能力 |

> **建议启动顺序**：方向 1（追踪）→ 方向 2（一致性）→ 方向 5（API 生命周期），三者互不依赖且投入可控，可在 1-2 个迭代周期内完成。方向 3 和 4 需更长的跨迭代规划。
