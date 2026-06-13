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
