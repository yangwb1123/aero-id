# aero-id — TODO

> Updated: 2026-06-13 — Phase 0 + P0 + P1 + P2 delivered.

## 前置动作

- [ ] 合并 `feat/roadmap` → `main`（41 commits ahead，所有测试绿灯）

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
