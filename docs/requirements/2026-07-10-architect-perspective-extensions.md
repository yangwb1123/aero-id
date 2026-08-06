# aero-id — 架构师视角：五组代码级证据驱动的扩展方向

> 日期：2026-07-10
> 视角：资深架构师 + 产品经理
> 方法：全量代码扫描（`internal/` 全部 ~45 个包、22 份迁移 SQL、双传输层 handler、`cmd/`、`configs/`、`api/proto/`），交叉比对 `docs/requirements/` 下 30+ 份、`docs/REVIEW_*` 下 8 份现有分析文档
> 承诺：以下 5 个方向在现有分析中 **未被系统性覆盖**，每个方向均有明确代码级证据

---

## 现状快照

| 领域 | 已交付能力 | 成熟度 |
|------|-----------|--------|
| 文件 | CRUD、分块上传、MIME 白名单、checksum 去重、S3 预签名直传、显式共享、存储 GC 回收、元数据 PATCH | ✅ |
| 消息 | 收发、软删除、编辑、已读回执、Reactions、系统消息、会话视图、未读数 | ✅ |
| 评论 | 创建/列表/回复、二级嵌套规则、点赞、举报+审核队列、编辑、自隐藏 | ✅ |
| 通知 | Outbox 驱动、SSE 实时推送、已读追踪、偏好(静音时段)、合并、限速、Block 感知 | ✅ |
| 用户拦截 | 方向性 Block、阻止私信 + 通知 | ✅ |
| 全文搜索 | PostgreSQL tsvector FTS、CJK ILIKE 回退、三实体联合搜索 | ✅ |
| 可观测性 | RED 指标、OTel Tracing、SSO 健康仪表盘、Outbox 仪表盘、Per-dependency Readiness | ✅ |
| 正确性 | 事务性 Outbox、幂等键、原子 reply_count、事务性 MarkRead | ✅ |
| 限流 | Per-actor / Per-IP token bucket、HTTP 429 + gRPC ResourceExhausted | ✅ |
| 双传输 | 38 个 gRPC RPC 与 HTTP 端点完全对齐 | ✅ |
| 管理 | 审计日志查询 + audit.read 权限校验 | ✅ |
| 安全加固 | SSO 断路器 + Stale-cache + Singleflight + Retry + Keepalive + TLS/mTLS + SPKI 证书锁定 | ✅ |

> 代码库工程成熟度很高。以下方向是代码级证据确凿的「未被触及」的缺口。

---

## 方向一：会话列表（Threaded Inbox）—— 收件箱是消息列表，不是会话列表

### 代码证据

**核心缺陷**：`ListInbox` 当前返回的是一个**扁平的消息列表**（所有发给我、按时间倒序排的消息），而非按发送方分组的会话列表。

```go
// internal/message/message.go:29
ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
```

实现：

```go
// internal/message/repository.go:89
q := r.db.WithContext(ctx).Where("to_uid = ?", toUID)
// ... ORDER BY created_at DESC, id DESC LIMIT n+1
```

这意味着如果一个用户 A 给你发了 20 条消息，返回的前 20 条记录全是用户 A 的，用户 B/C/D 的消息被完全淹没。客户端无法展示 WhatsApp/Telegram 风格的会话列表（每个联系人一行：最新消息预览 + 该联系人未读数）。

**对等的 gRPC RPC**：

```protobuf
// api/proto/aero_id/v1/aero_id.proto
message InboxRequest { Page page = 1; bool unread_only = 2; }
```

同样返回扁平消息列表。

**现有能力但无法利用**：`CountUnread` 返回全局未读总数，但无法按发送方分组。

### 为什么需要它

| 维度 | 分析 |
|------|------|
| **产品** | 任何即时通讯应用的第一屏就是会话列表。当前 API 让移动端/Web 无法实现这个最基础的 UX。 |
| **性能** | 客户端要模拟会话列表需要拉取全部消息在本地按 `from_uid` 分组聚合 —— O(n) 内存 + 翻页越多越差 |
| **竞品对标** | 所有主流通讯平台（WhatsApp、Telegram、微信）的第一层级都是会话列表，而非消息列表 |

### 实现思路

- 新增 `ListConversations(ctx, uid, page)` 端点（HTTP `GET /v1/conversations`，gRPC 相应 RPC）
- SQL 查询：`SELECT from_uid, MAX(created_at) AS latest, ... FROM messages WHERE to_uid = ? AND deleted_at IS NULL GROUP BY from_uid ORDER BY latest DESC`
- 每条记录包含：`from_uid`、`latest_message_preview`、`unread_count`、`latest_message_created_at`
- 复用已有的 `idx_messages_inbox` 索引

### 预估工作量

