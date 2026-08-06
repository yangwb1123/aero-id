# aero-id — 产品与平台演进：5 个高杠杆扩展方向

> **作者视角**: 资深架构师 / 产品经理  
> **日期**: 2026-07-10  
> **方法**: 全量代码库扫描（`internal/` 全部 22 包、120+ 源文件、22 个迁移、`api/proto/`、`cmd/`、`configs/`），  
>           逐层阅读 handler → service → repository → domain → 基础设施代码。  
> **原则**: 每个方向必须有精确的代码级证据，拒绝空泛的 wishlist。  
> **无代码变更**。

---

## 当前状态（核心摘要）

| 维度 | 状态 |
|---|---|
| 核心 CRUD（文件/消息/评论） | ✅ 完整，含软删除、编辑、回复计数、两级嵌套 |
| 双传输层（HTTP REST + gRPC） | ✅ 41 RPC，完全对等，parity 测试 |
| 写路径可靠性 | ✅ 事务 + 幂等键 + 事务性 outbox + 重试/死信 |
| SSO 韧性 | ✅ 缓存 + stale-fallback + singleflight + 断路器 + mTLS/cert-pinning |
| 可观测性 | ✅ RED 指标 + OTel 跟踪 + 每依赖健康检查 + SSO/outbox 指标 |
| 文件管道 | ✅ 流式上传、MIME 校验、大小限制、内容去重、预签名直传、GC 扫表 |
| 通知系统 | ✅ Outbox 驱动、偏好设置、静默时段、去重折叠、SSE 实时推送 |
| 搜索 | ✅ PostgreSQL tsvector + GIN 索引，跨三实体 |
| 审核 | ✅ 评论举报、自隐/恢复、审计日志、角色权限 |
| 用户阻止 | ✅ 方向性阻止，check 覆盖消息和通知（**文件分享遗漏**） |
| 管理 API | ⚠️ 最小（仅审计日志查询） |
| **<u>消费者 SDK**</u> | ❌ **无 OpenAPI 规范、无 TypeScript 类型、无客户端库** |
| **<u>运维文档/playbook**</u> | ❌ **无告警规则、无 runbook、无 SLO 定义** |
| **<u>合规生命周期**</u> | ❌ **无数据导出、无被遗忘权、无租户隔离** |
| **<u>多实例部署策略**</u> | ❌ **限流器/SSE Hub/缓存全为单机内存** |
| **<u>无停机迁移策略**</u> | ❌ **迁移器单事务执行，禁止 `CREATE INDEX CONCURRENTLY`** |

---

## 方向一：消费者 SDK & API 可消费性（Developer Experience Platform）

### 为什么需要

aero-id 设计为**被其他服务消费的数据平台**——前端 SPA、移动 App、后端批处理任务都是它的消费者。然而当前交付的**只有服务器代码**，没有任何消费端的工具链：

- 无 OpenAPI 3.0 规范 → 消费者必须读完 README 或从代码反向推导每个端点的请求/响应格式
- 无 TypeScript 类型定义 → 前端开发者需要手动定义每个 DTO，易出错且难以同步
- 无 gRPC-Web 网关配置 → 浏览器客户端无法直接使用 41 个 gRPC RPC
- 无 Postman / Bruno 集合 → 开发者 onboarding 成本高
- 无客户端 SDK（Go / JS / Python）→ 每个新消费者团队都在重新"发明"调用模式

对于**依赖该服务的整个产品线**而言，API 的可消费性直接决定了集成速度、错误率和维护成本。

### 代码级证据

| 位置 | 证据 |
|------|------|
| `api/proto/aero_id/v1/aero_id.proto` | 完整的 protobuf 定义——本可以用于生成全平台 SDK，但当前仅用于生成 Go gRPC stub |
| `README.md:1-200` | 仅 20 行快速开始 + 3 个 curl 示例。无端点索引、无请求/响应 schema、无错误码目录 |
| `internal/handler/http/router.go:48-80` | 14 个 handler 注册了 ~30 个 HTTP 端点——无一有 OpenAPI 注释或文档注解 |
| `internal/handler/grpc/server.go:45-60` | 9 个 gRPC 服务注册——无一暴露 HTTP/JSON transcoding 配置 |
| `internal/pkg/errors/errors.go:1-60` | 6 个错误码构造函数，自由字符串——无机器可读的错误目录 |
| `go.mod` | 无 `grpc-gateway`、无 `protoc-gen-openapiv2`、无 `connect-go` 依赖 |

