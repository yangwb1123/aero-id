Now I have a thorough understanding of the codebase. Let me compile the complete Tech Lead analysis.

---

# Tech Lead 分析报告

## 1. 任务分解

每个任务设计为 **2-4 小时** 可完成，以保持进度可追踪和风险可控。

---

### 方向 1：大文件端到端完整性验证（P0）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---------|---------|---------|---------|---------|---------|
| **TASK-001** | `File` domain 模型新增 `ChecksumSHA256` 字段 | `internal/domain/file.go`, `internal/file/file.go` | 无 | 2h | `File.ChecksumSHA256` 字段定义，JSON tag，`Store` 字段列表包含该字段 |
| **TASK-002** | 数据库迁移：files 表添加 `checksum_sha256` 列 | `migrations/000024_checksum_sha256.up.sql`, `migrations/000024_checksum_sha256.down.sql` | TASK-001 | 2h | `make migrate-up` 后 `files` 表存在可为空的 `checksum_sha256 text` 列，回滚正常 |
| **TASK-003** | Repository：读写 `ChecksumSHA256` 字段 | `internal/file/repository.go`, `internal/repository/file.go` | TASK-002 | 2h | `Create`/`Get` 正确持久化和读取 checksum；`FindByChecksum` 新增 `ChecksumSHA256` 查询分支 |
| **TASK-004** | `CompleteUpload` 接收 `expected_checksum` 参数并在服务层验证 | `internal/file/service_upload.go`, `internal/file/file.go`, `internal/service/file_presign.go` | TASK-001, TASK-003 | 3h | 传入 `expected_checksum` 时，读取已上传对象计算 SHA-256 对比；不匹配返回 400；空值跳过验证（向后兼容） |
| **TASK-005** | `CompleteUpload` 去重（presigned 路径使用 checksum 匹配） | `internal/file/service_upload.go` | TASK-004 | 3h | 同一 checksum 的文件通过 presigned 上传后，第二次上传复用 storage_key（refcount +1）；与 `Create` 路径行为一致 |
| **TASK-006** | HTTP + gRPC handler：透传 `expected_checksum` 参数 | `internal/file/handler_http.go`, `internal/file/handler_grpc.go`, `internal/handler/http/file.go`, `internal/handler/grpc/file.go` | TASK-004 | 2h | HTTP `POST /v1/files/upload-complete` 和 gRPC `CompleteUpload` 请求体接受 `checksum_sha256` 字段 |
| **TASK-007** | 单元测试：checksum 验证 + 去重逻辑 | `internal/file/service_test.go`, `internal/service/file_test.go` | TASK-005 | 3h | 覆盖：checksum 匹配成功、不匹配失败、空值跳过、去重命中、去重后第一个文件删除后第二个文件不被误删 |
| **TASK-008** | 集成测试：presigned 路径端到端 checksum 验证 | `internal/repository/file_integration_test.go` (或新建 `internal/file/file_integration_test.go`) | TASK-006 | 3h | 启动 testcontainers，完整走通 presign → 上传 → complete 流程，验证 checksum 在实际 S3/MinIO 上一致 |

**总工时：方向 1 = 20h**

---

### 方向 2：多设备同步与会话管理（P1）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---------|---------|---------|---------|---------|---------|
| **TASK-009** | 数据库迁移：创建 `user_devices` 表 | `migrations/000025_user_devices.up.sql`, `migrations/000025_user_devices.down.sql` | 无 | 2h | 表含 `uid`, `device_id`, `device_name`, `push_token`, `push_provider`, `last_seen_at`, `created_at`，PK 为 (uid, device_id) |
| **TASK-010** | Domain + Repository：设备增删查 | `internal/domain/file.go` → 新建 `internal/domain/device.go`, `internal/file/repository.go` → 新建 `internal/device/repository.go`, `internal/repository` 新增 `device.go` | TASK-009 | 3h | `Device` 实体与 `DeviceRepository` 接口定义；`Create`/`GetByUID`/`Delete`/`ListByUID` 实现 |
| **TASK-011** | 设备注册 API：HTTP + gRPC handler + service | `internal/device/handler_http.go`, `internal/device/handler_grpc.go`, `internal/device/service.go`, `internal/service/mocks/mocks.go` | TASK-010 | 4h | `POST /v1/devices` (注册), `GET /v1/devices` (列表), `DELETE /v1/devices/{device_id}` (登出)；统一 service 层 |
| **TASK-012** | 已读状态跨设备同步事件 | `internal/message/service.go`, `internal/notification/dispatcher.go`, `internal/outbox/outbox.go` | TASK-011 | 3h | `messages.mark_read` 事务中 outbox 发出 `message.read_cross_device` 事件；dispatcher 将该事件推送到该 uid 的所有 SSE 连接 |
| **TASK-013** | 通知已读跨设备同步 | `internal/notification/service.go`, `internal/notification/dispatcher.go` | TASK-012 | 2h | `MarkAllRead` 和 `MarkRead` 通过 outbox 发出 `notification.marked_read` 事件，SSE 实时推送 |
| **TASK-014** | SSE `session.revoked` 事件（退出所有设备） | `internal/hub/hub.go`, `internal/device/service.go`, `internal/handler/http/events.go` | TASK-011 | 2h | `DELETE /v1/devices` 被调用清空设备时，向该 uid 活跃 SSE 连接推送 `session.revoked` |
| **TASK-015** | Push Notification Handler（FCM/APNs 适配器 stub） | `internal/device/push.go`, `internal/outbox/chain.go` | TASK-011 | 4h | 新的 outbox handler `pushHandler` 注册到 chain；收到 `message.sent`/`notification.*` 事件时查询 user_devices 的 push_token；FCM/APNs 调用抽象为接口，有 mock 实现用于测试 |
| **TASK-016** | Push Token 过期标记（Push provider 返回非 200 时标记 inactive） | `internal/device/push.go`, `internal/device/repository.go` | TASK-015 | 2h | Push 发送失败时调用 `MarkDeviceInactive`，后续不再向 inactive 设备推送 |
| **TASK-017** | 跨设备同步 + Push 单元测试 | `internal/device/service_test.go`, `internal/notification/dispatcher_test.go`, `internal/outbox/chain_test.go` | TASK-016 | 3h | 覆盖：设备注册/列表/删除、跨设备心跳、Push handler 正常/失败路径 |

