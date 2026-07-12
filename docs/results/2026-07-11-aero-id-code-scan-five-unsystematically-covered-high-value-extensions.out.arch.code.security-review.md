以下是经过系统化安全审查的结果。

---

# 安全审查：aero-id

**日期**：2026-07-12  
**审阅者**：首席安全工程师  
**范围**：`/home/u1/aero-id` 的完整代码库

---

## 1. 认证与授权

| 类别 | 严重性 | 标题 | 位置 | 描述 |
|---------|----------|-------|----------|-------------|
| 认证 | **严重** | **SSO 令牌缓存陈旧窗口在 SSO 宕机时允许令牌重复使用达 5 分钟** | `internal/sso/token_cache.go:112-129` | `stale_ttl` 配置为 300 秒。当 SSO 断路跳闸（连续 5 次可用性故障后），`GetStale` 会提供过期条目，超出 60 秒 TTL 缓存 + 300 秒陈旧窗口 = 撤销令牌可在 **6 分钟**内重复使用。 |
| 认证 | **高** | **断路故障时降级处理：SSO 宕机时仍提供过期的标识，存在会话劫持风险** | `internal/sso/client.go:199-204` | `validateUpstream` 在 `errCircuitOpen` 时调用 `GetStale`。这将将已撤销的令牌视为仍有效，直至达到陈旧窗口的限制。 |
| 认证 | **高** | **首次管理者启动漏洞 — 没有引导程序入口点** | `internal/repository/admin.go:79-95` | 没有种子脚本、标志或引导端点来创建第一个管理角色。`GrantRole` 检查 `actor` 是否拥有 `system.config`，但最初没有人拥有。它是 chicken-and-egg（先有鸡还是先有蛋）问题。 |
| 认证 | **中** | **封禁中间件失败时保持开放（默认允许）** | `internal/middleware/ban.go:36-39` | 当 `checker.IsBanned()` 返回错误时，中间件会继续执行 `c.Next()`，**允许被封禁的用户继续操作**。这违反了纵深防御原则。 |
| 认证 | **高** | **SendSystem 绕过所有认证检查** | `internal/service/message.go:103-126` | `SendSystem` 方法无需已认证的操作者可调用——它使用 `domain.SystemSenderUID`。任何可以调用此方法（直接依赖注入）的内部组件都可以伪造系统消息。没有调用者身份验证。 |
| 授权 | **中** | **ListAudit HTTP 检查在前端而非后端** | `internal/handler/http/admin.go:32-35` | HTTP 路由使用 `RequirePermission(domain.PermAuditRead, ...)` 作为中间件，但 gRPC 使用拦截器。审计追踪的废弃功能仅靠中间件保护，而非服务层。 |
| 授权 | **低** | **注释审核是作者范围的，无法进行跨用户审核** | `internal/service/comment.go:216-240` | `Hide`、`Unhide`、`ListReports` 和 `ResolveReport` 检查 `c.OwnerUID == actor`。没有管理员可以代表他人进行审核。权限 `PermContentModerate` 已定义但**从未使用**。 |
| 认证 | **中** | **Idempotency-Key 劫持** | `internal/middleware/idempotency.go:31-34` | 幂等密钥作用于 `(actor_uid, endpoint, key)`。但 actor_uid 可能被错误读取/为空。如果在幂等中间件之前认证失败，则 actor_uid 为空，多个用户可能发生冲突。 |

---

## 2. 输入验证

