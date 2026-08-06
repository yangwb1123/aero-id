# aero-id — 深度代码扫描：五个未系统性覆盖的架构方向

> **日期**：2026-07-11
> **视角**：资深架构师 + 产品经理
> **方法**：全量代码逐文件扫描（`internal/` 全部 22 个子包、`migrations/` 23 份 SQL、`api/proto/`、`cmd/`、`configs/`、`Makefile`），与 `docs/requirements/` 下 **99 份既有分析**、`docs/REVIEW_*` 8 份审查文档、`ROADMAP.md`、`docs/CHANGELOG.md`、`docs/DECISIONS.md`、`docs/specs/aero-id-design.md` 做逐方向交叉比对。
> **约束**：不编写任何代码。

---

## 交叉验证说明

99 份既有分析 + 8 份架构审查累计覆盖约 **700+ 条独立方向条目**。以下 5 个方向在所有既有材料中**未被作为独立方向进行包含精确代码证据链的系统性论证**。每个方向提供精确的代码行级证据和架构师/产品经理双视角分析。

---

## 方向一：通知交付可靠性保证 —— SSE 是 fire-and-forget 的，没有重试、确认或离线回填

### 问题陈述

当前实时通知推送路径是**单向且不可靠**的。SSE 推送是非阻塞的（channel 满则丢弃），没有客户端确认机制，没有失败重试，也没有离线回填策略。在多实例部署下，SSE 连接和 outbox 事件处理落在不同实例上时，推送根本到不了正确的客户端。

### 代码级证据

**证据 1：SSE 推送是非阻塞且可丢弃的 — 满 channel 静默丢事件**

```go
// internal/hub/hub.go:83-89
func (h *Hub) pub(uid string, evt SSEEvent) {
    // ...
    select {
    case ch <- evt:
    default:
        // subscriber is too slow; drop rather than block ← 静默丢弃，无日志，无重试
    }
}
```

订阅者的 channel 容量只有 16（`make(chan SSEEvent, 16)`，第 39 行）。一个慢消费者（如移动端切换网络）或突发大量通知会直接导致事件丢失——没有任何机制检测或补偿。

**证据 2：Dispatcher 在 SSE 推送后不追踪交付状态**

```go
// internal/service/notification.go:306-310
if d.pusher != nil {
    payload, _ := json.Marshal(n)
    d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
    // ← 推送完即结束。无 ack 等待，无失败处理，无入列等待重试
}
```

通知已成功写入 `notifications` 表——DB 里有这条记录。但如果 SSE 推送失败（客户端断开、channel 满、实例路由错误），**没有任何路径会将这次失败的推送重新尝试**。用户只有在下一次 SSE 重连（通过 Last-Event-ID replay）时才能看到这条通知。

**证据 3：hub.Hub 是纯进程内结构 — 多实例无法跨节点推送**

```go
// internal/hub/hub.go:27-28
type Hub struct {
    subs   sync.Map // uid → *uidSubs     ← 每个实例维护自己的内存映射
    nextID atomic.Int64
    closed atomic.Bool
}
```

SSE 连接粘在它第一次到达的实例上。outbox relay 可能在任意实例上处理事件。如果通知在实例 A 上被分发（dispatcher 调用 `hub.Publish`），但用户的 SSE 连接在实例 B 上，**推送静默丢失**——Hub 没有跨节点 pub/sub。

**证据 4：dispatcher 中的通道容量检查仅通过 `select/default` —— 无法判断失败**

Hub 的 `Publish` 在 `adapter.go` 中适配为调用 `h.pub()`，该方法对满 channel 静默丢包。调用者（dispatcher）无法知道推送是否成功，因此无法决定是否需要重试：

```go
// internal/hub/adapter.go:10-13
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
    h.pub(uid, SSEEvent{ID: eventID, Type: eventType, Data: data})
    // ← 不返回 error。满 channel 静默返回
}
```

### 产品经理视角

