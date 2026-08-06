# aero-id — 资深架构师/产品经理视角：五项剩余高价值扩展方向

> **日期**：2026-07-11
> **视角**：资深架构师 + 产品经理
> **方法**：全量代码逐文件扫描 — `internal/` 全部 ~180 Go 源文件（23 个子包）、`migrations/` 22 份 SQL、`api/proto/`、`configs/`、`cmd/`、`deployments/`、`Makefile`。与 `docs/requirements/` 下 **100 份既有需求分析**、8 份 `docs/REVIEW_*` 审查文档、`ROADMAP.md`、`docs/CHANGELOG.md`、`docs/DECISIONS.md`、`docs/specs/aero-id-design.md` 做逐方向交叉比对。
> **约束**：不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角分析。

---

## 交叉验证声明

100 份既有需求分析 + 8 份架构审查 + ROADMAP + CHANGELOG + 设计文档累计覆盖了约 500+ 独立方向条目。以下 5 个方向在所有既有材料中 **零系统性覆盖**——从未作为独立方向进行完整的代码级证据链论证。每个方向对应一个当前代码中存在结构性盲区的平台级缺失能力。

---

## 方向一：开发者 API 生态 — API 密钥、速率计划分层、客户端 SDK 生成管线

### 问题陈述

aero-id 的 API 目前只能通过 **Bearer token（SSO 用户委托）** 消费。没有 API 密钥模型供第三方应用集成，没有速率计划分层（免费/专业/企业），没有官方的客户端 SDK 生成管线，也没有开发者门户。这意味着：

- 第三方应用无法代表用户调用 API（每次需要用户 SSO token）
- 无法对不同的消费者（移动 App vs 第三方集成 vs 内部服务）实施不同的速率限制和配额
- 每次 proto 变更后，调用方需要手动同步更新

当前 API 的消费方式假定**所有调用者是同一个前端**。

### 代码级证据

**证据 1：认证中间件只接受 Bearer token，无 API 密钥支持**

```go
// internal/middleware/auth.go:17-34
func Auth(v TokenValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.missing_token", "missing bearer token"))
			c.Abort()
			return
		}
		uid, err := v.ValidateToken(c.Request.Context(), token)
		if err != nil || uid == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.invalid_token", "invalid or expired token"))
			c.Abort()
			return
		}
		// ...
	}
}
```

认证路径只有一条：Bearer token → SSO validate。没有 `X-API-Key` header 解析，没有 API 密钥到 uid 的映射，没有密钥作用域（scope）检查。gRPC 拦截器也是同一个模式：

```go
// internal/handler/grpc/server.go — auth interceptor
// 同样只解析 Bearer token，无 API key 支持
```

**证据 2：速率限制器是全局的，无分层配额概念**

```go
// internal/config/config.go — RateLimitConfig
type RateLimitConfig struct {
	RPS   float64 // 全局 RPS，不区分调用者类型
	Burst int
	// 无 per-tier 配置、无 per-api-key 配置、无 per-operation 成本
}
```

速率限制器（`internal/pkg/ratelimit/ratelimit.go`）对所有经过认证的调用者使用同一个桶配置。没有"免费用户 10rps、付费用户 100rps"的区分。

```go
// internal/pkg/ratelimit/ratelimit.go:25-32
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket // key 是 uid 或 IP，但所有桶共享同一 RPS 配置
	// ...
}
```

**证据 3：Proto 定义是与代码绑定的，无 SDK 生成管线**

```protobuf
// api/proto/aero_id/v1/aero_id.proto 是唯一的 API 契约
// 它被签入 repo，但 make proto 生成的 .pb.go 仅用于 Go 服务端
// 没有 Makefile 目标生成 Python/JavaScript/Java/Kotlin 客户端
```

`Makefile` 中只有 `proto` 目标：

```makefile
# Makefile
proto:  # → 仅生成 Go 服务端 stubs
	protoc ... --go_out=... --go-grpc_out=...
	# 没有 --python_out、--js_out、--java_out、--openapi_out
```

**证据 4：没有 API 使用量统计或计费数据**

```bash
$ grep -rn "api_call\|usage_count\|request_count\|meter" internal/ --include='*.go' | grep -v "_test\|\.pb\."
# → 零结果
```