| 类别 | 严重性 | 标题 | 位置 | 描述 |
|---------|----------|-------|----------|-------------|
| 输入验证 | **严重** | **未经过滤的 XSS：评论和消息内容未转义** | `internal/comment/service.go:39`, `internal/message/service.go:43` | `content` 字段直接传递给 `domain.Comment`/`domain.Message`，无任何 HTML/脚本清理。当在 Web UI 中呈现时（通过 API 响应），这会直接导致反射式/存储式 XSS。OWASP Top 10 A03:2021 — 注入。 |
| 输入验证 | **高** | **文件名字段存在路径遍历漏洞（本地存储）** | `internal/storage/local.go:41` | 文件路径通过 `filepath.Join(l.cfg.Root, key)` 构建，其中 `key` = `ownerUID/yyyy/mm/uuid`。如果 `ownerUID` 包含 `../` 路径遍历序列（例如 `../../etc`），则写入可能发生在根目录之外。 |
| 输入验证 | **高** | **MIME 类型允许列表默认为空（允许任意类型）** | `configs/config.yaml:78` | `allowed_mime_types: []` 表示允许任意内容类型。结合 `name` 扩展名预处理（`name = fh.Filename`），攻击者可以上传任意可执行代码。 |
| 输入验证 | **中** | **消息内容长度无限制** | `internal/message/service.go:43` | 内容长度不受限。攻击者可发送 10MB 的 `content` 字段的消息，导致 OOM/高延迟。 |
| 输入验证 | **中** | **元数据字段未进行模式验证** | `internal/domain/file.go:79` | `Meta map[string]any`（JSONB）在所有实体中均可由客户端任意设定。键/值没有大小限制或模式验证。 |
| 输入验证 | **低** | **Content-Type 由客户端声明，服务器信任** | `internal/handler/http/file.go:67-71` | 上传文件的 `Content-Type` 取自客户端 header (`fh.Header.Get("Content-Type")`)，服务器不进行 MIME 嗅探验证。 |

---

## 3. 密码学

| 类别 | 严重性 | 标题 | 位置 | 描述 |
|---------|----------|-------|----------|-------------|
| 加密 | **严重** | **签署密钥在 configs/config.yaml 中硬编码为 "change-me"** | `configs/config.yaml:88` | `signing_key: change-me-please-very-long-random`。这是默认值，用于 HMAC 保护预签名 URL。如果部署时未用环境变量覆盖，任何拥有此字符串的人都可以伪造预签名 URL。 |
| 加密 | **高** | **gRPC 传输未加密（`insecure: true`）** | `configs/config.yaml:46`, `internal/sso/dial.go:47` | SSO gRPC 配置为 `insecure: true`。所有 SSO 令牌验证均通过明文 gRPC 发送。令牌可以被网络嗅探器拦截。 |
| 加密 | **中** | **本地存储中静态文件未加密** | `internal/storage/local.go:60-70` | 文件以明文写入磁盘。`storage_type: local` 时无加密。 |
| 加密 | **低** | **Webhook 签名密钥存储在配置中而非环境变量中** | `configs/config.yaml:70` | `outbox.webhook.signing_key` 是配置映射值，可通过 `AERO_ID_OUTBOX_WEBHOOK_SIGNING_KEY` 覆盖，但模式未强制使用环境变量进行签署（与 `signing_key` 不同）。 |

---

## 4. 会话管理

| 类别 | 严重性 | 标题 | 位置 | 描述 |
|---------|----------|-------|----------|-------------|
| 会话 | **高** | **SSO 撤销后令牌仍可使用 6 分钟** | `configs/config.yaml:48-49` | `token_cache_ttl: 60s` + `stale_ttl: 300s`。被 SSO 撤销的令牌在最快 60 秒内仍可被接受，之后再过 300 秒陈旧窗口。 |
| 会话 | **中** | **单点登录令牌缓存使用 sync.Map，无驱逐回叫** | `internal/sso/token_cache.go:57-60` | 令牌缓存是无界映射。只有 30 秒的清理定时器进行了大小限制。瞬间突发流量（每秒数千个唯一令牌）可能导致 OOM。 |
| 会话 | **中** | **幂等密钥 24h TTL 存在重放风险** | `configs/config.yaml:15` | `idempotency_ttl: 24h` 意味着幂等响应在 24 小时内可重放。写入操作（如文件创建）过早的重放可导致状态回放问题。 |

---

## 5. 数据保护

| 类别 | 严重性 | 标题 | 位置 | 描述 |
|---------|----------|-------|----------|-------------|
| 数据保护 | **高** | **预签名 URL 可能暴露存储密钥** | `internal/storage/local.go:108-113` | `PresignGet` 返回包含 `key` 参数中存储密钥的 URL。S3 版本使用 AWS 预签名 URL 更安全，但本地版本会泄漏文件路径。 |
| 数据保护 | **中** | **审核日志泄露封禁信息** | `internal/service/ban.go:57-67` | `AuditEvent` 元数据包含 `"expiresAt"` 和 `"reason"`——可能包含 PII/内部裁决。任何拥有 `audit.read` 权限的人都可以查看。 |
| 数据保护 | **中** | **评论/消息内容未被修改** | `internal/comment/repository.go` | 内容作为原始字符串存储，无加密。任何拥有数据库访问权限的人都可以读取所有用户评论/消息。 |
| 数据保护 | **低** | **访问日志记录 actor_uid（PII）** | `internal/middleware/access_log.go:35-36` | 访问日志行包含 `zap.String("actor", c.GetString("actor_uid"))`。在日志聚合器中，这会泄露用户交互模式。 |

