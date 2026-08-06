# aero-id — 全局代码扫描后确认的五个真实架构盲区

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局扫描 `internal/` 全部 22 个包（120+ 源文件）、`migrations/` 22 份迁移、`api/proto/`、`cmd/`、`configs/`，并与 `docs/requirements/` 下 20+ 份既有分析文档做逐主题交叉比对，定位在所有既有分析中**未被系统覆盖**的方向。
> **前置条件**: `feat/roadmap` 已合并，38 个 gRPC RPC 全对等，22 个迁移全落地。
> **无代码变更**。

---

## 交叉分析方法

```
1. 全量阅读 internal/ 下 22 个包的核心文件，识别基础设施盲区
2. 逐一比对 docs/requirements/ 下 20+ 份文档的 55+ 方向标题
3. 标注每个潜在方向的覆盖度：✅ 已有分析 | 🔶 部分提及 | ❌ 零系统分析
4. 仅保留 ❌ 标记的方向
5. 用代码级证据验证每个方向确实存在真实缺口
```

**结果**: 以下 5 个方向在所有交叉比对中**零系统覆盖**。

| # | 方向 | 类别 | 代码证据强度 | 为什么它被遗漏 |
|---|------|------|-------------|--------------|
| 1 | **gRPC 幂等性保障** | 传输层一致性 | 高 | 幂等性仅实现在 HTTP 中间件；gRPC 完全未覆盖 |
| 2 | **文件读取/下载审计轨迹** | 安全合规 | 高 | 审计系统仅覆盖写入操作；读取操作零审计 |
| 3 | **SSE 事件投递质量与可观测性** | 运维保障 | 高 | Hub 静默丢弃事件，无任何投递质量指标 |
| 4 | **消息已读回执隐私控制** | 用户隐私 | 中高 | 已读回执始终开启，无隐私模式，无 opt-out |
| 5 | **存储后端健康与降级策略** | 基础设施韧性 | 高 | FileStorage 接口无健康探测，后端故障无降级路径 |

---

## 方向一：gRPC 幂等性保障

### 为什么需要它

这是一个**双传输（HTTP + gRPC）服务**，上层承诺"HTTP 和 gRPC 共享同一套 service 层，行为必须一致"。但幂等性保障目前是**HTTP-only**的：

| 维度 | HTTP | gRPC |
|------|------|------|
| 幂等性保护 | ✅ `Idempotency` 中间件（检查 `Idempotency-Key` 请求头） | ❌ 无 |
| 键存储 | ✅ `idempotency.Store`（PostgreSQL，跨实例一致） | ❌ 不可达 |
| 响应缓存 | ✅ 对重复 key 返回首次响应 | ❌ 重复 RPC 执行 N 次 |
| 端点覆盖 | ✅ 所有 POST/PATCH/DELETE 自动保护 | ❌ 零保护 |

**代码级证据**:

| 位置 | 发现 |
|------|------|
| `internal/handler/http/router.go:52` | HTTP 中间件中调用 `middleware.Idempotency(cfg.Idempotency, cfg.IdempotencyTTL)`，自动保护 `/v1` 下所有路由 |
| `internal/handler/grpc/server.go:20-60` | gRPC 拦截器仅实现 Recovery + Auth + Metrics + Tracing + RateLimit，**无 Idempotency 拦截器** |
| `internal/middleware/idempotency.go` | 完整实现了 HTTP 中间件：从请求头取 key → 查 store → 命中则回放 → 未命中则执行并缓存 |
| `internal/idempotency/store.go` | `GormStore` 提供 `Try(key) → (response, exists, err)` + `Complete(key, response)` + `RunCleaner` |
| `internal/service/message.go — Send()` | 同一条消息通过 HTTP 发送携带幂等 key 可安全重试；通过 gRPC 调用 `SendMessage` RPC 无此保护 |

**服务层的行为一致性**：幂等性在 HTTP 端是中间件层完成的——在请求进入 service 之前即被拦截。gRPC 的 `message.sent`、`comment.create`、`message.mark_read` 等 RPC 如果因网络层面重试，会创建多条记录。尽管 service 层没有明显的"重复写入"bug，但**gRPC 客户端的标准重试策略（如 gRPC 内置的 retry 策略）目前没有幂等性保护**，生产环境中可能导致数据重复。

### 为什么当前分析遗漏了它