没有中间件或拦截器记录每个 uid/API key 的调用次数。即使想做按量计费或用量告警，也没有数据基础。

**证据 5：配置中没有"开发者应用"相关模型**

```go
// internal/config/config.go — 配置结构体
type Config struct {
	App        AppConfig
	Server     ServerConfig
	Database   DatabaseConfig
	SSO        SSOConfig
	Storage    StorageConfig
	Tracing    TracingConfig
	RateLimit  RateLimitConfig
	Outbox     OutboxConfig
	SigningKey string
	Log        LogConfig
	// 无 APIKeys、无 DeveloperApps、无 RateTierConfig
}
```

### 架构师视角

当前 API 的消费模式假设**调用者 = 浏览器/移动 App = SSO 认证用户**。这限制了 aero-id 的集成能力。要成为真正的平台，需要：

1. **API 密钥模型**：
   - `api_keys` 表：`(id, uid, name, key_hash, scopes[], tier, enabled, created_at, expires_at)`
   - 认证中间件增加 `X-API-Key` header 解析路径，哈希匹配后注入对应的 uid 和 tier
   - 作用域（scope）限制密钥可调用的 API 范围（如 `messages:read`、`files:write`）

2. **速率计划分层**：
   - 在 `RateLimitConfig` 中增加 `TierDefaults map[string]TierConfig`，每个 tier 有独立的 RPS/Burst
   - `rate_plans` 表存储 `(tier_name, rps, burst, daily_api_calls, storage_quota_bytes)` 
   - 限流器在 key 化时加入 tier 维度：`bucketKey = tier + ":" + uid/IP`

3. **客户端 SDK 生成管线**：
   - Makefile 增加 `make sdk` 目标，为常见语言生成类型安全的客户端
   - 生成的 SDK 内置重试、令牌管理、错误映射
   - 使 proto 成为「单一事实来源」，消除手动同步

4. **API 用量统计**：
   - gRPC 拦截器 / HTTP 中间件在每个请求后异步写 `api_usage` 行（或 Prometheus counter）
   - `GET /v1/me/api-usage` 暴露当前间隔的调用量和配额剩余

### 产品经理视角

**用户价值**：间接但战略性。对于最终用户，看不到 API 密钥。但对于业务来说，API 生态是**平台化的分水岭**：

- **场景一**：一个第三方自动化工具想代表用户发送消息。没有 API 密钥，用户必须分享自己的 SSO token——安全噩梦。有了 API 密钥，用户可以创建有限作用域的密钥，随时撤销。
- **场景二**：一个企业客户想批量导入文件。没有速率分层，所有用户共享同一个 RPS 限制——企业用户和免费用户抢资源。
- **场景三**：移动端团队每次 proto 变更需要手动更新 HTTP 客户端代码。有了 SDK 生成，CI 自动发布新版本客户端。

**优先级建议**：API 密钥是 P1（第三方集成的门槛），速率分层是 P2（可与商业模型一起推出），SDK 管线是 P3（效率提升而非功能缺失）。

---

## 方向二：SSO 账号删除生命周期与孤儿内容处理

### 问题陈述

aero-id **不存储账号**——账号属于外部 SSO。但当 SSO 删除（或禁用）一个用户时，aero-id 完全不知情。所有该用户的内容（文件、消息、评论）永远留在系统中，没有任何级联清理、内容重定向或合规性擦除机制。

具体缺失：
1. **无 SSO 到 aero-id 的删除回调/Webhook** — SSO 删除账号后不通知 aero-id
2. **无数据级联策略** — 用户删除后，其文件、消息、评论应如何处理？（保留？软删？硬删？转移？）
3. **无合规擦除管线** — GDPR Right to Erasure / CCPA Deletion Request 无法自动化
4. **无内容重定向（Re-parenting）** — 共享文件、群组内容在所有者被删除后成为孤儿

### 代码级证据

**证据 1：SSO 客户端仅提供 ValidateToken 和 GetUsers，无订阅/回调能力**

```protobuf
// api/proto/sso/v1/sso.proto
service SSO {
  rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);
  rpc GetUsers(GetUsersRequest) returns (GetUsersResponse);
  // 无 WatchUsers、无 OnUserDeleted、无 AccountStatusChanged RPC
}
```

