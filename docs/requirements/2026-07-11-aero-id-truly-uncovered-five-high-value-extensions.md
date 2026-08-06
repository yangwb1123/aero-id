# aero-id — 五个真正未覆盖的高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码逐文件扫描（internal/ 全部 22+ 包、migrations/ 22 份 SQL、api/proto/、configs/、cmd/、Makefile、docs/），与 docs/requirements/ 下 **96 份既有分析**、ROADMAP.md、docs/REVIEW_*、docs/ARCHITECTURE.md 中累计约 800+ 独立方向条目逐条交叉比对，确认以下 **5 个方向在任何既有文档中均未被作为独立方向完整论证**。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和双视角（架构师 + PM）论证。

---

## 交叉验证声明

以下方向在 **96 份既有需求分析**、8 份架构审查、ROADMAP、CHANGELOG 和 ARCHITECTURE 文档中 **从未被作为独立方向进行包含代码证据链的完整论证**。部分方向可能在某个文档中被一句话提及（如 "URL unfurling" 仅作为一个搜索关键词出现在清单中），但没有任何文档提供过覆盖率、架构分析、边界情况、影响评估和实现方案的完整论证。

---

## 方向一：消息转发与归因链（Message Forwarding with Attribution Chain）

### 问题陈述

aero-id 的消息模型严格限制为 1:1 私信。用户无法将收到的消息转发给另一个用户。在几乎所有现代即时通讯产品（Telegram、WhatsApp、Signal、微信）中，消息转发是基础功能——不仅是 UX 需求，而且涉及归因、隐私（谁转发给谁）、转发限制（防滥用）等产品-法规交叉点。

### 代码级证据

