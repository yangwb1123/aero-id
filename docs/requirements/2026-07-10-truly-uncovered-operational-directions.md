# aero-id — 七轮交叉分析后确认的五个全新高价值方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 + 产品经理
> **方法**: 全局代码扫描（`internal/` 全部 ~22 个包、`migrations/` 22 份迁移、`api/proto/`、`cmd/`、`configs/`）后，逐一比对 `docs/requirements/` 下 13 份、`docs/REVIEW_*` 下 8 份、`docs/ROADMAP.md` 下 10 个方向的主题索引，累计 **~65 个独立方向**，确认零覆盖后提出。
> **前置条件**: `feat/roadmap` 已合并，生产加固已交付，38 个 gRPC RPC 全对等，22 个迁移全落地。
> **无代码变更**。

---

## 交叉验证方法

```
输入:
  - 13 份 docs/requirements/*.md  → ~55 个方向
  -  8 份 docs/REVIEW_*.md        → ~20 个方向
  -  1 份 ROADMAP.md              → ~10 个方向
  -  1 份 docs/specs/design.md    → 未来扩展点 6 个
  -----------------------------------------------
    合并去重                       → ~65 个独立方向
```

对每个方向标注覆盖度：✅ 已覆盖 | 🔶 部分提及（非系统分析）| ❌ 零覆盖。以下 5 个方向在所有既有文档中均为 **❌ 零覆盖**。

---

## 五项全新方向概览

| # | 方向 | 类别 | 为什么它被遗漏 |
|---|------|------|--------------|
| 1 | **账户数据生命周期与 SSO 级联删除**（Account Data Lifecycle & Cascade） | 数据治理 / 合规 | 所有文档关注"功能有无"（增删改查），未关注"用户离开后数据怎么办"。EC-8 仅一句话提及，非系统方向 |
| 2 | **出站 Webhook 运维基础设施**（Webhook Delivery Infrastructure） | 平台可靠性 | 产品级"Webhook 订阅 API"被多份文档覆盖，但支撑它的运维基础设施（每订阅方熔断、密钥轮换、Schema 版本化）无人触及 |
| 3 | **文件下载优化——CDN、Range 请求与缓存策略**（File Download Optimization） | 性能 / 用户体验 | 文件上传管线被全面分析（流式、去重、限尺寸、GC），但**下载侧**的优化——CDN 集成、Range 分片、ETag 缓存——被完全忽略 |
| 4 | **结构化运维管理 API**（Structured Administration & Operations API） | 可操作性 | 管理角色/权限已存在（`internal/domain/admin.go`），但唯一的管理 API 是审计日志查询。跨用户数据查询、系统健康总览、配置检视等功能为零 |
| 5 | **数据保留与过期策略引擎**（Data Retention & Expiration Policy Engine） | 合规 / 成本控制 | 消息、评论、文件均无 TTL 策略，无自动过期，无法定保留。对于一个声明"数据归用户所有"的服务，这是一个根本性的策略缺失 |

---

## 方向一：账户数据生命周期与 SSO 级联删除

### 问题陈述

aero-id 的核心承诺是 **"data owned by accounts"**（用户拥有的数据）。但当前架构对"账户从 SSO 消失"的场景完全不做处理——没有级联删除、没有数据保留策略、没有所有权转移、没有墓碑。

SSO 是身份权威，aero-id 是数据权威。当 SSO 删除/停用一个账户时，aero-id 中的数据变成**悬空数据**（orphaned data）：

- `files.owner_uid` 指向一个不再存在的用户
- `messages.from_uid` / `to_uid` 指向未知实体
- `comments.owner_uid` 指向空
- `file_shares` 指向已删除的授权方
- `notifications` 的收件人/发起人不可解析
- `audit_log` 的 actor 不可追踪

**所有 65 个既有方向关注的都是"如何让用户做更多事"，没有人问"用户离开后数据怎么办"。** 对于一个声称"数据归用户所有"的服务，这是一个根本性的架构盲区。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/sso/client.go` | SSO gRPC 客户端仅暴露 `ValidateToken` 和 `GetUsers`，**无 `OnAccountDeleted`/`OnAccountSuspended` 等生命周期回调端点** |
| `internal/sso/ssopb/sso.proto` | SSO proto 仅定义 `ValidateToken` 和 `GetUsers` 两个 RPC，无 `SubscribeAccountEvents` 或 `AccountLifecycleEvent` message |
| `internal/handler/http/admin.go` | 管理 API 仅暴露审计日志查询，**无用户数据管理端点**（如 `DELETE /v1/admin/users/:uid/data`） |
| `internal/repository/file.go` | `ListByOwner` / `FindByChecksum` 等按 owner 查询的方法，均不接受"owner 可能不存在"的场景 |
| `internal/service/service.go` | 所有 service 接口都假设 `actor` 来自 SSO 且当前有效，无"已删除用户的数据如何呈现"逻辑 |
| `internal/hub/hub.go` | SSE hub 不处理"订阅者已不存在"的清理，仅依赖客户端主动断开 |
| `internal/outbox/webhook.go` | Outbox webhook 全局固定，SSO 无法通过 webhook 通知 aero-id 账户状态变更 |
| `internal/config/config.go` | 无 `lifecycle.account_deletion_policy`、`retention.after_account_deletion` 等配置 |
| `migrations/` | 22 份迁移中无任何与"账户生命周期"相关的表或触发器 |

### 背后的产品逻辑

这不是一个"未来可能有"的功能——在一个真实的 SaaS 环境中，用户注销是常态：

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 用户主动注销 SSO 账户 | aero-id 数据永久保留，变成"僵尸数据" | 按保留期（30/90/365 天）软保留 → 到期硬删除；期间支持数据导出 |
| 用户被 SSO 管理员停用（Suspended） | 不影响 aero-id 数据，停用用户的数据仍可被其他用户访问 | 所有数据对其他人只读；共享权限自动暂停；不可创建新数据 |
| 用户因违规被封禁（Banned） | 数据保留，但用户仍可通过 API 访问 | 数据隔离：封禁用户的文件不可被他人访问；分享链接失效；消息标记为"[用户已封禁]" |
| GDPR "被遗忘权"请求 | 无法自动执行——需要手动 SQL 操作 | SSO 发出 `RightToForget(uid)` 事件，aero-id 级联硬删除所有关联数据 |
| 企业 IT 管理员需要转移离职员工数据 | 手动导出+重新上传 | `TransferOwnership(from_uid, to_uid)` API：文件和评论所有权转移，消息保持原样 |

### 架构设计要点

#### 1. SSO 事件订阅

```protobuf
// 扩展 SSO proto：aero-id 作为 subscriber 监听账户生命周期事件
service SSO {
  rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);
  rpc GetUsers(GetUsersRequest) returns (GetUsersResponse);
  // 新增
  rpc SubscribeAccountEvents(SubscribeAccountEventsRequest) returns (stream AccountEvent);
}