SSO 集成是**拉模式**（aero-id 主动调用 SSO），没有**推模式**（SSO 通知 aero-id 用户状态变更）。SSO 删除用户后，aero-id 的缓存可能继续服务陈旧数据直到 TTL 过期，但**即使缓存过期，系统也只是认为用户"不存在"——不会触发任何清理动作**。

**证据 2：所有实体以 uid 字符串引用外部账号，且无"已删除"标记机制**

```go
// internal/domain/file.go
type File struct {
	OwnerUID   string // 对外部 SSO uid 的硬引用
	// 无 OwnerDeletedAt、无 OwnerStatus、无 ReAssignedTo
}

// internal/domain/message.go
type Message struct {
	FromUID string // 发送方
	ToUID   string // 接收方
	// 两个 uid 都可能指向已删除的 SSO 账号
}

// internal/domain/comment.go
type Comment struct {
	OwnerUID string // 评论作者
}
```

没有任何实体记录 uid 对应的账号是否存在或活跃。当 SSO 的 `GetUsers([...uids])` 返回空映射时，系统将其视为"查询无结果"，而不是"这些用户可能已被删除"。

**证据 3：用户缓存 TTL 过期后静默消失——不会触发删除事件**

```go
// internal/sso/user_cache.go
type UserCache struct {
	mu    sync.RWMutex
	users map[string]*cacheEntry // TTL 过期后条目被驱逐，无回调
	ttl   time.Duration
}

func (c *UserCache) Get(ctx context.Context, uid string) (*User, error) {
	c.mu.RLock()
	entry, ok := c.users[uid]
	c.mu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < c.ttl {
		return entry.user, nil // 过期后不 Get 就静默消失
	}
	// cache miss → 去 SSO 查 → 如果 SSO 返回"用户不存在"，只影响本次请求
}
```

缓存过期后的行为是"下次请求时发现用户不存在"，没有任何全局的"用户不再存在"信号传播。不会触发：
- 文件重新分配或标记
- 孤儿通知清理
- 引用完整性检查

**证据 4：没有任何关于"账号删除"的迁移或表设计**

```bash
$ grep -rn "deleted_account\|ghost_user\|orphan\|reparent\|account_status" migrations/*.sql
# → 零结果
```

22 份迁移定义了完整的软删除和审计基础设施，但**没有任何针对账号本身生命周期的设计**。这是 SSO 与 aero-id 之间的集成空白。

**证据 5：审计日志记录操作但不记录用户注销事件**

```go
// internal/domain/audit.go
const (
	AuditFileShareGrant  = "file.share.grant"
	AuditFileShareRevoke = "file.share.revoke"
	AuditReportResolve   = "comment.report.resolve"
	AuditUserBlock       = "user.block"
	AuditUserUnblock     = "user.unblock"
	AuditMessageDelete   = "message.delete"
	// 无 AuditAccountDeleted、无 AuditContentReassigned
)
```

### 架构师视角

要解决这个问题，需要两个方向的扩展：

**方向 A — SSO 集成增强（推模式）**：
1. SSO proto 增加 `WatchAccountStatus` 流式 RPC（或 webhook 回调端点）
2. aero-id 启动时注册一个 webhook 端点 `POST /sso-events`，接收账号状态变更通知
3. 收到`account.deleted`事件后，启动异步处理：标记 uid 为已删除、执行级联策略

**方向 B — aero-id 内的内容生命周期策略（拉模式）**：
1. 增加 `uid_status` 影子表：`(uid, status[active|deleted|suspended], deleted_at, re_parent_to_uid)`
2. 后台定期拉取 SSO 用户状态（通过 `GetUsers` + 与本地 uid 表对比）
3. 定义级联策略配置：`on_account_deletion: [soft_delete_content | reassign_to_admin | keep_but_anonymize | hard_delete_after_days]`
4. Storage GC 扩展为理解 uid 级别：已删除用户的数据可更快进入回收队列

**推荐的级联策略（从保守到激进）**：