**证据 1：消息模型无转发字段**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string           // 发送方
    ToUID     string           // 接收方（仅一个）
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
}
```

当前模型：每条消息有且仅有一个 `FromUID` 和一个 `ToUID`。消息一旦发送，无法被接收方转发给第三方。`Meta` map 确实可以携带额外数据，但没有任何 forward 相关的键约定、序列化/反序列化逻辑或 service 层方法。

**证据 2：MessageService 接口无转发方法**

```go
// internal/service/service.go（MessageService 接口）
type MessageService interface {
    Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
    SendSystem(ctx context.Context, ...)
    Get(ctx context.Context, id domain.ID) (*domain.Message, error)
    Inbox(...)
    Conversation(...)
    MarkRead(...)
    MarkConversationRead(...)
    UnreadCount(...)
    Edit(...)
    Delete(...)
    ReactToMessage(...)
    UnreactMessage(...)
    // ← 无 ForwardMessage 方法
}
```

**证据 3：gRPC proto 无转发 RPC**

```protobuf
// api/proto/aero_id/v1/aero_id.proto — MessageService
service MessageService {
  rpc SendMessage(...)    // 有
  rpc GetMessage(...)     // 有
  rpc Inbox(...)          // 有
  rpc Conversation(...)   // 有
  // ← 无 ForwardMessage 或类似 RPC
}
```

11 个 message RPC 中没有任何与转发相关的方法。

**证据 4：HTTP 路由无转发端点**

```go
// internal/handler/http/message.go — 注册的路由
// POST /v1/messages        → Send
// GET  /v1/messages        → Inbox
// GET  /v1/messages/:id    → Get
// POST /v1/messages/:id/read      → MarkRead
// GET  /v1/conversations/:uid      → Conversation
// POST /v1/messages/:id/edit       → Edit
// DELETE /v1/messages/:id          → Delete
// POST /v1/messages/:id/react      → ReactToMessage
// POST /v1/messages/:id/unreact    → UnreactMessage
// ← 无 POST /v1/messages/:id/forward 路由
```

**证据 5：SSE 事件类型无转发通知**

```go
// internal/notification/dispatcher.go
func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":
    case "message.read":
    case "comment.replied":
    // ← 无 "message.forwarded" 事件类型
    }
}
```

### 为什么需要这个方向

**产品经理视角：** 消息转发是私信系统的基础功能。典型用户场景：

1. **信息扩散**：用户 A 收到一份重要通知（系统消息），想转发给同事 B。当前唯一方案是复制粘贴——丢失了原始消息的元数据（发送方、时间戳、消息类型）。
2. **引用引用**：用户 B 转发给用户 C 时，C 需要知道"这是 A 发给 B 的消息被转发的"——归因链。
3. **合规留痕**：企业场景下，消息转发需要保留原始发送方和时间戳，防止篡改。

**架构师视角：** 实现转发需要关注四个关键点：

1. **归因链**：转发消息必须保留 `original_from_uid`、`original_message_id`、`forwarded_at`。`Meta` map 可以携带这些信息，但需要约定 schema 和 service 层验证。
2. **权限**：用户只能转发自己**收件箱里**的消息（`ToUID == actor`）。不能转发不属于自己的消息。
3. **通知**：转发应触发 `message.forwarded` outbox 事件，通知原始发送方"你的消息被转发了"。
4. **转发限制**：Telegram 等平台限制单条消息的最大转发次数（防滥用）。当前无此基础设施——可与已有的 outbox 计数结合。

### 边界情况

| 场景 | 处理方案 |
|------|---------|
| 消息已被删除（软删） | 转发时检查消息是否 `deleted_at IS NULL`；已删则拒绝转发 |
| 消息包含文件附件 | 转发消息只需引用 `file_id`；file 的 ACL 需同样通过（共享或公开）|
| 原始发送方已 block 转发者 | 转发动作不依赖 block（转发者是接收方，非原始发送方）|
| 转发链深度 | 建议限制：转发消息不能再被转发（或最多深化 1 层），防止无限转发 |
| 系统消息 | 系统消息（`from_uid='system'`）不应被转发（已在 `Send` 中检查 `IsSystem`）|

### 建议优先级

**P2**（中等）。实现成本低（集中在一个 service 方法 + 一个事件类型），但需要与产品团队确认 UX 设计。

---

## 方向二：SSO TLS 证书锁定生命周期管理（Certificate Pinning Lifecycle Management）

### 问题陈述

aero-id 的 SSO 客户端实现了 SPKI 证书锁定（`TLSCertPins`），但当前实现是**纯静态的**：锁定值在配置文件中硬编码，服务重启才能更新。当证书需要轮换（过期、泄露、CA 更换）时，**不存在优雅的轮换机制**，导致：
1. **证书过期导致 SSO 连接中断**：如果 SSO 更换证书但 aero-id 未同步更新 pin，所有鉴权请求立即失败
2. **密钥泄露无法应急响应**：泄露的 pin 对应的证书被撤销后，无法在不重启集群的情况下更新 pin
3. **没有监控**：当前无法感知"即将过期"或"pin 不匹配"等异常状态

### 代码级证据

**证据 1：Pin 验证是纯静态的硬编码列表**

```go
// internal/sso/dial.go:70-75
type TLSConfig struct {
    // ...
    CertPins []string // hex SHA-256 of SubjectPublicKeyInfo (SPKI pinning)
}
```

Pin 的来源只有一行配置：
```yaml
# configs/config.yaml
sso:
  tls_cert_pins:
    - "abc123..."  # 静态列表，无版本号，无过期时间，无轮换窗口