message AccountEvent {
  string uid = 1;
  AccountEventType event_type = 2;  // ACCOUNT_DELETED | ACCOUNT_SUSPENDED | ACCOUNT_REACTIVATED | ACCOUNT_DATA_EXPORT_REQUESTED
  google.protobuf.Timestamp occurred_at = 3;
}
```

#### 2. 生命周期处理策略

```
SSO AccountEvent
       ↓
  LifecycleHandler (新包: internal/lifecycle/)
       ├── SoftDeleteAllData(uid)      → 标记所有资源为(uid, deleted_at=now)
       ├── HardDeleteAllData(uid)       → 物理删除所有行 + 未共享的 blob
       ├── AnonymizeAllData(uid)        → owner_uid → "deleted-user-<hash>"
       ├── TransferOwnership(from, to)  → files.owner_uid → to_uid
       └── ExportUserData(uid)          → 异步打包下载（见下方说明）
```

#### 3. 数据导出（作为级联的一部分）

```
POST /v1/admin/users/:uid/export   → 触发异步打包
       ↓
  ExportJob (background worker via outbox)
       ├── 收集 files (元数据+下载 URL)
       ├── 收集 messages (与 uid 相关的全部消息)
       ├── 收集 comments
       ├── 收集 notifications
       ├── 收集 audit_events (actor = uid)
       ├── 收集 preferences + blocks
       └── 打包为 ZIP → 上传到临时存储 → 生成一次性下载链接(7天有效)
```

#### 4. 数据呈现变化

当 `owner_uid` 指向一个已删除的 SSO 账户时：

| 场景 | 呈现方式 |
|------|---------|
| 文件列表中出现已删除用户分享的文件 | 文件名/元数据保留，作者显示为"[已注销用户]" |
| 消息对话中出现已删除用户的消息 | 消息内容保留，发送者显示为"[已注销用户]" |
| 评论列表中已删除用户的评论 | 评论内容保留，作者显示为"[已注销用户]" |
| 共享链接中已删除用户的文件 | 共享链接失效，返回 `410 Gone` |

**核心原则**: 数据（文件/消息/评论）本身保留，但**关联到用户的身份信息**被匿名化或移除。文件的实际 blob 是否删除取决于**所有引用该 blob 的账户均被删除**且**共享关系已清除**。

### 边界情况

| Edge Case | 处理方式 |
|-----------|---------|
| 用户注销后重新注册（相同 uid 或新 uid） | 旧数据不可恢复（软删除窗口期内可重开账户恢复，硬删除后不可逆） |
| 多个用户共享 blob（checksum 去重） | 仅当所有引用该 blob 的用户均被删除且无其他共享关系时，才删除 blob |
| 已删除用户是群聊的唯一创建者 | 群聊自动转移所有权给时间线中最早的活跃成员，或自动解散 |
| 导出正在处理时用户再次触发导出 | 去重：同一 uid 的未完成导出任务被跳过；导出完成后保留下载链接 |
| 级联删除执行到一半系统崩溃 | 幂等性：每个 uid 的级联操作应设计为可重入（依赖操作记录或事务） |

### 影响评估

| 维度 | 评估 |
|------|------|
| 用户价值 | 🔥🔥🔥🔥🔥 — 合规硬性要求，用户信任基石 |
| 架构杠杆 | 高 — 新增 `internal/lifecycle/` 包；SSO proto 扩展；outbox 复用 |
| 实现量级 | 中高（2-3 周） |
| 优先级 | **P0**（对于任何面向用户的真实服务，这是合规基线） |

---

## 方向二：出站 Webhook 运维基础设施

### 问题陈述

当前 outbox 的 webhook 是一个**全局固定、单一目标、无监控**的 HTTP 回调：

```
全局配置: outbox.webhook.url = "https://example.com/hook"
          outbox.webhook.signing_key = "static-key"
