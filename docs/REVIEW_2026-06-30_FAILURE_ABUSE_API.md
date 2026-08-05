# aero-id — 第八轮审查：故障模式注入 · API 消费者缺口 · DR 策略 · 滥用防护

> 日期: 2026-06-30
> 角度: 故障注入测试 · 消费者视角 API 审查 · 灾备策略 · 配置审计 · 滥用检测

---

## 0. 与前七轮的关系

| 维度 | 前七轮 | 本轮 |
|------|--------|------|
| 故障模式分析 | 提及（降级策略） | ✅ **11 种故障注入场景** |
| API 消费者视角 | 提及（SDK 缺口） | ✅ **端点级场景 + HTTP 方法/响应码分析** |
| 备份/DR 策略 | 未覆盖 | ✅ **RPO/RTO/备份策略/恢复测试** |
| 配置审计 | 浅层提及 | ✅ **全字段逐项核对** |
| 滥用/垃圾检测 | 未覆盖 | ✅ **限流缺口 + 内容投毒/枚举/囤号** |
| 扩展方向 | 35 个 | ✅ **5 个全新方向** |

---

## 1. 故障模式注入分析：当每个组件失败时

### 1.1 SSO 不可用

| 故障 | 表现 | 影响 |
|------|------|------|
| SSO 宕机 | Token 验证全部失败 | `/v1/*` 端点全部 401。缓存中的 token 继续工作（StaleTTL 窗口） |
| SSO 延迟飙升 | Token 验证变慢 | API P99 延迟飙升。所有请求等待 SSO 响应 |
| SSO 返回错误数据 | 错误的 uid 或过期信息 | 认证绕过或错误的用户标识 |

**当前保护：** 
- TokenCache 提供 StaleTTL 窗口 ✅
- 断路器在连续错误后打开 ✅

**缺口：** 当 SSO 延迟高但未触发断路器时（如 2s 响应，仍返回成功），所有请求都被阻塞 2s。没有"快速失败"策略来跳过缓存 miss 的 SSO 调用。

**建议：** 为 SSO 调用添加"软超时"（如 500ms）。超过后返回 stale cache 或直接返回错误，而不是等待 2s。

### 1.2 Postgres 不可用

| 故障 | 表现 | 影响 |
|------|------|------|
| DB 宕机 | `sql: database is closed` | 全部端点 500。所有读/写操作失败 |
| DB 延迟 | 查询变慢 | API P99 延迟飙升。连接池耗尽 |
| DB 连接耗尽 | `too many connections` | 部分请求 500 |
| 死锁 | `deadlock detected` | 特定操作失败，重试可能成功 |
| 磁盘空间满 | 写入失败 | 所有写操作失败，读操作正常 |

**当前保护：**
- `max_open_conns: 50` ✅
- `/readyz` 有 DB ping ✅
- GORM 内置连接池 ✅

**缺口：**
- 无 `statement_timeout` 设置（长查询可能挂住连接 30s+）
- 无连接池饱和度监控（`aeroid_db_connections_in_use` 指标不存在）
- 无读副本重度——DB 宕机时完全不可服务

**建议：** 设置 `statement_timeout: '30s'`（配置），监控连接池使用率，考虑读副本。

### 1.3 S3/MinIO 不可用

| 故障 | 表现 | 影响 |
|------|------|------|
| S3 宕机 | `PresignUpload`/`Put`/`Get` 失败 | 文件上传/下载失败。已存在的文件可能仍可访问（如果 presigned URL 已生成） |
| S3 延迟 | 上传/下载变慢 | 文件操作 P99 上升 |
| S3 权限问题 | `AccessDenied` | 指定操作失败 |

**当前保护：**
- Local 存储作为替代（配置 `storage.type: local`）✅
- presigned URL 的 TTL 有限（减少风险窗口）✅

**缺口：**
- Local 和 S3 存储切换需要重启（配置文件变更）
- 没有自动降级到只读模式（S3 不可用时文件上传应该失败，但下载应该从其他方式提供）
- 没有存储健康检查（`readyz` 不检查存储）

**建议：** `readyz` 增加存储 health check（`HeadBucket` 或 stat 一个已知 key）。

### 1.4 出箱 relay 停止

