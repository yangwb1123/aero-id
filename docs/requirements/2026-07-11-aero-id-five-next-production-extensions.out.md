分析非常详实。经过代码库交叉验证，五个方向的代码级断言**基本准确**。以下是我验证后的发现：

---

## ✅ 已验证准确的断言

| 断言 | 状态 | 证据 |
|------|------|------|
| 无批量 API | ✅ | `api/proto/aero_id/v1/aero_id.proto` — 全部 38 个 RPC 均为单体操作；`MessageService`、`FileService`、`CommentService` 均无 `Batch*` |
| 单连接池共享 | ✅ | `internal/app/app.go:65-69` — 单个 `gdb` 传给所有 repo、outbox、idempotency、健康检查、GC |
| Rate limiter 不可变 | ✅ | `internal/pkg/ratelimit/ratelimit.go` — 无 `SetRate` / `SetBurst` 方法 |
| Poll interval 不可变 | ✅ | `internal/outbox/relay.go` — `Start(interval)` 后 ticker 固定，无 `SetInterval` |
| 日志级别不可运行时调整 | ✅ | `internal/pkg/logger/logger.go` — `zap.NewAtomicLevelAt` 未暴露 |
| Config 无 hot-reload | ✅ | `internal/config/config.go:Load()` — viper 一次性读取后 `Unmarshal`，无 `WatchConfig` |
| SSE 仅下行 | ✅ | `internal/handler/http/events.go` — 仅 `GET /events`，无上行通道（无 `POST`、无 WebSocket） |
| 无 delivered_at/delivery_status | ✅ | grep 无匹配 — `domain.Message` 有 `ReadAt` 但无 `DeliveredAt` |
| GENERATED ALWAYS AS STORED | ✅ | `migrations/000016_fts_indexes.up.sql` 确认 |
| search_vector @@ plainto_tsquery + ILIKE fallback | ✅ | `internal/repository/message.go:SearchMessages` + `SearchMessagesLike` |
| 无 accounts 表 | ✅ | `AGENTS.md` 明确："No accounts table, no FK to accounts" |

## ⚠️ 一个需要修正的断言

方向二中讲到 **"搜索索引来源不可靠：如果应用层在写 messages 行之后、写 tsvector 之前崩溃"** —— 实际上因为 FTS 使用了 **`GENERATED ALWAYS AS ... STORED`**，tsvector 是在 PostgreSQL 行级 `INSERT`/`UPDATE` 时**由数据库自动维护** 的，不存在应用层"先写行再写索引"的窗口期。这个错误不影响方向二的最终结论（确实缺少索引重建 API），但**核心论据需要调整**。

正确的论据应该是：
1. **迁移改变索引表达式后旧行不自动重算**：如果 `files` 表的 `search_vector` 表达式从 `coalesce(name, '')` 扩展为 `coalesce(name, '') || ' ' || coalesce(meta->>'description', '')`，历史数据的 tsvector 不会自动刷新（对比 migration 000016 的 `files` 列：当前只有 `name`，没有 `meta->>'description'`）
2. **没有重建 API**：管理员在数据修复/回滚/批量导入后无法触发在线重建
3. **`GENERATED ALWAYS` 的列不可直接 `REINDEX`** — 需要 `ALTER TABLE ... ALTER COLUMN ... TYPE tsvector USING search_vector::tsvector` 才能强制重算，这有锁表风险

## 与既有文档的**差异性确认**

我检查了 `docs/requirements/` 下 100+ 份文档（含 2026-07-11 的最新 50 份），关键词搜索确认：

| 方向 | 覆盖情况 |
|------|---------|
| 批量操作 | **零覆盖** — 无 `batch`/`bulk`/`批量` 出现 |
| 搜索索引重建 | **零覆盖** — 无 `索引重建`/`rebuild`/`REINDEX` |
| 连接池隔离 | **零覆盖** — 无 `连接池`/`pool isolation` |
| Typing/Presence | **零覆盖** — 无 `typing`/`presence`/`在线状态` |
| 配置热加载 | **零覆盖** — 无 `热加载`/`hot reload`/`WatchConfig` |

这五个方向确实是盲区，与之前 100+ 份分析无重复。**新颖性验证通过**。

---

那么——需要我把这份分析保存为 `docs/requirements/2026-07-11-aero-id-five-prod-extension-directions.md`，还是你有其他后续动作？
