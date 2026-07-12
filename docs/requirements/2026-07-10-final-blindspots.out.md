我现在已经对代码库和该文档进行了彻底的审查。让我提供一份全面的审查报告。

---

## 对"最终盲区"文档的审查

### ✅ 总体评估

这份文档质量高、研究方法严谨。交叉分析方法论是合理的——从 16 份现有文档中提取方向标题，然后反向查找零覆盖区域，这是定位遗漏主题的可靠方法。代码级证据在大部分方向上都有很好的支持。

### 🔍 已验证的代码级声明

| 方向 | 代码声明 | 验证结果 |
|-------|--------|---------|
| **1. 加密** | `local.go` 直接写入无加密 | ✅ `Put()` 用 `os.Create` + `io.Copy`，无加密 |
| | `s3.go` 未设置 `ServerSideEncryption` | ✅ `PutObjectInput` 仅有 `Bucket/Key/Body` |
| | `storage.go` 接口无加密 | ✅ `FileStorage` 接口无加密提及 |
| | 配置/config 无加密字段 | ✅ 已验证 |
| **2. 分享权限** | `Grant()` 无过期/权限参数 | ✅ 仅有 `(fileID, ownerUID, granteeUIDs)` |
| | `IsSharedWith()` 返回 bool | ✅ 二元 |
| | 迁移 SQL 无 `expires_at` 等 | ✅ 只有 `file_id, grantee_uid, owner_uid, created_at` |
| **3. API 版本化** | 路由固定在 `/v1` | ✅ `router.go`：`e.Group("/v1", authMW...)` |
| | 单一 proto 文件 | ✅ `api/proto/aero_id/v1/aero_id.proto` |
| | 单一 `RegisterAeroIDServiceServer` | ✅ `server.go` 无版本路由 |
| **4. 可观测性** | Dispatcher 无常驻指标 | ✅ 无计数器、直方图或仪表 |
| | GC sweeper 只记录日志，无指标 | ✅ `Stats` 仅通过 `log.Info()` 输出 |
| | Idempotency cleaner 无指标 | ✅ `RunCleaner` 无指标，仅有 `log.Warn` |
| | Webhook 无 `webhook_delivery_seconds` 直方图 | ✅ 只有日志中的 `zap.Int("status", resp.StatusCode)` |

### ⚠️ 已发现的事实错误

发现了 **3 个事实错误**，应予以修正：

**1. Redis 依赖声明（第 485 行）**

> `go.mod` 中有 `github.com/redis/go-redis/v9` 但代码中未使用

**事实**: 当前 `go.mod` 中**没有** redis 依赖。它被 `REVIEW_SCHEMA_QUERY_TEST.md`（2026-06-30 分析）发现，但此后已被清理。`go mod tidy` 已经移除了它。该声明已过时——该问题已修复，此处不应引用。

**2. 依赖计数被低估（第 477 行附近）**

> aero-id 的 `go.mod` 记录了 80+ 个直接和间接依赖

**事实**: 直接依赖有 **23 个**（go.mod 第一个 `require` 块）。`go list -m all` 总共列出 **340 个**模块（直接 + 间接）。说"80+"低估了约 4 倍——虽然"80+直接"是高估，"80+总模块"是低估。

**3. `migrate.go` 依赖声明（第 490 行）**

> `internal/pkg/migrate/migrate.go` — 该包使用 `github.com/golang-migrate/migrate/v4`，该库历史上曾出现过 CVE

**事实**: 阅读 `migrate.go`（第 1-10 行）——注释为："Package migrate is a tiny, dependency-free SQL migration runner"。它是**零外部依赖**的自定义实现。不使用 `golang-migrate` 库。此引用具有误导性。

### 🧩 覆盖声明澄清

该文档声称 **"零覆盖"** ，这一说法在严格定义下是合理的（作为专门的方向部分分析）。然而，作为透明度说明：

- 加密在 3 份文档的表格中被提及（作为"安全/合规"类别下的非分析条目）
- 供应链安全在 `systemic-correctness-and-platform-gaps.md` 中单独出现
- `API 版本化/兼容性` 在 `code-scan-architectural-extensions.md` 中简要讨论（关于签名版本化）

