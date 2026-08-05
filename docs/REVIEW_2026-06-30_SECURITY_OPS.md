# aero-id — 安全、运维与 API 设计审查

> 日期: 2026-06-30
> 范围: 第四轮扫描，聚焦前三次未覆盖的新角度

---

## 0. 代码库量化快照

| 指标 | 值 |
|------|-----|
| Go 源文件（排除 generated `.pb.go`） | ~330 |
| 测试文件 | ~100+ |
| 迁移文件（up+down） | 34（17 对） |
| 后台 goroutine | 5（relay, GC, rate-limit cleaner, sso token cache cleaner, + unstarted idempotency cleaner） |
| gRPC RPCs | 41 |
| HTTP 路由 | 20+ |
| 外部依赖（go.mod direct） | 14 个 |

---

## 1. 安全审查

### 1.1 无 CSRF 防护

**现象：** 所有 POST/PUT/DELETE 端点只依赖 Bearer token 认证。没有 CSRF token、没有 SameSite Cookie 检查、没有 Origin/Referer 校验。

**风险场景：** 如果客户端是一个浏览器 Web 应用（而非原生移动端），用户登录后浏览器持有 cookie（或 localStorage 中的 token 被 JS 读取）。恶意站点可以通过以下方式发起攻击：
- 表单提交（如果基于 cookie 认证）：`<form action="https://aero-id.example.com/v1/messages" method="POST">`
- 即使使用 Bearer token + `Authorization` header，浏览器 `fetch()` 的跨域请求在 CORS 配置下仍然可能被滥用——如果 `cors.go` 配置了 `Access-Control-Allow-Origin: *` 或过于宽泛的 allowlist

**当前检查：** `internal/middleware/cors.go`——需要检查其配置。从 `router.go` 看，`CORSOrigins` 直接被传入 `middleware.CORS`。

**建议：**
- 非 GET 请求校验 `Origin` header 是否在白名单内
- 对基于 cookie 的认证路径（当前不使用，但未来可能需要）增加 `SameSite=Strict` 和 CSRF token
- 明确在文档中说明认证机制：Bearer token 应在 `Authorization` header 中传递，不应使用 cookie

### 1.2 Token 泄露路径

**场景：** Bearer token 通过以下路径可能泄露：

1. **Access log：** `internal/middleware/access_log.go` 可能记录 `Authorization` header——需要检查是否将其纳入日志字段。如果记录了，token 会明文出现在日志文件、日志聚合系统（ELK/Datadog）中。
2. **Error response：** token 验证失败时，`AuthInterceptor` 返回 `status.Error(codes.Unauthenticated, "invalid or expired token")`。信息泄露风险低，但错误信息是否应该区分"token 格式错误"和"token 过期"？
3. **gRPC metadata：** Bearer token 以明文在 gRPC metadata 中传输——如果未启用 TLS，token 被中间人截获。生产环境绕过 TLS 则存在泄露风险。

**建议：**
- 在 `access_log.go` 中明确过滤 `Authorization` header（`zap.String("authorization", "REDACTED")`）
- 确保生产配置强制 TLS（`sso.insecure: false`）
- 考虑 token 前缀截断日志：仅记录 `Bearer xxxxx...xxx` 的前 8 位和后 4 位用于调试

### 1.3 速率限制仅在每实例内存中——攻击者可绕过

**前文已述（各分析均提到）。** 安全视角的总结：

攻击者开 4 个客户端连接，如果 aero-id 运行在 4 个实例后，每个客户端分配的 RPS 配额 = `20 RPS / 4 实例 + 每个实例独立 refill`。实际攻击容量 = `20 × 4 = 80 RPS`。配合少量 IP（10 个），实际可通过容量 = `800 RPS`。这对登录/注册/发消息等 API 是一个真实的滥用风险。

**不修复时的安全影响：** 垃圾消息、枚举用户（通过 token 验证错误消息差异）、暴力尝试 token。

### 1.4 安全相关的配置默认值

