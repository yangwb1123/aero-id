现在我已经深入了解了代码库。以下是我的架构分析：

---

# 架构分析：aero-id

## 1. 架构评估

### 当前架构优势

**aero-id 代码库已从其最初的 3 模块 MVP 演变为一个高度精炼的服务。** 它在现有架构契约（AGENTS.md）内优雅地实现了事务性发件箱模式、通知系统、SSE 推送、速率限制、幂等性、可观测性（指标 + 追踪）、存储 GC、搜索、管理 RBAC 以及用户屏蔽/偏好。

**分层纯度** 是主要的架构胜利。`handler → service → repository/storage/sso` 的严格单向依赖关系在所有新增功能中都得到了保持。没有业务逻辑泄漏到传输层——服务层是所有编排和授权的唯一真实来源。这意味着 HTTP 和 gRPC 的行为必然相同；不需要人肉验证。

**发件箱 + 中继模式** 是异步扩展的基础。事务事件写入与领域行共存，确保下游效果（通知、webhook）可靠，而不是“尽力而为”。`NotificationDispatcher` 的职责分离（事件 → 通知行 → SSE 推送），加上可选的块检查、偏好检查、速率限制和折叠，是一个设计良好的链，由可选的 `DispatcherOption` 函数选项配置。

**固有的可观测性** 是一个优点：自包含的 Prometheus 指标（无 `client_golang`）、SSO 断路器遥测、发件箱滞后/排水持续时间、以及用于 OTel 的 `tracing.Init` 无操作降级，意味着监控是内建的，而不是后来附加的。

**"默认关闭"模式** 用于速率限制、GC、追踪和 webhook——防止开发/CI 出现意外，同时允许生产操作员选择加入。

### 当前架构局限性

尽管其成熟度很高，但代码库暴露了几个架构弱点：

| 问题区域 | 症状 | 影响 |
|---|---|---|
| **无乐观锁定** | `UpdateContent` 执行直接 `UPDATE ... SET content = ?`，无版本检查（`repository/message.go`） | 多设备用户不知不觉地相互覆盖编辑。可以通过 `SELECT ... FOR UPDATE` 在读取后进行写入，但这只防止竞争，而不通知用户 |
| **发件箱中继中的同步 webhook** | `outbox.NewChain(dispatcher, webhookHandler)` 在同一个 `drain()` 周期内同步调用两者 | webhook 延迟较高的下游操作会阻塞通知交付；一个出错的 webhook 会延迟重试相同批次中的其他事件 |
| **进程内速率限制器** | `ratelimit.Limiter` 使用内存令牌桶 | 每个实例的速率限制不同；水平扩展（N 个副本）允许每个用户的吞吐量达到 N 倍。Redis 集中式限制器被标记为“有意超出范围”，但群聊加剧了这个问题 |
| **SSE Hub 内存驻留，无持久化** | `hub.Hub` 使用每个 UID 的缓冲通道 | 进程重启会断开所有客户端连接；失败事件回放依赖于客户端 `Last-Event-ID`，但离线期间生成的通知会丢失，除非客户端主动轮询 |
| **存储抽象缺少 `Lister` 一致性** | `Lister` 接口仅在 `storage/gc` 包中定义，不在 `storage/storage.go` 中 | 没有明确的存储契约要求全局性浏览能力；对于其他需求（数据导出、迁移），这需要重复发现 |
| **消息模式是隐式的** | 1:1 消息使用 `(from_uid, to_uid)` 对，没有 `conversations` 表 | 群聊需要对 `conversation_id` 进行大规模模式重构；没有统一的 `conversation` 实体，就无法使用单个外键替换 UNION 查询进行寻址 |
| **MIME 类型仅来自客户端信任** | `mime_type` 列按原样存储客户端值 | 允许用户声明 `application/octet-stream` 的内容为 `image/png`；没有服务器端的魔数验证 |

### 架构债务