**低-中**。新增一个 repository 方法 + service 方法 + HTTP/gRPC handler，复用现有数据结构，不需新迁移。

---

## 方向二：UserCache 无界增长 —— 生产环境的内存泄漏隐患

### 代码证据

`UserCache` 是一个简单的 `map[string]userEntry`，带 TTL 过期但没有**任何淘汰机制**：

```go
// internal/sso/user_cache.go:24-28
type UserCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	items   map[string]userEntry // ← 无界增长
	fetcher Fetcher
}
```

`GetMany` 在缓存未命中时填充，但**从不删除过期条目**：

```go
// internal/sso/user_cache.go:49-74
func (c *UserCache) GetMany(ctx context.Context, uids []string) (map[string]User, error) {
	// ... 检查时过滤过期条目（条件通过），但没过期的条目留在 map 中
	if e, ok := c.items[u]; ok && now.Before(e.expiry) {
		out[u] = e.user
	} else {
		missing = append(missing, u) // 加入获取列表
	}
	// ... 从 SSO 获取后写入 map，但从不清理
	c.mu.Lock()
	for u, info := range fetched {
		c.items[u] = userEntry{user: info, expiry: exp}
	}
	c.mu.Unlock()
}
```

对比 `TokenCache` 有 `RunCleaner` 定期清理，但 `UserCache` **没有**：

```go
// internal/sso/client.go:126
stop := c.tokenCache.RunCleaner(30 * time.Second)  // TokenCache 有清理
// UserCache 没有任何等价调用
```

### 风险量化

| 场景 | 每日唯一用户数 | 30 天后条目数 | 每条目约 ~200B | 总内存 |
|------|--------------|--------------|--------------|--------|
| 小规模 | 1 万 | 30 万 | 30 万 | ~60 MB |
| 中规模 | 10 万 | 300 万 | 300 万 | ~600 MB |
| 大规模 | 100 万 | 3000 万 | 3000 万 | ~6 GB |

加上 `UserCache` 是 `sync.RWMutex` 互斥访问，大 map + 高并发 = 锁竞争 + GC 压力。

### 为什么需要它

| 维度 | 分析 |
|------|------|
| **稳定性** | 长期运行的实例会因 map 膨胀不可逆转地消耗内存，直到 OOM。这不是概率问题，是时间问题。 |
| **成本** | 没有淘汰机制意味着缓存不会自然缩小。每晚的流量波谷期也无法释放。 |
| **一致性** | 当前已有的 `TokenCache.RunCleaner` 模式可直接复用，修复成本极低。 |

### 实现思路

- 为 `UserCache` 增加 `RunCleaner(interval)` 方法，定期扫描并删除 `time.Now().After(e.expiry)` 的条目
- 或者在 `GetMany` 的写入路径中概率性触发一次轻量清理（每次 1% 概率采样清理 10 个过期条目）
- 可选的 LRU 改造：用 `container/list` + map 实现，淘汰最久未访问的条目

### 预估工作量

**低**。增删 ~30 行代码。纯本地修改，无需新迁移、新配置或新端点。

---

## 方向三：Admin 运营操作面 —— 数据库里有表，API 上无路

### 代码证据

`domain/admin.go` 定义了完整的角色权限枚举：

```go
// internal/domain/admin.go:5-11
const (
	PermContentModerate = "content.moderate" // 审核评论
	PermAuditRead       = "audit.read"       // 查询审计日志
	PermUserBan         = "user.ban"         // 封禁用户  ← 从未被使用
	PermSystemConfig    = "system.config"    // 系统配置  ← 从未被使用
)
```

`migrations/000022_admin_roles` 创建了 `roles` 和 `user_roles` 表，并预置了 `moderator` 和 `admin` 角色：

```sql
INSERT INTO roles (code, name, permissions) VALUES
    ('moderator', 'Moderator',   ARRAY['content.moderate', 'audit.read']),
    ('admin', 'Administrator', ARRAY['content.moderate', 'audit.read', 'user.ban', 'system.config'])
ON CONFLICT (code) DO NOTHING;
```

`middleware/permission.go` 实现了 `RequirePermission` 中间件，可以按权限字符串拦截请求。

但 `admin.go`（HTTP handler）目前**只实现了一个端点**：

```go
// internal/handler/http/admin.go:25
func (h *AdminHandler) RegisterRoutes(r Routes) {
	r.Auth.GET("/admin/audit-log", h.ListAudit) // ← 唯一的 admin 端点
}
```

并且 `ListAudit` 是自己硬编码检查 `audit.read`，没有复用 `RequirePermission` 中间件：

```go
// internal/handler/http/admin.go:44
allowed, err := h.adminRepo.HasPermission(c.Request.Context(), actor, "audit.read")
```

