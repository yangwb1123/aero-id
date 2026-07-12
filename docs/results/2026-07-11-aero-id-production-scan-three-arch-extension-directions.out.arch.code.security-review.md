现在我已经对整个代码库有了全面的了解，让我来编写系统的安全审查。

---

# 安全审查：aero-id 服务

## 方法论
对实现用户认证、文件存储、消息传递、评论、通知、管理操作和出站模式的 Go gRPC/HTTP 服务进行了彻底的代码审查。完全采用 **STRIDE** 分析和 **OWASP Top 10** 参考。

---

## 发现结果

### 发现结果 #1 — 严重：本地存储后端存在路径遍历 ⇒ 任意文件读取

| 字段 | 描述 |
|-------|-------------|
| **类别** | 输入验证 / 威胁模型（篡改） |
| **严重性** | **严重** |
| **标题** | 本地文件下载中的路径遍历，绕过 HMAC 验证 |
| **位置** | `internal/storage/local.go:70` — `func (l *Local) Get(ctx context.Context, key string)` |
| **描述** | `keyToPath` 方法使用 `filepath.Join(l.cfg.Root, key)` 将存储键解析为文件系统路径。`key` 参数最终来自未经验证的用户输入（来自公共 `/files/dl?key=...` 端点的查询参数 `key`）。尽管存在 HMAC 签名验证，但如果攻击者能为包含路径遍历序列的 `key` 获取有效签名，他们就可以读取根目录之外的任意文件。 |
| **攻击场景** | 1) 攻击者从合法的预签名 URL 获取有效的 `(key, expires, sig)` 元组，例如 `/files/dl?key=user1/2026/07/uuid&exp=9999999999&sig=abc`。2) 他们注意到签名是针对原始 key+expires 的——路径遍历序列 `../` 被嵌入到 key 中。如果 SSO 泄露或存在允许任意 key 签名的内部滥用（例如，拥有存储访问权限的管理员），攻击者可以请求 `/files/dl?key=../../../etc/shadow&exp=9999999999&sig=<re-signed>`。3) `Local.Get` 打开 `filepath.Join("/data/files", "../../../etc/shadow")` ⇒ `/etc/shadow`。 |
| **影响** | 读取任意系统文件（passwd、shadow、应用配置、TLS 密钥）。完全系统泄露。 |
| **建议** | 在 `keyToPath` 中添加路径清理，或在使用前验证 key 仅包含允许的字符（字母数字、斜杠、破折号、下划线）。**更好的方案**：对所有键强制执行 `filepath.Clean` 并拒绝包含 `..` 的键： |
| **工作量** | S（< 1 天） |

```go
// local.go — 在 keyToPath 或 Get/Delete/Stat 的开头添加
func (l *Local) keyToPath(key string) string {
    clean := filepath.Clean(filepath.Join("/", key)) // 防止遍历到根目录之外
    return filepath.Join(l.cfg.Root, clean)
}
// 另外，在 Get 中验证：
if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
    return nil, ErrNotFound  // 或 ErrInvalidKey
}
```

---

### 发现结果 #2 — 严重：内容去重 TOCTOU 竞态 ⇒ 存储损坏 / 数据泄露

| 字段 | 描述 |
|-------|-------------|
| **类别** | 威胁模型（篡改 / 拒绝服务） |
| **严重性** | **严重** |
| **标题** | 文件创建中的检查时间与使用时间竞态条件 |
| **位置** | `internal/service/file_service.go:109-128` — 去重逻辑 |
| **描述** | `Create` 方法首先将 blob 写入存储（`store.Put`），然后检查是否已存在具有相同校验和和所有者的文件（`FindByChecksum`）。在 Put 和 FindByChecksum 之间，另一个并发 goroutine 可能会写入相同的键。两个 goroutine 都可以：1) 都写入 blob，2) 都检查到另一个刚刚写入的 blob，3) 都删除自己的副本，然后都返回指向**同一个** `storage_key` 的 `domain.File`。如果后续其中一个文件被删除，`CountByStorageKey`（或 `refs.Decrement`）可能显示引用计数为 0，从而删除共享 blob，使另一个文件失效。 |
| **攻击场景** | 1) 用户同时上传同一文件两次（例如，curl 并行运行）。2) 两个请求都写入相同的 checksum 键。3) 请求 A 将其 blob 写入 `key-A`。请求 B 将其 blob 写入 `key-B`。4) A 检查校验和 → 未找到 → 创建行 → 未调用删除。5) B 检查校验和 → 找到 A 的行 → 删除 `key-B` → 使用 A 的 `key-A` 创建行。6) 用户删除文件 B → `CountByStorageKey("key-A")` 返回 1（文件 A 存在）→ blob 保留。但如果 A 和 B 都指向同一个键，且 `CountByStorageKey` 返回 1，删除操作可能会删除 blob，**使另一个文件损坏**。 |
| **影响** | 文件损坏、静默数据丢失，以及并发写入/删除场景下的访问错误。 |
| **建议** | 使用数据库级唯一约束（`files(owner_uid, checksum)`），使重复的检查 + 插入具有原子性。或者，仅在数据库行安全创建后删除 blob：使用独占锁（Postgres `SELECT ... FOR UPDATE`）或带有重试的 upsert 模式。 |
| **工作量** | M（1-3 天） |