### 建议方向

1. **生成 OpenAPI 3.0 规范**：从 proto 文件通过 `protoc-gen-openapiv2` 生成 REST API 规范，建立 Swagger UI 端点。
2. **发布 TypeScript 类型包**（`@aero-id/types`）：从 proto 生成 TypeScript 定义，与 go stub 使用同一 source of truth。
3. **grpc-gateway / gRPC-Web**：为浏览器消费者启用 gRPC-Web 或 HTTP/JSON transcoding，消除"HTTP vs gRPC"的认知成本。
4. **错误目录标准化**：按端点编目所有可能的 `error.code` 值，输出 `error-catalog.yaml`，让消费者可以程序化处理每个错误。

### 估算影响

| 项目 | 影响 | 工作量 |
|------|------|--------|
| OpenAPI 规范（proto→swagger） | 高（可见性、工具化） | 低（已有 proto，加一项目标） |
| TypeScript 类型包 | 高（前端集成速度） | 低（自动化生成） |
| 错误目录 | 中（开发者体验） | 低-中（需要审核每个端点） |

---

## 方向二：运维成熟度 — 告警、Runbook 与 SLO

### 为什么需要

当前代码库在**技术可观测性**上投入巨大（RED 指标、OTel 跟踪、SSO 健康仪表盘、outbox 延迟仪表盘），但**运维可操作性**为零：

- 无 Prometheus 告警规则 → `aeroid_outbox_lag_seconds` 飙升时无人知晓
- 无 SLO/SLI 定义 → 无法回答"服务健康吗？"
- 无 runbook → SSO 断路器打开时操作员该做什么？
- 无容量指南 → 需要多少内存/连接来支持 1000 RPS？
- 无混沌测试 → 断路器从未在生产中被验证

