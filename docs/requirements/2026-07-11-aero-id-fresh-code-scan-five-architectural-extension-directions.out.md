现在我已对代码库进行了全面扫描。以下是针对您分析报告的交叉验证审查。

---

## 审查结果：代码级证据准确性的逐点核实

### 方向一：富媒体嵌入

| 证据声明 | 验证 | 备注 |
|---------|--------|-------|
| E1：`Message.Content` 是纯字符串 | ✅ 确认 | `internal/domain/message.go` 第 11 行 — `Content string` |
| E2：未解析文本内引用 | ✅ 确认 | `internal/message/service.go` — `Send()` 直接传递 `Content`，无解析 |
| E3：缺少实体引用 Schema | ✅ 确认 | 无可用的 `embeds` 表；23 次迁移中不存在 |
| E4：文件模型缺少缩略图字段 | ✅ 确认 | `File` 模型中无 `ThumbnailKey`/`PreviewKey` |
| E5：无缩略图端点 | ✅ 确认 | `file.go` handler — 仅 `GET /files/:id/url`（原始文件 presigned URL） |

### 方向二：消息调度、草稿与发送控制

| 证据声明 | 验证 | 备注 |
|---------|--------|-------|
| E1：`Send` 无调度参数 | ✅ 确认 | `SendMessageInput` 无 `ScheduledAt`/`DraftID`；`Service` 接口无 `SendScheduled()`/`Recall()` |
| E2：Message 模型无调度字段 | ✅ 确认 | 无 `Status`、`ScheduledAt`、`ExpiresAt`、`RecalledAt` |
| E3：写入后立即分发 | ✅ 确认 | `createMessage()` — 在同一事务中写入 outbox + 消息 |
| E4：仓库无草稿/调度查询 | ✅ 确认 | `MessageRepository` 无 `ListDrafts`/`MarkRecalled` |
| E5：无撤回工作流 | ✅ 确认 | 仅 `SoftDelete`（发送方可见），语义与撤回不同 |

### 方向三：API 消费者契约与传输优化

| 证据声明 | 验证 | 备注 |
|---------|--------|-------|
| E1：无字段选择 | ✅ 确认 | 代码库中无 `FieldMask`、`fields` 参数或 `FieldSelector` 工具 |
| **E1 细节：File 响应包含 StorageKey/Checksum** | ⚠️ **轻微不准确** | `StorageKey` 和 `Checksum` **不**在 `fileResponse` DTO 中。仅 `StorageType` 出现在 HTTP `GET /files/:id` 和列表响应中。`StorageKey` 仅出现在 `PresignUploadResponse` 中（客户端直接上传需要）。这并不影响核心观点——不存在字段选择机制，且 `storage_type` 仍是不必要的——但证据应更新。 |
| E2：无批量操作 | ✅ 确认 | 没有任何端点采用 `batch-delete`、`BatchGetFiles` 或类似模式 |
| E3：无条件请求（ETag） | ✅ 确认 | `Get` handler 中无 `If-None-Match`/`ETag`；代码库中未提及 ETag |
| E4：列表接口不支持字段选择 | ✅ 确认 | 搜索也返回完整实体（`SearchHit` 是联合类型，返回完整的 `*Message`/`*Comment`/`*File`） |
| E5：HTTP 未压缩 | ✅ 确认 | `app.go` 中的 `http.Server` — 无 gzip/compress 中间件；`go.mod` 中存在 `gin-contrib/gzip`（或其他压缩库）？ |

**方向三多出的发现**：`internal/handler/http/router.go` — 中间件堆栈为 `RequestID → Tracing → Recovery → Metrics → AccessLog → CORS → Timeout`。在 `gin` 作为可选依赖（默认不包含）的情况下，gzip 很容易添加。同时，`internal/domain/file.go` — `CanReadFile()` 方法的存在表明，如果根据调用上下文提供功能，字段选择可能会与 ACL 逻辑交互。

### 方向四：实体变更历史与内容版本化

| 证据声明 | 验证 | 备注 |
|---------|--------|-------|
| E1：编辑是破坏性覆盖 | ✅ 确认 | `repository/comment.go:190` — `Updates(map[string]any{"content": content})` 覆盖；`repository/message.go:226` 同理 |
| E2：Message 模型有 EditedAt，无版本 | ✅ 确认 | 无 `PreviousVersions`/`EditHistory`；`Comment` 同理 |
| E3：文件内容不可覆盖更新 | ✅ 确认 | `FileService` 接口 — 无 `ReplaceContent`/`NewVersion`/`Overwrite` |
| E4：审计日志记录操作，不记录快照 | ✅ 确认 | `AuditEvent` — 无 `before`/`after` 内容快照字段 |
| E5：Proto 中无版本字段 | ✅ 确认 | `Message` proto — 无 `version`、`EditHistoryEntry`、`version_count` |

**方向四多出的发现**：`internal/domain/audit.go` — 审计操作常量非常全面，但没有一个包含 `message.edit` 或 `comment.edit`，因此审计日志甚至不记录编辑发生的时间。`internal/message/service.go` — `Edit()` 方法从不调用 `s.audit.Log()`。