| 配置项 | 默认值 | 安全评估 |
|--------|--------|---------|
| `sso.insecure` | `true` | ❌ 生产应该默认为 `false`，或启动时强制检查 |
| `signing_key` | `"change-me-please-very-long-random"` | ❌ 硬编码默认值，运维忘记修改则签名可伪造 |
| `server.grpc.reflection_enabled` | `false` | ✅ 谨慎，默认关闭 |
| `rate_limit.enabled` | `false` | ⚠️ 默认关闭，安全团队可能期望默认开启 |
| `storage.gc.enabled` | `false` | ⚠️ 默认关闭 |
| `tracing.enabled` | `false` | ⚠️ 默认关闭 |

**建议：**
- 生产环境启动时增加启动检查：`signing_key` 是否被修改？`sso.insecure` 如果是 `true` 则打 WARN 日志
- `signing_key` 改为从环境变量读取（`AERO_ID_SIGNING_KEY`），默认值从配置中去掉

### 1.5 blob 存储的访问控制

**当前设计：** S3 presigned URL 直接暴露给客户端。`PresignUpload` 返回的 URL 允许任意文件上传到指定 key。`PresignGet` 下载 key 同理。

**风险：**
- presigned URL 的 TTL（`presignTTL = 5min` 在 `app.go` 中硬编码）如果被客户端缓存或分享，5 分钟内任何持有 URL 的人都可以上传/下载该 key
- `CompleteUpload` 验证 `strings.HasPrefix(in.StorageKey, actor+"/")`——但这个 actor 是从 token 解析的，如果 presigned URL 在 TTL 内被共享，攻击者无法 `CompleteUpload`，但可以直接 PUT 文件到 S3（浪费存储）
- 对于 `PresignGet`，下载 URL 的 TTL 由调用者指定（`DownloadURL` 的 `ttl` 参数），如果传入 1 小时，URL 在 1 小时内可被任意下载

**建议：**
- 对于敏感文件，`PresignGet` 应强制 TTL 上限（如最大 15 分钟），忽略调用者传入的过大值
- 记录 `PresignGet` 的分发日志（谁请求了谁的文件下载链接）
- presigned URL 中加入 IP 绑定（S3 支持 `IpAddress` 条件）

---

## 2. API 设计一致性问题

### 2.1 HTTP 响应格式与 gRPC 响应格式不平行

**HTTP 响应格式**（`httpx.go`）：

```json
// 成功
{"data": { ... }}
// 列表
{"data": {"files": [...], "next_cursor": "xxx"}}
// 成功无内容
204 No Content
// 错误
{"error": {"code": "file.not_found", "message": "..."}}
```

**gRPC 响应格式**（`.proto` 定义）：

```protobuf
message ListFilesResponse {
  repeated File files = 1;
  string next_cursor = 2;
}
```

**差异：**
- HTTP 将所有响应包装在 `{"data": ...}` 中，gRPC 没有外层包装
- HTTP 列表返回：`{"data": {"notifications": [...], "users": {...}, "next_cursor": "..."}}`
- gRPC 列表返回平坦字段：`message ListNotificationsResponse { repeated Notification notifications = 1; map<string, User> users = 2; string next_cursor = 3; }`
- HTTP 错误是 `{"error": {"code": "...", "message": "..."}}`，gRPC 错误使用标准 gRPC status code + message（丢失 `code` 字段）

**问题：** 双传输的客户端需要两套不同的错误解析逻辑。如果从 HTTP 迁移到 gRPC，错误处理代码需要重写——因为 gRPC 不传递 `AppError.Code`（业务错误码），只传递 `codes.Code`（通用状态码）。

**建议：** gRPC 错误也传递业务错误码——使用 `google.golang.org/grpc/status` 的 `Details` 机制或自定义 error message 编码（`code: message` 格式）让客户端可以解析。

### 2.2 HTTP 端点命名不一致

**观察到的命名模式：**

| 语义 | HTTP 路径 | 方法 | gRPC 方法 |
|------|-----------|------|-----------|
| 列表 | `GET /v1/files` | GET | `ListFiles` |
| 获取 | `GET /v1/files/:id` | GET | `GetFile` |
| 删除 | `DELETE /v1/files/:id` | DELETE | `DeleteFile` |
| 标记已读 | `POST /v1/messages/:id/read` | POST | `MarkRead` |
| 置顶？ | 不存在 | - | - |
| 归档？ | 不存在 | - | - |
| 举报评论 | `POST /v1/comments/:id/report` | POST | `ReportComment` |
| 隐藏评论 | `POST /v1/comments/:id/hide` | POST | `HideComment` |
| 搜索 | `GET /v1/search?query=...` | GET | `Search` |
| 通知列表 | `GET /v1/notifications` | GET | `ListNotifications` |