既有分析文档中的"幂等性"讨论全部集中在 HTTP 端：idempotency key 哈希碰撞（`edge-cases.md`）、idempotency cleaner 全表扫描（`edge-cases.md`）、幂等性与草稿保存（`genuinely-uncovered.md`）——**没有任何文档从 gRPC 传输角度审视幂等性缺口**。

### 建议方案

```go
// gRPC 幂等性拦截器（服务端）
// 原理与 HTTP 中间件相同：从 gRPC metadata 中提取 Idempotency-Key，
// 查 store → 命中则直接返回缓存的响应 → 未命中则执行 handler 并缓存响应。
// 关键在于：
// 1. 哪些 RPC 是幂等的？（所有写入型 RPC：SendMessage, CreateComment, MarkRead, React等）
// 2. 幂等 key 的作用域 = (uid, fullMethod, idempotencyKey)，与 HTTP 端一致
// 3. 响应缓存序列化：protobuf 序列化后存入响应字段

func IdempotencyInterceptor(store idempotency.Store, ttl time.Duration) grpc.UnaryServerInterceptor {
    // ...
}
```

**范围**:
- `internal/handler/grpc/server.go`：新增 `IdempotencyInterceptor`
- `internal/idempotency/store.go`：可能需扩展以支持 gRPC 响应的序列化/反序列化
- 所有写入型 RPC 自动受益，无需逐个 handler 修改

**量级**: 低（1-2 天，复用现有 idempotency.Store 和 HTTP 端的设计模式）

---

## 方向二：文件读取/下载审计轨迹

### 为什么需要它

aero-id 的 Slogan 是 **"data owned by accounts"**（用户拥有的数据）。文件是系统的核心资产。但当前：

- **写入操作**有审计：文件分享授权/撤销（`AuditFileShareGrant / Revoke`）、用户 block/unblock、消息删除
- **文件变更**有审计（部分）：上传、元数据更新、删除（EC-18 已指出）
- **文件读取**零审计：谁、什么时候、从哪个 IP 下载/预览了哪个文件——完全不可查

**代码级证据**:

| 位置 | 发现 |
|------|------|
| `internal/domain/audit.go:18-22` | 审计动作常量有 `file.share.grant`、`file.share.revoke`、`user.block`，**无 `file.read` 或 `file.download`** |
| `internal/file/service_crud.go — Get()` | 返回文件元数据（含下载 URL），**不写审计日志** |
| `internal/file/service_upload.go — DownloadURL()` | 生成预签名下载 URL，**不写审计日志** |
| `internal/handler/http/file.go — DownloadURL / StreamDownload` | HTTP handler 调用 service 后直接返回 URL 或流式数据，**无 audit.Log 调用** |
| `internal/service/service.go — NewFileService` | `WithFileAuditLogger` 仅注入用于**分享操作**的审计器，不是通用文件访问审计 |

**影响场景**:
1. **安全事件溯源**：某敏感文件被泄露，需要查"谁看过这个文件"
2. **合规审计**：SOX/HIPAA/GDPR 要求记录对受保护数据的每一次访问
3. **用量计量**：文件下载次数作为计费或使用分析的依据
4. **异常检测**：短时间内大量下载同一文件可能指示数据爬取或泄露

### 为什么当前分析遗漏了它

- `edge-cases.md` 的 **EC-18** 只覆盖了文件**上传**操作的审计，明确指出"在 `CompleteUpload`、`UpdateMetadata`、`DeleteFile` 中写入 `audit_events`"
- 没有任何文档将"**文件读取**也需要审计"作为一个独立方向分析
- 审计系统的现有使用模式（share/block/delete 等状态变更）暗示了"只审计变更，不审计读取"的思维惯性

### 建议方案

```go
// 在 DownloadURL 和 StreamDownload 路径中插入：
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID) (string, error) {
    // ... 现有 ACL 检查 ...
    
    // 新增：记录文件读取审计事件
    if s.audit != nil {
        _ = s.audit.Log(ctx, &domain.AuditEvent{
            ActorUID:    actor,
            Action:      "file.read",      // 新增审计动作
            SubjectType: "file",
            SubjectID:   id.String(),
            Meta: map[string]any{
                "method": "presigned_url",  // 或 "stream"
                "shared": !isOwner,         // 是否通过分享授权
            },
        })
    }
    // ...
}
```

