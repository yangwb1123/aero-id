# Source capability inventory（P0-07）

> 状态：以 2026-08-04 工作区代码快照为准整理的起点清单。**文档不能替代发布物核对**——
> 实施 connector 前必须现场确认待部署版本的 API、事件、认证与幂等能力，并更新本文。

## 结论摘要（来自仓库快照）

| 源 | 可用公共缝 | aero-id 接入方式 | 缺口（需源侧发布） |
|---|---|---|---|
| **Snaplink** | OAuth/OIDC + JWKS；稳定 AuthZ/Admin/AuditWriter gRPC（`api/proto/snaplink/`）；HTTP/OpenAPI | 热路径本地验 JWT（`internal/sso` JWKS）；权限/管理走 gRPC adapter | 账户生命周期/consent 批量查询契约；审计事件流覆盖 |
| **Aero IM** | Rust/Axum REST/WS；NATS JetStream/outbox；本地 JWT/PAT/OIDC/TOTP | HTTP connector（`internal/connector/aeroim`）经 summary/command/status seam | 账户级 batch/summary API、脱敏 account/workspace 事件 subject；`im.room.*` 原始 subject 不直接消费 |
| **Aero Vault** | Go REST/S3/WebDAV/MCP；持久事件/SSE/HMAC webhook | HTTP connector（`internal/connector/aerovault`）；webhook→canonical event | 账户摘要 batch API；blob 永不复制 |

## aero-id 侧已具备的接入面

- **Connector 端口**（`internal/connector`）：`Summary` / `Command`(Erase) / `OperationStatus` / `Health`，
  统一 `RemoteError`（timeout/unavailable/unauthorized/forbidden/invalid/unknown）+ deadline/retry/bulkhead/breaker；
- **事件缝**：canonical envelope（`internal/event`）+ HTTP ingest（HMAC/timestamp/source binding）+ NATS JetStream adapter（P3-04）；
- **权限缝**：`account:read/write/manage/export/erase`、`audit:read/export/replay`、`operation:read/manage`、
  `callback:manage`、`source:sync`（最终名称需与 Snaplink permission contract 对齐）；
- **幂等契约**：所有 command 携带 operation/step/idempotency key，source operation ID 落库可对账。

## 接入 checklist（每源发布后执行）

1. 核对 OpenAPI/proto 与本文档差异，更新 connector 契约测试（auth context、deadline、分页、partial、幂等、错误映射）；
2. 确认脱敏事件 subject 与 `event_envelope` 字段映射（`schema_version`、`aggregate_version`、`tenant_id`）；
3. 确认 source operation status 查询语义（unknown outcome 对账依赖）；
4. 跑 `make test-integration` + connector conformance；
5. 更新本清单状态与 `docs/SNAPLINK_INTEGRATION.md`。