---

## 6. 威胁模型（STRIDE 分析）

### 欺骗（身份伪造）
- **严重** — SSO 令牌缓存在 60 秒 TTL + 300 秒陈旧窗口期内接受已撤销令牌
- **高** — 当 SSO 不可用（断路故障）时，`GetStale` 提供过期的标识（`client.go:199`）
- **高** — `SendSystem` 绕过经过身份验证的操作者检查——任何内部调用者都可以发送系统消息（`service/message.go:103`）

### 篡改（数据修改）
- **中** — 文件 `Meta`（JSONB）无模式——任意字段可被设置/覆盖
- **中** — 元数据更新合并逻辑可能覆盖现有键（`service/file_share.go:85-91`）
- **低** — 没有版本控制/乐观锁定——并发编辑可能导致丢失更新

### 抵赖（行为否认）
- **高** — 审核日志记录操作，但写入时忽略错误（`_ = s.audit.Log(...)`）。如果审计存储失败，不会记录关键操作。
- **中** — 审计事件 UID 在 `Append` 中被覆盖（`repository/audit.go:36`），如果客户端已设置 UID，这可能会损坏审计追踪。

### 信息泄露
- **严重** — XSS 通过评论/消息内容可导致数据泄露（OWASP Top 10 A03:2021）
- **高** — 预签名 URL 泄漏存储密钥（`storage/local.go:108`）
- **高** — gRPC 传输通过明文运行（`sso.insecure: true`）
- **中** — actor_uid 日志记录泄露用户交互模式
- **中** — 审核日志包含封禁原因（`service/ban.go:64`）

### 拒绝服务
- **高** — **速率限制默认禁用**（`rate_limit.enabled: false`）。没有速率限制时，攻击者可以淹没任何端点。
- **中** — 消息内容无大小限制——攻击者可上传大型消息导致 OOM
- **中** — 令牌缓存是无界映射——瞬间唯一令牌峰值可能导致 OOM
- **中** — 封禁检查失败时保持开放——攻击者若损坏数据库可能会绕过封禁
- **低** — 幂等密钥存储无大小限制——攻击者可发送无限唯一密钥填满存储

### 权限提升
- **高** — **首次管理者 Bootstrap** — 由于没有引导程序，第一个拥有 `system.config` 权限的管理员无法创建。同时，如果任何管理员拥有 `GrantRole`，他们可以赋予自己任何角色。
- **中** — `PermContentModerate` 已定义但**从未使用**——因此没有交叉评论审核能力。用户可评论的内容范围超出应有的界限。
- **高** — 如果 `SendSystem` 暴露在外部 API（gRPC）上，攻击者可以向任何用户发送冒充系统的消息。

---

## 7. 合规性考虑

### OWASP Top 10 差距
| A 类 | 状态 | 风险 |
|-------|--------|------|
| **A01:2021 — 失效的访问控制** | **高风险** — `SendSystem` 无访问控制；`PermContentModerate` 未使用；评论审核仅为作者范围 |
| **A03:2021 — 注入** | **高风险** — 评论/消息中的存储 XSS；文件名的路径遍历 |
| **A04:2021 — 不安全的设计** | **中风险** — 首次管理者引导缺陷；幂等密钥劫持向量 |
| **A05:2021 — 安全配置错误** | **高** — 速率限制禁用；签署密钥默认值；gRPC 不安全；CORS `*` 默认 |
| **A06:2021 — 易受攻击和过时的组件** | **低** — 依赖项应通过 `go.mod` 审核 |
| **A07:2021 — 身份验证和会话管理失败** | **高** — SSO 令牌重用 6 分钟 | 
| **A09:2021 — 安全日志记录和监控失败** | **中** — 审核日志写入错误被忽略；访问日志采样可能隐藏攻击模式 |

