# aero-id — 最终盲区：五轮交叉分析后的剩余高价值方向

> **作者**: 资深架构师 / PM 视角  
> **日期**: 2026-07-10  
> **方法**: 对 `docs/requirements/` 下 8 份方向文档 + `docs/REVIEW_*` 下 8 份审查文档做逐主题交叉比对，定位零覆盖方向；再回扫 `internal/` 全部 22 个 Go 包验证代码级缺口  
> **前置条件**: `feat/roadmap` 全量合并，所有已有方向分析仅供参考  
> **无代码变更**

---

## 交叉分析方法

已有 16 份分析文档累计覆盖 **50+** 个方向。逐一提取每份文档的 "高价值扩展方向" 章节主题，合并去重，然后反向查找零覆盖主题。

```
交叉方法:
  1. 从 8 份 requirements/*.md 提取所有方向标题
  2. 从 8 份 REVIEW_*.md 提取所有方向标题
  3. 合并去重 → ~50 个方向
  4. 回扫 Go 代码寻找已实现但未分析的盲区
  5. 定位零覆盖或仅单句提及的主题
```

**结果**: 以下 5 个方向在所有 16 份现有文档中 **零覆盖**。

| 方向 | 现有覆盖 | 风险等级 |
|------|---------|---------|
| 1. 文件存储加密（静态加密） | ❌ 零覆盖 | High — 数据安全基线 |
| 2. 文件分享权限细化（过期/权限级/水印） | ❌ 零覆盖 | Medium — 产品差距 |
| 3. API 生命周期管理（版本化/弃用/兼容性契约） | ❌ 零覆盖 | High — 演进阻塞 |
| 4. 后台异步管线可观测性框架 | ❌ 零覆盖 | Medium — 运维盲区 |
| 5. 供应链安全与依赖治理 | ❌ 零覆盖 | Medium — 合规准入 |

---

## 方向一：文件存储加密（静态加密）

### 为什么需要它

aero-id 的核心承诺是 **"data owned by accounts"**（用户拥有的数据）。但在当前实现中，文件以明文 blob 的形式存储在磁盘（local 模式）或 S3（s3 模式）上。任何拥有存储后端访问权限的人——包括运维人员、云服务商员工、或者通过 SSRF 漏洞获得存储凭证的攻击者——都可以直接读取所有用户文件。

这是产品信任模型中最基础的缺口。

**代码级证据:**

| 位置 | 发现 |
|------|------|
| `internal/storage/local.go` | `Put` 直接将 `reader` 写入磁盘文件，无加密 |
| `internal/storage/s3.go` | 使用 AWS SDK `PutObject`，未设置 `ServerSideEncryption` 选项 |
| `internal/storage/storage.go` | `FileStorage` 接口定义 `Put/Get/Delete` 和 `PresignUpload`，均与加密无关 |
| `internal/file/service_crud.go` | `Create` 路径接受 `io.Reader` 并直接传给 `store.Put()`，无加密步骤 |
| `internal/file/service_upload.go` | `CompleteUpload` 调用 `store.Stat()` 验证对象存在后即写入元数据，无完整性校验 |
| `configs/config.yaml` | 无 `storage.encryption.*` 配置节 |
| `internal/config/config.go` | 无加密相关配置字段 |

现有安全机制全部集中传输层（SSO mTLS/TLS、HMAC 签名），存储层安全完全缺席。

### 建议方案

#### 1. 服务器端加密（SSE-S3 / SSE-KMS）

最轻量的方案：由 S3/MinIO 后端处理加密，aero-id 在请求中设置加密头。

```go
// S3 客户端配置：声明加密需求
input := &s3.PutObjectInput{
    Bucket:               &bucket,
    Key:                  &key,
    Body:                 reader,
    ServerSideEncryption: types.ServerSideEncryptionAwsKms,
    SSEKMSKeyId:          &kmsKeyID, // 可选：使用特定 KMS key
}
```