1.  **`notification/service.go` 与 `service/notification.go` 重复**：有一个 `internal/notification/` 包（旧版单独的 notification handler/service/repository 包），也有 `internal/service/notification.go`（集中式服务层）。从 `app.go` 的接线来看，旧的独立包似乎已被取代，但包树中没有文件被删除。这违反了 AGENTS.md 的单职责原则。

2.  **存储 GC 竞争窗口**：发件箱模式通过事务一致性解决了许多问题，但 `sweeper.go` 的 `countLive` 回退到 `CountAnyByStorageKey`，该函数统计的是 *任意* 行（活跃 + 软删除），而不是活跃行。`RefCounter` 路径（原子引用计数）更优越，但尚未设为默认值。

3.  **通知折叠会留下审计重复**：`NotificationDispatcher.Handle()` 的 `collapseWin` 路径创建了冗余的 `domain.Notification` 行（作为审计跟踪记录），但递增了现有行上的 `collapsed_count`。用户的通知列表仍会包含两个行——未折叠的行最先出现（按 `created_at` 排序），而折叠的重复项出现在下方。这需要在前端或查询层进行过滤。

4.  **幂等性存储清理是每个实例的**：`idempotency.GormStore.RunCleaner` 在进程内运行；在多个副本中，过期行的清理是重复的。无害但效率低下。

---

## 2. 扩展方向

### 方向 1：消息乐观锁定（高价值，P1）

**为什么需要：** 当前，`service.MessageService.Edit()` 执行 `repo.UpdateContent(ctx, id, content, now)`，该操作无条件地写入 `UPDATE ... SET content = ?`。在两个设备上打开相同对话的用户，如果都在没有刷新 stale 编辑框的情况下保存，会静默地覆盖彼此的修改。*没有数据丢失*（旧内容不会在事务上丢失），但*用户信任*会受损——被覆盖的文本在没有警告的情况下消失。

**核心挑战：**
- 向后兼容：旧客户端在请求中不发送 `expected_version`
- 传输透明：HTTP 和 gRPC 必须表现相同（服务层是单一事实来源）
- 版本存储开销：不需要在整个表中增加 `version` 列——一条消息的生命周期中，编辑很少超过几次

**提议的架构变更：**

```
messages 表（新列：version BIGINT NOT NULL DEFAULT 1）
  ↓
service.MessageService.Edit():
  if input.ExpectedVersion != nil && m.Version != *input.ExpectedVersion:
    return Conflict("message.edited", "content was modified by another device")
  repo.UpdateContent(ctx, id, content, version, now)  // WHERE id=? AND version=?
  // 影响的行数检测 stale 更新（需要 GORM 的 RowsAffected）
  // 如果行数为 0 → 重新加载并重试或报告冲突
```

**对现有系统的影响：**
- 模式迁移：`ALTER TABLE messages ADD COLUMN version BIGINT NOT NULL DEFAULT 1`
- `repository.MessageRepository` 新接口：`UpdateContentWithVersion(ctx, id, content, version, at)` 返回 `(rowUpdated bool, err)`
- 服务层：`EditInput` 获得可选的 `ExpectedVersion *int64`
- 迁移路径（如用户输入的分析所述）：
  - 第 1 阶段：发布 `messages.version` 迁移；新的 API 接受可选的 `expected_version`
  - 第 2 阶段（2 周过渡）：记录对无版本 POST 的警告；记录指标
  - 第 3 阶段：强制使用 `expected_version`
- 对评论编辑相同。对于 `comment.Reply` 和 `comment.Edit` 也是如此——但根据频率，评论版本控制优先级较低。

### 方向 2：群聊（高价值，P2 — 重大架构变革）

**为什么需要：** 当前的 1:1 消息模型使用 `messages.from_uid` + `messages.to_uid` 直接表示对话。群聊需要：
- `conversations` 表（`id`、`type`（`group`/`1:1`）、`name`、`avatar`、`created_at`）
- `conversation_members` 表（`conversation_id`、`member_uid`、`joined_at`、`left_at`）
- `messages.conversation_id` 用于所有消息的统一寻址
- 推送通知目标组成员（而不是 `to_uid`）