**总工时：方向 2 = 25h**

---

### 方向 3：消息附件生命周期管理（P1）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---------|---------|---------|---------|---------|---------|
| **TASK-018** | 数据库迁移：创建 `message_files` 快照表 | `migrations/000026_message_files.up.sql`, `migrations/000026_message_files.down.sql` | 无 | 2h | 表含 `message_id`, `file_id`, `snapshot_name`, `snapshot_mime`, `snapshot_size`, `snapshot_storage_key`, `created_at`；PK 为 message_id |
| **TASK-019** | Domain + Repository：消息附件快照 | 新建 `internal/domain/message_file.go`, `internal/message/repository.go` 新增接口，`internal/repository` 新增 `message_file.go` | TASK-018 | 3h | `MessageFileSnapshot` 实体定义；`CreateSnapshot`/`GetByMessageID`/`DeleteByMessageID` 实现 |
| **TASK-020** | 发送消息时生成附件快照 | `internal/message/service.go`（`Send` 方法） | TASK-019 | 3h | 发送消息时若 `file_id` 非空，复制文件元数据到 `message_files` 快照表；文件删除后消息仍能展示附件信息 |
| **TASK-021** | 消息附件优雅降级（文件删除后占位展示） | `internal/message/service.go`（`Get`/`ListConversation` 方法）, `internal/domain/message.go` | TASK-020 | 3h | 消息的 `file_id` 引用已删除文件时，返回 `deleted_attachment` 占位信息（含快照中的文件名/类型），不返回 404 |
| **TASK-022** | `file_service.Delete` 检查消息引用（防止误删） | `internal/file/service_crud.go`, `internal/file/repository.go`（新增 `CountByFileID`） | TASK-020 | 3h | 删除文件时检查 `message_files` 表，若有引用则返回 `409 Conflict`（或标记为 orphan）；提供 force 参数 |
| **TASK-023** | 缩略图生成 worker（image/* 类型） | `internal/file/thumbnail.go`, 新文件 `internal/file/thumbnail_worker.go` | TASK-020 | 4h | 图片上传完成后异步生成 128×128/256×256 缩略图；worker 通过 outbox 事件触发；缩略图可配置存储位置 |
| **TASK-024** | 缩略图 storage GC 保护 | `internal/storage/gc/sweeper.go` | TASK-023 | 2h | GC 扫描时不回收仍有原始文件引用的缩略图；缩略图与原文件同生命周期管理 |
| **TASK-025** | 单元测试 + 集成测试：附件快照 + 缩略图 | `internal/message/service_test.go`, `internal/file/service_test.go`, `internal/repository/message_file_integration_test.go` | TASK-024 | 3h | 覆盖：快照创建/读取、文件删除后消息展示、缩略图生成回调、引用计数冲突 |

**总工时：方向 3 = 23h**

---

### 方向 4：分布式速率限制与滥用检测（P0）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---------|---------|---------|---------|---------|---------|
| **TASK-026** | 定义 `Limiter` 接口并抽取现有 memory 实现 | `internal/pkg/ratelimit/ratelimit.go`（重构为接口 + `memoryLimiter` 结构体） | 无 | 2h | 现有 `Limiter` 变为接口，`memoryLimiter` 实现它；现有测试全部通过不做行为变更 |
| **TASK-027** | 新增 `redisLimiter` 实现（sliding window / GCRA） | `internal/pkg/ratelimit/redis.go`, `internal/pkg/ratelimit/redis_test.go` | TASK-026 | 4h | GCRA 算法实现，通过 Redis 的 `TIME` 统一时钟；配置化限流参数映射到 Redis key |
| **TASK-028** | 配置切换：`type: memory | redis` | `internal/config/config.go`（新增 `RateLimit.Type`, `Redis` 配置块） | TASK-027 | 2h | `rate_limit.type: memory` 使用现有行为；`type: redis` 初始化 redisLimiter；无 Redis 时启动失败（或降级到 memory 并记录警告） |
| **TASK-029** | 中间件注入：`redisLimiter` 支持 | `internal/middleware/ratelimit.go`, `internal/app/app.go` | TASK-028 | 2h | `RateLimit` 中间件接受 `Limiter` 接口（而非 `*ratelimit.Limiter`）；app wiring 根据配置选择实现 |
| **TASK-030** | Per-endpoint 差异化限流配置 | `internal/config/config.go`, `internal/middleware/ratelimit.go` | TASK-028 | 3h | 配置支持 `rate_limit.endpoints.POST /v1/comments/*/report: {rps: 2}`；中间件根据请求方法和路径匹配端点配置 |
| **TASK-031** | 举报滥用检测（reporter_uid 维度速率限制） | `internal/comment/service.go`（新增 `reportRateLimit` 检查）, `internal/service/comment.go` | TASK-029 | 3h | 同一 reporter_uid 每分钟最多 N 条举报（可配置）；超出后返回 `429 Too Many Reports` |
| **TASK-032** | Like/Unlike 反转检测（防抖） | `internal/comment/service.go`, `internal/message/service.go` | 无 | 2h | 同一用户对同一对象的 like/unlike 间隔 < 3s 时忽略（幂等）；可配置窗口大小 |
| **TASK-033** | 内容 Spam 评分接口 + 关键词黑名单 | `internal/comment/spam.go`, `internal/message/spam.go`, `internal/domain/spam.go` | 无 | 4h | `SpamScorer` 接口（`Score(ctx, text) (float64, reason)`）；关键词/正则实现；高于阈值自动隐藏 + 创建举报 |
| **TASK-034** | Redis 连接池和健康检查 | `internal/pkg/ratelimit/redis_pool.go`, `internal/app/app.go` | TASK-027 | 2h | Redis 连接池初始化；`/readyz` 包含 Redis 探针（配置 `readiness_critical`） |
| **TASK-035** | 集成测试：分布式限流行为 | `internal/middleware/rate_limit_integration_test.go`（新建） | TASK-030 | 3h | 启动 testcontainers Redis，验证多 key 限流、burst、per-endpoint 差异化配置 |
| **TASK-036** | 滥用检测单元测试 | `internal/comment/service_test.go`, `internal/message/service_test.go` | TASK-033 | 2h | 覆盖：举报频率限制、Like 防抖、Spam 评分阈值判定 |

**总工时：方向 4 = 29h**

---

### 方向 5：对话抽象与群组消息（P2）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---------|---------|---------|---------|---------|---------|
| **TASK-037** | 数据库迁移：建 `conversations` + `conversation_members` + `conversation_read_cursors` 表 | `migrations/000027_conversations.up.sql`, `migrations/000027_conversations.down.sql` | 无 | 3h | 三张表完整定义（含 FK、索引），回滚正常 |
| **TASK-038** | Domain：`Conversation`、`ConversationMember`、`ConversationReadCursor` 实体 | 新建 `internal/domain/conversation.go` | TASK-037 | 2h | 实体定义含类型枚举（1:1/group）、JSON tag |
| **TASK-039** | Repository：Conversation CRUD + member 管理 | 新建 `internal/conversation/repository.go`, `internal/repository/conversation.go` | TASK-038 | 4h | `Create`/`Get`/`ListByUID`/`Update`/`AddMember`/`RemoveMember` |
| **TASK-040** | 数据迁移工具：已有 1:1 消息自动创建 conversation | 新建 `internal/conversation/migrate.go` + `internal/conversation/migrate_test.go` | TASK-039 | 4h | 扫描现有 `messages` 表，为每一对 (from_uid, to_uid) 创建 1:1 conversation；幂等（可重复运行） |
| **TASK-041** | 消息模型新增 `conversation_id`（替代 from/to 作为路由） | `internal/domain/message.go`, `internal/message/repository.go`, `internal/repository/message.go` | TASK-040 | 3h | `Message.ConversationID` 字段；写入时自动关联 conversation；迁移旧数据填充 conversation_id |
| **TASK-042** | 会话级操作 API（创建/列表/更新/成员管理） | 新建 `internal/conversation/handler_http.go`, `internal/conversation/handler_grpc.go`, `internal/conversation/service.go` | TASK-041 | 4h | `POST /v1/conversations`, `GET /v1/conversations`, `PATCH /v1/conversations/{id}`, `POST /v1/conversations/{id}/members`, `DELETE /v1/conversations/{id}/members/{uid}` |
| **TASK-043** | 群聊消息发送（写入扩散） | `internal/message/service.go`（`Send` 方法重构） | TASK-042 | 4h | `Send` 支持 `ConversationID` 参数；群聊消息写入一次（不在 conversation_members 维度复制）；每个成员的已读位置由 `conversation_read_cursors` 追踪 |
| **TASK-044** | 会话列表（含最后一条消息摘要 + 未读数） | `internal/conversation/service.go`, `internal/conversation/repository.go` | TASK-043 | 3h | `GET /v1/conversations` 返回 `last_message_preview` + `unread_count`；支持分页（cursor-based） |
| **TASK-045** | 群聊消息撤回（5min 窗口） | `internal/message/service.go`, `internal/conversation/service.go` | TASK-043 | 3h | 发送者 5min 内可撤回；撤回后对所有成员显示"消息已撤回" |
| **TASK-046** | 旧 API 兼容层（`Inbox`/`Conversation` 标记 deprecated） | `internal/message/handler_http.go`, `internal/message/handler_grpc.go`, `internal/handler/http/message.go`, `internal/handler/grpc/message.go` | TASK-044 | 2h | 旧 `Inbox` 路由返回会话维度摘要（而非消息行）；添加 `Deprecated` 响应头 |
| **TASK-047** | 大群性能测试 + 集成测试 | 新建 `internal/conversation/conversation_integration_test.go`, 补充 `internal/message/service_test.go` | TASK-046 | 4h | 500 人群聊发送消息性能基线；完整集成测试覆盖：建群/加人/退群/发消息/撤回/已读追踪 |

**总工时：方向 5 = 36h**

---

### 总计

| 方向 | 任务数 | 总工时 |
|------|--------|--------|
| #1 文件完整性验证 | 8 | 20h |
| #2 多设备同步 | 9 | 25h |
| #3 消息附件生命周期 | 8 | 23h |
| #4 分布式速率限制 | 11 | 29h |
| #5 对话抽象与群组 | 11 | 36h |
| **Total** | **47** | **133h** |

---

## 2. 执行顺序

```mermaid
graph TD
    %% ===== 并行轨道 =====
    subgraph TrackA ["轨道 A：核心里程碑 (P0)"]
        T001[TASK-001<br/>Domain: ChecksumSHA256]
        T002[TASK-002<br/>Migration: checksum column]
        T003[TASK-003<br/>Repository: read/write checksum]
        T004[TASK-004<br/>CompleteUpload: verify checksum]
        T005[TASK-005<br/>Presigned dedup via checksum]
        T006[TASK-006<br/>Handler: pass-through checksum]
        T007[TASK-007<br/>Unit tests: checksum+dedup]
        T008[TASK-008<br/>Integration tests: end-to-end]

        T026[TASK-026<br/>Refactor Limiter interface]
        T027[TASK-027<br/>Redis limiter (GCRA)]
        T028[TASK-028<br/>Config: type switching]
        T029[TASK-029<br/>Middleware inject interface]
        T030[TASK-030<br/>Per-endpoint rate config]
        T034[TASK-034<br/>Redis pool + health check]
        T035[TASK-035<br/>Integration: distributed rate limit]

        T031[TASK-031<br/>Report abuse detection]
        T032[TASK-032<br/>Like/Unlike debounce]
        T033[TASK-033<br/>Spam scorer + keyword filter]
        T036[TASK-036<br/>Abuse detection tests]
    end

    subgraph TrackB ["轨道 B：用户体验增强 (P1)"]
        T009[TASK-009<br/>Migration: user_devices]
        T010[TASK-010<br/>Device domain + repository]
        T011[TASK-011<br/>Device registration API]
        T012[TASK-012<br/>Read status cross-device]
        T013[TASK-013<br/>Notification cross-device]
        T014[TASK-014<br/>session.revoked event]
        T015[TASK-015<br/>Push notification handler]
        T016[TASK-016<br/>Push token expiry handling]
        T017[TASK-017<br/>Device sync tests]

        T018[TASK-018<br/>Migration: message_files]
        T019[TASK-019<br/>Message file snapshot domain]
        T020[TASK-020<br/>Snapshot on send]
        T021[TASK-021<br/>Graceful degradation]
        T022[TASK-022<br/>File delete protect]
        T023[TASK-023<br/>Thumbnail worker]
        T024[TASK-024<br/>Thumbnail GC protection]
        T025[TASK-025<br/>Attachment tests]
    end

    subgraph TrackC ["轨道 C：架构重构 (P2)"]
        T037[TASK-037<br/>Migration: conversations]
        T038[TASK-038<br/>Conversation domain]
        T039[TASK-039<br/>Conversation repository]
        T040[TASK-040<br/>1:1 migration tool]
        T041[TASK-041<br/>Message: conversation_id]
        T042[TASK-042<br/>Conversation API]
        T043[TASK-043<br/>Group send (fan-out)]
        T044[TASK-044<br/>Conversation list with summary]
        T045[TASK-045<br/>Message recall]
        T046[TASK-046<br/>Legacy API compat]
        T047[TASK-047<br/>Large group perf test]
    end

    %% 方向 1 依赖链
    T001 --> T002 --> T003 --> T004 --> T005 --> T006 --> T007 --> T008

    %% 方向 4 依赖链
    T026 --> T027 --> T028 --> T029 --> T030
    T028 --> T034 --> T035
    T029 --> T031
    T029 --> T032
    T032 --> T033 --> T036
    T030 --> T035

    %% 方向 2 依赖链
    T009 --> T010 --> T011 --> T012
    T011 --> T014
    T012 --> T013
    T011 --> T015 --> T016
    T012 --> T015
    T016 --> T017

    %% 方向 3 依赖链
    T018 --> T019 --> T020 --> T021
    T020 --> T022
    T020 --> T023 --> T024
    T021 --> T025

    %% 方向 5 依赖链
    T037 --> T038 --> T039 --> T040 --> T041 --> T042 --> T043 --> T044 --> T045 --> T046 --> T047

    %% 跨轨道依赖（方向 2 和 3 依赖方向 1 的基础设施）
    T005 --> T020   %% 去重能力被附件快照复用
    T030 --> T031   %% per-endpoint ratelimit 配置用于 abuse detection

    %% 跨轨道依赖（方向 5 依赖方向 3 的附件基础设施）
    T020 --> T043   %% 群聊消息发送需要附件快照能力