**优点**: 零应用层变更，利用存储后端的能力  
**缺点**: 密钥管理依赖云服务商；本地存储（local）不支持此模式

#### 2. 服务器端加密 + 信封加密（Go 实现）

适用 local 模式或需要跨存储后端一致加密策略的场景：

```
┌─────────────────┐
│  应用层密钥环    │ ← 从环境变量 / Vault 加载 DEK（数据加密密钥）
├─────────────────┤
│  Put(reader) →  │
│  1. 生成随机 DEK │
│  2. AES-256-GCM  │
│     加密内容     │
│  3. 存储:        │
│    ciphertext    │
│    + nonce (12B) │
│    + DEK 包装    │
│    （可选KMS封装）│
│  4. 删除明文 DEK │
└─────────────────┘
```

`FileStorage` 接口需要扩展：`EncryptedStorage` 装饰器包裹 `Put/Get`，或 StorageKey 后缀约定非加密/加密态。

#### 3. 客户端加密（E2EE 可选）

对于高安全要求的场景（隐私消息附件、法律文件），支持客户端加密——文件在浏览器端用用户密码或密钥加密后再上传，服务端永不解密。

```
POST /v1/files/upload
Content-Type: multipart/form-data
x-encryption-algorithm: AES-256-GCM
x-encryption-key-id: user-key-abc     ← 服务端只存储标识符，不存储密钥

→ 服务端存储密文 blob
→ Get 时返回密文，客户端本地解密
→ 搜索/缩略图服务跳过加密文件（或由用户在本地解密后提交）
```

### 边界情况与设计考量

| Edge Case | 处理方式 |
|-----------|----------|
| **密钥轮换** | 配置 `encryption.key_rotation_interval: 90d`，后台 Job 遍历文件，用新密钥解密+再加密（re-encrypt） |
| **密钥丢失** | 提供灾难恢复方案：若 DEK 丢失，文件不可恢复。通过 `encryption.key_backup_phrase` 或 KMS 备份 |
| **加密对去重的影响** | 内容去重（checksum）在加密层之前计算：先 hash 明文，再加密。相同明文的两个用户上传共享同一 storage_key |
| **GC/缩略图管道** | 加密文件 GC sweep 需要解密后才能检查 content；缩略图生成需要解密后再处理——增加处理延迟 |
| **Presigned URL 上传** | presigned URL 模式不支持客户端加密中间人（客户端直接 PUT 到 S3）——需要调整：要么禁用 presigned 的加密文件，要么客户端先加密然后 PUT 密文 |

### 建议优先级

**P1** — 数据安全基线。从 SSE-S3（S3 模式）和信封加密（local 模式）开始，E2EE 作为 P2 可选。

---

## 方向二：文件分享权限细化（过期时间 / 权限层级 / 水印）

### 为什么需要它

当前的 `file_shares` 是一个二元模型：**"分享"或"未分享"**。当文件的 Read ACL 包含某个 uid 时，该 uid 可以**无限制地**读取、下载、转发该文件。没有过期时间、没有只读/可下载区分、没有防转发措施。

**代码级证据:**

| 位置 | 发现 |
|------|------|
| `internal/repository/file_share.go` | `Grant` 只接受 `(fileID, ownerUID, granteeUIDs)` —— 无过期时间、无权限层级参数 |
| `internal/repository/file_share.go` | `IsSharedWith` 返回 `bool` —— 二元判断，不区分权限范围 |
| `migrations/000006_create_file_shares.up.sql` | 表定义只有 `file_id`, `owner_uid`, `grantee_uid`, `created_at` —— 无 `expires_at`, `permission`, `max_downloads` 列 |
| `internal/file/service_share.go` | `ShareFile` / `UnshareFile` —— 无 permission 枚举、无过期校验 |
| `internal/handler/http/file.go` | `POST /v1/files/:id/share` 和 `DELETE /v1/files/:id/share` —— API 不携带过期参数 |

