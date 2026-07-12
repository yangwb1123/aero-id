这份文档的分析已经以两份文件的形式保存在 `docs/requirements/` 中：

| 文件 | 大小 |
|------|------|
| `2026-07-11-aero-id-three-systemic-missing-platform-level-features.md` | 395 行，22.6 KB |
| `2026-07-11-aero-id-three-missing-platform-functions.md` | 395 行，22.6 KB（仅格式微差异步） |

两份文件的内容与您提交的版本**完全一致**（仅空行和尾部空白有细微差异）。

---

**对分析本身的反馈**（作为代码库已读回顾）：

这三个方向的识别和论证质量很高，且与代码级证据的对应关系准确。以下是快速交叉验证确认的要点：

### ✅ 方向一：实体变更历史
- `internal/domain/audit.go` 的 `AuditEvent.Meta` 确实无 `before`/`after` 字段约定 —— **证据成立**
- `internal/service/message.go` 的 `Edit`、`internal/service/comment.go` 的 `Edit` 确实是原地覆盖 —— **证据成立**
- `internal/domain/message.go` 的 `EditedAt *time.Time` 无版本历史字段 —— **证据成立**
- migrations 中无 `entity_edits`/`message_edits`/`file_versions` 表 —— **证据成立**

### ✅ 方向二：多实例协调
- `internal/hub/hub.go` 的 `sync.Map` 纯进程级 —— **证据成立**
- `internal/notification/dispatcher.go` 的 `d.pusher.Publish` 只通知本进程 Hub —— **证据成立**
- `internal/pkg/ratelimit/ratelimit.go` 明确标注 `PER-INSTANCE` —— **证据成立**
- `internal/outbox/relay.go` 的 `drain` 无 `FOR UPDATE SKIP LOCKED` —— **证据成立**

### ✅ 方向三：连接级准入控制
- SSE Hub `Subscribe` 创建 `make(chan SSEEvent, 16)` 无上限 —— **证据成立**
- HTTP server 无 `MaxConcurrent` 概念 —— **证据成立**
- DB 连接池上限不能阻止 goroutine 排队堆积 —— **分析正确**

**小提示**：方向三中"SSE 连接无准入控制"的证据引用标注为 `internal/handler/http/events.go（假设的 EventsHandler）`——实际上该文件目前可能不存在于 handler/http 中（SSE 入口可能在 hub 层或 notification 层）。如果有需要，可以快速确认实际的 SSE 注册入口路径以便引用更精确。

是否需要我对其中某个方向的代码证据做更深层的逐行验证，或者将这份分析与 ROADMAP.md 做优先级对齐？
