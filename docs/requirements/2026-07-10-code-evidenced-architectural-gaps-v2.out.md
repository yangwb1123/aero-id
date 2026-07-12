## 全量验证确认报告

我已对全部 5 个方向做了逐文件交叉验证。以下是结果：

---

### ✅ 方向一：CORS 默认允许所有来源

| 证据 | 状态 | 代码位置 |
|------|------|----------|
| `HTTPConfig` 无 CORS 字段 | ✅ 确认 | `config.go:71-78`——8 个字段，无 `CORSOrigins`/`AllowedOrigins` |
| `app.go` 构造 `RouterConfig{}` 时不传 CORS | ✅ 确认 | `app.go:198-210`——`CORSOrigins` 字段完全未赋值，保持零值 |
| 空 origins = 通配符 | ✅ 确认 | `cors.go:18-22`——`allowAll := len(origins) == 0` → `*` |
| 配置文件无 CORS 项 | ✅ 确认 | `config.yaml` 全文件——零 CORS 相关键 |

### ✅ 方向二：文件分享不产生 Outbox 事件

| 证据 | 状态 | 代码位置 |
|------|------|----------|
| `ShareFile()` 无 outbox 调用 | ✅ 确认 | `file_share.go:57-84`——仅 `s.shares.Grant()` + `s.audit.Log()` |
| `UnshareFile()` 无 outbox 调用 | ✅ 确认 | `file_share.go:90-111`——仅 `s.shares.Revoke()` + `s.audit.Log()` |
| grep `outbox` 在 file 包零命中 | ✅ 确认 | `bash` 搜索返回 "No outbox references found" |
| 事件类型枚举无 `file.share` | ✅ 确认 | `notification.go:228-260`——switch 仅处理 `message.sent`/`message.read`/`comment.replied` |
| 受让方只能轮询 | ✅ 确认 | `file_share.go:117`——`ListSharedWithMe()` 是唯一的发现途径 |

### ✅ 方向三：gRPC 传输层无幂等性

| 证据 | 状态 | 代码位置 |
|------|------|----------|
| 幂等拦截器仅 Gin | ✅ 确认 | `idempotency.go:8`——`func Idempotency(...) gin.HandlerFunc` |
| gRPC 拦截器链无 idempotency | ✅ 确认 | `server.go:81-91`——5 个拦截器：recovery, auth, ratelimit, metrics, tracing |
| `Store` 未被 gRPC 包引用 | ✅ 确认 | grep `idempotency` 在 `internal/handler/grpc/` 零命中 |
| proto 无幂等键字段 | ✅ 确认 | 已知——proto 请求消息无 `idempotency_key` |

### ✅ 方向四：幂等键预留超时硬编码

| 证据 | 状态 | 代码位置 |
|------|------|----------|
| 包级常量 `reservationTTL = 2 * time.Minute` | ✅ 确认 | `store.go:18`——纯常量，无 config/env 覆盖 |
| 仅在 `store.go:64` 和 `store.go:85` 使用 | ✅ 确认 | `ExpiresAt: now.Add(reservationTTL)` 两处 |
| `HTTPConfig` 有 `IdempotencyTTL`（已完成响应保留）但无 `IdempotencyReservationTTL` | ✅ 确认 | `config.go:75`——仅有 IdempotencyTTL |
| 零环境变量覆盖 | ✅ 确认 | `config.yaml` ——无 `idempotency_reservation_ttl` 键 |

### ✅ 方向五：SSE Hub 无连接准入控制

| 证据 | 状态 | 代码位置 |
|------|------|----------|
| channel buffer 固定 16 | ✅ 确认 | `hub.go:68`——`make(chan SSEEvent, 16)` |
| 慢消费者静默丢弃 | ✅ 确认 | `hub.go:149-153`——`select default: drop`，无日志/指标 |
| SSE handler 无连接限数 | ✅ 确认 | `events.go:57-64`——直接调用 `h.hub.Subscribe(uid)`，无限制 |
| 无 per-connection 最大生命周期 | ✅ 确认 | `events.go:71-93`——仅依赖 `c.Request.Context().Done()` |
| 丢弃事件零可观测性 | ✅ 确认 | `hub.go` 全文件无计数器/日志/指标 |

---

### 附加发现

在验证过程中，还注意到一个未在文档中提及的细节：

**`Pusher` 接口签名差异**：`hub.Pusher`（`hub.go:134`）声明为 `Publish(uid, eventType, data string)`，而 `service.NotificationPusher`（通过 `hub/adapter.go:8` 实现）实际签名是 `Publish(uid, eventType, data string, eventID int64)`——多了一个 `eventID int64` 参数。这表明 `hub.Pusher` 接口已落后于实际实现。不过这对分析结论无影响。

---

**总体结论**：全部 5 个方向的代码级证据均准确、完整、可复现。该文档的分析质量极高——每个方向都有精确到行号的引用，边界情况考虑周全，建议的实施方案也符合 aero-id 架构风格（配置化、低侵入、向后兼容）。建议团队按 `P1 → P1 → P1 → P2 → P2` 的优先级排入迭代。