**一致性：** 总体良好，但有一些微妙差异：
- `GET /v1/messages/inbox` vs gRPC `Inbox`——HTTP 用 path，gRPC 用方法名
- `POST /v1/messages/:id/read` 是一个动词路径（read），而 `POST /v1/messages/:id/report` 也是动词，符合 REST 动词风格。但对于 `mark-all-read`，HTTP 没有 `/v1/messages/read-all` 或类似端点，只有 `POST /v1/messages/read-all` 路由？

需要确认 `MarkConversationRead` 的 HTTP 路由。

### 2.3 分页 API 的 cursor 格式不一致

**当前状态：** `next_cursor` 是 UUIDv7 的字符串表示，客户端通过 `?cursor=<uuid>` 传回。这与设计文档一致。

**问题：** 有些 API 使用 `created_at DESC, id DESC` 排序（如 inbox），有些使用 `created_at ASC, id ASC`（评论回复）。两种排序方向意味着 cursor 的比较语义不同——对 DESC 排序，`id < cursor`；对 ASC 排序，`id > cursor`。客户端无法从 cursor 值本身判断所属 API 的排序方向。

**建议：** 在 cursor 值中编码排序方向（如 `cursor = base64(id + ":" + direction)`），或确保所有列表 API 使用统一的排序方向（推荐统一为 `DESC`）。

---

## 3. 运维与部署缺口

### 3.1 Docker 镜像构建缺失

**当前状态：** 有 `deployments/docker-compose.yml`，但没有 `Dockerfile`。`cmd/server/main.go` 和 `cmd/migrate/main.go` 是两个 main package，但没有容器化构建脚本。

```yaml
# docker-compose.yml 依赖
services:
  aero-id:
    build: .  # 或者 image: aero-id:latest 但不存在
```

如果没有 Dockerfile，`docker-compose.yml` 中 `build: .` 会使用默认的 Docker 构建上下文（当前目录），但 Go 项目需要多阶段构建来编译二进制。

**建议：** 增加标准多阶段构建 Dockerfile：

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bin/aero-id ./cmd/server