**缺失的产品能力:**

- **临时分享**: 分享链接 24 小时后自动过期
- **只读 vs 可下载**: 某些文件只允许在线预览，不允许下载原文件
- **防转发水印**: 下载的文件自动添加水印（包含下载者 uid + 时间戳）
- **访问审计**: 查看"谁在什么时候下载了分享给我的文件"
- **分享上限**: 每个文件最多分享给 N 人

### 建议方案

```sql
-- 改造 file_shares 表
ALTER TABLE file_shares ADD COLUMN IF NOT EXISTS
    permission         TEXT NOT NULL DEFAULT 'read',   -- 'read' | 'download' | 'preview_only'
    expires_at         TIMESTAMPTZ,                    -- NULL = 永不过期
    max_downloads      INT,                            -- NULL = 无限制
    watermark          BOOLEAN NOT NULL DEFAULT FALSE, -- 是否加水印
    created_by         TEXT NOT NULL DEFAULT '',        -- 谁授权了这个分享
    last_accessed_at   TIMESTAMPTZ;
```

| API 变更 | 说明 |
|----------|------|
| `POST /v1/files/:id/share` | 增加 `permission`, `expires_in`, `max_downloads`, `watermark` 可选参数 |
| `GET /v1/files/:id/shares` | 返回分享列表（含过期状态、已用下载次数、最后访问时间） |
| `PATCH /v1/files/:id/shares/:uid` | 修改某个 grante 的权限/过期时间 |
| `GET /v1/files/:id/download-log` | 下载日志（谁 + 什么时候 + 从哪个 IP） |

### 服务层影响

```go
type ShareGranularInput struct {
    GranteeUIDs []string
    Permission  SharePermission  // read | download | preview_only
    ExpiresIn   *time.Duration
    MaxDownloads *int
    Watermark   bool
}

type SharePermission string
const (
    ShareRead        SharePermission = "read"         // 可查看 metadata + 在线预览
    ShareDownload    SharePermission = "download"     // 可下载原文件
    SharePreviewOnly SharePermission = "preview_only" // 仅在线预览，不可 metadata
)
```

`FileStorage` 的 `Get` 已返回 `io.ReadCloser`，水印可以在中间件层实现：检测到 `watermark=true`，在返回前用 `image` 或 `pdfcpu` 叠加水印。

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **分享过期后已下载的文件** | 过期不影响已下载的副本（服务端无法控制）。过期只阻止新的下载/预览 |
| **水印与缩略图的冲突** | 缩略图预生成时无水印（性能考虑）；原始下载加水印 |
| **max_downloads 并发下载** | 使用原子计数器 `UPDATE ... SET downloads = downloads + 1 WHERE downloads < max_downloads` |
| **分享者删除文件** | 所有分享记录随文件软删除而失效 |

### 建议优先级

**P2** — 在存储配额（expansion-directions.md）和公开分享（remaining-high-value-directions.md）之后，作为文件分享模型的增强。

---

## 方向三：API 生命周期管理（版本化 / 弃用 / 兼容性契约）

### 为什么需要它

aero-id 同时提供 **HTTP REST** 和 **gRPC** 两套传输协议，共享同一 proto 定义。这是当前最强的架构优势，也是最大的演进风险——因为没有版本化策略，任何对 proto 的破坏性变更（字段重命名、删除、类型变更）都会**同时破坏所有现有客户端**。

**代码级证据:**

| 位置 | 发现 |
|------|------|
| `api/proto/aero_id/v1/aero_id.proto` | 包名 `aero_id.v1`，所有 RPC 和服务都在同一个 proto 文件中。当需要 v2 时，没有既定的组织规范 |
| `internal/handler/grpc/server.go` | 注册单一 `AeroIDServiceServer`，无版本路由概念 |
| `internal/handler/http/router.go` | 路由组固定在 `/v1`，硬编码。如需 `/v2` 需要手动新建组 |
| `internal/handler/grpc/validate.go` | 参数校验器——变更字段需要更新这里，但旧客户端继续发送旧格式 |
| 所有 Response DTO | handler 层 `toXxxResponse()` 函数的 schema 直接映射 `domain` 模型——没有 response 版本化适配层 |

