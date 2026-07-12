现在我拥有了全面的代码理解。让我整理全面的安全审查报告。

---

# Security Review Report — aero-id

## 总体安全态势：良好

该代码库由安全意识强的工程师编写。清晰的关注点分离（handler → service → repository）、一致的授权模式、传输级加固以及自适应的降级行为都表明具有生产级别的安全思维。以下是按类别归类的发现。

---

## 发现

### 1. 身份验证与授权

| 字段 | 描述 |
|---|---|
| **类别** | 身份验证与授权 |
| **严重性** | **高** |
| **标题** | CORS 默认允许任意来源，暴露于基于浏览器的 CSRF |
| **位置** | `internal/middleware/cors.go` 第 24 行 — `allowAll := len(origins) == 0` |
| **描述** | 当 `RouterConfig.CORSOrigins` 为空（默认）时，中间件设置 `Access-Control-Allow-Origin: *`。结合 Bearer 认证，任何网站都可以使用 JavaScript 发出经过身份验证的请求并读取完整的响应。CORS 对带有 `Authorization` 头的请求强制执行预检请求，但 `Access-Control-Allow-Origin: *` 的响应告诉浏览器“把响应数据交给调用方 JS”。这使得攻击者网站可以逐字窃取所有经过身份验证的端点的响应。 |
| **攻击场景** | 1. 用户登录 aero-id（令牌存储在 localStorage）<br>2. 用户访问 `evil.com`<br>3. `evil.com` 的 JS 运行 `fetch('https://aero-id.example.com/v1/messages/inbox', {headers: {'Authorization': 'Bearer ...'}})`<br>4. 浏览器发送预检请求，获得 `Access-Control-Allow-Origin: *`，然后发送 GET 请求并读取所有私有消息 |
| **影响** | 完全读取所有经过身份验证的 API 响应 — 消息、文件元数据、通知、管理审计日志 |
| **建议** | 将 CORS 默认策略更改为**拒绝所有**。仅当明确配置时才反映来源。此外，添加 `Vary: Origin` 头。修正：<br><br>```go<br>allowAll := false  // 默认不再允许所有来源<br>if len(origins) == 0 {<br>    // 无来源配置 = 禁止 CORS 访问（安全默认）<br>    return func(c *gin.Context) {<br>        // 不允许跨来源请求<br>        if c.Request.Method == http.MethodOptions {<br>            c.AbortWithStatus(http.StatusForbidden)<br>            return<br>        }<br>        c.Next()<br>    }<br>}<br>``` |
| **工作量** | S (< 1 天) |

---

| 字段 | 描述 |
|---|---|
| **类别** | 身份验证与授权 |
| **严重性** | **中** |
| **标题** | 令牌撤销延迟：SSO 故障期间最多 5 分钟的过时缓存认证窗口 |
| **位置** | `internal/sso/token_cache.go` 第 72-80 行 (`GetStale`)，`internal/config/config.go` — `StaleTTL: 300s` |
| **描述** | 当 SSO 不可达时，`validateUpstream` 回退到 `GetStale(token)`，如果令牌在此前的 5 分钟内（stale_ttl 默认为 300 秒）已被缓存，则返回缓存的 uid。如果用户的令牌在 SSO 故障期间被撤销（例如管理员禁止帐户、用户更改密码），则旧的缓存映射仍然允许在该 5 分钟窗口内访问所有资源。注意：这是一个有意的弹性权衡，但在合规性与可用性优先的环境中需要显式记录。 |
| **攻击场景** | 1. SSO 经历间歇性网络中断<br>2. 管理员禁止一个滥用用户<br>3. 在被禁止的令牌旋转之前，它在过时缓存中仍然有效最多 5 分钟<br>4. 用户继续创建/删除文件，检查审计日志中的操作 |
| **影响** | 令牌撤销最多延迟 5 分钟。 |
| **建议** | 1. 在运维 Runbook 中记录失效窗口。<br>2. 考虑在 Dispatcher 中添加一个强制禁止检查（`banRepo` 已经传递给 Dispatcher），以便即使在过时缓存期间也能拒绝操作。运行时禁止检查比信任旧令牌更好。<br>3. 或者尽可能将 stale_ttl 减少到 60s。 |
| **工作量** | M（增加运行时禁止检查 + 文档） |