**核心挑战：**
- **现有数据的迁移**：数十万条 1:1 消息没有 `conversation_id`
- **N+1 消息分发**：在 1:1 中，一次写入 = 一个收件人。在群聊中，一次写入需要 N 个通知、N 个未读计数器更新、以及后端的 N 个 SSE 推送。发件箱模式将 N 个效果序列化为发件箱中的一条事件，但 *下游*（`NotificationDispatcher`）需要为每个成员生成 N 条通知行
- **离开/被移除的成员**：不应接收历史消息的推送；不应计入未读计数
- **读回执**：在 1:1 中很简单（`to_uid` 就是阅读者）。在群聊中，每条消息有 N 个可能的阅读者——需要一个单独的 `message_reads` 表

**预期的架构变更：**

```
新表：
  conversations(id, type, name, avatar, created_at)
  conversation_members(conversation_id, member_uid, role, joined_at)
  conversation_reads(conversation_id, member_uid, last_read_message_id)

现有 messages：
  ADD COLUMN conversation_id UUID NULLABLE DEFAULT NULL
  向 messages 添加 conversation_id 上的新索引
  
服务层：
  MessageService 获得：
    - CreateGroupConversation(name, memberUIDs) → conversation
    - SendToConversation(conversationID, content)  // 向所有成员分发
    - ListConversations() → 用户的对话列表
    - 旧版 ListConversation(withUID) 被重写为 1:1 对话
  
发件箱事件：
    "conversation.message.sent"（携带 conversation_id + 成员列表）
    
NotificationDispatcher 增长：
    for each member in conversation (excluding sender):
      create notification row
      increment unread counter
      push SSE
```

**与现有 1:1 消息的融合：**

如用户输入的分析中所提议的，为每个唯一的 `(from_uid, to_uid)` 对惰性创建 `type='1:1'` 的 `conversations` 行，允许所有消息在 UI 层通过 `conversation_id` 统一寻址。这需要一个后台迁移（对数千万条 1:1 消息运行），但长期收益（没有 UNION 查询，统一的 API 表面）是显著的。

**风险：** 粒度消息读回执显著增加了模式复杂性。一个务实的 P2 子步骤是：构建群聊基础设施但跳过每条消息的读回执；使用“成员在特定时间戳前已读整个群聊”作为最小可行方案。

### 方向 3：异步文件上传与 MIME 魔数验证（P1 — 可独立交付）

**为什么需要：** 当前，`POST /api/v1/files` 是同步的：服务层在返回文件元数据之前读取整个 `io.Reader`，计算 sha256 并写入对象存储。对大文件，这会将请求线程阻塞在 I/O 上。此外，`mime_type` 完全来自客户端——服务器不验证内容的实际类型（除非 `AllowedMimeTypes` 配置列表被启用，但这只过滤声明的类型，不进行内容检查）。

**分阶段交付：**

**阶段 1（P0，小改动）：MIME 魔数验证**

这是用户分析中提到的“独立小步”。不需要异步管道。

```
FileStorage.Put() 或 service.UploadFile()：
  // 读取前 512 字节
  buf := make([]byte, 512)
  _, err := io.ReadFull(reader, buf)  // 或 Peek(512)
  detected := http.DetectContentType(buf)  // Go 标准库，魔数感知
  if detected != declared_mime_type {
    // 宽松模式：存储检测到的，或拒绝
    mime_type = detected  // 信任服务器端检测
  }
  使用 tee reader 继续上传以计算 sha256
```

**阶段 2（P2）：完全异步处理管道**

当需要视频转码、病毒扫描或 OCR 时，完整的异步管道才需要。

```
当前：POST → service.Create() → storage.Put() → DB insert → 201
未来：POST → service.Create()（仅保留元数据，状态=pending）→ 202 
      后台处理器：发件箱事件 → 下载 → 验证 → 处理 → 写入存储 → 标记完成
```