**具体风险场景：**

1. **字段类型变更**: 假设 `MessageKind` 从 `int16` 变为 `string`（例如 `"text"` 替代 `1`）。新旧 proto 序列化不兼容，所有未更新 proto 的 gRPC 客户端立即崩溃。

2. **端点重命名**: `POST /v1/messages` 升级为 `POST /v1/conversations/:id/messages`。当前没有 `Deprecation` header 通知机制，旧客户端静默失败。

3. **Enum 值移除**: 移除 `MessageKindSystem` (4)。已序列化消息中如有旧值，新 proto 解析失败。

4. **双向传输不同步**: HTTP handler 和 gRPC handler 各自独立演化，HTTP 加了新字段但 gRPC 没加（或反之）——parity_test.go 能发现但不阻止。

### 建议方案

#### 1. Proto 包版本化约定

```protobuf
// 当前：所有 RPC 在 aero_id.v1
package aero_id.v1;

// 新增 v2 时：新建目录 + proto 文件
// api/proto/aero_id/v2/aero_id.proto
package aero_id.v2;
```

gRPC 天然支持版本共存：

```go
// server.go 同时注册两个版本
pbv1.RegisterAeroIDServiceServer(grpcServer, v1Svc)
pbv2.RegisterAeroIDServiceServer(grpcServer, v2Svc)
// 客户端通过不同的 fully-qualified service name 区分
```

#### 2. HTTP URL 版本策略

```go
// router.go 新增版本组
v2 := e.Group("/v2", authMW...)
// 旧客户端继续使用 /v1
// 新客户端使用 /v2
```

两者的 handler 共享同一 service 层，但在 request/response 映射层做版本适配：

```go
type FileHandlerV2 struct {
    svc service.FileService
}
func (h *FileHandlerV2) RegisterRoutes(r Routes) {
    r.V2Auth.GET("/files", h.List)
    // Response 可以包含新字段，旧版本没有的字段用 omitempty
}
```

#### 3. Deprecation 元数据契约

```go
// 弃用中间件：为即将移除的端点注入 Deprecation + Sunset header
func Deprecation(apiVersion string, sunsetDate string) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("Deprecation", "true")
        c.Header("Sunset", sunsetDate) // RFC 1123 格式
        c.Header("Link", fmt.Sprintf(
            `</v2%s>; rel="successor-version"`, c.Request.URL.Path))
        c.Next()
    }
}
```

#### 4. 向后兼容性 CI 门禁

新增 `make check-api-compat`：对比当前 proto 与上一个 release tag 的 proto，检测破坏性变更：

```
Breaking changes detected:
  ✗ aero_id.v1.Message: field "kind" changed type int32→string
  ✗ aero_id.v1.FileService: removed RPC "Delete"
  ! aero_id.v1.Message: added field "thread_id" (backward compatible)
```

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **gRPC 客户端使用旧 proto 调用新服务** | gRPC 的 unknown field 处理：新服务会忽略旧客户端不认识的字段；旧字段序列化兼容。但如果字段类型改变，则直接失败 |
| **同一连接上的多版本共存** | gRPC 的服务名（`aero_id.v1.AeroIDService` vs `aero_id.v2.AeroIDService`）天然隔离；HTTP 通过 `/v1/` vs `/v2/` 路径隔离 |
| **v1 的 bugs 在 v2 中修复** | v1 handler 冻结（只修安全漏洞），所有功能修复走 v2。v1 的 Sunset header 给出 6 个月过渡期 |
| **response 字段在新版本中改名** | v1 handler 保留旧字段名映射；v2 handler 用新字段名。service 层不变 |