**表现：** `relay.Start` 的 goroutine 崩溃或停止。
**影响：** 通知停止投递、webhook 停止调用、计数不更新。但所有数据仍在 outbox 表中，relay 重启后继续处理。

**当前保护：** relay 是单 goroutine，如果崩溃无法自动恢复。

**缺口：** 无 `/readyz` 检查 relay 是否存活。无 relay 崩溃告警。

### 1.5 OOM（内存溢出）

**触发条件：**
- SSO Token Cache 无上限增长（每秒 10k 不同 token × 360s TTL = 720MB）
- SSO User Cache 无上限增长（含大量并发用户查询）
- 大量 SSE 连接（每个订阅者一个 channel buffer 16 events）
- 大文件上传流（服务器端内存缓冲）

**表现：** `Out Of Memory Kill` → 整个进程崩溃。
**影响：** 所有活跃连接断开。SSE 客户端需要重连。outbox 事件延迟处理。

**当前保护：** 无自动缓存大小限制。

### 1.6 Goroutine 泄漏

**触发条件：**
- HTTP 请求完成但 handler 的 goroutine 未退出（如写入已关闭的 SSE 通道）
- `time.NewTicker` 未正确 `Stop()`（`relay.go` 中在 `<-done` 处 `t.Stop()` ✅ 已正确处理）
- Hub 订阅者未正确 `unsub()`（延迟在 handler 中 `defer unsub()` ✅）

**表现：** goroutine 数量持续增加 → 内存使用上升 → OOM。

### 1.7 时钟漂移

**触发条件：** 服务器时钟不准确（容器漂移、NTP 故障）。
**影响：**
- UUIDv7 的时间排序属性失效（保证时间单调递增失败）
- 分页 cursor 基于 `id < cursor` 的比较可能出错
- Token 缓存的 `expiresAt` 比较不可靠
- 审计日志的时间戳偏差

**当前保护：** 无时钟漂移检测。

---

## 2. 消费者视角 API 缺口分析

### 2.1 HTTP 端点遍历与消费者体验

```
GET    /v1/events                       SSE 实时事件流
GET    /v1/ping                         心跳（HTTP 200）

── 消息 ──
GET    /v1/messages                     收件箱列表（分页）
GET    /v1/messages/:id                 获取单条消息
GET    /v1/messages/unread_count        未读数
GET    /v1/conversations/:uid           与某用户的会话
POST   /v1/messages                     发送消息
POST   /v1/messages/:id/read            标记已读
POST   /v1/conversations/:uid/read      标记整个会话已读
POST   /v1/messages/:id/reactions       添加反应
DELETE /v1/messages/:id/reactions       移除反应
PATCH  /v1/messages/:id                 编辑消息
DELETE /v1/messages/:id                 删除消息

── 文件 ──
GET    /v1/files                        文件列表（分页）
GET    /v1/files/:id                    获取文件元数据
GET    /v1/files/:id/url                获取下载 URL
GET    /v1/files/dl                     公共下载（HMAC 签名）
GET    /v1/files/shared-with-me         与我共享的文件
POST   /v1/files                        上传文件（multipart）
POST   /v1/files/upload-url             预签名上传
POST   /v1/files/complete               完成上传
PATCH  /v1/files/:id                    更新元数据
DELETE /v1/files/:id                    删除文件
POST   /v1/files/:id/shares             共享文件
DELETE /v1/files/:id/shares/:uid        取消共享

── 评论 ──
GET    /v1/comments                     评论列表（?target_type=file&target_id=xxx）
GET    /v1/comments/:id/replies         评论的回复
POST   /v1/comments                     创建评论
DELETE /v1/comments/:id                 删除评论
PATCH  /v1/comments/:id                 编辑评论
POST   /v1/comments/:id/like            点赞
DELETE /v1/comments/:id/like            取消点赞
POST   /v1/comments/:id/report          举报
POST   /v1/comments/:id/hide            隐藏
POST   /v1/comments/:id/unhide          取消隐藏
GET    /v1/comments/:id/reports         查看举报列表
POST   /v1/comments/reports/:id/resolve  处理举报

── 通知 ──
GET    /v1/notifications                通知列表（分页）
GET    /v1/notifications/unread_count   未读通知数
POST   /v1/notifications/:id/read       标记已读
POST   /v1/notifications/read-all       全部标记已读

── 黑名单 ──
GET    /v1/blocks                      黑名单列表
POST   /v1/blocks                      添加黑名单
DELETE /v1/blocks/:uid                 移除黑名单

── 搜索 ──
GET    /v1/search                      全局搜索

── 偏好 ──
GET    /v1/notification-preferences    获取通知偏好
PUT    /v1/notification-preferences    设置通知偏好
```