第一阶段没有架构债务——它只是在现有的同步路径中增加了 512 字节的读取。第二阶段意味着核心 `FileService.Create()` 路径的范式转变。

**挑战：** 同步依赖项 `io.Reader`（来自 multipart 请求）不是为持久化而设计的；要使其异步，你需要要么：
- 将上传的 blob 缓冲到临时存储（LocalStorage 很好；S3 是附加请求）
- 或者通过响应 `PresignUpload` URL 并在回调查询时处理之前发布事件，完全解耦上传

### 方向 4：数据可移植性（用户数据导出/删除）（P1）

**为什么需要：** 数据隐私法规（GDPR、CCPA）要求用户可移植性。没有导出/删除 API，用户数据无法逃脱 SSO 生命周期——即使 SSO 账户被删除，aero-id 中的消息、文件和评论也会永远保留。

**预期的架构变更：**

```
Handler：
  GET /api/v1/exports → 触发异步作业，返回工作 ID
  GET /api/v1/exports/:id → 轮询状态，下载结果

Service：
  ExportService：
    RequestExport(ctx, actor) → (jobID, error)
    GetExportStatus(ctx, actor, jobID) → (status, downloadURL)
    DeleteAllData(ctx, actor) → 触发用户数据擦除

发件箱事件：
  "data.export.requested" → 由后台 worker 拾取
  "data.deletion.requested" → 由打扫卫生员拾取

导出格式：
  JSON 文件（消息、文件元数据、评论）
  对实际文件的可下载 blob（仅当文件仍存在时）
```

**核心挑战：**

1.  **审核日志脱敏**（如用户输入的分析中所指出的）：
    - 确定性哈希（`SHA256(uid + salt)`）允许通过哈希跟踪关联单个账户的日志——这与脱敏的目的相矛盾。
    - 更好的方案：**每个账户加盐的密钥派生**（为每个 actor_uid 生成随机盐，存储映射，或者使用 HMAC 密钥 + uid）。但即使这样，如果攻击者有密钥，也可以链接日志。
    - **最强方案**：在擦除后完全删除审核行（`actor_uid = ''` 或删除）。这会破坏审计链，但如果合规允许的话，这是最具隐私性的。

2.  **级联删除**：删除账户的消息也需要清理通知、发件箱事件和文件存储 blob。发件箱模式在这里提供了帮助——`"data.deletion.requested"` 事件可以由专用 handler 逐步处理。

3.  **文件删除的异步性**：文件 blob 可能很大，或者位于带有延迟的 S3 上。删除应该在后台进行，有进度反馈。

### 方向 5：SSE 连接管理改进（P1，群聊的先决条件）

**为什么需要：** 当前的 `hub.Hub` 实现每个 UID 支持多个订阅者（通过 `sync.Map` 键控的 `uidSubs`）。但它缺少连接上限，并且没有处理断开/重连节奏的机制。在群聊（方向 2）中，每个组成员可以有多个标签页，每个标签页持有一个 SSE 连接。如果没有保护措施，恶意用户可以通过打开 1000 个标签页来放大 `hub` 的内存压力。

**分阶段交付：**

**阶段 1：每个 UID 的连接上限（P1，群聊之前）**

```
hub.Hub：
  maxSubsPerUID int  // 配置，默认可能为 10
  
  Subscribe(uid):
    us = loadOrCreate(uidSubs)
    if len(us.subs) >= maxSubsPerUID:
      // 拒绝或驱逐最旧的连接
      从 us.subs 中移除最旧的条目
      关闭最旧的通道
```

**阶段 2：带有回退的自动重连（P1）**

```
SSE handler 发送 Last-Event-ID（通知 created_at unix 毫秒）
客户端断开连接后：
  1. 立即重试（≤ 3 次，间隔递增）
  2. 定期轮询备份（每隔 30 秒 GET /api/v1/notifications）
  
服务器：
  在 /events SSE 连接上处理 Last-Event-ID
  回放自该时间戳以来所有未发送通知的 notification rows
```

