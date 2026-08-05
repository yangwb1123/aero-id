# aero-id — Architecture Decision Records

## ADR-001

**日期:** 2026-05-25

**决策:** 双传输层（HTTP Gin + gRPC）共享同一服务层

**原因:** 避免重复业务逻辑；handler 层仅做协议转换（unmarshal → domain call → marshal）

**影响:** 所有服务方法签名必须与两种传输无关（无 gin.Context、无 grpc.ServerStream）

**替代方案:** 独立实现 HTTP 和 gRPC 逻辑（已拒绝：维护成本翻倍）

---

## ADR-002

**日期:** 2026-05-25

**决策:** SSO 为唯一认证源，aero-id 不签发 token

**原因:** 单一认证权威，避免 token 泄露面扩大；aero-id 只验证不签发

**影响:** 所有 `/v1/*` 路由必须通过 SSO 验证；SIGNING_KEY 仅用于本地存储 presign HMAC

**替代方案:** 本地 JWT 签发（已拒绝：安全模型复杂化）

---

## ADR-003

**日期:** 2026-05-26

**决策:** 事务性 Outbox 模式驱动通知

**原因:** 消息/评论创建和通知发布必须原子提交；直接调用通知服务会引入两阶段失败

**影响:** 所有产生通知的写操作必须通过 outbox；relay 轮询间隔（5s）决定通知延迟

**替代方案:** 同步通知调用（已拒绝：数据一致性风险）

---

## ADR-004

**日期:** 2026-05-27

**决策:** 电路断路器仅对可用性错误（Unavailable/DeadlineExceeded）触发，业务错误（Unauthenticated/PermissionDenied）不触发

**原因:** 业务错误证明 SSO 在线；将其计入断路器会因正常拒绝请求而误开路

**影响:** `isAvailabilityError()` 分类函数是断路器正确性的关键路径

**替代方案:** 所有错误都触发断路器（已拒绝：误报率过高）

---

## ADR-005

**日期:** 2026-05-28

**决策:** 用户阻塞为方向性（directional），blockerUID=recipient，blockedUID=actor

**原因:** 阻塞方（recipient）不希望收到被阻塞方（actor）的消息；语义是"我不想收到你的消息"

**影响:** `IsBlocked(blocker=recipient, blocked=actor)` 调用约定；suppresses inbound only

**替代方案:** 双向阻塞（已拒绝：过于限制，可能有合法沟通场景）

---

## ADR-006

**日期:** 2026-06-13

**决策:** 采用 Autonomous Software Factory 运行模式

**原因:** 项目演进速度需要持续自主推进；6 优先级框架确保架构稳定性高于功能交付速度

**影响:** 所有重大变更（>10 文件、公共接口修改、安全模型变化）需人工 Approve；其余允许自主决策

**替代方案:** 手动逐步指令（已拒绝：上下文消耗过高）

---

## ADR-007

**日期:** 2026-08-04

**决策:** 身份与数据权威矩阵（P0-01/P0-02）

**内容:** Snaplink 是唯一人类账户权威，负责认证、全局授权、租户、会话、MFA、consent 与生命周期；
aero-id 内部使用 UUIDv7 `account_id` 作为规范账户键，外部身份唯一映射 `(issuer, subject)`，
`canonical_uid` 在兼容期内等于 Snaplink subject。Aero IM/Vault 仅经 connector 公共服务契约访问，
其 source-native ID 一律按 opaque TEXT 引用，不 reinterpret 为 aero-id ID。

**字段权威表（当前已落地实现）:**

| 字段 | 权威 | aero-id 处理 |
|---|---|---|
| email/phone/verification/MFA/lifecycle | Snaplink | 只做带 version/freshness 的源投影，绝不成为第二份可写 Profile |
| display_name/avatar/locale/timezone/preferences | aero-id（明确命名的展示/偏好字段） | `PATCH /v1/me/profile` 仅允许此集合 |
| 文件/消息/评论数据 | legacy 兼容源（见 ADR-011） | 保持现有语义，等待迁移决策落地 |
| 成员关系 | 各源（workspace/bucket/tenant） | `account_memberships` 带 `(source, scope_type, scope_id)`，绝不折叠为单一 tenant_id |

