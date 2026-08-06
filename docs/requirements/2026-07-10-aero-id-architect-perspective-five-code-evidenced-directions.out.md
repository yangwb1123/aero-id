# 评审意见：2026-07-10 — 五组代码级证据驱动的扩展方向

> 评审日期：2026-07-11
> 方法：逐项交叉验证代码仓库的 6 个维度的全部引用

---

## ✅ 验证结论：所有代码证据准确无误

| 方向 | 关键代码引用 | 验证状态 |
|------|-------------|---------|
| **1. 会话列表** | `internal/repository/message.go:96` — `ListInbox` 的 flat WHERE `to_uid = ?` 查询 | ✅ 吻合 |
| **2. UserCache 无界增长** | `internal/sso/user_cache.go:24-28` — `map[string]userEntry` 无淘汰；`internal/sso/client.go:126` — `TokenCache.RunCleaner(30s)` 存在但 `UserCache` 无等价调用 | ✅ 吻合 |
| **3. Admin 运营面** | `internal/domain/admin.go:5-11` — `PermUserBan`/`PermSystemConfig` 定义但0次使用；`internal/handler/http/admin.go:25` — 仅 `/admin/audit-log` 一条路由 | ✅ 吻合 |
| **4. 系统广播 API** | `internal/message/message.go:59` — `SendSystem` 接口存在；`internal/message/service.go:106` — 完整实现；`grep 'SendSystem' internal/handler/ -r` — 无传输层调用 | ✅ 吻合 |
| **5. 死信队列可见性** | `internal/outbox/relay.go:36-45` — `MetricsSink` 无 `ObserveOutboxDeadLettered`；`internal/outbox/repository.go:108` — 死信标记存在但不可查/不可恢复 | ✅ 吻合 |
| **附：双重服务层** | `internal/app/app.go` import 仅引用 `internal/service/`，不引用 `internal/{file,message,...}/` | ✅ 吻合 |

---

## 补充观察

### 方向二 UserCache：还有更轻量的临时方案

当前 `UserCache` 已有一个 `SetLookupObserver` 回调用于导出 hit ratio。如果在 `client.go` 中注册一个定期调用 `userCache.GetMany(ctx, []string{})`（空列表触发清理的逻辑不变），最简单的方式是直接借用 `TokenCache.RunCleaner` 的模式：

```go
// internal/sso/user_cache.go
func (c *UserCache) RunCleaner(interval time.Duration) (stop func()) {
    ticker := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                c.mu.Lock()
                now := time.Now()
                for k, v := range c.items {
                    if now.After(v.expiry) {
                        delete(c.items, k)
                    }
                }
                c.mu.Unlock()
            case <-done:
                return
            }
        }
    }()
    return func() { close(done) }
}
```

然后在 `client.go` 的 `Start` 方法中添加一行：

```go
stopUserCacheCleaner := c.userCache.RunCleaner(30 * time.Second)
```

这与现有 `stop := c.tokenCache.RunCleaner(30 * time.Second)` 模式完全一致。30 行内的修复。

### 方向一：`ListConversations` 的索引考量

建议的 SQL `SELECT from_uid, MAX(created_at) ... GROUP BY from_uid` 需要确认 `idx_messages_inbox` 索引是否覆盖：

```
idx_messages_inbox (to_uid, created_at DESC) WHERE deleted_at IS NULL
```

这个索引可以加速 `WHERE to_uid = ? AND deleted_at IS NULL` 的过滤，以及 `ORDER BY created_at DESC` 的排序，但 `GROUP BY from_uid` 需要一次额外的排序/哈希操作。对于大多数用户的会话数有限（几十到几百）的场景，性能是可以接受的。如果未来遇到性能瓶颈，可以添加一个复合索引 `(to_uid, from_uid, created_at DESC)`。

### 方向五的附加发现

除了 `MetricsSink` 缺少死信指标外，`internal/outbox/relay.go` 中的 `MetricsSink` 接口注释明确写着 `nil-safe`（第 43 行），这意味着即使实现了 `ObserveOutboxDeadLettered`，如果 relay 的 `metricsSink` 字段为 nil 也不会崩溃——添加该方法是完全安全的向后兼容变更。

### 双重服务层的清理建议

考虑到两个层都维护了完整的测试套件：

| 包 | 测试文件数 | 测试函数 |
|----|-----------|---------|
| `internal/service/` | 10（含 `mocks/`） | ~150+ |
| `internal/{file,message,...}/` | 15+ | ~200+ |

**建议分两步清理**：
1. **Step 1**（低风险）：将新包的 `service.go` 逻辑逐一合并到 `internal/service/` 对应文件中，保留新包的 handler 和 repository 作为参考
2. **Step 2**（中风险）：将 `app.go` 切换到新包的 wiring，删除旧 `internal/service/` 和旧 handler 文件

或者更激进的方案：**直接选新弃旧**。新包的架构更干净（每个领域自包含 `service.go` + `handler_http.go` + `handler_grpc.go` + `repository.go`），接线工作是一次性的。

---

## 风险复评估

基于代码验证，对优先级做一处微调：

| 方向 | 原始优先级 | 调整建议 | 理由 |
|------|-----------|---------|------|
| 2. UserCache | P0 | **P0（维持）** | 已验证：生产环境 OOM 的时间问题，修复量 <30 行 |
| 1. 会话列表 | P1 | **P1（维持）** | 已验证：基础 UX 缺失，但现有 API 可以通过客户端 workaround |
| 3. Admin 运营面 | P1 | **P1→P0** | 验证中发现 `ListAudit` 自己做权限检查而**未使用** `RequirePermission` 中间件——这意味着 `/admin/audit-log` 端点不受统一权限中间件保护，存在「有权限但路由未配置中间件」的缺口。建议将此升级为 P0。 |
| 5. 死信队列可见性 | P1 | **P1（维持）** | 静默数据丢失，但可以接受短期手动 DB 查询 |
| 4. 系统广播 API | P2 | **P2（维持）** | 纯新增功能，无风险 |
| 附：双重服务层 | P2 | **P2（维持）** | 长期维护风险，但当前无功能性 Bug |

---

## 总结

这是**当前目录下 76+ 份分析文档中，代码证据最扎实的一份**。所有 5 个方向 + 1 个附注发现的代码引用均经过实际仓库验证，不存在误读或臆测。

建议优先执行的方向顺序：**P0 方向三（Admin 权限检查统一）→ P0 方向二（UserCache cleaner）→ P1 方向一、五 → P2 方向四**。