| 策略 | 文件 | 消息 | 评论 | 通知 |
|------|------|------|------|------|
| 匿名化 | OwnerUID → "deleted_user" | FromUID → "deleted_user" | OwnerUID → "deleted_user" | 删除 |
| 转移 | 指定 admin 接管 | 保留 | 保留 | 删除 |
| 软删 | 标记删除 | 保留（显示"用户已注销"） | 保留（显示"用户已注销"） | 删除 |
| 硬删 | 硬删 + 清理 blob | 硬删 | 硬删 | 硬删 |

### 产品经理视角

**用户价值**：间接但至关重要。单个用户看不到这个功能，但以下场景直接依赖它：

- **合规性**：GDPR 第 17 条（Right to Erasure）要求"删除用户数据时无不当延迟"。没有这个机制，每次删除请求都需要手动 SQL 操作——不可审计、不可重复、容易遗漏。
- **UX 完整性**：当用户 A 注销后，用户 B 看到"来自未知用户的消息"、"附件无法访问"——破坏体验。合理的显示"用户已注销"远比神秘的 404 友好。
- **数据质量**：随着时间推移，孤儿数据累积。已注销用户的文件占用存储却无人访问，已注销用户的消息使搜索结果膨胀。

**优先级建议**：底层能力（uid 状态追踪 + 匿名化策略）是 P1 合规必需；级联策略配置是 P2；SSO webhook 回调是 P1.5（取决于 SSO 侧的配合）。

---

## 方向三：文件访问审计与安全取证

### 问题陈述

aero-id 有完善的**操作审计日志**（audit_events 记录谁执行了敏感操作），但完全缺少**读取访问审计**。任何人对任何文件的每一次读取（DownloadURL、Get、List）都没有留下审计踪迹。在出现数据泄露、合规调查或内部威胁时，无法回答以下最基本的问题：

- "谁在什么时间看了文件 X？"
- "用户 UID 在过去 30 天访问了哪些文件？"
- "这个共享链接被谁用了多少次？"
- "文件 F 在图上的哪些地方被展示过？"

当前的文件访问控制（`domain/access.go`）是**静默的**：通过或拒绝，都不记录。

### 代码级证据

**证据 1：domain/access.go 的权限检查是纯函数——无副作用、无日志**

```go
// internal/domain/access.go
func CanReadFile(actor string, f *File) bool {
	return f.Visibility == VisibilityPublic || f.OwnerUID == actor
}
```

这是一个纯函数。它返回 true/false，**不记录谁尝试了什么、是否成功**。即使是失败的访问尝试（被拒绝的请求）也完全不留下痕迹。

**证据 2：文件服务层不记录读取操作**

```go
// internal/file/service_crud.go — Get
func (s *fileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.files.Get(ctx, id)
	if err != nil {
		return nil, ...
	}
	if !canReadFile(actor, f) {
		return nil, apperr.Forbidden("file.forbidden", "you do not have access to this file")
	}
	// ← 无审计日志，无访问记录，无计数器递增
	return f, nil
}

// internal/file/service_crud.go — DownloadURL
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
	actor, err := mustActor(ctx)
	// ...
	// ← 无审计日志，无下载计数器
	return s.store.PresignGet(ctx, f.StorageKey, f.MimeType, ttl)
}
```

无论是 Get（查看元数据）还是 DownloadURL（获取下载链接），都没有记录。一个恶意内部人员可以通过 DownloadURL API 批量获取所有共享文件的下载链接——这个行为完全不可追踪。

**证据 3：文件分享的"访问"完全不可审计**

```go
// internal/file/service_share.go — 分享 API 记录"谁授权了谁"
// 但被分享者实际访问文件时不记录
```

分享操作本身通过审计日志记录（`AuditFileShareGrant` / `AuditFileShareRevoke`），但**被分享者的每一次后续读取都不留下记录**。分享出去的链接可以在任意地方被转发和访问——原始分享者完全不知道。

**证据 4：审计日志模型没有"读取"事件类型**

```go
// internal/domain/audit.go
const (
	AuditFileShareGrant  = "file.share.grant"
	AuditFileShareRevoke = "file.share.revoke"
	// ...
	// 无 AuditFileRead, AuditFileDownload, AuditFileList
)
```