```

### 并行执行组

| 并行组 | 包含任务 | 条件 |
|--------|---------|------|
| **Group 1** (Week 1-2) | **Track A**: T001→T008 + **Track B 独立任务**: T009, T018 | Track A 和 Track B 的 migration 任务无依赖关系，可并行 |
| **Group 2** (Week 2-4) | T026→T030 (限流重构) + T010→T011 (设备注册) + T019→T020 (附件快照) | 限流重构与设备/附件完全独立 |
| **Group 3** (Week 4-6) | T031→T036 (滥用检测) + T012→T017 (跨设备同步) + T021→T025 (附件降级+缩略图) | 三个子轨道无交叉依赖 |
| **Group 4** (Week 6-10) | **Track C**: T037→T047 串行执行 | 方向 5 内部依赖紧密，需要串行 |

---

## 3. 技术风险

### 3.1 关键技术风险矩阵

| # | 风险描述 | 影响面 | 概率 | 严重度 | 缓解策略 |
|---|---------|-------|------|--------|---------|
| **R1** | Redis 成为单点故障（分布式限流方向 #4） | 方向 4 | 中 | 高 | T028 已规划 memory fallback；Redis 仅用于限流，不可用时降级到 memory 模式并记录告警；`/readyz` 增加 Redis 探针 |
| **R2** | Presigned 上传大文件的 checksum 计算成本高（方向 #1） | 方向 1 | 高 | 中 | 对大文件（>5GB multipart）不完整的 ETag → 客户端在 `x-amz-tagging` 传 SHA-256；服务端通过 S3 Select / 范围读取最后一块验证；或异步验证（TASK-004 已考虑范围读取方案） |
| **R3** | 群聊方向 #5 与现有消息模型的 schema 变更冲突 | 方向 5 | 高 | 高 | 需要 1:1 消息到 conversation 的数据迁移（TASK-040），迁移期间需停写或双写；建议在低峰期执行，有回滚方案 |
| **R4** | Push Notification 通道的集成复杂度（方向 #2） | 方向 2 | 中 | 中 | FCM/APNs 需要外部服务凭证，测试困难；建议：初期只做 SSE 同步（无 Push），Push 作为独立阶段，抽出 `PushProvider` 接口方便 mock |
| **R5** | 缩略图 worker 的存储膨胀（方向 #3） | 方向 3 | 中 | 低 | 单张图片生成 2 个缩略图（128×128 + 256×256）；每张 ~10-50KB；100 万张图片约 20-100GB；可配置是否启用+格式选择（webp > jpeg） |
| **R6** | 举报 / Like 滥用检测的误判（方向 #4） | 方向 4 | 中 | 中 | Spam 评分阈值过低可能导致正常用户被限；建议初期阈值保守（仅标记不自动隐藏），人工 review 数据后调整 |
| **R7** | Go 1.26 依赖 + protobuf 生成工具版本锁定 | 全局 | 低 | 高 | `protoc-gen-go@v1.34.1` 和 `protoc-gen-go-grpc@v1.5.1` 已固定；新增 proto 定义时需要确保 CI 环境安装了 protoc |
| **R8** | 跨设备同步事件可能导致 SSE 连接流量放大 | 方向 2 | 低 | 中 | 已读事件在 5 分钟内折叠（outbox chain 已有 `collapse` 窗口）；Push Notification 作为非关键通道 |

### 3.2 外部依赖评估

| 依赖 | 方向 | 用途 | 是否必需 | 替代方案 |
|------|------|------|---------|---------|
| **Redis** | #4 | 分布式 token bucket | 否（可用 memory fallback） | 单实例部署可跳过 Redis |
| **FCM/APNs** | #2 | Push Notification | 否（SSE 是主通道） | 只做 SSE 同步，Push 延后 |
| **S3 Select / 范围读取** | #1 | 大文件 checksum 验证 | 否 | 客户端主动提供 checksum，服务端用 Stat 验证 ETag |
| **MinIO / real S3** | #1 | 集成测试 | 是（testcontainers 已有） | 已在集成测试中使用 |

### 3.3 性能瓶颈与优化策略

| 场景 | 瓶颈 | 优化策略 |
|------|------|---------|
| 分布式限流（Redis） | 每个请求一个 RTT + Redis `INCR` | 批处理限流检查（不推荐/精度损失）；Redis 集群分片 |
| 群聊消息写扩散 | 500 人群 `conversation_read_cursors` 更新 | 批量 upsert + 异步更新未读数 |
| 缩略图生成 | 大图片处理耗时（>5s） | 异步 worker 处理，不阻塞 API 响应 |
| SSE 跨设备同步 | 同一 uid 多设备高频率已读事件 | 折叠窗口（5min）+ 限流（per-uid）；慢消费者丢事件 |
| Spam 评分 | 每条消息/评论都做评分 | 短文本（<100 字）用关键字匹配；长文本才调用 ML；可配置评分阈值 |

### 3.4 测试覆盖难点

| 难点 | 涉及方向 | 策略 |
|------|---------|------|
| Push Notification 测试 | #2 | 接口抽象 + mock provider；只有集成测试才连接真实 FCM sandbox |
| 分布式限流竞争条件 | #4 | 用 Redis 事务（MULTI/EXEC）或 Lua 脚本保证原子性；单元测试注入 clock |
| 大 checksum 验证 | #1 | mock storage 的 Stat 返回模拟 ETag；集成测试在 MinIO 上测试 1GB+ 文件 |
| 群聊并发发消息 | #5 | 使用 `go test -race` 检测并发安全；PostgreSQL 的 `SELECT FOR UPDATE` 处理 read_cursor 竞争 |

---

## 4. 资源评估

### 4.1 开发团队建议

| 角色 | 数量 | 技能要求 | 负责方向 |
|------|------|---------|---------|
| **Senior Go Engineer (Tech Lead)** | 1 | Go 熟练、系统架构设计、gRPC/PostgreSQL 经验、Redis 经验 | 架构决策、代码审查、方向 4 + 5 核心实现 |
| **Go Backend Engineer** | 1-2 | Go、GORM、PostgreSQL、REST + gRPC | 方向 1 + 2 实现 |
| **Go Backend Engineer** | 1 | Go、文件存储系统、异步 worker 模式 | 方向 3 实现 |
| **QA Engineer** | 1 | 集成测试（testcontainers）、性能测试、CI/CD | 所有方向的测试 + 性能基线 |

**最小可行团队**：2 名 Go 开发者（含 1 名 Senior）+ 1 名 QA（兼职）。

### 4.2 关键里程碑

| 里程碑 | 时间 | 交付物 | 验收标准 |
|--------|------|--------|---------|
| **M1: P0 核心交付** | 第 3 周结束 | 方向 1 + 4 完成 `make test && make lint` | presigned 上传有 checksum 验证；分布式限流可用；Redis 集成测试通过 |
| **M2: P1 Alpha** | 第 6 周结束 | 方向 2 + 3 核心路径完成 | 设备注册/列表/删除 API 可用；跨设备已读同步演示通过；附件快照创建 + 文件删除优雅降级 |
| **M3: P2 Internal Beta** | 第 10 周结束 | 方向 5 完成 + 全部方向集成测试 | 群聊创建/发消息/撤回/列表可用；1:1 旧数据迁移完成；旧 API 兼容 |
| **M4: 发布准备** | 第 12 周结束 | 性能测试报告 + 文档 + CHANGELOG | P0 方向在生产环境 7 天无 P0 告警；Redis 高可用方案文档化 |

### 4.3 阻塞点与解决策略

| 阻塞点 | 影响 | 解决策略 |
|--------|------|---------|
| **Redis 基础设施未就绪** | 方向 4 Blocked | 前期完全用 memory 模式开发（TASK-026），Redis 实现（TASK-027）在 Redis 就绪后 T 天内完成切换 |
| **Protobuf 工具链版本冲突** | 新的 gRPC 服务定义 | 使用 `go generate` + docker 化 protoc 环境；`Makefile` 中固定版本 |
| **Push Notification 证书配置** | 方向 2 Push 功能 | 初期 SSE-only（TASK-012∼014 不依赖 Push）；Push handler（TASK-015）设计为可选组件 |
| **群聊数据迁移期间停服** | 方向 5 | 设计为**低峰期在线迁移**——`conversation_id` 字段允许为空，旧消息逐批填充；迁移脚本可在服务运行时执行（只读扫描 + 批量 INSERT ON CONFLICT DO NOTHING） |

---

## 5. 质量保证

### 5.1 单元测试覆盖要求

| 层 | 最低覆盖率要求 | 重点关注 |
|----|---------------|---------|
| **domain** | 90%+ | 实体不变性、枚举验证、嵌套校验（评论层级） |
| **service** | 85%+ | 授权逻辑、错误转换、边界条件（空值/越界/权限不足） |
| **repository** | 70%+（集成测试部分需覆盖所有 CRUD） | SQL 查询正确性、事务行为、并发安全 |
| **handler (HTTP/gRPC)** | 75%+ | 参数绑定、状态码、错误格式 |
| **middleware** | 80%+ | 限流阈值、鉴权失败、幂等性键 |

**每个 service 测试必须包含的测试类型**：
1. **Happy path**：正常流程通过
2. **Authorization failure**：不同 actor（非 owner、无权限、被屏蔽）
3. **Not found**：ID 不存在
4. **Validation failure**：参数非法
5. **Boundary condition**：空值、超大值、嵌套越界

### 5.2 集成测试策略

| 层 | 工具 | 覆盖范围 | 执行时机 |
|----|------|---------|---------|
| Repository | testcontainers (Postgres + MinIO) | SQL 查询、事务回滚、并发冲突 | `make test-integration` |
| Service + Storage | testcontainers | 文件上传/S3/checksum 验证端到端 | `make test-integration` |
| 分布式限流 | testcontainers (Postgres + Redis) | 多 key 限流、Redis 故障降级 | `make test-integration` |
| 群聊 + 跨设备同步 | testcontainers | 多参与者并发操作 | `make test-integration` |

**集成测试注意事项**：
- 使用 `//go:build integration` 编译标签隔离
- `testpg` 辅助包提供 TestContainer 生命周期管理
- 每个测试包独立创建 testcontainer 实例（并行安全）
- 方向 4 的 Redis 测试需要新增 `testredis` 辅助包（参考 `testpg` 模式）