权限定义了、表建了、中间件写了，但：
- **没有角色管理 API**（创建/更新/删除角色，列出所有角色）
- **没有用户授权 API**（授予/撤销用户角色，列出某角色下的所有用户）
- **没有用户封禁 API**（`PermUserBan` 完全无代码使用）
- **没有系统配置 API**（`PermSystemConfig` 完全无代码使用）

### 为什么需要它

| 维度 | 分析 |
|------|------|
| **运维** | 没有角色管理 API，运营人员必须直接连数据库执行 SQL 来授予管理员权限 —— 不安全、不可审计、不可自动化。 |
| **安全** | `PermUserBan` 和 `PermSystemConfig` 定义了但未被调用。任何代码审计都会标记为死权限 —— 要么实现它们，要么删除它们。 |
| **产品** | 社交平台必须有能力封禁滥用用户、分发运营角色。当前这套权限框架是「半成品」。 |

### 实现思路

- 新增 `GET /v1/admin/roles`、`POST /v1/admin/roles`、`DELETE /v1/admin/roles/:code`
- 新增 `GET /v1/admin/roles/:code/users`、`POST /v1/admin/roles/:code/users`（批量授予）、`DELETE /v1/admin/roles/:code/users/:uid`
- 新增 `POST /v1/admin/users/:uid/ban`、`POST /v1/admin/users/:uid/unban`（利用 `PermUserBan`）
- 上述端点均接入 `RequirePermission(PermSystemConfig)` 或 `RequirePermission(PermUserBan)` 中间件

### 预估工作量

**中**。3-4 个新端点 + repository + service，复用现有 GORM 表。至少需要 1 个新迁移（`user_bans` 表）。

---

## 方向四：系统广播 API —— 基础设施就绪，传输层缺失

### 代码证据

`SendSystem` 在 service 层完整实现：

```go
// internal/message/service.go:106-129
func (s *service) SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error) {
	// ...
	m := &domain.Message{
		FromUID:   domain.SystemSenderUID, // "system"
		Kind:      domain.MessageKindSystem,
		// ...
	}
	evt := outbox.NewEvent("message", m.ID.String(), "message.sent", ...)
	if err := s.messages.Create(ctx, m, evt); err != nil {
		return nil, apperr.Internal(...)
	}
	return m, nil
}
```

`MessageService` 接口暴露了 `SendSystem`：

```go
// internal/message/message.go:59
SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error)
```

但 **没有任何 HTTP 或 gRPC 端点暴露这个能力**。`SendSystem` 在现有代码中仅被内部组件调用，无传输层入口。

验证：搜索整个代码库，`SendSystem` 的调用来源只有 service 内部，没有 handler。

```bash
# grep -rn "SendSystem" internal/handler/  → 无结果
```

### 为什么需要它

| 维度 | 分析 |
|------|------|
| **运营** | 运营需要发送系统通知（服务变更、维护公告、安全提醒）。当前只能通过写 SQL 或写一个一次性脚本来实现。 |
| **产品** | 系统消息是基础的平台能力。基础设施已经就绪（Outbox 驱动通知 + SSE 推送），只缺一层 API 暴露。 |
| **安全** | 系统广播必须受 `PermSystemConfig` 权限保护（内建 `admin` 角色已有此权限）。 |

### 实现思路

- 新增 `POST /v1/admin/broadcast`（带 `RequirePermission(PermSystemConfig)` 中间件）
- POST body：`{ "to_uids": ["..."], "content": "...", "meta": {...} }`——支持发送给指定用户列表，或 `"all": true` 全量广播
- 全量广播通过分批遍历用户或通过通知通道实现，避免单请求阻塞
- 对应的 gRPC RPC：`admin.Broadcast(BroadcastRequest)`

### 预估工作量

**低**。复用 `SendSystem` + Outbox 基础设施，新增 1 个 handler + 1 个 repository 方法（如果支持全量广播则需分批查询所有 uid）。

---

## 方向五：Outbox 死信队列的可见性与管理 —— 静默数据丢失路径

### 代码证据

Outbox relay 在 `maxRetries` 耗尽后自动将事件标记为死信：

```go
// internal/outbox/repository.go:100-108
if retryCount >= maxRetries {
	updates["dead_letter"] = true
}
```

并从查询中排除死信事件：

```go
// internal/outbox/repository.go:73
Where("published_at IS NULL AND NOT dead_letter")
```

死信事件的**命运**：
- 停留在 `outbox_events` 表中，`published_at IS NULL` + `dead_letter = true`
- **没有任何指标暴露死信数量**（`MetricsSink` 只追踪 `ObserveOutboxPublished` 和 `ObserveOutboxFailed`，没有 `ObserveOutboxDeadLettered`）
- **没有任何端点可以查询、重新投递或清除死信**
- **没有任何告警机制**（比如死信数超过阈值触发通知）

