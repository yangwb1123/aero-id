# aero-id 扩展方向分析报告

> **日期**：2026-07-11
> **方法**：全量代码扫描 + 架构逆向分析 + 边界情况推演
> **前提**：所有 ROADMAP 五大方向（可观测性、SSO 韧性、文件管道、写入正确性、产品面）均已交付，当前 `main` 上有 22 个可逆迁移、38 个 gRPC RPC、SSE 推送、全文搜索、审核日志、存储 GC、Webhook 分发、OTel 追踪等完善功能集。

---

## 总体评价

代码库在架构质量、测试覆盖、功能完整度方面已达到相当成熟的生产水准。以下方向不是"修修补补"，而是在现有坚实基础上构筑**下一阶段**的能力。每个方向均附代码证据（当前 what）、为什么需要、以及建议的切入路径。

---

## 方向一：乐观并发控制（Entity Versioning）

### 当前状态

`message` 和 `comment` 均已支持编辑（`edited_at`），但没有任何冲突检测机制：

- `internal/message/message.go` 中 `UpdateContent(ctx, id, content, at)` 直接执行 `UPDATE`，不检查操作时的基线版本
- `internal/comment/service.go` 中 `Edit` 同样直接更新
- `files` 的 `UpdateMetadata` 同理
- 没有任何实体拥有版本号（`version`）或 `updated_at` 条件更新

**并发场景下的数据丢失链**：
1. 用户 A 读取消息 → 获得内容 "Hello"，edited_at = nil
2. 用户 B 读取消息（相同状态）
3. A 编辑为 "Hello World" → 成功
4. B 编辑为 "Bonjour" → 成功，**覆盖了 A 的编辑**，B 从未看到 A 的版本

这不仅是并发问题——对于多设备/多标签页用户来说，这是**现实的数据丢失路径**。

### 为什么需要

| 维度 | 说明 |
|------|------|
| **数据完整性** | 编辑功能已发布给用户，但无法保证编辑不会静默覆盖他人/他设备的并发变更 |
| **UX 信任** | 用户期望「最后保存者赢」至少能通过冲突提示获得保护，而非直接丢失 |
| **审计基线** | 审核日志记录了谁编辑了什么，但无法判断编辑时*基于哪个版本*，降低取证价值 |
| **架构匹配度** | 写入路径已使用 GORM 事务，增加 `version` 条件更新的成本极低 |

### 建议切入路径

1. **Domain 层**：在 `Message`、`Comment`、`File` 中增加 `Version int` 字段
2. **迁移**：添加 `version` 列，默认值 1
3. **Repository 层**：所有 `Update*` 方法附加 `WHERE version = ?`，返回 `RowsAffected`；`RowsAffected == 0` 时返回 `ErrConflict`
4. **Service 层**：将 `ErrConflict` 映射为 `409 Conflict` 响应，附带当前最新版本
5. **Handler 层**：请求体可选带 `expected_version`，缺失时跳过检查（向后兼容）

### 边界情况

- **首次写入**：Create 时 `version = 1`，旧客户端写入时 `expected_version` 为空 → 跳过检查
- **软删 + 重新创建**：软删后的 `version` 在硬删除前保持不变——如果后续反删除（undelete）场景出现，版本号需重置
- **批量操作**：`MarkAllRead` 不涉及版本冲突（无数据丢失风险），可豁免

---

## 方向二：群组会话（Multi-Party Chat）

### 当前状态

消息模型严格限制为 1:1：

```go
// internal/domain/message.go
type Message struct {
    FromUID string
    ToUID   string
    Kind    MessageKind  // 1=文本 2=图片 3=文件附件 4=系统通知
    ...
}
```

- `internal/message/message.go` 中 `Conversation(ctx, withUID, page)` 的参数单一 `withUID`
- `internal/repository/message.go` 中 `ListConversation` 查询 `WHERE ((from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?))`——完全面向 1:1
- 通知系统仅支持 `message.received`（单收件人）
- 但基础设施**已完备**：outbox、notification、block、SSE push 均可复用

### 为什么需要

| 维度 | 说明 |
|------|------|
| **产品跃升** | 从 1:1 私信跃迁到群聊是消息系统最自然的演进。单聊是基础设施，群聊是产品 |
| **基础设施复用率** | outbox 事件分发、通知推送、block 系统、全文搜索等现有组件几乎无需修改即可支撑群聊 |
| **架构预留** | 当前 `MessageKind` 已预留 `kind=4`（系统通知），可扩展 `kind=5`（群消息），不影响现有客户端 |
| **竞争差异化** | 大部分 IM 系统会自建全套，而 aero-id 已有 SSO 集成 + 完善的权限体系，多租户群聊有天然优势 |