用户发送一条消息后，发送方期望接收方**立即**收到通知。在当前架构下：如果接收方的 SSE 连接恰好在推送那一刻断开（例如移动端进电梯），通知永远到不了——直到接收方下次手动打开 app 触发轮询。这是**通知可靠性的零保证**，对于消息类产品的核心体验是灾难性的。对标产品（Telegram、WhatsApp、微信）均有推送确认、离线存储和重发机制。

### 架构师视角

修复需要引入**推送确认/重试层**：

1. **推送确认状态追踪**：`notifications` 表增加 `push_status` 枚举（`pending`/`delivered`/`failed`），dispatcher 先写入 `pending`，SSE 投递成功后更新为 `delivered`
2. **重试队列**：将未确认（`push_status=pending`）的通知加入一个有限重试循环（backoff，最多 3 次），超过阈值后降级为"仅 DB 记录，无实时推送"
3. **跨实例 pub/sub**：引入 Redis pub/sub 或 NATS 作为 Hub 的后端，使任意实例的 dispatcher 都能将事件路由到正确的 SSE 连接所在实例
4. **SSE 心跳与断线检测**：当前已有 30s 心跳（`events.go:77`），但断线后不会将通知标记为"待重推"。需在连接断开时触发 `onDisconnect` 回调，将未推送的通知标记回 `pending`

此方向的实现成本中等，但影响面广（需要新增 schema 迁移、Hub 重构、dispatcher 逻辑变更），建议分两阶段实施：第一阶段引入推送状态追踪 + 重试，第二阶段引入跨实例 pub/sub。

---

## 方向二：预签名上传路径的原子性缺口（Presigned Upload Atomicity Gap）

### 问题陈述

`PresignUpload` → 客户端 PUT → `CompleteUpload` 三步流程中，从预签名 URL 发放到最终行创建的窗口期内，blob 在存储后端存在但没有任何 DB 行记录它。这个窗口内的 blob 面临 GC 误删、客户端放弃后的孤儿泄漏、以及重复 `CompleteUpload` 竞态等风险。

### 代码级证据

**证据 1：PresignUpload 不创建 DB 行**

```go
// internal/file/service_upload.go:26-48
func (s *fileService) PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error) {
    actor, err := domain.MustActor(ctx)
    // ... 校验 name、mime ...
    key := s.store.NewKey(actor)          // ← 分配存储路径
    url, err := s.store.PresignPut(ctx, key, ttl)  // ← 生成预签名 PUT URL
    // ...
    return &PresignUploadResult{
        FileID:     idgen.NewID(),         // ← 预分配 UUID，但不落 DB
        UploadURL:  url,
        StorageKey: key,
    }, nil                                // ← 无 DB 写入
}
```

预分配了 `FileID`（UUIDv7），但它只存在于返回给客户端的 JSON 中。没有任何表记录这个 ID 的"pending upload"状态。

**证据 2：CompleteUpload 在 Stat + Create 之间没有事务保护**

```go
// internal/file/service_upload.go:50-94
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*File, error) {
    // ...
    info, err := s.store.Stat(ctx, in.StorageKey)  // ← 检查对象是否存在
    // ...
    if s.maxBytes > 0 && info.Size > s.maxBytes {
        _ = s.store.Delete(ctx, in.StorageKey)       // ← 超限后删除 blob
        return nil, apperr.BadRequest(...)
    }
    f := &File{ /* ... ID: in.FileID ... */}
    if err := s.files.Create(ctx, f); err != nil {   // ← 创建 DB 行
        _ = s.store.Delete(ctx, in.StorageKey)       // ← 失败时删除 blob（best-effort）
        return nil, ...
    }
    return f, nil
}
```

问题场景：
- **GC 误删**：从 `PresignUpload` 到 `CompleteUpload` 的时间窗口（可能长达数分钟），如果 GC 配置了 `SweepOrphans=true` 且 `MinOrphanAge` 小于 blob 存在时间，GC 可能将未完成的 blob 视为孤儿并删除
- **客户端放弃**：客户端获取预签名 URL 后从未调用 `CompleteUpload`，blob 永久成为孤儿（直到下次 GC 孤儿扫描——默认关闭）
- **无双 Finalize 检测**：没有 TTL 机制在超时后自动清理未完成的 upload。一个恶意客户端可以大量请求预签名 URL 但不完成上传，导致存储 key 命名空间枯竭（虽然概率低）