**影响:** 通用 profile PATCH 永不创建 email/phone 的第二份可写副本；source ID 列一律 TEXT。

**替代方案:** 在 aero-id 复制用户凭据/邮箱（已拒绝：双重权威、泄露面扩大）。

---

## ADR-008

**日期:** 2026-08-04

**决策:** 多区域模型与 Home Region 写入（P0-03）

**内容:** 账户初始 home region 由 `(issuer, subject)` 经版本化 hash ring 解析（`internal/region.Resolver`），
account-affecting 写入必须路由到 home region，区域唯一约束是最终防线而非替代路由。
强一致仅限本地权威；跨源视图为 eventual/bounded，必须暴露 `status/version/generated_at/last_synced_at`。
跨区域只复制 approved 投影、引用与脱敏审计数据；原始 payload/PII 留在权威区域。
home region 不可用时：允许陈旧读；另一区域接受写入必须先完成 durable promotion + fencing epoch
证明旧 writer 无法提交，否则暂停写入而非 split-brain（该协议属外部基础设施门禁，见实施计划 §13）。

**影响:** 单区域部署下 resolver 恒返回本地区域；多区域能力按 ADR 边界逐步落地。

**替代方案:** 默认多主数据库（已拒绝：需要独立冲突模型 ADR；区域 PG + home-region 写入为默认路径）。

---

## ADR-009

**日期:** 2026-08-04

**决策:** 传输与事件总线边界（P0-04）

**内容:** 公共/浏览器客户端走 HTTP JSON + OpenAPI；内部 request/response 走 gRPC/Protobuf；
source connector 在源无稳定 gRPC 时使用受控 HTTP。事件传输是端口背后的实现细节：
HTTP CloudEvents ingest（已落地）与 NATS JetStream / Kafka 适配器可共存，域代码不耦合任一 broker。
每个远程调用有 deadline、消息大小上限、并发限制、circuit breaker 与指标；自动重试仅限幂等操作。

**影响:** 新增事件总线适配器不得修改 domain/service 的事件语义；receipt/version 幂等是唯一正确性来源。

---

## ADR-010

**日期:** 2026-08-04

**决策:** 审计账本与 Replay 语义（P0-05）

**内容:** 合规审计账本 append-only、durable、分区存储，带版本化 hash/signature chain 与 WORM 归档；
legacy best-effort `AuditLogger` 仅限诊断，不构成完整审计证据。区分 `request_id / trace_id /
operation_id / event_id / causation_id`，绝不混用一个 ID。审计 payload allow-list + 脱敏，
禁止 token/cookie/密码/MFA 材料/对象字节/未脱敏消息正文。Replay 三种模式（forensic 无副作用 /
dry-run 模拟 / controlled 重新授权+新幂等键+新审计事实），controlled replay 永不复用历史凭据。

---

## ADR-011

**日期:** 2026-08-04

**决策:** Legacy 文件/消息/评论权威处置（P8-01）——保留为兼容源

**内容:** 在 Aero Vault/IM 尚未发布账户摘要、命令与脱敏事件 API（外部发布门禁未完成）之前，
当前 files/messages/comments 表面**保留为 legacy 兼容源（第四 source）**，不迁移、不弃用：
- 现有 API 契约保持不变，路由挂 `Deprecation: true` + `Sunset` 提示头（P8-03 兼容头，非破坏性）；
- 不做新功能投资；新 canonical 文件/聊天权威一律归属 Vault/IM；
- 影子迁移（P8-02）在源系统契约发布后启动，采用双读比较 + 事件追平 + 分批切换；
- 移除旧写路径的唯一前置条件：引用/导出/审计/回滚窗口/删除证明全部完成。

**影响:** legacy 路由增加兼容头中间件；领域代码不新增文件/消息/评论 authority。
