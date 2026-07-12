现在我已完成全面审查。以下是完整的安全审查报告。

---

# 🔒 Principal Security Engineer Review — aero-id

**Review Date**: 2026-07-12  
**Codebase**: `/home/u1/aero-id`  
**Go Version**: 1.26  
**Review Methodology**: Manual code review + STRIDE threat model

---

## Executive Summary

**Overall Security Posture**: **Needs Improvement**

aero-id 展现出强劲的架构纪律——清晰的层分离、集中的授权、统一的错误处理和适当的加密基础。然而，发现了几个**关键/高**严重性问题，需要在生产部署前解决。该服务在身份验证、速率限制和审计方面大量依赖 OPT-IN 配置，默认情况下会留下显著的攻击面。

---

## Detailed Findings

---

### FINDING 1 — Bootstrapping 管理员账户的权限提升漏洞

| 字段 | 内容 |
|---|---|
| **Category** | Authorization / Elevation of Privilege |
| **Severity** | **Critical** |
| **Title** | 管理员角色无法自行引导 |
| **Location** | `internal/service/admin.go` (GrantRole), `migrations/000022_admin_roles.up.sql` |
| **Description** | 迁移脚本预置了 `moderator` 和 `admin` 角色及其权限。然而，`GrantRole` 端点要求调用者持有 `PermSystemConfig` 权限——该权限仅 `admin` 角色拥有。由于无法自行授权，且没有 CLI/环境变量引导机制，不存在授予第一个管理员角色的流程路径。 |
| **Attack Scenario** | 攻击者无法利用此问题；相反，这是一个**可用性漏洞**：合法操作者无法提升初始管理员，导致所有需要 `system.config`、`user.ban` 或 `audit.read` 权限的管理功能（审计日志、用户封禁、角色管理）完全无法访问。 |
| **Impact** | 所有管理员功能在初始部署后无法使用，直到有人直接连接到数据库并手动插入 `user_roles` 行。 |
| **Recommendation** | 实现引导机制。选项：(a) 环境变量 `AERO_ID_ADMIN_UID`——若设置，启动时若尚未授予则授予 `admin` 角色；(b) 专用的 `cmd/bootstrap` CLI 工具。**首选方案 (a)**： |
| **Effort** | S（< 1 天） |

```go
// In app/new.go after adminRepo is created:
if uid := os.Getenv("AERO_ID_ADMIN_UID"); uid != "" {
    if err := adminRepo.GrantRole(ctx, uid, "admin", "bootstrap"); err != nil {
        log.Warn("admin bootstrap failed (may already exist)", zap.Error(err))
    }
}
```

---

### FINDING 2 — 路径遍历：CompleteUpload 导致本地存储任意文件读取

| 字段 | 内容 |
|---|---|
| **Category** | Input Validation / Information Disclosure |
| **Severity** | **High** |
| **Title** | 用户控制的 `storage_key` 允许路径遍历读取任意文件 |
| **Location** | `internal/service/file_presign.go:88-89` (CompleteUpload), `internal/storage/local.go:147` (Stat) |
| **Description** | `CompleteUpload` handler 从请求体接受 `storage_key`。服务检查 `strings.HasPrefix(in.StorageKey, actor+"/")`，但该检查无法防止路径遍历：`"user1/../../etc/passwd"` 通过前缀检查，但被 `filepath.Join` 解析后指向根目录之外。对于 `local` 存储后端，`Stat()` 会按此路径打开文件。 |
| **Attack Scenario** | 1. 攻击者 POST 到 `/v1/files/complete`，设置 `storage_key: "victim/../../etc/shadow"`<br>2. `strings.HasPrefix("victim/../../etc/shadow", "victim/")` = true<br>3. 本地后端的 `Stat()` 执行 `os.Stat("./data/files/victim/../../etc/shadow")` → 解析为 `./data/files/etc/shadow`<br>4. 攻击者获得文件大小，创建一条指向系统文件的数据库记录，然后可通过 `/v1/files/:id` 读取元数据 |
| **Impact** | 通过 `Stat()` 的响应信息泄露文件系统的文件名和大小。在 `S3` 后端上不可利用（PresignPut 作用域仅限于特定 key），但 `Stat()` 仍会击中 S3。 |
| **Recommendation** | 在将 key 传递给存储层之前，对其进行清理。对 key 的每一部分拒绝 `..`： |
| **Effort** | S（< 1 天） |