**对现有系统的影响：**
- Hub 获得可选的 `maxSubsPerUID`，通过 `hub.New(option)` 配置
- SSE HTTP handler 需要从 `Last-Event-ID` 头读取并调用 `notifRepo.ListSince()`
- 通知仓库获得 `ListSince(ctx, uid, since timestamp)` 用于回放

---

## 3. 接口设计建议

### 关键模块的接口设计原则

当前代码库在接口设计方面表现出色。以下是一些强化这些原则的建议：

1.  **保持函数选项（`With*`）模式一致**：`messageService`、`NotificationDispatcher` 和 `Sweeper` 都使用此模式。在添加新功能时严格遵守它——它允许零成本的向后兼容添加。不要在已经函数选项化的构造函数上引入构建器模式。

2.  **不要向外暴露领域事件作为参数**：当前，`messageRepo.Create(ctx, msg, evt)` 接受一个 `*outbox.Event`。这对于现有的发件箱模式很有效。但对于群聊，事件将需要额外的上下文（成员列表）。考虑将发件箱事件创建提取到专用的 `EventFactory` 中：

    ```
    type EventFactory interface {
        MessageSent(msg *domain.Message) *outbox.Event
        ConversationMessageSent(msg *domain.Message, memberUIDs []string) *outbox.Event
    }
    ```

    这防止了领域逻辑泄漏到服务/仓库层。

3.  **优先使用并发安全的构建块类型**：`hub.Hub` 的每个 UID 锁定（`uidSubs.mu`）是一个优秀的模式——它比全局 `sync.Mutex` 更好，因为它防止不同用户之间的锁竞争，同时对同一用户的操作进行序列化。`notifRateLimiter` 也使用了每个键的槽清理。在需要内部并发的任何新组件中保持这种每个 UID 的锁定模式。

4.  **将 SSE pusher 与通知填充解耦**：`NotificationPusher` 接口当前定义了 `Publish(uid, eventType, data string, eventID int64)`。随着群聊的发展，pusher 可能需要区分“对话消息”和“系统通知”。考虑一个额外的 `Event` 层：

    ```
    type PushEvent struct {
        UID       string
        Type      string // "notification" | "conversation.message" | "typing"
        Payload   json.RawMessage
        ID        int64  // SSE 事件 ID
    }
    ```

    这允许 pusher 拥有一条单一的 `Publish(PushEvent)` 方法，而不是方法签名爆炸。

### 是否需要新的抽象层？

| 抽象 | 需要？ | 理由 |
|---|---|---|
| **ConversationService** | 是，方向 2 | 群聊引入了一个新的领域概念（Conversation），它横跨消息和成员身份——它不应该被塞进 MessageService |
| **ExportService** | 是，方向 4 | 数据可移植性关注点完全独立于消息/文件/评论 CRUD |
| **AsyncJobRunner** | 暂不 | 方向 3 阶段 2（完全异步）和方向 4（导出）都需要后台作业。目前 keep it simple：发件箱 + handler 模式已经存在并且运行良好。等到出现 3 个独立的异步 worker 后，再加一个专用作业调度器 |
| **StorageLister** | 是，紧跟 | 当前在 `storage/gc/sweeper.go` 中定义为接口的 `Lister` 应该被提升到 `storage/storage.go` 中的 `FileStorage` 接口，以便数据导出（方向 4）和迁移工具发现它 |

### 向后兼容性策略

方向 1（乐观锁定）有最清晰的迁移路径。以下是通用方法：

1.  **无老客户端中断**：所有新字段都是 `optional`/指针/`nilable`。服务层检查 `!= nil`。缺失 = 旧行为。
2.  **日志 + 指标**：`service.MessageService.Edit()` 在接收到无版本的请求时记录 `zap.Warn`，并将其计入指标计数器。操作面板显示旧客户端拖尾情况。
3.  **可部署性**：模式迁移是向前兼容的（`ADD COLUMN ... DEFAULT 1`）。回滚不需要降级模式，只需停止强制版本检查。
4.  **门控**：强制检查由配置标志控制：`message.enforce_version: true`。运营团队可以在新客户端达到 99% 的使用率后切换。