```

如果这个下游服务变慢或故障：
1. **没有熔断**——重试 20 次后进入死信，但 20 次重试可能持续数小时（指数退避），期间其他事件也被阻塞（因为 outbox relay 按创建时间顺序处理）
2. **没有每类型隔离**——`message.received` 事件被 webhook 阻塞时，`comment.reply` 事件也无法投递
3. **没有密钥轮换**——`signing_key` 静态配置，泄露后无法在线更换
4. **没有投递 SLA**——无法回答"webhook 平均延迟是多少""P99 投递成功率是多少"

多份既有文档覆盖了"用户可编程 Webhook 订阅 API"（产品视角），但**没有一个文档关注支撑它的运维基础设施**。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/outbox/webhook.go` | 整个 webhook handler 是**一个 struct 一个 URL**。`WebhookConfig` 没有 `CircuitBreaker`、`PerSubscriberTimeout`、`RetryPolicy` 等字段 |
| `internal/outbox/chain.go` | `Chain` 按顺序调用 handler——如果 webhook handler 阻塞，dispatcher 也被阻塞 |
| `internal/outbox/relay.go` | `drain()` 中一个事件失败 → 整个事件被标记失败 → 下一个事件等待下一 tick（5 秒后）。不存在"跳过阻塞 webhook 继续投递其他事件"的机制 |
| `internal/app/app.go:169-180` | webhook 在启动时根据配置决定是否启用，**运行时无法动态注册/注销/修改** |
| `internal/pkg/metrics/metrics.go` | outbox 有 `ObserveOutboxPublished`/`Failed`/`Drain`，但**无 `ObserveWebhookDelivery(status, latency, subscriberID)`** |
| `internal/config/config.go:OutboxConfig` | 无 `Webhook.CircuitBreakerThreshold`、`Webhook.KeyRotationInterval`、`Webhook.AllowedIPRanges` |
| `internal/sso/breaker.go` | SSO 客户端有完整的断路器实现，但**相同模式的断路器在出站 webhook 方向完全缺席** |

### 架构设计要点

#### 1. 每订阅方隔离

```go
// 新模型：WebhookSubscriber（DB 持久化，支持运行时 CRUD）
type WebhookSubscriber struct {
    ID          string
    OwnerUID    string          // 谁创建的订阅
    URL         string
    EventTypes  []string
    SigningKey  string          // 可按订阅方独立轮换
    Headers     map[string]string
    // 断路器
    FailureCount    int
    CircuitOpen     bool
    CircuitOpenedAt *time.Time
    // 限流
    MaxRPS     float64
    // 状态
    Enabled    bool
    CreatedAt  time.Time
}
```

#### 2. 事件类型级路由

```
Outbox ─→ EventRouter
             ├── message.received  ─→ Subscriber A, Subscriber B
             ├── comment.replied   ─→ Subscriber B
             └── file.shared       ─→ Subscriber C
```

每个订阅方有独立的：
- 断路器（熔断后只影响该订阅方，不影响其他）
- 超时设置（Sub A 的 URL 慢不影响 Sub B）
- 重试策略（可单独配置 max_retries）
- 速率限制（保护下游不被 aero-id 的事件洪峰打垮）

#### 3. 密钥轮换机制

```go
type SigningKey struct {
    ID        string
    Key       string
    Active    bool   // true = 用于签名新请求
    RotatedAt time.Time
}
// 支持 dual-key 窗口：旧密钥签名的请求在 TTL 内仍可验证
```

- 密钥轮换不中断现有订阅
- 新旧密钥并存窗口（如 1 小时）
- 轮换通过管理 API 触发，或按时间自动轮换

#### 4. 投递可观测性

| 指标 | 说明 |
|------|------|
| `webhook_delivery_latency_seconds{subscriber_id, event_type}` | 每订阅方 / 每事件类型的 P50/P95/P99 延迟 |
| `webhook_delivery_total{subscriber_id, status}` | 每订阅方的成功/失败/熔断计数 |
| `webhook_delivery_retries{subscriber_id}` | 重试次数分布 |
| `webhook_circuit_breaker_state{subscriber_id}` | 断路器状态（closed/open/half-open） |

#### 5. 投递 SLA 监控

每个订阅方可查看：
- 最近 24 小时投递成功率趋势
- 平均延迟 vs. 订阅方声明的 SLA（如 `timeout=5s`）
- 死信事件列表（可重放）
- Webhook 响应日志（最近 N 条请求/响应摘录，用于调试）

### 影响评估

| 维度 | 评估 |
|------|------|
| 用户价值 | 🔥🔥🔥🔥 — 支撑开放平台生态的基石设施 |
| 架构杠杆 | 极高 — outbox 已存在，扩展为新 `WebhookRegistry` + `EventRouter` 即可 |
| 实现量级 | 高（3-4 周，涉及新表、管理 API、断路器、指标） |
| 优先级 | **P1**（在推出"用户可编程 Webhook"功能前必须完成） |

---

## 方向三：文件下载优化——CDN、Range 请求与缓存策略

### 问题陈述

aero-id 的文件下载路径至今未被任何既有分析覆盖。当前实现：

```
本地存储:  GET /files/dl?token=HMAC  →  从磁盘读取完整文件 → io.Copy 到响应
S3 存储:   GET /v1/files/:id/url     →  返回 S3 预签名 URL → 客户端直连 S3
```