```go
// Before calling s.store.Stat:
if strings.Contains(in.StorageKey, "..") {
    return nil, apperr.BadRequest("file.invalid", "invalid storage key")
}
// Also normalize/clean the key path
```

---

### FINDING 3 — 内容类型伪造：用户提供的 MIME 类型

| 字段 | 内容 |
|---|---|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | 通过可伪造的 Content-Type 头上传的 MIME 类型绕过检查 |
| **Location** | `internal/service/file.go:163` (mimeAllowed), `internal/handler/http/file.go:69` (Upload) |
| **Description** | MIME 类型检查对 `Content-Type` HTTP 头进行操作，该头由客户端设置且易于伪造。一个可执行文件可被上传为 `image/jpeg`，绕过 `allowed_mime_types` 过滤器。上传后，MIME 类型被存储并返回给客户端，且没有服务端内容类型检测。 |
| **Attack Scenario** | 1. 攻击者将可执行文件或 HTML 上传为 `content-type: image/jpeg`<br>2. 具有宽松 `allowed_mime_types` 配置的服务器接受该文件<br>3. 如果文件通过 `mode=stream` 提供，浏览器可能根据文件内容（MIME 嗅探）而非声明的类型来渲染它 |
| **Impact** | 若文件以 `mode=stream`（代理模式）提供，则可能通过浏览器 MIME 嗅探实现跨站脚本攻击。 |
| **Recommendation** | 添加服务器端 MIME 类型检测（通过 `http.DetectContentType` 读取前 512 字节）并与其验证： |
| **Effort** | M（1-3 天） |

```go
// In service Create():
buf := make([]byte, 512)
n, _ := io.ReadFull(reader, buf)
detected := http.DetectContentType(buf[:n])
if !s.mimeAllowed(detected) {
    return nil, apperr.BadRequest("file.unsupported_type", "detected file type is not allowed")
}
// Prepend the sniffed bytes back via io.MultiReader
reader = io.MultiReader(bytes.NewReader(buf[:n]), reader)
```

---

### FINDING 4 — 缺少输入大小限制导致拒绝服务

| 字段 | 内容 |
|---|---|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **High** |
| **Title** | 评论和消息内容没有服务端长度限制 |
| **Location** | `internal/service/comment.go:72` (Create), `internal/service/message.go:68` (Send) |
| **Description** | `content` 字段在写入数据库前未经过长度验证。虽然数据库是 `TEXT`（Postgres 中理论上限 ~1GB），但恶意客户端可以发送极大数据体，消耗内存、带宽和磁盘存储。 |
| **Attack Scenario** | 攻击者通过已认证的会话发送多封包含 10MB 内容的并发消息。每条消息创建一次 GORM 插入、一个 outbox 事件以及随后针对每个收件人的通知。50 条并发的 10MB 消息消耗 ~500MB 数据库存储和显著的内存。 |
| **Impact** | 数据库存储膨胀、内存耗尽、潜在的服务降级——具有身份验证凭据的 DoS 向量。 |
| **Recommendation** | 为 `content` 字段添加配置最大长度，默认为合理值（例如，评论 10KB，消息 100KB）： |
| **Effort** | S（< 1 天） |

```go
const maxCommentLen = 10240 // 10KB
const maxMessageLen = 102400 // 100KB

func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
    if len(in.Content) > maxCommentLen {
        return nil, apperr.BadRequest("comment.too_long", fmt.Sprintf("content exceeds %d bytes", maxCommentLen))
    }
    // ...
}
```

---

### FINDING 5 — SSO 中断期间的陈旧令牌授权

