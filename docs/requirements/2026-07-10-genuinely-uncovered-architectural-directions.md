# aero-id — 深层代码扫描：5 个未被覆盖的高价值架构方向

> **日期**: 2026-07-10
> **方法**: 全量 Go 源码扫描（`internal/` 22 个包、120+ 源文件）、22 份 SQL 迁移、双传输层 handler、composition root `app.go`、proto 定义、`docs/requirements/` 下 40+ 份已有分析文档的逐条比对
> **原则**: 与既有文档零重复。每个方向均提供精确代码级证据（文件名 + 行号）
> **零代码变更**

---

## 前置：已有 40+ 份分析文档的核心覆盖范围

为避免重复，以下方向已在 `docs/requirements/` 中得到系统性分析，本文档**不重复**：

- 异步管道追踪连续性、Read-Your-Writes 一致性、数据导出与级联删除
- 多后端混合存储、API 生命周期管理、SSE Hub 单实例瓶颈
- 跨实例 pub/sub、通知外带渠道、文件分享过期时间、用户封禁端点
- 并发点赞 TOCTOU、未读计数索引优化、Outbox 重复投递
- SSO 缓存容量上限、搜索 API 数据泄露、中文 FTS 回退策略
- 通知偏好静音时段实现确认、文件审计日志缺失、GORM 查询优化
- 群聊、活动流、策略引擎、审核工作台、文件加工管线

**本文档聚焦于已经被代码实现但暴露不完整、或完全未被代码覆盖的高价值方向。**

---

## 方向一：`internal/file` 包是完整的死代码（Dead Code / 双实现维护陷阱）

### 为什么这是一个方向级别的问题

当前代码库存在**两套完整的文件域实现**——一套被生产使用，一套是完全独立的副本，但从不被 `app.go` 引用。这不是一个简单的"代码异味"，而是：

| 指标 | 活代码（生产使用） | 死代码（未引用） |
|------|------------------|-----------------|
| Service 定义 | `internal/service/file.go` + `_presign.go` + `_share.go` | `internal/file/service_crud.go` + `_share.go` + `_upload.go` |
| Service 接口 | `internal/service/service.go` (FileService interface) | `internal/file/file.go` (Service interface) |
| Repository 接口 | `internal/repository/file.go` + `_share.go` | `internal/file/repository.go` + `_share.go` |
| GORM 实现 | `internal/repository/` (repository package) | `internal/file/repository.go` (file package 独立实现) |
| Handler (HTTP) | `internal/handler/http/file.go` | `internal/file/handler_http.go` |
| Handler (gRPC) | `internal/handler/grpc/file.go` | `internal/file/handler_grpc.go` |

### 代码级证据

**证据 1：`internal/file` 包的 Service 接口从未被 app.go 引用**

```go
// internal/app/app.go:236
fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type, ...)
// 注意: 是 service.NewFileService (internal/service)，不是 file.NewService (internal/file)
```

**证据 2：`internal/file` 包的 HTTP handler 从未被注册**

```go
// internal/handler/http/router.go:122
httpapi.NewFileHandler(fileSvc, ...) // 来自 internal/handler/http，不是 internal/file
```

而 `internal/file/handler_http.go` 定义了自己的 `RegisterRoutes`，但从未被 `NewRouter` 调用。

**证据 3：`internal/file` 包的 Repository 实现(含 GORM 映射)完全独立**

```go
// internal/file/repository.go:14
type fileRepo struct { db *gorm.DB } // ← 自己的 GORM repo，独立于 internal/repository/
```

这不是简单的接口定义——它包含了完整的 `fileRow`、`fileRowToDomain`、CRUD 方法、甚至 `SearchFiles`（使用 raw SQL 而非 GORM）。

**证据 4：gRPC handler 同样有双实现**

```go
// internal/file/handler_grpc.go — 定义了自己的 Server 类型，有 Register 方法
type FileServer struct { ... }
// 但从未在 server.go 中注册
```

```go
// internal/handler/grpc/file.go — 被 app.go 实际使用
func NewFileServer(svc service.FileService) *FileServer { ... }
```

### 为什么需要修复

1. **维护分裂**：对一个功能做修改时需要改两处，或由于只改了一处导致行为不一致。已有测试覆盖两套代码（如 `internal/file/service_test.go`、`internal/file/handler_http_test.go`），但生产运行时走的是另一条路径——测试通过但生产行为不同。