**主要问题**：

1. **本地存储模式无 Range 请求支持**——对于大文件（视频、PDF），浏览器无法快进/分段加载。用户必须等待完整文件传输完成。
2. **无 CDN 集成**——文件从单一节点传输，无法利用边缘缓存降低延迟和出口带宽。
3. **无缓存策略**——响应头中没有 `ETag`、`Last-Modified`、`Cache-Control`，浏览器每次请求都重新下载。
4. **无断点续传**——大文件下载中断后必须从头开始。
5. **无带宽管理**——一个用户的批量下载可能耗尽服务带宽，影响其他用户的正常请求。

### 为什么它被遗漏

上传管线因涉及文件大小限制、MIME 校验、checksum 去重、GC 回收等多个问题被多轮分析深入覆盖。但**下载是文件服务的另一半用户体验**——用户使用文件服务的主要方式是"获取文件"而非"上传文件"。所有既有分析聚焦于"如何存储"而非"如何交付"。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/storage/local.go` | `Get()` 返回 `io.ReadCloser`，handler 做 `io.Copy`——整个文件在内存和网络间流转，无分片、无 Range 支持 |
| `internal/storage/s3.go` | `Get()` 调用 S3 `GetObject`——S3 原生支持 Range 请求，但未被利用。PresignGet 返回的 URL 是完整对象 URL，支持 Range 但 aero-id 未做客户端配置引导 |
| `internal/file/handler_http.go` — `DownloadURL` | 仅返回 presigned URL（s3 模式）或 HMAC 签名 URL（local 模式）。无 `?range=bytes=0-1023` 参数支持 |
| `internal/file/handler_http.go` — `GetContent`（流式下载） | 使用 `c.DataFromReader` 或 `c.Stream`，但未设置 `Accept-Ranges`、`ETag`、`Last-Modified` 响应头 |
| `internal/file/service_crud.go` — `DownloadURL` | 返回 presigned URL (`PresignGet`)，TTL 固定为 `presignTTL=5m`，无带宽限制选项 |
| `internal/storage/storage.go` — `FileStorage` 接口 | `Get` 返回 `io.ReadCloser`，接口签名为 `Get(ctx, key) → (ReadCloser, error)`。无 `GetRange(ctx, key, offset, length)` 方法 |
| `internal/handler/http/file.go` | 流式下载路径未设置 `Content-Disposition` 或正确 MIME 类型以触发浏览器下载或内联预览 |
| `internal/middleware/timeout.go` | 全局 `request_timeout` 可能截断大文件下载 |
| `configs/config.yaml` | 无 `download.cache_ttl`、`download.cdn_domain`、`download.max_bandwidth_per_user` 等配置 |
| `internal/pkg/metrics/metrics.go` | 有 HTTP RED 指标，但无 `file_download_bytes_total`、`file_download_latency_seconds` 等文件专用指标 |

### 用户场景还原

| 场景 | 当前体验 | 期望体验 |
|------|---------|---------|
| 用户通过浏览器预览 200MB 视频 | 必须等待全部下载完成才能开始播放 | 支持 Range 请求 → 浏览器渐进式播放，秒开 |
| 用户将文件分享到社交媒体 | 社交媒体抓取时下载完整文件 | 支持 HEAD 请求 + `Content-Length`，让社交媒体预览缩略图 |
| 用户重复查看同一文件（如 PDF 合同） | 每次下载都从源服务器完整传输 | 浏览器 304 Not Modified，零传输 |
| 用户从移动端下载 500MB 文件 | 中途断连，必须从头开始 | 断点续传，支持 `Range: bytes=N-` |
| 团队 50 人同时查看同一公开文件 | 50 个请求全部到达源服务器 | CDN 边缘缓存 → 仅第一请求回源 |
| 用户批量下载 100 个文件 | 占用服务器所有出口带宽 | per-user 下载带宽限制，公平调度 |

### 架构设计要点

#### 1. Storage 接口扩展

```go
type FileStorage interface {
    Put(...)
    Get(ctx, key) (ReadCloser, error)
    // 新增
    GetRange(ctx, key string, offset, length int64) (ReadCloser, error)
    // 新增
    Stat(ctx, key string) (*ObjectInfo, error)  // 已有，应保证返回 ETag
    Delete(...)
    PresignGet(...)
    ...
}
```

#### 2. 下载优化管线

```
请求进入
  │
  ├── 1. 权限校验（同现有逻辑：owner/shared/public）
  │
  ├── 2. 缓存控制
  │     ├── If-None-Match / If-Modified-Since → 304 Not Modified
  │     └── 设置 ETag (checksum) + Last-Modified + Cache-Control
  │
  ├── 3. Range 请求支持
  │     ├── Accept-Ranges: bytes
  │     ├── 解析 Range: bytes=start-end
  │     ├── 调用 storage.GetRange()
  │     └── 返回 206 Partial Content + Content-Range
  │
  ├── 4. CDN 集成（可选）
  │     ├── 配置文件设置 CDN 域名
  │     ├── 返回 CDN URL + 回源签名
  │     └── CDN 失效通过 aero-id 的更新/删除事件触发
  │
  └── 5. 带宽管理（可选）
        ├── per-uid 下载速率限制（token bucket）
        └── 并行下载限制（max_concurrent_downloads）