| 字段 | 内容 |
|---|---|
| **Category** | Authentication / Authorization |
| **Severity** | **High** |
| **Title** | SSO 停机期间陈旧令牌可被用于延长窗口期 |
| **Location** | `internal/sso/client.go:165-167` (validateUpstream), `configs/config.yaml` (sso.stale_ttl: 300s) |
| **Description** | 当 SSO 不可用时，验证断路器和陈旧缓存回退在最多 `stale_ttl`（默认 300 秒）内提供最后已知身份。这意味着即使用户的令牌在 SSO 上被撤销，如果撤销发生在 SSO 停机之前的 300 秒内，他们可能仍能通过陈旧缓存认证。 |
| **Attack Scenario** | 1. 用户被 SSO 管理员撤销令牌<br>2. 5 秒后，SSO 出现瞬态中断（或攻击者引发 SSO 泛洪，触发断路器）<br>3. 被撤销的用户在最多 300 秒内仍可通过陈旧身份令牌获得服务授权 |
| **Impact** | 违反撤销语义——最长 5 分钟的授权窗口，超出令牌的预期生命周期。 |
| **Recommendation** | (a) 将默认 `stale_ttl` 减少到 60 秒（与 `token_cache_ttl` 匹配）<br>(b) 实现单条强制撤销端点，清除活动缓存的令牌<br>(c) 为高安全性环境添加 `stale_ttl: 0`（禁用陈旧回退）的部署选项 |
| **Effort** | M（1-3 天） |

---

### FINDING 6 — CORS 允许所有来源（生产默认值）

| 字段 | 内容 |
|---|---|
| **Category** | Compliance / Security Misconfiguration |
| **Severity** | **Medium** |
| **Title** | CORS 空来源列表默认为万能通配符 `*` |
| **Location** | `configs/config.yaml`（无 `cors_origins`），`internal/middleware/cors.go:22-27` |
| **Description** | 当 `origins` 列表为空时（生产配置文件中的默认值），CORS 中间件设置 `Access-Control-Allow-Origin: *`。这允许任何来源的浏览器向 aero-id API 发送跨源请求。 |
| **Attack Scenario** | 虽然 Bearer 令牌不会由浏览器自动附加，但利用 `X-Requested-With` 头或 `Authorization` 头的恶意单页应用如果窃取了用户的令牌，可能会从恶意来源进行 API 调用。此外，`*` 通配符不允许使用 `Access-Control-Allow-Credentials: true`，但会向任何读取响应的站点暴露公共响应。 |
| **Impact** | 增加了凭据泄露时的攻击面；暴露了合法来源限制本应防止的操作。 |
| **Recommendation** | 在生产配置中设置显式的允许来源列表： |
| **Effort** | S（< 1 天） |

```yaml
# configs/config.yaml
cors_origins:
  - "https://app.example.com"
  - "https://admin.example.com"
```

---

### FINDING 7 — 默认禁用速率限制

| 字段 | 内容 |
|---|---|
| **Category** | Denial of Service / Compliance |
| **Severity** | **Medium** |
| **Title** | 速率限制 OPT-IN 且默认禁用 |
| **Location** | `configs/config.yaml` (rate_limit.enabled: false), `internal/app/new.go:184-198` |
| **Description** | 速率限制是一个 OPT-IN 配置（默认 `enabled: false`）。生产部署若未明确启用，则没有暴力破解保护、端点耗尽防护或针对已认证用户的 DoS 防护。 |
| **Attack Scenario** | 攻击者使用单个有效账户每秒发送数千条 API 请求，消耗服务器资源、数据库连接和存储，而不会被阻止。 |
| **Impact** | 无速率限制的 DoS，数据库连接池耗尽，潜在的成本激增。 |
| **Recommendation** | 在生产配置中启用速率限制，并设置合理的默认值（例如，已认证用户 100 rps，突发 200）： |
| **Effort** | S（< 1 天） |

---

### FINDING 8 — 审计日志范围不完整

| 字段 | 内容 |
|---|---|
| **Category** | Compliance / Repudiation |
| **Severity** | **Medium** |
| **Title** | 常规 CRUD 操作未记录审计事件 |
| **Location** | 整个 `internal/service/` + `internal/audit/` |
| **Description** | 审计日志仅涵盖管理操作（授予/撤销角色、封禁/解封用户、屏蔽/解屏蔽用户、解析报告）。关键的数据操作，如**文件上传、消息发送、评论创建、文件删除、文件共享**，默认**不**记录审计事件。审计日志记录器对文件、消息和评论服务是 OPT-IN（通过 `WithFileAuditLogger` 等）。但查看 `app/new.go`，这些选项确实传递了 `auditRepo`。 |
| **Attack Scenario** | 恶意内部人员删除重要的业务文件。没有审计线索可追溯到该行为，因为删除事件未记录。 |
| **Impact** | 对内容审核、事件响应和合规用例缺乏可追溯性。违反 SOC2（审计）要求。 |
| **Recommendation** | 通过确保文件、消息和评论服务中的关键操作（创建、删除、共享）记录审计事件，使 OPT-IN 审计成为默认行为： |
| **Effort** | M（1-3 天） |