---

### 2. 输入验证

| 字段 | 描述 |
|---|---|
| **类别** | 输入验证 |
| **严重性** | **中** |
| **标题** | 元数据 JSON 字段中任意深度/大小的用户输入 |
| **位置** | `internal/handler/http/file.go` 第 97 行 (`Meta map[string]any`)，`internal/handler/http/message.go` 等 |
| **描述** | 多个处理程序将 JSON 请求体解组到 `map[string]any` 中，然后直接存储到数据库中。没有深度/大小限制。虽然 `Meta` 字段的意图是灵活的，但攻击者可以提交：(a) 递归嵌套导致 GORM 序列化 OOM，(b) 数千个键导致极端行大小，(c) 元数据中的二进制 BLOB，破坏下游处理。相同模式存在于消息、评论、管理端点的 `Meta` 字段中。 |
| **攻击场景** | 攻击者 POST 一个具有 `{"meta": {"a": {"b": {"c": ... 10000 level deep}}}}` 的文件。数据库驱动或 JSON 序列化器在尝试存储时崩溃或分配过多内存，可能导致 DoS。 |
| **影响** | 拒绝服务（应用程序崩溃或数据库存储过度膨胀）。 |
| **建议** | 在元数据上添加服务层验证：<br>1. JSON 深度限制（例如 5 个嵌套层级）<br>2. 键数限制（例如 50 个键）<br>3. 序列化元数据总大小限制（例如 4 KB） |
| **工作量** | S （添加领域层常量 + 在 `file_service.Create`、`message_service.Send` 等处进行验证） |

---

| 字段 | 描述 |
|---|---|
| **类别** | 输入验证 |
| **严重性** | **低** |
| **标题** | DSN 字符串格式化 — 密码中的特殊字符 |
| **位置** | `internal/config/config.go` 第 136 行 — `DatabaseConfig.DSN()` |
| **描述** | DSN 是使用 `fmt.Sprintf` 构建的：`host=%s port=%d user=%s password=%s dbname=%s sslmode=%s`。如果密码包含特殊字符（空格、`'`、反斜杠），这些字符不会被转义。Go 的 `pgx` 驱动处理 URL 编码，但 `lib/pq` 格式的 DSN 期望键值对中的值被引用或转义。 |
| **攻击场景** | 如果密码包含空格（例如 `my pass`），生成的 DSN 是 `password=my pass`，这会被解析器错误分割。实际上这会导致连接失败，而不是注入。真正的风险很低，因为有问题的配置在启动时失败。 |
| **影响** | 使用特殊字符的密码的连接失败。 |
| **建议** | 使用 `net/url` 编码密码值，或切换到标准 PostgreSQL URI 格式（`postgres://user:password@host/db`），该格式正确处理 URL 编码。或者，在 DSN 方法中添加注释记录这一限制。 |
| **工作量** | S |

---

### 3. 密码学

| 字段 | 描述 |
|---|---|
| **类别** | 密码学 |
| **严重性** | **高** |
| **标题** | 默认签名密钥在配置中（HMAC 伪造风险） |
| **位置** | `configs/config.yaml` — `signing_key: change-me-me-please-very-long-random` |
| **描述** | 签名密钥在 `configs/config.yaml` 中默认为 `change-me-me-please-very-long-random`。`config.validate()` 检查密钥非空，但不检查它是否是默认值。任何生产部署忘记更改此密钥的风险在于，公共 `/files/dl` 下载 URL 的 HMAC 可以被任何人伪造。所有预签名下载 URL 都是通过 `presignHMAC(key, expires, signingKey)` 使用此密钥签名的。 |
| **攻击场景** | 1. 部署使用默认 `signing_key`<br>2. 攻击者知道默认密钥<br>3. 攻击者构造 `https://aero-id.example.com/files/dl?key=...&exp=9999999999&sig=...` 使用任意文件存储密钥<br>4. 攻击者可以读取存储中的任何文件 — 绕过所有权限 |
| **影响** | 完全读取所有存储的文件，绕过所有权和共享 ACL。 |
| **建议** | 在 `config.validate()` 中添加启动时检查：如果签名密钥是已知默认值，则中止启动：<br><br>```go<br>if c.SigningKey == "change-me-please-very-long-random" {<br>    return fmt.Errorf("signing_key must be changed from the default")<br>}<br>```<br>同时在启动日志中添加一个显式的 `WARN` 级别日志条目。 |
| **工作量** | S |

