# aero-id — 架构级扩展方向（2026-07-11）

> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码扫描 `internal/` 全部 ~180 个 Go 源文件（23 个子包）、`migrations/` 23 份 SQL、`api/proto/`、`configs/`、`cmd/` 及 `docs/` 目录。基于当前 `main` 分支的实际代码状态。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和双视角论证。  

---

## 当前成熟度小结

aero-id 已经完成了一个生产级服务平台所需的绝大部分能力：

| 域 | 已交付 |
|----|--------|
| **基础设施** | HTTP REST + gRPC 双协议（38+ RPCs）、GORM + PG16、本地/S3 双存储后端、OTel 链路追踪、RED 指标、per-dependency `/readyz`、冷启动 warmup、优雅关停 |
| **可靠性** | 事务性 Outbox（含指数退避 + 死信）、幂等键（持久化 + 自动清理）、per-actor 速率限制（双传输）、SSO 断路器 + stale-cache + singleflight + 重试退避 + 证书固定 |
| **文件** | 流上传、MIME/size 校验、SHA-256 内容去重（per-owner）、S3 presigned 直传 + 原子完成、显式分享/撤销、引用计数 GC 扫除、元数据 PATCH、软删除 |
| **消息** | 1:1 私信（含系统消息）、Reactions、编辑/软删、已读回执 + 计数、Inbox/Conversation 视图、Block 拦截、事务性 outbox 事件 |
| **评论** | 两层嵌套（楼中楼）、Like/Unlike（原子计数器）、举报→审核队列 + 解决/驳回、Hide/Unhide、编辑 |
| **通知** | Outbox 驱动的异步创建 + 折叠去重（5min 窗口）、SSE 实时推送（last_event_id 回放）、偏好设置（启用/静默时段/时区）、Block 抑制、每类型速率限制、Webhook 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引）、CJK ILIKE 回退、多 kind（messages/comments/files）联合搜索 |
| **安全** | 方向性 Block、审计日志（文件分享/消息删除/举报处理等）、Admin 角色 + 权限体系（moderator/admin） |
| **数据模型** | 23 次可逆迁移（含 FTS 索引、性能索引、通知折叠、Outbox 重试、Storage Ref、Admin 角色） |

---

## 方向一：存储分层与数据生命周期管理（Storage Tiering & Data Lifecycle）

### 现状

文件存储只有一层：文件上传后永久保存在配置的 backend（local 或 S3）上，没有任何自动迁移或过期规则。`File` 模型包含 `created_at`、`updated_at`、`size`、`mime_type` 等字段，但没有一个字段指示文件处于哪个存储层或何时应被归档。

