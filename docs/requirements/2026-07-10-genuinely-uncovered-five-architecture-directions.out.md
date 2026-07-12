已完成代码级验证。以下是我对文档的审查总结：

## ✅ 代码级验证结果

所有 5 个方向均已对照实际代码验证，核心事实基本准确。发现 3 处微小偏差：

### 方向一：like_count 漂移 ✅ 基本准确
- `Like` 在 `internal/comment/repository.go:248-270` ✅ 
- `Unlike` 在 `internal/comment/repository.go:275-294` ✅ 
- 事务使用 `clause.OnConflict{DoNothing: true}` 保证幂等性，计数±1 逻辑正确

### 方向二：gRPC reflection 安全 ⚠️ 小偏差
- 函数名是 `infraMethod`（不是 `isInfrastructure`），在 `internal/handler/grpc/server.go:168-171`
- 逻辑确认：`strings.HasPrefix(fullMethod, "/grpc.reflection.")` 绕过认证 ✅
- 偏差：文档写的函数名和行号(`:160-170`)与实际(`:168-171`)有细微出入，建议修正

### 方向三：SSO 用户缓存 ✅ 完全准确
- `UserCache` 使用 `map[string]userEntry`，**无**清理机制 ✅
- `TokenCache` 有 `RunCleaner`（`internal/sso/token_cache.go:140-155`），但 `UserCache` **没有** ✅
- `UserCache` 也没有 `serve-stale` 模式（`TokenCache` 有 `GetStale`）

### 方向四：文件搜索可见性 ⚠️ 文件路径偏差
- `SearchFiles` 在 `internal/file/repository.go:223-237` ✅（仅 `owner_uid = ?`）
- `SearchFilesLike` **不在** `internal/file/repository.go`，而在 `internal/repository/file.go:241` — 文档需要修正文件路径
- `SearchFilesLike` 同样只用 `owner_uid = ?` ✅

### 方向五：goroutine 停止模式 ✅ 完全准确
- Outbox relay 有最终 drain（`internal/outbox/relay.go:169`）✅
- Sweeper 无最终 sweep（`internal/storage/gc/sweeper.go:252-253`）✅
- Idempotency cleaner 无最终 purge（`internal/idempotency/store.go:124-125`）✅
- TokenCache cleaner 也无最终 purge（`internal/sso/token_cache.go:149-150`）✅

---

**建议的修正项**：
1. 方向二：改 `isInfrastructure` → `infraMethod`，更新行号为 `:168-171`
2. 方向四：删除 `SearchFilesLike` 的文档引用（已确认不存在，或者修正文件路径到 `internal/repository/file.go:241`）
3. 文档标题中的"第九轮"与目录中现有文件命名风格略有不同——现有文件名使用 `2026-07-11-aero-id-round9-...` 格式

**关于零覆盖声明**：通过 grep 交叉验证了所有 5 个方向的关键词组合，确认这些主题在 50+ 份既有分析文档中确实没有被系统性覆盖过。少数文档中出现的相关关键词（如 `genuinely-uncovered-five-architecture-directions.md` 命中 13 次"goroutine"相关词）经过核查，讨论的是**不同的问题上下文**，并非相同的方向。

---

你想让我把这份文档保存为 `docs/requirements/2026-07-11-aero-id-round9-code-scan-five-gaps.md`，并提供一份修正后的最终版本吗？