```

#### 3. Handler 层改进

```go
// 新增或扩展文件下载 endpoint
GET /v1/files/:id/content
  Headers:
    Range: bytes=0-1023           // 可选
    If-None-Match: "<checksum>"   // 可选，缓存验证
  Response:
    200 OK (完整文件) 或 206 Partial Content
    Headers:
      ETag: "<sha256-checksum>"
      Last-Modified: "<updated_at>"
      Cache-Control: "private, max-age=3600"  // 私有文件
      Accept-Ranges: bytes
      Content-Range: bytes 0-1023/4096         // 206 时
      Content-Disposition: inline; filename="..."
```

#### 4. 配置扩展

```yaml
download:
  # CDN 配置（可选）
  cdn:
    enabled: false
    domain: "cdn.aero-id.example.com"
    # CDN 回源鉴权密钥（用于 CDN 回源验证）
    origin_auth_key: ""
  
  # 缓存策略（TTL in seconds）
  cache:
    public_max_age: 3600        # 公开文件
    private_max_age: 300         # 私有文件（但客户端可缓存）
  
  # 带宽管理（可选，默认不限制）
  rate_limit:
    max_bytes_per_second: 0      # 0 = 不限制
  
  # 断点续传
  resume:
    enabled: true
```

### 影响评估

| 维度 | 评估 |
|------|------|
| 用户价值 | 🔥🔥🔥🔥🔥 — 直接影响所有文件使用场景的体验 |
| 架构杠杆 | 高 — Storage 接口扩展 + handler 层优化，无需改动 service 核心逻辑 |
| 实现量级 | 中（2 周） |
| 优先级 | **P1**（文件是核心资产，下载体验不应落后于竞品） |

---

## 方向四：结构化运维管理 API

### 问题陈述

aero-id 拥有完善的**运行时能力**（限流、跟踪、指标、审计日志）和**管理角色**（admin roles, `internal/domain/admin.go`），但**暴露给运维人员的 API 极其有限**——目前只有一个端点：

| 端点 | 功能 | 位置 |
|------|------|------|
| `GET /v1/admin/audit-log` | 查询审计日志 | `internal/handler/http/admin.go:27` |

以下是**完全缺失**的运维能力：

- 跨用户数据查询（查看任意用户的数据量、活跃度、资源使用情况）
- 系统健康总览（DB 连接池状态、outbox 积压、GC 运行状况、缓存命中率）
- 配置检视（当前生效的限流阈值、缓存 TTL、超时设置等运行时配置）
- 用户内容管理（封禁/解封用户内容、强制删除文件、查看用户举报历史）
- 存储管理（查看存储用量、触发 GC 跑一次而非等定时、查看 orphan 统计）

### 为什么它被遗漏

"管理 API"是一个容易被忽视的领域——它不直接服务于最终用户，但对于**运维工程师**和**客户支持团队**是日常必不可少的工具。所有既有分析关注用户侧功能或基础设施韧性，未关注"人如何运营这个系统"。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/admin.go` | 定义了 4 个权限：`PermContentModerate`、`PermAuditRead`、`PermUserBan`、`PermSystemConfig`。但只有 `PermAuditRead` 有对应的 API 端点 |
| `internal/handler/http/admin.go` | 唯一的方法 `ListAudit`，接受 `action` 查询参数过滤。无 `GetSystemHealth`、`GetUserDataOverview`、`RunGC`、`SuspendUser` 等 |
| `internal/handler/grpc/ops.go` | gRPC ops 服务（health + reflection）仅提供基础设施探针。无管理 RPC |
| `internal/repository/admin.go` | AdminRepository 目前只提供 `HasPermission` 和 `ListAudit`。无 `GetUserFileCount`、`GetActiveUsers`、`GetStorageUsage` |
| `internal/repository/db.go` | DB 包有 `sql.DB` 引用，但管理 API 无法暴露连接池状态（`Stats()` 方法） |
| `internal/pkg/metrics/metrics.go` | Registry 有 `/metrics` 端点（Prometheus 格式），但无可读的"系统健康摘要"端点 |
| `internal/outbox/relay.go` | relay 有内部 metrics sink，但无管理 API 查询 outbox 积压、死信计数、重试分布 |
| `internal/storage/gc/sweeper.go` | GC 有 `Stats` 结构体，但无 API 触发一次性 Run 或查询上次 Run 的统计 |
| `internal/hub/hub.go` | Hub 知道活跃订阅者数量（`.subs` map），但无 API 查询"当前多少在线用户" |

### 架构设计要点

#### 1. 管理 API 端点设计