2. **新团队成员的困惑**：`internal/file` 包的文件数（10 个源文件）和 `internal/service` 同类文件数量相当，新人需要花时间厘清哪一个才是"真正"的实现。

3. **代码审计负担**：每次安全/合规代码审查需要检查两套实现。

### 建议

- 短期：在 `internal/file` 包顶层加一个 `// Deprecated: unused — use internal/service/file.go instead` 注释，阻止新功能加到此处
- 中期：确认 `internal/file` 所有测试是否可以迁移或删除；确认无任何外围引用
- 长期：删除 `internal/file` 包中的 `service_*.go`、`handler_*.go`、`repository*.go`（保留 `file.go` 的类型定义如果被外部引用），消除双实现

---

## 方向二：管理员角色管理 API 只有一半——Repository 已实现但双传输层均未暴露

### 为什么需要它

`000022_admin_roles.up.sql` 迁移已创建了 `roles`、`user_roles` 表，预置了 `moderator` 和 `admin` 两个角色。`internal/repository/admin.go` 完整实现了 `GrantRole`、`RevokeRole`、`HasPermission`。但：

| 操作 | Repository 实现 | HTTP Handler | gRPC Handler | Proto 定义 |
|------|---------------|-------------|-------------|-----------|
| 授予角色 (GrantRole) | ✅ (admin.go:101) | ❌ | ❌ | ❌ |
| 撤销角色 (RevokeRole) | ✅ (admin.go:114) | ❌ | ❌ | ❌ |
| 检查权限 (HasPermission) | ✅ (admin.go:52) | ✅ (HTTP admin handler 内部使用) | ❌ | ❌ |
| 审计日志查询 (ListAudit) | ✅ (admin.go:69) | ✅ (GET /v1/admin/audit-log) | ❌ | ❌ |

### 代码级证据