### 5.3 代码审查要点

| 审查维度 | 具体检查项 |
|---------|-----------|
| **工程约束** | 文件 ≤ 500 行？函数 ≤ 50 行？复杂度 ≤ 10？if 嵌套 ≤ 3 层？ |
| **安全审计** | SQL 注入（使用 GORM 参数化查询）？路径穿越（storage key 前缀校验）？SSRF（webhook URL 白名单）？ |
| **错误处理** | 所有 `err` 被检查？raw error 未泄露到 handler？`AppError` 链完整？ |
| **事务一致性** | outbox 事件与主操作在同一事务中？并发读取使用 `SELECT FOR UPDATE`？ |
| **测试质量** | 有授权失败测试？有 DB 唯一约束冲突测试？mock 覆盖所有接口？ |
| **API 兼容性** | 新增字段是否可选（向后兼容）？旧路由是否标记 deprecated？gRPC 字段编号是否保留？ |

### 5.4 性能测试需求

| 测试场景 | 工具 | 目标指标 | 时间 |
|---------|------|---------|------|
| 分布式限流吞吐 | `wrk` / `ghz` | 1000 req/s 时 P99 < 5ms（含 Redis RTT） | M1 后 |
| 群聊 500 人消息发送 | 自定义 Go 测试脚本 | P99 消息投递延迟 < 200ms | M3 前 |
| SSE 跨设备同步（1000 连接/uid） | 自定义测试 | P99 事件投递延迟 < 100ms | M2 后 |
| 缩略图 worker 吞吐 | `pprof` | 单节点 10 张/s（128×128 webp） | M2 后 |
| 并发限流降级 | chaos engineering | Redis 断连时 < 5% 请求受内存限流影响 | M1 后 |

