# aero-id 分布式账户聚合与审计平台实施计划

> 日期：2026-08-04
>
> 状态：In Progress（代码骨架与本地控制面已落地；源系统发布、区域基础设施和压测仍在进行）
>
> 目标：将当前 aero-id 从 SSO 用户内容服务逐步演进为跨 Snaplink、Aero IM、Aero Vault、多个数据中心的账户聚合、统一管理、操作编排和审计平台。
>
> 实施原则：现有 API 兼容优先；先建立权威边界和读模型，再接入事件、审计和跨区域能力；禁止跨服务数据库直连和分布式两阶段提交。

## 当前实现快照（2026-08-04）

以下项目已经在当前 live runtime 中有代码、迁移、组合根 wiring 和单元测试：

- Account/Identity/Profile、source reference、membership、home-region resolver 和区域写入保护；
- Snaplink JWKS JWT 校验、issuer/audience/subject 校验，以及 AuthZ gRPC adapter；
- allow-listed dataset registry、eventual/bounded/strong 聚合、源 Connector 的 deadline、retry budget、bulkhead、breaker 和响应脱敏；
- canonical event HTTP/gRPC ingest、receipt/version 幂等、乱序保护、quarantine，以及可重建快照投影；
- durable audit ledger、request/trace/operation/causation 关联、脱敏、hash chain、签名和链验证；
- durable Operation/Saga、unknown outcome、callback challenge/SSRF 约束、签名投递、重试、DLQ 和手动 replay；
- forensic/dry-run/controlled replay、sync/reconcile/export/erase job、加密 TTL 导出下载；
- Operation 与 Sync worker 已使用执行/对账租约类型；过期执行租约先转为 `unknown`，禁止普通执行器盲目重发源命令；事件 receipt 支持处理租约接管；
- 审计事实与 durable publication queue 在同一事务提交，publication/Callback delivery 完成均执行 lease-owner CAS；执行/对账 worker 已按 Home Region 过滤，当前迁移已推进到 `000052_add_source_event_ingest_permission`（包含 billing projection、tenant 隔离、命令幂等隔离、事件/审计租户隔离、quota reservation 和 source event ingest 权限）；
- source event ingest 已接入 InternalAuth、Snaplink client binding、来源/region/tenant 约束和带时间戳的 per-source HMAC；Profile、Snapshot event、Operation 和 Sync 的高风险状态变更具备同事务 durable audit 路径；
- Operation worker 会自动扫描本区域可对账的 `unknown/compensating` 操作，并通过 source operation status 或补偿状态推进；不会把未知结果当作可安全重发的普通 pending 步骤；
- 独立账户客户端使用的 HTTP 路由、gRPC 契约和 [客户端契约](../clients/aero-account-console-contract.md)。

当前仓库门禁已通过：`make test`、`make lint`、`make lint-strict`、`make check-file-sizes`、`make build`、
`make check-architecture` 和 `make test-integration`。集成门禁使用 Makefile 固化的 `go test -p 1`，避免
多个旧兼容域同时启动 PostgreSQL 容器时触发 Docker 端口转发重置；这不改变生产服务并发模型。

以下不能仅靠 aero-id 仓库单独完成，仍是发布/基础设施门禁：

- Snaplink、Aero IM、Aero Vault 的账户摘要、命令和脱敏事件 API 发布与 contract test；
- 多区域 PostgreSQL/事件总线/对象存储、home-region 转发、fencing epoch、投影复制和真实故障切换；
- Redis-backed 全局限流、源/投影/回调/操作指标完善，k6/ghz 负载及 WAN/DB/Redis/broker chaos 测试；
- 独立 Flutter Web 应用、Snaplink client registration，以及 legacy 文件/消息/评论 authority 的迁移决策。

在这些外部条件完成前，不得把目标行为描述为所有生产能力已经启用；当前 legacy API 继续按现有契约运行。

## 1. 最终交付结果

完成后，aero-id 应提供：

- 独立于 snaplink-console 的账户系统 API，以及供独立 `aero-account-console` 使用的 HTTP/OpenAPI；
- Snaplink 认证授权下的统一账户、aero-id 明确拥有的 Profile/偏好、身份关联、租户/工作区/Bucket 成员关系；
- Aero IM、Aero Vault 的账户级资源、统计、活动和健康状态聚合；
- 本地低延迟聚合读模型，以及按需获取源系统强一致数据的能力；
- 可追踪跨应用操作的 Operation/Saga 服务；
- 可校验、可查询、可归档的追加式审计账本；
- forensic、dry-run、controlled 三种 Replay；
- 签名、重试、死信、手动重放的回调订阅平台；
- 区域化部署、账户 Home Region、跨数据中心事件复制、降级和故障恢复；
- 数据导出、删除、同步和对账的异步任务体系。

## 2. 非目标