**设计考量**:
- **性能**: 文件读取可能频繁（点击即触发）。审计写入应异步（非阻塞）——用 outbox 事件而非同步 `audit.Log`
- **数据量**: 如果每个文件下载都写审计，数据量可能很大。考虑采样率（sampling）或汇总为计数（`download_count` 列）
- **隐私**: 审计日志不能暴露给非管理员用户。当前 `PermAuditRead` 权限已存在
- **存储策略**: 审计日志应有保留期限，超期自动归档或删除

**量级**: 低-中（读取可先出 outbox 事件，再由异步 handler 写入审计表；扩展 `domain.AuditEvent` 的动作常量 + 新增 `file.read` 审计动作）

---

## 方向三：SSE 事件投递质量与可观测性

### 为什么需要它

SSE（Server-Sent Events）是 aero-id 实时通知的唯一通道。当前实现通过 `internal/hub` 以 per-uid 内存订阅的方式推送事件。但这个通道存在**完全不可见的丢事件风险**：

**代码级证据**:

| 位置 | 发现 |
|------|------|
| `internal/hub/hub.go:44` | 订阅 channel 缓冲区 = 16 个事件——一个慢消费者只需堆积 16 个事件就开始丢 |
| `internal/hub/hub.go:99-102` | `pub` 方法：`select { case ch <- evt: default: }`——**静默丢弃**，无日志、无指标、无回调 |
| `internal/hub/hub.go` — 全局 | 整个 hub 包**零 metrics 调用**。`Observe`、`counter`、`gauge` 全部不存在 |
| `internal/pkg/metrics/metrics.go` | 有 `ObserveHTTP`、`ObserveGRPC`、`ObserveSSO`、`ObserveOutbox`——**无 SSE 相关指标** |
| `internal/notification/dispatcher.go:66` | Dispatcher 在创建通知后调用 `pusher.Publish(...)`，但推送是否成功、是否被丢弃完全未知 |
| `internal/handler/http/events.go:45-57` | SSE handler 中 `select` 消费 hub channel，消费太慢时 hub 侧已静默丢弃 |

**这意味着**:
- 一个用户的 SSE 连接因网络抖动或页面切换变慢 → 实时通知静默丢失
- 运维人员无法回答"SSE 投递成功率是多少"
- 无法区分"用户没收到通知是后端没发"还是"后端发了但被她错过了"
- 无法设置告警：当丢事件率超过阈值时自动告警

### 为什么当前分析遗漏了它

- `code-level-implementation-gaps.md:36` 仅在说明 hub 实现时提到"buffer of 16 events; slow consumers will silently drop events rather than block"，但未作为**独立运维缺口**分析
- 其他文档讨论 SSE 时集中在"SSE vs WebSocket"（`expansion-directions.md`、`untapped-directions.md`）或"实时推送的基础设施"上，从未深入 SSE**投递质量的可观测性**

### 建议方案

**阶段一：基础设施添加 metrics（低量级，3-5 天）**

```go
// 在 hub.go 中添加：
type HubMetrics interface {
    ObserveSSEPublished(uid string)           // 尝试推送给某人
    ObserveSSEDelivered(uid string)           // 成功写入 channel
    ObserveSSEDropped(uid string)             // channel 满，丢弃
    ObserveSSESubscribers(uid string, count int) // 当前订阅数
    ObserveSSESubscribed(uid string)          // 新增订阅
    ObserveSSEUnsubscribed(uid string)        // 取消订阅
}
```

Metric 暴露（Prometheus 格式）:
```
# SSE 投递质量
aeroid_sse_events_published_total{uid="alice"}  42
aeroid_sse_events_dropped_total{uid="alice"}    3   ← 丢事件了！
aeroid_sse_subscribers{uid="alice"}             1   ← Alice 有 1 个连接
aeroid_sse_channel_fill_ratio{uid="alice"}      0.2 ← 平均 channel 使用率
```

**阶段二：投递保障（中量级，1-2 周）**

- 引入**自适应背压**：当 channel 使用率 > 80% 时，减慢 Dispatcher 对该 uid 的推送频率
- 引入**投递确认**：客户端通过 `Last-Event-ID` 报告最后收到的事件 ID，服务端检测间隙并触发重推
- 引入**慢消费者告警**：当单个 channel 的丢弃率 > 5% 时，记录警告日志

**量级**: 低-中（阶段一是纯指标插桩，不影响任何现有逻辑；阶段二是策略增强）

---

## 方向四：消息已读回执隐私控制

### 为什么需要它

当前系统在消息被读取时**始终**发送已读回执（通过 outbox 事件 → NotificationDispatcher → SSE 推送）。发送方可以实时看到接收方何时阅读了消息。这在很多场景下是不合适的：