```go
// In service/file.go Delete():
if s.audit != nil {
    _ = s.audit.Log(ctx, &domain.AuditEvent{
        ActorUID:    actor,
        Action:      domain.AuditFileDelete,
        SubjectType: "file",
        SubjectID:   id.String(),
    })
}
```

*注意：在 wiring 中，`auditRepo` 确实被传递，但 `fileService` 仅在 `action=FileShareGrant/Revoke` 时记录。像 Delete 和 Create 这样的基本操作未记录。*

---

### FINDING 9 — 反应式消息的 ActorUID 参数传递设计漏洞

| 字段 | 内容 |
|---|---|
| **Category** | Authentication / Authorization |
| **Severity** | **Medium** |
| **Title** | ReactToMessage 将 actorUID 作为参数而非从上下文中提取 |
| **Location** | `internal/service/message.go:278` (ReactToMessage 签名) |
| **Description** | `ReactToMessage(ctx, messageID, actorUID)` 和 `UnreactMessage(ctx, messageID, actorUID)` 将 `actorUID` 作为显式参数。虽然当前 HTTP 和 gRPC handler 通过 `service.ActorFrom(ctx)` 正确提取它，但这种设计允许未来调用者传递任意 uid，从而绕过身份验证。服务遵循的常规模式（例如 `CommentService.Like`）是从上下文中提取。 |
| **Attack Scenario** | 未来某天，一个内部调用者使用从用户输入推导的 `actorUID` 调用 `ReactToMessage`，允许任意用户模拟。 |
| **Impact** | 对消息反应 API 的可能授权绕过。 |
| **Recommendation** | 重构以遵循标准模式——从 `ctx` 内部提取 actor： |
| **Effort** | S（< 1 天） |

```go
func (s *messageService) ReactToMessage(ctx context.Context, messageID domain.ID) error {
    actor, err := mustActor(ctx)
    if err != nil {
        return err
    }
    // ...
}
```

---

### FINDING 10 — 缺少 HTTP 安全头

| 字段 | 内容 |
|---|---|
| **Category** | Compliance / Data Protection |
| **Severity** | **Medium** |
| **Title** | HTTP 响应缺少安全头（HSTS、X-Content-Type-Options、CSP） |
| **Location** | `internal/handler/http/router.go`（中间件链） |
| **Description** | aero-id 的 Gin 引擎未设置以下安全头：`Strict-Transport-Security`、`X-Content-Type-Options: nosniff`、`Content-Security-Policy`、`X-Frame-Options`、`Referrer-Policy`。这使得客户端面临 MIME 嗅探、点击劫持和协议降级攻击。 |
| **Attack Scenario** | 中间人攻击者将 `https://api.aero-id.example.com` 降级为 HTTP（若未强制 HSTS）。若通过流式提供用户上传的文件且没有 `X-Content-Type-Options: nosniff`，浏览器会进行 MIME 嗅探，可能将文本文件渲染为 HTML。 |
| **Impact** | 增加 XSS、点击劫持和协议降级的风险。 |
| **Recommendation** | 添加全局安全头中间件： |
| **Effort** | S（< 1 天） |

```go
// internal/middleware/security_headers.go
func SecurityHeaders() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-Frame-Options", "DENY")
        c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
        c.Next()
    }
}
```

---

### FINDING 11 — 数据库连接中的明文密码

| 字段 | 内容 |
|---|---|
| **Category | Cryptography / Data Protection |
| **Severity** | **Medium** |
| **Title** | 密码嵌入 PostgreSQL DSN，可能通过错误 / 日志暴露 |
| **Location** | `internal/config/config.go:120-123` (DSN()) |
| **Description** | `DatabaseConfig.DSN()` 使用 `fmt.Sprintf` 将密码构建到连接字符串中。虽然机密性由环境变量保护，但 DSN 嵌入在 `*sql.DB` 中，可能通过日志记录、错误消息或 `process stats` 暴露。 |
| **Attack Scenario** | 包含密码的 DSN 字符串可能出现在错误日志条目中、GORM 调试日志（若启用）或通过 `/proc/self/environ` 暴露。 |
| **Impact** | 数据库凭据泄露。 |
| **Recommendation** | 使用 `net/url` 为 DSN 构建 `Values` 以避免字符串连接错误，并确保日志过滤器屏蔽密码： |
| **Effort** | S（< 1 天） |