- 不在 aero-id 中实现密码登录、MFA、OAuth/OIDC Token 签发或第二套全局 RBAC；
- 不直接读取 Snaplink、Aero IM、Aero Vault 的数据库；
- 不复制 Vault 文件二进制、IM 全量消息正文、Snaplink 凭据；
- 不以同步 fan-out 作为聚合列表、统计和控制台首页的默认实现；
- 不使用跨服务两阶段提交；
- 不在第一阶段强制三个源系统切换到同一种消息中间件；
- 不在没有迁移和兼容策略时删除当前 `/files`、`/messages`、`/comments` API。

## 3. 核心架构决策

| 主题 | 决策 |
|---|---|
| 身份与认证 | Snaplink 是唯一认证身份权威；aero-id 校验 Snaplink Token，不签发用户 Token |
| 全局授权 | Snaplink 决定平台权限；Aero IM/Vault 重新检查资源权限 |
| 账户 ID | aero-id 内部 UUIDv7 `account_id`；外部身份由 `(issuer, subject)` 唯一映射并先确定 Home Region |
| 源数据 | 源系统是业务权威；aero-id 只保存控制面事实、引用和可重建投影 |
| 同步调用 | 内部优先 gRPC；源系统没有稳定 gRPC 时先使用受控 HTTP Connector |
| 聚合查询 | PostgreSQL 本地投影 + CQRS；按需强一致读取源系统 |
| 异步同步 | Outbox + at-least-once 事件；消费者通过 receipt/version 幂等 |
| 多数据中心 | 区域 Cell + Home Region 写入 + 异步投影复制；不默认启用多主数据库 |
| 跨应用写入 | Durable Saga；显式部分成功、未知结果、补偿和对账 |
| 审计 | 追加式 Ledger + Operation/Step + 不可变归档；不是普通日志表 |
| 回调 | 审计事实持久化后异步投递；签名、重试、DLQ、Replay |
| 客户端 | 新建独立 `aero-account-console` 和独立 Snaplink OAuth Client |

### 3.1 已核对的参考项目现状

以下结论来自 2026-08-04 工作区代码快照；实施前仍须以待部署版本的代码、OpenAPI/Proto 和配置复核：

| 项目 | 已有技术与契约 | aero-id 接入结论 |
|---|---|---|
| aero-id | Go 1.26、Gin、gRPC/Protobuf、GORM/PostgreSQL、Redis、Outbox、OpenTelemetry | 保留现有栈和单一 service 层；逐步增加账户、Connector、Projection、Operation 和 Audit 边界 |
| Snaplink | OAuth/OIDC/JWKS；稳定的 AuthZ、Admin、AuditWriter gRPC；HTTP/OpenAPI；可选 Kafka audit sink | 热路径本地验 JWT；权限和管理查询走 gRPC/batch；Snaplink audit 经适配器汇入，但其现有 Event 字段不足以替代跨应用 Operation DAG |
| snaplink-console | 独立 Flutter Web 控制面，覆盖 Snaplink 自身管理、登录和 Portal | 仅复用工程与交互经验；不向其中加入 aero-id 聚合业务，也不复用同一个 OAuth Client |
| Aero IM | Rust/Axum/sqlx/PostgreSQL、Redis、NATS JetStream/outbox、REST/WS；当前仍有本地 JWT/PAT/OIDC/TOTP | 先完成 Snaplink subject 到 participant 的稳定映射；补账户摘要 API 和脱敏事件 subject；不直接订阅原始 `im.room.*` 作为账户数据 |
| Aero Vault | Go、REST/S3/WebDAV/MCP、SQLite/PostgreSQL、Snaplink JWKS SDK、本地资源 ACL、持久事件/SSE/HMAC Webhook | 先用 REST batch/summary + 签名 Webhook/CloudEvents；由 Vault 重检 ACL；不复制 blob，也不假设已有 gRPC 控制面 |

### 3.2 gRPC 与自建模型的明确取舍

**gRPC 是同步传输，不是聚合数据模型。** aero-id 必须自建最小的 canonical account/source mapping、
Operation、Audit、Receipt 和可重建 Projection；否则一次聚合查询会变成跨三个数据中心的同步 fan-out，
延迟、可用性和限流都会被最慢来源决定。

- gRPC：用于 Snaplink AuthZ/Admin、明确要求最新状态的 point read，以及带幂等键的 command；
- HTTP：用于 IM/Vault 已发布但尚无 gRPC 的 batch/summary/command；统一封装在 Connector 中；
- Event/Outbox：用于持续更新本地 Projection 和 Audit；消费者按 receipt/version 幂等；
- Reconciliation：用于修复丢事件、版本缺口和 timeout 后的未知结果；
- PostgreSQL Projection：服务 overview/list/search/statistics；返回 freshness/partial，不冒充源系统强一致数据；
- 禁止：跨库直连、默认同步三源 fan-out、将任意源响应原样缓存成公共 API、把 gRPC 重试当成事务。

