非常感谢你这份详尽的分析。我已经完整阅读了文档，并对所有代码级证据进行了实地验证。以下是验证结果汇总：

---

## ✅ 代码证据验证结果

### 缺口一：FTS 生成列写放大 ✅ 已确认

`migrations/000016_fts_indexes.up.sql` 确实在三张表上使用 `GENERATED ALWAYS AS ... STORED`。我验证了每个触发路径：

| 操作 | 证据文件 | 行号 | 确认 |
|------|---------|------|------|
| `MarkRead` 只改 `read_at` | `internal/message/repository.go` | L162-170 | ✅ `Update("read_at", at)` — 触发 tsvector 重算 |
| `UpdateContent` 改 `content`+`edited_at` | 同上 | L193-202 | ✅ 合理触发（content 确实变了） |
| `SetStatus` 只改 `status` | `internal/comment/repository.go` | L320-330 | ✅ `UpdateColumn("status", ...)` — 不必要的重算 |
| `UpdateMetadata` 改 `meta`/`visibility` | `internal/file/repository.go` | L194-210 | ✅ 不必要的重算 |
| GORM soft-delete (`deleted_at`) | — | — | ✅ 通用的 GORM 行为 |

### 缺口二：后台 Context 治理 ✅ 已确认

逐一验证了 5 个后台 goroutine：

| 任务 | 位置 | 使用的 context | 确认 |
|------|------|---------------|------|
| Outbox Relay | `internal/outbox/relay.go:163` | `context.Background()` | ✅ |
| Storage GC Sweeper | `internal/storage/gc/sweeper.go:255` | `context.Background()` | ✅ |
| Rate Limit Cleaner | `internal/pkg/ratelimit/ratelimit.go:119-134` | 无 context（纯 ticker） | ✅ |
| Idempotency Cleaner | `internal/idempotency/store.go:126-140` | 无 context（纯 ticker） | ✅ |
| Warmup | `internal/app/app.go:496` | `context.Background()` | ✅（内部有 5s timeout 子 context） |

**补充发现**：`notification/dispatcher.go`（L57）的 `Handle` 方法似乎是一个**未使用的副本**——实际使用的 dispatcher 是 `internal/service/notification.go` 中的 `NotificationDispatcher`（在 `app.go:174` 通过 `service.NewNotificationDispatcher` 创建）。这本身是一个独立的发现。

### 缺口三：内容大小限制 ✅ 已确认

- `internal/config/config.go` 有 `StorageConfig.MaxUploadBytes` 和 `AllowedMimeTypes`，但没有任何 message/comment 的 `MaxContentBytes` 配置
- `internal/message/service.go` 的 `validateSendInput`（L40-50）只检查 `in.Content == ""`，无长度上限
- `internal/comment/service.go` 的 `Create`（L80-130）只检查 `in.Content == ""`，无长度上限
- `internal/domain/message.go` 和 `internal/domain/comment.go` 的 `Content string` 无 tag 注释

### 缺口四：SSE 静默丢包 ✅ 已确认

`internal/hub/hub.go` L87-93（`pub` 方法）：

```go
for _, ch := range us.subs {
    select {
    case ch <- evt:
    default:
        // subscriber is too slow; drop rather than block
    }
}
```

无日志、无指标、无计数器。测试文件 `hub_test.go` L76-98（`TestHub_PublishSlowConsumerDrops`）明确测试了这个行为并验证事件被静默丢弃。

### 缺口五：gRPC 健康可见性 ✅ 已确认

`internal/handler/grpc/ops.go` L18-38：

```go
hs := health.NewServer()
hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)  // 全局二元
```

对比 HTTP `/readyz`（`internal/app/app.go` L110-115）具有每依赖粒度。`remaining-architectural-gaps.md` L526 确实提到 `gRPC health service 仍然返回 SERVING` 但仅作为维护模式的子话题。

---

## 📋 与既有分析的重叠性

| 缺口 | 你的结论 | 验证结果 |
|------|---------|---------|
| 缺口一 | 零重叠 | ✅ 确认。既有文档中 tsvector 仅在查询侧被提及 |
| 缺口二 | 部分提及但未系统化 | ✅ 确认。`operational-blindspots-and-resilience.md` 方向一关注的是"goroutine 是否还活着"而非"context 传播与 deadline" |
| 缺口三 | 微重叠 | ✅ 确认。仅 `deep-extension-analysis.md` 附录有一行列举 |
| 缺口四 | 零重叠 | ✅ 确认。既有文档关注 SSE 横向扩展和重放，而非**连接存活期间的丢包可观测性** |
| 缺口五 | 微重叠 | ✅ 确认。`remaining-architectural-gaps.md` 中作为维护模式的子话题 |

---

## 补充观察

在验证过程中发现两个额外的有意思的点，可能与你的分析相关：

1. **`internal/notification/dispatcher.go` 似乎未被使用**。`app.go` 使用的是 `service.NewNotificationDispatcher`（在 `internal/service/notification.go` 中）。这个副本的 `Handle` 方法调用 `d.pusher.Publish(n.RecipientUID, n.Type, string(payload))` 时**不传递 `eventID`**（4 参数接口 vs 3 参数调用），这与活代码的行为不一致。

2. **缺口一的「建议方案 A」（条件触发器）存在一个小挑战**：PostgreSQL 的 `FOR EACH ROW` 触发器 + `OLD.content IS DISTINCT FROM NEW.content` 条件可以避免无谓重算，但对于 `MarkRead` 场景（只改 `read_at`），仍然会触发 `FOR EACH ROW` 触发器的执行（只是条件不满足时跳过 tsvector 重算）。这是最小成本的改进，但仍有每行触发器的开销。

这份分析非常扎实，所有代码级证据均可复现。要讨论具体某个缺口的实施细节吗？