这些是**提及**而非分析。文档的结论"没有文档将其中任何一个作为独立方向进行完整的代码证据链论证"是准确的。

### 💡 对分析的改进建议

| 领域 | 建议 |
|-------|--------|
| **加密** | 考虑提及 `PresignUpload` 路径——对于 presigned URL，客户端直接上传到 S3，因此服务器端加密需要 S3 存储桶策略（`x-amz-server-side-encryption`），而不仅仅是 `PutObjectInput` |
| **分享权限** | 注意现有的 `file_shares` 表在 `(file_id, grantee_uid)` 上有一个主键——在列上添加 `ON CONFLICT` 处理，这意味着迁移添加新约束可能需要通过 `DROP ... CASCADE` 重建或额外的迁移步骤 |
| **API 版本化** | 值得考虑 gRPC 的反射 API——当前的 proto 没有 `package` 版本化约定，但 gRPC 服务器注册了 `reflection_enabled`（配置中的可选）。启用反射会暴露当前未版本化的完整 RPC 表面 |
| **可观测性** | Outbox `MetricsSink` 接口已存在（`relay.go` 第 38-52 行）——这是一个在 relay 级别有可插入指标的现有钩子，因此具有良好架构。应更新分析以指出这**已经就位**，但 dispatcher/sweeper 尚未实现其底层指标注册表方法 |
| **供应链** | 修正 Redis 和 migrate.go 事实错误。注意 go.mod 使用 Go 1.26 的自动 `go mod tidy`，它通过设计防止僵尸依赖 |

### 📊 优先级再评估

文档的优先级矩阵基本合理。我想补充的：

1. **方向 1（加密）P1** ✅ ——然而，请注意：对于 local 存储，信封加密是合理但复杂的（密钥管理、轮换）。对于 S3/MinIO（预期生产后端），**SSE-S3 需要零应用层变更**——S3 存储桶策略可以通过 `bucket.s3:PutObject` 上的 `aws:SourceArn` 强制执行。这是一个 30 分钟的配置更改。第一轮仅使用 SSE-S3 将使影响/成本比极高。

2. **方向 3（API 版本化）P1** ——我同意。然而，aero-id 使用 gRPC，它**天生具有字段存在 + 未知字段保留的能力**。这与 REST 不同。proto 破坏性变更（字段重命名、类型更改）是罕见的。主要风险是端点退役。在 gRPC 中，版本化通常通过协议缓冲区中的 `Package` 声明处理（`aero_id.v2`），而不是通过 HTTP URL 路径。

3. **方向 2（分享权限）P1** ——对这个优先级排序评论：如果群聊和公开分享是路线图项目，那么首先建立精细权限**是**正确的。否则，该方向可以推迟到初始生产发布之后。

### 📝 杂项观察

- **文件大小**：该文档有 623 行，超过了 AGENTS.md 中每个文件 ≤500 行的约束。应将其分割，或如果这是分析文档而非代码，则放宽限制。
- **原型工具版本**：文档提到了 `protoc-gen-go@v1.34.1` 和 `protoc-gen-go-grpc@v1.5.1`。然而，`go.mod` 显示 `google.golang.org/protobuf v1.36.11` 和 `google.golang.org/grpc v1.80.0`。工具版本应与其生成的运行时版本匹配——值得验证。
- **Outbox trace 延续**：设计建议（第 398 行，提议 `TraceParent` 字段）在并发模型下需要仔细考虑——outbox 事件由后台轮询创建，并且可能在一个 goroutine 中处理多个事件，每个都有不同的父跟踪。W3C TraceContext 传播在这种情况下可能过于复杂；一个具有 span 链接的简单 `context.Context` 就足够了。

### 🔚 总结

这是一份强有力的分析文档，准确识别了代码库中的 5 个真实缺口。事实错误（Redis、migrate.go、依赖计数）降低了文档的可信度，但并未推翻其核心论点。在生成最终版本之前，应修复这三个错误，并且该文档将成为 sprint 规划的极好参考。