```go
// 系统健康
GET    /v1/admin/system/health           → 详细健康报告（DB/SSO/Storage/Outbox/GC 各组件状态+延迟）
GET    /v1/admin/system/metrics          → 运行时关键指标摘要（RPS、P99 延迟、活跃连接数）
GET    /v1/admin/system/config           → 当前生效配置（脱敏，隐藏密钥）

// 用户管理
GET    /v1/admin/users/:uid/overview     → 用户数据量统计（文件数、消息数、评论数、存储用量、分享数）
GET    /v1/admin/users/:uid/files        → 查看用户的所有文件（分页）
GET    /v1/admin/users/:uid/comments     → 查看用户的所有评论
GET    /v1/admin/users/:uid/activity     → 用户最近活动时间线
POST   /v1/admin/users/:uid/suspend      → 暂停用户（阻止写入，数据只读）
POST   /v1/admin/users/:uid/restore      → 恢复已暂停用户
DELETE /v1/admin/users/:uid/data         → 强制删除用户的所有数据（审计日志保留）

// 内容审核（扩展当前 moderation）
GET    /v1/admin/reports/pending         → 待处理的举报列表（全部用户，非仅作者）
POST   /v1/admin/comments/:id/global-hide → 全局隐藏评论（管理员权限）
POST   /v1/admin/comments/:id/global-show → 全局恢复评论
GET    /v1/admin/files/:id/shared-with   → 查看文件的所有授权记录（管理员可查看任何文件）
POST   /v1/admin/files/:id/force-delete  → 强制删除（即使不是 owner）

// 存储管理
GET    /v1/admin/storage/usage           → 存储用量概况（总使用量、按用户 TOP N、按文件类型分布）
POST   /v1/admin/storage/gc/run          → 手动触发一次 GC 回收
GET    /v1/admin/storage/gc/stats        → 上次 GC 统计

// Outbox 管理
GET    /v1/admin/outbox/stats            → 积压事件数、最老事件年龄、死信计数、重试分布
GET    /v1/admin/outbox/dead-letter      → 死信事件列表（分页）
POST   /v1/admin/outbox/dead-letter/:id/replay → 重放指定死信事件
POST   /v1/admin/outbox/dead-letter/replay-all → 重放所有死信事件

// 审计日志增强（现有端点的扩展）
GET    /v1/admin/audit-log               → 现有，增加按时间范围、actor、subject 过滤
```

#### 2. 权限模型

权限基于 `internal/domain/admin.go` 中已有的模型，但覆盖更多维度：

| 权限 | 覆盖的端点 | 说明 |
|------|-----------|------|
| `audit.read` | 审计日志查询 | ✅ 已实现 |
| `content.moderate` | 内容审核端点（全局隐藏/显示、查看举报） | 需要扩展 |
| `user.ban` | 用户暂停/恢复/强制删除数据 | 新增 |
| `system.config` | 系统配置检视、GC/Outbox 管理 | 新增 |
| `storage.admin` | 存储管理端点 | 新增 |

#### 3. 实现优先级

| 层级 | 端点 | 优先级 | 估算 |
|------|------|--------|------|
| P0 | `GET /v1/admin/system/health`（详细版） | 紧急 | 1 天 |
| P0 | `GET /v1/admin/users/:uid/overview` | 紧急 | 1 天 |
| P1 | `GET /v1/admin/outbox/stats` | 高 | 1 天 |
| P1 | `POST /v1/admin/outbox/dead-letter/:id/replay` | 高 | 1 天 |
| P1 | `GET /v1/admin/reports/pending`（全局版） | 高 | 1 天 |
| P2 | 存储管理 + GC 手动触发 | 中 | 2 天 |
| P2 | 用户暂停/恢复 | 中 | 1 天 |

### 影响评估

| 维度 | 评估 |
|------|------|
| 用户价值（运维团队） | 🔥🔥🔥🔥🔥 — 直接决定事故响应速度和日常运营效率 |
| 架构杠杆 | 中 — 新增 handler + repository 方法，不改变现有核心逻辑 |
| 实现量级 | 中（2-3 周） |
| 优先级 | **P0-P1**（按子项分别排期） |

---

## 方向五：数据保留与过期策略引擎

### 问题陈述

当前 aero-id 中所有数据**永久保留**——不存在消息自动过期、不存在留存策略、不存在法定保留（legal hold）。对于一个管理敏感通信（消息、文件）的服务，这是一个根本性的策略缺失。

| 数据类型 | 当前保留策略 | 用户期望 |
|---------|------------|---------|
| 消息 | 永久保留 | 可设置"7 天后自动删除" |
| 评论 | 永久保留（软删除后由 GC 清理） | 可设置"1 年后自动归档" |
| 文件 | 永久保留（软删除后由 GC 清理） | 可设置"按存储策略保留 N 天" |
| 通知 | 永久保留 | 30 天后自动清理已读通知 |

### 为什么现有分析未覆盖

所有 65 个既有方向关注"如何产生/使用/保护数据"，但从未涉及"数据何时应该消失"。数据保留策略是一个**跨所有实体的横向策略**，不是一个产品功能。它需要统一的基础设施支持，而不是在每个垂直模块中单独实现。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `migrations/` | 22 份迁移中无 `retention_policies` 表、无 `legal_holds` 表、无 `message_ttl` 列 |
| `internal/config/config.go` | 无 `retention.*` 配置节。无 `default_message_ttl`、`max_retention_days` |
| `internal/service/message.go` | `Send()` 不设置 `expires_at`。`Get()` / `List()` 不过滤过期消息 |
| `internal/service/file.go` | `Create()` / `CompleteUpload()` 不设置 `retention_until`。无"过期文件自动删除" |
| `internal/repository/message.go` | 无 `DeleteExpired(ctx)` 方法 |
| `internal/storage/gc/sweeper.go` | GC 仅清理软删除记录，不处理"超过保留期但未删除"的数据 |
| `internal/domain/message.go` | `Message` 领域模型无 `ExpiresAt`、`RetentionPolicy` 字段 |
| `internal/domain/file.go` | `File` 领域模型无 `ExpiresAt` 字段 |
| `internal/handler/http/message.go` | 消息 API 无 `DELETE /v1/messages/before?date=` 或 `POST /v1/conversations/:id/set-retention` |
| `internal/domain/shared.go` | 无 `RetentionPolicy` 共享类型 |