### 建议优先级

**P1** — 在添加新功能（群聊、公开分享）之前建立 API 版本化策略，否则这些新功能天然就是破坏性变更。

---

## 方向四：后台异步管线可观测性框架（Async Pipeline Observability）

### 为什么需要它

aero-id 拥有 **5 条**独立的后台异步管线，它们构成了系统的"暗面"（dark side）——请求路径之外的数据流。当前每条管线有零散指标，但缺乏统一的可观测性框架来回答："所有后台处理正常吗？延迟多少？积压多深？"

**代码级证据（5 条管线现状）:**

| 管线 | 文件 | 当前观测性 | 缺口 |
|------|------|-----------|------|
| **Outbox Relay** | `internal/outbox/relay.go` | `outbox_pending` gauge, `outbox_lag_seconds` gauge, `published_total`/`failed_total` counter | 无 per-event-type 延迟分布；无 backlog 老化度（最老事件年龄）；无 handler 耗时直方图 |
| **Notification Dispatcher** | `internal/notification/dispatcher.go` | 无常驻指标（Relay 包装它，但 dispatcher 自身无 metrics） | 无法区分 dispatcher 处理延迟 vs relay 轮询延迟；无 per-notification-type 计数 |
| **Storage GC Sweeper** | `internal/storage/gc/sweeper.go` | `Run()` 返回 `Stats` 但仅通过 `zap` 日志输出，无指标 | 无 `gc_rows_purged`/`gc_blobs_deleted` counter；无 `gc_pass_duration` 直方图；无 `gc_orphans_swept` 指标 |
| **Idempotency Cleaner** | `internal/idempotency/store.go` | `RunCleaner` 无任何指标或日志 | 运维人员无法知道 cleaner 是否在运行、每次清理了多少行、耗时多久 |
| **Webhook Handler** | `internal/outbox/webhook.go` | `zap` 日志记录非 2xx | 无 `webhook_delivery_seconds` 直方图；无 `webhook_status` 按状态码计数；无 `webhook_inflight` 跟踪 |

**架构图: 未观测的异步暗面**

```
请求路径（已观测 ✅）                  异步暗面（不可见 ❌）
┌──────────────┐                    ┌──────────────────────┐
│ HTTP Handler  │──→ RED metrics    │  Outbox Relay        │
└──────────────┘                    │  ┌────────────────┐  │
┌──────────────┐                    │  │ Notification   │  │──→ no per-type latency
│ gRPC Handler  │──→ RED metrics    │  │ Dispatcher     │  │
└──────────────┘                    │  └────────────────┘  │
┌──────────────┐                    │  ┌────────────────┐  │
│ Service      │──→ AppError chain  │  │ Webhook Handler│  │──→ no delivery histogram
└──────────────┘                    │  └────────────────┘  │
┌──────────────┐                    │  ┌────────────────┐  │
│ Repository   │──→ DB query timer  │  │ GC Sweeper     │  │──→ no pass duration
└──────────────┘                    │  └────────────────┘  │
                                    │  ┌────────────────┐  │
                                    │  │ Idemp Cleaner  │  │──→ zero observability
                                    │  └────────────────┘  │
                                    └──────────────────────┘
```

### 建议方案

#### 1. 统一指标注册

为每条管线注册以下标准指标集：

```go
// metrics/async.go
type PipelineMetrics struct {
    // 积压：当前待处理数
    Backlog gauge.Int64
    // 老化度：最老待处理项的年龄（秒）
    Staleness gauge.Float64
    // 处理延迟：从入队到完成的时间
    Latency histogram.Float64
    // 吞吐：每秒处理数
    Throughput counter.Int64
    // 失败率
    ErrorRate counter.Int64
    // 本次运行耗时
    RunDuration histogram.Float64
}

// 注册所有管线
pipelines := []struct{
    Name    string
    Metrics *PipelineMetrics
}{
    {"outbox_relay", ...},
    {"notif_dispatcher", ...},
    {"gc_sweeper", ...},
    {"idem_cleaner", ...},
    {"webhook_handler", ...},
}
```