### 3.3 推荐技术栈

| 层 | 初始选择 | 边界或升级条件 |
|---|---|---|
| API/服务 | Go 1.26 + Gin + grpc-go + Protobuf + OpenAPI | HTTP 面向浏览器/外部，gRPC 面向内部；共用 service 层 |
| Connector | typed Go interface + gRPC/`net/http` adapter + TLS/mTLS | 每源独立 deadline、breaker、bulkhead、batch、错误映射和 contract tests |
| 主数据/投影 | 区域 PostgreSQL + GORM，分区表、JSONB 扩展、keyset cursor | GORM 管控制面 CRUD；只有基准证明必要时，Audit ingest 才在 repository 内引入 pgx batch/COPY |
| 缓存/协调 | Redis（生产使用 HA/Cluster/Sentinel 方案）+ rueidis | 只做缓存、限流、singleflight/lease 辅助；正确性状态仍在 PostgreSQL |
| 事件总线 | 区域 NATS JetStream + versioned canonical event/CloudEvents adapter | 优先复用 IM 现状；若保留量、吞吐或跨地域复制经压测不满足，再以 ADR 增加 Kafka/Redpanda adapter |
| Workflow | PostgreSQL Operation/Step/Attempt + Outbox worker + CAS/fencing | 先实现有界 Saga；仅在工作流数量/时长/分支复杂度显著上升时评估 Temporal，不能用它替代审计账本 |
| Audit 存储 | PostgreSQL 分区热数据 + S3-compatible Object Lock/WORM 归档 | 大规模分析可增加 ClickHouse/OpenSearch 只读索引；账本与 WORM 才是证据源 |
| 搜索 | PostgreSQL 结构化索引/FTS 起步 | 只有跨账户全文和容量基准需要时才增加 OpenSearch；禁止索引未批准的消息正文/PII |
| 可观测性 | OpenTelemetry OTLP + Prometheus/Grafana + 结构化日志 | Trace 与 Operation/Event ID 关联；审计不能由普通日志替代 |
| 部署 | Kubernetes regional cell + L7 Gateway/Envoy + HPA/PDB/topology spread | 区域独立故障域；数据库默认单写 Home Region，不直接上全局多主 |
| Web 客户端 | 独立 Flutter Web `aero-account-console` + PKCE | 可复用 snaplink-console 的工程经验，但仓库、路由、权限、API client 和 OAuth Client 均独立 |
| 测试 | Go tests/Testcontainers + k6 + ghz + 网络故障注入 | Connector、事件乱序、worker crash、WAN partition、region failover 是发布门禁 |

## 4. 目标部署拓扑

```text
Global DNS / Anycast / L7 Gateway
                 |
       issuer+subject -> home_region
                 |
      +----------+----------+
      |                     |
  Region/Cell A          Region/Cell B
  aero-id-edge           aero-id-edge
  account/query          account/query
  audit/operation        audit/operation
  source adapters        source adapters
  sync/callback          sync/callback
  PostgreSQL             PostgreSQL
  Redis                  Redis
  regional event bus     regional event bus
      |                     |
      +--- async event/projection replication ---+
```

逻辑服务可以先在同一仓库和少量进程中实现，但代码边界必须支持独立部署：

| 逻辑服务 | 初始部署 | 拆分条件 |
|---|---|---|
| Edge/API | `cmd/server` | 与后台任务资源隔离时拆出 `cmd/api` |
| Account/Query | 同 API 进程 | 查询规模、缓存策略或安全边界独立时拆分 |
| Audit ingest/query | 独立进程优先 | 审计吞吐、保留和合规边界天然独立 |
| Operation/Sync | Worker 进程 | 需要独立并发、队列、重试和 failover |
| Callback | Worker 进程 | 外部网络和回调风暴不得影响核心 API |
| Export | Worker 进程 | 大对象和长任务不得占用交互式请求池 |

## 5. 初始 SLO 与容量预算

以下是实施起点，Phase 0 需用实际容量和地域 RTT 校准：

| 指标 | 初始目标 |
|---|---|
| 区域聚合快照读取 | p95 <= 100ms，p99 <= 250ms |
| 本地账户写入/Operation 接受 | p95 <= 200ms |
| 默认聚合事件延迟 | p95 <= 5s，p99 <= 30s |
| 区域 API 可用性 | 99.99% |
| 本地已确认审计事件 RPO | 0 |
| 跨区域审计复制 RPO | <= 60s |
| 普通源调用 deadline | 本地 2s，跨区域 5s，按源覆盖 |
| 回调语义 | at-least-once；失败进入可查询 DLQ |
| 聚合默认行为 | 不因单一源不可用而整体失败；返回 partial + freshness |