一个**被完美 instrument 但无人值守**的服务，在生产中并不比一个无 instrumentation 的服务更可靠——因为告警才是人类干预的触发点。

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/pkg/metrics/metrics.go:35-55` | 10 个直方图 bucket、5 类指标——但**零告警规则配套**（PrometheusRule 或 Grafana dashboard 无定义） |
| `configs/config.yaml` | 所有生产调优参数（`breaker_threshold: 5`、`stale_ttl: 300s`、`keepalive_time: 30s`）——但**无注释说明何时需要调整这些值** |
| `internal/sso/breaker.go:80-100` | 断路器状态机（Closed → Open → Half-Open）——完全无 metric 暴露断路器状态，操作员不知它是 open 还是 closed |
| `internal/storage/gc/sweeper.go:160-210` | GC sweeper 间隔 1h——但**无告警阈**（If `stats.OrphansSwept > 100` 持续多轮意味着什么问题？） |
| `.github/` | 持续集成存在，但**无告警配置目录、无 dashboard JSON、无 runbook 目录** |

### 建议方向

1. **SLO 框架**：定义 3 个核心 SLI——API 可用性（5xx 率 < 0.1%）、API 延迟（P99 < 500ms）、outbox 延迟（lag < 60s）。为每个 SLI 配置告警规则。
2. **Grafana dashboard**：导出 dashboard JSON 到 `deployments/monitoring/`，涵盖服务概览、SSO 健康、outbox、存储 GC。
3. **Runbook 目录**：`docs/runbooks/` 下最少 5 个 runbook——SSO 不可用、断路器打开、outbox 堆积、存储 GC 异常、数据库连接池耗尽。
4. **断路器状态暴露**：在 `/metrics` 上暴露 `aeroid_sso_breaker_state`（0=closed, 1=open, 2=half-open）gauge。

### 估算影响

| 项目 | 影响 | 工作量 |
|------|------|--------|
| SLO 框架 + 告警规则 | 高（避免无声故障） | 低（YAML 配置） |
| Grafana dashboard | 高（运维可见性） | 中 |
| Runbook 目录 | 高（MTTR 降低） | 低（文档编写） |
| 断路器状态 metric | 中 | 低（3 行代码） |

---

## 方向三：合规数据生命周期管理（Data Compliance & Governance）

### 为什么需要

aero-id 是一个**用户内容存储服务**——它持有用户的文件、私信、评论。在当前监管环境（GDPR、CCPA、中国《个人信息保护法》）下，这类服务必须提供：

1. **数据导出**：用户有权索取其全部数据（GDPR 第 20 条）
2. **被遗忘权**：用户有权要求删除其全部数据（GDPR 第 17 条）
3. **数据保留策略**：按类别/时间自动清除不再需要的数据
4. **数据分类**：区分"用户主动上传的数据"（文件）和"衍生数据"（审核日志）

当前实现**零覆盖**这些需求。虽然技术上可以通过 SQL 手动完成，但这不是可扩展的、可审计的、或符合合规要求的做法。

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/domain/file.go:5-18` | File 实体——`owner_uid` 是关键关联键。**无"按 uid 批量导出/删除的 Service 方法"** |
| `internal/domain/message.go:5-18` | Message 实体——`from_uid` 和 `to_uid` 都是 uid。删除用户需要清理其发送和接收的全部消息 |
| `internal/domain/comment.go:5-30` | Comment 实体——`owner_uid`。同样无批量操作 |
| `internal/domain/notification.go:5-30` | Notification——`recipient_uid` 和 `actor_uid`。被遗忘权需要清理涉及该 uid 的全部通知 |
| `internal/file/repository.go:15-40` | FileRepository 接口——无 `ListByOwner`（有 `ListFiles` 但带 owner 参数），无 `HardDeleteByOwner` |
| `internal/audit/repository.go:25-45` | 审计日志——按设计无法删除（append-only 用于合规）。但被遗忘权与之冲突 |
| `migrations/000014_create_audit_log.up.sql` | 审计日志是无期限的追加存储——**合规上需要配置保留期** |
| `internal/service/service.go:50-80` | FileService/MessageService/CommentService 接口——**无一提供数据导出或批量清理功能** |

### 建议方向

1. **数据导出 API**（`GET /v1/data-export`）：异步任务，将用户所有文件（元数据 + 下载链接）、消息、评论打包为 JSON/CSV，通过通知通道通知用户下载。
2. **被遗忘权 API**（`DELETE /v1/user-data?uid=...`）：管理员端点，触发全量数据删除——硬删除（超出 GC retention window）所有关联行、撤回文件分享、清除存储文件。记录到审计日志。
3. **数据保留策略**：在配置中增加 `data_retention` 段，为每个实体指定保留期，后台协程定期清理超期数据。
4. **审计日志归档**：追加 audit_events 的归档策略——超过配置保留期的记录转储到冷存储（S3 Glacier / 归档）。

### 实施注意事项

- 被遗忘权操作与**现有内容去重**冲突：多人引用的相同 checksum 文件不能立即删掉物理 blob。解决方案是保留 refcount，只在 count → 0 时删除。
- 审计日志的"不可篡改性"与"被遗忘权"在法律上是矛盾需求——通常审计日志可以匿名化（抹去 uid 但保留事件行）而非删除。

### 估算影响

| 项目 | 影响 | 工作量 |
|------|------|--------|
| 数据导出 API | 高（合规差异化能力） | 中 |
| 被遗忘权实现 | 高（法律合规必须） | 高（跨所有实体的协调清理） |
| 数据保留配置 | 中（成本控制） | 中-高 |
| 审计日志归档 | 中（合规） | 中 |

---

## 方向四：多实例部署基础设施（Horizontal Scaling Foundation）

### 为什么需要

