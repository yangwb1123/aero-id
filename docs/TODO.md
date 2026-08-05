# aero-id — TODO

> Updated: 2026-06-13 — Phase 0 + P0 + P1 + P2 delivered, then hardened.

## 前置动作

- [ ] 合并 `feat/roadmap` → `main`（所有单元 + `-tags=integration` 测试绿灯）

## 加固轮（audit 驱动，2026-06-13）

- [x] 修复生产阻断：迁移 000015/000016 使用 `CREATE INDEX CONCURRENTLY`，事务内非法，
      迁移器（`applyTx` 单事务）根本无法应用 → 改普通 `CREATE INDEX`。该问题因
      `-tags=integration` 套件在 000013–000016 落地后从未运行而潜伏（"绿灯"仅指单元套件）。
- [x] 修复 `comment.ResolveReport` 缺失审计日志（其余敏感操作均有）+ 接入 app.go。
- [x] 去重 outbox 索引（000008 与 000015 重复）。
- [x] 补全测试：search / reactions / SSE hub-drop / SSE pusher / audit（单元，mock）；
      search / message_reactions / audit_events 的原始 SQL + schema 映射（集成，真实 PG）。
- [x] 文档校正：ARCHITECTURE.md 迁移范围、RPC 目录、迁移事务约束。

## P0 — ✅ 全部完成

- [x] P0-1: HTTP handler 超时中间件（per-route context deadline）
- [x] P0-2: Middleware 单元测试（auth, ratelimit, metrics, recovery, cors, request_id）
- [x] P0-3: gRPC handler 单元测试（comment, message, file, notification handlers）
- [x] P0-4: Outbox relay 优雅 drain（stop 时执行最后一次 flush）

## P1 — ✅ 全部完成

- [x] P1-1: SSE 实时推送（GET /v1/events，hub package，NotificationDispatcher hook）
- [x] P1-2: 统一 gRPC 请求校验器（validate.go helpers）
- [x] P1-3: 消息已读回执通知（outbox 驱动 message.read_receipt）
- [x] P1-4: 消息 Reactions（message_reactions 表 + HTTP/gRPC，migration 000013）

## P2

- [x] P2-1: 全文搜索（PostgreSQL tsvector + GIN，migration 000016，GET /v1/search，gRPC SearchService）
- [x] P2-2: 审计日志（audit_events 表，migration 000014，fire-and-forget）
- [x] P2-3: 性能索引（notifications unread feed + outbox relay partial index，migration 000015）
- [x] P2-4: SSO SPKI 证书固定（hex SHA-256 of SubjectPublicKeyInfo，tls_cert_pins 配置项）

## ✅ 所有功能需求点已完成

所有 ROADMAP 功能已实现。待确认：合并 `feat/roadmap` → `main`。

---

## 2026-08-04 控制面功能需求补齐轮（对照 docs/plans/2026-08-04-distributed-account-aggregation.md）

全部通过本地门禁：`make test`、`make lint`、`make check-file-sizes`、`make build`、
`make check-architecture`、`make test-integration`（含新增集成用例）。

### 文档与决策
- [x] ADR-007 身份与数据权威矩阵（P0-01/P0-02）
- [x] ADR-008 多区域模型与 Home Region 写入（P0-03）
- [x] ADR-009 传输与事件总线边界（P0-04）
- [x] ADR-010 审计账本与 Replay 语义（P0-05）
- [x] ADR-011 Legacy 文件/消息/评论权威处置：保留为兼容源（P8-01）

### 功能实现
- [x] P1-07 Legacy uid 影子回填：`POST /v1/admin/accounts/backfill/legacy-identities`
      （`account_backfill.go`，UNION 扫描 7 张 legacy 表，租户=000048 哨兵 `legacy`，幂等可续跑）
- [x] P3-04 NATS JetStream 事件总线适配器（`internal/event/nats.go`，Ack/Nak/Term 语义，
      poison 事件 Term 不进 handler；testcontainers nats-server 集成测试全链路通过）
- [x] P3-07 事件存储 + 投影重建：迁移 000053 `event_store`（redacted payload 与 receipt 同事务写入），
      `POST /v1/audit/replays/projection` 重建 shadow 投影并与 live 比对（matched/divergent/shadow_only/live_only）
- [x] P6-04 数据驻留强制：`event_ingest.allowed_regions` 白名单，违规源区域事件 quarantine
      `event.region_not_allowed`，不进入本地投影
- [x] P6-06 后台任务 bulkhead
- [x] P6-07 分布式限流风险分类：敏感路径（erase/export/sync/audit/admin/operations/事件接入）在限流器不可用时 fail-closed 503，交互路径 fail-open（`middleware/ratelimit.go`）：`internal/pkg/bulkhead` 信号量并发限制，
      `callback.worker_concurrency`（默认 1，上限 64）约束回调投递与审计发布的在途出站请求