```go
func (d DatabaseConfig) DSN() string {
    v := url.Values{}
    v.Set("sslmode", d.SSLMode)
    v.Set("TimeZone", "UTC")
    return fmt.Sprintf("host=%s port=%d user=%s dbname=%s password=%s %s",
        d.Host, d.Port, url.QueryEscape(d.User), d.DBName,
        url.QueryEscape(d.Password), v.Encode())
}
```

---

### FINDING 12 — 分享文件不进行 SSO UID 验证

| 字段 | 内容 |
|---|---|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | File Share 接受任意 UID 而不验证其在 SSO 中的存在 |
| **Location** | `internal/service/file_share.go:40-60` (ShareFile) |
| **Description** | `ShareFile` 接受来自客户端的 grantee UID 字符串列表，并且仅检查非空值和去重。它不验证 UID 是否对应 SSO 中的真实用户。分享给不存在用户的文件会造成“幽灵分享”和垃圾数据。 |
| **Attack Scenario** | 攻击者与一批虚假 UID 分享文件，使 `file_shares` 表膨胀。列表端点须返回这些条目，导致性能下降。 |
| **Impact** | 分享表膨胀；无法验证分享是否真正授予真实用户。 |
| **Recommendation** | 批量验证 grantee UID（通过 SSO 的 `GetUsers`）并拒绝任何未知的： |
| **Effort** | S（< 1 天） |

```go
// In ShareFile, after dedup validation:
users, err := s.dir.GetUsers(ctx, grantees)
if err != nil {
    return apperr.Internal("file.user_lookup_failed", "failed to verify recipients")
}
for _, g := range grantees {
    if _, ok := users[g]; !ok {
        return apperr.BadRequest("file.invalid_grantee", "unknown user: "+g)
    }
}
```

---

### FINDING 13 — 搜索端点的 SQL 注入（低风险）

| 字段 | 内容 |
|---|---|
| **Category** | Input Validation |
| **Severity** | **Low** |
| **Title** | SearchFilesLike 使用用户输入构建 LIKE 模式 |
| **Location** | `internal/repository/file.go:217` (SearchFilesLike) |
| **Description** | `SearchFilesLike` 构建 `pattern := "%" + query + "%"` 以供 ILIKE 使用。虽然 GORM 参数化查询，但 LIKE 模式中的 `%` 和 `_` 等特殊字符与 SQL 通配符匹配，可能导致意外结果，而不是注入。 |
| **Attack Scenario** | 攻击者搜索 `%`，返回所有文件。搜索 `_`，匹配单个字符。 |
| **Impact** | 由于 GORM 参数化，SQL 注入风险极小。但 LIKE 模式可能导致信息泄露或数据库负载过大。 |
| **Recommendation** | 转义 LIKE 特殊字符： |
| **Effort** | S（< 1 天） |

```go
func escapeLike(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `%`, `\%`)
    s = strings.ReplaceAll(s, `_`, `\_`)
    return s
}
```

---

### FINDING 14 — 预签名上传响应中的 Key 泄漏

| 字段 | 内容 |
|---|---|
| **Category** | Information Disclosure |
| **Severity** | **Low** |
| **Title** | PresignUpload 响应暴露存储 URL 和存储键 |
| **Location** | `internal/handler/http/file.go:106-111` (PresignUpload) |
| **Description** | `PresignUpload` handler 返回 `storage_key` 和 `upload_url`。存储键遵循可预测的模式（`<owner>/<yyyy>/<mm>/<uuid>`）。拥有多个文件共享同一所有者的攻击者可以推断存储布局。 |
| **Attack Scenario** | 这主要是信息性/低风险，因为键本身不提供访问权限，且预签名 URL 有时间限制。 |
| **Impact** | 存储架构的轻微信息泄露。 |
| **Recommendation** | 考虑在 future 迭代中从响应中省略 `storage_key`；它仅在 `CompleteUpload` 中需要，而后者已经可从客户端获取。 |

---