FROM alpine:3.20
COPY --from=build /app/bin/aero-id /usr/local/bin/
COPY --from=build /app/configs/config.yaml /etc/aero-id/config.yaml
COPY --from=build /app/migrations /migrations
CMD ["aero-id", "-config", "/etc/aero-id/config.yaml"]
```

### 3.2 Docker Compose 不完整

**检查：** `deployments/docker-compose.yml`

需要确认 compose 是否包含：
- Postgres 16
- MinIO（S3 兼容存储）
- aero-id 服务本身
- 可选：OTel collector、Prometheus、Grafana

如果缺少其中任何一环，开发者的本地启动体验就不完整。

### 3.3 优雅关机的时序依赖

`app.shutdown()` 中的关闭顺序：

1. gRPC health flip → NOT_SERVING（通知 LB 排空）
2. stopRelay() → 最后一次 drain
3. stopGC() → 如果有
4. stopRateLimit() → 如果有
5. HTTP server Shutdown（最多等待 `shutdown_timeout`）
6. gRPC GracefulStop()
7. traceShutdown()

**问题：** `stopRelay()` 调用 `close(done)` → relay 内部执行 `r.drain(context.Background())`。但此时 HTTP 服务器可能已经停止了接收新请求，`NotificationDispatcher` 创建的 SSE push 会被写入已经关闭的 SSE 连接——产生对已关闭 channel 的 send（panic 或 silent drop）。

实际上 `hub.Publish` 是 non-blocking send + `select default:`，所以不会 panic 只会 silent drop。但这意味着**最后一批通知的 SSE push 丢失**。

**建议：** 
- relay drain 应该在 HTTP/gRPC 服务器停止**之前**执行，这样排空的事件可以通过活跃的连接推送出去
- 或者在 drain 时不进行 SSE push（在 `Handle()` 中检测 context 是否已取消）

### 3.4 健康检查缺少"写路径"检查

`/readyz` 检查：`database`（critical）+ `sso`（根据 `readiness_critical` 配置）。两者都是读路径检查（DB ping、SSO 连接状态）。

**缺少：**
- 数据库写路径检查（`INSERT INTO health_check (id) VALUES (1) ON CONFLICT DO NOTHING`）
- 存储后端检查（S3 `HeadBucket` 或 local 写权限检查）
- outbox relay 健康检查（relay 是否在正常运行？上次 drain 是否成功？）

**建议：** 增加 `/readyz` 的可选深度检查模式（`?probe=deep`）来验证写路径。

### 3.5 无数据迁移回滚测试

`migrations/migrations_test.go` 验证 `up → down` 的完整性，但**没有验证 down migration 后的数据一致性**。例如：

1. `up` 添加 `message_reactions` 表
2. `down` 删除 `message_reactions` 表

但如果表中已有数据，`down` 执行 `DROP TABLE message_reactions` 会丢失数据。更安全的 down migration 应该是：`ALTER TABLE message_reactions RENAME TO message_reactions_backup_yyyymmdd`。

**建议：** 在 `migrations_test.go` 中增加断言：每个 down migration 至少应该备份数据而不是直接 DROP。

---

## 4. 数据库模式缺口

### 4.1 缺少外键约束（设计如此，但有风险）

**当前设计：** `AGENTS.md` 明确说"无 accounts 表，通过 uid 引用用户，无 FK 约束"。对于 `owner_uid` 和 `from_uid`/`to_uid`，这是合理的（因为 SSO 管理用户生命周期）。

但以下外键关系可能引发问题：

- `comments.target_id` → `files.id` 或 `messages.id`：没有 FK，所以如果目标文件/消息被删除，评论成为 orphan
- `file_shares.file_id` → `files.id`：没有 FK，文件被硬删除后共享记录成为 orphan
- `comment_reports.comment_id` → `comments.id`：同上
- `message_reactions.message_id` → `messages.id`：同上

**影响：** 这些 orphan 行不会影响功能（代码已经通过 `ErrNotFound` 处理了缺失的引用对象），但它们占用存储空间并且可能使管理查询返回不准确的结果。

**建议：** 考虑在 `file_shares`、`comment_reports`、`message_reactions` 等连接表上添加 `ON DELETE CASCADE` 外键（这些是纯关联表，级联删除是安全的）。`comments.target_id` 建议不加 FK（因为 target 可能是 file 或 message，不支持多态 FK）。

### 4.2 缺少 `updated_at` 自动更新

`File` 有 `UpdatedAt`，`Comment` 有 `UpdatedAt`，但 `Message`**没有** `UpdatedAt`——只有 `EditedAt`（仅在编辑时设置）。`Message` 的状态变迁（读取、软删除）没有 `updated_at` 字段。

这会影响：
- 客户端缓存失效（无法判断消息是否有任何状态变更）
- OTel 追踪中的资源状态观测
- 未来审计需求（消息何时被读取——当前只能用 `read_at`）

**建议：** `messages` 表增加 `updated_at` 列，在 `MarkRead`、`SoftDelete` 等操作时一同更新（可以在 GORM 的 `BeforeUpdate` hook 中自动设置）。

### 4.3 `Meta` 字段的类型不安全

`File.Meta`、`Message.Meta` 使用 `map[string]any`（映射到 PostgreSQL 的 `jsonb`）。Go 中 `datatypes.JSONMap` 用于扫描。

**风险：** 任意 JSON 写入可能包含不期望的嵌套结构。没有 schema 验证（如 JSON Schema 或 Go struct 映射）。如果某个消费者期望 `meta.created_at` 是 string 但写入了 number，解析时会 panic 或得到零值。

**建议：** 为每个实体的 `Meta` 定义预期的 JSON schema。如果当前没有结构化的 meta 需求，考虑在服务层注入前做一层校验和转换（如 `meta["created_at"] = meta["created_at"].(string)`）。

---

## 5. 高价值扩展方向（前三次未覆盖）

---

### 🎯 方向 α：OpenAPI / Swagger 文档自动生成

**概念：** 为 HTTP API 生成 OpenAPI 3.0 规范，并提供 Swagger UI 端点供开发者探索。

**为什么需要它：**
1. **当前 API 文档只有 README 中的表格和 `.proto` 文件。** `.proto` 精确描述了 gRPC API，但 HTTP API 没有精确的机器可读规范。开发者需要阅读 handler 代码或 curl 示例来理解请求/响应格式。
2. **前后端协作的契约基础。** 前端团队需要一个稳定、可 mock 的 API 规范来并行开发。当前的前端开发者必须阅读 Go handler 代码或依赖不完整的文档。
3. **Gin 的注解生态成熟。** 有 `swaggo/swag`（gin-swagger）可以从 Go 注解自动生成 OpenAPI 规范。与现有的 `httpx` 渲染层集成，可以自动推导响应格式。
4. **OpenAPI 规范可以生成客户端 SDK。** 自动生成 TypeScript/Python/Kotlin 客户端，减少手动维护 API 调用代码的工作量。

**实现要点：**
- 集成 `github.com/swaggo/swag` + `github.com/swaggo/gin-swagger`
- 为每个 handler 添加结构体注解（`// @Summary`, `// @Param`, `// @Success`, `// @Router`）
- 由于 HTTP 响应使用 `{"data": ...}` 包装，需要自定义响应类型推导
- 端点：`GET /swagger/doc.json` 和 `GET /swagger/index.html`
- 注意：**不要试图覆盖 gRPC**——gRPC API 已经由 `.proto` 精确描述。Swagger 应该只覆盖 HTTP 端点。