审计事件专注于**写操作**（创建、删除、分享、修改），没有**读操作**。这与安全最佳实践相悖——在安全审计中，读操作的记录往往比写操作更关键（数据泄露通常是读出来的，不是写出来的）。

**证据 5：拒绝访问也完全不记录**

```go
// internal/service/file.go — 文件分享的读检查
func canReadFile(actor string, f *domain.File) bool {
	return f.Visibility == domain.VisibilityPublic || f.OwnerUID == actor
}
// 被拒绝的请求：不记录。无法检测暴力试探、扫描攻击
```

一个攻击者可以批量试探私有文件的 ID（UUIDv7，但若提前泄露），被拒绝的请求不会触发任何告警或速率限制升级。

### 架构师视角

修复方向不是"在每个 Get 前加一行 audit.Log"——那会引入显著的写入吞吐开销。需要一个更细粒度的策略：

1. **分层访问审计策略**：不是所有读取都需要审计。建议三种级别：
   - `none`：不记录（默认性能最优）
   - `metadata`：只记录 (who, what, when) 三元组，不记录具体内容
   - `full`：记录完整的请求上下文（用户代理、IP、引荐来源、获取的内容）

2. **存储方案**：使用专门的 `file_access_log` 表（写优化，append-only），与主业务数据分离：
   ```sql
   CREATE TABLE file_access_log (
       id          BIGSERIAL,
       file_id     UUID NOT NULL,
       actor_uid   TEXT NOT NULL,
       access_type TEXT NOT NULL,  -- 'read' | 'download' | 'list' | 'denied'
       ip          INET,
       user_agent  TEXT,
       accessed_at TIMESTAMPTZ NOT NULL DEFAULT now()
   ) PARTITION BY RANGE (accessed_at);
   ```

3. **安全响应增强**：在 `domain/access.go` 的拒绝路径中增加可选 hook——当拒绝率达到阈值时自动升级到限速阻断或告警。

4. **保留策略**：访问日志自动老化（如 90 天后通过分区删除），避免无限膨胀。

5. **审计查询 API**：`GET /v1/admin/audit/files/:id/access`（需要 `audit.read` 权限）提供按文件、按用户、按时间范围的访问历史查询。

### 产品经理视角

**用户价值**：零（对普通用户透明）。但是：

- **企业合规**：SOC2、ISO 27001、HIPAA 都要求文件访问审计。没有它，aero-id 无法进入企业市场。
- **数据泄露调查**：当发生数据泄露时，"谁看了什么"是第一个需要回答的问题。没有访问审计，调查只能依靠猜测和日志文件的 grep。
- **共享安全**：用户分享文件后，看不到谁访问了它。增加了访问日志 API，用户可以确认"只有我需要的人看了这个文件"。

**优先级建议**：`denied` 级别的审计（记录被拒绝的访问尝试）是 P1（安全检测）；`metadata` 级别的审计是 P1.5（合规基础）；`full` 级别和查询 API 是 P2（高级功能）。

---

## 方向四：跨实体细粒度通知订阅（基于资源、发送者、会话）

### 问题陈述

当前通知偏好（`NotificationPreference`）只能按**通知类型** 开关（`message.received` 开/关、`comment.reply` 开/关），并设置全局静默时段。没有更细粒度的控制：

- **无法对特定资源订阅**：用户无法说"当文件 X 有新评论时通知我"
- **无法对特定发送者静音**：用户无法说"不要通知我用户 A 的消息"
- **无法按会话设置**：用户无法说"群聊 X 免打扰，私聊 Y 提醒"
- **无法设置优先级路由**：用户无法说"紧急消息 → 推送，普通消息 → 摘要"
- **无法订阅非自有资源的事件**：用户无法关注一个公开文件的变更

通知分发的决策树（`internal/notification/dispatcher.go`）是硬编码的、扁平的、按类型路由的。

### 代码级证据

**证据 1：NotificationPreference 没有资源范围字段**

```go
// internal/domain/notification.go:28-38
type NotificationPreference struct {
	UID              string
	NotificationType string // 唯一维度：消息类型
	Enabled          bool
	QuietHoursStart  string
	QuietHoursEnd    string
	Timezone         string
	// 无 ResourceType、无 ResourceID、无 SenderUID、无 ConversationID
	// 无 Priority、无 ChannelPreference
}
```