**缺口 2a：缺少 `GET /v1/me` — 消费者不知道自己的 uid。** 客户端无法从 API 获取当前认证用户的 uid。必须在客户端保留登陆时 SSO 返回的 uid，或从 token 中解析。

**缺口 2b：缺少 `GET /v1/messages/:id/reactions` — 查看谁对消息做出了反应。** 只有添加/删除反应的端点，没有列出反应的端点。消费者在客户端显示"张三、李四和另外 3 人点赞"时无法获取这些数据。

**缺口 2c：缺少 `GET /v1/files/:id/shares` — 查看文件被分享给了谁。** 有 `Share`、`Unshare`、`ListSharedWithMe`，但没有 `GET /v1/files/:id/shares`（查看这个文件的分享列表）。

**缺口 2d：缺少 `GET /v1/conversations` — 会话列表。** 当前有 `Inbox`（按消息聚合的收件箱）和 `Conversation`（与指定用户的会话）。但没有纯会话列表（显示最近活跃的 N 个对话方）。

**缺口 2e：inbox 不区分"已读"的会话。** 消费者需要自己跟踪每个 `to_uid` 的 `read_at` 状态并过滤。

### 2.2 HTTP 状态码一致性

| 场景 | 当前状态码 | 预期 |
|------|-----------|------|
| 成功创建 | 201 Created ✅ | 201 |
| 成功操作（无返回体） | 204 No Content ✅ | 204 |
| 成功获取 | 200 OK ✅ | 200 |
| 列表成功 | 200 OK ✅ | 200 |
| 请求验证失败 | 400 Bad Request ✅ | 400 |
| 认证失败 | 401 Unauthorized ✅ | 401 |
| 权限不足 | 403 Forbidden ✅ | 403 |
| 未找到 | 404 Not Found ✅ | 404 |
| 幂等冲突 | 409 Conflict ✅ | 409 |
| 限流 | **缺少**（可能 429 但没看到测试断言） | 429 |
| 超时 | 500（应为 504）❌ | 504 |
| 数据校验失败（如嵌套太深） | 400 ✅ | 400 |

**缺口 2f：缺少 429 状态码测试验证。** 检查 ratelimit 中间件的测试——是否验证了返回的 HTTP 状态码是 429？

`middleware/ratelimit.go` 中：

```go
c.AbortWithStatusJSON(http.StatusTooManyRequests, rateLimitErrorBody{...})
```

所以 429 是实现的 ✅。但需要确认测试覆盖。

### 2.3 响应格式不一致

**缺口 2g：列表响应不一致——`files` 返回 `{ "files": [...], "next_cursor": "..." }`，但 `notifications` 返回 `{ "notifications": [...], "users": {...}, "next_cursor": "..." }`。**

消费者需要为每个列表端点编写不同的解析逻辑。建议统一为：

```json
{ "data": [...], "users": {...}, "next_cursor": "..." }
```

但当前 `httpx.OK` 包装器统一了最外层 `{"data": ...}`，响应体形状比较一致。

### 2.4 分页参数命名一致性

当前所有列表端点使用：
- 请求：`?cursor=<uuid>&limit=<n>`
- 响应：`{ ..., "next_cursor": "<uuid>" }`

✅ 高度一致。但注意**排序方向**（第 4 次分析中提到）——不同端点使用不同排序方向且不编码在 cursor 中。

---

## 3. 备份与灾难恢复策略分析

### 3.1 当前状态

**数据库：** Postgres 无自动备份配置。`deployments/docker-compose.yml` 中无备份卷或定时备份。
**文件存储：** Local 或 S3——Local 无备份，S3 依赖 AWS/MinIO 的版本控制和跨区域复制。
**配置：** `configs/config.yaml` 有默认值，但敏感值（密码、key）通过环境变量传入——这些 env 无备份策略。

### 3.2 RPO/RTO 评估