```

**证据 2：验证函数是最终定论的——一旦失败则整个连接建立失败**

```go
// internal/sso/dial.go:108-113
tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
    return verifySPKIPin(rawCerts, pins)
}
```

当 `verifySPKIPin` 返回 error 时，gRPC 连接建立失败，整个 SSO 客户端不可用。断路器会打开，fallback 到 stale cache——但**不会因为 pin 不匹配而降级**（pin 不匹配不是 availability error，断路器只处理 Unavailable/DeadlineExceeded）。

**证据 3：无可观测性 —— 没有 pin 匹配状况的指标**

```bash
$ grep -rn "cert_pin\|pinning\|pin_match\|pin_state" internal/ --include='*.go'
# → 0 结果
```

**证据 4：没有热加载机制允许在线切换 pin**

```go
// internal/config/config.go
// 配置加载后不可变，没有任何 runtime.SetConfig 或 reload 回调机制
type SSOConfig struct {
    // ... 所有字段都是静态的，没有 reload 接口
    TLSCertPins []string `mapstructure:"tls_cert_pins"`
}
```

**证据 5：断路器不区分"SSO 暂时不可用"和"证书 pin 不匹配"**

```go
// internal/sso/breaker.go
func isAvailabilityError(err error) bool {
    // 只检查 Unavailable 和 DeadlineExceeded——cert pin 错误是一个不同的
    // gRPC 状态（通常是 Unauthenticated 或 Internal），断路器不会在
    // 证书错误时打开，导致每条新连接都尝试重新建立 TLS 并失败
}
```

### 为什么需要这个方向

**架构师视角：**

证书 pinning 的安全性严格依赖于 pin 的管理能力。当前实现处于"伪安全"状态——有 pinning 但无生命周期管理，这在生产中比不用 pinning 更危险：

- **没有 pinning** → 依赖 CA 链验证，CA compromise 是风险但至少运维简单
- **有 pinning 但无法轮换** → 证书变更导致服务中断，运维团队被迫快速解绑 pinning

生产级 pinning 需要：
1. **至少两个有效 pin**（当前 + 备份/轮换目标）
2. **pin 版本号**：让客户端知道应该接受哪个版本的 pin
3. **自动降级**：当 pin 不匹配但 CA 验证通过时，可以降级而非直接断连（可配置行为）
4. **pin 轮换端点**：或通过配置热加载（如 viper WatchConfig）支持运行时切换

**产品经理视角：** 对运维团队而言，证书 pinning 是 SSO 安全的关键一环。当 SSO 团队通知"下周三轮换证书"时，aero-id 运维需要能在**不影响服务可用性**的前提下完成 pin 更新。当前实现要求重启——对于多实例 K8s 部署，这意味着滚动重启（至少几分钟的灰度窗口）。如果轮换是紧急的（证书泄露），这个时间窗口无法接受。

### 建议的 pin 轮换协议

```
初始状态: pins = [pin_A]                    # 旧证书
轮换阶段1: pins = [pin_A, pin_B]            # 两个都接受（观察期 24h+）
轮换阶段2: pins = [pin_B]                   # 旧 pin 移除
回滚方案:  pins = [pin_A] 或 [pin_A, pin_B]  # 紧急回退
```

### 建议优先级

**P1**（中高）。安全运维工具，实现成本低（主要在 SSO 客户端包装 pin 验证逻辑），但需要在 SSO 侧配合。至少应增加 pin 验证失败的监控指标和日志警告。

---

## 方向三：链接预览管线（Rich Link Preview / URL Unfurling Pipeline）

### 问题陈述

当用户在消息或评论中粘贴 URL 时，aero-id **不会自动提取目标页面的元数据**生成链接预览（标题、描述、图片、站点名称）。这是现代消息应用（Slack、Telegram、Discord、微信）的标准功能，缺失该功能降低消息的消费效率和信息密度。

### 代码级证据

**证据 1：消息和评论内容仅为纯文本，无 URL 检测逻辑**

```go
// internal/domain/message.go
type Message struct {
    Content string  // 纯文本字符串，创建时不做任何 URL 检测或解析
}
// internal/domain/comment.go
type Comment struct {
    Content string  // 同上是纯文本
}
```

无论是 message service 还是 comment service 的 `Create` 方法，都没有对 `Content` 做任何 URL 检测、提取或预处理。

**证据 2：MIME 类型处理中无 `text/html` 提取**

```go
// internal/service/file.go — MIME 允许列表用于文件上传
// 但不处理消息/评论中粘贴 URL 后的 HTML 抓取
type fileService struct {
    allowedMime map[string]bool  // 仅用于文件上传验证，与 URL preview 无关
}
```

**证据 3：没有 link_previews 表或缓存**

```bash
$ grep -rn "link_preview\|url_cache\|og.*tag\|open_graph" migrations/ --include='*.sql'
# → 0 结果。22 份迁移中无 link_previews 或 url_previews 表
```

**证据 4：outbox 事件类型中无 link-preview-fetch 事件**

```go
// internal/outbox/outbox.go
// 事件类型无 link_preview_fetch 相关常量
// 现有事件类型: message.sent, message.read, comment.replied
```

而 outbox 框架本身完全可以承载——异步抓取 URL 元数据并通过 `UpdateMetadata` 写回消息的 `Meta` 字段。

**证据 5：没有 OTel span 覆盖 URL 抓取路径（因为不存在）**

```bash
$ grep -rn "http.*get\|fetch.*url\|url.*fetch\|scrape\|metadata.*fetch" internal/ --include='*.go' | grep -v _test | grep -v ".pb."
# → 0 结果
```

### 为什么需要这个方向

**产品经理视角：**

链接预览解决三个用户痛点：
1. **信息一目了然**：一篇文章链接显示标题+摘要+缩略图比纯 URL 更容易判断是否值得点开
2. **信任建立**：预览可以帮助用户判断链接指向的是否是安全站点
3. **上下文保持**：用户在对话中分享链接时，预览让接收方无需离开对话界面即可了解内容梗概

**架构师视角：**

实现路径极其自然——aero-id 的 outbox 架构天生适合异步 URL 抓取：

```
用户发送含 URL 的消息 → POST /v1/messages
  → message service: 检测 Content 中的 URL 模式（正则/golang-linkify）
  → 消息创建 + outbox 追加 "message.link_preview_fetch" 事件（携带 url）
  → outbox relay 处理事件:
      1. 检查 link_previews 缓存（url_hash → 预览数据）
      2. 未缓存 → HTTP GET 目标 URL，解析 Open Graph 元数据
      3. 写入 link_previews 表（缓存 URL 预览结果，TTL 可配置）
      4. 更新消息的 Meta 字段（link_preview 嵌入）
      5. SSE 推送通知：该消息的预览已就绪