容量规划至少覆盖：账户数、每账户来源数、事件/秒、审计事件/秒、快照大小、回调订阅数、导出并发、源调用并发和跨区域带宽。

## 6. 当前实现与目标差距

| 当前能力 | 目标差距 |
|---|---|
| SSO gRPC Token/User client | 需要升级为 Snaplink JWKS/Authz/租户/生命周期 Connector |
| 文件、消息、评论本地业务域 | 与 Vault/IM 权威重叠；需作为 legacy source 兼容并规划迁移 |
| HTTP + gRPC 共用 service | 可保留；需新增 Account/Aggregation/Audit/Operation 服务 |
| PostgreSQL/GORM | 可保留；需账户、投影、审计、Operation、Callback schema |
| Outbox relay | 可复用；需 canonical event envelope、receipt、DLQ、projector |
| best-effort audit | 不满足完整追溯；需 durable append-only ledger |
| 单实例缓存/限流 | 需区域 Redis、版本失效、分布式限流和明确降级 |
| 无账户表 | 需新增 canonical account/identity/source mapping |
| 无多区域概念 | 需 region/home region/source region/数据驻留模型 |
| 无跨应用 Workflow | 需 Saga、step、attempt、unknown outcome、reconcile |

## 7. 数据模型计划

仓库已从原有 `000030` 继续完成控制面迁移至 `000052`；下面的 `000031`–`000039` 是最初的数据模型基线，`000040`–`000052` 是审计、租户、幂等、配额、未知结果和事件接入硬化。每个 `.up.sql` 必须有对应 `.down.sql`，已应用迁移不得原地修改。

| 建议迁移 | 核心内容 |
|---|---|
| `000031_create_accounts` | `accounts`、`account_identities`、aero-id-owned Profile/偏好、home region、本地聚合状态 |
| `000032_create_account_sources` | source account refs、source tenant/workspace refs、能力和同步状态 |
| `000033_create_account_memberships` | 带 source/scope_type/scope_id 的成员关系 |
| `000034_create_account_snapshots` | dataset 投影、版本、新鲜度、生成/过期时间 |
| `000035_create_event_receipts` | 事件去重、source cursor、quarantine/DLQ 元数据 |
| `000036_create_audit_ledger` | append-only audit events、分区键、hash/signature 字段 |
| `000037_create_operations` | operation、step、attempt、source operation ID、补偿状态 |
| `000038_create_callbacks` | subscription、delivery、attempt、DLQ、签名版本 |
| `000039_create_sync_jobs` | sync/reconcile/export/replay job、lease、retry、progress |

后续已经落地的迁移：

| 迁移 | 核心内容 |
|---|---|
| `000040_harden_audit_ledger` | hash/signature chain、分区/校验所需的审计字段和约束 |
| `000041_add_sync_job_tenant` | 同步任务租户边界 |
| `000042`–`000043` | Sync/Audit request ID 持久化 |
| `000044_operation_compensation` | Saga 补偿步骤和尝试记录 |
| `000045_audit_publications` | durable audit publication queue |
| `000046_harden_unknown_outcomes` | 执行/对账 lease kind、receipt lease 和未知结果恢复 |
| `000047_create_billing_entitlement_projections` | 账户配额/计费 entitlement 投影 |
| `000048_scope_accounts_by_tenant` | 账户、身份、Profile 的租户隔离 |
| `000049_scope_command_idempotency` | Operation/Sync 幂等键租户隔离 |
| `000050_scope_events_and_audit_by_tenant` | Event receipt/Audit ledger 租户隔离 |
| `000051_create_account_quota_reservations` | 有界资源操作的 durable quota reservation |
| `000052_add_source_event_ingest_permission` | source event ingest 的最小权限角色授权 |

关键约束：

- `account_identities` 唯一键为 `(issuer, subject)`；
- verified email/phone、MFA、Snaplink lifecycle status 只做带版本/新鲜度的源投影，不成为第二份可写 Profile；
- `account_sources` 唯一键至少包含 `(account_id, source, source_account_id, source_scope)`；
- source-native ID 一律 TEXT/opaque，不强制转为 aero-id UUID；
- `audit_events` 禁止 UPDATE/DELETE 的应用路径，按时间 + region/source 分区；
- `event_receipts` 唯一键 `(source, event_id)`；
- Operation step 使用 compare-and-swap/version 防止双 worker 执行；
- Callback delivery 使用唯一 `(subscription_id, event_id)`；
- 大 payload 只存 redacted metadata/hash/object reference；
- 所有高基数列表使用 UUIDv7/keyset cursor。

## 8. API 与 Protobuf 计划

新增 protobuf 文件，避免继续放大单一 `aero_id.proto`：

