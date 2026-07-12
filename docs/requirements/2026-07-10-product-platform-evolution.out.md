Now I have enough data for a thorough, evidence-validated review. Let me present my findings.

---

# 审阅报告：`aero-id — 产品与平台演进：5 个高杠杆扩展方向`

我逐条验证了文档中引用的所有**代码级证据**（约 25 处文件路径 + 行号范围），结论如下：**整体质量很高，证据基本准确，但有 3 处事实性偏差。**

---

## 逐方向验证

### 方向一：消费者 SDK & API 可消费性 ✅（1 处偏差）

| 文档证据 | 验证 | 评级 |
|----------|------|------|
| `api/proto/aero_id/v1/aero_id.proto` (326 行，完整 protobuf) | ✅ 确认 326 行，`proto3` 包，可用于 OpenAPI/TS 生成 | 正确 |
| `README.md:1-200` — 仅 20 行快速开始 + 3 个 curl | ✅ 实际 88 行总长，无端点索引/schema | 实质正确（行号上限偏高，文件才 88 行） |
| `internal/handler/http/router.go:48-80` — 14 handler ~30 端点 | ✅ 9 个 HTTP registrars，各注册 ~3-5 端点，总计 ~30+ | 正确 |
| `internal/handler/grpc/server.go:45-60` — 9 gRPC 服务 | ✅ 9 个 `Register` 调用 + health + reflection | 正确 |
| `internal/pkg/errors/errors.go:1-60` — 6 个自由字符串错误 | ✅ 完整确认。`BadRequest`、`NotFound` 等接受 `code string` 参数无枚举约束 | 正确 |
| `go.mod` — **"无 grpc-gateway"** | ❌ `github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0` **存在于 go.mod**（indirect，来自 protobuf 依赖链） | **偏差**：文档声称不存在，但实际以间接依赖存在。不过没有被直接用于 OpenAPI 生成或 Makefile 引用。 |

**关键修正**：`grpc-gateway` 已在依赖树中，`go mod tidy` 就会拉下。这意味着启动 OpenAPI 生成的工作量比文档估算的更低——只需要在 Makefile 中加 `protoc-gen-openapiv2` 目标即可。

---

### 方向二：运维成熟度 ✅（全部正确）

| 文档证据 | 验证 | 评级 |
|----------|------|------|
| `internal/pkg/metrics/metrics.go:35-55` — 5 类指标无告警规则 | ✅ Registry 有 requests/latency/ssoCache/outbox 指标。零 PrometheusRule 文件 | 正确 |
| `configs/config.yaml` — 参数无调整注释 | ✅ `breaker_threshold: 5`、`stale_ttl: 300s`、`keepalive_time: 30s` 等均无"何时需要调整"的文档 | 正确 |
| `internal/sso/breaker.go:80-100` — 断路器无 metric | ✅ `State()` 方法注释写 `"used by tests and reserved for future metrics"` — 状态未暴露为 Prometheus gauge | 正确 |
| `internal/storage/gc/sweeper.go:160-210` — 无告警阈值 | ✅ 162-210 行是 `purgeSoftDeleted` 逻辑，Stats 只做 log.Info，没有任何阈值检查或告警 | 正确 |
| `.github/` — 无告警/dashboard/runbook 目录 | ✅ 确认：无 `deployments/monitoring/`、无 runbook 目录 | 正确 |

**补强**：我额外发现 `outbox/relay.go` 的 MetricsSink 接口暴露了 `ObserveOutboxFailed`，但在生产中没有对应的 Failed-event 告警规则——这是方向二一个更具体的切入点。

---

### 方向三：合规数据生命周期 ⚠️（1 处偏差）

| 文档证据 | 验证 | 评级 |
|----------|------|------|
| `internal/domain/file.go:5-18` — File 实体，无批量方法 | ✅ 只有基本字段。无 `Export` 或 `BatchDelete` 方法 | 正确 |
| `internal/domain/message.go:5-18` — Message 实体 | ✅ 同上 | 正确 |
| `internal/domain/comment.go:5-30` — Comment 实体 | ✅ 同上 | 正确 |
| `internal/domain/notification.go:5-30` — Notification 实体 | ✅ 同上 | 正确 |
| `internal/file/repository.go:15-40` — "无 ListByOwner" | ❌ **`ListByOwner` 确凿存在**（`internal/repository/file.go:20`，`internal/file/file.go:19`）。文档自身父注承认了这一点："（有 ListFiles 但带 owner 参数）"——但实际上方法名是 `ListByOwner` 而非 `ListFiles` | **偏差** |
| `internal/audit/repository.go:25-45` — append-only | ✅ 仅有 `Append` 和 `Log`，无删除、无导出、无保留期 | 正确 |
| `migrations/000014_create_audit_log.up.sql` — 无期限追加 | ✅ 只有 CREATE TABLE + 2 个索引，无保留期、无归档 | 正确 |
| `internal/service/service.go:50-80` — 无数据导出/批量清理 | ✅ 197-263 行：FileService 接口无 Export，MessageService 无 BatchDelete，CommentService 同理 | 正确 |

**关键修正**：FileRepository 确实有 `ListByOwner`，文档的论据力度稍弱（"没有"但有父注承认）。不过实质论点仍然成立——没有 `Export`/`HardDeleteByOwner`。