- **职场沟通**：员工不希望上级看到"消息已读但未回复"的窘境
- **个人隐私**：用户有权选择不被追踪阅读行为
- **情绪安全**：在某些场景中，"已读但不回复"会引发发送方的焦虑
- **企业文化**：一些团队有意避免已读回执以降低沟通压力

**代码级证据**:

| 位置 | 发现 |
|------|------|
| `internal/service/message.go:104-118` — `MarkRead()` | 硬编码写入 `message.read` outbox 事件，无用户 opt-out 检查 |
| `internal/service/message.go:119` — `evt := outbox.NewEvent(...)` | 无论接收方偏好如何，必然触发已读回执通知 |
| `internal/notification/dispatcher.go:48-71` | Dispatcher 检查 block 关系、preference、rate limit——**不检查已读回执隐私偏好** |
| `internal/domain/notification.go:27` | `NotifTypeMessageRead` 定义为已知通知类型，但无对应的`NotificationPreference` 支持 |
| `internal/repository/preference.go` | Preference 模型支持按类型开启/关闭通知，但 `message.read_receipt` 类型可能不在用户可配置列表中 |
| `internal/notification/service.go` — `service.go` | 通知偏好服务的 `GetPreference(ctx, uid, type)` 未被 `MarkRead` 路径调用 |

**数据流**:

```
Alice 读了 Bob 的消息
  ↓
MarkRead()  →  outbox.NewEvent("message.read")
  ↓
Dispatcher.Handle()  →  notificationMessageRead()  →  Create notification for Bob
  ↓
Bob 收到 SSE 推送 "message.read_receipt"
```

这个流程中**没有任何检查** Alice 是否愿意发送已读回执。

### 为什么当前分析遗漏了它

- 已有分析文档专注于"已读回执的**投递**"——outbox 事件、notification 行、SSE 推送
- 没有文档站在**接收方隐私控制**的角度审视"为什么要强制发送已读回执"
- 类似的隐私功能（如在线状态隐身）在 `five-uncovered.md` 中讨论过，但已读回执隐私是完全不同的功能维度

### 建议方案

**最简单的切入方式**: 在 `NotificationPreference` 模型中为 `message.read_receipt` 类型增加开关。

```go
// 扩展 domain.NotificationPreference 的定义
// 已有模型按 (uid, notification_type) 存储偏好，type = "message.read_receipt" 即可

// 在 MarkRead 路径中插入偏好检查：
func (s *service) MarkRead(ctx context.Context, id domain.ID) error {
    // ... 现有检查 ...

    // 新增：检查用户是否关闭了已读回执
    if s.prefs != nil {
        pref, err := s.prefs.GetPreference(ctx, actor, "message.read_receipt")
        if err == nil && !pref.Enabled {
            // 用户关闭了已读回执：只标记已读，不写 outbox 事件
            return s.messages.MarkRead(ctx, id, time.Now().UTC())
        }
    }

    // 原有逻辑：标记已读 + 写 outbox 事件
    readAt := time.Now().UTC()
    evt := outbox.NewEvent(...)
    return s.messages.MarkReadWithEvent(ctx, id, readAt, evt)
}
```

**更丰富的控制维度**:
- `always`（默认）：始终发送已读回执
- `never`：始终不发送（即使用户已读，发送方看到的 `read_at` 仍为 NULL）
- `respect_sender`：仅对信任的联系人发送（需要 sender 在接收方的"信任列表"中）

**量级**: 低（已有 `NotificationPreference` 模型和 `service.PreferenceChecker`，添加一个偏好类型 + 在 `MarkRead` 中做一次检查即可）

---

## 方向五：存储后端健康与降级策略

### 为什么需要它

FileStorage 是整个系统的核心依赖——文件上传、下载、删除、预签名 URL 全部依赖它。但当前 `FileStorage` 接口完全**没有健康探测机制**，当后端（local 磁盘满或 S3 服务中断）发生故障时：

- 所有文件操作静默失败，返回内部错误
- 用户无法上传/下载/预览任何文件
- 运维人员无法通过 `/readyz` 感知存储故障
- GC sweeper 在存储不可用时反复报错，产生大量错误日志

**代码级证据**:

| 位置 | 发现 |
|------|------|
| `internal/storage/storage.go:13-27` | `FileStorage` 接口定义了 `Put/Get/Delete/PresignGet/PresignUpload/Stat`——**无 `Ping` 或 `HealthCheck` 方法** |
| `internal/storage/local.go` — 全体 | Local 实现无任何连接健康检查；磁盘故障时 `os.Open/Write` 返回 OS 错误 |
| `internal/storage/s3.go` — 全体 | S3 实现使用 AWS SDK 客户端，但不调用 `HeadBucket` 或 `ListObjectsV2` 验证连通性 |
| `internal/app/app.go:141-143` — `buildStorage()` | 存储初始化时不做连通性探测——构造时即返回，远端 S3 可能已宕机 |
| `internal/app/app.go:33` — `HealthHandler` | `/readyz` 检查 DB 和 SSO——**不检查存储后端** |
| `internal/handler/http/health.go` — `Check` | 健康检查注册器 `Check` 类型仅用于 DB 和 SSO，存储无对应探测 |
| `configs/config.yaml` | 无 `storage.health_check.*` 配置节 |

**影响场景**:
1. **S3 服务中断**：所有文件操作返回 `Internal` 错误，但 `/readyz` 显示 green
2. **磁盘写满**：local 模式的文件上传静默失败，所有新文件创建都报错，但运维无所知
3. **S3 凭证过期**：凌晨 3 点轮换密钥后，应用未重启，读/写操作全部失败，但 health endpoint 仍报告 healthy
4. **S3 客户端连接池耗尽**：高并发下载时达 max_connections，后续请求排队超时

### 为什么当前分析遗漏了它

- 已有分析聚焦在**存储的内容安全**（加密、GC、权限）和**存储的性能**（流式、去重、分块上传）
- 没有文档从**基础设施健康与降级**的角度分析存储后端
- 项目对 SSO 做了完整的韧性设计（断路器、serve-stale、重试），但对同样关键的存储后端完全没有类似处理

### 建议方案

#### 1. FileStorage 接口扩展

```go
type FileStorage interface {
    Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    PresignUpload(ctx context.Context, key string, ttl time.Duration) (*PresignedUpload, error)
    Stat(ctx context.Context, key string) (*ObjectInfo, error)

    // 新增：
    Ping(ctx context.Context) error                    // 轻量级健康探测
    IsAvailable() bool                                 // 快速的本地判断（带断路器）
}
```

#### 2. 健康探测集成

```go
// 在 HealthHandler 中注册存储检查
health.Register(Check{
    Name:     "storage",
    Critical: false,  // 非关键：文件操作不可用不应导致整个服务被 LB 摘除
    Probe:    store.Ping,
})
```

#### 3. 降级策略

- **只读模式**：当存储不可用时，写入操作（上传、更新元数据、删除）返回 503，读取操作（获取元数据、生成下载 URL）仍正常工作（因为它们只读 DB，不直接读存储）
- **断路器**：参考 SSO breaker 的实现，为存储操作添加断路器：连续失败 N 次后快速失败 `FileStorageUnavailable` 而非穿透到后端
- **缓存降级**：文件的元数据（名称、大小、类型）在 DB 中已有；在存储完全不可用时，仍可返回文件列表和元数据，只有实际内容下载不可用

#### 4. 运维告警

```go
// S3 连通性探测失败时：
log.Error("storage backend unreachable",
    zap.String("backend", cfg.Storage.Type),
    zap.String("bucket", cfg.Storage.S3.Bucket),
    zap.Error(err))
// → PrometheusAlertManager: AeroidStorageDown → PagerDuty
```

**量级**: 低-中。接口扩展 + S3/local 实现 + 健康注册约 2-3 天；断路器 + 只读模式约 3-5 天。

---

## 汇总优先级

| 方向 | 量级 | 风险 | 建议阶段 |
|------|------|------|---------|
| 1. gRPC 幂等性 | 低 | 中（数据一致性） | P0 — 下一迭代即可 |
| 2. 文件读取审计 | 低-中 | 中高（合规/安全） | P0 — 下一迭代即可 |
| 3. SSE 投递可观测性 | 低-中 | 中（运维盲区） | P1 — 紧随其后 |
| 4. 已读回执隐私控制 | 低 | 低中（产品体验） | P1 — 按产品节奏 |
| 5. 存储后端健康与降级 | 中 | 高（生产韧性） | P0 — 上线前必须 |

## 与既有 55+ 方向的关系