---

### 发现结果 #3 — 高：带外存储（本地）的预签名 URL 可被用户枚举

| 字段 | 描述 |
|-------|-------------|
| **类别** | 数据保护 / 信息泄露 |
| **严重性** | **高** |
| **标题** | 预签名下载 URL 是静态的且具有长 TTL（24 小时） |
| **位置** | `internal/handler/http/file.go:90` — `urlTTL`；`internal/file/service_crud.go` — `PresignGet` |
| **描述** | 预签名下载 URL 的 TTL 为 24 小时（默认 `idempotency_ttl`）。一旦共享，链接在 24 小时内有效，无论文件权限是否更改（例如，所有者在链接共享后撤销了共享）。此外，对于本地后端，HMAC 签名基于 `(key, expires)` 对——如果知道 `signing_key`（来自配置泄露），就可以**永久**伪造任意文件的 URL。 |
| **攻击场景** | 1) 用户与协作者共享文件。2) 协作者获取预签名 URL。3) 文件所有者撤销了共享。4) 协作者仍然可以使用预签名 URL 长达 24 小时（或更长，如果链接在他们撤销前被保存）。 |
| **影响** | 文件共享撤销后，访问权限仍然持续。 |
| **建议** | 1) 缩短预签名 URL TTL（例如，15 分钟，要求客户端立即下载）。2) 添加服务器端撤销检查：每次下载时，验证文件尚未从用户处撤销（但会破坏匿名下载的整个目的）。3) 记录并在文档中说明此限制。 |
| **工作量** | S（< 1 天） |

---

### 发现结果 #4 — 高：安全头缺失

| 字段 | 描述 |
|-------|-------------|
| **类别** | 合规性（OWASP） |
| **严重性** | **高** |
| **标题** | HTTP 响应缺少安全头 |
| **位置** | `internal/handler/http/router.go` — 整个文件 |
| **描述** | HTTP 路由器（Gin 引擎）没有设置安全头：无 `X-Content-Type-Options: nosniff`、无 `X-Frame-Options`、无 `Content-Security-Policy`、无 `Strict-Transport-Security`、无 `Referrer-Policy`。虽然此 API 主要供非浏览器客户端（移动应用、SPA）使用，但缺少这些头会使任何基于浏览器的客户端面临 MIME 嗅探、点击劫持和协议降级攻击的风险。 |
| **攻击场景** | 如果 SPA 在同一个源上运行（例如，`app.example.com` 上的 SPA，`api.example.com` 上的 API 与使用 CORS），浏览器可能会将 API 的 JSON 响应作为脚本执行（MIME 嗅探），从而在 XSS 上下文中泄露敏感信息。 |
| **影响** | 增加了浏览器中 XSS 和点击劫持攻击的风险。 |
| **建议** | 添加一个使用 `gin.WrapH` 设置安全头的安全头中间件，或在 Gin 引擎上设置： |
| **工作量** | S（< 1 天） |

```go
// router.go 中的新中间件：
func SecurityHeaders() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-Frame-Options", "DENY")
        c.Header("Referrer-Policy", "no-referrer")
        if c.Request.TLS != nil {
            c.Header("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
        }
        c.Next()
    }
}
```

---

### 发现结果 #5 — 中：TOCTOU — 出站箱发布 + 死信之间存在竞态