### 缺失的安全头
- 无 `Content-Security-Policy` 标头
- 无 `Strict-Transport-Security` 标头
- 无 `X-Content-Type-Options: nosniff` 标头
- 无 `X-Frame-Options` 标头
- 无 `Referrer-Policy` 标头

---

## 发现总结表

| # | 类别 | 严重性 | 标题 | 位置 | 影响 | 建议 | 工作量 |
|---|--------|----------|-------|----------|------|----------------|--------|
| 1 | 认证 | **严重** | SSO 令牌陈旧窗口允许在 SSO 宕机时重复使用已撤销令牌达 5 分钟 | `internal/sso/client.go:199-204`, `configs/config.yaml:49` | 已撤销的令牌允许持续访问长达 6 分钟（60s TTL + 300s 陈旧窗口） | 将 `stale_ttl` 减少到 ≤ 60 秒；添加断路器触发的紧急令牌黑名单端点 | S |
| 2 | 加密 | **严重** | 签署密钥默认值 "change-me" | `configs/config.yaml:88` | 拥有此值的任何人可伪造预签名下载 URL，绕过所有身份验证 | 在 `validate()` 中添加启动检查，拒绝已知的默认值；强制执行 `AERO_ID_SIGNING_KEY` 环境变量 | S |
| 3 | 输入验证 | **严重** | 评论/消息内容中的反射式/存储式 XSS | `internal/comment/service.go:39`, `internal/message/service.go:43` | 攻击者可以在 Web UI 中执行任意 JavaScript，窃取会话/数据 | 在存储前添加 HTML 消毒（例如 `bluemonday`）；在 API 响应中添加 `Content-Security-Policy: default-src 'none'` | M |
| 4 | 认证 | **高** | SendSystem 绕过所有身份验证 | `internal/service/message.go:103-126` | 任何内部调用者都可以发送冒充系统的消息 | 添加调用者验证参数；记录、审计并限制谁可以调用 `SendSystem` | S |
| 5 | 授权 | **高** | 首次管理者 Bootstrap 缺陷 | `internal/repository/admin.go` | 没有人可以授予第一个 `system.config` 权限——系统无法运行 | 添加引导端点（`POST /admin/bootstrap`）在 roles 表为空时创建一个超级管理员角色 | M |
| 6 | 加密 | **高** | 与 SSO 的 gRPC 通信未加密 | `configs/config.yaml:46`, `internal/sso/dial.go:47` | 令牌验证以及使用凭据在网络上明文发送 | 设置 `insecure: false`；要求 mTLS 或至少使用具有有效证书的 TLS | M |
| 7 | 拒绝服务 | **高** | 速率限制默认禁用 | `configs/config.yaml:65`, `internal/middleware/ratelimit.go` | 无速率限制下，攻击者可淹没任何端点导致 DoS | 在配置中启用默认速率限制（例如 20 RPS / 40 突发）；添加 Redis 后备用于分布式限制 | M |
| 8 | 输入验证 | **高** | 文件名字段的路径遍历 | `internal/storage/local.go:41` | 恶意 `ownerUID`（`../../etc/`）可能导致根目录外写入 | 净化 `ownerUID`：拒绝包含 `../`、`..\` 或 `./` 的路径；在 `domain` 中添加验证 | S |
| 9 | 输入验证 | **中** | 无内容长度限制的消息内容 | `internal/message/service.go:43` | 攻击者可以提交大型消息消耗内存/存储 | 添加 `MaxContentBytes` 配置（例如 64KB）并在服务层强制执行 | S |
| 10 | 认证 | **中** | `PermContentModerate` 已定义但未使用 | `internal/domain/admin.go:9-14`, `internal/handler/grpc/server.go:157` | 交叉评论审核不可能实现——用户无法报告/隐藏其他用户的评论 | 添加管理员端点 `POST /admin/comments/:id/hide` 和 `DELETE /admin/comments/:id/delete`，使用 `PermContentModerate` | M |
| 11 | 数据保护 | **中** | 审核日志包含封禁原因（PII） | `internal/service/ban.go:64` | 拥有 `audit.read` 权限的管理员可以查看封禁原因，其中可能包含敏感信息 | 在审核日志中对封禁原因进行脱敏处理；记录敏感数据前添加 `maskPII()` | S |
| 12 | 认证 | **中** | 封禁中间件失败时保持开放 | `internal/middleware/ban.go:36-39` | 如果在封禁检查期间数据库错误，封禁用户可以通过 | 失败关闭：记录错误并拒绝请求，而不是允许继续 | S |
| 13 | 会话 | **中** | 幂等密钥 24h TTL 存在重放风险 | `configs/config.yaml:15` | 幂等响应可在 24 小时内重放，允许意外状态回放 | 将幂等 TTL 降低至 1h 或使其在端点基础上可配置 | S |
| 14 | 合规 | **中** | 安全标头缺失 | `internal/handler/http/router.go` | 应用程序未设置 CSP、HSTS、X-Frame-Options | 添加 `SecurityHeaders` 中间件：`CSP: default-src 'none'`、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY` | S |
| 15 | 数据保护 | **低** | 访问日志记录 actor_uid（PII） | `internal/middleware/access_log.go:35` | 日志聚合器中泄露用户交互模式 | 添加可选的 actor_uid 日志脱敏；记录 `/healthz`/`/readyz` 的 actor_uid 为 `"probe"` | S |