| 组件 | RPO（数据丢失窗口） | RTO（恢复时间） |
|------|-------------------|----------------|
| Postgres | last backup | DB restore + migrate |
| S3/MinIO 文件 | 取决于 S3 版本控制 | 取决于副本策略 |
| Local 文件 | last backup 或全部丢失 | 恢复文件系统 |
| 配置 | 代码版本控制（git） | 重新部署 |

**评估：** 这是**未定义的**。没有备份策略文档，没有恢复 SOP，没有定期恢复测试。

### 3.3 建议的备份策略

**数据库（每日 + 持续归档）：**
```bash
# 每日基础备份
pg_dump -Fc --no-owner -f /backups/aero-id-$(date +%Y%m%d).dump

# WAL 归档（启用 postgresql.conf archive_mode=on + archive_command）
archive_command: 'rsync -a %p backup-user@backup-server:/wal-archive/%f'
```

**文件存储：**
- S3：启用 Bucket Versioning + Cross-Region Replication（CRR）
- Local：rsync 或 restic 备份到远程存储

**配置：**
- 所有 `.yaml` 文件在 git 中（不含敏感值） ✅
- 敏感值在 Secret Manager（AWS Secrets Manager / HashiCorp Vault）

### 3.4 无灾难恢复手册

**缺口：** 没有 `docs/DISASTER_RECOVERY.md`。没有文档说明以下场景的恢复步骤：
- "Postgres 数据库被误删"
- "服务器磁盘故障"
- "S3 bucket 被意外清空"
- "签署密钥泄露"

---

## 4. 配置审计：逐字段核对

### 4.1 `configs/config.yaml` 全字段

```yaml
server:
  http:
    port: 8080
    shutdown_timeout: 30s
  grpc:
    port: 9090
    reflection_enabled: false

sso:
  endpoint: "127.0.0.1:9000"
  insecure: true
  timeout: 5s
  # 缓存配置
  cache:
    token_ttl: 60s
    token_stale_ttl: 300s
    user_ttl: 5m
    user_stale_ttl: 10m

database:
  host: "localhost"
  port: 5432
  user: "aero"
  password: "aero"
  dbname: "aeroid"
  sslmode: "disable"
  max_open_conns: 50
  max_idle_conns: 10
  conn_max_lifetime: 30m

storage:
  type: "local"
  local:
    base_path: "/tmp/aero-storage"
  s3:
    bucket: "aero-id"
    region: "us-east-1"
    endpoint: "http://localhost:9001"    # MinIO 地址
    force_path_style: true
    upload_buffer_size: 5242880
  gc:
    enabled: false
    interval: 1h
    batch_size: 500
  max_upload_bytes: 1073741824      # 1GB

signing_key: "change-me-please-very-long-random"

outbox:
  relay_interval: 5s
  batch_size: 100
  max_retries: 20
  webhook:
    url: ""
    signing_key: ""
    timeout: 10s

rate_limit:
  enabled: false
  rps: 20
  burst: 40

idempotency:
  ttl: 24h

readiness:
  critical: ["database", "sso"]
  timeout: 3s

tracing:
  enabled: false
  endpoint: ""
  insecure: true
  sample_ratio: 0
  service_name: "aero-id"
  service_version: "dev"

logger:
  level: "info"
  format: "console"
  output: "stdout"
```

### 4.2 配置审计发现

**缺口 4a：`signing_key` 默认值未拒绝。** 如果运维忘记修改，所有 HMAC 签名可被伪造。建议在 `config.Load` 中强制检查。

**缺口 4b：`database.password` 默认值 `"aero"`。** 生产环境应拒绝默认值。

**缺口 4c：`storage.max_upload_bytes: 1GB` 对多数场景偏大。** 建议默认设为 100MB。

**缺口 4d：`rate_limit.enabled: false`。** 默认关闭意味着生产部署默认无限流。建议默认设为 true（`rps: 20`）。

**缺口 4e：`sso.insecure: true`。** 默认跳过 TLS。对于生产部署打 WARN 日志。

**缺口 4f：`outbox.webhook.url` 和 `signing_key` 空值。** 如果 webhook URL 为空，webhook sink 应该静默跳过——但需要确认代码是否处理了空值情况。

**缺口 4g：缺少 `statement_timeout` 配置。** 没有控制单个 SQL 语句的最大执行时间。