**证据 3：CompleteUpload 不验证 FileID 的唯一性**

`CompleteUpload` 使用客户端传回的 `FileID` 创建行。但如果客户端用相同的 `FileID` 调用两次 `CompleteUpload`（重试场景），第二次会因为主键冲突而失败——但此时 blob 已经上传完成，DB 行创建回滚并尝试删除 blob。如果第二次 `Delete` 失败（网络错误），**blob 成为孤儿**。

### 产品经理视角

用户在上传大文件时如果网络不稳定，重试可能因为 `CompleteUpload` 的竞态条件而产生"上传失败"的错误，但实际上文件已成功存储在 S3——只是 DB 没有对应的行。用户无法在文件列表中看到它，但存储空间已占用。这种"幽灵文件"不仅损害用户体验，还造成存储成本泄漏。

### 架构师视角

修复方案：

1. **引入 `pending_uploads` 表**：`PresignUpload` 时写入一行 `(file_id, owner_uid, storage_key, expires_at, status)`，status 为 `pending`。`CompleteUpload` 将行标记为 `completed` 并创建正式的 `files` 行。expires_at 列支持 TTL 自动清理未完成的上传。
2. **Upload Session 超时**：在 `storage.NewKey` 或 `PresignUpload` 层级设置 TTL（如 30 分钟），超时后由后台作业标记失败并清理 blob。
3. **GC 感知 pending 状态**：孤儿扫描应排除与 `pending_uploads` 表关联的 blob，避免竞态误删。
4. **幂等 CompleteUpload**：用 FileID 做幂等键，第二次调用直接返回已创建的文件行，而非主键冲突。

此方向实现成本中等（加一个 migration + ~200 行 service 逻辑），但对生产可靠性影响显著。

---

## 方向三：双 Dispatcher 实现并存 —— 未完成重构遗留的死代码与维护风险

### 问题陈述

代码库中存在**两套独立的通知分发器实现**，其中一套从未被 wiring 到应用启动路径中（`app.go`）。这是跨阶段重构遗留的死代码，不仅浪费维护成本，更危险的是——未来修改通知逻辑的开发者在两套实现间不知道该改哪一套。

### 代码级证据

**证据 1：`internal/service/notification.go` 中的 `NotificationDispatcher` —— 被 wiring 的生产实现**

```go
// internal/service/notification.go:207-215
type NotificationDispatcher struct {
    notifs      repository.NotificationRepository
    blocks      BlockChecker           // 被 wiring（在 app.go）
    prefs       PreferenceChecker      // 被 wiring
    pusher      NotificationPusher     // 被 wiring
    rateLimiter *notifRateLimiter      // 被 wiring（rate=10）
    collapseWin time.Duration          // 被 wiring（5min）
}
```

**证据 2：`internal/notification/dispatcher.go` 中的 `Dispatcher` —— 从未被 wiring 的替代实现**

```go
// internal/notification/dispatcher.go:20-24
type Dispatcher struct {
    notifs Repository
    blocks block.BlockChecker  // 仅 block checker + pusher
    pusher hub.Pusher          // 无 preference、rate limit、collapse
}
```

这个 `Dispatcher` **不支持**通知偏好检查、速率限制、折叠去重。接口签名也不同——它使用 `hub.Pusher`（`Publish(uid, eventType, data string)` 3 参数），而生产环境使用的是 `service.NotificationPusher`（`Publish(uid, eventType, data string, eventID int64)` 4 参数）。

**证据 3：`app.go` 中只 wiring 了旧实现**