---

## 最终总结

### 总体安全态势：**需改进**

该代码库展示了优秀的安全意识——断路模式、幂等密钥、请求超时、审核日志记录、CORS 原语和速率限制基础设施。然而，关键配置默认值（速率限制禁用、SSO `insecure: true`、签署密钥 "change-me"）和缺失的输入消毒使生产部署变得脆弱。

### 前 3 个关键问题

1. **SSO 令牌陈旧窗口在 SSO 宕机时允许已撤销令牌重复使用达 5 分钟**（#1）  
   最坏情况下，用户被 SSO 撤销后，仍可继续被 aero-id 认证长达 6 分钟。结合断路机制（连续 5 次故障），已撤销的令牌会形成一个可利用的认证窗口。

2. **签署密钥默认值为 "change-me" → 伪造预签名 URL**（#2）  
   签署密钥是本地存储下载 URL 的单一信任锚点。默认值 "change-me-please-very-long-random" 是公开已知的。任何了解配置（通过泄漏、扫描或程序员错误）的人都可以伪造任意文件的下载 URL。

3. **评论/消息中的 XSS — 无输入消毒**（#3）  
   评论和消息内容未进行 HTML 消毒存储，也未以安全 Content-Type 标头提供。Web 客户端渲染此内容时，攻击者可以窃取会话、执行任意操作并访问所有用户数据。

### 前 3 个快速胜利（高影响力，低工作量）

1. **在启动验证中拒绝默认签署密钥**（< 1 小时）  
   在 `config.go:validate()` 中添加检查：如果 `signing_key == "change-me-please-very-long-random"` 则 panic。除非被环境变量覆盖，否则会立即崩溃。

2. **减少陈旧窗口并提高断路器阈值**（< 30 分钟）  
   将 `stale_ttl: 300s` 改为 `stale_ttl: 60s`。将 `breaker_threshold: 5` 保持原样或减少至 3。

3. **添加安全标头中间件**（< 1 小时）  
   在 `router.go` 的全局中间件链中添加 `CSP: default-src 'none'`、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`。这会立即缓解 XSS 漏洞以及点击劫持等问题。

### 安全债务

| 类别 | 债务描述 | 追踪 |
|--------|--------------|--------|
| 配置强化 | 签署密钥默认值、SSO 不安全、速率限制禁用——所有这些都必须在生产部署前解决 | 配置验证 |
| 缺少消毒 | 在存储与渲染层之间没有 HTML/脚本消毒 | 内容安全 |
| 监控与速率限制 | 应用中内置了完整的速率限制器，但**默认禁用** | 速率限制配置 |
| TLS 在所有地方 | gRPC（SSO + 内部）未加密；OTel 收集器不安全 | 传输安全 |
| 管理员 Bootstrap | 无法启动管理员角色——先有鸡还是先有蛋 | 运营流程 |
| 评论审核 | `PermContentModerate` 存在但从未使用——用户不能互相报告评论 | 审核 API |