## STRIDE 威胁模型分析

| 威胁 | 发现 |
|---|---|
| **S**poofing（身份欺骗） | SSO 令牌缓存使用最强（60 秒 TTL）。陈旧回退会延长该窗口。需要管理员引导修复。 |
| **T**ampering（数据篡改） | Outbox webhook 使用 HMAC-SHA256 签名（良好）。预设 URL 使用 HMAC（local）或 AWS SigV4（S3）（良好）。Content-type 上传可被伪造。 |
| **R**epudiation（否认） | 仅审计管理操作。文件/消息/评论 CRUD 缺失审计事件。 |
| **I**nformation Disclosure（信息泄露） | `CompleteUpload` 路径遍历允许本地存储上读取任意文件。错误不会泄露内部细节。 |
| **D**enial of Service（拒绝服务） | 默认禁用速率限制。评论/消息无大小限制。预设 URL 无限期有效。 |
| **E**levation of Privilege（权限提升） | Admin 引导陷入死锁。`ReactToMessage` 设计允许潜在的 actor 伪造。 |

---

## OWASP Top 10 (2021) 映射

| OWASP 类别 | 关联发现 |
|---|---|
| A01:2021-Broken Access Control | F1（Admin 引导），F9（ReactToMessage actor） |
| A02:2021-Cryptographic Failures | F11（DSN 中的密码） |
| A03:2021-Injection | F13（LIKE 搜索中的 SQL 注入） |
| A04:2021-Insecure Design | F4（无大小限制），F7（无速率限制），F6（CORS） |
| A05:2021-Security Misconfiguration | F6（CORS 为 `*`），F10（头缺失），F7（速率限制关闭） |
| A07:2021-Identification & Auth Failures | F5（陈旧令牌），F12（UID 验证） |
| A08:2021-Software & Data Integrity | F2（路径遍历） |
| A09:2021-Security Logging & Monitoring | F8（审计缺口） |

---

## 最终摘要

### 总体安全态势：**需要改进**

aero-id 代码库展示了出色的架构纪律——服务层强制执行所有权检查，错误抽象不会泄露内部细节，HMAC 用于完整性，缓存受到 TTL 限制。然而，几个严重缺陷需要在生产部署前解决。

### 前 3 个关键问题

| # | 问题 | 严重性 | 努力 |
|---|---|---|---|
| 1 | **Admin 引导死锁** — 无法提升第一个管理员 | 关键 | S |
| 2 | **路径遍历 via CompleteUpload** — 本地存储上读取任意文件 | 高 | S |
| 3 | **无内容大小限制** — 已验证的 DoS 向量 | 高 | S |

### 前 3 个速胜项（高影响，低努力）

| # | 问题 | 努力 | 影响 |
|---|---|---|---|
| 1 | **Admin 引导** — 在 `app/new.go` 中添加 `AERO_ID_ADMIN_UID` 环境变量 | < 1 天 | 解决管理死锁 |
| 2 | **Storage_key 清理** — 在 `CompleteUpload` 中拒绝 `..` | < 1 天 | 消除路径遍历 |
| 3 | **内容大小限制** — 在评论/消息服务中添加最大长度检查 | < 1 天 | 防止存储 DoS |

### 安全债务

| 债务项 | 努力（估计） | 注意事项 |
|---|---|---|
| 常规操作的审计日志记录 | M（1-3 天） | 目前缺失的 SOC2 要求 |
| 安全头中间件 | S（< 1 天） | 点击劫持/XSS 防护 |
| 服务器端 MIME 检测 | M（1-3 天） | 关闭内容类型伪造 |
| 陈旧令牌回退缩减 | M（1-3 天） | 减少撤销窗口 |
| 启用速率限制配置 | S（< 1 天） | 环境特定配置 |
| CORS 限制 | S（< 1 天） | 生产部署要求 |
| UID 共享验证 | S（< 1 天） | 防止幽灵分享 |

---

**最终备注**：Aero-id 的架构使其具有相对良好的安全基础——责任分离、集中的授权（服务层检查）、SSO 集成与断路器以及统一的错误处理。上述问题代表了其安全态势中的关键缺口，而非系统性问题。三个优先修复项（Admin 引导、路径遍历、内容限制）是独立且低努力的——每个可在 <1 天内完成，并消除最重要的生产风险。