主键是 `(uid, notification_type)`。用户不能为不同的资源（不同文件、不同会话）设置不同的偏好。所有 `message.received` 通知要么全开、要么全关。

**证据 2：Dispatcher 的过滤逻辑是硬编码的——只有 block 和 preference 两道粗筛**

```go
// internal/notification/dispatcher.go:40-56
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
	n := d.notificationFor(e)
	if n == nil {
		return nil
	}
	// 第一道筛：block 检查（按 uid 维度）
	if d.blocks != nil && n.ActorUID != "" {
		blocked, err := d.blocks.IsBlocked(ctx, n.RecipientUID, n.ActorUID)
		if err != nil { return err }
		if blocked { return nil }
	}
	// 第二道筛：偏好检查（按 type 维度）
	if err := d.notifs.Create(ctx, n); err != nil { return err }
	// ...
}
```

过滤维度只有两个：blocks 是"这个发送者我屏蔽了吗"（全局性的），preferences 是"这个通知类型我关了吗"（全局性的）。**没有资源级、会话级、发送者级的过滤。**

**证据 3：文件/消息/评论交互不产生"订阅"语义**

```go
// internal/file/service_crud.go — 用户 Get 一个文件后并不"关注"它
// internal/file/service_share.go — 分享只授权读权限，不建立关注关系
```

没有 `subscriptions` 表或 `follows` 表。用户不能订阅一个公共文件的评论更新，不能关注其他用户的活动。通知只覆盖"直接与用户相关"的事件（收到了消息、评论被回复），不覆盖"用户关注的内容有更新"。

**证据 4：搜索和列表不返回"关注"状态**

```proto
// api/proto/aero_id/v1/aero_id.proto
// File、Comment、Message 消息中无 is_subscribed、follows_count 字段
```

即使服务层想展示"你已关注此文件"的 UI 状态，也没有数据支撑。

**证据 5：迁移中没有订阅/关注相关的表**

```bash
$ grep -rn "subscript\|follow\|watch" migrations/*.sql
# → 零结果
```

### 架构师视角

需要引入一个**通用的订阅/关注抽象**：

1. **`subscriptions` 表**：
   ```sql
   CREATE TABLE subscriptions (
       id            UUID PRIMARY KEY,
       uid           TEXT NOT NULL,           -- 订阅者
       resource_type TEXT NOT NULL,           -- 'file' | 'user' | 'conversation' | 'comment_thread'
       resource_id   TEXT NOT NULL,           -- 目标资源的 ID
       event_types   TEXT[] NOT NULL DEFAULT '{}',  -- 空 = 所有事件
       channel       TEXT NOT NULL DEFAULT 'in_app', -- 'in_app' | 'push' | 'email' | 'all'
       muted_until   TIMESTAMPTZ,            -- NULL = 不静音
       created_at    TIMESTAMPTZ NOT NULL,
       UNIQUE(uid, resource_type, resource_id)
   );
   ```

2. **Dispatcher 扩展**：在 Handle 方法中增加第三步——查询 `subscriptions` 表，寻找匹配的订阅者（不仅是收件人），对每个匹配的订阅者创建通知行。

3. **事件类型扩展**：添加 `file.commented`、`file.updated`、`user.new_file`、`comment.thread_updated` 等事件类型，使订阅者有事件可以订阅。

4. **API 扩展**：
   - `POST /v1/subscriptions` — 订阅一个资源
   - `DELETE /v1/subscriptions/:id` — 取消订阅
   - `GET /v1/subscriptions` — 我的订阅列表
   - `GET /v1/files/:id/subscribers` — 谁关注了这个文件（admin/owner only）

5. **通知偏好层级**（覆盖策略）：
   ```
   全局(notification_type) → 发送者(sender_uid) → 会话(conversation_id) → 资源(resource)
   较细粒度的设置覆盖较粗粒度的设置
   ```

### 产品经理视角

**用户价值**：直接且显著。这是**关注者模型**——几乎所有社交/协作平台（GitHub、Slack、Notion、Google Docs）的核心功能：