aero-id 的多个核心组件是**严格的单机实现**——这意味着它**无法水平扩展**。当单实例达到 CPU/内存/连接瓶颈时，增加实例不仅没有帮助，反而会因为：

- 每个实例有自己的**速率限制器**（token bucket 在内存中），用户可以在 N 个实例间以 N 倍速率请求
- 每个实例有自己的**SSE Hub**（连接在 `sync.Map` 中），推送通知只到达连接所在的那个实例
- 每个实例有自己的**SSO 缓存**（`TokenCache`/`UserCache`），缓存命中率随实例数线性下降

对于计划服务**数千并发用户**的生产部署，这是路线图上的关键瓶颈。

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/pkg/ratelimit/ratelimit.go:35-50` | `Limiter{mu sync.Mutex, buckets map[string]*bucket}`——**严格内存、单机、无共享状态**。文件注释也声明：*"Distributed/global rate limiting would need shared state (e.g. Redis) and is intentionally out of scope here."* |
| `internal/hub/hub.go:25-40` | `Hub.subs sync.Map`——**SSE 连接只在当前进程中可见**。用户连接到实例 A 后，由实例 B 触发的通知无法推送给该用户 |
| `internal/sso/token_cache.go:30-50` | `TokenCache{mu sync.RWMutex, entries map[string]*cacheEntry}`——每个实例独立缓存 SSO token。放大 SSO 负载 |
| `internal/sso/user_cache.go:20-40` | `UserCache`——同上 |
| `internal/storage/gc/sweeper.go:85-95` | GC sweeper 按间隔运行：**多实例时所有实例同时扫表**→ DB 负载尖峰 |
| `internal/outbox/relay.go:60-80` | Outbox relay：**多实例同时 poll 同一批事件**→ 竞争 published_at 更新，可能的重复通知 |

### 建议方向

1. **协调速率限制**：引入 Redis 作为 token bucket 后端（sliding window log 或 GCRA 算法）。保持内存 limiter 作为 fallback（无 Redis 时仍可用）。
2. **SSE Hub 跨实例广播**：通过 Redis Pub/Sub 实现跨实例通知广播。实例收到 outbox 事件后，**除了本地推送外** publish 到 Redis channel；其他实例 subscribe 并推送给本地连接的该用户。
3. **共享 SSO 缓存**：使用 Redis 共享 TokenCache（TTL-based，无需强一致）。减少 SSO 负载，提高多实例下的缓存命中率。
4. **协调 Outbox Relay**：Leader election（基于 PostgreSQL `pg_advisory_lock` 或 Redis 锁），确保仅一个实例执行 outbox drain。减少 DB 竞争和重复通知风险。

### 架构决策说明

这些改动不需要一次性全部实现：

```
阶段 1（即刻价值）：
  Redis 共享缓存（SSO TokenCache）→ 减少 SSO 负载
  数据库协调锁（Postgres advisory lock）→ outbox relay 单实例

阶段 2（高价值）：
  Redis 速率限制 → 全局一致的限流
  Redis Pub/Sub SSE Bridge → 实时推送跨实例

阶段 3（弹性）：
  Connection pooling / 连接迁移策略
  实例自注册 / 发现