---

| 字段 | 描述 |
|---|---|
| **类别** | 密码学 |
| **严重性** | **中** |
| **标题** | S3 密钥材料在内存中保存时间过长 |
| **位置** | `internal/storage/s3.go` 第 36-40 行，`internal/config/config.go` 第 85-86 行 |
| **描述** | S3 访问密钥和秘密密钥作为字符串存储在 `S3Config` 结构体中，并在 `NewS3` 中传递给 AWS SDK 的 `NewStaticCredentialsProvider`。在此之后，`config/s3.go` 中的原始字符串可能仍然作为 `Config` 结构体上的字段存在，直到垃圾回收。在 `sso.NewClient` 中，TLS 密钥文件路径（但不是密钥本身）存储在全局配置中。虽然不是严重漏洞，但让机密数据在启动后仍然存在于结构体字段中会增加它们在运行时通过 `runtime.ReadMemStats` 或 `/debug/pprof/heap` 暴露的风险（如果这些端点被意外暴露）。 |
| **攻击场景** | 如果 `/debug/pprof/heap` 被暴露（通过运维调试），攻击者可以读取堆转储，提取 S3 密钥或 SSO TLS 密钥路径。 |
| **影响** | 访问 S3 存储桶。 |
| **建议** | 启动后清除敏感配置字段：在 `NewS3(...)` 之后，设置 `cfg.Storage.S3.AccessKey = ""`；`cfg.Storage.S3.SecretKey = ""`。同样，也可以在 SSO 客户端初始化后清除 `cfg.SSO.TLSKeyFile`（虽然不是密钥本身，但路径提供了侦察信息）。 |
| **工作量** | S |

---

### 4. 会话管理

| 字段 | 描述 |
|---|---|
| **类别** | 会话管理 |
| **严重性** | **低** |
| **标题** | 无安全头（HSTS、X-Content-Type-Options、CSP） |
| **位置** | `internal/handler/http/router.go` 第 42-52 行 — 全局中间件注册 |
| **描述** | HTTP 服务器未设置安全相关头：`Strict-Transport-Security`、`X-Content-Type-Options`（`nosniff`）、`X-Frame-Options`（`DENY`）或 `Content-Security-Policy`。这增加了浏览器端攻击面。 |
| **攻击场景** | 缺少 `X-Content-Type-Options: nosniff` 使得攻击者上传带有隐藏 HTML/JS 的 `.txt` 文件，当通过 HMAC 链接下载时可能在浏览器中被解释为 HTML，允许 XSS。缺少 HSTS 使得 SSL 剥离攻击成为可能（尽管这不是 Web 应用）。 |
| **影响** | 浏览器端攻击面增加；可能通过上传感染的文件执行 XSS。 |
| **建议** | 添加安全头中间件，设置最低限度：<br>- `X-Content-Type-Options: nosniff`（防止 MIME 嗅探）<br>- `X-Frame-Options: DENY`（防止点击劫持）<br>- `Strict-Transport-Security: max-age=31536000; includeSubDomains`（如果使用 HTTPS）<br>- `Content-Security-Policy: default-src 'none'` 用于公共源服务 |
| **工作量** | S |

---

### 5. 数据保护

| 字段 | 描述 |
|---|---|
| **类别** | 数据保护 |
| **严重性** | **中** |
| **标题** | 幂等响应主体存储在 GORM 中，包含敏感数据 |
| **位置** | `internal/idempotency/store.go` 第 33 行 (`Response []byte`)，第 115 行 (`Complete`) |
| **描述** | GORM 幂等存储将完整的 HTTP 响应主体保存在 `idempotency_keys` 表中的 `response` 列中，TTL 为 24 小时。响应主体可能包含消息内容、文件元数据、通知负载、用户信息等。具有数据库只读访问权限的攻击者可以读取此表并收集所有已缓存幂等响应的敏感数据。对于 FAPI 或 SOC2 环境，这违反了按需最小化原则。 |
| **攻击场景** | 1. 攻击者获得对 Postgres 的只读访问权限（例如通过 SQL 注入在相关表中）<br>2. 执行 `SELECT response FROM idempotency_keys WHERE ...`<br>3. 提取响应主体，可能包含 PII（用户 id、昵称、消息内容） |
| **影响** | PII 泄漏到数据库的日志或只读备份中。 |
| **建议** | 选项 1（推荐）：使用 Redis 作为幂等存储（已经可用），其中键在 TTL 后自动过期，并且内存中的持久性较弱。<br><br>选项 2：加密 `response` 列值或在存储前编辑敏感字段。<br><br>选项 3：使用可选的响应过滤（例如仅为幂等重放存储状态代码和标头，但不存储主体，要求客户端在需要时重新获取）。 |
| **工作量** | M（切换到 Redis，或添加响应主体加密） |