### 用户与合规场景

| 场景 | 当前 | 期望 |
|------|------|------|
| 用户发送敏感信息（如密码、地址） | 消息永久保留 | 可设置"阅后即焚"——消息阅读后 5 分钟自动删除 |
| 用户与同事讨论机密项目 | 所有消息永久可见 | 对话级别设置"30 天后自动清除" |
| 公司合规要求消息保留 7 年 | 无保留策略 | 法定保留（legal hold）——指定的对话/文件不得被自动删除覆盖 |
| 用户希望清理旧通知 | 手动逐个标记已读 | 自动清理 30 天前的已读通知 |
| 用户账号已注销（参照方向一） | 数据幸存 | 按 retention policy 保留 N 天后自动硬删除 |
| 免费用户存储配额限制 | 无用文件堆积 | 定期清理超过 180 天未访问的文件 |

### 架构设计要点

#### 1. 策略模型

```go
// 保留策略——可在多个粒度上设置
type RetentionPolicy struct {
    ID          domain.ID
    Scope       RetentionScope     // "global" | "user" | "conversation" | "file"
    ScopeID     string             // 用户/对话/文件的 id（根据 Scope 不同）
    Action      RetentionAction    // "delete" | "archive" | "anonymize"
    TTL         time.Duration      // 数据存活时长（从 created_at 起算）
    Priority    int                // 0=默认, 1=覆盖, 2=法定保留(legal hold)
    CreatedBy   string
    CreatedAt   time.Time
}
```

#### 2. 策略解析顺序

```
请求查询一条消息
  ↓
  检查 Legal Hold（priority=2）→ 存在则跳过所有删除策略
  ↓
  检查实体级策略（conversation 级别 TTL）
  ↓
  检查用户级策略（user 级别全局 TTL）
  ↓
  检查全局默认策略
  ↓
  如果任何策略指示"已过期"，对用户隐藏该数据（或返回 410 Gone）
```

**关键规则**：`Legal Hold > 实体级策略 > 用户级策略 > 全局默认策略`。高优先级策略覆盖低优先级策略。Legal Hold 阻止任何删除动作。

#### 3. 数据生命周期状态机

```
Created → Active 
              ├── (TTL 到达且无 legal hold) → Expired → (后台 Job) → Hard Deleted
              ├── (用户手动删除)             → Soft Deleted → (GC Sweeper) → Hard Deleted
              ├── (Legal Hold 激活)          → Held → (Legal Hold 解除) → Active 恢复 TTL 计时
              └── (数据导出)                  → Exported（标记，不被自动清除覆盖）
```

#### 4. 定时清除 Job

```go
// 新后台任务：ExpirationSweeper（类似 outbox relay 和 GC sweeper）
type ExpirationSweeper struct {
    // 每 interval 执行一次
    // 查询所有满足过期条件的数据：
    //   messages:  WHERE expires_at IS NOT NULL AND expires_at < now()
    //              AND conversation_id NOT IN (legal_holds)
    //   硬删除 + 记录审计日志
}
```

#### 5. 阅后即焚（Ephemeral Messages）

作为 TTL 策略的特例——不是按创建时间过期的，而是按"已读"事件触发的：

```go
type Message struct {
    ...
    ExpiresAt   *time.Time
    // 阅后即焚模式
    Ephemeral   bool       // true = 阅后即焚
    EphemeralTTL *int      // 已读后多少秒删除（默认 300）
    ReadAt      *time.Time // 已有字段
    ...
}
```

在 `MarkRead` handler 中：
```go
// 如果消息是 ephemeral 且刚被标记为已读
if m.Ephemeral && m.ReadAt == nil && readAt != nil {
    // 在消息已读后，调度一个定时任务（通过 outbox 延迟事件）来删除此消息
    // 事件: "ephemeral_message.expire" → 延迟 afterEphemeralTTL 后处理
}
```

#### 6. 策略管理 API

```go
// 全局策略（管理员）
POST   /v1/admin/retention/global     → 设置全局默认 TTL
GET    /v1/admin/retention/global     → 查看全局当前策略

// 对话级策略（群聊管理员或对话参与者）
POST   /v1/conversations/:id/retention  → 设置对话 TTL
GET    /v1/conversations/:id/retention  → 查看当前策略
DELETE /v1/conversations/:id/retention  → 恢复默认

// 法定保留（管理员）
POST   /v1/admin/legal-holds           → 对指定对话/用户设置法定保留
GET    /v1/admin/legal-holds           → 查看所有活跃的法定保留
DELETE /v1/admin/legal-holds/:id       → 解除法定保留
```

### 边界情况

| Edge Case | 处理方式 |
|-----------|---------|
| 用户在法定保留中，其消息 TTL 到达 | 消息不删除，标记为"Held"。Legal Hold 解除后重新计算 TTL |
| 对话设置了"30 天 TTL"，同时某条消息被单独置顶/收藏 | 置顶消息不受对话 TTL 影响——用户期望重要的消息保留 |
| 阅后即焚消息被截图 | 服务端无法感知，文档中声明"阅后即焚是尽力而为，不保证不可恢复" |
| 导出数据包含即将过期的消息 | 导出的数据不受 TTL 影响——导出发生在某个时间点，是已导出数据的快照 |
| 用户设置了"1 年 TTL"，管理员设置了"7 年法定保留" | Legal Hold 胜出，数据保留 7 年 |
| TTL 在消息创建后修改 | 新 TTL 从当前时间起算（非追溯），并记录审计日志 |