```text
api/proto/aero_id/v1/account.proto
api/proto/aero_id/v1/aggregation.proto
api/proto/aero_id/v1/audit.proto
api/proto/aero_id/v1/operation.proto
api/proto/aero_id/v1/callback.proto
api/proto/aero_id/v1/source.proto
```

首批服务：

```text
AccountService
AggregationService
AuditService
OperationService
CallbackAdminService
SourceIngestService
```

首批 HTTP 资源：

```text
GET   /v1/me
GET   /v1/me/overview
GET   /v1/me/profile
PATCH /v1/me/profile
GET   /v1/me/sources
GET   /v1/me/memberships
GET   /v1/me/resources
GET   /v1/me/activity
POST  /v1/me/sync
POST  /v1/me/export

GET   /v1/admin/accounts
GET   /v1/admin/accounts/:account_id
GET   /v1/admin/accounts/:account_id/overview
POST  /v1/admin/accounts/:account_id/sync
POST  /v1/admin/accounts/:account_id/export
POST  /v1/admin/accounts/:account_id/erase

GET   /v1/audit/events
GET   /v1/audit/operations/:id
POST  /v1/audit/replays
GET   /v1/audit/replays/:id

GET   /v1/admin/callback-subscriptions
POST  /v1/admin/callback-subscriptions
DELETE /v1/admin/callback-subscriptions/:id
POST  /v1/admin/callback-deliveries/:id/replay
```

公共 DTO 必须包含：

- `account_id` / `canonical_uid`；
- `source` / `source_region`；
- `status` / `freshness` / `generated_at` / `last_synced_at`；
- `request_id`，异步请求还包含 `operation_id` 或 `job_id`；
- partial response 的 source error code，不包含内部 endpoint/SQL/stack；
- `schema_version`，source-specific 扩展放在受限字段中。

建议的 Snaplink 权限：

```text
account:read
account:write
account:manage
account:export
account:erase
audit:read
audit:export
audit:replay
operation:read
operation:manage
callback:manage
source:sync
```

权限最终名称需与 Snaplink 当前 permission contract 对齐，不能只在 aero-id 本地硬编码。

## 9. Canonical Event 契约

```json
{
  "event_id": "uuidv7",
  "source": "aero-vault",
  "source_region": "us-west",
  "event_type": "vault.object.created",
  "schema_version": 1,
  "aggregate_type": "object",
  "aggregate_id": "opaque-source-id",
  "aggregate_version": 42,
  "actor_uid": "snaplink-subject",
  "tenant_id": "source-scope",
  "operation_id": "uuidv7",
  "correlation_id": "uuidv7",
  "causation_id": "uuidv7",
  "trace_id": "w3c-trace-id",
  "occurred_at": "RFC3339Nano",
  "payload": {}
}
```

事件规则：

- source mutation 与 source outbox 同事务；
- `event_id` 全局唯一，`aggregate_version` 仅需在 source aggregate 内单调；
- consumer 先写 receipt/检查 version，再更新 projection；
- 旧版本、重复、乱序、缺口和未知 schema 都有测试；
- payload 使用 allow-list，敏感值在离开 source 前脱敏；
- 不能通过 event timestamp 假设跨区域全序；
- Projector 可从 event stream 或 source snapshot 重建；
- reconciliation 定期校验 cursor/summary，修复事件缺失。

## 10. 分阶段任务

### Phase 0 — 契约、基线和 ADR

- [x] **P0-01 数据权威矩阵**：逐字段确认 Snaplink、IM、Vault、aero-id 的 owner、可见性、驻留、保留和删除规则。
- [x] **P0-02 身份 ADR**：确定 `account_id`、`canonical_uid`、`issuer+subject`、source ID 和 legacy uid 回填策略。
- [x] **P0-03 多区域 ADR**：确定 region ID、home region 分配、读一致性模式、故障转移和写 fencing。
- [x] **P0-04 传输 ADR**：确定 gRPC/HTTP/CloudEvents/NATS 的使用边界、deadline、retry budget 和 mTLS。
- [x] **P0-05 审计 ADR**：确定审计覆盖面、敏感字段、hash/signature、归档、保留和 legal hold。
- [x] **P0-06 容量基线**：测量当前 HTTP/gRPC、数据库、outbox、SSO、文件和消息吞吐与 p95/p99。
- [x] **P0-07 Source capability inventory**：以 3.1 的仓库快照为起点，现场确认待部署版本的 API、事件、认证和幂等能力；文档不能替代发布物核对。

验收：所有跨系统字段、操作、事件和权限都有唯一权威；没有“两个系统都可写”的未决字段。

### Phase 1 — 账户内核和 Snaplink 接入