---

| 字段 | 描述 |
|---|---|
| **类别** | 数据保护 |
| **严重性** | **中** |
| **标题** | 访问日志记录完整的请求 URI，包括查询参数 |
| **位置** | `internal/middleware/access_log.go` 第 24 行 (`"uri", c.Request.URL.Path` — 但代码显示 `"uri", c.Request.URL.Path` 所以实际上不是完整的 URI) |
| **描述** | 复查：日志记录 `c.Request.URL.Path`（不是完整的 URL），所以查询参数不会被记录。这是正确的。然而，`actor_uid` 是记录下来的，这被认为适合审计。没有发现数据泄露问题。 |
| **影响** | 无 — 查询参数被正确排除。 |
| **建议** | 无 — 正确。 |
| **工作量** | N/A |

让我再次检查 access_log.go... 实际上它记录 `c.Request.URL.Path` 和 `c.FullPath()`，所以查询参数被正确排除。好的，数据保护合规性良好。让我添加一个关于请求主体记录的具体发现...

实际上，没有请求主体记录，所以这是安全的。

---

### 6. 威胁模型（STRIDE）

| 类别 | 评估 |
|---|---|
| **欺骗** | ✅ 强力。所有 /v1/* 路由都需要通过 SSO 验证的 Bearer 令牌。令牌在缓存中（默认 TTL：60s）。令牌撤销的过时缓存期最长为 300s。预签名下载链接使用 HMAC-SHA256 签名。 |
| **篡改** | ✅ 跨传输的良好完整性。gRPC 支持 mTLS。HTTP 依赖于 TLS（如果配置）。webhook 事件由 HMAC-SHA256 签名（X-Event-Signature）。文件存储不提供完整性检查，但 SHA256 校验和对创建时的文件进行哈希处理。 |
| **否认** | ✅ 审计日志记录文件、消息、评论、块和禁令的所有敏感操作。审计条目以追加方式写入。 |
| **信息泄露** | ⚠️ **高**：CORS 允许任何来源，使所有 API 响应向跨来源读取方暴露。<br>⚠️ **中**：幂等主体存储在 Postgres 中并包含响应数据。<br>✅ 访问日志仅记录路径（无查询参数）。错误不会泄露原始数据库错误。 |
| **拒绝服务** | ✅ gRPC keepalive 执行（最少 10s ping），`PermitWithoutStream: false`；HTTP `ReadHeaderTimeout: 10s` 用于防 Slowloris；`MaxConcurrentStreams: 100`；`MaxRecvMsgSize: 4 MiB`；每个实例的速率限制；速率限制器的空闲桶驱逐。⚠️ 注意：速率限制是每个实例的（无分布式/Redis 后端）。 |
| **权限提升** | ✅ RBAC 通过 `PermissionInterceptor` 在 gRPC 上强制执行管理操作。文件所有权、消息参与者和评论作者身份在服务层检查。禁止检查和偏好检查在通知调度程序中强制执行。 |

---

### 7. 合规考虑

| 领域 | 评估 |
|---|---|
| **OWASP Top 10** | A01:权限 — ✅（服务层所有权检查）<br>A02:密码学 — ⚠️（默认密钥）<br>A03:注入 — ✅（GORM（参数化查询），无 SQL 拼接）<br>A04:不安全设计 — ⚠️（CORS 默认允许所有）<br>A05:安全配置 — ⚠️（默认密钥）<br>A06:易受攻击组件 — ✅（Go 1.26，最新依赖）<br>A07:身份验证 — ✅（基于 SSO）<br>A08:完整性 — ✅（webhook 的 HMAC，文件的 SHA256）<br>A09:日志记录 — ✅（审计日志 + 访问日志 + 采样）<br>A10:SSRF — ⚠️（webhook URL，但仅限于出站事件） |
| **GDPR** | 通知清理器在 30 天后删除已读通知，90 天后删除未读通知。审计日志在 90 天后清除。幂等键在 24 小时后过期。整体数据生命周期良好。需要文档说明数据主体的删除请求流程（目前未发现）。 |
| **SOC2** | 审计日志覆盖所有敏感操作。变更管理通过版本控制。需要文档说明密钥轮换策略（签名密钥、S3 密钥、SSO 密钥）。 |

---

### 8. 供应链安全

| 字段 | 描述 |
|---|---|
| **类别** | 供应链 |
| **严重性** | **中** |
| **标题** | Docker CI 在 PR 上推送镜像 |
| **位置** | `.github/workflows/docker.yml` 第 32 行 — `push: ${{ github.event_name != 'pull_request' }}` |
| **描述** | Docker 工作流程仅在非 PR 事件时推送，但在 PR 上构建。这是正确的。然而，发布工作流程将 COSIGN_PRIVATE_KEY 和 COSIGN_PASSWORD 暴露给 goreleaser 步骤。任何工作流程的泄露都可能导致签名密钥被窃取。限制是：只有具有 `contents: write` 的工作流程可以触发发布，这严格限制了哪些上下文可以访问密钥。 |
| **建议** | 添加"环境"保护以将 COSIGN 密钥限制为仅 `v*` 标签推送。 |
| **工作量** | S |

---

## 最终总结

### 整体安全态势：良好

aero-id 代码库展示了高安全成熟度。架构强制执行清晰的关注点分离，每个入口点都检查身份验证，使用现代密码学（HMAC-SHA256、X.509 证书固定），并且数据生命周期管理内置于每个表中。

### 前 3 个关键问题

| # | 问题 | 严重性 | 影响 |
|---|---|---|---|
| 1 | **默认 CORS 允许所有来源** — 默认 `*` 的配置使所有经过身份验证的端点暴露于浏览器端窃取攻击。 | **高** | 攻击者网站可以读取所有经过身份验证的 API 响应。 |
| 2 | **默认签名密钥（change-me-please）** — 默认密钥如果未更改，将使公共文件下载链接的 HMAC 认证无效。 | **高** | 攻击者可以读取存储中的任何文件。 |
| 3 | **幂等响应主体存储在 Postgres** — 完整的敏感 API 响应在 `idempotency_keys` 中存储最多 24 小时。 | **中** | 数据库泄露会暴露响应数据（消息、文件等）。 |

### 前 3 个快速制胜

| # | 修复 | 工作量 |
|---|---|---|
| 1 | 将 CORS 默认更改为拒绝所有。删除 `allowAll := len(origins) == 0`。 | **< 1 天** |
| 2 | 添加启动时配置验证以拒绝默认签名密钥。 | **< 1 天** |
| 3 | 添加安全头中间件（X-Content-Type-Options、HSTS、X-Frame-Options）。 | **< 1 天** |

### 安全债务

| 债务 | 类型 | 工作量 |
|---|---|---|
| SSO 故障期间最多 5 分钟的令牌撤销延迟 | 弹性与安全 | **中**（添加运行时禁止检查） |
| 通过幂等响应在数据库中存储敏感数据 | 数据保护 | **中**（迁移到 Redis 后端或在启动时清除） |
| 速率限制是每个实例的（非分布式） | DoS | **大**（Redis 后端） |
| 元数据字段缺乏模式/深度验证 | 输入验证 | **小**（服务层边界检查） |
| 启动后清除配置机密（S3 密钥、签名密钥） | 数据保护 | **小**（启动后清除结构体字段） |

### 安全头部检查单 — 缺失

- [x] X-Content-Type-Options: nosniff —— **缺失**
- [x] X-Frame-Options: DENY —— **缺失**
- [x] Strict-Transport-Security —— **缺失**
- [x] Content-Security-Policy —— **缺失**
- [x] X-Aero-Degraded —— **存在** ✅
- [x] Access-Control-Allow-Origin —— **存在，但有漏洞** ⚠️