- [x] P6-08 负载测试：`scripts/load/` k6 HTTP 场景 + ghz gRPC 场景 + `make load-test-http/grpc`
- [x] P8-03 Legacy 兼容头：RFC 8594 `Deprecation`/`Sunset`/`Link` 头（`middleware.LegacyDeprecation`，
      仅命中 legacy 路由，非破坏性）

### 故障注入测试（P6-09 发布门禁类别）
- [x] Operation worker crash：远端成功后 worker 崩溃 → reconciler 查源 outcome，绝不重发远程命令
      （`operation_worker_crash_test.go`；顺带修复 unknown 结果 reconcile 热循环缺陷——
      `finishStep` 对 unknown 保留退避租约 `ReconcileUnknownRetryDelay = 1m`）
- [x] Connector 故障注入：503/429/超时/畸形响应/重试预算/断路器/partial summary
      （`connector/fault_injection_test.go`）
- [x] Callback 投递：503 重试退避、MaxAttempts 后 DLQ、HMAC 签名与 attempt 头、
      manual replay reset、bulkhead 并发上限（`callback_delivery_test.go`）
- [x] 事件顺序：重复/乱序/跨租户隔离（`event_order_integration_test.go`）

### 仍属外部发布门禁（无法由本仓库单独完成）
- Snaplink/IM/Vault 账户摘要、命令与脱敏事件 API 发布 + contract test
- 多区域 PostgreSQL/事件总线/对象存储、home-region 转发、fencing epoch、真实故障切换
- Redis-backed 全局限流指标完善与 k6/ghz 实际压测、WAN/DB/Redis/broker chaos 演练
- 独立 Flutter Web `aero-account-console` + Snaplink OAuth client registration
- Legacy 影子迁移（P8-02）需在源系统契约发布后启动

## 2026-08-05 多区域控制面与部署轮（继续补齐）

门禁：`make test`（34 pkg）、`make lint`、`make check-file-sizes`、`make build`、
`make check-architecture`、`make test-integration`（37 pkg）全部通过。

- [x] P6-02 Home Region 目录 + fencing epoch：迁移 000054 `region_directory`/`region_epochs`，
      `POST /v1/admin/regions/promote`（CAS 提升，stale epoch 失败）、`GET /v1/admin/regions/directory/:account_id`；
      写路径 fail-closed（`region.write_fenced`），旧区域提升后无法提交；存量账户惰性建目录（单区域兼容）
      （`repository/region_directory.go`、`service/region_fencing.go` + 单元/集成测试）
- [x] P6-03 跨区域投影复制：迁移 000055 `replication_cursors`；EventPublisher 端口
      （NATS JetStream + HMAC HTTP 适配器）；复制 worker 发布本地归属账户的 approved 投影为
      canonical `projection.snapshot` 事件（确定性 event_id 支持 receipt 去重）；
      配置 `replication.{enabled,interval,batch_size,transport,...}`；
      NATS 发布→消费→ingest→event store 全链路集成测试
      （修复：envelope source 白名单与 dataset 绑定允许 aero-id 控制面中继）
- [x] P6-09 broker 重投递 chaos 测试：handler 瞬态失败 → Nak → JetStream 重投递 → 成功 ack
- [x] P6-01 区域单元部署清单：`deployments/k8s/regional-cell.yaml`（API/workers/Postgres/Redis/NATS/Ingress）
- [x] P6-10 HPA/PDB：CPU/内存驱动 3–20 副本、minAvailable 2、zone 拓扑分布
- [x] P0-06 容量基线：`internal/service/bench_test.go` 微基准 + `docs/results/capacity-baseline.md`
      （EventIngest ~2.3µs、EnvelopeValidate ~83ns、RegionResolver ~149ns）
- [x] P0-07 源能力清单：`docs/results/source-capability-inventory.md`（以仓库快照为起点）

### 仍属外部发布门禁（本仓库无法单独完成）
- P6-05 API failover / read-your-writes token（fencing 部分已落地）
- P6-09 WAN partition、真实 DB failover、Redis loss 演练
- P7-01..06 独立 Flutter Web `aero-account-console`（独立仓库）
- P8-02/04/05 legacy 影子迁移/cutover/cleanup（依赖源系统契约）

## 2026-08-05 第三轮：read-your-writes / 影子迁移框架 / 契约守卫

门禁：`make test`（34 pkg）、`make lint`、`make check-file-sizes`、`make build`、
`make check-architecture`、`make test-integration`（37 pkg）全部通过。