### 建议切入路径

1. **新增 `conversations` 表**：`id, type(1:1|group), title, avatar, created_by, created_at, deleted_at`
2. **新增 `conversation_members` 表**：`conversation_id, uid, role(owner|admin|member), joined_at`
3. **消息模型扩展**：`messages.conversation_id` 可为 NULL（向后兼容 1:1），`to_uid` 在群聊场景下为空的广播语义
4. **通知适配**：`message.sent` 的 payload 扩展为支持批量收件人列表，通知创建时逐人展开（复用 `NotificationDispatcher` 的 block 和 preference 检查）
5. **API 新增**：`POST /v1/conversations`（创建）、`POST /v1/conversations/:id/messages`（发送）、`GET /v1/conversations`（列表）

### 边界情况

- **历史兼容**：已有 1:1 消息不迁移到 `conversation_id` 模型，新群聊走新表。list 接口 UNION 新旧两个来源
- **成员退出后内容可见性**：退出后应不可见后续消息，但历史消息保持可见（与 Slack/微信一致）
- **提及（@mention）**：群聊中 @user 需要通知特定成员而非全群——可以复用现有的 notification subject
- **管理员权限**：移出成员、修改群名称等操作需要角色化权限，复用现有的 `user_roles` 模式

---

## 方向三：按端点粒度限流与存储配额（Granular Quotas & Throttling）

### 当前状态

- `internal/pkg/ratelimit/`：全局 per-actor/per-IP token bucket，所有端点共享一个 RPS/Burst
- `configs/config.yaml`：仅有全局 `rate_limit.rps` 和 `rate_limit.burst`
- 文件上传：`storage.max_upload_bytes` 限制单文件大小，但**没有按用户的总存储配额**
- 消息发送：没有独立的发送频率限制（与消息读取共享全局桶）
- `internal/pkg/metrics/` 记录了请求量，但无法按用户追踪使用量

**具体缺失**：

1. 单个用户每天/每月能上传多少总字节 → 无限制
2. 单个用户每分钟能发送多少条消息 → 与读取共用全局桶
3. 单个用户能创建多少举报（`comment_reports`） → 无限制（可被用于骚扰被举报方）
4. 单个 IP 能创建多少并发的 SSE 连接 → 无限制

### 为什么需要

| 维度 | 说明 |
|------|------|
| **成本控制** | 文件存储有 GC 回收，但没有上游摄入截流。恶意用户可上传 TB 级数据在下次 GC 前耗尽 S3 预算 |
| **反滥用** | 全局桶意味着恶意发送者可以和正常读取者争抢配额；按端点隔离防止正常流量被拖垮 |
| **公平性** | 存储配额与 API 配额分离：一个用户可能耗尽存储却活跃于聊天，反之亦然 |
| **可运维性** | 出现生产事故时，无法单独对消息发送端点降级而不影响文件读取 |

### 建议切入路径

1. **配额表**：`user_quotas(uid, resource_type, used, limit, reset_at)`——`resource_type` 可为 `storage_bytes`、`messages_sent`、`reports_created` 等
2. **存储配额**：在 `file/service_crud.go` 的 `Create` 中，上传前检查 `used + size <= limit`；`Delete` 时递减用量
3. **端点级限流**：将全局 `ratelimit.Limiter` 替换为端点路由表，每端点可设置独立 RPS/Burst（或继承全局默认值），通过中间件配置注入
4. **SSE 连接限流**：在 `internal/handler/http/events.go` 的 `Stream` 中增加 per-uid 并发连接上限（如 `max 5`）
5. **用量暴露**：`GET /v1/me/quotas` 返回当前用量和限额，配合 `Retry-After` header

### 边界情况

- **配额重置**：`reset_at` 处理边界（UTC 日/月切换），避免多副本同时重置的竞态。建议用 GORM 的 `UPDATE ... SET used = 0 WHERE reset_at < now()` 惰性重置
- **超额删除**：用户删除文件后配额实时释放，允许重新上传（非严格周期制）
- **多租户**：如果未来支持组织（org），配额可能需要在组织级别和用户级别叠加
- **宽限期**：首次超配额应返回 429 + 错误体而非静默丢弃；连续超配额可考虑降级为 403