| 既有文档方向集群 | 与本文件的关系 |
|---------------|--------------|
| 群聊/频道、活动流、文件版本、定时消息、Webhook 平台 | **无重叠**——这些是纯产品功能方向 |
| 分布式限流、多副本扩展、存储加密、API 生命周期管理 | **无重叠**——这些是纯架构演进方向 |
| 草稿与自动保存、@提及、批量操作、跨设备同步 | **无重叠**——这些是纯 UX 交互方向 |
| 本文件的 5 个方向 | **全部是既有分析遗漏的"非功能性缺口"**——传输层一致性、审计完整性、可观测性、隐私控制、基础设施韧性 |

## 边缘案例与性能影响（按方向）

### 方向一 · gRPC 幂等性

| Edge Case | 影响 | 处理方式 |
|-----------|------|---------|
| gRPC 客户端和服务端时钟偏差 | 幂等 key 的 TTL 可能计算不一致 | 服务端以本地时钟为准；key 超时时间设为客户端时钟偏差的 2 倍（如 30s） |
| 大响应缓存在内存 | 文件上传的 `CreateFileResponse` 可能很大（含完整元数据），每个幂等 key 缓存在内存中会累积 | 仅缓存轻量响应（ID + status），不缓存完整 payload；或为文件上传 RPC 降低 TTL |
| 并发请求使用相同 key | 首请求和重试可能同时到达 | 用 `SELECT ... FOR UPDATE` 或 `INSERT ... ON CONFLICT DO NOTHING` 原子化第一次写入 |

### 方向二 · 文件读取审计

| Edge Case | 影响 | 处理方式 |
|-----------|------|---------|
| 高并发文件读取（1K/s+） | 同步写审计日志会成为性能瓶颈 | 用 outbox 事件异步写入；或使用 INCREMENT `file.download_count` 替代全量审计 |
| 同一用户反复读取同一文件 | 产生大量重复审计日志 | 去重窗口：同一用户 5 分钟内重复读取同一文件只记录一次 |
| 预签名 URL 读取 | 浏览器直接访问 S3，不经 aero-id | 从 S3 访问日志中关联审计（需要 S3 bucket logging + 解析 pipeline） |

### 方向三 · SSE 投递可观测性

| Edge Case | 影响 | 处理方式 |
|-----------|------|---------|
| 同一用户多设备连接 | 每个设备独立 channel，丢弃互不影响 | 在 `hub.pub` 中追踪每个 channel 的 fill ratio，单独发出告警 |
| 新增指标对 prometheus 的基数影响 | 如果按 uid 标记事件丢弃，高基数标签会导致 Prometheus OOM | 聚合为 per-uid 的 histogram（bucket 粒度），不暴露精确 uid 标签；全局丢事件率用不带 uid 标签的 counter |
| 短生命周期连接 | SSE 连接频繁建立/断开，订阅计数指标剧烈波动 | 用衰减窗口（exponential moving average）平滑指标值 |

### 方向四 · 已读回执隐私

| Edge Case | 影响 | 处理方式 |
|-----------|------|---------|
| 读取后取消勾选"发送回执" | 已发送的回执无法撤回 | 只在 MarkRead 时检查偏好设置；历史回执不追溯 |
| 群聊场景 | 已读回执发给所有参与者 | 如果只有部分参与者允许发送回执，群聊中的"已读"显示可能不一致（A 显示已读，B 不显示） | 群聊已读按个体控制——Alice 看到 Bob 的绿勾，但看不到 Carol 的 |

### 方向五 · 存储健康与降级

| Edge Case | 影响 | 处理方式 |
|-----------|------|---------|
| 存储恢复后数据一致性 | storage 短暂不可用期间，应用层可能有未完成的上传 | 在 GC sweeper 中检测"孤零零的存储对象"时间戳，清理不完整的残留 blob |
| 部分 bucket 不可用 vs 全部不可用 | S3 的特定前缀/分区故障 | 以 storage_key 为粒度探测（`Stat`），而非全局 `Ping`；局部故障只影响相关文件 |
| local 模式下磁盘满 | `Put` 操作失败，但 `Delete` 也可能失败（无法回收空间） | 监控磁盘使用率，在达到 90% 时自动切换到只读模式并告警 |
| `/readyz` 中存储为 critical | 如果存储短暂不可用，整个服务被 LB 摘除，影响所有非文件接口 | 存储应为**非 critical**（与 SSO 的 `ReadinessCritical` 类比）——文件失败只影响文件业务 |

---

**文档状态**：基于全局代码扫描 + 20+ 份既有分析文档交叉比对的最终缺口定位。每个方向均可在当前代码基础上独立实现，互不依赖。