---

## 4. 技术选型

### 当前技术栈是否足够？

代码库通过优先使用标准库 + 精心挑选的 Go 库（GORM、gin、zap、viper、grpc、otel）来避免框架锁定，表现出良好的自制力。对于提议的扩展方向，新依赖项很少：

| 方向 | 所需的新依赖项 | 评估 |
|---|---|---|
| 乐观锁定 | 无 | `database/sql.RowsAffected` 就足够了 |
| 群聊 | 无 | 纯模式 + 业务逻辑工作。无需 ORM 更改 |
| 异步 MIME/处理 | 潜在的 `filetype/go`（如果你想要比 Go 的 `http.DetectContentType` 更好的 MIME 检测） | Go 的 `http.DetectContentType` 是纯 stdlib 且可靠。对于大多数情况已经足够。`filetype/go` 提供了更广泛的覆盖，但增加了 0.2MB 的无压缩二进制文件。不需要；保持 stdlib |
| 数据导出 | 无 | JSON 序列化 + 流式 HTTP 响应。可能使用 `archive/zip`（stdlib）用于组合导出 |
| SSE 管理 | 无 | 纯通道和 http.Flusher |

**没有新的依赖项被证明是合理的。** 目前的策略（偏好 stdlib，谨慎地评估第三方库）是正确的。

### 架构债务技术

1.  **`internal/notification/` 与 `internal/service/notification.go` 的重复**：后者已经生效。前者应该在不破坏 git 历史的情况下被弃用和移除。这是技术债务——不是功能层面的债务，但一个阅读“internal/”结构的开发者会找到两个通知入口点。

2.  **Go 1.26 依赖项**：在像 google/uuid 的库版本中，针对 Go 1.26 编译器可能没有漂移，但值得关注——`github.com/google/uuid` 是否支持 UUIDv7？当前代码使用 `uuid.New()` 并通过 `pkg/idgen` 包装它。如果 uuid 库增加了原生 v7 支持，迁移过来可以减少代码量。

### 自建与外包决策

对于所提供的所有方向，**自建**都是正确答案：

- aero-id 是一个微服务，构建在特定于领域的抽象之上（文件存储、SSO 集成、发件箱模式）。没有现成的解决方案能在不引入耦合的情况下适合 3 模块模型。
- 群聊的直接替代品是 Firebase / Stream Chat，但 aero-id 的业务需求（SSO，本地消息附件屏蔽，审核集成）使得自建不可避免。
- 对于乐观锁定：这是几十行查询逻辑。引入一个“版本控制系统”是荒谬的。
- 对于数据导出：严格的自定义格式需要。

唯一的*如果*是用于 MIME 检测，但如前所述，Go 的 stdlib 就已经足够了。

---

## 5. 实施路线图

### 优先级排序

| 优先级 | 方向 | 理由 |
|---|---|---|
| **P0** | 方向 1（乐观锁定阶段 1-2） | 多设备数据丢失是一个产品信任问题；需要在群聊之前完成（群聊增加了写入者数量） |
| **P0** | 方向 5 阶段 1（每个 UID 的 SSE 连接上限） | 方向 2（群聊）的先决条件 |
| **P1** | 方向 3 阶段 1（MIME 魔数验证） | 几行代码带来很高的安全收益；可以独立落地 |
| **P1** | 方向 4（数据可移植性） | 法规要求；模式变更很小 |
| **P1** | 通知折叠修复（在查询时过滤重复项） | 现有技术债务 |
| **P1** | 弃用 `internal/notification/` | 消除架构债务 |
| **P2** | 方向 2（群聊） | 最大的架构变更；依赖 SSE 上限 + 乐观锁定；需要专门的前端工作 |
| **P2** | 方向 3 阶段 2（完全异步文件） | 在视频/图像处理成为产品需求之前，分析都声称没必要 YAGNI |
| **P2** | 发件箱 webhook 异步 + 批量 | 当延迟或可靠性成为问题时，改进 webhook 交付 |