---

## 方向四：数据可移植性与批量操作（Data Portability）

### 当前状态

- 所有删除均为**软删除**（`gorm.DeletedAt`），数据物理上永不消失
- 存储 GC 可配置保留窗口后硬删除，但**没有按用户维度的全量数据导出或清除接口**
- 没有 `GET /v1/me/data` 返回用户的所有数据
- 没有 `DELETE /v1/me/account` 触发全账户数据的异步擦除
- 审核日志记录操作但不记录数据内容（只有 action + subject_id，没有内容的快照）

### 为什么需要

| 维度 | 说明 |
|------|------|
| **合规（GDPR / 个人信息保护法）** | 「数据可携带权」和「被遗忘权」是硬性要求。虽然 SSO 持有身份主数据，但 aero-id 存储用户产生的内容（文件、消息、评论），同样管辖范围内 |
| **用户信任** | 即使没有法律要求，提供一键导出/删除是建立用户信任的基本实践 |
| **账户迁移** | 用户可能希望将数据迁移到另一个服务或备份到本地 |
| **数据清理** | 软删除 + 存储 GC 的组合不适合处理"用户要求彻底删除所有数据"的场景——GC 有保留窗口，用户期望即时生效 |

### 建议切入路径

1. **异步导出**：
   - `POST /v1/me/export` 创建导出任务 → outbox 事件 `data.export.requested`
   - 新增 `export_tasks` 表：`id, uid, status(pending|processing|completed|failed), expires_at, storage_key`
   - 后台 worker（复用 outbox relay 模式）收集用户所有数据，打包为 `.zip/.jsonl` 存入 `storage`
   - `GET /v1/me/export/:id` 下载或轮询状态
2. **同步导出（轻量版）**：
   - `GET /v1/me/data?kinds=messages,comments,files` 直接流式返回 JSON 行——适合小数据量用户
3. **账户删除**：
   - `POST /v1/me/erase` → 接收 `confirmation_token`（短信/邮件验证，需 SSO 配合）
   - 确认后触发异步任务：硬删除所有 files（+ blob 删除）、messages、comments、notifications、preferences、blocks、outbox 事件
   - 审核日志保留（保留审计链，但脱敏 actor_uid → hash）
4. **GC 兼容**：存储 GC 的 `Retention` 配置应对导出/擦除请求豁免——立即硬删而非等待窗口

### 边界情况

- **大文件导出超时**：超过 100MB 的导出必须走异步路径，同步接口返回 `202 Accepted + location header`
- **导出中途数据变更**：导出开始时快照当前状态（`SELECT ... WHERE deleted_at IS NULL AND created_at <= snapshot_time`），不一致但可接受
- **部分删除**：用户可能只想删除消息历史而非文件——擦除接口应支持 `kinds` 参数
- **取消导出**：导出进行中用户可以取消（`DELETE /v1/me/export/:id`），但已写入 blob 的部分需要 GC 清理
- **并发擦除请求**：幂等性保证——同用户第二次擦除时 `files` 表已为空，返回 `200` 而非 `404`

---

## 方向五：异步文件处理管道（Async File Processing Pipeline）

### 当前状态

文件上传后立即落库，没有任何后处理：

- `internal/file/service_upload.go` 中 `CompleteUpload` 调用 `store.Stat` 检查大小，然后直接写行——无校验和计算、无内容扫描、无格式转换
- `internal/file/service_crud.go` 中 `Create` 虽然计算了 SHA256 并做内容去重，但**没有将 checksum 用于后续处理**
- outbox 系统仅在消息/评论域触发通知事件，**没有扩展文件域**
- 虽然有 `file.meta` JSONB 字段预留扩展，但生产环境没有写入任何后处理元数据

### 为什么需要

| 维度 | 说明 |
|------|------|
| **安全** | 上传的图片/文档可能含恶意内容。现在的 MIME allow-list 只检查 Content-Type header，不防伪造（用户可将 .exe 重命名为 .jpg） |
| **UX** | 图片上传后没有缩略图预览；文档列表只有文件名没有摘要 |
| **去重增强** | `Create` 的 checksum 去重仅在单次上传时生效，`CompleteUpload`（presigned 路径）完全跳过 checksum——S3 预签名上传没有后续验证 |
| **架构对齐** | outbox 已为异步处理建立了完整的基础设施（事件存储 + 重试 + dead-letter + 指标），文件处理是其最自然的扩展消费者 |