#### 2. Outbox 事件级 Trace 延续

当前 trace 在请求路径创建，但在 outbox 事件处理时丢失（后台 goroutine 没有父 span）。改为：

```go
// outbox 事件写入时携带 trace context
type Event struct {
    // ... 现有字段
    TraceParent string // W3C TraceContext header，用于延续 trace
}

// relay 处理时：
func (r *Relay) handleEvent(ctx context.Context, e *Event) {
    if e.TraceParent != "" {
        sc, _ := propagation.TraceContext.Extract(
            propagation.MapCarrier{"traceparent": e.TraceParent})
        ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
    }
    // 现在 trace 从请求到 outbox 处理是连续的
}
```

#### 3. 健康端点整合

```go
// GET /async/status — 返回所有管线的实时状态
{
  "pipelines": {
    "outbox_relay": {
      "running": true,
      "pending": 142,
      "oldest_event_age_seconds": 8.3,
      "last_success_at": "2026-07-10T10:00:00Z",
      "last_error": null
    },
    "gc_sweeper": {
      "running": true,
      "last_pass_duration_ms": 234,
      "last_pass_rows_purged": 50,
      "last_pass_at": "2026-07-10T09:55:00Z",
      "scheduled_interval": "15m0s"
    },
    "idem_cleaner": {
      "running": true,
      "last_clean_rows": 1200,
      "last_clean_at": "2026-07-10T09:30:00Z",
      "next_clean_at": "2026-07-10T10:30:00Z"
    }
  }
}
```

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **管线下线/未启动** | `status` 为 `stopped`，不视为错误（GC sweeper 默认关闭） |
| **指标高基数** | 按 pipeline name + event type 维度，控制在 20 个以内，避免 Prometheus 内存膨胀 |
| **后台管线与请求路径的 trace 关联** | 通过 `TraceParent` 字段延续 trace，SpanKind 标记为 `Internal` |

### 建议优先级

**P2** — 运维强化。在 P0/P1 产品功能完成后的加固轮实施。

---

## 方向五：供应链安全与依赖治理（Supply Chain Security）

### 为什么需要它

aero-id 的 `go.mod` 记录了 80+ 个直接和间接依赖。这些依赖来自开源社区，每个都可能引入安全漏洞、许可证合规风险或供应链攻击。当前没有任何机制来管理这些风险。

**代码级证据:**

| 位置 | 发现 |
|------|------|
| `go.mod`（根目录） | 80+ 依赖，包括 AWS SDK、GORM、OpenTelemetry、Gin、gRPC、testcontainers 等。**没有 SBOM 生成**、**没有依赖锁定策略**（go.sum 是 checksum 但不是策略） |
| `.github/workflows/ci.yml` | CI 流水线执行 lint + test + build，**无依赖审计步骤**（无 `govulncheck`、无 `nancy`、无 `trivy`） |
| `HARNESS.md` | 定义了严格的架构规则，**但依赖健康不在检查范围内** |
| `internal/pkg/migrate/migrate.go` | 虽然与依赖无关，但展示了安全盲区：这个包使用 `github.com/golang-migrate/migrate/v4`，该库历史上有过 CVE |
| `internal/storage/s3.go` | 使用 `github.com/aws/aws-sdk-go-v2` —— 与云服务商交互的依赖，密钥泄露即灾难 |

**具体未处理的风险:**