| 字段 | 描述 |
|-------|-------------|
| **类别** | 威胁模型（拒绝服务 / 重放） |
| **严重性** | **中** |
| **标题** | 出站箱中继并发安全 |
| **位置** | `internal/outbox/relay.go` — 在 FetchUnpublished 和 MarkPublished 之间 |
| **描述** | 中继的轮询循环获取一批未发布的事件，然后一个接一个地处理它们。如果多个中继实例（或单个重新平衡）同时运行，它们可以获取相同的事件。`MarkPublished` 是幂等的，但处理程序（通知分发、webhook）可能运行多次，导致重复的通知或 webhook 投递。 |
| **攻击场景** | 1) 两个中继实例同时轮询。2) 两者都获取事件 ID 42。3) 两者都处理它。4) 接收端收到两次通知/webhook。 |
| **影响** | 重复通知、重复的 webhook 投递，可能导致状态损坏。 |
| **建议** | 实现 `FOR UPDATE SKIP LOCKED` 以进行中继获取（TASK-014）。在应用级添加去重。 |
| **工作量** | M（1-3 天） |

---

### 发现结果 #6 — 中：在未验证的 gRPC 调用中不正确地使用速率限制键

| 字段 | 描述 |
|-------|-------------|
| **类别** | 授权 / 拒绝服务 |
| **严重性** | **中** |
| **标题** | gRPC 速率限制键使用 `peer.Addr.String()`（包括端口），导致绕过 |
| **位置** | `internal/handler/grpc/server.go:310` — `rateLimitKey` |
| **描述** | 对于未经身份验证的 gRPC 调用，速率限制键是 `"ip:" + peer.Addr.String()`。`Addr.String()` 包括 `IP:port`。由于 gRPC 连接使用临时端口，每个连接都有不同的端口 → 每个连接都获得一个全新的速率限制桶。一个攻击者可以通过建立许多连接（默认情况下，无限制）来绕过速率限制。 |
| **攻击场景** | 1) 攻击者的 IP 是 `1.2.3.4`。2) 速率限制设置为 20 rps/burst 40。3) 攻击者打开 100 个 gRPC 连接（每个都有一个不同的临时端口，如 `1.2.3.4:50001`）。4) 每个连接每秒可以发送 20 个请求，总计 2000 rps，绕过了速率限制。 |
| **影响** | 公共 gRPC 端点上的速率限制可被轻易绕过。 |
| **建议** | 去除端口：仅使用 IP 地址： |
| **工作量** | S（< 1 天） |

```go
func rateLimitKey(ctx context.Context) string {
    if uid, ok := service.ActorFrom(ctx); ok {
        return "uid:" + uid
    }
    if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
        // 仅提取 IP，忽略端口
        host, _, _ := net.SplitHostPort(p.Addr.String())
        if host != "" {
            return "ip:" + host
        }
        return "ip:" + p.Addr.String()
    }
    return "ip:unknown"
}
```

---

### 发现结果 #7 — 中：webhook HMAC 签名密钥在配置和内存中为明文

| 字段 | 描述 |
|-------|-------------|
| **类别** | 加密 / 数据保护 |
| **严重性** | **中** |
| **标题** | 内存和磁盘中的 webhook 签名密钥 |
| **位置** | `internal/outbox/webhook.go:22` — `WebhookConfig.SigningKey`；`configs/config.yaml` |
| **描述** | `signing_key` 从 YAML 配置或环境变量加载，并作为明文字符串存储在 `config.Config` 和 `outbox.WebhookConfig` 中。如果攻击者获得对配置文件的读取权限或核心转储，他们可以伪造 webhook 事件。 |
| **影响** | webhook 端点的签名伪造。 |
| **建议** | 使用后可清除密钥（`byte` 切片 + `memclr.NoClobber`）。不过，Go 字符串是不可变的，这使得清除变得困难。文档应建议通过环境变量（`AERO_ID_OUTBOX_WEBHOOK_SIGNING_KEY`）提供密钥，而不是配置文件。 |
| **工作量** | S（< 1 天） |

---

### 发现结果 #8 — 中：无 NX 保护的幂等键冲突可能允许重放