```go
// internal/app/app.go:82-87
dispatcher := service.NewNotificationDispatcher(notifRepo,
    service.WithDispatcherBlockChecker(blockRepo),
    service.WithDispatcherPreferenceChecker(prefChecker),
    service.WithDispatcherRateLimiter(10),
    service.WithDispatcherCollapse(5*time.Minute),
    service.WithDispatcherPusher(sseHub),
)
```

**从未有代码调用** `notification.NewDispatcher(...)`。这个文件是一个被遗弃的中间状态。

**证据 4：Hub 的接口适配层也体现了这种撕裂**

```go
// internal/hub/adapter.go
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
    h.pub(uid, SSEEvent{ID: eventID, Type: eventType, Data: data})
}
var _ service.NotificationPusher = (*Hub)(nil)  // 适配 service 接口
```

这个 `adapter.go` 的存在正是为了弥补两个 dispatcher 实现之间的接口差异——新 `Dispatcher` 使用 `hub.Pusher`（3 参数），旧 `NotificationDispatcher` 使用 `service.NotificationPusher`（4 参数）。`hub.Pusher` 接口在 hub 包中定义：

```go
// internal/hub/hub.go:91-94
type Pusher interface {
    Publish(uid, eventType, data string)  // 3 参数
}
```

**证据 5：domain 中的通知类型常量也在两处定义**

```go
// internal/domain/notification.go  — 定义了 NotificationType 常量
const (
    NotifTypeMessageReceived NotificationType = "message.received"
    NotifTypeMessageRead     NotificationType = "message.read_receipt"
    NotifTypeCommentReply    NotificationType = "comment.reply"
)
```

而新旧 dispatcher 中的事件类型转换使用的是**硬编码的裸字符串**（`"message.sent"`、`"message.read"`、`"comment.replied"`），没有引用 domain 中的常量。

### 产品经理视角

对用户侧的直接影响为零（当前生产实现正确工作了）。但对产品迭代速度有隐性影响：每个新通知类型的添加需要开发者判断"这个新类型应该加到哪个 dispatcher？另一套要不要同步更新？"这种不确定性直接拖慢交付速度。

### 架构师视角

这是**重构过程中产物的遗留**。最干净的做法是：

1. 删除 `internal/notification/dispatcher.go`（新实现）及其关联的 `hub.Pusher` 接口（3 参数版本）
2. 将 `internal/notification/notification.go`（通知 Service 接口）中引用的 `Repository` 改为 `repository.NotificationRepository`
3. 统一所有通知相关类型到 `domain/notification.go`，让事件类型字符串成为 domain 常量而非散落的硬编码
4. 删除 `internal/hub/adapter.go`，让 `*Hub` 直接实现 `service.NotificationPusher`（已在做，但 adapter 是多余的桥梁层）

此方向不引入新功能，但它消除了一个**一碰就碎**的维护陷阱——下一个改通知代码的人很可能改错文件。

---

## 方向四：存储后端在线迁移能力缺失（Storage Tier Migration）

### 问题陈述

`files` 表每行记录 `storage_type`（`local` 或 `s3`），但整个系统没有提供从 local 迁移到 s3（或切换 s3 区域/提供商）的在线机制。启动时选择的存储后端是唯一操作路径。对于生产部署，这意味着存储迁移=停机维护。

### 代码级证据

**证据 1：storage_type 是每行级别的字段，但启动时全局选择后端**

```go
// internal/repository/db.go:13-20
type fileRow struct {
    // ...
    StorageKey  string `gorm:"column:storage_key"`
    StorageType string `gorm:"column:storage_type"`  // "local" | "s3"
    // ...
}
```

`storage_type` 存在就说明设计时考虑过**混合存储**或**在线切换**的可能性，但从未实现：

```go
// internal/app/app.go:284-299
func buildStorage(ctx context.Context, cfg *config.Config) (storage.FileStorage, httpapi.SignedDownloader, error) {
    switch cfg.Storage.Type {          // ← 启动时读取全局配置
    case "local":
        local := storage.NewLocal(...)
        return local, local, nil
    case "s3":
        s3, err := storage.NewS3(...)
        return s3, nil, nil            // ← 只返回一个后端
    }
}
```