### 方向五：用户数据可移植性与账户生命周期管理

| 证据声明 | 验证 | 备注 |
|---------|--------|-------|
| E1：无数据导出端点 | ✅ 确认 | 路由中无 `GET /v1/export` |
| E2：无账户删除端点 | ✅ 确认 | HTTP 路由或 gRPC proto 中无 `DELETE /v1/account` |
| E3：软删除数据无限累积 | ✅ 确认 | 23 次迁移使用 `deleted_at`；GC 仅处理文件（`gc/sweeper.go`） |
| E4：审计日志是 fire-and-forget | ✅ 确认 | 14 处出现 `_ = s.audit.Log(...)`（`internal/service/` 中 7 处，`internal/.../service.go` 中 7 处） |
| E5：无数据使用情况统计 | ✅ 确认 | 无 `GET /v1/account/usage` 端点 |

**方向五多出的发现**：`internal/repository/` 包含一个 `AuditRepository`，但审计日志记录的是操作而非状态（没有 `message.edit` 审计事件），因此合规时间线不完整。`internal/storage/gc/sweeper.go` — 仅处理文件软删除；消息、评论和通知的软删除永不清除。

---

## 架构师视角：未发现的问题

您提出的五个方向在架构上很合理。以下是我通过交叉验证证据发现的最佳补充：

### D1（富媒体嵌入）的架构附加项

> 注意：`storage_refs` 表（迁移 #0021）已经存在，用于通过引用计数处理 blob GC。嵌入解析器应**重用**此引用计数机制：当嵌入引用某个文件时，该文件的引用计数会增加，防止 GC 在嵌入预览仍活跃时回收它。

### D3（API 传输）的纠正项

> 关于 `StorageKey`/`Checksum` 准确性的说明：HTTP `fileResponse` DTO 仅暴露 `storage_type`（字符串），不暴露 `storage_key` 或 `checksum`。`PresignUploadResponse` 确实返回 `storage_key`，因为客户端需要知道将直接上传写入哪个键——这是设计所需，而非臃肿。然而，这个细节**不影响**方向三的论点：仍然缺少字段选择，`storage_type` 仍被浪费地序列化，并且基于时间戳的条件请求（使用 `updated_at` 作为隐式 ETag）完全缺失且将是轻量级添加。

### D4（版本化）的架构约束

当前代码库在 `internal/domain/comment.go` 中强制执行**两层嵌套限制**（`ErrNestingTooDeep`）。`comment_versions` 表必须保存版本号以及创建该版本的嵌套深度，以便回滚至层级结构不同的版本时不会破坏当前的嵌套检查。这是一个易被忽视的边界情况。

### D5（账户删除）的数据流复杂性

所有 14 个审计日志调用点均使用 `_ = s.audit.Log(...)`（fire-and-forget）。在账户删除工作流中，这是**存有隐患**的：如果审计日志在发出级联删除命令时静默失败，您可能会丢失谁删除过什么的可追溯记录。账户删除级联操作应使用与消息「标记已读」相同的模式（事务性 outbox 事件），而非 fire-and-forget 审计——以保障幂等性。

---

## 产品经理视角：优先级排序的说明

您的 P0→P2 优先级排序合理，但我将补充一个架构依赖：

| 方向 | 隐藏依赖 |
|------|----------------|
| D1（富媒体） | 依赖方向三（字段选择）来实现高效的嵌入序列化。如果嵌入预览数据作为 `embeds[]` 数组返回，且没有字段选择机制来跳过它，则小屏幕/低带宽场景下可节省的带宽将被浪费。 |
| D2（调度） | 依赖 D4（版本化）来实现已调度消息的「草稿→已调度→已发送」转换。草稿应隐式成为版本 1，始终可追溯。 |
| D5（数据可移植性） | 账户删除的级联逻辑应**在**消息Recall之上构建。同时拥有 Recall 和 Delete Account，而 Recall 不会在对方设备上隐藏消息，会导致用户体验混乱。 |

**建议的重新排序**：D3（HTTP 压缩 + ETag）= P0 是正确的——这本质上是 5 行中间件代码。其次，我建议将 D5（数据导出 + 使用情况仪表板）提前至 P0.5，因为它无需等待任何基础设施成果，并能立即提供合规性覆盖。先做「用户可移植性」，然后是「API 契约」，接着是「版本化」，最后是「富媒体」和「调度」。

---

## 总结

**整体质量**：您的分析文档在准确性方面得分很高。在已验证的 ~30 个代码级证据中，有 1 个略微不准确（方向三中 HTTP 响应暴露的字段），并且有 2 个重要的架构交叉依赖值得添加。文档对方向五中审计日志 fire-and-forget 模式的识别非常精确，并暴露了一个真实的合规性缺口。

证据链可信，架构师推理扎实，产品经理论证与当代平台期望保持一致。该文档为规划周期提供了扎实的起点。
