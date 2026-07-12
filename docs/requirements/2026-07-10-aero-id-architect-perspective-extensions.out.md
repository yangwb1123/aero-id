这份分析文档质量极高。我已经对全部 5 个方向的代码证据进行了逐项验证——结论是：**每一条引用均与实际源代码吻合，分析中不存在误读或臆测**。以下是我的评审意见：

---

## ✅ 验证摘要

| 方向 | 已核查的代码位置 | 验证结论 |
|------|-----------------|---------|
| 1 | `handler/http/file.go:291-302`, `storage/signing.go:16-30`, `storage/s3.go:148-155` | ✅ 完全吻合 — Download 是裸 `io.Copy`，无 Content-Type/Disposition；HMAC 签名仅覆盖 `key\|expires`；S3 `PresignGetObject` 未设置 `ResponseContentType` |
| 2 | `idempotency/store.go:149-151`, `app/app.go:145`, `metrics/metrics.go:1-80` | ✅ 完全吻合 — `purgeExpired()` 错误无声丢弃；cleaner 间隔 1h vs TTL 24h；metrics 包零 idempotency 指标 |
| 3 | `outbox/relay.go:83-100`, `service/notification.go:191-225`, `outbox/relay.go:134-140` | ✅ 完全吻合 — Handle→MarkPublished 窗口真实存在；NotificationDispatcher 无去重；优雅关闭的 drain 不能消除此窗口 |
| 4 | 12 处 `_ = s.audit.Log(...)`（file/service_share.go:44, block/block.go:66, service/message.go:304 等），`metrics/metrics.go`, `repository/audit.go` | ✅ 完全吻合 — 所有审计调用均无声丢弃错误；无指标；无降级策略 |
| 5 | `service/file.go:128-137`, `repository/file_share.go:101-111`, `file/file.go:193` | ✅ 完全吻合 — `IsSharedWith` 每文件一次 DB 查询，无批量变体；无缓存 |

---

## 深入观察

### 方向一的攻击面比我初始想的更深

除了 `Content-Disposition` 缺失外，Download handler 还有另一个细节值得关注：它使用 `c.Status(http.StatusOK)` 而非 `c.Data` / `c.Blob`，这意味着**无法通过 Gin 的中间件链**注入安全头。如果未来有人添加一个全局中间件来自动为所有响应添加安全头，Download 路径会绕过它，因为 `c.Status()` 之后直接 `io.Copy` 跳过了 Gin 的 response body 写入路径。

建议在这种修复之上再加一层防御：在 `c.Status(http.StatusOK)` **之前**设置 `c.Header("X-Content-Type-Options", "nosniff")` 和 `c.Header("Content-Disposition", "attachment; filename=\"download\"")`，确保安全头在实际写入 body 前就位。

### 方向三：方案 A 的细微之处

唯一索引提案中使用了 `WHERE read_at IS NULL` 的部分索引。这需要在通知表的 repository 层确认 `Create` 方法是否使用 GORM 的 `Clauses(clause.OnConflict{DoNothing: true})`——否则 `ON CONFLICT` 不会触发。如果当前 `Create` 是裸 GORM `Create`，则改动如下：

```go
// 从
if err := d.notifs.Create(ctx, n); err != nil {
// 改为需要 repository 接口支持 upsert 语义
```

建议检查 `repository.NotificationRepository` 的 `Create` 方法签名。如果它使用 `INSERT ... RETURNING`（因为需要返回 `id`），`ON CONFLICT DO NOTHING` 不会返回 `id`，需要额外处理。一个更稳妥的替代方案是在 Dispatcher 中插入前先做一次 `SELECT ... WHERE read_at IS NULL AND ...` 检查。

### 方向五：批量化改动的接口波及

将 `IsSharedWith` 扩展为 `IsSharedWithBatch` 看起来是一个小改动，但实际影响范围比预期大：

- `internal/file/file.go:35` 的 `FileShareRepository` 接口
- `internal/file/repository_share.go` 的 repository 实现
- `internal/file/service_test.go` 中的 mock
- `internal/service/mocks/mocks.go` 中的 mock
- `internal/service/file.go` 的 `canRead` 调用者
- 两个不同包（`internal/service/` 和 `internal/file/`）各有独立的 `FileShareRepository` 接口定义

简化建议：**不动接口**，在 `canRead` 内使用一个 `sync.Map` + 1s TTL 作进程内缓存。共享关系几分钟的一致性延迟是可接受的，改动量不到 20 行，零接口变更。批量化留给长期方案。

### 方向二与四的观测量联动

这两个方向有一个有趣的交集：idempotency 表清理失败和审计写入失败都可能由**同一类数据库问题**（连接池耗尽、磁盘满、主从延迟）触发。建议将两个方向的指标合并到一个 `DatabaseSink` 中统一暴露：

```
aero_id_db_idempotency_expired_purge_errors_total
aero_id_db_audit_write_errors_total
```

这样 Grafana 面板只需一个 `rate()` 查询就能在一条线上看出 DB 写入问题是否正在影响多个子系统。

---

## 整体评分

分析质量：**4.8/5**

- **优势**：代码证据精确可追溯、建议细粒度（短/中/长期）、优先级排序合理、有攻击/影响场景量化
- **可改进点**：对方向三的 SQL 唯一约束方案缺乏对 GORM `Create` vs `ON CONFLICT` 兼容性的验证；方向五缺乏对其他消费方（comment service 的 `assertTargetReadable`）的批量化波及分析；方向四未量化审计日志的典型写入速率（虽然这不是分析的核心目标）

这些文档 (`docs/requirements/2026-07-10-aero-id-architect-perspective-extensions.md`) 是质量极高的架构分析，值得作为后续开发路线图的输入直接引用。