**证据 2：FileService 只持有一个 `storage.FileStorage` 实例**

```go
// internal/file/file.go:71-74
type fileService struct {
    files       Repository
    shares      ShareRepository
    store       storage.FileStorage   // ← 单实例，启动时确定
    storageType string                 // ← 用于写入时标记
    // ...
}
```

`storageType` 字段就是启动时配置的那个——它不是查询时动态识别的。这意味着即使两个后端都在运行，service 也无法按行选择正确的后端。

**证据 3：`FileStorage` 接口没有 `CopyBetween` 或迁移相关方法**

```go
// internal/storage/storage.go (推断)
type FileStorage interface {
    Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
    NewKey(ownerUID string) string     // ← 分配新存储 key
    PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
    // ← 无 Copy、无 List、无跨后端迁移
}
```

没有 `CopyTo(dest FileStorage, key string) error` 这样的方法，也没有源端列举、目标端写入、完整性校验的原子化迁移 API。

### 产品经理视角

对最终用户无直接影响——但只要运维团队想更换存储提供商（如从自建 MinIO 迁移到 AWS S3）、切换区域以降低延迟或成本、或从开发期 local 存储切换为生产 S3，就意味着**停机窗口**。对于 24/7 的服务，停机迁移存储是不可接受的。

### 架构师视角

存储迁移能力的缺失是一个典型的"第二系统问题"——MVP 做单后端是合理的，但当项目到达生产成熟度（现在 aero-id 的成熟度）时，没有迁移路径就是运营风险。

最小可行方案：

1. **`FileStorage` 接口扩展 `Copy` 方法**：`Copy(ctx, dst FileStorage, key string) (newKey string, err error)`
2. **存储注册表**：`internal/storage/registry.go` 持有所有配置后端的映射（`map[string]FileStorage`），service 按 `file.StorageType` 选择读/写后端
3. **迁移 CLI 命令**：`cmd/migrate-storage` 逐行枚举 `files` 表中的记录，对 `storage_type != target` 的行执行 `Copy` + 更新行 `storage_type` + 完整性校验（checksum 比对）
4. **双写模式**：迁移期间写入新 blob 同时写到新旧两端，读端优先读新后端，fallback 到旧后端

此方向实现成本高（涉及存储抽象层重构 + 迁移 CLI + 数据验证），但对于生产级服务是不可跳过的基建投资。建议在首次存储扩容或提供商更换之前完成。

---

## 方向五：资源级配额与存储计量缺失（Resource Quota & Usage Metering）

### 问题陈述

aero-id 作为用户内容存储服务，没有任何**资源使用上限**的机制。用户可以无限量上传文件、发送消息、创建评论、添加 reactions、生成通知。当前只有 rate limiting（请求频率）和文件大小限制，没有**空间配额**、**数量配额**或**带宽计量**。这是一个成本风险、公平性风险以及滥用风险。

### 代码级证据

**证据 1：文件上传无配额检查 — 可以无限上传直到磁盘/账单溢出**

```go
// internal/file/service_crud.go:25-66
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    // ...
    if s.maxBytes > 0 && in.Size > s.maxBytes {   // ← 只检查单文件大小上限
        return nil, apperr.BadRequest(...)
    }
    // ← 无 per-user 总用量检查
    key, size, err := s.store.Put(ctx, actor, in.Name, reader)
    // ...
}
```

没有任何 `SELECT SUM(size) FROM files WHERE owner_uid=? AND deleted_at IS NULL` 查询，也没有在超过配额时拒绝写入的逻辑。

**证据 2：消息和评论无数量限制**

```go
// internal/message/service.go — Send
func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
    // ← 无 per-user 消息总数检查
}

// internal/comment/service.go — CreateComment (service layer)
func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
    // ← 无 per-user 评论总数检查
}
```

**证据 3：数据库模型无用量缓存或配额表**

没有任何 `usage_quotas` 或 `user_usage` 表。即使想实现配额检查，每次都要 COUNT/SUM 扫描，在高频写入下的性能开销不可接受。