---

## 6. 实施计划

### 阶段 1：基础设施搭建（第 1-2 周，10 个工作日）

```
Week 1      Week 2
├────────────┤
T001 ── T002 ── T003 ── T004 ── T005
T009 ─────────────
T018 ─────────────
T026 ── T027 ── T028
T037 (设计评审)
```

| 周 | 工作日 | 主要活动 | 交付物 |
|---|--------|---------|--------|
| 1 | 1-2 | 环境准备：配置 Redis 开发环境、安装 protoc 工具链、review 设计文档 | 开发环境就绪，设计文档 review 完成 |
| 1 | 3-5 | **TASK-001∼003**（checksum domain + migration + repository）**TASK-009**（user_devices migration）**TASK-018**（message_files migration）**TASK-026**（Limiter 接口重构） | 3 个 migration 文件 + domain 实体 + repository 实现 |
| 2 | 6-8 | **TASK-004∼005**（CompleteUpload checksum 验证 + presigned dedup）**TASK-010**（device domain + repository）**TASK-019**（message_file 快照 domain）**TASK-027**（Redis limiter） | P0 核心逻辑就绪 |
| 2 | 9-10 | **TASK-006**（handler 透传）**TASK-011**（设备注册 API 前半部分）**TASK-037**（conversation migration 设计评审）**TASK-028**（配置切换） | P0 handler + P1 API 原型 |