**缺口 4h：缺少 `cache.max_entries`。** 没有控制内存缓存大小的配置。

**缺口 4i：缺少 `audit_events.ttl`。** 审计日志的生命周期不可配置。

**缺口 4j：`server.admin.enabled` 不存在。** 没有管理端口的配置。

### 4.3 环境变量映射完整性

```
AERO_ID_SIGNING_KEY                      → 映射 ✅
AERO_ID_DATABASE_PASSWORD                → 映射 ✅
AERO_ID_S3_ACCESS_KEY                    → 存在但不在 yaml 中（通过 AWS SDK 链）
AERO_ID_S3_SECRET_KEY                    → 存在但不在 yaml 中
AERO_ID_SSO_ENDPOINT                     → 映射 ✅
AERO_ID_STORAGE_TYPE                     → 映射 ✅
AERO_ID_OUTBOX_WEBHOOK_URL              → 映射 ✅
AERO_ID_OUTBOX_WEBHOOK_SIGNING_KEY       → 映射 ✅
```

**缺口 4k：`AERO_ID_S3_ACCESS_KEY`/`_SECRET_KEY` 不在 yaml 中。** 通过 AWS SDK 的默认凭证链获取。这意味着如果 S3 是自管理的 MinIO，运维人员必须知道 SDK 的环境变量名（`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`）而不是 aero-id 的项目环境变量模式。

---

## 5. 滥用检测与限流缺口分析

### 5.1 当前限流设计

- **位置：** `internal/pkg/ratelimit/ratelimit.go`
- **算法：** Token Bucket（per-actor-uid，匿名请求 per-IP）
- **默认：** 20 RPS / 40 Burst（但 `enabled: false`）
- **范围：** `/v1/*` 所有端点共享一个限流器
- **gRPC：** 同样通过 `RateLimitInterceptor`

### 5.2 限流缺口

**缺口 5a：端点级别限流不存在。** 所有端点共享同一个 bucket。攻击者可以通过以下方式绕过：
- 攻击 `/v1/search`（计算密集型）消耗大量 CPU，但 `/v1/messages/send` 的配额也被消耗光
- 攻击 `/v1/auth`（如果存在）占用登录配额的 token bucket，合法用户无法发送消息

**建议：** 按端点组分配独立的限流配额（如 search: 5 RPS、messages: 50 RPS、files: 10 RPS）。

**缺口 5b：全局限流不存在。** "全球用户"无总量控制。即使用户级限流（20 RPS/用户）阻止了单一用户的滥用，但 1000 个用户×20 RPS = 20,000 RPS 仍然可能压垮服务器。

**建议：** 添加全局并发限制（`max_concurrent_requests`）或全局 RPS 限制（`global_rps: 5000`），作为**最后一道防线**。

**缺口 5c：无基于内容的限流。** 用户可以快速连续发送大量消息。即使限流在 20 RPS，60ms/条——4 秒内可发送 80 条消息给 80 个不同的用户。这对于垃圾消息发送者来说是足够的速率。

**建议：** 添加基于目标的限流（per-recipient-uid 的限流，限制每 10 秒最多发 5 条消息给同一个用户）。

**缺口 5d：无 IP 黑名单 / 自动封禁。** 如果某个 IP 持续发送恶意请求（如大量 401 失败的 token），当前机制不会自动封禁 IP。

**建议：** 添加动态 IP 黑名单（基于错误率自动添加到内存中的封禁列表，过期后自动移除）。

### 5.3 特定滥用场景分析

| 场景 | 当前防护 | 缺口 |
|------|---------|------|
| 暴力破解 token | 限流（20 RPS） | 20 RPS 对暴力攻击仍太高（可尝试 20 个 token/s，10 分钟 12,000 个 token） |
| 批量注册（通过 SSO，aero-id 不控制） | 无 | aero-id 无法阻止 SSO 的滥用 |
| 垃圾消息（spam） | 无 | 无内容分析、无速率限制 per-recipient、无关键词过滤 |
| 枚举用户 | 无 | 错误消息可能泄露用户存在性信息 |
| 囤号（sockpuppet） | 无 | 单个 IP 对应多个 uid，无 cross-checks |
| Refund 攻击（幂等 key 滥用） | Idempotency key | TTL 24h 内的幂等保护；超过 TTL 后可以重放 |
| 超大文件上传 | `max_upload_bytes: 1GB` | 1GB 对多数场景太大。并发上传 10 个 1GB 文件可能导致磁盘满 |