**代码证据 1：File 模型无生命周期字段**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string       // "local" | "s3" — 创建时确定，终生不变
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
    // 没有 StorageTier、ArchivedAt、LifecycleRule 等字段
}
```

`StorageType` 在创建时确定且永不改变。如果一个文件原本存储在 `local` backend，没有任何机制能将其迁移到 S3（或反之）。

**证据 2：Storage 接口无迁移/复制能力**

```go
// internal/storage/storage.go
type FileStorage interface {
    Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
    PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
    NewKey(ownerUID string) string
    // 没有 Copy/Move/ListAcrossBackends 方法
}
```

没有 `Copy`、`Move` 或跨后端复制方法，因此无法在存储层之间迁移文件。

**证据 3：GC 扫除只做删除，不做归档**

```go
// internal/storage/gc/sweeper.go — 核心逻辑
// purgeSoftDeleted: 硬删除过期行 + 删除无引用的 blob
// sweepOrphans:    删除无对应行的孤儿 blob
// 没有任何"将旧文件迁移到冷存储"的逻辑
```

**证据 4：config 无存储分层配置**

```go
// internal/config/config.go
type StorageConfig struct {
    Type             string              // "local" | "s3" — 单一值
    MaxUploadBytes   int64
    AllowedMimeTypes []string
    Local            LocalStorageConfig
    S3               S3StorageConfig
    GC               StorageGCConfig
    // 没有 TieredStorage、WarmStorage、ColdStorage 等配置
}
```

### 为什么需要

**从架构师视角**：一个"数据归账户所有"的服务，随着时间推移将积累海量文件。如果不做存储分层：

- 所有文件都保存在同一性能/成本层，不经济。90 天前的文件访问频率急剧下降，应该自动迁移到更便宜的存储。
- 无法满足数据合规需求（GDPR 的"被遗忘权"要求在一定时间后删除非活动数据）。
- `local` backend 的磁盘空间是有限的——没有自动迁移到 S3/归档存储的机制，磁盘填满将导致服务不可用。
- 纯生产的 S3 费用也会随着用户量级增长而失控——需要生命周期规则。

**从产品经理视角**：

- 用户无法看到"我的存储使用量"或"哪些文件占用了最多空间"。
- 没有自动归档——用户必须手动删除旧文件来释放空间（如果未来有配额限制）。
- 这是一个用户可见的信任信号："你的旧文件被安全归档了，而不是在热存储上浪费钱。"
- 合规团队需要数据生命周期策略（自动删除 N 年以上的数据）来满足法规要求。

### 边界情况与设计考量

1. **迁移期间的一致性**：从 warm 迁移到 cold 期间，如果用户恰好在迁移窗口内访问文件，需要有一个"重定向到新位置"或"同步等待迁移完成"的策略。
2. **StorageType 的语义扩展**：当前 `File.StorageType` 是字符串（"local"|"s3"），可以变为"存储层"概念（"hot"|"cold"|"archive"），每个层映射到不同的物理 backend。
3. **最小访问频率阈值**：需要定义"多久没有访问算冷数据"。当前没有 `last_accessed_at` 字段——需要新增。
4. **生命周期策略的可配置性**：不同租户/文件类型可能需要不同的策略。`File.Meta` JSONB 字段可以存储策略 override。
5. **GC 扫除的优先级**：如果 GC sweep 和生命周期迁移同时运行，需要协调——确保迁移不会在 GC 之前触发。

### 实现思路

1. 扩展 `Storage` 接口，增加 `Copy(ctx, srcKey, dstStorage, dstKey)` 方法。
2. 新增 `storage_tier` 枚举（hot / cold / archive）和 `last_accessed_at` 字段到 `files` 表。
3. 新增 `storage_lifecycle_rules` 表或配置，定义"文件超过 X 天未访问 → 迁移到 cold"等规则。
4. 新增后台 worker（类似 outbox relay），定期扫描满足迁移条件的文件并执行跨后端迁移。
5. `/readyz` 扩展，加入每个存储层的健康探测。

---

## 方向二：媒体资源处理管线（Media Pipeline — Thumbnails, Previews & Transforms）

### 现状

文件上传时只做存储，不做任何后处理。无论文件类型是图片、文档还是视频，系统不生成缩略图、预览或转码产物。

**代码证据 1：服务层 Upload 不做任何后处理**

```go
// internal/file/service_crud.go — Create 核心流程
// 1. 验证 name/size/mime
// 2. store.Put(ctx, actor, name, reader)   ← 直接存储原始字节
// 3. hasher 计算 sha256
// 4. files.Create(ctx, f)                   ← 只写 file 行
// 没有任何 "触发异步处理" 的步骤
```

文件数据流是纯线性的：上传 → 校验 → 存储 → 返回。没有 outbox 事件表示"文件已上传，请处理后处理"。

**证据 2：没有异步处理管线触发**

```go
// internal/file/service_crud.go (search for outbox events or post-processing)
// 在 Create 方法中没有任何 outbox.NewEvent 调用
// 没有 PipelineJob、ProcessingStatus、ThumbnailReady 等概念
```

对比消息和评论的 outbox 事件（它们会发出 `message.sent` 和 `comment.replied` 事件），文件创建路径完全不产生任何事件。这意味着不能通过 outbox relay 来驱动后处理。

**证据 3：File 模型没有处理状态字段**

```go
// internal/domain/file.go
type File struct {
    // 没有 ProcessingStatus（enum: pending/processing/ready/failed）
    // 没有 ThumbnailKey、PreviewKey、ThumbnailSizes 等字段
    // 没有 ProcessedAt 时间戳
}
```

### 为什么需要

**从产品经理视角**：

- 文件列表现在只能显示文件名和 MIME 类型。对于图片类应用（头像、相册、商品图片），没有缩略图意味着每个列表请求都要下载完整文件——带宽和加载时间不可接受。
- 消息中的图片附件没有 inline 预览——接收方只能下载文件查看。
- 视频/音频文件无法生成封面图或波形预览。
- 文档（PDF、Word）无法生成首屏预览图——用户无法在下载前知道文件内容。
- 这是任何一个文件存储服务最基础的产品能力。

**从架构师视角**：

- 当前缺少的就是 outbox 机制诞生时提到的第一个异步业务场景（见 `docs/specs/aero-id-design.md` 11.2 Outbox 事件流）："当出现首个异步业务（如文件转码、敏感词审核）时启用"。
- outbox 基础设施已经完全就位——只差事件产生 + handler 处理。
- 后处理管线是"可插拔的"：可以用本地工具（ffmpeg、ImageMagick）或外部服务（AWS Lambda、Cloudinary）。
- 处理结果应该缓存且可过期——需要有缓存淘汰策略，不能无限堆积衍生文件。

### 边界情况与设计考量

1. **处理超时与重试**：转码可能耗时很长（大视频）。需要合理的超时 + outbox 重试机制。
2. **存储成本**：每个文件可能产生 N 个衍生文件（多种尺寸的缩略图）。需要计算并使用 `storage_refs` 表来计数引用。
3. **异步失败通知**：转码失败时（格式不支持、损坏文件），需要一种方式通知上传者。
4. **渐进式处理**：可以先做图片缩略图（低成本高收益），再做视频和文档（成本更高）。
5. **MIME 类型过滤器**：只对可处理的 MIME 类型触发后处理（image/* → 缩略图，video/* → 封面帧，application/pdf → 首屏预览）。

### 实现思路

1. 在 `file.Create` 和 `file.CompleteUpload` 中加入 outbox 事件（`file.process`）。
2. 新增 `internal/processing/` 包，包含 Pipeline 接口（接口 → 实现分离）。
3. Pipeline handler 注册到 outbox relay chain（类似 webhook handler 的 chain 模式）。
4. 衍生文件（缩略图等）也存入 FileStorage，用 `File.ThumbnailKey` 等字段 `storage_refs` 表跟踪。
5. 下载 endpoint 可接受 `?size=thumbnail` query 参数来获取缩略图。

---

## 方向三：账户数据使用量计量与资源配额（Resource Metering & Quota Enforcement）

### 现状

系统对用户行为做了速率限制（per-actor RPS），但对资源消耗（存储空间、消息数量、带宽）没有任何计量或配额限制。

**代码证据 1：没有存储/消息用量查询**

```go
// internal/domain/file.go 和 internal/domain/message.go
// 没有 TotalStorageUsed、MessageCount、BandwidthUsage 等值对象
// internal/repository/file.go — Repository 接口
// 没有 CountByOwner、SumSizeByOwner、GetUsage 等方法
```

**代码证据 2：文件上传只做 size 检查，不做累积检查**

```go
// internal/file/service_crud.go
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    // ...
    if s.maxBytes > 0 && in.Size > s.maxBytes {   // ← 只检查单文件上限
        return nil, apperr.BadRequest(...)
    }
    // 没有检查 "此用户已使用的总存储 + 新文件大小 > 配额"
}
```

**证据 3：消息发送没有累计限制**

```go
// internal/message/service.go
func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
    // 只验证字段合法性 + block 检查
    // 没有 "此用户今天已发送 X 条消息 > 每日限额" 的检查
}
```

**证据 4：下载带宽无法追踪**

```go
// internal/handler/http/file.go — Download 和 DownloadURL handler
// 没有任何带宽计量逻辑
// internal/storage/storage.go — FileStorage 接口
// Put/Get 方法没有记录流量
```

### 为什么需要

**从产品经理视角**：

- 没有配额 = 无法限制免费用户。如果产品要走向 SaaS 模式（免费层 + 专业层 + 企业层），资源计量是最基础的底层能力。
- 无法防止单个用户耗尽共享存储。一个用户上传 100GB 视频会挤占所有其他用户的存储空间（在 local 模式下尤其危险）。
- 用户无法知道自己的使用情况——没有仪表盘、没有用量告警、没有"存储已用 80%"的提示。
- GDPR/合规：需要知道用户的数据量以响应数据可移植性请求。

**从架构师视角**：

- 配额是 SaaS 多租户的基础。当前没有 `org_id`，但即使是单用户场景，资源上限也是运维必需品。
- 没有计量 → 无法做账单 → 无法变现。
- 存储 GC 只能回收已软删除的文件，不能限制总用量。纯靠 GC 兜底，磁盘或 S3 bucket 会无限增长。
- 速率限制（RPS）只控制请求频率，不控制请求"重量"。一次 1GB 上传和一次 1KB 心跳用同一个 token bucket。

### 边界情况与设计考量

1. **配额上限的粒度**：per-owner（最基本）还是 per-file-type、per-day 还是 per-month？
2. **硬限制 vs 软限制**：硬限制（超过则拒绝）vs 软限制（超过则告警但允许），需要可配置。
3. **配额计算的一致性**：配额是"创建时扣除"还是"创建时保留、完成时确认"？presigned upload 路径（文件先上传到 S3，再调用 CompleteUpload）需要特殊处理——配额应在 PresignUpload 时预保留。
4. **软删除的配额影响**：文件被软删除后，应该仍然计入配额（因为在 GC 回收前文件仍占用空间）还是立即释放？建议软删除释放配额，GC 负责清理。
5. **配额变更的向后兼容性**：对已超过新配额的存量用户，是 force deny 还是 grandfather？
6. **性能考量**：`SUM(files.size) WHERE owner_uid = ?` 在百万级文件量级下不便宜。需要物化用量字段或独立用量表。

### 实现思路

1. 新增 `resource_usage` 物化表（`uid, resource_type, usage_bytes, period_start, period_end`）或直接用 Redis 计数器。
2. 在每个"创建资源"的 service 方法中检查配额，超过则返回 403 `resource.quota_exceeded`。
3. 新增 `GET /v1/usage` endpoint，返回用户的存储用量、消息数等信息。
4. 配额配置在 `config.yaml` 中定义，未来可扩展为 per-role 或 per-user 覆盖。
5. 后台 worker 定期重组用量计数（类似 outbox relay 的模式）。

---

## 方向四：跨实体活动流与统一数据时间线（Activity Feed / Unified Timeline）

### 现状

通知系统是按类型隔离的——用户查询"未读通知"时看到的是 message.received、comment.reply、message.read_receipt 的混合列表，但每个实体类型（文件、消息、评论）的"变更历史"是各自独立的。

**代码证据 1：没有跨实体活动查询**

```go
// internal/notification/repository.go
type Repository interface {
    ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*domain.Notification, string, error)
    // 只有 per-recipient 查询
    // 没有 UnifiedTimeline、ListAllActivity 等跨实体方法
}
```

通知只包含"谁对什么资源做了什么"，但**不是所有操作都产生通知**。

**证据 2：大量操作没有对应的通知事件**

```go
// internal/service/notification.go — Dispatcher.notificationFor 只处理三个事件类型
switch e.EventType {
case "message.sent":     // → message.received 通知
case "message.read":     // → message.read_receipt 通知
case "comment.replied":  // → comment.reply 通知
// 没有 file.shared、file.uploaded、comment.liked、message.edited 等事件
```

文件分享、评论点赞、文件上传/删除等操作产生了审计日志但不产生通知。这意味着用户无法在一个 unified feed 中看到"张三点赞了你的评论"和"李四分享了一个文件给你"。

**证据 3：审计日志虽然跨实体，但主要是面向管理员而非用户**

```go
// internal/repository/admin.go
type AdminRepository interface {
    ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
    // 只按 action 过滤，不按 recipient 过滤
    // 没有 "查询当前用户可见的活动" 的方法
}
```

审计日志面向管理员（需要 `audit.read` 权限），不面向普通用户。

### 为什么需要

**从产品经理视角**：

- "数据归账户所有"的核心体验是让用户看到自己的数据发生了什么。当前用户只能：
  - 轮询查未读通知（新消息、评论回复、已读回执）。
  - 分别查文件列表、消息列表、评论列表。
  - **无法在一个时间线中看到完整的故事**："张三评论了你的文件 → 你回复了 → 李四点赞了你的回复 → 王五分享了这个文件给赵六。"
- 通知的覆盖面不全——文件被分享时接收方没有通知（除非消息体包含文件引用），评论被点赞时作者没有通知。
- 这是社交产品的基础体验，也是"活动流"（Activity Stream）设计模式的核心用例。

**从架构师视角**：

- 当前通知系统是"事件驱动"的，但只覆盖了一个子集的事件。要提供统一活动流，需要：
  1. 补齐缺失的事件类型（outbox 事件 + dispatcher handler）。
  2. 新增"活动 feed"的存储模型（可以是独立表，也可以扩展通知表）。
- 活动流和通知 feed 本质上是同一个基础架构的不同视图：通知是"未读活动"，活动流是"历史活动"。
- 这可以复用 outbox 基础设施——只需要生产更多事件类型 + 新增一个 consumer。

### 边界情况与设计考量

1. **活动的时效性**：多久的活动应该保留？30 天？90 天？永久？
2. **活动的可见性**：文件分享活动应该只对参与方可见。评论点赞活动是公开还是仅作者可见？
3. **活动的聚合**：类似通知折叠，"张三点赞了你的 5 条评论"应该聚合为一条活动。
4. **活动的可删除性**：用户可以清除自己的活动流吗？
5. **性能**：高频操作（点赞）可能产生大量活动事件。需要写入优化（批量 insert）和读取优化（materialized view 或 cache）。
6. **与现有通知的关系**：活动流可以看作"所有通知 + 不产生通知的操作（like、share、edit）"的超集。

### 实现思路

1. 定义统一活动模型（Activity）：`(actor_uid, action, target_type, target_id, target_owner_uid, metadata, created_at)`。
2. 为缺失事件类型补全 outbox 事件和 notification dispatcher 映射：`file.shared`、`comment.liked`、`file.uploaded`、`message.edited`。
3. 活动流查询：`WHERE target_owner_uid = ? OR actor_uid = ? ORDER BY created_at DESC`，按参与方（owner 或 actor）可见。
4. 复用现有的 SSE 推送基础设施，将实时活动事件推送给在线用户。
5. 独立表的读写分离：写入走批量队列，读取走 PG 索引或物化视图。

---

## 方向五：多集群部署与实时推送粘性路由（Multi-Instance Deployment & SSE Sticky Routing）

### 现状

SSE Hub 是纯进程内内存结构，不支持多实例共享订阅状态。outbox relay 也是每个实例独立运行。

**代码证据 1：Hub 是进程级的 `sync.Map`**

```go
// internal/hub/hub.go
type Hub struct {
    subs   sync.Map // uid → *uidSubs    ← 纯进程内，不跨实例
    nextID atomic.Int64
    closed atomic.Bool
}

func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    // LoadOrStore 操作在 sync.Map 上-> 仅本地
}
```

当实例 A 处理了 outbox 事件并调用 `hub.Publish(uid, ...)` 时，如果用户连接在实例 B，推送不会到达。

**证据 2：Outbox relay 每个实例独立运行、无协调**

```go
// internal/app/app.go
relay := outbox.NewRelay(outboxRepo, relayHandler, ...)
stopRelay := relay.Start(pollInterval)  // 每个 app 实例独立启动自己的 relay
```

Kubernetes 下 3 个副本 = 3 个 relay，每个都轮询同一个 outbox_events 表。事件只会被处理一次（MarkPublished 避免重复），但 SSE 推送会丢失当连接不在处理事件的实例上。

**证据 3：当前架构下没有粘性路由机制**

```go
// internal/handler/http/events.go
func (h *EventsHandler) Stream(c *gin.Context) {
    uid, ok := service.ActorFrom(c.Request.Context())
    sub, unsub := h.hub.Subscribe(uid)  // 连接到当前实例的本地 hub
    // 没有实例亲和性提示（无 Set-Cookie、无 WebSocket 升级中的 sticky flag）
}
```

如果用户连接到实例 A，但从实例 B 收到了一个通知（实例 B 的 outbox relay 处理了一个事件），SSE 推送无法跨实例路由。

**证据 4：last_event_id 回放能部分缓解但延迟不可控**

```go
// internal/handler/http/events.go
// replayMissed 在重连时拉取遗漏的通知
// 但在多实例下，"遗漏"的窗口可能是 5s（outbox poll interval）+ 30s（SSE ticker）
// 用户可能延迟 35s 才看到实时通知
```

### 为什么需要

**从架构师视角**：

- 单实例 = 单点故障 + 无法水平扩展。当用户量增长到需要多副本时，实时推送会完全断裂。
- K8s 部署是标准实践——每个无状态服务在前加负载均衡。当前 SSE 实现假设所有连接和事件处理在同一进程。
- 这不是"以后再说"的问题——一旦上线多副本，实时推送就坏了。必须在架构层面解决。
- 几种方案：
  - **Sticky session（会话保持）**：负载均衡根据 uid 或 token hash 路由，保证同一用户始终连接到同一实例。实现简单但有状态，部署不够灵活。
  - **集中式 pub/sub（Redis Pub/Sub / NATS / RabbitMQ）**：Hub 变为消费者订阅一个全局 channel。事件发布到 channel，所有实例消费并推送给本地连接的对应 uid。无状态但引入中间件。
  - **混合方案**：outbox relay 在事件产生时写入一个"推送队列"（Redis list），每个实例消费自己连接的用户的事件。最接近当前架构的演进路径。

**从产品经理视角**：

- 用户不在乎后端架构——他们只关心中间件换来的"消息实时到达"。如果部署了 3 个实例但用户经常延迟 30s+ 才收到通知，产品体验毁于一旦。
- "实时"是通知系统的核心卖点。如果做不到真正的实时，SSE 和轮询的体验差异就消失了。

### 边界情况与设计考量

1. **不引入外部依赖的优雅退路**：当前架构无 Redis/消息队列。如果选择集中式 pub/sub，需要评估运维成本。第一个版本可以只用 sticky session（会话保持）。
2. **last_event_id 机制的保护作用**：即使实时推送偶尔丢失，重连时的 replayMissed 提供了"至少一次"的语义保证。这个保证在粘性路由方案中也应保持。
3. **实例优雅关停的迁移**：当实例被 K8s 滚动更新替换时，需要把该实例的 SSE 连接告知另一个实例接管（或至少让客户端重连到正确实例）。
4. **sticky session 的健康问题**：如果一个实例不健康但负载均衡仍然路由请求到它，SSE 连接会断裂。需要健康检查 + 客户端重连逻辑。
5. **outbox relay 的幂等性**：无论 relay 运行在哪个实例上，事件都只应被处理一次。`MarkPublished` 的原子性已经保证了这一点。

### 实现思路

1. **短期（无额外依赖）**：负载均衡层配置基于 `actor_uid` hash 的 sticky session（如 Nginx `ip_hash` 或 Traefik `sticky sessions`）。一个用户的所有请求和 SSE 连接始终路由到同一实例。
2. **中期（Redis pub/sub）**：Hub 增加一个 Redis 发布者，所有 `hub.Publish` 调用同时发布到 Redis channel。每个实例的 Hub 订阅 Redis channel，收到事件时检查本地是否有对应 uid 的连接，有则推送。
3. **长期（轻量消息队列）**：替换 outbox relay 为基于 Redis Stream 或 NATS 的消费者组模型，确保每个事件只消费一次 + 推送正确实例。
4. SSE 端点增加 `uid` 路由提示（`Set-Cookie: sse_node=<instance_id>`），让负载均衡可直接路由到持有连接的正确实例。

---

## 优先级建议

| 方向 | 优先级 | 估算工期 | 依赖 | 收益类型 |
|------|--------|---------|------|---------|
| 方向二：媒体管线 | **P0** | 2-3 sprints | 需要扩展 FileStorage 接口 + outbox 事件 | 产品体验（高）、带宽优化（中） |
| 方向三：资源计量与配额 | **P0** | 1-2 sprints | 新增 repository 方法 + service 层检查 | 运维安全（高）、SaaS 变现（高） |
| 方向一：存储分层与生命周期 | **P1** | 3-4 sprints | 需要 Storage 接口扩展 + 迁移 worker | 成本优化（高）、合规（中） |
| 方向五：多集群路由 | **P1** | 2-3 sprints | 需要 sticky session 或 Redis Pub/Sub | 架构扩展性（高）、UX 一致性（中） |
| 方向四：活动流 | **P2** | 3-4 sprints | 需要补齐 outbox 事件 + 新增聚合查询 | 产品差异化（高） |

### 推荐的第一阶段（前 2 sprints）

1. **方向三**（最低成本，最高安全收益）：实现 `resource_usage` 物化表 + 存储配额检查。立即解决运维敞口（磁盘填满），为未来 SaaS 奠定基础。
2. **方向二（阶段 1）**：只做图片缩略图（图片预览是最高频 + 最低成本的媒体处理），文件创建时发出 `file.process` outbox 事件，注册缩略图 handler。其他媒体类型后续迭代。