验证 `outbox/relay.go` 的 MetricsSink 接口：

```go
// internal/outbox/relay.go:36-45
type MetricsSink interface {
	ObserveOutboxPublished(eventType string)
	ObserveOutboxFailed(eventType string)
	ObserveOutboxDrain(pending int, oldestAge, dur time.Duration)
	// ← 没有 ObserveOutboxDeadLettered
}
```

### 为什么需要它

| 维度 | 分析 |
|------|------|
| **数据完整性** | 死信事件代表无法投递的领域事件（如通知、webhook）。在当前架构中，这些事件静默丢失 —— 没有任何人知道。 |
| **可观测性** | 运营人员无法回答「过去一小时有多少事件投递失败？」「有多少被死信了？」等基础问题。 |
| **可恢复性** | 死信的原因可能是临时性的（如 webhook 端点短暂下线）。没有 API 就无法批量重新投递。 |

### 实现思路

- `MetricsSink` 增加 `ObserveOutboxDeadLettered(eventType string)`，在 `MarkFailed` 命中 `retryCount >= maxRetries` 时调用
- 新增 `GET /admin/outbox/dead-letters?page=` 端点（带 `PermSystemConfig` 权限），返回死信事件列表
- 新增 `POST /admin/outbox/dead-letters/:id/replay`（重新投递：清除 `dead_letter` + `published_at`，重置 `retry_count`）
- 新增 `POST /admin/outbox/dead-letters/:id/dismiss`（硬删除该死信事件）
- 可选的：添加 Prometheus 告警规则，当 `sum(rate(aero_id_outbox_dead_letter_total[5m])) > 0` 时告警

### 预估工作量

**低-中**。主要是暴露已有数据 + 2-3 个新端点。无需新迁移（`dead_letter` 列已存在）。

---

## 附：架构发现 —— 双重服务层代码未接入

在扫描中发现 `internal/` 下存在**两套平行的服务层实现**：

| 旧架构（已接入） | 新架构（未接线，死代码） |
|----------------|-----------------------|
| `internal/service/` — `file.go`, `message.go`, `comment.go`, `block.go`, `notification.go`, `search.go` | `internal/{file,message,comment,block,notification,search}/` — 各自独立 `service.go`, `handler_http.go`, `handler_grpc.go`, `repository.go` |
| `internal/handler/http/` — 各 handler | `internal/{file,message,...}/handler_http.go` |
| `internal/handler/grpc/` — 各 handler | `internal/{file,message,...}/handler_grpc.go` |
| 被 `internal/app/app.go` 导入和调用 | **未被任何包导入** |

新架构包（如 `internal/message/service.go`、`internal/file/service_crud.go`）完全从 `app.go` 解耦 —— 没有 import 路径指向它们。这意味着：
- 任何对新包的修改都不会影响运行中的服务（也不可能被测试覆盖）
- 这是一个「重构了一半」的状态：新代码写了但没有完成接线的最后一步
- 存在维护风险：如果两套代码都持续更新，它们将产生分歧

**建议**：要么完成接线（删除旧 `internal/service/` + `internal/handler/{http,grpc}/` 中对应实现，将 `app.go` 切换到新包），要么删除新包以避免混淆。当前状态是危险的「双重真实来源」。

---

## 优先级排序

| 方向 | 类型 | 优先级 | 工作量 | 理由 |
|------|------|--------|--------|------|
| 2. UserCache 淘汰机制 | 稳定性 / 性能 | **P0** | 低 | 生产环境长期运行必定触发的内存泄漏。修复成本极低，收益巨大。 |
| 1. 会话列表 (Threaded Inbox) | 产品功能 | **P1** | 低-中 | 基础通讯 UX 的缺失。Schema 已支持，只需一个端点。 |
| 3. Admin 运营操作面 | 功能 / 安全 | **P1** | 中 | 角色管理和用户封禁是运营标配。当前需要操作用 SQL 操作。 |
| 5. 死信队列可见性 | 可观测性 / 可靠性 | **P1** | 低-中 | 静默数据丢失路径。监控和恢复能力是生产必备。 |
| 4. 系统广播 API | 产品功能 | **P2** | 低 | 基础设施就绪，只缺 API 暴露。价值明确但非紧急。 |
| 附：双重服务层清理 | 架构债务 | **P2** | 中 | 当前没有 Bug，但长期维护风险。选择一种实现并清理另一种。 |

> **门槛**：以上方向每一个都可以独立交付（无需依赖其他方向）。建议 P0 方向二优先修复，然后按团队优先级推进 P1 方向。