| 字段 | 描述 |
|-------|-------------|
| **类别** | 威胁模型（重放） |
| **严重性** | **中** |
| **标题** | Redis 幂等性存储在 NX 失败后没有验证所有者 |
| **位置** | `internal/idempotency/store_redis.go:70-73` — `Reserve` 方法 |
| **描述** | 当 `SET NX` 因键已存在而失败时，`getExisting` 获取现有记录。但是，它不验证新请求是否具有相同的 `(uid, endpoint, key)`——它只检查键是否存在。如果攻击者可以猜测幂等键（理论上，它是由客户端提供的），他们可以重放已完成的响应而无需身份验证……等等，身份验证已经通过中间件进行了检查，因此 actor_uid 是可信的。但是，来自同一用户的相同 `(uid, endpoint, key)` 的重放将重新发送缓存的响应。如果响应包含敏感信息且幂等键的 TTL 很长（24 小时），则存在信息泄露的风险。 |
| **影响** | 较低——需要来自同一经过身份验证的用户的相同幂等键。 |
| **建议** | 对于敏感的端点（例如，在响应中暴露 PII），文档中应说明幂等响应被缓存了 24 小时。 |
| **工作量** | 信息性 |

---

### 发现结果 #9 — 中：当 SSO 关闭时，缓存提供陈旧的身份验证

| 字段 | 描述 |
|-------|-------------|
| **类别** | 身份验证 / 会话管理 |
| **严重性** | **中** |
| **标题** | SSO 令牌缓存的陈旧窗口允许在 SSO 中断期间已撤销的用户继续访问 |
| **位置** | `internal/sso/client.go:149-161` — `validateUpstream` 具有陈旧回退；`configs/config.yaml` — `stale_ttl: 300s` |
| **描述** | 当 SSO 不可用时，电路断路器打开，`validateUpstream` 回退到 `tokenCache.GetStale(token)`。如果 `stale_ttl` 为 300 秒，则令牌在原始 TTL 过期后最多 5 分钟内保持有效。在此窗口期间已撤销令牌的用户可以继续访问。 |
| **攻击场景** | 1) SSO 管理员撤销了用户 UID 的令牌。2) 同时，SSO 变得不可用。3) 用户继续使用其缓存的令牌 5 分钟（或在 `stale_ttl` 内）。4) 审计日志显示在撤销时间戳之后的访问。 |
| **影响** | 在 SSO 中断期间最多 5 分钟的已撤销用户访问权限。 |
| **建议** | 这对于高可用性设置来说是一个已知的、文档化的权衡。但是，`stale_ttl` 应该针对安全性与可用性进行调整：对于高风险系统，将其设置为 0（禁用陈旧回退）。记录陈旧的访问以进行取证。 |
| **工作量** | S（< 1 天） |

---

### 发现结果 #10 — 低：审计日志不完整

| 字段 | 描述 |
|-------|-------------|
| **类别** | 数据保护 / 合规性 |
| **严重性** | **低** |
| **标题** | 某些敏感操作未审计 |
| **位置** | 各服务文件——`internal/service/message.go`、`internal/service/comment.go`、`internal/service/notification.go` |
| **描述** | 发送消息、创建评论、喜欢/取消喜欢、通知操作不会被审计。虽然某些操作（文件 CRUD、阻止、管理）会进行审计，但消息和评论操作是完全未审计的。在数据泄露事件中，没有审计线索可用于追溯谁与谁进行了通信或哪些内容被点赞。 |
| **影响** | 无法对消息/评论滥用进行取证审查。 |
| **建议** | 为消息发送、评论创建、评论点赞/取消点赞添加审计日志记录。 |
| **工作量** | M（1-3 天） |

---

### 发现结果 #11 — 低：无 CSRF 保护

| 字段 | 描述 |
|-------|-------------|
| **类别** | 身份验证 |
| **严重性** | **低** |
| **标题** | HTTP API 没有 CSRF 保护 |
| **位置** | `internal/handler/http/router.go` |
| **描述** | 该服务使用不记名令牌身份验证（而不是基于 cookie 的会话），这自然可以防止标准的 CSRF 攻击。但是，如果客户端将不记名令牌存储在 `localStorage` 中并通过 `Authorization` 头发送，则没有额外的 CSRF 保护措施。依赖 cookie 的基于浏览器的客户端（例如，来自反向代理设置的 cookie）将面临风险。 |
| **影响** | 非常低——不记名令牌模式本质上可以防止 CSRF。 |
| **建议** | 记录下此 API 使用 `Authorization: Bearer` 标头，而不是 cookie，因此不受 CSRF 影响。如果将来添加了 cookie 身份验证，则必须重新审视这一点。 |
| **工作量** | 信息性 |

---

### 发现结果 #12 — 低：元数据字段没有大小限制