1. **无 CVE 扫描**: 不知道当前 `go.sum` 中是否有已知漏洞依赖
2. **无依赖审查门禁**: 新 PR 引入的依赖没有自动审查（是否必要？许可证合规？）
3. **无 protoc 工具版本管理**: AGENTS.md 指定了 `protoc-gen-go@v1.34.1` 和 `protoc-gen-go-grpc@v1.5.1` 的版本，但未在代码中锁定（如 `tools.go` 模式）
4. **无 SBOM 输出**: 运维人员无法生成 SBOM（Software Bill of Materials）用于合规审计或集成到漏洞管理系统
5. **无依赖版本升级策略**: 安全更新依赖手动 `go get -u`
6. **Redis 依赖残留**: `go.mod` 中有 `github.com/redis/go-redis/v9` 但代码中未使用（SCHEMA_QUERY_TEST.md 已发现此问题）——表明 go.mod 可能包含僵尸依赖

### 建议方案

#### 1. CI 流水线接入依赖安全扫描

```yaml
# .github/workflows/ci.yml 新增 job
dependency-audit:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with: { go-version: '1.26' }
    - name: Go vulnerability check
      run: go tool govulncheck ./...
    - name: Dependency license check
      run: go-licenses check ./...
    - name: Outdated dependency report
      run: go list -u -m -json all | jq 'select(.Update != null)'
```

#### 2. protoc 工具版本锁定

新增 `tools.go`（Go 的 `//go:build tools` 模式）：

```go
//go:build tools

package tools

import (
    _ "google.golang.org/protobuf/cmd/protoc-gen-go"
    _ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
)
```

这样 `go.mod` 会锁定这两个工具的版本，同时它们不会被打入生产二进制。

#### 3. SBOM 生成

```makefile
# Makefile 新增
.PHONY: sbom
sbom:
	go install github.com/anchore/syft/cmd/syft@latest
	syft dir:. --output cyclonedx-json > docs/sbom.cyclonedx.json

.PHONY: sbom-audit
sbom-audit: sbom
	syft dir:. --output cyclonedx-json | grype --fail-on critical
```

#### 4. 依赖版本健康报告

新命令 `make check-deps`：

```
# go.mod 依赖健康报告
┌────────────────────────────────┬─────────┬────────┬──────────┐
│ 依赖                             │ 当前版本 │ 最新版 │ 有漏洞?  │
├────────────────────────────────┼─────────┼────────┼──────────┤
│ github.com/gin-gonic/gin       │ v1.10.0 │ v1.10.2│ ✗ CVE-xxx│
│ github.com/redis/go-redis/v9   │ v9.5.3  │ —      │ ⚠ 未使用 │
│ google.golang.org/grpc         │ v1.70.0 │ v1.71.0│ ✅       │
│ ...                            │         │        │          │
└────────────────────────────────┴─────────┴────────┴──────────┘
```

#### 5. 僵尸依赖清理

`make tidy` 已经包含 `go mod tidy`，但 CI 应验证 `go mod tidy` 是幂等的（即仓库已是最简状态）：

```yaml
- name: Verify go.mod is tidy
  run: go mod tidy && git diff --exit-code go.mod go.sum
```

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **testcontainers-go 依赖在生产二进制中** | 已通过 `//go:build integration` 标签排除。CI 验证 `go build ./...` 不含此依赖 |
| **间接依赖透传漏洞** | `govulncheck` 遍历全部依赖树（包括间接），`go list -json -m all` 可列出所有传递依赖 |
| **许可证合规（GPL 传染性）** | `go-licenses check` 可以检测 GPL/AGPL 依赖，但需要在 CI 中配置许可证白名单 |
| **紧急 CVE 修复合入策略** | 定义 SLA：Critical CVE → 24 小时内修复并发布 patch 版本；High → 7 天 |

### 建议优先级

**P2** — 生产环境部署前的必要准备工作，但在当前阶段（功能快速迭代期）不应成为瓶颈。

---

## 综合优先级矩阵