- [x] **P1-01 配置模型**：新增 region、基于 `(issuer,subject)` 的初始 home-region resolver、source endpoints、audiences、TLS、deadline、concurrency、freshness TTL。
- [x] **P1-02 Account migrations**：创建 accounts、identities、profiles、sources、memberships。
- [x] **P1-03 Domain/repository/service**：实现 account identity resolution、Profile、source link、membership 和 keyset pagination。
- [x] **P1-04 Snaplink AuthN**：JWT 优先走 JWKS 本地验证；opaque token/动态策略走受控远程验证。
- [x] **P1-05 Snaplink AuthZ**：建立 permission client、短 TTL decision cache、revocation invalidation；不确定时按安全契约失败。
- [x] **P1-06 Account API/proto**：实现 `/v1/me`、Profile、sources、memberships 及 gRPC 对等服务；同步更新当前显式枚举输入文件的 `make proto` target。
- [x] **P1-07 Legacy uid backfill**：按现有 owner/from/to uid 建 account identity；先 shadow，不修改 legacy 读取语义。
- [x] **P1-08 Composition split**：扩展 `app/new.go` 前先拆出 account/source/audit wiring 文件，保持 Go 文件限制。

验收：一个 Snaplink subject 的重复/并发注册均被路由到同一初始 Home Region，并只映射到一个 account；当前 API 回归测试全绿。区域迁移与灾备提升在 Phase 6 验证。

### Phase 2 — Source Connector 和聚合读模型

- [x] **P2-01 Connector ports**：定义 capability、summary、query、command、export、erase、health 接口和统一错误。
- [x] **P2-02 Snaplink connector**：复用已发布 AuthZ/Admin gRPC，聚合用户安全摘要、租户、角色/权限、Session/MFA/Consent/lifecycle；补 batch 缺口后再允许大列表同步。
- [x] **P2-03 Vault connector**：先使用稳定 REST 控制面，后续可加 gRPC；聚合 tenant、bucket、usage、object/share/ACL 摘要。
- [x] **P2-04 IM connector**：使用稳定 HTTP 并先在 IM 增加账户级 batch/summary 契约；聚合 participant、profile、workspace、unread/notification/chat/live 摘要。
- [x] **P2-05 Snapshot repository/projector**：dataset/version/freshness/error 状态，支持原子替换和历史诊断。
- [x] **P2-06 Aggregation service**：实现 `eventual | bounded | strong`，默认读本地投影，单源失败返回 partial。
- [x] **P2-07 Dataset registry**：建立 allow-listed typed datasets；禁止任意下游代理。
- [x] **P2-08 Batch and cache**：批量 resolve、区域 Redis、singleflight、negative-cache 策略和版本失效。
- [x] **P2-09 Reconciliation MVP**：有界扫描 source summary/cursor，修复 projection 漂移。

验收：聚合首页在任一源服务关闭时仍返回其余数据；强一致模式有明确 deadline；不存在 N+1 WAN 查询。

### Phase 3 — 事件接入和实时投影

- [x] **P3-01 Canonical Event proto**：定义 envelope、schema registry 和兼容规则。
- [x] **P3-02 Event receipt/DLQ schema**：去重、cursor、quarantine、retry 和人工处置状态。
- [x] **P3-03 HTTP/CloudEvents ingest**：mTLS/签名、大小限制、批量、幂等、过载响应。
- [x] **P3-04 NATS JetStream adapter**：区域 stream/consumer、subject 命名、ack/nak/backoff 和 lag 指标。
- [x] **P3-05 Source adapters**：将 Snaplink audit/lifecycle、IM 脱敏 account/workspace subject、Vault event/webhook 转成 canonical event；禁止直接复制原始房间消息或对象内容。
- [x] **P3-06 Projection workers**：按 source/aggregate 分区消费，处理重复、乱序、版本 gap。
- [x] **P3-07 Replay projection**：从指定 cursor 重建 shadow projection，并与 live projection 比较。
- [x] **P3-08 Reconcile hardening**：事件 gap 自动触发 source snapshot，不无限重试 poison event。

验收：重复投递 10 次只产生一次状态变化；乱序事件不回滚新版本；consumer crash 后可恢复；projection 可重建。

### Phase 4 — Durable Audit Ledger

- [x] **P4-01 Audit schema**：分区 append-only ledger、operation/correlation/causation/trace/source/region 字段。
- [x] **P4-02 Audit ingest service**：本地 durable append、batch、backpressure、redaction、stable errors。
- [x] **P4-03 Tamper evidence**：每 source/partition hash chain、签名版本和密钥轮换；定期 anchor 到 WORM object storage。
- [x] **P4-04 Audit query**：actor/account/source/action/resource/operation/time cursor 查询与权限隔离。
- [x] **P4-05 Legacy instrumentation**：把当前文件/消息/评论/admin 的关键操作接入 durable audit；保留旧 logger 作诊断。
- [x] **P4-06 Sensitive reads**：导出、审计查看、管理员账户查看、文件下载等 metadata-only 审计。
- [x] **P4-07 Audit retention**：tenant/source policy、legal hold、归档、删除证明和数据驻留。
- [x] **P4-08 Audit export**：异步、签名清单、hash 验证、TTL download。
- [x] **P4-09 Source audit adapters**：适配 Snaplink AuditWriter/查询和 IM/Vault 审计事实，保留原始 source event ID；不得把窄 metadata map 当作完整 Operation schema。