| 字段 | 描述 |
|-------|-------------|
| **类别** | 输入验证 / 拒绝服务 |
| **严重性** | **低** |
| **标题** | 文件 `meta` JSON 映射是无界的 |
| **位置** | `internal/service/file_service.go:52` — `Meta map[string]any`；数据库：`column:meta` 为 JSONB |
| **描述** | 文件元数据映射没有大小或键限制。一个用户可以上传一个文件，并使用一个包含数百个键或深度嵌套 JSON 的 `meta` 负载，从而消耗数据库存储和内存。 |
| **攻击场景** | 攻击者调用 `PATCH /files/:id` 并设置 `meta` 为一个包含 10,000 个键的映射，每个键值对 1KB ⇒ 负载为 10MB。该映射存储在 `datatypes.JSONMap` 中，并插入到 JSONB 列中。多次请求会累积大量数据，消耗磁盘和内存。 |
| **影响** | 数据库膨胀、内存压力、潜在的成本影响。 |
| **建议** | 限制 `meta` 映射：最大键数（例如，20）、最大序列化大小（例如，10KB）。在服务层添加验证。 |
| **工作量** | S（< 1 天） |

```go
// 在 file_service.go 中，Create/UpdateMetadata：
if len(in.Meta) > maxMetaKeys {
    return nil, apperr.BadRequest("file.meta_too_large", "meta exceeds maximum number of keys")
}
size := 0
for k, v := range in.Meta {
    size += len(k) + len(fmt.Sprintf("%v", v))
}
if size > maxMetaBytes {
    return nil, apperr.BadRequest("file.meta_too_large", "meta exceeds maximum size")
}
```

---

### 发现结果 #13 — 低：访问日志在路径之前记录 URI，可能泄露查询参数

| 字段 | 描述 |
|-------|-------------|
| **类别** | 数据保护 |
| **严重性** | **低** |
| **标题** | 访问日志记录原始 URI（包括查询参数） |
| **位置** | `internal/middleware/access_log.go:27` — `zap.String("uri", c.Request.URL.Path)` |
| **描述** | 实际上，代码记录的是 `c.Request.URL.Path`（没有查询参数），因此这是安全的。但是，对于 `/files/dl` 端点，路径是常量，因此可以通过阅读日志推断出文件访问模式。 |
| **影响** | 轻微——仅记录路径，如果路径包含标识符（通常不包含），可能会泄露。 |
| **建议** | 当前行为是可以接受的。对于未来的端点，确保敏感数据通过主体传递，而不是路径参数。 |
| **工作量** | 信息性 |

---

### 发现结果 #14 — 低：S3 后端上没有服务器端加密

| 字段 | 描述 |
|-------|-------------|
| **类别** | 数据保护 / 加密 |
| **严重性** | **低** |
| **标题** | S3 PutObject 没有指定 SSE 选项 |
| **位置** | `internal/storage/s3.go:79-87` — `Put` 方法 |
| **描述** | 当上传对象到 S3 时，`Put` 方法不设置 `ServerSideEncryption` 或 `SSEKMSKeyId`。如果目标 S3 存储桶默认未启用 SSE，则对象将以明文形式存储。 |
| **影响** | 存储在 S3 中的文件可能未加密。 |
| **建议** | 添加 SSE-S3 或 SSE-KMS 作为配置选项： |
| **工作量** | S（< 1 天） |

```go
// s3.go 中的 PutObjectInput：
_, err := uploader.Upload(ctx, &s3.PutObjectInput{
    Bucket:               aws.String(s.cfg.Bucket),
    Key:                  aws.String(key),
    Body:                 cr,
    ServerSideEncryption: types.ServerSideEncryptionAES256,  // 或使用配置的 KMS 密钥
})
```

---

## STRIDE 分析

