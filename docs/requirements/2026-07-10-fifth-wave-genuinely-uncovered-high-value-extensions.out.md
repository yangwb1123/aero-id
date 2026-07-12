所有代码级证据均已确认。以下是验证摘要：

## 验证结果：全部 5 个方向的证据均已确认 ✅

### 方向一：Idempotency Key 存储耗尽攻击面

| 证据 | 状态 | 实际代码 |
|---|---|---|
| `reservationTTL = 2 * time.Minute` | ✅ | `internal/idempotency/store.go:22` |
| `Reserve()` 使用 `OnConflict{DoNothing}` — 无用户级计数检查 | ✅ | `store.go:37-52` |
| 每用户键数无上限 | ✅ | `Reserve()` 方法中无 `COUNT` 检查 |
| 后台清理间隔 1 小时 | ✅ | `internal/app/app.go:290` — `idemStore.RunCleaner(time.Hour)` |
| `purgeExpired()` 不区分用户 | ✅ | `store.go:145-155` — `WHERE expires_at < ?` |
| 索引：`(uid, endpoint, idem_key)` UNIQUE + `expires_at` | ✅ | `migrations/000004_create_idempotency_keys.up.sql` |

### 方向二：通知折叠竞态条件

| 证据 | 状态 | 实际代码 |
|---|---|---|
| `Handle()` 先调用 `Create()`，后调用 `FindRecentByTypeAndActor` | ✅ | `internal/service/notification.go:228-234` |
| 折叠后新行仍保留在 DB 中 | ✅ | 注释明确说明："The newly created row (n) stays in the DB as audit trail" — `service/notification.go:238-239` |
| `FindRecentByTypeAndActor` 查找 `read_at IS NULL AND created_at >= ?` | ✅ | `internal/repository/notification.go:128-134` |
| `IncrementCollapsedCount` 仅递增，不做清除 | ✅ | `repository/notification.go:145-155` |

### 方向三：Outbox Dead Letter 队列运维盲区

| 证据 | 状态 | 实际代码 |
|---|---|---|
| `MarkFailed` 在 `retryCount >= maxRetries` 时设置 `dead_letter = true` | ✅ | `internal/outbox/repository.go:114-115` |
| `FetchUnpublished` 查询 `WHERE NOT dead_letter` | ✅ | `repository.go:72-74` |
| 无 dead letter API 端点 | ✅ | `internal/handler/http/admin.go:20-50` — 仅暴露审计日志 |
| gRPC Ops 仅支持 Health 和 Reflection | ✅ | `internal/handler/grpc/ops.go` |
| AdminHandler 构造时未传入 outbox repo | ✅ | `internal/app/app.go:308` — `NewAdminHandler(adminRepo)` |

### 方向四：跨事件类型因果顺序违反

| 证据 | 状态 | 实际代码 |
|---|---|---|
| `FetchUnpublished` 按 `created_at ASC` 排序 | ✅ | `internal/outbox/repository.go:74` |
| `Event` 结构体无 `CausationID` 或 `ParentEventID` | ✅ | `internal/outbox/outbox.go:16-25` |
| `Notification` 结构体无因果字段 | ✅ | `internal/domain/notification.go:15-28` |
| `Handle()` 针对每个事件独立调用 `Create()` | ✅ | `service/notification.go:228-242` |

### 方向五：Rate Limiter 粒度缺口

| 证据 | 状态 | 实际代码 |
|---|---|---|
| 中间件对所有路由调用 `l.Allow(key)` | ✅ | `internal/middleware/ratelimit.go:38-52` |
| Key 格式为 `"uid:<uid>"` 或 `"ip:<ip>"`，不含路径 | ✅ | `ratelimit.go:56-64` |
| gRPC 同理 — `"uid:xxx"` 不含方法名 | ✅ | `internal/handler/grpc/server.go:124-129` |
| 全局 RPS 配置单一值 | ✅ | `configs/config.yaml:35-40` — `rate_limit.rps: 20` |
| `Allow()` 每次调用消耗 1 个令牌，与端点无关 | ✅ | `internal/pkg/ratelimit/ratelimit.go:41-48` |

---

**所有 5 个方向的代码级证据均与当前代码库完全一致。** 分析准确、全面，提出的建议方案定位清晰。最迫切需要解决的是方向一（安全漏洞——存储耗尽型 DoS）和方向二（数据正确性——幽灵通知），这两个均属于 P1 级别且无前置依赖。