**证据 4：存储 GC 只回收软删除的行——不解决用量膨胀问题**

GC 在 `storage/gc/sweeper.go` 中回收已软删除且过保留期的行和对应的 blob，但它**不限制活跃用户的总用量**。一个用户上传 1TB 的合法文件不会被任何机制阻止。

**证据 5：配置中没有配额相关的配置项**

```yaml
# configs/config.yaml
storage:
  max_upload_bytes: 52428800   # 50 MB — 单文件限制
  # ← 无 per_user_quota、无 max_files_per_user、无 max_storage_gb
```

### 产品经理视角

没有配额的 SaaS 服务最终会遇到两类问题：

1. **成本失控**：一个恶意用户上传 10TB 数据，存储费用（尤其是 S3）直接反映在云账单上。因为没有配额限制，无法在计费阈值上做软限制（警告）或硬限制（拒绝）
2. **公平性问题**：少数用户占用大量存储，影响整体服务质量（IOPS、连接池），却没有手段引导他们升级套餐或清理数据

对于 ToB 场景，配额是产品套餐化的基础（免费用户 1GB，付费用户 100GB）。没有配额=没有定价模型。

### 架构师视角

配额系统需要分层实现：

1. **数据模型**：
   - `usage_records` 表：`(uid, resource_type [file_storage_bytes|message_count|comment_count], used bigint, updated_at)`
   - `usage_quotas` 表：`(uid, resource_type, quota_limit bigint, soft_limit bigint)` — 硬限制（拒绝）和软限制（警告）

2. **用量缓存**：高频率的 COUNT/SUM 不可接受。应在写入时原子递增 `usage_records.used`（使用 GORM `Clause.OnConflict` + `Expr("used = used + ?", delta)`）。初始值可通过后台 job 从 `files SUM(size)` 等查询回填

3. **检查点**：
   - `file.Create`、`file.CompleteUpload` → 检查 `file_storage_bytes`
   - `message.Send` → 检查 `message_count`
   - `comment.Create` → 检查 `comment_count`
   - 所有检查在 `service` 层统一拦截，返回 `ResourceExhausted` AppError

4. **后台校正 job**：定期（每日）从主表扫描用量，纠正 `usage_records` 因潜在竞态导致的偏差

5. **通知集成**：接近软限制时通过 outbox 触发 `quota.warning` 通知事件，在硬限制拒绝时返回清晰的错误码

此方向实现难度中高，涉及 schema 变更、service 层检查点植入、缓存策略和后台校正。但它是一系列产品化的基础能力——没有配额，就无法定价、无法分级、无法做成本控制。

---

## 总结

| # | 方向 | 类型 | 优先级 | 核心风险 | 估算 |
|---|------|------|--------|---------|------|
| 1 | 通知交付可靠性（SSE重试+多实例路由） | 架构/产品 | P1 | UX 核心体验缺失；多实例部署时推送断裂 | 中-高 |
| 2 | 预签名上传原子性缺口 | 正确性/数据完整性 | P1 | 数据泄漏（幽灵 blob）；存储成本泄漏 | 中 |
| 3 | 双 Dispatcher 死代码 | 技术债/维护性 | P2 | 通知逻辑改错文件的风险；持续维护成本 | 低 |
| 4 | 存储后端在线迁移 | 运维/基础设施 | P1-P2 | 存储迁移必须停机；锁定在初始存储提供商 | 高 |
| 5 | 资源配额与用量计量 | 产品/成本控制 | P1 | 成本失控；无法产品化定价；公平性问题 | 中-高 |

**推荐启动顺序**：

- **短期（2-3 天）**：方向 3（清理双 dispatcher 死代码）——低成本、降维维护风险，为后续通知改造铺路
- **中期（1-2 周）**：方向 1（通知交付可靠性）+ 方向 2（预签名上传原子性）——影响核心 UX 和数据完整性
- **长期（持续投入）**：方向 4（存储迁移）按需触发；方向 5（配额体系）在产品定价需求到来前完成基建