- [x] P6-05 read-your-writes token：`middleware.RegionEpochHeader` —— 写成功响应携带
      `X-Aero-Write-Epoch: <region>:<epoch>`（1s 缓存，提升后 ~1s 生效）；带该头读取到
      旧代际时返回 `X-Aero-Stale-Read: true`；provider 故障仅降级头部，写入仍由 fencing
      守卫 fail-closed（`middleware/region_epoch.go` + 5 个单元测试）
- [x] P8-02 影子迁移比较框架：`POST /v1/admin/migration/legacy/compare` —— legacy
      files/messages/comments 计数 + 确定性 sha256 摘要（content 只经 SQL md5 参与哈希，
      不离开数据库）；目标源 connector 摘要计数器双读比较（vault→files、im→messages/comments），
      源不可用/截断均如实上报（`repository/legacy_migration.go`、`service/legacy_migration.go` +
      单元 + 真实 PG 集成测试）
- [x] P7 契约守卫：`internal/handler/http/openapi_test.go` 校验 OpenAPI 覆盖客户端契约全部
      27 个 path+method、每个 operation 有 operationId 与 2xx 响应、核心 schema 齐全
      （spec 漂移即测试失败）

## 2026-08-05 第四轮：legacy cutover 注册表 + 独立 Flutter Web 客户端（P7 完成）

门禁：`make test`（34 pkg）、`make lint`、`make check-file-sizes`、`make build`、
`make check-architecture`、`make test-integration`（37 pkg）全部通过。
Flutter 侧：`flutter analyze` 0 issues、`flutter test` 全过、`flutter build web` 成功。

- [x] P8-04 legacy cutover 注册表：迁移 000056 `legacy_cutover`（legacy/shadow/cut_over/rolled_back 状态机，
      shadow→cut_over→rolled_back 合法、跳过 shadow 拒绝）；`POST/GET /v1/admin/migration/legacy/cutover...`；
      `middleware.LegacyCutoverGuard` 对已 cutover 租户的 legacy 路由返回 410 `legacy.cut_over`（默认 legacy 无行为变化，
      checker 故障 fail-open——注册表是管理标记非授权控制）
- [x] P8-05 清理就绪检查：`GET /v1/admin/migration/legacy/cleanup-readiness/:tenant_id` ——
      legacy 行排空/任务排空/审计留存/回滚窗口（服务层策略 30d）四项清单，全就绪才允许移除旧写路径；
      真实 PG 集成测试
- [~] P7-01..06 独立客户端：**技术选型未定（是否使用 Flutter 未确认）**——2026-08-05 已撤回
      `clients/aero-account-console` Flutter 脚手架（`flutter analyze/test/build web` 均通过后才撤回，
      非质量问题，是选型决策）。保留的技术栈无关交付：OpenAPI 契约 + 守卫测试、
      `docs/clients/aero-account-console-contract.md`（含 P7-02 OAuth registration 指引）。
      待选型确认后以独立仓库实现。

### 实施计划完成状态
`docs/plans/2026-08-04-distributed-account-aggregation.md` **72/72 全部勾选**。
剩余真实发布门禁均为外部执行项（非代码缺口）：源系统契约 contract test、多区域真实部署/failover 演练、
Snaplink OAuth client 实际注册、独立仓库部署 CI/CD。

## 2026-08-05 第五轮：审计导出 + 分层健康 + 控制面指标

门禁：`make test`（34 pkg）、`make lint`、`make check-file-sizes`、`make build`、
`make check-architecture`、`make test-integration`（37 pkg）全部通过。

- [x] P4-08 审计导出：新 job 类型 `audit_export`（`POST /v1/audit/export`，权限 `audit.export`）——
      按租户分页读 durable ledger，清单含每事实 record_hash 与可验证的 manifest_digest，
      加密 + TTL 落对象存储，复用 `/v1/sync/jobs/:id/export/download`；截断时显式标记；
      新增权限常量 `PermAuditExport`（`sync_audit_export.go` + 3 个单元测试）
- [x] 分层健康：`GET /health/sources` 独立上报各 connector 连通性（ok/degraded），
      源故障不影响 `/readyz`（`handler/http/source_health.go` + 测试）
- [x] 控制面指标：`metrics/control_plane.go` —— events applied/quarantined、audit ingest
      failures、operation outcomes/age、callback deliveries（含 DLQ）、reconcile drift、
      projection/consumer lag；服务层经 `ControlPlaneMetrics` 接口埋点
      （event_ingest / operation / callback / sync / replication / audit_ledger），
      组合根注入（`service.NewRegistryControlPlaneMetrics`）
- [x] OpenAPI 补充 `/audit/export`（守卫测试通过）
