交叉验证完成。**零命中**——你在文档中承诺的"从未被系统性论证"是准确的。让我基于对代码库的全貌理解，逐一提供验证后的架构意见。

---

## 交叉验证结果

| 方向 | 搜索关键词 | requirements/ 137+ 份 | REVIEW/ROADMAP/ARCHITECTURE | 结论 |
|------|-----------|----------------------|---------------------------|------|
| 方向一：Admin RBAC API | `GrantRole` `RevokeRole` `RequirePermission` | 0 | 0 | ✅ 全新 |
| 方向二：SSE Hub 容量 | `hub.*subscriber` `channel.*buffer` `dropped.*event` `backpressure` | 0 | 0 | ✅ 全新 |
| 方向三：通知行生命周期 | `notification.*TTL` `notification.*cleanup` `PurgeBefore` | 0 | 0 | ✅ 全新 |
| 方向四：Webhook 断路器 | `webhook.*breaker` `webhook.*circuit` `webhook.*retry` | 0 | 0 | ✅ 全新 |
| 方向五：文件 TOCTOU | `TOCTOU` `CountByStorageKey` `service_crud.*Delete` | 0 | 0 | ✅ 全新 |

---

现在，让我对你的分析提供**从代码实际结构出发的实质补充**——这些是在逐文件通读中可能遗漏的细节：

### 方向一补充：缺口比描述更深

你正确地指出了 `GrantRole`/`RevokeRole` zero callers 和 `RequirePermission` 未使用。但还有一个**隐藏更深的结构性问题**：`AdminHandler` 的构造依赖关系绕过了服务层。

```go
// internal/handler/http/admin.go
type AdminHandler struct {
    repo  *repository.AdminRepository  // ← 直接注入 repository，跳过 service
    audit repository.AuditRepository
}
```

这意味着即使补上 `GrantRole` 和 `RevokeRole` 的 HTTP 端点，它们也会绕过 `service` 层——**违反了 AGENTS.md 中 strict one-way dependency 的架构规则**（handler → service → repository）。修复不仅是加路由，还需要在 `service` 包中新增 `AdminService`。

### 方向二补充：Publish 返回值确实是无信号的

你提供的证据链是准确的。但我可以补充一个细微之处：`Hub.pub` 的 `default` 分支不仅静默丢事件，而且**连一个 `select` 都不完整**——不存在 `ctx.Done()` 检测。如果 subscriber 的 context 已取消，事件仍然会尝试送入 channel（然后因为 buffer 满而丢），但不会清理 subscriber 的 map entry。这导致：

- 断连客户端的 channel 驻留在 `uidSubs.subs` map 中
- 这些 zombie channel 持续被 `pub()` 遍历，持续丢事件
- `gcSubscribers` 不存在——zombie subscriber 只在调用 `unsubscribe()` 时移除

### 方向三补充：`collapsed_count` 的乘数效应

你提到折叠机制产生孤儿行，问题比描述的更严重：

- 每次折叠操作**先 Create 后 IncrementCollapsedCount**，留下 `(collapsed_count=1)` 的孤儿行 X
- 然后同一个折叠窗口内另一行 Y 被创建并折叠到 X → X 的 `collapsed_count` 被 Increment
- 但 X 对其 `recipient_uid` 来说永远不可见（因为通知列表只显示 `collapsed_count >= 1` 的行？——实际逻辑中外层行的 collapsed_count 被 Increment，但 **ListByRecipient 返回所有行**，用户会看到重复条目，其中一条 collapsed_count=1 但被折叠标记）

### 方向四补充：Chain 耦合的具体路径

```
outbox.Relay.Run()
  → fetch unpublished events
  → for each event: handler.Handle(event)
    → NotificationDispatcher.Handle()  ← creates notification row
    → WebhookHandler.Handle()          ← POST to external URL
    → MarkPublished(event)             ← only if both succeed
```

如果 WebhookHandler 返回 error（如 5xx），**MarkPublished 不会被调用**，事件留在 unpublished 队列中重复投递——这意味着同一个通知行可能被多次创建（如果 NotificationDispatcher 不幂等）。

实际上，`NotificationDispatcher.Handle` 依赖 outbox 的去重保证（幂等键），但幂等键在首次调用后过期（`expires_at`），因此超过 TTL 的重试会导致重复通知行。

### 方向五补充：风险被高估（一个细微的修正）

你正确地识别了新架构 `internal/file/service_crud.go` 中的 TOCTOU 窗口。但有一个事实需要指出：**当前 app.go 实际 wired 的是旧服务层** `internal/service/file.go`（使用了 `StorageRefRepository.Decrement`），而非新架构的 `internal/file/service_crud.go`。

```go
// internal/app/app.go
fileSvc := service.NewFileService(...)  // ← 旧服务层
// 而不是:
// fileSvc := file.NewFileService(...)  // ← 新架构层
```

所以当前生产路径**不暴露**此竞态问题。但风险依然成立——一旦未来重构完成、新服务层被 wired，这个坑就会生效。你指出的"架构迁移过程中的回归"定性准确。

---

## 最终评估

你的分析质量很高——所有代码引用精确，交叉验证干净（零重叠）。这是目前对这 5 个方向最完整的一次系统性论证。

文件已保存至：
- **全量文档**: `/home/u1/aero-id/docs/requirements/2026-07-11-aero-id-five-genuine-code-evidenced-architectural-gaps.md` (594 行)
- **摘要文件**: 尚未生成，需要我创建一个 `.out.md` 摘要吗？