- 用户评论了一个文件后，自动关注该文件的后续评论——不需要手动订阅
- 用户可以在繁忙的群聊中设置"仅 @提及 时通知"
- 用户可以关注其他用户以了解他们的公开活动
- 用户在文件分享之外获得"变更推送"——不仅仅是"你能看"，而是"你知道它变了"

**对留存的影响**：通知系统是用户回访的核心驱动力。当前的通知只有高门槛事件（私信、评论回复）。添加了细粒度订阅后，用户可以自定义信息流——这直接提升日活和留存。

**优先级建议**：自动订阅（用户产生交互后自动关注资源）是 P1（低投入高回报的 UX 提升）；手动订阅 API 是 P2；分层偏好覆盖是 P3。

---

## 方向五：非可靠网络支持与离线操作语义

### 问题陈述

aero-id 的 API 设计假设**客户端始终在线**。当网络连接不稳定时（移动网络、弱信号、地铁隧道、跨国链路），客户端没有标准化的方式来：

1. **离线操作**：在无网络时创建消息/评论，等恢复后同步
2. **操作重放**：用幂等键做安全重试，但客户端没有操作队列
3. **冲突感知**：两个设备同时编辑同一内容时静默覆盖
4. **进度恢复**：大文件上传中断后无法断点续传

系统目前有**服务端幂等性**（idempotency keys），但没有**客户端离线操作模型**。

### 代码级证据

**证据 1：所有写操作要求实时响应——无离线操作概念**

```proto
// api/proto/aero_id/v1/aero_id.proto
// 所有 RPC 都是同步 Unary——客户端发送请求，等待响应
// 无 deferred_write、无 batch_commit、无 op_log 概念
```

每个 `SendMessage`、`CreateComment`、`UploadFile` 都需要客户端发送请求后等待服务端确认。如果客户端在发送后、收到响应前断线，用户不知道操作是否成功。幂等键帮助了服务端去重，但客户端需要手动实现"重新查询上次操作状态"的逻辑。

**证据 2：文件上传不支持断点续传**

```go
// internal/file/service_upload.go — 文件上传是原子的、单次请求的
// 对于大文件，有 PresignUpload 方案（客户端直接 PUT 到 S3），
// 但没有分片上传、没有上传令牌续期、没有已上传字节数的追踪

// PresignUpload 返回一个一次性上传 URL
func (s *fileService) PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error) {
	// URL 在 TTL 过期后失效
	// 如果上传中途断线，客户端必须重新获取 Presign URL 并从零开始
}
```

**证据 3：冲突合并策略不存在——最后写入者胜出**

```go
// internal/message/service.go — Edit
func (s *service) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
	// ...
	m.Content = content // 无条件覆盖
	if err := s.messages.UpdateContent(ctx, id, content, now); err != nil {
		return nil, ...
	}
	// 没有版本检查、没有 diff 合并、没有冲突检测
}
```

如果用户同时在手机和电脑上编辑同一条消息，后保存的版本覆盖先保存的——用户可能在手机上改了半天，被电脑上的一个误触覆盖。没有"编辑冲突"的提示。

**证据 4：列表 API 不支持增量同步**

```go
// internal/message/message.go — MessageRepository 接口
type MessageRepository interface {
	ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	// 无 ListSince(ctx, uid, since time.Time) — 增量变更推送
}
```

SSE 通道有增量推送（通过 `ListSince` 重放），但 REST/gRPC 列表 API 不支持"只返回上次同步后的变更"。客户端要检测新消息必须轮询整个列表——这在移动网络上代价高昂。

**证据 5：幂等键存储有 TTL，但客户端没有操作日志概念**

```go
// internal/idempotency/store.go — 服务端幂等键只保证去重
// 但客户端无法枚举"我过去 24 小时发送了哪些操作"
```

幂等键的存在是为了让客户端安全重试。但如果没有客户端的操作日志（op log），客户端在断线重连后无法知道哪些操作已提交、哪些需要重试。

### 架构师视角

这不仅是一个"离线支持"功能——这是**操作模型从同步到异步的演进**。推荐分层实现：

**Layer 1 — 增量同步 API（低投入，高回报）**：
1. 为消息、通知、评论添加 `ListSince` 变体（与 SSE `ListSince` 同构），参数为上次同步时间戳
2. 客户端初次全量同步，后续只拉增量
3. 减少移动端的数据传输量和电量消耗