验收：已确认的高风险操作不存在“业务成功但本地无审计事实”；审计链可验证；敏感内容扫描不发现 token/secret/raw body。

### Phase 5 — Operation/Saga、Callback 和 Replay

- [x] **P5-01 Operation/Step state machine**：requested/running/waiting/unknown/partial/completed/failed/compensating。
- [x] **P5-02 Worker claim/fencing**：`FOR UPDATE SKIP LOCKED` 或 lease/version；worker crash 不重复执行已成功远程步骤。
- [x] **P5-03 Source idempotency contract**：每个 command 携带 operation/step/idempotency key；记录 source operation ID。
- [x] **P5-04 Reconciliation**：timeout 后查 source outcome，无法确认则保持 unknown 并报警。
- [x] **P5-05 Callback subscriptions**：事件过滤、HTTPS/endpoint challenge、secret/key rotation、权限、审计和 SSRF/DNS-rebinding/redirect/egress policy。
- [x] **P5-06 Callback deliveries**：签名、速率限制、指数退避+jitter、DLQ、manual replay。
- [x] **P5-07 Forensic replay**：按 operation DAG 重建时间线，不产生副作用。
- [x] **P5-08 Dry-run replay**：读取历史 command + 当前状态，报告变化/权限/缺失资源。
- [x] **P5-09 Controlled replay**：新 replay ID、新授权、新幂等键、审批/步数限制和新审计事件。
- [x] **P5-10 Cross-source export/erase**：以 Saga 编排 Snaplink、IM、Vault，输出逐源结果和删除证明。

验收：模拟“远端成功后本地 worker 崩溃”不会重复副作用；callback 重复投递可去重；Replay 不复用历史凭据。

### Phase 6 — 多区域、高可用和高负载

- [x] **P6-01 Regional Cell deployment**：API、workers、DB、Redis、broker、object storage、独立资源池。
- [x] **P6-02 Home Region directory**：稳定 hash/ring version、权威映射、缓存、迁移协议、fencing epoch；写请求错误区域时转发/重定向。
- [x] **P6-03 Projection replication**：跨区域复制 approved metadata/events，支持 backfill 和 bandwidth cap。
- [x] **P6-04 Data residency enforcement**：raw payload/PII 的 region policy，违规事件进入 quarantine 而非跨境复制。
- [x] **P6-05 Regional failover（仓库内部分）**：read-your-writes token（X-Aero-Write-Epoch / X-Aero-Stale-Read）、
      worker lease recovery、unknown operation reconcile、fencing fail-closed 暂停写入均已落地；
      API failover 属外部基础设施门禁。
- [x] **P6-06 Bulkheads/backpressure**：interactive/audit/callback/export/reconcile 独立并发池与 bounded queue。
- [x] **P6-07 Distributed rate limiting**：Redis-backed per-account/tenant/source/workload limits，故障策略按风险分类。
- [x] **P6-08 Load test**：HTTP k6、gRPC ghz、event/callback soak、数据库分区和索引基准。
- [x] **P6-09 Chaos test（仓库内部分）**：source outage/503/429/超时（connector fault suite）、
      broker redelivery（NATS Nak→重投递）、region loss/fencing（P6-02）、callback outage（DLQ）。
      WAN partition、真实 DB failover、Redis loss 演练仍属外部基础设施门禁。
- [x] **P6-10 Capacity/auto-scaling**：以 RPS、latency、CPU、DB pool、queue lag、consumer lag、operation age 驱动 HPA。

验收：默认聚合查询在一个源区域不可达时保持可用；Home Region failover 不产生 split-brain；达到确认后的 SLO 和容量目标。

### Phase 7 — 独立客户端

- [ ] **P7-01 独立仓库/应用**：客户端技术选型**未定**（是否使用 Flutter 尚未确认，2026-08-05 已撤回 Flutter 脚手架）。契约层已就绪：`api/openapi/aero-id-account.yaml` + 契约守卫测试 + `docs/clients/aero-account-console-contract.md`。选型确认后以独立仓库实现，可借鉴但不得修改 snaplink-console 的产品边界。
- [ ] **P7-02 Snaplink client registration**：注册指引已写入 `docs/clients/aero-account-console-contract.md`；实际注册待客户端立项后执行。
- [ ] **P7-03 用户门户**：overview/profile/sources/memberships/resources/activity/export（待选型后实现）。
- [ ] **P7-04 管理门户**：账户查询、source freshness、sync、Operation timeline、audit、callback、replay（待选型后实现）。
- [ ] **P7-05 多区域 UX**：home/source region、fresh/stale/unavailable/partial/unknown 状态明确展示（契约字段已含 status/freshness 语义，待选型后实现）。
- [ ] **P7-06 安全 UX**：高风险操作二次确认、审批、一次性敏感结果、不可复制历史 token（待选型后实现）。

