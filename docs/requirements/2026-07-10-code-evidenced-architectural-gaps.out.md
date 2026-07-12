现在我对文档中的所有断言都做了代码级验证。以下是完整的反馈报告：

---

## ✅ 代码验证结果总表

| # | 方向 | 文件证据验证 | 判断 | 备注 |
|---|------|------------|------|------|
| 1 | 双重文件服务 | ✅ 确认 `internal/file/` 含完整 Service（7 文件），仅 search 导入 | **成立** | 但 search 只用了 `file.Repository`，非 `file.Service`；comment/message 同理 |
| 2 | 幽灵通知行 | ✅ 确认 `notification.go:227 Create` → `:233` 折叠检查 → 不删除新行 | **成立但需澄清** | 代码注释明确称此为"审计追踪设计"，非无意 bug |
| 3 | gRPC 无幂等键 | ✅ 确认 HTTP 有完整中间件链，gRPC 拦截器链无 idempotency | **完全成立** | |
| 4 | Outbox 死信不可见 | ✅ 确认 `admin.go` 中无 outbox 操作端点 | **完全成立** | |
| 5 | SSE Hub 单实例 | ✅ 确认 `hub.go` 用 `sync.Map` 做进程内 subscriber 注册 | **完全成立** | |

---

## 对每个方向的深入评估

### 方向一：双重文件服务——需要更精确的定位

代码验证确认 `internal/file/` 的 13 个源文件中包含完整的双层结构：

```
internal/file/
├── file.go               → Service 接口(81) + fileService(95) + Option(105)
├── service_crud.go        → Create/Get/List/Delete/UpdateMetadata (与 service/file.go 重复)
├── service_upload.go      → 预签名上传 (service/file.go 没有)
├── service_share.go       → 文件共享 (service/file.go 已有)
├── repository.go          → Repository 接口 (search 只用了这个)
├── repository_share.go    → 共享仓库实现
├── handler_grpc.go        → gRPC handler
├── handler_http.go        → HTTP handler
└── 测试文件 ×5
```

**但需要澄清**：search 包只依赖 `file.Repository`（`SearchFiles` 方法），并不是 `file.Service`。duplicate 的是 `file.fileService` 和 `service.fileService` 的 CRUD 逻辑。`handler_grpc.go` / `handler_http.go` 在 `internal/file/` 中是**另一个重复的传输层**——它们实现独立的 HTTP 和 gRPC 路由，但不被 `app.go` 注册为生产路由。

**更精确的文档修改建议**：
- 将描述从"search 用了整个包"改为"search 只依赖 `file.Repository`，但包内存在完整的、未被 app.go 接入的复制传输+业务层"
- comment 和 message 包同理，但它们的 service 层是**单一实现**，不属于"双重"——只有 file 包有真正的双重 service

### 方向二：幽灵通知行——设计意图 vs 问题

这是个**很有意思的发现**。代码注释 `notification.go:240` 明确说：

```go
// Best effort: the extra row is harmless.
```

这暗示当前行为是**已知、有意识的设计选择**，而非无意的 bug。背后的逻辑：
1. `existing` 的 `created_at` 被更新为 `time.Now().UTC()`（line 244），所以用户 feed 查询按 `created_at DESC` 排序时，折叠行**排在最前面**
2. 折叠行有 `collapsed_count=N`，所以正确的合并计数可见
3. 多出来的行 `n` 的 `read_at` 为 NULL，但业务上不影响查询（如果查询正确处理了 `collapsed_count`）

**但问题依然存在**：
- 如果某个查询 `WHERE read_at IS NULL` 且没做 collapsed 聚合，会误报未读
- 如果 `collapsed_count` 在 `5min` 窗口内持续增长（比如 10 次），会积累 9 个冗余行
- 这对大用户量的场景会造成表膨胀

**建议修正**：文档应标注"当前行为是已知的设计权衡（代码注释证明了这一点），但存在表膨胀和误报风险"，而不是简单地描述为"bug"。

### 方向三：gRPC 无幂等键——完全正确

唯一需要补充的是：gRPC 的拦截器模式使得实现 `IdempotencyInterceptor` 比 HTTP 中间件更简洁——因为 gRPC 的 `UnaryServerInterceptor` 可以统一 wrap 所有 RPC 方法。HTTP 需要路由级别的配置（`router.go:68-70` 仅在特定路由上启用）。

### 方向四：Outbox 死信不可见——完全正确

补充观察：死信查询可以通过 SQL 直接查看 (`SELECT * FROM outbox_events WHERE dead_letter = true`)，但确实缺少管理面 API。这是一个生产中迟早会遇到的运维问题。

### 方向五：SSE Hub 单实例——完全正确

额外观察：`hub.go` 的设计良好（per-uid 锁分段、非阻塞推送、16 个 buffer），但这些优化反而让迁移到跨实例架构时需要更小心——因为目前依赖了进程内语义（`sync.Map` 的即时一致性），迁移到 Redis Pub/Sub 需要处理最终一致性。

---

## 补充验证：几个值得注意的细节

1. **`service/file.go` 的 `WithStorageRefs` 选项确实在 `internal/file/file.go` 中不存在**，但 `internal/file/file.go` 有 `WithFileAuditLogger`（两者都有审计支持）。两套选项集的漂移已经发生。

2. **`internal/file/handler_http.go` 是 8611 字节**，有完整的 HTTP 路由实现——这些路由**没有注册到主 router**，属于"死代码"。

3. **通知折叠的 push 逻辑**：当折叠命中时（line 244-246），推送的是 `existing`（更新后的行），但 outbox 事件是 `n`（新行）。这意味着 SSE 推送通知的是一个**没有持久化 'existing' 行状态**的混合对象（`existing.CollapsedCount++` 只发生在内存中）。

---

综上，这个分析文档的 5 个方向全部成立，但方向一需要更精确的措辞，方向二需要标注"已知设计权衡"而非纯 bug。这是一份高质量的架构分析。是否需要我基于此分析起草对 `AGENTS.md` 的修改（添加架构分裂规则），或者创建用于验证工具的 `make check-no-duplicate-file-service` 脚本？