### 建议切入路径

1. **新增事件类型**：`file.uploaded` 由 `Create`/`CompleteUpload` 写入 outbox。Payload 包含 `{ file_id, storage_key, mime_type, size, checksum, owner_uid }`
2. **处理流水线 Handler**：
   - **MIME 验证**：下载前几个字节检测 magic number，拒绝 header 与 Content-Type 不符的文件（软拒绝 → 将文件标记为 `meta.suspicious = true`，通知管理员）
   - **缩略图生成**：图片类 → 生成 `256x256` WebP 缩略图，存入 `thumbnails/<key>.webp`
   - **病毒扫描**：集成 ClamAV 或第三方 API（如 VirusTotal），结果写入 `meta.scan_status` / `meta.scan_result`
   - **文本提取**：PDF/DOCX → 提取纯文本到 `meta.extracted_text`，供搜索索引使用（增强现有 tsvector 搜索）
3. **缩略图存储**：新增 `FileStorage.GetThumbnail(key)` 方法或使用约定 key 路径 `thumbnails/<key>`
4. **进度状态**：`file.meta.processing_status` 跟踪 `pending → processing → completed | failed`
5. **回填**：为已有存量文件触发 `file.uploaded` 事件（通过管理命令或迁移脚本）

### 边界情况

- **处理失败不影响可用性**：缩略图生成失败时，原始文件仍可正常下载；`meta.processing_status = "failed"` + `meta.processing_error`
- **超大文件跳过处理**：>100MB 的视频不做缩略图（或降级为仅提取第一帧）；可在 `Config` 中设 `processing.skip_beyond_bytes`
- **并发处理**：同一文件可能被多个处理器同时看到（罕见），缩略图写入应使用原子对象存储（先检查存在再写入）
- **处理时长超出 outbox 重试窗口**：异步处理（如病毒扫描）可能耗时 >30 秒，不适合在 relay handler 的同步 `Handle` 调用中完成。应通过 outbox 事件触发轻量级 worker，worker 再将耗时任务提交给内部 goroutine pool 或外部队列

---

## 附录 A：其他值得关注的较小缺口

以下方向价值明确但优先级低于上述五个，记录供后续评估：

| # | 缺口 | 当前证据 | 建议 |
|---|------|----------|------|
| A1 | 通知静默周期时区处理歧义 | `internal/service/preference.go` 中 `quiet hours` 跨午夜判断条件存在边界模糊 | 改用 UTC 时间存储 + 仅比较分钟偏移量 |
| A2 | SSE 连接泄漏防护 | `internal/hub/hub.go` 中 slow consumer 丢弃事件但不主动断开 | 加 `max sub duration / idle timeout` 自动清理僵尸连接 |
| A3 | 评论举报接口未对接通知 | 举报后被举报方/管理员无法收到通知 | 新增 `comment.reported` outbox 事件 → 通知作者和管理员 |
| A4 | 删除已分享文件的级联行为 | `internal/file/service_crud.go` 中 `Delete` 不清理 `file_shares` 行 | 级联或在分享表上加外键 `ON DELETE CASCADE` |
| A5 | 仅 local 有 PresignGet，S3 没有独立实现 | `internal/storage/s3.go` 的 PresignGet 直接返回 ErrNotSupported | 实现 S3 PresignGet 以支持 mode=presign 的统一体验 |

---

## 附录 B：排序建议

| 优先级 | 方向 | 理由 |
|--------|------|------|
| **P0** | 乐观并发控制 | 数据正确性问题，已上线的编辑功能存在静默覆盖风险 |
| **P1** | 按端点粒度限流与存储配额 | 生产成本和反滥用硬需求，现有全局限流不够 |
| **P1** | 数据可移植性 | GDPR/合规硬要求，用户数据所有权的基本实践 |
| **P2** | 群组会话 | 高产品价值，但实现复杂度较高；建议在上述基础打牢后再投入 |
| **P2** | 异步文件处理管道 | 高安全/UX 价值，但可通过前置 MIME 魔数检查做最低安全防护（较低成本） |

---

*本报告基于对 `main` 分支（07b8e2a~）的全面静态分析生成。建议在切入每个方向前补充对应的集成测试计划。*