### 阶段 2：核心功能实现（第 3-6 周，20 个工作日）

```
Week 3      Week 4      Week 5      Week 6
├────────────┼────────────┼────────────┼────────────┤
T007 ── T008                            (方向 1 完成)
         T012 ── T013 ── T014           (方向 2 核心)
               T015 ── T016 ── T017     (方向 2 完成)
T020 ── T021 ── T022 ── T023 ── T024   (方向 3)
         T029 ── T030 ── T031 ── T032  (方向 4 完成)
               T034 ── T035 ── T036
                           T038 ── T039 (方向 5 启动)
```

| 周 | 主要活动 | 里程碑 |
|----|---------|--------|
| 3 | **TASK-007∼008**（checksum 单元+集成测试）**TASK-029∼030**（中间件注入 + per-endpoint 配置）**TASK-020**（消息发送时生成附件快照） | **M1: P0 核心交付** ✅ — 方向 1 完成 + 限流中间件可用 |
| 4 | **TASK-012∼014**（跨设备已读同步 + session.revoked）**TASK-031∼032**（举报+Likes 滥用检测）**TASK-021**（附件优雅降级）**TASK-038**（Conversation domain） | P1 核心路径走通 |
| 5 | **TASK-015∼016**（Push handler + token 过期）**TASK-022∼023**（文件删除保护 + 缩略图 worker）**TASK-033**（spam 评分）**TASK-039**（Conversation repository） | P1 功能接近完成 |
| 6 | **TASK-017**（设备同步测试）**TASK-024∼025**（缩略图 GC + 测试）**TASK-034∼036**（Redis 健康检查 + 限流集成测试 + 滥用检测测试）**TASK-040**（1:1 数据迁移工具） | **M2: P1 Alpha** ✅ — 方向 2+3 完成 |