| 方向 | 用户价值 | 技术必要性 | 实现成本 | 优先级 | 原因 |
|------|---------|-----------|---------|-------|------|
| **1. 文件存储加密** | 🔥🔥🔥🔥 | 强制（安全基线） | 低-中 | **P1** | 数据所有权的核心承诺；不加密不能说"data owned by accounts" |
| **2. 文件分享权限细化** | 🔥🔥🔥🔥 | 产品差距 | 低-中 | **P1** | 与现有文件分享功能互补；群聊/公开分享的前置条件 |
| **3. API 生命周期管理** | 🔥🔥🔥 | 架构必要性 | 中 | **P1** | 新功能（群聊、WebSocket）将引入破坏性变更；越早建立越好 |
| **4. 后台管线可观测性** | 🔥🔥 | 运维必要性 | 中 | **P2** | 对用户无直接影响，但对排障和容量规划至关重要 |
| **5. 供应链安全治理** | 🔥🔥 | 合规必要性 | 低 | **P2** | 生产上线前必须完成，但当前快速迭代期先关注功能 |

### 执行建议

- **立即启动**（1-2 周）：方向 1（文件加密 SSE-S3，2 天）+ 方向 3（API 版本化策略，3 天）。两者都是架构性决策，越早做越便宜。
- **配套开发**：方向 2（分享权限细化）与已有文件分享 API 紧密相关，建议在文件分享 API 的下一个迭代中一并改造。
- **持续集成**：方向 5（供应链安全）的 CI 门禁可以逐步加入——先加 `govulncheck`（半天），再用 SBOM 生成（半天）。
- **后续强化**：方向 4（后台管线可观测性）建议在下一个运维加固轮集中实施，与已有的 `metrics.Registry` 模式保持一致。

---

## 附录：交叉比对确认方法

为确保"真正零覆盖"，逐主题搜索了以下 16 份文档的方向章节：

| # | 文档 | 方向数 | 比对结果 |
|---|------|-------|---------|
| 1 | `expansion-directions.md` | 5 | ✅ 无重叠 |
| 2 | `architecture-extensions.md` | 5 | ✅ 无重叠 |
| 3 | `edge-cases-and-performance.md` | 5 | ✅ 无重叠 |
| 4 | `product-directions.md` | 5 | ✅ 无重叠 |
| 5 | `untapped-directions.md` | 5 | ✅ 无重叠 |
| 6 | `remaining-high-value-directions.md` | 5 | ✅ 无重叠 |
| 7 | `deep-extension-analysis.md` | 5 | ✅ 无重叠 |
| 8 | `architecture-migration-and-systemic-gaps.md` | 5 | ✅ 无重叠 |
| 9 | `REVIEW_PRODUCT_GAPS.md` | 5 | ✅ 无重叠 |
| 10 | `REVIEW_ARCHITECTURE_DEEP.md` | 5 | ✅ 无重叠 |
| 11 | `REVIEW_FAILURE_ABUSE_API.md` | 5 (directions) + 5 (failure modes) | ✅ 无重叠 |
| 12 | `REVIEW_FLOW_CONCURRENCY_CACHE.md` | 5 | ✅ 无重叠 |
| 13 | `REVIEW_INFRA_DEEP.md` | 5 | ✅ 无重叠 |
| 14 | `REVIEW_SCHEMA_QUERY_TEST.md` | 5 | ✅ 无重叠（依赖健康分析最接近，但聚焦 go.mod 结构而非供应链治理） |
| 15 | `REVIEW_SECURITY_OPS.md` | 5 | ✅ 无重叠（SLO 框架最接近，但聚焦指标而不包含 SBOM/依赖治理） |
| 16 | `ROADMAP.md` | 5 方向 + 后续补充 | ✅ 无重叠 |
| | **合计** | **~50 方向** | **本文件 5 方向全零覆盖** |

---

*本文件由全局代码仓扫描 + 16 份现有文档交叉比对驱动，无代码变更。建议作为下一轮 Sprint 规划中"未覆盖但高价值"方向的参考。*