---

## 6. 高价值扩展方向（全新方向，前七次未覆盖）

---

### 🎯 方向 A：移动推送通知（FCM / APNs）

**概念：** 在 SSE 实时推送之外，增加对移动设备推送通知（Firebase Cloud Messaging / Apple Push Notification Service）的支持。当用户不在线（SSE 断开）时，通过 FCM/APNs 将通知推送到移动设备。

**为什么需要它：**
1. **当前 SSE 推送只在 WebSocket 或 EventSource 连接时工作。** 用户关闭浏览器或网络切换时，SSE 连接断开，通知丢失。用户必须手动打开应用并拉取通知列表才能看到错过的通知。
2. **移动端不支持 SSE 的后台连接。** iOS/Android 的后台任务管理严格限制长连接。App 在后台时无法通过 SSE 接收实时通知。
3. **用户期望即时通知。** 对于消息类应用，用户期望收到消息时手机立刻弹出通知，而不是打开 App 后才看到。
4. **架构上合理。** 通知已经由出箱系统可靠地创建（`notifSink`）。FCM/APNs 集成可以作为另一个 outbox handler（`pushSink`），监听 `notification.created` 事件并推送到目标设备。

**实现要点：**
- 新增表 `device_tokens`：`(uid TEXT, platform TEXT, token TEXT, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`，主键 `(uid, platform, token)`
- API：`POST /v1/devices/register`（注册设备 token）、`DELETE /v1/devices/:token`（注销设备）
- 新 outbox handler `pushSink`：接收 `notification.created` 事件，查询目标 uid 的所有设备 token，通过 FCM/APNs API 推送
- FCM SDK：`firebase.google.com/go/v4/messaging`
- APNs：`github.com/sideshow/apns2` 或通过 Firebase 桥接
- 通知内容：`{ "title": "新消息", "body": "张三: 你好", "data": { "type": "message.new", "uid": "..." } }`
- 配置：`notifications.push.enabled: false`、`notifications.push.fcm.credentials_file: ""`、`notifications.push.apns.key_id: ""`、`notifications.push.apns.team_id: ""`
- 注意：不要在推送中泄露消息内容（可以考虑只推送"有新消息"，具体内容在 App 内加载）

---

### 🎯 方向 B：文件缩略图 / 预览生成（Thumbnail Pipeline）

**概念：** 为上传的图片、PDF、视频文件自动生成缩略图和预览。图片生成小/中/大三种缩略图；PDF 生成第一页预览；视频生成关键帧截图。

**为什么需要它：**
1. **当前文件列表只返回文件名和大小。没有预览图。** 用户上传了一张照片，在文件列表中只能看到文件名和 MIME type——看不到实际内容。在照片分享场景中这是巨大的 UX 缺口。
2. **与文件上传管道集成。** 可以使用与病毒扫描（第 6 次分析方向 B）和 EXIF 剥离（第 7 次分析方向 C）相同的 outbox 事件管道。上传完成 → `file.uploaded` 事件 → 缩略图生成 worker → 更新元数据。
3. **差异化优势。** 当前即时通讯/文件平台很多，但原生文件预览可以减少对第三方服务的依赖。
4. **S3 上已有最小化元数据。** 缩略图需要额外的存储空间，但 S3/MinIO 的存储成本很低。每个上传的文件生成 3 个缩略图（200×200, 600×600, 1200×1200）的额外存储成本可忽略。

**实现要点：**
- 新包 `internal/thumbnail/`：使用 Go 的 `image/jpeg`, `image/png`, `image/gif`, `golang.org/x/image/draw`，或调用外部工具（`ffmpeg` + `ImageMagick`）
- 缩略图存储在同一 storage backend 中，使用约定 key：`{original_key}/thumb_{size}.jpg`
- `files` 表增加 `thumbnail_map JSONB`：`{"small": "key/thumb_200.jpg", "medium": "key/thumb_600.jpg", "large": "key/thumb_1200.jpg"}`
- outbox 事件：`file.uploaded` → thumbnail worker 消费
- 支持的格式：
  - 图片（JPEG/PNG/GIF/WebP）：`image` 标准库
  - PDF：需要 `pdfcpu` 或外部工具（`pdftoppm`）
  - 视频（MP4/WebM）：需要 `ffmpeg`（通过 `exec` 调用）