| 威胁 | 评估 |
|-------|---------|
| **S**poofing（欺骗） | **中等风险。** SSO 令牌由外部 SSO 验证 + 使用 `singleflight` 缓存。使用 SPKI 证书固定来防止中间人攻击，以建立上游连接。TTL 限时缓存 + 在 SSO 中断期间使用陈旧回退。**没有本地的用户/身份验证存储**——完全委托给 SSO。 |
| **T**ampering（篡改） | **高风险。** 内容去重中的 TOCTOU 竞态（发现 #2）可能导致存储损坏。预签名 URL 使用 HMAC-SHA256 进行签名验证，但路径遍历（发现 #1）允许使用有效签名读取任意文件。GORM 使用参数化查询，因此 SQL 注入的风险较低。 |
| **R**epudiation（抵赖） | **中等风险。** 操作（文件、阻止、管理）会进行审计，但是**消息发送**、**评论创建/点赞**不会被审计（发现 #10）。如果发生滥用，没有用于追溯的审计线索。 |
| **I**nformation Disclosure（信息泄露） | **中等风险。** 预签名 URL 在 24 小时内仍然有效。没有设置安全头（发现 #4）。SSE 端点记录事件 ID，这些 ID 包含通知负载。没有对本地后端路径遍历进行清理（发现 #1）。 |
| **D**enial of Service（拒绝服务） | **中等风险。** gRPC 速率限制键包含端口，允许通过许多连接进行绕过（发现 #6）。元数据字段没有边界（发现 #12）。保存许多 SSE 连接会消耗 goroutine/内存。公共下载端点无身份验证/速率限制（发现 #3）。 |
| **E**levation of Privilege（权限提升） | **低风险。** 基于 UUIDv7 的访问控制，由 `mustActor` 严格按所有者执行。RBAC 通过 `PermissionMiddleware` 强制执行。文件共享使用显式的逐用户授权。 |

---

## 总体安全状态

### 需要改进

该代码库展示了**安全工程方面的扎实基础**：主要的身份验证是通过外部 SSO 处理的，数据访问控制由服务层严格执行，以及使用参数化查询防止了 SQL 注入。幂等性中间件、出站箱模式以及审计日志记录表明了对弹性和可问责性的深思熟虑。

然而，有**两个关键问题**需要立即处理：
1. 路径遍历漏洞（#1）允许在本地后端上进行任意文件读取——这是一个 P0 安全问题。
2. 内容去重竞态（#2）可能导致静默数据损坏。

**安全性亮点：**
- SSO 客户端具有证书固定 + 电路断路 + 带有陈旧回退的 TTL 限时缓存
- 预签名 URL 使用恒定时间 HMAC 比较（`hmac.Equal`）
- 没有跨 repo 的行，没有原始的 SQL 字符串拼接
- 服务层严格执行访问控制（从不信任来自 handler 的 actor_uid）
- 对所有数据进行软删除
- 出站箱实现可靠的事件投递

**主要差距：**
- 围绕内容去重的并发安全
- 路径遍历清理
- HTTP 安全头
- gRPC 速率限制键粒度
- 消息/评论操作的审计覆盖范围
- 文件元数据输入验证

### 最重要的 3 个关键问题

1. **路径遍历 ⇒ 任意文件读取**（严重）— 在 `Local.Get` 中清理 key，或使用键格式验证。
2. **内容去重 TOCTOU 竞态**（严重）— 对 `files(owner_uid, checksum)` 使用数据库级唯一约束，或使用原子 upsert。
3. **gRPC 速率限制绕过**（中等）— 从速率限制键中去除端口。

### 最重要的 3 个速胜点

1. **安全头中间件** — 添加 5 行 Gin 中间件来设置 `X-Content-Type-Options`、`X-Frame-Options`、`HSTS`。（< 1 小时）
2. **gRPC 速率限制键修复** — 在 `rateLimitKey` 中从 `peer.Addr.String()` 中去除端口。（15 分钟）
3. **元数据大小限制** — 在 `file_service.go` 中为 `Meta map[string]any` 添加简单的大小/计数检查。（30 分钟）

### 安全债务

| 项目 | 工作量 | 备注 |
|------|--------|-------|
| 本地路径遍历清理 | S | P0，修复路径连接 |
| 内容去重竞态 | M | 需要数据库级唯一约束或锁 |
| gRPC 速率限制键粒度 | S | 从键中去除端口 |
| HTTP 安全头 | S | 添加 Gin 中间件 |
| S3 SSE 加密 | S | 在 PutObject 中添加选项 |
| 消息/评论审计日志 | M | 为发送/创建/点赞添加日志记录 |
| Webhook 签名密钥处理 | S | 文档，考虑在使用后清除 |
| 陈旧令牌窗口记录 | S | 添加审计日志记录以显示陈旧的访问 |
| 公共端点上的速率限制 | S | 确保 `/files/dl` 包含在限制中 |
| 元数据输入边界 | S | 添加大小/键计数限制 |

---

**结论：** 该服务可以安全投入生产，但必须**立即**缓解路径遍历（#1）和内容去重竞态（#2）。剩下的问题在时间/风险权衡方面是可以接受的，但应计划在后续的冲刺中解决。代码的整体设计是可靠的，通过使用服务层抽象统一了授权边界，并且没有跨租户数据泄露的捷径。