**证据 1：Proto 中无 AdminService 定义**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
// 只有 FileService, MessageService, CommentService, NotificationService,
// BlockService, SearchService, PreferenceService — 无 AdminService
```

**证据 2：gRPC server 未注册任何 admin 服务**

```go
// internal/handler/grpc/server.go
// NewServerWithOptions 注册了 File/Message/Comment/Notification/Block/Search/Preference
// — 无 Admin 注册入口
```

**证据 3：HTTP Handler 只暴露了审计日志**

```go
// internal/handler/http/admin.go:22-23
func (h *AdminHandler) RegisterRoutes(r Routes) {
    r.Auth.GET("/admin/audit-log", h.ListAudit)
    // 无 GrantRole / RevokeRole 路由
}
```

**证据 4：Repository 的 GrantRole/RevokeRole 是完好的**

```go
// internal/repository/admin.go:101-124
func (r *adminRepo) GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error { ... }
func (r *adminRepo) RevokeRole(ctx context.Context, uid, roleCode string) error { ... }
```

### 影响

1. **角色管理无法自助**：当前没有 API 可以授予或撤销管理员角色。要设置一个管理员，运维人员必须直接操作数据库：
   ```sql
   INSERT INTO user_roles (uid, role_code, granted_by) VALUES ('some-uid', 'admin', 'operator');
   ```

2. **安全审计缺失**：直接操作数据库不记录审计日志（`audit_events` 表），无法追溯"谁在什么时候授予了谁管理员权限"。

3. **gRPC 协议不完整**：admin audit-log 在 HTTP 端有，但 gRPC 完全没有对应的 `AdminService`，破坏了"HTTP↔gRPC 对等"的架构承诺（Roadmap 明确宣称全部 38 个 RPC 已对等）。

4. **权限检查的入口不一致**：HTTP 端通过 `HasPermission` + middleware 检查权限。但实际使用中只有 `admin` handler 内部手工调用 `HasPermission`，没有统一的 `RequirePermission` 中间件。

### 建议

- 在 proto 中新增 `AdminService`，至少包含 `GrantRole`/`RevokeRole`/`ListAudit` 三个 RPC
- HTTP 侧补充 `POST /v1/admin/roles/grant` 和 `POST /v1/admin/roles/revoke`
- 所有 admin 端点由统一的 `RequirePermission` 中间件保护（而非 handler 内部手工检查）
- 在 app.go 的 composition root 中注册 `AdminServer`（当前 `adminRepo` 已被传入 HTTP handler，只需为 gRPC 创建对应的 server struct）

---

## 方向三：预签名直传路径（PresignUpload）跳过了内容去重和引用计数

### 为什么需要它

`PresignUpload` + `CompleteUpload` 是文件上传的"大文件优化路径"——客户端直接上传到 S3，aero-id 只做 finalize。但该路径**显式跳过了**两个关键机制：

1. **内容去重（Content-Dedup）**：`CompleteUpload` 的代码注释明确说："Content-checksum dedup is intentionally SKIPPED for direct uploads: the bytes never pass through this process, so we cannot hash them without an extra download."

2. **storage_ref 引用计数**：`CompleteUpload` 不调用 `refs.Increment()`，所以引用计数不会增加。但同一个 blob 如果通过普通创建（`Create`）路径上传并去重，引用计数已经被增加——两条路径的计数行为不一致。

### 代码级证据

**证据 1：CompleteUpload 不做去重**

```go
// internal/service/file_presign.go:60-65
// Content-checksum dedup is intentionally SKIPPED for direct uploads:
// the bytes never pass through this process, so we cannot hash them
// without an extra download. The row is persisted with an empty checksum.
```

对比普通创建路径：

```go
// internal/service/file.go:140-155
// Deduplicate: if this owner already has an identical file, reuse its blob ...
reused := false
if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
    if existing.StorageKey != key {
        _ = s.store.Delete(ctx, key)
        key = existing.StorageKey
    }
    reused = true
}
```

**证据 2：CompleteUpload 不调用 refs.Increment**

```go
// internal/service/file_presign.go:108-120 (CompleteUpload 结尾)
if err := s.files.Create(ctx, f); err != nil {
    _ = s.store.Delete(ctx, in.StorageKey)
    return nil, apperr.Internal("file.create_failed", ...)
}
return f, nil
// ← 无 s.refs.Increment(ctx, f.StorageKey) 调用
```

对比普通创建路径：

```go
// internal/service/file.go:162-165
if s.refs != nil {
    _, _ = s.refs.Increment(ctx, f.StorageKey)
}
```

### 边界条件

| 场景 | 后果 |
|------|------|
| 同一用户通过 direct upload 上传相同文件两次 | 存储 2 份副本，但 GC 只看到一个 ref count |
| 同一文件被普通创建上传 + 被 direct upload "上传" | 第一份有 ref count=1，第二份 ref count=0 → GC 可能错误回收第一份的 blob |
| 用户 upload → CompleteUpload → Delete → GC sweep | 普通创建的 Delete 调用 `Decrement` 到 0 且删除 blob；但 direct upload 的 Delete 调用 `Decrement` 但 count 已经是 0 → 重复删除可能返回 `ErrNotFound` |

### 建议

- **短期**：在 `CompleteUpload` 中加入 `s.refs.Increment()` 调用，使引用计数与普通创建路径一致
- **中期**：在 `CompleteUpload` 中加入可选的 checksum 计算——用户可以在上传时提供文件的 SHA256（通过请求头或额外的 presign 参数），aero-id 在 finalize 时验证，从而实现去重
- **长期**：引入 S3 事件通知——当 S3 对象创建时，自动触发 aero-id 的 webhook 来验证 + 去重 + 完成引用计数

---

## 方向四：内容安全管线的完全缺失——文件/消息/评论均无恶意内容检测

### 为什么需要它

aero-id 是一个管理**用户产生内容**的服务——文件上传、私信、评论。当前的安全边界全部在**准入层**（MIME 类型白名单、文件大小上限、认证授权），但对内容本身不做任何检测：

| 检测类型 | 功能状态 | 典型风险 |
|---------|---------|---------|
| 病毒/恶意软件扫描 | ❌ 无 | 用户上传含病毒文件→其他用户下载→感染 |
| 图片/视频 NSFW 检测 | ❌ 无 | 用户上传色情/暴力内容→通过公开分享传播 |
| 文本敏感词/仇恨言论检测 | ❌ 无 | 私信/评论中的辱骂、钓鱼链接 |
| CSAM（儿童性虐待内容）检测 | ❌ 无 | **法律合规风险**（多数司法管辖区要求上报） |
| 文件类型伪装检测 | ❌ 无 | `.jpg` 扩展名 + 实际 exe 内容绕过 MIME 检查 |
| 消息/评论中的 URL 安全检查 | ❌ 无 | 钓鱼链接、恶意域名 |

### 代码级证据

**证据 1：所有"内容检测"只有 MIME 类型白名单**

```go
// internal/service/file.go:170-176
func (s *fileService) mimeAllowed(contentType string) bool {
    if len(s.allowedMime) == 0 {
        return true // ← 默认允许所有类型！
    }
    mt, _, err := mime.ParseMediaType(contentType)
    if err != nil {
        return false
    }
    return s.allowedMime[strings.ToLower(mt)]
}
```

MIME 检查只检查 `Content-Type` 头，不验证实际文件内容（不读 magic bytes）。配置若留空（默认）则全部放行。

**证据 2：消息/评论内容无任何过滤**

```go
// internal/service/message.go — Send 方法
func (s *messageService) Send(ctx context.Context, toUID, content string, kind domain.MessageKind) (*domain.Message, error) {
    // ← 无 content 安全检查，无敏感词过滤，无 URL 验证
    // ← 无长度检查（content 字段是 TEXT，无上限）
}
```

```go
// internal/service/comment.go — Create 方法
func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
    // ← 无 content 安全检查，无嵌套链接检测
}
```

**证据 3：不存在任何内容审核 pipeline**

```bash
$ grep -rn "virus\|malware\|scan\|clamav\|nsfw\|moderat\|sensitive\|toxic\|harmful\|abuse\|phish\|spam" internal/ --include="*.go" | grep -v "comment_report\|CommentReport\|report\|moderation\|moderator\|audit" | head -5
# 无输出 — 没有任何恶意内容检测代码
```

### 为什么这是一个架构方向（而非简单功能）

1. **基础设施已就位**：outbox 事件系统是天然的异步处理管道——新文件上传、消息发送、评论创建都已产生 outbox 事件，内容检测可以作为一个新的 `Handler` 插入 `Chain` 中：
   ```
   relay → Chain{ContentScanner, NotificationDispatcher, WebhookHandler}
   ```

2. **检测需要异步 + 外部集成**：真正的病毒扫描（ClamAV）或 NSFW 检测（第三方 API）是阻塞操作，不能放在请求路径中。outbox relay 的 backoff/重试/死信机制正好适用。

3. **合规红线**：在多数司法管辖区，托管用户生成内容而不做 CSAM 检测可能构成违法。这不是功能选择，是合规底线。

### 建议

在 `internal/content/` 包中定义 Scanner 接口：

```go
type Scanner interface {
    Scan(ctx context.Context, content *ScanInput) (*ScanResult, error)
}
```

具体实现：

| 实现 | 检测内容 | 集成方式 |
|------|---------|---------|
| `clamav` | 文件病毒扫描 | ClamAV REST 或 UNIX socket |
| `nsfw` | 图像 NSFW 检测 | 第三方 API (Sightengine/AWS Rekognition) |
| `textfilter` | 文本敏感词/钓鱼检测 | 正则 + 域名黑名单 |
| `composite` | 组合多个 Scanner | Chain of Responsibility |

然后在 outbox chain 中插入：

```go
scanner := content.NewCompositeScanner(clamav, nsfw, textfilter)
contentHandler := content.NewOutboxHandler(scanner, fileRepo, commentRepo, messageRepo)
relayHandler := outbox.NewChain(contentHandler, dispatcher, webhookHandler)
```

---

## 方向五：`/metrics` 端点缺少 RED 断路器——生产可观测性的最后一公里

### 为什么需要它

当前 `/metrics` 端点暴露了 Prometheus 格式的指标：HTTP RED、gRPC RED、SSO 健康、outbox 延迟。但缺少一个关键能力：**基于指标的断路器**。

### 代码级证据

**证据 1：metrics registry 仅采集不决策**

```go
// internal/pkg/metrics/metrics.go  — 只实现 Register/Observe/Gauge 等采集方法
// 无任何阈值检查、无告警规则、无自动降级
```

**证据 2：系统有多个"熔断点"但各自独立**

```go
// 独立熔断点 1 — SSO circuit breaker
// internal/sso/breaker.go
// 只保护 SSO gRPC 调用，不感知全局请求成功率