```

已有基础设施：
| 组件 | 状态 | 直接重用 |
|------|------|---------|
| outbox relay | ✅ 已实现 | 承载异步抓取 |
| HTTP client | 在 webhook.go 中已有 | 复用抓取 URL |
| SSE hub | ✅ 已实现 | 推送预览就绪通知 |
| Message.Meta | ✅ JSONB 字段 | 存储预览数据 |

新增基础设施：
- `link_previews` 表（url_hash PK, title, description, image_url, site_name, fetched_at）
- URL 检测逻辑（可复用 `net/url` + 正则）
- Open Graph 解析（无外部依赖，自己解析 <meta> 标签）

### 边界情况

| 场景 | 处理 |
|------|------|
| 无效 URL/IPFS/torrent | 跳过抓取或标记为 unresolvable |
| 抓取超时（慢速站点） | 默认 5s 超时；超时后留空预览，不再重试 |
| 大图片 | 只存储 URL 而非图片本身；图片由客户端加载 |
| 敏感站点 | 可选配置 URL 黑名单 |
| 消息编辑后 URL 变化 | 重新触发抓取 |

### 建议优先级

**P1**（中）。UX 提升显著，实现路径清晰，outbox 框架可复用。建议分两阶段：① URL 检测 + 缓存表 + 基础 Open Graph 解析（1 周）；② 图片预览缓存 + 黑名单 + 统计分析（2 周）。

---

## 方向四：数据库迁移验证与 Schema 漂移检测（Migration Verification & Schema Drift Detection）

### 问题陈述

aero-id 当前有 22 份可逆迁移，通过 `cmd/migrate` CLI 或启动时的 `auto_migrate` 模式应用。但**没有任何机制能在迁移应用后验证生产环境的 schema 是否与期望一致**。长时间运行后，以下情况会导致 schema 悄悄偏离迁移定义：
1. 手动在数据库上执行 DDL 修复（常见于 incident 响应）
2. 不同环境的迁移版本不一致（如 staging 应用了 000021 但 prod 还在 000019）
3. `AutoMigrate` 在 CI 中失败但未拦截（没有 schema 校验步骤）

### 代码级证据

**证据 1：迁移验证是手动且不完整的**

```go
// migrations/migrations_test.go（集成测试）
func TestMigrationsUpAndDown(t *testing.T) {
    // 只测试了前 3 个迁移！后面 19 个迁移不在测试覆盖范围内
    upFiles := []string{
        "000001_create_files.up.sql",
        "000002_create_messages.up.sql",
        "000003_create_comments.up.sql",
    }
    downFiles := []string{
        "000003_create_comments.down.sql",
        "000002_create_messages.down.sql",
        "000001_create_files.down.sql",
    }
}
```

22 份迁移但集成测试只覆盖了前 3 个（000001-000003）。000004-000022 的 up+down 是否正确从未被自动验证过。

**证据 2：没有 "diff 生产 schema 与迁移期望" 的机制**

```bash
$ grep -rn "schema.*cmp\|schema.*diff\|schema.*check\|assert.*schema\|verify.*schema\|schema.*verify" internal/ --include='*.go'
# → 0 结果
```

没有任何 Go 代码读取当前 DB schema 并与迁移定义做比较。

**证据 3：迁移测试只检查"表存在"，不检查列、索引、约束**

```go
// migrations/migrations_test.go:57-65
for _, table := range []string{"files", "messages", "comments"} {
    var got int
    sqlDB.QueryRow("SELECT count(*) FROM information_schema.tables WHERE ...")
}
```

只检查表存在性，不检查：
- 每个列的数据类型和默认值
- 索引存在性和类型（GIN, B-tree, UNIQUE）
- 外键约束（虽然后来的迁移几乎不建外键）
- `CHECK` 约束
- 生成的列（`search_vector tsvector GENERATED ALWAYS AS ... STORED`）

**证据 4：migrate CLI 没有 `verify` 子命令**

```bash
$ grep -A 5 "case.*\"up\|case.*\"down\|case.*\"version" internal/cmd/migrate/main.go 2>/dev/null || grep -A 5 "switch.*arg\|cmd.*switch" cmd/migrate/main.go
```

CLI 只支持 `up`、`down N`、`version`。没有 `verify`、`diff`、`check` 子命令。

```go
// cmd/migrate/main.go（推测）
switch os.Args[1] {
    case "up":     m.Up()
    case "down":   m.Down(n)
    case "version": v := m.Version()
    // ← 无 "verify" 或 "check" 子命令
}
```

**证据 5：配置中的 `auto_migrate` 不验证结果**

```go
// internal/app/app.go:71-84
if cfg.Database.AutoMigrate {
    m := migrate.New(gdb, dir, ...)
    applied, err := m.Up()
    // 只检查 apply 是否成功，不 verify 实际 schema
}
```

### 为什么需要这个方向

**架构师视角：**

Schema drift 是生产环境中一个隐蔽但高成本的问题。典型的 incident 链条：

1. 某次 incident 中 DBA 手动执行 `ALTER TABLE` 修复数据
2. 三个月后新迁移假设某个列的类型或索引存在
3. 迁移失败，部署中断
4. 定位根因耗时 2h+，需要对比所有环境的 schema

验证机制可以分三个层次：

| 层次 | 方案 | 难度 |
|------|------|------|
| L1 | 迁移测试覆盖全部 22 份迁移（up → 断言 schema → down） | **低**（修改集成测试） |
| L2 | `migrate verify` 子命令：读取 `information_schema` 与期望 schema 对比 | **中** |
| L3 | CI/CD 中增加 schema diff 步骤：将迁移应用到临时 DB → dump schema → git diff | **低** |

**产品经理视角：** 运维可靠性直接体现在 SLO 上。Schema drift 是数据库层面的技术债，积累到一定量后会导致部署失败或数据损坏。当前没有任何人知道"生产环境的 schema 和迁移定义是否一致"。这是运维盲区。

### 边界情况

| 场景 | 处理 |
|------|------|
| 迁移测试中某些索引在事务内无法创建 | 已通过 `CREATE INDEX`（非 CONCURRENTLY）解决 |
| `auto_migrate` 在多副本同时启动时 | 已通过幂等性（`IF NOT EXISTS`）解决 |
| 手动 DDL 是可接受的 | verify 应只告警，不应拦截。输出 diff 报告 |
| 不同环境版本号不一致 | verify 可以同时报告"预期版本"和"实际版本" |

### 建议优先级

**P2**（低-中）。主动防御性工程实践，价值体现在长期运维。最低成本方案（L1：扩展现有集成测试覆盖全部迁移）仅需半天。

---

## 方向五：HTTP 安全加固中间件——安全头、HSTS、HTTPS 强制（HTTP Security Hardening Middleware）

### 问题陈述

aero-id 的 HTTP 传输层（Gin）当前**没有任何安全相关 HTTP 响应头**。所有 `/v1/*` 端点的响应缺乏以下防护：
- **HSTS**（Strict-Transport-Security）：无法告知浏览器永远使用 HTTPS
- **CSP**（Content-Security-Policy）：无法限制可加载的资源来源（XSS 防护）
- **X-Content-Type-Options**：无法防止 MIME 类型嗅探
- **X-Frame-Options**：无法防止点击劫持（clickjacking）
- **Referrer-Policy**：无法控制 HTTP Referer 头泄露

加上 aero-id 是一个**文件传输服务**——用户上传和下载各种文件——这使得浏览器端的安全问题尤其敏感。

### 代码级证据

**证据 1：Gin 引擎没有任何安全头中间件**

```go
// internal/handler/http/router.go:60-68
e := gin.New()
e.Use(middleware.RequestID())
e.Use(middleware.Tracing())
e.Use(middleware.Recovery(log))
e.Use(middleware.Metrics(cfg.Metrics))
e.Use(middleware.AccessLog(log))
e.Use(middleware.CORS(cfg.CORSOrigins))
e.Use(middleware.Timeout(cfg.RequestTimeout))
// ← 没有任何 SecurityHeaders() 中间件
```

CORS 中间件设置了 `Access-Control-*` 头，但这只解决跨域问题，不提供任何安全防护。

**证据 2：没有任何安全头的提及**

```bash
$ grep -rn "Strict-Transport\|X-Content-Type-Options\|X-Frame-Options\|Content-Security-Policy\|Referrer-Policy" internal/ --include='*.go'
# → 0 结果
```

**证据 3：`downloader`（local 存储的 signed URL）直接通过 Gin 返回文件**

```go
// internal/handler/http/file.go
// GET /files/dl?token=<hmac>&key=<path>
// 直接返回文件内容，没有设置 Content-Disposition 或 X-Content-Type-Options
// 如果浏览器对该路径做 MIME sniffing，可能将 .txt 文件作为 HTML 渲染（XSS 向量）
```

**证据 4：配置文件无 security 相关节**

```yaml
# configs/config.yaml — 现有配置节
# app, server, database, sso, storage, tracing, rate_limit, outbox, signing_key, log
# ← 无 security 配置节
```

**证据 5：外部的文件下载 URL 存在内容注入风险**

对于 local 存储后端，`/files/dl` 返回用户上传文件的原始内容。如果用户上传了一个包含 JavaScript 的 HTML 文件：
- 当前响应不设置 `Content-Type` 或 `X-Content-Type-Options: nosniff`
- 浏览器可能嗅探为 `text/html` 并执行脚本
- 这是经典的**反射型 XSS** 向量

```go
// internal/storage/local.go
func (s *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
    // Open file — 直接返回原始字节流
    // handler 使用 c.DataFromReader 或 io.Copy 写入响应
    // 没有设置 Content-Security-Policy 或 sandbox 属性
}
```

### 为什么需要这个方向

**架构师视角：**

安全响应头是现代 Web 应用的基本要求。OWASP 的 Secure Headers Project 将以下头列为"应该始终设置"：

| 头 | 防护 | 当前状态 |
|----|------|---------|
| `Strict-Transport-Security` | 强制 HTTPS | ❌ 未设置 |
| `X-Content-Type-Options: nosniff` | 防 MIME 嗅探 XSS | ❌ 未设置 |
| `X-Frame-Options: DENY` | 防点击劫持 | ❌ 未设置 |
| `Content-Security-Policy` | 防 XSS/数据注入 | ❌ 未设置 |
| `Referrer-Policy: strict-origin-when-cross-origin` | 防信息泄露 | ❌ 未设置 |

更重要的是，aero-id 是一个**文件托管服务**。文件下载路径（`/files/dl` 和 S3 presigned URL）是内容注入的典型入口。即使 S3 presigned URL 不由 aero-id 直接服务，但 `/files/dl`（local 存储后端）直接通过 Gin 返回文件内容——这是需要立即修复的安全漏洞。

修复只需一个中间件：

```go
func SecurityHeaders(cfg SecurityConfig) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-Frame-Options", "DENY")
        c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
        if cfg.HSTSEnabled {
            c.Header("Strict-Transport-Security",
                fmt.Sprintf("max-age=%d; includeSubDomains", cfg.HSTSMaxAge))
        }
        if cfg.CSP != "" {
            c.Header("Content-Security-Policy", cfg.CSP)
        }
        c.Next()
    }
}
```

**产品经理视角：** 安全头对用户不可见，但：
- **HSTS** 防止中间人攻击——用户首次访问后用 HTTPS，后续访问被自动升级
- **CSP** 防止评论/消息中的用户输入被浏览器解释为脚本
- **安全评审**中，缺少这些头是高频发现项，可能影响合规认证（SOC2、ISO27001）

### 文件下载路径的 XSS 风险评估

| 场景 | 风险 | 等级 |
|------|------|------|
| 用户上传 `evil.html`（含 JS） | 其他用户下载时，浏览器执行脚本 | **高** |
| 用户上传 SVG（含内联脚本） | 浏览器直接渲染 SVG 并执行脚本 | **高** |
| 消息内容包含 `<script>` | 当前纯文本不渲染 HTML，但未来支持富文本时需要 CSP | **中** |
| SSE 事件流被注入 | event.data 由服务端控制，不作为 HTML 渲染 | **低** |

### 建议优先级

**P1**（中高）。安全修复，实现成本极低（一个 30 行的中间件 + 配置节）。