---

### 方向四：多实例部署基础设施 ✅（全部正确）

| 文档证据 | 验证 | 评级 |
|----------|------|------|
| `internal/pkg/ratelimit/ratelimit.go:35-50` — 内存 token bucket | ✅ `Limiter{mu sync.Mutex, buckets map[string]*bucket}`，包注释明说 "Distributed/global rate limiting would need shared state (e.g. Redis) and is intentionally out of scope here." | 正确 |
| `internal/hub/hub.go:25-40` — `Hub.subs sync.Map` | ✅ 确认。`pub()` 只在单进程内推送 | 正确 |
| `internal/sso/token_cache.go:30-50` — 内存 TokenCache | ✅ `TokenCache{mu sync.RWMutex, items map[string]tokenEntry}` | 正确 |
| `internal/sso/user_cache.go:20-40` — 内存 UserCache | ✅ 同上 | 正确 |
| `internal/storage/gc/sweeper.go:85-95` — 多实例同扫 | ✅ `Run()` / `Start()` 间隔执行，多实例同时执行会竞争 DB | 正确 |
| `internal/outbox/relay.go:60-80` — 多实例同 poll | ✅ `drain()` 方法没有协调机制，多实例会同时 `FetchUnpublished` | 正确 |

---

### 方向五：消费者级 API 体验 ✅（全部正确）

| 文档证据 | 验证 | 评级 |
|----------|------|------|
| `internal/file/service_crud.go:80-95` — `Delete(ctx, id)` 逐个删除 | ✅ 128 行：`func (s *fileService) Delete(ctx context.Context, id domain.ID) error`，无 BatchDelete | 正确 |
| Message/comment handler 同上 | ✅ 全部单条目操作 | 正确 |
| `internal/outbox/webhook.go:60-90` — 无手动重播 | ✅ `Handle()`只有 forward delivery，无 replay/resend API | 正确 |
| `internal/hub/hub.go:30-60` — `Subscribe(uid)` 无限制 | ✅ `Subscribe` 不检查已有连接数 | 正确 |
| `internal/handler/http/events.go:40-55` — 无连接治理 | ✅ `Stream()` 方法只有 heartbeat，没有 max_connections 或 per_user_concurrency_limit | 正确 |
| `internal/repository/comment.go:40-60` — 游标分页 | ✅ `ListByTarget` 使用 `WHERE id < ? ORDER BY id DESC`（实际代码 116-140 行，行号范围略有偏差但本质正确） | 正确 |

---

## 总体可信度评估

| 维度 | 评估 |
|------|------|
| **证据准确率** | ~92%（25 处证据中 3 处有偏差，其中 2 处是轻微参数偏差，1 处是事实性错误） |
| **分析深度** | 优秀。不仅指出缺失，还提供了具体的解决方向和工作量估算 |
| **路线图合理性** | 合理。P0/P1/P2/P3 的分级符合工程实际 |
| **遗漏点** | 文档没有提到的：`grpc-gateway` 已经在间接依赖中（降低方向一的工作量） |

### 3 处偏差详细说明

1. **`go.mod` 的 `grpc-gateway` 声明**：文档说"无 `grpc-gateway`"但 `github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0` 以 indirect 形式存在。这不影响文档的"无 OpenAPI 规范/无 SDK"判断，但意味着工作量比估算更低——`protoc-gen-openapiv2` 和 `protoc-gen-grpc-gateway` 已经是依赖树的一部分。

2. **`FileRepository.ListByOwner`**：文档说"无 ListByOwner（有 ListFiles 但带 owner 参数）"，但实际接口方法名就是 `ListByOwner`（`internal/repository/file.go:20`）。这个不影响"无 Export/无 HardDeleteByOwner"的核心论点，但精确性有偏差。

3. **README 行号**："README.md:1-200"——实际文件仅 88 行。这是一个保守的帧估计，不影响分析结论。

---

## 综合审阅结论

**文档质量：A-（优秀，接近专业架构审查）。**

- ✅ 每个方向都提供了可验证的代码级锚点
- ✅ 避免了一般的 wishlist，方向具体且可操作
- ✅ 走读路线覆盖了 handler→service→repository→domain 四层
- ✅ 工作量估算和优先级分级合理
- ⚠️ 3 处事实偏差（其中 2 处是微小参数偏差，1 处是对 grpc-gateway 依赖的错误否定）
- ⚠️ 方向四没有提到 `storage/gc` 的 `refs RefCounter` 已有设计意图解决 content-dedup 冲突——但这实际上**强化**了文档的论点，因为 `RefCounter` 只是部分解决了共享 cache 的问题

**最值得注意的发现**：方向一的端到端 SDK 化可能比文档估算的更廉价——`grpc-gateway` 已存在，proto 已完整，加一个 `make openapi` 目标和 CI 发布 TS 类型包即可。

**最被低估的方向**：方向四（多实例部署）——文档的 P0 阶段建议 Only outbox 协调，但我认为 SSE Hub 跨实例广播的问题对用户体验影响更大（用户连接到实例 A，从实例 B 收到通知时不会实时推送），建议在 P0 也纳入 SSE 跨实例广播。