// 独立熔断点 2 — outbox dead letter
// internal/outbox/relay.go
// 事件失败超过 maxRetries 后标记死信，不感知系统负载

// 独立熔断点 3 — rate limiter
// internal/pkg/ratelimit/ratelimit.go
// 每个 actor 独立限流，不感知全局 P99 延迟
```

**证据 3：没有"全局健康"的 API 端点**

```go
// internal/handler/http/health.go
// /healthz → 进程存活（简单 ping）
// /readyz → DB ping + SSO 可连（通过就是 green，通不过就是 red）
// 无中间状态（degraded）、无基于指标的自我诊断
```

### 场景示例

以下场景当前无法自动响应：

| 场景 | 指标变化 | 应该发生 | 实际发生 |
|------|---------|---------|---------|
| S3 存储延迟突增（5s → 30s） | HTTP 文件下载 P99 延迟飙升 | `/readyz` 切换为 degraded 状态 | `/readyz` 仍为 green（不检查 S3），请求继续涌入，连接池耗尽 |
| SSO 部分可用（50% 请求超时） | SSO 错误率 > 5% | 自动启用 cache-stale 模式 | circuit breaker 按实例独立判断，无全局协调 |
| 大量 outbox 事件积压（>10K 未发布） | outbox 延迟 > 30min | 限制新的写入操作或触发告警 | 系统正常接受写请求，outbox 积压持续增加 |

### 建议

引入**基于 RED 指标的全局健康仪表盘**：

**Phase 1 — 全局健康端点**（低投入）

新增 `GET /v1/admin/health`（需要 `audit.read` 权限），返回：

```json
{
  "status": "degraded",
  "checks": {
    "database": {"status": "ok", "latency_p99_ms": 5},
    "sso": {"status": "degraded", "error_rate_pct": 3.2, "latency_p99_ms": 1200},
    "storage": {"status": "degraded", "latency_p99_ms": 15000},
    "outbox": {"status": "warning", "pending_events": 15420, "oldest_age_min": 12}
  },
  "recommendation": "S3 latency elevated; consider enabling storage GC or throttling uploads"
}
```

**Phase 2 — 基于指标的自动降级**（中投入）

在 app.go 的 composition root 中引入一个 `HealthMonitor`：

```go
type HealthMonitor struct {
    // 注册所有关键依赖的健康指标
    // 当某个指标越过阈值时:
    //   1. 更新 /readyz 状态为 degraded
    //   2. 推送告警事件到 outbox
    //   3. 可选: 触发自动降级策略（如限流调低、SSO 强制 serve-stale）
}
```

**Phase 3 — 全局协调断路器**（高投入）

当部分实例检测到整体错误率升高时：

1. 通过共享存储（Postgres advisory lock 或轻量心跳表）记录"集群级别"的健康状态
2. 所有实例共同响应：非关键路径降级、全量限流收紧、同步等待恢复

---

## 总结

| # | 方向 | 类型 | 代码证据强度 | 投入 | 优先级 |
|---|------|------|-------------|------|--------|
| 1 | `internal/file` 死代码消除 | 工程健康度 | ✅ 精确到 import 路径 | 低 | **P0** — 阻止后续维护错误 |
| 2 | Admin 角色管理 API 未暴露 | 功能不完整 | ✅ Repository 有但 Handler 无 | 中 | **P1** — 安全运营缺口 |
| 3 | PresignUpload 跳过去重+引用计数 | 数据完整性 | ✅ 代码注释明确声明 Skip | 低 | **P1** — 存储浪费+GC 风险 |
| 4 | 内容安全管线完全缺失 | 合规/安全 | ✅ 全仓搜索无任何扫描代码 | 高 | **P1** — 合规红线 |
| 5 | RED 断路器 + 全局健康端点的缺失 | 可观测性 | ✅ 独立熔断点已存在但无全局视图 | 中 | **P2** — 生产强化后 |

**建议执行顺序**：方向 1（立即）→ 方向 3（立即）→ 方向 2（本迭代）→ 方向 5（下个迭代）→ 方向 4（长期规划）。

> 方向 1 和 3 基本没有副作用，是纯收益的低投入修复。方向 4 虽然投入高，但考虑到法律合规风险，不应无限期推迟。
