我已确认分析中的关键细节：原始分析检查的 `internal/notification/dispatcher.go` 是**死代码**。`app.go` 实际上连接了 `internal/service/notification.go` 中的 `service.NewNotificationDispatcher`。但根本原因（relay 使用 `context.Background()`）仍然是一个真正的 P0，并且 TASK-002（用 `WithTimeout` 包装每个处理程序）正确地解决了两个实现的问题。

---

## 8. 附录：原始分析中遗漏的发现

### 8.1 死代码：旧的 `notification.Dispatcher`

`internal/notification/dispatcher.go` 中的 `notification.NewDispatcher` 在 `app.go` 中未被引用，是死代码。所有路由都通过 `service.NewNotificationDispatcher`（`internal/service/notification.go`）。这个较新的实现已经包含了速率限制、偏好检查、折叠和块检查。

**建议操作**：删除 `internal/notification/dispatcher.go` 及其测试，以消除混淆。在 Direction 5 中发现的超时问题是通过 TASK-002（修复 relay）解决的，而不是通过修改 dispatcher。但删除死代码可以防止未来的维护者意外修改错误的文件。

### 8.2 默认配置审查

几个新功能依赖于配置值，这些值在 `configs/config.yaml` 中没有默认值或注释：

| 参数 | 当前默认值 | 建议的生产值 | 备注 |
|-------|-------------|---------------|-------|
| `outbox.poll_interval` | 5 秒 | 5 秒 | 没问题 |
| `outbox.max_retries` | 20 | 20 | 没问题 |
| `sse.max_subscribers_per_uid` | 无限制 | 5 | 每用户最多 5 个设备 |
| `sse.idle_timeout` | 无限制 | 15 分钟 | 可配置；0 = 禁用 |
| `sse.buffer_size` | 16 | 16 | 低流量时没问题；64 用于高吞吐量 |
| `cleanup.notification_ttl` | 无清理 | 720 小时（30 天） | 已读通知 |
| `cleanup.audit_ttl` | 无清理 | 8760 小时（1 年） | 审计日志 |
| `cleanup.outbox_ttl` | 无清理 | 168 小时（7 天） | 已发布的事件 |

### 8.3 `make check-file-sizes` 合规风险

```
internal/repository/comment.go  — ~430 行  ← 接近限制
internal/service/notification.go — ~280 行  ← 可以接受
internal/outbox/webhook.go       — ~115 行  ← 可以接受
```

添加 TASK-003（文件去重）后，`internal/repository/file.go` 将需要更多行来容纳 `IsDuplicate` 辅助函数。监控文件大小是必要的。

---

## 9. 最终建议

### 9.1 必须做的事情（优先级顺序）

1. **TASK-001 + TASK-002 同时进行** — 两个 P0 问题，没有冲突的文件，并行化是安全的。在第 1 天部署到生产环境。
2. **TASK-003** — 文件去重损坏是真实存在的，但需要迁移，这需要仔细的发布工程。第 2 天部署。
3. **TASK-005 TASK-004 TASK-006** — SSE 是 P1，可以在 P0 修复之后或与之并行地安全部署。
4. **TASK-007 TASK-008 TASK-009** — P2。对数据完整性没有立即的威胁，但防止了无限的表增长。可以在下一个 sprint 中部署。

### 9.2 不一定要做的事情

- **修复 `FOR UPDATE` 在没有 relay 的场景中**：没有其他地方使用 `FetchUnpublished` 模式。仅 relay 需要此修复。
- **为 direction 2 Like/Unlike 添加悲观锁**：计数器的实现经验证是安全的。不需要更改。
- **重写旧的 `notification.Dispatcher`**：它是死代码。删除它，不要修复它。

### 9.3 令人头疼的现实：留给未来工作的事项

- **Notifications TTL 和 audit TTL**，虽然在物理上解决了表膨胀问题，但可能需要*逻辑*删除/归档策略，这些策略与用户查询模式兼容。例如，在清除已读通知时，不会影响 `ListByRecipient` 的性能，因为清除条件（`read_at IS NOT NULL`）与查询条件（`read_at IS NULL` → 仅列出未读）是不可避免相关的。这需要仔细的查询计划分析。
- **SSE 连接限流**是纯 Go goroutine 限制。它是正确的，但不会保护实例免受内存耗尽（每个 goroutine ~4KB 栈 + 16 个事件的通道缓冲区）。对于 > 10K 并发连接，需要代理或扩展策略。这超出了本次 sprint 的范围。

### 9.4 风险总结：在部署之前需要解决什么

```
🚨 阻止发布：
   → 方向 1（FOR UPDATE SKIP LOCKED）
   → 方向 5（出站超时）

⚠️  第 2 天（在 P0 修复后）：
   → 方向 2（文件去重唯一约束）

✅ 可选（可以等待下一个 sprint）：
   → 方向 3（SSE 限制）
   → 方向 4（TTL 清除）
```