### 阶段划分

**阶段 0（当前 sprint — 2 周）：安全基础**

| 工作项 | 规模 | 依赖项 |
|---|---|---|
| 乐观锁定阶段 1（模式 + 可选参数） | 小 | — |
| SSE 每个 UID 连接上限（配置：10） | 很小 | — |
| MIME 魔数验证（server.DetectContentType buf[512]） | 很小 | — |
| 通知折叠：过滤查询中的重复项 | 小 | — |

**阶段 1（下一 sprint — 3 周）：可移植性 + 清理**

| 工作项 | 规模 | 依赖项 |
|---|---|---|
| 数据导出（JSON + 文件 blob URL） | 中 | 阶段 0 |
| 数据删除（级联软删除） | 中 | 阶段 0 |
| 弃用旧版 `internal/notification/` | 小 | — |
| 为 SSO 升级准备 `user_cache` stale_ttl 保护 | 小 | — |
| 乐观锁定阶段 2（强制 + 指标控制） | 小 | 阶段 0 |

**阶段 2（未来季度 — 6 周）：群聊**

| 工作项 | 规模 | 依赖项 |
|---|---|---|
| 对话表 + 迁移 | 中 | 阶段 1 |
| 对话成员表 + 迁移 | 中 | 阶段 1 |
| `conversation_messages` 桥接表（ID + `conversation_id`）+ `messages.conversation_id` | 大 | 阶段 1 |
| 后台 1:1 对话惰性填充迁移 | 中 | — |
| 群聊服务（建群、邀请、离开） | 大 | — |
| 按会话通知分发（N 个出站通知） | 中 | 阶段 1（SSE 上限） |
| 对话读回执（粗略时间戳层级） | 中 | — |

### 风险和缓解

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| 群聊会增加 10 倍的通知量 | 高 | 中 | 速率限制（已存在）；按频率分批通知（而不是每消息单条通知）；在群聊 UI 中使用“沉默通知”偏好 |
| 乐观锁定过渡期间，旧客户端静默地覆盖数据 | 中 | 高 | 指标面板监控无版本写入；过渡期 2 周后强制 |
| 数据导出产生大量 S3 成本的下载风暴 | 低 | 中 | 每个 actor 每个时间窗口限速一次导出；导出链接在几分钟后过期；导出大小上限 |
| MIME 验证拒绝合法的上传 | 中 | 低 | 仅记录不阻止的“宽松”模式；运营面板监控拒绝率 |
| 发件箱 webhook 成为 SPOF | 低 | 中 | 目前 webhook 与通知分发共享重试状态。分离链（通知优先，webhook 异步）消除阻塞 |

---

## 总结

aero-id 是同类服务中架构精良的典范。事务性发件箱、干净的接口边界、机械同情（适当时使用批量计数器，适当使用内存缓存）以及明确定义的扩展点（函数选项、可配置的可选功能）为未来增长奠定了扎实的基础。

五个分析方向中，**乐观锁定** 和 **SSE 连接管理** 是群聊所依赖的 P0 先决条件——它们可以直接修复现有的产品问题（多设备数据丢失，无节制的连接放大）。**MIME 魔数验证** 是一个高影响力的低付出（几行代码），可以安全地立即交付。**数据可移植性** 是一项法规要求，具有适度的架构影响。**群聊** 是最大的架构变革，需要细致的模式迁移——但它带来的收益（统一的 `conversation_id` 寻址、多用户消息传递）证明了其 P2 排期的合理性。

架构中最薄弱的点——缺少版本控制、发件箱事件没有 `EventFactory` 抽象、`Lister` 接口在储存 GC 中存在重复——都是简单提取，而不是根本性的范式问题。总体而言，这份代码库是一流的。