### 阶段 3：群组消息与集成测试（第 7-10 周，20 个工作日）

```
Week 7      Week 8      Week 9      Week 10
├────────────┼────────────┼────────────┼────────────┤
T041 ── T042 ── T043 ── T044 ── T045 ── T046 ── T047
```

| 周 | 主要活动 | 里程碑 |
|----|---------|--------|
| 7 | **TASK-041∼042**（Message 模型添加 conversation_id + 会话 API 实现） | Conversation 基础 API 可用 |
| 8 | **TASK-043∼044**（群聊消息写扩散 + 会话列表含摘要和未读数） | 群聊核心功能可用 |
| 9 | **TASK-045∼046**（消息撤回 + 旧 API 兼容层） | 群聊完整功能 |
| 10 | **TASK-047**（大群性能测试 + 全部集成测试） | **M3: P2 Internal Beta** ✅ — 方向 5 完成 |

### 阶段 4：发布准备（第 11-12 周，10 个工作日）

```
Week 11     Week 12
├────────────┤
性能测试     压力测试修复
文档编写     CHANGELOG
安全审计      发布部署脚本
```

| 周 | 主要活动 | 交付物 |
|----|---------|--------|
| 11 | 全量性能测试（wrk/ghz）、Redis 故障演练、pprof CPU/memory 分析 | 性能测试报告 |
| 11-12 | 文档更新：`README.md` 新增 API 表 + 示例；`CHANGELOG.md` 更新；配置示例更新 | 文档 PR |
| 12 | 生产部署脚本（Docker Compose / K8s manifest 更新）、安全审查（OWASP 检查项） | 部署指南 |
| 12 | Staging 环境 7 天灰度运行 + 监控告警配置 | **M4: 发布就绪** ✅ |