- API 扩展：`GET /v1/files/:id/thumbnail?size=small|medium|large` 或 `GET /v1/files/:id?include_thumbnails=true`
- 注意：缩略图生成是 CPU 密集型——建议使用 worker pool（`semaphore.Weighted`），限制并发度

---

### 🎯 方向 C：垃圾消息检测管道（Spam Detection）

**概念：** 在消息发送和评论创建过程中加入自动垃圾消息检测，基于规则（关键词、频率、模式）和/或 ML（Bayesian 分类器）识别并处理可疑内容。

**为什么需要它：**
1. **当前没有任何内容过滤。** 用户可以发送任意内容的消息和评论。没有关键词过滤、没有链接黑名单、没有重复内容检测。平台一旦开始扩张，垃圾消息会成为最紧急的问题。
2. **与 block/举报功能互补。** 当前用户可以举报评论（`ReportComment`）和拉黑用户（`Block`）。但这些都是**被动防御**——用户在看到垃圾后才会举报。自动检测可以在用户看到之前阻止。
3. **可以使用 outbox 管道。** 创建一个新的 outbox handler `spamSink`，消费 `message.sent` 和 `comment.created` 事件：
   - 同步模式：发送/创建时检查（阻塞请求，延迟 5-50ms）
   - 异步模式：发送/创建后检查，发现可疑后标记或撤回（不阻塞请求，但需要撤回机制）
4. **渐进式实施。** 从简单的规则开始（同一内容发送给 >5 个用户、消息中包含已知恶意域名、纯链接消息），逐步引入 ML。

**实现要点：**
- `spam_rules` 表（或纯配置）：规则定义（关键词、正则、频率阈值）
- 规则类型：
  - **重复发送**：同一内容在 60s 内发送给 >N 个不同用户
  - **恶意链接**：URL 与预设黑名单匹配
  - **高频联系人**：在 T 时间内添加超过 N 个新联系人（通过发送消息）
  - **关键词**：与预设关键词列表匹配
  - **纯链接**：消息只包含 URL 且用户历史消息均类似
- `messages` 表增加 `spam_score FLOAT` 或 `spam_status TEXT`（`clean`/`suspicious`/`blocked`）
- 动作：`blocked` 消息不投递给收件人、不写入收件人的 inbox
- 通知：如果消息被标记为 spam，发送方收到通知（"你的消息因违反规则被拒绝"）
- 配置：`spam.enabled: false`、`spam.action: "flag" | "block"`、`spam.max_messages_per_minute: 10`
- 注意：垃圾检测要考虑误报（false positive）。建议初始动作为 `flag`（标记而不阻止），人工复核后再决定是否 `block`

---

### 🎯 方向 D：URL 链接预览（Link Unfurling / OG Metadata）

**概念：** 当消息包含 URL 时，自动提取该 URL 的 Open Graph（OG）元数据（标题、描述、图片、站点名），并在客户端显示为富文本预览卡片。

**为什么需要它：**
1. **消息中包含链接时，富文本预览是即时通讯的标准功能。** Telegram/Slack/Discord 都在用户发送链接后自动显示预览卡片。当前链接作为纯文本显示，没有上下文。
2. **实现架构简洁。** 与缩略图管道（方向 B）和病毒扫描（第 6 次方向 B）一样，可以作为 outbox handler `unfurlSink` 消费 `message.sent` 事件，异步获取 OG 元数据。
3. **数据可以缓存。** URL 的 OG 元数据相对稳定（除非页面更新）。可以缓存（`url_unfurls` 表，TTL 7 天），降低对外部服务的请求量。
4. **不需要外部服务。** Go 的 `net/http` + `golang.org/x/net/html` 即可解析 OG meta tags。无需调用第三方 link preview API。