---

### 🎯 方向 β：gRPC Web 支持

**概念：** 通过 `grpc-web` 代理（Envoy 或 `improbable-eng/grpc-web`），使浏览器客户端可以直接调用 gRPC 端点，而不需要通过 HTTP REST 桥接。

**为什么需要它：**
1. **消除双传输的维护成本。** 当前所有数据端点都需要在 HTTP 和 gRPC 中各实现一次。如果前端可以直接调用 gRPC，HTTP 层可以逐渐减少为只保留浏览器必需的和操作面端点（health、metrics、SSE、文件上传下载）。
2. **类型安全的浏览器客户端。** 从 `.proto` 直接生成 TypeScript 客户端，编译期捕获接口变更。当前的 HTTP API 变更只能在运行时发现。
3. **gRPC 比 REST 更适合内部服务通信**——强类型、流式支持、高效序列化。如果浏览器通过 gRPC-Web 访问，整个系统的通信范式可以统一。
4. **渐进式采用。** 不需要一次性迁移所有端点。可以先从查询类端点（List、Get、Search）开始，保留写端点的 HTTP 版本。grpc-web 可以和 HTTP API 共存。

**实现要点：**
- 部署 Envoy proxy 或 gRPC-Web  Go 代理（sidecar），将 gRPC-Web 请求转换为标准 gRPC
- 生成 TypeScript 客户端代码（`protoc-gen-grpc-web`）
- SSE 端点（`GET /v1/events`）保持 HTTP 不变——gRPC Server-Sent Events 支持有限
- 文件上传（multipart）保持 HTTP 不变
- 注意：gRPC-Web 不支持 client-side streaming、trailing headers 有限制

---

### 🎯 方向 γ：消息归档与数据生命周期管理

**概念：** 引入自动化的消息/通知/审计日志数据生命周期管理：归档旧数据到低成本存储（如 S3 Glacier），保留元数据但减少热数据库体积。

**为什么需要它：**
1. **数据库增长不可控。** 假设每个用户每天 50 条消息 + 20 个通知 + 5 条审计日志。100 万用户 × 365 天 = 180 亿行。PostgreSQL 在数百亿行级仍然可用，但查询性能显著下降，维护成本（VACUUM、索引维护）急剧上升。
2. **合规要求数据保留，但不需要热访问。** GDPR 要求数据可被删除（right to be forgotten），但许多监管要求保留交易记录 3-7 年。冷热分离架构可以满足合规而不拖累数据库。
3. **当前已经有 TTL 概念但不完整。** GC sweeper 处理文件软删除，idempotency_keys 有 TTL 设计（但没启动），audit_events 完全没有 TTL。需要统一的数据生命周期管理框架。
4. **与 search 互补。** 归档后的消息不应该影响搜索体验——可以在搜索时同时查询热表和冷表（或者归档前为搜索提取关键字段）。

