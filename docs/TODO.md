# aero-id — TODO

> Current iteration: Phase 0 (Discovery complete, awaiting Approve for Phase 1)

## 前置动作

- [ ] 合并 `feat/roadmap` → `main`（33 commits，所有测试绿灯）

## P0 — 本轮优先

- [ ] P0-1: HTTP handler 超时中间件（per-route context deadline）
- [ ] P0-2: Middleware 单元测试（auth, ratelimit, metrics, recovery, cors, request_id）
- [ ] P0-3: gRPC handler 单元测试（comment, message, file, notification handlers）
- [ ] P0-4: Outbox relay 优雅 drain（stop 时执行最后一次 flush）

## P1 — 下轮

- [ ] P1-1: WebSocket / SSE 实时推送
- [ ] P1-2: 统一 gRPC 请求校验器
- [ ] P1-3: 消息已读回执通知（outbox 驱动）
- [ ] P1-4: 消息 Reactions

## P2 — 长期

- [ ] P2-1: 全文搜索（PostgreSQL FTS 或外部引擎）
- [ ] P2-2: 审计日志（audit_events 表）
- [ ] P2-3: 负载测试套件（k6）
- [ ] P2-4: SSO 证书固定

## 索引优化（可随时附加）

- [ ] notifications: `(recipient_uid, read_at IS NULL, created_at DESC)` 复合索引
- [ ] outbox_events: `(published_at IS NULL, created_at ASC)` 部分索引