### 甘特图总览

```
Week       1   2   3   4   5   6   7   8   9  10  11  12
           ├───┼───┼───┼───┼───┼───┼───┼───┼───┼───┼───┼───┤
方向 1      ████░░░░░░                                   ✅
方向 4          █████░░░░░░░                             ✅
方向 2              █████████░░░░                        ✅
方向 3              █████████████                        ✅
方向 5                              ████████████████      ✅
性能测试                                              ████
文档/部署                                            ████

图例：████ = 开发阶段   ░░░░ = 缓冲/等待   ✅ = 里程碑完成
```

---

## 附录 A：各方向的分工建议

| 开发者 | 负责方向 | 理由 |
|--------|---------|------|
| **Senior A** | 方向 4（分布式限流）+ 方向 5（群组消息） | 架构变更最大，需要资深设计能力 |
| **Engineer B** | 方向 1（文件完整性）+ 方向 3（附件生命周期） | 与文件系统耦合，逻辑相对独立 |
| **Engineer C** | 方向 2（多设备同步） | SSE 已就绪，主要是模式复用，适合中级工程师独立负责 |
| **QA** | 所有方向集成测试 + 性能测试 | 确保跨方向的质量一致性 |

## 附录 B：配置文件变更预览（方向 4）

```yaml
rate_limit:
  enabled: true
  type: memory                # "memory" | "redis"
  default_rps: 20
  default_burst: 40
  idle_ttl: 10m
  cleanup_every: 1m
  endpoints:
    "POST /v1/comments/*/report": { rps: 2, burst: 5 }
    "POST /v1/messages": { rps: 5, burst: 10 }
    "POST /v1/comments/*/like": { rps: 10, burst: 20 }
redis:
  addr: "127.0.0.1:6379"
  password: ""
  db: 0
  pool_size: 20
  min_idle_conns: 5
  dial_timeout: 5s
  read_timeout: 3s
  write_timeout: 3s

abuse_detection:
  report_rate_limit:
    enabled: true
    max_per_minute: 10
  like_debounce_ms: 3000
  spam_scorer:
    type: keyword            # "keyword" | "ml" (future)
    threshold: 0.8
    keywords_file: "configs/spam_keywords.txt"
    auto_hide: false          # true = automatically hide flagged content
```

## 附录 C：风险应对清单

```markdown
## R1: Redis 不可用
- [ ] 确保 memory fallback 实现已测试
- [ ] 配置 `rate_limit.type: memory` 为默认值
- [ ] 告警：`redis_available 0` 时 alert

## R2: 大文件 checksum 验证
- [ ] 客户端 SDK 文档标注 multipart upload 需额外传 SHA-256
- [ ] 异步验证方案（Lambda/Worker）作为备用路径

## R3: 群聊 schema 迁移
- [ ] 迁移脚本设计为幂等（ON CONFLICT DO NOTHING）
- [ ] 准备回滚脚本（000027_conversations.down.sql）
- [ ] staging 环境先执行迁移，验证 1:1 数据无损

## R4: Push Notification
- [ ] 初期 SSE-only 发布（不做 Push block）
- [ ] PushProvider 接口定义清晰，mock 覆盖所有测试
```