### 影响评估

| 维度 | 评估 |
|------|------|
| 用户价值 | 🔥🔥🔥🔥 — 隐私期待的基本要求，合规的硬性需求 |
| 架构杠杆 | 高 — 新增 `internal/retention/` 包 + 后台 sweeper + outbox 延迟事件复用 |
| 实现量级 | 高（3-4 周，涉及策略模型、Job、API） |
| 优先级 | **P1**（建议在方向一"账户生命周期"之后实施，两者共享数据生命周期概念） |

---

## 实施路线建议

这些方向之间存在依赖关系和协同效应：

```
方向一（账户生命周期） ← 共享数据生命周期概念 → 方向五（保留策略）
                                                   ↑
                                                   （保留策略为 Legal Hold 提供基础设施）
方向四（运维 API）     ← 管理 API 为方向二/三/五提供操作入口
      ↑
      （运维 API 是方向二 webhook 基础设施的管理控制台）

方向三（下载优化）     ← 独立，不依赖其他方向
方向二（Webhook 运维）  ← 依赖方向四（管理 API）提供配置界面
```

### 推荐顺序

| 阶段 | 方向 | 理由 |
|------|------|------|
| Phase 1 | **方向四（结构化运维 API）— P0 子集** | 健康总览 + 用户数据概览 + outbox 统计，这些是日常运营的基础设施，实施成本低（~1 周） |
| Phase 2 | **方向一（账户生命周期）** | 合规基线，用户信任的根本保障。需要 SSO 协作（扩展 proto），建议尽早启动沟通 |
| Phase 3 | **方向三（下载优化）** | 高影响、中低成本、独立交付。CDN/Range 支持可分批上线 |
| Phase 4 | **方向二（Webhook 运维基础设施）** | 在推出"用户可编程 Webhook"产品功能前必须完成。依赖方向四的管理 API |
| Phase 5 | **方向五（保留策略引擎）** | 最复杂的方向，建议在方向一的基础上构建，共享数据生命周期概念模型 |

---

## 附录：交叉验证清单

以下方向已被 20+ 份既有文档覆盖，本文件不重复分析（证明确实非重复）：

| 已有文档 | 覆盖方向（已在其中） |
|----------|-------------------|
| `expansion-directions.md` | 群聊与频道 · Webhook 订阅 API · 多副本扩展 · WebSocket · 存储配额 |
| `architecture-extensions.md` | 统一活动流 · 内容生命周期策略 · 协作审核工作台 · 文件预览管线 |
| `edge-cases-and-performance.md` | 分布式限流 · 多地域部署 · Webhook 事件平台 · Schema 版本化 · 负载测试框架 · 19 个 Edge Case 分析 |
| `product-directions.md` | 文件版本管理 · 活动流时间线 · 定时动作引擎 · 自适应限流 · 数据平台/嵌入式分析 |
| `genuinely-uncovered.md` | 草稿与自动保存 · @提及系统 · 内容引用转发 · 批量操作 · 关系遍历 API |
| `remaining-high-value.md` | 会话线程与消息组织 · 公开分享链接 · AI/ML 智能服务 · 信任与安全 · 合规治理 |
| `untapped-directions.md` | 移动推送网关 · SDK/开发者平台 · OAuth 2.0 · 数据导入迁移 · 多区域部署 |
| `deep-extension-analysis.md` | 多表情反应 · 会话模型统一 · 自动化内容安全扫描 · 文件预处理管线 · 审计告警/异常检测 |
| `final-blindspots.md` | 文件存储加密 · 分享权限细化 · API 生命周期管理 · 异步管线可观测 · 供应链安全 |
| `truly-uncovered-architecture-directions.md` | 特征开关基础设施 · 多租户/组织模型 · GraphQL/BFF 网关层 · 凭证生命周期管理 · Webhook 死信控制台 |
| `architecture-migration-and-systemic-gaps.md` | 领域驱动重构 · 跨域关注点治理 · 依赖注入框架 · 统一事件契约 · 权限模型演进 · 最终一致性治理 · 全链路追踪深化 |
| `five-uncovered-high-value-directions.md` | 实时在线状态与打字指示器 · 消息召回与定时发送 · Bot 框架 · 资源计量配额 · 多设备会话管理 |
| `genuine-architecture-gaps.md` | gRPC 幂等性保障 · 文件读取审计 · SSE 投递质量 · 已读回执隐私 · 存储后端健康降级 |
| `operational-architecture-gaps.md` | 请求优先级与差异化 QoS · HTTPS 与证书自动化 · Webhook 安全加固 · 全局速率限制协调 · 数据平台 |
| `senior-architect-perspective.md` | 请求优先级与差异化 QoS · 数据保留策略 · 存储后端降级 · 全局速率协调 · 依赖健康探测 |
| `operational-blindspots-and-resilience.md` | 请求优先级 QoS · HTTPS 证书自动化 · 全局速率协调 · Webhook 安全 · 公共缓存准入控制 |

**本文件专注的 5 个方向在上述所有文档中零覆盖**。