**实现要点：**
- 新表 `url_unfurls`：`(url_hash TEXT PRIMARY KEY, og_title TEXT, og_description TEXT, og_image TEXT, og_site_name TEXT, fetched_at TIMESTAMPTZ)`
- outbox handler `unfurlSink`：消费 `message.sent` → 从 `content` 中提取 URL（简单正则 `https?://[^\s]+`）→ GET URL → 解析 HTML OG meta tags → 写入 `url_unfurls` 表
- `messages` 表增加 `unfurls JSONB`：`[{"url": "...", "og_title": "...", "og_image": "..."}]`
- API 变更：`GET /v1/messages/:id` 返回的 message 中已包含 `unfurls` 字段（如果有）
- 超时：每个 URL 的 fetch 有独立超时（如 3s）
- 隐私：URL 预览 fetch 的 IP 可能泄露给目标网站运营者——建议使用代理或用 `?no_unfurl=1` 参数允许用户选择不使用
- 配置：`unfurl.enabled: true`、`unfurl.timeout: 3s`、`unfurl.max_urls_per_message: 5`
- 注意：不要 fetch `internal`（RFC 1918）地址，避免 SSRF。建议做 IP 范围检查

---

### 🎯 方向 E：用户账户删除 & 数据擦除（GDPR 合规）

**概念：** 实现端到端的数据擦除流程——用户请求删除账户后，出于非法律义务的数据都在可配置的时间（如 30 天）后自动清除。

**为什么需要它：**
1. **GDPR / CCPA 等法规要求。** 用户有权要求删除他们的个人数据。平台必须能够在合理的时间内删除所有相关数据。这是法律合规要求，不是可选功能。
2. **当前有软删除但没有账户级删除。** 单独的"删除消息"（软删除）和"删除文件"（软删除）都存在。但没有一个"删除我所有数据"的统一流程。
3. **删除 ≠ 立即删除。** 通常：
   - 用户请求删除 → 账户标记 `pending_deletion`
   - 30 天宽限期（用户可以取消）
   - 宽限期后 → 执行硬删除（所有 messages、files、comments 等）
4. **可以利用现有 GC sweeper。** 文件 GC 已经遍历软删除的文件并清理 blob。可以扩展为删除所有用户的关联数据。

**实现要点：**
- `users_deletion` 表：`(uid TEXT PRIMARY KEY, requested_at TIMESTAMPTZ, scheduled_for TIMESTAMPTZ, canceled BOOLEAN DEFAULT FALSE)`
- API：
  - `POST /v1/me/delete`（请求删除）
  - `POST /v1/me/delete/cancel`（取消删除）
  - `GET /v1/me/delete/status`（查看删除状态）
- 删除流程：
  1. 标记所有用户的文件为 `deleted_at = now()`
  2. GC 清理 blob（存储引用计数递减到 0 后删除 blob）
  3. 硬删除所有 messages（`DELETE FROM messages WHERE from_uid = $1 OR to_uid = $1`）
  4. 硬删除所有 comments
  5. 硬删除所有 notifications
  6. 硬删除所有 audit_events（但合规要求可能需要保留某些记录 → 用 `meta.deleted_uid` 替换 uid 引用）
  7. 删除 `user_blocks` 记录
  8. 删除 `file_shares` 记录
  9. 删除 `message_reactions` 记录
- 数据留存策略：**不要**删除可能影响其他用户的数据：
  - 已发送给其他人的消息：保留消息但匿名化 `from_uid`（替换为 `deleted_user`）
  - 评论：保留评论但匿名化 `owner_uid`（替换为 `deleted_user`）
  - 文件：如果文件已被分享，保留 blob 但取消分享授权
- 配置：`data_retention.deletion_grace_period: 30d`、`data_retention.anonymize_uid: "deleted_user"`、`data_retention.keep_messages_after_deletion: true`
- 审计：所有删除操作写入审计日志（`action: "data.purge"`）

---

## 7. 本轮总结：八篇分析的全部覆盖

```
八篇审查 = 跨 8 个维度的 40 个高价值方向

      维度              覆盖篇数
  ──────────────────────────────
  产品/方向扩展            8/8
  安全/滥用防护           4/8
  运维/部署/DR            3/8
  API 设计/消费者缺口       3/8
  数据库/查询/索引          2/8
  测试金字塔               1/8
  中间件链                 1/8
  并发模型/缓存             1/8
  数据流追踪                1/8
  配置审计                 1/8
  依赖健康                 1/8
  文档完备性               1/8

  八篇累计覆盖约 40 个高价值方向、80+ 个边界/性能/缺口发现
```