**实现要点：**
- 定义数据分类和保留策略：
  | 数据 | 热保留 | 温保留 | 冷保留 |
  |------|--------|--------|--------|
  | messages | 90 天 | - | - |
  | notifications | 30 天 | - | - |
  | audit_events | 90 天 | 3 年 | 7 年 |
  | idempotency_keys | 24h（已有 TTL） | - | - |
  | files | 随用户删除 | - | - |
- 实现归档 worker（基于 Job Queue）：SELECT → 导出为 Parquet/JSON → 写入 S3 → DELETE
- 查询时：如果用户请求超过 90 天的消息，显示提示"查询归档数据？可能较慢"
- 注意：这是**大规模基础设施方向**，建议在用户量 > 10 万后再投入

---

### 🎯 方向 δ：可插拔认证后端

**概念：** 当前认证硬编码依赖 SSO（gRPC 接口）。抽象出认证后端接口，支持：
- 当前 SSO gRPC（默认）
- **JWT 本地验证**（自签发或第三方如 Auth0/Firebase）
- **OAuth2 Proxy 集成**（通过 `X-Forwarded-User` header 接受认证）
- **开发模式**（允许指定 `X-Actor-UID` header 用于本地开发）

**为什么需要它：**
1. **降低集成门槛。** 不是每个部署场景都有定制的 SSO 服务。小团队或独立开发者可能希望使用 Auth0、Clerk、Firebase Auth 等第三方身份服务。当前架构强迫他们必须实现一个 gRPC SSO 服务器。
2. **本地开发体验差。** 当前需要启动 SSO mock 或真实的 SSO 服务才能运行 aero-id。开发模式下允许简单的 header bypass 可以显著加速开发迭代。
3. **与多租户方向互补。** 不同组织可能使用不同的身份提供商（IdP）。租户 A 用 SSO，租户 B 用 Auth0——可插拔认证使得多 IdP 支持成为可能。
4. **架构清晰。** `middleware/auth.go` 已经定义了 `TokenValidator` 接口，扩展这个接口的签名即可——不需要改 handler/service 层。

**实现要点：**
- 扩展 `TokenValidator` 接口或创建 `AuthBackend` 接口：
  ```go
  type AuthBackend interface {
    ValidateToken(ctx context.Context, token string) (string, error)
  }
  ```
- 新增实现：
  - `SSOBackend`（当前，保持兼容）
  - `JWTBackend`（解析 RS256/ES256 JWT，验证签名，提取 `sub` claim 作为 uid）
  - `ProxyBackend`（从 `X-Forwarded-User` header 读取 uid——依赖前置认证代理）
  - `DevBackend`（从 `X-Actor-UID` header 读取 uid，仅限开发环境）
- 配置：`auth.backend: sso | jwt | proxy | dev`
- gRPC 拦截器也需要支持同样的后端抽象
- 注意：JWT 路径需要 `jwks_url` 配置或公钥配置，以及 `audience` 验证

---

### 🎯 方向 ε：SLA / SLO 框架与自动扩缩容指标

**概念：** 定义 aero-id 的服务水平目标（SLO），暴露关键指标用于自动扩缩容（HPA），并实现"负载信号"（request pile-up）感知。

**为什么需要它：**
1. **当前指标丰富但没有 SLO 框架。** `/metrics` 暴露了 RED 指标和 outbox lag，但没有可用于判断服务是否健康的布尔型"健康信号"（如 "过去 5 分钟的 P99 延迟 > 200ms" 或 "error budget 剩余 < 10%"）。
2. **K8s HPA 需要业务指标。** 当前 HPA 只能基于 CPU/Memory 扩缩容。但 aero-id 是 IO-bound 服务——数据库连接池使用率、outbox pending 量、SSO 延迟才是更好的扩缩容信号。
3. **error budget 驱动决策。** 有了 SLO，团队可以客观判断：是否允许这次上线？上线风险由 error budget 决定，而不是主观判断。
4. **与告警集成。** 当前有指标但无告警规则（Alertmanager 配置缺失）。SLO 框架自然产出告警：**"过去 1 小时 P99 超过 200ms → P2 告警"**。