```

### 估算影响

| 项目 | 影响 | 工作量 |
|------|------|--------|
| Redis 共享 SSO 缓存 | 高（生产必需） | 中 |
| Outbox relay 协调 | 高（避免重复） | 低（pg_advisory_lock） |
| 分布式速率限制 | 中（安全加固） | 中 |
| 跨实例 SSE 广播 | 高（UX 关键） | 中-高 |

---

## 方向五：消费者级 API 体验 — 批量操作、Webhook 重试管理、SSE 长连接治理

### 为什么需要

当前的 API 设计是**面向"每次一个操作"**的 RPC 风格。这对于后端服务集成够用，但对于真实的前端应用（列表管理、批量操作、实时同步）存在显著体验缺口：

| 缺口 | 场景 | 影响 |
|------|------|------|
| **无批量操作** | 用户要删除 50 个文件 → 需要 50 个 HTTP 请求 | 延迟 + 并发控制问题 |
| **Webhook 无管理界面** | 接收端挂了 → 事件堆积到死信，无法手动重播 | 数据丢失风险 |
| **SSE 无连接管理** | 一个用户开 5 个标签页 → 5 个 SSE 连接 | 资源浪费 |
| **无分页一致性保证** | 列表页翻到第 3 页时数据变化 → 游标分页产生重复/遗漏 | UX 问题 |
| **无条件请求**（ETag） | 列表每次拉取全量数据 | 带宽浪费 |

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/file/service_crud.go:80-95` | `Delete(ctx, id)`——**逐个删除**，无 `BatchDelete(ctx, ids []domain.ID)` |
| `internal/message/handler_http.go:50-65` | 同上 |
| `internal/comment/handler_http.go:40-55` | 同上 |
| `internal/outbox/webhook.go:60-90` | Webhook 发送失败→event 进入死信→**无手动重播 API** |
| `internal/hub/hub.go:30-60` | `Subscribe(uid)`——**无最大连接数限制**，无连接复用（同一 uid 的多个标签页各自独立连接） |
| `internal/handler/http/events.go:40-55` | SSE handler——**无心跳之外的连接治理**（无 `max_connections`、无 `per_user_concurrency_limit`） |
| `internal/repository/comment.go:40-60` | ListComments 使用 `WHERE id < ? ORDER BY id DESC`——**在数据频繁增删的场景下，游标分页不保证结果集的稳定性** |

### 建议方向

1. **批量操作端点**：
   - `POST /v1/files/batch-delete`，`POST /v1/messages/batch-delete`
   - 输入：`{ "ids": ["uuid1", "uuid2", ...] }`，上限 100
   - 在单个事务中执行，所有或全不（all-or-nothing）

2. **Webhook 管理面**：
   - `GET /v1/admin/webhook/events?status=dead_letter&page=...` 查看死信事件
   - `POST /v1/admin/webhook/events/:id/replay` 重播单个事件
   - `POST /v1/admin/webhook/events/replay-batch?event_type=...` 按类型批量重播

3. **SSE 连接治理**：
   - 每个 uid 最大并发 SSE 连接数（配置项，默认 3）
   - 超出时关闭最旧的连接（LRU）或拒绝新连接
   - Hub 暴露 `ActiveConnections(uid)` 接口

4. **ETag / If-None-Match 支持**：
   - 列表端点返回 `ETag: <hash-of-content>` header
   - 客户端带 `If-None-Match` 时返回 `304 Not Modified`
   - 减少重复数据传输

5. **快照分页（Snapshot Pagination）**：
   - 提供 `?snapshot_at=<timestamp>` 参数，分页基于该时间点的数据快照
   - 避免翻页过程中数据变化导致的结果偏移

### 估算影响

| 项目 | 影响 | 工作量 |
|------|------|--------|
| 批量操作 | 高（产品体验） | 低-中 |
| Webhook 管理面 | 中（可靠性） | 中 |
| SSE 连接治理 | 中（资源控制） | 低 |
| ETag 支持 | 中（性能） | 低 |
| 快照分页 | 低（复杂实现+罕见需要） | 高（需要可重复读隔离级别或 MVCC 快照） |

---

## 综合路线图建议

```
优先级       方向                                      时间线
──────────────────────────────────────────────────────────────────
P0        方向二（运维成熟度）：告警规则 + 断路器 metric     1-2 周
P0        方向四（多实例 foundation）：outbox 协调           1 周
P1        方向三（合规）：数据导出 API                       2-3 周
P1        方向一（SDK）：OpenAPI 规范 + TS types             1 周（自动化）
P2        方向四（多实例扩展）：Redis 共享缓存 + SSE          4-6 周
P2        方向五（消费者体验）：批量操作 + Webhook 管理       3-4 周
P3        方向三（合规）：被遗忘权 + 数据保留策略             6-8 周
```

所有方向均遵循现有的**严格分层架构**——改动集中在 service 层新增接口，repository 层新增查询，handler 层新增路由，不破坏既有依赖关系。