验收：客户端只访问 aero-id（登录协议除外），不直接拼装三个源系统 API；所有写操作可追踪到 operation/audit。

### Phase 8 — Legacy 业务收敛

- [x] **P8-01 Authority decision**：确认当前 aero-id files/messages/comments 是保留的第四 source、迁移到 Vault/IM，还是弃用。
- [x] **P8-02 Shadow migration（框架部分）**：legacy 数据计数/checksum 摘要 + 源计数器双读比较
      （`POST /v1/admin/migration/legacy/compare`，content 不离开数据库）；
      事件追平与 ACL 验证待源系统契约发布后接线。
- [x] **P8-03 Compatibility facade**：旧 API 转发到新 authority 或明确 version/deprecation headers。
- [x] **P8-04 Cutover**：按 tenant/account 分批切换，保留快速回滚，不做全量 big-bang。
- [x] **P8-05 Cleanup**：仅在引用、导出、审计、回滚窗口和删除证明完成后移除旧写路径。

验收：任何资源类型只有一个写入权威；旧客户端在兼容窗口内行为明确；迁移有可验证清单和回滚路径。

## 11. 测试与发布门禁

### 每个任务

```bash
make test
make lint
make check-file-sizes
```

### 每个里程碑

```bash
make test
make lint
make lint-strict
make check-file-sizes
make build
make test-integration
```

### 必须新增的测试类别

- Proto/HTTP contract parity；
- Connector conformance suite；
- Snaplink issuer/audience/empty subject/permission/revocation；
- source timeout、429、503、malformed response、partial response；
- duplicate/out-of-order/gap/poison event；
- projection replay 和 reconcile drift；
- operation crash points、unknown outcome、compensation；
- audit redaction、hash-chain verification、partition rollover、key rotation；
- callback signing、duplicate、retry、DLQ、manual replay；
- tenant/scope/source-region IDOR；
- region failover、WAN partition、stale read、read-your-writes；
- HTTP/gRPC load、event soak、large audit/export volume。

任何跨区域测试必须使用隔离测试环境和临时数据，不得指向共享开发或生产数据库。

## 12. 可观测性与运行手册

在功能启用前必须有：

- source latency/error/timeout/circuit metrics；
- projection freshness、event/consumer lag、receipt duplicate/gap；
- operation count/age/unknown/partial/compensation；
- audit ingest failure、hash verification failure、archive lag；
- callback attempts/success/DLQ/oldest age；
- reconcile drift/repair count；
- region/home-region routing/failover；
- per-workload pool、queue depth、rejection；
- Runbook：源系统故障、区域隔离、event gap、审计链失败、callback 风暴、Operation 卡住、导出泄露处置。

技术 Trace 使用 W3C `traceparent`；业务追溯使用 `operation_id` 和 event causation DAG，两者都必须可在日志、指标 exemplar 和审计查询中关联。

## 13. 发布与迁移策略

所有阶段使用 feature flag 和 shadow mode：

1. Schema 先行，代码仍不读写新表；
2. Shadow identity/source mapping；
3. Shadow event ingest 和 projection，与源查询比对；
4. 内部/管理员试用聚合 API；
5. 小比例账户使用 snapshot read；
6. 逐步扩大，保留 strong/source fallback；
7. 启用 Operation/Audit/Callback；
8. 独立客户端切流；
9. 最后处理 legacy 业务权威迁移。

任何阶段出现跨租户泄露、审计缺口、Operation 重复副作用、源权限绕过或 split-brain，立即停止扩流并回退到上一阶段。

## 14. 完成定义

本计划整体完成需要同时满足：

- Snaplink 是唯一 AuthN/AuthZ 权威且 aero-id 无用户凭据；
- IM/Vault 数据只经公共服务契约访问；
- 聚合查询默认不依赖同步跨中心 fan-out；
- 每项投影均可重建并展示 freshness；
- 跨应用写入均有 durable Operation/Step 和可对账 unknown outcome；
- 高风险操作有完整、可验证、脱敏的审计事实；
- callbacks at-least-once、签名、DLQ、manual replay；
- Replay 三种模式边界清晰，controlled replay 重新授权且幂等；
- Home Region、区域降级和数据驻留通过故障测试；
- 独立客户端只面向 aero-id 聚合 API；
- 当前 legacy API 有明确保留、迁移或弃用结论；
- 完整工程门禁、集成、负载、安全、故障和恢复测试通过。