**Layer 2 — 操作令牌与异步写入（中投入）**：
1. `POST /v1/operations` — 提交一个包含多个子操作的"操作批次"（类似 GraphQL mutation）
2. 返回 `operation_id`，客户端可以轮询 `GET /v1/operations/:id` 查看状态
3. 这是离线队列的服务端基础

**Layer 3 — 冲突检测与版本化写入（中高投入）**：
1. 更新操作（Edit）增加 `expected_version` 参数
2. 服务端使用 `UPDATE ... WHERE id = ? AND version = expected_version`，返回 409 如果版本不匹配
3. 客户端收到 409 后展示"内容已在他处更新，请刷新重试"
4. `version` 列通过 migration 添加到可编辑实体（messages、comments、files）的表中

**Layer 4 — 可恢复上传（中投入）**：
1. PresignUpload 支持分片（`upload_id` + `part_number`）
2. `CompleteUpload` 合并分片
3. 客户端在上传中断后可以只重传未完成的分片

### 产品经理视角

**用户价值**：直接且关键，尤其是在移动端：

- **场景一**：用户在地铁中输入一条长消息，进隧道后没信号。当前行为：点击发送 → 转圈 → 失败 → 消息丢失。期望行为：消息进入本地队列 → 信号恢复后自动发送 → 用户看到"已发送"。
- **场景二**：用户一边在电脑上编辑评论，一边在手机上编辑。最后保存的覆盖了之前的。期望行为：第二次保存时提示"内容已被其他设备修改，请刷新"。
- **场景三**：用户上传一个 500MB 的视频，传到 80% 时网络中断。当前行为：从头开始。期望行为：断点续传。

**对用户留存的影响**：在弱网环境下（移动端、跨国、偏远地区）的可靠性直接影响用户对平台的信任。在即时通讯竞争中，离线支持不再是"锦上添花"而是"必需品"。

**优先级建议**：增量同步 API 是 P1（迁移成本最低、收益最直接）；操作令牌是 P2（离线队列的基础）；冲突检测是 P2.5（用户体验提升）；可恢复上传是 P3（大文件场景优化）。

---

## 总结与实施优先级

| # | 方向 | 类型 | 产品价值 | 技术投入 | 依赖前置 | 是否被 100 份既有分析覆盖 |
|---|------|------|---------|---------|---------|------------------------|
| 1 | **开发者 API 生态**（API 密钥、速率分层、SDK 管线） | 平台化 | 中（间接，战略性） | 中 | 无 | ❌ 零系统性覆盖 |
| 2 | **SSO 账号删除生命周期与孤儿内容处理** | 合规/数据治理 | 高（合规） | 中 | SSO 配合 | ❌ 零系统性覆盖 |
| 3 | **文件访问审计与安全取证** | 安全/合规 | 高（企业合规） | 低-中 | 无 | ❌ 零系统性覆盖 |
| 4 | **跨实体细粒度通知订阅** | 产品/体验 | 高（留存） | 中-高 | 通知系统已存在 | ❌ 零系统性覆盖 |
| 5 | **非可靠网络支持与离线操作语义** | 产品/基础设施 | 高（移动端） | 中-高 | 幂等键已存在 | ❌ 零系统性覆盖 |

### 建议实施顺序

```
Phase 1 (P0 — 安全与合规基础):
  方向三（文件访问审计）Layer 1 — 在 access.go 中增加 denied 审计
  → 安全漏洞检测的基线，半天完成

Phase 2 (P1 — 数据治理):
  方向二（SSO 账号删除）Phase 1 — uid 状态影子表 + 后台同步
  → GDPR 合规里程碑，3-5 天

Phase 3 (P1 — 平台基础):
  方向一（API 生态）Phase 1 — API 密钥模型 + 中间件
  → 第三方集成的门槛，2-3 天

Phase 4 (P1 — 产品体验):
  方向五（离线支持）Layer 1 — 增量同步 API
  → 移动端体验质变，1-2 天

Phase 5 (P2 — 产品增长):
  方向四（通知订阅）Phase 1 — 自动关注
  → 通知覆盖面和用户留存驱动，3-4 天
```