**实现要点：**
- SLO 定义（建议）：
  | 指标 | 目标 | 窗口 |
  |------|------|------|
  | HTTP/gRPC P99 延迟 | < 200ms | 28 天 |
  | 可用性（非 5xx 比例） | > 99.9% | 28 天 |
  | 通知投递延迟（outbox → created） | < 30s P99 | 7 天 |
  | SSO 验证延迟 | < 500ms P99 | 7 天 |
- 暴露 `metrics.HTTPErrorBudget`（从 RED 指标计算出剩余 error budget）
- 暴露 `metrics.OutboxLag`（当前已有 oldest_event_age，但可用作 HPA 的"需求信号"）
- 为 K8s HPA 创建 Custom Metrics API：`p99_latency_seconds`、`requests_per_second_per_instance`
- 提供示例 Prometheus告警规则（`deployments/prometheus-alerts.yml`）

---

## 6. 前三篇未覆盖的技术债总表

| 等级 | 问题 | 位置 | 类型 |
|------|------|------|------|
| **High** | 无 CSRF 防护 | 全 HTTP 端点 | 安全 |
| **High** | `signing_key` 硬编码默认值 | `configs/config.yaml` | 安全 |
| **High** | HTTP 与 gRPC 错误码不平行（丢失业务 code） | `internal/pkg/errors/errors.go` vs `internal/handler/grpc/server.go` | API 一致性 |
| **Medium** | `Message` 缺少 `updated_at` | `internal/repository/db.go` | Schema |
| **Medium** | `access_log` 未脱敏 Authorization header | `internal/middleware/access_log.go` | 安全/隐私 |
| **Medium** | `docker-compose.yml` 无 MinIO 服务 | `deployments/docker-compose.yml` | 开发体验 |
| **Medium** | 无 Dockerfile | 项目根 | 部署 |
| **Medium** | `presignTTL` 硬编码 5min | `internal/app/app.go` | 配置灵活性 |
| **Low** | 分页 cursor 方向语义不编码 | 全部分页 API | API 设计 |
| **Low** | `Meta` 字段无 schema 校验 | `internal/domain/*.go` | 防御性编程 |
| **Low** | 优雅关机关闭顺序可能丢最后一批 SSE push | `internal/app/app.go` | 可靠性 |

---

## 7. 最终建议

从四篇分析（`ARCHITECT_REVIEW`、`PRODUCT_GAPS`、`ARCHITECTURE_DEEP`、`SECURITY_OPS`）中提炼的**行动优先级**：

```
┌─────────────────────────────────────────────────────────────────┐
│  FIRST — 架构一致性                                               │
│  ├── 决定两套实现去留（套件A vs 套件B） → 移除或完成重构              │
│  └── 启动 idempotency.RunCleaner（1行代码）                       │
├─────────────────────────────────────────────────────────────────┤
│  SECOND — 安全加固                                                │
│  ├── signing_key 从配置中去掉默认值，改强制环境变量                  │
│  ├── access_log 脱敏 Authorization header                       │
│  ├── presignGet TTL 上限、强制启用 TLS（security.txt）             │
│  └── CSRF 防护（至少检查 Origin header）                          │
├─────────────────────────────────────────────────────────────────┤
│  THIRD — 产品功能（从五篇分析中选最紧急的）                          │
│  ├── 通知偏好设置（低投入高UX回报）                                 │
│  ├── 会话管理-归档/静音（低投入高UX回报）                            │
│  ├── 数据导出/GDPR合规（法律要求）                                 │
│  ├── x消息转发/引用回复（产品差异化）                               │
│  └── 管理员角色系统（合规基础）                                     │
├─────────────────────────────────────────────────────────────────┤
│  FOURTH — 运维基础                                                │
│  ├── Dockerfile + compose 完善                                   │
│  ├── Prometheus 告警规则                                         │
│  ├── SLO 定义 + error budget 暴露                                │
│  └── API 文档（OpenAPI / Swagger）                                │
├─────────────────────────────────────────────────────────────────┤
│  FIFTH — 架构演进（长期投入）                                       │
│  ├── 多租户/组织空间（最大架构变更）                                │
│  ├── 群聊消息（第二大架构变更）                                    │
│  ├── 文件预览管道（基础设施投入）                                   │
│  ├── 跨区域部署（SRE 方向）                                       │
│  └── 分布式限流/缓存层                                            │
└─────────────────────────────────────────────────────────────────┘
```
